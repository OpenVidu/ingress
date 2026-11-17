// BEGIN OPENVIDU BLOCK

package media

import (
	"strings"
	"testing"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/stretchr/testify/require"

	"github.com/livekit/ingress/pkg/gstutil"
	"github.com/livekit/ingress/pkg/types"
)

const testDecodePrefix = "decode-"

// decodeBinOf returns the name of the test decodebin that e sits in, or "".
func decodeBinOf(e gst.Element) string {
	if e == nil {
		return ""
	}
	var o gst.Object = e
	for depth := 0; depth < 16 && o != nil; depth++ {
		if name := o.GetName(); strings.HasPrefix(name, testDecodePrefix) {
			return name
		}
		o = o.GetParent()
	}
	return ""
}

// newTestDecodeBin builds a bin holding one identity per child name and
// returns it with a ghost of the first child's src pad, like the pad a
// decodebin exposes.
func newTestDecodeBin(t *testing.T, name string, children ...string) (gst.Bin, gst.Pad) {
	bin := gstutil.NewBin(name)
	var first gst.Element
	for _, child := range children {
		e, err := gstutil.NewElementWithName("identity", child)
		require.NoError(t, err)
		require.NoError(t, gstutil.Add(bin, e))
		if first == nil {
			first = e
		}
	}
	pad := gst.NewGhostPad("src", first.GetStaticPad("src"))
	require.True(t, bin.AddPad(pad))
	return bin, pad
}

// newTestElement adds an identity called name to bin and returns it.
func newTestElement(t *testing.T, bin gst.Bin, name string) gst.Element {
	e, err := gstutil.NewElementWithName("identity", name)
	require.NoError(t, err)
	require.NoError(t, gstutil.Add(bin, e))
	return e
}

// Two real decodebin3 in one bin, as an RTSP pull has: each pad must resolve
// the typefind and the multiqueue of its own decodebin.
func TestDecodeLookupsWithOneDecodebinPerStream(t *testing.T) {
	gst.Init()

	const audioBin, videoBin = testDecodePrefix + "audio", testDecodePrefix + "video"
	p, err := gstutil.ParseLaunch(
		"audiotestsrc num-buffers=200 ! wavenc ! queue2 ! decodebin3 name=" + audioBin +
			" videotestsrc num-buffers=200 ! jpegenc ! matroskamux ! queue2 ! decodebin3 name=" + videoBin)
	require.NoError(t, err)
	defer p.SetState(gst.StateNull)

	type exposed struct {
		decodeBin string
		pad       gst.Pad
		typefind  string
	}
	pads := make(chan exposed, 8)
	onPadAdded := func(e gst.Element, pad gst.Pad) {
		// Looked up here, as Input.onPadAdded does.
		typefind := decodeBinOf(decodeTypefind(p, pad))
		// Keep the stream flowing so that the other decodebin exposes its pad.
		if sink, err := gstutil.NewElement("fakesink"); err == nil {
			_ = gstutil.SetProperty(sink, "async", false)
			_ = gstutil.Add(p, sink)
			sink.SyncStateWithParent()
			pad.Link(sink.GetStaticPad("sink"))
		}
		pads <- exposed{decodeBin: e.GetName(), pad: pad, typefind: typefind}
	}
	p.GetByName(audioBin).Connect("pad-added", onPadAdded)
	p.GetByName(videoBin).Connect("pad-added", onPadAdded)

	p.SetState(gst.StatePlaying)

	in := &Input{bin: p}
	timeout := time.After(20 * time.Second)
	for in.audioOutput == nil || in.videoOutput == nil {
		select {
		case e := <-pads:
			require.Equal(t, e.decodeBin, e.typefind, "typefind of the pad's own decodebin")
			if e.decodeBin == audioBin {
				in.audioOutput = e.pad
			} else {
				in.videoOutput = e.pad
			}
		case <-timeout:
			t.Fatal("timed out waiting for the pads of both decodebins")
		}
	}

	require.Equal(t, audioBin, decodeBinOf(in.decodeMultiqueue(types.Audio)))
	require.Equal(t, videoBin, decodeBinOf(in.decodeMultiqueue(types.Video)))
}

