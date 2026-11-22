package weibo

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/chialecode/trace/internal/site"
)

func TestGetPostLongTextInlinedAndFetchedAndURLStructRestore(t *testing.T) {
	var longTextHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/ajax/statuses/show":
			id := r.URL.Query().Get("id")
			switch id {
			case "O00000001": // Inlined longText -> 0 extra longtext requests.
				_, _ = w.Write([]byte(`{
					"ok": 1,
					"idstr": "5000000000000001",
					"mblogid": "O00000001",
					"created_at": "Sat Nov 21 15:00:00 +0800 2026",
					"isLongText": true,
					"text_raw": "截断预览...",
					"longText": {
						"longTextContent": "这是内联的完整长微博正文，参考链接 http://t.cn/A6SYNTHETIC 结束。",
						"url_struct": [
							{
								"short_url": "http://t.cn/A6SYNTHETIC",
								"long_url": "https://weibo.com/u/1000000001",
								"url_title": "合成主页"
							}
						]
					},
					"user": {"idstr": "1000000001", "screen_name": "合成示例用户"},
					"visible": {"type": 0, "list_id": 0}
				}`))
			case "P03d70010": // Truncated outer AND truncated retweeted_status -> 2 longtext requests!
				_, _ = w.Write([]byte(`{
					"ok": 1,
					"idstr": "5100123450000062",
					"mblogid": "P03d70010",
					"created_at": "Sat Nov 21 16:00:00 +0800 2026",
					"isLongText": true,
					"text_raw": "外层长评语...",
					"text": "外层长评语...<span class=\"expand\">展开</span>",
					"user": {"idstr": "1000000001", "screen_name": "转发者"},
					"retweeted_status": {
						"idstr": "4999999999999999",
						"mblogid": "NFXsjFXsj",
						"isLongText": true,
						"text_raw": "内层长原博...",
						"text": "内层长原博...<span class=\"expand\">展开</span>",
						"user": {"idstr": "1000000002", "screen_name": "原作者"}
					}
				}`))
			case "Q48VUdt4l": // Mismatched returned idstr -> schema_changed!
				_, _ = w.Write([]byte(`{
					"ok": 1,
					"idstr": "5000000000000099",
					"mblogid": "O0000001B",
					"text_raw": "错误的微博",
					"user": {"idstr": "1000000001", "screen_name": "合成示例用户"}
				}`))
			}
		case "/ajax/statuses/longtext":
			longTextHits.Add(1)
			switch r.URL.Query().Get("id") {
			case "P03d70010":
				_, _ = w.Write([]byte(`{"ok":1,"data":{"longTextContent":"外层补全后的完整转发长评语。"}}`))
			case "NFXsjFXsj":
				_, _ = w.Write([]byte(`{"ok":1,"data":{"longTextContent":"内层补全后的完整原博长正文。"}}`))
			}
		}
	}))
	defer srv.Close()

	client := newFastTestClient(t, srv.URL, newTestWeiboStore(t, "SUB=SYNTHETIC_SUB_POST"))

	// 1. Inlined longText + url_struct restoration without any extra network calls.
	p1, err := client.GetPost(context.Background(), "5000000000000001")
	if err != nil {
		t.Fatalf("GetPost(inlined) failed: %v", err)
	}
	if longTextHits.Load() != 0 {
		t.Fatalf("inlined longText triggered %d extra calls, want 0", longTextHits.Load())
	}
	if !p1.LongTextFetched || !strings.Contains(p1.Text, "合成主页 (https://weibo.com/u/1000000001)") {
		t.Fatalf("unexpected restored long text: %q", p1.Text)
	}
	if p1.RuleType != RuleTypeWord || p1.Visible.Label != "公开" {
		t.Fatalf("RuleType=%q Visible=%+v", p1.RuleType, p1.Visible)
	}

	// 2. Non-inlined outer + retweeted longText fetched separately.
	p2, err := client.GetPost(context.Background(), "P03d70010")
	if err != nil {
		t.Fatalf("GetPost(retweet long) failed: %v", err)
	}
	if longTextHits.Load() != 2 {
		t.Fatalf("longtext calls = %d, want 2", longTextHits.Load())
	}
	if p2.Text != "外层补全后的完整转发长评语。" || p2.Retweeted == nil || p2.Retweeted.Text != "内层补全后的完整原博长正文。" {
		t.Fatalf("longtext expansion mismatch: outer=%q inner=%+v", p2.Text, p2.Retweeted)
	}
	if p2.RuleType != RuleTypeForward || !p2.IsRetweet {
		t.Fatalf("expected forward ruleType, got %q isRetweet=%v", p2.RuleType, p2.IsRetweet)
	}

	// 3. Returned idstr mismatch -> schema_changed!
	if _, err := client.GetPost(context.Background(), "5209876543210009"); !site.IsKind(err, site.ErrKindSchemaChanged) {
		t.Fatalf("mismatched mid err = %v, want schema_changed", err)
	}

	// 4. UnauthenticatedCall:true skips GetLongText and never sends stored Cookie (ACT-022 V-09).
	var unauthLongHits atomic.Int32
	var unauthSawCookie atomic.Bool
	srvUnauth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" {
			unauthSawCookie.Store(true)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/ajax/statuses/longtext" {
			unauthLongHits.Add(1)
			_, _ = w.Write([]byte(`{"ok":-100}`))
			return
		}
		_, _ = w.Write([]byte(`{
			"ok": 1,
			"idstr": "5100123450000062",
			"mblogid": "P03d70010",
			"isLongText": true,
			"text_raw": "截断正文...",
			"text": "截断正文...<span class=\"expand\">展开</span>",
			"user": {"idstr": "1000000001", "screen_name": "博主"}
		}`))
	}))
	defer srvUnauth.Close()

	unauthClient := newFastTestClient(t, srvUnauth.URL, newTestWeiboStore(t, "SUB=SYNTHETIC_SUB_MUST_NOT_SEND"))
	pUnauth, err := unauthClient.GetPost(context.Background(), "5100123450000062", PostOptions{UnauthenticatedCall: true})
	if err != nil || pUnauth.Mid != "5100123450000062" {
		t.Fatalf("GetPost(UnauthenticatedCall) = (%+v, %v)", pUnauth, err)
	}
	if unauthSawCookie.Load() {
		t.Fatalf("UnauthenticatedCall unexpectedly sent Cookie header")
	}
	if unauthLongHits.Load() != 0 {
		t.Fatalf("UnauthenticatedCall issued %d longtext calls, want 0", unauthLongHits.Load())
	}
}

