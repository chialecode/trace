package live

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"math/bits"

	"github.com/Eyevinn/mp4ff/mp4"
)

const (
	videoTimescale uint32 = 90000

	defaultSingleTrackWaitMs int64 = 3000
	defaultMinKeyframeMs     int64 = 1000
	defaultMaxFragmentMs     int64 = 2000
	defaultMaxFragmentBytes  int64 = 4 * 1024 * 1024
	defaultMaxForwardJumpMs  int64 = 10_000
	defaultMaxBackwardJumpMs int64 = 1_000

	preKeyframeAudioKeepWindowMs int64 = 100
	maxPreKeyframeAudioSamples   int   = 256
)

// ErrNoVideoKeyframe indicates the FLV stream ended before a decodable video keyframe arrived.
var ErrNoVideoKeyframe = errors.New("live: 未收到可解码的视频关键帧")

// PartTarget is the file-like sink for one .mp4.part segment.
type PartTarget interface {
	io.Writer
	Sync() error
}

// FLVRemuxOptions tunes GOP buffering, startup track discovery, and timestamp jump thresholds.
type FLVRemuxOptions struct {
	SingleTrackWaitMs int64
	MinKeyframeMs     int64
	MaxFragmentMs     int64
	MaxFragmentBytes  int64
	MaxForwardJumpMs  int64
	MaxBackwardJumpMs int64
}

func (o FLVRemuxOptions) withDefaults() FLVRemuxOptions {
	if o.SingleTrackWaitMs <= 0 {
		o.SingleTrackWaitMs = defaultSingleTrackWaitMs
	}
	if o.MinKeyframeMs <= 0 {
		o.MinKeyframeMs = defaultMinKeyframeMs
	}
	if o.MaxFragmentMs <= 0 {
		o.MaxFragmentMs = defaultMaxFragmentMs
	}
	if o.MaxFragmentBytes <= 0 {
		o.MaxFragmentBytes = defaultMaxFragmentBytes
	}
	if o.MaxForwardJumpMs <= 0 {
		o.MaxForwardJumpMs = defaultMaxForwardJumpMs
	}
	if o.MaxBackwardJumpMs <= 0 {
		o.MaxBackwardJumpMs = defaultMaxBackwardJumpMs
	}
	return o
}

// FLVCheckpoint reports the fsynced byte length and cumulative fMP4 duration of the current part.
type FLVCheckpoint struct {
	BytesWritten int64
	DurationSec  float64
	Sequence     int64
	Codec        string     // "avc" or "hevc"
	FourCC       VideoCodec // "avc1" or "hvc1"
	Width        int
	Height       int
	HasAudio     bool
}

// FLVRemuxer converts a continuous HTTP-FLV stream into one or more fMP4 .part files,
// splitting cleanly at parameter-set changes or >10s / <-1s timestamp discontinuities.
type FLVRemuxer struct {
	flv            *FLVReader
	opts           FLVRemuxOptions
	latestVideoCfg *VideoConfig
	latestAudioCfg *AudioConfig
	pendingParsed  *ParsedTag
	eofReached     bool
}

// NewFLVRemuxer creates a stateful HTTP-FLV to fMP4 remuxer over r.
func NewFLVRemuxer(r io.Reader, opts FLVRemuxOptions) *FLVRemuxer {
	return &FLVRemuxer{
		flv:  NewFLVReader(r),
		opts: opts.withDefaults(),
	}
}

type tsUnwrapper struct {
	initialized bool
	prevRaw     uint32
	wrapOffset  int64
	lastStep    int64
}

