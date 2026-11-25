package remoteserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/remote"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site/douyin"
	"github.com/chialecode/trace/internal/site/x"
	"github.com/chialecode/trace/internal/sitecheck"
	"github.com/chialecode/trace/internal/subscription"
)

const (
	remoteSentinelXAuth    = "0123456789abcdef0123456789abcdef01234567"
	remoteSentinelXAuth2   = "fedcba9876543210fedcba9876543210fedcba98"
	remoteSentinelXCT0     = "abcdef0123456789abcdef0123456789"
	remoteSentinelXKDT     = "SynthRemXKdtToken0123456789"
	remoteSentinelXBearer  = "SYNTH_REM_X_BEARER_TOKEN_0001"
	remoteXSelfRestID      = "1000000000000000101"
	remoteXSelfHandle      = "rem_x_self"
	remoteXSelfName        = "合成远程X账号"
	remoteXCreatorRestID   = "1000000000000000102"
	remoteXCreatorHandle   = "rem_x_creator"
	remoteXCreatorName     = "合成远程X创作者"
	remoteXTweetBase       = "1791257321273790001"
	remoteXTweetNew        = "1791257321273790002"
	remoteDouyinSelfUID    = "2000000001"
	remoteDouyinSelfSecUID = "MS4wLjABAAAA0123456789abcdef01"
	remoteDouyinSelfName   = "合成远程抖音用户"
)

func remoteXCookie(authToken string) string {
	return "auth_token=" + authToken +
		"; ct0=" + remoteSentinelXCT0 +
		"; twid=u%3D" + remoteXSelfRestID +
		"; guest_id=v1%3A90000101" +
		"; kdt=" + remoteSentinelXKDT +
		"; __cf_bm=DROP_ME_CF_BM; personalization_id=DROP_ME_ADS"
}

type xAbsRewriteTransport struct {
	target *url.URL
	inner  http.RoundTripper
}

