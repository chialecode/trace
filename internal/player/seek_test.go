package player

import (
	"image"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// seekRecorder records which of its readers were moved, and from where, as
// the split Media Foundation decoder has one reader for video and one for
// audio.
type seekRecorder struct {
	*slowDecoder
	mu          sync.Mutex
	videoSeeks  int
	audioSeeks  []time.Duration
	previews    atomic.Int32
	callerSeeks atomic.Int32 // Seek of both readers
}

func (d *seekRecorder) Seek(pos time.Duration) error {
	d.callerSeeks.Add(1)
	d.mu.Lock()
	d.videoSeeks++
	d.mu.Unlock()
	return d.SynthDecoder.Seek(pos)
}

func (d *seekRecorder) SeekAudio(pos time.Duration) error {
	d.mu.Lock()
	d.audioSeeks = append(d.audioSeeks, pos)
	d.mu.Unlock()
	return d.SynthDecoder.Seek(pos)
}

func (d *seekRecorder) PreviewFrameAt(pos time.Duration, dst *image.RGBA) (*image.RGBA, time.Duration, error) {
	d.previews.Add(1)
	d.lastPos.Store(int64(pos))
	return d.SynthDecoder.DecodeVideoFrameAt(pos, dst)
}

// F-059: a seek moved both readers twice, once from the UI thread and once
// from the audio sink's goroutine (through SetPosition), so the audio reader
// jumped under the sink while it read, and the video reader was pulled back
// to the key frame after the worker had caught up.
func TestSeekMovesOnlyTheAudioReaderFromTheSink(t *testing.T) {
	dec := &seekRecorder{slowDecoder: &slowDecoder{SynthDecoder: NewSynthDecoder(64, 36, 60*time.Second, 30)}}
	c := NewController(Options{AudioSink: NewNullAudioSink()})
	defer c.Close()
	if err := c.LoadDecoder("seek.mp4", dec); err != nil {
		t.Fatal(err)
	}
	c.Play()
	if err := c.Seek(20); err != nil {
		t.Fatal(err)
	}
	if n := dec.callerSeeks.Load(); n != 0 {
		t.Fatalf("Seek moved both readers %d times; the video worker seeks video itself", n)
	}
	dec.mu.Lock()
	audio := append([]time.Duration(nil), dec.audioSeeks...)
	dec.mu.Unlock()
	if len(audio) != 1 || audio[0] != 20*time.Second {
		t.Fatalf("audio seeks = %v, want one to 20s", audio)
	}
	waitFor(t, "video at the new position", func() bool {
		return time.Duration(dec.lastPos.Load()) >= 20*time.Second
	})
}

// F-059: dragging the progress bar sought audio and video every frame.
// Now it previews key frames with the audio held, and seeks once on release,
// resuming playback.
func TestDraggingTheProgressBarPreviewsThenSeeksOnce(t *testing.T) {
	dec := &seekRecorder{slowDecoder: &slowDecoder{SynthDecoder: NewSynthDecoder(64, 36, 60*time.Second, 30)}}
	sink := NewNullAudioSink()
	c := NewController(Options{AudioSink: sink})
	defer c.Close()
	if err := c.LoadDecoder("seek.mp4", dec); err != nil {
		t.Fatal(err)
	}
	c.Play()
	for _, sec := range []float64{10, 11, 12, 13, 30} {
		c.Preview(sec)
		time.Sleep(5 * time.Millisecond)
	}
	if !c.Scrubbing() || sink.IsPlaying() {
		t.Fatalf("while dragging: scrubbing=%v, audio playing=%v; want the audio held", c.Scrubbing(), sink.IsPlaying())
	}
	if snap := c.Snapshot(640, 360); snap.Playing || snap.PositionSec != 30 {
		t.Fatalf("while dragging: playing=%v at %v, want paused at 30", snap.Playing, snap.PositionSec)
	}
	waitFor(t, "a key-frame preview", func() bool { return dec.previews.Load() > 0 })
	dec.mu.Lock()
	early := len(dec.audioSeeks)
	dec.mu.Unlock()
	if early != 0 {
		t.Fatalf("the audio was sought %d times while dragging", early)
	}

	if err := c.Seek(30); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "audio resumed with the frame at 30s", sink.IsPlaying)
	if c.Scrubbing() || !sink.IsPlaying() {
		t.Fatalf("after release: scrubbing=%v, audio playing=%v; want playback resumed", c.Scrubbing(), sink.IsPlaying())
	}
	dec.mu.Lock()
	audio := append([]time.Duration(nil), dec.audioSeeks...)
	dec.mu.Unlock()
	if len(audio) != 1 || audio[0] != 30*time.Second {
		t.Fatalf("audio seeks = %v, want one to 30s", audio)
	}
	if n := dec.callerSeeks.Load(); n != 0 {
		t.Fatalf("the caller moved the readers %d times", n)
	}
	// A drag that started paused stays paused.
	c.Pause()
	c.Preview(5)
	_ = c.Seek(5)
	if sink.IsPlaying() || c.Snapshot(640, 360).Playing {
		t.Fatal("a drag started while paused resumed playback")
	}
}

// F-059: after a seek during playback the sound waited for nothing and ran
// ahead of a picture still decoding from the key frame. The audio is held
// until the first frame at the new position is ready.
func TestSeekWhilePlayingHoldsAudioForTheFirstFrame(t *testing.T) {
	dec := &seekRecorder{slowDecoder: &slowDecoder{SynthDecoder: NewSynthDecoder(64, 36, 60*time.Second, 30), delay: 120 * time.Millisecond}}
	sink := NewNullAudioSink()
	c := NewController(Options{AudioSink: sink})
	defer c.Close()
	if err := c.LoadDecoder("seek.mp4", dec); err != nil {
		t.Fatal(err)
	}
	c.Play()
	time.Sleep(150 * time.Millisecond)
	before := dec.decodes.Load()
	if err := c.Seek(40); err != nil {
		t.Fatal(err)
	}
	if sink.IsPlaying() {
		t.Fatal("the audio ran on before any frame at the new position was decoded")
	}
	if !c.Snapshot(640, 360).Playing {
		t.Fatal("the player reported paused while it waits for the frame")
	}
	waitFor(t, "audio resumed", sink.IsPlaying)
	if dec.decodes.Load() <= before || time.Duration(dec.lastPos.Load()) < 40*time.Second {
		t.Fatalf("audio resumed before the frame at 40s (decodes %d→%d, last %v)", before, dec.decodes.Load(), time.Duration(dec.lastPos.Load()))
	}
	if pos := c.Snapshot(640, 360).PositionSec; pos < 40 || pos > 41 {
		t.Fatalf("position %v after resuming, want just past 40", pos)
	}
}
