package library

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestItemEditHistory_SanitizePortableVisitAndKeepListed(t *testing.T) {
	meta := ItemMetadata{
		Version:   1,
		ID:        "5001002003004005",
		Site:      "weibo",
		Kind:      "dynamic",
		Status:    "complete",
		Title:     "已编辑示例微博",
		Text:      "最终版本正文",
		URL:       "https://weibo.com/1000000001/O00000001",
		PubTs:     1764200000,
		EditCount: 2,
		Media: []MediaFile{
			{Role: "image", Path: "images/01.jpg", Bytes: 1200},
			{Role: "edit_image", Path: "images/edit_v01_01.jpg", Bytes: 900},
		},
		EditHistory: []ItemEditRevision{
			{
				Version: 3,
				EditTs:  1764203600,
				Text:    "最终版本正文",
				Links: []ItemLink{
					{Label: "项目说明", URL: "https://example.org/doc"},
					{Label: "坏签名", URL: "https://video.weibo.com/a.mp4?ssig=SECRET&Expires=999"},
				},
				MediaPaths: []string{
					"images/01.jpg",
					"images/01.jpg",                          // duplicate -> deduplicated
					"https://wx1.sinaimg.cn/large/pic01.jpg", // remote URL -> stripped
					"../secret.bin",                          // traversal -> stripped
				},
			},
			{
				Version: 1,
				EditTs:  1764200000,
				Text:    "原始版本正文 SUB=LEAKED_COOKIE",
				MediaPaths: []string{
					"images/edit_v01_01.jpg",
					"images/edit_v01_02.jpg?ssig=BAD",
				},
			},
			{
				// Empty revision -> dropped
				Version: 0,
				EditTs:  -5,
			},
		},
	}

	SanitizeItemMetadata(&meta)
	if meta.Version != 1 {
		t.Fatalf("schema version changed: got %d, want 1", meta.Version)
	}
	if len(meta.EditHistory) != 2 {
		t.Fatalf("expected 2 sanitized revisions, got %d: %+v", len(meta.EditHistory), meta.EditHistory)
	}
	if len(meta.EditHistory[0].Links) != 1 || meta.EditHistory[0].Links[0].URL != "https://example.org/doc" {
		t.Fatalf("expected bad signed link stripped, got %+v", meta.EditHistory[0].Links)
	}
	if len(meta.EditHistory[0].MediaPaths) != 1 || meta.EditHistory[0].MediaPaths[0] != "images/01.jpg" {
		t.Fatalf("expected only clean rel path, got %+v", meta.EditHistory[0].MediaPaths)
	}
	if len(meta.EditHistory[1].MediaPaths) != 1 || meta.EditHistory[1].MediaPaths[0] != "images/edit_v01_01.jpg" {
		t.Fatalf("expected only safe edit_image rel path, got %+v", meta.EditHistory[1].MediaPaths)
	}

	// RegisteredFiles should include both images/01.jpg (role=image) and images/edit_v01_01.jpg (role=edit_image).
	reg := RegisteredFiles(meta)
	roles := map[string]string{}
	for _, f := range reg {
		roles[f.Path] = f.Role
	}
	if roles["images/01.jpg"] != "image" {
		t.Fatalf("expected images/01.jpg role=image, got %q", roles["images/01.jpg"])
	}
	if roles["images/edit_v01_01.jpg"] != "edit_image" {
		t.Fatalf("expected images/edit_v01_01.jpg role=edit_image, got %q", roles["images/edit_v01_01.jpg"])
	}

	// PortableManifest roundtrip via ParseItemMetadata.
	raw, err := PortableManifest(meta)
	if err != nil {
		t.Fatalf("PortableManifest: %v", err)
	}
	if strings.Contains(string(raw), "ssig=") || strings.Contains(string(raw), "sinaimg.cn") || strings.Contains(string(raw), "secret.bin") {
		t.Fatalf("PortableManifest leaked unsafe values: %s", string(raw))
	}
	parsed, err := ParseItemMetadata(raw)
	if err != nil {
		t.Fatalf("ParseItemMetadata: %v", err)
	}
	if len(parsed.EditHistory) != 2 || parsed.EditHistory[1].MediaPaths[0] != "images/edit_v01_01.jpg" {
		t.Fatalf("roundtrip mismatch: %+v", parsed.EditHistory)
	}

	// UnsafeManifestPaths detects unsafe rel paths in EditHistory before sanitize.
	unsafe := meta
	unsafe.EditHistory = []ItemEditRevision{{Version: 1, MediaPaths: []string{"../escape.jpg"}}}
	if bad := UnsafeManifestPaths(unsafe); len(bad) == 0 {
		t.Fatalf("expected UnsafeManifestPaths to catch traversal path in EditHistory")
	}

	// KeepListedFiles drops missing historical media path while preserving text.
	kept, dropped := KeepListedFiles(meta, func(rel string) bool {
		return rel == "images/01.jpg"
	})
	if len(dropped) == 0 {
		t.Fatalf("expected images/edit_v01_01.jpg to be dropped")
	}
	if len(kept.EditHistory) != 2 {
		t.Fatalf("expected revision text preserved when image dropped, got %+v", kept.EditHistory)
	}
	if len(kept.EditHistory[1].MediaPaths) != 0 {
		t.Fatalf("expected unverified historical media path removed, got %+v", kept.EditHistory[1].MediaPaths)
	}
}