func newXAbsRewriteTransport(t *testing.T, ts *httptest.Server) http.RoundTripper {
	t.Helper()
	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("parse fake X server URL: %v", err)
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

func readXSiteFixture(t *testing.T, name string) []byte {
	t.Helper()
	p := filepath.Join("..", "site", "x", "testdata", name)
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return raw
}

func synthRemoteXUserJSON(restID, screenName, name, avatar string, statuses int) string {
	return fmt.Sprintf(`{"__typename":"User","rest_id":%q,"is_blue_verified":false,"core":{"name":%q,"screen_name":%q},"avatar":{"image_url":%q},"profile_bio":{"description":"合成远程X简介"},"legacy":{"statuses_count":%d,"followers_count":100,"friends_count":10,"protected":false}}`,
		restID, name, screenName, avatar, statuses)
}

func synthRemoteXTweetJSON(id, authorRestID, authorHandle, authorName, avatar, text, photoURL string) string {
	author := synthRemoteXUserJSON(authorRestID, authorHandle, authorName, avatar, 20)
	mediaField := ""
	if photoURL != "" {
		mediaField = fmt.Sprintf(`,"extended_entities":{"media":[{"media_key":"3_%s","type":"photo","media_url_https":%q,"original_info":{"width":1,"height":1}}]}`, id, photoURL)
	}
	return fmt.Sprintf(`{"__typename":"Tweet","rest_id":%q,"core":{"user_results":{"result":%s}},"legacy":{"id_str":%q,"user_id_str":%q,"created_at":"Mon Nov 18 10:00:00 +0000 2026","full_text":%q%s}}`,
		id, author, id, authorRestID, text, mediaField)
}

func synthRemoteXTweetEntry(entryID, tweetJSON string) string {
	return fmt.Sprintf(`{"entryId":%q,"content":{"entryType":"TimelineTimelineItem","itemContent":{"itemType":"TimelineTweet","tweetDisplayType":"Tweet","tweet_results":{"result":%s}}}}`, entryID, tweetJSON)
}

type fakeXRemote struct {
	srv                  *httptest.Server
	apiReqs              atomic.Int64
	settingsReqs         atomic.Int64
	userByScreenNameReqs atomic.Int64
	userTweetsReqs       atomic.Int64
	tweetDetailReqs      atomic.Int64
	expired              atomic.Bool
	failStep2            atomic.Bool
	publishNew           atomic.Bool
}

func newFakeXRemote(t *testing.T) *fakeXRemote {
	t.Helper()
	f := &fakeXRemote{}

	// Minimal 1x1 PNG so image downloads validate cleanly.
	pngBytes := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
		0xde, 0x00, 0x00, 0x00, 0x0c, 0x49, 0x44, 0x41,
		0x54, 0x08, 0xd7, 0x63, 0xf8, 0xff, 0xff, 0x3f,
		0x00, 0x05, 0xfe, 0x02, 0xfe, 0xdc, 0xcc, 0x59,
		0xe7, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e,
		0x44, 0xae, 0x42, 0x60, 0x82,
	}
	homeBytes := readXSiteFixture(t, "webpack_home.html")
	mainBytes := bytes.ReplaceAll(
		readXSiteFixture(t, "webpack_main.js"),
		[]byte("SYNTHETIC_WEB_BEARER_TOKEN_0001"),
		[]byte(remoteSentinelXBearer),
	)
	ondemandBytes := readXSiteFixture(t, "webpack_ondemand.js")

	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/home":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(homeBytes)
			return
		case "/responsive-web/client-web/main.synth01a.js":
			if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("X-Csrf-Token") != "" {
				t.Errorf("remote X static main.js request leaked auth headers: cookie=%q auth=%q csrf=%q",
					r.Header.Get("Cookie"), r.Header.Get("Authorization"), r.Header.Get("X-Csrf-Token"))
			}
			w.Header().Set("Content-Type", "application/javascript")
			_, _ = w.Write(mainBytes)
			return
		case "/responsive-web/client-web/ondemand.s.synth02a.js":
			if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("X-Csrf-Token") != "" {
				t.Errorf("remote X static ondemand.js request leaked auth headers: cookie=%q auth=%q csrf=%q",
					r.Header.Get("Cookie"), r.Header.Get("Authorization"), r.Header.Get("X-Csrf-Token"))
			}
			w.Header().Set("Content-Type", "application/javascript")
			_, _ = w.Write(ondemandBytes)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/xcdn/") {
			if r.Header.Get("Cookie") != "" || r.Header.Get("Referer") != "" || r.Header.Get("Authorization") != "" {
				t.Errorf("remote X CDN request leaked headers on %s: cookie=%q referer=%q auth=%q",
					r.URL.Path, r.Header.Get("Cookie"), r.Header.Get("Referer"), r.Header.Get("Authorization"))
			}
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngBytes)
			return
		}

		f.apiReqs.Add(1)
		w.Header().Set("Content-Type", "application/json")
		ck := r.Header.Get("Cookie")
		if strings.Contains(ck, "__cf_bm") || strings.Contains(ck, "personalization_id") {
			t.Errorf("remote X API request did not strip non-whitelisted cookies: %s", r.URL.Path)
		}
		validAuth := strings.Contains(ck, "auth_token="+remoteSentinelXAuth) || strings.Contains(ck, "auth_token="+remoteSentinelXAuth2)
		if f.expired.Load() || !validAuth || r.Header.Get("X-Csrf-Token") != remoteSentinelXCT0 || r.Header.Get("Authorization") != "Bearer "+remoteSentinelXBearer {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = fmt.Fprint(w, `{"errors":[{"code":32,"message":"Could not authenticate you."}]}`)
			return
		}

		cdn := f.srv.URL + "/xcdn"
		switch {
		case r.URL.Path == "/i/api/1.1/account/settings.json":
			f.settingsReqs.Add(1)
			_, _ = fmt.Fprintf(w, `{"screen_name":%q,"protected":false,"language":"en"}`, remoteXSelfHandle)

		case strings.HasSuffix(r.URL.Path, "/"+x.OpUserByScreenName):
			f.userByScreenNameReqs.Add(1)
			if r.Header.Get("X-Client-Transaction-Id") == "" {
				t.Errorf("remote X GraphQL %s missing x-client-transaction-id", r.URL.Path)
			}
			if f.failStep2.Load() {
				// Return a mismatched user so Step 2 cross-check rejects the candidate cookie.
				_, _ = fmt.Fprintf(w, `{"data":{"user":{"result":%s}}}`,
					synthRemoteXUserJSON("1000000000000000999", "wrong_x_user", "Wrong User", cdn+"/wrong.png", 1))
				return
			}
			vars := r.URL.Query().Get("variables")
			if strings.Contains(vars, remoteXCreatorHandle) {
				_, _ = fmt.Fprintf(w, `{"data":{"user":{"result":%s}}}`,
					synthRemoteXUserJSON(remoteXCreatorRestID, remoteXCreatorHandle, remoteXCreatorName, cdn+"/creator.png", 20))
				return
			}
			_, _ = fmt.Fprintf(w, `{"data":{"user":{"result":%s}}}`,
				synthRemoteXUserJSON(remoteXSelfRestID, remoteXSelfHandle, remoteXSelfName, cdn+"/self.png", 5))

		case strings.HasSuffix(r.URL.Path, "/"+x.OpUserByRestId):
			vars := r.URL.Query().Get("variables")
			if strings.Contains(vars, remoteXSelfRestID) {
				_, _ = fmt.Fprintf(w, `{"data":{"user":{"result":%s}}}`,
					synthRemoteXUserJSON(remoteXSelfRestID, remoteXSelfHandle, remoteXSelfName, cdn+"/self.png", 5))
				return
			}
			_, _ = fmt.Fprintf(w, `{"data":{"user":{"result":%s}}}`,
				synthRemoteXUserJSON(remoteXCreatorRestID, remoteXCreatorHandle, remoteXCreatorName, cdn+"/creator.png", 20))

		case strings.HasSuffix(r.URL.Path, "/"+x.OpUserTweets):
			f.userTweetsReqs.Add(1)
			if r.Header.Get("X-Client-Transaction-Id") == "" {
				t.Errorf("remote X GraphQL %s missing x-client-transaction-id", r.URL.Path)
			}
			baseTweet := synthRemoteXTweetJSON(
				remoteXTweetBase, remoteXCreatorRestID, remoteXCreatorHandle, remoteXCreatorName,
				cdn+"/creator.png", "远程基线X帖子", cdn+"/base.png",
			)
			entries := synthRemoteXTweetEntry("tweet-"+remoteXTweetBase, baseTweet)
			if f.publishNew.Load() {
				newTweet := synthRemoteXTweetJSON(
					remoteXTweetNew, remoteXCreatorRestID, remoteXCreatorHandle, remoteXCreatorName,
					cdn+"/creator.png", "远程自动缓存新X图文帖子", cdn+"/new_pic.png",
				)
				entries = synthRemoteXTweetEntry("tweet-"+remoteXTweetNew, newTweet) + "," + entries
			}
			_, _ = fmt.Fprintf(w, `{"data":{"user":{"result":{"__typename":"User","timeline_v2":{"timeline":{"instructions":[{"type":"TimelineAddEntries","entries":[%s]}]}}}}}}`, entries)

		case strings.HasSuffix(r.URL.Path, "/"+x.OpTweetDetail):
			f.tweetDetailReqs.Add(1)
			if r.Header.Get("X-Client-Transaction-Id") == "" {
				t.Errorf("remote X GraphQL %s missing x-client-transaction-id", r.URL.Path)
			}
			vars := r.URL.Query().Get("variables")
			id := remoteXTweetNew
			text := "远程自动缓存新X图文帖子"
			pic := cdn + "/new_pic.png"
			if strings.Contains(vars, remoteXTweetBase) {
				id = remoteXTweetBase
				text = "远程基线X帖子"
				pic = cdn + "/base.png"
			}
			tw := synthRemoteXTweetJSON(id, remoteXCreatorRestID, remoteXCreatorHandle, remoteXCreatorName, cdn+"/creator.png", text, pic)
			_, _ = fmt.Fprintf(w, `{"data":{"threaded_conversation_with_injections_v2":{"instructions":[{"type":"TimelineAddEntries","entries":[%s]}]}}}`,
				synthRemoteXTweetEntry("tweet-"+id, tw))

		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeXRemote) options(t *testing.T, bo *service.Options) {
	t.Helper()
	fixedNow := time.Unix(1795327200, 0).UTC()
	bo.XOpts = x.ClientOptions{
		Endpoints: x.Endpoints{
			HomeURL:         f.srv.URL + "/home",
			AccountSettings: f.srv.URL + "/i/api/1.1/account/settings.json",
			GraphQLBase:     f.srv.URL + "/i/api/graphql",
		},
		MinInterval:        -1,
		RateLimitJitterMax: -1,
		AllowLocalhost:     true,
		AssetTransport:     newXAbsRewriteTransport(t, f.srv),
		Now:                func() time.Time { return fixedNow },
		Sleep:              func(context.Context, time.Duration) error { return nil },
		TxIDOptions: []x.TxIDOption{
			x.WithTxIDClock(func() time.Time { return fixedNow }),
			x.WithTxIDRandByte(func() (byte, error) { return 0x42, nil }),
		},
	}
}

