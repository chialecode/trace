package library

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chialecode/trace/internal/storage"
)

// The media library's "刷新": files deleted in Explorer are reported once —
// a deleted folder leaves the index, a deleted file marks its item — and a
// second refresh with nothing new reports nothing.
func TestRefreshIndexReportsFilesDeletedOutsideTrace(t *testing.T) {
	tmp := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cacheDir := filepath.Join(tmp, "cache")
	store, err := OpenWithDataDir(db, cacheDir, tmp)
	if err != nil {
		t.Fatal(err)
	}
	save := func(id, title string, files ...string) string {
		dir := filepath.Join(cacheDir, "bilibili", "1_UP", id)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		var media []MediaFile
		for _, f := range files {
			if err := os.WriteFile(filepath.Join(dir, f), []byte("data-"+f), 0o600); err != nil {
				t.Fatal(err)
			}
			media = append(media, MediaFile{Role: "image", Path: f})
		}
		if _, err := store.SaveItem(dir, ItemMetadata{Version: 1, ID: "dynamic:" + id, Site: "bilibili", Kind: "dynamic", Title: title, PubTs: 1700000000, Author: ItemAuthor{Mid: 1, Name: "UP"}, Media: media}); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	save("100", "保留的动态", "a.jpg")
	gone := save("200", "整个删掉的动态", "b.jpg")
	partly := save("300", "删了一张图的动态", "c.jpg", "d.jpg")

	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(partly, "d.jpg")); err != nil {
		t.Fatal(err)
	}
	rep, err := store.RefreshIndex()
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Removed) != 1 || rep.Removed[0].Title != "整个删掉的动态" || rep.Removed[0].ItemDir != filepath.Clean(gone) {
		t.Fatalf("removed = %+v", rep.Removed)
	}
	if len(rep.NewlyMissing) != 1 || rep.NewlyMissing[0].Title != "删了一张图的动态" || len(rep.NewlyMissing[0].Files) != 1 || rep.NewlyMissing[0].Files[0] != "d.jpg" {
		t.Fatalf("newly missing = %+v", rep.NewlyMissing)
	}
	if _, ok, _ := store.GetItem(CanonicalItemKey("bilibili", "dynamic", "200")); ok {
		t.Fatal("the deleted folder's item is still indexed")
	}
	if meta, ok, _ := store.GetItem(CanonicalItemKey("bilibili", "dynamic", "300")); !ok || meta.Status != "missing_files" {
		t.Fatalf("partly deleted item: %+v %v", meta.Status, ok)
	}

	again, err := store.RefreshIndex()
	if err != nil || len(again.Removed) != 0 || len(again.NewlyMissing) != 0 {
		t.Fatalf("second refresh reported again: %+v %v", again, err)
	}
}
