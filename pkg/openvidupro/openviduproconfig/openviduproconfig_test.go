// BEGIN OPENVIDU BLOCK

package openviduproconfig

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateEngine(t *testing.T) {
	require.NoError(t, OpenViduProConfig{}.Validate(), "an empty config is the default")
	require.NoError(t, OpenViduProConfig{Rtc: RtcConfig{Engine: RtcEnginePion}}.Validate())
	require.NoError(t, OpenViduProConfig{Rtc: RtcConfig{Engine: RtcEngineMediasoup}, Rtsp: RtspConfig{PortRange: "5000-5100"}}.Validate())
	require.Error(t, OpenViduProConfig{Rtc: RtcConfig{Engine: "janus"}}.Validate())
}

func TestValidatePortRange(t *testing.T) {
	for _, ok := range []string{"0-0", "5000-5100", "5000-5000", "1-65535"} {
		require.NoError(t, validatePortRange(ok), ok)
	}
	for _, bad := range []string{"", "5000", "5100-5000", "0-5000", "-1-5", "a-b", "5000-70000", "5000-5100-5200"} {
		require.Error(t, validatePortRange(bad), bad)
	}
	require.Error(t, OpenViduProConfig{Rtsp: RtspConfig{PortRange: "5100-5000"}}.Validate(), "reaches Validate through the pro config")
}

func TestValidateLatency(t *testing.T) {
	ok := uint(500)
	require.NoError(t, RtspConfig{LatencyMs: &ok}.Validate())

	zero := uint(0)
	require.NoError(t, RtspConfig{LatencyMs: &zero}.Validate(), "an explicit 0 is a valid jitter buffer")

	tooBig := uint(math.MaxUint32)
	tooBig++
	require.Error(t, RtspConfig{LatencyMs: &tooBig}.Validate())
}

// END OPENVIDU BLOCK
