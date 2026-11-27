package weibo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/site"
)

func TestHTTP403BusinessErrorsVsWAFRiskControl(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		body        string
		wantKind    string
	}{
		{
			name:        "403_json_20003_user_missing",
			contentType: "application/json",
			body:        `{"ok":0,"error_code":20003,"msg":"用户不存在"}`,
			wantKind:    site.ErrKindUnavailable,
		},
		{
			name:        "403_json_20101_post_deleted",
			contentType: "application/json",
			body:        `{"ok":0,"errno":20101,"msg":"抱歉，此微博已被作者删除"}`,
			wantKind:    site.ErrKindUnavailable,
		},
		{
			name:        "403_json_20112_forbidden",
			contentType: "application/json",
			body:        `{"ok":0,"error_code":20112,"msg":"由于作者设置，你暂时没有这条微博的查看权限"}`,
			wantKind:    site.ErrKindForbidden,
		},
		{
			name:        "403_json_forbidden_text",
			contentType: "application/json",
			body:        `{"ok":0,"msg":"该内容仅粉丝可见"}`,
			wantKind:    site.ErrKindForbidden,
		},
		{
			name:        "403_json_challenge_stays_risk_control",
			contentType: "application/json",
			body:        `{"ok":0,"error_code":20112,"msg":"触发安全验证 captcha，请先完成滑块"}`,
			wantKind:    site.ErrKindRiskControl,
		},
		{
			name:        "403_html_waf_stays_risk_control",
			contentType: "text/html",
			body:        `<html><body>403 Forbidden WAF</body></html>`,
			wantKind:    site.ErrKindRiskControl,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hdr := make(http.Header)
			hdr.Set("Content-Type", tc.contentType)
			err := ClassifyHTTPAndEnvelopeError(EnvelopeErrorContext{
				Endpoint:   "/ajax/statuses/show",
				StatusCode: http.StatusForbidden,
				Header:     hdr,
				Body:       []byte(tc.body),
				HasCookie:  true,
			})
			if !site.IsKind(err, tc.wantKind) {
				t.Fatalf("ClassifyHTTPAndEnvelopeError(403, %s) = %v (kind=%q), want %q", tc.name, err, site.KindOf(err), tc.wantKind)
			}
		})
	}
}

func TestSystemBusyMappedToTransientNetwork(t *testing.T) {
	// Transient "系统繁忙" must map to ErrKindNetwork (does NOT trigger site-wide backoff),
	// whereas genuine rate-limit codes/messages must stay ErrKindRateLimited.
	rawFields := func(s string) map[string]json.RawMessage {
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			t.Fatalf("unmarshal %s: %v", s, err)
		}
		return m
	}

	busyErr := classifyWeiboOKError("/ajax/statuses/mymblog", 200, 0, rawFields(`{"ok":0,"msg":"系统繁忙，请稍后再试"}`))
	if !site.IsKind(busyErr, site.ErrKindNetwork) {
		t.Fatalf("系统繁忙 kind = %q (%v), want network", site.KindOf(busyErr), busyErr)
	}
	compBusyErr := classifyComponentCodeError("/tv/api/component", 200, "100001", rawFields(`{"code":"100001","msg":"系统繁忙"}`))
	if !site.IsKind(compBusyErr, site.ErrKindNetwork) {
		t.Fatalf("component 系统繁忙 kind = %q (%v), want network", site.KindOf(compBusyErr), compBusyErr)
	}
	rlErr := classifyWeiboOKError("/ajax/statuses/mymblog", 200, 0, rawFields(`{"ok":0,"error_code":10023,"msg":"请求过于频繁"}`))
	if !site.IsKind(rlErr, site.ErrKindRateLimited) {
		t.Fatalf("10023 rate limit kind = %q (%v), want rate_limited", site.KindOf(rlErr), rlErr)
	}
	rlTextErr := classifyWeiboOKError("/ajax/statuses/mymblog", 200, 0, rawFields(`{"ok":0,"msg":"操作过于频繁，请稍后再试"}`))
	if !site.IsKind(rlTextErr, site.ErrKindRateLimited) {
		t.Fatalf("频繁 rate limit kind = %q (%v), want rate_limited", site.KindOf(rlTextErr), rlTextErr)
	}
}

