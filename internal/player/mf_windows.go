//go:build windows

package player

import (
	"fmt"
	"image"
	"io"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modOle32       = windows.NewLazySystemDLL("ole32.dll")
	modMFPlat      = windows.NewLazySystemDLL("mfplat.dll")
	modMFReadWrite = windows.NewLazySystemDLL("mfreadwrite.dll")

	procCoInitializeEx              = modOle32.NewProc("CoInitializeEx")
	procPropVariantClear            = modOle32.NewProc("PropVariantClear")
	procMFStartup                   = modMFPlat.NewProc("MFStartup")
	procMFShutdown                  = modMFPlat.NewProc("MFShutdown")
	procMFCreateAttributes          = modMFPlat.NewProc("MFCreateAttributes")
	procMFCreateMediaType           = modMFPlat.NewProc("MFCreateMediaType")
	procMFCreateSourceReaderFromURL = modMFReadWrite.NewProc("MFCreateSourceReaderFromURL")
)

const (
	mfVersionWin10      = 0x00020070 // (MF_SDK_VERSION << 16) | MF_API_VERSION
	coinitMultithreaded = 0x0

	mfSourceReaderFirstVideoStream = 0xFFFFFFFC
	mfSourceReaderFirstAudioStream = 0xFFFFFFFD
	mfSourceReaderMediaSource      = 0xFFFFFFFF

	mfSourceReaderFlagError       = 0x00000001
	mfSourceReaderFlagEndOfStream = 0x00000002

	vtI8  = 20
	vtUI8 = 21
)

var (
	guidNull = windows.GUID{}

	// MF_SOURCE_READER_ENABLE_VIDEO_PROCESSING {fb394f3d-ccf1-42ee-bbb3-f9b845d5681d}
	guidMFSourceReaderEnableVideoProcessing = windows.GUID{
		Data1: 0xfb394f3d, Data2: 0xccf1, Data3: 0x42ee,
		Data4: [8]byte{0xbb, 0xb3, 0xf9, 0xb8, 0x45, 0xd5, 0x68, 0x1d},
	}
	// MF_MT_MAJOR_TYPE {48eba18e-f8c9-4687-bf11-0a74c9f96a8f}
	guidMFMTMajorType = windows.GUID{
		Data1: 0x48eba18e, Data2: 0xf8c9, Data3: 0x4687,
		Data4: [8]byte{0xbf, 0x11, 0x0a, 0x74, 0xc9, 0xf9, 0x6a, 0x8f},
	}
	// MF_MT_SUBTYPE {f7e34c9a-42e8-4714-b74b-cb29d72c35e5}
	guidMFMTSubtype = windows.GUID{
		Data1: 0xf7e34c9a, Data2: 0x42e8, Data3: 0x4714,
		Data4: [8]byte{0xb7, 0x4b, 0xcb, 0x29, 0xd7, 0x2c, 0x35, 0xe5},
	}
	// MF_MT_FRAME_SIZE {1652c33d-d6b2-4012-b834-72030849a37d}
	guidMFMTFrameSize = windows.GUID{
		Data1: 0x1652c33d, Data2: 0xd6b2, Data3: 0x4012,
		Data4: [8]byte{0xb8, 0x34, 0x72, 0x03, 0x08, 0x49, 0xa3, 0x7d},
	}
	// MF_MT_FRAME_RATE {c459a2e8-3d2c-4e44-b132-fee5156c7bb0}
	guidMFMTFrameRate = windows.GUID{
		Data1: 0xc459a2e8, Data2: 0x3d2c, Data3: 0x4e44,
		Data4: [8]byte{0xb1, 0x32, 0xfe, 0xe5, 0x15, 0x6c, 0x7b, 0xb0},
	}
	// MF_MT_DEFAULT_STRIDE {644b4e48-1e02-4516-b0eb-c01ca9d49ac6}
	guidMFMTDefaultStride = windows.GUID{
		Data1: 0x644b4e48, Data2: 0x1e02, Data3: 0x4516,
		Data4: [8]byte{0xb0, 0xeb, 0xc0, 0x1c, 0xa9, 0xd4, 0x9a, 0xc6},
	}
	// IID_IMF2DBuffer {7DC9D5F9-9ED9-44ec-9BBF-0600BB589FBB}
	iidIMF2DBuffer = windows.GUID{
		Data1: 0x7dc9d5f9, Data2: 0x9ed9, Data3: 0x44ec,
		Data4: [8]byte{0x9b, 0xbf, 0x06, 0x00, 0xbb, 0x58, 0x9f, 0xbb},
	}
	// MF_MT_AUDIO_NUM_CHANNELS {37e48bf5-645e-4c5b-89de-ada9e29b696a}
	guidMFMTAudioNumChannels = windows.GUID{
		Data1: 0x37e48bf5, Data2: 0x645e, Data3: 0x4c5b,
		Data4: [8]byte{0x89, 0xde, 0xad, 0xa9, 0xe2, 0x9b, 0x69, 0x6a},
	}
	// MF_MT_AUDIO_SAMPLES_PER_SECOND {5faeeae7-0290-4c31-9e8a-c534f68d9dba}
	guidMFMTAudioSamplesPerSecond = windows.GUID{
		Data1: 0x5faeeae7, Data2: 0x0290, Data3: 0x4c31,
		Data4: [8]byte{0x9e, 0x8a, 0xc5, 0x34, 0xf6, 0x8d, 0x9d, 0xba},
	}
	// MF_MT_AUDIO_BITS_PER_SAMPLE {f2deb57f-40fa-4764-aa33-ed4f2d1ff669}
	guidMFMTAudioBitsPerSample = windows.GUID{
		Data1: 0xf2deb57f, Data2: 0x40fa, Data3: 0x4764,
		Data4: [8]byte{0xaa, 0x33, 0xed, 0x4f, 0x2d, 0x1f, 0xf6, 0x69},
	}
	// MF_PD_DURATION {6c990d33-bb8e-477a-8598-0d5d96fcd88a}
	guidMFPDuration = windows.GUID{
		Data1: 0x6c990d33, Data2: 0xbb8e, Data3: 0x477a,
		Data4: [8]byte{0x85, 0x98, 0x0d, 0x5d, 0x96, 0xfc, 0xd8, 0x8a},
	}
	// MFMediaType_Video {73646976-0000-0010-8000-00AA00389B71}
	guidMFMediaTypeVideo = windows.GUID{
		Data1: 0x73646976, Data2: 0x0000, Data3: 0x0010,
		Data4: [8]byte{0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71},
	}
	// MFMediaType_Audio {73647561-0000-0010-8000-00AA00389B71}
	guidMFMediaTypeAudio = windows.GUID{
		Data1: 0x73647561, Data2: 0x0000, Data3: 0x0010,
		Data4: [8]byte{0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71},
	}
	// MFVideoFormat_RGB32 {00000016-0000-0010-8000-00aa00389b71}
	guidMFVideoFormatRGB32 = windows.GUID{
		Data1: 0x00000016, Data2: 0x0000, Data3: 0x0010,
		Data4: [8]byte{0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71},
	}
	// MFVideoFormat_NV12 {3231564E-0000-0010-8000-00AA00389B71}
	guidMFVideoFormatNV12 = windows.GUID{
		Data1: 0x3231564E, Data2: 0x0000, Data3: 0x0010,
		Data4: [8]byte{0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71},
	}
	// MFAudioFormat_Float {00000003-0000-0010-8000-00aa00389b71}
	guidMFAudioFormatFloat = windows.GUID{
		Data1: 0x00000003, Data2: 0x0000, Data3: 0x0010,
		Data4: [8]byte{0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71},
	}
	// MFAudioFormat_PCM {00000001-0000-0010-8000-00aa00389b71}
	guidMFAudioFormatPCM = windows.GUID{
		Data1: 0x00000001, Data2: 0x0000, Data3: 0x0010,
		Data4: [8]byte{0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71},
	}
)

