package live

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/chialecode/trace/internal/download"
)

// liveFinalizeToleranceSec bounds how far the converted duration may differ
// from the source timeline. Real Bilibili parts differ by under 0.02 s (the
// audio and video tracks end a fraction of a frame apart).
const liveFinalizeToleranceSec = 1.0

const (
	// defaultFinalizeStallTimeout aborts a conversion only when zero I/O progress
	// occurs for this duration (rather than imposing a fixed total wall-clock cap
	// on multi-gigabyte parts).
	defaultFinalizeStallTimeout = 2 * time.Minute
	finalizeWriteBufferSize     = 4 << 20 // 4 MiB sequential write buffer
)

// errFinalizeStalled indicates that live MP4 conversion made no I/O progress
// within the stall watchdog interval.
var errFinalizeStalled = errors.New("直播录像转换读写停滞超时")

// errFinalizeNoMediaSamples indicates an fMP4 .part contains an init header
// (or empty tracks) with no decodable video samples.
var errFinalizeNoMediaSamples = errors.New("直播录像片段缺少媒体样本")

// liveTracksWithSamples inspects moov tracks and fragment truns to report which
// tracks carry at least one progressive or fragment sample.
func liveTracksWithSamples(f *mp4.File) (keptIDs []uint32, hasVideo bool, emptyCount int, totalSamples uint64) {
	if f == nil {
		return nil, false, 0, 0
	}
	moov := f.Moov
	if moov == nil && f.Init != nil {
		moov = f.Init.Moov
	}
	if moov == nil {
		return nil, false, 0, 0
	}
	fragSamples := make(map[uint32]uint64, len(moov.Traks))
	for _, seg := range f.Segments {
		if seg == nil {
			continue
		}
		for _, frag := range seg.Fragments {
			if frag == nil || frag.Moof == nil {
				continue
			}
			for _, traf := range frag.Moof.Trafs {
				if traf == nil || traf.Tfhd == nil {
					continue
				}
				id := traf.Tfhd.TrackID
				for _, trun := range traf.Truns {
					if trun == nil {
						continue
					}
					n := uint64(len(trun.Samples))
					if n == 0 && trun.SampleCount() > 0 {
						n = uint64(trun.SampleCount())
					}
					fragSamples[id] += n
				}
			}
		}
	}
	for _, trak := range moov.Traks {
		if trak == nil || trak.Tkhd == nil {
			continue
		}
		id := trak.Tkhd.TrackID
		count := fragSamples[id]
		if trak.Mdia != nil && trak.Mdia.Minf != nil && trak.Mdia.Minf.Stbl != nil && trak.Mdia.Minf.Stbl.Stsz != nil {
			count += uint64(trak.Mdia.Minf.Stbl.Stsz.GetNrSamples())
		}
		totalSamples += count
		if count == 0 {
			emptyCount++
			continue
		}
		keptIDs = append(keptIDs, id)
		if trak.Mdia != nil && trak.Mdia.Hdlr != nil && trak.Mdia.Hdlr.HandlerType == "vide" {
			hasVideo = true
		}
	}
	return keptIDs, hasVideo, emptyCount, totalSamples
}

type finalizeStallTimeoutKey struct{}

// withFinalizeStallTimeout returns a child context overriding the no-progress
// stall watchdog duration used by finalizeLivePart when d > 0.
func withFinalizeStallTimeout(ctx context.Context, d time.Duration) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if d <= 0 {
		return ctx
	}
	return context.WithValue(ctx, finalizeStallTimeoutKey{}, d)
}

func finalizeStallTimeoutFromContext(ctx context.Context) time.Duration {
	if ctx != nil {
		if d, ok := ctx.Value(finalizeStallTimeoutKey{}).(time.Duration); ok && d > 0 {
			return d
		}
	}
	return defaultFinalizeStallTimeout
}

type progressContextReadSeeker struct {
	rs           io.ReadSeeker
	ctx          context.Context
	lastProgress *atomic.Int64
	pos          int64
	posKnown     bool
}