// The typefind of the decodebin behind the pad wins over a bin-wide one of
// the same name.
func TestDecodeTypefindPrefersThePadsDecodebin(t *testing.T) {
	gst.Init()

	// The pad's decodebin is neither the first nor the last one added, so a
	// bin-wide lookup finds another one's typefind whatever its order.
	input := gstutil.NewBin("input")
	before, _ := newTestDecodeBin(t, testDecodePrefix+"before", "typefind")
	own, pad := newTestDecodeBin(t, testDecodePrefix+"own", "parser", "typefind")
	after, _ := newTestDecodeBin(t, testDecodePrefix+"after", "typefind")
	require.NoError(t, gstutil.AddMany(input, before, own, after))
	require.NotEqual(t, own.GetName(), decodeBinOf(input.GetByName("typefind")))

	typefind := decodeTypefind(input, pad)
	require.NotNil(t, typefind)
	require.Equal(t, "typefind", typefind.GetName())
	require.Equal(t, own.GetName(), decodeBinOf(typefind))
}

// Without a typefind behind the pad, the lookup is upstream's bin-wide one.
func TestDecodeTypefindFallsBackToTheBin(t *testing.T) {
	gst.Init()

	// The pad's parent is a bin with no typefind inside.
	input := gstutil.NewBin("input")
	own, pad := newTestDecodeBin(t, testDecodePrefix+"own", "parser")
	require.NoError(t, gstutil.Add(input, own))
	require.Nil(t, decodeTypefind(input, pad), "no typefind anywhere")

	newTestElement(t, input, "typefind")
	typefind := decodeTypefind(input, pad)
	require.NotNil(t, typefind)
	require.Equal(t, "typefind", typefind.GetName())
	require.Equal(t, "", decodeBinOf(typefind), "the bin-wide typefind")

	// The pad's parent is not a bin at all.
	plain := newTestElement(t, input, "plain")
	typefind = decodeTypefind(input, plain.GetStaticPad("src"))
	require.NotNil(t, typefind)
	require.Equal(t, "typefind", typefind.GetName())
}

// Each kind gets the multiqueue of the decodebin behind its own output pad.
func TestDecodeMultiqueueIsTheOneOfTheKindsDecodebin(t *testing.T) {
	gst.Init()

	input := gstutil.NewBin("input")
	audio, audioPad := newTestDecodeBin(t, testDecodePrefix+"audio", "parser", "multiqueue1")
	video, videoPad := newTestDecodeBin(t, testDecodePrefix+"video", "parser", "multiqueue0")
	require.NoError(t, gstutil.AddMany(input, audio, video))

	in := &Input{bin: input, audioOutput: audioPad, videoOutput: videoPad}

	mq := in.decodeMultiqueue(types.Audio)
	require.NotNil(t, mq)
	require.Equal(t, "multiqueue1", mq.GetName())

	mq = in.decodeMultiqueue(types.Video)
	require.NotNil(t, mq)
	require.Equal(t, "multiqueue0", mq.GetName())
}

// Nothing to return: the caller then keeps upstream's "multiqueue0".
func TestDecodeMultiqueueWithoutOne(t *testing.T) {
	gst.Init()

	input := gstutil.NewBin("input")
	in := &Input{bin: input}

	// No output pad yet, and a kind that never has one.
	require.Nil(t, in.decodeMultiqueue(types.Audio))
	require.Nil(t, in.decodeMultiqueue(types.Video))
	require.Nil(t, in.decodeMultiqueue(types.Unknown))

	// A decodebin without a multiqueue.
	own, pad := newTestDecodeBin(t, testDecodePrefix+"own", "parser")
	require.NoError(t, gstutil.Add(input, own))
	in.audioOutput = pad
	require.Nil(t, in.decodeMultiqueue(types.Audio))

	// A pad whose parent is not a bin.
	in.videoOutput = newTestElement(t, input, "plain").GetStaticPad("src")
	require.Nil(t, in.decodeMultiqueue(types.Video))
}

func TestFindDecodeElement(t *testing.T) {
	gst.Init()

	outer := gstutil.NewBin(testDecodePrefix + "outer")
	inner := gstutil.NewBin("inner")
	require.NoError(t, gstutil.Add(outer, inner))
	newTestElement(t, inner, "multiqueue7")

	// Found by name prefix, at any depth.
	found := findDecodeElement(outer, "multiqueue")
	require.NotNil(t, found)
	require.Equal(t, "multiqueue7", found.GetName())

	require.Nil(t, findDecodeElement(outer, "typefind"), "no element with that prefix")
	require.Nil(t, findDecodeElement(found, "multiqueue"), "not a bin")
	require.Nil(t, findDecodeElement(nil, "multiqueue"), "no element")
}

// END OPENVIDU BLOCK
