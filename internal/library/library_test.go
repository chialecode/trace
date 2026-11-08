package library

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/storage"
)

// C16: Filename sanitization and path traversal containment.
func TestSanitizeFileNameAndEnsureWithinRoot(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`A<b>c:d"e/f\g|h?i*j`, `A_b_c_d_e_f_g_h_i_j`},
		{"trailing.dots.and.spaces. . .", "trailing.dots.and.spaces"},
		{"CON", "_CON"},
		{"prn.mp4", "_prn.mp4"},
		{"aux", "_aux"},
		{"LPT1.txt", "_LPT1.txt"},
		{strings.Repeat("汉", 100), strings.Repeat("汉", MaxFileNameRunes)},
	}
	for _, tc := range cases {
		got := SanitizeFileName(tc.in)
		if got != tc.want {
			t.Fatalf("SanitizeFileName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	root := filepath.Join(t.TempDir(), "cache-root")
	inside := filepath.Join(root, "bilibili", "1001_UP", "video_BV17x411w7KC")
	if _, err := EnsureWithinRoot(root, inside); err != nil {
		t.Fatalf("EnsureWithinRoot valid path failed: %v", err)
	}
	outside := filepath.Join(root, "..", "escaped")
	if _, err := EnsureWithinRoot(root, outside); err == nil {
		t.Fatalf("expected EnsureWithinRoot to reject escaping path %q", outside)
	}
}

// C24, C25, C26, C28: Index rebuild from item.json after DB deletion, missing_files detection,
// corrupt item.json backup, cache dir migration, and safe item deletion preserving user files.
func TestLibraryIndexRebuildMissingFilesMigrateAndDelete(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "library.db")
	cacheDir := filepath.Join(tmp, "cache1")

	db, _, _, err := storage.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer db.Close()

	store, err := OpenWithDataDir(db, cacheDir, tmp)
	if err != nil {
		t.Fatalf("library.Open: %v", err)
	}

	// Create a valid video item on disk.
	vidDir := filepath.Join(cacheDir, "bilibili", "12345_TestUP", "video_BV17x411w7KC")
	if err := os.MkdirAll(vidDir, 0o700); err != nil {
		t.Fatalf("mkdir vidDir: %v", err)
	}
	mp4Path := filepath.Join(vidDir, "P01_Part1.mp4")
	if err := os.WriteFile(mp4Path, []byte("synthetic-mp4-bytes"), 0o600); err != nil {
		t.Fatalf("write mp4: %v", err)
	}

	vidMeta := ItemMetadata{
		Version: 1,
		ID:      "video:BV17x411w7KC",
		Site:    "bilibili",
		Kind:    "video",
		Status:  "complete",
		Title:   "Cached Video 1",
		Desc:    "Video description",
		URL:     "https://www.bilibili.com/video/BV17x411w7KC",
		PubTs:   1700000100,
		Author:  ItemAuthor{Mid: 12345, Name: "TestUP"},
		Media: []MediaFile{
			{Role: "video", Path: "P01_Part1.mp4"},
		},
		Parts: []VideoPartRecord{
			{
				CID:   9001,
				Page:  1,
				Title: "Part1",
				File:  "P01_Part1.mp4",
				Video: PartVideoInfo{Quality: 64, QualityName: "720P 高清", CodecID: 7, Codecs: "avc1"},
				Audio: PartAudioInfo{Quality: 30280, Name: "192K 高音质", SavedAs: "P01_Part1.mp4"},
			},
		},
	}
	if _, err := store.SaveItem(vidDir, vidMeta); err != nil {
		t.Fatalf("SaveItem video: %v", err)
	}

	// Also insert an online-only discovered feed item to verify unified feed order and deduplication (C24).
	if err := store.UpsertFeedEntry(FeedEntry{
		Site:         "bilibili",
		Kind:         "dynamic",
		RawID:        "999888777",
		Mid:          12345,
		AuthorName:   "TestUP",
		Title:        "Online Dynamic",
		Summary:      "Dynamic summary",
		CanonicalURL: "https://t.bilibili.com/999888777",
		PubTs:        1700000200,
	}); err != nil {
		t.Fatalf("UpsertFeedEntry: %v", err)
	}

	feed, err := store.QueryFeed(FeedFilter{Kind: "all"})
	if err != nil || len(feed) != 2 {
		t.Fatalf("QueryFeed all = %+v, err=%v", feed, err)
	}
	if feed[0].RawID != "999888777" || feed[0].Cached {
		t.Fatalf("expected newest item to be uncached dynamic 999888777, got %+v", feed[0])
	}
	if feed[1].RawID != "BV17x411w7KC" || !feed[1].Cached || feed[1].CacheStatus != "complete" {
		t.Fatalf("expected second item to be cached video BV17x411w7KC, got %+v", feed[1])
	}

	// Create a corrupt item.json in another folder to verify C28 backup during RebuildIndex.
	corruptDir := filepath.Join(cacheDir, "bilibili", "12345_TestUP", "dynamic_bad")
	_ = os.MkdirAll(corruptDir, 0o700)
	_ = os.WriteFile(filepath.Join(corruptDir, "item.json"), []byte("{corrupt"), 0o600)

	// Delete library.db and reopen a fresh DB, then RebuildIndex from item.json files (C25).
	_ = db.Close()
	_ = os.Remove(dbPath)
	db2, _, _, err := storage.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("reopen SQLite: %v", err)
	}
	defer db2.Close()

	store2, err := OpenWithDataDir(db2, cacheDir, tmp)
	if err != nil {
		t.Fatalf("reopen library store: %v", err)
	}
	rep, err := store2.RebuildIndex()
	if err != nil {
		t.Fatalf("RebuildIndex: %v", err)
	}
	if rep.Indexed != 1 || len(rep.CorruptBackedUp) != 1 {
		t.Fatalf("unexpected RebuildReport: %+v", rep)
	}

	// Remove P01_Part1.mp4 on disk and rebuild -> status must become missing_files (C24).
	_ = os.Remove(mp4Path)
	repMissing, err := store2.RebuildIndex()
	if err != nil {
		t.Fatalf("RebuildIndex after file removal: %v", err)
	}
	if repMissing.MissingFiles != 1 {
		t.Fatalf("expected MissingFiles=1, got %+v", repMissing)
	}
	gotItem, ok, err := store2.GetItem("BV17x411w7KC")
	if err != nil || !ok || gotItem.Status != "missing_files" {
		t.Fatalf("expected status=missing_files, got %+v ok=%v err=%v", gotItem, ok, err)
	}

	// Restore file and migrate cacheDir to cache2 (C26).
	_ = os.WriteFile(mp4Path, []byte("synthetic-mp4-bytes"), 0o600)
	cacheDir2 := filepath.Join(tmp, "cache2")
	repMig, err := store2.MigrateCacheDir(cacheDir2, true)
	if err != nil || repMig.Indexed != 1 {
		t.Fatalf("MigrateCacheDir = %+v, err=%v", repMig, err)
	}
	migratedItem, ok, _ := store2.GetItem("BV17x411w7KC")
	if !ok || migratedItem.Status != "complete" || !strings.HasPrefix(migratedItem.ItemDir, cacheDir2) {
		t.Fatalf("unexpected migrated item: %+v", migratedItem)
	}

	// Delete index only (deleteFiles=false) -> item.json remains on disk and can be rebuilt.
	if err := store2.DeleteItem("bilibili:video:BV17x411w7KC", false); err != nil {
		t.Fatalf("DeleteItem index-only: %v", err)
	}
	if _, ok, _ := store2.GetItem("BV17x411w7KC"); ok {
		t.Fatalf("expected item removed from index")
	}
	if _, err := store2.RebuildIndex(); err != nil {
		t.Fatalf("RebuildIndex after index-only delete: %v", err)
	}
	if _, ok, _ := store2.GetItem("BV17x411w7KC"); !ok {
		t.Fatalf("expected item restored after RebuildIndex")
	}

	// Delete with files (deleteFiles=true) when no user files exist -> directory is deleted from disk.
	if err := store2.DeleteItem("bilibili:video:BV17x411w7KC", true); err != nil {
		t.Fatalf("DeleteItem with files: %v", err)
	}
	if _, err := os.Stat(migratedItem.ItemDir); !os.IsNotExist(err) {
		t.Fatalf("expected empty itemDir removed from disk, stat err=%v", err)
	}
}