func newProgressContextReadSeeker(rs io.ReadSeeker, ctx context.Context, lastProgress *atomic.Int64) *progressContextReadSeeker {
	return &progressContextReadSeeker{
		rs:           rs,
		ctx:          ctx,
		lastProgress: lastProgress,
		pos:          0,
		posKnown:     true,
	}
}

func (f *progressContextReadSeeker) Read(p []byte) (int, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := f.rs.Read(p)
	if n > 0 {
		if f.posKnown {
			f.pos += int64(n)
		}
		f.lastProgress.Store(time.Now().UnixNano())
	}
	if ctxErr := f.ctx.Err(); ctxErr != nil {
		return 0, ctxErr
	}
	return n, err
}

func (f *progressContextReadSeeker) Seek(offset int64, whence int) (int64, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	if f.posKnown {
		if (whence == io.SeekStart && offset == f.pos) || (whence == io.SeekCurrent && offset == 0) {
			return f.pos, nil
		}
	}
	pos, err := f.rs.Seek(offset, whence)
	if err != nil {
		f.posKnown = false
		return pos, err
	}
	f.pos = pos
	f.posKnown = true
	f.lastProgress.Store(time.Now().UnixNano())
	return pos, nil
}

type progressContextWriter struct {
	w            io.Writer
	ctx          context.Context
	lastProgress *atomic.Int64
}

func (w progressContextWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := w.w.Write(p)
	if n > 0 {
		w.lastProgress.Store(time.Now().UnixNano())
	}
	return n, err
}

type countingHashWriter struct {
	dst          *bufio.Writer
	hash         hash.Hash
	ctx          context.Context
	lastProgress *atomic.Int64
	written      int64
}

func (w *countingHashWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := w.dst.Write(p)
	if n > 0 {
		_, _ = w.hash.Write(p[:n])
		w.written += int64(n)
		w.lastProgress.Store(time.Now().UnixNano())
	}
	return n, err
}

func startFinalizeStallWatchdog(
	ctx context.Context,
	timeout time.Duration,
	lastProgress *atomic.Int64,
	onStall func(),
) func() {
	if timeout <= 0 {
		return func() {}
	}
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	interval := min(timeout/4, 5*time.Second)
	if interval < 5*time.Millisecond {
		interval = 5 * time.Millisecond
	}
	go func() {
		defer close(doneCh)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				last := time.Unix(0, lastProgress.Load())
				if now.Sub(last) >= timeout {
					onStall()
					return
				}
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(stopCh)
			<-doneCh
		})
	}
}

func wrapFinalizeErr(err error, stalled bool, timeout time.Duration) error {
	if err == nil {
		return nil
	}
	if stalled && errors.Is(err, context.Canceled) {
		return fmt.Errorf("%w（超过 %s 无进展）: %w", errFinalizeStalled, timeout, err)
	}
	return err
}

// finalizeLivePart defragments a recorded fMP4 .part into a progressive MP4
// with moov before mdat. Media samples stay on disk while mp4ff reads metadata
// and copies them to the progressive output. Output writes are buffered and
// SHA-256 is computed inline so post-write validation inspects box structure
// lazily without rereading mdat. The original .part remains until its manifest
// is saved. The output must keep the source's own media timeline; the
// recorder's running duration (HLS EXTINF sum) is only an estimate and is not
// compared.
func finalizeLivePart(ctx context.Context, partPath, finalPath string) (download.MuxTrackSummary, error) {
	f, err := os.Open(partPath)
	if err != nil {
		return download.MuxTrackSummary{}, err
	}
	defer f.Close()
	return finalizeLivePartFromReadSeeker(ctx, f, finalPath)
}

