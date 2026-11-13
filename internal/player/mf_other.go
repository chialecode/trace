//go:build !windows

package player

import (
	"context"
	"image"
	"io"
	"math"
	"os"
	"sync"
	"time"
)

// OpenMediaFile opens pure audio formats registered with mygo/audio on
// non-Windows builds and reports ErrMissingCodec for unsupported containers.
func OpenMediaFile(path string) (MediaDecoder, error) {
	if dec, err := openPureAudioFile(path); err == nil {
		return dec, nil
	}
	return nil, ErrMissingCodec
}

func openSharedReadFile(path string) (*os.File, error) {
	return os.Open(path)
}

type liveSourceSynthDecoder struct {
	src      *liveFMP4Source
	mu       sync.Mutex
	cond     *sync.Cond
	audioPos int64
	closed   bool
}

func openLivePreviewPlatform(ctx context.Context, path string, opts LivePreviewOptions) (MediaDecoder, error) {
	src, err := openLiveFMP4Source(ctx, path, opts)
	if err != nil {
		return nil, err
	}
	if !src.hasVideo {
		_ = src.Close()
		return nil, ErrLivePreviewUnsupported
	}
	d := &liveSourceSynthDecoder{src: src}
	d.cond = sync.NewCond(&d.mu)
	src.subscribe(func(committedEnd int64, ended bool, closed bool) {
		d.mu.Lock()
		if closed {
			d.closed = true
		}
		d.cond.Broadcast()
		d.mu.Unlock()
	})
	return d, nil
}

func (d *liveSourceSynthDecoder) BackendName() string     { return "synth-live" }
func (d *liveSourceSynthDecoder) HasVideo() bool          { return d.src.hasVideo }
func (d *liveSourceSynthDecoder) Width() int              { return d.src.width }
func (d *liveSourceSynthDecoder) Height() int             { return d.src.height }
func (d *liveSourceSynthDecoder) Duration() time.Duration { return 0 }
func (d *liveSourceSynthDecoder) FPS() float64            { return d.src.fps }
func (d *liveSourceSynthDecoder) SampleRate() int {
	if !d.src.hasAudio {
		return 0
	}
	return d.src.sampleRate
}
func (d *liveSourceSynthDecoder) Channels() int { return d.src.channels }
func (d *liveSourceSynthDecoder) IsLive() bool  { return true }
func (d *liveSourceSynthDecoder) LiveEnded() bool {
	return d.src.LiveEnded()
}
func (d *liveSourceSynthDecoder) AvailableDuration() time.Duration {
	return d.src.AvailableDuration()
}

func (d *liveSourceSynthDecoder) DecodeVideoFrameAt(pos time.Duration, dst *image.RGBA) (*image.RGBA, time.Duration, error) {
	if pos < 0 {
		pos = 0
	}
	d.mu.Lock()
	for {
		if d.closed {
			d.mu.Unlock()
			return nil, 0, io.EOF
		}
		avail := d.src.AvailableDuration()
		ended := d.src.LiveEnded()
		if pos <= avail || ended {
			if ended && pos > avail {
				pos = avail
			}
			break
		}
		d.cond.Wait()
	}
	d.mu.Unlock()
	return renderSynthLiveFrame(pos, d.src.width, d.src.height, d.src.fps, dst)
}

func (d *liveSourceSynthDecoder) ReadAudioSamples(buf []float32) (int, error) {
	if len(buf) == 0 || !d.src.hasAudio {
		return 0, io.EOF
	}
	sr := d.src.sampleRate
	ch := d.src.channels
	if sr <= 0 {
		sr = 48000
	}
	if ch <= 0 {
		ch = 2
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	for {
		if d.closed {
			return 0, io.EOF
		}
		avail := d.src.AvailableDuration()
		ended := d.src.LiveEnded()
		availFrames := int64(avail.Seconds() * float64(sr))
		if d.audioPos < availFrames {
			framesToRead := len(buf) / ch
			rem := int(availFrames - d.audioPos)
			if framesToRead > rem {
				framesToRead = rem
			}
			if framesToRead <= 0 {
				return 0, nil
			}
			for i := 0; i < framesToRead; i++ {
				t := float64(d.audioPos+int64(i)) / float64(sr)
				val := float32(0.05 * math.Sin(2*math.Pi*440.0*t))
				for c := 0; c < ch; c++ {
					buf[i*ch+c] = val
				}
			}
			d.audioPos += int64(framesToRead)
			return framesToRead * ch, nil
		}
		if ended {
			return 0, io.EOF
		}
		d.cond.Wait()
	}
}

func (d *liveSourceSynthDecoder) Seek(pos time.Duration) error {
	return nil
}

func (d *liveSourceSynthDecoder) abort() {
	d.mu.Lock()
	d.closed = true
	d.cond.Broadcast()
	d.mu.Unlock()
	if d.src != nil {
		d.src.abort()
	}
}

func (d *liveSourceSynthDecoder) Close() error {
	d.abort()
	if d.src != nil {
		return d.src.Close()
	}
	return nil
}