func (u *tsUnwrapper) unwrap(raw uint32) int64 {
	if !u.initialized {
		u.initialized = true
		u.prevRaw = raw
		return int64(raw)
	}
	// Unsigned 32-bit FLV timestamp rollover (~49.7 days: 0xFFFFFFFF -> 0x00000000).
	if u.prevRaw >= 0xFFFF0000 && raw < 0x00010000 {
		step := int64(1) << 32
		u.wrapOffset += step
		u.lastStep = step
		u.prevRaw = raw
		return u.wrapOffset + int64(raw)
	}
	// Signed 31-bit encoder timestamp overflow (~24.85 days: 0x7FFFFFFF -> 0x00000000).
	if u.prevRaw >= 0x7FFF0000 && u.prevRaw <= 0x8000FFFF && raw < 0x00010000 {
		step := int64(1) << 31
		u.wrapOffset += step
		u.lastStep = step
		u.prevRaw = raw
		return u.wrapOffset + int64(raw)
	}
	// Handle minor intra-track packet reordering right across a wrap boundary.
	if u.lastStep == (int64(1)<<32) && u.prevRaw < 0x00010000 && raw >= 0xFFFF0000 {
		return (u.wrapOffset - u.lastStep) + int64(raw)
	}
	if u.lastStep == (int64(1)<<31) && u.prevRaw < 0x00010000 && raw >= 0x7FFF0000 && raw <= 0x8000FFFF {
		return (u.wrapOffset - u.lastStep) + int64(raw)
	}
	u.prevRaw = raw
	return u.wrapOffset + int64(raw)
}

func (u tsUnwrapper) clone() tsUnwrapper {
	return u
}

type bufferedMediaSample struct {
	dts        uint64
	dur        uint32
	cts        int32
	isKeyframe bool
	data       []byte
}

type preKeyframeAudioPacket struct {
	rawTS       uint32
	unwrappedMs int64
	data        []byte
}

// WritePart writes one continuous fMP4 segment (ftyp+moov followed by GOP-aligned moof+mdat
// fragments) to target, calling fsync and onCheckpoint after every fragment.
//
// Returns errSegmentRollover when a parameter-set change or timestamp jump requires closing
// the current part and calling WritePart again on a new part file.
func (rm *FLVRemuxer) WritePart(
	ctx context.Context,
	target PartTarget,
	onCheckpoint func(FLVCheckpoint) error,
) (FLVCheckpoint, error) {
	if rm.eofReached && rm.pendingParsed == nil {
		return FLVCheckpoint{}, io.EOF
	}

	ps := &partWriterState{
		opts:           rm.opts,
		target:         target,
		onCheckpoint:   onCheckpoint,
		activeVideoCfg: rm.latestVideoCfg,
		activeAudioCfg: rm.latestAudioCfg,
	}

	for {
		if err := ctx.Err(); err != nil {
			if flushErr := ps.flushFinal(); flushErr != nil {
				return ps.lastCheckpoint, flushErr
			}
			return ps.lastCheckpoint, err
		}

		pkt, err := rm.nextParsedTag()
		if err != nil {
			if errors.Is(err, io.EOF) {
				rm.eofReached = true
				if !ps.started {
					if ps.preAudioSeen {
						return FLVCheckpoint{}, &UnsupportedCodecError{
							Media:  "stream",
							Codec:  "audio-only",
							Reason: "不支持无视频轨的纯音频直播流",
						}
					}
					return FLVCheckpoint{}, ErrNoVideoKeyframe
				}
				if flushErr := ps.flushFinal(); flushErr != nil {
					return ps.lastCheckpoint, flushErr
				}
				return ps.lastCheckpoint, nil
			}
			// On truncated tail or read error after valid frames were buffered, flush already-verified
			// complete samples before surfacing the stream error so checkpoints stay current.
			if ps.started && (errors.Is(err, ErrFLVTruncated) || (!IsFLVFormatError(err) && !IsUnsupportedCodec(err))) {
				_ = ps.flushFinal()
			}
			return ps.lastCheckpoint, err
		}

		switch pkt.Kind {
		case ParsedTagIgnored, ParsedTagScript:
			continue

		case ParsedTagVideoEnd:
			if ps.started {
				if flushErr := ps.flushFinal(); flushErr != nil {
					return ps.lastCheckpoint, flushErr
				}
			}
			// Drain to see if stream ends or starts a new sequence cleanly.
			nextPkt, nextErr := rm.nextParsedTag()
			if nextErr != nil {
				if errors.Is(nextErr, io.EOF) {
					rm.eofReached = true
					if !ps.started {
						return FLVCheckpoint{}, ErrNoVideoKeyframe
					}
					return ps.lastCheckpoint, nil
				}
				return ps.lastCheckpoint, nextErr
			}
			rm.pendingParsed = &nextPkt
			if ps.started {
				return ps.lastCheckpoint, errSegmentRollover
			}

		case ParsedTagVideoConfig:
			newCfg := pkt.VideoConfig
			if ps.started && ps.activeVideoCfg != nil {
				if bytes.Equal(newCfg.Canonical, ps.activeVideoCfg.Canonical) {
					// Identical parameter-set resend: update wire NALU prefix length if needed, do not split.
					ps.activeVideoCfg.NALULenSize = newCfg.NALULenSize
					rm.latestVideoCfg = ps.activeVideoCfg
					continue
				}
				// Real parameter-set change mid-stream: finalize current part and roll over.
				rm.latestVideoCfg = newCfg
				if flushErr := ps.flushFinal(); flushErr != nil {
					return ps.lastCheckpoint, flushErr
				}
				return ps.lastCheckpoint, errSegmentRollover
			}
			ps.activeVideoCfg = newCfg
			rm.latestVideoCfg = newCfg

		case ParsedTagAudioConfig:
			newCfg := pkt.AudioConfig
			if ps.started && ps.activeAudioCfg != nil && (ps.audioReady || ps.initWritten) {
				if bytes.Equal(newCfg.Canonical, ps.activeAudioCfg.Canonical) {
					continue
				}
				rm.latestAudioCfg = newCfg
				if flushErr := ps.flushFinal(); flushErr != nil {
					return ps.lastCheckpoint, flushErr
				}
				return ps.lastCheckpoint, errSegmentRollover
			}
			ps.activeAudioCfg = newCfg
			rm.latestAudioCfg = newCfg

		case ParsedTagVideoFrame:
			rollover, procErr := ps.handleVideoFrame(pkt)
			if procErr != nil {
				return ps.lastCheckpoint, procErr
			}
			if rollover {
				rm.pendingParsed = &pkt
				if flushErr := ps.flushFinal(); flushErr != nil {
					return ps.lastCheckpoint, flushErr
				}
				return ps.lastCheckpoint, errSegmentRollover
			}

		case ParsedTagAudioFrame:
			rollover, procErr := ps.handleAudioFrame(pkt)
			if procErr != nil {
				return ps.lastCheckpoint, procErr
			}
			if rollover {
				rm.pendingParsed = &pkt
				if flushErr := ps.flushFinal(); flushErr != nil {
					return ps.lastCheckpoint, flushErr
				}
				return ps.lastCheckpoint, errSegmentRollover
			}
		}
	}
}

