package player

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/Eyevinn/mp4ff/mp4"
)

// ErrLivePreviewUnsupported indicates that the recording stream or container does not support live preview.
var ErrLivePreviewUnsupported = errors.New("这场录制暂不支持实时预览")

// LivePreviewOptions configures opening and following a growing fragmented MP4 (.part) file.
type LivePreviewOptions struct {
	// IsActive reports whether the recording session is still actively writing to this .part file.
	// When nil, the file is treated as active until Close is called or the file disappears from disk.
	IsActive func() bool

	// CommittedBytes optionally returns an upper bound of flushed bytes in the .part file.
	// When nil or <= 0, complete fMP4 box boundaries (ftyp+moov and moof+mdat) on disk are used.
	CommittedBytes func() int64

	// PollInterval sets how often the reader checks for newly appended fragments (default 80ms).
	PollInterval time.Duration

	// InitialWait bounds how long OpenLivePreview waits for init + first fragment (default 4.5s).
	InitialWait time.Duration

	// TailWindow bounds how close to the live edge playback starts when opening an existing .part
	// (default 4s; negative starts from the beginning of the .part file).
	TailWindow time.Duration
}

// LiveDecoder is implemented by growing live-preview decoders.
type LiveDecoder interface {
	MediaDecoder
	IsLive() bool
	LiveEnded() bool
	AvailableDuration() time.Duration
}

type liveAborter interface {
	abort()
}

// OpenLivePreview opens a local growing fragmented MP4 (.part) file for real-time preview.
// It never opens any network connection.
func OpenLivePreview(ctx context.Context, path string, opts LivePreviewOptions) (MediaDecoder, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = 80 * time.Millisecond
	}
	if opts.InitialWait <= 0 {
		opts.InitialWait = 4500 * time.Millisecond
	}
	if opts.TailWindow == 0 {
		opts.TailWindow = 4 * time.Second
	}
	return openLivePreviewPlatform(ctx, path, opts)
}

type fmp4FragMeta struct {
	fileOff     int64
	length      int64
	hasVideo    bool
	hasAudio    bool
	syncVideo   bool
	startPTS    time.Duration
	endPTS      time.Duration
	hasTimeline bool
}

// liveFMP4Source validates and follows complete top-level fMP4 boxes in a growing .part file.
type liveFMP4Source struct {
	path string
	opts LivePreviewOptions

	initEnd        int64
	width          int
	height         int
	fps            float64
	hasVideo       bool
	hasAudio       bool
	sampleRate     int
	channels       int
	videoTrackID   uint32
	audioTrackID   uint32
	trackScales    map[uint32]uint32
	trexMap        map[uint32]*mp4.TrexBox
	startFileOff   int64
	originPTS      time.Duration
	originPTSKnown bool

	mu               sync.Mutex
	file             *os.File
	scannedOff       int64
	committedFragEnd int64
	latestEndPTS     time.Duration
	fragCount        int
	ended            bool
	closed           bool
	listeners        []func(committedEnd int64, ended bool, closed bool)

	stopPoll chan struct{}
	pollDone chan struct{}
}