func TestPostOver9ImagesOrderGIFLivePhotoMixedAndWatermarkHook(t *testing.T) {
	// Build 12 images in deliberate non-lexicographic pic_ids order to guarantee map iteration is NEVER used.
	picOrder := []string{
		"001SynPic_12", "001SynPic_03", "001SynPic_11", "001SynPic_01",
		"001SynPic_10", "001SynPic_05", "001SynPic_09", "001SynPic_02",
		"001SynPic_08", "001SynPic_04", "001SynPic_07", "001SynPic_06",
	}
	var picInfoParts []string
	var picIDParts []string
	for i, pid := range picOrder {
		picIDParts = append(picIDParts, fmt.Sprintf("%q", pid))
		picType := "pic"
		extra := ""
		if i == 1 {
			picType = "gif"
			extra = fmt.Sprintf(`,"video":"https://g.us.sinaimg.cn/%s.mp4?Expires=1800000000&ssig=syn12345&KID=unistore,video"`, pid)
		} else if i == 2 {
			picType = "livephoto"
			extra = `,"video":"https://video.weibo.com/media/play?livephoto=syn0001"`
		}
		ext := "jpg"
		if picType == "gif" {
			ext = "gif"
		}
		picInfoParts = append(picInfoParts, fmt.Sprintf(`%q:{
			"pic_id":%q,
			"type":%q,
			"bmiddle":{"url":"https://wx1.sinaimg.cn/bmiddle/%s.%s","width":440,"height":330},
			"large":{"url":"https://wx1.sinaimg.cn/large/%s.%s","width":1024,"height":768},
			"mw2000":{"url":"https://wx1.sinaimg.cn/mw2000/%s.%s","width":2000,"height":1500},
			"original":{"url":"https://wx1.sinaimg.cn/orj960/%s.%s","width":960,"height":720},
			"largest":{"url":"https://wx1.sinaimg.cn/mw2000/%s.%s","width":2400,"height":1800}%s
		}`, pid, pid, picType, pid, ext, pid, ext, pid, ext, pid, ext, pid, ext, extra))
	}

	rawJSON := fmt.Sprintf(`{
		"ok": 1,
		"idstr": "5000000000000001",
		"mblogid": "O00000001",
		"text_raw": "十二张合成配图测试",
		"user": {"idstr": "1000000001", "screen_name": "合成示例用户"},
		"pic_ids": [%s],
		"pic_infos": {%s},
		"visible": {"type": 1, "list_id": 0}
	}`, strings.Join(picIDParts, ","), strings.Join(picInfoParts, ","))

	client := newFastTestClient(t, "http://127.0.0.1:1", nil)
	post, err := client.ParseRawPostJSON([]byte(rawJSON), PostOptions{
		ImageURLRewriter: func(_ string, rawURL string) (string, bool) {
			if strings.HasSuffix(rawURL, ".gif") {
				return "", false
			}
			return RewriteSinaimgPath(rawURL, "oslarge")
		},
	})
	if err != nil {
		t.Fatalf("ParseRawPostJSON(12 pics) failed: %v", err)
	}
	if len(post.Images) != 12 {
		t.Fatalf("len(Images) = %d, want 12", len(post.Images))
	}
	for i, wantID := range picOrder {
		if post.Images[i].PicID != wantID || post.Images[i].Index != i+1 {
			t.Fatalf("image[%d] = (%q, idx=%d), want (%q, idx=%d)", i, post.Images[i].PicID, post.Images[i].Index, wantID, i+1)
		}
	}
	// Image 0: watermark hook rewrote /mw2000/ -> /oslarge/, original kept as first fallback.
	if !strings.Contains(post.Images[0].URL, "/oslarge/") || len(post.Images[0].FallbackURLs) == 0 || !strings.Contains(post.Images[0].FallbackURLs[0], "/mw2000/") {
		t.Fatalf("watermark hook URL/fallback = (%q, %v)", post.Images[0].URL, post.Images[0].FallbackURLs)
	}
	// Image 1: GIF takes original .gif largest.url.
	if post.Images[1].Kind != MediaKindGIF || !strings.HasSuffix(post.Images[1].URL, ".gif") {
		t.Fatalf("GIF image = (%q, %q)", post.Images[1].Kind, post.Images[1].URL)
	}
	// Image 2: LivePhoto has still URL + motion video + NeedsLivePhotoHop=true for video.weibo.com/media/play.
	if post.Images[2].Kind != MediaKindLivePhoto || !post.Images[2].NeedsLivePhotoHop || !strings.Contains(post.Images[2].MotionURL, "video.weibo.com/media/play") {
		t.Fatalf("LivePhoto image = %+v", post.Images[2])
	}
	if post.RuleType != RuleTypeDraw || post.Visible.Type != 1 || post.Visible.Label != "仅自己可见" {
		t.Fatalf("RuleType=%q Visible=%+v", post.RuleType, post.Visible)
	}

	// Mixed media (mix_media_info.items[]) takes priority over pic_ids and keeps exact order (pic -> video -> livephoto).
	mixedJSON := `{
		"ok": 1,
		"idstr": "5000000000000002",
		"mblogid": "O00000002",
		"text_raw": "图文视频混排",
		"user": {"idstr": "1000000001", "screen_name": "合成示例用户"},
		"mix_media_info": {
			"items": [
				{
					"id": "mix_pic_1",
					"type": "pic",
					"data": {"pic_id": "mix_pic_1", "type": "pic", "largest": {"url": "https://wx2.sinaimg.cn/large/mix_pic_1.jpg", "width": 1200, "height": 900}}
				},
				{
					"id": "1034:5000000000000010",
					"type": "video",
					"data": {
						"object_id": "1034:5000000000000010",
						"page_pic": "https://wx2.sinaimg.cn/large/mix_vid_cover.jpg",
						"media_info": {
							"duration": 12.4,
							"playback_list": [
								{"meta": {"label": "scrubber_hd", "quality_index": 0}, "play_info": {"url": "https://wx2.sinaimg.cn/sprite.jpg", "mime": "image/jpeg"}},
								{"meta": {"label": "mp4_1080p", "quality_index": 1080, "quality_label": "1080p"}, "play_info": {"url": "https://f.video.weibocdn.com/mix1080.mp4?Expires=1800000000&ssig=syn12345&KID=unistore,video", "mime": "video/mp4", "protocol": "general", "width": 1920, "height": 1080, "fps": 30, "bitrate": 2500000, "video_codecs": "avc1.640028"}}
							]
						}
					}
				},
				{
					"id": "mix_lp_3",
					"type": "pic",
					"data": {"pic_id": "mix_lp_3", "type": "livephoto", "largest": {"url": "https://wx2.sinaimg.cn/large/mix_lp_3.jpg", "width": 1080, "height": 1440}, "video": "https://livephoto.us.sinaimg.cn/mix_lp_3.mov?Expires=1800000000&ssig=syn12345&KID=unistore,video"}
				}
			]
		},
		"visible": {"type": 10, "list_id": 4000000000000001}
	}`
	mixed, err := client.ParseRawPostJSON([]byte(mixedJSON))
	if err != nil {
		t.Fatalf("ParseRawPostJSON(mixed) failed: %v", err)
	}
	if len(mixed.Images) != 3 || mixed.Images[0].Kind != MediaKindImage || mixed.Images[1].Kind != MediaKindVideo || mixed.Images[2].Kind != MediaKindLivePhoto {
		t.Fatalf("unexpected mixed items: %+v", mixed.Images)
	}
	if mixed.Images[1].VideoStream == nil || mixed.Images[1].VideoStream.ShortEdge != 1080 || mixed.RuleType != RuleTypeDraw {
		t.Fatalf("mixed video item = %+v, ruleType=%q", mixed.Images[1], mixed.RuleType)
	}
	if mixed.Visible.Type != 10 || mixed.Visible.ListID != "4000000000000001" || !strings.Contains(mixed.Visible.Label, "受限可见") {
		t.Fatalf("restricted visibility = %+v", mixed.Visible)
	}
}

