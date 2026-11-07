package download

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/Eyevinn/mp4ff/mp4"
)

type diskSample struct {
	sample mp4.Sample
	offset int64
	dts    uint64
}

type diskTrack struct {
	source           *os.File
	file             *mp4.File
	samples          []diskSample
	timescale        uint32
	origin, duration uint64
}

type diskChunk struct {
	track      *diskTrack
	first, end int
	id         uint32
}

type contextIO struct {
	*os.File
	ctx context.Context
}

func (f contextIO) Read(p []byte) (int, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	return f.File.Read(p)
}
func (f contextIO) Write(p []byte) (int, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	return f.File.Write(p)
}
func (f contextIO) Seek(offset int64, whence int) (int64, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	return f.File.Seek(offset, whence)
}

// MuxDASHFilesContext keeps media on disk. Only sample tables and a fixed copy
// buffer are in memory, including when input/output offsets require co64.
func MuxDASHFilesContext(ctx context.Context, videoPath, audioPath, outPath string) (MuxTrackSummary, error) {
	video, err := readDiskTrack(ctx, videoPath, "vide", 1)
	if err != nil {
		return MuxTrackSummary{}, err
	}
	defer video.source.Close()
	tracks := []*diskTrack{video}
	if audioPath != "" {
		audio, err := readDiskTrack(ctx, audioPath, "soun", 2)
		if err != nil {
			return MuxTrackSummary{}, err
		}
		defer audio.source.Close()
		tracks = append(tracks, audio)
	}
	init := video.file.Init
	var chunks []diskChunk
	for i, track := range tracks {
		trak := track.file.Init.Moov.Traks[0]
		ensureTrackElst(init.Moov, trak, track.duration)
		// Defragment shifts the media edit by the first tfdt; preserve that origin.
		if trak.Edts != nil && len(trak.Edts.Elst) > 0 && len(trak.Edts.Elst[0].Entries) > 0 {
			trak.Edts.Elst[0].Entries[0].MediaTime = int64(track.origin)
		}
		if i > 0 {
			init.Moov.AddChild(trak)
			init.Moov.Mvex.AddChild(track.file.Init.Moov.Mvex.Trexs[0])
		}
		first := 0
		for n := 1; n <= len(track.samples); n++ {
			atEnd := n == len(track.samples)
			if !atEnd && track.samples[n].dts-track.samples[first].dts < uint64(track.timescale) && track.samples[n].dts == track.samples[n-1].dts+uint64(track.samples[n-1].sample.Dur) {
				continue
			}
			chunks = append(chunks, diskChunk{track: track, first: first, end: n, id: uint32(i + 1)})
			first = n
		}
	}
	init.Moov.Mvhd.NextTrackID = uint32(len(tracks) + 1)
	sort.SliceStable(chunks, func(i, j int) bool {
		return float64(chunks[i].track.samples[chunks[i].first].dts)/float64(chunks[i].track.timescale) < float64(chunks[j].track.samples[chunks[j].first].dts)/float64(chunks[j].track.timescale)
	})
	combined, err := os.CreateTemp(filepath.Dir(outPath), ".trace-mux-*.part")
	if err != nil {
		return MuxTrackSummary{}, err
	}
	defer os.Remove(combined.Name())
	defer combined.Close()
	writer := contextIO{File: combined, ctx: ctx}
	if err := init.Encode(writer); err != nil {
		return MuxTrackSummary{}, err
	}
	copyBuffer := make([]byte, 64*1024)
	for i, chunk := range chunks {
		fragment, err := mp4.CreateFragment(uint32(i+1), chunk.id)
		if err != nil {
			return MuxTrackSummary{}, err
		}
		var payload uint64
		for _, sample := range chunk.track.samples[chunk.first:chunk.end] {
			fragment.AddSample(sample.sample, sample.dts)
			payload += uint64(sample.sample.Size)
		}
		fragment.Mdat.SetLazyDataSize(payload)
		if err := fragment.Encode(writer); err != nil {
			return MuxTrackSummary{}, err
		}
		for _, sample := range chunk.track.samples[chunk.first:chunk.end] {
			if err := ctx.Err(); err != nil {
				return MuxTrackSummary{}, err
			}
			if _, err := io.CopyBuffer(writer, io.NewSectionReader(chunk.track.source, sample.offset, int64(sample.sample.Size)), copyBuffer); err != nil {
				return MuxTrackSummary{}, err
			}
		}
	}
	if err := combined.Sync(); err != nil {
		return MuxTrackSummary{}, err
	}
	return writeProgressiveDisk(ctx, combined, outPath)
}

