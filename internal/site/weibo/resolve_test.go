package weibo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/chialecode/trace/internal/site"
)

func TestResolveSupportedLocalLinksZeroAPIRequests(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	store := newTestWeiboStore(t, "SUB=SYNTHETIC_SUB_RESOLVE")
	client := newFastTestClient(t, srv.URL, store)

	cases := []struct {
		name      string
		input     string
		wantKind  string
		wantID    string
		wantMblog string
		wantUID   string
	}{
		{
			name:      "pc_uid_mblogid",
			input:     "https://weibo.com/1000000001/O00000001",
			wantKind:  "dynamic",
			wantID:    "5000000000000001",
			wantMblog: "O00000001",
			wantUID:   "1000000001",
		},
		{
			name:      "pc_uid_numeric_mid",
			input:     "https://www.weibo.com/1000000001/5000000000000001",
			wantKind:  "dynamic",
			wantID:    "5000000000000001",
			wantMblog: "O00000001",
			wantUID:   "1000000001",
		},
		{
			name:      "pc_uid_ten_char_numeric_mblogid",
			input:     "https://weibo.com/1000000001/1000010001",
			wantKind:  "dynamic",
			wantID:    "6200000010000001",
			wantMblog: "1000010001",
			wantUID:   "1000000001",
		},
		{
			name:      "weibo_cn_post",
			input:     "https://weibo.cn/1000000001/P03d70010",
			wantKind:  "dynamic",
			wantID:    "5100123450000062",
			wantMblog: "P03d70010",
			wantUID:   "1000000001",
		},
		{
			name:      "detail_mid",
			input:     "https://weibo.com/detail/5000000000000001",
			wantKind:  "dynamic",
			wantID:    "5000000000000001",
			wantMblog: "O00000001",
		},
		{
			name:      "detail_v2_mid",
			input:     "https://weibo.com/2/detail/5000000000000001",
			wantKind:  "dynamic",
			wantID:    "5000000000000001",
			wantMblog: "O00000001",
		},
		{
			name:      "mobile_status_mblogid",
			input:     "https://m.weibo.cn/status/NFXsjFXsj",
			wantKind:  "dynamic",
			wantID:    "4999999999999999",
			wantMblog: "NFXsjFXsj",
		},
		{
			name:      "mobile_statuses_show_query",
			input:     "https://m.weibo.cn/statuses/show?id=5000000000000001",
			wantKind:  "dynamic",
			wantID:    "5000000000000001",
			wantMblog: "O00000001",
		},
		{
			name:      "mobile_channel_suffix_qq",
			input:     "https://m.weibo.cn/1000000001/5000000000000001/qq?wm=3333_2001",
			wantKind:  "dynamic",
			wantID:    "5000000000000001",
			wantMblog: "O00000001",
			wantUID:   "1000000001",
		},
		{
			name:      "app_share_comma_filename",
			input:     "https://share.api.weibo.cn/share/100000001,5000000000000001.html",
			wantKind:  "dynamic",
			wantID:    "5000000000000001",
			wantMblog: "O00000001",
		},
		{
			name:      "app_share_query_weibo_id",
			input:     "https://weibointl.api.weibo.com/share/100000001.html?weibo_id=5000000000000001",
			wantKind:  "dynamic",
			wantID:    "5000000000000001",
			wantMblog: "O00000001",
		},
		{
			name:      "share_text_prefers_insite_over_tcn",
			input:     "【合成正文】点这里 http://t.cn/A6SYNTHETIC 分享@合成示例用户 的微博 https://m.weibo.cn/status/5000000000000001",
			wantKind:  "dynamic",
			wantID:    "5000000000000001",
			wantMblog: "O00000001",
		},
		{
			name:     "video_page_fid_encoded_colon",
			input:    "https://video.weibo.com/show?fid=1034%3A5000000000000001",
			wantKind: "video",
			wantID:   "1034:5000000000000001",
		},
		{
			name:     "video_page_tv_show_hex",
			input:    "https://weibo.com/tv/show/1034:a1b2c3d4e5f60001",
			wantKind: "video",
			wantID:   "1034:a1b2c3d4e5f60001",
		},
		{
			name:     "video_page_mobile_index",
			input:    "https://m.weibo.cn/s/video/index?object_id=1034:5000000000000001",
			wantKind: "video",
			wantID:   "1034:5000000000000001",
		},
		{
			name:      "video_page_with_host_mid_query",
			input:     "https://video.weibo.com/show?fid=1034:5000000000000001&mid=5000000000000001",
			wantKind:  "dynamic",
			wantID:    "5000000000000001",
			wantMblog: "O00000001",
		},
		{
			name:     "user_u_uid_strip_leading_zeros",
			input:    "https://weibo.com/u/001000000001",
			wantKind: "user",
			wantID:   "1000000001",
			wantUID:  "1000000001",
		},
		{
			name:     "user_mobile_profile",
			input:    "https://m.weibo.cn/profile/1000000001",
			wantKind: "user",
			wantID:   "1000000001",
			wantUID:  "1000000001",
		},
		{
			name:     "user_p_100505_with_home",
			input:    "https://weibo.com/p/1005051000000001/home",
			wantKind: "user",
			wantID:   "1000000001",
			wantUID:  "1000000001",
		},
		{
			name:     "user_p_100606_org",
			input:    "https://weibo.com/p/1006061000000001/info",
			wantKind: "user",
			wantID:   "1000000001",
			wantUID:  "1000000001",
		},
		{
			name:     "user_single_numeric_segment",
			input:    "https://weibo.com/1000000001",
			wantKind: "user",
			wantID:   "1000000001",
			wantUID:  "1000000001",
		},
		{
			name:     "user_numeric_with_profile_subpath",
			input:    "https://weibo.com/1000000001/profile",
			wantKind: "user",
			wantID:   "1000000001",
			wantUID:  "1000000001",
		},
		{
			name:      "sinaurl_local_unpack_u_param",
			input:     "https://weibo.cn/sinaurl?u=https%3A%2F%2Fweibo.com%2F1000000001%2FO00000001",
			wantKind:  "dynamic",
			wantID:    "5000000000000001",
			wantMblog: "O00000001",
			wantUID:   "1000000001",
		},
		{
			name:     "sinaurl_local_unpack_toasturl",
			input:    "https://weibo.cn/sinaurl?toasturl=https%3A%2F%2Fweibo.com%2Fu%2F1000000001",
			wantKind: "user",
			wantID:   "1000000001",
			wantUID:  "1000000001",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := client.ResolveLink(context.Background(), tc.input)
			if err != nil {
				t.Fatalf("ResolveLink(%q) unexpected error: %v", tc.input, err)
			}
			if got.Site != site.Weibo || got.Kind != tc.wantKind || got.ID != tc.wantID {
				t.Fatalf("ResolveLink(%q) = %+v, want kind=%q id=%q", tc.input, got, tc.wantKind, tc.wantID)
			}
			if tc.wantMblog != "" && got.MblogID != tc.wantMblog {
				t.Fatalf("MblogID = %q, want %q", got.MblogID, tc.wantMblog)
			}
			if tc.wantUID != "" && got.AuthorUID != tc.wantUID {
				t.Fatalf("AuthorUID = %q, want %q", got.AuthorUID, tc.wantUID)
			}
		})
	}
	if hits.Load() != 0 {
		t.Fatalf("local links issued %d API requests, want 0", hits.Load())
	}
}