func (rm *FLVRemuxer) nextParsedTag() (ParsedTag, error) {
	if rm.pendingParsed != nil {
		pkt := *rm.pendingParsed
		rm.pendingParsed = nil
		return pkt, nil
	}
	rawTag, err := rm.flv.NextTag()
	if err != nil {
		return ParsedTag{}, err
	}
	return ParseFLVTag(rawTag)
}

type partWriterState struct {
	opts         FLVRemuxOptions
	target       PartTarget
	onCheckpoint func(FLVCheckpoint) error

	activeVideoCfg *VideoConfig
	activeAudioCfg *AudioConfig

	// Pre-keyframe audio tracking (detects >3s audio-only streams and preserves simultaneous t=0 AAC).
	preAudioSeen     bool
	preAudioUnwrap   tsUnwrapper
	preAudioOriginMs int64
	preAudioPackets  []preKeyframeAudioPacket

	started      bool
	originMs     int64
	videoUnwrap  tsUnwrapper
	audioUnwrap  tsUnwrapper
	prevVideoMs  int64
	prevAudioMs  int64
	hasPrevVideo bool
	hasPrevAudio bool
	prevVideoDTS uint64
	prevAudioDTS uint64
	minAudioDTS  uint64

	audioReady    bool // True once both activeAudioCfg != nil and >=1 raw AAC frame arrived
	initWritten   bool
	initHasAudio  bool
	lateAudioSeen bool // Audio arrived after a video-only InitSegment was locked

	vBuf          []bufferedMediaSample
	aBuf          []bufferedMediaSample
	bufferedBytes int64
	lastVideoDur  uint32
	lastAudioDur  uint32

	seqNumber          uint32
	writtenBytes       int64
	flushedVideoEndDTS uint64
	flushedAudioEndDTS uint64
	flushedVideoDur    uint64
	flushedAudioDur    uint64
	flushedAnyAudio    bool
	lastCheckpoint     FLVCheckpoint
}