func readDiskTrack(ctx context.Context, path, handler string, id uint32) (*diskTrack, error) {
	source, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*diskTrack, error) { source.Close(); return nil, err }
	file, err := mp4.DecodeFile(contextIO{File: source, ctx: ctx}, mp4.WithDecodeMode(mp4.DecModeLazyMdat), mp4.WithDecodeFlags(mp4.DecStartOnMoof))
	if err != nil {
		return fail(err)
	}
	if file.Init == nil || file.Init.Moov == nil || len(file.Init.Moov.Traks) != 1 || file.Init.Moov.Mvex == nil || len(file.Init.Moov.Mvex.Trexs) != 1 {
		return fail(fmt.Errorf("download: DASH 初始化轨道不完整"))
	}
	track := file.Init.Moov.Traks[0]
	if track.Mdia == nil || track.Mdia.Hdlr == nil || track.Mdia.Hdlr.HandlerType != handler || track.Mdia.Mdhd == nil || track.Mdia.Mdhd.Timescale == 0 {
		return fail(fmt.Errorf("download: DASH 轨道类型或时基错误"))
	}
	setSingleTrackID(file, id)
	result := &diskTrack{source: source, file: file, timescale: track.Mdia.Mdhd.Timescale}
	trex := file.Init.Moov.Mvex.Trexs[0]
	var previousEnd uint64
	for _, segment := range file.Segments {
		for _, fragment := range segment.Fragments {
			if fragment.Moof == nil || fragment.Mdat == nil || len(fragment.Moof.Trafs) != 1 {
				return fail(fmt.Errorf("download: DASH 媒体片段结构不完整"))
			}
			traf := fragment.Moof.Traf
			if traf.Tfhd == nil || traf.Tfdt == nil {
				return fail(fmt.Errorf("download: DASH 片段缺少轨道或解码时间"))
			}
			dts := traf.Tfdt.BaseMediaDecodeTime()
			base := fragment.Moof.StartPos
			if traf.Tfhd.HasBaseDataOffset() {
				base = traf.Tfhd.BaseDataOffset
			}
			payloadStart, payloadEnd := fragment.Mdat.PayloadAbsoluteOffset(), fragment.Mdat.StartPos+fragment.Mdat.Size()
			offset := int64(payloadStart)
			for _, run := range traf.Truns {
				run.AddSampleDefaultValues(traf.Tfhd, trex)
				if run.HasDataOffset() {
					offset = int64(base) + int64(run.DataOffset)
				}
				for _, sample := range run.Samples {
					if sample.Dur == 0 || sample.Size == 0 || offset < int64(payloadStart) || uint64(offset)+uint64(sample.Size) > payloadEnd {
						return fail(fmt.Errorf("download: DASH 样本长度或范围错误"))
					}
					if len(result.samples) == 0 {
						result.origin = dts
					}
					if dts < previousEnd {
						return fail(fmt.Errorf("download: DASH 解码时间重叠"))
					}
					result.samples = append(result.samples, diskSample{sample: sample, offset: offset, dts: dts})
					offset += int64(sample.Size)
					dts += uint64(sample.Dur)
					previousEnd = dts
				}
			}
		}
	}
	if len(result.samples) == 0 {
		return fail(fmt.Errorf("download: DASH 媒体没有样本"))
	}
	result.duration = previousEnd - result.origin
	return result, nil
}

