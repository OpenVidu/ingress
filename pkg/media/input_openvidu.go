// BEGIN OPENVIDU BLOCK

package media

import (
	"strings"

	"github.com/go-gst/go-gst/pkg/gst"

	"github.com/livekit/ingress/pkg/types"
)

// The RTSP path (and WHIP transcoding) builds one decodebin3 per stream. Their
// children share names ("typefind" inside each parsebin) or get process-global
// ones ("multiqueue0", "multiqueue1", ... in creation order), so a lookup by
// name from the input bin can return the element of the other stream. These
// helpers scope the lookup to the decodebin behind a given pad.

// decodeTypefind returns the typefind of the decodebin that exposed pad, or
// the bin-wide "typefind" when that decodebin has none.
func decodeTypefind(bin gst.Bin, pad gst.Pad) gst.Element {
	if tf := findDecodeElement(pad.GetParentElement(), "typefind"); tf != nil {
		return tf
	}
	return bin.GetByName("typefind")
}

// decodeMultiqueue returns the multiqueue of the decodebin that exposed the
// output pad of kind, or nil.
func (i *Input) decodeMultiqueue(kind types.StreamKind) gst.Element {
	i.lock.Lock()
	var pad gst.Pad
	switch kind {
	case types.Audio:
		pad = i.audioOutput
	case types.Video:
		pad = i.videoOutput
	}
	i.lock.Unlock()

	if pad == nil {
		return nil
	}
	return findDecodeElement(pad.GetParentElement(), "multiqueue")
}

// findDecodeElement returns the first element inside decodeBin, at any depth,
// whose name starts with prefix, or nil.
func findDecodeElement(decodeBin gst.Element, prefix string) gst.Element {
	bin, ok := decodeBin.(gst.Bin)
	if !ok {
		return nil
	}
	for v := range bin.IterateRecurse().Values() {
		if e, ok := v.(gst.Element); ok && strings.HasPrefix(e.GetName(), prefix) {
			return e
		}
	}
	return nil
}

// END OPENVIDU BLOCK
