package live

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/Eyevinn/mp4ff/aac"
	"github.com/Eyevinn/mp4ff/avc"
	"github.com/Eyevinn/mp4ff/hevc"
	"github.com/chialecode/trace/internal/site"
)

const (
	flvHeaderSize      = 9
	flvTagHeaderSize   = 11
	flvMaxHeaderOffset = 64 * 1024
	flvMaxTagDataSize  = 32 * 1024 * 1024 // 32 MiB upper bound per FLV tag

	flvTagTypeAudio  uint8 = 8
	flvTagTypeVideo  uint8 = 9
	flvTagTypeScript uint8 = 18

	flvSoundFormatAAC uint8 = 10

	flvVideoCodecAVC  uint8 = 7
	flvVideoCodecHEVC uint8 = 12 // Domestic HTTP-FLV HEVC CodecID

	flvExPacketSequenceStart uint8 = 0
	flvExPacketCodedFrames   uint8 = 1
	flvExPacketSequenceEnd   uint8 = 2
	flvExPacketCodedFramesX  uint8 = 3
	flvExPacketMetadata      uint8 = 5
)

// VideoCodec identifies the normalized ISO BMFF sample entry FourCC for live video.
type VideoCodec string

const (
	VideoCodecAVC  VideoCodec = "avc1"
	VideoCodecHEVC VideoCodec = "hvc1"
)

// ErrFLVTruncated indicates an unexpected EOF inside a tag or a PreviousTagSize mismatch.
var ErrFLVTruncated = errors.New("live: FLV 标签截断或长度校验失败")

// UnsupportedCodecError indicates an unsupported FLV video/audio codec, encrypted tag,
// or audio-only stream without a video track.
type UnsupportedCodecError struct {
	Media  string
	Codec  string
	Reason string
}

func (e *UnsupportedCodecError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("live: %s (%s/%s)", e.Reason, e.Media, e.Codec)
	}
	return fmt.Sprintf("live: 不支持的直播编码 (%s/%s)", e.Media, e.Codec)
}

func (e *UnsupportedCodecError) Unwrap() error {
	return &site.Error{
		Kind:        site.ErrKindBlocked,
		Unsupported: true,
		Message:     e.Error(),
	}
}

// IsUnsupportedCodec reports whether err represents an unsupported live codec or stream track layout.
func IsUnsupportedCodec(err error) bool {
	var target *UnsupportedCodecError
	return errors.As(err, &target)
}

// FLVFormatError indicates malformed container headers, invalid codec configuration records,
// empty parameter sets, or corrupt length-prefixed NALU framing.
type FLVFormatError struct {
	Reason string
	Err    error
}

func (e *FLVFormatError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("live: FLV 格式错误：%s: %v", e.Reason, e.Err)
	}
	return fmt.Sprintf("live: FLV 格式错误：%s", e.Reason)
}

func (e *FLVFormatError) Unwrap() error {
	return &site.Error{
		Kind:    site.ErrKindSchemaChanged,
		Message: e.Error(),
		Err:     e.Err,
	}
}

// IsFLVFormatError reports whether err is a malformed FLV/codec structure error.
func IsFLVFormatError(err error) bool {
	var target *FLVFormatError
	return errors.As(err, &target)
}

// FLVHeader represents the 9-byte FLV stream header.
type FLVHeader struct {
	Version    uint8
	HasAudio   bool
	HasVideo   bool
	DataOffset uint32
}

// FLVTag represents one raw FLV tag verified against its trailing PreviousTagSize.
type FLVTag struct {
	Type      uint8
	Timestamp uint32 // 32-bit raw millisecond timestamp (24-bit lower + 8-bit extended)
	Data      []byte
}

// FLVReader sequentially reads an FLV byte stream and validates tag framing.
type FLVReader struct {
	r          io.Reader
	headerRead bool
	header     FLVHeader
	scratch    [flvTagHeaderSize]byte
	tail       [4]byte
}

