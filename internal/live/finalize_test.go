package live

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Eyevinn/mp4ff/aac"
	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/chialecode/trace/internal/download"
)

func assertNoTempParts(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir %s: %v", dir, err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".trace-live-") {
			t.Fatalf("leaked temp conversion file: %s", e.Name())
		}
	}
}

func TestFinalizeLivePartInlineSHA256MatchesInspectMP4File(t *testing.T) {
	raw := overlappingLiveFMP4(t)
	dir := t.TempDir()
	partPath := filepath.Join(dir, "part-001.mp4.part")
	finalPath := filepath.Join(dir, "part-001.mp4")
	if err := os.WriteFile(partPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	summary, err := finalizeLivePart(context.Background(), partPath, finalPath)
	if err != nil {
		t.Fatalf("finalizeLivePart: %v", err)
	}
	full, err := download.InspectMP4FileContext(context.Background(), finalPath)
	if err != nil {
		t.Fatalf("InspectMP4FileContext: %v", err)
	}
	if summary.SHA256 == "" || summary.SHA256 != full.SHA256 {
		t.Fatalf("inline SHA256 = %q, InspectMP4FileContext = %q", summary.SHA256, full.SHA256)
	}
	if summary.FileBytes != full.FileBytes {
		t.Fatalf("FileBytes = %d, want %d", summary.FileBytes, full.FileBytes)
	}
	if !summary.MoovBeforeMdat || summary.TrackCount != full.TrackCount || math.Abs(summary.DurationSec-full.DurationSec) > 1e-6 {
		t.Fatalf("summary = %+v, full = %+v", summary, full)
	}
	assertNoTempParts(t, dir)
}

type stalledReadSeeker struct {
	r          *bytes.Reader
	stallAfter int
	stallFor   time.Duration
	reads      int
	stalled    bool
}

func (s *stalledReadSeeker) Read(p []byte) (int, error) {
	s.reads++
	if !s.stalled && s.reads > s.stallAfter {
		s.stalled = true
		time.Sleep(s.stallFor)
	}
	return s.r.Read(p)
}

func (s *stalledReadSeeker) Seek(offset int64, whence int) (int64, error) {
	return s.r.Seek(offset, whence)
}

func TestFinalizeLivePartStallWatchdogCancelsAndCleansTempFile(t *testing.T) {
	raw := overlappingLiveFMP4(t)
	dir := t.TempDir()
	finalPath := filepath.Join(dir, "part-001.mp4")

	src := &stalledReadSeeker{
		r:          bytes.NewReader(raw),
		stallAfter: 5,
		stallFor:   120 * time.Millisecond,
	}
	ctx := withFinalizeStallTimeout(context.Background(), 35*time.Millisecond)
	_, err := finalizeLivePartFromReadSeeker(ctx, src, finalPath)
	if err == nil {
		t.Fatal("expected stall watchdog cancellation error")
	}
	if !errors.Is(err, errFinalizeStalled) {
		t.Fatalf("err = %v, want errors.Is(errFinalizeStalled)", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want errors.Is(context.Canceled)", err)
	}
	if _, statErr := os.Stat(finalPath); !os.IsNotExist(statErr) {
		t.Fatalf("finalPath should not exist after stalled conversion: %v", statErr)
	}
	assertNoTempParts(t, dir)
}

type cancelingReadSeeker struct {
	r           *bytes.Reader
	cancelAfter int
	cancel      context.CancelFunc
	reads       int
}

func (c *cancelingReadSeeker) Read(p []byte) (int, error) {
	c.reads++
	if c.reads == c.cancelAfter {
		c.cancel()
	}
	return c.r.Read(p)
}

func (c *cancelingReadSeeker) Seek(offset int64, whence int) (int64, error) {
	return c.r.Seek(offset, whence)
}

func TestFinalizeLivePartParentCancelNotLabeledStalled(t *testing.T) {
	raw := overlappingLiveFMP4(t)
	dir := t.TempDir()
	finalPath := filepath.Join(dir, "part-001.mp4")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = withFinalizeStallTimeout(ctx, 10*time.Second)

	src := &cancelingReadSeeker{
		r:           bytes.NewReader(raw),
		cancelAfter: 5,
		cancel:      cancel,
	}
	_, err := finalizeLivePartFromReadSeeker(ctx, src, finalPath)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if errors.Is(err, errFinalizeStalled) {
		t.Fatalf("parent cancellation was mislabeled as stall: %v", err)
	}
	if _, statErr := os.Stat(finalPath); !os.IsNotExist(statErr) {
		t.Fatalf("finalPath should not exist after parent cancellation: %v", statErr)
	}
	assertNoTempParts(t, dir)
}

type countingReadSeeker struct {
	r         *bytes.Reader
	seekCalls int
}

func (c *countingReadSeeker) Read(p []byte) (int, error) {
	return c.r.Read(p)
}

func (c *countingReadSeeker) Seek(offset int64, whence int) (int64, error) {
	c.seekCalls++
	return c.r.Seek(offset, whence)
}

func TestProgressContextReadSeekerSkipsRedundantSeek(t *testing.T) {
	base := &countingReadSeeker{r: bytes.NewReader([]byte("0123456789abcdef"))}
	var last atomic.Int64
	rs := newProgressContextReadSeeker(base, context.Background(), &last)

	buf := make([]byte, 4)
	if n, err := rs.Read(buf); err != nil || n != 4 || string(buf) != "0123" {
		t.Fatalf("Read = (%d, %q, %v)", n, string(buf), err)
	}
	if pos, err := rs.Seek(4, io.SeekStart); err != nil || pos != 4 {
		t.Fatalf("Seek(4, Start) = (%d, %v)", pos, err)
	}
	if pos, err := rs.Seek(0, io.SeekCurrent); err != nil || pos != 4 {
		t.Fatalf("Seek(0, Current) = (%d, %v)", pos, err)
	}
	if base.seekCalls != 0 {
		t.Fatalf("redundant seeks triggered %d underlying Seek calls, want 0", base.seekCalls)
	}
	if pos, err := rs.Seek(10, io.SeekStart); err != nil || pos != 10 {
		t.Fatalf("Seek(10, Start) = (%d, %v)", pos, err)
	}
	if base.seekCalls != 1 {
		t.Fatalf("non-sequential seek triggered %d underlying Seek calls, want 1", base.seekCalls)
	}
	if n, err := rs.Read(buf); err != nil || n != 4 || string(buf) != "abcd" {
		t.Fatalf("Read after seek = (%d, %q, %v)", n, string(buf), err)
	}
}

// buildSyntheticTwoTrackInitBytes constructs an fMP4 init segment with video track 1
// and AAC audio track 2 (matching Bilibili HLS fMP4 headers).
func buildSyntheticTwoTrackInitBytes(t *testing.T) []byte {
	t.Helper()
	init := mp4.CreateEmptyInit()
	init.Moov.Mvhd.Timescale = 1000
	sps := mustHex(t, testAVCSPSHex)
	pps := mustHex(t, testAVCPPSHex)
	v := init.AddEmptyTrack(30000, "video", "und")
	if err := v.SetAVCDescriptor("avc1", [][]byte{sps}, [][]byte{pps}, true); err != nil {
		t.Fatal(err)
	}
	v.Tkhd.Width, v.Tkhd.Height = mp4.Fixed32(640<<16), mp4.Fixed32(360<<16)
	a := init.AddEmptyTrack(48000, "audio", "und")
	if err := a.SetAACDescriptor(aac.AAClc, 48000); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := init.Encode(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestFinalizeLivePartDropsEmptyAuxiliaryTrackWithDefragmentTracks(t *testing.T) {
	initBytes := buildSyntheticTwoTrackInitBytes(t)
	sps := mustHex(t, testAVCSPSHex)
	videoSample := append([]byte{0, 0, 0, byte(len(sps))}, sps...)
	vf, err := mp4.CreateFragment(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		vf.AddFullSample(mp4.FullSample{
			Sample:     mp4.Sample{Flags: mp4.SyncSampleFlags, Dur: 1000, Size: uint32(len(videoSample))},
			DecodeTime: uint64(i) * 1000,
			Data:       videoSample,
		})
	}
	var raw bytes.Buffer
	raw.Write(initBytes)
	if err := vf.Encode(&raw); err != nil {
		t.Fatal(err)
	}

	// Verify mp4.Defragment alone fails on the 0-sample audio track 2.
	parsed, err := mp4.DecodeFile(bytes.NewReader(raw.Bytes()), mp4.WithDecodeFlags(mp4.DecStartOnMoof))
	if err != nil {
		t.Fatal(err)
	}
	if err := mp4.Defragment(parsed, bytes.NewReader(raw.Bytes()), &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "track 2 has no samples") {
		t.Fatalf("expected raw mp4.Defragment to reject empty track 2, got: %v", err)
	}

	dir := t.TempDir()
	partPath := filepath.Join(dir, "part-001.mp4.part")
	finalPath := filepath.Join(dir, "part-001.mp4")
	if err := os.WriteFile(partPath, raw.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	summary, err := finalizeLivePart(context.Background(), partPath, finalPath)
	if err != nil {
		t.Fatalf("finalizeLivePart failed when audio track 2 had 0 samples: %v", err)
	}
	if !summary.MoovBeforeMdat || summary.TrackCount != 1 || summary.DurationSec < 0.9 || summary.DurationSec > 1.1 {
		t.Fatalf("unexpected converted summary: %+v", summary)
	}
	full, err := download.InspectMP4FileContext(context.Background(), finalPath)
	if err != nil || full.SHA256 != summary.SHA256 {
		t.Fatalf("InspectMP4FileContext = (%+v, %v), want SHA256 %q", full, err, summary.SHA256)
	}
	assertNoTempParts(t, dir)
}

func TestFinalizeLivePartRejectsInitOnlyZeroSamplePart(t *testing.T) {
	dir := t.TempDir()
	partPath := filepath.Join(dir, "part-001.mp4.part")
	finalPath := filepath.Join(dir, "part-001.mp4")
	if err := os.WriteFile(partPath, buildSyntheticTwoTrackInitBytes(t), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := finalizeLivePart(context.Background(), partPath, finalPath)
	if !errors.Is(err, errFinalizeNoMediaSamples) {
		t.Fatalf("err = %v, want errFinalizeNoMediaSamples", err)
	}
	if _, statErr := os.Stat(finalPath); !os.IsNotExist(statErr) {
		t.Fatalf("finalPath must not exist after init-only rejection: %v", statErr)
	}
	assertNoTempParts(t, dir)
}