func (ps *partWriterState) handleVideoFrame(pkt ParsedTag) (rollover bool, err error) {
	if ps.activeVideoCfg == nil || ps.activeVideoCfg.Codec != pkt.VideoCodec {
		return false, nil
	}
	naluBytes, err := normalizeLengthPrefixedNALUs(pkt.RawPayload, ps.activeVideoCfg.NALULenSize)
	if err != nil {
		return false, err
	}

	if !ps.started {
		if !pkt.IsKeyframe {
			return false, nil
		}
		ps.startAtFirstVideoKeyframe(pkt.Timestamp)
	}

	if ps.lateAudioSeen && pkt.IsKeyframe && len(ps.vBuf) > 0 {
		// Video-only InitSegment was committed after the 3s wait, and audio arrived later:
		// roll over cleanly at this keyframe so the next part includes both tracks.
		return true, nil
	}

	unwrappedMs := ps.videoUnwrap.unwrap(pkt.Timestamp)
	relMs := unwrappedMs - ps.originMs
	if ps.hasPrevVideo {
		deltaMs := relMs - ps.prevVideoMs
		if deltaMs > ps.opts.MaxForwardJumpMs || deltaMs < -ps.opts.MaxBackwardJumpMs {
			return true, nil
		}
		if deltaMs <= 0 {
			relMs = ps.prevVideoMs
		} else {
			ps.prevVideoMs = relMs
		}
	} else {
		if relMs < 0 {
			relMs = 0
		}
		ps.prevVideoMs = relMs
		ps.hasPrevVideo = true
	}

	dts := uint64(relMs) * uint64(videoTimescale) / 1000
	if len(ps.vBuf) > 0 || ps.flushedVideoEndDTS > 0 {
		minDTS := ps.prevVideoDTS + 1
		if ps.flushedVideoEndDTS > minDTS {
			minDTS = ps.flushedVideoEndDTS
		}
		if dts < minDTS {
			dts = minDTS
		}
	}
	ps.prevVideoDTS = dts

	ctsTicks := clampCTSTicks(pkt.CTSMs)

	if n := len(ps.vBuf); n > 0 {
		dur64 := dts - ps.vBuf[n-1].dts
		if dur64 == 0 {
			dur64 = 1
		}
		if dur64 > math.MaxUint32 {
			dur64 = math.MaxUint32
		}
		ps.vBuf[n-1].dur = uint32(dur64)
		ps.lastVideoDur = uint32(dur64)
	}

	// Check whether buffered samples before this new frame should be flushed now.
	if len(ps.vBuf) > 0 {
		spanTicks := dts - ps.vBuf[0].dts
		spanMs := int64(spanTicks * 1000 / uint64(videoTimescale))

		canLockInit := ps.initWritten || ps.audioReady || relMs >= ps.opts.SingleTrackWaitMs || ps.bufferedBytes >= ps.opts.MaxFragmentBytes
		shouldFlush := (pkt.IsKeyframe && spanMs >= ps.opts.MinKeyframeMs) ||
			spanMs >= ps.opts.MaxFragmentMs ||
			ps.bufferedBytes >= ps.opts.MaxFragmentBytes

		if canLockInit && shouldFlush {
			if err := ps.flushPending(false); err != nil {
				return false, err
			}
		}
	}

	ps.vBuf = append(ps.vBuf, bufferedMediaSample{
		dts:        dts,
		cts:        ctsTicks,
		isKeyframe: pkt.IsKeyframe,
		data:       naluBytes,
	})
	ps.bufferedBytes += int64(len(naluBytes))
	return false, nil
}

