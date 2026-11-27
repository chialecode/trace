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
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/remote"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/site/douyin"
	"github.com/chialecode/trace/internal/site/weibo"
	"github.com/chialecode/trace/internal/site/x"
	"github.com/chialecode/trace/internal/subscription"
)

const (
	synthXSelfRestID     = "1000000000000000001"
	synthXSelfScreenName = "synth_x_self"
	synthXSelfName       = "Synth X Self"
	synthXCreatorRestID  = "1000000000000000099"
	synthXCreatorHandle  = "synth_creator"
	synthXCreatorName    = "Synth Creator"
	synthXAuthSentinel   = "SYNTH_X_AUTH_TOKEN_SECRET_99112233"
	synthXCT0Sentinel    = "SynthXCT0SecretToken4455667788"
	synthXCT0Rotated     = "SynthXCT0RotatedToken8877665544"
	synthXValidCookie    = "auth_token=" + synthXAuthSentinel + "; ct0=" + synthXCT0Sentinel + "; twid=u%3D" + synthXSelfRestID + "; guest_id=v1%3A90000011; kdt=SynthXKdt01; __cf_bm=DROP_ME_CF_BM; personalization_id=DROP_ME_ADS"
)

type xAbsRewriteTransport struct {
	target *url.URL
	inner  http.RoundTripper
}

func newXAbsRewriteTransport(t *testing.T, ts *httptest.Server) http.RoundTripper {
	t.Helper()
	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("parse x test server url: %v", err)
	}
	return &xAbsRewriteTransport{target: u, inner: ts.Client().Transport}
}

func (rt *xAbsRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = rt.target.Scheme
	clone.URL.Host = rt.target.Host
	clone.Host = rt.target.Host
	return rt.inner.RoundTrip(clone)
}

func readXSiteTestdata(t *testing.T, name string) []byte {
	t.Helper()
	p := filepath.Join("..", "site", "x", "testdata", name)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return b
}

func makeSynthPNGBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for xx := 0; xx < 8; xx++ {
			img.Set(xx, y, color.RGBA{R: 32, G: 128, B: 224, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode synth png: %v", err)
	}
	return buf.Bytes()
}

func makeSynthMP4Bytes(t *testing.T) []byte {
	t.Helper()
	return synthWeiboMP4Bytes(t)
}

func synthXUserCoreJSON(restID, screenName, name, avatar, desc string, statuses int) string {
	return fmt.Sprintf(`{"__typename":"User","rest_id":%q,"is_blue_verified":true,"core":{"name":%q,"screen_name":%q},"avatar":{"image_url":%q},"profile_bio":{"description":%q},"legacy":{"statuses_count":%d,"followers_count":1234,"friends_count":56,"protected":false}}`,
		restID, name, screenName, avatar, desc, statuses)
}

func synthXUserResultEnvelope(restID, screenName, name, avatar, desc string, statuses int) string {
	return `{"data":{"user":{"result":` + synthXUserCoreJSON(restID, screenName, name, avatar, desc, statuses) + `}}}`
}

func synthXTweetDetailEnvelope(entriesJSON string) string {
	return `{"data":{"threaded_conversation_with_injections_v2":{"instructions":[{"type":"TimelineAddEntries","entries":[` + entriesJSON + `]}]}}}`
}

func synthXTweetEntry(entryID, tweetResultJSON string) string {
	return `{"entryId":` + mustJSONQuote(entryID) + `,"content":{"entryType":"TimelineTimelineItem","itemContent":{"itemType":"TimelineTweet","tweetDisplayType":"Tweet","tweet_results":{"result":` + tweetResultJSON + `}}}}`
}

func synthXCursorEntry(entryID, cursorType, value string) string {
	return fmt.Sprintf(`{"entryId":%q,"content":{"entryType":"TimelineTimelineCursor","cursorType":%q,"value":%q}}`, entryID, cursorType, value)
}

func synthXUserTimelineEnvelope(entriesJSON string) string {
	return `{"data":{"user":{"result":{"__typename":"User","timeline_v2":{"timeline":{"instructions":[{"type":"TimelineAddEntries","entries":[` + entriesJSON + `]}]}}}}}}`
}

func synthXFollowingEnvelope(entriesJSON string) string {
	return `{"data":{"user":{"result":{"__typename":"User","timeline":{"timeline":{"instructions":[{"type":"TimelineAddEntries","entries":[` + entriesJSON + `]}]}}}}}}`
}

func synthXBookmarksEnvelope(entriesJSON string) string {
	return `{"data":{"bookmark_timeline_v2":{"timeline":{"instructions":[{"type":"TimelineAddEntries","entries":[` + entriesJSON + `]}]}}}}`
}

func synthXUserEntry(entryID, userJSON string) string {
	return `{"entryId":` + mustJSONQuote(entryID) + `,"content":{"entryType":"TimelineTimelineItem","itemContent":{"itemType":"TimelineUser","user_results":{"result":` + userJSON + `}}}}`
}

func mustJSONQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

type xServiceTestHarness struct {
	t        *testing.T
	loc      appdirs.Locations
	backend  *Backend
	desktop  *MockDesktop
	xSrv     *httptest.Server
	biliSrv  *httptest.Server
	dySrv    *httptest.Server
	wbSrv    *httptest.Server
	pngBytes []byte
	mp4Bytes []byte

	xReqs    atomic.Int32
	biliReqs atomic.Int32
	dyReqs   atomic.Int32
	wbReqs   atomic.Int32

	mu           sync.Mutex
	xHandler     func(w http.ResponseWriter, r *http.Request) bool
	rotateCT0    bool
	lastMediaHdr http.Header
	hlsHits      atomic.Int32
}

func newXServiceTestHarness(t *testing.T, optsFn func(*Options)) *xServiceTestHarness {
	t.Helper()
	h := &xServiceTestHarness{
		t:        t,
		loc:      testLocations(t),
		desktop:  NewMockDesktop(),
		pngBytes: makeSynthPNGBytes(t),
		mp4Bytes: makeSynthMP4Bytes(t),
	}

	homeBytes := readXSiteTestdata(t, "webpack_home.html")
	mainBytes := readXSiteTestdata(t, "webpack_main.js")
	ondemandBytes := readXSiteTestdata(t, "webpack_ondemand.js")

	h.xSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/home":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(homeBytes)
			return
		case "/responsive-web/client-web/main.synth01a.js":
			w.Header().Set("Content-Type", "application/javascript")
			_, _ = w.Write(mainBytes)
			return
		case "/responsive-web/client-web/ondemand.s.synth02a.js":
			w.Header().Set("Content-Type", "application/javascript")
			_, _ = w.Write(ondemandBytes)
			return
		}
		if strings.HasSuffix(strings.ToLower(r.URL.Path), ".m3u8") {
			h.hlsHits.Add(1)
			w.WriteHeader(http.StatusGone)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/xcdn/") {
			h.mu.Lock()
			h.lastMediaHdr = r.Header.Clone()
			fn := h.xHandler
			h.mu.Unlock()
			if fn != nil && fn(w, r) {
				return
			}
			if strings.HasSuffix(r.URL.Path, ".mp4") {
				w.Header().Set("Content-Type", "video/mp4")
				http.ServeContent(w, r, "video.mp4", time.Time{}, bytes.NewReader(h.mp4Bytes))
				return
			}
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(h.pngBytes)
			return
		}
		h.xReqs.Add(1)
		h.mu.Lock()
		fn := h.xHandler
		rotate := h.rotateCT0
		h.mu.Unlock()
		if rotate {
			http.SetCookie(w, &http.Cookie{Name: "ct0", Value: synthXCT0Rotated, Path: "/"})
		}
		if fn != nil && fn(w, r) {
			return
		}
		switch {
		case r.URL.Path == "/i/api/1.1/account/settings.json":
			cookie := r.Header.Get("Cookie")
			if !strings.Contains(cookie, "auth_token="+synthXAuthSentinel) {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"errors":[{"code":32,"message":"Could not authenticate you."}]}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"screen_name":"` + synthXSelfScreenName + `","protected":false,"language":"en"}`))
		case strings.HasSuffix(r.URL.Path, "/"+x.OpUserByScreenName):
			vars := r.URL.Query().Get("variables")
			w.Header().Set("Content-Type", "application/json")
			if strings.Contains(vars, synthXCreatorHandle) {
				_, _ = w.Write([]byte(synthXUserResultEnvelope(
					synthXCreatorRestID, synthXCreatorHandle, synthXCreatorName,
					h.xSrv.URL+"/xcdn/creator_avatar.png", "Synthetic creator bio", 128,
				)))
				return
			}
			_, _ = w.Write([]byte(synthXUserResultEnvelope(
				synthXSelfRestID, synthXSelfScreenName, synthXSelfName,
				h.xSrv.URL+"/xcdn/self_avatar.png", "Synthetic self bio", 12,
			)))
		case strings.HasSuffix(r.URL.Path, "/"+x.OpUserByRestId):
			vars := r.URL.Query().Get("variables")
			w.Header().Set("Content-Type", "application/json")
			if strings.Contains(vars, synthXSelfRestID) {
				_, _ = w.Write([]byte(synthXUserResultEnvelope(
					synthXSelfRestID, synthXSelfScreenName, synthXSelfName,
					h.xSrv.URL+"/xcdn/self_avatar.png", "Synthetic self bio", 12,
				)))
				return
			}
			_, _ = w.Write([]byte(synthXUserResultEnvelope(
				synthXCreatorRestID, synthXCreatorHandle, synthXCreatorName,
				h.xSrv.URL+"/xcdn/creator_avatar.png", "Synthetic creator bio", 128,
			)))
		case strings.HasSuffix(r.URL.Path, "/"+x.OpUserTweets):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(synthXUserTimelineEnvelope("")))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(h.xSrv.Close)

	h.biliSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.biliReqs.Add(1)
		if r.URL.Path == "/x/web-interface/nav" && strings.Contains(r.Header.Get("Cookie"), "SESSDATA=") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"data":{"isLogin":true,"mid":778899,"uname":"SynthBili","wbi_img":{"img_url":"https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png","sub_url":"https://i0.hdslb.com/bfs/wbi/4932caff0ff746eab6f01bf08b70ac45.png"}}}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(h.biliSrv.Close)

	h.dySrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.dyReqs.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(h.dySrv.Close)

	h.wbSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.wbReqs.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(h.wbSrv.Close)

	fixedNow := time.Unix(1795327200, 0).UTC()
	opts := Options{
		Locations: h.loc,
		Desktop:   h.desktop,
		BilibiliOpts: bilibili.ClientOptions{
			Endpoints: bilibili.Endpoints{
				APIBase:      h.biliSrv.URL,
				PassportBase: h.biliSrv.URL,
				WWWBase:      h.biliSrv.URL,
				SpaceBase:    h.biliSrv.URL,
				DynamicBase:  h.biliSrv.URL,
				LiveAPIBase:  h.biliSrv.URL,
				LiveWebBase:  h.biliSrv.URL,
			},
			PacingInterval: -1,
			AllowLocalhost: true,
		},
		DouyinOpts: douyin.ClientOptions{
			Endpoints: douyin.Endpoints{
				ProfileSelf:  h.dySrv.URL + "/dy/self",
				ProfileOther: h.dySrv.URL + "/dy/other",
				AwemeDetail:  h.dySrv.URL + "/dy/detail",
				AwemePost:    h.dySrv.URL + "/dy/posts",
				LiveWebEnter: h.dySrv.URL + "/dy/live",
			},
			MinInterval:    -1,
			AllowLocalhost: true,
		},
		WeiboOpts: weibo.ClientOptions{
			Endpoints: weibo.Endpoints{
				PCBasicInfo:  h.wbSrv.URL + "/wb/basic",
				ProfileInfo:  h.wbSrv.URL + "/wb/profile",
				StatusesShow: h.wbSrv.URL + "/wb/show",
			},
			MinInterval:    -1,
			AllowLocalhost: true,
		},
		XOpts: x.ClientOptions{
			Endpoints: x.Endpoints{
				HomeURL:         h.xSrv.URL + "/home",
				AccountSettings: h.xSrv.URL + "/i/api/1.1/account/settings.json",
				GraphQLBase:     h.xSrv.URL + "/i/api/graphql",
			},
			MinInterval:        -1,
			RateLimitJitterMax: -1,
			AllowLocalhost:     true,
			AssetTransport:     newXAbsRewriteTransport(t, h.xSrv),
			Now:                func() time.Time { return fixedNow },
			Sleep:              func(context.Context, time.Duration) error { return nil },
			TxIDOptions: []x.TxIDOption{
				x.WithTxIDClock(func() time.Time { return fixedNow }),
				x.WithTxIDRandByte(func() (byte, error) { return 0x42, nil }),
			},
		},
		DisableBgWorkers: true,
		CacheQueuePacing: fastTestQueuePacing(),
	}
	if optsFn != nil {
		optsFn(&opts)
	}
	b, err := NewBackend(opts)
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	b.dl.SetBackoffScale(time.Millisecond)
	t.Cleanup(func() { _ = b.Close() })
	h.backend = b
	return h
}

func (h *xServiceTestHarness) loginX(t *testing.T) credential.Status {
	t.Helper()
	st, err := h.backend.LoginWithCookie(context.Background(), "x", synthXValidCookie, "")
	if err != nil || !st.LoggedIn {
		t.Fatalf("LoginWithCookie(x): st=%+v err=%v", st, err)
	}
	return st
}

func waitDownloadTaskTerminal(t *testing.T, b *Backend, taskID string) download.Task {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		detail, ok, err := b.TaskDetail(taskID)
		if err == nil && ok {
			switch detail.Task.Status {
			case "completed", "partial", "failed", "canceled", "paused":
				return detail.Task
			}
		}
		time.Sleep(15 * time.Millisecond)
	}
	detail, _, _ := b.TaskDetail(taskID)
	t.Fatalf("download task %s did not reach terminal status in time; last=%+v", taskID, detail.Task)
	return download.Task{}
}

func scanTreeForSentinels(t *testing.T, root string, sentinels []string) {
	t.Helper()
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		// Skip encrypted credential binary files (DPAPI/keychain/x.bin ciphertext).
		if strings.HasSuffix(strings.ToLower(p), ".bin") {
			return nil
		}
		raw, rErr := os.ReadFile(p)
		if rErr != nil {
			return nil
		}
		for _, s := range sentinels {
			if s != "" && bytes.Contains(raw, []byte(s)) {
				t.Errorf("sentinel %q leaked to disk file %s", s, p)
			}
		}
		return nil
	})
}

// M7-K03 & M7-K04: 4-site auth isolation, X-only app entry, 0 pre-auth requests,
// passive ct0 sync, logout pauses ONLY X work, and zero secret leakage.
func TestXAuthIsolationAndLoginVerification(t *testing.T) {
	h := newXServiceTestHarness(t, nil)
	ctx := context.Background()

	// 1. Unlogged X operations fail locally with 0 HTTP requests.
	if h.backend.HasAnyLogin() {
		t.Fatal("fresh backend should have HasAnyLogin=false")
	}
	if _, err := h.backend.XPost(ctx, "1791257321273778509"); !site.IsKind(err, site.ErrKindAuthRequired) {
		t.Fatalf("unlogged XPost err=%v, want auth_required", err)
	}
	if _, err := h.backend.XUser(ctx, synthXCreatorRestID); !site.IsKind(err, site.ErrKindAuthRequired) {
		t.Fatalf("unlogged XUser err=%v, want auth_required", err)
	}
	if _, err := h.backend.XUserPosts(ctx, synthXCreatorRestID, ""); !site.IsKind(err, site.ErrKindAuthRequired) {
		t.Fatalf("unlogged XUserPosts err=%v, want auth_required", err)
	}
	if _, err := h.backend.CacheXPost(ctx, "1791257321273778509", nil); !site.IsKind(err, site.ErrKindAuthRequired) {
		t.Fatalf("unlogged CacheXPost err=%v, want auth_required", err)
	}
	if _, err := h.backend.LoginWithCookie(ctx, "x", "auth_token=missing_ct0_only", ""); !site.IsKind(err, site.ErrKindInvalidInput) && !site.IsKind(err, site.ErrKindBadRequest) {
		t.Fatalf("missing ct0 LoginWithCookie err=%v, want invalid_input/bad_request", err)
	}
	if h.xReqs.Load() != 0 {
		t.Fatalf("unlogged/invalid X ops sent %d requests, want 0", h.xReqs.Load())
	}

	// 2. X-only login succeeds and enters the app.
	st := h.loginX(t)
	if !st.LoggedIn || st.UID != synthXSelfRestID || st.UniqueID != synthXSelfScreenName || st.Uname != synthXSelfName {
		t.Fatalf("X login status = %+v", st)
	}
	if !h.backend.HasAnyLogin() {
		t.Fatal("X-only login should satisfy HasAnyLogin")
	}
	ren := h.backend.Renewal("x")
	if ren.Available || ren.Reason != credential.XManualRenewalUnavailableReason {
		t.Fatalf("Renewal(x) = %+v, want Available=false Reason=%q", ren, credential.XManualRenewalUnavailableReason)
	}
	statuses := h.backend.Statuses()
	if len(statuses) != 4 {
		t.Fatalf("Statuses count = %d, want 4", len(statuses))
	}

	// 3. With ONLY X logged in, Bilibili/Douyin/Weibo online ops fail locally with 0 HTTP requests.
	if _, err := h.backend.OpenLink(ctx, "https://www.bilibili.com/video/BV1xx411c7mD"); !site.IsKind(err, site.ErrKindAuthRequired) {
		t.Fatalf("unlogged Bilibili OpenLink err=%v", err)
	}
	if _, err := h.backend.DouyinUser(ctx, "MS4wLjABAAAA_synth_01"); !site.IsKind(err, site.ErrKindAuthRequired) {
		t.Fatalf("unlogged DouyinUser err=%v", err)
	}
	if _, err := h.backend.WeiboUser(ctx, "1234567890"); !site.IsKind(err, site.ErrKindAuthRequired) {
		t.Fatalf("unlogged WeiboUser err=%v", err)
	}
	if h.biliReqs.Load() != 0 || h.dyReqs.Load() != 0 || h.wbReqs.Load() != 0 {
		t.Fatalf("unlogged sites sent requests: bili=%d dy=%d wb=%d", h.biliReqs.Load(), h.dyReqs.Load(), h.wbReqs.Load())
	}

	// 4. Failed new cookie login leaves stored valid X credential untouched.
	if _, err := h.backend.LoginWithCookie(ctx, "x", "auth_token=WRONG_TOKEN_999; ct0=WrongCT0Token999", ""); !site.IsKind(err, site.ErrKindAuthRequired) {
		t.Fatalf("bad X cookie err=%v, want auth_required", err)
	}
	if cur := h.backend.StatusOf("x"); !cur.LoggedIn || cur.UID != synthXSelfRestID {
		t.Fatalf("failed LoginWithCookie mutated stored X credential: %+v", cur)
	}

	// 5. Passive Set-Cookie ct0 rotation syncs via callback and BootstrapAuth checks only X.
	var ct0Synced atomic.Bool
	h.backend.XClient().SetCT0UpdatedHandler(func(newCT0 string) {
		if newCT0 == synthXCT0Rotated {
			ct0Synced.Store(true)
		}
	})
	h.mu.Lock()
	h.rotateCT0 = true
	h.mu.Unlock()
	if vst, err := h.backend.VerifySiteAccount(ctx, "x"); err != nil || !vst.LoggedIn {
		t.Fatalf("VerifySiteAccount(x): st=%+v err=%v", vst, err)
	}
	if !ct0Synced.Load() {
		t.Fatal("expected passive Set-Cookie ct0 rotation callback")
	}
	if _, err := h.backend.BootstrapAuth(ctx); err != nil {
		t.Fatalf("BootstrapAuth(X-only) err=%v", err)
	}
	if h.biliReqs.Load() != 0 || h.dyReqs.Load() != 0 || h.wbReqs.Load() != 0 {
		t.Fatalf("BootstrapAuth leaked requests to unlogged sites: bili=%d dy=%d wb=%d", h.biliReqs.Load(), h.dyReqs.Load(), h.wbReqs.Load())
	}

	// 6. Log in Bilibili too; enqueue both Bilibili and X queue tasks; logout X pauses ONLY X.
	if bst, err := h.backend.LoginWithCookie(ctx, "bilibili", "SESSDATA=SYNTH_BILI_SESS_X; DedeUserID=778899; bili_jct=SYNTH_JCT_X", ""); err != nil || !bst.LoggedIn {
		t.Fatalf("LoginWithCookie(bilibili): st=%+v err=%v", bst, err)
	}
	biliQ, err := h.backend.EnqueueCacheQueue(ctx, CacheQueueRequest{Site: "bilibili", List: "videos", User: "778899", UserName: "SynthBili"})
	if err != nil {
		t.Fatalf("EnqueueCacheQueue(bilibili): %v", err)
	}
	xQ, err := h.backend.EnqueueCacheQueue(ctx, CacheQueueRequest{Site: "x", List: "posts", User: synthXCreatorRestID, UserName: synthXCreatorName})
	if err != nil {
		t.Fatalf("EnqueueCacheQueue(x): %v", err)
	}
	h.backend.xPostCache.put(&x.Post{ID: "1791257321273778509", Author: x.AuthorSummary{RestID: synthXCreatorRestID}, Text: "cached in mem"}, true)

	if lSt, err := h.backend.LogoutSite("x"); err != nil || lSt.LoggedIn {
		t.Fatalf("LogoutSite(x): st=%+v err=%v", lSt, err)
	}
	if !h.backend.StatusOf("bilibili").LoggedIn {
		t.Fatal("X logout touched Bilibili login")
	}
	if _, ok := h.backend.xPostCache.get("1791257321273778509"); ok {
		t.Fatal("X logout did not clear xPostCache")
	}
	for _, qt := range h.backend.ListCacheQueue() {
		if qt.ID == xQ.ID && qt.Status != CacheQueuePaused {
			t.Fatalf("X queue task not paused after X logout: %+v", qt)
		}
		if qt.ID == biliQ.ID && qt.Status == CacheQueuePaused {
			t.Fatalf("Bilibili queue task was paused by X logout: %+v", qt)
		}
	}

	// 7. Log redaction covers all X secret key names and zero sentinels leak to disk/logs.
	for _, probe := range []string{
		"auth_token=SECRET_PROBE_1",
		"ct0=SECRET_PROBE_2",
		"twid=u%3D12345",
		"guest_id=v1%3A999",
		"kdt=SECRET_PROBE_3",
		"x-csrf-token: SECRET_PROBE_4",
		"x-client-transaction-id: SECRET_PROBE_5",
		"authorization: Bearer SECRET_PROBE_6",
		"bearer SECRET_PROBE_7",
	} {
		h.backend.Logf("test probe %s", probe)
	}
	logs, err := h.backend.RecentLogs(200)
	if err != nil {
		t.Fatalf("RecentLogs: %v", err)
	}
	joinedLogs := strings.Join(logs, "\n")
	for _, secret := range []string{
		synthXAuthSentinel, synthXCT0Sentinel, synthXCT0Rotated,
		"SECRET_PROBE_1", "SECRET_PROBE_2", "SECRET_PROBE_3",
		"SECRET_PROBE_4", "SECRET_PROBE_5", "SECRET_PROBE_6", "SECRET_PROBE_7",
		"DROP_ME_CF_BM", "DROP_ME_ADS",
	} {
		if strings.Contains(joinedLogs, secret) {
			t.Fatalf("secret %q leaked into RecentLogs", secret)
		}
	}
	scanTreeForSentinels(t, h.loc.DataDir, []string{
		synthXAuthSentinel, synthXCT0Sentinel, synthXCT0Rotated, "DROP_ME_CF_BM", "DROP_ME_ADS",
	})
}

// M7-K06: Resolve & OpenLink for posts, users, mirrors (0 mirror reqs), t.co expansion,
// unsupported routes (0 API reqs), history recording, FetchImage & StagePreviewMedia, proxy.
func TestXResolveOpenLinkAndMediaPreview(t *testing.T) {
	t.Cleanup(func() { _ = site.SetProxy(site.ProxySystem, "") })
	const statusID = "1791257321273778509"
	h := newXServiceTestHarness(t, nil)
	h.loginX(t)
	ctx := context.Background()

	h.mu.Lock()
	h.xHandler = func(w http.ResponseWriter, r *http.Request) bool {
		switch {
		case r.URL.Path == "/tco/post1":
			// Cookieless t.co redirect to canonical x.com post!
			if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
				t.Errorf("t.co request carried auth headers: Cookie=%q Auth=%q", r.Header.Get("Cookie"), r.Header.Get("Authorization"))
			}
			w.Header().Set("Location", "https://x.com/"+synthXCreatorHandle+"/status/"+statusID+"?s=20")
			w.WriteHeader(http.StatusMovedPermanently)
			return true
		case strings.HasSuffix(r.URL.Path, "/"+x.OpTweetDetail):
			tweetJSON := fmt.Sprintf(`{
				"__typename":"TweetWithVisibilityResults",
				"limitedActionResults":{"limited_actions":[{"action":"Reply"}]},
				"tweet":{
					"__typename":"Tweet",
					"rest_id":%q,
					"edit_control":{"edit_tweet_ids":["1791257321273778500",%q]},
					"core":{"user_results":{"result":%s}},
					"note_tweet":{"is_expandable":true,"note_tweet_results":{"result":{"text":"Full note_tweet body with link https://t.co/doc1","entity_set":{"urls":[{"url":"https://t.co/doc1","expanded_url":"https://example.org/spec","display_url":"example.org/spec"}]}}}},
					"card":{"legacy":{"name":"poll2choice_text_only","binding_values":[
						{"key":"choice1_label","value":{"string_value":"Option A"}},
						{"key":"choice2_label","value":{"string_value":"Option B"}},
						{"key":"choice1_count","value":{"string_value":"12"}},
						{"key":"choice2_count","value":{"string_value":"8"}},
						{"key":"counts_are_final","value":{"boolean_value":true}}
					]}},
					"legacy":{
						"id_str":%q,
						"user_id_str":%q,
						"created_at":"Fri May 17 00:00:00 +0000 2024",
						"full_text":"Truncated...",
						"reply_count":3,"retweet_count":4,"favorite_count":50,"quote_count":2,"bookmark_count":7,
						"extended_entities":{"media":[{
							"media_key":"3_1001",
							"type":"photo",
							"media_url_https":%q,
							"original_info":{"width":1200,"height":800},
							"ext_alt_text":"Synthetic diagram"
						}]}
					},
					"views":{"count":"999"}
				}
			}`, statusID, statusID,
				synthXUserCoreJSON(synthXCreatorRestID, synthXCreatorHandle, synthXCreatorName, h.xSrv.URL+"/xcdn/creator_avatar.png", "Creator bio", 128),
				statusID, synthXCreatorRestID, h.xSrv.URL+"/xcdn/photo1.png")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(synthXTweetDetailEnvelope(synthXTweetEntry("tweet-"+statusID, tweetJSON))))
			return true
		case strings.HasSuffix(r.URL.Path, "/"+x.OpUserTweets):
			tweetJSON := fmt.Sprintf(`{
				"__typename":"Tweet",
				"rest_id":%q,
				"core":{"user_results":{"result":%s}},
				"legacy":{
					"id_str":%q,
					"user_id_str":%q,
					"created_at":"Fri May 17 00:00:00 +0000 2024",
					"full_text":"Creator pinned timeline post"
				}
			}`, statusID,
				synthXUserCoreJSON(synthXCreatorRestID, synthXCreatorHandle, synthXCreatorName, h.xSrv.URL+"/xcdn/creator_avatar.png", "Creator bio", 128),
				statusID, synthXCreatorRestID)
			pinEntry := fmt.Sprintf(`{"entryId":"profile-pin-%s","content":{"entryType":"TimelineTimelineItem","itemContent":{"itemType":"TimelineTweet","tweetDisplayType":"Tweet","tweet_results":{"result":%s}}}}`, statusID, tweetJSON)
			entries := pinEntry + "," + synthXCursorEntry("cursor-bottom-1", "Bottom", "DAAHCgAB/+NextPage==")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(synthXUserTimelineEnvelope(entries)))
			return true
		}
		return false
	}
	h.mu.Unlock()

	// 1. Direct post OpenLink + DTO assertions + history entry.
	res, err := h.backend.OpenLink(ctx, "https://x.com/"+synthXCreatorHandle+"/status/"+statusID)
	if err != nil {
		t.Fatalf("OpenLink(x post): %v", err)
	}
	if res.Site != "x" || res.XPost == nil || res.XPost.ID != statusID {
		t.Fatalf("unexpected OpenLink result: %+v", res)
	}
	if !strings.Contains(res.XPost.Text, "https://example.org/spec") || !res.XPost.IsLongText || !res.XPost.IsEdited {
		t.Fatalf("XPost text/long/edit fields = %+v", res.XPost)
	}
	if res.XPost.VisibilityLabel == "" || res.XPost.Poll == nil || len(res.XPost.Poll.Options) != 2 || res.XPost.Poll.TotalVotes != 20 {
		t.Fatalf("XPost visibility/poll = %+v", res.XPost)
	}
	if res.XPost.CacheKey != "x:dynamic:"+statusID || res.XPost.Stats.ViewCount != 999 || len(res.XPost.Media) != 1 {
		t.Fatalf("XPost cacheKey/stats/media = %+v", res.XPost)
	}
	if res.HistoryThumbs == nil || res.HistoryThumbs.Site != "x" {
		t.Fatalf("HistoryThumbs = %+v", res.HistoryThumbs)
	}
	if err := h.backend.FetchHistoryThumbs(ctx, *res.HistoryThumbs); err != nil {
		t.Fatalf("FetchHistoryThumbs(x): %v", err)
	}
	hist := h.backend.ListHistory()
	if len(hist) == 0 || hist[0].Site != "x" || hist[0].ID != statusID {
		t.Fatalf("recent history = %+v", hist)
	}

	// 2. Mirror link rewritten locally to x.com with 0 mirror requests.
	mTarget, err := h.backend.Resolve(ctx, "https://fxtwitter.com/"+synthXCreatorHandle+"/status/"+statusID+".mp4?s=20")
	if err != nil || mTarget.Site != site.X || mTarget.Kind != "dynamic" || mTarget.ID != statusID {
		t.Fatalf("Resolve(mirror) = %+v err=%v", mTarget, err)
	}

	// 3. t.co expansion (cookieless).
	tcoTarget, err := h.backend.Resolve(ctx, h.xSrv.URL+"/tco/post1")
	if err != nil || tcoTarget.Site != site.X || tcoTarget.Kind != "dynamic" || tcoTarget.ID != statusID {
		t.Fatalf("Resolve(t.co) = %+v err=%v", tcoTarget, err)
	}

	// 4. User link -> XUserDTO + XUserPostsPageDTO + statuses_count in account_works.
	uRes, err := h.backend.OpenLink(ctx, "https://x.com/"+synthXCreatorHandle)
	if err != nil {
		t.Fatalf("OpenLink(x user): %v", err)
	}
	if uRes.XUser == nil || uRes.XUser.RestID != synthXCreatorRestID || uRes.XUser.ScreenName != synthXCreatorHandle || uRes.XUser.StatusesCount != 128 {
		t.Fatalf("XUser = %+v", uRes.XUser)
	}
	if uRes.XUserPosts == nil || len(uRes.XUserPosts.Posts) != 1 || uRes.XUserPosts.NextCursor != "DAAHCgAB/+NextPage==" {
		t.Fatalf("XUserPosts = %+v", uRes.XUserPosts)
	}

	// 5. Unsupported X routes rejected locally with 0 API requests!
	beforeUnsupported := h.xReqs.Load()
	for _, bad := range []string{
		"https://x.com/i/broadcasts/1RDGlSynth01",
		"https://x.com/i/spaces/1nAKEsynth02",
		"https://x.com/i/articles/19000000001",
		"https://x.com/synth_creator/lists/legacy-slug",
		"https://x.com/i/communities/17000000001",
		"https://x.com/search?q=golang",
		"https://x.com/home",
	} {
		if _, err := h.backend.OpenLink(ctx, bad); !site.IsKind(err, site.ErrKindUnsupportedLink) {
			t.Fatalf("OpenLink(%s) err=%v, want unsupported_link", bad, err)
		}
	}
	if delta := h.xReqs.Load() - beforeUnsupported; delta != 0 {
		t.Fatalf("unsupported X routes sent %d API requests, want 0", delta)
	}

	// 6. FetchImage & StagePreviewMedia for X send NO Cookie, NO Referer, NO Origin.
	imgData, err := h.backend.FetchImage(ctx, h.xSrv.URL+"/xcdn/photo1.png")
	if err != nil || len(imgData) == 0 {
		t.Fatalf("FetchImage(x): len=%d err=%v", len(imgData), err)
	}
	h.mu.Lock()
	hdr := h.lastMediaHdr.Clone()
	h.mu.Unlock()
	if hdr.Get("Cookie") != "" || hdr.Get("Referer") != "" || hdr.Get("Origin") != "" {
		t.Fatalf("X image request carried forbidden headers: Cookie=%q Referer=%q Origin=%q", hdr.Get("Cookie"), hdr.Get("Referer"), hdr.Get("Origin"))
	}
	previewPath, err := h.backend.StagePreviewMedia(ctx, "x", h.xSrv.URL+"/xcdn/clip.mp4", ".mp4")
	if err != nil || previewPath == "" {
		t.Fatalf("StagePreviewMedia(x): path=%q err=%v", previewPath, err)
	}
	if st, statErr := os.Stat(previewPath); statErr != nil || st.Size() == 0 {
		t.Fatalf("staged X preview file missing/empty: %v", statErr)
	}

	// 7. Settings proxy applies immediately.
	next := h.backend.GetSettings()
	next.Network.ProxyMode, next.Network.ProxyURL = "custom", "http://127.0.0.1:8089"
	if _, err := h.backend.UpdateSettings(next); err != nil || site.ProxyMode() != site.ProxyCustom {
		t.Fatalf("UpdateSettings proxy failed: %v mode=%q", err, site.ProxyMode())
	}
}

// X3/X4: CacheXPost covering single MP4 + 1x TweetDetail URL refresh on 403,
// single HLS-only degraded partial with 0 HLS fetches, thread + mixed media (photo/GIF),
// and forward/quote with degraded unavailable origin.
func TestXCachePostSingleMP4RefreshHLSOnlyGIFThreadAndForward(t *testing.T) {
	const (
		mp4ID    = "1791257321273779001"
		hlsID    = "1791257321273779002"
		threadID = "1791257321273779003"
		reply2ID = "1791257321273779004"
		fwdID    = "1791257321273779005"
	)

	h := newXServiceTestHarness(t, func(opts *Options) {
		opts.DisableBgWorkers = false
	})
	// Stop background loops except download manager? Actually DisableBgWorkers=false already starts dlMgr!
	h.loginX(t)
	ctx := context.Background()

	var mp4DetailCalls atomic.Int32
	h.mu.Lock()
	h.xHandler = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/xcdn/stale_video.mp4" {
			w.WriteHeader(http.StatusForbidden)
			return true
		}
		if r.URL.Path == "/xcdn/fresh_video.mp4" || r.URL.Path == "/xcdn/anim.mp4" {
			w.Header().Set("Content-Type", "video/mp4")
			http.ServeContent(w, r, "video.mp4", time.Time{}, bytes.NewReader(h.mp4Bytes))
			return true
		}
		if strings.HasSuffix(r.URL.Path, "/"+x.OpTweetDetail) {
			vars := r.URL.Query().Get("variables")
			authorJSON := synthXUserCoreJSON(synthXCreatorRestID, synthXCreatorHandle, synthXCreatorName, h.xSrv.URL+"/xcdn/creator_avatar.png", "Creator bio", 128)
			w.Header().Set("Content-Type", "application/json")
			switch {
			case strings.Contains(vars, mp4ID):
				n := mp4DetailCalls.Add(1)
				vidURL := h.xSrv.URL + "/xcdn/stale_video.mp4"
				if n >= 2 {
					vidURL = h.xSrv.URL + "/xcdn/fresh_video.mp4"
				}
				tweetJSON := fmt.Sprintf(`{
					"__typename":"Tweet","rest_id":%q,
					"core":{"user_results":{"result":%s}},
					"legacy":{
						"id_str":%q,"user_id_str":%q,"created_at":"Fri May 17 01:00:00 +0000 2024",
						"full_text":"Single MP4 refresh test",
						"extended_entities":{"media":[{
							"media_key":"7_9001","type":"video",
							"media_url_https":%q,
							"original_info":{"width":1280,"height":720},
							"video_info":{"duration_millis":2000,"variants":[
								{"content_type":"application/x-mpegURL","url":%q},
								{"content_type":"video/mp4","bitrate":2176000,"url":%q}
							]}
						}]}
					}
				}`, mp4ID, authorJSON, mp4ID, synthXCreatorRestID,
					h.xSrv.URL+"/xcdn/poster1.png",
					h.xSrv.URL+"/xcdn/never.m3u8",
					vidURL)
				_, _ = w.Write([]byte(synthXTweetDetailEnvelope(synthXTweetEntry("tweet-"+mp4ID, tweetJSON))))
				return true

			case strings.Contains(vars, hlsID):
				tweetJSON := fmt.Sprintf(`{
					"__typename":"Tweet","rest_id":%q,
					"core":{"user_results":{"result":%s}},
					"legacy":{
						"id_str":%q,"user_id_str":%q,"created_at":"Fri May 17 02:00:00 +0000 2024",
						"full_text":"HLS only degraded post",
						"extended_entities":{"media":[{
							"media_key":"7_9002","type":"video",
							"media_url_https":%q,
							"original_info":{"width":1280,"height":720},
							"video_info":{"duration_millis":9000,"variants":[
								{"content_type":"application/x-mpegURL","url":%q}
							]}
						}]}
					}
				}`, hlsID, authorJSON, hlsID, synthXCreatorRestID,
					h.xSrv.URL+"/xcdn/hls_poster.png",
					h.xSrv.URL+"/xcdn/stream_only.m3u8")
				_, _ = w.Write([]byte(synthXTweetDetailEnvelope(synthXTweetEntry("tweet-"+hlsID, tweetJSON))))
				return true

			case strings.Contains(vars, threadID):
				t1 := fmt.Sprintf(`{
					"__typename":"Tweet","rest_id":%q,
					"core":{"user_results":{"result":%s}},
					"legacy":{
						"id_str":%q,"user_id_str":%q,"conversation_id_str":%q,
						"created_at":"Fri May 17 03:00:00 +0000 2024",
						"full_text":"Thread part 1 with photo",
						"extended_entities":{"media":[{
							"media_key":"3_9003","type":"photo",
							"media_url_https":%q,
							"original_info":{"width":800,"height":600}
						}]}
					}
				}`, threadID, authorJSON, threadID, synthXCreatorRestID, threadID, h.xSrv.URL+"/xcdn/p1.png")
				t2 := fmt.Sprintf(`{
					"__typename":"Tweet","rest_id":%q,
					"core":{"user_results":{"result":%s}},
					"legacy":{
						"id_str":%q,"user_id_str":%q,"conversation_id_str":%q,
						"in_reply_to_status_id_str":%q,"in_reply_to_user_id_str":%q,
						"created_at":"Fri May 17 03:01:00 +0000 2024",
						"full_text":"Thread part 2 with GIF",
						"extended_entities":{"media":[{
							"media_key":"16_9004","type":"animated_gif",
							"media_url_https":%q,
							"original_info":{"width":480,"height":360},
							"video_info":{"variants":[{"content_type":"video/mp4","bitrate":0,"url":%q}]}
						}]}
					}
				}`, reply2ID, authorJSON, reply2ID, synthXCreatorRestID, threadID, threadID, synthXCreatorRestID,
					h.xSrv.URL+"/xcdn/gif_poster.png", h.xSrv.URL+"/xcdn/anim.mp4")
				entries := synthXTweetEntry("tweet-"+threadID, t1) + "," +
					`{"entryId":"conversationthread-1","content":{"entryType":"TimelineTimelineModule","items":[{"entryId":"conversationthread-1-tweet-` + reply2ID + `","item":{"itemContent":{"itemType":"TimelineTweet","tweetDisplayType":"Tweet","tweet_results":{"result":` + t2 + `}}}}]}}`
				_, _ = w.Write([]byte(synthXTweetDetailEnvelope(entries)))
				return true

			case strings.Contains(vars, fwdID):
				quoteJSON := fmt.Sprintf(`{
					"__typename":"Tweet","rest_id":%q,
					"core":{"user_results":{"result":%s}},
					"quoted_status_result":{"result":{"__typename":"TweetTombstone","tombstone":{"text":{"text":"Original post deleted"}}}},
					"legacy":{
						"id_str":%q,"user_id_str":%q,"created_at":"Fri May 17 04:00:00 +0000 2024",
						"full_text":"Quoting a deleted post",
						"is_quote_status":true,"quoted_status_id_str":"1791257321273770000"
					}
				}`, fwdID, authorJSON, fwdID, synthXCreatorRestID)
				_, _ = w.Write([]byte(synthXTweetDetailEnvelope(synthXTweetEntry("tweet-"+fwdID, quoteJSON))))
				return true
			}
		}
		return false
	}
	h.mu.Unlock()

	// 1. Single MP4 with 403 -> 1x TweetDetail refresh -> complete!
	t1, err := h.backend.CacheXPost(ctx, mp4ID, nil)
	if err != nil {
		t.Fatalf("CacheXPost(mp4): %v", err)
	}
	done1 := waitDownloadTaskTerminal(t, h.backend, t1.TaskID)
	if done1.Status != "completed" {
		t.Fatalf("MP4 refresh task status = %s (%s), want completed", done1.Status, done1.ErrorMessage)
	}
	if mp4DetailCalls.Load() < 2 {
		t.Fatalf("expected >=2 TweetDetail calls (initial + 1x refresh), got %d", mp4DetailCalls.Load())
	}
	if h.hlsHits.Load() != 0 {
		t.Fatalf("HLS URL was unexpectedly requested %d times", h.hlsHits.Load())
	}
	item1, ok, err := h.backend.GetLibraryItem("x:dynamic:" + mp4ID)
	if err != nil || !ok || item1.Author.UniqueID != synthXCreatorHandle {
		t.Fatalf("cached MP4 item = %+v ok=%v err=%v", item1, ok, err)
	}

	// 2. Single HLS-only video -> partial with XHLSOnlyMissingReason & 0 HLS requests.
	t2, err := h.backend.CacheXPost(ctx, hlsID, nil)
	if err != nil {
		t.Fatalf("CacheXPost(hls): %v", err)
	}
	done2 := waitDownloadTaskTerminal(t, h.backend, t2.TaskID)
	if done2.Status != "partial" {
		t.Fatalf("HLS-only task status = %s, want partial", done2.Status)
	}
	if h.hlsHits.Load() != 0 {
		t.Fatalf("HLS-only post fetched .m3u8 %d times, want 0", h.hlsHits.Load())
	}
	item2, ok, err := h.backend.GetLibraryItem("x:dynamic:" + hlsID)
	if err != nil || !ok || len(item2.Missing) == 0 || item2.Missing[0].Reason != x.HLSOnlyNoticeText {
		t.Fatalf("HLS-only item missing = %+v (ok=%v err=%v)", item2.Missing, ok, err)
	}

	// 3. Thread with part 1 photo + part 2 GIF -> complete with Thread parts & MediaIndexes!
	t3, err := h.backend.CacheXPost(ctx, threadID, nil)
	if err != nil {
		t.Fatalf("CacheXPost(thread): %v", err)
	}
	done3 := waitDownloadTaskTerminal(t, h.backend, t3.TaskID)
	if done3.Status != "completed" {
		t.Fatalf("thread task status = %s (%s), want completed", done3.Status, done3.ErrorMessage)
	}
	item3, ok, err := h.backend.GetLibraryItem("x:dynamic:" + threadID)
	if err != nil || !ok || len(item3.Thread) != 2 {
		t.Fatalf("thread item = %+v ok=%v err=%v", item3, ok, err)
	}
	if len(item3.Thread[0].MediaIndexes) != 1 || len(item3.Thread[1].MediaIndexes) != 1 {
		t.Fatalf("thread MediaIndexes = %+v", item3.Thread)
	}

	// 4. Quote with degraded unavailable origin -> stored under forwards/ with Via & Origin.Unavailable.
	via := &library.ItemVia{Reason: library.ViaForward, Site: "x", UID: synthXSelfRestID, Name: synthXSelfName, DynamicID: fwdID}
	t4, err := h.backend.CacheXPost(ctx, fwdID, via)
	if err != nil {
		t.Fatalf("CacheXPost(forward): %v", err)
	}
	done4 := waitDownloadTaskTerminal(t, h.backend, t4.TaskID)
	if done4.Status != "completed" {
		t.Fatalf("forward task status = %s (%s), want completed", done4.Status, done4.ErrorMessage)
	}
	item4, ok, err := h.backend.GetLibraryItem("x:dynamic:" + fwdID)
	if err != nil || !ok || item4.Origin == nil || !item4.Origin.Unavailable || !strings.Contains(filepath.ToSlash(item4.ItemDir), "/forwards/") {
		t.Fatalf("forward item = %+v ok=%v err=%v", item4, ok, err)
	}
}

// M7-K17: Desktop local login/re-login NEVER auto-pushes X cookies to remote & never queues pending;
// manual PushCredentialToRemote("x") works; remote AcceptPushedCredential/ClearPushedCredential & sync work.
func TestXRemoteManualPushNeverAutoPushesAndServerAccepts(t *testing.T) {
	const remoteToken = "SYNTH_REMOTE_TOKEN_X_0123456789_EXTRA"
	ctx := context.Background()

	var xPushCount atomic.Int32
	var biliPushCount atomic.Int32
	var failXPush atomic.Bool

	remoteSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+remoteToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/status":
			_ = json.NewEncoder(w).Encode(remote.Status{
				Version:  "0.1.0",
				Protocol: remote.ProtocolVersion,
				Node:     remote.Node{ID: "node-remote-x", Name: "RemoteX"},
			})
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/credentials/x":
			xPushCount.Add(1)
			if failXPush.Load() {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(remote.SiteStatus{Site: "x", State: remote.SiteValid, Uname: synthXSelfName})
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/credentials/bilibili":
			biliPushCount.Add(1)
			_ = json.NewEncoder(w).Encode(remote.SiteStatus{Site: "bilibili", State: remote.SiteValid, Uname: "SynthBili"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer remoteSrv.Close()
	fp := remote.Fingerprint(remoteSrv.Certificate().Raw)

	h := newXServiceTestHarness(t, nil)
	if _, err := h.backend.ConnectRemote(ctx, RemoteConnectInput{URL: remoteSrv.URL, Token: remoteToken, Fingerprint: fp}); err != nil {
		t.Fatalf("ConnectRemote: %v", err)
	}

	// 1. Local X login & re-login must NEVER auto-push X cookie or mark pending!
	h.loginX(t)
	h.loginX(t)
	time.Sleep(50 * time.Millisecond)
	if xPushCount.Load() != 0 {
		t.Fatalf("local X login auto-pushed X cookie %d times, want 0", xPushCount.Load())
	}
	if conn, _ := h.backend.RemoteConnection(); len(conn.PendingCookies) != 0 {
		t.Fatalf("X login queued PendingCookies=%v, want empty", conn.PendingCookies)
	}

	// 2. Contrast: Bilibili login DOES auto-push (proving remote connection is active and only X is opted out).
	if _, err := h.backend.LoginWithCookie(ctx, "bilibili", "SESSDATA=SYNTH_BILI_REM; DedeUserID=778899; bili_jct=SYNTH_JCT_REM", ""); err != nil {
		t.Fatalf("LoginWithCookie(bilibili): %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && biliPushCount.Load() == 0 {
		time.Sleep(15 * time.Millisecond)
	}
	if biliPushCount.Load() == 0 || xPushCount.Load() != 0 {
		t.Fatalf("biliPush=%d xPush=%d, want biliPush>=1 and xPush==0", biliPushCount.Load(), xPushCount.Load())
	}

	// 3. Failed manual X push must NOT queue pending auto-retry!
	failXPush.Store(true)
	if _, err := h.backend.PushCredentialToRemote(ctx, "x"); err == nil {
		t.Fatal("expected manual X push to fail on 500")
	}
	if conn, _ := h.backend.RemoteConnection(); len(conn.PendingCookies) != 0 {
		t.Fatalf("failed manual X push must not queue PendingCookies, got %v", conn.PendingCookies)
	}
	failXPush.Store(false)
	if h.backend.RetryPendingRemoteCookies(ctx) {
		t.Fatal("RetryPendingRemoteCookies should not push X")
	}
	if xPushCount.Load() != 1 {
		t.Fatalf("xPushCount = %d, want 1 (no background retry for X)", xPushCount.Load())
	}

	// 4. Explicit manual PushCredentialToRemote("x") succeeds.
	pushed, err := h.backend.PushCredentialToRemote(ctx, "x")
	if err != nil || pushed.Site != "x" || pushed.State != remote.SiteValid {
		t.Fatalf("manual PushCredentialToRemote(x) = %+v err=%v", pushed, err)
	}
	if xPushCount.Load() != 2 {
		t.Fatalf("xPushCount = %d, want 2", xPushCount.Load())
	}

	// 5. Remote Profile backend: AcceptPushedCredential, ServerStatus, Snapshot Sync, ClearPushedCredential.
	remRoot := t.TempDir()
	remExport := filepath.Join(remRoot, "export")
	remB, err := NewBackend(Options{
		Locations: RemoteLocations(filepath.Join(remRoot, "data"), remExport),
		Desktop:   NewMockDesktop(),
		Profile:   ProfileRemote,
		NodeName:  "RemoteXNode",
		XOpts: x.ClientOptions{
			Endpoints: x.Endpoints{
				HomeURL:         h.xSrv.URL + "/home",
				AccountSettings: h.xSrv.URL + "/i/api/1.1/account/settings.json",
				GraphQLBase:     h.xSrv.URL + "/i/api/graphql",
			},
			MinInterval:        -1,
			RateLimitJitterMax: -1,
			AllowLocalhost:     true,
			AssetTransport:     newXAbsRewriteTransport(t, h.xSrv),
		},
		DisableBgWorkers: true,
	})
	if err != nil {
		t.Fatalf("NewBackend(ProfileRemote): %v", err)
	}
	defer remB.Close()

	// Accept valid X cookie.
	rSt, err := remB.AcceptPushedCredential(ctx, "x", synthXValidCookie)
	if err != nil || rSt.Site != "x" || rSt.State != remote.SiteValid {
		t.Fatalf("remote AcceptPushedCredential(x) = %+v err=%v", rSt, err)
	}
	if cs := remB.StatusOf("x"); cs.Source != CredentialSourceRemotePush || !cs.LoggedIn {
		t.Fatalf("remote x cred status = %+v", cs)
	}

	// Reject bad X cookie WITHOUT disturbing valid stored credential.
	if _, err := remB.AcceptPushedCredential(ctx, "x", "auth_token=INVALID_REMOTE_999; ct0=InvalidCT0999"); err == nil {
		t.Fatal("remote accepted invalid X cookie")
	}
	if !remB.StatusOf("x").LoggedIn {
		t.Fatal("failed remote AcceptPushedCredential cleared valid X credential")
	}

	// Subscription sync includes X accounts and never runs full-cache on remote.
	snap := subscription.Snapshot{
		Format:  subscription.SnapshotFormat,
		Version: subscription.SnapshotVersion,
		Subscriptions: []subscription.SnapshotSubscription{{
			ID:   "sub-x-1",
			Name: synthXCreatorName,
			Tags: []string{subscription.TagScheduledCheck, subscription.TagCacheVideo, subscription.TagFullCache},
			Accounts: []subscription.SnapshotAccount{{
				Site:     "x",
				UID:      synthXCreatorRestID,
				UniqueID: synthXCreatorHandle,
				Name:     synthXCreatorName,
			}},
		}},
	}
	rawSnap, _ := json.Marshal(snap)
	if info, err := remB.ApplyRemoteSnapshot(rawSnap); err != nil || info.Accounts != 1 {
		t.Fatalf("ApplyRemoteSnapshot(x) = %+v err=%v", info, err)
	}
	if ids := remB.startDueFullCacheSweeps(ctx, remB.StartedAt().AddDate(1, 0, 0)); len(ids) != 0 {
		t.Fatalf("remote started full-cache sweeps for X: %v", ids)
	}

	// ServerStatus includes X row; ClearPushedCredential("x") resets X row to SiteNone.
	foundX := false
	for _, row := range remB.ServerStatus().Sites {
		if row.Site == "x" && row.State == remote.SiteValid {
			foundX = true
		}
	}
	if !foundX {
		t.Fatalf("ServerStatus missing valid X site row: %+v", remB.ServerStatus().Sites)
	}
	if err := remB.ClearPushedCredential("x"); err != nil {
		t.Fatalf("ClearPushedCredential(x): %v", err)
	}
	for _, row := range remB.ServerStatus().Sites {
		if row.Site == "x" && row.State != remote.SiteNone {
			t.Fatalf("after ClearPushedCredential(x), status row = %+v, want none", row)
		}
	}
}

// Perf #17: States() uses ListNonTerminalTasks, ListDownloadTasks uses ListTasksSummary,
// SetOnTaskBatchUpdate coalesces batch pause/retry to 1 EventTaskUpdated, and GetLibraryItemMetaOnly works.
func TestServicePerfOptimizations(t *testing.T) {
	h := newXServiceTestHarness(t, nil)
	h.loginX(t)

	// 1. Seed a library item and verify GetLibraryItemMetaOnly matches GetLibraryItem.
	itemDir := filepath.Join(h.backend.GetSettings().CacheDir, "x", synthXCreatorRestID, "posts", "1791257321273779999")
	if err := os.MkdirAll(itemDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(itemDir, "cover.jpg"), h.pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	saved, err := h.backend.LibraryStore().SaveItem(itemDir, library.ItemMetadata{
		Version: 1,
		ID:      "dynamic:1791257321273779999",
		Site:    "x",
		Kind:    "dynamic",
		Status:  "complete",
		Title:   "MetaOnly perf test",
		Author:  library.ItemAuthor{Site: "x", UID: synthXCreatorRestID, UniqueID: synthXCreatorHandle, Name: synthXCreatorName},
		Media:   []library.MediaFile{{Role: "cover", Path: "cover.jpg", Bytes: int64(len(h.pngBytes))}},
	})
	if err != nil {
		t.Fatalf("SaveItem: %v", err)
	}
	metaOnly, ok, err := h.backend.GetLibraryItemMetaOnly(saved.ItemKey)
	if err != nil || !ok || metaOnly.ItemKey != saved.ItemKey || metaOnly.Title != saved.Title {
		t.Fatalf("GetLibraryItemMetaOnly = %+v ok=%v err=%v", metaOnly, ok, err)
	}

	// 2. Insert 3 queued X download tasks with non-empty files_json/attempts_json.
	now := time.Now().UTC().Format(time.RFC3339)
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("dl-perf-x-%d", i)
		key := fmt.Sprintf("x:dynamic:179125732127377800%d", i)
		_, err := h.backend.db.Exec(`
			INSERT INTO download_tasks (
				task_id, site, kind, source, target_id, cid, page, title, part_title,
				mid, author_uid, author_name, author_face, cover_url, cache_linked_videos,
				status, pause_reason, error_kind, error_message, quality_hint,
				bytes_downloaded, total_bytes, progress, item_key, item_dir,
				files_json, attempts_json, created_at, updated_at, via_json
			) VALUES (?, 'x', 'dynamic', 'manual', ?, 0, 1, ?, '',
				0, ?, ?, '', '', 0,
				'queued', '', '', '', '',
				0, 0, 0, ?, '',
				'[{"path":"media_01.jpg","totalBytes":100,"downloaded":10,"status":"queued"}]',
				'[{"attemptNum":1,"filePath":"media_01.jpg"}]', ?, ?, '')
		`, id, fmt.Sprintf("179125732127377800%d", i), id, synthXCreatorRestID, synthXCreatorName, key, now, now)
		if err != nil {
			t.Fatalf("insert perf task %d: %v", i, err)
		}
	}

	// 3. ListDownloadTasks returns summaries (empty Files & Attempts); TaskDetail returns full Files & Attempts.
	summaries, err := h.backend.ListDownloadTasks()
	if err != nil || len(summaries) != 3 {
		t.Fatalf("ListDownloadTasks = %d err=%v", len(summaries), err)
	}
	for _, s := range summaries {
		if len(s.Files) != 0 || len(s.Attempts) != 0 {
			t.Fatalf("ListDownloadTasks summary should omit Files/Attempts: %+v", s)
		}
	}
	fullDetail, ok, err := h.backend.TaskDetail("dl-perf-x-1")
	if err != nil || !ok || len(fullDetail.Task.Files) != 1 || len(fullDetail.Task.Attempts) != 1 {
		t.Fatalf("TaskDetail should return full Files/Attempts: %+v ok=%v err=%v", fullDetail.Task, ok, err)
	}

	// 4. States returns "ring" for queued tasks and "green" for complete library item.
	dots := h.backend.States([]string{saved.ItemKey, "x:dynamic:1791257321273778001"})
	if dots[saved.ItemKey].Dot != "green" || dots["x:dynamic:1791257321273778001"].Dot != "ring" {
		t.Fatalf("States = %+v", dots)
	}

	// 5. Batch pause (PauseForSite) and batch retry (RetryAllTasks) each emit 1 coalesced EventTaskUpdated.
	var taskEvents atomic.Int32
	unsub := h.backend.SubscribeEvents(func(ev Event) {
		if ev.Type == EventTaskUpdated {
			taskEvents.Add(1)
		}
	})
	defer unsub()

	h.backend.dl.PauseForSite(site.X, "network", "batch pause test")
	if got := taskEvents.Load(); got != 1 {
		t.Fatalf("PauseForSite emitted %d EventTaskUpdated events for 3 tasks, want 1 coalesced event", got)
	}
	taskEvents.Store(0)
	res, err := h.backend.RetryAllTasks()
	if err != nil || res.Retried != 3 {
		t.Fatalf("RetryAllTasks = %+v err=%v, want Retried=3", res, err)
	}
	if got := taskEvents.Load(); got != 1 {
		t.Fatalf("RetryAllTasks emitted %d EventTaskUpdated events for 3 tasks, want 1 coalesced event", got)
	}
}
