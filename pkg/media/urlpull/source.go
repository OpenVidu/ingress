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

package urlpull

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/frostbyte73/core"
	"github.com/go-gst/go-gst/gst"

	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/protocol/logger"
)

var (
	supportedMimeTypes = []string{

		// BEGIN OPENVIDU BLOCK
		"application/x-rtp", // RTSP streams have this mime type
		// END OPENVIDU BLOCK

		"audio/x-m4a",
		"application/x-hls",
		"video/quicktime",
		"video/x-matroska",
		"video/webm",
		"video/mpegts",
		"audio/ogg",
		"application/x-id3",
		"audio/mpeg",
	}
)

// BEGIN OPENVIDU BLOCK

// RtspsrcElementName is the GStreamer element name given to the rtspsrc element
// created for RTSP/RTSPS ingress. It is shared so code in other packages (see
// media.Input) can locate the element by name without duplicating the literal.
const RtspsrcElementName = "rtspsrc"

// IsRTSP reports whether url is an RTSP or RTSPS URL. The RTSP ingress path
// builds a bespoke pipeline topology, and several call sites must agree on when
// that topology is in use, so the predicate lives in one place.
func IsRTSP(url string) bool {
	return strings.HasPrefix(url, "rtsp://") || strings.HasPrefix(url, "rtsps://")
}

// newQueueBin builds a bin named binName containing a single queue2 element
// named queueName, exposing the queue's sink and src pads as ghost pads on the
// bin. It is used to wrap the audio and video branches of an RTSP source so
// their dynamic rtspsrc pads can be linked independently.
func newQueueBin(binName, queueName string) (*gst.Bin, *gst.Element, error) {
	bin := gst.NewBin(binName)

	queue, err := gst.NewElementWithName("queue2", queueName)
	if err != nil {
		return nil, nil, err
	}
	if err := bin.Add(queue); err != nil {
		return nil, nil, err
	}

	sink := queue.GetStaticPad("sink")
	if sink == nil {
		return nil, nil, errors.ErrUnableToAddPad
	}
	if !bin.AddPad(gst.NewGhostPad("sink", sink).Pad) {
		return nil, nil, errors.ErrUnableToAddPad
	}

	src := queue.GetStaticPad("src")
	if src == nil {
		return nil, nil, errors.ErrUnableToAddPad
	}
	if !bin.AddPad(gst.NewGhostPad("src", src).Pad) {
		return nil, nil, errors.ErrUnableToAddPad
	}

	return bin, queue, nil
}

// drainToFakesink links pad into a new fakesink added to the same bin as src
// (the element that owns pad). It is used to absorb a surplus rtspsrc stream —
// e.g. a second audio or video track we do not ingest — so rtspsrc does not
// stall with not-linked flow errors on the dangling pad. Returns false if the
// fakesink could not be created, added and linked.
func drainToFakesink(src *gst.Element, pad *gst.Pad) bool {
	parent := src.GetParent()
	if parent == nil {
		return false
	}
	parentBin := gst.ToGstBin(parent)
	if parentBin == nil {
		return false
	}

	fakesink, err := gst.NewElement("fakesink")
	if err != nil {
		return false
	}
	if err := parentBin.Add(fakesink); err != nil {
		return false
	}
	if !fakesink.SyncStateWithParent() {
		return false
	}

	sinkPads, err := fakesink.GetSinkPads()
	if err != nil || len(sinkPads) == 0 {
		return false
	}
	return pad.Link(sinkPads[0]) == gst.PadLinkOK
}

// END OPENVIDU BLOCK

type URLSource struct {

	// BEGIN OPENVIDU BLOCK
	srcAudio *gst.Element // audio sub-bin, only set for RTSP sources
	Rtspsrc  *gst.Element // rtspsrc element, only set for RTSP sources
	// END OPENVIDU BLOCK

	params     *params.Params
	src        *gst.Element
	pad        *gst.Pad
	printStats func()

	done core.Fuse
}

