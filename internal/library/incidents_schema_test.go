package library

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestLiveIncidentsBackwardCompatibilityAndSanitization(t *testing.T) {
	// 1. Legacy item.json without live.incidents parses cleanly with Version == 1 unchanged.
	legacyJSON := `{
		"version": 1,
		"site": "bilibili",
		"kind": "live",
		"id": "9001-20261115",
		"title": "旧版直播录像",
		"author": {"mid": 1001, "name": "Streamer"},
		"pubTs": 1794024000,
		"url": "https://live.bilibili.com/9001",
		"cachedAt": "2026-11-15T08:00:00Z",
		"status": "complete",
		"live": {
			"roomId": 9001,
			"liveSince": 1794024000,
			"recordStartedAt": "2026-11-15T08:00:00Z",
			"recordEndedAt": "2026-11-15T08:10:00Z",
			"source": "manual"
		}
	}`
	legacyMeta, err := ParseItemMetadata([]byte(legacyJSON))
	if err != nil {
		t.Fatalf("ParseItemMetadata legacy item.json: %v", err)
	}
	if legacyMeta.Version != 1 {
		t.Fatalf("expected Version 1, got %d", legacyMeta.Version)
	}
	if legacyMeta.Live == nil || len(legacyMeta.Live.Incidents) != 0 {
		t.Fatalf("expected nil/empty incidents on legacy item.json, got %+v", legacyMeta.Live)
	}

	// Re-marshaling legacy metadata without incidents omits "incidents" field cleanly.
	reEncoded, err := json.Marshal(legacyMeta)
	if err != nil {
		t.Fatalf("Marshal legacyMeta: %v", err)
	}
	if strings.Contains(string(reEncoded), `"incidents"`) {
		t.Fatalf("expected empty incidents to be omitted in JSON, got %s", string(reEncoded))
	}

	// 2. Sanitization strips stream URLs, query parameters, directory paths on Part, Cookies, and caps at 200 entries.
	var noisy []LiveIncident
	noisy = append(noisy, LiveIncident{
		Kind:         LiveIncidentConvertFailed,
		At:           "2026-11-15T09:00:00Z",
		Part:         `C:\secret\dir\part-001.mp4.part`,
		Reason:       "转换失败 https://cn-hbwh-fx-01.bilivideo.com/live-bvc/123/index.m3u8?upsig=abc&trid=99 Cookie: SESSDATA=secret_sess; sessionid=dy_sec",
		DurationSec:  12.34,
		LostSegments: -5,
		Count:        0,
	})
	noisy = append(noisy, LiveIncident{
		Kind:   LiveIncidentReconnect,
		At:     "2026-11-15T09:05:00Z",
		Part:   "https://cn-hbwh-fx-01.bilivideo.com/live-bvc/seg1.m4s?upsig=secret123&expires=1999",
		Reason: "重连",
	})
	for i := 0; i < 248; i++ {
		noisy = append(noisy, LiveIncident{
			Kind:        LiveIncidentReconnect,
			At:          fmt.Sprintf("2026-11-15T10:%02d:%02dZ", (i/60)%60, i%60),
			DurationSec: 3,
			Count:       1,
		})
	}

	meta := ItemMetadata{
		Version:  1,
		Site:     "bilibili",
		Kind:     "live",
		RawID:    "9002-20261115",
		Title:    "带异常的直播录像",
		Author:   ItemAuthor{Mid: 1002, Name: "Streamer2"},
		PubTs:    1794025000,
		URL:      "https://live.bilibili.com/9002",
		CachedAt: "2026-11-15T09:00:00Z",
		Status:   "partial",
		Live: &LiveRecordingInfo{
			RoomID:    9002,
			StartedAt: "2026-11-15T08:50:00Z",
			Source:    "manual",
			Incidents: noisy,
		},
	}
	SanitizeItemMetadata(&meta)

	if len(meta.Live.Incidents) != MaxLiveIncidentsPerSession {
		t.Fatalf("expected incidents capped at %d, got %d", MaxLiveIncidentsPerSession, len(meta.Live.Incidents))
	}
	first := meta.Live.Incidents[0]
	if first.Part != "part-001.mp4.part" {
		t.Fatalf("expected Part basename stripped of dir, got %q", first.Part)
	}
	if meta.Live.Incidents[1].Part != "" {
		t.Fatalf("expected signed URL in Part cleared to empty, got %q", meta.Live.Incidents[1].Part)
	}
	if first.Count != 1 || first.LostSegments != 0 {
		t.Fatalf("expected normalized Count=1 LostSegments=0, got Count=%d LostSegments=%d", first.Count, first.LostSegments)
	}
	rawOut, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("Marshal sanitized meta: %v", err)
	}
	outStr := string(rawOut)
	for _, forbidden := range []string{"bilivideo.com", "upsig=", "expires=", "SESSDATA", "sessionid", `C:\secret`} {
		if strings.Contains(outStr, forbidden) {
			t.Fatalf("sanitized item.json leaked %q: %s", forbidden, outStr)
		}
	}
}