type propVariant struct {
	vt         uint16
	wReserved1 uint16
	wReserved2 uint16
	wReserved3 uint16
	hVal       int64
	pad        uint64
}

// MFDecoder implements MediaDecoder using Windows Media Foundation IMFSourceReader in pure Go.
type MFDecoder struct {
	mu             sync.Mutex
	reader         uintptr
	hasVideo       bool
	sourceHasVideo bool
	width          int
	height         int
	stride         int
	videoFormat    string  // "abgr32" (RGBA bytes), "rgb32", "nv12", or "none"
	device         uintptr // ID3D11Device when decoding on the GPU
	manager        uintptr // IMFDXGIDeviceManager owning device
	hardware       bool
	mode           string // readerMode name that opened the reader
	duration       time.Duration
	fps            float64
	hasAudio       bool
	audioFormat    string // "float" or "pcm16"
	sampleRate     int
	channels       int
	audioCarry     []float32
	lastVideoPos   time.Duration
	lastRequest    time.Duration
	lastFrame      *image.RGBA
	closed         bool
}

// OpenMediaFile opens a local media file using Windows Media Foundation
// IMFSourceReader: files with a video stream keep the video-first split reader
// path (GPU video + independent audio reader), while pure audio files with no
// video stream (such as .m4a or .mp3) open through an audio-only reader.
func OpenMediaFile(path string) (MediaDecoder, error) {
	video, err := openMFReader(path, true)
	if err == nil {
		if !video.hasAudio {
			return video, nil
		}
		audio, err := openMFReader(path, false)
		if err != nil {
			_ = video.Close()
			return nil, err
		}
		return &splitDecoder{MFDecoder: video, audio: audio}, nil
	}

	audioOnly, audioErr := openMFReader(path, false)
	if audioErr == nil {
		if !audioOnly.sourceHasVideo && audioOnly.hasAudio {
			return audioOnly, nil
		}
		_ = audioOnly.Close()
	}
	return nil, err
}

