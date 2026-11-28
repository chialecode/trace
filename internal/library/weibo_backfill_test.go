package library

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/chialecode/trace/internal/storage"
)

func TestWeiboCacheStandardVersionAndOfflineHelpers(t *testing.T) {
	if CurrentCacheStandardVersion != 2 {
		t.Fatalf("CurrentCacheStandardVersion = %d, want 2 (legacy compatibility constant)", CurrentCacheStandardVersion)
	}
	if WeiboNoWMStandardVersion != 3 {
		t.Fatalf("WeiboNoWMStandardVersion = %d, want 3", WeiboNoWMStandardVersion)
	}
	if WeiboCacheStandardVersion != 4 {
		t.Fatalf("WeiboCacheStandardVersion = %d, want 4", WeiboCacheStandardVersion)
	}
	wantSiteStandards := []struct {
		site string
		kind string
		want int
	}{
		{"bilibili", "video", BilibiliVideoStandardVersion},
		{"bilibili", "dynamic", BilibiliDynamicStandardVersion},
		{"bilibili", "live", CurrentCacheStandardVersion},
		{"douyin", "video", DouyinCacheStandardVersion},
		{"douyin", "dynamic", DouyinCacheStandardVersion},
		{"weibo", "dynamic", WeiboCacheStandardVersion},
		{"weibo", "video", WeiboCacheStandardVersion},
		{"x", "dynamic", XCacheStandardVersion},
		{"x", "video", XCacheStandardVersion},
	}
	for _, tc := range wantSiteStandards {
		if got := SiteCacheStandardVersion(tc.site, tc.kind); got != tc.want {
			t.Fatalf("SiteCacheStandardVersion(%q, %q) = %d, want %d", tc.site, tc.kind, got, tc.want)
		}
	}

	staticMissing := ItemMetadata{
		Site:                 "weibo",
		Kind:                 "dynamic",
		ID:                   "5090000000000001",
		RawID:                "5090000000000001",
		Status:               "complete",
		CacheStandardVersion: 2,
		AssetIDs:             []string{"001picA"},
		Media: []MediaFile{
			{Role: "image", Path: "images/01.jpg", Bytes: 1024, SHA256: "abc"},
		},
		NotePages: []NotePageRecord{
			{Index: 1, Kind: "image", Image: "images/01.jpg", MediaID: "001picA"},
		},
	}

	if !WeiboNeedsNoWMBackfill(staticMissing, true) {
		t.Fatal("expected staticMissing with removeWatermark=true to need backfill")
	}
	if WeiboNeedsNoWMBackfill(staticMissing, false) {
		t.Fatal("expected staticMissing with removeWatermark=false NOT to need backfill")
	}
	if !WeiboHasAllFastCDNPicIDs(staticMissing) {
		t.Fatal("expected staticMissing with MediaID/AssetIDs to qualify for fast CDN backfill")
	}

	// Already stamped with WeiboNoWMStandardVersion = 3 -> no-watermark CDN backfill does not repeat infinitely,
	// while EditHistory backfill remains eligible until WeiboCacheStandardVersion = 4.
	v3NoWM := staticMissing
	v3NoWM.CacheStandardVersion = WeiboNoWMStandardVersion
	if WeiboNeedsNoWMBackfill(v3NoWM, true) {
		t.Fatal("item stamped with WeiboNoWMStandardVersion=3 must not need no-watermark backfill again")
	}
	v3Edited := v3NoWM
	v3Edited.EditCount = 2
	if !WeiboNeedsEditHistoryBackfill(v3Edited) {
		t.Fatal("v3 edited Weibo post without EditHistory must qualify for WeiboNeedsEditHistoryBackfill")
	}
	v4Edited := v3Edited
	v4Edited.CacheStandardVersion = WeiboCacheStandardVersion
	if WeiboNeedsEditHistoryBackfill(v4Edited) {
		t.Fatal("v4 edited Weibo post must not repeat EditHistory backfill")
	}
	v3WithHistory := v3Edited
	v3WithHistory.EditHistory = []ItemEditRevision{{Version: 1, Text: "旧版正文"}}
	if WeiboNeedsEditHistoryBackfill(v3WithHistory) {
		t.Fatal("Weibo post already carrying EditHistory must not trigger missing EditHistory backfill")
	}

	// Already stamped with WeiboCacheStandardVersion = 4 -> never repeats infinitely.
	v3Done := staticMissing
	v3Done.CacheStandardVersion = WeiboCacheStandardVersion
	if WeiboNeedsNoWMBackfill(v3Done, true) {
		t.Fatal("item stamped with WeiboCacheStandardVersion=4 must not need backfill again")
	}

	// Already has verified NoWatermark companion -> does not need backfill.
	hasCompanion := staticMissing
	hasCompanion.Media = append(append([]MediaFile(nil), staticMissing.Media...), MediaFile{
		Role: "image_nowm", Path: "images/01_nowm.jpg", Bytes: 800, SHA256: "def",
	})
	hasCompanion.NotePages = []NotePageRecord{
		{Index: 1, Kind: "image", Image: "images/01.jpg", NoWatermark: "images/01_nowm.jpg", MediaID: "001picA"},
	}
	if WeiboNeedsNoWMBackfill(hasCompanion, true) {
		t.Fatal("item already containing verified companion must not need backfill")
	}

	// Archived snapshot -> skipped.
	archived := staticMissing
	archived.ArchivedOf = "weibo:dynamic:5090000000000001"
	if WeiboNeedsNoWMBackfill(archived, true) {
		t.Fatal("archived snapshot must not need backfill")
	}

	// GIF-only post -> skipped.
	gifOnly := ItemMetadata{
		Site:                 "weibo",
		Kind:                 "dynamic",
		ID:                   "5090000000000002",
		Status:               "complete",
		CacheStandardVersion: 0,
		Media:                []MediaFile{{Role: "image", Path: "images/01.gif", Bytes: 2048}},
		NotePages:            []NotePageRecord{{Index: 1, Kind: "image", Image: "images/01.gif", GIF: true}},
	}
	if WeiboNeedsNoWMBackfill(gifOnly, true) {
		t.Fatal("GIF-only Weibo post must not need no-watermark backfill")
	}

	// Non-Weibo item -> skipped.
	xItem := staticMissing
	xItem.Site = "x"
	if WeiboNeedsNoWMBackfill(xItem, true) {
		t.Fatal("non-Weibo item must not trigger Weibo backfill")
	}

	// Missing pic_id -> cannot use fast CDN directly.
	noPicID := staticMissing
	noPicID.AssetIDs = nil
	noPicID.NotePages = []NotePageRecord{{Index: 1, Kind: "image", Image: "images/01.jpg"}}
	if WeiboHasAllFastCDNPicIDs(noPicID) {
		t.Fatal("item without AssetIDs or NotePages[].MediaID must not qualify for fast CDN")
	}

	// Forward detection.
	fwdByVia := staticMissing
	fwdByVia.Via = &ItemVia{Reason: ViaForward, Site: "weibo", UID: "1001"}
	if !WeiboIsForwardItem(fwdByVia) {
		t.Fatal("expected ViaForward item to be detected as forward")
	}
	fwdByDir := staticMissing
	fwdByDir.ItemDir = filepath.Join("cache", "weibo", "1001", "forwards", "5090000000000001")
	if !WeiboIsForwardItem(fwdByDir) {
		t.Fatal("expected forwards/ itemDir to be detected as forward")
	}
	if WeiboIsForwardItem(staticMissing) {
		t.Fatal("ordinary original post must not be detected as forward")
	}

	// MergeItemMetadata preserves higher CacheStandardVersion (v2 -> v3, v3 stays v3).
	mergedUp, _ := MergeItemMetadata(staticMissing, v3Done, "", nil)
	if mergedUp.CacheStandardVersion != WeiboCacheStandardVersion {
		t.Fatalf("MergeItemMetadata v2+v3 CacheStandardVersion = %d, want %d", mergedUp.CacheStandardVersion, WeiboCacheStandardVersion)
	}
	mergedKeep, _ := MergeItemMetadata(v3Done, staticMissing, "", nil)
	if mergedKeep.CacheStandardVersion != WeiboCacheStandardVersion {
		t.Fatalf("MergeItemMetadata v3+v2 CacheStandardVersion = %d, want %d", mergedKeep.CacheStandardVersion, WeiboCacheStandardVersion)
	}
}