// NewFLVReader creates a streaming FLV reader over r.
func NewFLVReader(r io.Reader) *FLVReader {
	return &FLVReader{r: r}
}

// ReadHeader reads and validates the 9-byte FLV header and initial PreviousTagSize0.
// Calling ReadHeader more than once returns the cached header.
func (fr *FLVReader) ReadHeader() (FLVHeader, error) {
	if fr.headerRead {
		return fr.header, nil
	}
	var hdr [flvHeaderSize]byte
	if _, err := io.ReadFull(fr.r, hdr[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return FLVHeader{}, &FLVFormatError{Reason: "文件头不完整", Err: err}
		}
		return FLVHeader{}, err
	}
	if hdr[0] != 'F' || hdr[1] != 'L' || hdr[2] != 'V' {
		return FLVHeader{}, &FLVFormatError{Reason: "缺少 FLV 签名"}
	}
	if hdr[3] != 1 {
		return FLVHeader{}, &FLVFormatError{Reason: fmt.Sprintf("不支持的 FLV 版本 %d", hdr[3])}
	}
	offset := binary.BigEndian.Uint32(hdr[5:9])
	if offset < flvHeaderSize || offset > flvMaxHeaderOffset {
		return FLVHeader{}, &FLVFormatError{Reason: fmt.Sprintf("异常的 FLV DataOffset %d", offset)}
	}
	if extra := int64(offset - flvHeaderSize); extra > 0 {
		if _, err := io.CopyN(io.Discard, fr.r, extra); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return FLVHeader{}, &FLVFormatError{Reason: "扩展文件头截断", Err: err}
			}
			return FLVHeader{}, err
		}
	}
	if _, err := io.ReadFull(fr.r, fr.tail[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return FLVHeader{}, &FLVFormatError{Reason: "缺少首个 PreviousTagSize0", Err: err}
		}
		return FLVHeader{}, err
	}
	if prev0 := binary.BigEndian.Uint32(fr.tail[:]); prev0 != 0 {
		return FLVHeader{}, &FLVFormatError{Reason: fmt.Sprintf("首个 PreviousTagSize0 非零 (%d)", prev0)}
	}
	fr.header = FLVHeader{
		Version:    hdr[3],
		HasAudio:   hdr[4]&0x04 != 0,
		HasVideo:   hdr[4]&0x01 != 0,
		DataOffset: offset,
	}
	fr.headerRead = true
	return fr.header, nil
}

// NextTag reads the next complete FLV tag and validates PreviousTagSize.
// Returns io.EOF only on a clean EOF at a tag boundary; mid-tag EOF or
// PreviousTagSize mismatch returns an error wrapping ErrFLVTruncated.
func (fr *FLVReader) NextTag() (FLVTag, error) {
	if !fr.headerRead {
		if _, err := fr.ReadHeader(); err != nil {
			return FLVTag{}, err
		}
	}
	n, err := io.ReadFull(fr.r, fr.scratch[:])
	if err != nil {
		if err == io.EOF && n == 0 {
			return FLVTag{}, io.EOF
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return FLVTag{}, fmt.Errorf("%w: 标签头仅读到 %d 字节", ErrFLVTruncated, n)
		}
		return FLVTag{}, err
	}

	if fr.scratch[0]&0x20 != 0 {
		return FLVTag{}, &UnsupportedCodecError{
			Media:  "stream",
			Codec:  "encrypted-flv",
			Reason: "不支持加密的直播流",
		}
	}
	tagType := fr.scratch[0] & 0x1f
	dataSize := uint32(fr.scratch[1])<<16 | uint32(fr.scratch[2])<<8 | uint32(fr.scratch[3])
	if dataSize > flvMaxTagDataSize {
		return FLVTag{}, &FLVFormatError{Reason: fmt.Sprintf("单标签长度超限 (%d 字节)", dataSize)}
	}
	rawTS := uint32(fr.scratch[4])<<16 | uint32(fr.scratch[5])<<8 | uint32(fr.scratch[6]) | uint32(fr.scratch[7])<<24

	data := make([]byte, dataSize)
	if dataSize > 0 {
		if _, err := io.ReadFull(fr.r, data); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return FLVTag{}, fmt.Errorf("%w: 标签载荷截断", ErrFLVTruncated)
			}
			return FLVTag{}, err
		}
	}
	if _, err := io.ReadFull(fr.r, fr.tail[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return FLVTag{}, fmt.Errorf("%w: 缺少 PreviousTagSize", ErrFLVTruncated)
		}
		return FLVTag{}, err
	}
	expectedPrev := flvTagHeaderSize + dataSize
	if gotPrev := binary.BigEndian.Uint32(fr.tail[:]); gotPrev != expectedPrev {
		return FLVTag{}, fmt.Errorf("%w: PreviousTagSize=%d 期望=%d", ErrFLVTruncated, gotPrev, expectedPrev)
	}
	return FLVTag{
		Type:      tagType,
		Timestamp: rawTS,
		Data:      data,
	}, nil
}

