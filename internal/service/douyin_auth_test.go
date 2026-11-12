package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/site/douyin"
	"github.com/chialecode/trace/internal/uistate"
)

type dualSiteTestHarness struct {
	backend  *Backend
	loc      appdirs.Locations
	biliSrv  *httptest.Server
	dySrv    *httptest.Server
	biliReqs *atomic.Int64
	dyReqs   *atomic.Int64
	dyMode   *atomic.Value // "ok", "expired", "captcha"
}

func newDualSiteTestHarness(t *testing.T) *dualSiteTestHarness {
	t.Helper()

	biliReqs := &atomic.Int64{}
	dyReqs := &atomic.Int64{}
	dyMode := &atomic.Value{}
	dyMode.Store("ok")

	thumbPNG := historyThumbnailPNG(t, 64, 64)

	var biliSrv *httptest.Server
	biliSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		biliReqs.Add(1)
		switch r.URL.Path {
		case "/x/web-interface/nav":
			ck := r.Header.Get("Cookie")
			if !strings.Contains(ck, "SESSDATA=") || strings.Contains(ck, "SESSDATA=expired") {
				_, _ = fmt.Fprint(w, `{"code":-101,"message":"账号未登录","data":{"isLogin":false}}`)
				return
			}
			_, _ = fmt.Fprint(w, `{"code":0,"data":{"isLogin":true,"mid":778899,"uname":"合成B站测试用户","face":"`+biliSrv.URL+`/bfs/face/avatar.png","wbi_img":{"img_url":"https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png","sub_url":"https://i0.hdslb.com/bfs/wbi/4932caff0ff746eab6f01bf08b70ac45.png"}}}`)
		case "/x/web-interface/view", "/x/web-interface/wbi/view":
			_, _ = fmt.Fprint(w, `{"code":0,"data":{"bvid":"BV1xx411c7mD","aid":1001,"title":"合成B站视频","pic":"`+biliSrv.URL+`/bfs/archive/cover.png","pubdate":1710000000,"duration":60,"owner":{"mid":12345,"name":"合成UP主","face":"`+biliSrv.URL+`/bfs/face/up.png"},"pages":[{"cid":2001,"page":1,"part":"P1","duration":60}]}}`)
		case "/bfs/face/avatar.png", "/bfs/archive/cover.png", "/bfs/face/up.png":
			if r.Header.Get("Cookie") != "" {
				t.Errorf("bilibili CDN request leaked Cookie: %s", r.URL.Path)
			}
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(thumbPNG)
		case "/room/v1/Room/room_init":
			_, _ = fmt.Fprint(w, `{"code":0,"data":{"room_id":223344,"short_id":223,"uid":12345,"live_status":1,"live_time":1710000000}}`)
		case "/room/v1/Room/get_status_info_by_uids":
			_, _ = fmt.Fprint(w, `{"code":0,"data":{"12345":{"uid":12345,"room_id":223344,"uname":"合成UP主","title":"合成B站直播","live_status":1,"live_time":1710000000}}}`)
		case "/xlive/web-room/v2/index/getRoomPlayInfo":
			_, _ = fmt.Fprintf(w, `{"code":0,"data":{"room_id":223344,"uid":12345,"live_status":1,"playurl_info":{"playurl":{"g_qn_desc":[{"qn":10000,"desc":"原画"}],"stream":[{"protocol_name":"http_hls","format":[{"format_name":"fmp4","codec":[{"codec_name":"avc","current_qn":10000,"accept_qn":[10000],"base_url":"/live/bili.m3u8","url_info":[{"host":%q,"extra":""}]}]}]}]}}}}`, biliSrv.URL)
		case "/live/bili.m3u8":
			if r.Header.Get("Cookie") != "" {
				t.Errorf("bilibili live stream request leaked Cookie")
			}
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			w.WriteHeader(http.StatusOK)
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
			<-r.Context().Done()
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(biliSrv.Close)

	const (
		synthSelfSecUID   = "MS4wLjABAAAA_synth_self_account_000001"
		synthAuthorSecUID = "MS4wLjABAAAA_synth_creator_acct_000002"
		synthVideoID      = "7350000000000000101"
		synthNoteID       = "7350000000000000202"
	)

	var dySrv *httptest.Server
	dySrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Media / CDN endpoints must NEVER carry Cookie!
		if strings.HasPrefix(r.URL.Path, "/obj/") || strings.HasPrefix(r.URL.Path, "/media/") {
			if r.Header.Get("Cookie") != "" {
				t.Errorf("douyin media/image request leaked Cookie header on %s", r.URL.Path)
			}
			switch r.URL.Path {
			case "/obj/avatar.png", "/obj/cover.png", "/obj/note1.png":
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write(thumbPNG)
			case "/media/bgm.mp3":
				w.Header().Set("Content-Type", "audio/mpeg")
				_, _ = w.Write([]byte("ID3\x04\x00\x00\x00\x00\x00\x00synthetic-mp3-payload"))
			case "/media/live.flv":
				w.Header().Set("Content-Type", "video/x-flv")
				_, _ = w.Write([]byte("FLV\x01\x05\x00\x00\x00\x09\x00\x00\x00\x00"))
				if fl, ok := w.(http.Flusher); ok {
					fl.Flush()
				}
				<-r.Context().Done()
			default:
				http.NotFound(w, r)
			}
			return
		}

		dyReqs.Add(1)
		mode, _ := dyMode.Load().(string)
		ck := r.Header.Get("Cookie")

		if mode == "captcha" || strings.Contains(ck, "sessionid=trigger_captcha") {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = fmt.Fprint(w, `<html><head><title>验证码中间页</title></head><body><div id="verify-captcha">slide</div></body></html>`)
			return
		}
		if mode == "expired" || strings.Contains(ck, "sessionid=unauth_token") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"status_code":2483,"status_msg":"登录已过期"}`)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/aweme/v1/web/user/profile/self/":
			if !strings.Contains(ck, "sessionid=") && !strings.Contains(ck, "sessionid_ss=") {
				_, _ = fmt.Fprint(w, `{"status_code":8,"status_msg":"请先登录"}`)
				return
			}
			// Dynamic browser tickets must have been stripped before sending!
			for _, forbidden := range []string{"bd_ticket_guard", "_bd_ticket_crypt_cookie", "__security_", "x_tt_token"} {
				if strings.Contains(ck, forbidden) {
					t.Errorf("dynamic browser ticket %q was not stripped from outbound Cookie", forbidden)
				}
			}
			_, _ = fmt.Fprintf(w, `{"status_code":0,"user":{"uid":"88001122","sec_uid":%q,"unique_id":"synth_dy_self","nickname":"合成抖音用户","avatar_thumb":{"url_list":[%q]}}}`,
				synthSelfSecUID, dySrv.URL+"/obj/avatar.png")

		case "/aweme/v1/web/aweme/detail/":
			awemeID := r.URL.Query().Get("aweme_id")
			if awemeID == synthNoteID {
				_, _ = fmt.Fprintf(w, `{"status_code":0,"aweme_detail":{"aweme_id":%q,"aweme_type":68,"desc":"合成图文作品 #测试","create_time":1711000200,"author":{"uid":"88009900","sec_uid":%q,"unique_id":"synth_creator","nickname":"合成抖音创作者","avatar_thumb":{"url_list":[%q]}},"statistics":{"digg_count":42,"comment_count":5,"collect_count":9,"share_count":3,"play_count":0},"images":[{"uri":"tos-cn-i-synth/note1","width":1080,"height":1440,"url_list":[%q]}],"music":{"id_str":"99001","title":"合成配乐","author":"合成音乐人","duration":18,"is_original_sound":false,"cover_hd":{"url_list":[%q]},"play_url":{"url_list":[%q]}}}}`,
					synthNoteID, synthAuthorSecUID, dySrv.URL+"/obj/avatar.png", dySrv.URL+"/obj/note1.png?x-signature=SYNTH_URL_SIG_NOTE", dySrv.URL+"/obj/cover.png", dySrv.URL+"/media/bgm.mp3")
				return
			}
			_, _ = fmt.Fprintf(w, `{"status_code":0,"aweme_detail":{"aweme_id":%q,"aweme_type":0,"desc":"合成视频作品","create_time":1711000100,"duration":12000,"author":{"uid":"88009900","sec_uid":%q,"unique_id":"synth_creator","nickname":"合成抖音创作者","avatar_thumb":{"url_list":[%q]}},"statistics":{"digg_count":128,"comment_count":12,"collect_count":34,"share_count":7,"play_count":0},"video":{"width":1920,"height":1080,"duration":12000,"cover":{"url_list":[%q]},"bit_rate":[{"gear_name":"normal_1080_0","quality_type":10,"bit_rate":2500000,"is_bytevc1":0,"FPS":30,"format":"mp4","play_addr":{"width":1920,"height":1080,"data_size":3750000,"url_list":[%q]}},{"gear_name":"normal_720_0","quality_type":20,"bit_rate":1200000,"is_bytevc1":0,"FPS":30,"format":"mp4","play_addr":{"width":1280,"height":720,"data_size":1800000,"url_list":[%q]}}]}}}`,
				awemeID, synthAuthorSecUID, dySrv.URL+"/obj/avatar.png", dySrv.URL+"/obj/cover.png",
				dySrv.URL+"/media/v1080.mp4?a_bogus=SYNTH_BOGUS_TOKEN_1080",
				dySrv.URL+"/media/v720.mp4?a_bogus=SYNTH_BOGUS_TOKEN_720")

		case "/aweme/v1/web/user/profile/other/":
			secUID := r.URL.Query().Get("sec_user_id")
			_, _ = fmt.Fprintf(w, `{"status_code":0,"user":{"uid":"88009900","sec_uid":%q,"unique_id":"synth_creator","nickname":"合成抖音创作者","signature":"合成简介","aweme_count":2,"follower_count":512,"following_count":16,"total_favorited":"2048","avatar_larger":{"url_list":[%q]}}}`,
				secUID, dySrv.URL+"/obj/avatar.png")

		case "/aweme/v1/web/aweme/post/":
			_, _ = fmt.Fprintf(w, `{"status_code":0,"has_more":0,"max_cursor":0,"aweme_list":[{"aweme_id":%q,"aweme_type":0,"is_top":1,"desc":"合成视频作品","create_time":1711000100,"duration":12000,"author":{"uid":"88009900","sec_uid":%q,"nickname":"合成抖音创作者"},"video":{"width":1920,"height":1080,"duration":12000,"cover":{"url_list":[%q]},"bit_rate":[{"gear_name":"normal_1080_0","quality_type":10,"bit_rate":2500000,"is_bytevc1":0,"FPS":30,"format":"mp4","play_addr":{"width":1920,"height":1080,"url_list":[%q]}}]}}]}`,
				synthVideoID, synthAuthorSecUID, dySrv.URL+"/obj/cover.png", dySrv.URL+"/media/v1080.mp4")

		case "/webcast/room/web/enter/":
			_, _ = fmt.Fprintf(w, `{"status_code":0,"data":{"room_status":2,"web_rid":"88990011","user":{"id_str":"88009900","sec_uid":%q,"nickname":"合成抖音创作者","web_rid":"88990011"},"data":[{"id_str":"7359990000000000333","status":2,"title":"合成抖音直播","start_time":1711000500,"owner":{"id_str":"88009900","sec_uid":%q,"nickname":"合成抖音创作者","web_rid":"88990011"},"stream_url":{"flv_pull_url":{"FULL_HD1":%q}}}]}}`,
				synthAuthorSecUID, synthAuthorSecUID, dySrv.URL+"/media/live.flv")

		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(dySrv.Close)

	loc := testLocations(t)
	b, err := NewBackend(Options{
		Locations: loc,
		Desktop:   NewMockDesktop(),
		BilibiliOpts: bilibili.ClientOptions{
			Endpoints: bilibili.Endpoints{
				APIBase:      biliSrv.URL,
				PassportBase: biliSrv.URL,
				WWWBase:      biliSrv.URL,
				SpaceBase:    biliSrv.URL,
				DynamicBase:  biliSrv.URL,
				LiveAPIBase:  biliSrv.URL,
				LiveWebBase:  biliSrv.URL,
			},
			PacingInterval: -1,
			AllowLocalhost: true,
		},
		DouyinOpts: douyin.ClientOptions{
			Endpoints: douyin.Endpoints{
				ProfileSelf:    dySrv.URL + "/aweme/v1/web/user/profile/self/",
				ProfileOther:   dySrv.URL + "/aweme/v1/web/user/profile/other/",
				AwemeDetail:    dySrv.URL + "/aweme/v1/web/aweme/detail/",
				AwemePost:      dySrv.URL + "/aweme/v1/web/aweme/post/",
				LiveWebEnter:   dySrv.URL + "/webcast/room/web/enter/",
				LiveReflowInfo: dySrv.URL + "/webcast/room/reflow/info/",
				LiveCheckAlive: dySrv.URL + "/webcast/room/check_alive/",
			},
			MinInterval:    -1,
			JitterMax:      -1,
			AllowLocalhost: true,
		},
		DisableBgWorkers: true,
	})
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })

	return &dualSiteTestHarness{
		backend:  b,
		loc:      loc,
		biliSrv:  biliSrv,
		dySrv:    dySrv,
		biliReqs: biliReqs,
		dyReqs:   dyReqs,
		dyMode:   dyMode,
	}
}

func assertSiteAuthRequired(t *testing.T, err error, wantSite site.ID) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected auth_required error for site %q, got nil", wantSite)
	}
	if site.KindOf(err) != site.ErrKindAuthRequired {
		t.Fatalf("expected KindOf=%q, got %q (err=%v)", site.ErrKindAuthRequired, site.KindOf(err), err)
	}
	var se *site.Error
	if !errors.As(err, &se) {
		t.Fatalf("expected *site.Error via errors.As, got %T (%v)", err, err)
	}
	if wantSite != "" && se.Site != wantSite {
		t.Fatalf("expected site.Error.Site=%q, got %q (err=%v)", wantSite, se.Site, err)
	}
}

// TestDouyinOnlyLoginEntersAppAndBlocksBilibiliZeroRequests verifies that logging into Douyin alone
// grants full app entry (local library/feed/history/subscriptions + Douyin browsing/preview/cache) while
// rejecting Bilibili inputs and actions with auth_required and zero HTTP requests to Bilibili.
func TestDouyinOnlyLoginEntersAppAndBlocksBilibiliZeroRequests(t *testing.T) {
	h := newDualSiteTestHarness(t)
	ctx := context.Background()

	// 1. Neither site logged in: HasAnyLogin is false, local & remote actions fail with 0 HTTP requests.
	if h.backend.HasAnyLogin() {
		t.Fatal("expected HasAnyLogin() == false before any login")
	}
	_, err := h.backend.OpenLink(ctx, "https://www.douyin.com/video/7350000000000000101")
	assertSiteAuthRequired(t, err, "")
	_, err = h.backend.OpenLink(ctx, "https://www.bilibili.com/video/BV1xx411c7mD")
	assertSiteAuthRequired(t, err, "")
	_, err = h.backend.ListLibraryItems(library.FeedFilter{})
	assertSiteAuthRequired(t, err, "")
	if h.biliReqs.Load() != 0 || h.dyReqs.Load() != 0 {
		t.Fatalf("expected 0 requests when logged out, got bili=%d dy=%d", h.biliReqs.Load(), h.dyReqs.Load())
	}

	// 2. Log in to Douyin ONLY (with dynamic browser tickets that must be stripped).
	dySt, err := h.backend.LoginWithCookie(
		ctx,
		"douyin",
		"sessionid=SYNTH_DY_SESSION_01; sid_guard=SYNTH_SID_GUARD_01; ttwid=SYNTH_TTWID_01; bd_ticket_guard_client_data=MUST_STRIP",
		"",
	)
	if err != nil {
		t.Fatalf("LoginWithCookie(douyin): %v", err)
	}
	if !dySt.LoggedIn || dySt.Site != "douyin" || dySt.UniqueID != "synth_dy_self" {
		t.Fatalf("unexpected Douyin status after login: %+v", dySt)
	}
	if !h.backend.HasAnyLogin() {
		t.Fatal("expected HasAnyLogin() == true when Douyin is logged in")
	}
	if h.backend.StatusOf("bilibili").LoggedIn {
		t.Fatal("expected Bilibili to remain logged out")
	}
	if ren := h.backend.Renewal("douyin"); ren.Available || ren.Reason == "" {
		t.Fatalf("expected Douyin renewal to be unavailable with explanation, got %+v", ren)
	}

	// 3. Local app views work when only Douyin is logged in.
	if _, err := h.backend.ListLibraryItems(library.FeedFilter{}); err != nil {
		t.Fatalf("ListLibraryItems with Douyin-only login failed: %v", err)
	}
	if _, err := h.backend.FeedPage(library.FeedPageRequest{Limit: 10}); err != nil {
		t.Fatalf("FeedPage with Douyin-only login failed: %v", err)
	}
	if _, err := h.backend.ListSubscriptions(); err != nil {
		t.Fatalf("ListSubscriptions with Douyin-only login failed: %v", err)
	}

	// 4. Bilibili links, share text, creator preview, and cache enqueue are rejected with 0 Bilibili HTTP requests!
	biliBefore := h.biliReqs.Load()
	for _, biliInput := range []string{
		"https://www.bilibili.com/video/BV1xx411c7mD",
		"https://b23.tv/synthshort",
		"https://space.bilibili.com/12345",
		"https://live.bilibili.com/223344",
		"BV1xx411c7mD",
		"UID:12345",
	} {
		_, rErr := h.backend.Resolve(ctx, biliInput)
		assertSiteAuthRequired(t, rErr, site.Bilibili)
		_, oErr := h.backend.OpenLink(ctx, biliInput)
		assertSiteAuthRequired(t, oErr, site.Bilibili)
	}
	_, _, pErr := h.backend.PreviewAccount(ctx, "https://space.bilibili.com/12345")
	assertSiteAuthRequired(t, pErr, site.Bilibili)
	_, relErr := h.backend.RelationTags(ctx)
	assertSiteAuthRequired(t, relErr, site.Bilibili)
	if got := h.biliReqs.Load() - biliBefore; got != 0 {
		t.Fatalf("expected 0 HTTP requests to Bilibili while Bilibili is logged out, got %d", got)
	}

	// 5. Douyin video & note OpenLink, history thumbnails (without Cookie), stage preview media, creator preview, and CacheDouyinWork succeed!
	noteRes, err := h.backend.OpenLink(ctx, "7.42 复制打开抖音，看看【合成作品】 https://www.douyin.com/note/7350000000000000202 快来围观")
	if err != nil || noteRes.DouyinWork == nil || noteRes.DouyinWork.Kind != "note" {
		t.Fatalf("OpenLink Douyin note failed: res=%+v err=%v", noteRes, err)
	}
	if noteRes.DouyinWork.Music == nil || noteRes.DouyinWork.Music.Title != "合成配乐" {
		t.Fatalf("expected Douyin note music DTO, got %+v", noteRes.DouyinWork.Music)
	}
	if noteRes.HistoryThumbs == nil || noteRes.HistoryThumbs.Site != "douyin" {
		t.Fatalf("expected Douyin HistoryThumbJob, got %+v", noteRes.HistoryThumbs)
	}
	if err := h.backend.FetchHistoryThumbs(ctx, *noteRes.HistoryThumbs); err != nil {
		t.Fatalf("FetchHistoryThumbs(douyin): %v", err)
	}
	hist := h.backend.ListHistory()
	if len(hist) == 0 || hist[0].Site != "douyin" || hist[0].AvatarThumb == "" || hist[0].CoverThumb == "" {
		t.Fatalf("expected Douyin history entry with cached local thumbs, got %+v", hist)
	}

	stagedBGM, err := h.backend.StagePreviewMedia(ctx, "douyin", noteRes.DouyinWork.Music.PlayURL, ".mp3")
	if err != nil || !strings.HasSuffix(stagedBGM, ".mp3") {
		t.Fatalf("StagePreviewMedia failed: path=%q err=%v", stagedBGM, err)
	}

	acct, _, err := h.backend.PreviewAccount(ctx, "https://www.douyin.com/user/MS4wLjABAAAA_synth_creator_acct_000002")
	if err != nil || acct.Site != "douyin" || acct.UniqueID != "synth_creator" {
		t.Fatalf("PreviewAccount(douyin) failed: acct=%+v err=%v", acct, err)
	}

	task, err := h.backend.CacheDouyinWork(ctx, "7350000000000000101")
	if err != nil || task.Site != "douyin" || task.Kind != "video" {
		t.Fatalf("CacheDouyinWork failed: task=%+v err=%v", task, err)
	}

	// 6. Logging out of Douyin when Bilibili is also logged out returns HasAnyLogin() == false (back to login screen).
	if _, err := h.backend.LogoutSite("douyin"); err != nil {
		t.Fatalf("LogoutSite(douyin): %v", err)
	}
	if h.backend.HasAnyLogin() {
		t.Fatal("expected HasAnyLogin() == false after logging out the only logged-in site")
	}
	_, err = h.backend.ListLibraryItems(library.FeedFilter{})
	assertSiteAuthRequired(t, err, "")
}

// TestBilibiliOnlyLoginBlocksDouyinZeroRequests verifies the symmetric gate:
// when only Bilibili is logged in, Douyin share text, short links, video/note/user/live URLs,
// sec_uid inputs, PreviewAccount, and CacheDouyinWork fail with auth_required and 0 Douyin HTTP requests.
func TestBilibiliOnlyLoginBlocksDouyinZeroRequests(t *testing.T) {
	h := newDualSiteTestHarness(t)
	ctx := context.Background()

	st, err := h.backend.LoginWithCookie(ctx, "bilibili", "SESSDATA=SYNTH_BILI_SESS_01; DedeUserID=778899; bili_jct=SYNTH_JCT_01", "")
	if err != nil || !st.LoggedIn {
		t.Fatalf("LoginWithCookie(bilibili): st=%+v err=%v", st, err)
	}
	if !h.backend.HasAnyLogin() || h.backend.StatusOf("douyin").LoggedIn {
		t.Fatal("expected Bilibili logged in and Douyin logged out")
	}

	dyBefore := h.dyReqs.Load()
	for _, dyInput := range []string{
		"4.89 复制打开抖音 https://v.douyin.com/iSynthShort/ 看看作品",
		"https://www.douyin.com/video/7350000000000000101",
		"https://www.douyin.com/note/7350000000000000202",
		"https://www.douyin.com/user/MS4wLjABAAAA_synth_creator_acct_000002",
		"https://live.douyin.com/88990011",
		"MS4wLjABAAAA_synth_creator_acct_000002",
	} {
		_, rErr := h.backend.Resolve(ctx, dyInput)
		assertSiteAuthRequired(t, rErr, site.Douyin)
		_, oErr := h.backend.OpenLink(ctx, dyInput)
		assertSiteAuthRequired(t, oErr, site.Douyin)
	}
	_, wErr := h.backend.DouyinWork(ctx, "7350000000000000101")
	assertSiteAuthRequired(t, wErr, site.Douyin)
	_, uErr := h.backend.DouyinUser(ctx, "MS4wLjABAAAA_synth_creator_acct_000002")
	assertSiteAuthRequired(t, uErr, site.Douyin)
	_, cErr := h.backend.CacheDouyinWork(ctx, "7350000000000000101")
	assertSiteAuthRequired(t, cErr, site.Douyin)
	_, _, pErr := h.backend.PreviewAccount(ctx, "https://www.douyin.com/user/MS4wLjABAAAA_synth_creator_acct_000002")
	assertSiteAuthRequired(t, pErr, site.Douyin)

	if got := h.dyReqs.Load() - dyBefore; got != 0 {
		t.Fatalf("expected 0 HTTP requests to Douyin while Douyin is logged out, got %d", got)
	}

	// Meanwhile Bilibili OpenLink works normally.
	biliRes, err := h.backend.OpenLink(ctx, "https://www.bilibili.com/video/BV1xx411c7mD")
	if err != nil || biliRes.Video == nil || biliRes.Video.BVID != "BV1xx411c7mD" {
		t.Fatalf("expected Bilibili OpenLink to succeed, got res=%+v err=%v", biliRes, err)
	}
}

// TestPerSiteExpiryAndLogoutIsolation verifies that credential expiry or logout on one site
// pauses ONLY that site's queued/running tasks, emits site-scoped EventAccountChanged, blocks resuming
// that site's paused tasks, and leaves the other site's credential and active tasks untouched.
func TestPerSiteExpiryAndLogoutIsolation(t *testing.T) {
	h := newDualSiteTestHarness(t)
	ctx := context.Background()

	if _, err := h.backend.LoginWithCookie(ctx, "bilibili", "SESSDATA=SYNTH_BILI_SESS_02; DedeUserID=778899", ""); err != nil {
		t.Fatalf("bilibili login: %v", err)
	}
	if _, err := h.backend.LoginWithCookie(ctx, "douyin", "sessionid=SYNTH_DY_SESSION_02", ""); err != nil {
		t.Fatalf("douyin login: %v", err)
	}

	var gotDyAuthEvt atomic.Bool
	unsub := h.backend.SubscribeEvents(func(ev Event) {
		if ev.Type == EventAccountChanged && ev.Site == "douyin" {
			gotDyAuthEvt.Store(true)
		}
	})
	defer unsub()

	// Seed one running/queued task per site directly in download_tasks.
	now := time.Now().UTC().Format(time.RFC3339)
	db, err := sql.Open("sqlite", filepath.Join(h.loc.DataDir, "library.db"))
	if err != nil {
		t.Fatalf("open library.db: %v", err)
	}
	defer db.Close()
	for _, row := range []struct {
		id, site, status string
	}{
		{"dl-bili-active", "bilibili", "queued"},
		{"dl-dy-active", "douyin", "queued"},
	} {
		if _, err := db.Exec(
			`INSERT INTO download_tasks (task_id, site, kind, target_id, status, created_at, updated_at) VALUES (?, ?, 'video', ?, ?, ?, ?)`,
			row.id, row.site, row.id, row.status, now, now,
		); err != nil {
			t.Fatalf("seed task %s: %v", row.id, err)
		}
	}

	// Also start one active live recording on Bilibili and one on Douyin simultaneously!
	biliRec, err := h.backend.StartLiveRecordingByRoom(ctx, "bilibili", "223344")
	if err != nil || !biliRec.Recording {
		t.Fatalf("StartLiveRecordingByRoom(bilibili): st=%+v err=%v", biliRec, err)
	}
	dyRec, err := h.backend.StartLiveRecordingByRoom(ctx, "douyin", "88990011")
	if err != nil || !dyRec.Recording {
		t.Fatalf("StartLiveRecordingByRoom(douyin): st=%+v err=%v", dyRec, err)
	}
	if activeTasks, activeRecs := h.backend.ActiveWork(); activeTasks != 2 || activeRecs != 2 {
		t.Fatalf("expected ActiveWork=(2, 2), got (%d, %d)", activeTasks, activeRecs)
	}

	// Trigger Douyin credential expiry during an API request (status_code=2483).
	h.dyMode.Store("expired")
	_, expErr := h.backend.DouyinWork(ctx, "7350000000000000101")
	assertSiteAuthRequired(t, expErr, site.Douyin)

	// Douyin is now expired, while Bilibili remains logged_in!
	if dySt := h.backend.StatusOf("douyin"); dySt.LoggedIn || dySt.State != "expired" {
		t.Fatalf("expected Douyin state=expired, got %+v", dySt)
	}
	if biliSt := h.backend.StatusOf("bilibili"); !biliSt.LoggedIn || biliSt.State != "logged_in" {
		t.Fatalf("expected Bilibili to stay logged_in, got %+v", biliSt)
	}

	// Only Douyin's task was paused AND only Douyin's live recording stopped; Bilibili's task AND live recording remain active!
	dyDetail, ok, err := h.backend.TaskDetail("dl-dy-active")
	if err != nil || !ok || dyDetail.Task.Status != "paused" || dyDetail.Task.PauseReason != "auth_required" {
		t.Fatalf("expected dl-dy-active paused with auth_required, got %+v (ok=%v err=%v)", dyDetail.Task, ok, err)
	}
	biliDetail, ok, err := h.backend.TaskDetail("dl-bili-active")
	if err != nil || !ok || biliDetail.Task.Status != "queued" {
		t.Fatalf("expected dl-bili-active to remain queued, got %+v (ok=%v err=%v)", biliDetail.Task, ok, err)
	}
	if activeTasks, activeRecs := h.backend.ActiveWork(); activeTasks != 1 || activeRecs != 1 {
		t.Fatalf("expected ActiveWork=(1, 1) after Douyin expiry (Bilibili task+recording untouched), got (%d, %d)", activeTasks, activeRecs)
	}
	for _, acct := range h.backend.ListLiveAccounts() {
		if acct.Site == "douyin" && acct.Recording {
			t.Fatalf("expected Douyin live recording stopped after Douyin expiry, got %+v", acct)
		}
		if acct.Site == "bilibili" && !acct.Recording {
			t.Fatalf("expected Bilibili live recording still active after Douyin expiry, got %+v", acct)
		}
	}

	// Resuming Douyin's paused task while Douyin is expired must fail with Douyin auth_required!
	_, resErr := h.backend.ResumeTask("dl-dy-active")
	assertSiteAuthRequired(t, resErr, site.Douyin)

	// Verify an EventAccountChanged carrying Site == "douyin" was emitted.
	if !gotDyAuthEvt.Load() {
		t.Fatal("expected EventAccountChanged with Site == \"douyin\" upon Douyin expiry")
	}

	// Re-login Douyin, resume Douyin task + restart Douyin recording, then Logout Bilibili -> only Bilibili task & recording stop!
	h.dyMode.Store("ok")
	if _, err := h.backend.LoginWithCookie(ctx, "douyin", "sessionid=SYNTH_DY_SESSION_03", ""); err != nil {
		t.Fatalf("re-login douyin: %v", err)
	}
	if resumed, err := h.backend.ResumeTask("dl-dy-active"); err != nil || resumed.Status != "queued" {
		t.Fatalf("expected ResumeTask(dl-dy-active) to succeed after re-login, got %+v err=%v", resumed, err)
	}
	if dyRec2, err := h.backend.StartLiveRecordingByRoom(ctx, "douyin", "88990011"); err != nil || !dyRec2.Recording {
		t.Fatalf("restart Douyin recording after re-login: st=%+v err=%v", dyRec2, err)
	}

	if _, err := h.backend.LogoutSite("bilibili"); err != nil {
		t.Fatalf("LogoutSite(bilibili): %v", err)
	}
	biliTaskAfter, _, _ := h.backend.TaskDetail("dl-bili-active")
	dyTaskAfter, _, _ := h.backend.TaskDetail("dl-dy-active")
	if biliTaskAfter.Task.Status != "paused" || biliTaskAfter.Task.PauseReason != "auth_required" {
		t.Fatalf("expected Bilibili task paused on Bilibili logout, got %+v", biliTaskAfter.Task)
	}
	if dyTaskAfter.Task.Status != "queued" {
		t.Fatalf("expected Douyin task untouched by Bilibili logout, got %+v", dyTaskAfter.Task)
	}
	if activeTasks, activeRecs := h.backend.ActiveWork(); activeTasks != 1 || activeRecs != 1 {
		t.Fatalf("expected ActiveWork=(1, 1) after Bilibili logout (Douyin task+recording untouched), got (%d, %d)", activeTasks, activeRecs)
	}
	_, _ = h.backend.StopLiveRecordingByKey("douyin:live:7359990000000000333")
	if !h.backend.HasAnyLogin() {
		t.Fatal("expected HasAnyLogin() == true because Douyin is still logged in")
	}
}

// TestServiceDouyinCookieFailureModesAndSentinelRedaction verifies the three Douyin Cookie login
// failure modes preserve existing credentials without echoing raw secrets, and verifies runtime logs,
// diagnostics, settings.json, ui-state.json, and history.json contain zero Douyin Cookie or signed-URL sentinels.
func TestServiceDouyinCookieFailureModesAndSentinelRedaction(t *testing.T) {
	h := newDualSiteTestHarness(t)
	ctx := context.Background()

	const (
		sentinelSessionID = "SYNTH_SECRET_SESSIONID_9001"
		sentinelGuard     = "SYNTH_SECRET_SID_GUARD_9002"
		sentinelTTWID     = "SYNTH_SECRET_TTWID_9003"
		sentinelMsToken   = "SYNTH_SECRET_MSTOKEN_9004"
		sentinelBogus     = "SYNTH_SECRET_ABOGUS_9005"
		sentinelXSig      = "SYNTH_SECRET_XSIG_9006"
	)

	// Failure 1: Guest-only ttwid/msToken Cookie -> invalid_input, 0 HTTP requests, no secret echoed.
	beforeReqs := h.dyReqs.Load()
	_, err := h.backend.LoginWithCookie(ctx, "douyin", "ttwid="+sentinelTTWID+"; msToken="+sentinelMsToken, "")
	if site.KindOf(err) != site.ErrKindInvalidInput {
		t.Fatalf("expected invalid_input for guest-only Cookie, got %v", err)
	}
	if h.dyReqs.Load() != beforeReqs {
		t.Fatalf("expected 0 HTTP requests on missing sessionid, got %d", h.dyReqs.Load()-beforeReqs)
	}
	if strings.Contains(err.Error(), sentinelTTWID) || strings.Contains(err.Error(), sentinelMsToken) {
		t.Fatalf("error echoed raw Cookie value: %v", err)
	}

	// Failure 2: Unauthenticated sessionid -> auth_required, not saved.
	_, err = h.backend.LoginWithCookie(ctx, "douyin", "sessionid=unauth_token; ttwid="+sentinelTTWID, "")
	if site.KindOf(err) != site.ErrKindAuthRequired || h.backend.StatusOf("douyin").LoggedIn {
		t.Fatalf("expected auth_required without saving, got err=%v st=%+v", err, h.backend.StatusOf("douyin"))
	}

	// Failure 3: Captcha / verification page -> risk_control, not saved.
	_, err = h.backend.LoginWithCookie(ctx, "douyin", "sessionid=trigger_captcha; ttwid="+sentinelTTWID, "")
	if site.KindOf(err) != site.ErrKindRiskControl || h.backend.StatusOf("douyin").LoggedIn {
		t.Fatalf("expected risk_control without saving, got err=%v st=%+v", err, h.backend.StatusOf("douyin"))
	}

	// Valid login succeeds, and subsequent failed logins NEVER overwrite the active Douyin credential!
	validRaw := fmt.Sprintf("sessionid=%s; sid_guard=%s; ttwid=%s; msToken=%s",
		sentinelSessionID, sentinelGuard, sentinelTTWID, sentinelMsToken)
	st, err := h.backend.LoginWithCookie(ctx, "douyin", validRaw, "")
	if err != nil || !st.LoggedIn {
		t.Fatalf("valid Douyin login failed: st=%+v err=%v", st, err)
	}
	_, _ = h.backend.LoginWithCookie(ctx, "douyin", "sessionid=unauth_token", "")
	_, _ = h.backend.LoginWithCookie(ctx, "douyin", "sessionid=trigger_captcha", "")
	if after := h.backend.StatusOf("douyin"); !after.LoggedIn || after.UniqueID != "synth_dy_self" {
		t.Fatalf("failed subsequent LoginWithCookie overwrote active Douyin credential: %+v", after)
	}

	// Trigger history write, UI state write, and explicit log lines containing sentinels & signed URLs.
	_, _ = h.backend.OpenLink(ctx, "https://www.douyin.com/note/7350000000000000202")
	_, _ = h.backend.UpdateUIState(uistate.State{LastRoute: "parse", FeedFilter: uistate.FeedFilter{Site: "douyin"}})
	h.backend.logInfo("synthetic cookie leak check: sessionid=" + sentinelSessionID + "; sid_guard=" + sentinelGuard + "; msToken=" + sentinelMsToken)
	h.backend.logInfo("synthetic signed url check: https://v26-web.douyinvod.com/video/tos/mp4/?a_bogus=" + sentinelBogus + "&x-signature=" + sentinelXSig)

	sentinels := []string{
		sentinelSessionID,
		sentinelGuard,
		sentinelTTWID,
		sentinelMsToken,
		sentinelBogus,
		sentinelXSig,
		"SYNTH_URL_SIG_NOTE",
	}

	// Scan RecentLogs(500), DiagnosticsSnapshot JSON, and plaintext files on disk.
	lines, err := h.backend.RecentLogs(500)
	if err != nil {
		t.Fatalf("RecentLogs: %v", err)
	}
	joinedLogs := strings.Join(lines, "\n")
	diagJSON, _ := json.Marshal(h.backend.DiagnosticsSnapshot())

	for _, s := range sentinels {
		if strings.Contains(joinedLogs, s) {
			t.Fatalf("RecentLogs leaked sentinel %q", s)
		}
		if strings.Contains(string(diagJSON), s) {
			t.Fatalf("DiagnosticsSnapshot leaked sentinel %q", s)
		}
	}

	for _, p := range []string{
		filepath.Join(h.loc.LogsDir, "trace.log"),
		filepath.Join(h.loc.DataDir, "history.json"),
		filepath.Join(h.loc.DataDir, "settings.json"),
		filepath.Join(h.loc.DataDir, "ui-state.json"),
		filepath.Join(h.loc.CredentialsDir, "douyin.bin"),
	} {
		raw, err := os.ReadFile(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatalf("ReadFile(%s): %v", p, err)
		}
		for _, s := range sentinels {
			if strings.Contains(string(raw), s) {
				t.Fatalf("file %s contained plaintext sentinel %q", filepath.Base(p), s)
			}
		}
	}
}

func TestDouyinWorkPlannerStreamSelectionAndDowngrade(t *testing.T) {
	h := newDualSiteTestHarness(t)
	ctx := context.Background()

	if _, err := h.backend.LoginWithCookie(ctx, "douyin", "sessionid=SYNTH_DY_PLANNER_01", ""); err != nil {
		t.Fatalf("douyin login: %v", err)
	}

	planner := &douyinWorkPlanner{backend: h.backend}

	// 1. VideoQuality="720" selects the 720p stream rather than 1080p.
	plan720, err := planner.PlanWorkDownload(ctx, "7350000000000000101", download.WorkPlanOptions{
		VideoQuality: "720",
		CodecOrder:   []string{"avc", "hevc"},
	})
	if err != nil {
		t.Fatalf("PlanWorkDownload(720): %v", err)
	}
	if plan720.Height != 720 || plan720.DowngradeHint != "" {
		t.Fatalf("expected 720p stream without downgrade hint, got height=%d hint=%q", plan720.Height, plan720.DowngradeHint)
	}

	// 2. VideoQuality="2160" falls back to highest available (1080p) with an explicit downgrade hint.
	plan4k, err := planner.PlanWorkDownload(ctx, "7350000000000000101", download.WorkPlanOptions{
		VideoQuality: "2160",
		CodecOrder:   []string{"avc", "hevc"},
	})
	if err != nil {
		t.Fatalf("PlanWorkDownload(2160): %v", err)
	}
	if plan4k.Height != 1080 || plan4k.DowngradeHint == "" {
		t.Fatalf("expected 1080p stream with downgrade hint when requesting 2160p, got height=%d hint=%q", plan4k.Height, plan4k.DowngradeHint)
	}
}