// readerMode is one way to build a Source Reader, tried in order.
type readerMode struct {
	name     string
	hardware bool // D3D11 device manager + hardware transforms
	advanced bool // video processor MFT: color conversion and scaling
}

var videoReaderModes = []readerMode{
	{name: "gpu", hardware: true, advanced: true},
	{name: "software-scaled", advanced: true},
	{name: "software"},
}

// openMFReader opens path for its first video stream (forVideo) or its
// first audio stream; every other stream is deselected.
func openMFReader(path string, forVideo bool) (*MFDecoder, error) {
	_, _, _ = procCoInitializeEx.Call(0, coinitMultithreaded)
	r1, _, _ := procMFStartup.Call(mfVersionWin10, 0)
	if int32(r1) < 0 {
		return nil, fmt.Errorf("mf: MFStartup failed: 0x%08x", uint32(r1))
	}
	modes := videoReaderModes
	if !forVideo {
		modes = []readerMode{{name: "audio"}}
	}
	var lastErr error
	for _, mode := range modes {
		dec, err := openMFReaderMode(path, forVideo, mode)
		if err == nil {
			return dec, nil
		}
		lastErr = err
	}
	_, _, _ = procMFShutdown.Call()
	return nil, lastErr
}

func openMFReaderMode(path string, forVideo bool, mode readerMode) (*MFDecoder, error) {
	var device, manager uintptr
	if mode.hardware {
		if device, manager = newDXGIDeviceManager(); manager == 0 {
			return nil, fmt.Errorf("mf: no GPU video device")
		}
	}
	release := func() {
		comRelease(manager)
		comRelease(device)
	}

	var attrs uintptr
	hr, _, _ := procMFCreateAttributes.Call(uintptr(unsafe.Pointer(&attrs)), 3)
	if int32(hr) < 0 {
		release()
		return nil, fmt.Errorf("mf: MFCreateAttributes failed: 0x%08x", uint32(hr))
	}
	defer comRelease(attrs)
	switch {
	case mode.advanced:
		_ = comSetUINT32(attrs, &guidMFSourceReaderEnableAdvancedVideoProcessing, 1)
	case forVideo:
		// Legacy software converter (YUV to RGB32), the last resort.
		_ = comSetUINT32(attrs, &guidMFSourceReaderEnableVideoProcessing, 1)
	}
	if manager != 0 {
		_ = comSetUINT32(attrs, &guidMFReadWriteEnableHardwareTransforms, 1)
		if err := comSetUnknown(attrs, &guidMFSourceReaderD3DManager, manager); err != nil {
			release()
			return nil, err
		}
	}

	urlPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		release()
		return nil, err
	}
	var reader uintptr
	hr, _, _ = procMFCreateSourceReaderFromURL.Call(
		uintptr(unsafe.Pointer(urlPtr)),
		attrs,
		uintptr(unsafe.Pointer(&reader)),
	)
	if int32(hr) < 0 || reader == 0 {
		release()
		return nil, fmt.Errorf("mf: MFCreateSourceReaderFromURL(%s) failed: 0x%08x", path, uint32(hr))
	}

	dec := &MFDecoder{
		reader:      reader,
		hasVideo:    forVideo,
		videoFormat: "none",
		device:      device,
		manager:     manager,
		hardware:    manager != 0,
		mode:        mode.name,
		sampleRate:  48000,
		channels:    2,
	}
	if forVideo {
		dec.fps = 30.0
	}
	fail := func(err error) (*MFDecoder, error) {
		dec.releaseCOM()
		return nil, err
	}
	_ = vcall(reader, 4, mfSourceReaderAllStreams, 0) // SetStreamSelection(all, FALSE)
	if forVideo {
		_ = vcall(reader, 4, mfSourceReaderFirstVideoStream, 1)
		if err := dec.configureVideoStream(mode.advanced); err != nil {
			return fail(err)
		}
		dec.sourceHasVideo = true
		// Only report audio; the separate audio reader decodes it.
		var native uintptr
		if hr := vcall(reader, 5, mfSourceReaderFirstAudioStream, 0, uintptr(unsafe.Pointer(&native))); int32(hr) >= 0 && native != 0 {
			comRelease(native)
			dec.hasAudio = true
		}
	} else {
		var videoNative uintptr
		if hr := vcall(reader, 5, mfSourceReaderFirstVideoStream, 0, uintptr(unsafe.Pointer(&videoNative))); int32(hr) >= 0 && videoNative != 0 {
			comRelease(videoNative)
			dec.sourceHasVideo = true
		}
		_ = vcall(reader, 4, mfSourceReaderFirstAudioStream, 1)
		dec.configureAudioStream()
		if !dec.hasAudio {
			return fail(fmt.Errorf("mf: no decodable audio stream"))
		}
	}
	dec.queryDuration()
	return dec, nil
}