// VideoConfig holds validated AVC or HEVC decoder configuration and parameter sets.
type VideoConfig struct {
	Codec       VideoCodec
	Width       int
	Height      int
	NALULenSize int
	VPS         [][]byte
	SPS         [][]byte
	PPS         [][]byte
	SEI         [][]byte
	Canonical   []byte // Canonical parameter-set fingerprint (ignores NALU length prefix size)
}

// AudioConfig holds validated AAC AudioSpecificConfig parameters.
type AudioConfig struct {
	ASC             *aac.AudioSpecificConfig
	SampleRate      int
	Channels        int
	SamplesPerFrame uint32
	Canonical       []byte
}

// ParsedTagKind classifies a parsed FLV tag.
type ParsedTagKind uint8

const (
	ParsedTagIgnored ParsedTagKind = iota
	ParsedTagScript
	ParsedTagVideoConfig
	ParsedTagVideoFrame
	ParsedTagVideoEnd
	ParsedTagAudioConfig
	ParsedTagAudioFrame
)

// ParsedTag contains the decoded media payload or codec configuration of one FLVTag.
type ParsedTag struct {
	Kind        ParsedTagKind
	Timestamp   uint32
	VideoCodec  VideoCodec
	VideoConfig *VideoConfig
	AudioConfig *AudioConfig
	IsKeyframe  bool
	CTSMs       int32
	RawPayload  []byte // Coded video payload (before NALU length normalization) or raw AAC frame
}

// ParseFLVTag decodes one FLVTag into a typed media/config event without consulting onMetaData.
func ParseFLVTag(tag FLVTag) (ParsedTag, error) {
	switch tag.Type {
	case flvTagTypeScript:
		return ParsedTag{Kind: ParsedTagScript, Timestamp: tag.Timestamp}, nil
	case flvTagTypeAudio:
		return parseFLVAudioTag(tag)
	case flvTagTypeVideo:
		return parseFLVVideoTag(tag)
	default:
		return ParsedTag{Kind: ParsedTagIgnored, Timestamp: tag.Timestamp}, nil
	}
}

