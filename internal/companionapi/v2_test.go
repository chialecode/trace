package companionapi

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestV2PathsAndCapabilities(t *testing.T) {
	if ProtocolVersion != "1" {
		t.Fatalf("ProtocolVersion = %q, want 1 (additive compatibility)", ProtocolVersion)
	}
	caps := V2Capabilities()
	for _, want := range []string{
		CapParseAndCache,
		CapSubscriptionTags,
		CapConflictResolve,
		CapPlaybackExtras,
		CapLivePreview,
		CapMDNS,
		CapPreventSleep,
	} {
		if !slices.Contains(caps, want) {
			t.Fatalf("V2Capabilities() missing %q: %v", want, caps)
		}
	}

	if got := SubscriptionTagsPath("sub:1/a"); got != "/api/v1/subscriptions/sub:1%2Fa/tags" {
		t.Fatalf("SubscriptionTagsPath = %q", got)
	}
	if got := ConflictResolvePath("cf/123"); got != "/api/v1/conflicts/cf%2F123/resolve" {
		t.Fatalf("ConflictResolvePath = %q", got)
	}
	if got := LiveRecordingPreviewPath("bilibili:1001:2002"); got != "/api/v1/live/recordings/bilibili:1001:2002/preview" {
		t.Fatalf("LiveRecordingPreviewPath = %q", got)
	}
	if got := LiveRecordingPreviewHLSPath("bilibili:1001:2002"); got != "/api/v1/live/recordings/bilibili:1001:2002/preview.m3u8" {
		t.Fatalf("LiveRecordingPreviewHLSPath = %q", got)
	}
}

func TestV2DTORoundTrip(t *testing.T) {
	// 1. Explicit empty tags ([]string{}) vs omitted tags (nil with preset).
	emptyReq := UpdateSubscriptionTagsRequest{Tags: []string{}}
	rawEmpty, err := json.Marshal(emptyReq)
	if err != nil {
		t.Fatal(err)
	}
	var decEmpty UpdateSubscriptionTagsRequest
	if err := json.Unmarshal(rawEmpty, &decEmpty); err != nil {
		t.Fatal(err)
	}
	if decEmpty.Tags == nil || len(decEmpty.Tags) != 0 {
		t.Fatalf("empty tags should decode as non-nil empty slice, got %#v (raw=%s)", decEmpty.Tags, rawEmpty)
	}

	autoRec := true
	presetReq := UpdateSubscriptionTagsRequest{Preset: PresetFullArchive, AutoRecord: &autoRec}
	rawPreset, err := json.Marshal(presetReq)
	if err != nil {
		t.Fatal(err)
	}
	var decPreset UpdateSubscriptionTagsRequest
	if err := json.Unmarshal(rawPreset, &decPreset); err != nil {
		t.Fatal(err)
	}
	if decPreset.Tags != nil || decPreset.Preset != PresetFullArchive || decPreset.AutoRecord == nil || !*decPreset.AutoRecord {
		t.Fatalf("preset decode mismatch: %+v", decPreset)
	}

	// 2. VideoPartDTO playback extras round-trip.
	part := VideoPartDTO{
		Page:           1,
		Title:          "P1",
		Duration:       120,
		File:           "P01.mp4",
		DanmakuRelPath: "P01.danmaku.xml",
		Subtitles: []SubtitleTrackDTO{
			{Lang: "zh-CN", Label: "中文", RelPath: "P01.zh-CN.vtt", VTT: "P01.zh-CN.vtt", SRT: "P01.zh-CN.srt"},
		},
		Chapters: []ChapterDTO{
			{FromSec: 0, ToSec: 30, Title: "片头"},
			{FromSec: 30, ToSec: 120, Title: "正文"},
		},
	}
	rawPart, err := json.Marshal(part)
	if err != nil {
		t.Fatal(err)
	}
	var decPart VideoPartDTO
	if err := json.Unmarshal(rawPart, &decPart); err != nil {
		t.Fatal(err)
	}
	if decPart.DanmakuRelPath != "P01.danmaku.xml" || len(decPart.Subtitles) != 1 || len(decPart.Chapters) != 2 {
		t.Fatalf("VideoPartDTO round-trip mismatch: %+v", decPart)
	}

	// 3. LiveRecordingDTO preview fields round-trip.
	rec := LiveRecordingDTO{
		RecordingKey:     "bilibili:1:2",
		Recording:        true,
		PreviewAvailable: true,
		PartIndex:        2,
		PartBytes:        4096,
	}
	rawRec, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	var decRec LiveRecordingDTO
	if err := json.Unmarshal(rawRec, &decRec); err != nil {
		t.Fatal(err)
	}
	if !decRec.PreviewAvailable || decRec.PartIndex != 2 || decRec.PartBytes != 4096 {
		t.Fatalf("LiveRecordingDTO round-trip mismatch: %+v", decRec)
	}
}
