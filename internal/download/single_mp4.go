package download

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/chialecode/trace/internal/site"
)

const singleMP4StreamBufSize = 256 * 1024

// EncryptedMediaError indicates that an MP4 container or track uses CENC/DRM encryption
// (such as encv, enca, sinf, senc, saiz, saio, or pssh).
type EncryptedMediaError struct {
	TrackID uint32
	BoxType string
	Reason  string
}

func (e *EncryptedMediaError) Error() string {
	if e.TrackID > 0 {
		return fmt.Sprintf("download: 不支持加密的 MP4 媒体 (track=%d box=%s): %s", e.TrackID, e.BoxType, e.Reason)
	}
	return fmt.Sprintf("download: 不支持加密的 MP4 媒体 (box=%s): %s", e.BoxType, e.Reason)
}

func (e *EncryptedMediaError) Unwrap() error {
	return &site.Error{
		Kind:        site.ErrKindBlocked,
		Unsupported: true,
		Message:     e.Error(),
	}
}

// IsEncryptedMedia reports whether err was caused by CENC/DRM encrypted MP4 boxes.
func IsEncryptedMedia(err error) bool {
	var target *EncryptedMediaError
	return errors.As(err, &target)
}

// SingleMP4ValidationError indicates a structural, track, offset, or duration validation failure
// on a single-file progressive MP4.
type SingleMP4ValidationError struct {
	Reason string
	Err    error
}

func (e *SingleMP4ValidationError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("download: 单文件 MP4 校验失败：%s: %v", e.Reason, e.Err)
	}
	return fmt.Sprintf("download: 单文件 MP4 校验失败：%s", e.Reason)
}

func (e *SingleMP4ValidationError) Unwrap() error { return e.Err }

// IsSingleMP4ValidationError reports whether err is a single-file MP4 validation error.
func IsSingleMP4ValidationError(err error) bool {
	var target *SingleMP4ValidationError
	return errors.As(err, &target)
}

type mdatByteSpan struct {
	payloadStart uint64
	payloadEnd   uint64
}

type singleMP4Inspection struct {
	moovIdx      int
	firstMdatIdx int
	needsReloc   bool
	width        int
	height       int
	durationSec  float64
}

// EnsureSingleMP4 validates a progressive single-file MP4 on disk (without requiring elst),
// rejects CENC-encrypted tracks and >2s duration mismatches, and relocates moov before mdat
// on disk with bounded memory when moov trails mdat.
func EnsureSingleMP4(path string, expectedDurationSec float64) (MuxTrackSummary, error) {
	return EnsureSingleMP4Context(context.Background(), path, expectedDurationSec)
}

