package player

import (
	"image"
	"image/color"
	"io"
	"math"
	"sync"
	"time"
)

// MediaDecoder abstracts video frame + PCM audio decoding from a local media file.
type MediaDecoder interface {
	BackendName() string
	Width() int
	Height() int
	Duration() time.Duration
	FPS() float64
	SampleRate() int
	Channels() int
	DecodeVideoFrameAt(pos time.Duration, dst *image.RGBA) (*image.RGBA, time.Duration, error)
	ReadAudioSamples(buf []float32) (int, error)
	Seek(pos time.Duration) error
	Close() error
}

// SynthDecoder is a deterministic in-memory MediaDecoder for unit tests, uishots, and fallback.
type SynthDecoder struct {
	mu          sync.Mutex
	hasVideo    bool
	width       int
	height      int
	duration    time.Duration
	fps         float64
	sampleRate  int
	channels    int
	audioPos    int64
	totalFrames int64
	closed      bool
}

// NewSynthDecoder creates a deterministic video + audio decoder.
func NewSynthDecoder(width, height int, duration time.Duration, fps float64) *SynthDecoder {
	if width <= 0 {
		width = 320
	}
	if height <= 0 {
		height = 180
	}
	if duration <= 0 {
		duration = 10 * time.Second
	}
	if fps <= 0 {
		fps = 30
	}
	sr := 48000
	ch := 2
	totalFrames := int64(duration.Seconds() * float64(sr))
	return &SynthDecoder{
		hasVideo:    true,
		width:       width,
		height:      height,
		duration:    duration,
		fps:         fps,
		sampleRate:  sr,
		channels:    ch,
		totalFrames: totalFrames,
	}
}

// NewSynthAudioDecoder creates a deterministic audio-only decoder with no video stream.
func NewSynthAudioDecoder(duration time.Duration, sampleRate, channels int) *SynthDecoder {
	if duration <= 0 {
		duration = 10 * time.Second
	}
	if sampleRate <= 0 {
		sampleRate = 48000
	}
	if channels <= 0 {
		channels = 2
	}
	totalFrames := int64(duration.Seconds() * float64(sampleRate))
	return &SynthDecoder{
		hasVideo:    false,
		width:       0,
		height:      0,
		duration:    duration,
		fps:         0,
		sampleRate:  sampleRate,
		channels:    channels,
		totalFrames: totalFrames,
	}
}

func (d *SynthDecoder) BackendName() string     { return "synth" }
func (d *SynthDecoder) HasVideo() bool          { return d.hasVideo }
func (d *SynthDecoder) Width() int              { return d.width }
func (d *SynthDecoder) Height() int             { return d.height }
func (d *SynthDecoder) Duration() time.Duration { return d.duration }
func (d *SynthDecoder) FPS() float64            { return d.fps }
func (d *SynthDecoder) SampleRate() int         { return d.sampleRate }
func (d *SynthDecoder) Channels() int           { return d.channels }

func (d *SynthDecoder) DecodeVideoFrameAt(pos time.Duration, dst *image.RGBA) (*image.RGBA, time.Duration, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, 0, io.EOF
	}
	if !d.hasVideo {
		return nil, pos, nil
	}
	if pos < 0 {
		pos = 0
	}
	if pos > d.duration {
		pos = d.duration
	}

	if dst == nil || dst.Bounds().Dx() != d.width || dst.Bounds().Dy() != d.height {
		dst = image.NewRGBA(image.Rect(0, 0, d.width, d.height))
	}

	frameIdx := int(pos.Seconds() * d.fps)
	barX := (frameIdx * 6) % d.width

	for y := 0; y < d.height; y++ {
		rBase := uint8(24 + (y*40)/d.height)
		gBase := uint8(34 + (y*50)/d.height)
		bBase := uint8(58 + (y*80)/d.height)
		for x := 0; x < d.width; x++ {
			c := color.RGBA{R: rBase, G: gBase, B: bBase, A: 255}
			if x >= barX && x < barX+16 {
				c = color.RGBA{R: 56, G: 189, B: 248, A: 255}
			}
			dst.SetRGBA(x, y, c)
		}
	}

	pts := time.Duration(float64(frameIdx) / d.fps * float64(time.Second))
	return dst, pts, nil
}