func TestResolveUnsupportedAndLiveLinksZeroAPIRequests(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	store := newTestWeiboStore(t, "SUB=SYNTHETIC_SUB_UNSUP")
	client := newFastTestClient(t, srv.URL, store)

	// Live links return Kind:"live", Unsupported:true with exact Chinese message and 0 requests.
	liveLinks := []string{
		"https://weibo.com/l/wblive/p/show/1022:2321325000000000000001",
		"https://weibo.com/l/wblive/m/show/1022:2321325000000000000001",
		"https://live.weibo.com/show?id=1022:2321325000000000000001",
	}
	for _, link := range liveLinks {
		target, err := client.ResolveLink(context.Background(), link)
		if !site.IsUnsupported(err) {
			t.Fatalf("live link %q err = %v, want Unsupported", link, err)
		}
		if target.Kind != "live" || !target.Unsupported || target.Reason != liveUnsupportText {
			t.Fatalf("live link %q target = %+v, want kind=live unsupported=true reason=%q", link, target, liveUnsupportText)
		}
		if !strings.Contains(err.Error(), liveUnsupportText) {
			t.Fatalf("live link %q err text = %q, want %q", link, err.Error(), liveUnsupportText)
		}
	}

	// Unsupported links & reserved paths MUST be rejected BEFORE custom-domain lookup with 0 API calls.
	unsupportedLinks := []string{
		"https://s.weibo.com/weibo?q=%E5%90%88%E6%88%90",
		"https://card.weibo.com/article/m/show/id/2309400000000001",
		"https://weibo.com/ttarticle/p/show?id=2309400000000001",
		"https://weibo.com/p/10080800000000000000000000000001",
		"https://m.weibo.cn/p/index?containerid=10080800000000000000000000000001",
		"https://weibo.com/k/supertopic_synthetic",
		"https://weibo.com/hot/search",
		"https://weibo.com/a/hot/realtime",
		"https://weibo.com/home",
		"https://weibo.com/fav",
		"https://weibo.com/mygroups",
		"https://weibo.com/ajax",
		"https://weibo.com/settings",
		"https://weibo.com/discover",
		"https://vote.weibo.com/h5/index/index?vote_id=100001",
		"https://mall.weibo.com/item/100001",
		"https://e.weibo.com/1000000001",
		"https://passport.weibo.com/sso/login",
		"https://weibo.com/newlogin?tabtype=weibo",
		"https://visitor.passport.weibo.cn/visitor/visitor",
		"https://weibo.com/tv/collection/100001",
		"https://weibo.cn/sinaurl?u=https%3A%2F%2Fexample.com%2Fexternal",
		"http://weibo.com/1000000001/O00000001",
		"https://user:pass@weibo.com/1000000001/O00000001",
		"https://weibo.com:8443/1000000001/O00000001",
		"http://t.cn:8080/A6SYNTHETIC",
		"https://user:pass@t.cn/A6SYNTHETIC",
	}
	for _, link := range unsupportedLinks {
		if _, err := client.ResolveLink(context.Background(), link); err == nil {
			t.Fatalf("expected unsupported/blocked error for %q, got nil", link)
		} else if !site.IsUnsupported(err) && !site.IsKind(err, site.ErrKindBlocked) {
			t.Fatalf("link %q err = %v, want unsupported_link or blocked", link, err)
		}
	}

	if hits.Load() != 0 {
		t.Fatalf("unsupported/live links issued %d API requests, want 0", hits.Load())
	}
}

