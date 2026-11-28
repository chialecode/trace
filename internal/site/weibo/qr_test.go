package weibo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/site"
)

func makeSyntheticQRPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if (x/4+y/4)%2 == 0 {
				img.Set(x, y, color.Black)
			} else {
				img.Set(x, y, color.White)
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode synthetic qr png: %v", err)
	}
	return buf.Bytes()
}

func makeSyntheticQRJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: 30, G: 30, B: 30, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatalf("encode synthetic qr jpeg: %v", err)
	}
	return buf.Bytes()
}

func newFastQRTestClient(t *testing.T, baseURL string, store *credential.Store, nowFn func() time.Time) *Client {
	t.Helper()
	return NewClient(ClientOptions{
		CredStore: store,
		Endpoints: Endpoints{
			PCBasicInfo:     baseURL + "/ajax/setting/getBasicInfo",
			ProfileInfo:     baseURL + "/ajax/profile/info",
			StatusesShow:    baseURL + "/ajax/statuses/show",
			StatusesLong:    baseURL + "/ajax/statuses/longtext",
			StatusesMyMblog: baseURL + "/ajax/statuses/mymblog",
			TVComponent:     baseURL + "/tv/api/component",
			Friends:         baseURL + "/ajax/friendships/friends",
			FollowContent:   baseURL + "/ajax/profile/followContent",
			AllGroups:       baseURL + "/ajax/feed/allGroups",
			AllFavorites:    baseURL + "/ajax/favorites/all_fav",
			LikeList:        baseURL + "/ajax/statuses/likelist",
			QRImage:         baseURL + "/sso/v2/qrcode/image",
			QRCheck:         baseURL + "/sso/v2/qrcode/check",
			QRSsoLogin:      baseURL + "/sso/v2/login",
		},
		MinInterval:    -1,
		JitterMax:      -1,
		AllowLocalhost: true,
		Now:            nowFn,
	})
}