func openLiveFMP4Source(ctx context.Context, path string, opts LivePreviewOptions) (*liveFMP4Source, error) {
	if path == "" {
		return nil, errors.New("player: empty live preview path")
	}
	deadline := time.Now().Add(opts.InitialWait)
	isActive := func() bool {
		if opts.IsActive != nil {
			return opts.IsActive()
		}
		return true
	}

	var f *os.File
	for {
		var err error
		f, err = openSharedReadFile(path)
		if err == nil {
			break
		}
		if !os.IsNotExist(err) {
			return nil, err
		}
		if !isActive() || time.Now().After(deadline) {
			return nil, fmt.Errorf("等待直播分片文件创建超时")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(opts.PollInterval):
		}
	}

	src := &liveFMP4Source{
		path:        path,
		opts:        opts,
		fps:         30.0,
		sampleRate:  48000,
		channels:    2,
		trackScales: make(map[uint32]uint32),
		trexMap:     make(map[uint32]*mp4.TrexBox),
		file:        f,
		stopPoll:    make(chan struct{}),
		pollDone:    make(chan struct{}),
	}

	// Wait for complete init segment (ftyp + moov) and at least one media fragment.
	var frags []fmp4FragMeta
	for {
		if err := ctx.Err(); err != nil {
			_ = f.Close()
			return nil, err
		}
		limit, statErr := src.safeDiskLimit(f)
		if statErr != nil {
			_ = f.Close()
			return nil, statErr
		}

		if src.initEnd == 0 && limit >= 16 {
			initEnd, supported, initReady, initErr := src.tryParseInit(f, limit)
			if !supported {
				_ = f.Close()
				if initErr != nil {
					return nil, fmt.Errorf("%w: %v", ErrLivePreviewUnsupported, initErr)
				}
				return nil, ErrLivePreviewUnsupported
			}
			if initErr != nil {
				_ = f.Close()
				return nil, initErr
			}
			if initReady {
				src.initEnd = initEnd
				src.scannedOff = initEnd
				src.committedFragEnd = initEnd
			}
		}

		if src.initEnd > 0 && limit > src.scannedOff {
			newFrags, unsupported := src.scanFragmentsFrom(f, limit)
			if unsupported {
				_ = f.Close()
				return nil, ErrLivePreviewUnsupported
			}
			frags = append(frags, newFrags...)
		}

		active := isActive()
		if src.hasInitialFragments(frags, active) {
			break
		}
		if !active {
			_ = f.Close()
			if src.initEnd == 0 || len(frags) == 0 {
				return nil, fmt.Errorf("直播录制尚未写入可预览的媒体分片")
			}
			break
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("等待首个直播分片写入超时")
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(opts.PollInterval):
		}
	}

	src.selectStartFragment(frags)
	if !isActive() {
		src.ended = true
	}

	go src.pollLoop()
	return src, nil
}

func (s *liveFMP4Source) hasInitialFragments(frags []fmp4FragMeta, active bool) bool {
	if s.initEnd <= 0 || len(frags) == 0 {
		return false
	}
	seenVideo := !s.hasVideo
	seenAudio := !s.hasAudio
	for _, fg := range frags {
		if fg.hasVideo {
			seenVideo = true
		}
		if fg.hasAudio {
			seenAudio = true
		}
	}
	if seenVideo && seenAudio {
		return true
	}
	return !active && (seenVideo || seenAudio)
}

func (s *liveFMP4Source) selectStartFragment(frags []fmp4FragMeta) {
	startIdx := 0
	if s.opts.TailWindow > 0 && len(frags) > 2 {
		latest := s.latestEndPTS
		cutoff := latest - s.opts.TailWindow
		best := -1
		seenAudioTail := !s.hasAudio
		for i := len(frags) - 1; i >= 0; i-- {
			fg := frags[i]
			if fg.hasAudio {
				seenAudioTail = true
			}
			if s.hasVideo && (!fg.hasVideo || !fg.syncVideo) {
				continue
			}
			if !seenAudioTail {
				continue
			}
			if best == -1 {
				best = i
			}
			if fg.hasTimeline && fg.startPTS <= cutoff {
				best = i
				break
			}
			best = i
		}
		if best > 0 {
			startIdx = best
		}
	}

	s.startFileOff = frags[startIdx].fileOff
	if frags[startIdx].hasTimeline {
		s.originPTS = frags[startIdx].startPTS
		s.originPTSKnown = true
	}
}

func (s *liveFMP4Source) safeDiskLimit(f *os.File) (int64, error) {
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	limit := info.Size()
	if s.opts.CommittedBytes != nil {
		if cb := s.opts.CommittedBytes(); cb > 0 && cb < limit {
			limit = cb
		}
	}
	return limit, nil
}

func readBoxHeaderAt(r io.ReaderAt, off int64, limit int64) (boxType string, boxSize int64, hdrLen int64, complete bool, valid bool) {
	if off+8 > limit {
		return "", 0, 0, false, true
	}
	var hdr [16]byte
	n, err := r.ReadAt(hdr[:8], off)
	if n < 8 {
		if err != nil && err != io.EOF {
			return "", 0, 0, false, false
		}
		return "", 0, 0, false, true
	}
	sz32 := binary.BigEndian.Uint32(hdr[0:4])
	boxType = string(hdr[4:8])
	for i := 0; i < 4; i++ {
		ch := boxType[i]
		if ch < 0x20 || ch > 0x7E {
			return "", 0, 0, false, false
		}
	}
	if sz32 == 1 {
		if off+16 > limit {
			return boxType, 0, 16, false, true
		}
		n2, _ := r.ReadAt(hdr[8:16], off+8)
		if n2 < 8 {
			return boxType, 0, 16, false, true
		}
		sz64 := binary.BigEndian.Uint64(hdr[8:16])
		if sz64 < 16 || sz64 > (1<<40) {
			return boxType, 0, 16, false, false
		}
		boxSize = int64(sz64)
		hdrLen = 16
	} else if sz32 == 0 {
		return boxType, 0, 8, false, false
	} else {
		if sz32 < 8 {
			return boxType, 0, 8, false, false
		}
		boxSize = int64(sz32)
		hdrLen = 8
	}
	return boxType, boxSize, hdrLen, off+boxSize <= limit, true
}