func finalizeLivePartFromReadSeeker(ctx context.Context, src io.ReadSeeker, finalPath string) (download.MuxTrackSummary, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	workCtx, cancelWork := context.WithCancel(ctx)
	defer cancelWork()

	var lastProgress atomic.Int64
	lastProgress.Store(time.Now().UnixNano())
	var stalled atomic.Bool
	stallTimeout := finalizeStallTimeoutFromContext(ctx)
	stopWatchdog := startFinalizeStallWatchdog(workCtx, stallTimeout, &lastProgress, func() {
		stalled.Store(true)
		cancelWork()
	})
	defer stopWatchdog()

	source := newProgressContextReadSeeker(src, workCtx, &lastProgress)
	parsed, err := mp4.DecodeFile(source, mp4.WithDecodeMode(mp4.DecModeLazyMdat), mp4.WithDecodeFlags(mp4.DecStartOnMoof))
	if err != nil {
		stopWatchdog()
		return download.MuxTrackSummary{}, wrapFinalizeErr(err, stalled.Load(), stallTimeout)
	}
	trimFragmentOverlaps(parsed)
	keptIDs, hasVideo, emptyCount, _ := liveTracksWithSamples(parsed)
	if !hasVideo || len(keptIDs) == 0 {
		stopWatchdog()
		return download.MuxTrackSummary{}, errFinalizeNoMediaSamples
	}
	sourceSec := fragmentTimelineSec(parsed)
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o700); err != nil {
		stopWatchdog()
		return download.MuxTrackSummary{}, err
	}
	out, err := os.CreateTemp(filepath.Dir(finalPath), ".trace-live-*.part")
	if err != nil {
		stopWatchdog()
		return download.MuxTrackSummary{}, err
	}
	name := out.Name()
	defer os.Remove(name)

	bufOut := bufio.NewWriterSize(progressContextWriter{
		w:            out,
		ctx:          workCtx,
		lastProgress: &lastProgress,
	}, finalizeWriteBufferSize)
	hasher := sha256.New()
	sink := &countingHashWriter{
		dst:          bufOut,
		hash:         hasher,
		ctx:          workCtx,
		lastProgress: &lastProgress,
	}

	var defragErr error
	if emptyCount > 0 {
		defragErr = mp4.DefragmentTracks(parsed, source, sink, keptIDs...)
	} else {
		defragErr = mp4.Defragment(parsed, source, sink)
	}
	if defragErr != nil {
		stopWatchdog()
		_ = out.Close()
		return download.MuxTrackSummary{}, wrapFinalizeErr(defragErr, stalled.Load(), stallTimeout)
	}
	if err := bufOut.Flush(); err != nil {
		stopWatchdog()
		_ = out.Close()
		return download.MuxTrackSummary{}, wrapFinalizeErr(err, stalled.Load(), stallTimeout)
	}
	// Stop the progress watchdog before out.Sync() so flushing large OS write
	// buffers to NTFS never false-trips the no-progress detector.
	stopWatchdog()
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return download.MuxTrackSummary{}, err
	}
	if err := out.Close(); err != nil {
		return download.MuxTrackSummary{}, err
	}
	summary, err := download.InspectMP4StructureContext(workCtx, name)
	if err != nil {
		return summary, wrapFinalizeErr(err, stalled.Load(), stallTimeout)
	}
	if sink.written != summary.FileBytes {
		return summary, fmt.Errorf("直播录像输出长度校验失败（写入 %d，磁盘 %d）", sink.written, summary.FileBytes)
	}
	summary.SHA256 = hex.EncodeToString(hasher.Sum(nil))
	if !finalizedLivePart(summary) || sourceSec > 0 && math.Abs(summary.DurationSec-sourceSec) > liveFinalizeToleranceSec {
		return summary, errors.New("直播录像轨道或时长校验失败")
	}
	if err := workCtx.Err(); err != nil {
		return summary, wrapFinalizeErr(err, stalled.Load(), stallTimeout)
	}
	if err := os.Rename(name, finalPath); err != nil {
		return summary, err
	}
	return summary, nil
}

// finalizedLivePart reports whether an MP4 is a converted recording: a video
// track, moov before mdat and a positive duration.
func finalizedLivePart(summary download.MuxTrackSummary) bool {
	video := false
	for _, handler := range summary.Handlers {
		video = video || handler == "vide"
	}
	return video && summary.MoovBeforeMdat && summary.DurationSec > 0
}
