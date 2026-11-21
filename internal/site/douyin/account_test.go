package douyin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/douyin/sign"
)

const selfSecUID = "MS4wLjABAAAA_EXISTING_SEED_USER_01"

type capturedReq struct {
	method string
	path   string
	query  url.Values
	raw    string
	header http.Header
	body   string
}

// accountFixtureClient serves responses by path from fn and records every request.
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

	// A GET account request carries no body and no form content type.
	_, _ = client.Favorites(context.Background(), selfSecUID, "")
	g := (*seen)[2]
	if g.method != http.MethodGet || g.body != "" || g.header.Get("Content-Type") != "" {
		t.Fatalf("favorite request = %s body=%q ct=%q", g.method, g.body, g.header.Get("Content-Type"))
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
	q := (*seen)[0].query
	if q.Get("sec_user_id") != selfSecUID || q.Get("max_cursor") != "0" || q.Get("count") != "18" || q.Get("a_bogus") == "" {
		t.Fatalf("favorite query = %s", (*seen)[0].raw)
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
	if WatchHistoryEnabled {
		t.Fatal("观看历史 stays off until ACT-020 D-V5 confirms the endpoint")
	}
}
