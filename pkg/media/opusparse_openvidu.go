// BEGIN OPENVIDU BLOCK

package media

import (
	"os"
	"strings"
)

// gstFeatureRankEnv is read by GStreamer when it initializes, so it has to be
// set before gst.Init: a package initializer is early enough.
const gstFeatureRankEnv = "GST_PLUGIN_FEATURE_RANK"

// opusparseRank gives opusparse the lowest rank parsebin autoplugs.
//
// opusparse ships with rank NONE, so parsebin never inserts it and exposes Opus
// streams unparsed. With MPEG-TS that trips a decodebin3 race: tsdemux pushes
// its first Opus buffer into the freshly exposed pad before decodebin3 has
// linked that pad to its multiqueue, the push returns not-linked and the source
// fails with "Internal data stream error". An MPEG-TS pull (SRT, UDP) carrying
// only an Opus track therefore never publishes; with a video track next to it
// the ordering happens to work, which is how it went unnoticed. Every other
// codec we ingest has a ranked parser (aacparse, mpegaudioparse, ac3parse,
// h264parse, ...) and takes the working path; this puts Opus on that same path.
// Seen with GStreamer 1.26.7 and reproduced with "filesrc ! decodebin3" on an
// Opus-only .ts, which "filesrc ! decodebin" plays fine.
const opusparseRank = "opusparse:MARGINAL"

func init() {
	if v, ok := withOpusparseRank(os.Getenv(gstFeatureRankEnv)); ok {
		os.Setenv(gstFeatureRankEnv, v)
	}
}

// withOpusparseRank returns the GST_PLUGIN_FEATURE_RANK value to set given the
// current one, or false when it must be left alone: a value that already ranks
// opusparse is the operator's decision and wins.
func withOpusparseRank(current string) (string, bool) {
	if strings.Contains(current, "opusparse") {
		return "", false
	}
	if current == "" {
		return opusparseRank, true
	}
	return current + "," + opusparseRank, true
}

// END OPENVIDU BLOCK
