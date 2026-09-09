// Copyright 2026 LiveKit, Inc.
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

package stats

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/psrpc"

	"github.com/livekit/ingress/pkg/errors"
)

// BEGIN OPENVIDU BLOCK
// The RTMP and WHIP servers accept publishers before Service.Run starts the
// monitor. A request in that window must be refused, not crash the process
// (which restarted it into the same window while the publisher kept retrying).
func TestRequestsBeforeStartAreRefused(t *testing.T) {
	m := NewMonitor()
	info := &livekit.IngressInfo{
		IngressId: "IN_test",
		InputType: livekit.IngressInput_RTMP_INPUT,
	}

	require.NotPanics(t, func() {
		require.False(t, m.AcceptIngress(info))
		require.False(t, m.CanAcceptIngress(info))
		require.False(t, m.CanAccept())
		require.Zero(t, m.GetAvailableCPU())
	})
}

// END OPENVIDU BLOCK

func TestPublicationStatus(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		expected string
	}{
		{
			name:     "nil error is success",
			err:      nil,
			expected: "success",
		},
		{
			name:     "unstructured error is internal",
			err:      errors.New("boom"),
			expected: "internal",
		},
		{
			name:     "HTTPError 4xx is 4xx",
			err:      errors.NewHTTPError(http.StatusBadRequest, errors.New("bad request")),
			expected: "4xx",
		},
		{
			name:     "HTTPError 5xx is 5xx",
			err:      errors.NewHTTPError(http.StatusBadGateway, errors.New("upstream down")),
			expected: "5xx",
		},
		{
			name:     "HTTPError non-error status is internal",
			err:      errors.NewHTTPError(http.StatusOK, errors.New("weird")),
			expected: "internal",
		},
		{
			name:     "HTTPError 5xx wrapped in psrpc is still 5xx",
			err:      psrpc.NewError(psrpc.Internal, errors.NewHTTPError(http.StatusServiceUnavailable, errors.New("unavailable"))),
			expected: "5xx",
		},
		{
			name:     "psrpc 4xx code is 4xx",
			err:      psrpc.NewErrorf(psrpc.InvalidArgument, "bad input"),
			expected: "4xx",
		},
		{
			name:     "psrpc internal code is internal",
			err:      psrpc.NewErrorf(psrpc.Internal, "boom"),
			expected: "internal",
		},
		{
			name:     "psrpc 5xx code is internal, not 5xx",
			err:      psrpc.NewErrorf(psrpc.Unavailable, "unavailable"),
			expected: "internal",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.expected, publicationStatus(c.err))
		})
	}
}