// EnsureSingleMP4Context is the context-aware variant of EnsureSingleMP4.
func EnsureSingleMP4Context(ctx context.Context, path string, expectedDurationSec float64) (MuxTrackSummary, error) {
	if err := ctx.Err(); err != nil {
		return MuxTrackSummary{}, err
	}

	source, err := os.Open(path)
	if err != nil {
		return MuxTrackSummary{}, err
	}
	sourceClosed := false
	defer func() {
		if !sourceClosed {
			_ = source.Close()
		}
	}()

	info, err := source.Stat()
	if err != nil {
		return MuxTrackSummary{}, err
	}
	if info.Size() <= 0 {
		return MuxTrackSummary{}, &SingleMP4ValidationError{Reason: "文件为空"}
	}

	reader := contextIO{File: source, ctx: ctx}
	parsed, err := mp4.DecodeFile(reader, mp4.WithDecodeMode(mp4.DecModeLazyMdat), mp4.WithDecodeFlags(mp4.DecStartOnMoof))
	if err != nil || parsed == nil {
		return MuxTrackSummary{}, &SingleMP4ValidationError{Reason: "解析 MP4 容器失败", Err: err}
	}

	inspected, err := validateDecodedSingleMP4(parsed, uint64(info.Size()), expectedDurationSec)
	if err != nil {
		return MuxTrackSummary{}, err
	}

	if inspected.needsReloc {
		if err := relocateMoovOnDisk(ctx, source, &sourceClosed, parsed, inspected.moovIdx, inspected.firstMdatIdx, path); err != nil {
			return MuxTrackSummary{}, err
		}
	} else {
		sourceClosed = true
		if err := source.Close(); err != nil {
			return MuxTrackSummary{}, err
		}
	}

	summary, err := InspectMP4FileContext(ctx, path)
	if err != nil {
		return MuxTrackSummary{}, err
	}
	if summary.Width == 0 && inspected.width > 0 {
		summary.Width = inspected.width
	}
	if summary.Height == 0 && inspected.height > 0 {
		summary.Height = inspected.height
	}
	if !summary.MoovBeforeMdat {
		return summary, &SingleMP4ValidationError{Reason: "MP4 moov 未位于 mdat 之前"}
	}
	if summary.DurationSec <= 0 {
		return summary, &SingleMP4ValidationError{Reason: "MP4 时长必须大于 0"}
	}
	if expectedDurationSec > 0 && math.Abs(summary.DurationSec-expectedDurationSec) > 2.0 {
		return summary, &SingleMP4ValidationError{
			Reason: fmt.Sprintf("MP4 时长 %.2fs 与预期 %.2fs 误差超过 2 秒", summary.DurationSec, expectedDurationSec),
		}
	}
	return summary, nil
}

