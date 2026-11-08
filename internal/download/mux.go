package download

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/chialecode/trace/internal/storage"
)

// MuxTrackSummary describes the tracks and structural invariants inside a muxed MP4 file (C17, PR-2).
type MuxTrackSummary struct {
	TrackCount      int      `json:"trackCount"`
	Handlers        []string `json:"handlers"`
	Codecs          []string `json:"codecs"`
	Width           int      `json:"width,omitempty"`
	Height          int      `json:"height,omitempty"`
	FileBytes       int64    `json:"fileBytes"`
	SHA256          string   `json:"sha256,omitempty"`
	MoovBeforeMdat  bool     `json:"moovBeforeMdat"`
	HasElst         bool     `json:"hasElst"`
	Interleaved     bool     `json:"interleaved"`
	DurationSec     float64  `json:"durationSec"`
	MaxTrackDiffSec float64  `json:"maxTrackDiffSec"`
}

// MuxDASHFiles remuxes a Bilibili DASH video .m4s file and an optional audio .m4s file
// into a standard progressive (moov-before-mdat) MP4 file at outPath using pure-Go mp4ff (C17, PR-2).
func MuxDASHFiles(videoPath, audioPath, outPath string) (MuxTrackSummary, error) {
	return MuxDASHFilesContext(context.Background(), videoPath, audioPath, outPath)
}

// DefragmentFMP4Bytes converts a fragmented MP4 byte slice into a progressive MP4 file at outPath.
// Returns an error if defragmentation fails (never silently writes fragmented bytes).
func DefragmentFMP4Bytes(raw []byte, outPath string) (MuxTrackSummary, error) {
	return writeDefragmentedOutput(raw, outPath)
}

func ensureTrackElst(moov *mp4.MoovBox, trak *mp4.TrakBox, mediaDur uint64) {
	if trak == nil {
		return
	}
	if trak.Edts != nil && len(trak.Edts.Elst) > 0 {
		return
	}
	movieTS := uint32(1000)
	if moov != nil && moov.Mvhd != nil && moov.Mvhd.Timescale > 0 {
		movieTS = moov.Mvhd.Timescale
	}
	trackTS := movieTS
	if trak.Mdia != nil && trak.Mdia.Mdhd != nil && trak.Mdia.Mdhd.Timescale > 0 {
		trackTS = trak.Mdia.Mdhd.Timescale
	}
	segDur := mediaDur
	if trackTS > 0 && movieTS > 0 && trackTS != movieTS {
		segDur = (mediaDur * uint64(movieTS)) / uint64(trackTS)
	}
	edts := &mp4.EdtsBox{}
	elst := &mp4.ElstBox{
		Version: 0,
		Entries: []mp4.ElstEntry{
			{
				SegmentDuration:   segDur,
				MediaTime:         0,
				MediaRateInteger:  1,
				MediaRateFraction: 0,
			},
		},
	}
	edts.AddChild(elst)
	// Insert EdtsBox into trak (preserving Tkhd first).
	trak.Edts = edts
	var newChildren []mp4.Box
	inserted := false
	for _, ch := range trak.Children {
		newChildren = append(newChildren, ch)
		if ch.Type() == "tkhd" && !inserted {
			newChildren = append(newChildren, edts)
			inserted = true
		}
	}
	if !inserted {
		newChildren = append([]mp4.Box{edts}, newChildren...)
	}
	trak.Children = newChildren
}

func setSingleTrackID(f *mp4.File, trackID uint32) {
	if f.Init != nil && f.Init.Moov != nil {
		for _, trak := range f.Init.Moov.Traks {
			if trak.Tkhd != nil {
				trak.Tkhd.TrackID = trackID
			}
		}
		if f.Init.Moov.Mvex != nil {
			for _, trex := range f.Init.Moov.Mvex.Trexs {
				trex.TrackID = trackID
			}
		}
	}
	for _, seg := range f.Segments {
		for _, frag := range seg.Fragments {
			if frag.Moof != nil {
				for _, traf := range frag.Moof.Trafs {
					if traf.Tfhd != nil {
						traf.Tfhd.TrackID = trackID
					}
				}
			}
		}
	}
}