func (d *MFDecoder) configureVideoStream(advanced bool) error {
	nativeW, nativeH := 0, 0
	var native uintptr
	if hr := vcall(d.reader, 5, mfSourceReaderFirstVideoStream, 0, uintptr(unsafe.Pointer(&native))); int32(hr) >= 0 && native != 0 {
		if packed, err := comGetUINT64(native, &guidMFMTFrameSize); err == nil {
			nativeW, nativeH = int(packed>>32), int(packed&0xFFFFFFFF)
		}
		comRelease(native)
	}
	type output struct {
		format    string
		subtype   *windows.GUID
		w, h      int
		fullRange bool
	}
	var outputs []output
	if advanced {
		// The video processor converts straight to RGBA byte order and scales
		// to the size the window can show.
		w, h := decodeSize(nativeW, nativeH)
		outputs = append(outputs,
			output{"abgr32", &guidMFVideoFormatABGR32, w, h, true},
			output{"rgb32", &guidMFVideoFormatRGB32, w, h, true},
			output{"rgb32", &guidMFVideoFormatRGB32, w, h, false},
		)
	}
	outputs = append(outputs,
		output{"rgb32", &guidMFVideoFormatRGB32, 0, 0, false},
		output{"nv12", &guidMFVideoFormatNV12, 0, 0, false},
	)
	var err error
	for _, out := range outputs {
		if err = d.setVideoOutput(out.subtype, out.w, out.h, out.fullRange); err == nil {
			d.videoFormat = out.format
			break
		}
	}
	if err != nil {
		return fmt.Errorf("mf: failed to configure video output format: %w", err)
	}

	var curType uintptr
	hr := vcall(d.reader, 6, uintptr(mfSourceReaderFirstVideoStream), uintptr(unsafe.Pointer(&curType)))
	if int32(hr) < 0 || curType == 0 {
		return fmt.Errorf("mf: GetCurrentMediaType(video) failed: 0x%08x", uint32(hr))
	}
	defer comRelease(curType)

	if packedSize, err := comGetUINT64(curType, &guidMFMTFrameSize); err == nil && packedSize > 0 {
		d.width = int(packedSize >> 32)
		d.height = int(packedSize & 0xFFFFFFFF)
	}
	if d.width <= 0 {
		d.width = 320
	}
	if d.height <= 0 {
		d.height = 180
	}

	if strideU32, err := comGetUINT32(curType, &guidMFMTDefaultStride); err == nil && strideU32 != 0 {
		d.stride = int(int32(strideU32))
	} else if d.videoFormat != "nv12" {
		d.stride = d.width * 4
	} else {
		d.stride = d.width
	}

	if packedRate, err := comGetUINT64(curType, &guidMFMTFrameRate); err == nil && packedRate > 0 {
		num := float64(packedRate >> 32)
		den := float64(packedRate & 0xFFFFFFFF)
		if den > 0 && num > 0 {
			d.fps = num / den
		}
	}
	return nil
}

func (d *MFDecoder) configureAudioStream() {
	if err := d.setStreamSubtype(mfSourceReaderFirstAudioStream, &guidMFMediaTypeAudio, &guidMFAudioFormatFloat); err == nil {
		d.hasAudio = true
		d.audioFormat = "float"
	} else if err := d.setStreamSubtype(mfSourceReaderFirstAudioStream, &guidMFMediaTypeAudio, &guidMFAudioFormatPCM); err == nil {
		d.hasAudio = true
		d.audioFormat = "pcm16"
	} else {
		d.hasAudio = false
		return
	}

	var curType uintptr
	hr := vcall(d.reader, 6, uintptr(mfSourceReaderFirstAudioStream), uintptr(unsafe.Pointer(&curType)))
	if int32(hr) < 0 || curType == 0 {
		return
	}
	defer comRelease(curType)

	if ch, err := comGetUINT32(curType, &guidMFMTAudioNumChannels); err == nil && ch > 0 {
		d.channels = int(ch)
	}
	if sr, err := comGetUINT32(curType, &guidMFMTAudioSamplesPerSecond); err == nil && sr > 0 {
		d.sampleRate = int(sr)
	}
}

