package bilibili

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func writeBili(w http.ResponseWriter, data any) {
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "message": "0", "data": data})
}

func TestFavFoldersDefaultFirstPrivateAndNullData(t *testing.T) {
	var null atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/x/v3/fav/folder/created/list-all", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("up_mid") != "10001" {
			t.Errorf("up_mid = %q", r.URL.Query().Get("up_mid"))
		}
		if r.Header.Get("Cookie") == "" {
			t.Error("list-all sent without cookies")
		}
		if null.Load() {
			_, _ = w.Write([]byte(`{"code":0,"message":"0","data":null}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"count":3,"list":[
			{"id":9007199254740993,"fid":1,"attr":3,"title":"私密夹","media_count":4},
			{"id":"1052","fid":2,"attr":0,"title":"默认收藏夹","media_count":12},
			{"id":1053,"fid":3,"attr":2,"title":"公开夹","media_count":0}]}}`))
	})
	c, _, _ := newTestLoggedClient(t, mux, -1)
	got, err := c.FavFolders(context.Background(), 10001)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Title != "默认收藏夹" || !got[0].IsDefault || got[0].ID != "1052" {
		t.Fatalf("default not first: %+v", got)
	}
	if got[1].ID != "9007199254740993" || !got[1].Private || got[1].IsDefault {
		t.Fatalf("int64 id or private bit lost: %+v", got[1])
	}
	if got[2].Private || got[2].IsDefault {
		t.Fatalf("public folder flags: %+v", got[2])
	}
	null.Store(true)
	got, err = c.FavFolders(context.Background(), 10001)
	if err != nil || len(got) != 0 {
		t.Fatalf("data=null should be empty, got %+v err=%v", got, err)
	}
}

func TestFavResourcesPagingKindsAndInvalid(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/x/v3/fav/resource/list", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("ps") != "20" || q.Get("order") != "mtime" || q.Get("platform") != "web" || q.Get("media_id") != "1052" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		if q.Get("w_rid") != "" {
			t.Error("resource/list must not be WBI signed")
		}
		switch q.Get("pn") {
		case "1":
			_, _ = w.Write([]byte(`{"code":0,"data":{"info":{"media_count":4},"has_more":true,"medias":[
				{"id":170001,"type":2,"title":"V1","cover":"http://i0.hdslb.com/a.jpg","duration":61,"attr":0,"bvid":"BV17x411w7KC","fav_time":1700000000,"upper":{"mid":42,"name":"UP"}},
				{"id":170002,"type":2,"title":"已失效视频","attr":9,"bvid":"BV1xx411c7mD","fav_time":1699999000,"upper":{"mid":0,"name":""}},
				{"id":555,"type":12,"title":"歌","attr":0,"fav_time":1699998000,"upper":{"mid":7,"name":"音"}}]}}`))
		case "2":
			_, _ = w.Write([]byte(`{"code":0,"data":{"info":{"media_count":4},"has_more":false,"medias":[
				{"id":"777","type":24,"title":"番","attr":0,"bv_id":"","fav_time":1699990000,"ogv":{"type_name":"电影","season_id":33},"upper":{"mid":1,"name":"官方"}}]}}`))
		case "3":
			_, _ = w.Write([]byte(`{"code":0,"data":{"info":{"media_count":0},"has_more":true,"medias":[]}}`))
		default:
			_, _ = w.Write([]byte(`{"code":0,"data":{"info":{"media_count":0},"has_more":false,"medias":null}}`))
		}
	})
	c, _, _ := newTestLoggedClient(t, mux, -1)
	ctx := context.Background()
	p1, err := c.FavResources(ctx, "1052", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !p1.HasMore || len(p1.Items) != 3 || p1.Total != 4 {
		t.Fatalf("page1 = %+v", p1)
	}
	if v := p1.Items[0]; v.Kind != FavKindVideo || v.BVID != "BV17x411w7KC" || v.Invalid || !strings.HasPrefix(v.Cover, "https://") || v.UpperName != "UP" {
		t.Fatalf("video item = %+v", v)
	}
	if !p1.Items[1].Invalid {
		t.Fatalf("attr=9 should be invalid: %+v", p1.Items[1])
	}
	if a := p1.Items[2]; a.Kind != FavKindAudio || a.TypeLabel != "音频" {
		t.Fatalf("audio = %+v", a)
	}
	p2, err := c.FavResources(ctx, "1052", 2)
	if err != nil || p2.HasMore || len(p2.Items) != 1 || p2.Items[0].Kind != FavKindOGV || p2.Items[0].TypeLabel != "电影" || p2.Items[0].SeasonID != "33" {
		t.Fatalf("page2 = %+v err=%v", p2, err)
	}
	if _, err := c.FavResources(ctx, "1052", 3); !IsKind(err, ErrKindSchemaChanged) {
		t.Fatalf("has_more with empty medias: err=%v", err)
	}
	empty, err := c.FavResources(ctx, "1052", 4)
	if err != nil || empty.HasMore || len(empty.Items) != 0 {
		t.Fatalf("empty folder = %+v err=%v", empty, err)
	}
	if _, err := c.FavResources(ctx, "1052&x=1", 1); !IsKind(err, ErrKindBadRequest) {
		t.Fatalf("bad media id err=%v", err)
	}
}