func (ps *partWriterState) startAtFirstVideoKeyframe(keyframeRawTS uint32) {
	ps.started = true
	if ps.preAudioSeen && !ps.videoUnwrap.initialized {
		ps.videoUnwrap = ps.preAudioUnwrap.clone()
	}
	ps.originMs = ps.videoUnwrap.unwrap(keyframeRawTS)
	ps.audioUnwrap = ps.videoUnwrap.clone()

	// Replay any simultaneous/near-simultaneous raw AAC frames (within 100ms before keyframe)
	// so streams whose t=0 Audio tag precedes t=0 Video keyframe lose zero samples.
	if ps.activeAudioCfg != nil && len(ps.preAudioPackets) > 0 {
		var kept []preKeyframeAudioPacket
		for _, ap := range ps.preAudioPackets {
			diff := ap.unwrappedMs - ps.originMs
			if diff >= -preKeyframeAudioKeepWindowMs && diff <= ps.opts.MaxForwardJumpMs {
				kept = append(kept, ap)
			}
		}
		ps.preAudioPackets = nil
		for _, ap := range kept {
			_, _ = ps.appendAudioSample(ap.rawTS, ap.data)
		}
	} else {
		ps.preAudioPackets = nil
	}
}

func (ps *partWriterState) handleAudioFrame(pkt ParsedTag) (rollover bool, err error) {
	if !ps.started {
		aMs := ps.preAudioUnwrap.unwrap(pkt.Timestamp)
		if !ps.preAudioSeen {
			ps.preAudioSeen = true
			ps.preAudioOriginMs = aMs
		} else if aMs-ps.preAudioOriginMs > ps.opts.SingleTrackWaitMs {
			return false, &UnsupportedCodecError{
				Media:  "stream",
				Codec:  "audio-only",
				Reason: "超过 3 秒仅收到音频包而无视频关键帧",
			}
		}
		if ps.activeAudioCfg != nil {
			cp := make([]byte, len(pkt.RawPayload))
			copy(cp, pkt.RawPayload)
			ps.preAudioPackets = append(ps.preAudioPackets, preKeyframeAudioPacket{
				rawTS:       pkt.Timestamp,
				unwrappedMs: aMs,
				data:        cp,
			})
			if len(ps.preAudioPackets) > maxPreKeyframeAudioSamples {
				ps.preAudioPackets = ps.preAudioPackets[len(ps.preAudioPackets)-maxPreKeyframeAudioSamples:]
			}
		}
		return false, nil
	}

	if ps.activeAudioCfg == nil {
		return false, nil
	}
	if ps.initWritten && !ps.initHasAudio {
		// Video-only InitSegment was already written; wait for the next video keyframe to split.
		ps.lateAudioSeen = true
		return false, nil
	}
	return ps.appendAudioSample(pkt.Timestamp, pkt.RawPayload)
}

func (ps *partWriterState) appendAudioSample(rawTS uint32, rawPayload []byte) (rollover bool, err error) {
	unwrappedMs := ps.audioUnwrap.unwrap(rawTS)
	relMs := unwrappedMs - ps.originMs
	if ps.hasPrevAudio {
		deltaMs := relMs - ps.prevAudioMs
		if deltaMs > ps.opts.MaxForwardJumpMs || deltaMs < -ps.opts.MaxBackwardJumpMs {
			return true, nil
		}
		if deltaMs <= 0 {
			relMs = ps.prevAudioMs
		} else {
			ps.prevAudioMs = relMs
		}
	} else {
		if relMs < -ps.opts.MaxBackwardJumpMs || relMs > ps.opts.MaxForwardJumpMs {
			return true, nil
		}
		if relMs < 0 {
			relMs = 0
		}
		ps.prevAudioMs = relMs
		ps.hasPrevAudio = true
	}

	dts := mulDiv64(uint64(relMs), uint64(ps.activeAudioCfg.SampleRate), 1000)
	if len(ps.aBuf) > 0 || ps.minAudioDTS > 0 {
		minDTS := uint64(0)
		if len(ps.aBuf) > 0 || ps.flushedAnyAudio {
			minDTS = ps.prevAudioDTS + 1
		}
		if ps.minAudioDTS > minDTS {
			minDTS = ps.minAudioDTS
		}
		if dts < minDTS {
			dts = minDTS
		}
	}
	ps.prevAudioDTS = dts
	ps.audioReady = true

	if n := len(ps.aBuf); n > 0 {
		dur64 := dts - ps.aBuf[n-1].dts
		if dur64 == 0 {
			dur64 = 1
		}
		if dur64 > math.MaxUint32 {
			dur64 = math.MaxUint32
		}
		ps.aBuf[n-1].dur = uint32(dur64)
		ps.lastAudioDur = uint32(dur64)
	}

	dataCopy := make([]byte, len(rawPayload))
	copy(dataCopy, rawPayload)
	ps.aBuf = append(ps.aBuf, bufferedMediaSample{
		dts:        dts,
		isKeyframe: true,
		data:       dataCopy,
	})
	ps.bufferedBytes += int64(len(dataCopy))
	return false, nil
}