func (s *liveFMP4Source) tryParseInit(f *os.File, limit int64) (initEnd int64, supported bool, ready bool, err error) {
	off := int64(0)
	sawFtyp := false
	for off < limit {
		bType, bSize, _, complete, valid := readBoxHeaderAt(f, off, limit)
		if !valid {
			return 0, false, false, errors.New("非 MP4 容器头")
		}
		if bType == "" {
			return 0, true, false, nil
		}
		if off == 0 && bType != "ftyp" && bType != "styp" && bType != "moov" && bType != "free" && bType != "skip" {
			return 0, false, false, fmt.Errorf("不支持的容器头 %q", bType)
		}
		if !complete {
			if bSize > 32<<20 {
				return 0, false, false, errors.New("初始化分片过大")
			}
			return 0, true, false, nil
		}
		switch bType {
		case "ftyp", "styp":
			sawFtyp = true
			off += bSize
		case "free", "skip":
			off += bSize
		case "moov":
			initEnd = off + bSize
			if initEnd > 32<<20 {
				return 0, false, false, errors.New("moov 超过大小上限")
			}
			raw := make([]byte, initEnd)
			if _, rErr := f.ReadAt(raw, 0); rErr != nil && rErr != io.EOF {
				return 0, true, false, rErr
			}
			parsed, dErr := mp4.DecodeFile(bytes.NewReader(raw), mp4.WithDecodeFlags(mp4.DecStartOnMoof))
			if dErr != nil || parsed == nil {
				return 0, false, false, errors.New("解析初始化分片失败")
			}
			moov := parsed.Moov
			if parsed.Init != nil && parsed.Init.Moov != nil {
				moov = parsed.Init.Moov
			}
			if moov == nil || moov.Mvex == nil {
				return 0, false, false, errors.New("不是分片 MP4（缺少 mvex）")
			}
			for _, trex := range moov.Mvex.Trexs {
				if trex != nil {
					s.trexMap[trex.TrackID] = trex
				}
			}
			for _, trak := range moov.Traks {
				if trak == nil || trak.Tkhd == nil || trak.Mdia == nil || trak.Mdia.Hdlr == nil || trak.Mdia.Mdhd == nil {
					continue
				}
				tid := trak.Tkhd.TrackID
				scale := trak.Mdia.Mdhd.Timescale
				if scale == 0 {
					scale = 1000
				}
				s.trackScales[tid] = scale
				switch trak.Mdia.Hdlr.HandlerType {
				case "vide":
					if !s.hasVideo {
						s.hasVideo = true
						s.videoTrackID = tid
						w := int(trak.Tkhd.Width >> 16)
						h := int(trak.Tkhd.Height >> 16)
						if trak.Mdia.Minf != nil && trak.Mdia.Minf.Stbl != nil && trak.Mdia.Minf.Stbl.Stsd != nil {
							stsd := trak.Mdia.Minf.Stbl.Stsd
							if stsd.AvcX != nil {
								if stsd.AvcX.Width > 0 {
									w = int(stsd.AvcX.Width)
								}
								if stsd.AvcX.Height > 0 {
									h = int(stsd.AvcX.Height)
								}
							} else if stsd.HvcX != nil {
								if stsd.HvcX.Width > 0 {
									w = int(stsd.HvcX.Width)
								}
								if stsd.HvcX.Height > 0 {
									h = int(stsd.HvcX.Height)
								}
							}
						}
						if w <= 0 {
							w = 640
						}
						if h <= 0 {
							h = 360
						}
						s.width = w
						s.height = h
					}
				case "soun":
					if !s.hasAudio {
						s.hasAudio = true
						s.audioTrackID = tid
						sr := int(scale)
						ch := 2
						if trak.Mdia.Minf != nil && trak.Mdia.Minf.Stbl != nil && trak.Mdia.Minf.Stbl.Stsd != nil && trak.Mdia.Minf.Stbl.Stsd.Mp4a != nil {
							mp4a := trak.Mdia.Minf.Stbl.Stsd.Mp4a
							if mp4a.SampleRate > 0 {
								sr = int(mp4a.SampleRate)
							}
							if mp4a.ChannelCount > 0 {
								ch = int(mp4a.ChannelCount)
							}
						}
						if sr <= 0 {
							sr = 48000
						}
						if ch <= 0 {
							ch = 2
						}
						s.sampleRate = sr
						s.channels = ch
					}
				}
			}
			if !s.hasVideo && !s.hasAudio {
				return 0, false, false, errors.New("初始化分片缺少有效音视频轨")
			}
			return initEnd, true, true, nil
		case "mdat", "moof":
			if !sawFtyp {
				return 0, false, false, errors.New("缺少初始化分片")
			}
			return 0, false, false, errors.New("分片前缺少 moov")
		default:
			off += bSize
		}
	}
	return 0, true, false, nil
}