func parseFLVAudioTag(tag FLVTag) (ParsedTag, error) {
	b := tag.Data
	if len(b) == 0 {
		return ParsedTag{Kind: ParsedTagIgnored, Timestamp: tag.Timestamp}, nil
	}
	soundFormat := b[0] >> 4
	if soundFormat != flvSoundFormatAAC {
		return ParsedTag{}, &UnsupportedCodecError{
			Media:  "audio",
			Codec:  fmt.Sprintf("soundformat-%d", soundFormat),
			Reason: "直播音频仅支持 AAC",
		}
	}
	if len(b) < 2 {
		return ParsedTag{}, &FLVFormatError{Reason: "AAC 标签缺少包类型字节"}
	}
	switch b[1] {
	case 0:
		cfg, err := decodeAACConfig(b[2:])
		if err != nil {
			return ParsedTag{}, err
		}
		return ParsedTag{
			Kind:        ParsedTagAudioConfig,
			Timestamp:   tag.Timestamp,
			AudioConfig: cfg,
		}, nil
	case 1:
		if len(b) == 2 {
			return ParsedTag{Kind: ParsedTagIgnored, Timestamp: tag.Timestamp}, nil
		}
		return ParsedTag{
			Kind:       ParsedTagAudioFrame,
			Timestamp:  tag.Timestamp,
			RawPayload: b[2:],
		}, nil
	default:
		return ParsedTag{}, &FLVFormatError{Reason: fmt.Sprintf("未知的 AAC PacketType %d", b[1])}
	}
}

func parseFLVVideoTag(tag FLVTag) (ParsedTag, error) {
	b := tag.Data
	if len(b) == 0 {
		return ParsedTag{Kind: ParsedTagIgnored, Timestamp: tag.Timestamp}, nil
	}
	b0 := b[0]
	if b0&0x80 != 0 {
		return parseEnhancedVideoTag(tag.Timestamp, b)
	}
	return parseClassicVideoTag(tag.Timestamp, b)
}

func parseClassicVideoTag(ts uint32, b []byte) (ParsedTag, error) {
	frameType := (b[0] >> 4) & 0x07
	codecID := b[0] & 0x0f
	if frameType == 5 {
		return ParsedTag{Kind: ParsedTagIgnored, Timestamp: ts}, nil
	}
	var codec VideoCodec
	switch codecID {
	case flvVideoCodecAVC:
		codec = VideoCodecAVC
	case flvVideoCodecHEVC:
		codec = VideoCodecHEVC
	default:
		return ParsedTag{}, &UnsupportedCodecError{
			Media:  "video",
			Codec:  fmt.Sprintf("codecid-%d", codecID),
			Reason: "不支持的直播视频编码",
		}
	}
	if len(b) < 2 {
		return ParsedTag{}, &FLVFormatError{Reason: "视频标签缺少包类型字节"}
	}
	pktType := b[1]
	if pktType == 2 {
		return ParsedTag{
			Kind:       ParsedTagVideoEnd,
			Timestamp:  ts,
			VideoCodec: codec,
		}, nil
	}
	if len(b) < 5 {
		return ParsedTag{}, &FLVFormatError{Reason: "视频标签缺少 24 位 CTS 字段"}
	}
	ctsMs := decodeSI24(b[2:5])
	payload := b[5:]
	switch pktType {
	case 0:
		cfg, err := decodeVideoConfig(codec, payload)
		if err != nil {
			return ParsedTag{}, err
		}
		return ParsedTag{
			Kind:        ParsedTagVideoConfig,
			Timestamp:   ts,
			VideoCodec:  codec,
			VideoConfig: cfg,
		}, nil
	case 1:
		if len(payload) == 0 {
			return ParsedTag{Kind: ParsedTagIgnored, Timestamp: ts}, nil
		}
		return ParsedTag{
			Kind:       ParsedTagVideoFrame,
			Timestamp:  ts,
			VideoCodec: codec,
			IsKeyframe: frameType == 1,
			CTSMs:      ctsMs,
			RawPayload: payload,
		}, nil
	default:
		return ParsedTag{}, &FLVFormatError{Reason: fmt.Sprintf("未知的视频 AVCPacketType %d", pktType)}
	}
}