func TestPostVideoPlaybackSelectionLegacyFallbackAndTVComponentNormalization(t *testing.T) {
	playbackJSON := `{
		"ok": 1,
		"idstr": "5000000000000001",
		"mblogid": "O00000001",
		"text_raw": "视频选流测试",
		"user": {"idstr": "1000000001", "screen_name": "视频博主"},
		"page_info": {
			"object_type": "video",
			"object_id": "1034:5000000000000001",
			"page_title": "高清演示视频",
			"page_pic": {"url": "https://wx1.sinaimg.cn/large/video_cover.jpg"},
			"media_info": {
				"duration": 25.2,
				"playback_list": [
					{"meta": {"label": "scrubber_hd", "quality_index": 9999}, "play_info": {"url": "https://wx1.sinaimg.cn/sprite.jpg", "mime": "image/jpeg", "width": 4000, "height": 4000}},
					{"meta": {"label": "mp4_4k_hidden", "quality_index": 2160, "is_hidden": true}, "play_info": {"url": "https://f.video.weibocdn.com/hidden4k.mp4", "mime": "video/mp4", "width": 3840, "height": 2160}},
					{"meta": {"label": "mp4_720p", "quality_index": 720, "quality_label": "720p"}, "play_info": {"url": "https://f.video.weibocdn.com/720.mp4", "mime": "video/mp4", "protocol": "general", "width": 1280, "height": 720, "fps": 30, "bitrate": 1200000, "video_codecs": "avc1.64001f"}},
					{"meta": {"label": "hevc_mp4_1080p", "quality_index": 1080, "quality_label": "1080p"}, "play_info": {"url": "https://f.video.weibocdn.com/1080_hevc.mp4", "mime": "video/mp4", "protocol": "general", "width": 1920, "height": 1080, "fps": 60, "bitrate": 3200000, "video_codecs": "hev1.1.6.L120"}},
					{"meta": {"label": "mp4_1080p_30", "quality_index": 1080, "quality_label": "1080p"}, "play_info": {"url": "https://f.video.weibocdn.com/1080_avc_30.mp4", "mime": "video/mp4", "protocol": "general", "width": 1920, "height": 1080, "fps": 30, "bitrate": 3000000, "video_codecs": "avc1.640028"}},
					{"meta": {"label": "mp4_1080p_60", "quality_index": 1080, "quality_label": "1080p"}, "play_info": {"url": "https://f.video.weibocdn.com/1080_avc_60.mp4", "mime": "video/mp4", "protocol": "general", "width": 1920, "height": 1080, "fps": 60, "bitrate": 4500000, "video_codecs": "avc1.64002a"}}
				]
			}
		}
	}`

	client := newFastTestClient(t, "http://127.0.0.1:1", nil)

	// Prefer AVC -> selects 1080p 60fps AVC over 30fps AVC and HEVC, filtering out scrubber_hd and is_hidden.
	pAVC, err := client.ParseRawPostJSON([]byte(playbackJSON), PostOptions{StreamOptions: StreamSelectOptions{PreferredCodec: "avc"}})
	if err != nil {
		t.Fatalf("ParseRawPostJSON(avc) failed: %v", err)
	}
	if pAVC.RuleType != RuleTypeVideo || pAVC.Video == nil || pAVC.Video.Selected == nil {
		t.Fatalf("expected video post, got ruleType=%q video=%+v", pAVC.RuleType, pAVC.Video)
	}
	if len(pAVC.Video.Streams) != 4 {
		t.Fatalf("filtered stream count = %d, want 4 (scrubber & hidden excluded)", len(pAVC.Video.Streams))
	}
	if pAVC.Video.Selected.Label != "mp4_1080p_60" || pAVC.Video.Selected.Codec != "avc" {
		t.Fatalf("AVC selected = %+v, want mp4_1080p_60", pAVC.Video.Selected)
	}

	// Prefer HEVC -> selects 1080p HEVC!
	pHEVC, err := client.ParseRawPostJSON([]byte(playbackJSON), PostOptions{StreamOptions: StreamSelectOptions{PreferredCodec: "hevc"}})
	if err != nil || pHEVC.Video.Selected.Label != "hevc_mp4_1080p" || pHEVC.Video.Selected.Codec != "hevc" {
		t.Fatalf("HEVC selected = %+v, err=%v", pHEVC.Video.Selected, err)
	}

	// Legacy media_info fallback when playback_list is empty.
	legacyJSON := `{
		"ok": 1,
		"idstr": "5000000000000003",
		"mblogid": "O00000003",
		"text_raw": "老视频兜底",
		"user": {"idstr": "1000000001", "screen_name": "视频博主"},
		"page_info": {
			"type": "11",
			"media_info": {
				"duration": 15,
				"mp4_hd_url": "https://f.us.sinaimg.cn/legacy_hd.mp4?label=mp4_720p&template=1280x720.25.0",
				"mp4_sd_url": "https://f.us.sinaimg.cn/legacy_sd.mp4?label=mp4_ld&template=640x360.25.0"
			}
		}
	}`
	pLegacy, err := client.ParseRawPostJSON([]byte(legacyJSON))
	if err != nil || pLegacy.Video == nil || !pLegacy.Video.LegacyFallback || pLegacy.Video.Selected.ShortEdge != 720 {
		t.Fatalf("legacy fallback = (%+v, err=%v)", pLegacy.Video, err)
	}

	// GetVideo via POST /tv/api/component checking Page-Referer header & host mid normalization.
	var gotPageReferer string
	var gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/tv/api/component":
			gotMethod = r.Method
			gotPageReferer = r.Header.Get("Page-Referer")
			pageParam := r.URL.Query().Get("page")
			if strings.HasSuffix(pageParam, "1034:5000000000000001") {
				_, _ = w.Write([]byte(`{
					"code": "100000",
					"data": {
						"Component_Play_Playinfo": {
							"mid": "5000000000000001",
							"title": "宿主微博视频",
							"author": "视频博主",
							"author_id": 1000000001,
							"cover_image": "//wx1.sinaimg.cn/large/cover.jpg",
							"duration": 25,
							"urls": {
								"高清 1080P": "//f.video.weibocdn.com/comp_1080.mp4",
								"标清 480P": "//f.video.weibocdn.com/comp_480.mp4"
							}
						}
					}
				}`))
				return
			}
			// Standalone video with no host mid.
			_, _ = w.Write([]byte(`{
				"code": "100000",
				"data": {
					"Component_Play_Playinfo": {
						"mid": "",
						"title": "无宿主独立视频",
						"author": "视频博主",
						"author_id": "1000000001",
						"cover_image": "//wx1.sinaimg.cn/large/standalone.jpg",
						"duration": 18,
						"urls": {
							"高清 1080P": "//f.video.weibocdn.com/standalone_1080.mp4"
						}
					}
				}
			}`))
		case "/ajax/statuses/show":
			_, _ = w.Write([]byte(playbackJSON))
		}
	}))
	defer srv.Close()

	tvClient := newFastTestClient(t, srv.URL, newTestWeiboStore(t, "SUB=SYNTHETIC_SUB_TV"))
	vr, err := tvClient.GetVideo(context.Background(), "1034:5000000000000001")
	if err != nil {
		t.Fatalf("GetVideo(with host mid) failed: %v", err)
	}
	if gotMethod != http.MethodPost || gotPageReferer != "/tv/show/1034:5000000000000001" {
		t.Fatalf("tv/api/component method=%q Page-Referer=%q", gotMethod, gotPageReferer)
	}
	if vr.HostPost == nil || vr.HostPost.Mid != "5000000000000001" {
		t.Fatalf("expected normalization to host post 5000000000000001, got %+v", vr)
	}

	vrStandalone, err := tvClient.GetVideo(context.Background(), "1034:5000000000000099")
	if err != nil || vrStandalone.HostPost != nil || vrStandalone.Video == nil || vrStandalone.Video.Selected.URL != "https://f.video.weibocdn.com/standalone_1080.mp4" {
		t.Fatalf("standalone GetVideo = (%+v, err=%v)", vrStandalone, err)
	}
}

