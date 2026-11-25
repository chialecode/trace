package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"math/bits"
	"os"
	"path/filepath"

	"github.com/Eyevinn/mp4ff/mp4"
)

// MuxTrackSummary describes the tracks and structural invariants inside a muxed MP4 file.
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
// into a standard progressive (moov-before-mdat) MP4 file at outPath using pure-Go mp4ff.
func MuxDASHFiles(videoPath, audioPath, outPath string) (MuxTrackSummary, error) {
	return MuxDASHFilesContext(context.Background(), videoPath, audioPath, outPath)
}

// DefragmentFMP4Bytes converts a fragmented MP4 byte slice into a progressive MP4 file at outPath.
// Returns an error if defragmentation fails (never silently writes fragmented bytes).
func DefragmentFMP4Bytes(raw []byte, outPath string) (MuxTrackSummary, error) {
	if err := os.MkdirAll(filepath.Dir(outPath), 0o700); err != nil {
		return MuxTrackSummary{}, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(outPath), ".trace-defrag-*.part")
	if err != nil {
		return MuxTrackSummary{}, err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return MuxTrackSummary{}, err
	}
	return writeProgressiveDisk(context.Background(), tmp, tmpPath, outPath)
}

// ensureTrackElst adds a single-entry edit list when trak does not already carry one.
// It uses elst version 1 (64-bit) whenever SegmentDuration or MediaTime exceeds 32-bit bounds
// and never overwrites an existing edit list (e.g. B-frame CTS delay or empty edit).
func ensureTrackElst(moov *mp4.MoovBox, trak *mp4.TrakBox, mediaDur uint64, mediaTime int64) bool {
	if trak == nil {
		return false
	}
	if trak.Edts != nil && len(trak.Edts.Elst) > 0 {
		return false
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
		hi, lo := bits.Mul64(mediaDur, uint64(movieTS))
		if hi < uint64(trackTS) {
			segDur, _ = bits.Div64(hi, lo, uint64(trackTS))
		} else {
			segDur = math.MaxUint64
		}
	}
	var version byte
	if segDur > math.MaxUint32 || mediaTime > math.MaxInt32 || mediaTime < math.MinInt32 {
		version = 1
	}
	elst := &mp4.ElstBox{
		Version: version,
		Entries: []mp4.ElstEntry{
			{
				SegmentDuration:   segDur,
				MediaTime:         mediaTime,
				MediaRateInteger:  1,
				MediaRateFraction: 0,
			},
		},
	}
	edts := &mp4.EdtsBox{Elst: []*mp4.ElstBox{elst}}
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
	return true
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

// InspectMP4File parses an MP4 file on disk and returns its track count, handlers, codecs, dimensions, and box layout invariants.
func InspectMP4File(path string) (MuxTrackSummary, error) {
	return InspectMP4FileContext(context.Background(), path)
}

func InspectMP4FileContext(ctx context.Context, path string) (MuxTrackSummary, error) {
	return inspectMP4File(ctx, path, true)
}

// InspectMP4StructureContext validates MP4 box layout, track metadata, file
// length, and duration lazily (DecModeLazyMdat) without reading mdat sample
// bytes or computing SHA-256. Callers that already hashed the output stream
// while writing populate summary.SHA256 directly.
func InspectMP4StructureContext(ctx context.Context, path string) (MuxTrackSummary, error) {
	return inspectMP4File(ctx, path, false)
}

func inspectMP4File(ctx context.Context, path string, computeSHA256 bool) (MuxTrackSummary, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	f, err := os.Open(path)
	if err != nil {
		return MuxTrackSummary{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return MuxTrackSummary{}, err
	}
	var shaHex string
	if computeSHA256 {
		hash := sha256.New()
		if _, err := io.CopyBuffer(hash, contextIO{File: f, ctx: ctx}, make([]byte, 1<<20)); err != nil {
			return MuxTrackSummary{}, err
		}
		shaHex = hex.EncodeToString(hash.Sum(nil))
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return MuxTrackSummary{}, err
		}
	}
	parsed, err := mp4.DecodeFile(contextIO{File: f, ctx: ctx}, mp4.WithDecodeMode(mp4.DecModeLazyMdat), mp4.WithDecodeFlags(mp4.DecStartOnMoof))
	if err != nil {
		return MuxTrackSummary{}, err
	}
	if parsed == nil {
		return MuxTrackSummary{}, fmt.Errorf("download: mp4 file %s decoded to nil", path)
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
