// BEGIN OPENVIDU BLOCK

package media

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWithOpusparseRank(t *testing.T) {
	v, ok := withOpusparseRank("")
	require.True(t, ok)
	require.Equal(t, "opusparse:MARGINAL", v)

	v, ok = withOpusparseRank("x264enc:MAX")
	require.True(t, ok, "other rankings are kept")
	require.Equal(t, "x264enc:MAX,opusparse:MARGINAL", v)

	_, ok = withOpusparseRank("opusparse:NONE")
	require.False(t, ok, "an explicit opusparse ranking is the operator's and must not be overridden")

	_, ok = withOpusparseRank("h264parse:MAX,opusparse:PRIMARY")
	require.False(t, ok)
}

// The package initializer has run by the time any test does: GStreamer, which
// every pipeline in this package initializes later, will see the ranking.
func TestOpusparseRankIsSetBeforeGstInit(t *testing.T) {
	require.Contains(t, os.Getenv(gstFeatureRankEnv), "opusparse:")
}

// END OPENVIDU BLOCK