func parseEnhancedVideoTag(ts uint32, b []byte) (ParsedTag, error) {
	frameType := (b[0] >> 4) & 0x07
	pktType := b[0] & 0x0f
	if pktType == flvExPacketMetadata || frameType == 5 {
		return ParsedTag{Kind: ParsedTagIgnored, Timestamp: ts}, nil
	}
	if len(b) < 5 {
		return ParsedTag{}, &FLVFormatError{Reason: "Enhanced FLV 视频标签不足 5 字节"}
	}
	fourCC := string(b[1:5])
	var codec VideoCodec
	switch fourCC {
	case "hvc1":
		codec = VideoCodecHEVC
	case "avc1":
		codec = VideoCodecAVC
	default:
		return ParsedTag{}, &UnsupportedCodecError{
			Media:  "video",
			Codec:  fourCC,
			Reason: "不支持的 Enhanced FLV 视频编码",
		}
	}
	switch pktType {
	case flvExPacketSequenceStart:
		cfg, err := decodeVideoConfig(codec, b[5:])
		if err != nil {
			return ParsedTag{}, err
		}
		return ParsedTag{
			Kind:        ParsedTagVideoConfig,
			Timestamp:   ts,
			VideoCodec:  codec,
			VideoConfig: cfg,
		}, nil
	case flvExPacketCodedFrames:
		if len(b) < 8 {
			return ParsedTag{}, &FLVFormatError{Reason: "Enhanced FLV CodedFrames 缺少 3 字节 CTS"}
		}
		ctsMs := decodeSI24(b[5:8])
		payload := b[8:]
		if len(payload) == 0 {
			return ParsedTag{Kind: ParsedTagIgnored, Timestamp: ts}, nil
		}
		return ParsedTag{
			Kind:       ParsedTagVideoFrame,
			Timestamp:  ts,
			VideoCodec: codec,
			IsKeyframe: frameType == 1,
			CTSMs:      ctsMs,
			RawPayload: payload,
		}, nil
	case flvExPacketCodedFramesX:
		payload := b[5:]
		if len(payload) == 0 {
			return ParsedTag{Kind: ParsedTagIgnored, Timestamp: ts}, nil
		}
		return ParsedTag{
			Kind:       ParsedTagVideoFrame,
			Timestamp:  ts,
			VideoCodec: codec,
			IsKeyframe: frameType == 1,
			CTSMs:      0,
			RawPayload: payload,
		}, nil
	case flvExPacketSequenceEnd:
		return ParsedTag{
			Kind:       ParsedTagVideoEnd,
			Timestamp:  ts,
			VideoCodec: codec,
		}, nil
	default:
		return ParsedTag{}, &FLVFormatError{Reason: fmt.Sprintf("不支持的 Enhanced FLV PacketType %d", pktType)}
	}
}

func decodeSI24(b []byte) int32 {
	v := uint32(b[0])<<16 | uint32(b[1])<<8 | uint32(b[2])
	if v&0x800000 != 0 {
		v |= 0xFF000000
	}
	return int32(v)
}

func decodeVideoConfig(codec VideoCodec, raw []byte) (*VideoConfig, error) {
	switch codec {
	case VideoCodecAVC:
		return decodeAVCConfig(raw)
	case VideoCodecHEVC:
		return decodeHEVCConfig(raw)
	default:
		return nil, &UnsupportedCodecError{Media: "video", Codec: string(codec)}
	}
}