func validateDecodedSingleMP4(parsed *mp4.File, fileSize uint64, expectedDurationSec float64) (singleMP4Inspection, error) {
	var declared uint64
	moovIdx := -1
	moovCount := 0
	firstMdatIdx := -1
	lastMdatIdx := -1
	var mdatSpans []mdatByteSpan

	for idx, ch := range parsed.Children {
		declared += ch.Size()
		switch ch.Type() {
		case "pssh":
			return singleMP4Inspection{}, &EncryptedMediaError{
				BoxType: "pssh",
				Reason:  "顶层包含 CENC pssh 加密信息",
			}
		case "moof":
			return singleMP4Inspection{}, &SingleMP4ValidationError{Reason: "单文件 MP4 不接受分片 moof 结构"}
		case "moov":
			moovCount++
			if moovIdx == -1 {
				moovIdx = idx
			}
		case "mdat":
			mdat, ok := ch.(*mp4.MdatBox)
			if !ok {
				return singleMP4Inspection{}, &SingleMP4ValidationError{Reason: "mdat 类型异常"}
			}
			pStart := mdat.PayloadAbsoluteOffset()
			pEnd := mdat.StartPos + mdat.Size()
			if pEnd > pStart {
				if firstMdatIdx == -1 {
					firstMdatIdx = idx
				}
				lastMdatIdx = idx
				mdatSpans = append(mdatSpans, mdatByteSpan{
					payloadStart: pStart,
					payloadEnd:   pEnd,
				})
			} else if firstMdatIdx == -1 {
				firstMdatIdx = idx
			}
		}
	}

	if declared != fileSize {
		return singleMP4Inspection{}, &SingleMP4ValidationError{
			Reason: fmt.Sprintf("顶层 box 总长 %d 与文件字节数 %d 不一致", declared, fileSize),
		}
	}
	if parsed.IsFragmented() && len(parsed.Segments) > 0 {
		return singleMP4Inspection{}, &SingleMP4ValidationError{Reason: "单文件 MP4 不接受分片流"}
	}
	if moovCount != 1 || moovIdx < 0 || parsed.Moov == nil {
		return singleMP4Inspection{}, &SingleMP4ValidationError{Reason: "缺少唯一有效的 moov box"}
	}
	if len(mdatSpans) == 0 || firstMdatIdx < 0 {
		return singleMP4Inspection{}, &SingleMP4ValidationError{Reason: "缺少非空 mdat 媒体数据"}
	}

	moov := parsed.Moov
	for _, ch := range moov.Children {
		if ch.Type() == "pssh" {
			return singleMP4Inspection{}, &EncryptedMediaError{
				BoxType: "pssh",
				Reason:  "moov 包含 CENC pssh 加密信息",
			}
		}
	}
	if len(moov.Traks) == 0 {
		return singleMP4Inspection{}, &SingleMP4ValidationError{Reason: "moov 不包含任何轨道"}
	}

	var durationSec float64
	if moov.Mvhd != nil && moov.Mvhd.Timescale > 0 {
		durationSec = float64(moov.Mvhd.Duration) / float64(moov.Mvhd.Timescale)
	}

	videoTracks := 0
	maxWidth, maxHeight := 0, 0

	for _, trak := range moov.Traks {
		if trak == nil {
			continue
		}
		var trackID uint32
		if trak.Tkhd != nil {
			trackID = trak.Tkhd.TrackID
		}
		if err := checkTrackNotEncrypted(moov, trak, trackID); err != nil {
			return singleMP4Inspection{}, err
		}
		if trak.Mdia == nil || trak.Mdia.Hdlr == nil || trak.Mdia.Minf == nil || trak.Mdia.Minf.Stbl == nil {
			return singleMP4Inspection{}, &SingleMP4ValidationError{Reason: "轨道缺少 mdia/hdlr/stbl 结构"}
		}

		stbl := trak.Mdia.Minf.Stbl
		handler := trak.Mdia.Hdlr.HandlerType
		switch handler {
		case "vide":
			if trak.Mdia.Mdhd == nil || trak.Mdia.Mdhd.Timescale == 0 {
				return singleMP4Inspection{}, &SingleMP4ValidationError{Reason: "视频轨缺少有效时间刻度"}
			}
			if countProgressiveSamples(stbl) == 0 {
				return singleMP4Inspection{}, &SingleMP4ValidationError{Reason: "视频轨没有样本"}
			}
			if err := validateChunkOffsetsInMdat(stbl, mdatSpans, true); err != nil {
				return singleMP4Inspection{}, err
			}
			w, h := extractVideoTrackDimensions(trak)
			if w <= 0 || h <= 0 {
				return singleMP4Inspection{}, &SingleMP4ValidationError{Reason: "视频轨缺少有效宽高"}
			}
			if w > maxWidth {
				maxWidth = w
			}
			if h > maxHeight {
				maxHeight = h
			}
			vDur := float64(trak.Mdia.Mdhd.Duration) / float64(trak.Mdia.Mdhd.Timescale)
			if vDur <= 0 {
				return singleMP4Inspection{}, &SingleMP4ValidationError{Reason: "视频轨时长必须大于 0"}
			}
			if vDur > durationSec {
				durationSec = vDur
			}
			videoTracks++

		case "soun":
			if trak.Mdia.Mdhd == nil || trak.Mdia.Mdhd.Timescale == 0 {
				return singleMP4Inspection{}, &SingleMP4ValidationError{Reason: "音频轨缺少有效时间刻度"}
			}
			if stbl.Stsd == nil || len(stbl.Stsd.Children) == 0 {
				return singleMP4Inspection{}, &SingleMP4ValidationError{Reason: "音频轨缺少 stsd 样本描述"}
			}
			if countProgressiveSamples(stbl) == 0 {
				return singleMP4Inspection{}, &SingleMP4ValidationError{Reason: "音频轨没有样本"}
			}
			if err := validateChunkOffsetsInMdat(stbl, mdatSpans, true); err != nil {
				return singleMP4Inspection{}, err
			}
			aDur := float64(trak.Mdia.Mdhd.Duration) / float64(trak.Mdia.Mdhd.Timescale)
			if aDur <= 0 {
				return singleMP4Inspection{}, &SingleMP4ValidationError{Reason: "音频轨时长必须大于 0"}
			}
			if aDur > durationSec {
				durationSec = aDur
			}

		default:
			if err := validateChunkOffsetsInMdat(stbl, mdatSpans, false); err != nil {
				return singleMP4Inspection{}, err
			}
		}
	}

	if videoTracks == 0 {
		return singleMP4Inspection{}, &SingleMP4ValidationError{Reason: "MP4 缺少有效视频轨"}
	}
	if durationSec <= 0 {
		return singleMP4Inspection{}, &SingleMP4ValidationError{Reason: "MP4 总时长必须大于 0"}
	}
	if expectedDurationSec > 0 && math.Abs(durationSec-expectedDurationSec) > 2.0 {
		return singleMP4Inspection{}, &SingleMP4ValidationError{
			Reason: fmt.Sprintf("MP4 时长 %.2fs 与预期 %.2fs 误差超过 2 秒", durationSec, expectedDurationSec),
		}
	}

	// Relocate whenever moov appears after any mdat box.
	needsReloc := moovIdx > firstMdatIdx || (lastMdatIdx >= 0 && moovIdx > lastMdatIdx)
	return singleMP4Inspection{
		moovIdx:      moovIdx,
		firstMdatIdx: firstMdatIdx,
		needsReloc:   needsReloc,
		width:        maxWidth,
		height:       maxHeight,
		durationSec:  durationSec,
	}, nil
}

