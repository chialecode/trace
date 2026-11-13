package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chialecode/trace/internal/site/douyin"
)

// 2026-11-16 feedback: a private Douyin account shows its works to the
// followers it approved. TRACE asks for the works anyway: shown when they
// come back, otherwise the page says the account is private (no error).
func TestDouyinPrivateAccountShowsWorksVisibleToTheSignedInAccount(t *testing.T) {
	const visible, hidden = "MS4wLjABAAAA_synth_private_visible", "MS4wLjABAAAA_synth_private_hidden"
	const limited = "MS4wLjABAAAA_synth_private_limited"
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/aweme/v1/web/user/profile/self/":
			if !strings.Contains(r.Header.Get("Cookie"), "sessionid=") {
				_, _ = fmt.Fprint(w, `{"status_code":8,"status_msg":"请先登录"}`)
				return
			}
			_, _ = fmt.Fprint(w, `{"status_code":0,"user":{"uid":"88001","sec_uid":"MS4wLjABAAAA_synth_self_private_0001","unique_id":"synth_self","nickname":"合成抖音本人"}}`)
		case "/aweme/v1/web/user/profile/other/":
			sec := r.URL.Query().Get("sec_user_id")
			_, _ = fmt.Fprintf(w, `{"status_code":0,"user":{"uid":"99001","sec_uid":%q,"unique_id":"priv","nickname":"私密账号","secret":1,"aweme_count":2}}`, sec)
		case "/aweme/v1/web/aweme/post/":
			sec := r.URL.Query().Get("sec_user_id")
			if sec == limited {
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			if sec != visible {
				_, _ = fmt.Fprint(w, `{"status_code":0,"aweme_list":[],"has_more":0,"max_cursor":0}`)
				return
			}
			_, _ = fmt.Fprintf(w, `{"status_code":0,"has_more":0,"max_cursor":0,"aweme_list":[{"aweme_id":"7400000000000000101","aweme_type":68,"desc":"仅关注者可见","create_time":1720000000,"author":{"uid":"99001","sec_uid":%q,"nickname":"私密账号"},"images":[{"uri":"tos/a","width":32,"height":32,"url_list":[%q]}]}]}`, sec, srv.URL+"/obj/a.png")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	b, err := NewBackend(Options{Locations: testLocations(t), Desktop: NewMockDesktop(), DisableBgWorkers: true,
		DouyinOpts: douyin.ClientOptions{
			Endpoints: douyin.Endpoints{
				ProfileSelf:  srv.URL + "/aweme/v1/web/user/profile/self/",
				ProfileOther: srv.URL + "/aweme/v1/web/user/profile/other/",
				AwemePost:    srv.URL + "/aweme/v1/web/aweme/post/",
			},
			MinInterval: -1, JitterMax: -1, AllowLocalhost: true,
		}})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ctx := context.Background()
	if _, err := b.LoginWithCookie(ctx, "douyin", "sessionid=synth_dy_private; ttwid=synth", ""); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		sec       string
		wantWorks int
	}{{visible, 1}, {hidden, 0}} {
		uv, err := b.DouyinUser(ctx, tc.sec)
		if err != nil || !uv.IsPrivate {
			t.Fatalf("%s: profile %+v err=%v", tc.sec, uv, err)
		}
		wp, errText := b.firstDouyinUserWorks(ctx, uv)
		if errText != "" {
			t.Fatalf("%s: a private account reported an error %q", tc.sec, errText)
		}
		got := 0
		if wp != nil {
			got = len(wp.Works)
		}
		if got != tc.wantWorks || uv.IsPrivate == (tc.wantWorks > 0) || uv.PrivateVisible != (tc.wantWorks > 0) {
			t.Fatalf("%s: works=%d private=%v visible=%v, want %d works", tc.sec, got, uv.IsPrivate, uv.PrivateVisible, tc.wantWorks)
		}
	}

	// A rate limit is not "cannot see the works": it is reported, with the
	// account still private until a retry shows its works.
	uv, err := b.DouyinUser(ctx, limited)
	if err != nil {
		t.Fatal(err)
	}
	wp, errText := b.firstDouyinUserWorks(ctx, uv)
	if wp != nil || errText == "" || !uv.IsPrivate || uv.PrivateVisible {
		t.Fatalf("rate-limited private account: works=%v err=%q private=%v visible=%v, want the error reported", wp, errText, uv.IsPrivate, uv.PrivateVisible)
	}
}
