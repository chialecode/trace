package douyin

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/douyin/guard"
	"github.com/chialecode/trace/internal/site/douyin/sign"
)

const (
	selfSecUID       = "MS4wLjABAAAA_EXISTING_SEED_USER_01"
	fixtureSyncWebID = "7312345678901234567"
)

type capturedReq struct {
	method string
	path   string
	query  url.Values
	raw    string
	header http.Header
	body   string
}

// accountFixtureClient serves responses by path from fn and records every request.
// Cold-start profile/self warmup requests receive a valid self-profile with cookie_ttwidinfo_webid.
func accountFixtureClient(t *testing.T, fn func(r capturedReq) (int, string)) (*Client, *[]capturedReq) {
	t.Helper()
	var seen []capturedReq
	client := NewClient(ClientOptions{
		CredStore:   openTestDouyinCredStore(t, true),
		MinInterval: -1,
		Now:         func() time.Time { return time.UnixMilli(1763000000000) },
		Entropy:     newDeterministicEntropy(0x33),
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			cr := capturedReq{method: req.Method, path: req.URL.Path, query: req.URL.Query(), raw: req.URL.RawQuery, header: req.Header.Clone()}
			if req.Body != nil {
				b, _ := io.ReadAll(req.Body)
				cr.body = string(b)
			}
			seen = append(seen, cr)
			if req.URL.Path == "/aweme/v1/web/user/profile/self/" {
				return jsonHTTPResponse(200, `{"status_code":0,"user":{"uid":"900000000001","sec_uid":"`+selfSecUID+`","nickname":"已登录种子账号"}}`, map[string]string{
					"cookie_ttwidinfo_webid": fixtureSyncWebID,
				}), nil
			}
			status, body := fn(cr)
			return jsonHTTPResponse(status, body, nil), nil
		}),
	})
	return client, &seen
}

// awemeNodes builds n synthetic aweme nodes from the video fixture with ids prefix+i.
func awemeNodes(t *testing.T, prefix string, n int) string {
	t.Helper()
	var detail struct {
		AwemeDetail map[string]any `json:"aweme_detail"`
	}
	if err := json.Unmarshal([]byte(mustReadFixture(t, "aweme_detail_video.json")), &detail); err != nil {
		t.Fatal(err)
	}
	out := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		node := map[string]any{}
		for k, v := range detail.AwemeDetail {
			node[k] = v
		}
		node["aweme_id"] = prefix + string(rune('0'+i))
		out = append(out, node)
	}
	b, _ := json.Marshal(out)
	return string(b)
}

