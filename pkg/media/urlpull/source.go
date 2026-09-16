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

	"github.com/livekit/protocol/logger"

	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/params"
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
	// Like the queue of the other URL sources (see NewURLSource), let bytes and
	// time bound the queue rather than a buffer count: RTP buffers are small
	// (about 1.2 KB), so queue2's default of 100 buffers would block the rtspsrc
	// streaming thread on the first hiccup downstream.
	if err := queue.SetProperty("max-size-buffers", uint(0)); err != nil {
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
// (the element that owns pad). It absorbs an rtspsrc stream we do not ingest,
// so rtspsrc does not stall with not-linked flow errors on the dangling pad.
// Returns false if the fakesink could not be created, added and linked.
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
	// A drain must never hold the pipeline up: no clock sync, and no async
	// state change that would make the running pipeline wait for its preroll.
	if err := fakesink.SetProperty("sync", false); err != nil {
		return false
	}
	if err := fakesink.SetProperty("async", false); err != nil {
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

// rtspStreamPolicy decides which of the streams announced in the RTSP SDP are
// set up. rtspsrc emits select-stream once per media before SETUP and skips the
// stream when the handler returns false, so nothing is received for it and no
// pad ever appears. The ingress publishes one audio and one video track, so the
// first stream of each kind is taken and everything else is declined: a second
// stream of a kind, ONVIF metadata (media "application"), backchannel audio.
// Declining beats draining: no bandwidth is spent and no pad is left to link.
type rtspStreamPolicy struct {
	mu       sync.Mutex
	accepted map[string]bool
}

func newRTSPStreamPolicy() *rtspStreamPolicy {
	return &rtspStreamPolicy{accepted: make(map[string]bool)}
}

// accept reports whether a stream of the given media kind is set up, and
// records it so the next stream of that kind is declined.
func (p *rtspStreamPolicy) accept(media string) bool {
	if media != "audio" && media != "video" {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.accepted[media] {
		return false
	}
	p.accepted[media] = true
	return true
}

// rtspStreamDescription reads the media kind and the encoding name from RTP
// caps ("application/x-rtp, media=(string)video, encoding-name=(string)H264,
// ..."), as rtspsrc builds them from the SDP. Either is empty when missing;
// nil caps, or caps wrapping a NULL pointer, yield two empty strings.
func rtspStreamDescription(caps *gst.Caps) (media, encodingName string) {
	if caps == nil || caps.Unsafe() == nil || caps.GetSize() == 0 {
		return "", ""
	}
	s := caps.GetStructureAt(0)
	if s == nil {
		return "", ""
	}
	if v, err := s.GetValue("media"); err == nil {
		media, _ = v.(string)
	}
	if v, err := s.GetValue("encoding-name"); err == nil {
		encodingName, _ = v.(string)
	}
	return media, encodingName
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
		elem, err = gst.NewElementWithName("rtspsrc", "rtspsrc")
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

		// select-stream fires once per media announced in the SDP, before
		// SETUP. Take the first audio and the first video stream and decline
		// the rest, so surplus or foreign streams are never set up at all.
		policy := newRTSPStreamPolicy()
		if _, err = elem.Connect("select-stream", func(_ *gst.Element, num uint, caps *gst.Caps) bool {
			media, encodingName := rtspStreamDescription(caps)
			accepted := policy.accept(media)
			logger.Infow("rtsp stream announced", "stream", num, "media", media, "encodingName", encodingName, "accepted", accepted)
			return accepted
		}); err != nil {
			return nil, err
		}

		// pad-added fires on rtspsrc's streaming thread as each selected RTP
		// stream starts. Serialize with mu so concurrent audio/video pads do
		// not race on the shared bins. Whatever cannot be linked is drained
		// into a fakesink: a pad rtspsrc leaves unlinked stalls the source
		// with not-linked flow errors.
		var mu sync.Mutex
		if _, err = elem.Connect("pad-added", func(src *gst.Element, pad *gst.Pad) {
			padName := pad.GetName()
			// GetCurrentCaps can return nil for a pad added before its caps
			// are negotiated; rtspStreamDescription tolerates that.
			media, encodingName := rtspStreamDescription(pad.GetCurrentCaps())
			logger.Infow("rtspsrc pad-added", "padName", padName, "media", media, "encodingName", encodingName)

			mu.Lock()
			defer mu.Unlock()

			var targetBin *gst.Bin
			var targetQueue *gst.Element
			switch media {
			case "audio":
				targetBin, targetQueue = audioBin, audioQueue
			case "video":
				targetBin, targetQueue = videoBin, videoQueue
			}

			var sinkPad *gst.Pad
			if targetBin != nil {
				sinkPad = targetBin.GetStaticPad("sink")
			}
			if sinkPad == nil || sinkPad.IsLinked() {
				// Not audio or video, no caps yet, or a second stream of a kind
				// that select-stream should have declined: nothing to ingest.
				logger.Warnw("rtspsrc pad not ingested, draining it to a fakesink", nil, "padName", padName, "media", media)
				if !drainToFakesink(src, pad) {
					logger.Errorw("failed to drain rtspsrc pad to fakesink", nil, "padName", padName, "media", media)
				}
				return
			}

			if ret := pad.Link(sinkPad); ret != gst.PadLinkOK && ret != gst.PadLinkWasLinked {
				logger.Errorw("failed to link rtspsrc pad", nil, "padName", padName, "media", media, "padLinkReturnValue", ret)
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

	} else if p.EnableUDPURLPull && strings.HasPrefix(p.Url, "udp://") {
		elem, err = gst.NewElement("udpsrc")
		if err != nil {
			return nil, err
		}
		err = elem.SetProperty("uri", p.Url)
		if err != nil {
			return nil, err
		}

		if p.MulticastInterface != "" {
			err = elem.SetProperty("multicast-iface", p.MulticastInterface)
			if err != nil {
				return nil, err
			}
		}

		// udpsrc doesn't expose a stats property, so leave printStats unset
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
