package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site/bilibili"
)

func newDiffTestBackend(t *testing.T, biliBaseURL string) *Backend {
	t.Helper()
	loc := testLocations(t)
	opts := Options{
		Locations:        loc,
		Desktop:          NewMockDesktop(),
		DisableBgWorkers: true,
	}
	if biliBaseURL != "" {
		opts.BilibiliOpts = bilibili.ClientOptions{
			Endpoints: bilibili.Endpoints{
				APIBase: biliBaseURL, PassportBase: biliBaseURL, WWWBase: biliBaseURL,
				SpaceBase: biliBaseURL, DynamicBase: biliBaseURL, LiveAPIBase: biliBaseURL, LiveWebBase: biliBaseURL,
			},
			PacingInterval: -1,
			AllowLocalhost: true,
		}
	}
	b, err := NewBackend(opts)
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

func TestDiffTextSideBySideReconstructsVerbatimAndHighlightsEdits(t *testing.T) {
	cases := []struct {
		name        string
		local       string
		remote      string
		wantChanged bool
	}{
		{
			name:        "identical text",
			local:       "原始标题与正文保持不变",
			remote:      "原始标题与正文保持不变",
			wantChanged: false,
		},
		{
			name:        "chinese and ascii inline edit",
			local:       "第一版发布说明：支持 1080P 清晰度，共 2 张配图。",
			remote:      "第二版修订说明：支持 4K HDR 清晰度，新增 3 张配图。",
			wantChanged: true,
		},
		{
			name:        "multiline insertion and deletion",
			local:       "第一行不变\n旧的第二行\n第三行不变",
			remote:      "第一行不变\n新的第二行\n追加第四行\n第三行不变",
			wantChanged: true,
		},
		{
			name:        "empty local",
			local:       "",
			remote:      "新增正文",
			wantChanged: true,
		},
		{
			name:        "empty remote",
			local:       "旧正文",
			remote:      "",
			wantChanged: true,
		},
	}

	concat := func(segs []ConflictTextSegment) string {
		var sb strings.Builder
		for _, s := range segs {
			sb.WriteString(s.Text)
		}
		return sb.String()
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			changed, locSegs, remSegs := DiffTextSideBySide(tc.local, tc.remote)
			if changed != tc.wantChanged {
				t.Fatalf("changed = %v, want %v", changed, tc.wantChanged)
			}
			if got := concat(locSegs); got != tc.local {
				t.Fatalf("local reconstruction = %q, want %q", got, tc.local)
			}
			if got := concat(remSegs); got != tc.remote {
				t.Fatalf("remote reconstruction = %q, want %q", got, tc.remote)
			}
			if tc.wantChanged {
				hasHighlight := false
				for _, s := range locSegs {
					if s.Kind == "removed" {
						hasHighlight = true
					}
					if s.Kind == "added" {
						t.Fatalf("local side must not emit added segment: %+v", s)
					}
				}
				for _, s := range remSegs {
					if s.Kind == "added" {
						hasHighlight = true
					}
					if s.Kind == "removed" {
						t.Fatalf("remote side must not emit removed segment: %+v", s)
					}
				}
				if !hasHighlight {
					t.Fatalf("expected removed/added highlight segments, got local=%+v remote=%+v", locSegs, remSegs)
				}
			}
		})
	}
}

