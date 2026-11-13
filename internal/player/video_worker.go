package player

import (
	"image"
	"sync"
	"time"
)

// videoWorker decodes video frames off the UI thread. Decoding a
// frame costs milliseconds even on the GPU and far more in software; done in
// the view it froze the whole window and fell ever further behind.
//
// Ownership of frame buffers: the worker decodes into a buffer from free,
// hands it over as pending, and the UI thread shows it. MyGo's Bitmap.Update
// keeps the shown buffer until the next Update, so a buffer returns to free
// only once a newer frame replaces it on screen.
type videoWorker struct {
	dec  MediaDecoder
	stop chan struct{}
	wake chan struct{}

	mu         sync.Mutex
	pending    *image.RGBA
	pendingGen uint64
	shown      *image.RGBA
	free       []*image.RGBA
}

const maxFreeFrames = 3

func newVideoWorker(dec MediaDecoder) *videoWorker {
	return &videoWorker{dec: dec, stop: make(chan struct{}), wake: make(chan struct{}, 1)}
}

func (w *videoWorker) signal() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *videoWorker) takeFree() *image.RGBA {
	w.mu.Lock()
	defer w.mu.Unlock()
	if n := len(w.free); n > 0 {
		img := w.free[n-1]
		w.free = w.free[:n-1]
		return img
	}
	return nil
}

func (w *videoWorker) putFreeLocked(img *image.RGBA) {
	if img == nil || img == w.shown || img == w.pending || len(w.free) >= maxFreeFrames {
		return
	}
	for _, f := range w.free {
		if f == img {
			return
		}
	}
	w.free = append(w.free, img)
}

func (w *videoWorker) putFree(img *image.RGBA) {
	w.mu.Lock()
	w.putFreeLocked(img)
	w.mu.Unlock()
}

// publish offers a freshly decoded frame. A buffer that is on screen, already
// pending or pooled (a decoder fallback returning an old frame) is not new.
func (w *videoWorker) publish(img *image.RGBA, gen uint64) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if img == w.shown || img == w.pending {
		return false
	}
	for _, f := range w.free {
		if f == img {
			return false
		}
	}
	old := w.pending
	w.pending, w.pendingGen = img, gen
	w.putFreeLocked(old)
	return true
}

// take returns the newest pending frame for the UI thread.
func (w *videoWorker) take() (*image.RGBA, uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	img, gen := w.pending, w.pendingGen
	w.pending = nil
	return img, gen
}

// showing records img as the buffer on screen and recycles the previous one.
func (w *videoWorker) showing(img *image.RGBA) {
	w.mu.Lock()
	defer w.mu.Unlock()
	old := w.shown
	w.shown = img
	if old != img {
		w.putFreeLocked(old)
	}
}

// runVideoWorker decodes the frame due at the playback clock, then sleeps
// until that frame's time; paused, it decodes only after a seek.
func (c *Controller) runVideoWorker(w *videoWorker, startGen uint64) {
	lastGen := startGen
	lastPTS := time.Duration(-1)
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		c.mu.Lock()
		if c.worker != w {
			c.mu.Unlock()
			return
		}
		playing, gen, pos, scrubbing := c.playing, c.seekGen, c.position, c.scrubbing
		holding := c.holdingLocked()
		holdOver := holding && time.Now().After(c.holdUntil)
		if playing && !holding {
			pos = c.sink.Position()
		}
		notify := c.onFrame
		c.mu.Unlock()

		if !playing && gen == lastGen {
			select {
			case <-w.stop:
				return
			case <-w.wake:
			}
			continue
		}

		buf := w.takeFree()
		var img *image.RGBA
		var pts time.Duration
		var err error
		if p, ok := w.dec.(keyFramePreviewer); ok && scrubbing {
			img, pts, err = p.PreviewFrameAt(pos, buf)
		} else {
			img, pts, err = w.dec.DecodeVideoFrameAt(pos, buf)
		}
		published := false
		if err == nil && img != nil && (pts != lastPTS || gen != lastGen) {
			published = w.publish(img, gen)
			if published {
				lastPTS = pts
			}
		}
		if !published || img != buf {
			w.putFree(buf)
		}
		lastGen = gen
		if holding && (published || holdOver) {
			c.releaseHold(gen)
		}
		if published && !playing && notify != nil {
			notify() // a paused seek: nothing else redraws the window
		}
		if !playing {
			continue
		}

		// Sleep until the decoded frame is due; then the next one is needed.
		wait := pts - pos
		if err != nil || !published {
			wait = 4 * time.Millisecond
		}
		wait = min(max(wait, time.Millisecond), 100*time.Millisecond)
		timer.Reset(wait)
		select {
		case <-w.stop:
			return
		case <-w.wake:
			timer.Stop() // Go 1.23+: no stale tick survives Stop
		case <-timer.C:
		}
	}
}
