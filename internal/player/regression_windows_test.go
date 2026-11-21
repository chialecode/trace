//go:build windows

package player

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"github.com/chialecode/trace/internal/download"
)

func TestSplitDecoderDelegatesAudioSampleRateAndChannels(t *testing.T) {
	video := &MFDecoder{hasVideo: true, hasAudio: true, sampleRate: 48000, channels: 2}
	audio := &MFDecoder{hasAudio: true, sampleRate: 44100, channels: 1}
	sd := &splitDecoder{MFDecoder: video, audio: audio}

	if got := sd.SampleRate(); got != 44100 {
		t.Fatalf("splitDecoder.SampleRate() = %d, want 44100", got)
	}
	if got := sd.Channels(); got != 1 {
		t.Fatalf("splitDecoder.Channels() = %d, want 1", got)
	}
}

func TestPreviewFrameAtScrubBackDoesNotReturnStalePreviewFrame(t *testing.T) {
	tmp := t.TempDir()
	vPath := filepath.Join(tmp, "video.m4s")
	aPath := filepath.Join(tmp, "audio.m4s")
	mp4Path := filepath.Join(tmp, "muxed.mp4")

	// Build a 4-second synthetic MP4 (120 frames at 30fps).
	vBytes, err := download.BuildSyntheticVideoM4S(120, 30)
	if err != nil {
		t.Fatalf("BuildSyntheticVideoM4S: %v", err)
	}
	aBytes, err := download.BuildSyntheticAudioM4S(180)
	if err != nil {
		t.Fatalf("BuildSyntheticAudioM4S: %v", err)
	}
	if err := os.WriteFile(vPath, vBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(aPath, aBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := download.MuxDASHFiles(vPath, aPath, mp4Path); err != nil {
		t.Fatalf("MuxDASHFiles: %v", err)
	}

	dec, err := OpenMediaFile(mp4Path)
	if err != nil {
		t.Fatalf("OpenMediaFile: %v", err)
	}
	defer dec.Close()

	// 1. Decode near start (0.1s).
	if _, pts0, err := dec.DecodeVideoFrameAt(100*time.Millisecond, nil); err != nil || pts0 > 500*time.Millisecond {
		t.Fatalf("initial decode pts=%v err=%v", pts0, err)
	}

	// 2. Scrub far ahead to 3.0s via PreviewFrameAt.
	previewer, ok := dec.(keyFramePreviewer)
	if !ok {
		t.Fatalf("decoder %T does not implement keyFramePreviewer", dec)
	}
	if _, prevPTS, err := previewer.PreviewFrameAt(3*time.Second, nil); err != nil {
		t.Fatalf("PreviewFrameAt(3s): %v", err)
	} else if prevPTS < 0 {
		t.Fatalf("invalid preview pts %v", prevPTS)
	}

	// 3. Release scrub back near 0.2s — must seek back, never return a >2s preview frame.
	_, ptsBack, err := dec.DecodeVideoFrameAt(200*time.Millisecond, nil)
	if err != nil {
		t.Fatalf("DecodeVideoFrameAt(200ms) after scrub-back: %v", err)
	}
	if ptsBack > 1*time.Second {
		t.Fatalf("scrub-back returned stale far-ahead preview frame at %v (want <= 1s)", ptsBack)
	}
}

func TestMFLiveByteStreamAbortedSourceAndPendingReadsRetention(t *testing.T) {
	initSeg, frags := buildSyntheticGrowingFMP4Parts(t, true, 0, 0, 1)
	partPath := filepath.Join(t.TempDir(), "live.mp4.part")
	raw := append(append([]byte(nil), initSeg...), frags[0]...)
	if err := os.WriteFile(partPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	src, err := openLiveFMP4Source(context.Background(), partPath, LivePreviewOptions{
		PollInterval: 10 * time.Millisecond,
		InitialWait:  2 * time.Second,
		IsActive:     func() bool { return false },
	})
	if err != nil {
		t.Fatalf("openLiveFMP4Source: %v", err)
	}

	// Verify newMFLiveByteStream succeeds cleanly even before any other MFStartup,
	// initializes bs.attrs, and retains pendingReads entries for async EndRead.
	bs, err := newMFLiveByteStream(src)
	if err != nil {
		_ = src.Close()
		t.Fatalf("newMFLiveByteStream: %v", err)
	}
	if bs.attrs == 0 {
		bs.waitClosed()
		bs.releaseOwner()
		_ = src.Close()
		t.Fatal("expected bs.attrs to be created after MFStartup")
	}

	// Simulate two overlapping async results and verify EndRead consumes each by key
	// without falling back to the wrong outcome.
	const fakeRes1 uintptr = 0x1001
	const fakeRes2 uintptr = 0x1002
	bs.mu.Lock()
	bs.pendingReads[fakeRes1] = mfAsyncReadOutcome{n: 111, hr: hrOK}
	bs.pendingReads[fakeRes2] = mfAsyncReadOutcome{n: 222, hr: hrOK}
	bs.mu.Unlock()

	var got2 uint32
	if hr := vcall(bs.comPtr(), 11, fakeRes2, uintptr(unsafe.Pointer(&got2))); hr != hrOK || got2 != 222 {
		t.Fatalf("EndRead(fakeRes2) hr=0x%x n=%d, want hrOK/222", hr, got2)
	}
	var got1 uint32
	if hr := vcall(bs.comPtr(), 11, fakeRes1, uintptr(unsafe.Pointer(&got1))); hr != hrOK || got1 != 111 {
		t.Fatalf("EndRead(fakeRes1) hr=0x%x n=%d, want hrOK/111", hr, got1)
	}
	// Unknown pResult must NOT return a stale lastOutcome.
	var gotUnknown uint32
	if hr := vcall(bs.comPtr(), 11, 0x9999, uintptr(unsafe.Pointer(&gotUnknown))); hr != hrEInvalidArg || gotUnknown != 0 {
		t.Fatalf("EndRead(unknown) hr=0x%x n=%d, want hrEInvalidArg/0", hr, gotUnknown)
	}

	bs.waitClosed()
	bs.releaseOwner()

	// Aborting src first must make newMFLiveByteStream return immediately (no deadlock).
	src.abort()
	if _, err := newMFLiveByteStream(src); err == nil {
		t.Fatal("expected newMFLiveByteStream on aborted source to fail immediately")
	}
	_ = src.Close()
}

func TestOpenLivePreviewCanceledContextDoesNotDeadlockAndReleasesFile(t *testing.T) {
	initSeg, frags := buildSyntheticGrowingFMP4Parts(t, true, 0, 0, 2)
	partPath := filepath.Join(t.TempDir(), "cancel.mp4.part")
	var raw []byte
	raw = append(raw, initSeg...)
	for _, f := range frags {
		raw = append(raw, f...)
	}
	if err := os.WriteFile(partPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	// Open and close immediately across multiple cycles to exercise teardown ordering.
	for i := 0; i < 3; i++ {
		dec, err := OpenLivePreview(context.Background(), partPath, LivePreviewOptions{
			PollInterval: 10 * time.Millisecond,
			InitialWait:  5 * time.Second,
			IsActive:     func() bool { return true },
		})
		if err != nil {
			t.Fatalf("cycle %d OpenLivePreview: %v", i, err)
		}
		if err := dec.Close(); err != nil {
			t.Fatalf("cycle %d Close: %v", i, err)
		}
	}

	// Pre-canceled context returns promptly without hanging.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() {
		dec, err := OpenLivePreview(ctx, partPath, LivePreviewOptions{
			PollInterval: 10 * time.Millisecond,
			InitialWait:  5 * time.Second,
			IsActive:     func() bool { return true },
		})
		if dec != nil {
			_ = dec.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error for canceled context")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("OpenLivePreview deadlocked on canceled context")
	}

	// Verify zero lingering file handles on Windows by renaming the .part file.
	finalPath := filepath.Join(filepath.Dir(partPath), "renamed.mp4")
	if err := os.Rename(partPath, finalPath); err != nil {
		t.Fatalf("file handle leaked after Close/cancel: rename failed: %v", err)
	}
}
