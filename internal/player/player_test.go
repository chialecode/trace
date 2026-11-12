package player

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/download"
)

// Pure-Go Windows Media Foundation IMFSourceReader decoding of a muxed MP4 file.
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

	// Decode 5 frames and verify *image.RGBA output has real decoded non-black pixels from Media Foundation.
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

	// Verify missing codec error helper.
	if !IsMissingCodecError(ErrMissingCodec) {
		t.Fatalf("expected IsMissingCodecError(ErrMissingCodec) == true")
	}

}

// 10,000 danmaku items single-frame layout < 1ms, seek from 30s to 120s leaves only [114s, 120s],
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

	// Measure 100 frame layout calls at t=150.0s and assert average < 1ms per frame.
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

	// Verify seek from 30s to 120s leaves only items in [114s, 120s] window (0 stale items from 30s).
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

	// 2. Controller with NullAudioSink + SynthDecoder + Danmaku + WebVTT Subtitles.
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

func buildSyntheticWAV(sampleRate, channels, numFrames int) []byte {
	bytesPerSample := 2
	dataBytes := numFrames * channels * bytesPerSample
	buf := make([]byte, 44+dataBytes)
	copy(buf[0:4], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:8], uint32(36+dataBytes))
	copy(buf[8:12], "WAVE")
	copy(buf[12:16], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:20], 16)
	binary.LittleEndian.PutUint16(buf[20:22], 1) // PCM
	binary.LittleEndian.PutUint16(buf[22:24], uint16(channels))
	binary.LittleEndian.PutUint32(buf[24:28], uint32(sampleRate))
	binary.LittleEndian.PutUint32(buf[28:32], uint32(sampleRate*channels*bytesPerSample))
	binary.LittleEndian.PutUint16(buf[32:34], uint16(channels*bytesPerSample))
	binary.LittleEndian.PutUint16(buf[34:36], 16)
	copy(buf[36:40], "data")
	binary.LittleEndian.PutUint32(buf[40:44], uint32(dataBytes))
	off := 44
	for f := 0; f < numFrames; f++ {
		s := int16(4096 * math.Sin(2*math.Pi*440*float64(f)/float64(sampleRate)))
		for c := 0; c < channels; c++ {
			binary.LittleEndian.PutUint16(buf[off:off+2], uint16(s))
			off += 2
		}
	}
	return buf
}

func buildSyntheticSilentMP3(numFrames int) []byte {
	// Valid CBR MPEG-1 Layer III 48 kHz 128 kbps Mono frame:
	// frame length = floor(144 * 128000 / 48000) + 0 = 384 bytes;
	// zeroed side_info (main_data_begin=0, part2_3_length=0) decodes to silence.
	const frameBytes = 384
	out := make([]byte, numFrames*frameBytes)
	for i := 0; i < numFrames; i++ {
		off := i * frameBytes
		out[off+0] = 0xFF
		out[off+1] = 0xFB // MPEG-1, Layer III, no CRC
		out[off+2] = 0x94 // 128 kbps, 48000 Hz, no padding
		out[off+3] = 0xC0 // Mono
	}
	return out
}

