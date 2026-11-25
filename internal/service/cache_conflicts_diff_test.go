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
	"github.com/chialecode/trace/internal/site/douyin"
	"github.com/chialecode/trace/internal/site/weibo"
	"github.com/chialecode/trace/internal/site/x"
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

func TestExtractLocalDiffTextAndMediaSiteParity(t *testing.T) {
	// 1. X thread text must not duplicate meta.Text and must suppress synthetic meta.Title.
	xMeta := library.ItemMetadata{
		Site:    "x",
		Kind:    "dynamic",
		ItemKey: "x:dynamic:1002",
		Title:   "合成摘要标题（截断自正文）",
		Text:    "Anc text\n\nFocal text\n\nReply text",
		Thread: []library.ItemThreadPart{
			{ID: "1001", Text: "Anc text"},
			{ID: "1002", Text: "Focal text"},
			{ID: "1003", Text: "Reply text"},
		},
	}
	xTitle, xBody := extractLocalDiffText(xMeta)
	if xTitle != "" {
		t.Fatalf("expected empty title for X post, got %q", xTitle)
	}
	if xBody != "Anc text\n\nFocal text\n\nReply text" {
		t.Fatalf("unexpected X thread body (must not duplicate): %q", xBody)
	}

	// 2. Weibo non-video post suppresses synthetic title, while standalone video keeps real title.
	wbPostTitle, wbPostBody := extractLocalDiffText(library.ItemMetadata{
		Site:  "weibo",
		Kind:  "dynamic",
		Title: "微博截断标题",
		Text:  "微博正文内容",
	})
	if wbPostTitle != "" || wbPostBody != "微博正文内容" {
		t.Fatalf("weibo non-video = (%q, %q), want (\"\", \"微博正文内容\")", wbPostTitle, wbPostBody)
	}
	wbVidTitle, _ := extractLocalDiffText(library.ItemMetadata{
		Site:  "weibo",
		Kind:  "video",
		Title: "独立微博视频标题",
		Text:  "宿主正文",
	})
	if wbVidTitle != "独立微博视频标题" {
		t.Fatalf("weibo video title = %q, want 独立微博视频标题", wbVidTitle)
	}

	// 3. Mixed retweet holding both NotePages and DouyinVideo must extract both.
	mixedMedia := extractLocalDiffMedia(library.ItemMetadata{
		Site:    "weibo",
		Kind:    "dynamic",
		ItemKey: "weibo:dynamic:555",
		NotePages: []library.NotePageRecord{
			{Index: 1, Kind: "image", Image: "page_01.jpg", MediaID: "pic_outer_1", Width: 1080, Height: 1080},
		},
		DouyinVideo: &library.DouyinVideoInfo{
			File:    "video.mp4",
			MediaID: "1034_origin_vid",
			Width:   1920,
			Height:  1080,
		},
	})
	if len(mixedMedia) != 2 || mixedMedia[0].MediaID != "pic_outer_1" || mixedMedia[1].MediaID != "1034_origin_vid" {
		t.Fatalf("expected both NotePages and DouyinVideo extracted, got %+v", mixedMedia)
	}

	// 4. Bilibili dynamic linked videos in meta.Videos must align with remote v.BVID.
	dynLocal := extractLocalDiffMedia(library.ItemMetadata{
		Site:    "bilibili",
		Kind:    "dynamic",
		ItemKey: "bilibili:dynamic:9001",
		Media:   []library.MediaFile{{Role: "cover", Path: "cover.jpg"}},
		Videos:  []string{"video:BV1LINKED123"},
	})
	dynRemote := []ConflictMediaSideDTO{{
		Kind:    "video",
		Label:   "关联视频标题",
		MediaID: "BV1LINKED123",
	}}
	dynPairs := alignConflictMediaPairs(dynLocal, dynRemote)
	if len(dynPairs) != 1 || dynPairs[0].Status != ConflictDiffStatusSame || dynPairs[0].Local == nil || dynPairs[0].Remote == nil {
		t.Fatalf("expected linked video pair to align as same, got %+v", dynPairs)
	}

	// 5. Portrait height-only upgrade must classify as upgradeable.
	portraitStatus := classifyMediaPairStatus(
		ConflictMediaSideDTO{Kind: "video", Width: 720, Height: 960, MediaID: "m1"},
		ConflictMediaSideDTO{Kind: "video", Width: 720, Height: 1280, MediaID: "m1"},
	)
	if portraitStatus != ConflictDiffStatusUpgradeable {
		t.Fatalf("portrait height upgrade status = %q, want %q", portraitStatus, ConflictDiffStatusUpgradeable)
	}
}