func TestAlignConflictMediaPairs(t *testing.T) {
	local := []ConflictMediaSideDTO{
		{Kind: "video", Label: "P01 旧版", CID: 101, DurationSec: 120},
		{Kind: "video", Label: "P02 已保留", CID: 102, DurationSec: 90},
	}
	remote := []ConflictMediaSideDTO{
		{Kind: "video", Label: "P01 重制版", CID: 209, DurationSec: 135},
		{Kind: "video", Label: "P02 已保留", CID: 102, DurationSec: 90},
		{Kind: "video", Label: "P03 新增勘误", CID: 210, DurationSec: 45},
	}

	pairs := alignConflictMediaPairs(local, remote)
	if len(pairs) != 3 {
		t.Fatalf("len(pairs) = %d, want 3: %+v", len(pairs), pairs)
	}
	if pairs[0].Status != ConflictDiffStatusChanged || pairs[0].Local == nil || pairs[0].Remote == nil || pairs[0].Local.CID != 101 || pairs[0].Remote.CID != 209 {
		t.Fatalf("pair[0] = %+v, want changed 101 -> 209", pairs[0])
	}
	if pairs[1].Status != ConflictDiffStatusSame || pairs[1].Local == nil || pairs[1].Remote == nil || pairs[1].Local.CID != 102 {
		t.Fatalf("pair[1] = %+v, want same cid=102", pairs[1])
	}
	if pairs[2].Status != ConflictDiffStatusRemoteOnly || pairs[2].Local != nil || pairs[2].Remote == nil || pairs[2].Remote.CID != 210 {
		t.Fatalf("pair[2] = %+v, want remote_only cid=210", pairs[2])
	}
}

func TestInspectCacheConflictDiffRequiresLoginGracefullyAndPreservesLocalSide(t *testing.T) {
	b := newDiffTestBackend(t, "")
	meta := library.ItemMetadata{
		Version: 1,
		Site:    "bilibili",
		Kind:    "video",
		ID:      "video:BV1DIFFTEST1",
		ItemKey: "bilibili:video:BV1DIFFTEST1",
		RawID:   "BV1DIFFTEST1",
		Title:   "本地缓存标题 V1",
		Desc:    "本地缓存简介 V1",
		Status:  "complete",
		Parts: []library.VideoPartRecord{
			{Page: 1, CID: 101, Title: "初版分P", Duration: 120, File: "P01.mp4"},
		},
		Media: []library.MediaFile{
			{Role: "cover", Path: "cover.jpg"},
			{Role: "video", Path: "P01.mp4"},
		},
	}
	if _, err := b.lib.SaveItem(t.TempDir(), meta); err != nil {
		t.Fatal(err)
	}
	rec, _, err := b.conflicts.recordOrUpdate(cacheConflictRecord{
		ItemKey:           meta.ItemKey,
		Site:              "bilibili",
		Kind:              "video",
		TargetID:          "BV1DIFFTEST1",
		Title:             meta.Title,
		ReasonKind:        ConflictReasonParts,
		LocalSummary:      "共 1 P（CID 101）",
		RemoteSummary:     "共 2 P（CID 209）",
		RemoteFingerprint: "fp_diff_test_1",
	})
	if err != nil {
		t.Fatal(err)
	}

	diff, err := b.InspectCacheConflictDiff(context.Background(), rec.ID)
	if err != nil {
		t.Fatalf("InspectCacheConflictDiff: %v", err)
	}
	if diff.Local.Title != "本地缓存标题 V1" || diff.Local.Text != "本地缓存简介 V1" {
		t.Fatalf("unexpected local diff text: %+v", diff.Local)
	}
	if len(diff.MediaPairs) != 1 || diff.MediaPairs[0].Local == nil || diff.MediaPairs[0].Local.CID != 101 {
		t.Fatalf("expected 1 local media pair, got %+v", diff.MediaPairs)
	}
	if !strings.Contains(diff.RemoteError, "请先登录哔哩哔哩") {
		t.Fatalf("expected login prompt in RemoteError, got %q", diff.RemoteError)
	}
}