func checkTrackNotEncrypted(moov *mp4.MoovBox, trak *mp4.TrakBox, trackID uint32) error {
	if trackID > 0 && moov.IsEncrypted(trackID) {
		return &EncryptedMediaError{
			TrackID: trackID,
			BoxType: "sinf",
			Reason:  "轨道包含加密保护描述",
		}
	}
	if trak.Mdia == nil || trak.Mdia.Minf == nil || trak.Mdia.Minf.Stbl == nil {
		return nil
	}
	stbl := trak.Mdia.Minf.Stbl
	for _, ch := range stbl.Children {
		switch ch.Type() {
		case "senc", "saiz", "saio":
			return &EncryptedMediaError{
				TrackID: trackID,
				BoxType: ch.Type(),
				Reason:  fmt.Sprintf("轨道包含 CENC %s 加密元数据", ch.Type()),
			}
		}
	}
	if stbl.Stsd == nil {
		return nil
	}
	for _, entry := range stbl.Stsd.Children {
		t := entry.Type()
		switch t {
		case "encv", "enca", "encs", "encf", "enct", "encm":
			return &EncryptedMediaError{
				TrackID: trackID,
				BoxType: t,
				Reason:  fmt.Sprintf("样本描述使用加密编码类型 %s", t),
			}
		}
		if vse, ok := entry.(*mp4.VisualSampleEntryBox); ok && vse.Sinf != nil {
			return &EncryptedMediaError{TrackID: trackID, BoxType: "sinf", Reason: "视频样本项含 sinf 加密保护"}
		}
		if ase, ok := entry.(*mp4.AudioSampleEntryBox); ok && ase.Sinf != nil {
			return &EncryptedMediaError{TrackID: trackID, BoxType: "sinf", Reason: "音频样本项含 sinf 加密保护"}
		}
	}
	return nil
}

func countProgressiveSamples(stbl *mp4.StblBox) uint32 {
	if stbl == nil {
		return 0
	}
	if stbl.Stsz != nil && stbl.Stsz.SampleNumber > 0 {
		return stbl.Stsz.SampleNumber
	}
	if stbl.Stts != nil {
		var total uint32
		for _, c := range stbl.Stts.SampleCount {
			total += c
		}
		return total
	}
	return 0
}

func extractVideoTrackDimensions(trak *mp4.TrakBox) (int, int) {
	var w, h int
	if trak.Tkhd != nil {
		w = int(uint32(trak.Tkhd.Width) >> 16)
		h = int(uint32(trak.Tkhd.Height) >> 16)
	}
	if (w <= 0 || h <= 0) && trak.Mdia != nil && trak.Mdia.Minf != nil && trak.Mdia.Minf.Stbl != nil && trak.Mdia.Minf.Stbl.Stsd != nil {
		for _, ch := range trak.Mdia.Minf.Stbl.Stsd.Children {
			if vse, ok := ch.(*mp4.VisualSampleEntryBox); ok {
				if w <= 0 {
					w = int(vse.Width)
				}
				if h <= 0 {
					h = int(vse.Height)
				}
			}
		}
	}
	return w, h
}

