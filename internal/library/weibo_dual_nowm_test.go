package library

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/storage"
)

func TestWeiboNoWatermarkCompanionLifecycleDeleteMergeArchiveRefresh(t *testing.T) {
	base := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(base, "library.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer db.Close()

	cacheDir := filepath.Join(base, "cache")
	store, err := OpenWithDataDir(db, cacheDir, base)
	if err != nil {
		t.Fatalf("OpenWithDataDir: %v", err)
	}

	itemDir := filepath.Join(cacheDir, "weibo", "1000000001", "posts", "5000000000009101")
	if err := os.MkdirAll(filepath.Join(itemDir, "images"), 0o700); err != nil {
		t.Fatal(err)
	}
	primaryBytes := []byte("synthetic-primary-original-jpeg-bytes-01")
	nowmBytes := []byte("synthetic-nowm-companion-jpeg-bytes-01")
	if err := os.WriteFile(filepath.Join(itemDir, "images", "01.jpg"), primaryBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(itemDir, "images", "01_nowm.jpg"), nowmBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	sumPrimary := sha256.Sum256(primaryBytes)
	sumNoWM := sha256.Sum256(nowmBytes)

	meta := ItemMetadata{
		Version:      1,
		ID:           "dynamic:5000000000009101",
		Site:         "weibo",
		Kind:         "dynamic",
		Status:       "complete",
		Title:        "微博双份原图与无水印生命周期测试",
		Text:         "微博双份原图与无水印生命周期测试",
		DynamicType:  "draw",
		TypeLabel:    "图文",
		URL:          "https://weibo.com/1000000001/O000002m1",
		PubTs:        1764000000,
		DirectCached: true,
		Author:       ItemAuthor{Site: "weibo", UID: "1000000001", Name: "合成博主"},
		Media: []MediaFile{
			{Role: "image", Path: "images/01.jpg", Bytes: int64(len(primaryBytes)), SHA256: hex.EncodeToString(sumPrimary[:]), Width: 1280, Height: 960},
			{Role: "image_nowm", Path: "images/01_nowm.jpg", Bytes: int64(len(nowmBytes)), SHA256: hex.EncodeToString(sumNoWM[:]), Width: 690, Height: 518},
		},
		NotePages: []NotePageRecord{
			{Index: 1, Kind: "image", MediaID: "pic01", Image: "images/01.jpg", NoWatermark: "images/01_nowm.jpg", Width: 1280, Height: 960},
		},
	}

	saved, err := store.SaveItem(itemDir, meta)
	if err != nil {
		t.Fatalf("SaveItem: %v", err)
	}

	// 1. RegisteredFiles & MediaPath include companion file.
	reg := RegisteredFiles(saved)
	foundPrimary, foundNoWM := false, false
	for _, f := range reg {
		if f.Path == "images/01.jpg" && f.Role == "image" {
			foundPrimary = true
		}
		if f.Path == "images/01_nowm.jpg" && f.Role == "image_nowm" {
			foundNoWM = true
		}
	}
	if !foundPrimary || !foundNoWM {
		t.Fatalf("RegisteredFiles missing image or image_nowm: %+v", reg)
	}
	if p, err := store.MediaPath(saved.ItemKey, "images/01_nowm.jpg"); err != nil || p == "" {
		t.Fatalf("MediaPath(image_nowm) = (%q, %v)", p, err)
	}

	// 2. ListItems(Kind="image") returns the item once (not duplicated).
	listed, err := store.ListItems(FeedFilter{Site: "weibo", Kind: "image", CachedOnly: true})
	if err != nil || len(listed) != 1 {
		t.Fatalf("ListItems(image) = (len=%d, err=%v), want 1", len(listed), err)
	}

	// 3. MergeItemMetadata supplements NoWatermark & KeepListedFiles prunes missing NoWatermark.
	baseMeta := saved
	baseMeta.NotePages = []NotePageRecord{{Index: 1, Kind: "image", MediaID: "pic01", Image: "images/01.jpg"}}
	baseMeta.Media = []MediaFile{saved.Media[0]}
	merged, _ := MergeItemMetadata(baseMeta, saved, "merge", func(rel string) bool { return rel == "images/01.jpg" })
	if len(merged.NotePages) != 1 || merged.NotePages[0].NoWatermark != "images/01_nowm.jpg" {
		t.Fatalf("MergeItemMetadata did not supplement NoWatermark: %+v", merged.NotePages)
	}
	pruned, _ := KeepListedFiles(merged, func(rel string) bool { return rel == "images/01.jpg" })
	if pruned.NotePages[0].NoWatermark != "" || len(pruned.Media) != 1 {
		t.Fatalf("KeepListedFiles did not prune omitted image_nowm: pages=%+v media=%+v", pruned.NotePages, pruned.Media)
	}

	// 4. ArchiveItemSnapshot preserves both primary and companion in archived V1 snapshot.
	archMeta, err := store.ArchiveItemSnapshot(saved.ItemKey, time.Unix(1764000100, 0).UTC())
	if err != nil {
		t.Fatalf("ArchiveItemSnapshot: %v", err)
	}
	if len(archMeta.NotePages) != 1 || archMeta.NotePages[0].NoWatermark != "images/01_nowm.jpg" {
		t.Fatalf("archived snapshot lost NoWatermark: %+v", archMeta.NotePages)
	}
	if _, err := os.Stat(filepath.Join(archMeta.ItemDir, "images", "01_nowm.jpg")); err != nil {
		t.Fatalf("archived snapshot missing companion file on disk: %v", err)
	}

	// 5. RefreshIndex detects missing companion when manually removed on disk, then Delete removes all.
	if err := os.Remove(filepath.Join(itemDir, "images", "01_nowm.jpg")); err != nil {
		t.Fatal(err)
	}
	report, err := store.RefreshIndex()
	if err != nil {
		t.Fatalf("RefreshIndex: %v", err)
	}
	if report.MissingFiles < 1 || len(report.NewlyMissing) != 1 || report.NewlyMissing[0].ItemKey != saved.ItemKey {
		t.Fatalf("RefreshIndex expected missing companion reported for %s, got %+v", saved.ItemKey, report)
	}

	// Restore companion and verify PlanDelete + Delete cleans up both primary and companion.
	if err := os.WriteFile(filepath.Join(itemDir, "images", "01_nowm.jpg"), nowmBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := store.PlanDelete(saved.ItemKey)
	if err != nil {
		t.Fatalf("PlanDelete: %v", err)
	}
	if len(plan.RegisteredFiles) < 2 {
		t.Fatalf("PlanDelete RegisteredFiles = %+v, want >= 2 (primary + companion)", plan.RegisteredFiles)
	}
	res, err := store.Delete(saved.ItemKey)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(res.PreservedUserFiles) != 0 {
		t.Fatalf("Delete unexpectedly preserved user files (companion leaked?): %+v", res.PreservedUserFiles)
	}
	if _, err := os.Stat(filepath.Join(itemDir, "images", "01_nowm.jpg")); !os.IsNotExist(err) {
		t.Fatalf("Delete left 01_nowm.jpg behind: %v", err)
	}
	// Archived V1 snapshot stays intact after deleting V2.
	if _, err := os.Stat(filepath.Join(archMeta.ItemDir, "images", "01_nowm.jpg")); err != nil {
		t.Fatalf("deleting V2 affected archived V1 companion: %v", err)
	}
}