func TestWeiboQREndToEndFlowCrossDomainAllowlistAndVerification(t *testing.T) {
	qrPNG := makeSyntheticQRPNG(t, 64, 64)
	const (
		synthQRID = "SYNTH_QRID_0123456789"
		synthAlt  = "ALT-SYNTH-9988776655"
		synthSUB  = "_2A25synthQRSubTokenValid=="
		synthSUBP = "0033WrSXqPxfM725synthQRSubp"
		synthALF  = "1800000001"
		synthSCF  = "synthQRScfToken=="
		synthWBP  = "synthQRWbpSess=="
		synthSSO  = "1760000123"
		synthSUHB = "0synthQRSuhb"
		synthPC   = "synthQRPcToken"
		synthXSRF = "synthQRXsrfFromBasicInfo"
	)

	var (
		mu              sync.Mutex
		pollStage       int
		imageReqs       int
		wbssoHits       int
		sinaCrossHits   int
		evilHits        atomic.Int32
		basicInfoCookie string
	)

	evilSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		evilHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer evilSrv.Close()

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sso/v2/qrcode/image":
			if r.Header.Get("Cookie") != "" {
				t.Errorf("qrcode/image must not send cookies, got %q", r.Header.Get("Cookie"))
			}
			if got := r.URL.Query().Get("entry"); got != "miniblog" {
				t.Errorf("qrcode/image entry = %q, want miniblog", got)
			}
			if got := r.URL.Query().Get("size"); got != "180" {
				t.Errorf("qrcode/image size = %q, want 180", got)
			}
			cb := r.URL.Query().Get("callback")
			if !strings.HasPrefix(cb, "STK_") {
				t.Errorf("unexpected callback %q", cb)
			}
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
			payload, _ := json.Marshal(map[string]any{
				"retcode": 20000000,
				"msg":     "succ",
				"data": map[string]any{
					"qrid":  synthQRID,
					"image": srv.URL + "/inf/gen?api_key=synth_key_1",
				},
			})
			_, _ = fmt.Fprintf(w, "window.%s && %s(%s);", cb, cb, payload)

		case "/inf/gen":
			if r.Header.Get("Cookie") != "" {
				t.Errorf("qr image fetch must not send cookies, got %q", r.Header.Get("Cookie"))
			}
			mu.Lock()
			imageReqs++
			mu.Unlock()
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(qrPNG)

		case "/sso/v2/qrcode/check":
			if r.Header.Get("Cookie") != "" {
				t.Errorf("qrcode/check must not send cookies, got %q", r.Header.Get("Cookie"))
			}
			if got := r.URL.Query().Get("qrid"); got != synthQRID {
				t.Errorf("qrcode/check qrid = %q, want %q", got, synthQRID)
			}
			cb := r.URL.Query().Get("callback")
			mu.Lock()
			stage := pollStage
			pollStage++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/javascript")
			switch stage {
			case 0:
				_, _ = fmt.Fprintf(w, `%s({"retcode":50114001,"msg":"unused","data":null});`, cb)
			case 1:
				_, _ = fmt.Fprintf(w, `%s({"retcode":50114002,"msg":"scanned","data":null});`, cb)
			default:
				_, _ = fmt.Fprintf(w, `%s({"retcode":20000000,"msg":"succ","data":{"alt":%q}});`, cb, synthAlt)
			}

		case "/sso/v2/login":
			if r.Header.Get("Cookie") != "" {
				t.Errorf("sso/v2/login must not send existing stored cookies")
			}
			q := r.URL.Query()
			if q.Get("entry") != "miniblog" || q.Get("returntype") != "TEXT" ||
				q.Get("crossdomain") != "1" || q.Get("cdult") != "3" ||
				q.Get("domain") != "weibo.com" || q.Get("alt") != synthAlt || q.Get("savestate") != "30" {
				t.Errorf("unexpected sso/v2/login query: %s", q.Encode())
			}
			cb := q.Get("callback")
			respObj := map[string]any{
				"retcode": "0",
				"uid":     "1000000001",
				"nick":    "合成示例用户",
				"crossDomainUrlList": []string{
					// Must be skipped: non-allowlisted / insecure / userinfo / non-443 URLs
					"https://evil.example.com/steal?ticket=bad",
					"https://passport.97973.com/sso/crossdomain?action=login",
					"http://passport.weibo.com/wbsso/login?ticket=insecure",
					"https://user:pass@passport.weibo.com/wbsso/login?ticket=userinfo",
					"https://passport.weibo.com:8443/wbsso/login?ticket=badport",
					// Valid allowlisted loopback cross-domain steps
					srv.URL + "/wbsso/login?ticket=SYNTH_TICKET_1&ssosavestate=1800000001",
					srv.URL + "/sso/crossdomain?action=login&domain=sina",
				},
			}
			raw, _ := json.Marshal(respObj)
			w.Header().Set("Content-Type", "application/javascript")
			_, _ = fmt.Fprintf(w, "%s(%s);", cb, raw)

		case "/wbsso/login":
			mu.Lock()
			wbssoHits++
			mu.Unlock()
			http.SetCookie(w, &http.Cookie{Name: "SUB", Value: synthSUB, Domain: ".weibo.com"})
			http.SetCookie(w, &http.Cookie{Name: "SUBP", Value: synthSUBP, Domain: ".weibo.com"})
			http.SetCookie(w, &http.Cookie{Name: "ALF", Value: synthALF, Domain: ".weibo.com"})
			http.SetCookie(w, &http.Cookie{Name: "SCF", Value: synthSCF, Domain: ".weibo.com"})
			http.SetCookie(w, &http.Cookie{Name: "WBPSESS", Value: synthWBP, Domain: ".weibo.com"})
			http.SetCookie(w, &http.Cookie{Name: "SSOLoginState", Value: synthSSO, Domain: ".weibo.com"})
			http.SetCookie(w, &http.Cookie{Name: "SUHB", Value: synthSUHB, Domain: ".weibo.com"})
			http.SetCookie(w, &http.Cookie{Name: "PC_TOKEN", Value: synthPC, Domain: ".weibo.com"})
			// Stat cookie must be stripped; deletion cookie must not clobber SUB.
			http.SetCookie(w, &http.Cookie{Name: "SINAGLOBAL", Value: "drop_stat_123", Domain: ".weibo.com"})
			http.SetCookie(w, &http.Cookie{Name: "SUB", Value: "deleted", MaxAge: -1, Domain: ".weibo.com"})
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result":true}`))

		case "/sso/crossdomain":
			mu.Lock()
			sinaCrossHits++
			mu.Unlock()
			// Non-.weibo.com Domain cookies MUST NOT overwrite .weibo.com SUB/SUBP.
			http.SetCookie(w, &http.Cookie{Name: "SUB", Value: "wrong_sina_sub_must_ignore", Domain: ".sina.com.cn"})
			http.SetCookie(w, &http.Cookie{Name: "SUB", Value: "wrong_cn_sub_must_ignore", Domain: ".weibo.cn"})
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result":true}`))

		case "/ajax/setting/getBasicInfo":
			mu.Lock()
			basicInfoCookie = r.Header.Get("Cookie")
			mu.Unlock()
			http.SetCookie(w, &http.Cookie{Name: "XSRF-TOKEN", Value: synthXSRF})
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(syntheticBasicInfoFixture))

		case "/ajax/profile/info":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"ok": 1,
				"data": {
					"user": {
						"id": 1000000001,
						"idstr": "1000000001",
						"screen_name": "合成示例用户",
						"avatar_hd": "https://tvax1.sinaimg.cn/crop.0.0.180.180/synthetic_qr_hd.jpg",
						"domain": "synthetic_creator"
					}
				}
			}`))

		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	store := newTestWeiboStore(t, "")
	client := newFastQRTestClient(t, srv.URL, store, time.Now)

	sess, err := client.StartQR(context.Background())
	if err != nil {
		t.Fatalf("StartQR failed: %v", err)
	}
	if sess.FlowID == "" || !strings.HasPrefix(sess.FlowID, "wbqr-") {
		t.Fatalf("unexpected flowID: %q", sess.FlowID)
	}
	if len(sess.PNG) == 0 {
		t.Fatal("expected non-empty PNG bytes")
	}
	if ext, ok := site.SniffImageMagicBytes(sess.PNG); !ok || ext != ".png" {
		t.Fatalf("StartQR PNG magic check failed: ext=%q ok=%v", ext, ok)
	}
	if strings.Contains(sess.FlowID, synthQRID) {
		t.Fatal("FlowID must not embed qrid")
	}

	// Poll 1: waiting (50114001)
	res1, err := client.PollQR(context.Background(), sess.FlowID)
	if err != nil || res1.State != "waiting" {
		t.Fatalf("Poll 1 = (%+v, %v), want waiting", res1, err)
	}

	// Poll 2: scanned (50114002)
	res2, err := client.PollQR(context.Background(), sess.FlowID)
	if err != nil || res2.State != "scanned" {
		t.Fatalf("Poll 2 = (%+v, %v), want scanned", res2, err)
	}

	// Poll 3: confirmed (20000000) -> sso/v2/login -> crossDomainUrlList -> getBasicInfo -> profile/info -> saved!
	res3, err := client.PollQR(context.Background(), sess.FlowID)
	if err != nil {
		t.Fatalf("Poll 3 failed: %v", err)
	}
	if res3.State != "confirmed" || !res3.Account.LoggedIn || res3.Account.Source != "qr" {
		t.Fatalf("Poll 3 unexpected result: %+v", res3)
	}
	if res3.Account.UID != "1000000001" || res3.Account.Uname != "合成示例用户" || res3.Account.HasRefreshToken {
		t.Fatalf("unexpected saved account status: %+v", res3.Account)
	}

	// Verify cookies persisted accurately and stat/.sina.com.cn cookies excluded.
	saved := store.Cookies()
	wantCookies := map[string]string{
		"SUB":           synthSUB,
		"SUBP":          synthSUBP,
		"ALF":           synthALF,
		"SCF":           synthSCF,
		"WBPSESS":       synthWBP,
		"SSOLoginState": synthSSO,
		"SUHB":          synthSUHB,
		"PC_TOKEN":      synthPC,
		"XSRF-TOKEN":    synthXSRF,
	}
	for k, want := range wantCookies {
		if saved[k] != want {
			t.Fatalf("saved cookie %s = %q, want %q", k, saved[k], want)
		}
	}
	if _, leaked := saved["SINAGLOBAL"]; leaked {
		t.Fatal("SINAGLOBAL stat cookie must not be saved")
	}
	if !strings.Contains(basicInfoCookie, "SUB="+synthSUB) {
		t.Fatalf("getBasicInfo did not receive collected SUB cookie: %q", basicInfoCookie)
	}
	if evilHits.Load() != 0 {
		t.Fatalf("evil server received %d requests, want 0", evilHits.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	if imageReqs != 1 || wbssoHits != 1 || sinaCrossHits != 1 {
		t.Fatalf("unexpected hit counts: image=%d wbsso=%d sinaCross=%d", imageReqs, wbssoHits, sinaCrossHits)
	}
}

func TestWeiboQRImageRenderingModesAndGuards(t *testing.T) {
	t.Run("jpeg_reencoded_to_png", func(t *testing.T) {
		jpgBytes := makeSyntheticQRJPEG(t, 48, 48)
		var srv *httptest.Server
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/sso/v2/qrcode/image":
				cb := r.URL.Query().Get("callback")
				_, _ = fmt.Fprintf(w, `%s({"retcode":20000000,"data":{"qrid":"qr_jpg_1","image":%q}})`, cb, srv.URL+"/qr.jpg")
			case "/qr.jpg":
				w.Header().Set("Content-Type", "image/jpeg")
				_, _ = w.Write(jpgBytes)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer srv.Close()

		c := newFastQRTestClient(t, srv.URL, newTestWeiboStore(t, ""), time.Now)
		sess, err := c.StartQR(context.Background())
		if err != nil {
			t.Fatalf("StartQR JPEG failed: %v", err)
		}
		if ext, ok := site.SniffImageMagicBytes(sess.PNG); !ok || ext != ".png" {
			t.Fatalf("expected JPEG QR image re-encoded to PNG, got ext=%q ok=%v", ext, ok)
		}
	})

	t.Run("raw_scan_uri_encoded_with_go_qrcode", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cb := r.URL.Query().Get("callback")
			_, _ = fmt.Fprintf(w, `%s({"retcode":20000000,"data":{"qrid":"qr_uri_1","url":"https://passport.weibo.cn/signin/qrcode/scan?qrid=synth"}})`, cb)
		}))
		defer srv.Close()

		c := newFastQRTestClient(t, srv.URL, newTestWeiboStore(t, ""), time.Now)
		sess, err := c.StartQR(context.Background())
		if err != nil {
			t.Fatalf("StartQR raw scan URI failed: %v", err)
		}
		if ext, ok := site.SniffImageMagicBytes(sess.PNG); !ok || ext != ".png" {
			t.Fatalf("expected locally rendered PNG, got ext=%q ok=%v", ext, ok)
		}
	})

	t.Run("oversized_and_html_qr_images_rejected", func(t *testing.T) {
		cases := []struct {
			name     string
			body     []byte
			ct       string
			wantKind string
		}{
			{"oversized_257k", bytes.Repeat([]byte("A"), maxQRImageBytes+1024), "image/png", site.ErrKindBlocked},
			{"html_disguised", []byte("<html><body>not a png</body></html>"), "text/html", site.ErrKindSchemaChanged},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				var srv *httptest.Server
				srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/sso/v2/qrcode/image" {
						cb := r.URL.Query().Get("callback")
						_, _ = fmt.Fprintf(w, `%s({"retcode":20000000,"data":{"qrid":"qr_bad_1","image":%q}})`, cb, srv.URL+"/bad-img")
						return
					}
					w.Header().Set("Content-Type", tc.ct)
					_, _ = w.Write(tc.body)
				}))
				defer srv.Close()

				c := newFastQRTestClient(t, srv.URL, newTestWeiboStore(t, ""), time.Now)
				_, err := c.StartQR(context.Background())
				if !site.IsKind(err, tc.wantKind) {
					t.Fatalf("%s: err = %v, want %s", tc.name, err, tc.wantKind)
				}
			})
		}
	})
}

func TestWeiboQRPollStatesErrorsRiskControlAndLocalExpiry(t *testing.T) {
	qrPNG := makeSyntheticQRPNG(t, 32, 32)
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	nowFn := func() time.Time { return now }

	t.Run("expired_50114004_and_canceled_50114003_50114015_and_unknown", func(t *testing.T) {
		cases := []struct {
			name      string
			retcode   string
			wantState string
			wantKind  string
		}{
			{"expired_50114004", "50114004", "expired", ""},
			{"canceled_50114003", "50114003", "canceled", ""},
			{"canceled_50114015", "50114015", "canceled", ""},
			{"unknown_retcode", "50114999", "", site.ErrKindSchemaChanged},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				var srv *httptest.Server
				srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					cb := r.URL.Query().Get("callback")
					switch r.URL.Path {
					case "/sso/v2/qrcode/image":
						_, _ = fmt.Fprintf(w, `%s({"retcode":20000000,"data":{"qrid":"qr_state_1","image":%q}})`, cb, srv.URL+"/qr.png")
					case "/qr.png":
						_, _ = w.Write(qrPNG)
					case "/sso/v2/qrcode/check":
						_, _ = fmt.Fprintf(w, `%s({"retcode":%s,"msg":"state"})`, cb, tc.retcode)
					}
				}))
				defer srv.Close()

				c := newFastQRTestClient(t, srv.URL, newTestWeiboStore(t, ""), nowFn)
				sess, err := c.StartQR(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				res, err := c.PollQR(context.Background(), sess.FlowID)
				if tc.wantKind != "" {
					if !site.IsKind(err, tc.wantKind) {
						t.Fatalf("err = %v, want %s", err, tc.wantKind)
					}
					if strings.Contains(err.Error(), "qr_state_1") {
						t.Fatalf("error leaked qrid: %v", err)
					}
					return
				}
				if err != nil || res.State != tc.wantState {
					t.Fatalf("PollQR = (%+v, %v), want state %q", res, err, tc.wantState)
				}
			})
		}
	})

	t.Run("local_ttl_expiry_zero_poll_requests", func(t *testing.T) {
		var checkHits atomic.Int32
		var srv *httptest.Server
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cb := r.URL.Query().Get("callback")
			switch r.URL.Path {
			case "/sso/v2/qrcode/image":
				_, _ = fmt.Fprintf(w, `%s({"retcode":20000000,"data":{"qrid":"qr_ttl_1","image":%q}})`, cb, srv.URL+"/qr.png")
			case "/qr.png":
				_, _ = w.Write(qrPNG)
			case "/sso/v2/qrcode/check":
				checkHits.Add(1)
				_, _ = fmt.Fprintf(w, `%s({"retcode":50114001})`, cb)
			}
		}))
		defer srv.Close()

		cur := now
		c := newFastQRTestClient(t, srv.URL, newTestWeiboStore(t, ""), func() time.Time { return cur })
		sess, err := c.StartQR(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		cur = cur.Add(181 * time.Second)
		res, err := c.PollQR(context.Background(), sess.FlowID)
		if err != nil || res.State != "expired" {
			t.Fatalf("PollQR after TTL = (%+v, %v), want expired", res, err)
		}
		if checkHits.Load() != 0 {
			t.Fatalf("expired QR issued %d poll requests, want 0", checkHits.Load())
		}
	})

	t.Run("risk_control_http_403_418_432_and_jsonp_mismatch", func(t *testing.T) {
		cases := []struct {
			name     string
			status   int
			body     string
			wantKind string
		}{
			{"http_403", http.StatusForbidden, "forbidden", site.ErrKindRiskControl},
			{"http_418", http.StatusTeapot, "teapot", site.ErrKindRiskControl},
			{"http_432", 432, "risk", site.ErrKindRiskControl},
			{"http_429", http.StatusTooManyRequests, "rate limit", site.ErrKindRateLimited},
			{"html_geetest", http.StatusOK, "<html><body>geetest 滑块验证码</body></html>", site.ErrKindRiskControl},
			{"wrong_jsonp_callback", http.StatusOK, `WRONG_CB({"retcode":50114001})`, site.ErrKindSchemaChanged},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				var srv *httptest.Server
				srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					cb := r.URL.Query().Get("callback")
					switch r.URL.Path {
					case "/sso/v2/qrcode/image":
						_, _ = fmt.Fprintf(w, `%s({"retcode":20000000,"data":{"qrid":"qr_rc_1","image":%q}})`, cb, srv.URL+"/qr.png")
					case "/qr.png":
						_, _ = w.Write(qrPNG)
					case "/sso/v2/qrcode/check":
						w.WriteHeader(tc.status)
						_, _ = w.Write([]byte(tc.body))
					}
				}))
				defer srv.Close()

				store := newTestWeiboStore(t, "SUB=EXISTING_KEEP_SUB_001")
				c := newFastQRTestClient(t, srv.URL, store, time.Now)
				sess, err := c.StartQR(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				_, err = c.PollQR(context.Background(), sess.FlowID)
				if !site.IsKind(err, tc.wantKind) {
					t.Fatalf("%s: err = %v, want %s", tc.name, err, tc.wantKind)
				}
				if store.CookieValue("SUB") != "EXISTING_KEEP_SUB_001" || !store.Status().LoggedIn {
					t.Fatalf("existing credential was modified on %s", tc.name)
				}
			})
		}
	})
}

func TestWeiboQRCancelSupersedeAndVerificationFailureNeverOverwriteCred(t *testing.T) {
	qrPNG := makeSyntheticQRPNG(t, 32, 32)

	t.Run("canceled_mid_flight_never_writes_cred", func(t *testing.T) {
		gate := make(chan struct{})
		entered := make(chan struct{}, 1)
		t.Cleanup(func() {
			select {
			case <-gate:
			default:
				close(gate)
			}
		})

		var srv *httptest.Server
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cb := r.URL.Query().Get("callback")
			switch r.URL.Path {
			case "/sso/v2/qrcode/image":
				_, _ = fmt.Fprintf(w, `%s({"retcode":20000000,"data":{"qrid":"qr_cancel_1","image":%q}})`, cb, srv.URL+"/qr.png")
			case "/qr.png":
				_, _ = w.Write(qrPNG)
			case "/sso/v2/qrcode/check":
				select {
				case entered <- struct{}{}:
				default:
				}
				select {
				case <-gate:
				case <-r.Context().Done():
					return
				}
				_, _ = fmt.Fprintf(w, `%s({"retcode":20000000,"data":{"alt":"SYNTH_ALT_CANCELED"}})`, cb)
			case "/sso/v2/login":
				t.Error("canceled QR flow must not call sso/v2/login")
				w.WriteHeader(http.StatusInternalServerError)
			}
		}))
		defer srv.Close()

		store := newTestWeiboStore(t, "SUB=EXISTING_KEEP_SUB_002")
		c := newFastQRTestClient(t, srv.URL, store, time.Now)
		sess, err := c.StartQR(context.Background())
		if err != nil {
			t.Fatal(err)
		}

		resCh := make(chan QRPollResult, 1)
		errCh := make(chan error, 1)
		go func() {
			res, pErr := c.PollQR(context.Background(), sess.FlowID)
			resCh <- res
			errCh <- pErr
		}()

		<-entered
		c.CancelQR(sess.FlowID)
		close(gate)

		res := <-resCh
		if pErr := <-errCh; pErr != nil || res.State != "canceled" {
			t.Fatalf("late canceled poll = (%+v, %v), want canceled", res, pErr)
		}
		if store.CookieValue("SUB") != "EXISTING_KEEP_SUB_002" || store.Status().Source != "manual" {
			t.Fatalf("canceled QR overwrote stored credential: %+v", store.Status())
		}
	})

	t.Run("superseded_flow_and_failed_verification_preserve_cred", func(t *testing.T) {
		var srv *httptest.Server
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cb := r.URL.Query().Get("callback")
			switch r.URL.Path {
			case "/sso/v2/qrcode/image":
				_, _ = fmt.Fprintf(w, `%s({"retcode":20000000,"data":{"qrid":"qr_sup_1","image":%q}})`, cb, srv.URL+"/qr.png")
			case "/qr.png":
				_, _ = w.Write(qrPNG)
			case "/sso/v2/qrcode/check":
				_, _ = fmt.Fprintf(w, `%s({"retcode":20000000,"data":{"alt":"SYNTH_ALT_VERIFY_FAIL"}})`, cb)
			case "/sso/v2/login":
				_, _ = fmt.Fprintf(w, `%s({"retcode":"0","uid":"1000000001","arrURL":[%q]})`, cb, srv.URL+"/wbsso/login")
			case "/wbsso/login":
				http.SetCookie(w, &http.Cookie{Name: "SUB", Value: "SYNTH_GUEST_SUB_BAD", Domain: ".weibo.com"})
				w.WriteHeader(http.StatusOK)
			case "/ajax/setting/getBasicInfo":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"ok":-100,"url":"https://passport.weibo.com/sso/login"}`))
			}
		}))
		defer srv.Close()

		store := newTestWeiboStore(t, "SUB=EXISTING_KEEP_SUB_003")
		c := newFastQRTestClient(t, srv.URL, store, time.Now)

		sess1, err := c.StartQR(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		sess2, err := c.StartQR(context.Background())
		if err != nil {
			t.Fatal(err)
		}

		// Polling superseded sess1 returns canceled without touching credentials.
		oldRes, err := c.PollQR(context.Background(), sess1.FlowID)
		if err != nil || oldRes.State != "canceled" {
			t.Fatalf("superseded PollQR = (%+v, %v), want canceled", oldRes, err)
		}

		// Polling active sess2 fails strict getBasicInfo verification -> preserves existing cred.
		_, err = c.PollQR(context.Background(), sess2.FlowID)
		if !site.IsKind(err, site.ErrKindAuthRequired) {
			t.Fatalf("unverified QR poll err = %v, want auth_required", err)
		}
		if store.CookieValue("SUB") != "EXISTING_KEEP_SUB_003" || store.Status().Source != "manual" {
			t.Fatalf("failed QR verification altered stored credential: %+v", store.Status())
		}

		// LogoutCredential cancels active QR flow.
		if err := c.LogoutCredential(); err != nil {
			t.Fatalf("LogoutCredential failed: %v", err)
		}
		afterLogout, err := c.PollQR(context.Background(), sess2.FlowID)
		if err != nil || afterLogout.State != "canceled" {
			t.Fatalf("PollQR after logout = (%+v, %v), want canceled", afterLogout, err)
		}
	})
}

