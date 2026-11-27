package library

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"
)

func TestPlanDeleteLiveRecordingExactFileCountWithoutOrigJSON(t *testing.T) {
	store, cacheDir := openBatchTestStore(t)

	liveDir := filepath.Join(cacheDir, "bilibili", "10001", "lives", "50001-1700000100")
	files := map[string][]byte{
		"cover.jpg": []byte("synthetic-cover-jpg"),
	}
	media := []MediaFile{{Role: "cover", Path: "cover.jpg"}}
	segments := make([]LiveSegmentInfo, 0, 6)
	for i := 1; i <= 6; i++ {
		name := fmt.Sprintf("part-%03d.mp4", i)
		payload := []byte(fmt.Sprintf("synthetic-live-segment-%03d", i))
		files[name] = payload
		media = append(media, MediaFile{
			Role:  "live_segment",
			Path:  name,
			Bytes: int64(len(payload)),
		})
		segments = append(segments, LiveSegmentInfo{
			File:  name,
			Bytes: int64(len(payload)),
		})
	}

	saved := saveSyntheticMediaItem(t, store, liveDir, ItemMetadata{
		Version:      1,
		ID:           "50001:1700000100",
		Site:         "bilibili",
		Kind:         "live",
		Status:       "complete",
		Title:        "合成直播录像六段测试",
		PubTs:        1700000100,
		DirectCached: true,
		Author:       ItemAuthor{Site: "bilibili", UID: "10001", Mid: 10001, Name: "合成主播"},
		Media:        media,
		Live: &LiveRecordingInfo{
			RoomID:    50001,
			LiveSince: 1700000100,
			Segments:  segments,
		},
	}, files)

	plan, err := store.PlanDelete(saved.ItemKey)
	if err != nil {
		t.Fatalf("PlanDelete: %v", err)
	}
	// Disk has exactly 8 files: cover.jpg + part-001.mp4..part-006.mp4 + item.json (no orig.json).
	if len(plan.RegisteredFiles) != 8 {
		t.Fatalf("PlanDelete RegisteredFiles count = %d, want 8; files = %v", len(plan.RegisteredFiles), plan.RegisteredFiles)
	}
	if len(plan.PreservedUserFiles) != 0 {
		t.Fatalf("PlanDelete PreservedUserFiles = %v, want empty", plan.PreservedUserFiles)
	}
	if filepath.Base(plan.RegisteredFiles[len(plan.RegisteredFiles)-1]) != "item.json" {
		t.Fatalf("item.json must be deleted last, got order %v", plan.RegisteredFiles)
	}
	for _, f := range plan.RegisteredFiles {
		if filepath.Base(f) == "orig.json" {
			t.Fatalf("non-existent orig.json leaked into RegisteredFiles: %v", plan.RegisteredFiles)
		}
	}

	res, err := store.Delete(saved.ItemKey)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(res.RegisteredFiles) != 8 {
		t.Fatalf("Delete RegisteredFiles count = %d, want 8", len(res.RegisteredFiles))
	}
	if _, err := os.Stat(liveDir); !os.IsNotExist(err) {
		t.Fatalf("expected live item dir removed, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "bilibili", "10001")); !os.IsNotExist(err) {
		t.Fatalf("expected empty author dir removed, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "bilibili")); err != nil {
		t.Fatalf("site root folder must be preserved: %v", err)
	}
}

func TestPlanDeleteForwardDynamicOrigJSONHandlingAndSafety(t *testing.T) {
	t.Run("forward_dynamic_with_manifest_orig_json", func(t *testing.T) {
		store, cacheDir := openBatchTestStore(t)
		dir := filepath.Join(cacheDir, "bilibili", "10002", "forwards", "800001")
		saved := saveSyntheticMediaItem(t, store, dir, ItemMetadata{
			Version:     1,
			ID:          "800001",
			Site:        "bilibili",
			Kind:        "dynamic",
			DynamicType: "DYNAMIC_TYPE_FORWARD",
			Status:      "complete",
			Title:       "转发动态含登记快照",
			PubTs:       1700000200,
			Author:      ItemAuthor{Site: "bilibili", UID: "10002", Mid: 10002, Name: "合成转发者"},
			Media: []MediaFile{
				{Role: "image", Path: "images/01.jpg"},
				{Role: "snapshot", Path: "orig.json"},
			},
		}, map[string][]byte{
			"images/01.jpg": []byte("synthetic-img-01"),
			"orig.json":     []byte(`{"id_str":"700001","type":"DYNAMIC_TYPE_DRAW"}`),
		})

		plan, err := store.PlanDelete(saved.ItemKey)
		if err != nil {
			t.Fatalf("PlanDelete: %v", err)
		}
		if len(plan.RegisteredFiles) != 3 {
			t.Fatalf("RegisteredFiles = %v (len=%d), want 3 (images/01.jpg, orig.json, item.json)", plan.RegisteredFiles, len(plan.RegisteredFiles))
		}
		if len(plan.PreservedUserFiles) != 0 {
			t.Fatalf("orig.json must not be classified as user file, got PreservedUserFiles=%v", plan.PreservedUserFiles)
		}
		if filepath.Base(plan.RegisteredFiles[len(plan.RegisteredFiles)-1]) != "item.json" {
			t.Fatalf("item.json must be last, got %v", plan.RegisteredFiles)
		}

		if _, err := store.Delete(saved.ItemKey); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("expected forward dir completely removed after deleting orig.json, stat err=%v", err)
		}
	})

	t.Run("legacy_forward_dynamic_with_unlisted_orig_json", func(t *testing.T) {
		store, cacheDir := openBatchTestStore(t)
		dir := filepath.Join(cacheDir, "bilibili", "10003", "forwards", "800002")
		saved := saveSyntheticMediaItem(t, store, dir, ItemMetadata{
			Version:     1,
			ID:          "800002",
			Site:        "bilibili",
			Kind:        "dynamic",
			DynamicType: "DYNAMIC_TYPE_FORWARD",
			Status:      "complete",
			Title:       "历史转发动态未在Media登记orig.json",
			PubTs:       1700000210,
			Author:      ItemAuthor{Site: "bilibili", UID: "10003", Mid: 10003, Name: "合成转发者"},
			Media: []MediaFile{
				{Role: "image", Path: "images/01.jpg"},
			},
		}, map[string][]byte{
			"images/01.jpg": []byte("synthetic-img-01"),
			"orig.json":     []byte(`{"id_str":"700002","type":"DYNAMIC_TYPE_WORD"}`),
		})

		plan, err := store.PlanDelete(saved.ItemKey)
		if err != nil {
			t.Fatalf("PlanDelete: %v", err)
		}
		if len(plan.RegisteredFiles) != 3 {
			t.Fatalf("RegisteredFiles = %v (len=%d), want 3 (images/01.jpg, orig.json, item.json)", plan.RegisteredFiles, len(plan.RegisteredFiles))
		}
		if len(plan.PreservedUserFiles) != 0 {
			t.Fatalf("legacy orig.json must not leak into PreservedUserFiles: %v", plan.PreservedUserFiles)
		}
		if _, err := store.Delete(saved.ItemKey); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("expected legacy forward dir removed, stat err=%v", err)
		}
	})

	t.Run("user_directory_named_orig_json_is_preserved", func(t *testing.T) {
		store, cacheDir := openBatchTestStore(t)
		dir := filepath.Join(cacheDir, "bilibili", "10004", "videos", "BV1synOrigDir")
		saved := saveSyntheticMediaItem(t, store, dir, ItemMetadata{
			Version:      1,
			ID:           "BV1synOrigDir",
			Site:         "bilibili",
			Kind:         "video",
			Status:       "complete",
			Title:        "含同名子目录orig.json的安全检查",
			PubTs:        1700000220,
			DirectCached: true,
			Author:       ItemAuthor{Site: "bilibili", UID: "10004", Mid: 10004, Name: "合成UP"},
			Media:        []MediaFile{{Role: "video", Path: "P01.mp4"}},
			Parts:        []VideoPartRecord{{CID: 1, Page: 1, File: "P01.mp4"}},
		}, map[string][]byte{
			"P01.mp4":             []byte("synthetic-mp4"),
			"orig.json/notes.txt": []byte("keep-user-note"),
		})

		plan, err := store.PlanDelete(saved.ItemKey)
		if err != nil {
			t.Fatalf("PlanDelete: %v", err)
		}
		if len(plan.RegisteredFiles) != 2 {
			t.Fatalf("RegisteredFiles = %v (len=%d), want 2 (P01.mp4, item.json)", plan.RegisteredFiles, len(plan.RegisteredFiles))
		}
		if len(plan.PreservedUserFiles) != 1 || plan.PreservedUserFiles[0] != "orig.json/notes.txt" {
			t.Fatalf("PreservedUserFiles = %v, want [orig.json/notes.txt]", plan.PreservedUserFiles)
		}
		if _, err := store.Delete(saved.ItemKey); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if raw, err := os.ReadFile(filepath.Join(dir, "orig.json", "notes.txt")); err != nil || string(raw) != "keep-user-note" {
			t.Fatalf("user file under orig.json/ was not preserved: err=%v data=%q", err, string(raw))
		}
	})
}