func TestPOSTSignFormBodyFeedsABogusAndGETUnchanged(t *testing.T) {
	signer := sign.NewSigner(sign.Options{Now: func() time.Time { return time.UnixMilli(1763000000000) }})
	base := sign.SignInput{
		Endpoint:    pathListCollection,
		Params:      []sign.Pair{{Key: "aid", Value: "6383"}, {Key: "msToken", Value: "SYNTH"}},
		UserAgent:   DefaultUserAgent,
		BrowserInfo: "Win32",
	}
	signWith := func(body string) string {
		in := base
		in.Body = body
		in.ContentType = formContentType
		in.Entropy = newDeterministicEntropy(0x44)
		res, err := signer.Sign(context.Background(), in)
		if err != nil {
			t.Fatal(err)
		}
		return res.SignedQuery
	}
	a, b, a2 := signWith("count=20&cursor=0"), signWith("count=20&cursor=7"), signWith("count=20&cursor=0")
	if a != a2 {
		t.Fatal("fixed clock and entropy must give the same signature")
	}
	if a == b {
		t.Fatal("changing the form body must change a_bogus")
	}

	// The real request: POST, form content type, body as signed, protected-path headers, no csrf token.
	client, seen := accountFixtureClient(t, func(r capturedReq) (int, string) {
		return 200, `{"status_code":0,"aweme_list":` + awemeNodes(t, "71000000000000001", 2) + `,"has_more":1,"cursor":20}`
	})
	page, err := client.Collection(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	r := (*seen)[0]
	if r.method != http.MethodPost || r.path != pathListCollection || r.body != "count=20&cursor=0" {
		t.Fatalf("request = %s %s body=%q", r.method, r.path, r.body)
	}
	if r.header.Get("Content-Type") != formContentType || r.header.Get("Referer") != "https://www.douyin.com/user/self?from_tab_name=main&showTab=favorite_collection" || r.header.Get("Origin") != DefaultOrigin {
		t.Fatalf("headers = %v", r.header)
	}
	if r.header.Get("x-secsdk-csrf-token") != "" {
		t.Fatal("must not send x-secsdk-csrf-token")
	}
	if r.query.Get("a_bogus") == "" || r.query.Get("version_code") != "170400" || r.query.Get("version_name") != "17.4.0" || r.query.Get("publish_video_strategy_type") != "2" {
		t.Fatalf("query = %s", r.raw)
	}
	if r.header.Get("x-secsdk-web-signature") == "" {
		t.Fatal("listcollection is a protected path and needs the gateway signature")
	}
	if page.NextCursor != "20" || !page.HasMore || len(page.Works) != 2 {
		t.Fatalf("page = %+v", page)
	}
	if _, err := client.Collection(context.Background(), "20"); err == nil {
		// stalled cursor: the fixture returns cursor 20 again
		t.Fatal("cursor that does not advance must stop")
	} else if !site.IsKind(err, site.ErrKindSchemaChanged) {
		t.Fatalf("stall err = %v", err)
	}
	if (*seen)[1].body != "count=20&cursor=20" {
		t.Fatalf("second form body = %q", (*seen)[1].body)
	}

	// A GET account request carries no body, no form content type, no Origin header, Sec-Fetch-* headers,
	// and uses the server-synced webid warmed up via profile/self on cold start.
	_, _ = client.Favorites(context.Background(), selfSecUID, "")
	if len(*seen) != 4 || (*seen)[2].path != "/aweme/v1/web/user/profile/self/" || (*seen)[3].path != pathFavorite {
		t.Fatalf("expected cold-start warmup profile/self followed by favorite, got %d requests", len(*seen))
	}
	g := (*seen)[3]
	if g.method != http.MethodGet || g.body != "" || g.header.Get("Content-Type") != "" || g.header.Get("Origin") != "" {
		t.Fatalf("favorite request = %s body=%q ct=%q origin=%q", g.method, g.body, g.header.Get("Content-Type"), g.header.Get("Origin"))
	}
	if g.header.Get("Sec-Fetch-Site") != "same-origin" || g.header.Get("Sec-Fetch-Mode") != "cors" || g.header.Get("Sec-Fetch-Dest") != "empty" {
		t.Fatalf("favorite Sec-Fetch headers = %v", g.header)
	}
	if g.query.Get("webid") != fixtureSyncWebID {
		t.Fatalf("favorite webid = %q, want synced %q", g.query.Get("webid"), fixtureSyncWebID)
	}
}

func TestFollowingsMaxTimeCursorAndStall(t *testing.T) {
	client, seen := accountFixtureClient(t, func(r capturedReq) (int, string) {
		switch r.query.Get("max_time") {
		case "":
			return 200, `{"status_code":0,"followings":[
				{"uid":"1","sec_uid":"MS4wLjABAAAA_FOLLOW_USER_000001","nickname":"甲","unique_id":"","short_id":"123","signature":"签名\n第二行","avatar_thumb":{"url_list":["https://p3-pc.douyinpic.com/a.jpeg"]}},
				{"uid":"2","sec_uid":"MS4wLjABAAAA_FOLLOW_USER_000002","nickname":"乙","unique_id":"yi"}],
				"has_more":true,"min_time":1700000000,"max_time":1700009999,"total":3}`
		case "1700000000":
			return 200, `{"status_code":0,"follow_list":[{"uid":"3","sec_uid":"MS4wLjABAAAA_FOLLOW_USER_000003","nickname":"丙"}],"has_more":false,"min_time":1690000000,"total":3}`
		default:
			return 200, `{"status_code":0,"followings":[{"uid":"3","sec_uid":"MS4wLjABAAAA_FOLLOW_USER_000003","nickname":"丙"}],"has_more":true,"min_time":1680000000}`
		}
	})
	ctx := context.Background()
	p1, err := client.Followings(ctx, selfSecUID, "")
	if err != nil {
		t.Fatal(err)
	}
	q := (*seen)[0].query
	for k, want := range map[string]string{"user_id": selfSecUID, "sec_user_id": selfSecUID, "offset": "0", "count": "20", "source_type": "1", "min_change": "0"} {
		if q.Get(k) != want {
			t.Errorf("%s = %q want %q", k, q.Get(k), want)
		}
	}
	if q.Has("max_time") || (*seen)[0].method != http.MethodGet {
		t.Fatal("first page must be a GET without max_time")
	}
	if len(p1.Users) != 2 || p1.NextMaxTime != "1700000000" || !p1.HasMore || p1.Users[0].UniqueID != "123" || p1.Users[0].SecUID != "MS4wLjABAAAA_FOLLOW_USER_000001" {
		t.Fatalf("p1 = %+v", p1)
	}
	p2, err := client.Followings(ctx, selfSecUID, p1.NextMaxTime)
	if err != nil || p2.HasMore || len(p2.Users) != 1 || p2.Users[0].Nickname != "丙" {
		t.Fatalf("p2 = %+v err=%v", p2, err)
	}
	if _, err := client.Followings(ctx, selfSecUID, "1680000000"); !site.IsKind(err, site.ErrKindSchemaChanged) {
		t.Fatalf("stalled min_time err = %v", err)
	}
	if _, err := client.Followings(ctx, "", ""); !site.IsKind(err, site.ErrKindAuthRequired) {
		t.Fatalf("missing self sec_uid err = %v", err)
	}
}

func TestFavoritesShellVersusEmpty(t *testing.T) {
	var body string
	var status = 200
	client, seen := accountFixtureClient(t, func(r capturedReq) (int, string) { return status, body })
	ctx := context.Background()

	cases := []struct {
		name, body string
		kind       string
	}{
		{"zero_bytes", ``, site.ErrKindUnavailable}, // 喜欢's refusal of non-browser requests, not the session (2026-11-14 probe)
		{"null_list_no_has_more", `{"status_code":0,"aweme_list":null}`, site.ErrKindRiskControl},
		{"bare_status", `{"status_code":0}`, site.ErrKindRiskControl},
		{"list_without_has_more", `{"status_code":0,"aweme_list":[]}`, site.ErrKindRiskControl},
		{"verify_page", `<html>验证码中间页</html>`, site.ErrKindRiskControl},
	}
	for _, tc := range cases {
		body = tc.body
		_, err := client.Favorites(ctx, selfSecUID, "")
		if !site.IsKind(err, tc.kind) {
			t.Errorf("%s: err=%v want %s", tc.name, err, tc.kind)
		}
		if tc.kind == site.ErrKindRiskControl && tc.name != "verify_page" && tc.name != "zero_bytes" && !strings.Contains(err.Error(), "登录已失效或被拦截") {
			t.Errorf("%s: message %q should say the login is lost or blocked", tc.name, err)
		}
	}
	// A real empty list has has_more.
	client2, _ := accountFixtureClient(t, func(r capturedReq) (int, string) {
		return 200, `{"status_code":0,"aweme_list":[],"has_more":0,"max_cursor":0}`
	})
	p, err := client2.Favorites(ctx, selfSecUID, "")
	if err != nil || p.HasMore || len(p.Works) != 0 {
		t.Fatalf("genuine empty = %+v err=%v", p, err)
	}
	for _, s := range []int{403, 429} {
		status, body = s, `{}`
		if _, err := client.Favorites(ctx, selfSecUID, ""); !site.IsKind(err, site.ErrKindRateLimited) {
			t.Errorf("HTTP %d err=%v", s, err)
		}
	}
	status, body = 403, `ArgusSecurityPlugin blocked`
	if _, err := client.Favorites(ctx, selfSecUID, ""); !site.IsKind(err, site.ErrKindRiskControl) {
		t.Errorf("gateway block err=%v", err)
	}
	status, body = 200, `{"status_code":2483,"status_msg":"请先登录"}`
	if _, err := client.Favorites(ctx, selfSecUID, ""); !site.IsKind(err, site.ErrKindAuthRequired) {
		t.Errorf("login lost err=%v", err)
	}
	if len(*seen) < 2 || (*seen)[0].path != "/aweme/v1/web/user/profile/self/" || (*seen)[1].path != pathFavorite {
		t.Fatalf("expected profile/self warmup followed by favorite, got %d requests", len(*seen))
	}
	q := (*seen)[1].query
	if q.Get("sec_user_id") != selfSecUID ||
		q.Get("max_cursor") != "0" ||
		q.Get("min_cursor") != "0" ||
		q.Get("count") != "18" ||
		q.Get("version_code") != "170400" ||
		q.Get("version_name") != "17.4.0" ||
		q.Get("update_version_code") != "170400" ||
		q.Get("pc_libra_divert") != "Windows" ||
		q.Get("publish_video_strategy_type") != "2" ||
		q.Get("webid") != fixtureSyncWebID ||
		q.Get("a_bogus") == "" {
		t.Fatalf("favorite query = %s", (*seen)[1].raw)
	}
}

func TestFavoritesSessionFallbackMSTokenCookieSyncAndExtendedCookies(t *testing.T) {
	store := openTestDouyinCredStore(t, false)
	if _, err := store.SaveDouyinManual(strings.Join([]string{
		"sessionid=SYNTH_SESS_NO_MS_01",
		"sid_ucp_v1=1.0.0-SYNTH_UCP_01",
		"d_ticket=SYNTH_DTICKET_01",
		"UIFIDTEMP=SYNTH_UIFIDTEMP_01",
	}, "; ")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateDouyinProfile(credential.DouyinProfile{
		UID:    "900000000099",
		SecUID: selfSecUID,
		Uname:  "无msToken种子",
	}); err != nil {
		t.Fatal(err)
	}

	var seen []capturedReq
	client := NewClient(ClientOptions{
		CredStore:   store,
		MinInterval: -1,
		Now:         func() time.Time { return time.UnixMilli(1763000000000) },
		Entropy:     newDeterministicEntropy(0x7B),
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			seen = append(seen, capturedReq{
				method: req.Method,
				path:   req.URL.Path,
				query:  req.URL.Query(),
				raw:    req.URL.RawQuery,
				header: req.Header.Clone(),
			})
			return jsonHTTPResponse(200, `{"status_code":0,"aweme_list":[],"has_more":0,"max_cursor":0}`, nil), nil
		}),
	})

	ctx := context.Background()
	if _, err := client.Favorites(ctx, selfSecUID, ""); err != nil {
		t.Fatalf("Favorites page 1 failed: %v", err)
	}
	if _, err := client.Favorites(ctx, selfSecUID, "1699"); err != nil {
		t.Fatalf("Favorites page 2 failed: %v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("seen requests = %d, want 2", len(seen))
	}

	ms1 := seen[0].query.Get("msToken")
	ms2 := seen[1].query.Get("msToken")
	if len(ms1) != 128 || strings.Contains(ms1, "+") || strings.Contains(seen[0].raw, "%2B") {
		t.Fatalf("invalid fallback msToken %q (raw=%s)", ms1, seen[0].raw)
	}
	if ms1 != ms2 {
		t.Fatalf("session fallback msToken changed across requests: %q != %q", ms1, ms2)
	}
	for i, r := range seen {
		ck := r.header.Get("Cookie")
		if !strings.Contains(ck, "msToken="+ms1) {
			t.Fatalf("req[%d] Cookie missing synchronized msToken: %s", i, ck)
		}
		if !strings.Contains(ck, "sid_ucp_v1=1.0.0-SYNTH_UCP_01") || !strings.Contains(ck, "d_ticket=SYNTH_DTICKET_01") || !strings.Contains(ck, "UIFIDTEMP=SYNTH_UIFIDTEMP_01") {
			t.Fatalf("req[%d] Cookie missing extended keys: %s", i, ck)
		}
		if r.header.Get("uifid") != "SYNTH_UIFIDTEMP_01" || r.header.Get("x-secsdk-web-signature") == "" {
			t.Fatalf("req[%d] missing stage-2 signature from UIFIDTEMP: %v", i, r.header)
		}
		// Missing s_v_web_id must NOT be forged into fake verifyFp/fp.
		if r.query.Has("verifyFp") || r.query.Has("fp") || strings.Contains(ck, "s_v_web_id=") {
			t.Fatalf("req[%d] unexpectedly forged verifyFp/s_v_web_id: query=%s cookie=%s", i, r.raw, ck)
		}
		if r.header.Get("Origin") != "" || r.header.Get("Sec-Fetch-Site") != "same-origin" {
			t.Fatalf("req[%d] unexpected Origin/Sec-Fetch-Site: %v", i, r.header)
		}
	}
	if seen[0].query.Get("min_cursor") != "0" {
		t.Fatalf("page 1 missing min_cursor=0: %s", seen[0].raw)
	}
	if seen[1].query.Has("min_cursor") {
		t.Fatalf("page 2 should not send min_cursor=0: %s", seen[1].raw)
	}

	// Verify Sec-Fetch-Site derivation across same-origin, same-site, and cross-site hosts.
	liveURL, _ := url.Parse("https://live.douyin.com/webcast/room/web/enter/")
	if got := deriveSecFetchSite(liveURL, DefaultReferer); got != "same-site" {
		t.Fatalf("live.douyin.com with www.douyin.com referer Sec-Fetch-Site = %q, want same-site", got)
	}
	if got := deriveSecFetchSite(liveURL, DefaultLiveReferer); got != "same-origin" {
		t.Fatalf("live.douyin.com with live.douyin.com referer Sec-Fetch-Site = %q, want same-origin", got)
	}
	amemvURL, _ := url.Parse("https://webcast.amemv.com/webcast/room/reflow/info/")
	if got := deriveSecFetchSite(amemvURL, DefaultLiveReferer); got != "cross-site" {
		t.Fatalf("webcast.amemv.com with live.douyin.com referer Sec-Fetch-Site = %q, want cross-site", got)
	}

	// Logout clears session fallback msToken; re-login generates a fresh session fallback.
	if err := client.LogoutCredential(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveDouyinManual("sessionid=SYNTH_SESS_NO_MS_02"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateDouyinProfile(credential.DouyinProfile{UID: "900000000100", SecUID: selfSecUID, Uname: "新会话"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Favorites(ctx, selfSecUID, ""); err != nil {
		t.Fatal(err)
	}
	ms3 := seen[2].query.Get("msToken")
	if ms3 == "" || ms3 == ms1 {
		t.Fatalf("expected rotated fallback msToken after logout/relogin, got %q (prev %q)", ms3, ms1)
	}
}

func TestFavoritesCursorFieldsAndDedup(t *testing.T) {
	client, _ := accountFixtureClient(t, func(r capturedReq) (int, string) {
		if r.query.Get("max_cursor") == "0" {
			nodes := awemeNodes(t, "7200000000000000", 3)
			// duplicate the first node inside the page
			var arr []json.RawMessage
			_ = json.Unmarshal([]byte(nodes), &arr)
			arr = append(arr, arr[0])
			b, _ := json.Marshal(arr)
			return 200, `{"status_code":0,"aweme_list":` + string(b) + `,"has_more":1,"cursor":0,"max_cursor":1699}`
		}
		return 200, `{"status_code":0,"aweme_list":` + awemeNodes(t, "7300000000000000", 1) + `,"has_more":0,"cursor":0}`
	})
	ctx := context.Background()
	p1, err := client.Favorites(ctx, selfSecUID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(p1.Works) != 3 || p1.NextCursor != "1699" || !p1.HasMore {
		t.Fatalf("p1 = works %d cursor %q more %v", len(p1.Works), p1.NextCursor, p1.HasMore)
	}
	if p1.Works[0].Kind != WorkKindVideo || p1.Works[0].Author.SecUID == "" {
		t.Fatalf("work = %+v", p1.Works[0])
	}
	p2, err := client.Favorites(ctx, selfSecUID, p1.NextCursor)
	if err != nil || p2.HasMore || len(p2.Works) != 1 {
		t.Fatalf("p2 = %+v err=%v", p2, err)
	}
}

func TestCollectFolderListAndWorks(t *testing.T) {
	client, seen := accountFixtureClient(t, func(r capturedReq) (int, string) {
		switch r.path {
		case pathCollectsList:
			return 200, `{"status_code":0,"collects_list":[
				{"collects_id":7300000000000000123,"collects_id_str":"7300000000000000127","collects_name":"做饭","total_number":12,"collects_cover":{"url_list":["https://p3-pc.douyinpic.com/c.jpeg"]}},
				{"collects_id":7,"collects_name":"旧夹","total_number":0}],"has_more":false,"cursor":2}`
		case pathCollectsVideos:
			return 200, `{"status_code":0,"aweme_list":` + awemeNodes(t, "7400000000000000", 2) + `,"has_more":false,"cursor":2}`
		}
		return 404, `{}`
	})
	ctx := context.Background()
	f, err := client.CollectFolders(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Folders) != 2 || f.Folders[0].ID != "7300000000000000127" || f.Folders[0].Count != 12 || f.Folders[0].CoverURL == "" || f.Folders[1].ID != "7" || f.HasMore {
		t.Fatalf("folders = %+v", f)
	}
	if q := (*seen)[0].query; q.Get("count") != "10" || q.Get("version_code") != "170400" {
		t.Fatalf("collects/list query = %s", (*seen)[0].raw)
	}
	w, err := client.CollectFolderWorks(ctx, f.Folders[0].ID, "")
	if err != nil || len(w.Works) != 2 {
		t.Fatalf("folder works = %+v err=%v", w, err)
	}
	if q := (*seen)[1].query; q.Get("collects_id") != "7300000000000000127" || q.Get("cursor") != "0" {
		t.Fatalf("collects/video/list query = %s", (*seen)[1].raw)
	}
	if _, err := client.CollectFolderWorks(ctx, "12&x=1", ""); !site.IsKind(err, site.ErrKindBadRequest) {
		t.Fatalf("bad folder id err = %v", err)
	}
	if len(*seen) != 2 {
		t.Fatalf("bad folder id must not send a request; sent %d", len(*seen))
	}
}

func TestAccountListsRequireLogin(t *testing.T) {
	client := NewClient(ClientOptions{
		CredStore:   openTestDouyinCredStore(t, false),
		MinInterval: -1,
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			t.Fatalf("logged-out request sent: %s", req.URL.Path)
			return nil, nil
		}),
	})
	ctx := context.Background()
	if _, err := client.Collection(ctx, ""); !site.IsKind(err, site.ErrKindAuthRequired) {
		t.Fatalf("collection err = %v", err)
	}
	if _, err := client.CollectFolders(ctx, ""); !site.IsKind(err, site.ErrKindAuthRequired) {
		t.Fatalf("collects err = %v", err)
	}
	if _, err := client.WatchHistory(ctx, ""); !site.IsKind(err, site.ErrKindAuthRequired) {
		t.Fatalf("watch history err = %v", err)
	}
	if !WatchHistoryEnabled {
		t.Fatal("观看历史 should be enabled after ACT-026 D-V5 confirmed the endpoint")
	}
}

func TestWatchHistoryPaginationAwemeDateEmptyAndErrors(t *testing.T) {
	client, seen := accountFixtureClient(t, func(r capturedReq) (int, string) {
		if r.path != pathHistoryRead {
			return 404, `{}`
		}
		switch r.query.Get("max_cursor") {
		case "0":
			nodes := awemeNodes(t, "7500000000000000", 2)
			var arr []json.RawMessage
			_ = json.Unmarshal([]byte(nodes), &arr)
			// Duplicate within page and an unsupported live-card item to verify dedup & non-fatal mapping.
			arr = append(arr, arr[0], json.RawMessage(`{"aweme_id":"75000000000000009","aweme_type":101,"desc":"直播回放占位"}`))
			b, _ := json.Marshal(arr)
			return 200, `{"status_code":0,"status_msg":"","aweme_date":{"75000000000000000":1732400200,"75000000000000001":1732400150},"aweme_list":` + string(b) + `,"has_more":1,"max_cursor":1732400100}`
		case "1732400100":
			return 200, `{"status_code":0,"status_msg":"","aweme_date":{"75000000000000010":1732399900},"aweme_list":` + awemeNodes(t, "7500000000000001", 1) + `,"has_more":0,"max_cursor":0}`
		default:
			return 200, `{"status_code":0,"status_msg":"","aweme_date":{},"aweme_list":` + awemeNodes(t, "7500000000000002", 1) + `,"has_more":1,"max_cursor":1732400999}`
		}
	})
	ctx := context.Background()
	p1, err := client.WatchHistory(ctx, "")
	if err != nil {
		t.Fatalf("WatchHistory page 1 failed: %v", err)
	}
	r0 := (*seen)[0]
	if r0.method != http.MethodGet || r0.path != pathHistoryRead || r0.body != "" {
		t.Fatalf("page 1 request = %s %s body=%q", r0.method, r0.path, r0.body)
	}
	if r0.query.Get("max_cursor") != "0" || r0.query.Get("count") != "20" || r0.query.Get("a_bogus") == "" {
		t.Fatalf("page 1 query = %s", r0.raw)
	}
	if r0.header.Get("Referer") != "https://www.douyin.com/user/self?showTab=record" || r0.header.Get("Origin") != "" {
		t.Fatalf("page 1 headers = %v", r0.header)
	}
	if len(p1.Works) != 3 || !p1.HasMore || p1.NextCursor != "1732400100" {
		t.Fatalf("p1 = works %d more=%v cursor=%q", len(p1.Works), p1.HasMore, p1.NextCursor)
	}
	if p1.Works[2].Kind != WorkKindUnsupported {
		t.Fatalf("unsupported item kind = %q", p1.Works[2].Kind)
	}

	p2, err := client.WatchHistory(ctx, p1.NextCursor)
	if err != nil {
		t.Fatalf("WatchHistory page 2 failed: %v", err)
	}
	if p2.HasMore || len(p2.Works) != 1 || p2.Works[0].ID != "75000000000000010" {
		t.Fatalf("p2 = %+v", p2)
	}
	if (*seen)[1].query.Get("max_cursor") != "1732400100" {
		t.Fatalf("page 2 cursor = %q", (*seen)[1].query.Get("max_cursor"))
	}

	// Stalled cursor must fail with schema_changed instead of looping forever.
	if _, err := client.WatchHistory(ctx, "1732400999"); !site.IsKind(err, site.ErrKindSchemaChanged) {
		t.Fatalf("stalled cursor err = %v", err)
	}

	// Genuine empty list vs shell/error responses.
	var status = 200
	var body string
	errClient, _ := accountFixtureClient(t, func(r capturedReq) (int, string) { return status, body })
	body = `{"status_code":0,"status_msg":"","aweme_date":{},"aweme_list":[],"has_more":0,"max_cursor":0}`
	empty, err := errClient.WatchHistory(ctx, "")
	if err != nil || empty.HasMore || len(empty.Works) != 0 {
		t.Fatalf("genuine empty = %+v err=%v", empty, err)
	}
	for _, tc := range []struct {
		name   string
		status int
		body   string
		kind   string
	}{
		{"zero_bytes", 200, ``, site.ErrKindRiskControl},
		{"null_list", 200, `{"status_code":0,"aweme_list":null,"has_more":0}`, site.ErrKindRiskControl},
		{"missing_has_more", 200, `{"status_code":0,"aweme_list":[]}`, site.ErrKindRiskControl},
		{"bare_status", 200, `{"status_code":0}`, site.ErrKindRiskControl},
		{"rate_limited", 429, `{}`, site.ErrKindRateLimited},
		{"auth_lost", 200, `{"status_code":2483,"status_msg":"请先登录"}`, site.ErrKindAuthRequired},
	} {
		status, body = tc.status, tc.body
		if _, err := errClient.WatchHistory(ctx, ""); !site.IsKind(err, tc.kind) {
			t.Errorf("%s: err=%v want %s", tc.name, err, tc.kind)
		}
	}
}

func TestTicketGuardHandshakeSigningAndLogoutRotation(t *testing.T) {
	store := openTestDouyinCredStore(t, false)
	const fixedTS = int64(1763000000)
	const srvTicket = "hash.SYNTH_ISSUED_TICKET_074"
	const srvTsSign = "ts.2.SYNTH_ISSUED_TSSIGN_074"

	serverDataB64 := base64.StdEncoding.EncodeToString([]byte(`{"ticket":"` + srvTicket + `","ts_sign":"` + srvTsSign + `"}`))
	issueServerData := true

	var seen []capturedReq
	client := NewClient(ClientOptions{
		CredStore:   store,
		MinInterval: -1,
		Now:         func() time.Time { return time.Unix(fixedTS, 0).UTC() },
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			seen = append(seen, capturedReq{
				method: req.Method,
				path:   req.URL.Path,
				query:  req.URL.Query(),
				raw:    req.URL.RawQuery,
				header: req.Header.Clone(),
			})
			switch req.URL.Path {
			case "/aweme/v1/web/user/profile/self/":
				h := map[string]string{
					"bd-ticket-guard-result": "1101",
				}
				if issueServerData {
					h["bd-ticket-guard-result"] = "0"
					h["bd-ticket-guard-server-data"] = serverDataB64
				}
				return jsonHTTPResponse(200, `{"status_code":0,"user":{"uid":"770000000074","sec_uid":"`+selfSecUID+`","nickname":"卫士测试用户"}}`, h), nil
			case pathFavorite:
				h := map[string]string{
					"bd-ticket-guard-result": "0",
				}
				if !issueServerData {
					h["bd-ticket-guard-result"] = "1101"
				}
				return jsonHTTPResponse(200, `{"status_code":0,"aweme_list":[],"has_more":0,"max_cursor":0}`, h), nil
			default:
				return jsonHTTPResponse(404, `{}`, nil), nil
			}
		}),
	})

	assertNoGuardHeaders := func(label string, hdr http.Header) {
		t.Helper()
		for _, k := range []string{
			guard.HeaderVersion,
			guard.HeaderIterationVersion,
			guard.HeaderWebVersion,
			guard.HeaderWebSignType,
			guard.HeaderREEPublicKey,
			guard.HeaderClientData,
		} {
			if got := hdr.Get(k); got != "" {
				t.Fatalf("%s unexpectedly sent unregistered guard header %s=%q", label, k, got)
			}
		}
	}

	ctx := context.Background()
	st, err := client.LoginWithCookie(ctx, "sessionid=SYNTH_GUARD_SESS_01; UIFID=SYNTH_GUARD_UIFID_01; bd_ticket_guard_client_data=MUST_STRIP_BROWSER_GUARD")
	if err != nil || !st.LoggedIn {
		t.Fatalf("LoginWithCookie failed: st=%+v err=%v", st, err)
	}
	if len(seen) != 1 {
		t.Fatalf("expected 1 login request, got %d", len(seen))
	}
	loginHdr := seen[0].header
	if strings.Contains(loginHdr.Get("Cookie"), "bd_ticket_guard") {
		t.Fatalf("browser bd_ticket_guard cookie leaked into outbound Cookie: %s", loginHdr.Get("Cookie"))
	}
	// Without a server-issued ts_sign, read-only login must NOT attach unregistered handshake headers.
	assertNoGuardHeaders("initial profile/self without ts_sign", loginHdr)

	// Verify server-issued ticket/ts_sign were still adopted and persisted encrypted in store.
	persisted := store.DouyinGuard()
	if persisted == nil || persisted.PublicKeySEC1 == "" || persisted.Ticket != srvTicket || persisted.TsSign != srvTsSign {
		t.Fatalf("persisted DouyinGuard mismatch: %+v", persisted)
	}
	pub1 := persisted.PublicKeySEC1

	// Favorites request now has valid ts_sign + ticket, so it attaches full signed guard headers.
	if _, err := client.Favorites(ctx, selfSecUID, ""); err != nil {
		t.Fatalf("Favorites failed: %v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(seen))
	}
	favHdr := seen[1].header
	if favHdr.Get(guard.HeaderVersion) != "2" ||
		favHdr.Get(guard.HeaderIterationVersion) != "1" ||
		favHdr.Get(guard.HeaderWebVersion) != "2" ||
		favHdr.Get(guard.HeaderREEPublicKey) != pub1 ||
		favHdr.Get(guard.HeaderWebSignType) != "1" {
		t.Fatalf("unexpected favorite guard headers: %v", favHdr)
	}
	rawCD, err := base64.StdEncoding.DecodeString(favHdr.Get(guard.HeaderClientData))
	if err != nil {
		t.Fatalf("decode favorite bd-ticket-guard-client-data: %v", err)
	}
	var payload guard.ClientDataPayload
	if err := json.Unmarshal(rawCD, &payload); err != nil {
		t.Fatalf("unmarshal favorite client-data: %v", err)
	}
	if payload.TsSign != srvTsSign || payload.ReqContent != guard.DefaultReqContent || payload.Timestamp != fixedTS {
		t.Fatalf("unexpected client-data payload: %+v", payload)
	}
	pubKey, err := guard.DecodePublicKeySEC1(pub1)
	if err != nil {
		t.Fatalf("DecodePublicKeySEC1 failed: %v", err)
	}
	wantPreimage := guard.BuildPreimage(srvTicket, pathFavorite, fixedTS)
	if !guard.VerifyPreimageASN1(pubKey, wantPreimage, payload.ReqSign) {
		t.Fatal("favorite req_sign failed ECDSA P-256 ASN.1 verification against bd-ticket-guard-ree-public-key")
	}

	// Logout clears guard state; subsequent login without server-data rotates to a fresh P-256
	// keypair and must NOT send unregistered bd-ticket-guard-* headers on profile/self or favorites.
	if err := client.LogoutCredential(); err != nil {
		t.Fatal(err)
	}
	if store.DouyinGuard() != nil {
		t.Fatal("LogoutCredential did not clear DouyinGuard")
	}
	issueServerData = false
	if _, err := client.LoginWithCookie(ctx, "sessionid=SYNTH_GUARD_SESS_02; d_ticket=SYNTH_DTICKET_ONLY"); err != nil {
		t.Fatal(err)
	}
	assertNoGuardHeaders("second profile/self without ts_sign", seen[2].header)
	persisted2 := store.DouyinGuard()
	if persisted2 == nil || persisted2.PublicKeySEC1 == "" || persisted2.PublicKeySEC1 == pub1 || persisted2.TsSign != "" {
		t.Fatalf("expected rotated P-256 keypair without ts_sign after logout, got %+v (prev pub=%q)", persisted2, pub1)
	}
	if _, err := client.Favorites(ctx, selfSecUID, ""); err != nil {
		t.Fatalf("Favorites without ts_sign failed: %v", err)
	}
	assertNoGuardHeaders("favorites without ts_sign", seen[3].header)
}

func TestSyncedWebIDFromLoginWarmupTTWIDBindingAndOrder(t *testing.T) {
	ctx := context.Background()

	t.Run("isValidServerWebID_boundaries", func(t *testing.T) {
		valid := []string{
			"1000000000",               // 10 digits
			"7312345678901234567",      // 19 digits
			"999999999999999999999999", // 24 digits
		}
		for _, v := range valid {
			if !isValidServerWebID(v) {
				t.Errorf("isValidServerWebID(%q) = false, want true", v)
			}
		}
		invalid := []string{
			"",
			"123456789",                 // 9 digits
			"1000000000000000000000000", // 25 digits
			"0000000000000000000",       // all zero
			"0123456789012345678",       // leading zero
			"731234567890123456a",       // non-digit
			"7312345678 1234567",        // embedded space
			"-731234567890123456",       // negative
			"٧٣١٢٣٤٥٦٧٨٩٠١٢٣٤٥٦٧",       // non-ASCII digits
		}
		for _, v := range invalid {
			if isValidServerWebID(v) {
				t.Errorf("isValidServerWebID(%q) = true, want false", v)
			}
		}
	})

	t.Run("login_syncs_webid_preserves_param_order_and_stage2_signature", func(t *testing.T) {
		store := openTestDouyinCredStore(t, false)
		const (
			serverWebID = "7398765432109876543"
			uifidVal    = "SYNTH_UIFID_SYNC_01"
			fixedMS     = int64(1763000000000)
		)
		var seen []capturedReq
		client := NewClient(ClientOptions{
			CredStore:   store,
			WebID:       "7300000000000000001",
			MinInterval: -1,
			Now:         func() time.Time { return time.UnixMilli(fixedMS) },
			Entropy:     newDeterministicEntropy(0x61),
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				cr := capturedReq{
					method: req.Method,
					path:   req.URL.Path,
					query:  req.URL.Query(),
					raw:    req.URL.RawQuery,
					header: req.Header.Clone(),
				}
				seen = append(seen, cr)
				switch req.URL.Path {
				case "/aweme/v1/web/user/profile/self/":
					return jsonHTTPResponse(200, `{"status_code":0,"user":{"uid":"880011","sec_uid":"`+selfSecUID+`","nickname":"同步测试号"}}`, map[string]string{
						"cookie_ttwidinfo_webid": serverWebID,
					}), nil
				case pathFavorite:
					return jsonHTTPResponse(200, `{"status_code":0,"aweme_list":[],"has_more":0,"max_cursor":0}`, nil), nil
				default:
					return jsonHTTPResponse(404, `{}`, nil), nil
				}
			}),
		})

		_, err := client.LoginWithCookie(ctx, "sessionid=SYNTH_SESS_SYNC_01; ttwid=SYNTH_TTWID_SYNC_01; UIFID="+uifidVal+"; s_v_web_id=verify_synth_sync_01")
		if err != nil {
			t.Fatalf("LoginWithCookie failed: %v", err)
		}
		if _, err := client.Favorites(ctx, selfSecUID, ""); err != nil {
			t.Fatalf("Favorites page 1 failed: %v", err)
		}
		if _, err := client.Favorites(ctx, selfSecUID, "1699"); err != nil {
			t.Fatalf("Favorites page 2 failed: %v", err)
		}
		// LoginWithCookie already synced webid -> 0 extra profile/self calls on Favorites page 1 & 2.
		if len(seen) != 3 {
			t.Fatalf("total requests = %d, want 3 (1 login + 2 favorites)", len(seen))
		}
		for _, idx := range []int{1, 2} {
			r := seen[idx]
			if r.path != pathFavorite {
				t.Fatalf("req[%d] path = %q, want %q", idx, r.path, pathFavorite)
			}
			if got := r.query.Get("webid"); got != serverWebID {
				t.Fatalf("req[%d] webid = %q, want %q", idx, got, serverWebID)
			}
			// Parameter order must remain in-place: round_trip_time -> webid -> sec_user_id.
			wantSlice := "round_trip_time=50&webid=" + serverWebID + "&sec_user_id="
			if !strings.Contains(r.raw, wantSlice) {
				t.Fatalf("req[%d] raw query lost in-place webid order: %s", idx, r.raw)
			}
			// Verify Stage-2 x-secsdk-web-signature matches exact RawQuery containing the synced webid.
			stage2Query, sigHex, ok := strings.Cut(r.raw, "&x-secsdk-web-signature=")
			if !ok || sigHex == "" {
				t.Fatalf("req[%d] missing stage-2 signature in query: %s", idx, r.raw)
			}
			stamp := r.header.Get("x-secsdk-web-expire")
			sum := md5.Sum([]byte(uifidVal + "_" + stamp + "_A96D855A08C0A9707F8BEF0D9A527E4E_" + stage2Query))
			wantSig := hex.EncodeToString(sum[:])
			if sigHex != wantSig || r.header.Get("x-secsdk-web-signature") != wantSig {
				t.Fatalf("req[%d] stage-2 sig mismatch: query=%q header=%q want=%q", idx, sigHex, r.header.Get("x-secsdk-web-signature"), wantSig)
			}
		}
	})

	t.Run("cold_start_concurrent_warmup_dedup_and_page2_no_rewarm", func(t *testing.T) {
		store := openTestDouyinCredStore(t, true)
		const coldWebID = "7355555555555555555"

		var profileHits atomic.Int32
		var favHits atomic.Int32
		var favWebIDsMu sync.Mutex
		var favWebIDs []string

		warmupStarted := make(chan struct{})
		releaseWarmup := make(chan struct{})
		var closeOnce sync.Once
		releaseAll := func() { closeOnce.Do(func() { close(releaseWarmup) }) }
		t.Cleanup(releaseAll)

		client := NewClient(ClientOptions{
			CredStore:   store,
			WebID:       "7300000000000000002",
			MinInterval: -1,
			Entropy:     newDeterministicEntropy(0x62),
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Path {
				case "/aweme/v1/web/user/profile/self/":
					if profileHits.Add(1) == 1 {
						close(warmupStarted)
					}
					select {
					case <-req.Context().Done():
						return nil, req.Context().Err()
					case <-releaseWarmup:
					}
					return jsonHTTPResponse(200, `{"status_code":0,"user":{"uid":"900000000001","sec_uid":"`+selfSecUID+`","nickname":"冷启动用户"}}`, map[string]string{
						"cookie_ttwidinfo_webid": coldWebID,
					}), nil
				case pathFavorite:
					favHits.Add(1)
					favWebIDsMu.Lock()
					favWebIDs = append(favWebIDs, req.URL.Query().Get("webid"))
					favWebIDsMu.Unlock()
					return jsonHTTPResponse(200, `{"status_code":0,"aweme_list":[],"has_more":0,"max_cursor":0}`, nil), nil
				default:
					return jsonHTTPResponse(404, `{}`, nil), nil
				}
			}),
		})

		errs := make(chan error, 3)
		go func() {
			_, err := client.Favorites(ctx, selfSecUID, "")
			errs <- err
		}()
		<-warmupStarted
		for i := 0; i < 2; i++ {
			go func() {
				_, err := client.Favorites(ctx, selfSecUID, "")
				errs <- err
			}()
		}
		time.Sleep(25 * time.Millisecond)
		releaseAll()

		for i := 0; i < 3; i++ {
			if err := <-errs; err != nil {
				t.Fatalf("concurrent Favorites[%d] failed: %v", i, err)
			}
		}
		if _, err := client.Favorites(ctx, selfSecUID, "1699"); err != nil {
			t.Fatalf("Favorites page 2 failed: %v", err)
		}
		if got := profileHits.Load(); got != 1 {
			t.Fatalf("profile/self warmup hits = %d, want exactly 1", got)
		}
		if got := favHits.Load(); got != 4 {
			t.Fatalf("favorite hits = %d, want 4", got)
		}
		for i, wid := range favWebIDs {
			if wid != coldWebID {
				t.Fatalf("favorite[%d] webid = %q, want %q", i, wid, coldWebID)
			}
		}
	})

	t.Run("warmup_without_header_marks_tried_and_falls_back_to_default_webid", func(t *testing.T) {
		store := openTestDouyinCredStore(t, true)
		const defaultWID = "7300000000000000099"
		var profileHits atomic.Int32
		var favWebIDs []string

		client := NewClient(ClientOptions{
			CredStore:   store,
			WebID:       defaultWID,
			MinInterval: -1,
			Entropy:     newDeterministicEntropy(0x63),
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Path {
				case "/aweme/v1/web/user/profile/self/":
					profileHits.Add(1)
					return jsonHTTPResponse(200, `{"status_code":0,"user":{"uid":"900000000001","sec_uid":"`+selfSecUID+`","nickname":"无头用户"}}`, map[string]string{
						"cookie_ttwidinfo_webid": "invalid_not_digits",
					}), nil
				case pathFavorite:
					favWebIDs = append(favWebIDs, req.URL.Query().Get("webid"))
					return jsonHTTPResponse(200, `{"status_code":0,"aweme_list":[],"has_more":0,"max_cursor":0}`, nil), nil
				default:
					return jsonHTTPResponse(404, `{}`, nil), nil
				}
			}),
		})

		if _, err := client.Favorites(ctx, selfSecUID, ""); err != nil {
			t.Fatalf("Favorites 1 failed: %v", err)
		}
		if _, err := client.Favorites(ctx, selfSecUID, "1699"); err != nil {
			t.Fatalf("Favorites 2 failed: %v", err)
		}
		if got := profileHits.Load(); got != 1 {
			t.Fatalf("profile/self hits = %d, want 1 (must not loop when header absent)", got)
		}
		if len(favWebIDs) != 2 || favWebIDs[0] != defaultWID || favWebIDs[1] != defaultWID {
			t.Fatalf("favWebIDs = %v, want [%s %s]", favWebIDs, defaultWID, defaultWID)
		}
	})

	t.Run("warmup_auth_risk_rate_limit_and_cancel_abort_before_favorites", func(t *testing.T) {
		cases := []struct {
			name     string
			status   int
			body     string
			cancel   bool
			wantKind string
		}{
			{"auth_required", 200, `{"status_code":2483,"status_msg":"未登录"}`, false, site.ErrKindAuthRequired},
			{"risk_control", 200, `<html>验证码中间页</html>`, false, site.ErrKindRiskControl},
			{"rate_limited", 429, `{}`, false, site.ErrKindRateLimited},
			{"ctx_canceled", 200, `{}`, true, site.ErrKindNetwork},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				store := openTestDouyinCredStore(t, true)
				var favHits atomic.Int32
				client := NewClient(ClientOptions{
					CredStore:   store,
					MinInterval: -1,
					Entropy:     newDeterministicEntropy(0x64),
					Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
						if req.URL.Path == pathFavorite {
							favHits.Add(1)
						}
						return jsonHTTPResponse(tc.status, tc.body, nil), nil
					}),
				})
				callCtx := ctx
				if tc.cancel {
					var cancel context.CancelFunc
					callCtx, cancel = context.WithCancel(ctx)
					cancel()
				}
				_, err := client.Favorites(callCtx, selfSecUID, "")
				if !site.IsKind(err, tc.wantKind) {
					t.Fatalf("Favorites(%s) err = %v, want kind %s", tc.name, err, tc.wantKind)
				}
				if got := favHits.Load(); got != 0 {
					t.Fatalf("Favorites(%s) hit aweme/favorite %d times after warmup failure, want 0", tc.name, got)
				}
			})
		}
	})

	t.Run("logout_ttwid_rotation_and_failed_candidate_isolation", func(t *testing.T) {
		store := openTestDouyinCredStore(t, false)
		const (
			defaultWID = "7300000000000000077"
			webIDA     = "7311111111111111111"
			webIDB     = "7322222222222222222"
			webIDBad   = "7399999999999999999"
		)
		var profileMode atomic.Int32 // 0=userA(webIDA), 1=badCandidate(user:null, webIDBad), 2=userB(webIDB)
		var lastFavWebID, lastFollowWebID string

		client := NewClient(ClientOptions{
			CredStore:   store,
			WebID:       defaultWID,
			MinInterval: -1,
			Entropy:     newDeterministicEntropy(0x65),
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Path {
				case "/aweme/v1/web/user/profile/self/":
					switch profileMode.Load() {
					case 0:
						return jsonHTTPResponse(200, `{"status_code":0,"user":{"uid":"1001","sec_uid":"`+selfSecUID+`","nickname":"用户A"}}`, map[string]string{
							"cookie_ttwidinfo_webid": webIDA,
						}), nil
					case 1:
						return jsonHTTPResponse(200, `{"status_code":0,"user":null}`, map[string]string{
							"cookie_ttwidinfo_webid": webIDBad,
						}), nil
					default:
						return jsonHTTPResponse(200, `{"status_code":0,"user":{"uid":"1002","sec_uid":"`+selfSecUID+`","nickname":"用户B"}}`, map[string]string{
							"cookie_ttwidinfo_webid": webIDB,
						}), nil
					}
				case pathFavorite:
					lastFavWebID = req.URL.Query().Get("webid")
					return jsonHTTPResponse(200, `{"status_code":0,"aweme_list":[],"has_more":0,"max_cursor":0}`, nil), nil
				case pathFollowingList:
					lastFollowWebID = req.URL.Query().Get("webid")
					return jsonHTTPResponse(200, `{"status_code":0,"followings":[],"has_more":false,"min_time":0}`, nil), nil
				default:
					return jsonHTTPResponse(404, `{}`, nil), nil
				}
			}),
		})

		// 1. Login user A -> syncs webIDA.
		profileMode.Store(0)
		if _, err := client.LoginWithCookie(ctx, "sessionid=SYNTH_SESS_A; ttwid=SYNTH_TTWID_A"); err != nil {
			t.Fatal(err)
		}
		// 2. Failed candidate LoginWithCookie MUST NOT clobber user A's synced webIDA.
		profileMode.Store(1)
		if _, err := client.LoginWithCookie(ctx, "sessionid=SYNTH_SESS_BAD; ttwid=SYNTH_TTWID_BAD"); !site.IsKind(err, site.ErrKindAuthRequired) {
			t.Fatalf("bad candidate err = %v, want auth_required", err)
		}
		if _, err := client.Favorites(ctx, selfSecUID, ""); err != nil {
			t.Fatal(err)
		}
		if lastFavWebID != webIDA {
			t.Fatalf("user A favorite webid = %q after failed candidate login, want %q", lastFavWebID, webIDA)
		}

		// 3. ttwid rotation inside store invalidates user A's synced webIDA immediately.
		if err := store.MergeContextCookies(map[string]string{"ttwid": "SYNTH_TTWID_ROTATED"}); err != nil {
			t.Fatal(err)
		}
		if _, err := client.Followings(ctx, selfSecUID, ""); err != nil {
			t.Fatal(err)
		}
		if lastFollowWebID != defaultWID {
			t.Fatalf("after ttwid rotation, Followings leaked old webid %q, want default %q", lastFollowWebID, defaultWID)
		}

		// 4. Logout clears syncedWebID; re-seeding user B warms up and syncs webIDB.
		if err := client.LogoutCredential(); err != nil {
			t.Fatal(err)
		}
		if _, err := store.SaveVerifiedDouyinManual(map[string]string{
			"sessionid": "SYNTH_SESS_B",
			"ttwid":     "SYNTH_TTWID_B",
		}, credential.DouyinProfile{UID: "1002", SecUID: selfSecUID, Uname: "用户B"}); err != nil {
			t.Fatal(err)
		}
		profileMode.Store(2)
		if _, err := client.Favorites(ctx, selfSecUID, ""); err != nil {
			t.Fatal(err)
		}
		if lastFavWebID != webIDB {
			t.Fatalf("user B favorite webid = %q, want %q", lastFavWebID, webIDB)
		}
	})

	t.Run("stale_queued_request_cannot_overwrite_new_account_or_logged_out_webid", func(t *testing.T) {
		const (
			defaultWID = "7300000000000000088"
			webIDA     = "7311111111111111118"
			webIDB     = "7322222222222222228"
			webIDC     = "7333333333333333338"
		)

		// Sub-scenario 1: Account A request resolves cookie & queues behind LoginWithCookie(B) on serialGate.
		store := openTestDouyinCredStore(t, false)
		if _, err := store.SaveVerifiedDouyinManual(map[string]string{
			"sessionid": "SYNTH_SESS_A",
			"ttwid":     "SYNTH_TTWID_A",
		}, credential.DouyinProfile{UID: "1001", SecUID: selfSecUID, Uname: "用户A"}); err != nil {
			t.Fatal(err)
		}

		loginBInTransport := make(chan struct{})
		releaseLoginB := make(chan struct{})
		loginBDone := make(chan struct{})
		reqAPastResolve := make(chan struct{})
		var reqAPastOnce sync.Once
		var profileHits atomic.Int32
		var favWebIDB string

		client := NewClient(ClientOptions{
			CredStore:   store,
			WebID:       defaultWID,
			MinInterval: -1,
			Entropy:     newDeterministicEntropy(0x66),
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Path {
				case "/aweme/v1/web/user/profile/self/":
					if profileHits.Add(1) == 1 {
						close(loginBInTransport)
						<-releaseLoginB
					}
					return jsonHTTPResponse(200, `{"status_code":0,"user":{"uid":"1002","sec_uid":"`+selfSecUID+`","nickname":"用户B"}}`, map[string]string{
						"cookie_ttwidinfo_webid": webIDB,
					}), nil
				case pathFollowingList:
					// Hold Account A's response until LoginWithCookie(B) has persisted and applied webIDB.
					<-loginBDone
					if !strings.Contains(req.Header.Get("Cookie"), "SYNTH_TTWID_A") {
						t.Errorf("stale request cookie = %q, want SYNTH_TTWID_A", req.Header.Get("Cookie"))
					}
					return jsonHTTPResponse(200, `{"status_code":0,"followings":[],"has_more":false,"min_time":0}`, map[string]string{
						"cookie_ttwidinfo_webid": webIDA,
					}), nil
				case pathFavorite:
					favWebIDB = req.URL.Query().Get("webid")
					return jsonHTTPResponse(200, `{"status_code":0,"aweme_list":[],"has_more":0,"max_cursor":0}`, nil), nil
				default:
					return jsonHTTPResponse(404, `{}`, nil), nil
				}
			}),
		})
		client.SetAutomaticRequestHandlers(func(context.Context, string, string) error {
			reqAPastOnce.Do(func() { close(reqAPastResolve) })
			return nil
		}, nil)

		loginBErrCh := make(chan error, 1)
		go func() {
			_, err := client.LoginWithCookie(ctx, "sessionid=SYNTH_SESS_B; ttwid=SYNTH_TTWID_B")
			close(loginBDone)
			loginBErrCh <- err
		}()
		<-loginBInTransport

		reqAErrCh := make(chan error, 1)
		go func() {
			_, err := client.Followings(site.WithAutomaticRequests(ctx, "test-stale-a"), selfSecUID, "")
			reqAErrCh <- err
		}()
		<-reqAPastResolve
		// Release LoginWithCookie(B); Request A already resolved Account A's Cookie before B saved.
		close(releaseLoginB)

		if err := <-loginBErrCh; err != nil {
			t.Fatalf("LoginWithCookie(B) failed: %v", err)
		}
		if err := <-reqAErrCh; err != nil {
			t.Fatalf("in-flight Account A Followings failed: %v", err)
		}
		if got := client.syncedWebIDFor(hashTTWID("SYNTH_TTWID_B")); got != webIDB {
			t.Fatalf("Account B syncedWebID = %q after stale Account A response, want %q", got, webIDB)
		}
		if got := client.syncedWebIDFor(hashTTWID("SYNTH_TTWID_A")); got != "" {
			t.Fatalf("stale Account A syncedWebID = %q leaked into client, want empty", got)
		}
		if _, err := client.Favorites(ctx, selfSecUID, ""); err != nil {
			t.Fatalf("Account B Favorites failed: %v", err)
		}
		if favWebIDB != webIDB {
			t.Fatalf("Account B Favorites webid = %q, want %q", favWebIDB, webIDB)
		}
		if got := profileHits.Load(); got != 1 {
			t.Fatalf("Account B triggered extra warmup profile/self: hits = %d, want 1", got)
		}

		// Sub-scenario 2: Logout while Account B request is in-flight must not resurrect syncedWebID.
		logoutReqInFlight := make(chan struct{})
		releaseLogoutReq := make(chan struct{})
		logoutClient := NewClient(ClientOptions{
			CredStore:   store,
			WebID:       defaultWID,
			MinInterval: -1,
			Entropy:     newDeterministicEntropy(0x67),
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				close(logoutReqInFlight)
				<-releaseLogoutReq
				return jsonHTTPResponse(200, `{"status_code":0,"followings":[],"has_more":false,"min_time":0}`, map[string]string{
					"cookie_ttwidinfo_webid": webIDB,
				}), nil
			}),
		})
		logoutReqDone := make(chan error, 1)
		go func() {
			_, err := logoutClient.Followings(ctx, selfSecUID, "")
			logoutReqDone <- err
		}()
		<-logoutReqInFlight
		if err := logoutClient.LogoutCredential(); err != nil {
			t.Fatalf("LogoutCredential failed: %v", err)
		}
		close(releaseLogoutReq)
		if err := <-logoutReqDone; err != nil {
			t.Fatalf("in-flight Followings before logout failed: %v", err)
		}
		logoutClient.webIDMu.Lock()
		gotSynced, gotHash, gotWarmup := logoutClient.syncedWebID, logoutClient.syncedTTWIDHash, logoutClient.webIDWarmupHash
		logoutClient.webIDMu.Unlock()
		if gotSynced != "" || gotHash != "" || gotWarmup != "" {
			t.Fatalf("logged-out client retained stale webID state: synced=%q hash=%q warmup=%q", gotSynced, gotHash, gotWarmup)
		}

		// Sub-scenario 3: markAuthExpired while in-flight must linearize cred expiry before webIDGen++.
		if _, err := store.SaveVerifiedDouyinManual(map[string]string{
			"sessionid": "SYNTH_SESS_C",
			"ttwid":     "SYNTH_TTWID_C",
		}, credential.DouyinProfile{UID: "1003", SecUID: selfSecUID, Uname: "用户C"}); err != nil {
			t.Fatal(err)
		}
		expReqInFlight := make(chan struct{})
		releaseExpReq := make(chan struct{})
		expClient := NewClient(ClientOptions{
			CredStore:   store,
			WebID:       defaultWID,
			MinInterval: -1,
			Entropy:     newDeterministicEntropy(0x68),
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				close(expReqInFlight)
				<-releaseExpReq
				return jsonHTTPResponse(200, `{"status_code":0,"followings":[],"has_more":false,"min_time":0}`, map[string]string{
					"cookie_ttwidinfo_webid": webIDC,
				}), nil
			}),
		})
		expReqDone := make(chan error, 1)
		go func() {
			_, err := expClient.Followings(ctx, selfSecUID, "")
			expReqDone <- err
		}()
		<-expReqInFlight
		expClient.markAuthExpired("expired")
		close(releaseExpReq)
		if err := <-expReqDone; err != nil {
			t.Fatalf("in-flight Followings before markAuthExpired failed: %v", err)
		}
		if st := store.Status(); st.State != "expired" {
			t.Fatalf("store state = %q, want expired", st.State)
		}
		expClient.webIDMu.Lock()
		gotSynced, gotHash, gotWarmup = expClient.syncedWebID, expClient.syncedTTWIDHash, expClient.webIDWarmupHash
		expClient.webIDMu.Unlock()
		if gotSynced != "" || gotHash != "" || gotWarmup != "" {
			t.Fatalf("expired client retained stale webID state: synced=%q hash=%q warmup=%q", gotSynced, gotHash, gotWarmup)
		}
	})

	t.Run("abnormal_or_blocked_responses_do_not_record_synced_webid", func(t *testing.T) {
		const (
			defaultWID = "7300000000000000066"
			dirtyWID   = "7388888888888888888"
		)
		cases := []struct {
			name     string
			callFav  bool
			headers  map[string]string
			body     string
			wantKind string
		}{
			{
				name:     "favorites_empty_with_bdturing_params",
				callFav:  true,
				headers:  map[string]string{"cookie_ttwidinfo_webid": dirtyWID, "x-vc-bdturing-parameters": `{"code":"10000"}`},
				body:     "",
				wantKind: site.ErrKindUnavailable,
			},
			{
				name:     "watch_history_empty_with_bdturing_params",
				headers:  map[string]string{"cookie_ttwidinfo_webid": dirtyWID, "x-vc-bdturing-parameters": `{"code":"10000"}`},
				body:     "",
				wantKind: site.ErrKindRiskControl,
			},
			{
				name:     "watch_history_json_with_bdturing_verify",
				headers:  map[string]string{"cookie_ttwidinfo_webid": dirtyWID, "bdturing-verify": "verify_1"},
				body:     `{"status_code":0,"aweme_list":[],"has_more":0}`,
				wantKind: site.ErrKindRiskControl,
			},
			{
				name:     "watch_history_html_captcha",
				headers:  map[string]string{"cookie_ttwidinfo_webid": dirtyWID},
				body:     `<html><title>验证码中间页</title></html>`,
				wantKind: site.ErrKindRiskControl,
			},
			{
				name:     "watch_history_malformed_json",
				headers:  map[string]string{"cookie_ttwidinfo_webid": dirtyWID},
				body:     `{"status_code":0,`,
				wantKind: site.ErrKindSchemaChanged,
			},
			{
				name:     "watch_history_nonzero_status_code",
				headers:  map[string]string{"cookie_ttwidinfo_webid": dirtyWID},
				body:     `{"status_code":10000,"status_msg":"verify"}`,
				wantKind: site.ErrKindRiskControl,
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				store := openTestDouyinCredStore(t, true)
				var followWebID string
				client := NewClient(ClientOptions{
					CredStore:   store,
					WebID:       defaultWID,
					MinInterval: -1,
					Entropy:     newDeterministicEntropy(0x69),
					Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
						switch req.URL.Path {
						case "/aweme/v1/web/user/profile/self/":
							// No cookie_ttwidinfo_webid header so Favorites tests abnormal response directly.
							return jsonHTTPResponse(200, `{"status_code":0,"user":{"uid":"900000000001","sec_uid":"`+selfSecUID+`","nickname":"异常拦截用户"}}`, nil), nil
						case pathFavorite, pathHistoryRead:
							return jsonHTTPResponse(200, tc.body, tc.headers), nil
						case pathFollowingList:
							followWebID = req.URL.Query().Get("webid")
							return jsonHTTPResponse(200, `{"status_code":0,"followings":[],"has_more":false,"min_time":0}`, nil), nil
						default:
							return jsonHTTPResponse(404, `{}`, nil), nil
						}
					}),
				})
				var err error
				if tc.callFav {
					_, err = client.Favorites(ctx, selfSecUID, "")
				} else {
					_, err = client.WatchHistory(ctx, "")
				}
				if !site.IsKind(err, tc.wantKind) {
					t.Fatalf("call(%s) err = %v, want kind %s", tc.name, err, tc.wantKind)
				}
				if got := client.syncedWebIDFor(hashTTWID("SYNTH_TTWID")); got != "" {
					t.Fatalf("call(%s) polluted syncedWebID = %q, want empty", tc.name, got)
				}
				if _, err := client.Followings(ctx, selfSecUID, ""); err != nil {
					t.Fatalf("Followings after %s failed: %v", tc.name, err)
				}
				if followWebID != defaultWID {
					t.Fatalf("Followings after %s sent webid = %q, want default %q", tc.name, followWebID, defaultWID)
				}
			})
		}
	})

	t.Run("warmup_ttwid_rotation_and_leader_cancel_vs_remote_timeout", func(t *testing.T) {
		t.Run("leader_cancel_and_ttwid_rotation_marks_actual_outbound_ttwid", func(t *testing.T) {
			store := openTestDouyinCredStore(t, false)
			if _, err := store.SaveVerifiedDouyinManual(map[string]string{
				"sessionid": "SYNTH_SESS_ROT",
				"ttwid":     "SYNTH_TTWID_OLD",
			}, credential.DouyinProfile{UID: "1004", SecUID: selfSecUID, Uname: "轮换用户"}); err != nil {
				t.Fatal(err)
			}

			leaderStarted := make(chan struct{})
			var profileHits atomic.Int32
			client := NewClient(ClientOptions{
				CredStore:   store,
				WebID:       "7300000000000000055",
				MinInterval: -1,
				Entropy:     newDeterministicEntropy(0x6a),
				Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					switch req.URL.Path {
					case "/aweme/v1/web/user/profile/self/":
						if profileHits.Add(1) == 1 {
							close(leaderStarted)
							<-req.Context().Done()
							return nil, req.Context().Err()
						}
						// Second warmup succeeds for rotated ttwid WITHOUT cookie_ttwidinfo_webid header.
						return jsonHTTPResponse(200, `{"status_code":0,"user":{"uid":"1004","sec_uid":"`+selfSecUID+`","nickname":"轮换用户"}}`, nil), nil
					case pathFavorite:
						return jsonHTTPResponse(200, `{"status_code":0,"aweme_list":[],"has_more":0,"max_cursor":0}`, nil), nil
					default:
						return jsonHTTPResponse(404, `{}`, nil), nil
					}
				}),
			})

			leaderCtx, cancelLeader := context.WithCancel(ctx)
			leaderErrCh := make(chan error, 1)
			go func() {
				_, err := client.Favorites(leaderCtx, selfSecUID, "")
				leaderErrCh <- err
			}()
			<-leaderStarted

			waiterErrCh := make(chan error, 1)
			go func() {
				_, err := client.Favorites(ctx, selfSecUID, "")
				waiterErrCh <- err
			}()
			time.Sleep(20 * time.Millisecond)

			// Rotate ttwid before canceling leader so waiter retries with SYNTH_TTWID_NEW.
			if err := store.MergeContextCookies(map[string]string{"ttwid": "SYNTH_TTWID_NEW"}); err != nil {
				t.Fatal(err)
			}
			cancelLeader()

			if err := <-leaderErrCh; !site.IsKind(err, site.ErrKindNetwork) {
				t.Fatalf("canceled leader err = %v, want network cancel", err)
			}
			if err := <-waiterErrCh; err != nil {
				t.Fatalf("waiter after leader cancel failed: %v", err)
			}
			if got := profileHits.Load(); got != 2 {
				t.Fatalf("profile/self hits = %d, want 2 (1 canceled leader + 1 waiter retry)", got)
			}
			client.webIDMu.Lock()
			gotWarmup := client.webIDWarmupHash
			client.webIDMu.Unlock()
			if gotWarmup != hashTTWID("SYNTH_TTWID_NEW") {
				t.Fatalf("webIDWarmupHash = %q, want hash of SYNTH_TTWID_NEW", gotWarmup)
			}
			// Subsequent Favorites for SYNTH_TTWID_NEW must not re-warm.
			if _, err := client.Favorites(ctx, selfSecUID, "1699"); err != nil {
				t.Fatalf("second Favorites failed: %v", err)
			}
			if got := profileHits.Load(); got != 2 {
				t.Fatalf("profile/self hits after second Favorites = %d, want 2", got)
			}
		})

		t.Run("remote_http_timeout_does_not_retrigger_waiters", func(t *testing.T) {
			store := openTestDouyinCredStore(t, true)
			started := make(chan struct{})
			release := make(chan struct{})
			var profileHits atomic.Int32
			client := NewClient(ClientOptions{
				CredStore:   store,
				MinInterval: -1,
				Entropy:     newDeterministicEntropy(0x6b),
				Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					if profileHits.Add(1) == 1 {
						close(started)
						<-release
					}
					// Simulate remote httpClient.Timeout while caller ctx is NOT canceled.
					return nil, context.DeadlineExceeded
				}),
			})

			errCh := make(chan error, 3)
			go func() {
				_, err := client.Favorites(ctx, selfSecUID, "")
				errCh <- err
			}()
			<-started
			for i := 0; i < 2; i++ {
				go func() {
					_, err := client.Favorites(ctx, selfSecUID, "")
					errCh <- err
				}()
			}
			time.Sleep(20 * time.Millisecond)
			close(release)

			for i := 0; i < 3; i++ {
				if err := <-errCh; !site.IsKind(err, site.ErrKindNetwork) {
					t.Fatalf("caller[%d] err = %v, want network timeout error", i, err)
				}
			}
			if got := profileHits.Load(); got != 1 {
				t.Fatalf("profile/self hits on remote timeout = %d, want 1 (waiters must not sequentially retry)", got)
			}
		})
	})
}