func decodeAVCConfig(raw []byte) (*VideoConfig, error) {
	if len(raw) < 7 {
		return nil, &FLVFormatError{Reason: "AVCDecoderConfigurationRecord 过短"}
	}
	lso := raw[4] & 0x03
	if lso == 2 {
		return nil, &FLVFormatError{Reason: "AVC 不允许 3 字节 NALU 长度前缀"}
	}
	naluLenSize := int(lso) + 1

	// mp4ff's DecodeAVCDecConfRec requires LengthSizeMinus1 == 3; normalize a copy
	// while recording the wire NALU prefix length for coded frame conversion.
	normalized := append([]byte(nil), raw...)
	normalized[4] |= 0x03
	dcr, err := avc.DecodeAVCDecConfRec(normalized)
	if err != nil {
		return nil, &FLVFormatError{Reason: "解析 avcC 失败", Err: err}
	}
	spsList := filterNonEmptyNALUs(dcr.SPSnalus)
	ppsList := filterNonEmptyNALUs(dcr.PPSnalus)
	if len(spsList) == 0 || len(ppsList) == 0 {
		return nil, &FLVFormatError{Reason: "AVC 参数集缺少非空 SPS 或 PPS"}
	}
	parsedSPS, err := avc.ParseSPSNALUnit(spsList[0], false)
	if err != nil || parsedSPS == nil || parsedSPS.Width == 0 || parsedSPS.Height == 0 {
		return nil, &FLVFormatError{Reason: "解析 AVC SPS 失败", Err: err}
	}
	return &VideoConfig{
		Codec:       VideoCodecAVC,
		Width:       int(parsedSPS.Width),
		Height:      int(parsedSPS.Height),
		NALULenSize: naluLenSize,
		SPS:         spsList,
		PPS:         ppsList,
		Canonical:   buildCanonicalVideoPS(VideoCodecAVC, nil, spsList, ppsList),
	}, nil
}

func decodeHEVCConfig(raw []byte) (*VideoConfig, error) {
	if len(raw) < 23 {
		return nil, &FLVFormatError{Reason: "HEVCDecoderConfigurationRecord 过短"}
	}
	lso := raw[21] & 0x03
	if lso == 2 {
		return nil, &FLVFormatError{Reason: "HEVC 不允许 3 字节 NALU 长度前缀"}
	}
	naluLenSize := int(lso) + 1

	// mp4ff's DecodeHEVCDecConfRec requires LengthSizeMinusOne == 3; normalize a copy.
	normalized := append([]byte(nil), raw...)
	normalized[21] |= 0x03
	hdcr, err := hevc.DecodeHEVCDecConfRec(normalized)
	if err != nil {
		return nil, &FLVFormatError{Reason: "解析 hvcC 失败", Err: err}
	}
	vpsList := filterNonEmptyNALUs(hdcr.GetNalusForType(hevc.NALU_VPS))
	spsList := filterNonEmptyNALUs(hdcr.GetNalusForType(hevc.NALU_SPS))
	ppsList := filterNonEmptyNALUs(hdcr.GetNalusForType(hevc.NALU_PPS))
	seiList := filterNonEmptyNALUs(hdcr.GetNalusForType(hevc.NALU_SEI_PREFIX))
	// Mandatory guard: mp4ff SetHEVCDescriptor indexes spsNALUs[0] without bounds checks,
	// and valid hvc1 sample entries require non-empty VPS, SPS, and PPS arrays.
	if len(vpsList) == 0 || len(spsList) == 0 || len(ppsList) == 0 {
		return nil, &FLVFormatError{Reason: "HEVC 参数集缺少非空 VPS、SPS 或 PPS"}
	}
	parsedSPS, err := hevc.ParseSPSNALUnit(spsList[0])
	if err != nil || parsedSPS == nil {
		return nil, &FLVFormatError{Reason: "解析 HEVC SPS 失败", Err: err}
	}
	w, h := parsedSPS.ImageSize()
	if w == 0 || h == 0 {
		return nil, &FLVFormatError{Reason: "HEVC SPS 分辨率无效"}
	}
	return &VideoConfig{
		Codec:       VideoCodecHEVC,
		Width:       int(w),
		Height:      int(h),
		NALULenSize: naluLenSize,
		VPS:         vpsList,
		SPS:         spsList,
		PPS:         ppsList,
		SEI:         seiList,
		Canonical:   buildCanonicalVideoPS(VideoCodecHEVC, vpsList, spsList, ppsList),
	}, nil
}