func TestConflictDiffMediaIDParityForDouyinWeiboAndX(t *testing.T) {
	// 1. Douyin live photo note (even if Kind != WorkKindNote when Pages populated) uses douyinPageMediaID(pg),
	// and single video falls back to Video.URI via douyinDetailMediaID(detail).
	dyDetail := &douyin.WorkDetail{
		Kind: douyin.WorkKindVideo,
		Pages: []douyin.NotePage{
			{
				Index:       1,
				Kind:        douyin.NotePageLivePhoto,
				URI:         "img_uri_1",
				ImageURLs:   []string{"https://example.invalid/live.jpg"},
				VideoStream: &douyin.SelectedVODStream{URI: "vid_uri_1"},
			},
		},
	}
	dyRemote := extractDouyinRemoteDiffMedia(dyDetail)
	if len(dyRemote) != 1 || dyRemote[0].MediaID != douyinPageMediaID(dyDetail.Pages[0]) || !dyRemote[0].LivePhoto {
		t.Fatalf("unexpected Douyin note remote media: %+v", dyRemote)
	}
	dyVidDetail := &douyin.WorkDetail{
		Kind:  douyin.WorkKindVideo,
		Video: &douyin.SelectedVODStream{URI: "video_uri_fallback", Width: 1080, Height: 1920},
	}
	dyVidRemote := extractDouyinRemoteDiffMedia(dyVidDetail)
	if len(dyVidRemote) != 1 || dyVidRemote[0].MediaID != library.SanitizeMediaToken("video_uri_fallback") {
		t.Fatalf("unexpected Douyin video remote media: %+v", dyVidRemote)
	}

	// 2. Weibo mixed post uses weiboImageMediaID (ObjectID priority for MediaKindVideo) + video.
	wbPost := &weibo.Post{
		Mid:  "501",
		Text: "混排微博",
		Images: []weibo.ImageAsset{
			{Kind: weibo.MediaKindVideo, PicID: "pic_ignored", ObjectID: "1034:999", URL: "https://example.invalid/v.jpg", Width: 720, Height: 1280},
		},
		Video: &weibo.VideoAsset{
			ObjectID: "1034:888",
			CoverURL: "https://example.invalid/c.jpg",
			Selected: &weibo.VideoStream{QualityLabel: "1080P", Width: 1920, Height: 1080},
		},
	}
	wbRemote := extractWeiboPostRemoteDiffMedia(wbPost)
	if len(wbRemote) != 2 || wbRemote[0].MediaID != library.SanitizeMediaToken("1034:999") || wbRemote[1].MediaID != library.SanitizeMediaToken("1034:888") {
		t.Fatalf("unexpected Weibo remote media: %+v", wbRemote)
	}

	// 3. X thread text (ancestors + focal + replies) & retweet/quote/thread remote media via EffectiveMediaWithOrigin.
	xPost := &x.Post{
		ID:        "2002",
		Text:      "Focal tweet",
		IsRetweet: true,
		Retweeted: &x.Post{
			ID:   "2000",
			Text: "Origin retweet",
			Media: []x.MediaItem{
				{Kind: x.MediaKindPhoto, MediaKey: "3_origin_photo", ImageURL: "https://pbs.twimg.com/media/origin.jpg", Width: 1200, Height: 800},
			},
		},
		ThreadAncestors: []x.Post{
			{
				ID:   "2001",
				Text: "Ancestor tweet",
				Media: []x.MediaItem{
					{Kind: x.MediaKindPhoto, MediaKey: "3_anc_photo", ImageURL: "https://pbs.twimg.com/media/anc.jpg", Width: 800, Height: 600},
				},
			},
		},
		Thread: []x.Post{
			{
				ID:   "2003",
				Text: "Reply tweet",
				Media: []x.MediaItem{
					{Kind: x.MediaKindVideo, MediaKey: "7_reply_vid", ImageURL: "https://pbs.twimg.com/media/vid.jpg", DurationSec: 15, Width: 1280, Height: 720},
				},
			},
		},
	}
	xText, xMedia := extractXRemoteDiffTextAndMedia(xPost)
	if xText != "Ancestor tweet\n\nFocal tweet\n\nReply tweet" {
		t.Fatalf("unexpected X remote thread text: %q", xText)
	}
	if len(xMedia) != 3 || xMedia[0].MediaID != "3_anc_photo" || xMedia[1].MediaID != "3_origin_photo" || xMedia[2].MediaID != "7_reply_vid" {
		t.Fatalf("unexpected X remote media: %+v", xMedia)
	}
}