func writeDefragmentedOutput(combinedFragmented []byte, outPath string) (MuxTrackSummary, error) {
	rs := bytes.NewReader(combinedFragmented)
	lazyFile, err := mp4.DecodeFile(rs, mp4.WithDecodeMode(mp4.DecModeLazyMdat), mp4.WithDecodeFlags(mp4.DecStartOnMoof))
	if err != nil {
		return MuxTrackSummary{}, fmt.Errorf("download: decode combined fragmented stream: %w", err)
	}

	var outBuf bytes.Buffer
	if dErr := mp4.Defragment(lazyFile, rs, &outBuf); dErr != nil {
		return MuxTrackSummary{}, fmt.Errorf("download: defragment fmp4: %w", dErr)
	}

	finalBytes, err := injectElstIntoProgressiveMP4(outBuf.Bytes())
	if err != nil {
		finalBytes = outBuf.Bytes()
	}

	if err := storage.WriteFileAtomic(outPath, finalBytes, 0o600); err != nil {
		return MuxTrackSummary{}, err
	}
	summary, err := InspectMP4File(outPath)
	if err != nil {
		return MuxTrackSummary{}, err
	}
	if !summary.MoovBeforeMdat {
		return summary, fmt.Errorf("download: output mp4 moov is not before mdat")
	}
	if summary.MaxTrackDiffSec > 2.0 {
		return summary, fmt.Errorf("download: output mp4 track duration mismatch %.2fs > 2s", summary.MaxTrackDiffSec)
	}
	return summary, nil
}

func injectElstIntoProgressiveMP4(defragBytes []byte) ([]byte, error) {
	progFile, err := mp4.DecodeFile(bytes.NewReader(defragBytes))
	if err != nil {
		return nil, err
	}
	if progFile.Moov == nil {
		return defragBytes, nil
	}
	oldMoovSize := progFile.Moov.Size()
	modified := false
	for _, trak := range progFile.Moov.Traks {
		if trak.Edts == nil || len(trak.Edts.Elst) == 0 {
			var mediaDur uint64
			if trak.Mdia != nil && trak.Mdia.Mdhd != nil {
				mediaDur = trak.Mdia.Mdhd.Duration
			}
			ensureTrackElst(progFile.Moov, trak, mediaDur)
			modified = true
		}
	}
	if !modified {
		return defragBytes, nil
	}
	newMoovSize := progFile.Moov.Size()
	delta := int64(newMoovSize) - int64(oldMoovSize)
	if delta != 0 {
		for _, trak := range progFile.Moov.Traks {
			if trak.Mdia == nil || trak.Mdia.Minf == nil || trak.Mdia.Minf.Stbl == nil {
				continue
			}
			stbl := trak.Mdia.Minf.Stbl
			if stbl.Stco != nil {
				for i := range stbl.Stco.ChunkOffset {
					stbl.Stco.ChunkOffset[i] = uint32(int64(stbl.Stco.ChunkOffset[i]) + delta)
				}
			}
			if stbl.Co64 != nil {
				for i := range stbl.Co64.ChunkOffset {
					stbl.Co64.ChunkOffset[i] = uint64(int64(stbl.Co64.ChunkOffset[i]) + delta)
				}
			}
		}
	}
	var finalBuf bytes.Buffer
	if err := progFile.Encode(&finalBuf); err != nil {
		return nil, err
	}
	return finalBuf.Bytes(), nil
}

// InspectMP4File parses an MP4 file on disk and returns its track count, handlers, codecs, dimensions, and box layout invariants.
func InspectMP4File(path string) (MuxTrackSummary, error) {
	return InspectMP4FileContext(context.Background(), path)
}

