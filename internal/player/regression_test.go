package player

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNV12AndBGRAOddDimensionsAndSmallStrideDoNotPanic(t *testing.T) {
	// Odd width/height NV12 with stride == width (previously panicked on uvRow[uvIdx+1]
	// for x = width-1 on the last UV row).
	for _, tc := range []struct {
		w, h, stride int
	}{
		{1, 1, 1},
		{5, 5, 5},
		{5, 5, 6},
		{7, 3, 7},
		{0, 0, 0},
	} {
		ySize := tc.stride * tc.h
		uvRows := (tc.h + 1) / 2
		buf := make([]byte, ySize+uvRows*tc.stride)
		for i := range buf {
			buf[i] = 128
		}
		img := NV12ToRGBA(buf, tc.w, tc.h, tc.stride, nil)
		if img.Bounds().Dx() != tc.w || img.Bounds().Dy() != tc.h {
			t.Fatalf("NV12ToRGBA(%dx%d, stride=%d) bounds = %v", tc.w, tc.h, tc.stride, img.Bounds())
		}
	}

	// BGRA32ToRGBA with absStride < width*4 must not panic.
	small := make([]byte, 8*4)
	img := BGRA32ToRGBA(small, 8, 4, 8, nil)
	if img.Bounds().Dx() != 8 || img.Bounds().Dy() != 4 {
		t.Fatalf("BGRA32ToRGBA small stride bounds = %v", img.Bounds())
	}
	imgNeg := BGRA32ToRGBA(small, 8, 4, -8, nil)
	if imgNeg.Bounds().Dx() != 8 || imgNeg.Bounds().Dy() != 4 {
		t.Fatalf("BGRA32ToRGBA negative small stride bounds = %v", imgNeg.Bounds())
	}
	// Zero dimensions must not panic on BGRA32ToRGBA / RGBA32Rows.
	_ = BGRA32ToRGBA(nil, 0, 0, 0, nil)
	_ = RGBA32Rows(nil, 0, 0, 0, nil)

	// contiguousStride for odd-height NV12 uses height + (height+1)/2 rows.
	const oddW, oddH, paddedStride = 7, 5, 16
	oddRows := oddH + (oddH+1)/2 // 8 rows
	if got := contiguousStride("nv12", oddW, oddH, oddW, paddedStride*oddRows); got != paddedStride {
		t.Fatalf("contiguousStride(nv12, %dx%d) = %d, want %d", oddW, oddH, got, paddedStride)
	}
}

func TestSubtitleOverlappingCuesNaNAndWebVTTFormatting(t *testing.T) {
	// Overlapping cues where EndSec is non-monotonic: [0, 10], [1, 2], [5, 8].
	raw := "\uFEFFWEBVTT\r\n   \r\n" +
		"00:00:00.000 --> 00:00:10.000\r\nLong cue\r\n\t \r\n" +
		"00:00:01.000 --> 00:00:02.000\r\nShort early\r\n\r\n" +
		"00:00:05.000 --> 00:00:08.000\r\nMiddle cue\r\n\r\n" +
		"00:00:NaN --> 00:00:09.000\r\nBad NaN start\r\n\r\n" +
		"00:00:01.000 --> 00:00:NaN\r\nBad NaN end\r\n\r\n" +
		"00:00:01.000 --> 00:00:+Inf\r\nBad Inf end\r\n\r\n" +
		"-00:00:01.000 --> 00:00:03.000\r\nBad neg start\r\n"

	cues := ParseWebVTT([]byte(raw))
	if len(cues) != 3 {
		t.Fatalf("expected 3 valid cues (rejecting NaN/Inf/neg), got %d: %+v", len(cues), cues)
	}
	wantAt6 := "Long cue\nMiddle cue"
	if got := ActiveCueAt(cues, 6.0); got != wantAt6 {
		t.Fatalf("ActiveCueAt(6.0) = %q, want %q", got, wantAt6)
	}
	if got := ActiveCueAt(cues, math.NaN()); got != "" {
		t.Fatalf("ActiveCueAt(NaN) = %q, want empty", got)
	}

	// Verify indexed SubtitleTrack inside Controller.Snapshot agrees.
	ctrl := NewController(Options{AudioSink: NewNullAudioSink(), SubtitleLang: "zh-CN"})
	defer ctrl.Close()
	if err := ctrl.LoadDecoder("sub.mp4", NewSynthDecoder(64, 36, 20*time.Second, 30)); err != nil {
		t.Fatal(err)
	}
	ctrl.SetSubtitles([]SubtitleTrack{{Lang: "zh-CN", Label: "中文", Cues: cues}})
	if err := ctrl.Seek(6.0); err != nil {
		t.Fatal(err)
	}
	if got := ctrl.Snapshot(640, 360).ActiveSubtitle; got != wantAt6 {
		t.Fatalf("Controller.Snapshot(640,360).ActiveSubtitle = %q, want %q", got, wantAt6)
	}
	// Snapshot(1, 1) skips overlay/subtitle layout for lightweight tick checks.
	tiny := ctrl.Snapshot(1, 1)
	if !tiny.Open || tiny.ActiveSubtitle != "" || len(tiny.SubtitleOptions) != 0 || len(tiny.VisibleDanmaku) != 0 {
		t.Fatalf("Snapshot(1, 1) should skip overlay computation, got %+v", tiny)
	}
}