func TestValidateWeiboSSOCrossDomainAndQRImageURLAllowlist(t *testing.T) {
	allowedSSO := []string{
		"https://passport.weibo.com/wbsso/login?ticket=t1",
		"https://passport.weibo.com:443/sso/v2/login?entry=miniblog",
		"https://login.sina.com.cn/sso/crossdomain?action=login",
		"https://passport.weibo.cn/sso/crossdomain?action=login",
		"https://weibo.com/ajax/setting/getBasicInfo",
		"https://www.weibo.com/",
	}
	for _, raw := range allowedSSO {
		if _, err := validateSSOCrossDomainURL(raw, false); err != nil {
			t.Fatalf("validateSSOCrossDomainURL(%q) unexpected error: %v", raw, err)
		}
	}

	rejectedSSO := []string{
		"",
		"http://passport.weibo.com/wbsso/login",
		"https://passport.weibo.com:8443/wbsso/login",
		"https://user:pass@passport.weibo.com/wbsso/login",
		"https://passport.97973.com/sso/crossdomain",
		"https://passport.krcom.cn/sso/crossdomain",
		"https://evil.example.com/wbsso/login",
		"https://weibo.com.evil.com/wbsso/login",
		"https://127.0.0.1/wbsso/login",
		"javascript:alert(1)",
	}
	for _, raw := range rejectedSSO {
		if u, err := validateSSOCrossDomainURL(raw, false); err == nil {
			t.Fatalf("validateSSOCrossDomainURL(%q) unexpectedly accepted %v", raw, u)
		}
	}

	allowedQRImg := []string{
		"https://v2.qr.weibo.cn/inf/gen?api_key=synth",
		"//v2.qr.weibo.cn/inf/gen?api_key=synth",
		"https://qr.weibo.cn/inf/gen?api_key=synth",
		"https://passport.weibo.com/sso/v2/qrcode/img.png",
		"https://sub.weibo.com/qr.png",
	}
	for _, raw := range allowedQRImg {
		u, err := validateQRImageURL(raw, false)
		if err != nil {
			t.Fatalf("validateQRImageURL(%q) unexpected error: %v", raw, err)
		}
		if u.Scheme != "https" {
			t.Fatalf("validateQRImageURL(%q) scheme = %q, want https", raw, u.Scheme)
		}
	}

	rejectedQRImg := []string{
		"http://v2.qr.weibo.cn/inf/gen?api_key=synth",
		"https://v2.qr.weibo.cn:8443/inf/gen",
		"https://user@v2.qr.weibo.cn/inf/gen",
		"https://evil.example.com/qr.png",
		"https://weibo.cn.evil.com/qr.png",
		"https://127.0.0.1/qr.png",
	}
	for _, raw := range rejectedQRImg {
		if u, err := validateQRImageURL(raw, false); err == nil {
			t.Fatalf("validateQRImageURL(%q) unexpectedly accepted %v", raw, u)
		}
	}
}

