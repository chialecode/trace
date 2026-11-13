package live

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/Eyevinn/mp4ff/avc"
	"github.com/Eyevinn/mp4ff/hevc"
	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/settings"
	"github.com/chialecode/trace/internal/storage"
)

const (
	// Synthetic 640x360 AVC SPS/PPS/slice NALUs (no start codes).
	testAVCSPSHex   = "6764001eacd940a02ff9610000030001000003003c8f162d96"
	testAVCPPSHex   = "68ebecb22c"
	testAVCPPS2Hex  = "68ebef2c"
	testAVCSliceHex = "658884004ffede23c0a6109524aac694"
	testAVCPSlice   = "419a246c4211223344556677"

	// Synthetic HEVC VPS/SPS/PPS/slice NALUs (no start codes; SPS1=640x360, SPS2=960x540).
	testHEVCVPSHex   = "40010c01ffff016000000300900000030000030078959809"
	testHEVCSPS1Hex  = "420101016000000300900000030000030078a00502016965959a4932bc05a80808082000000300200000030321"
	testHEVCSPS2Hex  = "420101022000000300b0000003000003007ba0078200887db6718b92448053888892cf24a69272c9124922dc91aa48fca223ff000100016a02020201"
	testHEVCPPSHex   = "4401c172b46240"
	testHEVCIDRHex   = "2601af0940a1b2c3d4e5f601"
	testHEVCTrailHex = "0201d0094011223344556677"
)

var (
	// AAC-LC 48kHz stereo AudioSpecificConfig + silent raw CPE frame.
	testAACConfig48kStereo = []byte{0x11, 0x90}
	testAACSilentFrame     = []byte{0x21, 0x10, 0x04, 0x60, 0x8c, 0x1c}
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("hex decode: %v", err)
	}
	return b
}

type flvBuilder struct {
	buf bytes.Buffer
}

func newFLVBuilder(flags uint8) *flvBuilder {
	var fb flvBuilder
	// 9B FLV header + 4B PreviousTagSize0(0)
	fb.buf.Write([]byte{'F', 'L', 'V', 0x01, flags, 0x00, 0x00, 0x00, 0x09})
	fb.buf.Write([]byte{0x00, 0x00, 0x00, 0x00})
	return &fb
}

func (fb *flvBuilder) writeTag(tagType uint8, ts uint32, body []byte) {
	var hdr [11]byte
	hdr[0] = tagType
	size := uint32(len(body))
	hdr[1] = byte(size >> 16)
	hdr[2] = byte(size >> 8)
	hdr[3] = byte(size)
	hdr[4] = byte(ts >> 16)
	hdr[5] = byte(ts >> 8)
	hdr[6] = byte(ts)
	hdr[7] = byte(ts >> 24)
	fb.buf.Write(hdr[:])
	fb.buf.Write(body)
	var prev [4]byte
	binary.BigEndian.PutUint32(prev[:], 11+size)
	fb.buf.Write(prev[:])
}

func (fb *flvBuilder) writeOnMetaData() {
	// Misleading onMetaData payload to verify track setup ignores script data completely.
	fb.writeTag(flvTagTypeScript, 0, []byte{0x02, 0x00, 0x0a, 'o', 'n', 'M', 'e', 't', 'a', 'D', 'a', 't', 'a', 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x09})
}

func (fb *flvBuilder) writeAACConfig(ts uint32, asc []byte) {
	body := append([]byte{0xAF, 0x00}, asc...)
	fb.writeTag(flvTagTypeAudio, ts, body)
}

func (fb *flvBuilder) writeAACFrame(ts uint32, raw []byte) {
	body := append([]byte{0xAF, 0x01}, raw...)
	fb.writeTag(flvTagTypeAudio, ts, body)
}

func encodeSI24(v int32) [3]byte {
	u := uint32(v) & 0xFFFFFF
	return [3]byte{byte(u >> 16), byte(u >> 8), byte(u)}
}

func packLengthPrefixedNALUs(naluLenSize int, nalus ...[]byte) []byte {
	var out bytes.Buffer
	for _, nalu := range nalus {
		switch naluLenSize {
		case 1:
			out.WriteByte(byte(len(nalu)))
		case 2:
			var p [2]byte
			binary.BigEndian.PutUint16(p[:], uint16(len(nalu)))
			out.Write(p[:])
		case 4:
			var p [4]byte
			binary.BigEndian.PutUint32(p[:], uint32(len(nalu)))
			out.Write(p[:])
		}
		out.Write(nalu)
	}
	return out.Bytes()
}