func (d *SynthDecoder) ReadAudioSamples(buf []float32) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || d.audioPos >= d.totalFrames {
		return 0, io.EOF
	}
	framesToRead := len(buf) / d.channels
	rem := int(d.totalFrames - d.audioPos)
	if framesToRead > rem {
		framesToRead = rem
	}
	for i := 0; i < framesToRead; i++ {
		t := float64(d.audioPos+int64(i)) / float64(d.sampleRate)
		val := float32(0.05 * math.Sin(2*math.Pi*440.0*t))
		for c := 0; c < d.channels; c++ {
			buf[i*d.channels+c] = val
		}
	}
	d.audioPos += int64(framesToRead)
	return framesToRead * d.channels, nil
}

func (d *SynthDecoder) Seek(pos time.Duration) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if pos < 0 {
		pos = 0
	}
	if pos > d.duration {
		pos = d.duration
	}
	d.audioPos = int64(pos.Seconds() * float64(d.sampleRate))
	return nil
}

func (d *SynthDecoder) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closed = true
	return nil
}

// SynthLiveDecoder is a deterministic in-memory LiveDecoder for quiet tests, non-Windows builds, and UI prototypes.
type SynthLiveDecoder struct {
	mu          sync.Mutex
	cond        *sync.Cond
	width       int
	height      int
	fps         float64
	sampleRate  int
	channels    int
	audioPos    int64
	startedAt   time.Time
	manualAvail bool
	avail       time.Duration
	frozenAvail time.Duration
	ended       bool
	closed      bool
	isActive    func() bool
}

// NewSynthLiveDecoder creates a synthetic live-preview decoder whose available media grows while isActive reports true.
func NewSynthLiveDecoder(width, height int, fps float64, isActive func() bool) *SynthLiveDecoder {
	if width <= 0 {
		width = 320
	}
	if height <= 0 {
		height = 180
	}
	if fps <= 0 {
		fps = 30
	}
	d := &SynthLiveDecoder{
		width:      width,
		height:     height,
		fps:        fps,
		sampleRate: 48000,
		channels:   2,
		startedAt:  time.Now(),
		isActive:   isActive,
	}
	d.cond = sync.NewCond(&d.mu)
	return d
}

// SetAvailableDuration pins the synthetic stream's currently buffered duration for deterministic growth tests.
func (d *SynthLiveDecoder) SetAvailableDuration(avail time.Duration) {
	d.mu.Lock()
	d.manualAvail = true
	if avail > d.avail {
		d.avail = avail
	}
	d.cond.Broadcast()
	d.mu.Unlock()
}

func (d *SynthLiveDecoder) BackendName() string     { return "synth-live" }
func (d *SynthLiveDecoder) HasVideo() bool          { return true }
func (d *SynthLiveDecoder) Width() int              { return d.width }
func (d *SynthLiveDecoder) Height() int             { return d.height }
func (d *SynthLiveDecoder) Duration() time.Duration { return 0 }
func (d *SynthLiveDecoder) FPS() float64            { return d.fps }
func (d *SynthLiveDecoder) SampleRate() int         { return d.sampleRate }
func (d *SynthLiveDecoder) Channels() int           { return d.channels }
func (d *SynthLiveDecoder) IsLive() bool            { return true }

func (d *SynthLiveDecoder) refreshStateLocked() (avail time.Duration, ended bool) {
	if d.closed {
		return d.avail, true
	}
	active := true
	if d.isActive != nil {
		active = d.isActive()
	}
	if d.manualAvail {
		if !active {
			d.ended = true
		}
		return d.avail, d.ended
	}
	if !d.ended {
		cur := time.Since(d.startedAt) + 2*time.Second
		if cur > d.frozenAvail {
			d.frozenAvail = cur
		}
		if !active {
			d.ended = true
		}
	}
	return d.frozenAvail, d.ended
}