func (s *liveFMP4Source) scanFragmentsFrom(f *os.File, limit int64) (added []fmp4FragMeta, unsupported bool) {
	off := s.scannedOff
	for off < limit {
		bType, bSize, _, complete, valid := readBoxHeaderAt(f, off, limit)
		if !valid {
			return added, off == s.initEnd && s.fragCount == 0
		}
		if bType == "" || !complete {
			break
		}
		switch bType {
		case "free", "skip", "styp", "sidx", "prft", "emsg":
			off += bSize
			s.scannedOff = off
			s.committedFragEnd = off
		case "mfra":
			off += bSize
			s.scannedOff = off
		case "moof":
			// Ensure the companion mdat (plus any tiny intervening box) is also completely flushed on disk.
			cur := off + bSize
			foundMdat := false
			var unitEnd int64
			for cur < limit {
				nt, nsz, _, nComp, nValid := readBoxHeaderAt(f, cur, limit)
				if !nValid || nt == "" || !nComp {
					break
				}
				if nt == "mdat" {
					foundMdat = true
					unitEnd = cur + nsz
					break
				}
				if nt == "free" || nt == "skip" || nt == "prft" || nt == "sidx" {
					cur += nsz
					continue
				}
				break
			}
			if !foundMdat {
				return added, false
			}
			meta := s.inspectMoof(f, off, bSize, unitEnd-off)
			added = append(added, meta)
			s.fragCount++
			if meta.hasTimeline && meta.endPTS > s.latestEndPTS {
				s.latestEndPTS = meta.endPTS
			}
			off = unitEnd
			s.scannedOff = off
			s.committedFragEnd = off
		default:
			off += bSize
			s.scannedOff = off
			s.committedFragEnd = off
		}
	}
	return added, false
}

func (s *liveFMP4Source) inspectMoof(f *os.File, moofOff int64, moofSize int64, unitLen int64) fmp4FragMeta {
	meta := fmp4FragMeta{
		fileOff: moofOff,
		length:  unitLen,
	}
	if moofSize <= 0 || moofSize > 4<<20 {
		return meta
	}
	raw := make([]byte, moofSize)
	if n, _ := f.ReadAt(raw, moofOff); int64(n) < moofSize {
		return meta
	}
	box, err := mp4.DecodeBox(0, bytes.NewReader(raw))
	if err != nil {
		return meta
	}
	moof, ok := box.(*mp4.MoofBox)
	if !ok || moof == nil {
		return meta
	}
	for _, traf := range moof.Trafs {
		if traf == nil || traf.Tfhd == nil {
			continue
		}
		tid := traf.Tfhd.TrackID
		scale := s.trackScales[tid]
		if scale == 0 {
			scale = 1000
		}
		if tid == s.videoTrackID {
			meta.hasVideo = true
		}
		if tid == s.audioTrackID {
			meta.hasAudio = true
		}
		var startTicks uint64
		hasTFDT := false
		if traf.Tfdt != nil {
			startTicks = traf.Tfdt.BaseMediaDecodeTime()
			hasTFDT = true
		}
		var durTicks uint64
		checkedFirstVideo := false
		for _, trun := range traf.Truns {
			if trun == nil {
				continue
			}
			trun.AddSampleDefaultValues(traf.Tfhd, s.trexMap[tid])
			for i, smp := range trun.Samples {
				if tid == s.videoTrackID && !checkedFirstVideo && i == 0 {
					checkedFirstVideo = true
					// ISO BMFF sample_is_non_sync_sample is bit 16 (0x00010000).
					meta.syncVideo = (smp.Flags & 0x00010000) == 0
				}
				durTicks += uint64(smp.Dur)
			}
		}
		if hasTFDT || durTicks > 0 {
			startDur := ticksToDuration(startTicks, scale)
			endDur := ticksToDuration(startTicks+durTicks, scale)
			if !meta.hasTimeline || (tid == s.videoTrackID) || startDur < meta.startPTS {
				if !meta.hasTimeline || tid == s.videoTrackID {
					meta.startPTS = startDur
				}
			}
			if !meta.hasTimeline || endDur > meta.endPTS {
				meta.endPTS = endDur
			}
			meta.hasTimeline = true
		}
	}
	return meta
}