func buildAVCDecConfBytes(t *testing.T, sps, pps []byte, naluLenSize int) []byte {
	t.Helper()
	dcr, err := avc.CreateAVCDecConfRec([][]byte{sps}, [][]byte{pps}, true)
	if err != nil {
		t.Fatalf("CreateAVCDecConfRec: %v", err)
	}
	var buf bytes.Buffer
	if err := dcr.Encode(&buf); err != nil {
		t.Fatalf("encode avcC: %v", err)
	}
	raw := buf.Bytes()
	raw[4] = (raw[4] & 0xFC) | byte((naluLenSize-1)&0x03)
	return raw
}

func buildHEVCDecConfBytes(t *testing.T, vps, sps, pps []byte, naluLenSize int) []byte {
	t.Helper()
	var vpsArr, spsArr, ppsArr [][]byte
	if len(vps) > 0 {
		vpsArr = [][]byte{vps}
	}
	if len(sps) > 0 {
		spsArr = [][]byte{sps}
	}
	if len(pps) > 0 {
		ppsArr = [][]byte{pps}
	}
	hdcr := hevc.DecConfRec{
		ConfigurationVersion: 1,
		LengthSizeMinusOne:   3,
		AvgFrameRate:         0,
	}
	if len(vpsArr) > 0 {
		hdcr.NaluArrays = append(hdcr.NaluArrays, hevc.NewNaluArray(true, hevc.NALU_VPS, vpsArr))
	}
	if len(spsArr) > 0 {
		if parsed, err := hevc.ParseSPSNALUnit(sps); err == nil {
			hdcr.GeneralProfileSpace = parsed.ProfileTierLevel.GeneralProfileSpace
			hdcr.GeneralTierFlag = parsed.ProfileTierLevel.GeneralTierFlag
			hdcr.GeneralProfileIDC = parsed.ProfileTierLevel.GeneralProfileIDC
			hdcr.GeneralProfileCompatibilityFlags = parsed.ProfileTierLevel.GeneralProfileCompatibilityFlags
			hdcr.GeneralConstraintIndicatorFlags = parsed.ProfileTierLevel.GeneralConstraintIndicatorFlags
			hdcr.GeneralLevelIDC = parsed.ProfileTierLevel.GeneralLevelIDC
			hdcr.ChromaFormatIDC = byte(parsed.ChromaFormatIDC)
			hdcr.BitDepthLumaMinus8 = byte(parsed.BitDepthLumaMinus8)
			hdcr.BitDepthChromaMinus8 = byte(parsed.BitDepthChromaMinus8)
		}
		hdcr.NaluArrays = append(hdcr.NaluArrays, hevc.NewNaluArray(true, hevc.NALU_SPS, spsArr))
	}
	if len(ppsArr) > 0 {
		hdcr.NaluArrays = append(hdcr.NaluArrays, hevc.NewNaluArray(true, hevc.NALU_PPS, ppsArr))
	}
	var buf bytes.Buffer
	if err := hdcr.Encode(&buf); err != nil {
		t.Fatalf("encode hvcC: %v", err)
	}
	raw := buf.Bytes()
	raw[21] = (raw[21] & 0xFC) | byte((naluLenSize-1)&0x03)
	return raw
}

func (fb *flvBuilder) writeClassicVideoConfig(ts uint32, codecID uint8, cfg []byte) {
	body := make([]byte, 5+len(cfg))
	body[0] = 0x10 | (codecID & 0x0f)
	body[1] = 0x00
	copy(body[5:], cfg)
	fb.writeTag(flvTagTypeVideo, ts, body)
}

func (fb *flvBuilder) writeClassicVideoFrame(ts uint32, codecID uint8, keyframe bool, ctsMs int32, naluPayload []byte) {
	ft := uint8(2)
	if keyframe {
		ft = 1
	}
	body := make([]byte, 5+len(naluPayload))
	body[0] = (ft << 4) | (codecID & 0x0f)
	body[1] = 0x01
	cts := encodeSI24(ctsMs)
	copy(body[2:5], cts[:])
	copy(body[5:], naluPayload)
	fb.writeTag(flvTagTypeVideo, ts, body)
}

func (fb *flvBuilder) writeEnhancedVideo(ts uint32, frameType uint8, pktType uint8, fourCC string, extra []byte) {
	body := make([]byte, 5+len(extra))
	body[0] = 0x80 | ((frameType & 0x07) << 4) | (pktType & 0x0f)
	copy(body[1:5], []byte(fourCC))
	copy(body[5:], extra)
	fb.writeTag(flvTagTypeVideo, ts, body)
}

