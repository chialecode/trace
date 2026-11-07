package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHistoryStoreCapDedupAndSanitize(t *testing.T) {
	dir := t.TempDir()
	histPath := filepath.Join(dir, "history.json")

	store, backup, err := OpenHistory(histPath)
	if err != nil {
		t.Fatalf("OpenHistory: %v", err)
	}
	if backup != "" {
		t.Fatalf("unexpected backup %q", backup)
	}

	// Record 205 items (with cached thumbnails on item 1 and item 10) to verify 200 cap and thumbnail eviction cleanup (C03, R11).
	thumb1, err := store.SaveThumb("video", "BV1xx411c001", "cover", ".png", []byte("png-1"))
	if err != nil || thumb1 == "" || strings.ContainsAny(thumb1, "?&=") {
		t.Fatalf("SaveThumb 1: name=%q err=%v", thumb1, err)
	}
	thumb10Av, err := store.SaveThumb("video", "BV1xx411c010", "avatar", ".png", []byte("png-10-av"))
	if err != nil {
		t.Fatalf("SaveThumb 10 avatar: %v", err)
	}
	thumb10Cv, err := store.SaveThumb("video", "BV1xx411c010", "cover", ".png", []byte("png-10-cv"))
	if err != nil {
		t.Fatalf("SaveThumb 10 cover: %v", err)
	}
	for i := 1; i <= 205; i++ {
		id := fmt.Sprintf("BV1xx411c%03d", i)
		entry := HistoryEntry{
			Site:         "bilibili",
			Kind:         "video",
			ID:           id,
			Title:        fmt.Sprintf("Video %d", i),
			CanonicalURL: fmt.Sprintf("https://www.bilibili.com/video/%s?p=1&w_rid=secret&wts=12345&SESSDATA=leak", id),
		}
		if i == 1 {
			entry.CoverThumb = thumb1
		}
		if i == 10 {
			entry.AvatarThumb = thumb10Av
			entry.CoverThumb = thumb10Cv
		}
		err := store.Record(entry)
		if err != nil {
			t.Fatalf("Record %d: %v", i, err)
		}
	}

	// Item 1 was evicted by the 200 cap, so its thumbnail file must have been removed from thumbs/.
	if _, err := store.ThumbPath(thumb1); err == nil {
		t.Fatalf("expected evicted item 1 thumbnail %q to be deleted from thumbs/", thumb1)
	}

	items := store.List()
	if len(items) != MaxHistoryItems {
		t.Fatalf("len(items) = %d, want %d", len(items), MaxHistoryItems)
	}
	if items[0].ID != "BV1xx411c205" {
		t.Fatalf("newest item ID = %q, want BV1xx411c205", items[0].ID)
	}
	if items[len(items)-1].ID != "BV1xx411c006" {
		t.Fatalf("oldest item ID = %q, want BV1xx411c006", items[len(items)-1].ID)
	}

	// Re-visit BV1xx411c010 and ensure it moves to index 0 without increasing count and preserves its thumbnails.
	if err := store.Record(HistoryEntry{
		Site:  "bilibili",
		Kind:  "video",
		ID:    "BV1xx411c010",
		Title: "Video 10 Updated",
	}); err != nil {
		t.Fatalf("re-record: %v", err)
	}
	items = store.List()
	if len(items) != MaxHistoryItems {
		t.Fatalf("len(items) after dedup = %d, want %d", len(items), MaxHistoryItems)
	}
	if items[0].ID != "BV1xx411c010" || items[0].Title != "Video 10 Updated" || items[0].AvatarThumb != thumb10Av || items[0].CoverThumb != thumb10Cv {
		t.Fatalf("top item = %+v, want updated BV1xx411c010 with preserved thumbs", items[0])
	}
	if p, err := store.ThumbPath(thumb10Av); err != nil || p == "" {
		t.Fatalf("expected ThumbPath(%q) to exist, got %q err=%v", thumb10Av, p, err)
	}

	// Reject any traversal or signed query strings in ThumbPath and sanitizeThumbFileName (R11).
	for _, bad := range []string{"../history.json", "cover.jpg?upsig=secret", "cover.jpg&w_rid=123", `sub\cover.jpg`} {
		if _, err := store.ThumbPath(bad); err == nil {
			t.Fatalf("expected ThumbPath to reject %q", bad)
		}
	}

	// Remove single item BV1xx411c010 -> removes entry AND deletes its avatar & cover thumbnails (R11).
	if err := store.Remove("video", "BV1xx411c010"); err != nil {
		t.Fatalf("Remove BV1xx411c010: %v", err)
	}
	if _, err := store.ThumbPath(thumb10Av); err == nil {
		t.Fatalf("expected Remove to delete avatar thumbnail %q", thumb10Av)
	}
	if _, err := store.ThumbPath(thumb10Cv); err == nil {
		t.Fatalf("expected Remove to delete cover thumbnail %q", thumb10Cv)
	}

	// Save another thumbnail and test Clear() removes all entries and all thumbnails (R11).
	thumb205, err := store.SaveThumb("video", "BV1xx411c205", "cover", ".jpg", []byte("jpg-205"))
	if err != nil {
		t.Fatalf("SaveThumb 205: %v", err)
	}
	if _, err := store.ThumbPath(thumb205); err != nil {
		t.Fatalf("expected thumb205 to exist before Clear: %v", err)
	}

	// Attempt to record an entry with sensitive credential text.
	_ = store.Record(HistoryEntry{
		Site:        "bilibili",
		Kind:        "video",
		ID:          "SESSDATA=secret_cookie_value",
		Title:       "SESSDATA=secret_cookie_value",
		AvatarThumb: "avatar.jpg?upsig=secret_sig",
	})

	rawBytes, err := os.ReadFile(histPath)
	if err != nil {
		t.Fatalf("read history.json: %v", err)
	}
	raw := string(rawBytes)
	for _, forbidden := range []string{"w_rid", "SESSDATA", "secret_cookie_value", "upsig", "secret_sig"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("history.json leaked %q:\n%s", forbidden, raw)
		}
	}

	if err := store.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if len(store.List()) != 0 {
		t.Fatalf("expected 0 items after Clear")
	}
	if _, err := store.ThumbPath(thumb205); err == nil {
		t.Fatalf("expected Clear to delete thumbnail %q", thumb205)
	}
}

func TestAtomicWriteAndHistoryCap(t *testing.T) {
	TestHistoryStoreCapDedupAndSanitize(t)
}

func TestOpenSQLiteCorruptRecovery(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "library.db")

	if err := os.WriteFile(dbPath, []byte("not a valid sqlite database file at all"), 0o600); err != nil {
		t.Fatalf("write corrupt db: %v", err)
	}

	db, recovered, backupPath, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite failed: %v", err)
	}
	defer db.Close()

	if !recovered {
		t.Fatalf("expected recovered=true for corrupt db")
	}
	if backupPath == "" || !strings.Contains(filepath.Base(backupPath), "library.db.corrupt-") {
		t.Fatalf("unexpected backupPath %q", backupPath)
	}
	if _, err := os.Stat(backupPath); err != nil {
		t.Fatalf("backup file not found at %q: %v", backupPath, err)
	}
}