func TestMutedUserDetectionAndBottomTipsTextVisibility(t *testing.T) {
	const synthCookie = "SUB=_2A25SynthDec089; SUBP=0033Synth; XSRF-TOKEN=xsrf_dec089"
	for _, muteVal := range []string{"true", "1"} {
		t.Run("is_muteuser_"+muteVal, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/ajax/profile/info":
					_, _ = w.Write([]byte(`{"ok":1,"data":{"blockText":"","user":{"idstr":"500000010","screen_name":"禁言测试账号","statuses_count":360,"is_muteuser":` + muteVal + `}}}`))
				case "/ajax/statuses/mymblog":
					_, _ = w.Write([]byte(`{"ok":1,"data":{"list":[],"since_id":"","total":360,"bottom_tips_visible":false,"bottom_tips_text":""}}`))
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()

			store := newTestWeiboStore(t, synthCookie)
			client := NewClient(ClientOptions{
				CredStore:      store,
				Endpoints:      testEndpointsForServer(srv.URL),
				AllowLocalhost: true,
				Sleep:          func(context.Context, time.Duration) error { return nil },
			})

			prof, err := client.GetUserProfile(context.Background(), "500000010")
			if err != nil {
				t.Fatalf("GetUserProfile: %v", err)
			}
			if !prof.IsMuteUser {
				t.Fatalf("expected IsMuteUser=true for is_muteuser=%s, got %+v", muteVal, prof)
			}

			_, err = client.GetUserPosts(context.Background(), UserPostsRequest{
				UID:               "500000010",
				Page:              1,
				CrossCheckProfile: true,
			})
			if !site.IsKind(err, site.ErrKindUnavailable) || !strings.Contains(err.Error(), "禁言") {
				t.Fatalf("GetUserPosts(muted) = %v (kind=%q), want unavailable with 禁言 message", err, site.KindOf(err))
			}
		})
	}

	t.Run("bottom_tips_text_without_visible_flag", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":1,"data":{"list":[],"since_id":"","total":88,"bottom_tips_visible":false,"bottom_tips_text":"博主设置了仅展示半年内的微博"}}`))
		}))
		defer srv.Close()

		store := newTestWeiboStore(t, synthCookie)
		client := NewClient(ClientOptions{
			CredStore:      store,
			Endpoints:      testEndpointsForServer(srv.URL),
			AllowLocalhost: true,
			Sleep:          func(context.Context, time.Duration) error { return nil },
		})

		pg, err := client.GetUserPosts(context.Background(), UserPostsRequest{UID: "500000011", Page: 1, CrossCheckProfile: true})
		if err != nil {
			t.Fatalf("GetUserPosts(bottom_tips_text): %v", err)
		}
		if pg.UncertainEmpty || !pg.BottomTipsVisible || pg.HasMore || !strings.Contains(pg.BottomTipsText, "半年") {
			t.Fatalf("expected clean visibility-window end, got %+v", pg)
		}
	})

	t.Run("uncertain_empty_neutral_wording", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/ajax/profile/info":
				_, _ = w.Write([]byte(`{"ok":1,"data":{"blockText":"","user":{"idstr":"500000012","screen_name":"私密测试账号","statuses_count":50,"is_muteuser":false}}}`))
			case "/ajax/statuses/mymblog":
				_, _ = w.Write([]byte(`{"ok":1,"data":{"list":[],"since_id":"","total":50,"bottom_tips_visible":false,"bottom_tips_text":""}}`))
			}
		}))
		defer srv.Close()

		store := newTestWeiboStore(t, synthCookie)
		client := NewClient(ClientOptions{
			CredStore:      store,
			Endpoints:      testEndpointsForServer(srv.URL),
			AllowLocalhost: true,
			Sleep:          func(context.Context, time.Duration) error { return nil },
		})

		pg, err := client.GetUserPosts(context.Background(), UserPostsRequest{UID: "500000012", Page: 1, CrossCheckProfile: true})
		if err != nil {
			t.Fatalf("GetUserPosts(uncertain): %v", err)
		}
		if !pg.UncertainEmpty || strings.Contains(pg.UncertainReason, "风控拦截") {
			t.Fatalf("expected neutral UncertainEmpty without 风控拦截 accusation, got %+v", pg)
		}
	})
}
