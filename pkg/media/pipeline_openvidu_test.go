// BEGIN OPENVIDU BLOCK

// Tests for the OpenVidu fix to the notify::caps race in onOutputReady.
// Entirely OpenVidu's, so it carries no OPENVIDU BLOCK fences.

package media

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/livekit/ingress/pkg/types"
)

// The caps of a pad reach onParamsReady from two places now: the notify::caps
// handler and the direct call onOutputReady makes when they were already set.
// Both can fire for the same pad, and only one of them may build the track.
func TestClaimParamsLetsOnlyTheFirstCallerThrough(t *testing.T) {
	p := &Pipeline{}

	require.True(t, p.claimParams(types.Video), "the first caller builds the track")
	require.False(t, p.claimParams(types.Video), "the second one must not")
	require.False(t, p.claimParams(types.Video))

	// Kinds are claimed independently: a video track already built must not
	// keep the audio one from being built.
	require.True(t, p.claimParams(types.Audio))
	require.False(t, p.claimParams(types.Audio))
}

// The two paths run on GStreamer streaming threads, so the claim has to hold
// when they land at the same time.
func TestClaimParamsIsSafeUnderConcurrency(t *testing.T) {
	const callers = 64

	for _, kind := range []types.StreamKind{types.Audio, types.Video} {
		p := &Pipeline{}
		var wg sync.WaitGroup
		var mu sync.Mutex
		granted := 0

		wg.Add(callers)
		for range callers {
			go func() {
				defer wg.Done()
				if p.claimParams(kind) {
					mu.Lock()
					granted++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()

		require.Equal(t, 1, granted, "exactly one caller may build the %s track", kind)
	}
}

// An unexpected kind is never blocked: the claim exists to stop a double build,
// not to filter what reaches onParamsReady.
func TestClaimParamsDoesNotBlockOtherKinds(t *testing.T) {
	p := &Pipeline{}
	require.True(t, p.claimParams(types.Interleaved))
	require.True(t, p.claimParams(types.Unknown))
}

// END OPENVIDU BLOCK
