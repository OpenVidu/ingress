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

type URLSource struct {

	// BEGIN OPENVIDU BLOCK
	srcAudio *gst.Element
	Rtspsrc  *gst.Element
	// END OPENVIDU BLOCK

	params *params.Params
	src    *gst.Element
	pad    *gst.Pad
}

func NewURLSource(ctx context.Context, p *params.Params) (*URLSource, error) {
	bin := gst.NewBin("input")

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

		// BEGIN OPENVIDU BLOCK
	} else if strings.HasPrefix(p.Url, "rtsp://") || strings.HasPrefix(p.Url, "rtsps://") {

		elem, err = gst.NewElementWithName("rtspsrc", "rtspsrc")
		if err != nil {
			return nil, err
		}
		err = elem.SetProperty("location", p.Url)
		if err != nil {
			return nil, err
		}
		err = elem.SetProperty("latency", uint(2000))
		if err != nil {
			return nil, err
		}
		err = elem.SetProperty("drop-on-latency", true)
		if err != nil {
			return nil, err
		}
		err = elem.SetProperty("port-range", "0-0")
		if err != nil {
			return nil, err
		}
		// END OPENVIDU BLOCK

	} else {
		return nil, errors.ErrUnsupportedURLFormat
	}

	// BEGIN OPENVIDU BLOCK
	if strings.HasPrefix(p.Url, "rtsp://") || strings.HasPrefix(p.Url, "rtsps://") {
		// Video
		videoqueue, _ := gst.NewElementWithName("queue2", "videoqueue")
		bin.Add(videoqueue)
		// Create video queue ghost sink
		videoqueuesink := videoqueue.GetStaticPad("sink")
		if videoqueuesink == nil {
			return nil, errors.ErrUnableToAddPad
		}
		videoqueueghostsink := gst.NewGhostPad("sink", videoqueuesink)
		bin.AddPad(videoqueueghostsink.Pad)
		// Create video queue ghost source
		videoqueuesrc := videoqueue.GetStaticPad("src")
		if videoqueuesrc == nil {
			return nil, errors.ErrUnableToAddPad
		}
		videoqueueghostsrc := gst.NewGhostPad("src", videoqueuesrc)
		if !bin.AddPad(videoqueueghostsrc.Pad) {
			return nil, errors.ErrUnableToAddPad
		}

		// Audio
		audiobin := gst.NewBin("audioinput")
		audioqueue, _ := gst.NewElementWithName("queue2", "audioqueue")
		audiobin.Add(audioqueue)
		// Create audio queue ghost sink
		audioqueuesink := audioqueue.GetStaticPad("sink")
		if audioqueuesink == nil {
			return nil, errors.ErrUnableToAddPad
		}
		audioqueueghostsink := gst.NewGhostPad("sink", audioqueuesink)
		audiobin.AddPad(audioqueueghostsink.Pad)
		// Create audio queue ghost source
		audioqueuesrc := audioqueue.GetStaticPad("src")
		if audioqueuesrc == nil {
			return nil, errors.ErrUnableToAddPad
		}
		audioqueueghostsrc := gst.NewGhostPad("src", audioqueuesrc)
		if !audiobin.AddPad(audioqueueghostsrc.Pad) {
			return nil, errors.ErrUnableToAddPad
		}

		var mu sync.Mutex

		elem.Connect("pad-added", func(src *gst.Element, pad *gst.Pad) {

			padName := pad.GetName()

			logger.Infow("rtspsrc pad-added", "padName", padName)

			var queue *gst.Element
			var sinkPad *gst.Pad
			var getQueueErr error
			isAudio := false
			isVideo := false

			pad.GetCurrentCaps().ForEach(func(features *gst.CapsFeatures, structure *gst.Structure) bool {
				value, getMediaErr := structure.GetValue("media")
				if getMediaErr != nil {
					logger.Errorw("failed to get media value from caps", getMediaErr)
					return false
				}
				if value == "audio" {
					isAudio = true
					return true
				} else if value == "video" {
					isVideo = true
					return true
				} else {
					logger.Errorw("pad unrecognized media type", nil, "media", value)
					return false
				}
			})

			mu.Lock()
			defer mu.Unlock()

			if isAudio {
				sinkPad = audiobin.GetStaticPad("sink")
				queue, getQueueErr = audiobin.GetElementByName("audioqueue")
			} else if isVideo {
				sinkPad = bin.GetStaticPad("sink")
				queue, getQueueErr = bin.GetElementByName("videoqueue")
			} else {
				logger.Errorw("pad media type not audio nor video", nil, "padName", padName)
				return
			}

			if sinkPad == nil {
				logger.Errorw("failed to get sink pad", nil)
				return
			}
			if getQueueErr != nil {
				logger.Errorw("failed to get queue element", err)
				return
			}

			if sinkPad.IsLinked() {
				var kind string
				if isAudio {
					kind = "audio"
				} else {
					kind = "video"
				}
				logger.Warnw("sink pad of kind "+kind+" is already linked", nil)
				logger.Warnw("this usually means that the source is sending multiple streams of the same kind", nil)
				return
			}

			padLinkReturnValue := pad.Link(sinkPad)
			if padLinkReturnValue != gst.PadLinkOK && padLinkReturnValue != gst.PadLinkWasLinked {
				logger.Errorw("failed to link pad", nil, "padLinkReturnValue", padLinkReturnValue)
				return
			}
			queue.SyncStateWithParent()
		})

		return &URLSource{
			params:   p,
			src:      bin.Element,
			srcAudio: audiobin.Element,
			Rtspsrc:  elem,
		}, nil
	}
	// END OPENVIDU BLOCK

	queue, err := gst.NewElement("queue2")
	if err != nil {
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
		params: p,
		src:    bin.Element,
		pad:    pad,
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

func (s *URLSource) ValidateCaps(caps *gst.Caps) error {
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

func (u *URLSource) Start(ctx context.Context) error {
	return nil
}

func (u *URLSource) Close() error {
	// TODO find a way to send a EOS event without hanging

	return nil
}