func TestToViewWBIViewedAndProgress(t *testing.T) {
	var seen []url.Values
	mux := http.NewServeMux()
	mux.HandleFunc("/x/v2/history/toview/web", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		seen = append(seen, q)
		if q.Get("viewed") == "2" {
			writeBili(w, map[string]any{"count": 0, "list": nil})
			return
		}
		if q.Get("pn") == "1" {
			writeBili(w, map[string]any{"count": 21, "list": []map[string]any{
				{"aid": 170001, "bvid": "BV17x411w7KC", "title": "看完", "pic": "https://i0.hdslb.com/x.jpg", "duration": 100, "progress": -1, "owner": map[string]any{"mid": 42, "name": "UP"}},
				{"aid": 170002, "bvid": "BV1xx411c7mD", "title": "一半", "duration": 300, "progress": 200, "owner": map[string]any{"mid": 43, "name": "UP2"}},
				{"aid": 3, "bvid": "", "title": "番剧", "is_pgc": true, "progress": 0, "owner": map[string]any{"mid": 1, "name": "官方"}},
			}})
			return
		}
		writeBili(w, map[string]any{"count": 21, "list": []map[string]any{}})
	})
	c, _, _ := newTestLoggedClient(t, mux, -1)
	ctx := context.Background()
	p, err := c.ToView(ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	q := seen[0]
	for k, want := range map[string]string{"pn": "1", "ps": "20", "viewed": "0", "asc": "false", "need_split": "true", "web_location": "333.881", "wts": "1700000000"} {
		if q.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, q.Get(k), want)
		}
	}
	// Fixed keys and clock give a fixed w_rid: recompute it independently of the request path.
	params := url.Values{}
	for k, v := range q {
		if k != "w_rid" {
			params[k] = v
		}
	}
	params.Del("wts")
	want := SignWBIParams(params, MixinKey("7cd084941338484aae1ad9425b84077c", "4932caff0ff746eab6f01bf08b70ac45"), 1700000000).Get("w_rid")
	if len(q.Get("w_rid")) != 32 || q.Get("w_rid") != want {
		t.Fatalf("w_rid = %q, want %q", q.Get("w_rid"), want)
	}
	if p.Total != 21 || !p.HasMore || len(p.Items) != 3 {
		t.Fatalf("page = %+v", p)
	}
	if !p.Items[0].Finished || p.Items[0].Progress != 0 {
		t.Fatalf("progress -1 should be finished: %+v", p.Items[0])
	}
	if p.Items[1].Finished || p.Items[1].Progress != 200 {
		t.Fatalf("half watched: %+v", p.Items[1])
	}
	if !p.Items[2].Unsupported {
		t.Fatalf("pgc should be unsupported: %+v", p.Items[2])
	}
	if _, err := c.ToView(ctx, 2, false); !IsKind(err, ErrKindSchemaChanged) {
		t.Fatalf("empty page below count: err=%v", err)
	}
	un, err := c.ToView(ctx, 1, true)
	if err != nil || un.HasMore || len(un.Items) != 0 || seen[len(seen)-1].Get("viewed") != "2" {
		t.Fatalf("viewed=2 empty: %+v err=%v", un, err)
	}
}

