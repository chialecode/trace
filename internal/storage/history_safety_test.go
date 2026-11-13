package storage

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestHistorySharedThumbnailRespectsWindowsFilenameCase(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows filename comparison")
	}
	store, _, err := OpenHistory(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	name, err := store.SaveThumb("video", "legacy-case", "cover", ".png", []byte("shared image"))
	if err != nil {
		t.Fatal(err)
	}
	for i, reference := range []string{name, strings.ToUpper(name)} {
		if err := store.Record(HistoryEntry{Kind: "video", ID: string(rune('a' + i)), CoverThumb: reference}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Remove("video", "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ThumbPath(strings.ToUpper(name)); err != nil {
		t.Fatalf("deleting a lowercase reference removed the same file used by an uppercase reference: %v", err)
	}
	if err := store.Remove("video", "b"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store.ThumbsDir(), name)); !os.IsNotExist(err) {
		t.Fatalf("last case-insensitive reference retained its thumbnail: %v", err)
	}
}

func TestHistoryCleanupKeepsSharedThumbnails(t *testing.T) {
	store, _, err := OpenHistory(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	thumb, err := store.SaveThumb("video", "shared", "cover", ".png", []byte("shared image"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"first", "second"} {
		if err := store.Record(HistoryEntry{Kind: "video", ID: id, CoverThumb: thumb}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Remove("video", "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ThumbPath(thumb); err != nil {
		t.Fatalf("removing one history entry removed a thumbnail still used by another: %v", err)
	}
	if err := store.Remove("video", "second"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store.ThumbsDir(), thumb)); !os.IsNotExist(err) {
		t.Fatalf("last reference did not clean up its thumbnail: %v", err)
	}
}

func TestHistoryCleanupNeverUsesIDPrefixes(t *testing.T) {
	store, _, err := OpenHistory(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	var retained string
	for _, id := range []string{"part", "part_alt"} {
		thumb, err := store.SaveThumb("video", id, "cover", ".png", []byte(id))
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Record(HistoryEntry{Kind: "video", ID: id, CoverThumb: thumb}); err != nil {
			t.Fatal(err)
		}
		if id == "part_alt" {
			retained = thumb
		}
	}
	if err := store.Remove("video", "part"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ThumbPath(retained); err != nil {
		t.Fatalf("cleanup matched a different object's ID prefix: %v", err)
	}
}

func TestHistoryClearPreservesUnregisteredFiles(t *testing.T) {
	store, _, err := OpenHistory(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	thumb, err := store.SaveThumb("video", "owned", "cover", ".png", []byte("owned image"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Record(HistoryEntry{Kind: "video", ID: "owned", CoverThumb: thumb}); err != nil {
		t.Fatal(err)
	}
	userPath := filepath.Join(store.ThumbsDir(), "my-photo.png")
	if err := os.WriteFile(userPath, []byte("user file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ThumbPath("my-photo.png"); err == nil {
		t.Fatal("history media access served an unregistered file")
	}
	if err := store.Clear(); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(userPath); err != nil || string(raw) != "user file" {
		t.Fatalf("clearing history deleted an unregistered file: raw=%q err=%v", raw, err)
	}
	if _, err := os.Stat(filepath.Join(store.ThumbsDir(), thumb)); !os.IsNotExist(err) {
		t.Fatalf("clearing history left its registered thumbnail: %v", err)
	}
}

func TestHistoryThumbUpdatesCannotOverwriteReferencedFiles(t *testing.T) {
	dir := t.TempDir()
	historyPath := filepath.Join(dir, "history.json")
	store, _, err := OpenHistory(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	oldName, err := store.SaveThumb("video", "BV1Case", "cover", ".png", []byte("old image"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Record(HistoryEntry{Kind: "video", ID: "BV1Case", CoverThumb: oldName}); err != nil {
		t.Fatal(err)
	}
	newName, err := store.SaveThumb("video", "BV1Case", "cover", ".png", []byte("new image"))
	if err != nil {
		t.Fatal(err)
	}
	if newName == oldName {
		t.Fatal("a refreshed thumbnail overwrites a file referenced by the committed history")
	}
	otherName, err := store.SaveThumb("video", "BV1case", "cover", ".png", []byte("other image"))
	if err != nil {
		t.Fatal(err)
	}
	if otherName == newName {
		t.Fatal("case-sensitive object IDs collided in the thumbnail cache")
	}
	if err := os.Remove(historyPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(historyPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.Record(HistoryEntry{Kind: "video", ID: "BV1Case", CoverThumb: newName}); err == nil {
		t.Fatal("expected the history write to fail")
	}
	if items := store.List(); len(items) != 1 || items[0].CoverThumb != oldName {
		t.Fatalf("failed history write changed the committed entry: %+v", items)
	}
	if raw, err := os.ReadFile(filepath.Join(store.ThumbsDir(), oldName)); err != nil || string(raw) != "old image" {
		t.Fatalf("failed history update changed the old thumbnail: raw=%q err=%v", raw, err)
	}
}

func TestHistoryThumbnailCommitFailureDiscardsOnlyNewFiles(t *testing.T) {
	historyPath := filepath.Join(t.TempDir(), "history.json")
	store, _, err := OpenHistory(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordWithThumbs(HistoryEntry{Kind: "video", ID: "BV1Commit"}, HistoryThumbnail{Role: "cover", Ext: ".png", Data: []byte("original")}); err != nil {
		t.Fatal(err)
	}
	old := store.List()[0]
	if err := os.Remove(historyPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(historyPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordWithThumbs(HistoryEntry{Kind: "video", ID: "BV1Commit"}, HistoryThumbnail{Role: "cover", Ext: ".png", Data: []byte("replacement")}); err == nil {
		t.Fatal("expected history commit failure")
	}
	entries, err := os.ReadDir(store.ThumbsDir())
	if err != nil || len(entries) != 1 || entries[0].Name() != old.CoverThumb {
		t.Fatalf("failed thumbnail commit left new files or removed the old one: %+v err=%v", entries, err)
	}
	if got := store.List(); len(got) != 1 || got[0].CoverThumb != old.CoverThumb {
		t.Fatalf("failed commit changed the history reference: %+v", got)
	}
}

func TestHistoryThumbnailCleanupFailureIsRecoverableAfterRestart(t *testing.T) {
	historyPath := filepath.Join(t.TempDir(), "history.json")
	store, _, err := OpenHistory(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordWithThumbs(HistoryEntry{Kind: "user", ID: "123"}, HistoryThumbnail{Role: "avatar", Ext: ".png", Data: []byte("avatar")}); err != nil {
		t.Fatal(err)
	}
	thumb := store.List()[0].AvatarThumb
	thumbPath := filepath.Join(store.ThumbsDir(), thumb)
	if err := os.Remove(thumbPath); err != nil {
		t.Fatal(err)
	}
	// A directory cannot be removed as a registered thumbnail file. This
	// produces the same retry path as a sharing or permission failure.
	if err := os.Mkdir(thumbPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.Clear(); err == nil {
		t.Fatal("thumbnail cleanup failure was hidden")
	}
	if len(store.List()) != 0 {
		t.Fatal("the successful metadata commit was rolled back after cleanup failed")
	}
	if err := os.Remove(thumbPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(thumbPath, []byte("avatar"), 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, _, err := OpenHistory(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Clear(); err != nil {
		t.Fatalf("persisted cleanup could not be retried: %v", err)
	}
	if _, err := os.Stat(thumbPath); !os.IsNotExist(err) {
		t.Fatalf("restart lost the pending thumbnail cleanup: %v", err)
	}
}

// Thumbnails fetched after an entry was recorded keep its place,
// replace its old image and are not written for an entry removed meanwhile.
func TestHistoryAttachThumbsKeepsPlaceAndSkipsRemovedEntries(t *testing.T) {
	store, _, err := OpenHistory(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordWithThumbs(HistoryEntry{Site: "bilibili", Kind: "video", ID: "BV1Attach"}, HistoryThumbnail{Role: "cover", Ext: ".png", Data: []byte("old cover")}); err != nil {
		t.Fatal(err)
	}
	old := store.List()[0].CoverThumb
	if err := store.Record(HistoryEntry{Site: "bilibili", Kind: "user", ID: "42"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AttachThumbs("bilibili", "video", "BV1Attach", HistoryThumbnail{Role: "cover", Ext: ".png", Data: []byte("new cover")}, HistoryThumbnail{Role: "avatar", Ext: ".png", Data: []byte("avatar")}); err != nil {
		t.Fatal(err)
	}
	items := store.List()
	if len(items) != 2 || items[0].ID != "42" || items[1].ID != "BV1Attach" {
		t.Fatalf("attaching thumbnails moved the entry: %+v", items)
	}
	if items[1].CoverThumb == "" || items[1].CoverThumb == old || items[1].AvatarThumb == "" {
		t.Fatalf("thumbnails were not attached: %+v", items[1])
	}
	if _, err := os.Stat(filepath.Join(store.ThumbsDir(), old)); !os.IsNotExist(err) {
		t.Fatalf("the replaced cover was kept: %v", err)
	}

	if err := store.Remove("user", "42"); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadDir(store.ThumbsDir())
	if err := store.AttachThumbs("bilibili", "user", "42", HistoryThumbnail{Role: "avatar", Ext: ".png", Data: []byte("late avatar")}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadDir(store.ThumbsDir())
	if len(after) != len(before) || len(store.List()) != 1 {
		t.Fatalf("a removed entry came back or got a file: %d -> %d files, %+v", len(before), len(after), store.List())
	}
}

func TestHistoryMultiSiteIsolationAndEmptySiteDefault(t *testing.T) {
	historyPath := filepath.Join(t.TempDir(), "history.json")
	store, _, err := OpenHistory(historyPath)
	if err != nil {
		t.Fatal(err)
	}

	// Empty site defaults to "bilibili" and stays isolated from "douyin" with identical kind+id+bytes.
	if err := store.RecordWithThumbs(
		HistoryEntry{Kind: "video", ID: "9001", Title: "Bilibili Title"},
		HistoryThumbnail{Role: "cover", Ext: ".jpg", Data: []byte("identical-bytes")},
	); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordWithThumbs(
		HistoryEntry{
			Site:         "douyin",
			Kind:         "video",
			ID:           "9001",
			Title:        "My Signature Dish",
			CanonicalURL: "https://www.douyin.com/video/9001?a_bogus=secret123",
		},
		HistoryThumbnail{Role: "cover", Ext: ".jpg", Data: []byte("identical-bytes")},
	); err != nil {
		t.Fatal(err)
	}

	items := store.List()
	if len(items) != 2 {
		t.Fatalf("expected 2 isolated multi-site entries, got %+v", items)
	}
	var dyItem, biliItem HistoryEntry
	for _, it := range items {
		switch it.Site {
		case "douyin":
			dyItem = it
		case "bilibili":
			biliItem = it
		}
	}
	if dyItem.CoverThumb == "" || biliItem.CoverThumb == "" || dyItem.CoverThumb == biliItem.CoverThumb {
		t.Fatalf("multi-site thumbnails collided or missing: dy=%q bili=%q", dyItem.CoverThumb, biliItem.CoverThumb)
	}
	if dyItem.CanonicalURL != "https://www.douyin.com/video/9001" {
		t.Fatalf("signed query not stripped on douyin canonical URL: %q", dyItem.CanonicalURL)
	}
	if dyItem.Title != "My Signature Dish" {
		t.Fatalf("benign title unexpectedly redacted: %q", dyItem.Title)
	}

	// Removing with legacy Remove("video", "9001") (or empty site) removes only bilibili:video:9001.
	if err := store.Remove("video", "9001"); err != nil {
		t.Fatal(err)
	}
	if len(store.List()) != 1 || store.List()[0].Site != "douyin" {
		t.Fatalf("removing bilibili entry affected douyin entry: %+v", store.List())
	}
	if _, err := os.Stat(filepath.Join(store.ThumbsDir(), biliItem.CoverThumb)); !os.IsNotExist(err) {
		t.Fatalf("bilibili thumbnail was not cleaned up: %v", err)
	}
	if _, err := store.ThumbPath(dyItem.CoverThumb); err != nil {
		t.Fatalf("douyin thumbnail was deleted when removing bilibili entry: %v", err)
	}

	// Removing douyin entry cleans up douyin thumbnail; history.json stays version 1.
	if err := store.RemoveBySite("douyin", "video", "9001"); err != nil {
		t.Fatal(err)
	}
	if len(store.List()) != 0 {
		t.Fatalf("douyin entry remained after RemoveBySite: %+v", store.List())
	}
	if _, err := os.Stat(filepath.Join(store.ThumbsDir(), dyItem.CoverThumb)); !os.IsNotExist(err) {
		t.Fatalf("douyin thumbnail was not cleaned up: %v", err)
	}

	v, exists, err := ReadSchemaVersion(historyPath, SchemaJSON)
	if err != nil || !exists || v != 1 {
		t.Fatalf("history.json version must stay 1, got v=%d exists=%v err=%v", v, exists, err)
	}

	// Douyin cookie/token markers in IDs are rejected and redacted in text.
	if err := store.Record(HistoryEntry{Site: "douyin", Kind: "user", ID: "sessionid=leak"}); err != nil {
		t.Fatal(err)
	}
	if len(store.List()) != 0 {
		t.Fatalf("credential marker ID should be rejected: %+v", store.List())
	}
}
