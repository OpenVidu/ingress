// Copyright 2023 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package media

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/frostbyte73/core"
	"github.com/go-gst/go-gst/gst"

	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/media/rtmp"
	"github.com/livekit/ingress/pkg/media/urlpull"
	"github.com/livekit/ingress/pkg/media/whip"
	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/ingress/pkg/stats"
	"github.com/livekit/ingress/pkg/types"
	"github.com/livekit/protocol/ingress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
)

const (
	packetLatencyCaps = "application/x-livekit-latency"
)

type Source interface {
	GetSources() []*gst.Element
	ValidateCaps(*gst.Caps) error
	Start(ctx context.Context, onClose func()) error
	Close() error
}

type Input struct {
	lock sync.Mutex

	trackStatsGatherer map[types.StreamKind]*stats.MediaTrackStatGatherer

	bin    *gst.Bin
	source Source

	audioOutput *gst.Pad
	videoOutput *gst.Pad

	onOutputReady OutputReadyFunc
	closeFuse     core.Fuse
	closeErr      error

	enableStreamLatencyReduction bool

	padTiming map[string]*padTimingState

	gateMu         sync.Mutex
	gateReady      map[string]bool
	gateAllReady   bool
	gateOffset     time.Duration
	gateTerminator sync.Once
	latencyCaps    *gst.Caps

	onEOS func()
}

type OutputReadyFunc func(pad *gst.Pad, kind types.StreamKind)

func NewInput(ctx context.Context, p *params.Params, g *stats.LocalMediaStatsGatherer) (*Input, error) {
	src, err := CreateSource(ctx, p)
	if err != nil {
		return nil, err
	}

	bin := gst.NewBin("input")

	i := &Input{
		bin:                          bin,
		source:                       src,
		trackStatsGatherer:           make(map[types.StreamKind]*stats.MediaTrackStatGatherer),
		enableStreamLatencyReduction: shouldEnableStreamLatencyReduction(p),
		padTiming:                    make(map[string]*padTimingState),
		gateReady:                    make(map[string]bool),
		latencyCaps:                  gst.NewCapsFromString(packetLatencyCaps),
	}

	// BEGIN OPENVIDU BLOCK
	// We must add the rtspsrc element to the parent bin so it can later link its
	// dynamic pads to the ghost sink pads of the video bin and audio bin.
	if urlpull.IsRTSP(p.Url) {
		urlsource, ok := src.(*urlpull.URLSource)
		if !ok {
			return nil, errors.New("URL is rtsp but source is not of type URLSource")
		}
		if err := bin.Add(urlsource.Rtspsrc); err != nil {
			return nil, err
		}
	}
	// END OPENVIDU BLOCK

	if p.InputType == livekit.IngressInput_URL_INPUT {
		// Gather input stats from the pipeline
		i.trackStatsGatherer[types.Audio] = g.RegisterTrackStats(stats.InputAudio)
		i.trackStatsGatherer[types.Video] = g.RegisterTrackStats(stats.InputVideo)
	}

	srcs := src.GetSources()
	if len(srcs) == 0 {
		return nil, errors.ErrSourceNotReady
	}

	for _, src := range srcs {
		decodeBin, err := gst.NewElement("decodebin3")
		if err != nil {
			return nil, err
		}

		if err := bin.AddMany(decodeBin, src); err != nil {
			return nil, err
		}

		if _, err = decodeBin.Connect("pad-added", i.onPadAdded); err != nil {
			return nil, err
		}

		if err = src.Link(decodeBin); err != nil {
			return nil, err
		}
	}

	return i, nil
}

func CreateSource(ctx context.Context, p *params.Params) (Source, error) {
	switch p.InputType {
	case livekit.IngressInput_RTMP_INPUT:
		return rtmp.NewRTMPRelaySource(ctx, p)
	case livekit.IngressInput_WHIP_INPUT:
		return whip.NewWHIPRelaySource(ctx, p)
	case livekit.IngressInput_URL_INPUT:
		return urlpull.NewURLSource(ctx, p)
	default:
		return nil, ingress.ErrInvalidIngressType
	}
}

func (i *Input) OnOutputReady(f OutputReadyFunc) {
	i.onOutputReady = f
}

func (i *Input) SetOnEOS(f func()) {
	i.onEOS = f
}

func (i *Input) Start(ctx context.Context, onCloseTimeout func(ctx context.Context)) error {
	return i.source.Start(ctx, func() {
		if i.onEOS != nil {
			i.onEOS()
		}

		go func() {
			t := time.NewTimer(5 * time.Second)
			select {
			case <-t.C:
				logger.Infow("timeout while waiting for source closure to trigger pipeline stop. Pipeline frozen")
				if onCloseTimeout != nil {
					onCloseTimeout(context.Background())
				}
			case <-i.closeFuse.Watch():
				t.Stop()
			}
		}()
	})
}

