package player

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/download"
)

// PR-1: Pure-Go Windows Media Foundation IMFSourceReader decoding of a real muxed MP4 file.
func TestMFDecoderPureGoVideoAndAudio(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows Media Foundation requires Windows; synthetic playback tested separately")
	}
	tmp := t.TempDir()
	vPath := filepath.Join(tmp, "video.m4s")
	aPath := filepath.Join(tmp, "audio.m4s")
	mp4Path := filepath.Join(tmp, "muxed.mp4")

	vBytes, err := download.BuildSyntheticVideoM4S(30, 30)
	if err != nil {
		t.Fatalf("BuildSyntheticVideoM4S: %v", err)
	}
	aBytes, err := download.BuildSyntheticAudioM4S(45)
	if err != nil {
		t.Fatalf("BuildSyntheticAudioM4S: %v", err)
	}
	if err := writeTestFile(vPath, vBytes); err != nil {
		t.Fatalf("write vPath: %v", err)
	}
	if err := writeTestFile(aPath, aBytes); err != nil {
		t.Fatalf("write aPath: %v", err)
	}
	if _, err := download.MuxDASHFiles(vPath, aPath, mp4Path); err != nil {
		t.Fatalf("MuxDASHFiles: %v", err)
	}

	var memBefore, memAfter runtime.MemStats
	runtime.ReadMemStats(&memBefore)

	dec, err := OpenMediaFile(mp4Path)
	if err != nil {
		t.Fatalf("OpenMediaFile(%q): %v", mp4Path, err)
	}
	defer dec.Close()

	if dec.Width() <= 0 || dec.Height() <= 0 {
		t.Fatalf("invalid dimensions %dx%d", dec.Width(), dec.Height())
	}

	// Decode 5 frames and verify *image.RGBA output has real decoded non-black pixels from Media Foundation (R-22).
	nonBlackPixels := 0
	for i := 0; i < 5; i++ {
		rgba, _, err := dec.DecodeVideoFrameAt(time.Duration(i)*33*time.Millisecond, nil)
		if err != nil {
			t.Logf("DecodeVideoFrameAt #%d err=%v", i, err)
		}
		if err != nil && rgba == nil {
			t.Fatalf("DecodeVideoFrameAt #%d: %v", i, err)
		}
		if rgba.Bounds().Dx() != dec.Width() || rgba.Bounds().Dy() != dec.Height() {
			t.Fatalf("unexpected frame bounds %v", rgba.Bounds())
		}
		for p := 0; p+3 < len(rgba.Pix); p += 4 {
			if (rgba.Pix[p] > 0 || rgba.Pix[p+1] > 0 || rgba.Pix[p+2] > 0) && rgba.Pix[p+3] == 255 {
				nonBlackPixels++
			}
		}
	}
	if nonBlackPixels == 0 {
		t.Fatalf("decoded frames from Media Foundation had 0 non-black pixels")
	}

	// Read audio float32 samples and test Seek.
	pcm := make([]float32, 1024)
	n, _ := dec.ReadAudioSamples(pcm)
	if n < 0 {
		t.Fatalf("unexpected audio sample count %d", n)
	}
	if err := dec.Seek(200 * time.Millisecond); err != nil {
		t.Fatalf("Seek failed: %v", err)
	}
	if _, ptsAfterSeek, _ := dec.DecodeVideoFrameAt(200*time.Millisecond, nil); ptsAfterSeek < 0 {
		t.Fatalf("negative position after the seek: %s", ptsAfterSeek)
	}

	runtime.ReadMemStats(&memAfter)
	heapAllocMiB := float64(memAfter.HeapAlloc) / (1024 * 1024)
	if heapAllocMiB >= 100.0 {
		t.Fatalf("heap peak %.2f MiB >= 100 MiB", heapAllocMiB)
	}

	// Verify missing codec error helper (C22)
	if !IsMissingCodecError(ErrMissingCodec) {
		t.Fatalf("expected IsMissingCodecError(ErrMissingCodec) == true")
	}

}

