//go:build windows

package player

import (
	"image"
	"path/filepath"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/download"
)

// The GPU/RGBA reader path must show the same picture as the original
// software BGRA path (no swapped channels, no transparency) and must only
// select the stream it decodes.
func TestDecodePathsAgreeOnPixels(t *testing.T) {
	mp4Path := syntheticMuxedMP4(t)

	decode := func(modes []readerMode) (*image.RGBA, string) {
		t.Helper()
		saved := videoReaderModes
		videoReaderModes = modes
		defer func() { videoReaderModes = saved }()
		dec, err := openMFReader(mp4Path, true)
		if err != nil {
			t.Skipf("Media Foundation reader unavailable for %v: %v", modes[0].name, err)
		}
		defer dec.Close()
		if !dec.hasAudio {
			t.Fatal("video reader did not report the audio stream")
		}
		rgba, _, err := dec.DecodeVideoFrameAt(500*time.Millisecond, nil)
		if rgba == nil {
			t.Fatalf("%s decode: %v", dec.DecodePath(), err)
		}
		return rgba, dec.DecodePath()
	}

	ref, refPath := decode([]readerMode{{name: "software"}})
	if refPath != "software/rgb32" {
		t.Fatalf("reference path = %s, want software/rgb32", refPath)
	}
	got, gotPath := decode(videoReaderModes)
	t.Logf("default path %s, reference %s", gotPath, refPath)
	if got.Bounds() != ref.Bounds() {
		t.Fatalf("%s bounds %v differ from reference %v", gotPath, got.Bounds(), ref.Bounds())
	}
	var sum [3]int
	n := 0
	for i := 0; i+3 < len(got.Pix); i += 4 {
		if got.Pix[i+3] != 255 {
			t.Fatalf("%s produced a transparent pixel at %d", gotPath, i/4)
		}
		for c := 0; c < 3; c++ {
			d := int(got.Pix[i+c]) - int(ref.Pix[i+c])
			if d < 0 {
				d = -d
			}
			sum[c] += d
		}
		n++
	}
	var gm, rm [3]float64
	for i := 0; i+3 < len(got.Pix); i += 4 {
		for c := 0; c < 3; c++ {
			gm[c] += float64(got.Pix[i+c]) / float64(n)
			rm[c] += float64(ref.Pix[i+c]) / float64(n)
		}
	}
	t.Logf("mean RGB %s=%.1f/%.1f/%.1f reference=%.1f/%.1f/%.1f; first pixel %v vs %v", gotPath, gm[0], gm[1], gm[2], rm[0], rm[1], rm[2], got.Pix[:4], ref.Pix[:4])
	for c, name := range []string{"R", "G", "B"} {
		if mean := float64(sum[c]) / float64(n); mean > 6 {
			t.Fatalf("%s channel %s differs from the software path by %.1f on average", gotPath, name, mean)
		}
	}
}

func TestDecodeSizeFitsWindowAndKeepsAspect(t *testing.T) {
	for _, tc := range []struct{ w, h, tw, th int }{
		{3840, 2160, 1920, 1080},
		{2160, 3840, 1080, 1920},
		{1920, 1080, 1920, 1080},
		{1080, 1920, 1080, 1920},
		{640, 360, 640, 360},
		{4096, 1716, 1920, 804},
	} {
		if w, h := decodeSize(tc.w, tc.h); w != tc.tw || h != tc.th {
			t.Fatalf("decodeSize(%d, %d) = %d×%d, want %d×%d", tc.w, tc.h, w, h, tc.tw, tc.th)
		}
	}
}