func (d *SynthLiveDecoder) LiveEnded() bool {
	d.mu.Lock()
	_, ended := d.refreshStateLocked()
	d.mu.Unlock()
	return ended
}

func (d *SynthLiveDecoder) AvailableDuration() time.Duration {
	d.mu.Lock()
	avail, _ := d.refreshStateLocked()
	d.mu.Unlock()
	return avail
}

func (d *SynthLiveDecoder) DecodeVideoFrameAt(pos time.Duration, dst *image.RGBA) (*image.RGBA, time.Duration, error) {
	if pos < 0 {
		pos = 0
	}
	d.mu.Lock()
	for {
		if d.closed {
			d.mu.Unlock()
			return nil, 0, io.EOF
		}
		avail, ended := d.refreshStateLocked()
		if pos <= avail || ended {
			if ended && pos > avail {
				pos = avail
			}
			break
		}
		if !d.manualAvail {
			break
		}
		d.cond.Wait()
	}
	w, h, fps := d.width, d.height, d.fps
	d.mu.Unlock()

	return renderSynthLiveFrame(pos, w, h, fps, dst)
}

func renderSynthLiveFrame(pos time.Duration, w, h int, fps float64, dst *image.RGBA) (*image.RGBA, time.Duration, error) {
	if dst == nil || dst.Bounds().Dx() != w || dst.Bounds().Dy() != h {
		dst = image.NewRGBA(image.Rect(0, 0, w, h))
	}
	frameIdx := int(pos.Seconds() * fps)
	barX := (frameIdx * 8) % w
	accentG := uint8(120 + (frameIdx*7)%120)

	for y := 0; y < h; y++ {
		rBase := uint8(20 + (y*44)/h)
		gBase := uint8(30 + (y*52)/h)
		bBase := uint8(54 + (y*84)/h)
		for x := 0; x < w; x++ {
			c := color.RGBA{R: rBase, G: gBase, B: bBase, A: 255}
			if x >= barX && x < barX+18 {
				c = color.RGBA{R: 56, G: accentG, B: 248, A: 255}
			}
			dst.SetRGBA(x, y, c)
		}
	}
	pts := time.Duration(float64(frameIdx) / fps * float64(time.Second))
	return dst, pts, nil
}

func (d *SynthLiveDecoder) ReadAudioSamples(buf []float32) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	for {
		if d.closed {
			return 0, io.EOF
		}
		avail, ended := d.refreshStateLocked()
		availFrames := int64(avail.Seconds() * float64(d.sampleRate))
		if d.audioPos < availFrames {
			framesToRead := len(buf) / d.channels
			rem := int(availFrames - d.audioPos)
			if framesToRead > rem {
				framesToRead = rem
			}
			if framesToRead <= 0 {
				return 0, nil
			}
			for i := 0; i < framesToRead; i++ {
				t := float64(d.audioPos+int64(i)) / float64(d.sampleRate)
				val := float32(0.05 * math.Sin(2*math.Pi*440.0*t))
				for c := 0; c < d.channels; c++ {
					buf[i*d.channels+c] = val
				}
			}
			d.audioPos += int64(framesToRead)
			return framesToRead * d.channels, nil
		}
		if ended {
			return 0, io.EOF
		}
		if !d.manualAvail {
			return 0, nil
		}
		d.cond.Wait()
	}
}

func (d *SynthLiveDecoder) Seek(pos time.Duration) error {
	return nil
}

func (d *SynthLiveDecoder) abort() {
	d.mu.Lock()
	d.closed = true
	d.cond.Broadcast()
	d.mu.Unlock()
}

func (d *SynthLiveDecoder) Close() error {
	d.abort()
	return nil
}