func (d *MFDecoder) queryDuration() {
	var pv propVariant
	hr := vcall(
		d.reader,
		12, // GetPresentationAttribute
		uintptr(mfSourceReaderMediaSource),
		uintptr(unsafe.Pointer(&guidMFPDuration)),
		uintptr(unsafe.Pointer(&pv)),
	)
	if int32(hr) >= 0 && (pv.vt == vtUI8 || pv.vt == vtI8) && pv.hVal > 0 {
		// MF_PD_DURATION is in 100-nanosecond units
		d.duration = time.Duration(pv.hVal * 100)
	}
	_, _, _ = procPropVariantClear.Call(uintptr(unsafe.Pointer(&pv)))
	if d.duration <= 0 {
		d.duration = time.Second
	}
}

func (d *MFDecoder) setStreamSubtype(streamIdx uint32, major, sub *windows.GUID) error {
	var mt uintptr
	hr, _, _ := procMFCreateMediaType.Call(uintptr(unsafe.Pointer(&mt)))
	if int32(hr) < 0 || mt == 0 {
		return fmt.Errorf("MFCreateMediaType failed: 0x%08x", uint32(hr))
	}
	defer comRelease(mt)

	if err := comSetGUID(mt, &guidMFMTMajorType, major); err != nil {
		return err
	}
	if err := comSetGUID(mt, &guidMFMTSubtype, sub); err != nil {
		return err
	}

	hr = vcall(d.reader, 7, uintptr(streamIdx), 0, mt)
	if int32(hr) < 0 {
		return fmt.Errorf("SetCurrentMediaType failed: 0x%08x", uint32(hr))
	}
	return nil
}

func (d *MFDecoder) BackendName() string { return "windows-mf" }
func (d *MFDecoder) HasVideo() bool      { return d.hasVideo }

// DecodePath names the reader mode and output format, such as "gpu/abgr32".
func (d *MFDecoder) DecodePath() string      { return d.mode + "/" + d.videoFormat }
func (d *MFDecoder) Width() int              { return d.width }
func (d *MFDecoder) Height() int             { return d.height }
func (d *MFDecoder) Duration() time.Duration { return d.duration }
func (d *MFDecoder) FPS() float64            { return d.fps }
func (d *MFDecoder) SampleRate() int {
	if !d.hasAudio {
		return 0
	}
	return d.sampleRate
}
func (d *MFDecoder) Channels() int { return d.channels }

// DecodeVideoFrameAt reads the next video frame (seeking first if pos jumped backward).
func (d *MFDecoder) DecodeVideoFrameAt(pos time.Duration, dst *image.RGBA) (*image.RGBA, time.Duration, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || d.reader == 0 {
		return nil, 0, io.EOF
	}
	if !d.hasVideo {
		return nil, pos, nil
	}

	if pos < d.lastRequest || pos-d.lastRequest > 2*time.Second {
		if err := d.seekLocked(pos); err != nil {
			return nil, pos, err
		}
	}
	d.lastRequest = pos
	if d.lastFrame != nil && d.lastVideoPos >= pos {
		return d.lastFrame, d.lastVideoPos, nil
	}

	for tries := 0; tries < 240; tries++ {
		var actualIdx uint32
		var flags uint32
		var tsHNS int64
		var sample uintptr

		hr := vcall(
			d.reader,
			9, // ReadSample
			uintptr(mfSourceReaderFirstVideoStream),
			0,
			uintptr(unsafe.Pointer(&actualIdx)),
			uintptr(unsafe.Pointer(&flags)),
			uintptr(unsafe.Pointer(&tsHNS)),
			uintptr(unsafe.Pointer(&sample)),
		)
		if int32(hr) < 0 {
			return d.fallbackFrameLocked(dst, pos), pos, fmt.Errorf("mf: ReadSample(video) failed: 0x%08x", uint32(hr))
		}
		if sample != 0 {
			pts := time.Duration(tsHNS * 100)
			if pts < pos {
				// A frame already replaced by a later one is never shown:
				// skip reading it back and converting it.
				comRelease(sample)
				d.lastVideoPos = pts
				continue
			}
			rgba := d.extractVideoSample(sample, dst)
			comRelease(sample)
			d.lastVideoPos = pts
			d.lastFrame = rgba
			return rgba, pts, nil
		}
		if (flags & mfSourceReaderFlagEndOfStream) != 0 {
			if d.lastFrame != nil {
				return d.lastFrame, d.lastVideoPos, io.EOF
			}
			return d.fallbackFrameLocked(dst, pos), pos, fmt.Errorf("mf: video reached EOS without sample (flags=0x%x)", flags)
		}
		if (flags & mfSourceReaderFlagError) != 0 {
			return d.fallbackFrameLocked(dst, pos), pos, fmt.Errorf("mf: video stream flag error (flags=0x%x)", flags)
		}
	}

	return d.fallbackFrameLocked(dst, pos), pos, fmt.Errorf("mf: video ReadSample yielded no sample after 64 tries")
}