func validateChunkOffsetsInMdat(stbl *mp4.StblBox, spans []mdatByteSpan, requireNonEmpty bool) error {
	hasStco := stbl.Stco != nil && len(stbl.Stco.ChunkOffset) > 0
	hasCo64 := stbl.Co64 != nil && len(stbl.Co64.ChunkOffset) > 0
	if !hasStco && !hasCo64 {
		if requireNonEmpty {
			return &SingleMP4ValidationError{Reason: "媒体轨缺少 stco/co64 块偏移表"}
		}
		return nil
	}
	inAnySpan := func(off uint64) bool {
		for _, sp := range spans {
			if off >= sp.payloadStart && off < sp.payloadEnd {
				return true
			}
		}
		return false
	}
	if hasStco {
		for _, off := range stbl.Stco.ChunkOffset {
			if !inAnySpan(uint64(off)) {
				return &SingleMP4ValidationError{Reason: fmt.Sprintf("stco 块偏移 %d 超出 mdat 载荷范围", off)}
			}
		}
	}
	if hasCo64 {
		for _, off := range stbl.Co64.ChunkOffset {
			if !inAnySpan(off) {
				return &SingleMP4ValidationError{Reason: fmt.Sprintf("co64 块偏移 %d 超出 mdat 载荷范围", off)}
			}
		}
	}
	return nil
}