func (ps *partWriterState) flushFinal() error {
	if !ps.started || (len(ps.vBuf) == 0 && len(ps.aBuf) == 0) {
		return nil
	}
	return ps.flushPending(true)
}

func (ps *partWriterState) flushPending(flushAll bool) error {
	if !ps.initWritten {
		if err := ps.writeInitSegment(); err != nil {
			return err
		}
	}

	var vFlush []bufferedMediaSample
	if flushAll {
		if len(ps.vBuf) > 0 {
			lastIdx := len(ps.vBuf) - 1
			if ps.vBuf[lastIdx].dur == 0 {
				dur := ps.lastVideoDur
				if dur == 0 {
					dur = 3000 // ~33.33ms fallback for 1-frame stream at 90kHz
				}
				ps.vBuf[lastIdx].dur = dur
			}
			vFlush = ps.vBuf
			ps.vBuf = nil
		}
	} else {
		// In normal lookahead mode, all buffered video frames already have exact durations
		// computed from the newly arrived frame that triggered flushPending.
		vFlush = ps.vBuf
		ps.vBuf = nil
	}

	var aFlush []bufferedMediaSample
	if ps.initHasAudio && len(ps.aBuf) > 0 {
		if flushAll {
			lastIdx := len(ps.aBuf) - 1
			if ps.aBuf[lastIdx].dur == 0 {
				dur := ps.activeAudioCfg.SamplesPerFrame
				if dur == 0 {
					dur = ps.lastAudioDur
				}
				if dur == 0 {
					dur = 1024
				}
				ps.aBuf[lastIdx].dur = dur
			}
			aFlush = ps.aBuf
			ps.aBuf = nil
		} else if len(ps.aBuf) >= 2 {
			// Flush all audio samples except the final lookahead sample so every flushed
			// audio sample carries its exact nextDTS - currDTS duration.
			split := len(ps.aBuf) - 1
			aFlush = ps.aBuf[:split]
			remaining := make([]bufferedMediaSample, 1)
			remaining[0] = ps.aBuf[split]
			ps.aBuf = remaining
		} else if !ps.flushedAnyAudio && len(ps.aBuf) == 1 {
			// Ensure the very first checkpointed fragment already has >=1 sample on Track 2
			// so crash recovery + mp4.Defragment never sees a 0-sample audio track.
			dur := ps.activeAudioCfg.SamplesPerFrame
			if dur == 0 {
				dur = 1024
			}
			ps.aBuf[0].dur = dur
			aFlush = ps.aBuf
			ps.aBuf = nil
		}
	} else if !ps.initHasAudio {
		ps.aBuf = nil
	}

	if len(vFlush) == 0 && len(aFlush) == 0 {
		return nil
	}

	var trackIDs []uint32
	if len(vFlush) > 0 {
		trackIDs = append(trackIDs, 1)
	}
	if len(aFlush) > 0 {
		trackIDs = append(trackIDs, 2)
	}

	ps.seqNumber++
	frag, err := mp4.CreateMultiTrackFragment(ps.seqNumber, trackIDs)
	if err != nil {
		return err
	}

	videoSyncFlags := mp4.SyncSampleFlags
	videoNonSyncFlags := mp4.SampleFlags{SampleDependsOn: 1, SampleIsNonSync: true}.Encode()
	audioSyncFlags := mp4.SyncSampleFlags

	for _, s := range vFlush {
		flags := videoNonSyncFlags
		if s.isKeyframe {
			flags = videoSyncFlags
		}
		dur := s.dur
		if dur == 0 {
			dur = 1
		}
		if err := frag.AddFullSampleToTrack(mp4.FullSample{
			Sample: mp4.Sample{
				Flags:                 flags,
				Dur:                   dur,
				Size:                  uint32(len(s.data)),
				CompositionTimeOffset: s.cts,
			},
			DecodeTime: s.dts,
			Data:       s.data,
		}, 1); err != nil {
			return err
		}
		ps.flushedVideoEndDTS = s.dts + uint64(dur)
		ps.flushedVideoDur += uint64(dur)
	}

	for _, s := range aFlush {
		dur := s.dur
		if dur == 0 {
			dur = 1
		}
		if err := frag.AddFullSampleToTrack(mp4.FullSample{
			Sample: mp4.Sample{
				Flags: audioSyncFlags,
				Dur:   dur,
				Size:  uint32(len(s.data)),
			},
			DecodeTime: s.dts,
			Data:       s.data,
		}, 2); err != nil {
			return err
		}
		ps.flushedAudioEndDTS = s.dts + uint64(dur)
		ps.flushedAudioDur += uint64(dur)
		ps.minAudioDTS = ps.flushedAudioEndDTS
		ps.flushedAnyAudio = true
	}

	var fragBuf bytes.Buffer
	if err := frag.Encode(&fragBuf); err != nil {
		return err
	}
	n, err := ps.target.Write(fragBuf.Bytes())
	ps.writtenBytes += int64(n)
	if err != nil {
		return err
	}
	if err := ps.target.Sync(); err != nil {
		return err
	}

	// Recompute remaining buffered payload size.
	var remBytes int64
	for _, s := range ps.vBuf {
		remBytes += int64(len(s.data))
	}
	for _, s := range ps.aBuf {
		remBytes += int64(len(s.data))
	}
	ps.bufferedBytes = remBytes

	durSec := float64(ps.flushedVideoDur) / float64(videoTimescale)
	if ps.initHasAudio && ps.activeAudioCfg != nil && ps.activeAudioCfg.SampleRate > 0 && ps.flushedAudioDur > 0 {
		aDurSec := float64(ps.flushedAudioDur) / float64(ps.activeAudioCfg.SampleRate)
		if aDurSec > durSec {
			durSec = aDurSec
		}
	}

	codecLabel := "avc"
	if ps.activeVideoCfg.Codec == VideoCodecHEVC {
		codecLabel = "hevc"
	}
	cp := FLVCheckpoint{
		BytesWritten: ps.writtenBytes,
		DurationSec:  durSec,
		Sequence:     int64(ps.seqNumber),
		Codec:        codecLabel,
		FourCC:       ps.activeVideoCfg.Codec,
		Width:        ps.activeVideoCfg.Width,
		Height:       ps.activeVideoCfg.Height,
		HasAudio:     ps.initHasAudio,
	}
	ps.lastCheckpoint = cp
	if ps.onCheckpoint != nil {
		if err := ps.onCheckpoint(cp); err != nil {
			return err
		}
	}
	return nil
}