func TestPostRetweetDeletedOriginMediaDedupAndRichCardDegradation(t *testing.T) {
	client := newFastTestClient(t, "http://127.0.0.1:1", nil)

	// 1. Deleted origin variants (deleted:"1", user:null, system prompt) degrade cleanly without schema_changed!
	deletedOrigins := []struct {
		name string
		body string
	}{
		{
			name: "deleted_string_1",
			body: `{"ok":1,"idstr":"5000000000000010","mblogid":"O0000000a","text_raw":"转发评语","user":{"idstr":"1000000001","screen_name":"用户甲"},"retweeted_status":{"deleted":"1","text":"抱歉，此微博已被作者删除"}}`,
		},
		{
			name: "user_null",
			body: `{"ok":1,"idstr":"5000000000000011","mblogid":"O0000000b","text_raw":"转发评语2","user":{"idstr":"1000000001","screen_name":"用户甲"},"retweeted_status":{"idstr":"5000000000000098","user":null,"text_raw":"由于作者设置，你暂时没有这条微博的查看权限"}}`,
		},
		{
			name: "system_prompt",
			body: `{"ok":1,"idstr":"5000000000000012","mblogid":"O0000000c","text_raw":"转发评语3","user":{"idstr":"1000000001","screen_name":"用户甲"},"retweeted_status":{"idstr":"5000000000000097","user":{"idstr":"1000000002","screen_name":"原博"},"text_raw":"目前内容暂不可见"}}`,
		},
	}
	for _, tc := range deletedOrigins {
		p, err := client.ParseRawPostJSON([]byte(tc.body))
		if err != nil {
			t.Fatalf("%s: unexpected error %v", tc.name, err)
		}
		if p.Retweeted == nil || !p.Retweeted.Unavailable || p.RuleType != RuleTypeForward {
			t.Fatalf("%s: expected unavailable retweeted origin, got %+v", tc.name, p.Retweeted)
		}
	}

	// 2. Retweet media attached on outer (video) + inner (images + duplicate video) deduplicated cleanly!
	retweetMediaJSON := `{
		"ok": 1,
		"idstr": "5000000000000020",
		"mblogid": "O0000000k",
		"text_raw": "看这个视频",
		"user": {"idstr": "1000000001", "screen_name": "转发者"},
		"page_info": {
			"object_type": "video",
			"object_id": "1034:5000000000000020",
			"media_info": {
				"playback_list": [
					{"meta": {"label": "mp4_720p", "quality_index": 720}, "play_info": {"url": "https://f.video.weibocdn.com/rt_outer.mp4", "mime": "video/mp4", "width": 1280, "height": 720}}
				]
			}
		},
		"retweeted_status": {
			"idstr": "5000000000000019",
			"mblogid": "O0000000j",
			"text_raw": "原视频微博",
			"user": {"idstr": "1000000002", "screen_name": "原作者"},
			"page_info": {
				"object_type": "video",
				"object_id": "1034:5000000000000020",
				"media_info": {
					"playback_list": [
						{"meta": {"label": "mp4_720p", "quality_index": 720}, "play_info": {"url": "https://f.video.weibocdn.com/rt_outer.mp4", "mime": "video/mp4", "width": 1280, "height": 720}}
					]
				}
			}
		}
	}`
	rtPost, err := client.ParseRawPostJSON([]byte(retweetMediaJSON))
	if err != nil {
		t.Fatalf("retweet media parse failed: %v", err)
	}
	effImgs, effVid := rtPost.EffectiveMedia()
	if len(effImgs) != 0 || effVid == nil || effVid.ObjectID != "1034:5000000000000020" {
		t.Fatalf("EffectiveMedia(retweet video) = (%d imgs, %+v)", len(effImgs), effVid)
	}

	// 3. Rich card degradation: article, vote, music, topic (no cover leak!), movie (no MP4!), live card (no MP4!).
	cards := []struct {
		name      string
		pageInfo  string
		wantKind  string
		wantCover bool
	}{
		{
			name:      "article",
			pageInfo:  `{"object_type":"article","page_title":"合成头条文章","page_desc":"摘要","page_url":"https://weibo.com/ttarticle/p/show?id=1","page_pic":"https://wx1.sinaimg.cn/large/art.jpg"}`,
			wantKind:  CardKindArticle,
			wantCover: true,
		},
		{
			name:      "vote",
			pageInfo:  `{"object_type":"vote","page_title":"合成投票","page_url":"https://vote.weibo.com/h5/index?vote_id=1"}`,
			wantKind:  CardKindVote,
			wantCover: false,
		},
		{
			name:      "music",
			pageInfo:  `{"object_type":"audio","page_title":"合成曲目","page_url":"https://weibo.com/music/1","page_pic":"https://wx1.sinaimg.cn/large/music.jpg"}`,
			wantKind:  CardKindMusic,
			wantCover: true,
		},
		{
			name:      "topic_no_cover_leak",
			pageInfo:  `{"object_type":"topic","page_title":"#合成超话#","page_url":"https://weibo.com/p/1008081","page_pic":"https://wx1.sinaimg.cn/large/topic_icon.jpg"}`,
			wantKind:  CardKindWebpage,
			wantCover: false,
		},
		{
			name:      "movie_not_mp4",
			pageInfo:  `{"type":"5","object_type":"movie","page_title":"合成电影","media_info":{"stream_url":"https://f.video.weibocdn.com/trailer.mp4"}}`,
			wantKind:  CardKindWebpage,
			wantCover: false,
		},
		{
			name:      "live_card_not_mp4",
			pageInfo:  `{"object_type":"live","object_id":"1022:2321325000000000000001","page_title":"合成直播间","page_url":"https://weibo.com/l/wblive/p/show/1022:2321325000000000000001","media_info":{"live_status":1,"stream_url":"https://live.video.weibocdn.com/live.flv"}}`,
			wantKind:  CardKindLive,
			wantCover: false,
		},
	}
	for _, tc := range cards {
		raw := fmt.Sprintf(`{"ok":1,"idstr":"5000000000000030","mblogid":"O0000000u","text_raw":"卡片测试","user":{"idstr":"1000000001","screen_name":"用户"},"page_info":%s}`, tc.pageInfo)
		p, err := client.ParseRawPostJSON([]byte(raw))
		if err != nil {
			t.Fatalf("%s parse error: %v", tc.name, err)
		}
		if p.Video != nil {
			t.Fatalf("%s MUST NOT populate Video (got %+v)", tc.name, p.Video)
		}
		if p.Card == nil || p.Card.Kind != tc.wantKind || !p.Card.Unsupported {
			t.Fatalf("%s card = %+v, want kind=%q unsupported=true", tc.name, p.Card, tc.wantKind)
		}
		if p.RuleType != RuleTypeWord {
			t.Fatalf("%s ruleType = %q, want word", tc.name, p.RuleType)
		}
		if tc.wantCover && p.Card.CoverURL == "" {
			t.Fatalf("%s expected CoverURL, got empty", tc.name)
		}
		if !tc.wantCover && p.Card.CoverURL != "" {
			t.Fatalf("%s leaked CoverURL: %q", tc.name, p.Card.CoverURL)
		}
	}
}