func InspectMP4FileContext(ctx context.Context, path string) (MuxTrackSummary, error) {
	f, err := os.Open(path)
	if err != nil {
		return MuxTrackSummary{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return MuxTrackSummary{}, err
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, contextIO{File: f, ctx: ctx}); err != nil {
		return MuxTrackSummary{}, err
	}
	shaHex := hex.EncodeToString(hash.Sum(nil))
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return MuxTrackSummary{}, err
	}
	parsed, err := mp4.DecodeFile(contextIO{File: f, ctx: ctx}, mp4.WithDecodeMode(mp4.DecModeLazyMdat), mp4.WithDecodeFlags(mp4.DecStartOnMoof))
	if err != nil || parsed == nil {
		return MuxTrackSummary{}, err
	}
	var declared uint64
	for _, box := range parsed.Children {
		declared += box.Size()
	}
	if declared != uint64(info.Size()) {
		return MuxTrackSummary{}, fmt.Errorf("download: MP4 box lengths do not match file length")
	}
	moov := parsed.Moov
	if moov == nil && parsed.Init != nil {
		moov = parsed.Init.Moov
	}
	if moov == nil {
		return MuxTrackSummary{}, fmt.Errorf("download: mp4 file %s has no moov box", path)
	}

	moovIdx := -1
	mdatIdx := -1
	hasMoof := false
	for idx, ch := range parsed.Children {
		switch ch.Type() {
		case "moov":
			if moovIdx == -1 {
				moovIdx = idx
			}
		case "mdat":
			if mdatIdx == -1 {
				mdatIdx = idx
			}
		case "moof":
			hasMoof = true
		}
	}

	summary := MuxTrackSummary{
		TrackCount:     len(moov.Traks),
		FileBytes:      info.Size(),
		SHA256:         shaHex,
		MoovBeforeMdat: moovIdx >= 0 && mdatIdx > moovIdx && !hasMoof && !parsed.IsFragmented(),
		HasElst:        len(moov.Traks) > 0,
	}

	if moov.Mvhd != nil && moov.Mvhd.Timescale > 0 {
		summary.DurationSec = float64(moov.Mvhd.Duration) / float64(moov.Mvhd.Timescale)
	}

	var trackDurs []float64
	var trackFirstOffsets []uint64
	var trackMultiChunks int

	for _, trak := range moov.Traks {
		if trak.Edts == nil || len(trak.Edts.Elst) == 0 {
			summary.HasElst = false
		}
		if trak.Mdia != nil && trak.Mdia.Hdlr != nil {
			summary.Handlers = append(summary.Handlers, trak.Mdia.Hdlr.HandlerType)
		}
		if trak.Mdia != nil && trak.Mdia.Mdhd != nil && trak.Mdia.Mdhd.Timescale > 0 {
			d := float64(trak.Mdia.Mdhd.Duration) / float64(trak.Mdia.Mdhd.Timescale)
			trackDurs = append(trackDurs, d)
			if summary.DurationSec == 0 || d > summary.DurationSec {
				summary.DurationSec = d
			}
		}
		if trak.Tkhd != nil {
			w := int(uint32(trak.Tkhd.Width) >> 16)
			h := int(uint32(trak.Tkhd.Height) >> 16)
			if w > summary.Width {
				summary.Width = w
			}
			if h > summary.Height {
				summary.Height = h
			}
		}
		if trak.Mdia != nil && trak.Mdia.Minf != nil && trak.Mdia.Minf.Stbl != nil {
			stbl := trak.Mdia.Minf.Stbl
			if stbl.Stsd != nil {
				for _, child := range stbl.Stsd.Children {
					summary.Codecs = append(summary.Codecs, child.Type())
				}
			}
			if stbl.Stco != nil && len(stbl.Stco.ChunkOffset) > 0 {
				trackFirstOffsets = append(trackFirstOffsets, uint64(stbl.Stco.ChunkOffset[0]))
				if len(stbl.Stco.ChunkOffset) > 1 {
					trackMultiChunks++
				}
			} else if stbl.Co64 != nil && len(stbl.Co64.ChunkOffset) > 0 {
				trackFirstOffsets = append(trackFirstOffsets, stbl.Co64.ChunkOffset[0])
				if len(stbl.Co64.ChunkOffset) > 1 {
					trackMultiChunks++
				}
			}
		}
	}

	if len(trackDurs) >= 2 {
		minD, maxD := trackDurs[0], trackDurs[0]
		for _, d := range trackDurs[1:] {
			if d < minD {
				minD = d
			}
			if d > maxD {
				maxD = d
			}
		}
		summary.MaxTrackDiffSec = math.Abs(maxD - minD)
	}
	if len(moov.Traks) == 1 {
		summary.Interleaved = len(trackFirstOffsets) == 1
	} else if len(trackFirstOffsets) >= 2 {
		// In interleaved progressive MP4, each 1s fragment becomes a chunk in stco/co64.
		summary.Interleaved = trackFirstOffsets[0] != trackFirstOffsets[1] && (trackMultiChunks > 0 || summary.DurationSec <= 1.5)
	}

	return summary, nil
}
