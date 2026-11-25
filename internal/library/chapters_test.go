package library

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/chialecode/trace/internal/storage"
)

func TestVideoPartChaptersBackwardCompatSanitizeAndMerge(t *testing.T) {
	tmp := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	cacheDir := filepath.Join(tmp, "cache")
	st, err := Open(db, cacheDir)
	if err != nil {
		t.Fatalf("library.Open: %v", err)
	}

	// 1. Backward compatibility: legacy v1 item.json without "chapters" unmarshals cleanly.
	legacyJSON := `{
		"version": 1,
		"id": "video:BV1legacy0001",
		"site": "bilibili",
		"kind": "video",
		"status": "complete",
		"author": {"mid": 888, "name": "UP主"},
		"title": "旧版视频清单",
		"pubTs": 1700000000,
		"cachedAt": "2026-01-01T00:00:00Z",
		"parts": [{"page": 1, "cid": 1001, "title": "P1", "file": "p01.mp4"}]
	}`
	var legacy ItemMetadata
	if err := json.Unmarshal([]byte(legacyJSON), &legacy); err != nil {
		t.Fatalf("unmarshal legacy item.json: %v", err)
	}
	if legacy.Version != 1 || len(legacy.Parts) != 1 || len(legacy.Parts[0].Chapters) != 0 {
		t.Fatalf("unexpected legacy manifest: %+v", legacy)
	}

	// 2. SaveItem sanitizes chapters and persists version 1 with chapters.
	itemDir := filepath.Join(cacheDir, "bilibili", "888_UP主", "video_BV1chap000001")
	if err := os.MkdirAll(itemDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(itemDir, "p01.mp4"), []byte("synthetic"), 0o600); err != nil {
		t.Fatalf("write mp4: %v", err)
	}
	item := ItemMetadata{
		Version:  1,
		ID:       "video:BV1chap000001",
		RawID:    "BV1chap000001",
		Site:     "bilibili",
		Kind:     "video",
		Status:   "complete",
		Author:   ItemAuthor{Site: "bilibili", UID: "888", Mid: 888, Name: "UP主"},
		Title:    "带章节视频",
		PubTs:    1700000100,
		CachedAt: "2026-01-01T00:00:00Z",
		Parts: []VideoPartRecord{
			{
				Page:  1,
				CID:   2001,
				Title: "P1",
				File:  "p01.mp4",
				Chapters: []VideoChapter{
					{FromSec: 60, ToSec: 120, Title: "  第二节  "},
					{FromSec: 0, ToSec: 60, Title: "第一节"},
					{FromSec: -1, ToSec: 10, Title: "非法负秒"},
					{FromSec: 30, ToSec: 30, Title: "非法零长"},
					{FromSec: 120, ToSec: 180, Title: "   "},
				},
			},
		},
	}
	saved, err := st.SaveItem(itemDir, item)
	if err != nil {
		t.Fatalf("SaveItem: %v", err)
	}
	got, ok, err := st.GetItem(saved.ItemKey)
	if err != nil || !ok {
		t.Fatalf("GetItem: ok=%v err=%v", ok, err)
	}
	if got.Version != 1 {
		t.Fatalf("expected schema version 1, got %d", got.Version)
	}
	if len(got.Parts) != 1 || len(got.Parts[0].Chapters) != 2 {
		t.Fatalf("expected 2 sanitized chapters, got %+v", got.Parts)
	}
	if got.Parts[0].Chapters[0] != (VideoChapter{FromSec: 0, ToSec: 60, Title: "第一节"}) ||
		got.Parts[0].Chapters[1] != (VideoChapter{FromSec: 60, ToSec: 120, Title: "第二节"}) {
		t.Fatalf("unexpected sanitized chapters order/content: %+v", got.Parts[0].Chapters)
	}

	// Verify item.json on disk contains chapters and stays version 1.
	raw, err := os.ReadFile(filepath.Join(itemDir, "item.json"))
	if err != nil {
		t.Fatalf("ReadFile item.json: %v", err)
	}
	var diskMeta ItemMetadata
	if err := json.Unmarshal(raw, &diskMeta); err != nil {
		t.Fatalf("unmarshal disk item.json: %v", err)
	}
	if diskMeta.Version != 1 || len(diskMeta.Parts) != 1 || len(diskMeta.Parts[0].Chapters) != 2 {
		t.Fatalf("expected v1 with 2 chapters on disk, got %+v", diskMeta)
	}

	// 3. MergeItemMetadata: local wins when present, and fills empty local chapters from imported.
	incomingEmpty := got
	incomingEmpty.Parts = []VideoPartRecord{{Page: 1, CID: 2001, Title: "P1", File: "p01.mp4"}}
	merged, _ := MergeItemMetadata(got, incomingEmpty, "", nil)
	if len(merged.Parts[0].Chapters) != 2 {
		t.Fatalf("MergeItemMetadata should preserve existing chapters when incoming is empty, got %+v", merged.Parts[0].Chapters)
	}
	localEmpty := got
	localEmpty.Parts = []VideoPartRecord{{Page: 1, CID: 2001, Title: "P1", File: "p01.mp4"}}
	incomingFilled := got
	incomingFilled.Parts = []VideoPartRecord{{
		Page:     1,
		CID:      2001,
		Title:    "P1",
		File:     "p01.mp4",
		Chapters: []VideoChapter{{FromSec: 0, ToSec: 90, Title: "新章节"}},
	}}
	merged2, _ := MergeItemMetadata(localEmpty, incomingFilled, "", nil)
	if len(merged2.Parts[0].Chapters) != 1 || merged2.Parts[0].Chapters[0].Title != "新章节" {
		t.Fatalf("MergeItemMetadata should fill empty local chapters from imported, got %+v", merged2.Parts[0].Chapters)
	}
}
