package service

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/site/douyin"
	"github.com/chialecode/trace/internal/subscription"
)

// Sentinels that must never reach a file or the log.
const (
	acctSentinelTitle     = "SENTINEL_FAV_TITLE_7f3a"
	acctSentinelName      = "SENTINEL_FOLLOW_NICK_9b2c"
	acctSentinelDY        = "SENTINEL_DY_LIKE_DESC_4e1d"
	acctSentinelDYHistory = "SENTINEL_DY_HIST_DESC_8c4f"
	acctSelfSecUID        = "MS4wLjABAAAA_synth_self_account_000001"
	acctFollowSecUID      = "MS4wLjABAAAA_synth_follow_acct_0000003"
)

type accountHarness struct {
	b        *Backend
	biliReqs atomic.Int64
	dyReqs   atomic.Int64
	biliMode atomic.Value
	dyMode   atomic.Value
	paths    chan string
}

func newAccountHarness(t *testing.T) *accountHarness {
	t.Helper()
	h := &accountHarness{paths: make(chan string, 256)}
	h.biliMode.Store("")
	h.dyMode.Store("")
	var bili *httptest.Server
	bili = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.biliReqs.Add(1)
		select {
		case h.paths <- r.URL.Path:
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/x/web-interface/nav" {
			switch h.biliMode.Load().(string) {
			case "risk":
				fmt.Fprint(w, `{"code":-352,"message":"风控校验失败"}`)
				return
			}
		}
		q := r.URL.Query()
		switch r.URL.Path {
		case "/x/web-interface/nav":
			fmt.Fprint(w, `{"code":0,"data":{"isLogin":true,"mid":778899,"uname":"自己","wbi_img":{"img_url":"https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png","sub_url":"https://i0.hdslb.com/bfs/wbi/4932caff0ff746eab6f01bf08b70ac45.png"}}}`)
		case "/x/relation/tags":
			fmt.Fprint(w, `{"code":0,"data":[{"tagid":-10,"name":"特别关注","count":1},{"tagid":0,"name":"默认分组","count":2}]}`)
		case "/x/relation/followings":
			if q.Get("pn") == "1" {
				fmt.Fprintf(w, `{"code":0,"data":{"total":3,"list":[{"mid":12345,"uname":%q,"face":"https://i0.hdslb.com/f.jpg","sign":"第一行\n第二行"},{"mid":12346,"uname":"乙"}]}}`, acctSentinelName)
				return
			}
			fmt.Fprint(w, `{"code":0,"data":{"total":3,"list":[{"mid":12347,"uname":"丙"}]}}`)
		case "/x/relation/tag":
			fmt.Fprint(w, `{"code":0,"data":[{"mid":12345,"uname":"特别的人"}]}`)
		case "/x/v3/fav/folder/created/list-all":
			fmt.Fprint(w, `{"code":0,"data":{"count":2,"list":[{"id":2002,"attr":3,"title":"私密夹","media_count":0},{"id":2001,"attr":0,"title":"默认收藏夹","media_count":3}]}}`)
		case "/x/v3/fav/resource/list":
			if q.Get("media_id") == "2002" {
				fmt.Fprint(w, `{"code":0,"data":{"info":{"media_count":0},"medias":null,"has_more":false}}`)
				return
			}
			if q.Get("pn") == "1" {
				fmt.Fprintf(w, `{"code":0,"data":{"info":{"media_count":3},"has_more":true,"medias":[{"id":1,"type":2,"title":%q,"bvid":"BV1xx411c7mD","attr":0,"upper":{"mid":5,"name":"UP"}},{"id":2,"type":2,"title":"已失效视频","bvid":"BV1yy411c7mD","attr":9},{"id":3,"type":12,"title":"音频","attr":0}]}}`, acctSentinelTitle)
				return
			}
			fmt.Fprint(w, `{"code":0,"data":{"info":{"media_count":4},"has_more":false,"medias":[{"id":4,"type":24,"title":"番","attr":0,"ogv":{"type_name":"番剧","season_id":9}}]}}`)
		case "/x/v2/history/toview/web":
			fmt.Fprint(w, `{"code":0,"data":{"count":1,"list":[{"aid":1,"bvid":"BV1xx411c7mD","title":"稍后","duration":100,"progress":-1,"owner":{"mid":5,"name":"UP"}}]}}`)
		case "/x/web-interface/history/cursor":
			if q.Get("max") == "0" {
				fmt.Fprint(w, `{"code":0,"data":{"cursor":{"max":10,"view_at":99,"business":"archive"},"list":[
					{"title":"V","history":{"oid":1,"bvid":"BV1xx411c7mD","business":"archive"},"view_at":100,"author_mid":5,"author_name":"UP"},
					{"title":"L","history":{"oid":213,"business":"live"},"author_mid":77,"live_status":1},
					{"title":"A","uri":"https://www.bilibili.com/read/cv1","history":{"oid":6,"business":"article"}},
					{"title":"Evil","uri":"http://evil.example.com/x","history":{"oid":7,"business":"article"}}]}}`)
				return
			}
			fmt.Fprint(w, `{"code":0,"data":{"cursor":{"max":0,"view_at":0,"business":""},"list":[]}}`)
		case "/x/web-interface/view", "/x/web-interface/wbi/view":
			fmt.Fprint(w, `{"code":0,"data":{"bvid":"BV1xx411c7mD","aid":1001,"title":"打开的视频","pubdate":1710000000,"duration":60,"owner":{"mid":5,"name":"UP"},"pages":[{"cid":2001,"page":1,"part":"P1","duration":60}]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(bili.Close)

	var dy *httptest.Server
	dy = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.dyReqs.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/aweme/v1/web/user/profile/self/" {
			switch h.dyMode.Load().(string) {
			case "shell":
				fmt.Fprint(w, `{"status_code":0}`)
				return
			case "empty":
				if strings.HasSuffix(r.URL.Path, "/aweme/favorite/") {
					return // HTTP 200, no body: 喜欢 refuses non-browser requests (2026-11-14 probe)
				}
			case "captcha":
				w.Header().Set("Content-Type", "text/html")
				fmt.Fprint(w, `<html>验证码中间页</html>`)
				return
			}
		}
		node := func(id, desc string) string {
			return fmt.Sprintf(`{"aweme_id":%q,"aweme_type":0,"desc":%q,"create_time":1711000100,"duration":12000,"author":{"uid":"9","sec_uid":"MS4wLjABAAAA_synth_creator_acct_000002","nickname":"作者"},"video":{"width":1920,"height":1080,"duration":12000,"cover":{"url_list":[%q]},"bit_rate":[{"gear_name":"normal_1080_0","quality_type":10,"bit_rate":2500000,"is_bytevc1":0,"FPS":30,"format":"mp4","play_addr":{"width":1920,"height":1080,"url_list":[%q]}}]}}`,
				id, desc, dy.URL+"/obj/cover.png", dy.URL+"/media/v.mp4")
		}
		switch r.URL.Path {
		case "/aweme/v1/web/user/profile/self/":
			fmt.Fprintf(w, `{"status_code":0,"user":{"uid":"88001122","sec_uid":%q,"unique_id":"self","nickname":"自己"}}`, acctSelfSecUID)
		case "/aweme/v1/web/user/following/list/":
			fmt.Fprintf(w, `{"status_code":0,"followings":[{"uid":"3","sec_uid":%q,"nickname":"关注的人","unique_id":"fan3","signature":"签名"}],"has_more":false,"min_time":1}`, acctFollowSecUID)
		case "/aweme/v1/web/aweme/favorite/":
			fmt.Fprintf(w, `{"status_code":0,"aweme_list":[%s],"has_more":0,"max_cursor":0}`, node("7350000000000000111", acctSentinelDY))
		case "/aweme/v1/web/aweme/listcollection/":
			if r.Method != http.MethodPost {
				http.Error(w, "Unsupported path(Janus)", http.StatusNotFound)
				return
			}
			fmt.Fprintf(w, `{"status_code":0,"aweme_list":[%s],"has_more":1,"cursor":1}`, node("7350000000000000222", "收藏"))
		case "/aweme/v1/web/collects/list/":
			fmt.Fprint(w, `{"status_code":0,"collects_list":[{"collects_id_str":"7300000000000000127","collects_name":"做饭","total_number":3}],"has_more":false,"cursor":1}`)
		case "/aweme/v1/web/collects/video/list/":
			fmt.Fprintf(w, `{"status_code":0,"aweme_list":[%s],"has_more":false,"cursor":1}`, node("7350000000000000333", "夹里"))
		case "/aweme/v1/web/history/read/":
			if r.URL.Query().Get("max_cursor") == "1732400100" {
				fmt.Fprintf(w, `{"status_code":0,"status_msg":"","aweme_date":{"7350000000000000445":1732400050},"aweme_list":[%s],"has_more":0,"max_cursor":0}`, node("7350000000000000445", "历史第二页"))
				return
			}
			fmt.Fprintf(w, `{"status_code":0,"status_msg":"","aweme_date":{"7350000000000000444":1732400200},"aweme_list":[%s,{"aweme_id":"7350000000000000446","aweme_type":101,"desc":"直播占位"}],"has_more":1,"max_cursor":1732400100}`, node("7350000000000000444", acctSentinelDYHistory))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(dy.Close)

	b, err := NewBackend(Options{
		Locations: testLocations(t),
		Desktop:   NewMockDesktop(),
		BilibiliOpts: bilibili.ClientOptions{
			Endpoints:      bilibili.Endpoints{APIBase: bili.URL, PassportBase: bili.URL, WWWBase: bili.URL, SpaceBase: bili.URL, DynamicBase: bili.URL, LiveAPIBase: bili.URL, LiveWebBase: bili.URL},
			PacingInterval: -1,
			AllowLocalhost: true,
		},
		DouyinOpts: douyin.ClientOptions{
			Endpoints: douyin.Endpoints{
				ProfileSelf: dy.URL + "/aweme/v1/web/user/profile/self/", ProfileOther: dy.URL + "/aweme/v1/web/user/profile/other/",
				AwemeDetail: dy.URL + "/aweme/v1/web/aweme/detail/", AwemePost: dy.URL + "/aweme/v1/web/aweme/post/",
				LiveWebEnter: dy.URL + "/webcast/room/web/enter/", LiveReflowInfo: dy.URL + "/r/", LiveCheckAlive: dy.URL + "/c/",
			},
			MinInterval: -1, JitterMax: -1, AllowLocalhost: true,
		},
		DisableBgWorkers: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	h.b = b
	return h
}

func (h *accountHarness) login(t *testing.T, sites ...string) {
	t.Helper()
	for _, s := range sites {
		cookie := "SESSDATA=SYNTH_ACCT_SESS; DedeUserID=778899; bili_jct=SYNTH_JCT"
		if s == "douyin" {
			cookie = "sessionid=SYNTH_ACCT_DY_SESSION"
		}
		if st, err := h.b.LoginWithCookie(context.Background(), s, cookie, ""); err != nil || !st.LoggedIn {
			t.Fatalf("login %s: %+v %v", s, st, err)
		}
	}
}

func TestAccountSectionsLoginGate(t *testing.T) {
	h := newAccountHarness(t)
	ctx := context.Background()
	for _, s := range []string{"bilibili", "douyin"} {
		secs := h.b.AccountSections(s)
		if len(secs) != 1 || secs[0].Kind != AccountLogin || secs[0].Available || secs[0].Reason != "未登录" {
			t.Fatalf("%s logged out sections = %+v", s, secs)
		}
		if _, err := h.b.AccountPage(ctx, s, AccountFollowing, "", ""); !site.IsKind(err, site.ErrKindAuthRequired) {
			t.Fatalf("%s page before login: %v", s, err)
		}
	}
	if h.biliReqs.Load() != 0 || h.dyReqs.Load() != 0 {
		t.Fatalf("logged-out 我的 sent requests: bili=%d dy=%d", h.biliReqs.Load(), h.dyReqs.Load())
	}
	h.login(t, "bilibili")
	got := h.b.AccountSections("bilibili")
	var kinds []string
	for _, s := range got {
		kinds = append(kinds, s.Kind)
	}
	if strings.Join(kinds, ",") != "following,favorites,toview,history" {
		t.Fatalf("bilibili sections = %v", kinds)
	}
	if secs := h.b.AccountSections("douyin"); len(secs) != 1 || secs[0].Kind != AccountLogin {
		t.Fatalf("douyin still logged out: %+v", secs)
	}
	h.login(t, "douyin")
	kinds = nil
	for _, s := range h.b.AccountSections("douyin") {
		kinds = append(kinds, s.Kind)
	}
	if strings.Join(kinds, ",") != "following,likes,collection,watch_history" {
		t.Fatalf("douyin sections = %v, want following,likes,collection,watch_history", kinds)
	}
	// Feature gate still blocks watch_history cleanly when disabled.
	prevWatchHistory := accountWatchHistoryEnabled
	accountWatchHistoryEnabled = false
	defer func() { accountWatchHistoryEnabled = prevWatchHistory }()
	if _, err := h.b.AccountPage(ctx, "douyin", AccountWatchHistory, "", ""); !site.IsKind(err, site.ErrKindBadRequest) {
		t.Fatalf("disabled 观看历史 must be refused: %v", err)
	}
	accountWatchHistoryEnabled = prevWatchHistory
	if _, err := h.b.LogoutSite("bilibili"); err != nil {
		t.Fatal(err)
	}
	before := h.biliReqs.Load()
	if _, err := h.b.AccountPage(ctx, "bilibili", AccountFavorites, "", ""); !site.IsKind(err, site.ErrKindAuthRequired) {
		t.Fatalf("after logout: %v", err)
	}
	if h.biliReqs.Load() != before {
		t.Fatal("logged-out page sent a request")
	}
}

func TestAccountBilibiliListsMapping(t *testing.T) {
	h := newAccountHarness(t)
	h.login(t, "bilibili")
	ctx := context.Background()

	folders, err := h.b.AccountFolders(ctx, "bilibili", AccountFavorites)
	if err != nil || len(folders) != 2 || !folders[0].IsDefault || folders[0].ID != "2001" || !folders[1].Private {
		t.Fatalf("fav folders = %+v err=%v", folders, err)
	}
	p1, err := h.b.AccountPage(ctx, "bilibili", AccountFavorites, "", "")
	if err != nil || !p1.HasMore || len(p1.Items) != 3 {
		t.Fatalf("fav p1 = %+v err=%v", p1, err)
	}
	v, gone, audio := p1.Items[0], p1.Items[1], p1.Items[2]
	if v.Kind != AccountItemVideo || v.OpenURL != "https://www.bilibili.com/video/BV1xx411c7mD" || v.CacheKey != "bilibili:video:BV1xx411c7mD" {
		t.Fatalf("video = %+v", v)
	}
	if !gone.Invalid || gone.Kind != AccountItemUnsupported || gone.Title != "已失效" || gone.BrowserURL != "" || gone.OpenURL != "" {
		t.Fatalf("invalid = %+v", gone)
	}
	if audio.Kind != AccountItemUnsupported || audio.TypeLabel != "音频" || !strings.HasPrefix(audio.BrowserURL, "https://www.bilibili.com/audio/") || audio.OpenURL != "" {
		t.Fatalf("audio = %+v", audio)
	}
	p2, err := h.b.AccountPage(ctx, "bilibili", AccountFavorites, "2001", p1.Cursor)
	if err != nil || p2.HasMore || len(p2.Items) != 1 || p2.Items[0].TypeLabel != "番剧" || p2.Cursor != "" {
		t.Fatalf("fav p2 = %+v err=%v", p2, err)
	}
	empty, err := h.b.AccountPage(ctx, "bilibili", AccountFavorites, "2002", "")
	if err != nil || len(empty.Items) != 0 || empty.HasMore {
		t.Fatalf("empty folder = %+v err=%v", empty, err)
	}

	tv, err := h.b.AccountPage(ctx, "bilibili", AccountToView, AccountFolderAll, "")
	if err != nil || len(tv.Items) != 1 || !tv.Items[0].Finished || tv.Total != 1 || tv.HasMore {
		t.Fatalf("toview = %+v err=%v", tv, err)
	}

	hist, err := h.b.AccountPage(ctx, "bilibili", AccountHistory, "", "")
	if err != nil || !hist.HasMore || len(hist.Items) != 4 {
		t.Fatalf("history = %+v err=%v", hist, err)
	}
	if hist.Items[0].Kind != AccountItemVideo || hist.Items[1].Kind != AccountItemUser || hist.Items[1].OpenURL != "https://space.bilibili.com/77" || !hist.Items[1].Live {
		t.Fatalf("history archive/live = %+v", hist.Items[:2])
	}
	if hist.Items[2].Kind != AccountItemUnsupported || hist.Items[2].BrowserURL != "https://www.bilibili.com/read/cv1" || hist.Items[3].BrowserURL != "" {
		t.Fatalf("history article/off-site = %+v", hist.Items[2:])
	}
	end, err := h.b.AccountPage(ctx, "bilibili", AccountHistory, "", hist.Cursor)
	if err != nil || end.HasMore {
		t.Fatalf("history end = %+v err=%v", end, err)
	}

	// 关注 with groups, subscription ownership before and after adding.
	groups, err := h.b.AccountFolders(ctx, "bilibili", AccountFollowing)
	if err != nil || len(groups) != 3 || groups[0].ID != AccountFolderAll || groups[1].ID != "-10" {
		t.Fatalf("groups = %+v err=%v", groups, err)
	}
	f1, err := h.b.AccountPage(ctx, "bilibili", AccountFollowing, AccountFolderAll, "")
	if err != nil || !f1.HasMore || len(f1.Items) != 2 || f1.Items[0].Signature != "第一行" || f1.Items[0].OwnerID != "" || f1.Items[0].AccountKey == "" {
		t.Fatalf("followings = %+v err=%v", f1, err)
	}
	sub, err := h.b.CreateSubscription("订阅甲", "", []subscription.AccountRef{{Site: "bilibili", UID: "12345", Name: "甲"}}, subscription.SubRules{})
	if err != nil {
		t.Fatal(err)
	}
	f1, _ = h.b.AccountPage(ctx, "bilibili", AccountFollowing, AccountFolderAll, "")
	if f1.Items[0].OwnerID != sub.ID || f1.Items[0].OwnerName != "订阅甲" {
		t.Fatalf("owner after subscribe = %+v", f1.Items[0])
	}
	f2, err := h.b.AccountPage(ctx, "bilibili", AccountFollowing, AccountFolderAll, f1.Cursor)
	if err != nil || f2.HasMore || len(f2.Items) != 1 || f2.Items[0].ID != "12347" {
		t.Fatalf("followings p2 = %+v err=%v", f2, err)
	}
	tagged, err := h.b.AccountPage(ctx, "bilibili", AccountFollowing, "-10", "")
	if err != nil || len(tagged.Items) != 1 {
		t.Fatalf("tag = %+v err=%v", tagged, err)
	}
}

func TestAccountDouyinListsMapping(t *testing.T) {
	h := newAccountHarness(t)
	h.login(t, "douyin")
	ctx := context.Background()
	f, err := h.b.AccountPage(ctx, "douyin", AccountFollowing, "", "")
	if err != nil || len(f.Items) != 1 || f.Items[0].ID != acctFollowSecUID || f.Items[0].OpenURL != "https://www.douyin.com/user/"+acctFollowSecUID || f.Items[0].UniqueID != "fan3" {
		t.Fatalf("following = %+v err=%v", f, err)
	}
	likes, err := h.b.AccountPage(ctx, "douyin", AccountLikes, "", "")
	if err != nil || len(likes.Items) != 1 || likes.HasMore {
		t.Fatalf("likes = %+v err=%v", likes, err)
	}
	w := likes.Items[0]
	if w.Kind != AccountItemWork || w.Work == nil || w.CacheKey != "douyin:video:7350000000000000111" || !strings.HasPrefix(w.OpenURL, "https://www.douyin.com/") {
		t.Fatalf("work = %+v", w)
	}
	folders, err := h.b.AccountFolders(ctx, "douyin", AccountCollection)
	if err != nil || len(folders) != 2 || folders[0].ID != AccountFolderAll || folders[1].ID != "7300000000000000127" {
		t.Fatalf("collect folders = %+v err=%v", folders, err)
	}
	all, err := h.b.AccountPage(ctx, "douyin", AccountCollection, AccountFolderAll, "")
	if err != nil || len(all.Items) != 1 || !all.HasMore || all.Cursor == "" {
		t.Fatalf("collection = %+v err=%v", all, err)
	}
	one, err := h.b.AccountPage(ctx, "douyin", AccountCollection, "7300000000000000127", "")
	if err != nil || len(one.Items) != 1 {
		t.Fatalf("folder = %+v err=%v", one, err)
	}
	wh1, err := h.b.AccountPage(ctx, "douyin", AccountWatchHistory, "", "")
	if err != nil || len(wh1.Items) != 2 || !wh1.HasMore || wh1.Cursor == "" {
		t.Fatalf("watch history p1 = %+v err=%v", wh1, err)
	}
	if wh1.Items[0].Kind != AccountItemWork || wh1.Items[0].Work == nil || wh1.Items[0].CacheKey != "douyin:video:7350000000000000444" {
		t.Fatalf("watch history work = %+v", wh1.Items[0])
	}
	if wh1.Items[1].Kind != AccountItemUnsupported || wh1.Items[1].TypeLabel != "暂不支持的作品" || wh1.Items[1].BrowserURL != "https://www.douyin.com/video/7350000000000000446" {
		t.Fatalf("watch history unsupported = %+v", wh1.Items[1])
	}
	wh2, err := h.b.AccountPage(ctx, "douyin", AccountWatchHistory, "", wh1.Cursor)
	if err != nil || len(wh2.Items) != 1 || wh2.HasMore || wh2.Cursor != "" || wh2.Items[0].ID != "7350000000000000445" {
		t.Fatalf("watch history p2 = %+v err=%v", wh2, err)
	}
	h.dyMode.Store("shell")
	if _, err := h.b.AccountPage(ctx, "douyin", AccountLikes, "", ""); !site.IsKind(err, site.ErrKindRiskControl) || !strings.Contains(err.Error(), "登录已失效或被拦截") {
		t.Fatalf("shell must not look empty: %v", err)
	}
	h2 := newAccountHarness(t)
	h2.login(t, "douyin")
	h2.dyMode.Store("shell")
	if _, err := h2.b.AccountPage(ctx, "douyin", AccountWatchHistory, "", ""); !site.IsKind(err, site.ErrKindRiskControl) || !strings.Contains(err.Error(), "登录已失效或被拦截") {
		t.Fatalf("watch history shell must not look empty: %v", err)
	}
}

func TestAccountBreakerPerSite(t *testing.T) {
	h := newAccountHarness(t)
	h.login(t, "bilibili", "douyin")
	ctx := context.Background()
	h.biliMode.Store("risk")
	if _, err := h.b.AccountPage(ctx, "bilibili", AccountFavorites, "2001", ""); !site.IsKind(err, site.ErrKindRiskControl) {
		t.Fatalf("risk err = %v", err)
	}
	if inBackoff, _ := h.b.subs.IsSiteInBackoff("bilibili"); !inBackoff {
		t.Fatal("expected bilibili breaker active after risk_control")
	}
	// DEC-089: While still in risk_control, manual probe sends 1 real request and preserves backoff without doubling BackoffLevel.
	levelBefore := h.b.subs.GetSiteSchedulerState("bilibili").BackoffLevel
	before := h.biliReqs.Load()
	_, err := h.b.AccountPage(ctx, "bilibili", AccountFavorites, "2001", "")
	if !site.IsKind(err, site.ErrKindRiskControl) || !strings.Contains(err.Error(), "探测仍处于风控中，已保持退避") {
		t.Fatalf("manual probe during risk_control = %v", err)
	}
	if h.biliReqs.Load() != before+1 {
		t.Fatalf("expected exactly 1 manual probe request, sent %d", h.biliReqs.Load()-before)
	}
	if afterLevel := h.b.subs.GetSiteSchedulerState("bilibili").BackoffLevel; afterLevel != levelBefore {
		t.Fatalf("failed manual probe escalated BackoffLevel: got %d, want %d", afterLevel, levelBefore)
	}
	if _, err := h.b.AccountPage(ctx, "douyin", AccountLikes, "", ""); err != nil {
		t.Fatalf("douyin must be unaffected by the bilibili breaker: %v", err)
	}
	// Once Bilibili recovers, manual AccountPage probe succeeds and clears Bilibili backoff immediately.
	h.biliMode.Store("")
	if _, err := h.b.AccountPage(ctx, "bilibili", AccountFavorites, "2001", ""); err != nil {
		t.Fatalf("recovered bilibili manual probe failed: %v", err)
	}
	if inBackoff, _ := h.b.subs.IsSiteInBackoff("bilibili"); inBackoff {
		t.Fatal("successful manual bilibili probe must clear bilibili backoff immediately")
	}

	h.dyMode.Store("captcha")
	if _, err := h.b.AccountPage(ctx, "douyin", AccountLikes, "", ""); !site.IsKind(err, site.ErrKindRiskControl) {
		t.Fatalf("captcha = %v", err)
	}
	dyLevelBefore := h.b.subs.GetSiteSchedulerState("douyin").BackoffLevel
	dyBefore := h.dyReqs.Load()
	if _, err := h.b.AccountPage(ctx, "douyin", AccountLikes, "", ""); !site.IsKind(err, site.ErrKindRiskControl) || h.dyReqs.Load() != dyBefore+1 {
		t.Fatalf("douyin manual probe during captcha: err=%v sent=%d", err, h.dyReqs.Load()-dyBefore)
	}
	if afterLevel := h.b.subs.GetSiteSchedulerState("douyin").BackoffLevel; afterLevel != dyLevelBefore {
		t.Fatalf("failed douyin manual probe escalated BackoffLevel: got %d, want %d", afterLevel, dyLevelBefore)
	}
}

// A 喜欢 refusal (HTTP 200, empty body) is that list's own limit: it says so
// and must not pause Douyin, so 收藏 still loads right after (2026-11-14 feedback #4).
func TestDouyinLikesRefusalLeavesTheSiteRunning(t *testing.T) {
	h := newAccountHarness(t)
	h.login(t, "douyin")
	ctx := context.Background()
	h.dyMode.Store("empty")
	_, err := h.b.AccountPage(ctx, "douyin", AccountLikes, "", "")
	if !site.IsKind(err, site.ErrKindUnavailable) || !strings.Contains(err.Error(), "浏览器") {
		t.Fatalf("likes refusal = %v", err)
	}
	if active, _ := h.b.subs.IsSiteInBackoff("douyin"); active {
		t.Fatal("a likes refusal must not pause Douyin")
	}
	if _, err := h.b.AccountPage(ctx, "douyin", AccountCollection, AccountFolderAll, ""); err != nil {
		t.Fatalf("collection after a likes refusal: %v", err)
	}
}

func TestAccountPrivacySentinels(t *testing.T) {
	h := newAccountHarness(t)
	h.login(t, "bilibili", "douyin")
	ctx := context.Background()
	historyBefore := len(h.b.ListHistory())
	for _, c := range []struct{ s, k, f string }{
		{"bilibili", AccountFollowing, AccountFolderAll}, {"bilibili", AccountFavorites, ""}, {"bilibili", AccountToView, ""}, {"bilibili", AccountHistory, ""},
		{"douyin", AccountFollowing, ""}, {"douyin", AccountLikes, ""}, {"douyin", AccountCollection, ""}, {"douyin", AccountWatchHistory, ""},
	} {
		if _, err := h.b.AccountFolders(ctx, c.s, c.k); err != nil {
			t.Fatalf("%s/%s folders: %v", c.s, c.k, err)
		}
		if _, err := h.b.AccountPage(ctx, c.s, c.k, c.f, ""); err != nil {
			t.Fatalf("%s/%s: %v", c.s, c.k, err)
		}
	}
	if n := len(h.b.ListHistory()); n != historyBefore {
		t.Fatalf("browsing wrote %d recent parses", n-historyBefore)
	}
	logs, _ := h.b.RecentLogs(2000)
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "account.bilibili.favorites page=1 items=3") {
		t.Fatalf("log lacks the list summary:\n%s", joined)
	}
	if !strings.Contains(joined, "account.douyin.watch_history page=1 items=2") {
		t.Fatalf("log lacks douyin watch_history summary:\n%s", joined)
	}
	if strings.Contains(joined, acctSentinelDYHistory) {
		t.Fatalf("watch_history sentinel leaked into recent logs")
	}
	scanForSentinels(t, h.b)

	// Opening an item is the only thing that records a recent parse.
	p, _ := h.b.AccountPage(ctx, "bilibili", AccountFavorites, "", "")
	if _, err := h.b.OpenLink(ctx, p.Items[0].OpenURL); err != nil {
		t.Fatalf("OpenLink: %v", err)
	}
	if n := len(h.b.ListHistory()); n != historyBefore+1 {
		t.Fatalf("open recorded %d entries, want 1", n-historyBefore)
	}
}

func scanForSentinels(t *testing.T, b *Backend) {
	t.Helper()
	roots := []string{b.loc.DataDir, b.loc.LogsDir, b.loc.DefaultCacheDir}
	for _, root := range roots {
		if root == "" {
			continue
		}
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			raw, rErr := os.ReadFile(path)
			if rErr != nil {
				return nil
			}
			for _, s := range []string{acctSentinelTitle, acctSentinelName, acctSentinelDY, acctSentinelDYHistory} {
				if strings.Contains(string(raw), s) {
					t.Errorf("sentinel %s leaked into %s", s, path)
				}
			}
			return nil
		})
	}
}

func TestSafeBrowserURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://www.bilibili.com/read/cv1": "https://www.bilibili.com/read/cv1",
		"//www.bilibili.com/bangumi/play/1": "https://www.bilibili.com/bangumi/play/1",
		"https://b23.tv/x":                  "https://b23.tv/x",
		"https://www.douyin.com/video/1":    "https://www.douyin.com/video/1",
		"http://www.bilibili.com/x":         "",
		"https://evilbilibili.com/x":        "",
		"https://user@www.bilibili.com/x":   "",
		"https://www.bilibili.com:8443/x":   "",
		"javascript:alert(1)":               "",
		"bilibili://video/1":                "",
	} {
		if got := safeBrowserURL(in); got != want {
			t.Errorf("safeBrowserURL(%q) = %q want %q", in, got, want)
		}
	}
}