type fakeDouyinRemote struct {
	srv  *httptest.Server
	reqs atomic.Int64
}

func newFakeDouyinRemote(t *testing.T) *fakeDouyinRemote {
	t.Helper()
	f := &fakeDouyinRemote{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.reqs.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/aweme/v1/web/user/profile/self/" && strings.Contains(r.Header.Get("Cookie"), "sessionid=") {
			_, _ = fmt.Fprintf(w, `{"status_code":0,"user":{"uid":%q,"sec_uid":%q,"unique_id":"synth_dy_self","nickname":%q}}`,
				remoteDouyinSelfUID, remoteDouyinSelfSecUID, remoteDouyinSelfName)
			return
		}
		_, _ = fmt.Fprint(w, `{"status_code":8,"status_msg":"未登录"}`)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeDouyinRemote) options(bo *service.Options) {
	bo.DouyinOpts = douyin.ClientOptions{
		Endpoints: douyin.Endpoints{
			ProfileSelf:  f.srv.URL + "/aweme/v1/web/user/profile/self/",
			ProfileOther: f.srv.URL + "/aweme/v1/web/user/profile/other/",
			AwemeDetail:  f.srv.URL + "/aweme/v1/web/aweme/detail/",
			AwemePost:    f.srv.URL + "/aweme/v1/web/aweme/post/",
			LiveWebEnter: f.srv.URL + "/webcast/room/web/enter/",
		},
		MinInterval:    -1,
		AllowLocalhost: true,
	}
}

func assertNoXSecretLeaks(t *testing.T, root string, sentinels []string, namedPayloads map[string]string) {
	t.Helper()
	for name, text := range namedPayloads {
		if findings := sitecheck.ScanTextForSecrets(text); len(findings) > 0 {
			t.Errorf("sitecheck found leaked secret in %s: %v", name, findings)
		}
		for _, s := range sentinels {
			if s != "" && strings.Contains(text, s) {
				t.Errorf("sentinel %q leaked into %s", s[:min(8, len(s))], name)
			}
		}
	}
	if root == "" {
		return
	}
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		raw, rErr := os.ReadFile(p)
		if rErr != nil {
			return nil
		}
		if findings := sitecheck.ScanTextForSecrets(string(raw)); len(findings) > 0 {
			t.Errorf("sitecheck found leaked secret in file %s: %v", p, findings)
		}
		for _, s := range sentinels {
			if s != "" && bytes.Contains(raw, []byte(s)) {
				t.Errorf("sentinel %q leaked into file %s", s[:min(8, len(s))], p)
			}
		}
		return nil
	})
}