func TestBDTuringParametersAndVerifyDetectionWithoutLeaks(t *testing.T) {
	ctx := context.Background()
	const (
		secretDetail = "SYNTH_SECRET_DETAIL_TOKEN_99"
		secretFP     = "verify_SYNTH_SECRET_FP_99"
		secretVerify = "SYNTH_SECRET_VERIFY_HDR_88"
	)

	t.Run("favorites_0b_distinguishes_bdturing_headers_and_stays_unavailable", func(t *testing.T) {
		cases := []struct {
			name        string
			headers     map[string]string
			wantSubstr  string
			forbidMatch string
			wantParams  bool
			wantVerify  bool
			wantGuard   bool
			wantCode    int
		}{
			{
				name: "x_vc_bdturing_parameters",
				headers: map[string]string{
					"x-vc-bdturing-parameters": `{"code":"10000","type":"verify","subtype":"slide","detail":"` + secretDetail + `","fp":"` + secretFP + `"}`,
					"bd-ticket-guard-result":   "1101",
				},
				wantSubstr: "触发抖音动态校验拦截（x-vc-bdturing-parameters）",
				wantParams: true,
				wantGuard:  true,
				wantCode:   1101,
			},
			{
				name: "bdturing_verify",
				headers: map[string]string{
					"bdturing-verify":        secretVerify,
					"bd-ticket-guard-result": "1101",
				},
				wantSubstr: "触发抖音动态校验拦截（bdturing-verify）",
				wantVerify: true,
				wantGuard:  true,
				wantCode:   1101,
			},
			{
				name:        "plain_empty_body",
				headers:     map[string]string{"bd-ticket-guard-result": "1101"},
				wantSubstr:  "返回空响应",
				forbidMatch: "动态校验拦截",
				wantGuard:   true,
				wantCode:    1101,
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				store := openTestDouyinCredStore(t, true)
				var breakerHits atomic.Int32
				client := NewClient(ClientOptions{
					CredStore:   store,
					MinInterval: -1,
					Entropy:     newDeterministicEntropy(0x71),
					Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
						if req.URL.Path == "/aweme/v1/web/user/profile/self/" {
							return jsonHTTPResponse(200, `{"status_code":0,"user":{"uid":"900000000001","sec_uid":"`+selfSecUID+`","nickname":"校验用户"}}`, map[string]string{
								"cookie_ttwidinfo_webid": fixtureSyncWebID,
							}), nil
						}
						return jsonHTTPResponse(200, "", tc.headers), nil
					}),
				})
				client.SetAutomaticRequestHandlers(nil, func(string, error) {
					breakerHits.Add(1)
				})

				autoCtx := site.WithAutomaticRequests(ctx, "test")
				_, err := client.Favorites(autoCtx, selfSecUID, "")
				if !site.IsKind(err, site.ErrKindUnavailable) {
					t.Fatalf("Favorites(%s) err = %v, want unavailable", tc.name, err)
				}
				if breakerHits.Load() != 0 {
					t.Fatalf("Favorites(%s) tripped automatic breaker %d times, want 0", tc.name, breakerHits.Load())
				}
				msg := err.Error()
				if !strings.Contains(msg, tc.wantSubstr) || !strings.Contains(msg, "浏览器") {
					t.Fatalf("Favorites(%s) message = %q, want %q and 浏览器", tc.name, msg, tc.wantSubstr)
				}
				if tc.forbidMatch != "" && strings.Contains(msg, tc.forbidMatch) {
					t.Fatalf("Favorites(%s) message %q should not contain %q", tc.name, msg, tc.forbidMatch)
				}
				for _, secret := range []string{secretDetail, secretFP, secretVerify} {
					if strings.Contains(msg, secret) {
						t.Fatalf("Favorites(%s) leaked secret %q in error: %q", tc.name, secret, msg)
					}
				}
				diag, ok := ExtractResponseDiagnostics(err)
				if !ok {
					t.Fatalf("Favorites(%s) missing ResponseDiagnostics", tc.name)
				}
				if diag.HTTPStatus != 200 || diag.BodyBytes != 0 || !diag.EmptyBody ||
					diag.HasBDTuringParams != tc.wantParams || diag.HasBDTuringVerify != tc.wantVerify ||
					diag.HasGuardResult != tc.wantGuard || diag.GuardResultCode != tc.wantCode || !diag.WebIDSynced {
					t.Fatalf("Favorites(%s) diag = %+v", tc.name, diag)
				}
			})
		}
	})

	t.Run("non_favorites_bdturing_headers_trigger_risk_control_without_leaks", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			header string
			val    string
			body   string
		}{
			{"params_empty", "x-vc-bdturing-parameters", `{"detail":"` + secretDetail + `"}`, ""},
			{"params_json", "x-vc-bdturing-parameters", `{"fp":"` + secretFP + `"}`, `{"status_code":0,"aweme_list":[],"has_more":0}`},
			{"verify_header", "bdturing-verify", secretVerify, ""},
		} {
			t.Run(tc.name, func(t *testing.T) {
				client := NewClient(ClientOptions{
					CredStore:   openTestDouyinCredStore(t, true),
					MinInterval: -1,
					Entropy:     newDeterministicEntropy(0x72),
					Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
						return jsonHTTPResponse(200, tc.body, map[string]string{tc.header: tc.val}), nil
					}),
				})
				_, err := client.WatchHistory(ctx, "")
				if !site.IsKind(err, site.ErrKindRiskControl) {
					t.Fatalf("WatchHistory(%s) err = %v, want risk_control", tc.name, err)
				}
				if !strings.Contains(err.Error(), tc.header) {
					t.Fatalf("WatchHistory(%s) message = %q, want header name %q", tc.name, err.Error(), tc.header)
				}
				for _, secret := range []string{secretDetail, secretFP, secretVerify} {
					if strings.Contains(err.Error(), secret) {
						t.Fatalf("WatchHistory(%s) leaked secret %q: %q", tc.name, secret, err.Error())
					}
				}
			})
		}
	})
}