func TestPlanDeleteExcludesMissingFilesAndHandlesPendingPartsAndSubdirParts(t *testing.T) {
	t.Run("missing_manifest_file_and_missing_item_json_not_counted", func(t *testing.T) {
		store, cacheDir := openBatchTestStore(t)
		dir := filepath.Join(cacheDir, "bilibili", "10005", "lives", "50002-1700000300")
		saved := saveSyntheticMediaItem(t, store, dir, ItemMetadata{
			Version:      1,
			ID:           "50002:1700000300",
			Site:         "bilibili",
			Kind:         "live",
			Status:       "complete",
			Title:        "缺失部分文件的直播录像",
			PubTs:        1700000300,
			DirectCached: true,
			Author:       ItemAuthor{Site: "bilibili", UID: "10005", Mid: 10005, Name: "合成主播"},
			Media: []MediaFile{
				{Role: "cover", Path: "cover.jpg"},
				{Role: "live_segment", Path: "part-001.mp4"},
				{Role: "live_segment", Path: "part-002.mp4"},
			},
			Live: &LiveRecordingInfo{
				RoomID:    50002,
				LiveSince: 1700000300,
				Segments: []LiveSegmentInfo{
					{File: "part-001.mp4"},
					{File: "part-002.mp4"},
				},
			},
		}, map[string][]byte{
			"cover.jpg":    []byte("cover"),
			"part-001.mp4": []byte("seg1"),
			"part-002.mp4": []byte("seg2"),
		})

		// User removes part-002.mp4 externally in file explorer.
		if err := os.Remove(filepath.Join(dir, "part-002.mp4")); err != nil {
			t.Fatal(err)
		}
		plan, err := store.PlanDelete(saved.ItemKey)
		if err != nil {
			t.Fatalf("PlanDelete after removing part-002.mp4: %v", err)
		}
		if len(plan.RegisteredFiles) != 3 {
			t.Fatalf("RegisteredFiles = %v (len=%d), want 3 (cover.jpg, part-001.mp4, item.json)", plan.RegisteredFiles, len(plan.RegisteredFiles))
		}

		// User also removes item.json externally.
		if err := os.Remove(filepath.Join(dir, "item.json")); err != nil {
			t.Fatal(err)
		}
		planNoManifest, err := store.PlanDelete(saved.ItemKey)
		if err != nil {
			t.Fatalf("PlanDelete after removing item.json: %v", err)
		}
		if len(planNoManifest.RegisteredFiles) != 2 {
			t.Fatalf("RegisteredFiles = %v (len=%d), want 2 (cover.jpg, part-001.mp4)", planNoManifest.RegisteredFiles, len(planNoManifest.RegisteredFiles))
		}
		if _, err := store.Delete(saved.ItemKey); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("expected dir removed after deleting remaining files, stat err=%v", err)
		}
	})

	t.Run("pending_parts_absent_empty_part_and_renamed_mp4", func(t *testing.T) {
		store, cacheDir := openBatchTestStore(t)

		// 1. Stale PendingParts where neither .part nor .mp4 exists on disk.
		dirAbsent := filepath.Join(cacheDir, "bilibili", "10006", "lives", "50003-1700000310")
		absent := saveSyntheticMediaItem(t, store, dirAbsent, ItemMetadata{
			Version:      1,
			ID:           "50003:1700000310",
			Site:         "bilibili",
			Kind:         "live",
			Status:       "partial",
			Title:        "残留空记录PendingParts",
			PubTs:        1700000310,
			Author:       ItemAuthor{Site: "bilibili", UID: "10006", Mid: 10006, Name: "合成主播"},
			Media:        []MediaFile{{Role: "cover", Path: "cover.jpg"}, {Role: "live_segment", Path: "part-001.mp4"}},
			PendingParts: []string{"part-002.mp4.part"},
		}, map[string][]byte{
			"cover.jpg":    []byte("cover"),
			"part-001.mp4": []byte("seg1"),
		})
		planAbsent, err := store.PlanDelete(absent.ItemKey)
		if err != nil {
			t.Fatalf("PlanDelete absent pending: %v", err)
		}
		if len(planAbsent.RegisteredFiles) != 3 {
			t.Fatalf("absent pending RegisteredFiles = %v (len=%d), want 3", planAbsent.RegisteredFiles, len(planAbsent.RegisteredFiles))
		}

		// 2. Existing 0-byte .part in PendingParts (must be counted & deleted, not preserved as user file).
		dirZeroPart := filepath.Join(cacheDir, "bilibili", "10006", "lives", "50003-1700000320")
		zeroPart := saveSyntheticMediaItem(t, store, dirZeroPart, ItemMetadata{
			Version:      1,
			ID:           "50003:1700000320",
			Site:         "bilibili",
			Kind:         "live",
			Status:       "partial",
			Title:        "存在0字节part文件的PendingParts",
			PubTs:        1700000320,
			Author:       ItemAuthor{Site: "bilibili", UID: "10006", Mid: 10006, Name: "合成主播"},
			Media:        []MediaFile{{Role: "cover", Path: "cover.jpg"}, {Role: "live_segment", Path: "part-001.mp4"}},
			PendingParts: []string{"part-002.mp4.part"},
		}, map[string][]byte{
			"cover.jpg":         []byte("cover"),
			"part-001.mp4":      []byte("seg1"),
			"part-002.mp4.part": {}, // 0-byte regular file
		})
		planZero, err := store.PlanDelete(zeroPart.ItemKey)
		if err != nil {
			t.Fatalf("PlanDelete zero-byte pending: %v", err)
		}
		if len(planZero.RegisteredFiles) != 4 || len(planZero.PreservedUserFiles) != 0 {
			t.Fatalf("zero-byte pending plan = reg:%v user:%v, want 4 reg / 0 user", planZero.RegisteredFiles, planZero.PreservedUserFiles)
		}
		if _, err := store.Delete(zeroPart.ItemKey); err != nil {
			t.Fatalf("Delete zero-byte pending: %v", err)
		}
		if _, err := os.Stat(dirZeroPart); !os.IsNotExist(err) {
			t.Fatalf("expected dirZeroPart removed, stat err=%v", err)
		}

		// 3. Crashed after final rename: part-002.mp4 exists on disk while PendingParts still lists part-002.mp4.part.
		dirRenamed := filepath.Join(cacheDir, "bilibili", "10006", "lives", "50003-1700000330")
		renamed := saveSyntheticMediaItem(t, store, dirRenamed, ItemMetadata{
			Version:      1,
			ID:           "50003:1700000330",
			Site:         "bilibili",
			Kind:         "live",
			Status:       "partial",
			Title:        "改名后崩溃残留PendingParts",
			PubTs:        1700000330,
			Author:       ItemAuthor{Site: "bilibili", UID: "10006", Mid: 10006, Name: "合成主播"},
			Media:        []MediaFile{{Role: "cover", Path: "cover.jpg"}, {Role: "live_segment", Path: "part-001.mp4"}},
			PendingParts: []string{"part-002.mp4.part"},
		}, map[string][]byte{
			"cover.jpg":    []byte("cover"),
			"part-001.mp4": []byte("seg1"),
			"part-002.mp4": []byte("renamed-seg2"),
		})
		planRenamed, err := store.PlanDelete(renamed.ItemKey)
		if err != nil {
			t.Fatalf("PlanDelete renamed pending: %v", err)
		}
		if len(planRenamed.RegisteredFiles) != 4 || len(planRenamed.PreservedUserFiles) != 0 {
			t.Fatalf("renamed pending plan = reg:%v user:%v, want 4 reg / 0 user", planRenamed.RegisteredFiles, planRenamed.PreservedUserFiles)
		}
		if _, err := store.Delete(renamed.ItemKey); err != nil {
			t.Fatalf("Delete renamed pending: %v", err)
		}
		if _, err := os.Stat(dirRenamed); !os.IsNotExist(err) {
			t.Fatalf("expected dirRenamed removed, stat err=%v", err)
		}
	})

	t.Run("subdir_companion_part_cleaned_and_unregistered_user_files_preserved", func(t *testing.T) {
		store, cacheDir := openBatchTestStore(t)
		dir := filepath.Join(cacheDir, "bilibili", "10007", "dynamics", "900001")
		saved := saveSyntheticMediaItem(t, store, dir, ItemMetadata{
			Version: 1,
			ID:      "900001",
			Site:    "bilibili",
			Kind:    "dynamic",
			Status:  "partial",
			Title:   "子目录伴随part与用户文件保留",
			PubTs:   1700000340,
			Author:  ItemAuthor{Site: "bilibili", UID: "10007", Mid: 10007, Name: "合成UP"},
			Media: []MediaFile{
				{Role: "image", Path: "images/01.jpg"}, // missing on disk; images/01.jpg.part exists
			},
		}, map[string][]byte{
			"images/01.jpg.part":   []byte("partial-image-download"),
			"images/personal.part": []byte("user-owned-part"),
			"keep.txt":             []byte("user-note"),
		})

		plan, err := store.PlanDelete(saved.ItemKey)
		if err != nil {
			t.Fatalf("PlanDelete: %v", err)
		}
		// Only images/01.jpg.part + item.json actually exist on disk (not missing images/01.jpg or orig.json).
		if len(plan.RegisteredFiles) != 2 {
			t.Fatalf("RegisteredFiles = %v (len=%d), want 2 (images/01.jpg.part, item.json)", plan.RegisteredFiles, len(plan.RegisteredFiles))
		}
		wantUser := []string{"images/personal.part", "keep.txt"}
		gotUser := append([]string(nil), plan.PreservedUserFiles...)
		sort.Strings(gotUser)
		if !slices.Equal(gotUser, wantUser) {
			t.Fatalf("PreservedUserFiles = %v, want %v", gotUser, wantUser)
		}

		if _, err := store.Delete(saved.ItemKey); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "images", "01.jpg.part")); !os.IsNotExist(err) {
			t.Fatalf("expected images/01.jpg.part deleted, stat err=%v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "item.json")); !os.IsNotExist(err) {
			t.Fatalf("expected item.json deleted, stat err=%v", err)
		}
		for _, kept := range wantUser {
			if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(kept))); err != nil {
				t.Fatalf("expected user file %s preserved, err=%v", kept, err)
			}
		}
	})
}

