package bilibili

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
)

func TestRenewalRejectsUnverifiedAccount(t *testing.T) {
	for _, response := range []string{
		`{"code":0,"data":{"isLogin":true,"mid":20002,"uname":"different"}}`,
		`{"code":0,"data":{"isLogin":false}}`,
		`{"code":-101,"message":"invalid candidate"}`,
	} {
		t.Run(response, func(t *testing.T) {
			var confirmations atomic.Int32
			client, store, _ := newTestLoggedClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/x/passport-login/web/cookie/info":
					_, _ = w.Write([]byte(`{"code":0,"data":{"refresh":true,"timestamp":1700000123456}}`))
				case len(r.URL.Path) > len("/correspond/1/") && r.URL.Path[:len("/correspond/1/")] == "/correspond/1/":
					_, _ = w.Write([]byte(`<div id="1-name">fixture-csrf</div>`))
				case r.URL.Path == "/x/passport-login/web/cookie/refresh":
					http.SetCookie(w, &http.Cookie{Name: "SESSDATA", Value: "candidate-session"})
					http.SetCookie(w, &http.Cookie{Name: "bili_jct", Value: "candidate-csrf"})
					_, _ = w.Write([]byte(`{"code":0,"data":{"status":0,"refresh_token":"candidate-refresh"}}`))
				case r.URL.Path == "/x/web-interface/nav":
					_, _ = w.Write([]byte(response))
				case r.URL.Path == "/x/passport-login/web/confirm/refresh":
					confirmations.Add(1)
					_, _ = w.Write([]byte(`{"code":0}`))
				default:
					http.NotFound(w, r)
				}
			}), -1)
			if _, err := store.SaveQR(map[string]string{"SESSDATA": "original-session", "bili_jct": "original-csrf"}, "original-refresh", 10001, "original", ""); err != nil {
				t.Fatal(err)
			}
			if _, err := client.CheckAndRefreshCookie(context.Background()); err == nil {
				t.Fatal("unverified renewed credential was accepted")
			}
			refresh, pending, _ := store.RefreshState()
			if store.Cookies()["SESSDATA"] != "original-session" || refresh != "original-refresh" || pending || store.Status().Mid != 10001 || confirmations.Load() != 0 {
				t.Fatal("renewal replaced the original credential before validation")
			}
		})
	}
}

func TestQRConfirmationRequiresValidCandidate(t *testing.T) {
	client, store, _ := newTestLoggedClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/x/passport-login/web/qrcode/generate":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"url": "https://passport.bilibili.com/qrcode/fixture", "qrcode_key": "fixture"}})
		case "/x/passport-login/web/qrcode/poll":
			http.SetCookie(w, &http.Cookie{Name: "SESSDATA", Value: "bad-qr-session"})
			http.SetCookie(w, &http.Cookie{Name: "bili_jct", Value: "bad-qr-csrf"})
			_, _ = w.Write([]byte(`{"code":0,"data":{"code":0,"refresh_token":"bad-qr-refresh"}}`))
		case "/x/web-interface/nav":
			_, _ = w.Write([]byte(`{"code":-101,"data":{"isLogin":false}}`))
		default:
			http.NotFound(w, r)
		}
	}), -1)
	session, err := client.StartQR(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.PollQR(context.Background(), session.FlowID)
	if err == nil || result.State == "confirmed" || store.Cookies()["SESSDATA"] != "test_sess_1" || store.Status().Source != "manual" {
		t.Fatalf("invalid QR result replaced existing credential: %+v %v", result, err)
	}
}