func syntheticMuxedMP4(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	vPath := filepath.Join(tmp, "video.m4s")
	aPath := filepath.Join(tmp, "audio.m4s")
	mp4Path := filepath.Join(tmp, "muxed.mp4")
	vBytes, err := download.BuildSyntheticVideoM4S(30, 30)
	if err != nil {
		t.Fatal(err)
	}
	aBytes, err := download.BuildSyntheticAudioM4S(45)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeTestFile(vPath, vBytes); err != nil {
		t.Fatal(err)
	}
	if err := writeTestFile(aPath, aBytes); err != nil {
		t.Fatal(err)
	}
	if _, err := download.MuxDASHFiles(vPath, aPath, mp4Path); err != nil {
		t.Fatal(err)
	}
	return mp4Path
}

// On the real reader: a key-frame preview decodes one frame, a decode
// at the position afterwards goes on from it, and seeking the audio leaves
// the video reader where it is.
func TestPreviewAndAudioSeekOnMediaFoundation(t *testing.T) {
	dec, err := OpenMediaFile(syntheticMuxedMP4(t))
	if err != nil {
		t.Skipf("Media Foundation unavailable: %v", err)
	}
	defer dec.Close()
	split, ok := dec.(*splitDecoder)
	if !ok {
		t.Fatalf("decoder %T has no separate audio reader", dec)
	}
	img, pts, err := split.PreviewFrameAt(700*time.Millisecond, nil)
	if err != nil || img == nil {
		t.Fatalf("preview: %v", err)
	}
	if pts > 700*time.Millisecond {
		t.Fatalf("preview frame at %v, after the position", pts)
	}
	img, pts, err = split.DecodeVideoFrameAt(700*time.Millisecond, nil)
	if err != nil || img == nil || pts < 700*time.Millisecond-40*time.Millisecond {
		t.Fatalf("decode after preview: pts %v, err %v", pts, err)
	}
	split.MFDecoder.mu.Lock()
	videoAt := split.MFDecoder.lastVideoPos
	split.MFDecoder.mu.Unlock()
	if err := split.SeekAudio(100 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	split.MFDecoder.mu.Lock()
	after := split.MFDecoder.lastVideoPos
	split.MFDecoder.mu.Unlock()
	if after != videoAt {
		t.Fatalf("an audio seek moved the video reader from %v to %v", videoAt, after)
	}
	buf := make([]float32, 4096)
	if n, err := split.ReadAudioSamples(buf); n == 0 {
		t.Fatalf("no audio after the audio seek: %v", err)
	}
}

func TestMFDecoderPureAudioM4AAndVideoFirstUnchanged(t *testing.T) {
	tmp := t.TempDir()
	m4aPath := filepath.Join(tmp, "music.m4a")
	vPath := filepath.Join(tmp, "video.m4s")
	mp4Path := filepath.Join(tmp, "muxed.mp4")

	aBytes, err := download.BuildSyntheticAudioM4S(60)
	if err != nil {
		t.Fatalf("BuildSyntheticAudioM4S: %v", err)
	}
	if err := writeTestFile(m4aPath, aBytes); err != nil {
		t.Fatalf("write music.m4a: %v", err)
	}
	vBytes, err := download.BuildSyntheticVideoM4S(30, 30)
	if err != nil {
		t.Fatalf("BuildSyntheticVideoM4S: %v", err)
	}
	if err := writeTestFile(vPath, vBytes); err != nil {
		t.Fatalf("write video.m4s: %v", err)
	}
	if _, err := download.MuxDASHFiles(vPath, m4aPath, mp4Path); err != nil {
		t.Fatalf("MuxDASHFiles: %v", err)
	}

	// 1. Audio-only .m4a opens via audio-only MFDecoder with HasVideo == false.
	audioDec, err := OpenMediaFile(m4aPath)
	if err != nil {
		t.Fatalf("OpenMediaFile(music.m4a): %v", err)
	}
	if decoderHasVideo(audioDec) || audioDec.Width() != 0 || audioDec.Height() != 0 {
		_ = audioDec.Close()
		t.Fatalf("expected audio-only decoder with 0x0 video, got hasVideo=%v %dx%d", decoderHasVideo(audioDec), audioDec.Width(), audioDec.Height())
	}
	if _, isSplit := audioDec.(*splitDecoder); isSplit {
		_ = audioDec.Close()
		t.Fatalf("expected pure audio *MFDecoder, got *splitDecoder")
	}
	pcm := make([]float32, 2048)
	n, err := audioDec.ReadAudioSamples(pcm)
	if err != nil && n == 0 {
		_ = audioDec.Close()
		t.Fatalf("ReadAudioSamples(music.m4a): n=%d err=%v", n, err)
	}
	if err := audioDec.Seek(250 * time.Millisecond); err != nil {
		_ = audioDec.Close()
		t.Fatalf("Seek(music.m4a): %v", err)
	}

	ctrl := NewController(Options{AudioSink: NewNullAudioSink(), Volume: 0.6})
	if err := ctrl.LoadDecoder(m4aPath, audioDec); err != nil {
		t.Fatalf("Controller.LoadDecoder(music.m4a): %v", err)
	}
	if ctrl.HasVideo() || ctrl.Bitmap() != nil {
		_ = ctrl.Close()
		t.Fatalf("expected HasVideo=false and nil Bitmap for music.m4a")
	}
	ctrl.Play()
	if err := ctrl.Seek(0.3); err != nil {
		_ = ctrl.Close()
		t.Fatalf("Controller.Seek(music.m4a): %v", err)
	}
	ctrl.Pause()
	_ = ctrl.Close()

	// 2. Audio-only .mp3 also opens via audio-only MFDecoder with HasVideo == false.
	mp3Path := filepath.Join(tmp, "music.mp3")
	if err := writeTestFile(mp3Path, buildSyntheticSilentMP3(25)); err != nil {
		t.Fatalf("write music.mp3: %v", err)
	}
	mp3Dec, err := OpenMediaFile(mp3Path)
	if err != nil {
		t.Fatalf("OpenMediaFile(music.mp3): %v", err)
	}
	if decoderHasVideo(mp3Dec) || mp3Dec.Width() != 0 || mp3Dec.Height() != 0 {
		_ = mp3Dec.Close()
		t.Fatalf("expected audio-only MP3 decoder with 0x0 video, got hasVideo=%v %dx%d", decoderHasVideo(mp3Dec), mp3Dec.Width(), mp3Dec.Height())
	}
	mp3Ctrl := NewController(Options{AudioSink: NewNullAudioSink(), Volume: 0.6})
	if err := mp3Ctrl.LoadDecoder(mp3Path, mp3Dec); err != nil {
		_ = mp3Dec.Close()
		t.Fatalf("Controller.LoadDecoder(music.mp3): %v", err)
	}
	if mp3Ctrl.HasVideo() || mp3Ctrl.Bitmap() != nil {
		_ = mp3Ctrl.Close()
		t.Fatalf("expected HasVideo=false and nil Bitmap for music.mp3")
	}
	_ = mp3Ctrl.Close()

	// 3. Video+audio MP4 keeps the exact existing video-first splitDecoder path unchanged.
	vidDec, err := OpenMediaFile(mp4Path)
	if err != nil {
		t.Fatalf("OpenMediaFile(muxed.mp4): %v", err)
	}
	if _, isSplit := vidDec.(*splitDecoder); !isSplit {
		_ = vidDec.Close()
		t.Fatalf("expected muxed video+audio MP4 to keep *splitDecoder path, got %T", vidDec)
	}
	vidCtrl := NewController(Options{AudioSink: NewNullAudioSink(), Volume: 0.8})
	if err := vidCtrl.LoadDecoder(mp4Path, vidDec); err != nil {
		t.Fatalf("Controller.LoadDecoder(muxed.mp4): %v", err)
	}
	defer vidCtrl.Close()
	if !vidCtrl.HasVideo() || vidCtrl.Bitmap() == nil || !vidCtrl.Snapshot(1280, 720).HasVideo {
		t.Fatalf("expected HasVideo=true and non-nil Bitmap for muxed.mp4")
	}
}