func TestPlanDeleteBatchExactFileCountAcrossMixedItems(t *testing.T) {
	store, cacheDir := openBatchTestStore(t)

	// 1. Live recording with 6 segments + cover.jpg + item.json = 8 files, plus 1 user file.
	liveDir := filepath.Join(cacheDir, "bilibili", "20001", "lives", "60001-1700000400")
	liveFiles := map[string][]byte{
		"cover.jpg": []byte("live-cover"),
		"notes.txt": []byte("user-notes"),
	}
	liveMedia := []MediaFile{{Role: "cover", Path: "cover.jpg"}}
	liveSegs := make([]LiveSegmentInfo, 0, 6)
	for i := 1; i <= 6; i++ {
		name := fmt.Sprintf("part-%03d.mp4", i)
		liveFiles[name] = []byte(fmt.Sprintf("seg-%d", i))
		liveMedia = append(liveMedia, MediaFile{Role: "live_segment", Path: name})
		liveSegs = append(liveSegs, LiveSegmentInfo{File: name})
	}
	liveItem := saveSyntheticMediaItem(t, store, liveDir, ItemMetadata{
		Version:      1,
		ID:           "60001:1700000400",
		Site:         "bilibili",
		Kind:         "live",
		Status:       "complete",
		Title:        "批量测试直播录像",
		PubTs:        1700000400,
		DirectCached: true,
		Author:       ItemAuthor{Site: "bilibili", UID: "20001", Mid: 20001, Name: "合成主播"},
		Media:        liveMedia,
		Live:         &LiveRecordingInfo{RoomID: 60001, LiveSince: 1700000400, Segments: liveSegs},
	}, liveFiles)

	// 2. Forward dynamic with images/01.jpg + orig.json + item.json = 3 files.
	fwdDir := filepath.Join(cacheDir, "bilibili", "20001", "forwards", "810001")
	fwdItem := saveSyntheticMediaItem(t, store, fwdDir, ItemMetadata{
		Version:     1,
		ID:          "810001",
		Site:        "bilibili",
		Kind:        "dynamic",
		DynamicType: "DYNAMIC_TYPE_FORWARD",
		Status:      "complete",
		Title:       "批量测试转发动态",
		PubTs:       1700000410,
		Author:      ItemAuthor{Site: "bilibili", UID: "20001", Mid: 20001, Name: "合成主播"},
		Media: []MediaFile{
			{Role: "image", Path: "images/01.jpg"},
			{Role: "snapshot", Path: "orig.json"},
		},
	}, map[string][]byte{
		"images/01.jpg": []byte("fwd-img"),
		"orig.json":     []byte(`{"id_str":"710001"}`),
	})

	// 3. Exclusively referenced video: P01.mp4 + item.json = 2 files (no orig.json).
	vidDir := filepath.Join(cacheDir, "bilibili", "20001", "videos", "BV1synBatch01")
	exclVid := saveSyntheticMediaItem(t, store, vidDir, ItemMetadata{
		Version:      1,
		ID:           "BV1synBatch01",
		Site:         "bilibili",
		Kind:         "video",
		Status:       "complete",
		Title:        "批量测试独占引用视频",
		PubTs:        1700000420,
		DirectCached: false,
		Author:       ItemAuthor{Site: "bilibili", UID: "20001", Mid: 20001, Name: "合成主播"},
		Media:        []MediaFile{{Role: "video", Path: "P01.mp4"}},
		Parts:        []VideoPartRecord{{CID: 1, Page: 1, File: "P01.mp4"}},
	}, map[string][]byte{
		"P01.mp4": []byte("video-bytes"),
	})

	// 4. Dynamic referencing exclVid, with images/01.jpg present and images/02.jpg missing + item.json = 2 files.
	dynDir := filepath.Join(cacheDir, "bilibili", "20001", "dynamics", "810002")
	dynItem := saveSyntheticMediaItem(t, store, dynDir, ItemMetadata{
		Version: 1,
		ID:      "810002",
		Site:    "bilibili",
		Kind:    "dynamic",
		Status:  "complete",
		Title:   "批量测试含缺失图动态",
		PubTs:   1700000430,
		Author:  ItemAuthor{Site: "bilibili", UID: "20001", Mid: 20001, Name: "合成主播"},
		Media: []MediaFile{
			{Role: "image", Path: "images/01.jpg"},
			{Role: "image", Path: "images/02.jpg"},
		},
		Videos: []string{"BV1synBatch01"},
	}, map[string][]byte{
		"images/01.jpg": []byte("img1"),
		"images/02.jpg": []byte("img2"),
	})
	if err := os.Remove(filepath.Join(dynDir, "images", "02.jpg")); err != nil {
		t.Fatal(err)
	}

	keys := []string{liveItem.ItemKey, fwdItem.ItemKey, dynItem.ItemKey}
	plan, err := store.PlanDeleteBatch(keys)
	if err != nil {
		t.Fatalf("PlanDeleteBatch: %v", err)
	}
	if len(plan.ItemKeys) != 4 {
		t.Fatalf("ItemKeys = %v (len=%d), want 4 (including %s)", plan.ItemKeys, len(plan.ItemKeys), exclVid.ItemKey)
	}
	// Exact total on-disk registered files: 8 (live) + 3 (fwd) + 2 (dyn) + 2 (exclVid) = 15.
	if len(plan.RegisteredFiles) != 15 {
		t.Fatalf("PlanDeleteBatch RegisteredFiles count = %d, want 15; files = %v", len(plan.RegisteredFiles), plan.RegisteredFiles)
	}
	if len(plan.PreservedUserFiles) != 1 || plan.PreservedUserFiles[0] != "notes.txt" {
		t.Fatalf("PreservedUserFiles = %v, want [notes.txt]", plan.PreservedUserFiles)
	}

	res, err := store.DeleteBatch(keys, true)
	if err != nil {
		t.Fatalf("DeleteBatch: %v", err)
	}
	if len(res.RegisteredFiles) != 15 {
		t.Fatalf("DeleteBatch RegisteredFiles count = %d, want 15", len(res.RegisteredFiles))
	}
	if len(res.PreservedUserFiles) != 1 || res.PreservedUserFiles[0] != "notes.txt" {
		t.Fatalf("DeleteBatch PreservedUserFiles = %v, want [notes.txt]", res.PreservedUserFiles)
	}
	if raw, err := os.ReadFile(filepath.Join(liveDir, "notes.txt")); err != nil || string(raw) != "user-notes" {
		t.Fatalf("user file notes.txt not preserved: err=%v data=%q", err, string(raw))
	}
	for _, removedDir := range []string{fwdDir, vidDir, dynDir} {
		if _, err := os.Stat(removedDir); !os.IsNotExist(err) {
			t.Fatalf("expected dir %s removed, stat err=%v", removedDir, err)
		}
	}
}
