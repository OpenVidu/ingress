// BEGIN OPENVIDU BLOCK

package urlpull

import (
	"testing"

	"github.com/go-gst/go-gst/gst"
	"github.com/stretchr/testify/require"
)

// A camera announces its streams in the SDP; the first audio and the first
// video are taken, everything else (a second stream of a kind, ONVIF metadata,
// backchannel audio) is declined so rtspsrc never sets it up.
func TestRTSPStreamPolicyTakesOneStreamPerKind(t *testing.T) {
	p := newRTSPStreamPolicy()

	require.True(t, p.accept("video"))
	require.True(t, p.accept("audio"))
	require.False(t, p.accept("video"), "a second video stream is declined")
	require.False(t, p.accept("audio"), "a second audio stream is declined")
	require.False(t, p.accept("application"), "metadata streams are declined")
	require.False(t, p.accept("text"))
	require.False(t, p.accept(""), "a stream without a media kind is declined")
}

// Declining a stream must not spend a kind: a camera that lists its metadata
// stream first still gets its audio and video.
func TestRTSPStreamPolicyDeclinedStreamsAreFree(t *testing.T) {
	p := newRTSPStreamPolicy()

	require.False(t, p.accept("application"))
	require.True(t, p.accept("audio"))
	require.True(t, p.accept("video"))
}

func TestRTSPStreamDescription(t *testing.T) {
	gst.Init(nil)

	media, enc := rtspStreamDescription(gst.NewCapsFromString(
		"application/x-rtp, media=(string)video, payload=(int)96, clock-rate=(int)90000, encoding-name=(string)H264"))
	require.Equal(t, "video", media)
	require.Equal(t, "H264", enc)

	media, enc = rtspStreamDescription(gst.NewCapsFromString("application/x-rtp, media=(string)application"))
	require.Equal(t, "application", media)
	require.Equal(t, "", enc)

	media, enc = rtspStreamDescription(gst.NewCapsFromString("audio/x-raw"))
	require.Equal(t, "", media)
	require.Equal(t, "", enc)

	// A pad added before negotiation has no caps, and go-gst hands a NULL
	// caps pointer over as a non-nil wrapper; neither may crash the streaming
	// thread that calls this.
	media, enc = rtspStreamDescription(nil)
	require.Equal(t, "", media)
	require.Equal(t, "", enc)

	media, enc = rtspStreamDescription(gst.ToGstCaps(nil))
	require.Equal(t, "", media)
	require.Equal(t, "", enc)
}

// END OPENVIDU BLOCK