func TestInspectCacheConflictDiffBilibiliVideoSideBySideAndZeroURLPersistence(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/x/web-interface/nav":
			_, _ = fmt.Fprint(w, `{"code":0,"data":{"isLogin":true,"mid":778899,"uname":"测试登录用户","wbi_img":{"img_url":"https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png","sub_url":"https://i0.hdslb.com/bfs/wbi/4932caff0ff746eab6f01bf08b70ac45.png"}}}`)
		case "/x/web-interface/wbi/view":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"bvid":     "BV1DIFFLIVE2",
					"aid":      2002,
					"title":    "测试视频：V2 重制高清版",
					"desc":     "新版简介：修正字幕并补充第二分P。",
					"pic":      "https://i0.hdslb.com/bfs/archive/v2cover.jpg",
					"duration": 210,
					"pubdate":  1700000100,
					"owner":    map[string]any{"mid": 20001, "name": "UP_Diff"},
					"pages": []map[string]any{
						{"cid": 209, "page": 1, "part": "重制第一集", "duration": 150, "dimension": map[string]any{"width": 3840, "height": 2160}},
						{"cid": 210, "page": 2, "part": "勘误补充", "duration": 60, "dimension": map[string]any{"width": 1920, "height": 1080}},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	b := newDiffTestBackend(t, srv.URL)
	if _, err := b.ImportCookie(context.Background(), "SESSDATA=synth_diff; bili_jct=csrf_diff; DedeUserID=778899", ""); err != nil {
		t.Fatalf("ImportCookie: %v", err)
	}

	meta := library.ItemMetadata{
		Version: 1,
		Site:    "bilibili",
		Kind:    "video",
		ID:      "video:BV1DIFFLIVE2",
		ItemKey: "bilibili:video:BV1DIFFLIVE2",
		RawID:   "BV1DIFFLIVE2",
		Title:   "测试视频：V1 初版",
		Desc:    "旧版简介：初版上传内容。",
		Status:  "complete",
		Parts: []library.VideoPartRecord{
			{Page: 1, CID: 101, Title: "初版第一集", Duration: 120, File: "P01.mp4", Video: library.PartVideoInfo{QualityName: "1080P", Codecs: "avc1"}},
		},
		Media: []library.MediaFile{
			{Role: "cover", Path: "cover.jpg"},
			{Role: "video", Path: "P01.mp4"},
		},
	}
	if _, err := b.lib.SaveItem(t.TempDir(), meta); err != nil {
		t.Fatal(err)
	}
	rec, _, err := b.conflicts.recordOrUpdate(cacheConflictRecord{
		ItemKey:           meta.ItemKey,
		Site:              "bilibili",
		Kind:              "video",
		TargetID:          "BV1DIFFLIVE2",
		Title:             meta.Title,
		ReasonKind:        ConflictReasonParts,
		LocalSummary:      "共 1 P（CID 101）",
		RemoteSummary:     "共 2 P（CID 209、210）",
		RemoteFingerprint: "fp_diff_live_2",
	})
	if err != nil {
		t.Fatal(err)
	}

	diff, err := b.InspectCacheConflictDiff(context.Background(), rec.ID)
	if err != nil {
		t.Fatalf("InspectCacheConflictDiff: %v", err)
	}
	if diff.RemoteError != "" {
		t.Fatalf("unexpected RemoteError: %q", diff.RemoteError)
	}
	if !diff.TitleChanged || !diff.TextChanged {
		t.Fatalf("expected title & text changes, got titleChanged=%v textChanged=%v", diff.TitleChanged, diff.TextChanged)
	}
	if len(diff.MediaPairs) != 2 {
		t.Fatalf("len(MediaPairs) = %d, want 2: %+v", len(diff.MediaPairs), diff.MediaPairs)
	}
	if diff.MediaPairs[0].Status != ConflictDiffStatusChanged || diff.MediaPairs[0].Local.CID != 101 || diff.MediaPairs[0].Remote.CID != 209 {
		t.Fatalf("pair[0] = %+v", diff.MediaPairs[0])
	}
	if diff.MediaPairs[1].Status != ConflictDiffStatusRemoteOnly || diff.MediaPairs[1].Remote.CID != 210 {
		t.Fatalf("pair[1] = %+v", diff.MediaPairs[1])
	}
	if diff.MediaPairs[0].Remote.RemoteURL != "https://i0.hdslb.com/bfs/archive/v2cover.jpg" {
		t.Fatalf("expected transient remote cover URL, got %q", diff.MediaPairs[0].Remote.RemoteURL)
	}

	raw, err := os.ReadFile(filepath.Join(b.loc.DataDir, "cache-conflicts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "hdslb.com") || strings.Contains(string(raw), "http://") || strings.Contains(string(raw), "https://") {
		t.Fatalf("cache-conflicts.json must never persist URLs: %s", string(raw))
	}
}