func TestPureAudioControllerClockSeekPreviewVolumeAndEOF(t *testing.T) {
	now := time.Date(2026, 11, 13, 1, 0, 0, 0, time.UTC)
	sink := NewNullAudioSink()
	sink.NowFunc = func() time.Time { return now }

	ctrl := NewController(Options{
		AudioSink: sink,
		Volume:    0.7,
		Muted:     false,
	})
	defer ctrl.Close()

	audioDec := NewSynthAudioDecoder(4*time.Second, 48000, 2)
	if err := ctrl.LoadDecoder("music.m4a", audioDec); err != nil {
		t.Fatalf("LoadDecoder(audio-only): %v", err)
	}
	if ctrl.HasVideo() {
		t.Fatalf("expected HasVideo() == false for audio-only decoder")
	}
	if ctrl.Bitmap() != nil {
		t.Fatalf("expected nil Bitmap() for audio-only decoder")
	}

	snap0 := ctrl.Snapshot(640, 120)
	if !snap0.Open || snap0.HasVideo || snap0.Width != 0 || snap0.Height != 0 || snap0.DurationSec != 4.0 {
		t.Fatalf("unexpected initial audio-only snapshot: %+v", snap0)
	}

	// Play & advance audio clock without video worker.
	ctrl.Play()
	if !sink.IsPlaying() {
		t.Fatalf("expected sink playing after Play()")
	}
	now = now.Add(600 * time.Millisecond)
	ctrl.Advance(0)
	snapPlay := ctrl.Snapshot(640, 120)
	if !snapPlay.Playing || math.Abs(snapPlay.PositionSec-0.6) > 0.02 {
		t.Fatalf("expected PositionSec ~0.6s while playing, got %+v", snapPlay)
	}

	// Seek while playing must NOT hold/pause audio waiting for a video frame.
	if err := ctrl.Seek(1.5); err != nil {
		t.Fatalf("Seek(1.5): %v", err)
	}
	if !sink.IsPlaying() {
		t.Fatalf("audio-only Seek paused sink waiting for non-existent video frame")
	}
	now = now.Add(250 * time.Millisecond)
	ctrl.Advance(0)
	if got := ctrl.Snapshot(640, 120).PositionSec; math.Abs(got-1.75) > 0.02 {
		t.Fatalf("expected PositionSec ~1.75s after seek+advance, got %.3f", got)
	}

	// Scrubbing Preview pauses audio, then Seek commits and resumes immediately.
	ctrl.Preview(0.4)
	if !ctrl.Scrubbing() || sink.IsPlaying() {
		t.Fatalf("expected scrubbing=true and sink paused during Preview")
	}
	if err := ctrl.Seek(0.8); err != nil {
		t.Fatalf("Seek(0.8) after Preview: %v", err)
	}
	if ctrl.Scrubbing() || !sink.IsPlaying() {
		t.Fatalf("expected scrubbing=false and sink resumed after Seek")
	}

	// Volume & Mute controls.
	ctrl.SetVolume(0.35)
	if math.Abs(sink.volume-0.35) > 1e-6 {
		t.Fatalf("expected sink volume 0.35, got %f", sink.volume)
	}
	ctrl.SetMuted(true)
	if sink.volume != 0 {
		t.Fatalf("expected sink volume 0 when muted, got %f", sink.volume)
	}
	ctrl.SetMuted(false)
	if math.Abs(sink.volume-0.35) > 1e-6 {
		t.Fatalf("expected sink volume restored to 0.35 after unmute, got %f", sink.volume)
	}

	// Advancing to EOF pauses playback automatically.
	now = now.Add(5 * time.Second)
	ctrl.Advance(0)
	snapEnd := ctrl.Snapshot(640, 120)
	if snapEnd.Playing || snapEnd.PositionSec != 4.0 || sink.IsPlaying() {
		t.Fatalf("expected auto-pause at 4.0s EOF, got %+v sinkPlaying=%v", snapEnd, sink.IsPlaying())
	}

	// Also verify cross-platform pureAudioFileDecoder on a synthetic WAV file.
	wavPath := filepath.Join(t.TempDir(), "synthetic.wav")
	if err := writeTestFile(wavPath, buildSyntheticWAV(48000, 2, 24000)); err != nil {
		t.Fatalf("write synthetic.wav: %v", err)
	}
	wavDec, err := openPureAudioFile(wavPath)
	if err != nil {
		t.Fatalf("openPureAudioFile: %v", err)
	}
	defer wavDec.Close()
	if decoderHasVideo(wavDec) || wavDec.SampleRate() != 48000 || wavDec.Channels() != 2 {
		t.Fatalf("unexpected wavDec metadata: hasVideo=%v sr=%d ch=%d", decoderHasVideo(wavDec), wavDec.SampleRate(), wavDec.Channels())
	}
	pcm := make([]float32, 512)
	if n, err := wavDec.ReadAudioSamples(pcm); err != nil || n == 0 {
		t.Fatalf("wavDec ReadAudioSamples: n=%d err=%v", n, err)
	}
	if err := wavDec.Seek(200 * time.Millisecond); err != nil {
		t.Fatalf("wavDec Seek: %v", err)
	}
	if runtime.GOOS != "windows" {
		otherDec, err := OpenMediaFile(wavPath)
		if err != nil {
			t.Fatalf("OpenMediaFile(synthetic.wav) on non-Windows: %v", err)
		}
		defer otherDec.Close()
		if decoderHasVideo(otherDec) {
			t.Fatalf("expected HasVideo=false on non-Windows OpenMediaFile(wav)")
		}
	}
}
