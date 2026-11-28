package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/weibo"
)

func synthServiceQRPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			img.Set(x, y, color.Black)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestWeiboQRLoginServiceLifecycleEventsRedactionAndCancel(t *testing.T) {
	const (
		qridSecret   = "SYNTH_SVC_QRID_SECRET_7788"
		altSecret    = "SYNTH_SVC_ALT_SECRET_9900"
		ticketSecret = "SYNTH_SVC_TICKET_SECRET_1122"
		subSecret    = "_2A25synthSvcQRSubSecret=="
		subpSecret   = "0033synthSvcQRSubpSecret"
		xsrfSecret   = "synthSvcQRXsrfSecret"
	)

	qrPNG := synthServiceQRPNG(t)
	var pollStep atomic.Int32

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cb := r.URL.Query().Get("callback")
		switch r.URL.Path {
		case "/sso/v2/qrcode/image":
			payload, _ := json.Marshal(map[string]any{
				"retcode": 20000000,
				"data": map[string]any{
					"qrid":  qridSecret,
					"image": srv.URL + "/qr.png",
				},
			})
			_, _ = fmt.Fprintf(w, "%s(%s);", cb, payload)
		case "/qr.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(qrPNG)
		case "/sso/v2/qrcode/check":
			switch pollStep.Add(1) {
			case 1:
				_, _ = fmt.Fprintf(w, `%s({"retcode":50114001});`, cb)
			case 2:
				_, _ = fmt.Fprintf(w, `%s({"retcode":50114002});`, cb)
			default:
				_, _ = fmt.Fprintf(w, `%s({"retcode":20000000,"data":{"alt":%q}});`, cb, altSecret)
			}
		case "/sso/v2/login":
			raw, _ := json.Marshal(map[string]any{
				"retcode": "0",
				"uid":     synthWeiboSelfUID,
				"nick":    synthWeiboSelfName,
				"crossDomainUrlList": []string{
					srv.URL + "/wbsso/login?ticket=" + ticketSecret,
				},
			})
			_, _ = fmt.Fprintf(w, "%s(%s);", cb, raw)
		case "/wbsso/login":
			http.SetCookie(w, &http.Cookie{Name: "SUB", Value: subSecret, Domain: ".weibo.com"})
			http.SetCookie(w, &http.Cookie{Name: "SUBP", Value: subpSecret, Domain: ".weibo.com"})
			w.WriteHeader(http.StatusOK)
		case "/ajax/setting/getBasicInfo":
			http.SetCookie(w, &http.Cookie{Name: "XSRF-TOKEN", Value: xsrfSecret})
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"ok":1,"data":{"screen_name":%q,"domain":"synthself"}}`, synthWeiboSelfName)
		case "/ajax/profile/info":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"ok":1,"data":{"user":{"idstr":%q,"screen_name":%q,"avatar_hd":"https://tvax1.sinaimg.cn/synth.jpg"}}}`, synthWeiboSelfUID, synthWeiboSelfName)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	b, err := NewBackend(Options{
		Locations:        testLocations(t),
		Desktop:          NewMockDesktop(),
		DisableBgWorkers: true,
		WeiboOpts: weibo.ClientOptions{
			Endpoints: weibo.Endpoints{
				PCBasicInfo:     srv.URL + "/ajax/setting/getBasicInfo",
				ProfileInfo:     srv.URL + "/ajax/profile/info",
				StatusesShow:    srv.URL + "/ajax/statuses/show",
				StatusesLong:    srv.URL + "/ajax/statuses/longtext",
				StatusesMyMblog: srv.URL + "/ajax/statuses/mymblog",
				TVComponent:     srv.URL + "/tv/api/component",
				Friends:         srv.URL + "/ajax/friendships/friends",
				FollowContent:   srv.URL + "/ajax/profile/followContent",
				AllGroups:       srv.URL + "/ajax/feed/allGroups",
				AllFavorites:    srv.URL + "/ajax/favorites/all_fav",
				LikeList:        srv.URL + "/ajax/statuses/likelist",
				QRImage:         srv.URL + "/sso/v2/qrcode/image",
				QRCheck:         srv.URL + "/sso/v2/qrcode/check",
				QRSsoLogin:      srv.URL + "/sso/v2/login",
			},
			MinInterval:    -1,
			JitterMax:      -1,
			AllowLocalhost: true,
		},
	})
	if err != nil {
		t.Fatalf("service.New: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })

	var (
		evMu          sync.Mutex
		weiboAcctEvts []string
	)
	unsub := b.SubscribeEvents(func(ev Event) {
		if ev.Type == EventAccountChanged && ev.Site == string(site.Weibo) {
			evMu.Lock()
			weiboAcctEvts = append(weiboAcctEvts, ev.Message)
			evMu.Unlock()
		}
	})
	defer unsub()

	ctx := context.Background()
	sess, err := b.StartWeiboQRLogin(ctx)
	if err != nil {
		t.Fatalf("StartWeiboQRLogin: %v", err)
	}
	if sess.FlowID == "" || len(sess.PNG) == 0 {
		t.Fatalf("unexpected QR session DTO: flow=%q pngLen=%d", sess.FlowID, len(sess.PNG))
	}

	// Cancel test first: cancel flow -> late poll returns canceled and does NOT log in.
	b.CancelWeiboQRLogin(sess.FlowID)
	canceledRes, err := b.PollWeiboQRLogin(ctx, sess.FlowID)
	if err != nil || canceledRes.State != "canceled" || b.StatusOf(string(site.Weibo)).LoggedIn {
		t.Fatalf("canceled poll = (%+v, %v), loggedIn=%v", canceledRes, err, b.StatusOf(string(site.Weibo)).LoggedIn)
	}

	// Start fresh session and drive waiting -> scanned -> confirmed.
	pollStep.Store(0)
	sess2, err := b.StartWeiboQRLogin(ctx)
	if err != nil {
		t.Fatalf("StartWeiboQRLogin(2): %v", err)
	}
	r1, err := b.PollWeiboQRLogin(ctx, sess2.FlowID)
	if err != nil || r1.State != "waiting" {
		t.Fatalf("Poll 1 = (%+v, %v), want waiting", r1, err)
	}
	r2, err := b.PollWeiboQRLogin(ctx, sess2.FlowID)
	if err != nil || r2.State != "scanned" {
		t.Fatalf("Poll 2 = (%+v, %v), want scanned", r2, err)
	}
	r3, err := b.PollWeiboQRLogin(ctx, sess2.FlowID)
	if err != nil || r3.State != "confirmed" {
		t.Fatalf("Poll 3 = (%+v, %v), want confirmed", r3, err)
	}

	st := b.StatusOf(string(site.Weibo))
	if !st.LoggedIn || st.Source != "qr" || st.HasRefreshToken || st.UID != synthWeiboSelfUID || st.Uname != synthWeiboSelfName {
		t.Fatalf("unexpected StatusOf(weibo): %+v", st)
	}
	ren := b.Renewal(string(site.Weibo))
	if ren.Available || !strings.Contains(ren.Reason, "扫码") {
		t.Fatalf("unexpected Renewal(weibo): %+v", ren)
	}

	evMu.Lock()
	evCount := len(weiboAcctEvts)
	evMu.Unlock()
	if evCount == 0 {
		t.Fatal("expected EventAccountChanged emitted for weibo after QR confirmation")
	}

	// Ensure NO QR/cookie secrets appear in run logs.
	logs, err := b.RecentLogs(200)
	if err != nil {
		t.Fatalf("RecentLogs: %v", err)
	}
	joined := strings.Join(logs, "\n")
	for _, secret := range []string{qridSecret, altSecret, ticketSecret, subSecret, subpSecret, xsrfSecret} {
		if strings.Contains(joined, secret) {
			t.Fatalf("RecentLogs leaked secret %q", secret)
		}
	}
}