func ticksToDuration(ticks uint64, scale uint32) time.Duration {
	if scale == 0 {
		return 0
	}
	sec := ticks / uint64(scale)
	rem := ticks % uint64(scale)
	return time.Duration(sec)*time.Second + time.Duration(rem*uint64(time.Second)/uint64(scale))
}

func (s *liveFMP4Source) pollLoop() {
	defer close(s.pollDone)
	ticker := time.NewTicker(s.opts.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopPoll:
			return
		case <-ticker.C:
		}
		active := true
		if s.opts.IsActive != nil {
			active = s.opts.IsActive()
		}

		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return
		}
		f := s.file
		prevEnd := s.committedFragEnd
		prevEnded := s.ended
		if f != nil {
			if limit, err := s.safeDiskLimit(f); err == nil && limit > s.scannedOff {
				_, _ = s.scanFragmentsFrom(f, limit)
			} else if err != nil {
				active = false
			}
			// Check if path was removed or renamed away when IsActive is nil.
			if s.opts.IsActive == nil {
				if _, statErr := os.Stat(s.path); statErr != nil {
					active = false
				}
			}
		} else {
			active = false
		}
		if !active {
			s.ended = true
		}
		changed := s.committedFragEnd != prevEnd || s.ended != prevEnded
		committed := s.committedFragEnd
		ended := s.ended
		listeners := append(([]func(int64, bool, bool))(nil), s.listeners...)
		s.mu.Unlock()

		if changed {
			for _, fn := range listeners {
				fn(committed, ended, false)
			}
		}
		if ended {
			return
		}
	}
}

func (s *liveFMP4Source) subscribe(fn func(committedEnd int64, ended bool, closed bool)) (committedEnd int64, ended bool) {
	s.mu.Lock()
	committedEnd = s.committedFragEnd
	ended = s.ended
	if !s.closed {
		s.listeners = append(s.listeners, fn)
	}
	s.mu.Unlock()
	return committedEnd, ended
}

func (s *liveFMP4Source) AvailableDuration() time.Duration {
	s.mu.Lock()
	latest := s.latestEndPTS
	fragCount := s.fragCount
	s.mu.Unlock()
	if s.originPTSKnown && latest >= s.originPTS {
		avail := latest - s.originPTS
		if avail > 0 {
			return avail
		}
	}
	if fragCount > 0 {
		return time.Duration(fragCount) * time.Second
	}
	return 0
}

func (s *liveFMP4Source) LiveEnded() bool {
	s.mu.Lock()
	ended := s.ended || s.closed
	s.mu.Unlock()
	return ended
}

func (s *liveFMP4Source) abort() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	f := s.file
	s.file = nil
	committed := s.committedFragEnd
	listeners := append(([]func(int64, bool, bool))(nil), s.listeners...)
	s.listeners = nil
	select {
	case <-s.stopPoll:
	default:
		close(s.stopPoll)
	}
	s.mu.Unlock()

	for _, fn := range listeners {
		fn(committed, true, true)
	}
	if f != nil {
		_ = f.Close()
	}
}

func (s *liveFMP4Source) Close() error {
	s.abort()
	<-s.pollDone
	return nil
}
