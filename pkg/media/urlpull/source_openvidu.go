// BEGIN OPENVIDU BLOCK

package urlpull

import (
	"strings"
	"sync"

	"github.com/go-gst/go-gst/pkg/gst"

	"github.com/livekit/protocol/logger"

	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/gstutil"
	"github.com/livekit/ingress/pkg/params"
)

// IsRTSP reports whether url is an RTSP or RTSPS URL. The RTSP ingress path
// builds a bespoke pipeline topology, and several call sites must agree on when
// that topology is in use, so the predicate lives in one place.
func IsRTSP(url string) bool {
	return strings.HasPrefix(url, "rtsp://") || strings.HasPrefix(url, "rtsps://")
}

// newRTSPSource builds the URL source of an RTSP pull: an rtspsrc whose audio
// and video RTP streams land in one queue bin each, so that Input decodes them
// with a decodebin per stream.
func newRTSPSource(p *params.Params) (*URLSource, error) {
	elem, err := gstutil.NewElementWithName("rtspsrc", "rtspsrc")
	if err != nil {
		return nil, err
	}

	rtsp := p.OpenVidu.Rtsp
	if err = gstutil.SetProperty(elem, "location", p.Url); err != nil {
		return nil, err
	}
	if err = gstutil.SetProperty(elem, "latency", uint32(rtsp.Latency())); err != nil {
		return nil, err
	}
	if err = gstutil.SetProperty(elem, "drop-on-latency", rtsp.DropOnLatencyOrDefault()); err != nil {
		return nil, err
	}
	if err = gstutil.SetProperty(elem, "port-range", rtsp.PortRangeOrDefault()); err != nil {
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
	elem.Connect("select-stream", func(_ gst.Element, num uint, caps *gst.Caps) bool {
		media, encodingName := rtspStreamDescription(caps)
		// The binding refs the caps it passes here and leaves the unref to a
		// finalizer. rtspsrc edits these caps after the signal returns, which
		// fails while they are shared, so drop that reference now.
		if caps != nil {
			gst.UnsafeCapsUnref(caps)
		}
		accepted := policy.accept(media)
		logger.Infow("rtsp stream announced", "stream", num, "media", media, "encodingName", encodingName, "accepted", accepted)
		return accepted
	})

	// pad-added fires on rtspsrc's streaming thread as each selected RTP
	// stream starts. Serialize with mu so concurrent audio/video pads do
	// not race on the shared bins. Whatever cannot be linked is drained
	// into a fakesink: a pad rtspsrc leaves unlinked stalls the source
	// with not-linked flow errors.
	var mu sync.Mutex
	elem.Connect("pad-added", func(src gst.Element, pad gst.Pad) {
		padName := pad.GetName()
		// GetCurrentCaps can return nil for a pad added before its caps
		// are negotiated; rtspStreamDescription tolerates that.
		media, encodingName := rtspStreamDescription(pad.GetCurrentCaps())
		logger.Infow("rtspsrc pad-added", "padName", padName, "media", media, "encodingName", encodingName)

		mu.Lock()
		defer mu.Unlock()

		var targetBin gst.Bin
		var targetQueue gst.Element
		switch media {
		case "audio":
			targetBin, targetQueue = audioBin, audioQueue
		case "video":
			targetBin, targetQueue = videoBin, videoQueue
		}

		var sinkPad gst.Pad
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
	})

	// NOTE: rtspsrc does not expose a top-level "stats" property, so unlike
	// the SRT path there is no periodic stats logging wired here.
	return &URLSource{
		params:   p,
		src:      videoBin,
		srcAudio: audioBin,
		Rtspsrc:  elem,
	}, nil
}

// newQueueBin builds a bin named binName containing a single queue2 element
// named queueName, exposing the queue's sink and src pads as ghost pads on the
// bin. It is used to wrap the audio and video branches of an RTSP source so
// their dynamic rtspsrc pads can be linked independently.
func newQueueBin(binName, queueName string) (gst.Bin, gst.Element, error) {
	bin := gstutil.NewBin(binName)

	queue, err := gstutil.NewElementWithName("queue2", queueName)
	if err != nil {
		return nil, nil, err
	}
	// Like the queue of the other URL sources (see NewURLSource), let bytes and
	// time bound the queue rather than a buffer count: RTP buffers are small
	// (about 1.2 KB), so queue2's default of 100 buffers would block the rtspsrc
	// streaming thread on the first hiccup downstream.
	if err := gstutil.SetProperty(queue, "max-size-buffers", uint32(0)); err != nil {
		return nil, nil, err
	}
	if err := gstutil.Add(bin, queue); err != nil {
		return nil, nil, err
	}

	sink := queue.GetStaticPad("sink")
	if sink == nil {
		return nil, nil, errors.ErrUnableToAddPad
	}
	if !bin.AddPad(gst.NewGhostPad("sink", sink)) {
		return nil, nil, errors.ErrUnableToAddPad
	}

	src := queue.GetStaticPad("src")
	if src == nil {
		return nil, nil, errors.ErrUnableToAddPad
	}
	if !bin.AddPad(gst.NewGhostPad("src", src)) {
		return nil, nil, errors.ErrUnableToAddPad
	}

	return bin, queue, nil
}

// drainToFakesink links pad into a new fakesink added to the same bin as src
// (the element that owns pad). It absorbs an rtspsrc stream we do not ingest,
// so rtspsrc does not stall with not-linked flow errors on the dangling pad.
// Returns false if the fakesink could not be created, added and linked.
func drainToFakesink(src gst.Element, pad gst.Pad) bool {
	parentBin, ok := src.GetParent().(gst.Bin)
	if !ok || parentBin == nil {
		return false
	}

	fakesink, err := gstutil.NewElement("fakesink")
	if err != nil {
		return false
	}
	// A drain must never hold the pipeline up: no clock sync, and no async
	// state change that would make the running pipeline wait for its preroll.
	if err := gstutil.SetProperty(fakesink, "sync", false); err != nil {
		return false
	}
	if err := gstutil.SetProperty(fakesink, "async", false); err != nil {
		return false
	}
	if err := gstutil.Add(parentBin, fakesink); err != nil {
		return false
	}
	if !fakesink.SyncStateWithParent() {
		return false
	}

	sinkPad := fakesink.GetStaticPad("sink")
	if sinkPad == nil {
		return false
	}
	return pad.Link(sinkPad) == gst.PadLinkOK
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
// nil caps yield two empty strings.
func rtspStreamDescription(caps *gst.Caps) (media, encodingName string) {
	if caps == nil || caps.GetSize() == 0 {
		return "", ""
	}
	s := caps.GetStructure(0)
	if s == nil {
		return "", ""
	}
	return s.GetString("media"), s.GetString("encoding-name")
}

// END OPENVIDU BLOCK
