// BEGIN OPENVIDU BLOCK
package openvidu

type OpenViduConfig struct {
	Width     uint32  `yaml:"width"`
	Height    uint32  `yaml:"height"`
	FrameRate float64 `yaml:"frameRate"`
	Bitrate   uint32  `yaml:"bitrate"`
}

func DefaultOpenViduConfig() OpenViduConfig {
	return OpenViduConfig{
		Width:     1920,
		Height:    1080,
		FrameRate: 32,
		Bitrate:   5000000,
	}
}

// END OPENVIDU BLOCK