func remuxSinglePartAndFinalize(t *testing.T, flvBytes []byte) (FLVCheckpoint, download.MuxTrackSummary, string) {
	t.Helper()
	dir := t.TempDir()
	partPath := filepath.Join(dir, "part-001.mp4.part")
	finalPath := filepath.Join(dir, "part-001.mp4")

	f, err := os.OpenFile(partPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if err != nil {
		t.Fatalf("create part: %v", err)
	}
	var checkpoints []FLVCheckpoint
	remuxer := NewFLVRemuxer(bytes.NewReader(flvBytes), FLVRemuxOptions{})
	cp, err := remuxer.WritePart(context.Background(), f, func(c FLVCheckpoint) error {
		checkpoints = append(checkpoints, c)
		return nil
	})
	_ = f.Close()
	if err != nil {
		t.Fatalf("WritePart failed: %v", err)
	}
	if len(checkpoints) == 0 {
		t.Fatalf("expected >=1 checkpoint callback")
	}
	summary, err := finalizeLivePart(context.Background(), partPath, finalPath, cp.DurationSec)
	if err != nil {
		t.Fatalf("finalizeLivePart(expected=%.4fs) failed: %v", cp.DurationSec, err)
	}
	return cp, summary, finalPath
}

func TestFLVRemuxClassicAVCWith2ByteNALUAndSignedCTS(t *testing.T) {
	sps := mustHex(t, testAVCSPSHex)
	pps := mustHex(t, testAVCPPSHex)
	idr := mustHex(t, testAVCSliceHex)
	pSlice := mustHex(t, testAVCPSlice)

	fb := newFLVBuilder(0x05)
	fb.writeOnMetaData()
	// Use 2-byte NALU length prefixes to verify normalization to 4-byte ISO BMFF samples.
	fb.writeClassicVideoConfig(0, flvVideoCodecAVC, buildAVCDecConfBytes(t, sps, pps, 2))
	fb.writeAACConfig(0, testAACConfig48kStereo)

	// Generate 2.4 seconds (72 frames @ ~33.3ms, 3 GOPs) with signed B-frame CTS offsets (-33ms, +66ms, 0ms).
	audioTS := uint32(0)
	for i := 0; i < 72; i++ {
		vTS := uint32(i * 100 / 3)
		for audioTS <= vTS {
			fb.writeAACFrame(audioTS, testAACSilentFrame)
			audioTS += 21
		}
		isKey := i%30 == 0
		slice := pSlice
		cts := int32(0)
		if isKey {
			slice = idr
		} else if i%3 == 1 {
			cts = 66
		} else {
			cts = -33 // Negative 24-bit signed CTS!
		}
		fb.writeClassicVideoFrame(vTS, flvVideoCodecAVC, isKey, cts, packLengthPrefixedNALUs(2, slice))
	}

	cp, summary, finalPath := remuxSinglePartAndFinalize(t, fb.buf.Bytes())
	if !summary.MoovBeforeMdat || summary.TrackCount != 2 {
		t.Fatalf("unexpected MP4 summary: %+v", summary)
	}
	if summary.Width != 640 || summary.Height != 360 {
		t.Fatalf("expected 640x360, got %dx%d", summary.Width, summary.Height)
	}
	if math.Abs(summary.DurationSec-2.4) > 0.15 {
		t.Fatalf("expected ~2.4s duration, got %.3fs (checkpoint=%.3fs)", summary.DurationSec, cp.DurationSec)
	}

	// Verify normalized 4-byte NALU prefix and signed CTTS survived defragmentation.
	rawOut, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	parsed, err := mp4.DecodeFile(bytes.NewReader(rawOut))
	if err != nil {
		t.Fatalf("DecodeFile: %v", err)
	}
	vStbl := parsed.Moov.Traks[0].Mdia.Minf.Stbl
	if vStbl.Ctts == nil || vStbl.Ctts.Version != 1 {
		t.Fatalf("expected signed Version=1 ctts box for negative B-frame CTS, got %+v", vStbl.Ctts)
	}
}

func TestFLVRemuxDomesticCodecID12HEVCAndEnhancedFLVHVC1(t *testing.T) {
	vps := mustHex(t, testHEVCVPSHex)
	sps := mustHex(t, testHEVCSPS1Hex)
	pps := mustHex(t, testHEVCPPSHex)
	idr := mustHex(t, testHEVCIDRHex)
	trail := mustHex(t, testHEVCTrailHex)

	t.Run("DomesticCodecID12", func(t *testing.T) {
		fb := newFLVBuilder(0x05)
		fb.writeOnMetaData()
		// 1-byte NALU prefix in domestic CodecID=12 (0x1c / 0x2c) stream.
		fb.writeClassicVideoConfig(0, flvVideoCodecHEVC, buildHEVCDecConfBytes(t, vps, sps, pps, 1))
		fb.writeAACConfig(0, testAACConfig48kStereo)

		for i := 0; i < 65; i++ {
			ts := uint32(i * 40) // 25fps -> 2.6s
			if i%2 == 0 {
				fb.writeAACFrame(ts, testAACSilentFrame)
			}
			isKey := i%25 == 0
			nalu := trail
			if isKey {
				nalu = idr
			}
			fb.writeClassicVideoFrame(ts, flvVideoCodecHEVC, isKey, 0, packLengthPrefixedNALUs(1, nalu))
		}

		cp, summary, _ := remuxSinglePartAndFinalize(t, fb.buf.Bytes())
		if cp.Codec != "hevc" || cp.FourCC != VideoCodecHEVC || !cp.HasAudio {
			t.Fatalf("unexpected HEVC checkpoint: %+v", cp)
		}
		if !summary.MoovBeforeMdat || summary.TrackCount != 2 || summary.Width != 640 || summary.Height != 360 {
			t.Fatalf("unexpected CodecID=12 MP4 summary: %+v", summary)
		}
		if math.Abs(summary.DurationSec-2.6) > 0.15 {
			t.Fatalf("expected ~2.6s duration, got %.3fs", summary.DurationSec)
		}
	})

	t.Run("EnhancedFLVHVC1CodedFramesAndCodedFramesX", func(t *testing.T) {
		fb := newFLVBuilder(0x05)
		hvcC := buildHEVCDecConfBytes(t, vps, sps, pps, 4)

		// Enhanced Metadata (PacketType=5) must be skipped cleanly.
		fb.writeEnhancedVideo(0, 1, flvExPacketMetadata, "hvc1", []byte("hdr-info-ignored"))
		// SequenceStart (PacketType=0)
		fb.writeEnhancedVideo(0, 1, flvExPacketSequenceStart, "hvc1", hvcC)
		fb.writeAACConfig(0, testAACConfig48kStereo)

		for i := 0; i < 60; i++ {
			ts := uint32(10000 + i*40) // Non-zero initial stream uptime (10s) normalized to 0!
			fb.writeAACFrame(ts, testAACSilentFrame)

			ft := uint8(2)
			nalu := trail
			if i%25 == 0 {
				ft = 1
				nalu = idr
			}
			framed := packLengthPrefixedNALUs(4, nalu)
			if i%2 == 0 {
				// CodedFramesX (PacketType=3): implicit CTS=0, no 3-byte SI24 field!
				fb.writeEnhancedVideo(ts, ft, flvExPacketCodedFramesX, "hvc1", framed)
			} else {
				// CodedFrames (PacketType=1): explicit 3-byte signed SI24 CTS!
				cts := encodeSI24(40)
				extra := append(cts[:], framed...)
				fb.writeEnhancedVideo(ts, ft, flvExPacketCodedFrames, "hvc1", extra)
			}
		}
		// SequenceEnd (PacketType=2)
		fb.writeEnhancedVideo(12400, 1, flvExPacketSequenceEnd, "hvc1", nil)

		_, summary, _ := remuxSinglePartAndFinalize(t, fb.buf.Bytes())
		if !summary.MoovBeforeMdat || summary.TrackCount != 2 || summary.Width != 640 || summary.Height != 360 {
			t.Fatalf("unexpected Enhanced FLV MP4 summary: %+v", summary)
		}
		if math.Abs(summary.DurationSec-2.4) > 0.15 {
			t.Fatalf("expected ~2.4s normalized duration (not 12.4s!), got %.3fs", summary.DurationSec)
		}
	})
}

func TestFLVTimestamp32BitAnd31BitWrapAndJitterClamp(t *testing.T) {
	sps := mustHex(t, testAVCSPSHex)
	pps := mustHex(t, testAVCPPSHex)
	idr := mustHex(t, testAVCSliceHex)
	pSlice := mustHex(t, testAVCPSlice)
	avcC := buildAVCDecConfBytes(t, sps, pps, 4)

	runWrapCase := func(t *testing.T, startRaw uint32, wrapMod uint64) {
		t.Helper()
		fb := newFLVBuilder(0x05)
		fb.writeClassicVideoConfig(0, flvVideoCodecAVC, avcC)
		fb.writeAACConfig(0, testAACConfig48kStereo)

		// 60 frames @ 40ms = 2.4s crossing wrap boundary, plus a -200ms intra-track jitter mid-stream.
		for i := 0; i < 60; i++ {
			offsetMs := int64(i * 40)
			if i == 35 {
				offsetMs -= 200 // [-1000ms, 0] jitter: must clamp monotonically without splitting!
			}
			var rawTS uint32
			sum := uint64(startRaw) + uint64(offsetMs)
			if wrapMod == (1 << 31) {
				if sum >= (1 << 31) {
					rawTS = uint32(sum - (1 << 31))
				} else {
					rawTS = uint32(sum)
				}
			} else {
				rawTS = uint32(sum) // Natural uint32 wrap at 2^32
			}
			fb.writeAACFrame(rawTS, testAACSilentFrame)
			isKey := i%25 == 0
			nalu := pSlice
			if isKey {
				nalu = idr
			}
			fb.writeClassicVideoFrame(rawTS, flvVideoCodecAVC, isKey, 0, packLengthPrefixedNALUs(4, nalu))
		}

		_, summary, _ := remuxSinglePartAndFinalize(t, fb.buf.Bytes())
		if !summary.MoovBeforeMdat || summary.TrackCount != 2 {
			t.Fatalf("wrap remux invalid summary: %+v", summary)
		}
		if math.Abs(summary.DurationSec-2.4) > 0.15 {
			t.Fatalf("expected ~2.4s across wrap, got %.3fs", summary.DurationSec)
		}
	}

	t.Run("Unsigned32BitWrap", func(t *testing.T) {
		runWrapCase(t, 0xFFFFFC00, 1<<32)
	})
	t.Run("Signed31BitOverflowWrap", func(t *testing.T) {
		runWrapCase(t, 0x7FFFFC00, 1<<31)
	})
}

func TestFLVPartSplitOnParamSetChangeAndTimestampJump(t *testing.T) {
	vps := mustHex(t, testHEVCVPSHex)
	sps1 := mustHex(t, testHEVCSPS1Hex) // 1280x720
	sps2 := mustHex(t, testHEVCSPS2Hex) // 960x540
	pps := mustHex(t, testHEVCPPSHex)
	idr := mustHex(t, testHEVCIDRHex)
	trail := mustHex(t, testHEVCTrailHex)

	hvcC1 := buildHEVCDecConfBytes(t, vps, sps1, pps, 4)
	hvcC2 := buildHEVCDecConfBytes(t, vps, sps2, pps, 4)

	fb := newFLVBuilder(0x05)
	fb.writeEnhancedVideo(0, 1, flvExPacketSequenceStart, "hvc1", hvcC1)
	fb.writeAACConfig(0, testAACConfig48kStereo)

	// Part 1: 0..1.6s @ 1280x720, including an identical hvcC1 resend at 0.8s (must NOT split!).
	for i := 0; i < 40; i++ {
		ts := uint32(i * 40)
		if i == 20 {
			fb.writeEnhancedVideo(ts, 1, flvExPacketSequenceStart, "hvc1", hvcC1)
		}
		fb.writeAACFrame(ts, testAACSilentFrame)
		ft := uint8(2)
		nalu := trail
		if i%20 == 0 {
			ft = 1
			nalu = idr
		}
		fb.writeEnhancedVideo(ts, ft, flvExPacketCodedFramesX, "hvc1", packLengthPrefixedNALUs(4, nalu))
	}

	// Real parameter-set change to 960x540 -> triggers Part 2!
	fb.writeEnhancedVideo(1600, 1, flvExPacketSequenceStart, "hvc1", hvcC2)
	for i := 0; i < 35; i++ {
		ts := uint32(1600 + i*40)
		fb.writeAACFrame(ts, testAACSilentFrame)
		ft := uint8(2)
		nalu := trail
		if i%20 == 0 {
			ft = 1
			nalu = idr
		}
		fb.writeEnhancedVideo(ts, ft, flvExPacketCodedFramesX, "hvc1", packLengthPrefixedNALUs(4, nalu))
	}

	// > +10s forward jump (from ~3000ms to 20000ms) -> triggers Part 3!
	for i := 0; i < 35; i++ {
		ts := uint32(20000 + i*40)
		fb.writeAACFrame(ts, testAACSilentFrame)
		ft := uint8(2)
		nalu := trail
		if i%20 == 0 {
			ft = 1
			nalu = idr
		}
		fb.writeEnhancedVideo(ts, ft, flvExPacketCodedFramesX, "hvc1", packLengthPrefixedNALUs(4, nalu))
	}

	// < -1s backward jump (encoder reset from ~21400ms back to 0ms) -> triggers Part 4!
	for i := 0; i < 35; i++ {
		ts := uint32(i * 40)
		fb.writeAACFrame(ts, testAACSilentFrame)
		ft := uint8(2)
		nalu := trail
		if i%20 == 0 {
			ft = 1
			nalu = idr
		}
		fb.writeEnhancedVideo(ts, ft, flvExPacketCodedFramesX, "hvc1", packLengthPrefixedNALUs(4, nalu))
	}

	dir := t.TempDir()
	var finalized []download.MuxTrackSummary
	parts, err := RemuxFLVStream(
		context.Background(),
		bytes.NewReader(fb.buf.Bytes()),
		FLVRemuxOptions{},
		func(partIndex int) (PartTarget, func(FLVCheckpoint, error) error, error) {
			partPath := filepath.Join(dir, fmt.Sprintf("part-%03d.mp4.part", partIndex))
			finalPath := filepath.Join(dir, fmt.Sprintf("part-%03d.mp4", partIndex))
			f, err := os.OpenFile(partPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
			if err != nil {
				return nil, nil, err
			}
			return f, func(cp FLVCheckpoint, writeErr error) error {
				_ = f.Close()
				if writeErr != nil || cp.BytesWritten == 0 {
					return writeErr
				}
				s, err := finalizeLivePart(context.Background(), partPath, finalPath, cp.DurationSec)
				if err != nil {
					return err
				}
				finalized = append(finalized, s)
				return nil
			}, nil
		},
		nil,
	)
	if err != nil {
		t.Fatalf("RemuxFLVStream multi-part failed: %v", err)
	}
	if len(parts) != 4 || len(finalized) != 4 {
		t.Fatalf("expected exactly 4 parts (identical resend ignored, SPS change + >10s jump + <-1s jump split), got parts=%d finalized=%d", len(parts), len(finalized))
	}
	if finalized[0].Width != 640 || finalized[0].Height != 360 {
		t.Fatalf("part 1 expected 640x360, got %dx%d", finalized[0].Width, finalized[0].Height)
	}
	if finalized[1].Width != 960 || finalized[1].Height != 540 {
		t.Fatalf("part 2 expected 960x540, got %dx%d", finalized[1].Width, finalized[1].Height)
	}
	if finalized[2].Width != 960 || finalized[2].Height != 540 || !finalized[2].MoovBeforeMdat {
		t.Fatalf("part 3 unexpected summary: %+v", finalized[2])
	}
	if finalized[3].Width != 960 || finalized[3].Height != 540 || !finalized[3].MoovBeforeMdat {
		t.Fatalf("part 4 unexpected summary: %+v", finalized[3])
	}
}

func TestFLVSingleVideoTrack3SecondFallbackWithoutAudio(t *testing.T) {
	sps := mustHex(t, testAVCSPSHex)
	pps := mustHex(t, testAVCPPSHex)
	idr := mustHex(t, testAVCSliceHex)
	pSlice := mustHex(t, testAVCPSlice)

	fb := newFLVBuilder(0x01) // Video only, zero audio tags over 3.6 seconds
	fb.writeClassicVideoConfig(0, flvVideoCodecAVC, buildAVCDecConfBytes(t, sps, pps, 4))
	for i := 0; i < 90; i++ {
		ts := uint32(i * 40) // 3.6s
		isKey := i%25 == 0
		nalu := pSlice
		if isKey {
			nalu = idr
		}
		fb.writeClassicVideoFrame(ts, flvVideoCodecAVC, isKey, 0, packLengthPrefixedNALUs(4, nalu))
	}

	cp, summary, _ := remuxSinglePartAndFinalize(t, fb.buf.Bytes())
	if cp.HasAudio || summary.TrackCount != 1 {
		t.Fatalf("expected 1-track video-only MP4 after 3s fallback, got cp=%+v summary=%+v", cp, summary)
	}
	if !summary.MoovBeforeMdat || math.Abs(summary.DurationSec-3.6) > 0.15 {
		t.Fatalf("unexpected video-only MP4 summary: %+v", summary)
	}
}

func TestFLVMalformedParameterSetsAndUnsupportedCodecsNeverPanic(t *testing.T) {
	vps := mustHex(t, testHEVCVPSHex)
	sps := mustHex(t, testHEVCSPS1Hex)
	pps := mustHex(t, testHEVCPPSHex)
	idr := mustHex(t, testHEVCIDRHex)

	// 1. Empty VPS / empty SPS / empty PPS in HEVC hvcC must return FLVFormatError without panicking!
	for _, tc := range []struct {
		name string
		v    []byte
		s    []byte
		p    []byte
	}{
		{name: "EmptySPS", v: vps, s: nil, p: pps},
		{name: "EmptyVPS", v: nil, s: sps, p: pps},
		{name: "EmptyPPS", v: vps, s: sps, p: nil},
	} {
		t.Run("HEVC_"+tc.name, func(t *testing.T) {
			fb := newFLVBuilder(0x01)
			badHvcC := buildHEVCDecConfBytes(t, tc.v, tc.s, tc.p, 4)
			fb.writeEnhancedVideo(0, 1, flvExPacketSequenceStart, "hvc1", badHvcC)
			fb.writeEnhancedVideo(0, 1, flvExPacketCodedFramesX, "hvc1", packLengthPrefixedNALUs(4, idr))

			partPath := filepath.Join(t.TempDir(), "bad.mp4.part")
			f, _ := os.Create(partPath)
			defer f.Close()

			_, err := NewFLVRemuxer(bytes.NewReader(fb.buf.Bytes()), FLVRemuxOptions{}).WritePart(context.Background(), f, nil)
			if err == nil || !IsFLVFormatError(err) {
				t.Fatalf("expected FLVFormatError for %s without panic, got %v", tc.name, err)
			}
		})
	}

	// 2. AVC avcC with 0 SPS must return FLVFormatError without panicking!
	t.Run("AVC_EmptySPS", func(t *testing.T) {
		fb := newFLVBuilder(0x01)
		// Version=1, profile=0x64, compat=0, level=0x1e, lengthSizeMinusOne=0xff, numSPS=0xe0 (0), numPPS=0x01, ppsLen=4, pps
		badAvcC := []byte{0x01, 0x64, 0x00, 0x1e, 0xff, 0xe0, 0x01, 0x00, 0x04, 0x68, 0xeb, 0xec, 0xb2}
		fb.writeClassicVideoConfig(0, flvVideoCodecAVC, badAvcC)

		f, _ := os.Create(filepath.Join(t.TempDir(), "bad-avc.mp4.part"))
		defer f.Close()
		_, err := NewFLVRemuxer(bytes.NewReader(fb.buf.Bytes()), FLVRemuxOptions{}).WritePart(context.Background(), f, nil)
		if err == nil || !IsFLVFormatError(err) {
			t.Fatalf("expected FLVFormatError for empty AVC SPS, got %v", err)
		}
	})

	// 3. Non-AAC audio (MP3 SoundFormat=2) -> UnsupportedCodecError.
	t.Run("UnsupportedMP3Audio", func(t *testing.T) {
		fb := newFLVBuilder(0x04)
		fb.writeTag(flvTagTypeAudio, 0, []byte{0x2F, 0x11, 0x22, 0x33})
		f, _ := os.Create(filepath.Join(t.TempDir(), "mp3.mp4.part"))
		defer f.Close()
		_, err := NewFLVRemuxer(bytes.NewReader(fb.buf.Bytes()), FLVRemuxOptions{}).WritePart(context.Background(), f, nil)
		if !IsUnsupportedCodec(err) {
			t.Fatalf("expected UnsupportedCodecError for MP3 audio, got %v", err)
		}
	})

	// 4. Audio-only live stream (>3s AAC without any video) -> UnsupportedCodecError.
	t.Run("AudioOnlyLiveRejected", func(t *testing.T) {
		fb := newFLVBuilder(0x04)
		fb.writeAACConfig(0, testAACConfig48kStereo)
		for i := 0; i < 160; i++ {
			fb.writeAACFrame(uint32(i*25), testAACSilentFrame) // 4.0s of AAC, no video
		}
		f, _ := os.Create(filepath.Join(t.TempDir(), "audio-only.mp4.part"))
		defer f.Close()
		_, err := NewFLVRemuxer(bytes.NewReader(fb.buf.Bytes()), FLVRemuxOptions{}).WritePart(context.Background(), f, nil)
		if !IsUnsupportedCodec(err) {
			t.Fatalf("expected UnsupportedCodecError for audio-only stream, got %v", err)
		}
	})
}

func TestFLVCheckpointTruncatedTailAndOrphanPartRecovery(t *testing.T) {
	sps := mustHex(t, testAVCSPSHex)
	pps := mustHex(t, testAVCPPSHex)
	idr := mustHex(t, testAVCSliceHex)
	pSlice := mustHex(t, testAVCPSlice)

	fb := newFLVBuilder(0x05)
	fb.writeClassicVideoConfig(0, flvVideoCodecAVC, buildAVCDecConfBytes(t, sps, pps, 4))
	fb.writeAACConfig(0, testAACConfig48kStereo)
	for i := 0; i < 75; i++ {
		ts := uint32(i * 40) // 3.0s
		fb.writeAACFrame(ts, testAACSilentFrame)
		isKey := i%25 == 0
		nalu := pSlice
		if isKey {
			nalu = idr
		}
		fb.writeClassicVideoFrame(ts, flvVideoCodecAVC, isKey, 0, packLengthPrefixedNALUs(4, nalu))
	}
	// Append a truncated half-FLV video tag at the tail to simulate abrupt TCP drop!
	fb.buf.Write([]byte{flvTagTypeVideo, 0x00, 0x01, 0x00, 0x00, 0x0b, 0xb8, 0x00, 0x00, 0x00, 0x00, 0x17, 0x01})

	root := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(root, "library.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	lib, err := library.OpenWithDataDir(db, filepath.Join(root, "cache"), root)
	if err != nil {
		t.Fatalf("OpenWithDataDir: %v", err)
	}
	itemDir := filepath.Join(lib.CacheDir(), "douyin", "sec123456789", "lives", "7300001")
	if err := os.MkdirAll(itemDir, 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	pendingRel := "part-001.mp4.part"
	meta, err := lib.SaveItem(itemDir, library.ItemMetadata{
		Site:         "douyin",
		Kind:         "live",
		ID:           "douyin:live:7300001",
		Status:       "recording",
		PendingParts: []string{pendingRel},
		Live: &library.LiveRecordingInfo{
			RoomID:    7300001,
			LiveSince: 1794000000,
			Codec:     "avc",
		},
	})
	if err != nil {
		t.Fatalf("SaveItem: %v", err)
	}

	partPath := filepath.Join(itemDir, pendingRel)
	partFile, err := os.OpenFile(partPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if err != nil {
		t.Fatalf("create part file: %v", err)
	}

	remuxer := NewFLVRemuxer(bytes.NewReader(fb.buf.Bytes()), FLVRemuxOptions{})
	cp, writeErr := remuxer.WritePart(context.Background(), partFile, func(c FLVCheckpoint) error {
		meta.Live.PendingFile = pendingRel
		meta.Live.PendingBytes = c.BytesWritten
		meta.Live.PendingDuration = c.DurationSec
		meta.Live.LastSequence = c.Sequence
		meta.Live.HasSequence = true
		saved, saveErr := lib.SaveRecordingCheckpoint(itemDir, meta)
		if saveErr == nil {
			meta = saved
		}
		return saveErr
	})
	if !errors.Is(writeErr, ErrFLVTruncated) {
		_ = partFile.Close()
		t.Fatalf("expected ErrFLVTruncated on truncated FLV tail, got %v", writeErr)
	}
	if cp.BytesWritten <= 0 || cp.DurationSec < 2.8 {
		_ = partFile.Close()
		t.Fatalf("expected complete GOPs before truncated tail to be checkpointed, got %+v", cp)
	}

	// Simulate an uncheckpointed partial moof+mdat write right before process crash:
	// bytes past meta.Live.PendingBytes MUST be truncated by RecoverOrphanParts!
	if _, err := partFile.Write([]byte{0x00, 0x00, 0x10, 0x00, 'm', 'o', 'o', 'f', 0xde, 0xad, 0xbe, 0xef}); err != nil {
		t.Fatalf("append uncheckpointed crash tail: %v", err)
	}
	_ = partFile.Close()

	mgr := NewManager(lib, nil, nil, settings.Default(lib.CacheDir()).Live, nil, nil)
	mgr.RecoverOrphanParts()

	finalPath := filepath.Join(itemDir, "part-001.mp4")
	summary, err := download.InspectMP4File(finalPath)
	if err != nil {
		t.Fatalf("InspectMP4File recovered part-001.mp4: %v", err)
	}
	if !summary.MoovBeforeMdat || summary.TrackCount != 2 || math.Abs(summary.DurationSec-cp.DurationSec) > 0.1 {
		t.Fatalf("unexpected recovered MP4 summary: %+v (checkpoint=%.3fs)", summary, cp.DurationSec)
	}
	if _, statErr := os.Stat(partPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected recovered .part file to be removed after manifest commit, got %v", statErr)
	}
}