func TestUnwrapJSONPStrictValidation(t *testing.T) {
	cb := "STK_17600000000001"
	valid := []string{
		`STK_17600000000001({"retcode":20000000})`,
		`STK_17600000000001({"retcode":20000000});`,
		`window.STK_17600000000001 && STK_17600000000001({"retcode":20000000});`,
		`{"retcode":20000000}`,
	}
	for _, raw := range valid {
		got, err := unwrapJSONP([]byte(raw), cb, "https://passport.weibo.com/test")
		if err != nil {
			t.Fatalf("unwrapJSONP(%q) unexpected error: %v", raw, err)
		}
		if string(got) != `{"retcode":20000000}` {
			t.Fatalf("unwrapJSONP(%q) = %q", raw, string(got))
		}
	}

	invalid := []string{
		`STK_99999999999999({"retcode":20000000})`,
		`window.STK_17600000000001 || STK_17600000000001({"retcode":20000000})`,
		`STK_17600000000001(alert(1))`,
		`alert(1)`,
		``,
	}
	for _, raw := range invalid {
		if got, err := unwrapJSONP([]byte(raw), cb, "https://passport.weibo.com/test"); err == nil {
			t.Fatalf("unwrapJSONP(%q) unexpectedly succeeded with %q", raw, string(got))
		}
	}
	// Ensure query params are stripped from endpoint labels and secrets are not echoed.
	u, _ := url.Parse("https://passport.weibo.com/sso/v2/qrcode/check?qrid=SECRET_QRID_XYZ")
	if lbl := sanitizeEndpointLabel(u); strings.Contains(lbl, "SECRET_QRID_XYZ") {
		t.Fatalf("sanitizeEndpointLabel leaked query secret: %q", lbl)
	}
}