func (ps *partWriterState) writeInitSegment() error {
	if ps.activeVideoCfg == nil {
		return &FLVFormatError{Reason: "缺少视频编码参数集"}
	}
	init := mp4.CreateEmptyInit()
	init.Moov.Mvhd.Timescale = videoTimescale

	vTrak := init.AddEmptyTrack(videoTimescale, "video", "und")
	switch ps.activeVideoCfg.Codec {
	case VideoCodecAVC:
		if len(ps.activeVideoCfg.SPS) == 0 || len(ps.activeVideoCfg.PPS) == 0 {
			return &FLVFormatError{Reason: "AVC 初始化缺少 SPS 或 PPS"}
		}
		if err := vTrak.SetAVCDescriptor("avc1", ps.activeVideoCfg.SPS, ps.activeVideoCfg.PPS, true); err != nil {
			return &FLVFormatError{Reason: "构建 avc1 初始化描述失败", Err: err}
		}
	case VideoCodecHEVC:
		if len(ps.activeVideoCfg.VPS) == 0 || len(ps.activeVideoCfg.SPS) == 0 || len(ps.activeVideoCfg.PPS) == 0 {
			return &FLVFormatError{Reason: "HEVC 初始化缺少 VPS、SPS 或 PPS"}
		}
		if err := vTrak.SetHEVCDescriptor("hvc1", ps.activeVideoCfg.VPS, ps.activeVideoCfg.SPS, ps.activeVideoCfg.PPS, ps.activeVideoCfg.SEI, true); err != nil {
			return &FLVFormatError{Reason: "构建 hvc1 初始化描述失败", Err: err}
		}
	default:
		return &UnsupportedCodecError{Media: "video", Codec: string(ps.activeVideoCfg.Codec)}
	}

	includeAudio := ps.audioReady && ps.activeAudioCfg != nil && len(ps.aBuf) > 0
	if includeAudio {
		aTrak := init.AddEmptyTrack(uint32(ps.activeAudioCfg.SampleRate), "audio", "und")
		if err := aTrak.SetAACDescriptorFromConfig(ps.activeAudioCfg.ASC); err != nil {
			return &FLVFormatError{Reason: "构建 mp4a 初始化描述失败", Err: err}
		}
	}

	var initBuf bytes.Buffer
	if err := init.Encode(&initBuf); err != nil {
		return fmt.Errorf("live: 编码 fMP4 InitSegment 失败: %w", err)
	}
	n, err := ps.target.Write(initBuf.Bytes())
	ps.writtenBytes += int64(n)
	if err != nil {
		return err
	}
	ps.initWritten = true
	ps.initHasAudio = includeAudio
	return nil
}