// R-01 (P0) & C26: PlanDelete and Delete preserve user-owned files, protect shared videos, and reject active recordings.
func TestPlanDeleteAndDeletePreservesUserFiles(t *testing.T) {
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

	// 1. Create a video item with a user-owned file in the same directory.
	vidDir := filepath.Join(cacheDir, "bilibili", "1001", "videos", "BV1userfile")
	_ = os.MkdirAll(vidDir, 0o700)
	regMP4 := filepath.Join(vidDir, "P01.mp4")
	userFile := filepath.Join(vidDir, "user-owned.txt")
	_ = os.WriteFile(regMP4, []byte("mp4-content"), 0o600)
	_ = os.WriteFile(userFile, []byte("keep-my-notes"), 0o600)

	_, err = store.SaveItem(vidDir, ItemMetadata{
		Version:      1,
		ID:           "BV1userfile",
		Site:         "bilibili",
		Kind:         "video",
		Status:       "complete",
		Title:        "Video With User File",
		PubTs:        1700000100,
		DirectCached: false,
		Author:       ItemAuthor{Mid: 1001, Name: "UP"},
		Media:        []MediaFile{{Role: "video", Path: "P01.mp4"}},
		Parts:        []VideoPartRecord{{CID: 1, Page: 1, File: "P01.mp4"}},
	})
	if err != nil {
		t.Fatalf("SaveItem: %v", err)
	}

	// 2. Create two dynamics referencing BV1userfile.
	for _, dynID := range []string{"dyn1", "dyn2"} {
		dynDir := filepath.Join(cacheDir, "bilibili", "1001", "dynamics", dynID)
		_ = os.MkdirAll(dynDir, 0o700)
		_, err = store.SaveItem(dynDir, ItemMetadata{
			Version: 1,
			ID:      dynID,
			Site:    "bilibili",
			Kind:    "dynamic",
			Status:  "complete",
			Title:   "Dyn " + dynID,
			PubTs:   1700000200,
			Author:  ItemAuthor{Mid: 1001, Name: "UP"},
			Videos:  []string{"BV1userfile"},
		})
		if err != nil {
			t.Fatalf("SaveItem %s: %v", dynID, err)
		}
	}

	// Deleting dyn1 must preserve shared video BV1userfile because dyn2 still references it.
	plan1, err := store.PlanDelete("bilibili:dynamic:dyn1")
	if err != nil {
		t.Fatalf("PlanDelete dyn1: %v", err)
	}
	if len(plan1.PreservedSharedVideos) != 1 || plan1.PreservedSharedVideos[0] != "bilibili:video:BV1userfile" {
		t.Fatalf("expected BV1userfile in PreservedSharedVideos, got %+v", plan1)
	}
	if _, err := store.Delete("bilibili:dynamic:dyn1"); err != nil {
		t.Fatalf("Delete dyn1: %v", err)
	}
	if _, ok, _ := store.GetItem("BV1userfile"); !ok {
		t.Fatalf("shared video BV1userfile should still exist after deleting dyn1")
	}

	// Deleting dyn2 now also deletes BV1userfile, while preserving user-owned.txt!
	plan2, err := store.PlanDelete("bilibili:dynamic:dyn2")
	if err != nil {
		t.Fatalf("PlanDelete dyn2: %v", err)
	}
	if len(plan2.PreservedUserFiles) != 1 || plan2.PreservedUserFiles[0] != "user-owned.txt" {
		t.Fatalf("expected user-owned.txt in PreservedUserFiles, got %+v", plan2)
	}
	delRes2, err := store.Delete("bilibili:dynamic:dyn2")
	if err != nil {
		t.Fatalf("Delete dyn2: %v", err)
	}
	if len(delRes2.PreservedUserFiles) != 1 || delRes2.PreservedUserFiles[0] != "user-owned.txt" {
		t.Fatalf("Delete result PreservedUserFiles = %v, want [user-owned.txt]", delRes2.PreservedUserFiles)
	}
	if _, err := os.Stat(regMP4); !os.IsNotExist(err) {
		t.Fatalf("expected registered P01.mp4 to be deleted, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(vidDir, "item.json")); !os.IsNotExist(err) {
		t.Fatalf("expected item.json to be deleted, stat err=%v", err)
	}
	if data, err := os.ReadFile(userFile); err != nil || string(data) != "keep-my-notes" {
		t.Fatalf("CRITICAL R-01: user-owned.txt was not preserved! err=%v data=%q", err, string(data))
	}

	// 3. Active recording item cannot be deleted.
	recDir := filepath.Join(cacheDir, "bilibili", "1001", "lives", "9988-1700000300")
	_ = os.MkdirAll(recDir, 0o700)
	_, _ = store.SaveItem(recDir, ItemMetadata{
		Version: 1,
		ID:      "9988:1700000300",
		Site:    "bilibili",
		Kind:    "live",
		Status:  "recording",
		Title:   "Active Live",
		PubTs:   1700000300,
		Author:  ItemAuthor{Mid: 1001, Name: "UP"},
	})
	if _, err := store.PlanDelete("bilibili:live:9988:1700000300"); err == nil {
		t.Fatalf("expected PlanDelete to reject active recording item")
	}
	if err := store.DeleteItem("bilibili:live:9988:1700000300", true); err == nil {
		t.Fatalf("expected DeleteItem to reject active recording item")
	}
}

// INV-08: MediaPath whitelist and traversal/symlink rejection.
func TestMediaPathWhitelistAndSymlinkRejection(t *testing.T) {
	tmp := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer db.Close()

	cacheDir := filepath.Join(tmp, "cache")
	store, _ := OpenWithDataDir(db, cacheDir, tmp)

	vidDir := filepath.Join(cacheDir, "bilibili", "1001", "videos", "BV1media")
	_ = os.MkdirAll(vidDir, 0o700)
	mp4File := filepath.Join(vidDir, "P01.mp4")
	unregFile := filepath.Join(vidDir, "secret.txt")
	_ = os.WriteFile(mp4File, []byte("mp4"), 0o600)
	_ = os.WriteFile(unregFile, []byte("secret"), 0o600)

	_, _ = store.SaveItem(vidDir, ItemMetadata{
		Version: 1,
		ID:      "BV1media",
		Site:    "bilibili",
		Kind:    "video",
		Status:  "complete",
		Title:   "Media Test",
		PubTs:   1700000100,
		Author:  ItemAuthor{Mid: 1001, Name: "UP"},
		Media:   []MediaFile{{Role: "video", Path: "P01.mp4"}},
		Parts:   []VideoPartRecord{{CID: 1, Page: 1, File: "P01.mp4"}},
	})

	// 1. Registered file succeeds.
	gotPath, err := store.MediaPath("bilibili:video:BV1media", "P01.mp4")
	if err != nil || gotPath != mp4File {
		t.Fatalf("MediaPath registered = %q, err=%v (want %q)", gotPath, err, mp4File)
	}

	// 2. Unregistered file in same folder is rejected.
	if _, err := store.MediaPath("bilibili:video:BV1media", "secret.txt"); err == nil {
		t.Fatalf("expected MediaPath to reject unregistered file secret.txt")
	}

	// 3. Traversal, backslash, and colon are rejected.
	for _, bad := range []string{"../P01.mp4", `images\01.jpg`, "C:/P01.mp4", "item.json"} {
		if _, err := store.MediaPath("bilibili:video:BV1media", bad); err == nil {
			t.Fatalf("expected MediaPath to reject %q", bad)
		}
	}
}

// C19: State dot inference (green, yellow, red, ring, none).
func TestStateDotInference(t *testing.T) {
	tmp := t.TempDir()
	db, _, _, _ := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	defer db.Close()
	store, _ := OpenWithDataDir(db, filepath.Join(tmp, "cache"), tmp)

	vDir := filepath.Join(tmp, "cache", "v1")
	_ = os.MkdirAll(vDir, 0o700)
	_ = os.WriteFile(filepath.Join(vDir, "P01.mp4"), []byte("mp4"), 0o600)
	_, _ = store.SaveItem(vDir, ItemMetadata{
		Version: 1, ID: "BV1green", Site: "bilibili", Kind: "video", Status: "complete",
		Media: []MediaFile{{Role: "video", Path: "P01.mp4"}},
	})

	vUpDir := filepath.Join(tmp, "cache", "v2")
	_ = os.MkdirAll(vUpDir, 0o700)
	_ = os.WriteFile(filepath.Join(vUpDir, "P01.mp4"), []byte("mp4"), 0o600)
	_, _ = store.SaveItem(vUpDir, ItemMetadata{
		Version: 1, ID: "BV1yellow", Site: "bilibili", Kind: "video", Status: "complete",
		UpgradeAvailable: true, UpgradeHint: "可升级 4K",
		Media: []MediaFile{{Role: "video", Path: "P01.mp4"}},
	})

	states := store.States([]string{"bilibili:video:BV1green", "bilibili:video:BV1yellow", "bilibili:video:BV1none"})
	if states["bilibili:video:BV1green"].Dot != "green" {
		t.Fatalf("expected green dot, got %+v", states["bilibili:video:BV1green"])
	}
	if states["bilibili:video:BV1yellow"].Dot != "yellow" {
		t.Fatalf("expected yellow dot, got %+v", states["bilibili:video:BV1yellow"])
	}
	if states["bilibili:video:BV1none"].Dot != "none" {
		t.Fatalf("expected none dot, got %+v", states["bilibili:video:BV1none"])
	}
}

// C24: Bidirectional keyset pagination (pub_ts, key) and NewerCount.
func TestBidirectionalKeysetFeedAndAnchor(t *testing.T) {
	tmp := t.TempDir()
	db, _, _, _ := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	defer db.Close()
	store, _ := OpenWithDataDir(db, filepath.Join(tmp, "cache"), tmp)

	for i := 1; i <= 5; i++ {
		_ = store.UpsertFeedEntry(FeedEntry{
			Site:       "bilibili",
			Kind:       "dynamic",
			RawID:      fmt.Sprintf("dyn_%d", i),
			Mid:        int64(100 + (i % 2)),
			AuthorName: "UP",
			Title:      fmt.Sprintf("Post %d", i),
			PubTs:      int64(1700000000 + i*10),
			Cached:     true,
		})
	}

	// Page 1 newest (limit 2): should return dyn_5, dyn_4.
	p1, err := store.FeedPage(FeedPageRequest{Order: "newest", Direction: "older", Limit: 2})
	if err != nil || len(p1.Items) != 2 || !p1.HasMore {
		t.Fatalf("FeedPage p1 = %+v, err=%v", p1, err)
	}
	if p1.Items[0].RawID != "dyn_5" || p1.Items[1].RawID != "dyn_4" {
		t.Fatalf("unexpected p1 items: %+v", p1.Items)
	}

	// Page 2 older from p1.NextCursor: should return dyn_3, dyn_2.
	p2, err := store.FeedPage(FeedPageRequest{Cursor: p1.NextCursor, Order: "newest", Direction: "older", Limit: 2})
	if err != nil || len(p2.Items) != 2 {
		t.Fatalf("FeedPage p2 = %+v, err=%v", p2, err)
	}
	if p2.Items[0].RawID != "dyn_3" || p2.Items[1].RawID != "dyn_2" {
		t.Fatalf("unexpected p2 items: %+v", p2.Items)
	}

	// NewerCount from dyn_3 (pubTs=1700000030): should be 2 (dyn_4, dyn_5).
	newer, err := store.NewerCount(p2.Items[0].PubTs, p2.Items[0].FeedKey)
	if err != nil || newer != 2 {
		t.Fatalf("NewerCount = %d, err=%v (want 2)", newer, err)
	}

	// Bidirectional newer page from dyn_3 cursor: should return dyn_5, dyn_4 in newest-first order with no duplicates or omissions (C24, R10).
	pNewer, err := store.FeedPage(FeedPageRequest{Cursor: &FeedCursor{PubTs: p2.Items[0].PubTs, Key: p2.Items[0].FeedKey}, Order: "newest", Direction: "newer", Limit: 5})
	if err != nil || len(pNewer.Items) != 2 || pNewer.Items[0].RawID != "dyn_5" || pNewer.Items[1].RawID != "dyn_4" {
		t.Fatalf("FeedPage newer from dyn_3 = %+v, err=%v", pNewer, err)
	}

	// Around cursor when anchor item still exists vs when anchor item was deleted (R10).
	pAround, err := store.FeedPage(FeedPageRequest{Cursor: &FeedCursor{PubTs: p2.Items[0].PubTs, Key: p2.Items[0].FeedKey}, Order: "newest", Direction: "around", Limit: 10})
	if err != nil || len(pAround.Items) != 5 {
		t.Fatalf("FeedPage around = %+v, err=%v", pAround, err)
	}
}

func TestFeedAroundTimestampChoosesNearestOlderItemAcrossTies(t *testing.T) {
	root := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(root, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := OpenWithDataDir(db, filepath.Join(root, "cache"), root)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 85; i++ {
		id := fmt.Sprintf("tie_%03d", i)
		if _, err := store.SaveItem(filepath.Join(store.CacheDir(), "dynamic", id), ItemMetadata{Site: "bilibili", Kind: "dynamic", ID: id, PubTs: 100, Author: ItemAuthor{Mid: 123}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []string{"", "bilibili:dynamic:deleted"} {
		page, err := store.FeedPage(FeedPageRequest{Order: "newest", Direction: "around", Cursor: &FeedCursor{PubTs: 100, Key: key}, CachedOnly: true})
		if err != nil || len(page.Items) == 0 || page.Items[0].RawID != "tie_084" {
			firstID := ""
			if len(page.Items) > 0 {
				firstID = page.Items[0].RawID
			}
			t.Fatalf("timestamp fallback did not include the nearest item at or before its timestamp: cursor=%q first=%q count=%d err=%v", key, firstID, len(page.Items), err)
		}
	}
	// Continue both edges of a restored middle page. More than one page shares
	// this timestamp, so cursor mistakes produce duplicates or missing items.
	around, err := store.FeedPage(FeedPageRequest{Order: "newest", Direction: "around", Cursor: &FeedCursor{PubTs: 100, Key: "bilibili:dynamic:tie_042"}, CachedOnly: true, Authors: []int64{123}})
	if err != nil || len(around.Items) == 0 {
		t.Fatalf("cannot restore a middle page: %v", err)
	}
	seen := make(map[string]bool)
	collect := func(page FeedPageResult) {
		t.Helper()
		for i, entry := range page.Items {
			if seen[entry.FeedKey] || (i > 0 && page.Items[i-1].FeedKey <= entry.FeedKey) {
				t.Fatal("keyset paging repeated an item or broke descending tie order")
			}
			seen[entry.FeedKey] = true
		}
		if len(page.Items) > 0 && (page.PrevCursor.Key != page.Items[0].FeedKey || page.NextCursor.Key != page.Items[len(page.Items)-1].FeedKey) {
			t.Fatal("page cursors do not identify the actual combined edges")
		}
	}
	collect(around)
	for _, direction := range []string{"newer", "older"} {
		cursor := around.NextCursor
		if direction == "newer" {
			cursor = around.PrevCursor
		}
		for i := 0; i < 20; i++ {
			page, err := store.FeedPage(FeedPageRequest{Order: "newest", Direction: direction, Cursor: cursor, Limit: 7, CachedOnly: true, Authors: []int64{123}})
			if err != nil {
				t.Fatal(err)
			}
			collect(page)
			if !page.HasMore {
				break
			}
			cursor = page.NextCursor
			if direction == "newer" {
				cursor = page.PrevCursor
			}
		}
	}
	if len(seen) != 85 {
		t.Fatalf("restored bidirectional paging omitted items: got %d of 85", len(seen))
	}
}

// C25: Filter by type, subscription authors, and creator mid.
func TestLibraryFilterByTypeSubscriptionAndCreator(t *testing.T) {
	tmp := t.TempDir()
	db, _, _, _ := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	defer db.Close()
	cacheDir := filepath.Join(tmp, "cache")
	store, _ := OpenWithDataDir(db, cacheDir, tmp)

	for i, mid := range []int64{101, 202} {
		dir := filepath.Join(cacheDir, fmt.Sprintf("v_%d", i))
		_ = os.MkdirAll(dir, 0o700)
		_ = os.WriteFile(filepath.Join(dir, "P01.mp4"), []byte("mp4"), 0o600)
		_, _ = store.SaveItem(dir, ItemMetadata{
			Version: 1, ID: fmt.Sprintf("BV1_%d", i), Site: "bilibili", Kind: "video",
			Title: fmt.Sprintf("Video %d", i), PubTs: int64(1700000000 + i),
			Author: ItemAuthor{Mid: mid, Name: fmt.Sprintf("UP_%d", mid)},
			Media:  []MediaFile{{Role: "video", Path: "P01.mp4"}},
		})
	}

	items, err := store.ListItems(FeedFilter{Kind: "video", Authors: []int64{202}})
	if err != nil || len(items) != 1 || items[0].Author.Mid != 202 {
		t.Fatalf("ListItems filtered by Authors=[202] = %+v, err=%v", items, err)
	}
}

// C27: AddLibraryRoot recognizes legacy trace-data folder with library/ and NO settings.json, skipping duplicates.
func TestAddLibraryRootLegacyWithoutSettingsJSON(t *testing.T) {
	tmp := t.TempDir()
	db, _, _, _ := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	defer db.Close()
	store, _ := OpenWithDataDir(db, filepath.Join(tmp, "cache"), tmp)

	// Create a synthetic legacy trace-data directory containing library/bilibili/.../item.json and NO settings.json.
	legacyTraceData := filepath.Join(tmp, "legacy-trace-data")
	legacyItemDir := filepath.Join(legacyTraceData, "library", "bilibili", "94697", "dynamics", "10001")
	_ = os.MkdirAll(filepath.Join(legacyItemDir, "images"), 0o700)
	_ = os.WriteFile(filepath.Join(legacyItemDir, "images", "01.jpg"), []byte("jpg-bytes"), 0o600)
	legacyManifest := `{
  "version": 1,
  "site": "bilibili",
  "kind": "dynamic",
  "id": "10001",
  "author": {"mid": "94697", "name": "LegacyUP", "face": "https://i0.hdslb.com/face.jpg"},
  "pubTs": 1700000500,
  "cachedAt": "2026-11-05T10:00:00Z",
  "dynamicType": "DYNAMIC_TYPE_DRAW",
  "typeLabel": "图文动态",
  "url": "https://t.bilibili.com/10001",
  "status": "complete",
  "media": [{"role": "image", "path": "images/01.jpg", "bytes": 9}]
}`
	_ = os.WriteFile(filepath.Join(legacyItemDir, "item.json"), []byte(legacyManifest), 0o600)

	rep1, err := store.AddLibraryRoot(legacyTraceData)
	if err != nil {
		t.Fatalf("AddLibraryRoot first run: %v", err)
	}
	if rep1.Added != 1 || rep1.Skipped != 0 {
		t.Fatalf("expected Added=1 Skipped=0, got %+v", rep1)
	}

	// Second run on the same root must skip the existing key and count Skipped=1.
	rep2, err := store.AddLibraryRoot(legacyTraceData)
	if err != nil {
		t.Fatalf("AddLibraryRoot second run: %v", err)
	}
	if rep2.Added != 0 || rep2.Skipped != 1 {
		t.Fatalf("expected Added=0 Skipped=1 on duplicate import, got %+v", rep2)
	}
}

func TestRebuildIndexFromItemJSONManifests(t *testing.T) {
	TestLibraryIndexRebuildMissingFilesMigrateAndDelete(t)
}

// C20 & C27: Quality upgrade detection and 24-hour cooldown.
func TestCheckQualityUpgradeDetection(t *testing.T) {
	tmp := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer db.Close()

	cacheDir := filepath.Join(tmp, "cache")
	store, err := OpenWithDataDir(db, cacheDir, tmp)
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}

	vidDir := filepath.Join(cacheDir, "bilibili", "12345_TestUP", "video_BV17x411w7KC")
	_ = os.MkdirAll(vidDir, 0o700)
	_ = os.WriteFile(filepath.Join(vidDir, "P01_Part1.mp4"), []byte("mp4"), 0o600)

	_, err = store.SaveItem(vidDir, ItemMetadata{
		Version: 1,
		ID:      "video:BV17x411w7KC",
		Site:    "bilibili",
		Kind:    "video",
		Status:  "complete",
		Title:   "Low Quality Cached Video",
		PubTs:   1700000100,
		Author:  ItemAuthor{Mid: 12345, Name: "TestUP"},
		Media:   []MediaFile{{Role: "video", Path: "P01_Part1.mp4"}},
		Parts: []VideoPartRecord{
			{
				CID:   9001,
				Page:  1,
				Title: "Part1",
				File:  "P01_Part1.mp4",
				Video: PartVideoInfo{Quality: 64, QualityName: "720P 高清", CodecID: 7, Codecs: "avc1"},
				Audio: PartAudioInfo{Quality: 30280, Name: "192K 高音质", SavedAs: "P01_Part1.mp4"},
			},
		},
	})
	if err != nil {
		t.Fatalf("SaveItem: %v", err)
	}

	var playurlCalls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/x/player/wbi/playurl", func(w http.ResponseWriter, r *http.Request) {
		playurlCalls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"quality":        120,
				"accept_quality": []int{120, 80, 64},
				"dash": map[string]any{
					"duration": 120,
					"video": []map[string]any{
						{
							"id":        120,
							"baseUrl":   "https://upos-sz-mirrorcos.bilivideo.com/v120.m4s",
							"codecid":   7,
							"codecs":    "avc1",
							"width":     3840,
							"height":    2160,
							"bandwidth": 8000000,
						},
					},
					"audio": []map[string]any{
						{
							"id":        30280,
							"baseUrl":   "https://upos-sz-mirrorcos.bilivideo.com/a30280.m4s",
							"bandwidth": 192000,
						},
					},
					"flac": map[string]any{
						"display": true,
						"audio": map[string]any{
							"id":        30251,
							"baseUrl":   "https://upos-sz-mirrorcos.bilivideo.com/a30251.flac",
							"bandwidth": 900000,
						},
					},
				},
			},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	credStore, _, _ := credential.Open(filepath.Join(tmp, "cred.bin"))
	_, _ = credStore.SaveManual("SESSDATA=s; bili_jct=c", "rt")
	client := bilibili.NewClient(bilibili.ClientOptions{
		CredStore:      credStore,
		Endpoints:      bilibili.Endpoints{APIBase: srv.URL},
		PacingInterval: -1,
		AllowLocalhost: true,
	})
	client.SetWBIKeys("7cd084941338484aae1ad9425b84077c", "4932caff0ff746eab6f01bf08b70ac45")

	upgradable, err := store.CheckUpgradeKeys(context.Background(), client, nil, false, bilibili.PlaySelectionOptions{
		PreferredCodec:      "avc",
		PreferLosslessAudio: true,
	})
	if err != nil {
		t.Fatalf("CheckUpgradeKeys: %v", err)
	}
	if len(upgradable) != 1 || !upgradable[0].UpgradeAvailable || !strings.Contains(upgradable[0].UpgradeHint, "4K 超清") {
		t.Fatalf("expected 1 upgradable item with 4K hint, got %+v", upgradable)
	}
	if playurlCalls.Load() != 1 {
		t.Fatalf("expected 1 playurl call, got %d", playurlCalls.Load())
	}

	// Second call within 24h cooldown (force=false) must make 0 additional playurl requests!
	upgradable2, err := store.CheckUpgradeKeys(context.Background(), client, nil, false, bilibili.PlaySelectionOptions{
		PreferredCodec:      "avc",
		PreferLosslessAudio: true,
	})
	if err != nil || len(upgradable2) != 1 {
		t.Fatalf("second CheckUpgradeKeys within 24h cooldown: %+v, err=%v", upgradable2, err)
	}
	if playurlCalls.Load() != 1 {
		t.Fatalf("expected 0 new playurl calls during 24h cooldown, total=%d", playurlCalls.Load())
	}
}
