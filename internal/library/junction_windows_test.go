//go:build windows

package library

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/chialecode/trace/internal/storage"
)

// Windows junctions report ModeIrregular rather than ModeSymlink. A
// registered file reached through one must never be served or deleted (INV-08/09).
func TestJunctionInsideItemIsNeitherServedNorDeleted(t *testing.T) {
	tmp := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer db.Close()
	cacheDir := filepath.Join(tmp, "cache")
	store, err := OpenWithDataDir(db, cacheDir, tmp)
	if err != nil {
		t.Fatalf("OpenWithDataDir: %v", err)
	}

	outside := filepath.Join(tmp, "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(outside, "01.jpg")
	if err := os.WriteFile(victim, []byte("user image"), 0o600); err != nil {
		t.Fatal(err)
	}
	itemDir := filepath.Join(cacheDir, "bilibili", "1001", "dynamics", "900")
	if err := os.MkdirAll(itemDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("cmd", "/c", "mklink", "/J", filepath.Join(itemDir, "images"), outside)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot create junction: %v %s", err, out)
	}
	if info, err := os.Lstat(filepath.Join(itemDir, "images")); err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("fixture must be a junction reported without ModeSymlink: %v %v", info, err)
	}

	if _, err := store.SaveItem(itemDir, ItemMetadata{
		Version: 1, ID: "900", Site: "bilibili", Kind: "dynamic", Status: "complete",
		Author: ItemAuthor{Mid: 1001, Name: "UP"},
		Media:  []MediaFile{{Role: "image", Path: "images/01.jpg"}},
	}); err != nil {
		t.Fatalf("SaveItem: %v", err)
	}
	key := "bilibili:dynamic:900"
	if _, err := store.MediaPath(key, "images/01.jpg"); err == nil {
		t.Fatal("MediaPath served a file through a junction")
	}
	if _, err := ResolveItemPath(itemDir, "images/01.jpg"); err == nil {
		t.Fatal("ResolveItemPath accepted a path through a junction")
	}
	plan, err := store.PlanDelete(key)
	if err != nil {
		t.Fatalf("PlanDelete: %v", err)
	}
	for _, f := range plan.RegisteredFiles {
		if filepath.Base(filepath.Dir(f)) == "images" {
			t.Fatalf("delete plan reaches through junction: %s", f)
		}
	}
	if err := store.DeleteItem(key, true); err != nil {
		t.Fatalf("DeleteItem: %v", err)
	}
	if data, err := os.ReadFile(victim); err != nil || string(data) != "user image" {
		t.Fatalf("file behind junction was removed or changed: %q %v", data, err)
	}
}

func TestRebuildAndAddRootIgnoreWindowsJunction(t *testing.T) {
	tmp := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer db.Close()
	cacheDir := filepath.Join(tmp, "cache")
	store, err := OpenWithDataDir(db, cacheDir, tmp)
	if err != nil {
		t.Fatalf("OpenWithDataDir: %v", err)
	}
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(tmp, "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	// Put a non-TRACE item.json outside: if WalkDir traversed the junction,
	// RebuildIndex would rename it via BackupCorruptFile!
	outsideJSON := filepath.Join(outside, "item.json")
	if err := os.WriteFile(outsideJSON, []byte(`{"not":"trace"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	junc := filepath.Join(cacheDir, "linked_outside")
	cmd := exec.Command("cmd", "/c", "mklink", "/J", junc, outside)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot create junction: %v %s", err, out)
	}

	rep, err := store.RebuildIndex()
	if err != nil {
		t.Fatalf("RebuildIndex: %v", err)
	}
	if rep.Scanned != 0 || len(rep.CorruptBackedUp) != 0 {
		t.Fatalf("RebuildIndex traversed junction: %+v", rep)
	}
	if _, err := os.Stat(outsideJSON); err != nil {
		t.Fatalf("outside item.json was touched through junction: %v", err)
	}

	// AddLibraryRoot must also skip junctions inside the scanned root.
	addRep, err := store.AddLibraryRoot(cacheDir)
	if err != nil {
		t.Fatalf("AddLibraryRoot: %v", err)
	}
	if addRep.Scanned != 0 {
		t.Fatalf("AddLibraryRoot traversed junction: %+v", addRep)
	}
	// Direct junction as root must be rejected.
	if _, err := store.AddLibraryRoot(junc); err == nil {
		t.Fatal("AddLibraryRoot accepted a junction root")
	}

	// Swapping an itemDir to a junction within the 3s read-cache TTL must be
	// caught immediately by ResolveItemPath (no TTL cache on writes/deletes).
	swapDir := filepath.Join(cacheDir, "swap_item")
	if err := os.MkdirAll(swapDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if !safeRegisteredPathRead(swapDir, "a.mp4") {
		t.Fatal("expected real swapDir to pass read check")
	}
	if err := os.Remove(swapDir); err != nil {
		t.Fatal(err)
	}
	cmd2 := exec.Command("cmd", "/c", "mklink", "/J", swapDir, outside)
	cmd2.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	if out, err := cmd2.CombinedOutput(); err != nil {
		t.Skipf("cannot create second junction: %v %s", err, out)
	}
	if _, err := ResolveItemPath(swapDir, "a.mp4"); err == nil {
		t.Fatal("ResolveItemPath trusted cached dir after swap to junction")
	}
	if safeRegisteredPathRead(swapDir, "a.mp4") {
		t.Fatal("safeRegisteredPathRead cached itemDir itself after swap to junction")
	}
}
