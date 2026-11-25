package library

import (
	"reflect"
	"testing"
)

func TestManifestConflictFieldsSanitizeMergeAndPortable(t *testing.T) {
	if CurrentCacheStandardVersion != 2 {
		t.Fatalf("CurrentCacheStandardVersion = %d, want 2", CurrentCacheStandardVersion)
	}

	meta := ItemMetadata{
		Version:              1,
		ID:                   "dynamic:7300000000000000001__v1-1763900000",
		Site:                 "douyin",
		Kind:                 "dynamic",
		Status:               "complete",
		Title:                "archived v1",
		PubTs:                1763800000,
		CachedAt:             "2026-11-24T10:00:00Z",
		CacheStandardVersion: CurrentCacheStandardVersion,
		RecheckedAt:          "2026-11-24T12:00:00Z",
		EditTs:               1763850000,
		EditCount:            2,
		AssetIDs: []string{
			"v0200fg10000abcdefghij",
			"1034:5000000000000020",
			"3_1800000000000000001",
			"https://v3-web.douyinvod.com/video.mp4?x-signature=secret",
			"tos-cn-i-0813/with-slash",
			"..\\escape",
			"sessdata=secret_cookie",
			"video.twimg.com",
		},
		ArchivedOf: "douyin:note:7300000000000000001",
		DouyinVideo: &DouyinVideoInfo{
			MediaID: "v0200fg10000abcdefghij",
			File:    "video.mp4",
		},
		NotePages: []NotePageRecord{
			{
				Index:   1,
				Kind:    "motion",
				MediaID: "005Xabc1gy1clean001",
				Image:   "images/01.jpg",
				Motion:  "images/01_live.mp4",
				VideoInfo: &DouyinVideoInfo{
					MediaID: "https://weibocdn.com/signed.mp4?ssig=abc",
					File:    "images/01_live.mp4",
				},
			},
			{
				Index:   2,
				Kind:    "image",
				MediaID: "bad/path?a_bogus=1",
				Image:   "images/02.jpg",
			},
		},
	}

	SanitizeItemMetadata(&meta)

	wantAssets := []string{
		"v0200fg10000abcdefghij",
		"1034:5000000000000020",
		"3_1800000000000000001",
	}
	if !reflect.DeepEqual(meta.AssetIDs, wantAssets) {
		t.Fatalf("AssetIDs = %#v, want %#v", meta.AssetIDs, wantAssets)
	}
	if meta.ArchivedOf != "douyin:dynamic:7300000000000000001" || !meta.IsArchivedSnapshot() {
		t.Fatalf("ArchivedOf = %q, want canonical douyin:dynamic:7300000000000000001", meta.ArchivedOf)
	}
	if meta.DouyinVideo.MediaID != "v0200fg10000abcdefghij" {
		t.Fatalf("DouyinVideo.MediaID = %q, want clean ID", meta.DouyinVideo.MediaID)
	}
	if meta.NotePages[0].MediaID != "005Xabc1gy1clean001" {
		t.Fatalf("NotePages[0].MediaID = %q, want clean ID", meta.NotePages[0].MediaID)
	}
	if meta.NotePages[0].VideoInfo.MediaID != "" {
		t.Fatalf("NotePages[0].VideoInfo.MediaID = %q, want scrubbed empty", meta.NotePages[0].VideoInfo.MediaID)
	}
	if meta.NotePages[1].MediaID != "" {
		t.Fatalf("NotePages[1].MediaID = %q, want scrubbed empty", meta.NotePages[1].MediaID)
	}

	// Reject invalid or self-referential ArchivedOf and signed RecheckedAt.
	for _, badArchive := range []string{
		"https://www.bilibili.com/video/BV1xx411c7mD",
		"bilibili:video:../escape",
		"unknown:video:BV1xx411c7mD",
		"not-canonical",
		"douyin:dynamic:7300000000000000001__v1-1763900000", // self reference
	} {
		check := meta
		check.ArchivedOf = badArchive
		check.RecheckedAt = "https://example.com/?sessdata=bad"
		SanitizeItemMetadata(&check)
		if check.ArchivedOf != "" {
			t.Fatalf("ArchivedOf(%q) = %q, want empty", badArchive, check.ArchivedOf)
		}
		if check.RecheckedAt != "" {
			t.Fatalf("RecheckedAt with URL was not scrubbed: %q", check.RecheckedAt)
		}
	}

	// MergeKeepLocal supplements missing fields from imported and PortableManifest round-trips cleanly.
	bare := ItemMetadata{
		Version: 1,
		ID:      meta.ID,
		Site:    "douyin",
		Kind:    "dynamic",
		Status:  "complete",
		DouyinVideo: &DouyinVideoInfo{
			File: "video.mp4",
		},
		NotePages: []NotePageRecord{
			{
				Index:     1,
				Kind:      "motion",
				Image:     "images/01.jpg",
				Motion:    "images/01_live.mp4",
				VideoInfo: &DouyinVideoInfo{File: "images/01_live.mp4"},
			},
		},
	}
	meta.NotePages[0].VideoInfo.MediaID = "live_clip_01"
	merged, _ := MergeItemMetadata(bare, meta, MergeKeepLocal, nil)
	if merged.CacheStandardVersion != CurrentCacheStandardVersion ||
		merged.RecheckedAt != "2026-11-24T12:00:00Z" ||
		merged.EditTs != 1763850000 ||
		merged.EditCount != 2 ||
		merged.ArchivedOf != "douyin:dynamic:7300000000000000001" ||
		!reflect.DeepEqual(merged.AssetIDs, wantAssets) ||
		merged.DouyinVideo.MediaID != "v0200fg10000abcdefghij" ||
		merged.NotePages[0].MediaID != "005Xabc1gy1clean001" ||
		merged.NotePages[0].VideoInfo.MediaID != "live_clip_01" {
		t.Fatalf("MergeItemMetadata did not supplement conflict/manifest fields: %+v", merged)
	}

	raw, err := PortableManifest(merged)
	if err != nil {
		t.Fatalf("PortableManifest: %v", err)
	}
	reparsed, err := ParseItemMetadata(raw)
	if err != nil {
		t.Fatalf("ParseItemMetadata(PortableManifest): %v", err)
	}
	if reparsed.CacheStandardVersion != CurrentCacheStandardVersion ||
		reparsed.RecheckedAt != "2026-11-24T12:00:00Z" ||
		reparsed.EditTs != 1763850000 ||
		reparsed.EditCount != 2 ||
		reparsed.ArchivedOf != "douyin:dynamic:7300000000000000001" ||
		!reflect.DeepEqual(reparsed.AssetIDs, wantAssets) ||
		reparsed.DouyinVideo.MediaID != "v0200fg10000abcdefghij" ||
		reparsed.NotePages[0].MediaID != "005Xabc1gy1clean001" ||
		reparsed.NotePages[0].VideoInfo.MediaID != "live_clip_01" {
		t.Fatalf("PortableManifest round-trip lost fields: %+v", reparsed)
	}
}