func (d *MFDecoder) extractVideoSample(sample uintptr, dst *image.RGBA) *image.RGBA {
	var buf uintptr
	hr := vcall(sample, 41, uintptr(unsafe.Pointer(&buf))) // ConvertToContiguousBuffer
	if int32(hr) < 0 || buf == 0 {
		return d.fallbackFrameLocked(dst, 0)
	}
	defer comRelease(buf)

	// Decoders pad rows (a 1080-wide portrait frame arrives with 1088-pixel
	// rows), so MF_MT_DEFAULT_STRIDE is not the real pitch. Lock2D reports it.
	if rgba, ok := d.extract2DVideoBuffer(buf, dst); ok {
		return rgba
	}

	var dataPtr *byte
	var maxLen, curLen uint32
	hr = vcall(
		buf,
		3, // Lock
		uintptr(unsafe.Pointer(&dataPtr)),
		uintptr(unsafe.Pointer(&maxLen)),
		uintptr(unsafe.Pointer(&curLen)),
	)
	if int32(hr) < 0 || dataPtr == nil || curLen == 0 {
		return d.fallbackFrameLocked(dst, 0)
	}
	defer vcall(buf, 4) // Unlock

	raw := unsafe.Slice(dataPtr, int(curLen))
	stride := contiguousStride(d.videoFormat, d.width, d.height, d.stride, int(curLen))
	switch d.videoFormat {
	case "abgr32":
		return RGBA32Rows(raw, d.width, d.height, stride, dst)
	case "rgb32":
		return BGRA32ToRGBA(raw, d.width, d.height, stride, dst)
	}
	return NV12ToRGBA(raw, d.width, d.height, stride, dst)
}

func (d *MFDecoder) extract2DVideoBuffer(buf uintptr, dst *image.RGBA) (*image.RGBA, bool) {
	var buf2d uintptr
	if hr := vcall(buf, 0, uintptr(unsafe.Pointer(&iidIMF2DBuffer)), uintptr(unsafe.Pointer(&buf2d))); int32(hr) < 0 || buf2d == 0 {
		return nil, false
	}
	defer comRelease(buf2d)
	var scan0 *byte
	var pitch int32
	if hr := vcall(buf2d, 3, uintptr(unsafe.Pointer(&scan0)), uintptr(unsafe.Pointer(&pitch))); int32(hr) < 0 || scan0 == nil || pitch == 0 { // Lock2D
		return nil, false
	}
	defer vcall(buf2d, 4) // Unlock2D
	abs := int(pitch)
	if abs < 0 {
		abs = -abs
	}
	rows := d.height
	if d.videoFormat == "nv12" {
		rows = d.height * 3 / 2
	}
	start := unsafe.Pointer(scan0)
	if pitch < 0 { // bottom-up: the lowest address holds the last row
		start = unsafe.Add(start, int(pitch)*(d.height-1))
	}
	raw := unsafe.Slice((*byte)(start), abs*rows)
	if d.videoFormat != "nv12" {
		if abs < d.width*4 {
			return nil, false
		}
		if d.videoFormat == "abgr32" {
			return RGBA32Rows(raw, d.width, d.height, int(pitch), dst), true
		}
		return BGRA32ToRGBA(raw, d.width, d.height, int(pitch), dst), true
	}
	if pitch < 0 || abs < d.width {
		return nil, false
	}
	return NV12ToRGBA(raw, d.width, d.height, abs, dst), true
}

func (d *MFDecoder) fallbackFrameLocked(dst *image.RGBA, pos time.Duration) *image.RGBA {
	if d.lastFrame != nil {
		return d.lastFrame
	}
	if dst == nil || dst.Bounds().Dx() != d.width || dst.Bounds().Dy() != d.height {
		dst = image.NewRGBA(image.Rect(0, 0, d.width, d.height))
	}
	return dst
}

// ReadAudioSamples decodes PCM float32 audio samples from IMFSourceReader.
func (d *MFDecoder) ReadAudioSamples(out []float32) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || d.reader == 0 || !d.hasAudio {
		return 0, io.EOF
	}

	written := 0
	if len(d.audioCarry) > 0 {
		n := copy(out, d.audioCarry)
		d.audioCarry = d.audioCarry[n:]
		written += n
		if written == len(out) {
			return written, nil
		}
	}

	for written < len(out) {
		var actualIdx uint32
		var flags uint32
		var tsHNS int64
		var sample uintptr

		hr := vcall(
			d.reader,
			9, // ReadSample
			uintptr(mfSourceReaderFirstAudioStream),
			0,
			uintptr(unsafe.Pointer(&actualIdx)),
			uintptr(unsafe.Pointer(&flags)),
			uintptr(unsafe.Pointer(&tsHNS)),
			uintptr(unsafe.Pointer(&sample)),
		)
		if int32(hr) < 0 {
			if written > 0 {
				return written, nil
			}
			return 0, io.EOF
		}

		if sample != 0 {
			floats := d.extractAudioFloats(sample)
			comRelease(sample)
			if len(floats) > 0 {
				n := copy(out[written:], floats)
				written += n
				if n < len(floats) {
					d.audioCarry = append(d.audioCarry, floats[n:]...)
				}
			}
			continue
		}

		if (flags & (mfSourceReaderFlagEndOfStream | mfSourceReaderFlagError)) != 0 {
			if written > 0 {
				return written, nil
			}
			return 0, io.EOF
		}
	}
	return written, nil
}

