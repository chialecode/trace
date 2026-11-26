package library

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/storage"
)

func openBatchTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	tmp := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cacheDir := filepath.Join(tmp, "cache")
	store, err := OpenWithDataDir(db, cacheDir, tmp)
	if err != nil {
		t.Fatalf("OpenWithDataDir: %v", err)
	}
	return store, cacheDir
}

func saveSyntheticMediaItem(t *testing.T, store *Store, dir string, meta ItemMetadata, files map[string][]byte) ItemMetadata {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	for rel, data := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatalf("mkdir parent %s: %v", p, err)
		}
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	saved, err := store.SaveItem(dir, meta)
	if err != nil {
		t.Fatalf("SaveItem %s: %v", meta.ID, err)
	}
	return saved
}

func TestPlanDeleteBatchAndDeleteBatchSharedVideosAndUserFiles(t *testing.T) {
	store, cacheDir := openBatchTestStore(t)

	// Video V_excl: referenced ONLY by D1 and D2 (not directly cached), has a user-owned file.
	vExclDir := filepath.Join(cacheDir, "bilibili", "1001", "videos", "BV1excl001")
	vExcl := saveSyntheticMediaItem(t, store, vExclDir, ItemMetadata{
		Version:      1,
		ID:           "BV1excl001",
		Site:         "bilibili",
		Kind:         "video",
		Status:       "complete",
		Title:        "Shared Only By Selected Batch",
		PubTs:        1700000100,
		DirectCached: false,
		Author:       ItemAuthor{Mid: 1001, Name: "UP1"},
		Media:        []MediaFile{{Role: "video", Path: "P01.mp4"}},
		Parts:        []VideoPartRecord{{CID: 1, Page: 1, File: "P01.mp4"}},
	}, map[string][]byte{
		"P01.mp4":       []byte("exclusive-video-bytes"),
		"keep-note.txt": []byte("user-personal-note"),
	})

	// Video V_keep: referenced by D1, D2 AND unselected D3 (must be preserved).
	vKeepDir := filepath.Join(cacheDir, "bilibili", "1001", "videos", "BV1keep002")
	vKeep := saveSyntheticMediaItem(t, store, vKeepDir, ItemMetadata{
		Version:      1,
		ID:           "BV1keep002",
		Site:         "bilibili",
		Kind:         "video",
		Status:       "complete",
		Title:        "Referenced By Unselected Dynamic",
		PubTs:        1700000110,
		DirectCached: false,
		Author:       ItemAuthor{Mid: 1001, Name: "UP1"},
		Media:        []MediaFile{{Role: "video", Path: "P01.mp4"}},
		Parts:        []VideoPartRecord{{CID: 2, Page: 1, File: "P01.mp4"}},
	}, map[string][]byte{
		"P01.mp4": []byte("shared-preserved-video-bytes"),
	})

	// Video V_direct: directly cached video also referenced by D1 and D2 (must be preserved).
	vDirectDir := filepath.Join(cacheDir, "bilibili", "1001", "videos", "BV1dir0003")
	vDirect := saveSyntheticMediaItem(t, store, vDirectDir, ItemMetadata{
		Version:      1,
		ID:           "BV1dir0003",
		Site:         "bilibili",
		Kind:         "video",
		Status:       "complete",
		Title:        "Directly Cached Video",
		PubTs:        1700000120,
		DirectCached: true,
		Author:       ItemAuthor{Mid: 1001, Name: "UP1"},
		Media:        []MediaFile{{Role: "video", Path: "P01.mp4"}},
		Parts:        []VideoPartRecord{{CID: 3, Page: 1, File: "P01.mp4"}},
	}, map[string][]byte{
		"P01.mp4": []byte("direct-video-bytes"),
	})

	// Dynamics D1, D2 (selected in batch) and D3 (unselected).
	d1 := saveSyntheticMediaItem(t, store, filepath.Join(cacheDir, "bilibili", "1001", "dynamics", "dyn_b1"), ItemMetadata{
		Version: 1, ID: "dyn_b1", Site: "bilibili", Kind: "dynamic", Status: "complete",
		Title: "Dyn 1", PubTs: 1700000200, Author: ItemAuthor{Mid: 1001, Name: "UP1"},
		Videos: []string{"BV1excl001", "BV1keep002", "BV1dir0003"},
	}, nil)
	d2 := saveSyntheticMediaItem(t, store, filepath.Join(cacheDir, "bilibili", "1001", "dynamics", "dyn_b2"), ItemMetadata{
		Version: 1, ID: "dyn_b2", Site: "bilibili", Kind: "dynamic", Status: "complete",
		Title: "Dyn 2", PubTs: 1700000210, Author: ItemAuthor{Mid: 1001, Name: "UP1"},
		Videos: []string{"BV1excl001", "BV1keep002", "BV1dir0003"},
	}, nil)
	d3 := saveSyntheticMediaItem(t, store, filepath.Join(cacheDir, "bilibili", "1001", "dynamics", "dyn_b3"), ItemMetadata{
		Version: 1, ID: "dyn_b3", Site: "bilibili", Kind: "dynamic", Status: "complete",
		Title: "Dyn 3 Unselected", PubTs: 1700000220, Author: ItemAuthor{Mid: 1001, Name: "UP1"},
		Videos: []string{"BV1keep002"},
	}, nil)

	// Recording live item: must be refused in batch plan and batch delete.
	rec := saveSyntheticMediaItem(t, store, filepath.Join(cacheDir, "bilibili", "1001", "lives", "777-1700000300"), ItemMetadata{
		Version: 1, ID: "777:1700000300", Site: "bilibili", Kind: "live", Status: "recording",
		Title: "Live Recording", PubTs: 1700000300, Author: ItemAuthor{Mid: 1001, Name: "UP1"},
	}, nil)

	// Standalone item under a separate author to verify empty author/kind dir cleanup
	// while keeping the site folder and cache root intact.
	soloDir := filepath.Join(cacheDir, "bilibili", "2002", "videos", "BV1solo004")
	solo := saveSyntheticMediaItem(t, store, soloDir, ItemMetadata{
		Version: 1, ID: "BV1solo004", Site: "bilibili", Kind: "video", Status: "complete",
		Title: "Solo Video", PubTs: 1700000250, DirectCached: true,
		Author: ItemAuthor{Mid: 2002, Name: "UP2"},
		Media:  []MediaFile{{Role: "video", Path: "P01.mp4"}},
		Parts:  []VideoPartRecord{{CID: 4, Page: 1, File: "P01.mp4"}},
	}, map[string][]byte{"P01.mp4": []byte("solo-bytes")})

	// 1. Empty batch rejected.
	if _, err := store.PlanDeleteBatch(nil); err == nil {
		t.Fatal("expected empty PlanDeleteBatch to fail")
	}

	// 2. Batch containing recording item rejected with no side effects.
	if _, err := store.PlanDeleteBatch([]string{d1.ItemKey, rec.ItemKey}); err == nil {
		t.Fatal("expected PlanDeleteBatch containing recording item to fail")
	}
	if _, err := store.DeleteBatch([]string{d1.ItemKey, rec.ItemKey}, true); err == nil {
		t.Fatal("expected DeleteBatch containing recording item to fail")
	}
	if _, ok, _ := store.GetItem(d1.ItemKey); !ok {
		t.Fatal("failed batch delete must not remove non-recording item d1")
	}

	// 3. PlanDeleteBatch([d1, d2, d1]) deduplicates keys, includes vExcl (exclusively
	// referenced by the batch), preserves vKeep and vDirect once each, and detects keep-note.txt.
	plan, err := store.PlanDeleteBatch([]string{d1.ItemKey, d2.ItemKey, d1.ItemKey})
	if err != nil {
		t.Fatalf("PlanDeleteBatch: %v", err)
	}
	if len(plan.TargetKeys) != 2 {
		t.Fatalf("TargetKeys = %v, want 2 deduplicated keys", plan.TargetKeys)
	}
	wantDeleteSet := map[string]bool{d1.ItemKey: true, d2.ItemKey: true, vExcl.ItemKey: true}
	if len(plan.ItemKeys) != len(wantDeleteSet) {
		t.Fatalf("ItemKeys = %v, want %v", plan.ItemKeys, wantDeleteSet)
	}
	for _, k := range plan.ItemKeys {
		if !wantDeleteSet[k] {
			t.Fatalf("unexpected key %q in ItemKeys %v", k, plan.ItemKeys)
		}
	}
	wantSharedSet := map[string]bool{vKeep.ItemKey: true, vDirect.ItemKey: true}
	if len(plan.PreservedSharedVideos) != len(wantSharedSet) {
		t.Fatalf("PreservedSharedVideos = %v, want %v", plan.PreservedSharedVideos, wantSharedSet)
	}
	for _, k := range plan.PreservedSharedVideos {
		if !wantSharedSet[k] {
			t.Fatalf("unexpected key %q in PreservedSharedVideos %v", k, plan.PreservedSharedVideos)
		}
	}
	if len(plan.PreservedUserFiles) != 1 || plan.PreservedUserFiles[0] != "keep-note.txt" {
		t.Fatalf("PreservedUserFiles = %v, want [keep-note.txt]", plan.PreservedUserFiles)
	}

	// 4. Selecting both a dynamic and its referenced video directly must not duplicate keys
	// or count the explicitly selected video as preserved.
	planWithDirect := func() DeletePlan {
		p, pErr := store.PlanDeleteBatch([]string{d1.ItemKey, vKeep.ItemKey})
		if pErr != nil {
			t.Fatalf("PlanDeleteBatch(d1, vKeep): %v", pErr)
		}
		return p
	}()
	for _, k := range planWithDirect.PreservedSharedVideos {
		if k == vKeep.ItemKey {
			t.Fatalf("explicitly selected video %s must not appear in PreservedSharedVideos: %v", vKeep.ItemKey, planWithDirect.PreservedSharedVideos)
		}
	}

	// 5. Execute DeleteBatch([d1, d2, d1], true).
	res, err := store.DeleteBatch([]string{d1.ItemKey, d2.ItemKey, d1.ItemKey}, true)
	if err != nil {
		t.Fatalf("DeleteBatch: %v", err)
	}
	if len(res.PreservedUserFiles) != 1 || res.PreservedUserFiles[0] != "keep-note.txt" {
		t.Fatalf("DeleteBatch PreservedUserFiles = %v, want [keep-note.txt]", res.PreservedUserFiles)
	}
	for _, gone := range []string{d1.ItemKey, d2.ItemKey, vExcl.ItemKey} {
		if _, ok, _ := store.GetItem(gone); ok {
			t.Fatalf("expected %s deleted from DB", gone)
		}
	}
	if _, err := os.Stat(filepath.Join(vExclDir, "P01.mp4")); !os.IsNotExist(err) {
		t.Fatalf("expected vExcl registered P01.mp4 deleted, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(vExclDir, "item.json")); !os.IsNotExist(err) {
		t.Fatalf("expected vExcl item.json deleted, err=%v", err)
	}
	if data, err := os.ReadFile(filepath.Join(vExclDir, "keep-note.txt")); err != nil || string(data) != "user-personal-note" {
		t.Fatalf("non-TRACE user file keep-note.txt was not preserved: err=%v data=%q", err, string(data))
	}
	for _, kept := range []string{vKeep.ItemKey, vDirect.ItemKey, d3.ItemKey} {
		if _, ok, _ := store.GetItem(kept); !ok {
			t.Fatalf("expected %s preserved in DB", kept)
		}
	}
	if _, err := os.Stat(filepath.Join(vKeepDir, "P01.mp4")); err != nil {
		t.Fatalf("expected preserved shared video file to stay on disk: %v", err)
	}
	if _, err := os.Stat(filepath.Join(vDirectDir, "P01.mp4")); err != nil {
		t.Fatalf("expected directly cached video file to stay on disk: %v", err)
	}

	// 6. DeleteBatch on solo author item cleans up empty item/kind/author dirs while
	// preserving site root (<cache>/bilibili) and cache root.
	if _, err := store.DeleteBatch([]string{solo.ItemKey}, true); err != nil {
		t.Fatalf("DeleteBatch solo: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "bilibili", "2002")); !os.IsNotExist(err) {
		t.Fatalf("expected empty author dir 2002 removed, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "bilibili")); err != nil {
		t.Fatalf("site root folder must be preserved: %v", err)
	}
	if _, err := os.Stat(cacheDir); err != nil {
		t.Fatalf("cache root folder must be preserved: %v", err)
	}
}

func TestStatisticsAuthorItemCountAndBytesWithViaAnd5000Items(t *testing.T) {
	store, cacheDir := openBatchTestStore(t)

	// Verify collab and forward via-owner attribution sums ItemCount and TotalBytes accurately.
	saveSyntheticMediaItem(t, store, filepath.Join(cacheDir, "bilibili", "1001", "videos", "BV1own1"), ItemMetadata{
		Version: 1, ID: "BV1own1", Site: "bilibili", Kind: "video", Status: "complete",
		Title: "Own Video", PubTs: 1700000100, Author: ItemAuthor{Mid: 1001, Name: "Member"},
		Media: []MediaFile{{Role: "video", Path: "P01.mp4"}},
	}, map[string][]byte{"P01.mp4": make([]byte, 1000)})

	saveSyntheticMediaItem(t, store, filepath.Join(cacheDir, "bilibili", "1001", "videos", "BV1col1"), ItemMetadata{
		Version: 1, ID: "BV1col1", Site: "bilibili", Kind: "video", Status: "complete",
		Title: "Collab Video", PubTs: 1700000110,
		Author: ItemAuthor{Mid: 9999, Name: "Uploader"},
		Via:    &ItemVia{Reason: ViaCollab, Site: "bilibili", UID: "1001", Name: "Member"},
		Media:  []MediaFile{{Role: "video", Path: "P01.mp4"}},
	}, map[string][]byte{"P01.mp4": make([]byte, 2500)})

	saveSyntheticMediaItem(t, store, filepath.Join(cacheDir, "bilibili", "1001", "forwards", "BV1fwd1"), ItemMetadata{
		Version: 1, ID: "BV1fwd1", Site: "bilibili", Kind: "video", Status: "complete",
		Title: "Forward Video", PubTs: 1700000120,
		Author: ItemAuthor{Mid: 8888, Name: "OriginAuthor"},
		Via:    &ItemVia{Reason: ViaForward, Site: "bilibili", UID: "1001", Name: "Member", DynamicID: "501"},
		Media:  []MediaFile{{Role: "video", Path: "P01.mp4"}},
	}, map[string][]byte{"P01.mp4": make([]byte, 4000)})

	saveSyntheticMediaItem(t, store, filepath.Join(cacheDir, "douyin", "sec1", "works", "7001"), ItemMetadata{
		Version: 1, ID: "7001", Site: "douyin", Kind: "video", Status: "complete",
		Title: "Douyin Video", PubTs: 1700000130,
		Author: ItemAuthor{Site: "douyin", UID: "sec_dy_1", Name: "DouyinAuthor"},
		Media:  []MediaFile{{Role: "video", Path: "video.mp4"}},
	}, map[string][]byte{"video.mp4": make([]byte, 6000)})

	stats, err := store.Statistics()
	if err != nil {
		t.Fatalf("Statistics: %v", err)
	}
	byKey := map[string]ItemAuthor{}
	for _, au := range stats.Authors {
		byKey[au.Site+":"+au.UID] = au
	}
	if _, leaked := byKey["bilibili:9999"]; leaked {
		t.Fatalf("collab uploader 9999 leaked into Statistics.Authors: %+v", stats.Authors)
	}
	if _, leaked := byKey["bilibili:8888"]; leaked {
		t.Fatalf("forward original author 8888 leaked into Statistics.Authors: %+v", stats.Authors)
	}
	member := byKey["bilibili:1001"]
	if member.ItemCount != 3 || member.TotalBytes != 7500 {
		t.Fatalf("member stats = %+v, want ItemCount=3 TotalBytes=7500", member)
	}
	dy := byKey["douyin:sec_dy_1"]
	if dy.ItemCount != 1 || dy.TotalBytes != 6000 {
		t.Fatalf("douyin stats = %+v, want ItemCount=1 TotalBytes=6000", dy)
	}

	// Scale test: 5,000 synthetic items across 50 authors must aggregate accurately and fast offline.
	store5k, _ := openBatchTestStore(t)
	tx, err := store5k.db.Begin()
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	stmt, err := tx.Prepare(`
		INSERT INTO library_items (
			item_key, site, kind, raw_id, mid, author_uid, author_name, author_face,
			title, summary, pub_ts, cached_at, status, item_dir, total_bytes,
			upgrade_available, upgrade_hint, json_data
		) VALUES (?, ?, 'video', ?, ?, ?, ?, '', ?, '', ?, '2026-11-26T00:00:00Z', 'complete', ?, ?, 0, '', ?)
	`)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	const totalItems = 5000
	const authorCount = 50
	var wantTotalBytes int64
	wantAuthorBytes := make(map[string]int64, authorCount)
	wantAuthorCounts := make(map[string]int, authorCount)
	for i := 0; i < totalItems; i++ {
		aIdx := i % authorCount
		uid := fmt.Sprintf("%d", 10000+aIdx)
		name := fmt.Sprintf("Author_%02d", aIdx)
		rawID := fmt.Sprintf("BV1s%07d", i)
		key := "bilibili:video:" + rawID
		sz := int64((aIdx + 1) * 1024)
		wantTotalBytes += sz
		wantAuthorBytes["bilibili:"+uid] += sz
		wantAuthorCounts["bilibili:"+uid]++
		jsonData := fmt.Sprintf(`{"version":1,"id":"%s","site":"bilibili","kind":"video","status":"complete","title":"%s","pubTs":%d,"author":{"site":"bilibili","uid":"%s","mid":%s,"name":"%s"}}`,
			rawID, rawID, 1700000000+i, uid, uid, name)
		if _, err := stmt.Exec(key, "bilibili", rawID, 10000+aIdx, uid, name, rawID, 1700000000+i, "/tmp/"+rawID, sz, jsonData); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	_ = stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	start := time.Now()
	stats5k, err := store5k.Statistics()
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Statistics 5k: %v", err)
	}
	if stats5k.Items != totalItems || stats5k.Bytes != wantTotalBytes || len(stats5k.Authors) != authorCount {
		t.Fatalf("stats5k = items:%d bytes:%d authors:%d, want %d / %d / %d",
			stats5k.Items, stats5k.Bytes, len(stats5k.Authors), totalItems, wantTotalBytes, authorCount)
	}
	for _, au := range stats5k.Authors {
		k := au.Site + ":" + au.UID
		if au.ItemCount != wantAuthorCounts[k] || au.TotalBytes != wantAuthorBytes[k] {
			t.Fatalf("author %s = (%d, %d), want (%d, %d)", k, au.ItemCount, au.TotalBytes, wantAuthorCounts[k], wantAuthorBytes[k])
		}
	}
	if elapsed > 2*time.Second {
		t.Fatalf("Statistics() on 5,000 items too slow: %v", elapsed)
	}
}

func TestListItemsByItemKeysIncludingOver400Boundary(t *testing.T) {
	store, _ := openBatchTestStore(t)
	store.SetAllowMovedFiles(true)

	tx, err := store.db.Begin()
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	stmt, err := tx.Prepare(`
		INSERT INTO library_items (
			item_key, site, kind, raw_id, mid, author_uid, author_name, author_face,
			title, summary, pub_ts, cached_at, status, item_dir, total_bytes,
			upgrade_available, upgrade_hint, json_data
		) VALUES (?, 'bilibili', 'video', ?, 1001, '1001', 'UP', '', ?, '', ?, '2026-11-26T00:00:00Z', 'complete', ?, 1024, 0, '', ?)
	`)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	const total = 450
	allKeys := make([]string, 0, total)
	for i := 0; i < total; i++ {
		rawID := fmt.Sprintf("BV1k%07d", i)
		key := "bilibili:video:" + rawID
		allKeys = append(allKeys, key)
		jsonData := fmt.Sprintf(`{"version":1,"id":"%s","site":"bilibili","kind":"video","status":"complete","title":"Item %d","pubTs":%d,"author":{"site":"bilibili","uid":"1001","mid":1001,"name":"UP"}}`,
			rawID, i, 1700000000+i)
		if _, err := stmt.Exec(key, rawID, fmt.Sprintf("Item %d", i), 1700000000+i, "/tmp/"+rawID, jsonData); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	_ = stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// 1. Small explicit subset.
	subset := []string{allKeys[3], allKeys[10], allKeys[449], allKeys[10]}
	got, err := store.ListItems(FeedFilter{ItemKeys: subset, Limit: 20})
	if err != nil {
		t.Fatalf("ListItems small subset: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("ListItems small subset returned %d items, want 3", len(got))
	}

	// 2. Whitespace-only ItemKeys slice must return empty, never the whole library.
	emptyGot, err := store.ListItems(FeedFilter{ItemKeys: []string{"", "   "}, Limit: 20})
	if err != nil {
		t.Fatalf("ListItems blank keys: %v", err)
	}
	if len(emptyGot) != 0 {
		t.Fatalf("blank ItemKeys returned %d items, want 0", len(emptyGot))
	}

	// 3. >400 keys boundary (420 keys) via json_each fallback works without SQL parameter overflow.
	largeSubset := allKeys[:420]
	gotLarge, err := store.ListItems(FeedFilter{ItemKeys: largeSubset, Limit: 500})
	if err != nil {
		t.Fatalf("ListItems 420 keys: %v", err)
	}
	if len(gotLarge) != 420 {
		t.Fatalf("ListItems 420 keys returned %d items, want 420", len(gotLarge))
	}
}