func TestInspectCacheConflictDiffWeiboLinksAndLiveLegacyAutoResolve(t *testing.T) {
	const cid = "100808c9a1f5c18abbfc31cecd32f45a14ddc1"
	const oriURL = "sinaweibo://pageinfo?containerid=" + cid + "&extparam=合成超话"
	const postEditedID = "5090000000000301"
	const postLegacyID = "5090000000000302"
	mblogEdited, err := weibo.MidToMblogID(postEditedID)
	if err != nil {
		t.Fatal(err)
	}
	mblogLegacy, err := weibo.MidToMblogID(postLegacyID)
	if err != nil {
		t.Fatal(err)
	}

	wbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/ajax/setting/getBasicInfo":
			_, _ = fmt.Fprint(w, `{"ok":1,"data":{"uid":"510000001","screen_name":"微博登录号"}}`)
		case "/ajax/profile/info":
			_, _ = fmt.Fprint(w, `{"ok":1,"data":{"user":{"id":510000001,"idstr":"510000001","screen_name":"微博登录号"}}}`)
		case "/ajax/statuses/show":
			id := r.URL.Query().Get("id")
			switch id {
			case mblogEdited, postEditedID:
				_ = json.NewEncoder(w).Encode(map[string]any{
					"ok":         1,
					"idstr":      postEditedID,
					"mblogid":    mblogEdited,
					"created_at": "Sat Nov 21 15:00:00 +0800 2026",
					"text_raw":   "#合成[超话]# 新版修订正文，详见 http://t.cn/A6DOC99",
					"url_struct": []map[string]any{
						{"short_url": "http://t.cn/A6SUP99", "ori_url": oriURL, "url_title": "合成超话"},
						{"short_url": "http://t.cn/A6DOC99", "long_url": "https://example.com/spec", "url_title": "规范说明"},
						{"short_url": "http://t.cn/A6SIGN9", "long_url": "https://f.video.weibocdn.com/a.mp4?ssig=secret&Expires=1", "url_title": "签名视频"},
					},
					"user": map[string]any{"idstr": "51001", "screen_name": "测试博主"},
				})
			case mblogLegacy, postLegacyID:
				_ = json.NewEncoder(w).Encode(map[string]any{
					"ok":         1,
					"idstr":      postLegacyID,
					"mblogid":    mblogLegacy,
					"created_at": "Sat Nov 21 15:00:00 +0800 2026",
					"text_raw":   "#合成[超话]# 正文完全未改",
					"pic_ids":    []string{"005XpicLegacyImg"},
					"pic_infos": map[string]any{
						"005XpicLegacyImg": map[string]any{
							"pic_id": "005XpicLegacyImg",
							"type":   "pic",
							"large":  map[string]any{"url": "https://wx1.sinaimg.cn/large/005XpicLegacyImg.jpg", "width": 800, "height": 600},
						},
					},
					"url_struct": []map[string]any{
						{"short_url": "http://t.cn/A6SUP98", "ori_url": oriURL, "url_title": "合成超话"},
					},
					"user": map[string]any{"idstr": "51001", "screen_name": "测试博主"},
				})
			default:
				http.NotFound(w, r)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer wbSrv.Close()

	loc := testLocations(t)
	b, err := NewBackend(Options{
		Locations:        loc,
		Desktop:          NewMockDesktop(),
		DisableBgWorkers: true,
		WeiboOpts: weibo.ClientOptions{
			Endpoints: weibo.Endpoints{
				PCBasicInfo:  wbSrv.URL + "/ajax/setting/getBasicInfo",
				ProfileInfo:  wbSrv.URL + "/ajax/profile/info",
				StatusesShow: wbSrv.URL + "/ajax/statuses/show",
			},
			MinInterval:    -1,
			JitterMax:      -1,
			AllowLocalhost: true,
		},
	})
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	defer b.Close()

	if _, err := b.LoginWithCookie(context.Background(), "weibo", "SUB=synth_wb_diff; SUBP=synth_subp", ""); err != nil {
		t.Fatalf("LoginWithCookie weibo: %v", err)
	}

	// Case 1: Genuine Weibo text edit surfaces both LocalLinks (extracted from legacy text) and RemoteLinks (safe only).
	dirEdited := filepath.Join(b.lib.CacheDir(), "weibo", "51001", "posts", postEditedID)
	if _, err := b.lib.SaveItem(dirEdited, library.ItemMetadata{
		Version:  1,
		ID:       "dynamic:" + postEditedID,
		Site:     "weibo",
		Kind:     "dynamic",
		Status:   "complete",
		Title:    "旧版标题",
		Text:     "合成超话 (" + oriURL + ")\n旧版原始正文",
		Desc:     "合成超话 (" + oriURL + ")\n旧版原始正文",
		URL:      "https://weibo.com/51001/" + postEditedID,
		CachedAt: "2026-03-02T09:00:00Z",
		Author:   library.ItemAuthor{Site: "weibo", UID: "51001", Name: "测试博主"},
	}); err != nil {
		t.Fatal(err)
	}
	recEdited, _, err := b.conflicts.recordOrUpdate(cacheConflictRecord{
		ItemKey:           "weibo:dynamic:" + postEditedID,
		Site:              "weibo",
		Kind:              "dynamic",
		TargetID:          postEditedID,
		Title:             "旧版标题",
		ReasonKind:        ConflictReasonText,
		LocalSummary:      "纯文字微博",
		RemoteSummary:     "源站微博正文内容已修改",
		RemoteFingerprint: "fp_real_weibo_edit_301",
		State:             ConflictStatePending,
	})
	if err != nil {
		t.Fatal(err)
	}

	diffEdited, err := b.InspectCacheConflictDiff(context.Background(), recEdited.ID)
	if err != nil {
		t.Fatalf("InspectCacheConflictDiff(edited): %v", err)
	}
	if diffEdited.AutoResolved || !diffEdited.TextChanged {
		t.Fatalf("expected real edit to remain unresolved with TextChanged=true, got auto=%v changed=%v", diffEdited.AutoResolved, diffEdited.TextChanged)
	}
	if len(diffEdited.LocalLinks) != 1 || diffEdited.LocalLinks[0] != (TextLinkDTO{Label: "合成超话", URL: "https://weibo.com/p/" + cid}) {
		t.Fatalf("LocalLinks = %+v", diffEdited.LocalLinks)
	}
	if len(diffEdited.RemoteLinks) != 2 ||
		diffEdited.RemoteLinks[0] != (TextLinkDTO{Label: "合成超话", URL: "https://weibo.com/p/" + cid}) ||
		diffEdited.RemoteLinks[1] != (TextLinkDTO{Label: "规范说明", URL: "https://example.com/spec"}) {
		t.Fatalf("RemoteLinks = %+v (must exclude signed media URL)", diffEdited.RemoteLinks)
	}

	// Case 2: Pre-DEC-076 image post without AssetIDs/MediaID auto-resolves during live ConflictDiff review.
	dirLegacy := filepath.Join(b.lib.CacheDir(), "weibo", "51001", "posts", postLegacyID)
	imgMF := writeVerifiedLocalFile(t, dirLegacy, "images/01.jpg", []byte("IMG_V1_SAFE"), "image", 800, 600, 0)
	const origLegacyCachedAt = "2026-03-01T07:00:00Z"
	if _, err := b.lib.SaveItem(dirLegacy, library.ItemMetadata{
		Version:   1,
		ID:        "dynamic:" + postLegacyID,
		Site:      "weibo",
		Kind:      "dynamic",
		Status:    "complete",
		Title:     "合成超话 (" + oriURL + ")",
		Text:      "合成超话 (" + oriURL + ")\n正文完全未改",
		Desc:      "合成超话 (" + oriURL + ")\n正文完全未改",
		URL:       "https://weibo.com/51001/" + postLegacyID,
		CachedAt:  origLegacyCachedAt,
		Author:    library.ItemAuthor{Site: "weibo", UID: "51001", Name: "测试博主"},
		Media:     []library.MediaFile{imgMF},
		NotePages: []library.NotePageRecord{{Index: 1, Kind: "image", Image: "images/01.jpg", Width: 800, Height: 600}}, // no MediaID, no AssetIDs
	}); err != nil {
		t.Fatal(err)
	}
	recLegacy, _, err := b.conflicts.recordOrUpdate(cacheConflictRecord{
		ItemKey:           "weibo:dynamic:" + postLegacyID,
		Site:              "weibo",
		Kind:              "dynamic",
		TargetID:          postLegacyID,
		Title:             "旧缓存图博误报",
		ReasonKind:        ConflictReasonText,
		LocalSummary:      "共 1 项媒体",
		RemoteSummary:     "源站微博正文内容已修改",
		RemoteFingerprint: "fp_pre_dec076_with_remote_picid",
		State:             ConflictStatePending,
	})
	if err != nil {
		t.Fatal(err)
	}

	diffLegacy, err := b.InspectCacheConflictDiff(context.Background(), recLegacy.ID)
	if err != nil {
		t.Fatalf("InspectCacheConflictDiff(legacy): %v", err)
	}
	if !diffLegacy.AutoResolved {
		t.Fatalf("expected live review of pre-DEC-076 legacy inline URL conflict to AutoResolved=true")
	}
	migrated, ok, err := b.lib.GetItem("weibo:dynamic:" + postLegacyID)
	if err != nil || !ok || migrated.CachedAt != origLegacyCachedAt || migrated.Text != "合成超话\n正文完全未改" || len(migrated.Links) != 1 || migrated.Links[0].URL != "https://weibo.com/p/"+cid {
		t.Fatalf("migrated pre-DEC-076 item mismatch: ok=%v err=%v meta=%+v", ok, err, migrated)
	}
	rem, _ := b.ListCacheConflicts()
	if len(rem) != 1 || rem[0].ItemKey != "weibo:dynamic:"+postEditedID {
		t.Fatalf("expected only edited conflict to remain, got %+v", rem)
	}
}