func (i *Input) Close() error {
	// Make sure Close is idempotent and always return the input error
	i.closeFuse.Once(func() {
		i.closeErr = i.source.Close()
	})

	return i.closeErr
}

func (i *Input) onPadAdded(decodeBin *gst.Element, pad *gst.Pad) {
	var err error

	defer func() {
		if err != nil {
			msg := gst.NewErrorMessage(i.bin.Element, err, err.Error(), nil)
			i.bin.Element.GetBus().Post(msg)
		}
	}()

	typefind, err := i.bin.GetElementByName("typefind")
	if err == nil && typefind != nil {
		var caps interface{}
		caps, err = typefind.GetProperty("caps")
		if err == nil && caps != nil {
			typedCaps := caps.(*gst.Caps)
			err = i.source.ValidateCaps(typedCaps)
			if err != nil {
				logger.Infow("input caps validation failed", "error", err)

				// BEGIN OPENVIDU BLOCK
				// rtspsrc can momentarily expose empty/unnegotiated caps at the
				// typefind stage; ignore the validation failure ONLY in that
				// case so the stream can still proceed. A genuinely unsupported
				// RTSP payload (non-empty caps that fail validation) must still
				// surface its error rather than fail opaquely downstream.
				// NOTE: application/x-rtp is in urlpull.supportedMimeTypes, so
				// valid RTSP streams validate cleanly and never reach here; if a
				// future valid stream surfaces different caps, add its mime type
				// to supportedMimeTypes instead of broadening this bypass.
				_, rtspErr := i.bin.GetElementByName(urlpull.RtspsrcElementName)
				if rtspErr != nil || typedCaps.GetSize() != 0 {
					// Not the empty-caps rtspsrc case: propagate the error.
					return
				}
				logger.Infow("ignoring empty caps validation failure for rtspsrc")
				err = nil
				// END OPENVIDU BLOCK
			}
		}
	}

	// Make sure we emit scte35 markers if available
	tsparser, _ := i.bin.GetElementByName("tsdemux0")
	if tsparser != nil {
		err := tsparser.SetProperty("send-scte35-events", true)
		if err != nil {
			logger.Errorw("failed setting `send-scte35-events` property", err)
		}
	}

	// surface callback for first audio and video pads, plug in fakesink on the rest
	i.lock.Lock()
	newPad := false
	var kind types.StreamKind
	var ghostPad *gst.GhostPad
	var timingState *padTimingState
	if strings.HasPrefix(pad.GetName(), "audio") {
		if i.audioOutput == nil {
			newPad = true
			kind = types.Audio
			i.audioOutput = pad
			ghostPad = gst.NewGhostPad("audio", pad)
			timingState = &padTimingState{}
		}
	} else if strings.HasPrefix(pad.GetName(), "video") {
		if i.videoOutput == nil {
			newPad = true
			kind = types.Video
			i.videoOutput = pad
			ghostPad = gst.NewGhostPad("video", pad)
			timingState = &padTimingState{}
		}
	}
	i.lock.Unlock()

	// don't need this pad, link to fakesink
	if !newPad {
		var sink *gst.Element

		sink, err = gst.NewElement("fakesink")
		if err != nil {
			logger.Errorw("failed to create fakesink", err)
			return
		}
		var pads []*gst.Pad

		pads, err = sink.GetSinkPads()
		pad.Link(pads[0])
		return
	}

	if !i.bin.AddPad(ghostPad.Pad) {
		logger.Errorw("failed to add ghost pad", nil)
		return
	}
	pad = ghostPad.Pad
	padName := pad.GetName()

	logger.Debugw("input ghost pad added", "padName", padName)

	if i.enableStreamLatencyReduction {
		state := timingState
		i.registerGatePad(padName, state)
		i.addGateProbe(pad, padName, state)
		i.addSegmentEventProbe(pad, padName, state)
	}

	// Gather bitrate stats & attach latency meta from the pipeline
	// BEGIN OPENVIDU BLOCK
	// Pass the decodebin that produced this pad so the stats probe can locate
	// the correct internal multiqueue even when multiple decodebins exist (RTSP).
	i.addStatsCollectionProbe(decodeBin, kind)
	// END OPENVIDU BLOCK

	if i.onOutputReady != nil {
		i.onOutputReady(pad, kind)
	}
}