func (d *MFDecoder) extractAudioFloats(sample uintptr) []float32 {
	var buf uintptr
	hr := vcall(sample, 41, uintptr(unsafe.Pointer(&buf))) // ConvertToContiguousBuffer
	if int32(hr) < 0 || buf == 0 {
		return nil
	}
	defer comRelease(buf)

	var dataPtr *byte
	var maxLen, curLen uint32
	hr = vcall(
		buf,
		3, // Lock
		uintptr(unsafe.Pointer(&dataPtr)),
		uintptr(unsafe.Pointer(&maxLen)),
		uintptr(unsafe.Pointer(&curLen)),
	)
	if int32(hr) < 0 || dataPtr == nil || curLen == 0 {
		return nil
	}
	defer vcall(buf, 4) // Unlock

	raw := unsafe.Slice(dataPtr, int(curLen))
	if d.audioFormat == "float" {
		count := len(raw) / 4
		if count <= 0 {
			return nil
		}
		src := unsafe.Slice((*float32)(unsafe.Pointer(dataPtr)), count)
		copied := make([]float32, count)
		copy(copied, src)
		return copied
	}

	// 16-bit PCM fallback conversion to float32 [-1.0, 1.0]
	count := len(raw) / 2
	if count <= 0 {
		return nil
	}
	src16 := unsafe.Slice((*int16)(unsafe.Pointer(dataPtr)), count)
	out := make([]float32, count)
	for i, s := range src16 {
		out[i] = float32(s) / 32768.0
	}
	return out
}

// setVideoOutput requests subtype, at w×h when both are set, in full-range
// RGB when fullRange is set.
func (d *MFDecoder) setVideoOutput(subtype *windows.GUID, w, h int, fullRange bool) error {
	var mt uintptr
	hr, _, _ := procMFCreateMediaType.Call(uintptr(unsafe.Pointer(&mt)))
	if int32(hr) < 0 || mt == 0 {
		return fmt.Errorf("MFCreateMediaType failed: 0x%08x", uint32(hr))
	}
	defer comRelease(mt)
	if err := comSetGUID(mt, &guidMFMTMajorType, &guidMFMediaTypeVideo); err != nil {
		return err
	}
	if err := comSetGUID(mt, &guidMFMTSubtype, subtype); err != nil {
		return err
	}
	if fullRange {
		_ = comSetUINT32(mt, &guidMFMTVideoNominalRange, mfNominalRange0To255)
	}
	if w > 0 && h > 0 {
		if hr := vcall(mt, 22, uintptr(unsafe.Pointer(&guidMFMTFrameSize)), uintptr(uint64(w)<<32|uint64(h))); int32(hr) < 0 { // SetUINT64
			return fmt.Errorf("SetUINT64 failed: 0x%08x", uint32(hr))
		}
	}
	if hr := vcall(d.reader, 7, uintptr(mfSourceReaderFirstVideoStream), 0, mt); int32(hr) < 0 {
		return fmt.Errorf("SetCurrentMediaType failed: 0x%08x", uint32(hr))
	}
	return nil
}

func (d *MFDecoder) Seek(pos time.Duration) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.seekLocked(pos)
}

func (d *MFDecoder) seekLocked(pos time.Duration) error {
	if d.closed || d.reader == 0 {
		return io.EOF
	}
	if pos < 0 {
		pos = 0
	}
	pv := propVariant{
		vt:   vtI8,
		hVal: int64(pos / 100), // 100ns units
	}
	hr := vcall(
		d.reader,
		8, // SetCurrentPosition
		uintptr(unsafe.Pointer(&guidNull)),
		uintptr(unsafe.Pointer(&pv)),
	)
	if int32(hr) < 0 {
		return fmt.Errorf("mf: SetCurrentPosition failed: 0x%08x", uint32(hr))
	}
	d.audioCarry = d.audioCarry[:0]
	d.lastVideoPos = pos
	d.lastRequest = pos
	d.lastFrame = nil
	return nil
}

func (d *MFDecoder) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true
	d.releaseCOM()
	d.audioCarry = nil
	d.lastFrame = nil
	_, _, _ = procMFShutdown.Call()
	return nil
}

