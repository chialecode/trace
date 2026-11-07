//go:build windows

package player

import (
	"image"
	"path/filepath"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/download"
)

// F-043: the GPU/RGBA reader path must show the same picture as the original
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

// F-059 on the real reader: a key-frame preview decodes one frame, a decode
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