func decodeAACConfig(raw []byte) (*AudioConfig, error) {
	if len(raw) < 2 {
		return nil, &FLVFormatError{Reason: "AudioSpecificConfig 过短"}
	}
	asc, err := aac.DecodeAudioSpecificConfig(bytes.NewReader(raw))
	if err != nil || asc == nil {
		return nil, &FLVFormatError{Reason: "解析 AudioSpecificConfig 失败", Err: err}
	}
	channels := asc.NrChannels()
	if asc.SamplingFrequency <= 0 || channels <= 0 {
		return nil, &FLVFormatError{Reason: "AAC 采样率或声道数无效"}
	}
	samplesPerFrame := uint32(asc.CoreSamplesPerFrame())
	if samplesPerFrame == 0 {
		samplesPerFrame = 1024
	}
	var canonBuf bytes.Buffer
	if err := asc.Encode(&canonBuf); err != nil {
		return nil, &FLVFormatError{Reason: "规范化 AudioSpecificConfig 失败", Err: err}
	}
	return &AudioConfig{
		ASC:             asc,
		SampleRate:      asc.SamplingFrequency,
		Channels:        channels,
		SamplesPerFrame: samplesPerFrame,
		Canonical:       canonBuf.Bytes(),
	}, nil
}

func filterNonEmptyNALUs(in [][]byte) [][]byte {
	var out [][]byte
	for _, nalu := range in {
		if len(nalu) > 0 {
			cp := make([]byte, len(nalu))
			copy(cp, nalu)
			out = append(out, cp)
		}
	}
	return out
}

func buildCanonicalVideoPS(codec VideoCodec, vps, sps, pps [][]byte) []byte {
	var buf bytes.Buffer
	buf.WriteString(string(codec))
	for _, group := range [][][]byte{vps, sps, pps} {
		var count [4]byte
		binary.BigEndian.PutUint32(count[:], uint32(len(group)))
		buf.Write(count[:])
		for _, nalu := range group {
			var sz [4]byte
			binary.BigEndian.PutUint32(sz[:], uint32(len(nalu)))
			buf.Write(sz[:])
			buf.Write(nalu)
		}
	}
	return buf.Bytes()
}

// normalizeLengthPrefixedNALUs converts 1-, 2-, or 4-byte length-prefixed NALUs into
// canonical 4-byte big-endian length-prefixed NALUs required by avc1/hvc1 ISO BMFF samples.
func normalizeLengthPrefixedNALUs(payload []byte, naluLenSize int) ([]byte, error) {
	if naluLenSize != 1 && naluLenSize != 2 && naluLenSize != 4 {
		return nil, &FLVFormatError{Reason: fmt.Sprintf("不支持的 NALU 长度字节数 %d", naluLenSize)}
	}
	if len(payload) == 0 {
		return nil, &FLVFormatError{Reason: "视频编码帧为空"}
	}

	outCap := len(payload)
	if naluLenSize < 4 {
		outCap += 32
	}
	out := make([]byte, 0, outCap)
	for off := 0; off < len(payload); {
		if len(payload)-off < naluLenSize {
			return nil, &FLVFormatError{Reason: "NALU 长度前缀截断"}
		}
		var naluLen uint32
		switch naluLenSize {
		case 1:
			naluLen = uint32(payload[off])
		case 2:
			naluLen = uint32(binary.BigEndian.Uint16(payload[off : off+2]))
		case 4:
			naluLen = binary.BigEndian.Uint32(payload[off : off+4])
		}
		off += naluLenSize
		if naluLen == 0 {
			continue
		}
		if uint64(naluLen) > uint64(len(payload)-off) {
			return nil, &FLVFormatError{Reason: "NALU 载荷长度越界"}
		}
		var prefix [4]byte
		binary.BigEndian.PutUint32(prefix[:], naluLen)
		out = append(out, prefix[:]...)
		out = append(out, payload[off:off+int(naluLen)]...)
		off += int(naluLen)
	}
	if len(out) == 0 {
		return nil, &FLVFormatError{Reason: "视频帧不含有效非空 NALU"}
	}
	return out, nil
}