// relocateMoovOnDisk moves moov immediately before the first mdat box while streaming lazy
// mdat payloads directly on disk in fixed 256 KiB chunks. If any step fails, the temporary
// file is removed and the original file at path remains untouched.
func relocateMoovOnDisk(
	ctx context.Context,
	source *os.File,
	sourceClosed *bool,
	parsed *mp4.File,
	moovIdx int,
	firstMdatIdx int,
	path string,
) error {
	moov := parsed.Moov
	origMoovStart := moov.StartPos
	origMoovSize := moov.Size()
	origMoovEnd := origMoovStart + origMoovSize
	firstMdat, ok := parsed.Children[firstMdatIdx].(*mp4.MdatBox)
	if !ok {
		return &SingleMP4ValidationError{Reason: "首个 mdat 类型异常"}
	}
	insertPos := firstMdat.StartPos

	shiftOffset := func(orig uint64, newMoovSize uint64) (uint64, error) {
		if orig >= insertPos && orig < origMoovStart {
			return orig + newMoovSize, nil
		}
		if orig >= origMoovEnd {
			shifted := int64(orig) + (int64(newMoovSize) - int64(origMoovSize))
			if shifted < 0 {
				return 0, &SingleMP4ValidationError{Reason: "重排后块偏移出现负值"}
			}
			return uint64(shifted), nil
		}
		return 0, &SingleMP4ValidationError{Reason: fmt.Sprintf("块偏移 %d 位于预期 mdat 区间之外", orig)}
	}

	candMoovSize := moov.Size()
	needCo64 := false
	for _, trak := range moov.Traks {
		if trak == nil || trak.Mdia == nil || trak.Mdia.Minf == nil || trak.Mdia.Minf.Stbl == nil {
			continue
		}
		stco := trak.Mdia.Minf.Stbl.Stco
		if stco == nil {
			continue
		}
		for _, off := range stco.ChunkOffset {
			shifted, err := shiftOffset(uint64(off), candMoovSize)
			if err != nil {
				return err
			}
			if shifted > math.MaxUint32 {
				needCo64 = true
			}
		}
	}

	if needCo64 {
		for _, trak := range moov.Traks {
			if trak == nil || trak.Mdia == nil || trak.Mdia.Minf == nil || trak.Mdia.Minf.Stbl == nil {
				continue
			}
			upgradeTrackStcoToCo64(trak.Mdia.Minf.Stbl)
		}
	}

	finalMoovSize := moov.Size()
	for _, trak := range moov.Traks {
		if trak == nil || trak.Mdia == nil || trak.Mdia.Minf == nil || trak.Mdia.Minf.Stbl == nil {
			continue
		}
		stbl := trak.Mdia.Minf.Stbl
		if stbl.Stco != nil {
			for i, off := range stbl.Stco.ChunkOffset {
				shifted, err := shiftOffset(uint64(off), finalMoovSize)
				if err != nil {
					return err
				}
				if shifted > math.MaxUint32 {
					return &SingleMP4ValidationError{Reason: "stco 偏移超出 32 位上限"}
				}
				stbl.Stco.ChunkOffset[i] = uint32(shifted)
			}
		}
		if stbl.Co64 != nil {
			for i, off := range stbl.Co64.ChunkOffset {
				shifted, err := shiftOffset(off, finalMoovSize)
				if err != nil {
					return err
				}
				stbl.Co64.ChunkOffset[i] = shifted
			}
		}
	}
	if moov.Size() != finalMoovSize {
		return &SingleMP4ValidationError{Reason: "重算偏移后 moov 长度不一致"}
	}

	out, err := os.CreateTemp(filepath.Dir(path), ".trace-singlemp4-*.part")
	if err != nil {
		return err
	}
	tempPath := out.Name()
	outClosed := false
	defer func() {
		if !outClosed {
			_ = out.Close()
		}
		if tempPath != "" {
			_ = os.Remove(tempPath)
		}
	}()

	bw := bufio.NewWriterSize(contextIO{File: out, ctx: ctx}, singleMP4StreamBufSize)
	copyBuf := make([]byte, singleMP4StreamBufSize)

	writeBox := func(box mp4.Box) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := box.Encode(bw); err != nil {
			return err
		}
		if mdat, ok := box.(*mp4.MdatBox); ok && mdat.IsLazy() {
			payloadLen := int64(mdat.Size() - mdat.HeaderSize())
			sec := io.NewSectionReader(contextIO{File: source, ctx: ctx}, int64(mdat.PayloadAbsoluteOffset()), payloadLen)
			if _, err := io.CopyBuffer(bw, sec, copyBuf); err != nil {
				return err
			}
		}
		return nil
	}

	for idx := 0; idx < firstMdatIdx; idx++ {
		if idx == moovIdx {
			continue
		}
		if err := writeBox(parsed.Children[idx]); err != nil {
			return err
		}
	}
	if err := moov.Encode(bw); err != nil {
		return err
	}
	for idx := firstMdatIdx; idx < len(parsed.Children); idx++ {
		if idx == moovIdx {
			continue
		}
		if err := writeBox(parsed.Children[idx]); err != nil {
			return err
		}
	}

	if err := bw.Flush(); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	outClosed = true
	if err := out.Close(); err != nil {
		return err
	}

	// Close source before atomic rename so Windows file handles do not block replacement.
	*sourceClosed = true
	if err := source.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	tempPath = ""
	return nil
}

func upgradeTrackStcoToCo64(stbl *mp4.StblBox) {
	if stbl == nil || stbl.Stco == nil {
		return
	}
	co64 := &mp4.Co64Box{
		Version:     stbl.Stco.Version,
		Flags:       stbl.Stco.Flags,
		ChunkOffset: make([]uint64, len(stbl.Stco.ChunkOffset)),
	}
	for i, off := range stbl.Stco.ChunkOffset {
		co64.ChunkOffset[i] = uint64(off)
	}
	for i, ch := range stbl.Children {
		if ch == stbl.Stco {
			stbl.Children[i] = co64
		}
	}
	stbl.Stco = nil
	stbl.Co64 = co64
}