func TestResolveNicknameAndCustomDomainSingleAPICall(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("screen_name") == "合成示例用户" {
			_, _ = w.Write([]byte(`{"ok":1,"data":{"user":{"idstr":"1000000001","screen_name":"合成示例用户"}}}`))
			return
		}
		if r.URL.Query().Get("custom") == "synthetic_creator" {
			_, _ = w.Write([]byte(`{"ok":1,"data":{"user":{"idstr":"1000000002","screen_name":"个性域名创作者","domain":"synthetic_creator"}}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	store := newTestWeiboStore(t, "SUB=SYNTHETIC_SUB_USER")
	client := newFastTestClient(t, srv.URL, store)

	// /n/<nick> -> exactly 1 API call.
	t1, err := client.ResolveLink(context.Background(), "https://weibo.com/n/%E5%90%88%E6%88%90%E7%A4%BA%E4%BE%8B%E7%94%A8%E6%88%B7")
	if err != nil {
		t.Fatalf("/n/ resolve failed: %v", err)
	}
	if t1.Kind != "user" || t1.ID != "1000000001" || hits.Load() != 1 {
		t.Fatalf("/n/ resolve = (%+v, hits=%d), want uid=1000000001 hits=1", t1, hits.Load())
	}

	// Custom domain -> exactly 1 API call.
	t2, err := client.ResolveLink(context.Background(), "https://weibo.com/synthetic_creator")
	if err != nil {
		t.Fatalf("custom domain resolve failed: %v", err)
	}
	if t2.Kind != "user" || t2.ID != "1000000002" || hits.Load() != 2 {
		t.Fatalf("custom domain resolve = (%+v, hits=%d), want uid=1000000002 hits=2", t2, hits.Load())
	}

	// /n/<nick> with literal percent sequence (%2520 -> %20) must NOT be double-unescaped!
	var gotPercentNick string
	srvPct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPercentNick = r.URL.Query().Get("screen_name")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":1,"data":{"user":{"idstr":"1000000003","screen_name":"100%20测试"}}}`))
	}))
	defer srvPct.Close()
	pctClient := newFastTestClient(t, srvPct.URL, store)
	t3, err := pctClient.ResolveLink(context.Background(), "https://weibo.com/n/100%2520%E6%B5%8B%E8%AF%95")
	if err != nil || t3.Kind != "user" || t3.ID != "1000000003" {
		t.Fatalf("/n/ percent-encoded resolve = (%+v, %v)", t3, err)
	}
	if gotPercentNick != "100%20测试" {
		t.Fatalf("/n/ screen_name double-unescaped: got %q, want %q", gotPercentNick, "100%20测试")
	}
}