func TestWatchHistoryCursorBusinessesAndStall(t *testing.T) {
	var calls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/x/web-interface/history/cursor", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		q := r.URL.Query()
		if q.Get("type") != "all" || q.Get("ps") != "20" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		switch q.Get("max") {
		case "0":
			if q.Has("business") {
				t.Error("first page must not send business")
			}
			writeBili(w, map[string]any{"cursor": map[string]any{"max": 900, "view_at": 1700000000, "business": "archive"}, "list": []map[string]any{
				{"title": "V", "cover": "http://i0.hdslb.com/v.jpg", "author_name": "UP", "author_mid": 42, "view_at": 1700000100, "progress": 200, "duration": 300, "history": map[string]any{"oid": 170001, "bvid": "BV17x411w7KC", "page": 1, "business": "archive"}},
				{"title": "直播间", "cover": "", "covers": []string{"https://i0.hdslb.com/l.jpg"}, "author_name": "主播", "author_mid": 77, "view_at": 1700000050, "live_status": 1, "history": map[string]any{"oid": 213, "business": "live"}},
			}})
		case "900":
			if q.Get("view_at") != "1700000000" || q.Get("business") != "archive" {
				t.Errorf("cursor not passed: %s", r.URL.RawQuery)
			}
			writeBili(w, map[string]any{"cursor": map[string]any{"max": 800, "view_at": 1690000000, "business": "pgc"}, "list": []map[string]any{
				{"title": "番", "badge": "番剧", "uri": "https://www.bilibili.com/bangumi/play/ep1", "view_at": 1690000000, "progress": -1, "history": map[string]any{"oid": 5, "business": "pgc"}},
				{"title": "文", "uri": "https://www.bilibili.com/read/cv1", "history": map[string]any{"oid": 6, "business": "article"}},
			}})
		case "800":
			// Stalled: the site hands back the cursor it was given.
			writeBili(w, map[string]any{"cursor": map[string]any{"max": 800, "view_at": 1690000000, "business": "pgc"}, "list": []map[string]any{
				{"title": "重复", "history": map[string]any{"oid": 5, "business": "pgc"}},
			}})
		default:
			writeBili(w, map[string]any{"cursor": map[string]any{"max": 0, "view_at": 0, "business": ""}, "list": []map[string]any{}})
		}
	})
	c, _, _ := newTestLoggedClient(t, mux, -1)
	ctx := context.Background()
	p1, err := c.WatchHistory(ctx, HistoryCursor{})
	if err != nil || !p1.HasMore || len(p1.Items) != 2 {
		t.Fatalf("p1 = %+v err=%v", p1, err)
	}
	if p1.Items[0].Business != HistoryArchive || p1.Items[0].BVID == "" || p1.Items[0].Progress != 200 {
		t.Fatalf("archive = %+v", p1.Items[0])
	}
	if l := p1.Items[1]; l.Business != HistoryLive || !l.Live || l.AuthorMid != 77 || l.Cover != "https://i0.hdslb.com/l.jpg" || l.TypeLabel != "直播" {
		t.Fatalf("live = %+v", l)
	}
	p2, err := c.WatchHistory(ctx, p1.Next)
	if err != nil || !p2.HasMore || p2.Next.Max != 800 {
		t.Fatalf("p2 = %+v err=%v", p2, err)
	}
	if p2.Items[0].TypeLabel != "番剧" || !p2.Items[0].Finished || p2.Items[1].TypeLabel != "专栏" {
		t.Fatalf("p2 kinds = %+v", p2.Items)
	}
	before := calls.Load()
	if _, err := c.WatchHistory(ctx, p2.Next); !IsKind(err, ErrKindSchemaChanged) {
		t.Fatalf("stalled cursor err=%v", err)
	}
	if calls.Load()-before != 1 {
		t.Fatalf("stall detection made %d requests, want 1", calls.Load()-before)
	}
	end, err := c.WatchHistory(ctx, HistoryCursor{Max: 1, ViewAt: 1})
	if err != nil || end.HasMore {
		t.Fatalf("end = %+v err=%v", end, err)
	}
}

func TestAccountListsErrorClasses(t *testing.T) {
	var mode atomic.Value
	mode.Store("")
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch mode.Load().(string) {
		case "-101":
			_, _ = w.Write([]byte(`{"code":-101,"message":"账号未登录"}`))
		case "-352":
			_, _ = w.Write([]byte(`{"code":-352,"message":"风控校验失败"}`))
		case "-412":
			_, _ = w.Write([]byte(`{"code":-412,"message":"请求被拦截"}`))
		case "412":
			w.WriteHeader(http.StatusPreconditionFailed)
		case "html":
			_, _ = w.Write([]byte(`<html>bad</html>`))
		case "huge":
			_, _ = w.Write([]byte(`{"code":0,"data":"` + strings.Repeat("x", MaxAPIResponseBytes) + `"}`))
		}
	})
	c, store, _ := newTestLoggedClient(t, mux, -1)
	ctx := context.Background()
	cases := []struct{ mode, kind string }{
		{"-352", ErrKindRiskControl}, {"-412", ErrKindRiskControl}, {"412", ErrKindRiskControl},
		{"html", ErrKindSchemaChanged}, {"huge", ErrKindSchemaChanged}, {"-101", ErrKindAuthRequired},
	}
	for _, tc := range cases {
		mode.Store(tc.mode)
		if _, err := c.FavResources(ctx, "1", 1); !IsKind(err, tc.kind) {
			t.Errorf("fav %s: err=%v want %s", tc.mode, err, tc.kind)
		}
		if tc.mode == "-101" {
			break
		}
		if _, err := c.WatchHistory(ctx, HistoryCursor{}); !IsKind(err, tc.kind) {
			t.Errorf("history %s: err=%v want %s", tc.mode, err, tc.kind)
		}
	}
	if store.HasActiveCredential() {
		t.Fatal("-101 should expire the login")
	}
	if _, err := c.FavFolders(ctx, 0); !IsKind(err, ErrKindAuthRequired) {
		t.Fatalf("after -101 requests must stop before sending: %v", err)
	}
}