// NV12ToRGBA converts an NV12 YUV 4:2:0 frame buffer into *image.RGBA (BT.709 integer math).
func NV12ToRGBA(nv12 []byte, width, height, stride int, dst *image.RGBA) *image.RGBA {
	if stride < width {
		stride = width
	}
	if dst == nil || dst.Bounds().Dx() != width || dst.Bounds().Dy() != height {
		dst = image.NewRGBA(image.Rect(0, 0, width, height))
	}
	yPlaneSize := stride * height
	if len(nv12) < yPlaneSize+(stride*height)/2 {
		return dst
	}
	uvPlane := nv12[yPlaneSize:]
	pix := dst.Pix

	for y := 0; y < height; y++ {
		yRow := nv12[y*stride:]
		uvRow := uvPlane[(y/2)*stride:]
		dstRow := pix[y*dst.Stride:]
		for x := 0; x < width; x++ {
			yv := int(yRow[x]) - 16
			if yv < 0 {
				yv = 0
			}
			uvIdx := (x & ^1)
			u := int(uvRow[uvIdx]) - 128
			v := int(uvRow[uvIdx+1]) - 128

			c := 298 * yv
			r := (c + 459*v + 128) >> 8
			g := (c - 55*u - 136*v + 128) >> 8
			b := (c + 541*u + 128) >> 8

			di := x * 4
			dstRow[di+0] = clampByte(r)
			dstRow[di+1] = clampByte(g)
			dstRow[di+2] = clampByte(b)
			dstRow[di+3] = 255
		}
	}
	return dst
}

// BGRA32ToRGBA converts a Windows MFVideoFormat_RGB32 (BGRA in memory) buffer into *image.RGBA.
func BGRA32ToRGBA(bgra []byte, width, height, stride int, dst *image.RGBA) *image.RGBA {
	if stride == 0 {
		stride = width * 4
	}
	if dst == nil || dst.Bounds().Dx() != width || dst.Bounds().Dy() != height {
		dst = image.NewRGBA(image.Rect(0, 0, width, height))
	}
	absStride := stride
	if absStride < 0 {
		absStride = -absStride
	}
	if len(bgra) < absStride*height {
		return dst
	}
	pix := dst.Pix
	for y := 0; y < height; y++ {
		srcY := y
		if stride < 0 {
			srcY = height - 1 - y
		}
		sRow := bgra[srcY*absStride:]
		dRow := pix[y*dst.Stride:]
		for x := 0; x < width; x++ {
			si := x * 4
			di := x * 4
			dRow[di+0] = sRow[si+2] // R
			dRow[di+1] = sRow[si+1] // G
			dRow[di+2] = sRow[si+0] // B
			dRow[di+3] = 255
		}
	}
	return dst
}

func clampByte(v int) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// RGBA32Rows copies frames whose bytes are already R, G, B, A (Media
// Foundation's ABGR32) row by row, honouring padded or bottom-up rows, and
// makes them opaque: a video frame never shows what is behind it.
func RGBA32Rows(src []byte, width, height, stride int, dst *image.RGBA) *image.RGBA {
	if stride == 0 {
		stride = width * 4
	}
	if dst == nil || dst.Bounds().Dx() != width || dst.Bounds().Dy() != height {
		dst = image.NewRGBA(image.Rect(0, 0, width, height))
	}
	absStride := stride
	if absStride < 0 {
		absStride = -absStride
	}
	if absStride < width*4 || len(src) < absStride*(height-1)+width*4 {
		return dst
	}
	for y := 0; y < height; y++ {
		srcY := y
		if stride < 0 {
			srcY = height - 1 - y
		}
		row := dst.Pix[y*dst.Stride : y*dst.Stride+width*4]
		copy(row, src[srcY*absStride:srcY*absStride+width*4])
		for x := 3; x < len(row); x += 4 {
			row[x] = 255
		}
	}
	return dst
}

// contiguousStride derives the real row pitch from a contiguous buffer when
// the media type's stride ignores decoder row padding (1080-wide portrait
// H.264 decodes into 1088-pixel rows). It keeps the type's stride otherwise.
func contiguousStride(format string, width, height, typeStride, length int) int {
	rows, minRow := height, width*4
	if format == "nv12" {
		rows, minRow = height*3/2, width
	}
	if rows <= 0 || length%rows != 0 {
		return typeStride
	}
	abs := typeStride
	if abs < 0 {
		abs = -abs
	}
	if s := length / rows; s >= minRow && s != abs {
		if typeStride < 0 {
			return -s
		}
		return s
	}
	return typeStride
}