func NewURLSource(_ context.Context, p *params.Params) (*URLSource, error) {
	var printStats func()

	var elem *gst.Element
	var err error
	if strings.HasPrefix(p.Url, "http://") || strings.HasPrefix(p.Url, "https://") {
		elem, err = gst.NewElement("souphttpsrc")
		if err != nil {
			return nil, err
		}

		err = elem.SetProperty("location", p.Url)
		if err != nil {
			return nil, err
		}

	} else if strings.HasPrefix(p.Url, "srt://") {
		elem, err = gst.NewElement("srtclientsrc")
		if err != nil {
			return nil, err
		}
		err = elem.SetProperty("uri", p.Url)
		if err != nil {
			return nil, err
		}

		printStats = func() {
			str, _ := elem.GetProperty("stats")
			if str != nil {
				if v, ok := str.(*gst.Structure); ok {
					logger.Infow("SRT input stats", "stats", v.String())
				}
			}
		}

		// BEGIN OPENVIDU BLOCK
	} else if IsRTSP(p.Url) {
		elem, err = gst.NewElementWithName("rtspsrc", RtspsrcElementName)
		if err != nil {
			return nil, err
		}

		rtsp := p.OpenVidu.Rtsp
		if err = elem.SetProperty("location", p.Url); err != nil {
			return nil, err
		}
		if err = elem.SetProperty("latency", rtsp.Latency()); err != nil {
			return nil, err
		}
		if err = elem.SetProperty("drop-on-latency", rtsp.DropOnLatencyOrDefault()); err != nil {
			return nil, err
		}
		if err = elem.SetProperty("port-range", rtsp.PortRangeOrDefault()); err != nil {
			return nil, err
		}

		// rtspsrc exposes its audio and video RTP streams as separate dynamic
		// pads. Build one queue bin per kind so each can be linked and decoded
		// independently (Input.NewInput creates a decodebin per source returned
		// by GetSources).
		videoBin, videoQueue, err := newQueueBin("videoinput", "videoqueue")
		if err != nil {
			return nil, err
		}
		audioBin, audioQueue, err := newQueueBin("audioinput", "audioqueue")
		if err != nil {
			return nil, err
		}

		// pad-added fires on rtspsrc's streaming thread as each RTP stream is
		// negotiated. Serialize with mu so concurrent audio/video pads do not
		// race on the shared bins.
		var mu sync.Mutex
		if _, err = elem.Connect("pad-added", func(src *gst.Element, pad *gst.Pad) {
			padName := pad.GetName()
			logger.Infow("rtspsrc pad-added", "padName", padName)

			// Guard against a pad added before its caps are negotiated:
			// GetCurrentCaps() can return nil, and ForEach on a nil caps would
			// panic on this streaming thread and crash the process.
			caps := pad.GetCurrentCaps()
			if caps == nil {
				logger.Errorw("rtspsrc pad has no current caps", nil, "padName", padName)
				return
			}

			var media string
			caps.ForEach(func(_ *gst.CapsFeatures, structure *gst.Structure) bool {
				value, getMediaErr := structure.GetValue("media")
				if getMediaErr != nil {
					return true // keep scanning the remaining structures
				}
				if s, ok := value.(string); ok {
					media = s
					return false // found the media type, stop iterating
				}
				return true
			})

			var targetBin *gst.Bin
			var targetQueue *gst.Element
			switch media {
			case "audio":
				targetBin, targetQueue = audioBin, audioQueue
			case "video":
				targetBin, targetQueue = videoBin, videoQueue
			default:
				logger.Errorw("rtspsrc pad is neither audio nor video", nil, "padName", padName, "media", media)
				return
			}

			mu.Lock()
			defer mu.Unlock()

			sinkPad := targetBin.GetStaticPad("sink")
			if sinkPad == nil {
				logger.Errorw("failed to get target bin sink pad", nil, "media", media)
				return
			}

			if sinkPad.IsLinked() {
				// The source is sending more than one stream of this kind; we
				// only ingest the first. Drain the surplus pad into a fakesink so
				// rtspsrc does not stall with not-linked flow errors on it.
				logger.Warnw("target sink already linked; draining extra stream to fakesink", nil, "media", media, "padName", padName)
				if !drainToFakesink(src, pad) {
					logger.Errorw("failed to drain extra rtspsrc pad to fakesink", nil, "media", media, "padName", padName)
				}
				return
			}

			if ret := pad.Link(sinkPad); ret != gst.PadLinkOK && ret != gst.PadLinkWasLinked {
				logger.Errorw("failed to link rtspsrc pad", nil, "media", media, "padLinkReturnValue", ret)
				return
			}
			targetQueue.SyncStateWithParent()
		}); err != nil {
			return nil, err
		}

		// NOTE: rtspsrc does not expose a top-level "stats" property, so unlike
		// the SRT path there is no periodic stats logging wired here.
		return &URLSource{
			params:   p,
			src:      videoBin.Element,
			srcAudio: audioBin.Element,
			Rtspsrc:  elem,
		}, nil
		// END OPENVIDU BLOCK

	} else {
		return nil, errors.ErrUnsupportedURLFormat
	}

	bin := gst.NewBin("urlinput")

	queue, err := gst.NewElement("queue2")
	if err != nil {
		return nil, err
	}

	// Disable buffer count limit and rely on bytes and time limits
	if err := queue.SetProperty("max-size-buffers", uint(0)); err != nil {
		return nil, err
	}

	if strings.HasPrefix(p.Url, "http://") || strings.HasPrefix(p.Url, "https://") {
		err = queue.SetProperty("use-buffering", true)
		if err != nil {
			return nil, err
		}
	}

	err = bin.AddMany(elem, queue)
	if err != nil {
		return nil, err
	}

	err = elem.Link(queue)
	if err != nil {
		return nil, err
	}

	pad := queue.GetStaticPad("src")
	if pad == nil {
		return nil, errors.ErrUnableToAddPad
	}

	ghostPad := gst.NewGhostPad("src", pad)
	if !bin.AddPad(ghostPad.Pad) {
		return nil, errors.ErrUnableToAddPad
	}

	return &URLSource{
		params:     p,
		src:        bin.Element,
		pad:        pad,
		printStats: printStats,
	}, nil
}

func (u *URLSource) GetSources() []*gst.Element {
	// BEGIN OPENVIDU BLOCK
	if u.srcAudio != nil {
		return []*gst.Element{
			u.src,
			u.srcAudio,
		}
	}
	// END OPENVIDU BLOCK
	return []*gst.Element{
		u.src,
	}
}

func (u *URLSource) ValidateCaps(caps *gst.Caps) error {
	if caps.GetSize() == 0 {
		return errors.ErrUnsupportedDecodeFormat
	}

	str := caps.GetStructureAt(0)
	if str == nil {
		return errors.ErrUnsupportedDecodeFormat
	}

	for _, mime := range supportedMimeTypes {
		if str.Name() == mime {
			return nil
		}
	}

	return errors.ErrUnsupportedDecodeMimeType(str.Name())
}

func (u *URLSource) Start(_ context.Context, _ func()) error {
	if u.printStats == nil {
		return nil
	}

	go func() {
		ticker := time.NewTicker(time.Minute)
		for {
			select {
			case <-u.done.Watch():
				ticker.Stop()
				return
			case <-ticker.C:
				u.printStats()
			}
		}
	}()

	return nil
}

func (u *URLSource) Close() error {
	// TODO find a way to send a EOS event without hanging

	u.done.Break()

	return nil
}
