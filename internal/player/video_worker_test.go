package player

import (
	"image"
	"sync/atomic"
	"testing"
	"time"
)

// slowDecoder makes every video decode take delay, as software decoding of a
// 4K or 60 fps file does, and records the positions it was asked for.
type slowDecoder struct {
	*SynthDecoder
	delay   time.Duration
	decodes atomic.Int32
	lastPos atomic.Int64
}

func (d *slowDecoder) DecodeVideoFrameAt(pos time.Duration, dst *image.RGBA) (*image.RGBA, time.Duration, error) {
	time.Sleep(d.delay)
	d.decodes.Add(1)
	d.lastPos.Store(int64(pos))
	return d.SynthDecoder.DecodeVideoFrameAt(pos, dst)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// F-043: the view calls Advance every frame; with decoding on the UI thread a
// slow decoder froze the window (13 s per call for a real 4K file).
func TestAdvanceNeverDecodesOnTheUIThread(t *testing.T) {
	dec := &slowDecoder{SynthDecoder: NewSynthDecoder(64, 36, 30*time.Second, 30), delay: 150 * time.Millisecond}
	c := NewController(Options{AudioSink: NewNullAudioSink()})
	defer c.Close()
	if err := c.LoadDecoder("slow.mp4", dec); err != nil {
		t.Fatal(err)
	}
	notified := make(chan struct{}, 8)
	c.SetFrameNotify(func() { notified <- struct{}{} })

	c.Play()
	var slowest time.Duration
	for i := 0; i < 20; i++ {
		start := time.Now()
		c.Advance(0)
		_ = c.Snapshot(1280, 720)
		slowest = max(slowest, time.Since(start))
		time.Sleep(10 * time.Millisecond)
	}
	if slowest > 50*time.Millisecond {
		t.Fatalf("Advance blocked the UI thread for %v with a %v decoder", slowest, dec.delay)
	}
	waitFor(t, "frames decoded while playing", func() bool { return dec.decodes.Load() >= 2 })

	// Paused seek: the frame arrives later, with a redraw request.
	c.Pause()
	start := time.Now()
	if err := c.Seek(12); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 200*time.Millisecond+dec.delay {
		t.Fatalf("Seek waited %v", took)
	}
	select {
	case <-notified:
	case <-time.After(5 * time.Second):
		t.Fatal("no redraw was requested for the frame decoded after a paused seek")
	}
	if got := time.Duration(dec.lastPos.Load()); got != 12*time.Second {
		t.Fatalf("worker decoded %v after the seek, want 12s", got)
	}
	c.Advance(0)
	c.worker.mu.Lock()
	shown := c.worker.shown
	c.worker.mu.Unlock()
	if shown == nil || c.Bitmap() == nil {
		t.Fatal("the frame decoded after the seek was not shown")
	}
}

// Frames decoded for a position the user already left are never shown, and
// the buffer on screen is never handed back to the decoder.
func TestStaleFramesAreDroppedAndShownBufferIsNotReused(t *testing.T) {
	dec := &slowDecoder{SynthDecoder: NewSynthDecoder(64, 36, 30*time.Second, 30), delay: 20 * time.Millisecond}
	c := NewController(Options{AudioSink: NewNullAudioSink()})
	defer c.Close()
	if err := c.LoadDecoder("slow.mp4", dec); err != nil {
		t.Fatal(err)
	}
	w := c.worker
	stale := image.NewRGBA(image.Rect(0, 0, 64, 36))
	w.publish(stale, c.seekGen)
	_ = c.Seek(5) // a newer position: the pending frame is stale now
	c.mu.Lock()
	c.showReadyFrameLocked()
	c.mu.Unlock()
	w.mu.Lock()
	if w.shown == stale {
		w.mu.Unlock()
		t.Fatal("a frame decoded before the seek was shown")
	}
	w.mu.Unlock()

	waitFor(t, "frame after the seek", func() bool {
		c.Advance(0)
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.shown != nil
	})
	w.mu.Lock()
	shown := w.shown
	w.mu.Unlock()
	if w.publish(shown, c.seekGen) {
		t.Fatal("the buffer on screen was accepted as a new frame")
	}
	for i := 0; i < maxFreeFrames+2; i++ {
		if got := w.takeFree(); got == shown {
			t.Fatal("the buffer on screen was handed out for decoding")
		}
	}
}
