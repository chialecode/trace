package live

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/chialecode/trace/internal/download"
)

// liveFinalizeToleranceSec bounds how far the converted duration may differ
// from the source timeline. Real Bilibili parts differ by under 0.02 s (the
// audio and video tracks end a fraction of a frame apart).
const liveFinalizeToleranceSec = 1.0

type contextFile struct {
	*os.File
	ctx context.Context
}

func (f contextFile) Read(p []byte) (int, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	return f.File.Read(p)
}

func (f contextFile) Write(p []byte) (int, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	return f.File.Write(p)
}

func (f contextFile) Seek(offset int64, whence int) (int64, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	return f.File.Seek(offset, whence)
}

// finalizeLivePart defragments a recorded fMP4 .part into a progressive MP4
// with moov before mdat. Media samples stay on disk while mp4ff reads metadata
// and copies them to the progressive output. The original .part remains until
// its manifest is saved. The output must keep the source's own media timeline;
// the recorder's running duration (HLS EXTINF sum) is only an estimate and is
// not compared.
func finalizeLivePart(ctx context.Context, partPath, finalPath string) (download.MuxTrackSummary, error) {
	f, err := os.Open(partPath)
	if err != nil {
		return download.MuxTrackSummary{}, err
	}
	defer f.Close()
	source := contextFile{File: f, ctx: ctx}
	parsed, err := mp4.DecodeFile(source, mp4.WithDecodeMode(mp4.DecModeLazyMdat), mp4.WithDecodeFlags(mp4.DecStartOnMoof))
	if err != nil {
		return download.MuxTrackSummary{}, err
	}
	trimFragmentOverlaps(parsed)
	sourceSec := fragmentTimelineSec(parsed)
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o700); err != nil {
		return download.MuxTrackSummary{}, err
	}
	out, err := os.CreateTemp(filepath.Dir(finalPath), ".trace-live-*.part")
	if err != nil {
		return download.MuxTrackSummary{}, err
	}
	name := out.Name()
	defer os.Remove(name)
	if err := mp4.Defragment(parsed, source, contextFile{File: out, ctx: ctx}); err != nil {
		_ = out.Close()
		return download.MuxTrackSummary{}, err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return download.MuxTrackSummary{}, err
	}
	if err := out.Close(); err != nil {
		return download.MuxTrackSummary{}, err
	}
	summary, err := download.InspectMP4FileContext(ctx, name)
	if err != nil {
		return summary, err
	}
	if !finalizedLivePart(summary) || sourceSec > 0 && math.Abs(summary.DurationSec-sourceSec) > liveFinalizeToleranceSec {
		return summary, errors.New("直播录像轨道或时长校验失败")
	}
	if err := ctx.Err(); err != nil {
		return summary, err
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