func TestDanmakuRejectsNaNInfNegAndMode78AndScalesFontSize(t *testing.T) {
	xmlData := `<i>
		<d p="NaN,1,25,16777215,0,0,0,1">bad-nan</d>
		<d p="Inf,1,25,16777215,0,0,0,2">bad-inf</d>
		<d p="-1.5,1,25,16777215,0,0,0,3">bad-neg</d>
		<d p="1.0,7,25,16777215,0,0,0,4">[0.5,0.5,"advanced-json"]</d>
		<d p="1.5,8,25,16777215,0,0,0,5">code-danmaku</d>
		<d p="2.0,1,12,16777215,0,0,0,6">small</d>
		<d p="2.0,1,20,16777215,0,0,0,7">large</d>
	</i>`

	eng, err := ParseDanmakuXML([]byte(xmlData))
	if err != nil {
		t.Fatalf("ParseDanmakuXML: %v", err)
	}
	if eng.Count() != 2 {
		t.Fatalf("Count() = %d, want 2 (only valid mode 1 entries)", eng.Count())
	}
	vis := eng.LayoutFrame(3.0, 1280, 720, 0.5, 0.85)
	if len(vis) != 2 {
		t.Fatalf("visible = %d, want 2: %+v", len(vis), vis)
	}
	var smallW, largeW float32
	for _, dm := range vis {
		if strings.Contains(dm.Text, "advanced") || strings.Contains(dm.Text, "code") {
			t.Fatalf("unexpected mode 7/8 danmaku leaked into layout: %+v", dm)
		}
		if dm.Text == "small" {
			smallW = dm.Width
		}
		if dm.Text == "large" {
			largeW = dm.Width
		}
	}
	if largeW <= smallW {
		t.Fatalf("estimateTextWidth should scale with fontSize: small=%v large=%v", smallW, largeW)
	}
	// NewDanmakuEngine directly also filters NaN/Inf/neg.
	direct := NewDanmakuEngine([]DanmakuItem{
		{ID: "nan", TimeSec: math.NaN(), Mode: 1, Text: "x"},
		{ID: "neg", TimeSec: -2, Mode: 1, Text: "y"},
		{ID: "ok", TimeSec: 1, Mode: 1, Text: "ok"},
	})
	if direct.Count() != 1 {
		t.Fatalf("direct NewDanmakuEngine Count() = %d, want 1", direct.Count())
	}
}

// cachedFrameDecoder mimics MFDecoder returning the cached lastFrame (which
// aliases videoWorker.shown) on a small forward/in-place seek, or io.EOF at tail.
type cachedFrameDecoder struct {
	*SynthDecoder
	mu         sync.Mutex
	cached     *image.RGBA
	cachedPTS  time.Duration
	returnEOF  atomic.Bool
	decodeHook func()
}

func (d *cachedFrameDecoder) DecodeVideoFrameAt(pos time.Duration, dst *image.RGBA) (*image.RGBA, time.Duration, error) {
	if d.decodeHook != nil {
		d.decodeHook()
	}
	if d.returnEOF.Load() {
		d.mu.Lock()
		c, pts := d.cached, d.cachedPTS
		d.mu.Unlock()
		return c, pts, io.EOF
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cached != nil && d.cachedPTS >= pos {
		return d.cached, d.cachedPTS, nil
	}
	img, pts, err := d.SynthDecoder.DecodeVideoFrameAt(pos, dst)
	if err == nil && img != nil {
		d.cached = img
		d.cachedPTS = pts + 100*time.Millisecond
	}
	return img, d.cachedPTS, err
}

func TestShortSeekCachedFrameAndEOFReleaseHoldImmediately(t *testing.T) {
	dec := &cachedFrameDecoder{SynthDecoder: NewSynthDecoder(64, 36, 30*time.Second, 30)}
	sink := NewNullAudioSink()
	c := NewController(Options{AudioSink: sink})
	defer c.Close()

	if err := c.LoadDecoder("cache.mp4", dec); err != nil {
		t.Fatal(err)
	}
	if c.IsPlaying() {
		t.Fatal("IsPlaying() should be false before Play()")
	}
	c.Play()
	if !c.IsPlaying() {
		t.Fatal("IsPlaying() should be true after Play()")
	}
	// Let the worker publish and display the initial frame into w.shown.
	waitFor(t, "initial shown frame", func() bool {
		c.Advance(0)
		c.worker.mu.Lock()
		shown := c.worker.shown
		c.worker.mu.Unlock()
		return shown != nil
	})

	// Seek to a timestamp <= cachedPTS so DecodeVideoFrameAt returns the exact
	// buffer currently in w.shown. Audio must resume promptly (<< 1.5s seekHoldLimit).
	start := time.Now()
	if err := c.Seek(0.05); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "audio resumed after cached-frame short seek", sink.IsPlaying)
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Fatalf("short seek stalled audio for %v (want << %v)", took, seekHoldLimit)
	}

	// Seeking when decoder reports io.EOF must also release hold immediately.
	dec.returnEOF.Store(true)
	startEOF := time.Now()
	if err := c.Seek(29.95); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "audio resumed after EOF seek", sink.IsPlaying)
	if took := time.Since(startEOF); took > 500*time.Millisecond {
		t.Fatalf("EOF seek stalled audio for %v (want << %v)", took, seekHoldLimit)
	}
}