func (i *Input) addStatsCollectionProbe(decodeBin *gst.Element, kind types.StreamKind) {
	// Do a best effort to add probe to retrieve bitrate and ingest time metadata.
	// Time metadata would be best ingested at the source but flv muxer doesn't support carrying it through.
	// The multiqueue is generally created in the pipeline before the decoders

	// BEGIN OPENVIDU BLOCK
	// The OpenVidu RTSP path builds one decodebin per stream (audio + video), so
	// the process-global "multiqueue0" name no longer identifies the decoder for
	// a given kind. Locate the multiqueue inside the decodebin that produced this
	// pad; fall back to the legacy global lookup for single-decodebin inputs.
	mq := findDecodeMultiqueue(decodeBin)
	if mq == nil {
		var err error
		mq, err = i.bin.GetElementByName("multiqueue0")
		if err != nil {
			// No multiqueue in that pipeline
			logger.Debugw("could not retrieve multiqueue element from pipeline", "error", err)
			return
		}
	}
	// END OPENVIDU BLOCK

	pads, err := mq.GetSinkPads()
	if err != nil {
		logger.Errorw("failed retrieving multiqueue sink pads", err)
		return
	}

	for _, pad := range pads {
		caps := pad.GetCurrentCaps()
		if caps != nil && caps.GetSize() > 0 {
			gstStruct := caps.GetStructureAt(0)
			padKind := getKindFromGstMimeType(gstStruct)

			g := i.trackStatsGatherer[kind]

			if padKind == kind {
				var lastAttached time.Time

				pad.AddProbe(gst.PadProbeTypeBuffer, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
					buffer := info.GetBuffer()
					if buffer == nil {
						return gst.PadProbeOK
					}

					size := buffer.GetSize()
					if g != nil {
						g.MediaReceived(size)
					}

					// mark the packet with the current time to be able to calculate packet processing latency at output stage
					now := time.Now()
					if now.Sub(lastAttached) >= latencySampleInterval && buffer.IsWritable() {
						buffer.AddReferenceTimestampMeta(i.latencyCaps, gst.ClockTime(uint64(now.UnixNano())), gst.ClockTime(0))
						lastAttached = now
					}

					return gst.PadProbeOK
				})

				return
			}
		} else {
			logger.Debugw("could not retrieve multiqueue pad caps", "error", err)
		}
	}

	logger.Debugw("no pad on multiqueue with required kind found", "kind", kind)
}

// BEGIN OPENVIDU BLOCK
// findDecodeMultiqueue returns the internal multiqueue element of the given
// decodebin, or nil if it has none. decodebin3 creates its multiqueue eagerly,
// but its process-global name (multiqueue0, multiqueue1, ...) depends on
// creation order, so we search by name prefix within the decodebin rather than
// relying on a fixed global name (which breaks when >1 decodebin exists).
func findDecodeMultiqueue(decodeBin *gst.Element) *gst.Element {
	if decodeBin == nil {
		return nil
	}
	elems, err := gst.ToGstBin(decodeBin).GetElementsRecursive()
	if err != nil {
		return nil
	}
	for _, e := range elems {
		if strings.HasPrefix(e.GetName(), "multiqueue") {
			return e
		}
	}
	return nil
}

// END OPENVIDU BLOCK

func shouldEnableStreamLatencyReduction(p *params.Params) bool {
	enableGate := p.EnableStreamLatencyReduction
	if !enableGate {
		return false
	}

	// whip RTP streams are independent and common latency reduction offset can't be applied
	if p.InputType == livekit.IngressInput_WHIP_INPUT {
		return false
	}

	// BEGIN OPENVIDU BLOCK
	// RTSP is ingested as two independent decodebins (separate audio/video RTP
	// streams), like WHIP above. A common latency-reduction offset cannot be
	// applied across independent streams, and the gate's single-source
	// assumptions cause dropped buffers / A-V desync, so disable it for RTSP.
	if p.InputType == livekit.IngressInput_URL_INPUT && urlpull.IsRTSP(p.Url) {
		return false
	}
	// END OPENVIDU BLOCK

	if p.InputType == livekit.IngressInput_URL_INPUT &&
		(strings.HasPrefix(p.Url, "http://") || strings.HasPrefix(p.Url, "https://")) {
		// Disable for non SRT URLs
		// For sreaming VOD files or HLS streams over HTTP, at least 1 segment worth of data will be buffered.
		// That doesn't allow arrival rate based latency reduction to be applied.
		return false
	}

	logger.Debugw("stream latency reduction enabled", "inputType", p.InputType)
	return true
}