// releaseCOM releases the reader before the device it decodes on.
func (d *MFDecoder) releaseCOM() {
	comRelease(d.reader)
	comRelease(d.manager)
	comRelease(d.device)
	d.reader, d.manager, d.device = 0, 0, 0
}

func vcall(obj uintptr, index int, args ...uintptr) uintptr {
	if obj == 0 {
		return 0x80004003 // E_POINTER
	}
	objPtr := *(*unsafe.Pointer)(unsafe.Pointer(&obj))
	vtbl := *(**[64]uintptr)(objPtr)
	fn := vtbl[index]
	all := make([]uintptr, 0, len(args)+1)
	all = append(all, obj)
	all = append(all, args...)
	r1, _, _ := syscall.SyscallN(fn, all...)
	return r1
}

func comRelease(obj uintptr) {
	if obj != 0 {
		_ = vcall(obj, 2)
	}
}

func comGetUINT32(obj uintptr, key *windows.GUID) (uint32, error) {
	var val uint32
	hr := vcall(obj, 7, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(&val)))
	if int32(hr) < 0 {
		return 0, fmt.Errorf("GetUINT32 failed: 0x%08x", uint32(hr))
	}
	return val, nil
}

func comGetUINT64(obj uintptr, key *windows.GUID) (uint64, error) {
	var val uint64
	hr := vcall(obj, 8, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(&val)))
	if int32(hr) < 0 {
		return 0, fmt.Errorf("GetUINT64 failed: 0x%08x", uint32(hr))
	}
	return val, nil
}

func comSetUINT32(obj uintptr, key *windows.GUID, val uint32) error {
	hr := vcall(obj, 21, uintptr(unsafe.Pointer(key)), uintptr(val))
	if int32(hr) < 0 {
		return fmt.Errorf("SetUINT32 failed: 0x%08x", uint32(hr))
	}
	return nil
}

func comSetGUID(obj uintptr, key *windows.GUID, val *windows.GUID) error {
	hr := vcall(obj, 24, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(val)))
	if int32(hr) < 0 {
		return fmt.Errorf("SetGUID failed: 0x%08x", uint32(hr))
	}
	return nil
}

// Independent SourceReaders keep video catch-up and audio buffering from
// seeking or consuming each other's stream positions.
type splitDecoder struct {
	*MFDecoder
	audio *MFDecoder
}

func (d *splitDecoder) ReadAudioSamples(buf []float32) (int, error) {
	return d.audio.ReadAudioSamples(buf)
}
func (d *splitDecoder) Seek(pos time.Duration) error {
	if err := d.MFDecoder.Seek(pos); err != nil {
		return err
	}
	return d.audio.Seek(pos)
}
func (d *splitDecoder) Close() error { _ = d.audio.Close(); return d.MFDecoder.Close() }

// SeekAudio moves only the audio reader (audioSeeker).
func (d *splitDecoder) SeekAudio(pos time.Duration) error { return d.audio.Seek(pos) }

// PreviewFrameAt seeks the video reader to pos and returns the first frame
// it decodes there: the key frame at or before pos. Dragging the progress
// bar shows these, which cost one frame each instead of decoding on from
// the key frame to pos.
func (d *MFDecoder) PreviewFrameAt(pos time.Duration, dst *image.RGBA) (*image.RGBA, time.Duration, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || d.reader == 0 {
		return nil, 0, io.EOF
	}
	if !d.hasVideo {
		return nil, pos, nil
	}
	if err := d.seekLocked(pos); err != nil {
		return nil, pos, err
	}
	for tries := 0; tries < 16; tries++ {
		var actualIdx, flags uint32
		var tsHNS int64
		var sample uintptr
		hr := vcall(d.reader, 9, // ReadSample
			uintptr(mfSourceReaderFirstVideoStream), 0,
			uintptr(unsafe.Pointer(&actualIdx)), uintptr(unsafe.Pointer(&flags)),
			uintptr(unsafe.Pointer(&tsHNS)), uintptr(unsafe.Pointer(&sample)))
		if int32(hr) < 0 {
			return d.fallbackFrameLocked(dst, pos), pos, fmt.Errorf("mf: ReadSample(video) failed: 0x%08x", uint32(hr))
		}
		if sample != 0 {
			pts := time.Duration(tsHNS * 100)
			rgba := d.extractVideoSample(sample, dst)
			comRelease(sample)
			// The reader stands after this frame: a later decode at pos
			// goes on from here without seeking again.
			d.lastVideoPos, d.lastFrame = pts, rgba
			return rgba, pts, nil
		}
		if flags&(mfSourceReaderFlagEndOfStream|mfSourceReaderFlagError) != 0 {
			break
		}
	}
	return d.fallbackFrameLocked(dst, pos), pos, fmt.Errorf("mf: no video frame near %v", pos)
}
