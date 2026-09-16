// BEGIN OPENVIDU BLOCK

package params

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/livekit/protocol/livekit"
)

func TestRedactURLUserinfo(t *testing.T) {
	cases := map[string]string{
		"rtsp://admin:s3cret@192.168.1.79/mystream":    "rtsp://admin:xxxxx@192.168.1.79/mystream",
		"rtsps://admin:p%40ss@cam.local:8554/live?x=1": "rtsps://admin:xxxxx@cam.local:8554/live?x=1",
		"rtsp://admin@192.168.1.79/mystream":           "rtsp://admin@192.168.1.79/mystream",
		"rtsp://192.168.1.79/mystream":                 "rtsp://192.168.1.79/mystream",
		"http://host/playlist.m3u8":                    "http://host/playlist.m3u8",
		"":                                             "",
		"::not a url":                                  "::not a url",
	}
	for in, want := range cases {
		require.Equal(t, want, redactURLUserinfo(in), in)
	}
}

// The service logs the redacted copy on every start; the password of a camera
// must not be in it, and the original info must keep it for the pull.
func TestCopyRedactedIngressInfoHidesThePassword(t *testing.T) {
	info := &livekit.IngressInfo{IngressId: "IN_x", StreamKey: "key", Url: "rtsp://admin:s3cret@cam/stream"}

	redacted := CopyRedactedIngressInfo(info)

	require.Equal(t, "rtsp://admin:xxxxx@cam/stream", redacted.Url)
	require.NotContains(t, redacted.String(), "s3cret")
	require.Equal(t, "rtsp://admin:s3cret@cam/stream", info.Url, "the original must not be touched")
}

// END OPENVIDU BLOCK