// Verifies PUT /api/v1/credentials/x enforces 2-step verification (settings.json + UserByScreenName)
// before saving, leaves stored credentials unchanged on local/step-1/step-2 failures, reports all
// 4 sites (bilibili, douyin, weibo, x) in /api/v1/status, supports DELETE /api/v1/credentials/x,
// and never leaks X cookie, CSRF, Bearer, or remote token sentinels.
func TestRemoteXCredentialPushTwoStepVerifyRejectDeleteAndFourSiteStatus(t *testing.T) {
	dataDir, res := initServer(t)
	cfg, err := LoadConfig(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	fx := newFakeXRemote(t)
	var logBuf syncBuffer
	bo := service.Options{
		Locations:        service.RemoteLocations(dataDir, cfg.ExportDir),
		Desktop:          service.NewMockDesktop(),
		Profile:          service.ProfileRemote,
		NodeName:         cfg.Name,
		DisableBgWorkers: true,
		LogMirror:        &logBuf,
	}
	fx.options(t, &bo)
	b, err := service.NewBackend(bo)
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })

	s := New(Options{DataDir: dataDir, TokenHash: cfg.TokenHash, Service: b})
	h := httptest.NewServer(s)
	t.Cleanup(h.Close)

	doReq := func(method, path string, body []byte) (int, []byte) {
		t.Helper()
		req, _ := http.NewRequest(method, h.URL+path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+res.Conn.Token)
		req.Header.Set(remote.HeaderProtocol, remote.ProtocolVersion)
		resp, err := h.Client().Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, raw
	}

	// Unknown site must return 404.
	badSiteBody, _ := json.Marshal(remote.CredentialPush{Cookie: remoteXCookie(remoteSentinelXAuth)})
	if code, _ := doReq("PUT", "/api/v1/credentials/unknown", badSiteBody); code != http.StatusNotFound {
		t.Fatalf("PUT unknown site = %d, want 404", code)
	}

	// Missing auth_token or ct0 must be rejected locally with 0 upstream X requests and 0 echoed secrets.
	missingCT0Secret := "9988776655443322110099887766554433221100"
	missingBody, _ := json.Marshal(remote.CredentialPush{Cookie: "auth_token=" + missingCT0Secret})
	code, missingRaw := doReq("PUT", "/api/v1/credentials/x", missingBody)
	if code != http.StatusUnprocessableEntity || fx.apiReqs.Load() != 0 {
		t.Fatalf("PUT missing ct0 = %d reqs=%d raw=%s, want 422 with 0 upstream reqs", code, fx.apiReqs.Load(), missingRaw)
	}
	if bytes.Contains(missingRaw, []byte(missingCT0Secret)) {
		t.Fatalf("rejected response echoed missing-ct0 auth_token: %s", missingRaw)
	}

	// Valid X cookie push runs 2-step verification (settings.json + UserByScreenName) before saving.
	pushBody, _ := json.Marshal(remote.CredentialPush{Cookie: remoteXCookie(remoteSentinelXAuth)})
	code, pushRaw := doReq("PUT", "/api/v1/credentials/x", pushBody)
	if code != http.StatusOK {
		t.Fatalf("PUT x valid = %d %s", code, pushRaw)
	}
	if fx.settingsReqs.Load() < 1 || fx.userByScreenNameReqs.Load() < 1 {
		t.Fatalf("expected 2-step X verification, got settingsReqs=%d userByScreenNameReqs=%d",
			fx.settingsReqs.Load(), fx.userByScreenNameReqs.Load())
	}
	var pushed remote.SiteStatus
	if err := json.Unmarshal(pushRaw, &pushed); err != nil {
		t.Fatalf("unmarshal pushed SiteStatus: %v", err)
	}
	if pushed.Site != "x" || pushed.State != remote.SiteValid || pushed.Uname != remoteXSelfName || pushed.CookieSyncedAt == "" {
		t.Fatalf("pushed x status = %+v", pushed)
	}
	if st := b.StatusOf("x"); !st.LoggedIn || st.Source != service.CredentialSourceRemotePush || st.UID != remoteXSelfRestID || st.UniqueID != remoteXSelfHandle {
		t.Fatalf("stored x status = %+v", st)
	}
	if ren := b.Renewal("x"); ren.Available {
		t.Fatalf("remote X renewal should be unavailable, got %+v", ren)
	}

	// Step 1 rejection (invalid auth_token) returns 422, echoes no secret, and leaves stored X login unchanged.
	rejAuthSecret := "1111222233334444555566667777888899990000"
	rejCT0Secret := "00009999888877776666555544443333"
	rejBody, _ := json.Marshal(remote.CredentialPush{Cookie: "auth_token=" + rejAuthSecret + "; ct0=" + rejCT0Secret})
	code, rejRaw := doReq("PUT", "/api/v1/credentials/x", rejBody)
	var eb remote.ErrorBody
	_ = json.Unmarshal(rejRaw, &eb)
	if code != http.StatusUnprocessableEntity || eb.Error.Code != remote.CodeCredentialRejected ||
		bytes.Contains(rejRaw, []byte(rejAuthSecret)) || bytes.Contains(rejRaw, []byte(rejCT0Secret)) {
		t.Fatalf("rejected step-1 x push = %d %s", code, rejRaw)
	}
	if st := b.StatusOf("x"); !st.LoggedIn || st.UID != remoteXSelfRestID {
		t.Fatalf("step-1 rejected push altered stored x login: %+v", st)
	}

	// Step 2 rejection (UserByScreenName screen_name/twid mismatch) returns 422 and leaves stored X login unchanged.
	fx.failStep2.Store(true)
	step2Body, _ := json.Marshal(remote.CredentialPush{Cookie: remoteXCookie(remoteSentinelXAuth2)})
	code, step2Raw := doReq("PUT", "/api/v1/credentials/x", step2Body)
	fx.failStep2.Store(false)
	var eb2 remote.ErrorBody
	_ = json.Unmarshal(step2Raw, &eb2)
	if code != http.StatusUnprocessableEntity || eb2.Error.Code != remote.CodeCredentialRejected {
		t.Fatalf("rejected step-2 x push = %d %s, want 422 credential_rejected", code, step2Raw)
	}
	if st := b.StatusOf("x"); !st.LoggedIn || st.UID != remoteXSelfRestID || st.UniqueID != remoteXSelfHandle {
		t.Fatalf("step-2 rejected push altered stored x login: %+v", st)
	}

	// Step 2 twid mismatch (valid auth_token + matching screen_name, but wrong twid rest_id) is also rejected without altering stored login.
	badTWIDCookie := "auth_token=" + remoteSentinelXAuth2 + "; ct0=" + remoteSentinelXCT0 + "; twid=u%3D1000000000000000999"
	twidBody, _ := json.Marshal(remote.CredentialPush{Cookie: badTWIDCookie})
	code, twidRaw := doReq("PUT", "/api/v1/credentials/x", twidBody)
	var ebTWID remote.ErrorBody
	_ = json.Unmarshal(twidRaw, &ebTWID)
	if code != http.StatusUnprocessableEntity || ebTWID.Error.Code != remote.CodeCredentialRejected {
		t.Fatalf("rejected twid-mismatch x push = %d %s, want 422 credential_rejected", code, twidRaw)
	}
	if st := b.StatusOf("x"); !st.LoggedIn || st.UID != remoteXSelfRestID || st.UniqueID != remoteXSelfHandle {
		t.Fatalf("twid-mismatch rejected push altered stored x login: %+v", st)
	}

	// GET /api/v1/status lists all 4 sites (bilibili, douyin, weibo, x) with x valid.
	code, statusRaw := doReq("GET", "/api/v1/status", nil)
	if code != http.StatusOK {
		t.Fatalf("GET status = %d %s", code, statusRaw)
	}
	var st remote.Status
	if err := json.Unmarshal(statusRaw, &st); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}
	seenSites := make(map[string]remote.SiteStatus, len(st.Sites))
	for _, row := range st.Sites {
		seenSites[row.Site] = row
	}
	for _, want := range []string{"bilibili", "douyin", "weibo", "x"} {
		if _, ok := seenSites[want]; !ok {
			t.Fatalf("GET /api/v1/status missing site %q: %+v", want, st.Sites)
		}
	}
	if seenSites["x"].State != remote.SiteValid || seenSites["x"].Uname != remoteXSelfName || seenSites["x"].CookieSyncedAt == "" {
		t.Fatalf("status x row = %+v, want valid with uname & cookieSyncedAt", seenSites["x"])
	}

	// DELETE /api/v1/credentials/x clears X login cleanly.
	code, delRaw := doReq("DELETE", "/api/v1/credentials/x", nil)
	if code != http.StatusOK {
		t.Fatalf("DELETE x credential = %d %s, want 200", code, delRaw)
	}
	var cleared remote.SiteStatus
	if err := json.Unmarshal(delRaw, &cleared); err != nil || cleared.Site != "x" || cleared.State != remote.SiteNone {
		t.Fatalf("DELETE x response = %s (%v), want site=x state=none", delRaw, err)
	}
	if b.StatusOf("x").LoggedIn {
		t.Fatal("X still logged in after DELETE credential")
	}

	sentinels := []string{
		res.Conn.Token,
		remoteSentinelXAuth,
		remoteSentinelXAuth2,
		remoteSentinelXCT0,
		remoteSentinelXKDT,
		remoteSentinelXBearer,
		missingCT0Secret,
		rejAuthSecret,
		rejCT0Secret,
	}
	assertNoXSecretLeaks(t, filepath.Dir(dataDir), sentinels, map[string]string{
		"push_response":   string(pushRaw),
		"reject_response": string(rejRaw),
		"step2_response":  string(step2Raw),
		"twid_response":   string(twidRaw),
		"status_response": string(statusRaw),
		"delete_response": string(delRaw),
		"log_mirror":      logBuf.String(),
	})
}