func TestListItemsMetaOnlyBySiteNo200Cap(t *testing.T) {
	tmp := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer db.Close()
	cacheDir := filepath.Join(tmp, "cache")
	st, err := OpenWithDataDir(db, cacheDir, tmp)
	if err != nil {
		t.Fatal(err)
	}

	const weiboCount = 215
	for i := 1; i <= weiboCount; i++ {
		mid := fmt.Sprintf("509000000000%04d", i)
		dir := filepath.Join(cacheDir, "weibo", "100001", "posts", mid)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := st.SaveItem(dir, ItemMetadata{
			Site:   "weibo",
			Kind:   "dynamic",
			ID:     mid,
			RawID:  mid,
			Title:  "微博条目",
			PubTs:  int64(1700000000 + i),
			Status: "complete",
			Author: ItemAuthor{Site: "weibo", UID: "100001", Name: "合成博主"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Seed a few non-Weibo items to confirm site isolation.
	for i := 1; i <= 3; i++ {
		bvid := fmt.Sprintf("BV1meta00000%d", i)
		dir := filepath.Join(cacheDir, "bilibili", "20001", "videos", bvid)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := st.SaveItem(dir, ItemMetadata{
			Site:   "bilibili",
			Kind:   "video",
			ID:     bvid,
			RawID:  bvid,
			Title:  "B站视频",
			PubTs:  int64(1700001000 + i),
			Status: "complete",
			Author: ItemAuthor{Site: "bilibili", Mid: 20001, Name: "UP"},
		}); err != nil {
			t.Fatal(err)
		}
	}

	got, err := st.ListItemsMetaOnlyBySite("weibo")
	if err != nil {
		t.Fatalf("ListItemsMetaOnlyBySite: %v", err)
	}
	if len(got) != weiboCount {
		t.Fatalf("ListItemsMetaOnlyBySite(weibo) returned %d items, want %d", len(got), weiboCount)
	}
	for _, it := range got {
		if it.Site != "weibo" {
			t.Fatalf("unexpected non-weibo item %q", it.ItemKey)
		}
	}
}

func TestHasCachedBilibiliDynamicsAndBatchUIDs(t *testing.T) {
	tmp := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer db.Close()
	cacheDir := filepath.Join(tmp, "cache")
	st, err := OpenWithDataDir(db, cacheDir, tmp)
	if err != nil {
		t.Fatal(err)
	}

	save := func(subPath string, meta ItemMetadata) {
		dir := filepath.Join(cacheDir, filepath.FromSlash(subPath))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := st.SaveItem(dir, meta); err != nil {
			t.Fatal(err)
		}
	}
	// UID 1001: authored dynamic
	save("bilibili/1001/dynamics/9001", ItemMetadata{
		Site: "bilibili", Kind: "dynamic", ID: "dynamic:9001", RawID: "9001", Status: "complete",
		Author: ItemAuthor{Site: "bilibili", Mid: 1001, UID: "1001", Name: "UP1"},
	})
	// UID 1002: only forwarded dynamic via Via.UID
	save("bilibili/1002/forwards/9002", ItemMetadata{
		Site: "bilibili", Kind: "dynamic", ID: "dynamic:9002", RawID: "9002", Status: "complete",
		Author: ItemAuthor{Site: "bilibili", Mid: 9999, UID: "9999", Name: "Orig"},
		Via:    &ItemVia{Reason: ViaForward, Site: "bilibili", UID: "1002", Name: "UP2"},
	})
	// UID 1003: only video (no dynamic)
	save("bilibili/1003/videos/BV1novid00001", ItemMetadata{
		Site: "bilibili", Kind: "video", ID: "video:BV1novid00001", RawID: "BV1novid00001", Status: "complete",
		Author: ItemAuthor{Site: "bilibili", Mid: 1003, UID: "1003", Name: "UP3"},
	})
	// UID 1004: only archived snapshot of dynamic -> must be excluded!
	save("bilibili/1004/dynamics/9004__v1-1700000000", ItemMetadata{
		Site: "bilibili", Kind: "dynamic", ID: "dynamic:9004__v1-1700000000", RawID: "9004__v1-1700000000", Status: "complete",
		ArchivedOf: "bilibili:dynamic:9004",
		Author:     ItemAuthor{Site: "bilibili", Mid: 1004, UID: "1004", Name: "UP4"},
	})

	if ok, err := st.HasCachedBilibiliDynamics("1001"); err != nil || !ok {
		t.Fatalf("HasCachedBilibiliDynamics(1001) = %v, %v; want true", ok, err)
	}
	if ok, err := st.HasCachedBilibiliDynamics("1002"); err != nil || !ok {
		t.Fatalf("HasCachedBilibiliDynamics(1002 via forward) = %v, %v; want true", ok, err)
	}
	if ok, err := st.HasCachedBilibiliDynamics("1003"); err != nil || ok {
		t.Fatalf("HasCachedBilibiliDynamics(1003 video-only) = %v, %v; want false", ok, err)
	}
	if ok, err := st.HasCachedBilibiliDynamics("1004"); err != nil || ok {
		t.Fatalf("HasCachedBilibiliDynamics(1004 archived-only) = %v, %v; want false", ok, err)
	}

	got, err := st.CachedBilibiliDynamicUIDs([]string{"1001", "1002", "1003", "1004"})
	if err != nil {
		t.Fatalf("CachedBilibiliDynamicUIDs: %v", err)
	}
	if !got["1001"] || !got["1002"] || got["1003"] || got["1004"] {
		t.Fatalf("CachedBilibiliDynamicUIDs unexpected result: %+v", got)
	}
}
