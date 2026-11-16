package library

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chialecode/trace/internal/storage"
)

// Deleting a creator's last item removes the emptied kind and author folders
// but keeps the site folder, the root, and any folder that still has files
// (2026-11-15 user feedback).
func TestDeletingTheLastItemRemovesTheEmptyAuthorFolder(t *testing.T) {
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
	save := func(author, kind, id string, extra string) string {
		t.Helper()
		dir := filepath.Join(cacheDir, "bilibili", author, kind+"s", id)
		if err := os.MkdirAll(filepath.Join(dir, "images"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "images", "01.jpg"), []byte("jpg"), 0o600); err != nil {
			t.Fatal(err)
		}
		if extra != "" {
			if err := os.WriteFile(filepath.Join(dir, extra), []byte("mine"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := store.SaveItem(dir, ItemMetadata{
			Version: 1, ID: id, Site: "bilibili", Kind: kind, Status: "complete",
			Author: ItemAuthor{Mid: 1, Name: "UP"},
			Media:  []MediaFile{{Role: "image", Path: "images/01.jpg"}},
		}); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	save("1001", "dynamic", "d1", "")
	save("1001", "dynamic", "d2", "")
	save("1002", "dynamic", "d3", "notes.txt")

	authorDir := filepath.Join(cacheDir, "bilibili", "1001")
	if _, err := store.Delete("bilibili:dynamic:d1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(authorDir, "dynamics", "d2")); err != nil {
		t.Fatalf("another item of the same creator was touched: %v", err)
	}
	if _, err := store.Delete("bilibili:dynamic:d2"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(authorDir); !os.IsNotExist(err) {
		t.Fatalf("empty author folder should be gone, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "bilibili")); err != nil {
		t.Fatalf("site folder must stay: %v", err)
	}

	// A user's own file keeps the item, kind and author folders.
	if _, err := store.Delete("bilibili:dynamic:d3"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "bilibili", "1002", "dynamics", "d3", "notes.txt")); err != nil {
		t.Fatalf("user file and its folders must stay: %v", err)
	}
}