// Verifies remote X subscription sync preserves watermarks across re-syncs, scheduled check +
// rule-based auto-cache downloads new X posts into ExportDir while NEVER running full-cache,
// expired X auth stops all subsequent X requests without affecting bilibili/douyin/weibo,
// and zero X secrets leak into status, logs, or disk files.
func TestRemoteXSubscriptionSyncWatermarksScheduledCheckAutoCacheNoFullCacheAndExpiredIsolation(t *testing.T) {
	dataDir, _ := initServer(t)
	cfg, err := LoadConfig(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	bili := newFakeBili(t)
	dy := newFakeDouyinRemote(t)
	wb := newFakeWeiboRemote(t)
	fx := newFakeXRemote(t)

	var logBuf syncBuffer
	bo := service.Options{
		Locations:        service.RemoteLocations(dataDir, cfg.ExportDir),
		Desktop:          service.NewMockDesktop(),
		Profile:          service.ProfileRemote,
		NodeName:         cfg.Name,
		DisableBgWorkers: false,
		LogMirror:        &logBuf,
	}
	bili.options(&bo)
	dy.options(&bo)
	wb.options(&bo)
	fx.options(t, &bo)
	b, err := service.NewBackend(bo)
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	ctx := context.Background()

	// Log all 4 sites into the remote via AcceptPushedCredential so cross-site isolation is proven.
	if _, err := b.AcceptPushedCredential(ctx, "bilibili", sentinelCookie()); err != nil {
		t.Fatalf("AcceptPushedCredential(bilibili): %v", err)
	}
	if _, err := b.AcceptPushedCredential(ctx, "douyin", "sessionid=synth_rem_dy_sess_01; ttwid=synth_rem_dy_ttwid_01"); err != nil {
		t.Fatalf("AcceptPushedCredential(douyin): %v", err)
	}
	if _, err := b.AcceptPushedCredential(ctx, "weibo", remoteWeiboCookie()); err != nil {
		t.Fatalf("AcceptPushedCredential(weibo): %v", err)
	}
	if _, err := b.AcceptPushedCredential(ctx, "x", remoteXCookie(remoteSentinelXAuth)); err != nil {
		t.Fatalf("AcceptPushedCredential(x): %v", err)
	}

	// Sync a multi-site subscription snapshot with X (FullCache=true + ScheduledCheck=true + AutoCache=true)
	// and Weibo (ScheduledCheck=true).
	snap := subscription.Snapshot{
		Format:  subscription.SnapshotFormat,
		Version: subscription.SnapshotVersion,
		Subscriptions: []subscription.SnapshotSubscription{
			{
				ID:   "sub_x",
				Name: "合成X订阅",
				Tags: []string{
					subscription.TagScheduledCheck,
					subscription.TagCacheVideo,
					subscription.TagCacheDraw,
					subscription.TagCacheWord,
					subscription.TagFullCache,
				},
				AutoCacheSince: "2026-11-18T00:00:00Z",
				Accounts: []subscription.SnapshotAccount{{
					Site:     "x",
					UID:      remoteXCreatorRestID,
					UniqueID: remoteXCreatorHandle,
					Name:     remoteXCreatorName,
				}},
			},
			{
				ID:   "sub_wb",
				Name: "合成微博订阅",
				Tags: []string{subscription.TagScheduledCheck},
				Accounts: []subscription.SnapshotAccount{{
					Site: "weibo",
					UID:  remoteWeiboCreatorUID,
					Name: remoteWeiboCreatorName,
				}},
			},
		},
	}
	snapRaw, _ := json.Marshal(snap)
	info, err := b.ApplyRemoteSnapshot(snapRaw)
	if err != nil || info.Subscriptions != 2 || info.Accounts != 2 {
		t.Fatalf("ApplyRemoteSnapshot = %+v err=%v", info, err)
	}

	// Remote must note that full_cache never runs on remote, keep cache queue empty, and reject EnqueueCacheQueue.
	stBefore := b.ServerStatus()
	hasFullCacheNote := false
	for _, note := range stBefore.Notes {
		if strings.Contains(note, "全部缓存") {
			hasFullCacheNote = true
		}
	}
	if !hasFullCacheNote {
		t.Fatalf("expected ServerStatus.Notes to mention 全部缓存 disabled on remote, got %v", stBefore.Notes)
	}
	if len(b.ListCacheQueue()) != 0 {
		t.Fatalf("remote unexpectedly started cache queues: %+v", b.ListCacheQueue())
	}
	if _, err := b.EnqueueCacheQueue(ctx, service.CacheQueueRequest{
		Site: "x", List: "posts", User: remoteXCreatorRestID,
	}); !errors.Is(err, service.ErrRemoteUnsupported) {
		t.Fatalf("EnqueueCacheQueue(x) on remote = %v, want ErrRemoteUnsupported", err)
	}

	// Scheduled round 1 establishes baseline watermark for X (and Weibo) with 0 new items cached.
	sum1, err := b.SubscriptionManager().RunScheduledRound(ctx)
	if err != nil {
		t.Fatalf("scheduled round 1: %v", err)
	}
	if sum1.NewVideos+sum1.NewDynamics != 0 {
		t.Fatalf("baseline scheduled round reported %+v, want 0 new items", sum1)
	}
	if fx.userTweetsReqs.Load() == 0 {
		t.Fatal("scheduled round 1 did not query X UserTweets")
	}

	// Re-applying the snapshot must preserve the established X baseline watermark.
	if _, err := b.ApplyRemoteSnapshot(snapRaw); err != nil {
		t.Fatalf("re-apply snapshot: %v", err)
	}
	xCheckFound := false
	for _, c := range b.ServerStatus().Checks {
		if c.Site == "x" && c.UID == remoteXCreatorRestID {
			xCheckFound = true
			if c.LastCheckedAt == "" {
				t.Fatalf("re-applying snapshot wiped X check state: %+v", c)
			}
		}
	}
	if !xCheckFound {
		t.Fatalf("X account missing from ServerStatus().Checks: %+v", b.ServerStatus().Checks)
	}
	// A check before any new tweet is published must still report 0 new items (watermark preserved, not re-baselined).
	sumNoChange, err := b.SubscriptionManager().RunScheduledRound(ctx)
	if err != nil || sumNoChange.NewVideos+sumNoChange.NewDynamics != 0 || sumNoChange.BaselineInitialized != 0 {
		t.Fatalf("post-resync check without new post = %+v err=%v, want 0 new and 0 baseline re-init", sumNoChange, err)
	}

	// Publish a new X image post; scheduled check round detects it and auto-caches it into ExportDir.
	fx.publishNew.Store(true)
	sum2, err := b.SubscriptionManager().RunScheduledRound(ctx)
	if err != nil {
		t.Fatalf("scheduled round 2: %v", err)
	}
	if sum2.NewVideos+sum2.NewDynamics != 1 {
		t.Fatalf("scheduled round 2 summary = %+v, want 1 new X post", sum2)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, ok, _ := b.LibraryStore().GetItem("x:dynamic:" + remoteXTweetNew); ok {
			break
		}
		tasks, _ := b.ListDownloadTasks()
		for _, tk := range tasks {
			if tk.Status == "failed" {
				t.Fatalf("remote X auto-cache task failed: %+v", tk)
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("auto-cached X post did not finish; sum2=%+v tasks=%+v logs=%s", sum2, tasks, logBuf.String())
		}
		time.Sleep(25 * time.Millisecond)
	}
	cachedItem, ok, err := b.LibraryStore().GetItem("x:dynamic:" + remoteXTweetNew)
	if err != nil || !ok || !strings.HasPrefix(filepath.Clean(cachedItem.ItemDir), filepath.Clean(cfg.ExportDir)) {
		t.Fatalf("auto-cached X item = %+v ok=%v err=%v (want inside %s)", cachedItem, ok, err, cfg.ExportDir)
	}
	if len(b.ListCacheQueue()) != 0 {
		t.Fatalf("remote unexpectedly created full-cache queues after auto-cache: %+v", b.ListCacheQueue())
	}

	// When X auth expires on remote, remote marks ONLY X as resync, stops all future X requests,
	// and leaves bilibili, douyin, and weibo completely untouched and operational!
	fx.expired.Store(true)
	_, _ = b.SubscriptionManager().RunScheduledRound(ctx)

	stAfter := b.ServerStatus()
	siteMap := make(map[string]remote.SiteStatus, len(stAfter.Sites))
	for _, r := range stAfter.Sites {
		siteMap[r.Site] = r
	}
	if siteMap["x"].State != remote.SiteResync || siteMap["x"].Message == "" {
		t.Fatalf("expired remote x status = %+v, want resync with message", siteMap["x"])
	}
	for _, other := range []string{"bilibili", "douyin", "weibo"} {
		if siteMap[other].State != remote.SiteValid {
			t.Fatalf("X expiry affected %s status: %+v, want valid", other, siteMap[other])
		}
	}

	// Subsequent scheduled round and subscription check must make 0 HTTP requests to X,
	// while Bilibili, Douyin, and Weibo continue working normally!
	xReqsAfterExpire := fx.apiReqs.Load()
	wbReqsBefore := wb.reqs.Load()
	biliReqsBefore := bili.reqs.Load()
	dyReqsBefore := dy.reqs.Load()

	_, _ = b.SubscriptionManager().RunScheduledRound(ctx)
	_, _ = b.CheckSubscription(ctx, "sub_x")
	if delta := fx.apiReqs.Load() - xReqsAfterExpire; delta != 0 {
		t.Fatalf("expired remote X still sent %d HTTP requests, want 0", delta)
	}
	if wb.reqs.Load() <= wbReqsBefore {
		t.Fatalf("Weibo scheduled check did not run while X was expired (wbReqs=%d)", wb.reqs.Load())
	}
	if st, err := b.VerifySiteAccount(ctx, "bilibili"); err != nil || !st.LoggedIn || bili.reqs.Load() <= biliReqsBefore {
		t.Fatalf("Bilibili verify affected by X expiry: st=%+v err=%v reqs=%d", st, err, bili.reqs.Load())
	}
	if st, err := b.VerifySiteAccount(ctx, "douyin"); err != nil || !st.LoggedIn || dy.reqs.Load() <= dyReqsBefore {
		t.Fatalf("Douyin verify affected by X expiry: st=%+v err=%v reqs=%d", st, err, dy.reqs.Load())
	}

	stRaw, _ := json.Marshal(stAfter)
	sentinels := []string{
		remoteSentinelXAuth,
		remoteSentinelXCT0,
		remoteSentinelXKDT,
		remoteSentinelXBearer,
		remoteSentinelWeiboSUB,
		remoteSentinelWeiboSUBP,
		remoteSentinelWeiboXSRF,
		remoteSentinelWeiboWBPSESS,
		sentinelSess,
		sentinelCSRF,
	}
	assertNoXSecretLeaks(t, filepath.Dir(dataDir), sentinels, map[string]string{
		"server_status": string(stRaw),
		"log_mirror":    logBuf.String(),
	})
}

// Verifies desktop-to-remote integration over real TLS 1.3 pinned HTTPS:
// local X login and re-login NEVER auto-push to remote (0 PUT /credentials/x requests),
// failed manual X push NEVER queues background pending retry, while explicit manual
// PushCredentialToRemote("x") after user confirmation succeeds, subscription sync includes X,
// ClearRemoteCredential("x") leaves local desktop X login intact, and zero secrets leak.
func TestDesktopToRemoteXNoAutoPushAndExplicitManualPush(t *testing.T) {
	dataDir, res := initServer(t)
	cfg, err := LoadConfig(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	bili := newFakeBili(t)
	fx := newFakeXRemote(t)

	var remoteLogBuf syncBuffer
	remBO := service.Options{
		Locations:        service.RemoteLocations(dataDir, cfg.ExportDir),
		Desktop:          service.NewMockDesktop(),
		Profile:          service.ProfileRemote,
		NodeName:         cfg.Name,
		DisableBgWorkers: true,
		LogMirror:        &remoteLogBuf,
	}
	bili.options(&remBO)
	fx.options(t, &remBO)
	remBackend, err := service.NewBackend(remBO)
	if err != nil {
		t.Fatalf("NewBackend(remote): %v", err)
	}
	t.Cleanup(func() { _ = remBackend.Close() })

	srv := New(Options{DataDir: dataDir, TokenHash: cfg.TokenHash, Service: remBackend})
	tlsCfg, err := TLSConfig(dataDir)
	if err != nil {
		t.Fatal(err)
	}

	var down atomic.Bool
	var xPutReqs atomic.Int64
	var biliPutReqs atomic.Int64
	httpsSrv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		if r.Method == http.MethodPut && r.URL.Path == "/api/v1/credentials/x" {
			xPutReqs.Add(1)
		}
		if r.Method == http.MethodPut && r.URL.Path == "/api/v1/credentials/bilibili" {
			biliPutReqs.Add(1)
		}
		srv.ServeHTTP(w, r)
	}))
	httpsSrv.TLS = tlsCfg
	httpsSrv.StartTLS()
	t.Cleanup(httpsSrv.Close)

	conn := res.Conn
	conn.URL = httpsSrv.URL
	code := remote.EncodeCode(conn)

	desktopTmp := t.TempDir()
	desktopLoc, err := appdirs.ResolveWithOptions(appdirs.Options{
		Channel:   "dev",
		ForceMode: "portable",
		DataDir:   filepath.Join(desktopTmp, "data"),
		CacheDir:  filepath.Join(desktopTmp, "cache"),
	})
	if err != nil {
		t.Fatal(err)
	}
	var desktopLogBuf syncBuffer
	deskBO := service.Options{
		Locations:        desktopLoc,
		Desktop:          service.NewMockDesktop(),
		DisableBgWorkers: true,
		LogMirror:        &desktopLogBuf,
	}
	bili.options(&deskBO)
	fx.options(t, &deskBO)
	desktop, err := service.NewBackend(deskBO)
	if err != nil {
		t.Fatalf("NewBackend(desktop): %v", err)
	}
	t.Cleanup(func() { _ = desktop.Close() })

	ctx := context.Background()
	if _, err := desktop.ConnectRemote(ctx, service.RemoteConnectInput{Code: code}); err != nil {
		t.Fatalf("ConnectRemote: %v", err)
	}

	// 1. Local X login AND re-login must NEVER auto-push to remote (0 PUT /credentials/x requests).
	if _, err := desktop.LoginWithCookie(ctx, "x", remoteXCookie(remoteSentinelXAuth), ""); err != nil {
		t.Fatalf("local X login 1: %v", err)
	}
	if _, err := desktop.LoginWithCookie(ctx, "x", remoteXCookie(remoteSentinelXAuth2), ""); err != nil {
		t.Fatalf("local X re-login 2: %v", err)
	}
	if _, err := desktop.RemoteStatus(ctx); err != nil {
		t.Fatalf("RemoteStatus after local X login: %v", err)
	}
	if desktop.RetryPendingRemoteCookies(ctx) {
		t.Fatal("RetryPendingRemoteCookies unexpectedly pushed after X login")
	}
	time.Sleep(50 * time.Millisecond)

	if xPutReqs.Load() != 0 {
		t.Fatalf("local X login/re-login sent %d remote PUT /credentials/x requests, want 0", xPutReqs.Load())
	}
	if info, ok := desktop.RemoteConnection(); !ok || len(info.PendingCookies) != 0 {
		t.Fatalf("PendingCookies after local X login = %+v (ok=%v), want empty", info.PendingCookies, ok)
	}
	remoteRow := func(siteID string) remote.SiteStatus {
		for _, r := range srv.Status().Sites {
			if r.Site == siteID {
				return r
			}
		}
		t.Fatalf("missing remote status row for %q", siteID)
		return remote.SiteStatus{}
	}
	if st := remoteRow("x"); st.State != remote.SiteNone {
		t.Fatalf("remote x state after local X login = %+v, want none", st)
	}

	// 2. Control: Bilibili local login DOES auto-push, proving auto-push is active for non-X sites only.
	if _, err := desktop.ImportCookie(ctx, sentinelCookie(), ""); err != nil {
		t.Fatalf("local bilibili login: %v", err)
	}
	waitFor(t, "bilibili automatic push", func() bool {
		return biliPutReqs.Load() >= 1 && remoteRow("bilibili").State == remote.SiteValid &&
			strings.Contains(desktopLogBuf.String(), "cookie pushed to the remote: site=bilibili")
	})
	if xPutReqs.Load() != 0 {
		t.Fatalf("X PUT count became %d after bilibili auto-push, want 0", xPutReqs.Load())
	}

	// 3. A failed manual X push (remote temporarily down) must NOT mark X as pending for background retry.
	down.Store(true)
	if _, err := desktop.PushCredentialToRemote(ctx, "x"); err == nil {
		t.Fatal("manual X push to down remote unexpectedly succeeded")
	}
	if info, _ := desktop.RemoteConnection(); len(info.PendingCookies) != 0 {
		t.Fatalf("failed manual X push queued PendingCookies = %+v, want empty", info.PendingCookies)
	}
	down.Store(false)
	if desktop.RetryPendingRemoteCookies(ctx) {
		t.Fatal("RetryPendingRemoteCookies pushed X after failed manual push")
	}
	if _, err := desktop.RemoteStatus(ctx); err != nil {
		t.Fatalf("RemoteStatus after recovery: %v", err)
	}
	if xPutReqs.Load() != 0 {
		t.Fatalf("background retry silently sent %d PUT /credentials/x requests, want 0", xPutReqs.Load())
	}

	// 4. Explicit manual PushCredentialToRemote("x") (after user risk confirmation) succeeds via 2-step check.
	pushed, err := desktop.PushCredentialToRemote(ctx, "x")
	if err != nil {
		t.Fatalf("explicit PushCredentialToRemote(x): %v", err)
	}
	if xPutReqs.Load() != 1 {
		t.Fatalf("xPutReqs = %d, want 1", xPutReqs.Load())
	}
	if pushed.Site != "x" || pushed.State != remote.SiteValid || pushed.Uname != remoteXSelfName || pushed.CookieSyncedAt == "" {
		t.Fatalf("pushed x status = %+v", pushed)
	}
	if st := remBackend.StatusOf("x"); !st.LoggedIn || st.Source != service.CredentialSourceRemotePush || st.UID != remoteXSelfRestID {
		t.Fatalf("remote stored x credential = %+v", st)
	}

	// 5. Desktop syncs X subscription to remote and both sides report InSync.
	sub, err := desktop.CreateSubscription("桌面同步X订阅", "", []subscription.AccountRef{{
		Site:     "x",
		UID:      remoteXCreatorRestID,
		UniqueID: remoteXCreatorHandle,
		Name:     remoteXCreatorName,
	}}, subscription.DefaultSubRules())
	if err != nil {
		t.Fatalf("CreateSubscription(x): %v", err)
	}
	syncInfo, err := desktop.SyncSubscriptionsToRemote(ctx)
	if err != nil || syncInfo.Subscriptions != 1 || syncInfo.Accounts != 1 {
		t.Fatalf("SyncSubscriptionsToRemote(x) = %+v err=%v", syncInfo, err)
	}
	view, err := desktop.RemoteStatus(ctx)
	if err != nil || !view.InSync {
		t.Fatalf("RemoteStatus after sync = %+v err=%v, want InSync=true", view, err)
	}
	remSubs, _ := remBackend.ListSubscriptions()
	if len(remSubs) != 1 || remSubs[0].ID != sub.ID || len(remSubs[0].Accounts) != 1 ||
		remSubs[0].Accounts[0].Site != "x" || remSubs[0].Accounts[0].UID != remoteXCreatorRestID {
		t.Fatalf("remote synced X subscriptions = %+v", remSubs)
	}

	// 6. Clearing remote X credential leaves desktop local X login intact.
	if err := desktop.ClearRemoteCredential(ctx, "x"); err != nil {
		t.Fatalf("ClearRemoteCredential(x): %v", err)
	}
	if st := remoteRow("x"); st.State != remote.SiteNone {
		t.Fatalf("remote x status after ClearRemoteCredential = %+v, want none", st)
	}
	if !desktop.StatusOf("x").LoggedIn {
		t.Fatal("ClearRemoteCredential(x) unexpectedly cleared desktop local X login")
	}

	statusRaw, _ := json.Marshal(view.Status)
	sentinels := []string{
		res.Conn.Token,
		remoteSentinelXAuth,
		remoteSentinelXAuth2,
		remoteSentinelXCT0,
		remoteSentinelXKDT,
		remoteSentinelXBearer,
		sentinelSess,
		sentinelCSRF,
	}
	assertNoXSecretLeaks(t, filepath.Dir(dataDir), sentinels, map[string]string{
		"remote_status": string(statusRaw),
		"remote_logs":   remoteLogBuf.String(),
		"desktop_logs":  desktopLogBuf.String(),
	})
	assertNoXSecretLeaks(t, desktopTmp, sentinels, nil)
}