func clampCTSTicks(ctsMs int32) int32 {
	ticks := int64(ctsMs) * int64(videoTimescale) / 1000
	if ticks > math.MaxInt32 {
		return math.MaxInt32
	}
	if ticks < math.MinInt32 {
		return math.MinInt32
	}
	return int32(ticks)
}

func mulDiv64(a, b, div uint64) uint64 {
	if div == 0 {
		return 0
	}
	hi, lo := bits.Mul64(a, b)
	q, _ := bits.Div64(hi, lo, div)
	return q
}

// RemuxFLVStream drives FLVRemuxer across automatic part splits (parameter-set changes
// and timestamp jumps), invoking openPart for each new part starting at partIndex 1.
func RemuxFLVStream(
	ctx context.Context,
	r io.Reader,
	opts FLVRemuxOptions,
	openPart func(partIndex int) (target PartTarget, closePart func(FLVCheckpoint, error) error, err error),
	onCheckpoint func(partIndex int, cp FLVCheckpoint) error,
) ([]FLVCheckpoint, error) {
	remuxer := NewFLVRemuxer(r, opts)
	var parts []FLVCheckpoint
	for partIdx := 1; ; partIdx++ {
		target, closePart, err := openPart(partIdx)
		if err != nil {
			return parts, err
		}
		var cpHook func(FLVCheckpoint) error
		if onCheckpoint != nil {
			idx := partIdx
			cpHook = func(cp FLVCheckpoint) error {
				return onCheckpoint(idx, cp)
			}
		}
		cp, writeErr := remuxer.WritePart(ctx, target, cpHook)
		isRollover := errors.Is(writeErr, errSegmentRollover)
		closeErr := error(nil)
		if closePart != nil {
			reportedErr := writeErr
			if isRollover || errors.Is(writeErr, io.EOF) {
				reportedErr = nil
			}
			closeErr = closePart(cp, reportedErr)
		}
		if cp.BytesWritten > 0 {
			parts = append(parts, cp)
		}
		if closeErr != nil {
			return parts, closeErr
		}
		if isRollover {
			continue
		}
		if errors.Is(writeErr, io.EOF) || errors.Is(writeErr, ErrNoVideoKeyframe) && len(parts) > 0 {
			return parts, nil
		}
		return parts, writeErr
	}
}