func TestItemEditHistory_MergeAndClone(t *testing.T) {
	local := ItemMetadata{
		Version:   1,
		ID:        "5001002003004005",
		Site:      "weibo",
		Kind:      "dynamic",
		EditCount: 1,
		EditHistory: []ItemEditRevision{
			{
				Version:    2,
				EditTs:     1764203600,
				Text:       "第二版正文",
				MediaPaths: []string{"images/01.jpg"},
			},
		},
	}
	imported := ItemMetadata{
		Version:   1,
		ID:        "5001002003004005",
		Site:      "weibo",
		Kind:      "dynamic",
		EditCount: 2,
		EditHistory: []ItemEditRevision{
			{
				Version:    2,
				EditTs:     1764203600,
				Text:       "第二版正文",
				Links:      []ItemLink{{Label: "参考", URL: "https://example.org/ref"}},
				MediaPaths: []string{"images/01.jpg", "images/edit_v02_02.jpg"},
			},
			{
				Version:    1,
				EditTs:     1764200000,
				Text:       "原始第一版正文",
				MediaPaths: []string{"images/edit_v01_01.jpg"},
			},
		},
	}

	merged, _ := MergeItemMetadata(local, imported, MergeUnion, nil)
	if len(merged.EditHistory) != 2 {
		t.Fatalf("expected 2 merged revisions, got %+v", merged.EditHistory)
	}
	if merged.EditHistory[0].Version != 2 || len(merged.EditHistory[0].Links) != 1 || len(merged.EditHistory[0].MediaPaths) != 2 {
		t.Fatalf("expected v2 supplemented with links & media, got %+v", merged.EditHistory[0])
	}
	if merged.EditHistory[1].Version != 1 || merged.EditHistory[1].Text != "原始第一版正文" {
		t.Fatalf("expected v1 appended from imported, got %+v", merged.EditHistory[1])
	}

	// Verify deep copy independence.
	merged.EditHistory[0].MediaPaths[0] = "mutated.jpg"
	if local.EditHistory[0].MediaPaths[0] != "images/01.jpg" {
		t.Fatalf("MergeItemMetadata mutated local slice")
	}

	raw, _ := json.Marshal(merged)
	if !strings.Contains(string(raw), `"editHistory"`) {
		t.Fatalf("expected editHistory field in JSON: %s", string(raw))
	}
}
