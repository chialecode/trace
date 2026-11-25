package library

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/storage"
)

func openSavePerfStore(t *testing.T) (*Store, string) {
	t.Helper()
	tmp := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cacheDir := filepath.Join(tmp, "cache")
	store, err := OpenWithDataDir(db, cacheDir, tmp)
	if err != nil {
		t.Fatal(err)
	}
	return store, cacheDir
}

func testSHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestSaveItemDoesNotHoldLockDuringSHA256Verification(t *testing.T) {
	store, cacheDir := openSavePerfStore(t)

	videoDir := filepath.Join(cacheDir, "bilibili", "1001", "videos", "BV1perf00001")
	if err := os.MkdirAll(videoDir, 0o700); err != nil {
		t.Fatal(err)
	}
	videoBytes := []byte("synthetic-video-payload-for-lock-free-hash")
	if err := os.WriteFile(filepath.Join(videoDir, "P01.mp4"), videoBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	liveDir := filepath.Join(cacheDir, "douyin", "2002", "lives", "live_2002_100")
	if err := os.MkdirAll(liveDir, 0o700); err != nil {
		t.Fatal(err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	var enterOnce sync.Once
	var hashCalls atomic.Int32
	verifySHA256Hook = func(full string) {
		hashCalls.Add(1)
		enterOnce.Do(func() { close(entered) })
		<-release
	}
	t.Cleanup(func() { verifySHA256Hook = nil })

	type saveResult struct {
		meta ItemMetadata
		err  error
	}
	done := make(chan saveResult, 1)
	go func() {
		saved, err := store.SaveItem(videoDir, ItemMetadata{
			Version: 1,
			Site:    "bilibili",
			Kind:    "video",
			ID:      "BV1perf00001",
			Title:   "Hash Outside Lock",
			Author:  ItemAuthor{Site: "bilibili", Mid: 1001, Name: "Tester"},
			Media: []MediaFile{{
				Role:   "video",
				Path:   "P01.mp4",
				Bytes:  int64(len(videoBytes)),
				SHA256: testSHA256Hex(videoBytes),
			}},
		})
		done <- saveResult{meta: saved, err: err}
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("timed out waiting for SaveItem to enter SHA-256 verification")
	}

	// While SaveItem is blocked hashing on disk, concurrent SaveRecordingCheckpoint
	// and GetItem must NOT block waiting on s.mu.
	concurrentDone := make(chan error, 1)
	go func() {
		savedLive, err := store.SaveRecordingCheckpoint(liveDir, ItemMetadata{
			Version: 1,
			Site:    "douyin",
			Kind:    "live",
			ID:      "2002:100",
			Status:  "recording",
			Title:   "Concurrent Live Checkpoint",
			Author:  ItemAuthor{Site: "douyin", UID: "2002", Name: "Streamer"},
			Live:    &LiveRecordingInfo{RoomID: 2002},
		})
		if err != nil {
			concurrentDone <- err
			return
		}
		got, ok, err := store.GetItem(savedLive.ItemKey)
		if err != nil {
			concurrentDone <- err
			return
		}
		if !ok || got.Status != "recording" {
			concurrentDone <- os.ErrInvalid
			return
		}
		concurrentDone <- nil
	}()

	select {
	case err := <-concurrentDone:
		if err != nil {
			close(release)
			t.Fatalf("concurrent SaveRecordingCheckpoint/GetItem failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("SaveRecordingCheckpoint/GetItem blocked while SaveItem was hashing on disk")
	}

	close(release)
	res := <-done
	if res.err != nil {
		t.Fatalf("SaveItem failed: %v", res.err)
	}
	if res.meta.Status != "complete" || res.meta.TotalBytes != int64(len(videoBytes)) {
		t.Fatalf("SaveItem = status %q bytes %d, want complete/%d", res.meta.Status, res.meta.TotalBytes, len(videoBytes))
	}
	if hashCalls.Load() != 1 {
		t.Fatalf("hashCalls = %d, want 1", hashCalls.Load())
	}
}

func TestSaveLiveItemSkipsSHA256AndValidatesFiles(t *testing.T) {
	store, cacheDir := openSavePerfStore(t)

	var hashCalls atomic.Int32
	verifySHA256Hook = func(full string) {
		hashCalls.Add(1)
	}
	t.Cleanup(func() { verifySHA256Hook = nil })

	// Non-live items must be rejected by SaveLiveItem.
	nonLiveDir := filepath.Join(cacheDir, "bilibili", "1001", "videos", "BV1notlive01")
	if _, err := store.SaveLiveItem(nonLiveDir, ItemMetadata{
		Version: 1,
		Site:    "bilibili",
		Kind:    "video",
		ID:      "BV1notlive01",
	}); err == nil {
		t.Fatal("SaveLiveItem accepted non-live item")
	}

	liveDir := filepath.Join(cacheDir, "douyin", "3003", "lives", "live_3003_200")
	if err := os.MkdirAll(liveDir, 0o700); err != nil {
		t.Fatal(err)
	}
	segData := []byte("finalized-live-segment-bytes")
	segPath := filepath.Join(liveDir, "part-001.mp4")
	if err := os.WriteFile(segPath, segData, 0o600); err != nil {
		t.Fatal(err)
	}

	baseMeta := ItemMetadata{
		Version: 1,
		Site:    "douyin",
		Kind:    "live",
		ID:      "3003:200",
		Status:  "complete",
		Title:   "Live Recording",
		Author:  ItemAuthor{Site: "douyin", UID: "3003", Name: "Host"},
		Media: []MediaFile{{
			Role:   "live_segment",
			Path:   "part-001.mp4",
			Bytes:  int64(len(segData)),
			SHA256: "0000000000000000000000000000000000000000000000000000000000000000",
		}},
		Live: &LiveRecordingInfo{
			RoomID: 3003,
			Segments: []LiveSegmentInfo{{
				File:   "part-001.mp4",
				Bytes:  int64(len(segData)),
				SHA256: "0000000000000000000000000000000000000000000000000000000000000000",
			}},
		},
	}

	// 1. Valid size & regular file -> complete without SHA-256 disk reread.
	saved, err := store.SaveLiveItem(liveDir, baseMeta)
	if err != nil {
		t.Fatalf("SaveLiveItem valid: %v", err)
	}
	if saved.Status != "complete" || saved.TotalBytes != int64(len(segData)) {
		t.Fatalf("SaveLiveItem valid = status %q bytes %d, want complete/%d", saved.Status, saved.TotalBytes, len(segData))
	}
	if hashCalls.Load() != 0 {
		t.Fatalf("SaveLiveItem triggered %d SHA-256 reads, want 0", hashCalls.Load())
	}

	// 2. Resized file -> missing_files without SHA-256 disk reread.
	if err := os.WriteFile(segPath, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	resized, err := store.SaveLiveItem(liveDir, baseMeta)
	if err != nil {
		t.Fatalf("SaveLiveItem resized: %v", err)
	}
	if resized.Status != "missing_files" {
		t.Fatalf("SaveLiveItem resized status = %q, want missing_files", resized.Status)
	}

	// 3. Path traversal -> missing_files.
	evilMeta := baseMeta
	evilMeta.Media = []MediaFile{{Role: "live_segment", Path: "../escape.mp4", Bytes: 5}}
	evilSaved, err := store.SaveLiveItem(liveDir, evilMeta)
	if err != nil {
		t.Fatalf("SaveLiveItem traversal: %v", err)
	}
	if evilSaved.Status != "missing_files" {
		t.Fatalf("SaveLiveItem traversal status = %q, want missing_files", evilSaved.Status)
	}

	// 4. Non-regular path (directory in place of segment file) -> missing_files.
	if err := os.Remove(segPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(segPath, 0o700); err != nil {
		t.Fatal(err)
	}
	dirSaved, err := store.SaveLiveItem(liveDir, baseMeta)
	if err != nil {
		t.Fatalf("SaveLiveItem non-regular: %v", err)
	}
	if dirSaved.Status != "missing_files" {
		t.Fatalf("SaveLiveItem non-regular status = %q, want missing_files", dirSaved.Status)
	}
	if err := os.Remove(segPath); err != nil {
		t.Fatal(err)
	}

	// 5. Missing file -> missing_files; under allowMovedFiles -> complete with preserved TotalBytes.
	missingSaved, err := store.SaveLiveItem(liveDir, baseMeta)
	if err != nil {
		t.Fatalf("SaveLiveItem missing: %v", err)
	}
	if missingSaved.Status != "missing_files" {
		t.Fatalf("SaveLiveItem missing status = %q, want missing_files", missingSaved.Status)
	}

	store.SetAllowMovedFiles(true)
	movedSaved, err := store.SaveLiveItem(liveDir, baseMeta)
	if err != nil {
		t.Fatalf("SaveLiveItem allowMoved: %v", err)
	}
	if movedSaved.Status != "complete" || movedSaved.TotalBytes != int64(len(segData)) {
		t.Fatalf("SaveLiveItem allowMoved = status %q bytes %d, want complete/%d", movedSaved.Status, movedSaved.TotalBytes, len(segData))
	}
	if hashCalls.Load() != 0 {
		t.Fatalf("SaveLiveItem triggered %d SHA-256 reads across all cases, want 0", hashCalls.Load())
	}
}

func TestSaveItemTOCTOUDriftRetriesAndRehashes(t *testing.T) {
	store, cacheDir := openSavePerfStore(t)

	t.Run("drift to valid file retries and succeeds", func(t *testing.T) {
		itemDir := filepath.Join(cacheDir, "bilibili", "1001", "videos", "BV1toctou001")
		if err := os.MkdirAll(itemDir, 0o700); err != nil {
			t.Fatal(err)
		}
		filePath := filepath.Join(itemDir, "P01.mp4")
		initial := []byte("v1-stale")
		finalData := []byte("v2-valid-replacement-content")
		if err := os.WriteFile(filePath, initial, 0o600); err != nil {
			t.Fatal(err)
		}

		var calls atomic.Int32
		verifySHA256Hook = func(full string) {
			if calls.Add(1) == 1 {
				_ = os.WriteFile(filePath, finalData, 0o600)
				future := time.Now().Add(2 * time.Second)
				_ = os.Chtimes(filePath, future, future)
			}
		}
		t.Cleanup(func() { verifySHA256Hook = nil })

		saved, err := store.SaveItem(itemDir, ItemMetadata{
			Version: 1,
			Site:    "bilibili",
			Kind:    "video",
			ID:      "BV1toctou001",
			Title:   "TOCTOU Retry Valid",
			Author:  ItemAuthor{Site: "bilibili", Mid: 1001, Name: "Tester"},
			Media: []MediaFile{{
				Role:   "video",
				Path:   "P01.mp4",
				SHA256: testSHA256Hex(finalData),
			}},
		})
		if err != nil {
			t.Fatalf("SaveItem: %v", err)
		}
		if calls.Load() != 2 {
			t.Fatalf("hash attempts = %d, want 2 (initial + TOCTOU retry)", calls.Load())
		}
		if saved.Status != "complete" || saved.TotalBytes != int64(len(finalData)) {
			t.Fatalf("saved = status %q bytes %d, want complete/%d", saved.Status, saved.TotalBytes, len(finalData))
		}
	})

	t.Run("drift to corrupt file retries and marks missing_files", func(t *testing.T) {
		itemDir := filepath.Join(cacheDir, "bilibili", "1001", "videos", "BV1toctou002")
		if err := os.MkdirAll(itemDir, 0o700); err != nil {
			t.Fatal(err)
		}
		filePath := filepath.Join(itemDir, "P01.mp4")
		goodData := []byte("v1-good-content")
		corruptData := []byte("v2-corrupt-content-changed")
		if err := os.WriteFile(filePath, goodData, 0o600); err != nil {
			t.Fatal(err)
		}

		var calls atomic.Int32
		verifySHA256Hook = func(full string) {
			if calls.Add(1) == 1 {
				_ = os.WriteFile(filePath, corruptData, 0o600)
				future := time.Now().Add(2 * time.Second)
				_ = os.Chtimes(filePath, future, future)
			}
		}
		t.Cleanup(func() { verifySHA256Hook = nil })

		saved, err := store.SaveItem(itemDir, ItemMetadata{
			Version: 1,
			Site:    "bilibili",
			Kind:    "video",
			ID:      "BV1toctou002",
			Title:   "TOCTOU Retry Corrupt",
			Author:  ItemAuthor{Site: "bilibili", Mid: 1001, Name: "Tester"},
			Media: []MediaFile{{
				Role:   "video",
				Path:   "P01.mp4",
				SHA256: testSHA256Hex(goodData),
			}},
		})
		if err != nil {
			t.Fatalf("SaveItem: %v", err)
		}
		if calls.Load() != 2 {
			t.Fatalf("hash attempts = %d, want 2 (initial + TOCTOU retry)", calls.Load())
		}
		if saved.Status != "missing_files" {
			t.Fatalf("saved status = %q, want missing_files", saved.Status)
		}
	})

	t.Run("stable hash mismatch marks missing_files without retry", func(t *testing.T) {
		itemDir := filepath.Join(cacheDir, "bilibili", "1001", "videos", "BV1toctou003")
		if err := os.MkdirAll(itemDir, 0o700); err != nil {
			t.Fatal(err)
		}
		filePath := filepath.Join(itemDir, "P01.mp4")
		data := []byte("stable-mismatch-content")
		if err := os.WriteFile(filePath, data, 0o600); err != nil {
			t.Fatal(err)
		}

		var calls atomic.Int32
		verifySHA256Hook = func(full string) {
			calls.Add(1)
		}
		t.Cleanup(func() { verifySHA256Hook = nil })

		saved, err := store.SaveItem(itemDir, ItemMetadata{
			Version: 1,
			Site:    "bilibili",
			Kind:    "video",
			ID:      "BV1toctou003",
			Title:   "Stable Mismatch",
			Author:  ItemAuthor{Site: "bilibili", Mid: 1001, Name: "Tester"},
			Media: []MediaFile{{
				Role:   "video",
				Path:   "P01.mp4",
				SHA256: testSHA256Hex([]byte("different-expected-content")),
			}},
		})
		if err != nil {
			t.Fatalf("SaveItem: %v", err)
		}
		if calls.Load() != 1 {
			t.Fatalf("hash attempts = %d, want 1", calls.Load())
		}
		if saved.Status != "missing_files" {
			t.Fatalf("saved status = %q, want missing_files", saved.Status)
		}
	})
}
