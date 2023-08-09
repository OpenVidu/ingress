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

	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/params"
)

type URLSource struct {
	params *params.Params
	src    *gst.Element
}

func NewURLSource(ctx context.Context, p *params.Params) (*URLSource, error) {
	bin := gst.NewBin("input")

	elem, err := gst.NewElement("curlhttpsrc")
	if err != nil {
		return nil, err
	}

	err = elem.SetProperty("location", p.Url)
	if err != nil {
		return nil, err
	}

	/*	queue, err := gst.NewElement("queue2")
		if err != nil {
			return nil, err
		}

		err = queue.SetProperty("use-buffering", true)
		if err != nil {
			return nil, err
		}

		err = bin.AddMany(elem, queue)*/
	err = bin.AddMany(elem)
	if err != nil {
		return nil, err
	}

	//	err = elem.Link(queue)
	//	if err != nil {
	//		return nil, err
	//	}

	pad := elem.GetStaticPad("src")
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
	}, nil
}

func (u *URLSource) GetSources() []*gst.Element {
	return []*gst.Element{
		u.src,
	}
}

func (u *URLSource) Start(ctx context.Context) error {
	return nil
}

func (u *URLSource) Close() error {
	// TODO find a way to send a EOS event without hanging

	return nil
}
