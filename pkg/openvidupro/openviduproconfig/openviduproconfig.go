// BEGIN OPENVIDU BLOCK
package openviduproconfig

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// RtcEngine selects the WebRTC engine.
// NOTE: this setting is currently informational only
type RtcEngine string

const (
	RtcEnginePion      RtcEngine = "pion"
	RtcEngineMediasoup RtcEngine = "mediasoup"

	// Defaults for the rtspsrc element used by RTSP/RTSPS URL-pull ingress.
	DefaultRtspLatencyMs     uint   = 2000
	DefaultRtspDropOnLatency bool   = true
	DefaultRtspPortRange     string = "0-0"
)

// RtcConfig configures the WebRTC engine. See RtcEngine for current limitations.
type RtcConfig struct {
	Engine RtcEngine `yaml:"engine,omitempty"`
}

// RtspConfig exposes the tunables of the GStreamer rtspsrc element used for
// RTSP/RTSPS URL-pull ingress. Unset (nil/empty) fields fall back to the
// Default* constants above.
type RtspConfig struct {
	// LatencyMs is the rtspsrc jitter-buffer size in milliseconds
	// (rtspsrc "latency"). A pointer so an explicit 0 is distinguishable from
	// "unset".
	LatencyMs *uint `yaml:"latency_ms,omitempty"`
	// DropOnLatency drops packets that arrive past the latency window
	// (rtspsrc "drop-on-latency"). A pointer so an explicit false is
	// distinguishable from "unset".
	DropOnLatency *bool `yaml:"drop_on_latency,omitempty"`
	// PortRange is the local UDP port range for RTP/RTCP reception
	// (rtspsrc "port-range"), e.g. "5000-5100". The default "0-0" lets the OS
	// pick ephemeral ports.
	PortRange string `yaml:"port_range,omitempty"`
}

type OpenViduProConfig struct {
	Rtc  RtcConfig  `yaml:"rtc,omitempty"`
	Rtsp RtspConfig `yaml:"rtsp,omitempty"`
}

// Latency returns the configured rtspsrc "latency" in milliseconds, or the default.
func (c RtspConfig) Latency() uint {
	if c.LatencyMs != nil {
		return *c.LatencyMs
	}
	return DefaultRtspLatencyMs
}

// DropOnLatencyOrDefault returns the configured rtspsrc "drop-on-latency", or the default.
func (c RtspConfig) DropOnLatencyOrDefault() bool {
	if c.DropOnLatency != nil {
		return *c.DropOnLatency
	}
	return DefaultRtspDropOnLatency
}

// PortRangeOrDefault returns the configured rtspsrc "port-range", or the default.
func (c RtspConfig) PortRangeOrDefault() string {
	if c.PortRange != "" {
		return c.PortRange
	}
	return DefaultRtspPortRange
}

// Validate checks that the config holds recognized values, returning an error
// upon misconfigurations to fail fast at startup.
func (c OpenViduProConfig) Validate() error {
	switch c.Rtc.Engine {
	case "", RtcEnginePion, RtcEngineMediasoup:
	default:
		return fmt.Errorf("invalid openvidu rtc engine %q (expected %q or %q)", c.Rtc.Engine, RtcEnginePion, RtcEngineMediasoup)
	}
	return c.Rtsp.Validate()
}

// Validate checks the rtspsrc tunables at startup, before they reach the
// element, where a bad value would only surface at the first RTSP pull: as an
// obscure property error for the latency, or as a warning and an ignored
// setting for the port range.
func (c RtspConfig) Validate() error {
	if c.LatencyMs != nil && *c.LatencyMs > math.MaxUint32 {
		return fmt.Errorf("invalid openvidu.rtsp.latency_ms %d (must fit in 32 bits)", *c.LatencyMs)
	}
	if c.PortRange != "" {
		if err := validatePortRange(c.PortRange); err != nil {
			return fmt.Errorf("invalid openvidu.rtsp.port_range %q: %w", c.PortRange, err)
		}
	}
	return nil
}

// validatePortRange accepts rtspsrc's "min-max" syntax with 0 < min <= max <=
// 65535, and the "0-0" that stands for no restriction. Each UDP stream takes an
// RTP/RTCP port pair from the range, so a range for audio and video needs at
// least four ports; that is left to the operator.
func validatePortRange(s string) error {
	minStr, maxStr, ok := strings.Cut(s, "-")
	if !ok {
		return errors.New("expected min-max")
	}
	min, err := strconv.ParseUint(minStr, 10, 16)
	if err != nil {
		return fmt.Errorf("min port: %w", err)
	}
	max, err := strconv.ParseUint(maxStr, 10, 16)
	if err != nil {
		return fmt.Errorf("max port: %w", err)
	}
	if min == 0 && max == 0 {
		return nil
	}
	if min == 0 || min > max {
		return errors.New("expected 0 < min <= max")
	}
	return nil
}

// END OPENVIDU BLOCK