func writeProgressiveDisk(ctx context.Context, source *os.File, outPath string) (MuxTrackSummary, error) {
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return MuxTrackSummary{}, err
	}
	reader := contextIO{File: source, ctx: ctx}
	file, err := mp4.DecodeFile(reader, mp4.WithDecodeMode(mp4.DecModeLazyMdat), mp4.WithDecodeFlags(mp4.DecStartOnMoof))
	if err != nil {
		return MuxTrackSummary{}, err
	}
	out, err := os.CreateTemp(filepath.Dir(outPath), ".trace-progressive-*.part")
	if err != nil {
		return MuxTrackSummary{}, err
	}
	defer os.Remove(out.Name())
	if err := mp4.Defragment(file, reader, contextIO{File: out, ctx: ctx}); err != nil {
		out.Close()
		return MuxTrackSummary{}, err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return MuxTrackSummary{}, err
	}
	if err := out.Close(); err != nil {
		return MuxTrackSummary{}, err
	}
	if err := addProgressiveEdits(ctx, out.Name()); err != nil {
		return MuxTrackSummary{}, err
	}
	summary, err := InspectMP4FileContext(ctx, out.Name())
	if err != nil {
		return summary, err
	}
	if !summary.MoovBeforeMdat || summary.MaxTrackDiffSec > 2 {
		return summary, fmt.Errorf("download: MP4 布局或音视频时长校验失败")
	}
	if err := os.Rename(out.Name(), outPath); err != nil {
		return summary, err
	}
	return summary, nil
}

// Keep required edit lists while moving only metadata. Use co64 for offsets so
// growing moov can never wrap a 32-bit chunk offset near the 4 GiB boundary.
func addProgressiveEdits(ctx context.Context, path string) error {
	source, err := os.Open(path)
	if err != nil {
		return err
	}
	reader := contextIO{File: source, ctx: ctx}
	file, err := mp4.DecodeFile(reader, mp4.WithDecodeMode(mp4.DecModeLazyMdat))
	if err != nil {
		source.Close()
		return err
	}
	if file.Moov == nil || file.Mdat == nil {
		source.Close()
		return fmt.Errorf("download: 合并后缺少 moov/mdat")
	}
	oldSize := file.Moov.Size()
	for _, track := range file.Moov.Traks {
		if track.Edts == nil || len(track.Edts.Elst) == 0 {
			ensureTrackElst(file.Moov, track, track.Mdia.Mdhd.Duration)
		}
		stbl := track.Mdia.Minf.Stbl
		if stbl.Stco != nil {
			co64 := &mp4.Co64Box{Version: stbl.Stco.Version, Flags: stbl.Stco.Flags}
			for _, offset := range stbl.Stco.ChunkOffset {
				co64.ChunkOffset = append(co64.ChunkOffset, uint64(offset))
			}
			for i, child := range stbl.Children {
				if child == stbl.Stco {
					stbl.Children[i] = co64
				}
			}
			stbl.Stco, stbl.Co64 = nil, co64
		}
	}
	delta := file.Moov.Size() - oldSize
	for _, track := range file.Moov.Traks {
		if offsets := track.Mdia.Minf.Stbl.Co64; offsets != nil {
			for i := range offsets.ChunkOffset {
				offsets.ChunkOffset[i] += delta
			}
		}
	}
	out, err := os.CreateTemp(filepath.Dir(path), ".trace-edits-*.part")
	if err != nil {
		source.Close()
		return err
	}
	defer os.Remove(out.Name())
	writer := contextIO{File: out, ctx: ctx}
	for _, child := range file.Children {
		if err = child.Encode(writer); err != nil {
			break
		}
		if mdat, ok := child.(*mp4.MdatBox); ok {
			_, err = io.Copy(writer, io.NewSectionReader(source, int64(mdat.PayloadAbsoluteOffset()), int64(mdat.Size()-mdat.HeaderSize())))
			if err != nil {
				break
			}
		}
	}
	closeSource := source.Close()
	if err == nil {
		err = closeSource
	}
	if err == nil {
		err = out.Sync()
	}
	closeOutput := out.Close()
	if err == nil {
		err = closeOutput
	}
	if err != nil {
		return err
	}
	return os.Rename(out.Name(), path)
}
