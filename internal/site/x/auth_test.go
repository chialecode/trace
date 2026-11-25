package x

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/site"
)

func newTestXStoreAt(t *testing.T, dir string, rawCookie string) (*credential.Store, string) {
	t.Helper()
	binPath := filepath.Join(dir, credential.XFileName)
	store, _, err := credential.OpenX(binPath)
	if err != nil {
		t.Fatalf("OpenX: %v", err)
	}
	if rawCookie != "" {
		if _, err := store.SaveXManual(rawCookie); err != nil {
			t.Fatalf("SaveXManual: %v", err)
		}
	}
	return store, binPath
}

func newTestXStore(t *testing.T, rawCookie string) *credential.Store {
	t.Helper()
	store, _ := newTestXStoreAt(t, t.TempDir(), rawCookie)
	return store
}

type synthFrontendServer struct {
	server       *httptest.Server
	homeHits     atomic.Int32
	mainHits     atomic.Int32
	ondemandHits atomic.Int32
	bearer       string
	queryIDs     map[string]string
}

func newSynthFrontendHarness(t *testing.T, apiHandler http.HandlerFunc) (*synthFrontendServer, *Client) {
	t.Helper()
	return newSynthFrontendHarnessWithOpts(t, apiHandler, nil, nil)
}

func newSynthFrontendHarnessWithOpts(
	t *testing.T,
	apiHandler http.HandlerFunc,
	store *credential.Store,
	modifyOpts func(*ClientOptions),
) (*synthFrontendServer, *Client) {
	t.Helper()
	homeBytes := mustReadTestdata(t, "webpack_home.html")
	mainBytes := mustReadTestdata(t, "webpack_main.js")
	ondemandBytes := mustReadTestdata(t, "webpack_ondemand.js")

	h := &synthFrontendServer{
		bearer: "SYNTHETIC_WEB_BEARER_TOKEN_0001",
		queryIDs: map[string]string{
			OpUserByScreenName: "SynthQidUserByScreenName01",
			OpUserByRestId:     "SynthQidUserByRestId02",
			OpUserTweets:       "SynthQidUserTweets03",
			OpTweetDetail:      "SynthQidTweetDetail04",
			OpFollowing:        "SynthQidFollowing05",
			OpBookmarks:        "SynthQidBookmarks06",
			OpLikes:            "SynthQidLikes07",
		},
	}

	h.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/home":
			h.homeHits.Add(1)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(homeBytes)
			return
		case "/responsive-web/client-web/main.synth01a.js":
			h.mainHits.Add(1)
			w.Header().Set("Content-Type", "application/javascript")
			_, _ = w.Write(mainBytes)
			return
		case "/responsive-web/client-web/ondemand.s.synth02a.js":
			h.ondemandHits.Add(1)
			w.Header().Set("Content-Type", "application/javascript")
			_, _ = w.Write(ondemandBytes)
			return
		}
		if apiHandler != nil {
			apiHandler(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(h.server.Close)

	fixedNow := time.Unix(1795327200, 0).UTC()
	opts := ClientOptions{
		CredStore: store,
		Endpoints: Endpoints{
			HomeURL:         h.server.URL + "/home",
			AccountSettings: h.server.URL + "/i/api/1.1/account/settings.json",
			GraphQLBase:     h.server.URL + "/i/api/graphql",
		},
		MinInterval:        -1,
		RateLimitJitterMax: -1,
		AllowLocalhost:     true,
		AssetTransport:     newAbsRewriteTransport(t, h.server),
		Now:                func() time.Time { return fixedNow },
		Sleep:              func(context.Context, time.Duration) error { return nil },
		TxIDOptions: []TxIDOption{
			WithTxIDClock(func() time.Time { return fixedNow }),
			WithTxIDRandByte(func() (byte, error) { return 0x42, nil }),
		},
	}
	if modifyOpts != nil {
		modifyOpts(&opts)
	}
	return h, NewClient(opts)
}

func synthUserByScreenNamePayload(restID, screenName, name, avatar string) string {
	return `{"data":{"user":{"result":{"__typename":"User","rest_id":"` + restID + `","is_blue_verified":true,"core":{"name":"` + name + `","screen_name":"` + screenName + `"},"avatar":{"image_url":"` + avatar + `"},"profile_bio":{"description":"Synthetic profile bio"},"legacy":{"statuses_count":42,"followers_count":100,"friends_count":20,"protected":false}}}}}`
}

func TestLoginWithCookieTwoStepSuccessWhitelistAndSentinelRedaction(t *testing.T) {
	authSentinel := "SYNTHAUTHTOKEN9988776655"
	ct0Sentinel := "SynthCT0Token1122334455"
	restID := "1000000000000000001"
	handle := "Example_User"

	var settingsSawHeaders http.Header
	var gqlSawHeaders http.Header

	store, binPath := newTestXStoreAt(t, t.TempDir(), "")
	_, client := newSynthFrontendHarnessWithOpts(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/i/api/1.1/account/settings.json":
			settingsSawHeaders = r.Header.Clone()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"screen_name":"example_user","protected":false,"language":"en"}`))
		case strings.HasSuffix(r.URL.Path, "/"+OpUserByScreenName):
			gqlSawHeaders = r.Header.Clone()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(synthUserByScreenNamePayload(
				restID,
				handle,
				"Example Creator",
				"https://pbs.twimg.com/profile_images/1001/avatar_normal.jpg",
			)))
		default:
			http.NotFound(w, r)
		}
	}, store, nil)

	// Include non-whitelisted ad/WAF/tracking cookies that MUST be stripped before sending & saving.
	rawInput := "Cookie: auth_token=" + authSentinel + "; ct0=" + ct0Sentinel +
		"; twid=u%3D" + restID +
		"; guest_id=v1%3A99000001; kdt=SynthKdt01" +
		"; __cf_bm=TRACKING_CF_BM; att=TEMP_ATT; gt=TEMP_GT; personalization_id=ADS_ID; guest_id_ads=ADS1; lang=en"

	st, err := client.LoginWithCookie(context.Background(), rawInput)
	if err != nil {
		t.Fatalf("LoginWithCookie failed: %v", err)
	}
	if !st.LoggedIn || st.State != "logged_in" {
		t.Fatalf("unexpected status: %+v", st)
	}
	if st.UID != restID || st.UniqueID != handle || st.Uname != "Example Creator" {
		t.Fatalf("profile fields = (uid=%q handle=%q name=%q)", st.UID, st.UniqueID, st.Uname)
	}
	if st.Face != "https://pbs.twimg.com/profile_images/1001/avatar_400x400.jpg" {
		t.Fatalf("avatar not upgraded: %q", st.Face)
	}

	// Verify headers sent to settings.json and UserByScreenName.
	for name, hdr := range map[string]http.Header{"settings": settingsSawHeaders, "graphql": gqlSawHeaders} {
		if hdr.Get("Authorization") != "Bearer SYNTHETIC_WEB_BEARER_TOKEN_0001" {
			t.Errorf("%s Authorization = %q", name, hdr.Get("Authorization"))
		}
		if hdr.Get("x-csrf-token") != ct0Sentinel {
			t.Errorf("%s x-csrf-token = %q", name, hdr.Get("x-csrf-token"))
		}
		if hdr.Get("x-twitter-auth-type") != "OAuth2Session" {
			t.Errorf("%s x-twitter-auth-type = %q", name, hdr.Get("x-twitter-auth-type"))
		}
		if hdr.Get("x-twitter-active-user") != "yes" {
			t.Errorf("%s x-twitter-active-user = %q", name, hdr.Get("x-twitter-active-user"))
		}
		if hdr.Get("x-xp-forwarded-for") != "" {
			t.Errorf("%s must NOT send x-xp-forwarded-for", name)
		}
		cookieHdr := hdr.Get("Cookie")
		if strings.Count(cookieHdr, "ct0=") != 1 {
			t.Errorf("%s Cookie must contain exactly one ct0, got: %q", name, cookieHdr)
		}
		for _, stripped := range []string{"__cf_bm", "att=", "gt=", "personalization_id", "guest_id_ads", "lang="} {
			if strings.Contains(cookieHdr, stripped) {
				t.Errorf("%s leaked stripped cookie %q in %q", name, stripped, cookieHdr)
			}
		}
	}
	if gqlSawHeaders.Get("x-client-transaction-id") == "" {
		t.Fatalf("GraphQL UserByScreenName missing x-client-transaction-id header")
	}
	// Real use (2026-11-23): the REST login confirmation without this header got 404 code 34.
	if settingsSawHeaders.Get("x-client-transaction-id") == "" {
		t.Fatalf("settings.json login confirmation missing x-client-transaction-id header")
	}

	// Verify saved cookies are whitelisted only, and persisted x.bin contains no plaintext sentinels.
	saved := store.Cookies()
	if saved["auth_token"] != authSentinel || saved["ct0"] != ct0Sentinel || saved["__cf_bm"] != "" {
		t.Fatalf("unexpected stored cookie map keys: %v", saved)
	}
	rawBin, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatalf("read x.bin: %v", err)
	}
	for _, secret := range []string{authSentinel, ct0Sentinel, "SYNTHETIC_WEB_BEARER_TOKEN_0001"} {
		if strings.Contains(string(rawBin), secret) {
			t.Fatalf("secret sentinel %q found in encrypted x.bin", secret)
		}
	}
	// Verify RedactString masks saved cookie values and Bearer token.
	logLine := "cookie auth_token=" + authSentinel + "; ct0=" + ct0Sentinel + " Authorization: Bearer SYNTHETIC_WEB_BEARER_TOKEN_0001"
	redacted := store.RedactString(logLine)
	for _, secret := range []string{authSentinel, ct0Sentinel, "SYNTHETIC_WEB_BEARER_TOKEN_0001"} {
		if strings.Contains(redacted, secret) {
			t.Fatalf("RedactString leaked %q: %q", secret, redacted)
		}
	}
}

func TestLoginWithCookieLocalPreCheckZeroRequestsNoEcho(t *testing.T) {
	var hits atomic.Int32
	store := newTestXStore(t, "")
	h, client := newSynthFrontendHarnessWithOpts(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}, store, nil)

	secretMarker := "SECRET_MARKER_NEVER_ECHO_999"
	cases := []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"missing auth_token", "ct0=SynthCT0Token0001; foo=" + secretMarker},
		{"missing ct0", "auth_token=" + secretMarker},
		{"invalid ct0 chars", "auth_token=" + secretMarker + "; ct0=bad_ct0!chars"},
		{"control char injection", "auth_token=" + secretMarker + "\x00; ct0=SynthCT0Token0001"},
		{"header prefix multiline", "Cookie: auth_token=" + secretMarker + "\nX-Injected: 1; ct0=SynthCT0Token0001"},
		{"oversized > 64 KiB", "auth_token=" + secretMarker + "; ct0=SynthCT0Token0001; pad=" + strings.Repeat("a", credential.MaxCookieInputBytes+64)},
		{"cleaned > 32 KiB", "auth_token=" + secretMarker + strings.Repeat("b", credential.MaxCleanedCookieHeaderBytes+64) + "; ct0=SynthCT0Token0001"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := client.LoginWithCookie(context.Background(), tc.raw)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !site.IsKind(err, site.ErrKindInvalidInput) {
				t.Fatalf("error kind = %q, want invalid_input (%v)", site.KindOf(err), err)
			}
			if strings.Contains(err.Error(), secretMarker) {
				t.Fatalf("error echoed raw cookie input: %q", err.Error())
			}
		})
	}
	if hits.Load() != 0 || h.homeHits.Load() != 0 || h.mainHits.Load() != 0 || h.ondemandHits.Load() != 0 {
		t.Fatalf("local pre-check issued network requests: api=%d home=%d main=%d ondemand=%d",
			hits.Load(), h.homeHits.Load(), h.mainHits.Load(), h.ondemandHits.Load())
	}
	if store.HasActiveCredential() {
		t.Fatalf("store must remain empty after failed local pre-check")
	}
}

func TestLoginWithCookieRejectsUnauthMismatchRateLimitAndRiskWithoutMutatingExistingStore(t *testing.T) {
	existingAuth := "EXISTING_SAVED_AUTH_0001"
	existingCT0 := "ExistingCT0Saved0001"
	existingRestID := "1000000000000000099"

	cases := []struct {
		name           string
		candidate      string
		settingsStatus int
		settingsCT     string
		settingsBody   string
		gqlStatus      int
		gqlBody        string
		wantKind       string
	}{
		{
			name:           "settings 401 code 32",
			candidate:      "auth_token=NEW_BAD_01; ct0=NewCT0Bad0001",
			settingsStatus: http.StatusUnauthorized,
			settingsBody:   `{"errors":[{"code":32,"message":"Could not authenticate you."}]}`,
			wantKind:       site.ErrKindAuthRequired,
		},
		{
			name:           "settings 200 with errors array",
			candidate:      "auth_token=NEW_BAD_02; ct0=NewCT0Bad0002",
			settingsStatus: http.StatusOK,
			settingsBody:   `{"screen_name":"example_user","errors":[{"code":89,"message":"Invalid or expired token"}]}`,
			wantKind:       site.ErrKindAuthRequired,
		},
		{
			name:           "settings 200 empty screen_name guest shell",
			candidate:      "auth_token=NEW_BAD_03; ct0=NewCT0Bad0003",
			settingsStatus: http.StatusOK,
			settingsBody:   `{"protected":false}`,
			wantKind:       site.ErrKindAuthRequired,
		},
		{
			name:           "settings 200 HTML login page",
			candidate:      "auth_token=NEW_BAD_04; ct0=NewCT0Bad0004",
			settingsStatus: http.StatusOK,
			settingsCT:     "text/html",
			settingsBody:   `<!doctype html><html><body>Sign in to X /i/flow/login</body></html>`,
			wantKind:       site.ErrKindAuthRequired,
		},
		{
			name:           "settings 200 empty body",
			candidate:      "auth_token=NEW_BAD_05; ct0=NewCT0Bad0005",
			settingsStatus: http.StatusOK,
			settingsBody:   "",
			wantKind:       site.ErrKindRiskControl,
		},
		{
			name:           "screen_name mismatch between Step 1 and Step 2",
			candidate:      "auth_token=NEW_BAD_06; ct0=NewCT0Bad0006",
			settingsStatus: http.StatusOK,
			settingsBody:   `{"screen_name":"example_user"}`,
			gqlStatus:      http.StatusOK,
			gqlBody:        synthUserByScreenNamePayload("1000000000000000001", "different_user", "Other", ""),
			wantKind:       site.ErrKindAuthRequired,
		},
		{
			name:           "twid rest_id mismatch",
			candidate:      "auth_token=NEW_BAD_07; ct0=NewCT0Bad0007; twid=u%3D1000000000000000777",
			settingsStatus: http.StatusOK,
			settingsBody:   `{"screen_name":"example_user"}`,
			gqlStatus:      http.StatusOK,
			gqlBody:        synthUserByScreenNamePayload("1000000000000000001", "example_user", "Example", ""),
			wantKind:       site.ErrKindAuthRequired,
		},
		{
			name:           "429 code 88 rate limited",
			candidate:      "auth_token=NEW_BAD_08; ct0=NewCT0Bad0008",
			settingsStatus: http.StatusTooManyRequests,
			settingsBody:   `{"errors":[{"code":88,"message":"Rate limit exceeded"}]}`,
			wantKind:       site.ErrKindRateLimited,
		},
		{
			name:           "403 code 326 temporarily locked risk control",
			candidate:      "auth_token=NEW_BAD_09; ct0=NewCT0Bad0009",
			settingsStatus: http.StatusForbidden,
			settingsBody:   `{"errors":[{"code":326,"message":"Account temporarily locked"}]}`,
			wantKind:       site.ErrKindRiskControl,
		},
		{
			name:           "403 code 226 automated request risk control",
			candidate:      "auth_token=NEW_BAD_10; ct0=NewCT0Bad0010",
			settingsStatus: http.StatusForbidden,
			settingsBody:   `{"errors":[{"code":226,"message":"This request looks like it might be automated"}]}`,
			wantKind:       site.ErrKindRiskControl,
		},
		{
			name:           "403 code 64 suspended risk control",
			candidate:      "auth_token=NEW_BAD_11; ct0=NewCT0Bad0011",
			settingsStatus: http.StatusForbidden,
			settingsBody:   `{"errors":[{"code":64,"message":"Your account is suspended"}]}`,
			wantKind:       site.ErrKindRiskControl,
		},
		{
			// Real-use regression (2026-11-23): x.com/i/api/1.1/account/settings.json
			// answered code 34; the login must say the endpoint changed, not
			// "user or post does not exist".
			name:           "settings 404 code 34 endpoint moved",
			candidate:      "auth_token=NEW_BAD_34; ct0=NewCT0Bad0034",
			settingsStatus: http.StatusNotFound,
			settingsBody:   `{"errors":[{"code":34,"message":"Sorry, that page does not exist."}]}`,
			wantKind:       site.ErrKindSchemaChanged,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestXStore(t, "")
			if _, err := store.SaveVerifiedXManual(
				map[string]string{"auth_token": existingAuth, "ct0": existingCT0},
				credential.XProfile{RestID: existingRestID, ScreenName: "saved_user", Name: "Saved User"},
			); err != nil {
				t.Fatalf("seed store: %v", err)
			}

			_, client := newSynthFrontendHarnessWithOpts(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/i/api/1.1/account/settings.json":
					ct := tc.settingsCT
					if ct == "" {
						ct = "application/json"
					}
					w.Header().Set("Content-Type", ct)
					if tc.settingsStatus == http.StatusTooManyRequests {
						w.Header().Set("x-rate-limit-remaining", "0")
						w.Header().Set("x-rate-limit-reset", "1795328100")
					}
					w.WriteHeader(tc.settingsStatus)
					_, _ = w.Write([]byte(tc.settingsBody))
				case strings.HasSuffix(r.URL.Path, "/"+OpUserByScreenName):
					w.Header().Set("Content-Type", "application/json")
					status := tc.gqlStatus
					if status == 0 {
						status = http.StatusOK
					}
					w.WriteHeader(status)
					_, _ = w.Write([]byte(tc.gqlBody))
				default:
					http.NotFound(w, r)
				}
			}, store, nil)

			_, err := client.LoginWithCookie(context.Background(), tc.candidate)
			if err == nil {
				t.Fatalf("expected error kind %q, got nil", tc.wantKind)
			}
			if !site.IsKind(err, tc.wantKind) {
				t.Fatalf("error kind = %q, want %q (err=%v)", site.KindOf(err), tc.wantKind, err)
			}

			// Existing saved credential MUST be completely untouched!
			afterSt := store.Status()
			if !afterSt.LoggedIn || afterSt.UID != existingRestID || store.CookieValue("auth_token") != existingAuth || store.CT0() != existingCT0 {
				t.Fatalf("existing credential was mutated on failed LoginWithCookie: status=%+v auth=%q ct0=%q",
					afterSt, store.CookieValue("auth_token"), store.CT0())
			}
		})
	}
}

func TestGuestControlAndVerifyCookieWithoutStoreMutation(t *testing.T) {
	var guestHeaders http.Header
	var guestOnlyCookieHeaders http.Header

	store := newTestXStore(t, "")
	_, client := newSynthFrontendHarnessWithOpts(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/i/api/1.1/account/settings.json":
			if r.Header.Get("Cookie") == "" {
				guestHeaders = r.Header.Clone()
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"errors":[{"code":32,"message":"Could not authenticate you."}]}`))
				return
			}
			if !strings.Contains(r.Header.Get("Cookie"), "auth_token=") {
				guestOnlyCookieHeaders = r.Header.Clone()
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"errors":[{"code":32,"message":"Could not authenticate you."}]}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"screen_name":"remote_verify"}`))
		case strings.HasSuffix(r.URL.Path, "/"+OpUserByScreenName):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(synthUserByScreenNamePayload("1000000000000000055", "remote_verify", "Remote Verify", "")))
		default:
			http.NotFound(w, r)
		}
	}, store, nil)

	// 1. Unauthenticated guest control: MUST NOT send Cookie, x-csrf-token, or x-twitter-auth-type!
	if _, err := client.CheckSettingsGuest(context.Background()); !site.IsKind(err, site.ErrKindAuthRequired) {
		t.Fatalf("CheckSettingsGuest err = %v, want auth_required", err)
	}
	if guestHeaders.Get("Cookie") != "" || guestHeaders.Get("x-csrf-token") != "" || guestHeaders.Get("x-twitter-auth-type") != "" {
		t.Fatalf("guest request leaked auth headers: cookie=%q csrf=%q authType=%q",
			guestHeaders.Get("Cookie"), guestHeaders.Get("x-csrf-token"), guestHeaders.Get("x-twitter-auth-type"))
	}

	// 2. Guest-only cookie (guest_id without auth_token/ct0): MUST NOT send x-twitter-auth-type: OAuth2Session!
	if _, err := client.CheckSettingsWithCookies(context.Background(), map[string]string{"guest_id": "v1%3A12345"}); !site.IsKind(err, site.ErrKindAuthRequired) {
		t.Fatalf("CheckSettingsWithCookies guest-only err = %v, want auth_required", err)
	}
	if guestOnlyCookieHeaders.Get("x-twitter-auth-type") != "" {
		t.Fatalf("guest-only cookie request sent x-twitter-auth-type = %q", guestOnlyCookieHeaders.Get("x-twitter-auth-type"))
	}

	// 3. VerifyCookie succeeds WITHOUT mutating the empty credential store!
	prof, cleaned, err := client.VerifyCookie(context.Background(), "auth_token=SYNTH_VERIFY_ONLY; ct0=SynthVerifyCT0001")
	if err != nil {
		t.Fatalf("VerifyCookie failed: %v", err)
	}
	if prof.RestID != "1000000000000000055" || prof.ScreenName != "remote_verify" || cleaned["ct0"] != "SynthVerifyCT0001" {
		t.Fatalf("VerifyCookie returned unexpected profile/cookies: %+v %v", prof, cleaned)
	}
	if store.HasActiveCredential() {
		t.Fatalf("VerifyCookie must NOT mutate credential store")
	}

	// Verify SelfProfile JSON marshaling has no secrets.
	raw, _ := json.Marshal(prof)
	if strings.Contains(string(raw), "SYNTH_VERIFY_ONLY") || strings.Contains(string(raw), "SynthVerifyCT0001") {
		t.Fatalf("SelfProfile JSON leaked cookie secrets")
	}
}

func TestVerifyCredentialUpdatesProfileAndLogoutClearsStore(t *testing.T) {
	store := newTestXStore(t, "auth_token=SYNTH_AUTH_VC; ct0=SynthCT0VC0001; twid=u%3D1000000000000000077")
	_, client := newSynthFrontendHarnessWithOpts(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/i/api/1.1/account/settings.json":
			_, _ = w.Write([]byte(`{"screen_name":"verify_acct"}`))
		case strings.HasSuffix(r.URL.Path, "/"+OpUserByScreenName):
			_, _ = w.Write([]byte(synthUserByScreenNamePayload("1000000000000000077", "verify_acct", "Updated Name", "https://pbs.twimg.com/profile_images/77/v_normal.jpg")))
		default:
			http.NotFound(w, r)
		}
	}, store, nil)

	st, err := client.VerifyCredential(context.Background())
	if err != nil {
		t.Fatalf("VerifyCredential failed: %v", err)
	}
	if !st.LoggedIn || st.UID != "1000000000000000077" || st.UniqueID != "verify_acct" || st.Uname != "Updated Name" {
		t.Fatalf("VerifyCredential status = %+v", st)
	}
	if err := client.LogoutCredential(); err != nil {
		t.Fatalf("LogoutCredential failed: %v", err)
	}
	if store.HasActiveCredential() {
		t.Fatalf("store still active after LogoutCredential")
	}
}

// The web client serves REST v1.1 from api.x.com; x.com/i/api/1.1/... answered
// code 34 in real use (2026-11-23), so the default must stay on api.x.com.
func TestDefaultAccountSettingsEndpointUsesAPIHost(t *testing.T) {
	u, err := url.Parse(DefaultAccountSettingsURL)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "https" || u.Hostname() != "api.x.com" || u.Path != "/1.1/account/settings.json" {
		t.Fatalf("DefaultAccountSettingsURL = %q, want https://api.x.com/1.1/account/settings.json", DefaultAccountSettingsURL)
	}
	if err := NewClient(ClientOptions{}).validateAPIURL(u); err != nil {
		t.Fatalf("api.x.com settings endpoint rejected by host allowlist: %v", err)
	}
}