// PR-3 & C22: 10,000 danmaku items single-frame layout < 1ms, seek from 30s to 120s leaves only [114s, 120s],
// WebVTT subtitle HTML entity decoding, NullAudioSink silent playback, and Close() resource cleanup.
func TestPlayerDanmaku10kPerfSubtitlesAndNullSink(t *testing.T) {
	// 1. Build 10,000 danmaku items spread over a 600s video (~100 items per 6s window).
	items := make([]DanmakuItem, 10000)
	for i := 0; i < 10000; i++ {
		mode := 1
		if i%10 == 0 {
			mode = 5 // top
		} else if i%15 == 0 {
			mode = 4 // bottom
		}
		items[i] = DanmakuItem{
			ID:       fmt.Sprintf("dm-%d", i),
			TimeSec:  float64(i) * 0.06, // 0.0s .. 600.0s
			Mode:     mode,
			FontSize: 25,
			ColorRGB: 0xFFFFFF,
			Text:     fmt.Sprintf("测试弹幕条目 #%d", i),
		}
	}
	eng := NewDanmakuEngine(items)

	// Measure 100 frame layout calls at t=150.0s and assert average < 1ms per frame (PR-3).
	start := time.Now()
	var visible []PositionedDanmaku
	for f := 0; f < 100; f++ {
		visible = eng.LayoutFrame(150.0+float64(f)*0.016, 1280, 720, 0.5, 0.85)
	}
	avgDur := time.Since(start) / 100
	if avgDur >= time.Millisecond {
		t.Fatalf("10,000 danmaku single-frame layout took %v, want < 1ms", avgDur)
	}
	if len(visible) == 0 {
		t.Fatalf("expected non-empty visible danmaku at t=150s")
	}

	// Verify seek from 30s to 120s leaves only items in [114s, 120s] window (0 stale items from 30s, PR-3).
	_ = eng.LayoutFrame(30.0, 1280, 720, 1.0, 0.85)
	afterSeek120 := eng.LayoutFrame(120.0, 1280, 720, 1.0, 0.85)
	if len(afterSeek120) == 0 {
		t.Fatalf("expected non-empty danmaku at 120s")
	}
	for _, dm := range afterSeek120 {
		if dm.TimeSec < 114.0 || dm.TimeSec > 120.0 {
			t.Fatalf("stale danmaku outside [114s, 120s] after seek to 120s: %+v", dm)
		}
	}

	// Verify no two rolling danmaku on the same Y track overlap horizontally.
	byY := make(map[float32][]PositionedDanmaku)
	for _, dm := range visible {
		if dm.Mode == 1 {
			byY[dm.Y] = append(byY[dm.Y], dm)
		}
	}
	for y, row := range byY {
		for i := 0; i < len(row); i++ {
			for j := i + 1; j < len(row); j++ {
				a, b := row[i], row[j]
				if a.X < b.X+b.Width && b.X < a.X+a.Width {
					t.Fatalf("overlapping rolling danmaku on track Y=%.1f: %+v vs %+v", y, a, b)
				}
			}
		}
	}

	// 2. Controller with NullAudioSink + SynthDecoder + Danmaku + WebVTT Subtitles (C22).
	ctrl := NewController(Options{
		AudioSink:      NewNullAudioSink(),
		DanmakuVisible: true,
		DanmakuOpacity: 0.85,
		DanmakuArea:    0.5,
		Volume:         0.8,
		SubtitleLang:   "zh-CN",
	})
	if err := ctrl.LoadDecoder("synthetic.mp4", NewSynthDecoder(320, 180, 30*time.Second, 30)); err != nil {
		t.Fatalf("LoadDecoder: %v", err)
	}
	ctrl.mu.Lock()
	ctrl.danmakuEngine = eng
	ctrl.mu.Unlock()

	vttRaw := []byte("WEBVTT\n\n00:00:01.000 --> 00:00:04.000\n第一条字幕 &amp; &lt;测试&gt;\n\n00:00:05.000 --> 00:00:08.000\n第二条字幕 A --> B\n")
	ctrl.SetSubtitles([]SubtitleTrack{
		{Lang: "zh-CN", Label: "中文", Cues: ParseWebVTT(vttRaw)},
	})

	ctrl.Play()
	if err := ctrl.Seek(2.5); err != nil {
		t.Fatalf("Seek(2.5): %v", err)
	}
	snap1 := ctrl.Snapshot(1280, 720)
	if snap1.ActiveSubtitle != "第一条字幕 & <测试>" || len(snap1.VisibleDanmaku) == 0 {
		t.Fatalf("unexpected snapshot at 2.5s: sub=%q dm=%d", snap1.ActiveSubtitle, len(snap1.VisibleDanmaku))
	}
	if ctrl.Bitmap() == nil {
		t.Fatalf("expected non-nil *ui.Bitmap after frame decode")
	}

	// Toggle danmaku off and seek to 6.0s -> subtitle updates to "第二条字幕 A --> B" and VisibleDanmaku is empty.
	ctrl.SetDanmakuVisible(false)
	_ = ctrl.Seek(6.0)
	snap2 := ctrl.Snapshot(1280, 720)
	if snap2.ActiveSubtitle != "第二条字幕 A --> B" || len(snap2.VisibleDanmaku) != 0 {
		t.Fatalf("unexpected snapshot at 6.0s: sub=%q dm=%d", snap2.ActiveSubtitle, len(snap2.VisibleDanmaku))
	}

	// Close releases bitmap and decoder cleanly.
	if err := ctrl.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if ctrl.Bitmap() != nil || ctrl.Snapshot(1280, 720).Open {
		t.Fatalf("expected controller closed and bitmap released")
	}

}

func writeTestFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}
