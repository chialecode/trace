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