func TestGetPostRetweetOriginEnrichmentHoistedVideoDegradationAndHintUID(t *testing.T) {
	var (
		showHits     atomic.Int32
		originShow   atomic.Int32
		nestedShow   atomic.Int32
		longTextHits atomic.Int32
	)
	mustMB := func(mid string) string {
		mb, err := MidToMblogID(mid)
		if err != nil {
			t.Fatalf("MidToMblogID(%q): %v", mid, err)
		}
		return mb
	}
	const (
		mid101 = "5000000000000101"
		mid201 = "5000000000000201"
		mid999 = "5000000000000999"
		mid102 = "5000000000000102"
		mid202 = "5000000000000202"
		mid103 = "5000000000000103"
		mid203 = "5000000000000203"
		mid104 = "5000000000000104"
		mid204 = "5000000000000204"
	)
	mb101, mb201, mb999 := mustMB(mid101), mustMB(mid201), mustMB(mid999)
	mb102, mb202 := mustMB(mid102), mustMB(mid202)
	mb103, mb203 := mustMB(mid103), mustMB(mid203)
	mb104, mb204 := mustMB(mid104), mustMB(mid204)

	longOver140 := strings.Repeat("这是一段超过一百四十字符但仍被微博截断的转发原微博正文内容测试，含话题与说明。", 4)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/ajax/statuses/show":
			showHits.Add(1)
			id := r.URL.Query().Get("id")
			switch id {
			case mb101: // Abbreviated retweet: missing pic_infos + pic_num=2 + isLongText (>140 runes), plus outer comment pic!
				_, _ = w.Write([]byte(fmt.Sprintf(`{
					"ok": 1,
					"idstr": %q,
					"mblogid": %q,
					"created_at": "Sat Nov 21 16:00:00 +0800 2026",
					"text_raw": "外层转发评语",
					"user": {"idstr": "1000000001", "screen_name": "转发博主"},
					"pic_ids": ["outerpic1"],
					"pic_infos": {
						"outerpic1": {"pic_id": "outerpic1", "type": "pic", "largest": {"url": "https://wx1.sinaimg.cn/large/outerpic1.jpg", "width": 600, "height": 600}}
					},
					"retweeted_status": {
						"idstr": %q,
						"mblogid": %q,
						"created_at": "Sat Nov 21 12:00:00 +0800 2026",
						"isLongText": true,
						"text_raw": %q,
						"pic_num": 2,
						"pic_ids": ["origpic1", "origpic2"],
						"user": {"idstr": "1000000002", "screen_name": "原作者"}
					}
				}`, mid101, mb101, mid201, mb201, longOver140)))
			case mb201: // Full origin post (also has nested retweeted_status to prove SkipOriginFetch stops recursion!)
				originShow.Add(1)
				_, _ = w.Write([]byte(fmt.Sprintf(`{
					"ok": 1,
					"idstr": %q,
					"mblogid": %q,
					"created_at": "Sat Nov 21 12:00:00 +0800 2026",
					"isLongText": true,
					"text_raw": "截断...",
					"longText": {"longTextContent": "补全后的完整原博长正文，包含全部细节。"},
					"user": {"idstr": "1000000002", "screen_name": "原作者", "avatar_hd": "https://tvax1.sinaimg.cn/public/orig.jpg"},
					"pic_num": 2,
					"pic_ids": ["origpic1", "origpic2"],
					"pic_infos": {
						"origpic1": {"pic_id": "origpic1", "type": "pic", "largest": {"url": "https://wx1.sinaimg.cn/large/origpic1.jpg", "width": 1200, "height": 900}},
						"origpic2": {"pic_id": "origpic2", "type": "livephoto", "largest": {"url": "https://wx1.sinaimg.cn/large/origpic2.jpg", "width": 1080, "height": 1440}, "video": "https://livephoto.us.sinaimg.cn/origpic2.mov?Expires=1999999999&ssig=SYNTHLP&KID=unistore,video"}
					},
					"retweeted_status": {
						"idstr": %q,
						"mblogid": %q,
						"pic_num": 3,
						"pic_ids": ["deep1", "deep2", "deep3"],
						"user": {"idstr": "1000000009", "screen_name": "更深层作者"}
					}
				}`, mid201, mb201, mid999, mb999)))
			case mb999:
				nestedShow.Add(1)
				_, _ = w.Write([]byte(fmt.Sprintf(`{"ok":1,"idstr":%q,"mblogid":%q,"text_raw":"deep","user":{"idstr":"1000000009","screen_name":"更深层作者"}}`, mid999, mb999)))
			case mb102: // Abbreviated video retweet (page_info without playback_list) -> fetches origin video post
				_, _ = w.Write([]byte(fmt.Sprintf(`{
					"ok": 1,
					"idstr": %q,
					"mblogid": %q,
					"created_at": "Sat Nov 21 16:05:00 +0800 2026",
					"text_raw": "转发微博",
					"user": {"idstr": "1000000001", "screen_name": "转发博主"},
					"retweeted_status": {
						"idstr": %q,
						"mblogid": %q,
						"created_at": "Sat Nov 21 13:00:00 +0800 2026",
						"text_raw": "原视频简介",
						"user": {"idstr": "1000000003", "screen_name": "视频原作者"},
						"page_info": {"object_type": "video", "object_id": "1034:5000000000000202", "page_title": "原视频标题"}
					}
				}`, mid102, mb102, mid202, mb202)))
			case mb202:
				_, _ = w.Write([]byte(fmt.Sprintf(`{
					"ok": 1,
					"idstr": %q,
					"mblogid": %q,
					"created_at": "Sat Nov 21 13:00:00 +0800 2026",
					"text_raw": "原视频完整说明文字",
					"user": {"idstr": "1000000003", "screen_name": "视频原作者"},
					"page_info": {
						"object_type": "video",
						"object_id": "1034:5000000000000202",
						"page_title": "高清原视频标题",
						"page_pic": "https://wx1.sinaimg.cn/large/vcover.jpg",
						"media_info": {
							"duration": 42,
							"playback_list": [
								{"meta": {"label": "mp4_1080p", "quality_index": 1080, "quality_label": "1080P"}, "play_info": {"url": "https://f.video.weibocdn.com/orig1080.mp4?Expires=1999999999&ssig=SYNTHVID&KID=unistore,video", "mime": "video/mp4", "width": 1920, "height": 1080, "video_codecs": "avc1.640028"}}
							]
						}
					}
				}`, mid202, mb202)))
			case mb103: // Longtext >140 runes on retweeted_status without inlined longText -> GetLongText called!
				_, _ = w.Write([]byte(fmt.Sprintf(`{
					"ok": 1,
					"idstr": %q,
					"mblogid": %q,
					"text_raw": "转发长文",
					"user": {"idstr": "1000000001", "screen_name": "转发博主"},
					"retweeted_status": {
						"idstr": %q,
						"mblogid": %q,
						"isLongText": true,
						"text_raw": %q,
						"user": {"idstr": "1000000004", "screen_name": "长文原作者"}
					}
				}`, mid103, mb103, mid203, mb203, longOver140)))
			case mb104: // Abbreviated origin whose statuses/show returns unavailable (20101) -> graceful degrade!
				_, _ = w.Write([]byte(fmt.Sprintf(`{
					"ok": 1,
					"idstr": %q,
					"mblogid": %q,
					"text_raw": "转发不可拉取的原博",
					"user": {"idstr": "1000000001", "screen_name": "转发博主"},
					"retweeted_status": {
						"idstr": %q,
						"mblogid": %q,
						"text_raw": "内嵌简略原博文字",
						"pic_num": 2,
						"pic_ids": ["miss1", "miss2"],
						"user": {"idstr": "1000000005", "screen_name": "原博"}
					}
				}`, mid104, mb104, mid204, mb204)))
			case mb204:
				_, _ = w.Write([]byte(`{"ok": 0, "error_code": 20101, "msg": "抱歉，此微博已被作者删除"}`))
			}
		case "/ajax/statuses/longtext":
			longTextHits.Add(1)
			if r.URL.Query().Get("id") == mb203 {
				_, _ = w.Write([]byte(`{"ok":1,"data":{"longTextContent":"超过140字符截断后补全的完整原博长正文。"}}`))
			}
		case "/ajax/statuses/mymblog":
			_, _ = w.Write([]byte(fmt.Sprintf(`{
				"ok": 1,
				"data": {
					"total": 1,
					"since_id": "",
					"list": [{
						"idstr": %q,
						"mblogid": %q,
						"text_raw": "时间线里的简略转发",
						"user": {"idstr": "1000000001", "screen_name": "转发博主"},
						"retweeted_status": {
							"idstr": %q,
							"mblogid": %q,
							"text_raw": "简略原博",
							"mix_media_ids": ["m1", "m2"],
							"user": {"idstr": "1000000002", "screen_name": "原作者"}
						}
					}]
				}
			}`, mid101, mb101, mid201, mb201)))
		}
	}))
	defer srv.Close()

	client := newFastTestClient(t, srv.URL, newTestWeiboStore(t, "SUB=SYNTHETIC_SUB_RT"))
	adapter := NewAdapter(client)

	// 1. Abbreviated retweet with missing pic_infos + >140 rune longtext -> enriched in 1 origin GetPost call, 0 nested recursion.
	p1, err := client.GetPost(context.Background(), mb101)
	if err != nil {
		t.Fatalf("GetPost(%s): %v", mb101, err)
	}
	if originShow.Load() != 1 || nestedShow.Load() != 0 {
		t.Fatalf("originShow=%d nestedShow=%d, want 1 and 0", originShow.Load(), nestedShow.Load())
	}
	if p1.OriginIncomplete || p1.Retweeted == nil || p1.Retweeted.Text != "补全后的完整原博长正文，包含全部细节。" || len(p1.Retweeted.Images) != 2 {
		t.Fatalf("unexpected enriched p1: incomplete=%v rt=%+v", p1.OriginIncomplete, p1.Retweeted)
	}
	effImgs, effVid, vidFromOrig := p1.EffectiveMediaWithOrigin()
	if len(effImgs) != 3 || effVid != nil || vidFromOrig {
		t.Fatalf("EffectiveMediaWithOrigin(p1) = (%d imgs, %+v, %v)", len(effImgs), effVid, vidFromOrig)
	}
	if effImgs[0].FromOrigin || !effImgs[1].FromOrigin || !effImgs[2].FromOrigin {
		t.Fatalf("FromOrigin flags = [%v, %v, %v], want [false, true, true]", effImgs[0].FromOrigin, effImgs[1].FromOrigin, effImgs[2].FromOrigin)
	}
	plan1, err := adapter.PlanDownloadFromPost(p1)
	if err != nil {
		t.Fatalf("PlanDownloadFromPost(p1): %v", err)
	}
	if plan1.OriginText != p1.Retweeted.Text || plan1.OriginAuthorUID != "1000000002" || len(plan1.Images) != 3 || plan1.Images[0].FromOrigin || !plan1.Images[1].FromOrigin {
		t.Fatalf("unexpected plan1 origin/media: %+v", plan1)
	}

	// 2. Abbreviated video retweet -> enriched origin video + VideoFromOrigin=true.
	p2, err := client.GetPost(context.Background(), mb102)
	if err != nil {
		t.Fatalf("GetPost(%s): %v", mb102, err)
	}
	if p2.Video != nil || p2.Retweeted == nil || p2.Retweeted.Video == nil || p2.Retweeted.Video.Selected == nil || p2.Retweeted.Video.Selected.ShortEdge != 1080 {
		t.Fatalf("expected enriched 1080P video on Retweeted only: outer=%+v rt=%+v", p2.Video, p2.Retweeted)
	}
	plan2, err := adapter.PlanDownloadFromPost(p2)
	if err != nil {
		t.Fatalf("PlanDownloadFromPost(p2): %v", err)
	}
	if plan2.Kind != "single_mp4" || !plan2.VideoFromOrigin || plan2.OriginVideoTitle != "高清原视频标题" || plan2.OriginText != "原视频完整说明文字" {
		t.Fatalf("unexpected plan2: kind=%q videoFromOrigin=%v originTitle=%q originText=%q", plan2.Kind, plan2.VideoFromOrigin, plan2.OriginVideoTitle, plan2.OriginText)
	}

	// 3. Longtext >140 runes on retweeted_status without inlined longText -> GetLongText expands it.
	p3, err := client.GetPost(context.Background(), mb103)
	if err != nil {
		t.Fatalf("GetPost(%s): %v", mb103, err)
	}
	if longTextHits.Load() != 1 || p3.Retweeted == nil || p3.Retweeted.Text != "超过140字符截断后补全的完整原博长正文。" || p3.OriginIncomplete {
		t.Fatalf("unexpected p3 longtext expansion: hits=%d incomplete=%v rt=%+v", longTextHits.Load(), p3.OriginIncomplete, p3.Retweeted)
	}

	// 4. Outer-hoisted retweet video normalized onto Retweeted.Video (never duplicated on outer).
	hoistedJSON := `{
		"ok": 1,
		"idstr": "5000000000000105",
		"mblogid": "O00000105",
		"text_raw": "外层挂了原视频",
		"user": {"idstr": "1000000001", "screen_name": "转发者"},
		"page_info": {
			"object_type": "video",
			"object_id": "1034:5000000000000105",
			"page_title": "挂在外层的原视频",
			"media_info": {"playback_list": [{"meta": {"label": "mp4_1080p", "quality_index": 1080}, "play_info": {"url": "https://f.video.weibocdn.com/hoisted.mp4", "mime": "video/mp4", "width": 1920, "height": 1080}}]}
		},
		"retweeted_status": {
			"idstr": "5000000000000205",
			"mblogid": "O00000205",
			"text_raw": "原微博正文",
			"user": {"idstr": "1000000002", "screen_name": "原作者"}
		}
	}`
	pHoist, err := client.ParseRawPostJSON([]byte(hoistedJSON))
	if err != nil {
		t.Fatalf("ParseRawPostJSON(hoisted): %v", err)
	}
	if pHoist.Video != nil || pHoist.Retweeted == nil || pHoist.Retweeted.Video == nil || pHoist.Retweeted.Video.ObjectID != "1034:5000000000000105" {
		t.Fatalf("expected outer-hoisted video moved to Retweeted.Video: outer=%+v rt=%+v", pHoist.Video, pHoist.Retweeted)
	}

	// 5. Unavailable origin during enrichment degrades gracefully without failing outer GetPost.
	p4, err := client.GetPost(context.Background(), mb104)
	if err != nil {
		t.Fatalf("GetPost(%s) should degrade gracefully: %v", mb104, err)
	}
	if p4.Retweeted == nil || p4.Retweeted.Text != "内嵌简略原博文字" {
		t.Fatalf("degraded p4 lost embedded origin: %+v", p4.Retweeted)
	}

	// 6. GetUserPosts marks incomplete retweet OriginIncomplete=true with zero extra statuses/show calls.
	showsBefore := showHits.Load()
	page, err := client.GetUserPosts(context.Background(), UserPostsRequest{UID: "1000000001", Page: 1})
	if err != nil || len(page.Posts) != 1 {
		t.Fatalf("GetUserPosts = %+v err=%v", page, err)
	}
	if !page.Posts[0].OriginIncomplete || showHits.Load() != showsBefore {
		t.Fatalf("mymblog retweet OriginIncomplete=%v extraShows=%d", page.Posts[0].OriginIncomplete, showHits.Load()-showsBefore)
	}

	// 7. ResolveLink preserves HintAuthorUID from /<uid>/<mblogid>.
	resolved, err := client.ResolveLink(context.Background(), "https://weibo.com/1000000001/"+mb101)
	if err != nil {
		t.Fatalf("ResolveLink: %v", err)
	}
	if resolved.HintAuthorUID != "1000000001" || resolved.AuthorUID != "1000000001" {
		t.Fatalf("ResolveLink HintAuthorUID=%q AuthorUID=%q, want 1000000001", resolved.HintAuthorUID, resolved.AuthorUID)
	}
}
