package library

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chialecode/trace/internal/storage"
)

func TestAllowMovedFilesPreservesCompleteStatus(t *testing.T) {
	tmp := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cacheDir := filepath.Join(tmp, "export")
	store, err := OpenWithDataDir(db, cacheDir, tmp)
	if err != nil {
		t.Fatal(err)
	}
	store.SetAllowMovedFiles(true)

	itemDir := filepath.Join(cacheDir, "bilibili", "1001_UP", "lives", "live_42_100")
	if err := os.MkdirAll(itemDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Simulate segment 1 already uploaded & deleted locally while segment 2 finishes.
	if err := os.WriteFile(filepath.Join(itemDir, "part-002.mp4"), []byte("seg2-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	saved, err := store.SaveItem(itemDir, ItemMetadata{
		Version: 1,
		ID:      "42:100",
		Site:    "bilibili",
		Kind:    "live",
		Status:  "complete",
		Title:   "Live",
		Author:  ItemAuthor{Mid: 1001, Name: "UP"},
		Media: []MediaFile{
			{Role: "live_segment", Path: "part-001.mp4", Bytes: 1234, SHA256: "aaa"},
			{Role: "live_segment", Path: "part-002.mp4", Bytes: 10, SHA256: "bbb"},
		},
		Live: &LiveRecordingInfo{
			RoomID: 42,
			Segments: []LiveSegmentInfo{
				{File: "part-001.mp4", Bytes: 1234, SHA256: "aaa"},
				{File: "part-002.mp4", Bytes: 10, SHA256: "bbb"},
			},
		},
	})
	if err != nil {
		t.Fatalf("SaveItem: %v", err)
	}
	if saved.Status != "complete" || saved.TotalBytes != 1244 {
		t.Fatalf("saved = status %q bytes %d, want complete / 1244", saved.Status, saved.TotalBytes)
	}
	// Remove segment 2 as well (everything moved to cloud).
	_ = os.Remove(filepath.Join(itemDir, "part-002.mp4"))
	got, ok, err := store.GetItem(saved.ItemKey)
	if err != nil || !ok || got.Status != "complete" {
		t.Fatalf("GetItem after move = (%q, %v, %v), want complete", got.Status, ok, err)
	}
	states := store.States([]string{saved.ItemKey})
	if states[saved.ItemKey].Dot != "green" {
		t.Fatalf("States after move = %+v, want green", states[saved.ItemKey])
	}
	list, err := store.ListItems(FeedFilter{})
	if err != nil || len(list) != 1 || list[0].Status != "complete" {
		t.Fatalf("ListItems after move = %+v, %v", list, err)
	}
}

func TestMergeItemMetadataLiveSequentialOrderContentDedupAndSupplement(t *testing.T) {
	// 1. Two machines recorded same broadcast: [L1 (001)] + [R1 (001), R2 (002)] -> [L1 (001), R1 (002), R2 (003)].
	local := ItemMetadata{
		ItemKey: "bilibili:live:42:100",
		Kind:    "live",
		Media:   []MediaFile{{Role: "live_segment", Path: "part-001.mp4", Bytes: 10, SHA256: "hashL1"}},
		Live: &LiveRecordingInfo{
			RoomID:   42,
			Segments: []LiveSegmentInfo{{File: "part-001.mp4", Bytes: 10, SHA256: "hashL1"}},
		},
	}
	imported := ItemMetadata{
		ItemKey: "bilibili:live:42:100",
		Kind:    "live",
		Media: []MediaFile{
			{Role: "live_segment", Path: "part-001.mp4", Bytes: 11, SHA256: "hashR1"},
			{Role: "live_segment", Path: "part-002.mp4", Bytes: 12, SHA256: "hashR2"},
		},
		Live: &LiveRecordingInfo{
			RoomID: 42,
			Segments: []LiveSegmentInfo{
				{File: "part-001.mp4", Bytes: 11, SHA256: "hashR1"},
				{File: "part-002.mp4", Bytes: 12, SHA256: "hashR2"},
			},
		},
	}
	merged, renames := MergeItemMetadata(local, imported, MergeUnion, nil)
	if len(merged.Live.Segments) != 3 {
		t.Fatalf("merged segments = %+v, want 3", merged.Live.Segments)
	}
	wantOrder := []struct{ file, sha string }{
		{"part-001.mp4", "hashL1"},
		{"part-002.mp4", "hashR1"},
		{"part-003.mp4", "hashR2"},
	}
	for i, w := range wantOrder {
		got := merged.Live.Segments[i]
		if got.File != w.file || got.SHA256 != w.sha {
			t.Fatalf("segment[%d] = %+v, want %+v", i, got, w)
		}
	}
	if len(renames) != 2 || renames[0] != (SegmentRename{From: "part-001.mp4", To: "part-002.mp4"}) || renames[1] != (SegmentRename{From: "part-002.mp4", To: "part-003.mp4"}) {
		t.Fatalf("renames = %+v", renames)
	}
	// Ensure caller's local slice was not mutated.
	if len(local.Live.Segments) != 1 || len(local.Media) != 1 {
		t.Fatal("MergeItemMetadata mutated local input slices")
	}

	// 2. Content deduplication even when duplicate segment is under an untaken filename.
	local2 := ItemMetadata{
		ItemKey: "bilibili:live:42:100",
		Kind:    "live",
		Media:   []MediaFile{{Role: "live_segment", Path: "part-001.mp4", Bytes: 12, SHA256: "hashB"}},
		Live: &LiveRecordingInfo{
			RoomID:   42,
			Segments: []LiveSegmentInfo{{File: "part-001.mp4", Bytes: 12, SHA256: "hashB"}},
		},
	}
	imported2 := ItemMetadata{
		ItemKey: "bilibili:live:42:100",
		Kind:    "live",
		Media: []MediaFile{
			{Role: "live_segment", Path: "part-001.mp4", Bytes: 11, SHA256: "hashA"},
			{Role: "live_segment", Path: "part-002.mp4", Bytes: 12, SHA256: "hashB"},
		},
		Live: &LiveRecordingInfo{
			RoomID: 42,
			Segments: []LiveSegmentInfo{
				{File: "part-001.mp4", Bytes: 11, SHA256: "hashA"},
				{File: "part-002.mp4", Bytes: 12, SHA256: "hashB"},
			},
		},
	}
	merged2, _ := MergeItemMetadata(local2, imported2, MergeUnion, nil)
	if len(merged2.Live.Segments) != 2 {
		t.Fatalf("duplicate segment not deduped: %+v", merged2.Live.Segments)
	}

	// 3. Supplementing NotePages, Music, and Parts fields previously cleared by KeepListedFiles.
	full := ItemMetadata{
		ItemKey: "douyin:dynamic:999",
		Kind:    "dynamic",
		Media: []MediaFile{
			{Role: "image", Path: "images/01.jpg"},
			{Role: "image", Path: "images/02.jpg"},
			{Role: "audio", Path: "music.mp3"},
		},
		NotePages: []NotePageRecord{
			{Index: 1, Kind: "image", Image: "images/01.jpg"},
			{Index: 2, Kind: "image", Image: "images/02.jpg"},
		},
		Music: &MusicInfo{Title: "BGM", File: "music.mp3"},
		Parts: []VideoPartRecord{{Page: 1, File: "P01.mp4", Danmaku: "P01.danmaku.xml", Subtitles: []string{"P01.zh.srt"}}},
	}
	partial, _ := KeepListedFiles(full, func(rel string) bool {
		return rel == "images/01.jpg" || rel == "P01.mp4"
	})
	if partial.NotePages[1].Image != "" || partial.Music.File != "" || partial.Parts[0].Danmaku != "" {
		t.Fatalf("KeepListedFiles did not clear missing fields: %+v", partial)
	}
	supplemented, _ := MergeItemMetadata(partial, full, MergeUnion, nil)
	if supplemented.NotePages[1].Image != "images/02.jpg" || supplemented.Music.File != "music.mp3" || supplemented.Parts[0].Danmaku != "P01.danmaku.xml" || len(supplemented.Parts[0].Subtitles) != 1 {
		t.Fatalf("supplemented fields stayed empty: pages=%+v music=%+v parts=%+v", supplemented.NotePages, supplemented.Music, supplemented.Parts)
	}
}

func TestUnsafeManifestPathsRejectsCrossPlatformTraversal(t *testing.T) {
	bad := UnsafeManifestPaths(ItemMetadata{
		Media: []MediaFile{
			{Path: `..\..\evil.mp4`},
			{Path: "C:evil.mp4"},
			{Path: "ok\x00evil.mp4"},
			{Path: "./ok.mp4"},
			{Path: "images/01.jpg"},
		},
	})
	if len(bad) != 4 {
		t.Fatalf("UnsafeManifestPaths = %v, want 4 unsafe paths", bad)
	}
}

func TestGetItemLiveShortKeyLikeEscapeAndDotLabel(t *testing.T) {
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
	liveDir := filepath.Join(cacheDir, "bilibili", "1001_UP", "lives", "live_42_100")
	_ = os.MkdirAll(liveDir, 0o700)
	_ = os.WriteFile(filepath.Join(liveDir, "part-001.mp4"), []byte("live"), 0o600)
	if _, err := store.SaveItem(liveDir, ItemMetadata{
		Version: 1, ID: "42:100", Site: "bilibili", Kind: "live", Status: "complete",
		Title: "100% Pure_Test", Author: ItemAuthor{Mid: 1001, Name: "UP"},
		Media: []MediaFile{{Role: "live_segment", Path: "part-001.mp4"}},
		Live:  &LiveRecordingInfo{RoomID: 42, Segments: []LiveSegmentInfo{{File: "part-001.mp4"}}},
	}); err != nil {
		t.Fatal(err)
	}
	// Live short key "live:42:100" must resolve to "bilibili:live:42:100".
	if got, ok, err := store.GetItem("live:42:100"); err != nil || !ok || got.ItemKey != "bilibili:live:42:100" {
		t.Fatalf("GetItem(live:42:100) = (%+v, %v, %v)", got, ok, err)
	}
	// LIKE wildcard escaping: searching "100%" must match "100% Pure_Test",
	// whereas searching "100X" must not match via unescaped '%'.
	vidDir := filepath.Join(cacheDir, "bilibili", "1001_UP", "videos", "video_BV1like")
	_ = os.MkdirAll(vidDir, 0o700)
	_ = os.WriteFile(filepath.Join(vidDir, "P01.mp4"), []byte("vid"), 0o600)
	if _, err := store.SaveItem(vidDir, ItemMetadata{
		Version: 1, ID: "BV1like", Site: "bilibili", Kind: "video", Status: "complete",
		Title: "1000 PureXTest", Author: ItemAuthor{Mid: 1001, Name: "UP"},
		Media: []MediaFile{{Role: "video", Path: "P01.mp4"}},
	}); err != nil {
		t.Fatal(err)
	}
	pctItems, err := store.ListItems(FeedFilter{Keyword: "100%"})
	if err != nil || len(pctItems) != 1 || pctItems[0].ItemKey != "bilibili:live:42:100" {
		t.Fatalf("ListItems(100%%) = %+v, err=%v", pctItems, err)
	}
	underItems, err := store.ListItems(FeedFilter{Keyword: "Pure_Test"})
	if err != nil || len(underItems) != 1 || underItems[0].ItemKey != "bilibili:live:42:100" {
		t.Fatalf("ListItems(Pure_Test) = %+v, err=%v", underItems, err)
	}

	// ComputeDotState belowBest without Missing or UpgradeAvailable must not say "部分缺失".
	dot := ComputeDotState(ItemMetadata{
		ItemKey: "bilibili:video:BV1q",
		Status:  "complete",
		Parts: []VideoPartRecord{{
			Page:      1,
			File:      "P01.mp4",
			Video:     PartVideoInfo{Quality: 64},
			Available: PartAvailableInfo{BestVideo: 80},
		}},
	})
	if dot.Dot != "yellow" || dot.Label != "非最高画质" {
		t.Fatalf("ComputeDotState(belowBest) = %+v, want yellow / 非最高画质", dot)
	}
}

func TestPlanDeleteFailsClosedOnDBError(t *testing.T) {
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
	vidDir := filepath.Join(cacheDir, "bilibili", "1001_UP", "videos", "video_BV1shared")
	dynDir := filepath.Join(cacheDir, "bilibili", "1001_UP", "dynamics", "dyn_1")
	_ = os.MkdirAll(vidDir, 0o700)
	_ = os.MkdirAll(dynDir, 0o700)
	_ = os.WriteFile(filepath.Join(vidDir, "P01.mp4"), []byte("v"), 0o600)
	if _, err := store.SaveItem(vidDir, ItemMetadata{
		Version: 1, ID: "BV1shared", Site: "bilibili", Kind: "video", Status: "complete",
		Author: ItemAuthor{Mid: 1001, Name: "UP"}, Media: []MediaFile{{Role: "video", Path: "P01.mp4"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveItem(dynDir, ItemMetadata{
		Version: 1, ID: "1", Site: "bilibili", Kind: "dynamic", Status: "complete",
		Author: ItemAuthor{Mid: 1001, Name: "UP"}, Videos: []string{"BV1shared"},
	}); err != nil {
		t.Fatal(err)
	}
	// Drop item_refs to simulate a SQL error during shared-reference checking.
	if _, err := db.Exec(`DROP TABLE item_refs`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PlanDelete("bilibili:dynamic:1"); err == nil {
		t.Fatal("PlanDelete succeeded despite item_refs DB error")
	}
}