func TestResolveShortLinkTCNSecurityHopsAndEarlyConvergence(t *testing.T) {
	var hopHits atomic.Int32
	var leakedCookie atomic.Bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hopHits.Add(1)
		if r.Header.Get("Cookie") != "" {
			leakedCookie.Store(true)
		}
		switch r.URL.Path {
		case "/tcn/direct":
			http.Redirect(w, r, "https://weibo.com/1000000001/O00000001", http.StatusFound)
		case "/tcn/via_sinaurl":
			http.Redirect(w, r, "https://weibo.cn/sinaurl?u=https%3A%2F%2Fweibo.com%2F1000000001%2FP03d70010", http.StatusFound)
		case "/tcn/via_sinaurl_ext":
			http.Redirect(w, r, "https://weibo.cn/sinaurl?u=https%3A%2F%2Fexample.com%2Fad", http.StatusFound)
		case "/tcn/external":
			http.Redirect(w, r, "https://evil.example.com/phish", http.StatusFound)
		case "/tcn/loginwall":
			http.Redirect(w, r, "https://passport.weibo.com/visitor/visitor?entry=miniblog", http.StatusFound)
		case "/tcn/newlogin":
			http.Redirect(w, r, "https://weibo.com/newlogin?url=https%3A%2F%2Fweibo.com", http.StatusFound)
		case "/tcn/h1":
			http.Redirect(w, r, "/tcn/h2", http.StatusFound)
		case "/tcn/h2":
			http.Redirect(w, r, "/tcn/h3", http.StatusFound)
		case "/tcn/h3":
			http.Redirect(w, r, "/tcn/h4", http.StatusFound)
		case "/tcn/h4":
			http.Redirect(w, r, "/tcn/h5", http.StatusFound)
		case "/tcn/h5":
			http.Redirect(w, r, "/tcn/h6", http.StatusFound)
		case "/tcn/h6":
			http.Redirect(w, r, "https://weibo.com/1000000001/O00000001", http.StatusFound)
		case "/tcn/loop":
			http.Redirect(w, r, "/tcn/loop", http.StatusFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	store := newTestWeiboStore(t, "SUB=SYNTHETIC_SUB_NEVER_SEND_TO_TCN")
	client := newFastTestClient(t, srv.URL, store)

	// 1. Direct early convergence (1 hop, no Cookie).
	hopHits.Store(0)
	got, err := client.ResolveLink(context.Background(), srv.URL+"/tcn/direct")
	if err != nil || got.Kind != "dynamic" || got.ID != "5000000000000001" || hopHits.Load() != 1 {
		t.Fatalf("direct tcn = (%+v, %v, hits=%d)", got, err, hopHits.Load())
	}

	// 2. t.cn -> sinaurl local unpack (1 network hop only!).
	hopHits.Store(0)
	got, err = client.ResolveLink(context.Background(), srv.URL+"/tcn/via_sinaurl")
	if err != nil || got.Kind != "dynamic" || got.ID != "5100123450000062" || hopHits.Load() != 1 {
		t.Fatalf("via_sinaurl tcn = (%+v, %v, hits=%d)", got, err, hopHits.Load())
	}

	// 3. t.cn -> sinaurl pointing external -> blocked.
	if _, err := client.ResolveLink(context.Background(), srv.URL+"/tcn/via_sinaurl_ext"); !site.IsKind(err, site.ErrKindBlocked) {
		t.Fatalf("via_sinaurl_ext err = %v, want blocked", err)
	}

	// 4. t.cn -> external host -> blocked.
	if _, err := client.ResolveLink(context.Background(), srv.URL+"/tcn/external"); !site.IsKind(err, site.ErrKindBlocked) {
		t.Fatalf("external hop err = %v, want blocked", err)
	}

	// 5. t.cn -> visitor / login wall -> auth_required.
	if _, err := client.ResolveLink(context.Background(), srv.URL+"/tcn/loginwall"); !site.IsKind(err, site.ErrKindAuthRequired) {
		t.Fatalf("visitor hop err = %v, want auth_required", err)
	}
	if _, err := client.ResolveLink(context.Background(), srv.URL+"/tcn/newlogin"); !site.IsKind(err, site.ErrKindAuthRequired) {
		t.Fatalf("newlogin hop err = %v, want auth_required", err)
	}

	// 6. >5 hops -> blocked at exactly 5 requests.
	hopHits.Store(0)
	if _, err := client.ResolveLink(context.Background(), srv.URL+"/tcn/h1"); !site.IsKind(err, site.ErrKindBlocked) || hopHits.Load() != 5 {
		t.Fatalf("over-5-hops = (err=%v, hits=%d), want blocked with 5 hits", err, hopHits.Load())
	}

	// 7. Self-loop -> blocked immediately.
	if _, err := client.ResolveLink(context.Background(), srv.URL+"/tcn/loop"); !site.IsKind(err, site.ErrKindBlocked) {
		t.Fatalf("self-loop err = %v, want blocked", err)
	}

	// 8. Verify NO Cookie was ever transmitted on ANY short-link hop!
	if leakedCookie.Load() {
		t.Fatalf("short-link hop leaked Cookie header!")
	}

	// 9. Verify http://t.cn/ locally upgrades to https://t.cn/.
	upgraded, err := client.parseAndValidateInputURL("http://t.cn/A6SYNTHETIC")
	if err != nil || upgraded.Scheme != "https" || upgraded.Hostname() != "t.cn" {
		t.Fatalf("http://t.cn upgrade = (%v, %v), want https://t.cn/A6SYNTHETIC", upgraded, err)
	}

	// 10. Logged-out client issues 0 requests even on valid short links.
	hopHits.Store(0)
	loggedOutClient := newFastTestClient(t, srv.URL, newTestWeiboStore(t, ""))
	if _, err := loggedOutClient.ResolveLink(context.Background(), srv.URL+"/tcn/direct"); !site.IsKind(err, site.ErrKindAuthRequired) || hopHits.Load() != 0 {
		t.Fatalf("logged-out resolve = (err=%v, hits=%d), want auth_required with 0 hits", err, hopHits.Load())
	}
}
