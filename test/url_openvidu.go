// BEGIN OPENVIDU BLOCK

//go:build integration

package test

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/protocol/livekit"
)

// The URL pulls that only the OpenVidu fork has (RTSP) or fixes (an Opus-only
// MPEG-TS). Both sources are served from this container.

const (
	openviduRTSPPort = 8554
	// Where mediamtx takes the camera's stream. Not 1935, the ingress's own.
	openviduRTMPFeedPort = 1936
	openviduSRTPort      = 8891

	openviduSourceTimeout = 30 * time.Second
	openviduPullDuration  = 15 * time.Second
	// The input stats reach the state once a minute.
	openviduStatsDuration = 65 * time.Second
)

func (r *Runner) testOpenViduURL(t *testing.T) {
	if !r.runURL() {
		return
	}

	t.Run("OpenViduURL", func(t *testing.T) {
		// One decodebin per stream: each one must feed the stats of its own track.
		r.runOpenViduPull(t, "RTSP/AudioAndVideo", serveRTSPCamera, openviduStatsDuration,
			func(t *testing.T, ended *livekit.IngressState) {
				require.Equal(t, "video/H264", ended.Video.GetMimeType())
				require.Equal(t, "audio/aac", ended.Audio.GetMimeType())
				require.NotZero(t, ended.Video.GetAverageBitrate(), "video input bitrate")
				require.NotZero(t, ended.Audio.GetAverageBitrate(), "audio input bitrate")
			})

		// Without the opusparse rank this pull ends in "Internal data stream error".
		r.runOpenViduPull(t, "SRT/OpusOnlyMPEGTS", serveOpusOnlySRT, openviduPullDuration,
			func(t *testing.T, ended *livekit.IngressState) {
				require.Equal(t, "audio/opus", ended.Audio.GetMimeType())
				require.Nil(t, ended.Video, "no video stream")
			})
	})
}

// runOpenViduPull pulls the URL that serve returns, deletes the ingress once it
// has published for runFor and hands the state it ended with to check.
func (r *Runner) runOpenViduPull(
	t *testing.T,
	name string,
	serve func(t *testing.T) string,
	runFor time.Duration,
	check func(t *testing.T, ended *livekit.IngressState),
) {
	r.awaitIdle(t)

	t.Run(name, func(t *testing.T) {
		info := r.ingressInfo(livekit.IngressInput_URL_INPUT, caseIdent(name))
		info.Url = serve(t)

		r.startIngress(t, info)
		r.checkUpdate(t, info.IngressId, livekit.IngressState_ENDPOINT_PUBLISHING)

		time.Sleep(runFor)

		ended := r.stopIngress(t, info.IngressId)
		require.Equal(t, livekit.IngressState_ENDPOINT_INACTIVE, ended.Status, "ended with: %s", ended.Error)
		check(t, ended)

		r.awaitIdle(t)
	})
}

// serveRTSPCamera serves H.264 and AAC over RTSP, as an IP camera does. The
// image has no RTSP server element, so mediamtx serves what gst-launch feeds it.
func serveRTSPCamera(t *testing.T) string {
	t.Helper()

	conf := filepath.Join(t.TempDir(), "mediamtx.yml")
	require.NoError(t, os.WriteFile(conf, []byte(fmt.Sprintf(
		"rtspAddress: :%d\nrtmpAddress: :%d\nhls: no\nwebrtc: no\nsrt: no\nmoq: no\npaths:\n  all_others:\n",
		openviduRTSPPort, openviduRTMPFeedPort)), 0o600))
	publish(t, "mediamtx", conf)

	feed := fmt.Sprintf("127.0.0.1:%d", openviduRTMPFeedPort)
	require.Eventually(t, func() bool {
		conn, err := net.DialTimeout("tcp", feed, time.Second)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	}, openviduSourceTimeout, statePoll, "mediamtx did not start")

	publish(t, "gst-launch-1.0", strings.Fields(fmt.Sprintf(
		"flvmux streamable=true name=mux ! rtmp2sink location=rtmp://%s/camera "+
			"audiotestsrc is-live=true freq=200 ! audioconvert ! avenc_aac ! aacparse ! mux. "+
			"videotestsrc pattern=ball is-live=true ! video/x-raw,width=1280,height=720,framerate=30/1 "+
			"! x264enc speed-preset=3 tune=zerolatency key-int-max=60 ! mux.",
		feed))...)

	url := fmt.Sprintf("rtsp://127.0.0.1:%d/camera", openviduRTSPPort)
	require.Eventually(t, func() bool { return rtspDescribes(url) },
		openviduSourceTimeout, statePoll, "the RTSP stream did not come up")

	return url
}

// rtspDescribes reports whether the RTSP server has a stream at url.
func rtspDescribes(url string) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", openviduRTSPPort), time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(time.Second))
	if _, err = fmt.Fprintf(conn, "DESCRIBE %s RTSP/1.0\r\nCSeq: 1\r\nAccept: application/sdp\r\n\r\n", url); err != nil {
		return false
	}
	status, err := bufio.NewReader(conn).ReadString('\n')

	return err == nil && strings.HasPrefix(status, "RTSP/1.0 200")
}

// serveOpusOnlySRT serves an MPEG-TS whose only stream is Opus from an SRT
// listener, which sends nothing until the ingress connects. It has to be
// ffmpeg's MPEG-TS: the pull never failed on the one GStreamer's mpegtsmux writes.
func serveOpusOnlySRT(t *testing.T) string {
	t.Helper()

	publish(t, "ffmpeg", strings.Fields(fmt.Sprintf(
		"-hide_banner -loglevel error -re -f lavfi -i sine=frequency=200 -c:a libopus "+
			"-f mpegts srt://:%d?mode=listener",
		openviduSRTPort))...)

	// The listener is up once its port can no longer be bound.
	require.Eventually(t, func() bool {
		conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: openviduSRTPort})
		if err != nil {
			return true
		}
		_ = conn.Close()
		return false
	}, openviduSourceTimeout, statePoll, "the SRT listener did not start")

	return fmt.Sprintf("srt://127.0.0.1:%d", openviduSRTPort)
}

// END OPENVIDU BLOCK