func TestWeiboQRWebPImageTranscodingAndGuardBlockedErrorPreservation(t *testing.T) {
	t.Run("webp_qr_image_transcoded_to_png", func(t *testing.T) {
		// Minimal valid 1x1 lossless VP8L WebP image.
		webp1x1 := []byte{
			'R', 'I', 'F', 'F', 0x14, 0x00, 0x00, 0x00,
			'W', 'E', 'B', 'P',
			'V', 'P', '8', 'L', 0x08, 0x00, 0x00, 0x00,
			0x2f, 0x00, 0x00, 0x00, 0x00, 0x88, 0x88, 0x08,
		}
		var srv *httptest.Server
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/sso/v2/qrcode/image":
				cb := r.URL.Query().Get("callback")
				_, _ = fmt.Fprintf(w, `%s({"retcode":20000000,"data":{"qrid":"qr_webp_1","image":%q}})`, cb, srv.URL+"/qr.webp")
			case "/qr.webp":
				w.Header().Set("Content-Type", "image/webp")
				_, _ = w.Write(webp1x1)
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()

		c := newFastQRTestClient(t, srv.URL, nil, time.Now)
		sess, err := c.StartQR(context.Background())
		if err != nil {
			t.Fatalf("StartQR with WebP image failed: %v", err)
		}
		if ext, ok := site.SniffImageMagicBytes(sess.PNG); !ok || ext != ".png" {
			t.Fatalf("expected PNG output after WebP transcoding, got ext=%q ok=%v", ext, ok)
		}
		cfg, format, err := image.DecodeConfig(bytes.NewReader(sess.PNG))
		if err != nil || format != "png" || cfg.Width != 1 || cfg.Height != 1 {
			t.Fatalf("transcoded PNG config = (%+v, %q, %v), want 1x1 png", cfg, format, err)
		}
	})

	t.Run("guarded_dialer_blocked_preserved_without_query_leak", func(t *testing.T) {
		guardErr := &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindBlocked,
			Message: "已拦截微博扫码请求指向内网或回环地址",
		}
		cSSO := NewClient(ClientOptions{
			Endpoints: Endpoints{
				QRImage: "https://passport.weibo.com/sso/v2/qrcode/image",
				QRCheck: "https://passport.weibo.com/sso/v2/qrcode/check",
			},
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return nil, &url.Error{
					Op:  "Get",
					URL: req.URL.String(),
					Err: guardErr,
				}
			}),
		})
		// Seed an active flow with a secret qrid and verify PollQR preserves ErrKindBlocked without leaking qrid.
		cSSO.qr.active = &qrFlowState{FlowID: "wbqr-block-1", QRID: "SECRET_QRID_DIAL_BLOCK", ExpiresAt: time.Now().Add(time.Minute)}
		cSSO.qr.history = map[string]string{"wbqr-block-1": "active"}

		_, err := cSSO.PollQR(context.Background(), "wbqr-block-1")
		if !site.IsKind(err, site.ErrKindBlocked) {
			t.Fatalf("PollQR blocked dialer err = %v, want ErrKindBlocked", err)
		}
		if !strings.Contains(err.Error(), "已拦截微博扫码请求指向内网或回环地址") {
			t.Fatalf("PollQR blocked dialer message lost guard text: %v", err)
		}
		if strings.Contains(err.Error(), "SECRET_QRID_DIAL_BLOCK") {
			t.Fatalf("PollQR blocked dialer error leaked qrid query param: %v", err)
		}

		// Verify fetchQRImagePNG also preserves ErrKindBlocked without leaking query secrets.
		cImg := NewClient(ClientOptions{
			Endpoints: Endpoints{
				QRImage: "https://passport.weibo.com/sso/v2/qrcode/image",
			},
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/sso/v2/qrcode/image" {
					cb := req.URL.Query().Get("callback")
					body := fmt.Sprintf(`%s({"retcode":20000000,"data":{"qrid":"qr_img_block","image":"https://v2.qr.weibo.cn/inf/gen?qrid=SECRET_IMG_TOKEN"}})`, cb)
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"application/javascript"}},
						Body:       io.NopCloser(strings.NewReader(body)),
					}, nil
				}
				return nil, &url.Error{
					Op:  "Get",
					URL: req.URL.String(),
					Err: guardErr,
				}
			}),
		})
		_, err = cImg.StartQR(context.Background())
		if !site.IsKind(err, site.ErrKindBlocked) {
			t.Fatalf("StartQR image fetch blocked dialer err = %v, want ErrKindBlocked", err)
		}
		if strings.Contains(err.Error(), "SECRET_IMG_TOKEN") {
			t.Fatalf("StartQR image fetch blocked error leaked query secret: %v", err)
		}
	})
}
