// BEGIN OPENVIDU BLOCK
package openviduproconfig

type RtcEngine string

const (
	RtcEnginePion      RtcEngine = "pion"
	RtcEngineMediasoup RtcEngine = "mediasoup"
)

type RtcConfig struct {
	Engine RtcEngine `yaml:"engine,omitempty"`
}

type OpenViduProConfig struct {
	// WebRTC engine config.
	Rtc RtcConfig `yaml:"rtc"`
}

// END OPENVIDU BLOCK