func TestControllerCloseSuppressesLateFrameNotify(t *testing.T) {
	inDecode := make(chan struct{})
	releaseDecode := make(chan struct{})
	var notifyAfterClose atomic.Int32
	var closedFlag atomic.Bool

	dec := &cachedFrameDecoder{
		SynthDecoder: NewSynthDecoder(64, 36, 30*time.Second, 30),
	}
	var hookOnce sync.Once
	c := NewController(Options{AudioSink: NewNullAudioSink()})
	if err := c.LoadDecoder("late.mp4", dec); err != nil {
		t.Fatal(err)
	}
	c.SetFrameNotify(func() {
		if closedFlag.Load() {
			notifyAfterClose.Add(1)
		}
	})
	dec.decodeHook = func() {
		hookOnce.Do(func() {
			close(inDecode)
			<-releaseDecode
		})
	}
	if err := c.Seek(5.0); err != nil {
		t.Fatal(err)
	}
	<-inDecode
	closedFlag.Store(true)
	_ = c.Close()
	close(releaseDecode)
	time.Sleep(50 * time.Millisecond)
	if n := notifyAfterClose.Load(); n != 0 {
		t.Fatalf("onFrame callback fired %d times after Close()", n)
	}
}

func makeRawMP4Box(boxType string, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out[0:4], uint32(len(out)))
	copy(out[4:8], boxType)
	copy(out[8:], payload)
	return out
}

func TestLiveFMP4SourceEmsgOrphanMoofAndSubscribeClosed(t *testing.T) {
	initSeg, frags := buildSyntheticGrowingFMP4Parts(t, true, 0, 0, 2)
	emsg := makeRawMP4Box("emsg", []byte{0, 0, 0, 0, 't', 'e', 's', 't'})
	orphanMoof := makeRawMP4Box("moof", []byte{0, 0, 0, 8, 'm', 'f', 'h', 'd'})

	// Inject an emsg between moof and mdat in fragment 0, plus an orphan moof before fragment 1.
	f0 := frags[0]
	moofSize := int(binary.BigEndian.Uint32(f0[0:4]))
	var combined bytes.Buffer
	combined.Write(initSeg)
	combined.Write(f0[:moofSize])
	combined.Write(emsg)
	combined.Write(f0[moofSize:])
	combined.Write(orphanMoof)
	combined.Write(frags[1])

	partPath := filepath.Join(t.TempDir(), "live.mp4.part")
	if err := os.WriteFile(partPath, combined.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	// Open with IsActive=false (already ended) — must keep src.file open and scan both fragments.
	src, err := openLiveFMP4Source(context.Background(), partPath, LivePreviewOptions{
		PollInterval: 10 * time.Millisecond,
		InitialWait:  2 * time.Second,
		IsActive:     func() bool { return false },
	})
	if err != nil {
		t.Fatalf("openLiveFMP4Source: %v", err)
	}
	if src.file == nil {
		t.Fatal("src.file was closed prematurely when !active with valid fragments")
	}
	if src.fragCount < 2 {
		t.Fatalf("fragCount = %d, want >= 2 (emsg between moof/mdat and orphan moof must not stall scan)", src.fragCount)
	}

	// Abort src and verify subscribe reports closed == true immediately.
	src.abort()
	_, _, closed := src.subscribe(func(int64, bool, bool) {})
	if !closed {
		t.Fatal("subscribe() on aborted source must report closed == true")
	}
	_ = src.Close()
}