func TestParseAccountWorksItemFaultToleranceAndLastPageNullList(t *testing.T) {
	ctx := context.Background()

	t.Run("skips_single_broken_malformed_and_deleted_items_without_failing_page", func(t *testing.T) {
		validRaw := awemeNodes(t, "7600000000000000", 2)
		var validArr []json.RawMessage
		if err := json.Unmarshal([]byte(validRaw), &validArr); err != nil {
			t.Fatal(err)
		}
		mixed := []json.RawMessage{
			validArr[0],
			json.RawMessage(`null`),
			json.RawMessage(`{"aweme_id":"76000000000000091","status":{"is_delete":true}}`),
			json.RawMessage(`{"aweme_id":"76000000000000092","aweme_type":68,"images":[{"url_list":[]}]}`), // broken note without watermark-free image URL
			json.RawMessage(`{"aweme_id":"76000000000000093","is_top":"type_drift_string"}`),               // single item JSON type drift
			validArr[1],
		}
		payload, _ := json.Marshal(mixed)
		client, _ := accountFixtureClient(t, func(r capturedReq) (int, string) {
			return 200, `{"status_code":0,"aweme_list":` + string(payload) + `,"has_more":1,"max_cursor":1700}`
		})
		page, err := client.Favorites(ctx, selfSecUID, "")
		if err != nil {
			t.Fatalf("Favorites with mixed broken items failed: %v", err)
		}
		if len(page.Works) != 2 || page.Works[0].ID != "76000000000000000" || page.Works[1].ID != "76000000000000001" || !page.HasMore || page.NextCursor != "1700" {
			t.Fatalf("unexpected page = %+v", page)
		}
	})

	t.Run("all_items_broken_returns_schema_changed", func(t *testing.T) {
		client, _ := accountFixtureClient(t, func(r capturedReq) (int, string) {
			return 200, `{"status_code":0,"aweme_list":[{"aweme_id":"76000000000000094","aweme_type":68,"images":[{"url_list":[]}]},{"aweme_id":"76000000000000095","is_top":"bad"}],"has_more":0,"max_cursor":0}`
		})
		if _, err := client.Favorites(ctx, selfSecUID, ""); !site.IsKind(err, site.ErrKindSchemaChanged) {
			t.Fatalf("all broken items err = %v, want schema_changed", err)
		}
	})

	t.Run("duplicate_broken_items_still_return_schema_changed", func(t *testing.T) {
		client, _ := accountFixtureClient(t, func(r capturedReq) (int, string) {
			return 200, `{"status_code":0,"aweme_list":[{"aweme_id":"76000000000000099","aweme_type":68,"images":[{"url_list":[]}]},{"aweme_id":"76000000000000099","aweme_type":68,"images":[{"url_list":[]}]}],"has_more":0,"max_cursor":0}`
		})
		if _, err := client.Favorites(ctx, selfSecUID, ""); !site.IsKind(err, site.ErrKindSchemaChanged) {
			t.Fatalf("duplicate broken items err = %v, want schema_changed", err)
		}
	})

	t.Run("all_items_explicitly_deleted_succeeds_empty", func(t *testing.T) {
		client, _ := accountFixtureClient(t, func(r capturedReq) (int, string) {
			return 200, `{"status_code":0,"aweme_list":[{"aweme_id":"76000000000000096","status":{"is_delete":true}}],"has_more":0,"max_cursor":0}`
		})
		page, err := client.Favorites(ctx, selfSecUID, "")
		if err != nil || len(page.Works) != 0 || page.HasMore {
			t.Fatalf("all deleted page = %+v err=%v", page, err)
		}
	})

	t.Run("last_page_null_list_with_has_more_zero_succeeds_while_page1_or_has_more_1_stays_shell", func(t *testing.T) {
		var body string
		client, _ := accountFixtureClient(t, func(r capturedReq) (int, string) {
			return 200, body
		})
		// Subsequent last page: aweme_list:null + has_more:0 -> normal empty last page.
		body = `{"status_code":0,"aweme_list":null,"has_more":0,"max_cursor":0}`
		page, err := client.Favorites(ctx, selfSecUID, "1700")
		if err != nil || len(page.Works) != 0 || page.HasMore {
			t.Fatalf("last page null list = %+v err=%v, want empty non-error page", page, err)
		}
		// Page 1: aweme_list:null + has_more:0 -> still shell (risk_control).
		if _, err := client.Favorites(ctx, selfSecUID, ""); !site.IsKind(err, site.ErrKindRiskControl) {
			t.Fatalf("page 1 null list err = %v, want risk_control", err)
		}
		// Subsequent page: aweme_list:null + has_more:1 -> still shell (risk_control).
		body = `{"status_code":0,"aweme_list":null,"has_more":1,"max_cursor":1800}`
		if _, err := client.Favorites(ctx, selfSecUID, "1700"); !site.IsKind(err, site.ErrKindRiskControl) {
			t.Fatalf("has_more=1 null list err = %v, want risk_control", err)
		}
	})
}
