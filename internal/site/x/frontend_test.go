package x

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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/site"
)

func mustReadTestdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read testdata/%s: %v", name, err)
	}
	return b
}

// absRewriteTransport routes validated https://abs.twimg.com/... requests to an
// httptest.Server while keeping production allowLocalhost=false validation active.
// Original Host is preserved in X-Test-Orig-Host so tests can inspect routing.
type absRewriteTransport struct {
	target *url.URL
	inner  http.RoundTripper
}

func newAbsRewriteTransport(t *testing.T, ts *httptest.Server) http.RoundTripper {
	t.Helper()
	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("parse test server url: %v", err)
	}
	return &absRewriteTransport{target: u, inner: ts.Client().Transport}
}

func (rt *absRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("X-Test-Orig-Host", req.URL.Hostname())
	clone.URL.Scheme = rt.target.Scheme
	clone.URL.Host = rt.target.Host
	clone.Host = rt.target.Host
	return rt.inner.RoundTrip(clone)
}

func TestFrontendDiscoveryWebpackAndViteFixtures(t *testing.T) {
	cases := []struct {
		name           string
		homeFile       string
		mainPath       string
		mainFile       string
		ondemandPath   string
		ondemandFile   string
		wantBearer     string
		wantQIDs       map[string]string
		wantRowIndex   int
		wantKeyIndices []int
		wantDetailFeat map[string]bool
		wantDetailTogg map[string]bool
		wantScreenTogg map[string]bool
	}{
		{
			name:         "webpack bundle",
			homeFile:     "webpack_home.html",
			mainPath:     "/responsive-web/client-web/main.synth01a.js",
			mainFile:     "webpack_main.js",
			ondemandPath: "/responsive-web/client-web/ondemand.s.synth02a.js",
			ondemandFile: "webpack_ondemand.js",
			wantBearer:   "SYNTHETIC_WEB_BEARER_TOKEN_0001",
			wantQIDs: map[string]string{
				OpUserByScreenName: "SynthQidUserByScreenName01",
				OpUserByRestId:     "SynthQidUserByRestId02",
				OpUserTweets:       "SynthQidUserTweets03",
				OpTweetDetail:      "SynthQidTweetDetail04",
				OpFollowing:        "SynthQidFollowing05",
				OpBookmarks:        "SynthQidBookmarks06",
				OpLikes:            "SynthQidLikes07",
			},
			wantRowIndex:   7,
			wantKeyIndices: []int{12, 19, 25},
			wantDetailFeat: map[string]bool{
				"creator_subscriptions_tweet_preview_api_enabled":  true,
				"responsive_web_graphql_exclude_directive_enabled": true,
				"longform_notetweets_consumption_enabled":          true,
			},
			wantDetailTogg: map[string]bool{
				"withArticleRichContentState": true,
				"withArticlePlainText":        false,
				"withGrokAnalyze":             false,
			},
			wantScreenTogg: map[string]bool{
				"withAuxiliaryUserLabels": false,
			},
		},
		{
			name:         "vite x-web bundle",
			homeFile:     "vite_home.html",
			mainPath:     "/responsive-web/x-web/assets/main-vite01.js",
			mainFile:     "vite_main.js",
			ondemandPath: "/responsive-web/x-web/assets/ondemand.s-vite02.js",
			ondemandFile: "vite_ondemand.js",
			wantBearer:   "SYNTHETIC_VITE_BEARER_TOKEN_0002",
			wantQIDs: map[string]string{
				OpUserByScreenName: "SynthViteQidUserByScreenName01",
				OpUserByRestId:     "SynthViteQidUserByRestId02",
				OpUserTweets:       "SynthViteQidUserTweets03",
				OpTweetDetail:      "SynthViteQidTweetDetail04",
				OpFollowing:        "SynthViteQidFollowing05",
				OpBookmarks:        "SynthViteQidBookmarks06",
				OpLikes:            "SynthViteQidLikes07",
			},
			wantRowIndex:   9,
			wantKeyIndices: []int{14, 21, 30},
			wantDetailFeat: map[string]bool{
				"responsive_web_graphql_exclude_directive_enabled": true,
				"creator_subscriptions_tweet_preview_api_enabled":  true,
				"longform_notetweets_consumption_enabled":          true,
			},
			wantDetailTogg: map[string]bool{
				"withArticleRichContentState": true,
				"withArticlePlainText":        false,
			},
			wantScreenTogg: map[string]bool{
				"withAuxiliaryUserLabels": false,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			homeBytes := mustReadTestdata(t, tc.homeFile)
			mainBytes := mustReadTestdata(t, tc.mainFile)
			ondemandBytes := mustReadTestdata(t, tc.ondemandFile)

			var sawCookieOnAsset atomic.Bool
			var sawAuthOnAsset atomic.Bool
			var sawCSRFOnAsset atomic.Bool

			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Cookie") != "" {
					sawCookieOnAsset.Store(true)
				}
				if r.Header.Get("Authorization") != "" {
					sawAuthOnAsset.Store(true)
				}
				if r.Header.Get("X-Csrf-Token") != "" {
					sawCSRFOnAsset.Store(true)
				}
				switch r.URL.Path {
				case tc.mainPath:
					w.Header().Set("Content-Type", "application/javascript")
					_, _ = w.Write(mainBytes)
				case tc.ondemandPath:
					w.Header().Set("Content-Type", "application/javascript")
					_, _ = w.Write(ondemandBytes)
				default:
					http.NotFound(w, r)
				}
			}))
			defer ts.Close()

			var charged []FrontendRequestKind
			fixedNow := time.Unix(1795327200, 0).UTC()
			disc := NewFrontendDiscovery(FrontendDiscoveryConfig{
				FetchHome: func(ctx context.Context, homeURL string) ([]byte, error) {
					return homeBytes, nil
				},
				ChargeRequest: func(ctx context.Context, kind FrontendRequestKind, targetURL string) error {
					charged = append(charged, kind)
					return nil
				},
				Now:            func() time.Time { return fixedNow },
				AssetTransport: newAbsRewriteTransport(t, ts),
				TxIDOptions: []TxIDOption{
					WithTxIDClock(func() time.Time { return fixedNow }),
					WithTxIDRandByte(func() (byte, error) { return 0x42, nil }),
				},
			})

			meta, err := disc.Ensure(context.Background())
			if err != nil {
				t.Fatalf("Ensure failed: %v", err)
			}

			if sawCookieOnAsset.Load() || sawAuthOnAsset.Load() || sawCSRFOnAsset.Load() {
				t.Fatalf("static asset requests leaked auth headers: cookie=%v auth=%v csrf=%v",
					sawCookieOnAsset.Load(), sawAuthOnAsset.Load(), sawCSRFOnAsset.Load())
			}

			// Verify all 3 static fetches charged to quota/budget hook in order.
			wantCharged := []FrontendRequestKind{
				FrontendRequestHomeHTML,
				FrontendRequestMainBundle,
				FrontendRequestOndemandJS,
			}
			if len(charged) != len(wantCharged) {
				t.Fatalf("charged = %v, want %v", charged, wantCharged)
			}
			for i := range wantCharged {
				if charged[i] != wantCharged[i] {
					t.Fatalf("charged[%d] = %q, want %q", i, charged[i], wantCharged[i])
				}
			}

			// Verify Bearer extraction and non-leakage on JSON/String formatting.
			if meta.Bearer != tc.wantBearer {
				t.Fatalf("Bearer = %q, want %q", meta.Bearer, tc.wantBearer)
			}
			if meta.BearerHeader() != "Bearer "+tc.wantBearer {
				t.Fatalf("BearerHeader mismatch")
			}
			marshaled, err := json.Marshal(meta)
			if err != nil {
				t.Fatalf("json.Marshal(meta) failed: %v", err)
			}
			if strings.Contains(string(marshaled), tc.wantBearer) {
				t.Fatalf("Bearer sentinel leaked into json.Marshal(meta)")
			}
			if strings.Contains(meta.String(), tc.wantBearer) {
				t.Fatalf("Bearer sentinel leaked into meta.String()")
			}

			// Verify all 7 required operations.
			for _, opName := range RequiredOperations() {
				spec, ok := meta.Operation(opName)
				if !ok {
					t.Fatalf("missing operation %q", opName)
				}
				if spec.QueryID != tc.wantQIDs[opName] {
					t.Errorf("op %s QueryID = %q, want %q", opName, spec.QueryID, tc.wantQIDs[opName])
				}
				if len(spec.Features) == 0 {
					t.Errorf("op %s has empty Features", opName)
				}
			}

			detailOp, _ := meta.Operation(OpTweetDetail)
			for k, wantV := range tc.wantDetailFeat {
				if gotV, ok := detailOp.Features[k]; !ok || gotV != wantV {
					t.Errorf("TweetDetail.Features[%q] = (%v, %v), want (%v, true)", k, gotV, ok, wantV)
				}
			}
			for k, wantV := range tc.wantDetailTogg {
				if gotV, ok := detailOp.FieldToggles[k]; !ok || gotV != wantV {
					t.Errorf("TweetDetail.FieldToggles[%q] = (%v, %v), want (%v, true)", k, gotV, ok, wantV)
				}
			}

			screenOp, _ := meta.Operation(OpUserByScreenName)
			for k, wantV := range tc.wantScreenTogg {
				if gotV, ok := screenOp.FieldToggles[k]; !ok || gotV != wantV {
					t.Errorf("UserByScreenName.FieldToggles[%q] = (%v, %v), want (%v, true)", k, gotV, ok, wantV)
				}
			}
			// Defensive copy check on Operation().
			screenOp.Features["mutated_by_caller"] = true
			screenOpAgain, _ := meta.Operation(OpUserByScreenName)
			if _, exists := screenOpAgain.Features["mutated_by_caller"]; exists {
				t.Fatalf("Operation() returned shared Features map")
			}

			// Verify TxIDMaterial and Stage 2 header generation.
			if meta.TxIDMaterial.RowIndex != tc.wantRowIndex {
				t.Fatalf("RowIndex = %d, want %d", meta.TxIDMaterial.RowIndex, tc.wantRowIndex)
			}
			if len(meta.TxIDMaterial.KeyByteIndices) != len(tc.wantKeyIndices) {
				t.Fatalf("KeyByteIndices = %v, want %v", meta.TxIDMaterial.KeyByteIndices, tc.wantKeyIndices)
			}
			for i := range tc.wantKeyIndices {
				if meta.TxIDMaterial.KeyByteIndices[i] != tc.wantKeyIndices[i] {
					t.Fatalf("KeyByteIndices[%d] = %d, want %d", i, meta.TxIDMaterial.KeyByteIndices[i], tc.wantKeyIndices[i])
				}
			}
			txid, err := meta.TxID.Generate("GET", "/i/api/graphql/"+detailOp.QueryID+"/"+OpTweetDetail)
			if err != nil || txid == "" {
				t.Fatalf("TxID.Generate failed: (%q, %v)", txid, err)
			}
		})
	}
}

func TestFrontendDiscoveryWebpackNumericChunkMap(t *testing.T) {
	home := string(mustReadTestdata(t, "webpack_home.html"))
	home = strings.Replace(
		home,
		`"ondemand.s": "synth02"`,
		`42:"ondemand.s"}; window.__SYNTH_HASHES__ = {42:"synth02"`,
		1,
	)
	parsed, err := ParseHomeHTML(home)
	if err != nil {
		t.Fatalf("ParseHomeHTML failed: %v", err)
	}
	want := "https://abs.twimg.com/responsive-web/client-web/ondemand.s.synth02a.js"
	if parsed.OndemandJSURL != want {
		t.Fatalf("OndemandJSURL = %q, want %q", parsed.OndemandJSURL, want)
	}
}

func TestFrontendDiscoverySingleflight10ConcurrentCallers(t *testing.T) {
	homeBytes := mustReadTestdata(t, "webpack_home.html")
	mainBytes := mustReadTestdata(t, "webpack_main.js")
	ondemandBytes := mustReadTestdata(t, "webpack_ondemand.js")

	var homeHits, mainHits, ondemandHits atomic.Int32
	homeGate := make(chan struct{})
	homeEntered := make(chan struct{}, 1)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/responsive-web/client-web/main.synth01a.js":
			mainHits.Add(1)
			_, _ = w.Write(mainBytes)
		case "/responsive-web/client-web/ondemand.s.synth02a.js":
			ondemandHits.Add(1)
			_, _ = w.Write(ondemandBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	disc := NewFrontendDiscovery(FrontendDiscoveryConfig{
		FetchHome: func(ctx context.Context, homeURL string) ([]byte, error) {
			homeHits.Add(1)
			select {
			case homeEntered <- struct{}{}:
			default:
			}
			<-homeGate
			return homeBytes, nil
		},
		AssetTransport: newAbsRewriteTransport(t, ts),
	})

	const callers = 10
	results := make([]*FrontendMetadata, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func(idx int) {
			defer wg.Done()
			results[idx], errs[idx] = disc.Ensure(context.Background())
		}(i)
	}

	<-homeEntered
	time.Sleep(20 * time.Millisecond)
	close(homeGate)
	wg.Wait()

	for i := 0; i < callers; i++ {
		if errs[i] != nil {
			t.Fatalf("caller %d failed: %v", i, errs[i])
		}
		if results[i] == nil || results[i] != results[0] {
			t.Fatalf("caller %d did not receive shared metadata instance", i)
		}
	}

	if homeHits.Load() != 1 || mainHits.Load() != 1 || ondemandHits.Load() != 1 {
		t.Fatalf("expected singleflight hit counts (1,1,1), got home=%d main=%d ondemand=%d",
			homeHits.Load(), mainHits.Load(), ondemandHits.Load())
	}

	// Subsequent Ensure hits cache with 0 upstream calls.
	if _, err := disc.Ensure(context.Background()); err != nil {
		t.Fatalf("warm Ensure failed: %v", err)
	}
	if homeHits.Load() != 1 || mainHits.Load() != 1 || ondemandHits.Load() != 1 {
		t.Fatalf("warm Ensure triggered unexpected upstream hits: (%d,%d,%d)",
			homeHits.Load(), mainHits.Load(), ondemandHits.Load())
	}

	// 10 concurrent Refresh callers also collapse into a single upstream refresh run.
	refreshGate := make(chan struct{})
	refreshEntered := make(chan struct{}, 1)
	disc.fetchHome = func(ctx context.Context, homeURL string) ([]byte, error) {
		homeHits.Add(1)
		select {
		case refreshEntered <- struct{}{}:
		default:
		}
		<-refreshGate
		return homeBytes, nil
	}
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func(idx int) {
			defer wg.Done()
			results[idx], errs[idx] = disc.Refresh(context.Background(), "empty_404")
		}(i)
	}
	<-refreshEntered
	time.Sleep(20 * time.Millisecond)
	close(refreshGate)
	wg.Wait()

	for i := 0; i < callers; i++ {
		if errs[i] != nil {
			t.Fatalf("refresh caller %d failed: %v", i, errs[i])
		}
		if results[i] == nil || results[i] != results[0] {
			t.Fatalf("refresh caller %d did not receive shared refreshed metadata", i)
		}
	}
	if homeHits.Load() != 2 || mainHits.Load() != 2 || ondemandHits.Load() != 2 {
		t.Fatalf("expected concurrent Refresh hit counts (2,2,2), got home=%d main=%d ondemand=%d",
			homeHits.Load(), mainHits.Load(), ondemandHits.Load())
	}
}

func TestFrontendDiscoveryRefreshCooldown10Minutes(t *testing.T) {
	homeBytes := mustReadTestdata(t, "webpack_home.html")
	mainBytes := mustReadTestdata(t, "webpack_main.js")
	ondemandBytes := mustReadTestdata(t, "webpack_ondemand.js")

	var homeHits, mainHits, ondemandHits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/responsive-web/client-web/main.synth01a.js":
			mainHits.Add(1)
			_, _ = w.Write(mainBytes)
		case "/responsive-web/client-web/ondemand.s.synth02a.js":
			ondemandHits.Add(1)
			_, _ = w.Write(ondemandBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	var clockMu sync.Mutex
	curTime := time.Unix(1795327200, 0).UTC()
	nowFn := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return curTime
	}
	advance := func(d time.Duration) {
		clockMu.Lock()
		curTime = curTime.Add(d)
		clockMu.Unlock()
	}

	disc := NewFrontendDiscovery(FrontendDiscoveryConfig{
		FetchHome: func(ctx context.Context, homeURL string) ([]byte, error) {
			homeHits.Add(1)
			return homeBytes, nil
		},
		Now:             nowFn,
		RefreshCooldown: DefaultFrontendRefreshCooldown,
		AssetTransport:  newAbsRewriteTransport(t, ts),
	})

	// Initial Ensure at t=0.
	if _, err := disc.Ensure(context.Background()); err != nil {
		t.Fatalf("initial Ensure failed: %v", err)
	}
	if homeHits.Load() != 1 || mainHits.Load() != 1 || ondemandHits.Load() != 1 {
		t.Fatalf("after Ensure hits = (%d,%d,%d), want (1,1,1)", homeHits.Load(), mainHits.Load(), ondemandHits.Load())
	}

	// Drift signal at t=1m: first Refresh succeeds.
	advance(1 * time.Minute)
	if _, err := disc.Refresh(context.Background(), "empty_404"); err != nil {
		t.Fatalf("first Refresh failed: %v", err)
	}
	if homeHits.Load() != 2 || mainHits.Load() != 2 || ondemandHits.Load() != 2 {
		t.Fatalf("after 1st Refresh hits = (%d,%d,%d), want (2,2,2)", homeHits.Load(), mainHits.Load(), ondemandHits.Load())
	}

	// Second Refresh within 10m (at +5m): MUST return schema_changed with 0 upstream calls.
	advance(5 * time.Minute)
	_, err := disc.Refresh(context.Background(), "features_cannot_be_null")
	if !site.IsKind(err, site.ErrKindSchemaChanged) {
		t.Fatalf("2nd Refresh within 10m error = %v, want schema_changed", err)
	}
	if site.SiteOf(err) != SiteID {
		t.Fatalf("2nd Refresh error site = %q, want %q", site.SiteOf(err), SiteID)
	}
	if homeHits.Load() != 2 || mainHits.Load() != 2 || ondemandHits.Load() != 2 {
		t.Fatalf("2nd Refresh hit upstream during cooldown: (%d,%d,%d)", homeHits.Load(), mainHits.Load(), ondemandHits.Load())
	}

	// Still blocked at +9m59s from 1st Refresh.
	advance(4*time.Minute + 59*time.Second)
	if _, err := disc.Refresh(context.Background(), "query_unspecified"); !site.IsKind(err, site.ErrKindSchemaChanged) {
		t.Fatalf("Refresh at 9m59s error = %v, want schema_changed", err)
	}
	if homeHits.Load() != 2 {
		t.Fatalf("Refresh at 9m59s hit upstream")
	}

	// After 10m+1s (advance 2 more seconds => 10m01s after 1st Refresh): succeeds again!
	advance(2 * time.Second)
	if _, err := disc.Refresh(context.Background(), "empty_404"); err != nil {
		t.Fatalf("Refresh after 10m+1s failed: %v", err)
	}
	if homeHits.Load() != 3 || mainHits.Load() != 3 || ondemandHits.Load() != 3 {
		t.Fatalf("after cooldown Refresh hits = (%d,%d,%d), want (3,3,3)", homeHits.Load(), mainHits.Load(), ondemandHits.Load())
	}
}

func TestShouldTriggerFrontendRefresh(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		wantOK     bool
		wantReason string
	}{
		{"empty 404", 404, "", true, "empty_404"},
		{"whitespace-only 404", 404, "  \r\n\t ", true, "empty_404"},
		{"json 404 not drift", 404, `{"errors":[{"code":34}]}`, false, ""},
		{"400 features cannot be null", 400, `{"errors":[{"message":"The following features cannot be null: foo_enabled"}]}`, true, "features_cannot_be_null"},
		{"400 Query Unspecified", 400, `{"errors":[{"message":"Query: Unspecified"}]}`, true, "query_unspecified"},
		{"400 unrelated", 400, `{"errors":[{"code":214,"message":"bad cursor"}]}`, false, ""},
		{"200 ok", 200, `{"data":{}}`, false, ""},
	}
	for _, tc := range cases {
		reason, ok := ShouldTriggerFrontendRefresh(tc.status, []byte(tc.body))
		if ok != tc.wantOK || reason != tc.wantReason {
			t.Errorf("%s: got (%q, %v), want (%q, %v)", tc.name, reason, ok, tc.wantReason, tc.wantOK)
		}
	}
}

func TestFrontendStaticAssetSecurityGuards(t *testing.T) {
	// Direct URL validation checks (host, path, scheme, port, traversal, userinfo).
	badURLs := []string{
		"",
		"http://abs.twimg.com/responsive-web/client-web/main.synth01a.js",
		"https://abs.twimg.com:8443/responsive-web/client-web/main.synth01a.js",
		"https://user:pass@abs.twimg.com/responsive-web/client-web/main.synth01a.js",
		"https://evil.example.com/responsive-web/client-web/main.synth01a.js",
		"https://pbs.twimg.com/responsive-web/client-web/main.synth01a.js",
		"https://sub.abs.twimg.com/responsive-web/client-web/main.synth01a.js",
		"https://abs.twimg.com.evil.com/responsive-web/client-web/main.synth01a.js",
		"https://abs.twimg.com/other/main.synth01a.js",
		"https://abs.twimg.com/responsive-web/../other/main.synth01a.js",
		"https://abs.twimg.com/responsive-web/%2e%2e/other/main.synth01a.js",
		"https://abs.twimg.com/responsive-web/",
	}
	for _, raw := range badURLs {
		if _, err := ValidateStaticAssetURL(raw, false); !site.IsKind(err, site.ErrKindBlocked) {
			t.Errorf("ValidateStaticAssetURL(%q) error = %v, want blocked", raw, err)
		}
	}

	// Valid URLs pass.
	goodURLs := []string{
		"https://abs.twimg.com/responsive-web/client-web/main.synth01a.js",
		"https://abs.twimg.com:443/responsive-web/x-web/assets/ondemand.s-vite02.js",
	}
	for _, raw := range goodURLs {
		if _, err := ValidateStaticAssetURL(raw, false); err != nil {
			t.Errorf("ValidateStaticAssetURL(%q) unexpected error: %v", raw, err)
		}
	}

	// End-to-end rejection when HTML points main.*.js to disallowed host or path (0 asset hits).
	baseHTML := string(mustReadTestdata(t, "webpack_home.html"))
	for _, tc := range []struct {
		name string
		repl string
	}{
		{"disallowed host in HTML", "https://evil.example.com/responsive-web/client-web/main.synth01a.js"},
		{"disallowed path in HTML", "https://abs.twimg.com/not-responsive/main.synth01a.js"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			defer ts.Close()

			mutatedHTML := strings.ReplaceAll(baseHTML, "https://abs.twimg.com/responsive-web/client-web/main.synth01a.js", tc.repl)
			disc := NewFrontendDiscovery(FrontendDiscoveryConfig{
				FetchHome:      func(ctx context.Context, homeURL string) ([]byte, error) { return []byte(mutatedHTML), nil },
				AssetTransport: newAbsRewriteTransport(t, ts),
			})
			_, err := disc.Ensure(context.Background())
			if !site.IsKind(err, site.ErrKindBlocked) {
				t.Fatalf("%s: error = %v, want blocked", tc.name, err)
			}
			if hits.Load() != 0 {
				t.Fatalf("%s: expected 0 asset hits, got %d", tc.name, hits.Load())
			}
		})
	}

	// Reject redirect to disallowed host and disallowed path.
	for _, tc := range []struct {
		name     string
		location string
	}{
		{"redirect to disallowed host", "https://evil.example.com/responsive-web/client-web/main.synth01a.js"},
		{"redirect to disallowed path", "https://abs.twimg.com/other/main.synth01a.js"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var targetHits atomic.Int32
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/responsive-web/client-web/main.synth01a.js" {
					w.Header().Set("Location", tc.location)
					w.WriteHeader(http.StatusFound)
					return
				}
				targetHits.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			defer ts.Close()

			disc := NewFrontendDiscovery(FrontendDiscoveryConfig{
				FetchHome:      func(ctx context.Context, homeURL string) ([]byte, error) { return []byte(baseHTML), nil },
				AssetTransport: newAbsRewriteTransport(t, ts),
			})
			_, err := disc.Ensure(context.Background())
			if !site.IsKind(err, site.ErrKindBlocked) {
				t.Fatalf("%s: error = %v, want blocked", tc.name, err)
			}
			if targetHits.Load() != 0 {
				t.Fatalf("%s: followed disallowed redirect", tc.name)
			}
		})
	}

	// Valid redirect within https://abs.twimg.com/responsive-web/ succeeds and stays unauthenticated
	// with Referer and Origin stripped across every redirect hop (x.md §15.1 #4).
	t.Run("allowed redirect within abs.twimg.com responsive-web", func(t *testing.T) {
		mainBytes := mustReadTestdata(t, "webpack_main.js")
		ondemandBytes := mustReadTestdata(t, "webpack_ondemand.js")
		var sawAuthHeader atomic.Bool
		var sawReferer atomic.Bool
		var sawOrigin atomic.Bool
		var hopCount atomic.Int32
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("X-Csrf-Token") != "" {
				sawAuthHeader.Store(true)
			}
			if r.Header.Get("Referer") != "" {
				sawReferer.Store(true)
			}
			if r.Header.Get("Origin") != "" {
				sawOrigin.Store(true)
			}
			switch r.URL.Path {
			case "/responsive-web/client-web/main.synth01a.js":
				hopCount.Add(1)
				w.Header().Set("Location", "https://abs.twimg.com/responsive-web/client-web/main.synth01a.redirected.js")
				w.WriteHeader(http.StatusFound)
			case "/responsive-web/client-web/main.synth01a.redirected.js":
				hopCount.Add(1)
				_, _ = w.Write(mainBytes)
			case "/responsive-web/client-web/ondemand.s.synth02a.js":
				_, _ = w.Write(ondemandBytes)
			default:
				http.NotFound(w, r)
			}
		}))
		defer ts.Close()

		disc := NewFrontendDiscovery(FrontendDiscoveryConfig{
			FetchHome:      func(ctx context.Context, homeURL string) ([]byte, error) { return []byte(baseHTML), nil },
			AssetTransport: newAbsRewriteTransport(t, ts),
		})
		if _, err := disc.Ensure(context.Background()); err != nil {
			t.Fatalf("allowed redirect failed: %v", err)
		}
		if hopCount.Load() != 2 {
			t.Fatalf("expected 2 hops for redirected main.js, got %d", hopCount.Load())
		}
		if sawAuthHeader.Load() {
			t.Fatalf("redirected static request carried auth/cookie header")
		}
		if sawReferer.Load() || sawOrigin.Load() {
			t.Fatalf("redirected static request leaked Referer (%v) or Origin (%v)", sawReferer.Load(), sawOrigin.Load())
		}
	})

	// AllowLocalhostAssets permits loopback /responsive-web/ while still blocking other hosts and paths.
	t.Run("AllowLocalhostAssets keeps path and non-loopback guards", func(t *testing.T) {
		if _, err := ValidateStaticAssetURL("http://127.0.0.1:18080/responsive-web/client-web/main.synth01a.js", true); err != nil {
			t.Fatalf("expected loopback /responsive-web/ allowed, got %v", err)
		}
		if _, err := ValidateStaticAssetURL("http://127.0.0.1:18080/other/main.synth01a.js", true); !site.IsKind(err, site.ErrKindBlocked) {
			t.Fatalf("expected loopback non-/responsive-web/ blocked, got %v", err)
		}
		if _, err := ValidateStaticAssetURL("https://evil.example.com/responsive-web/client-web/main.synth01a.js", true); !site.IsKind(err, site.ErrKindBlocked) {
			t.Fatalf("expected non-loopback disallowed host blocked even with allowLocalhost, got %v", err)
		}
	})

	// Reject > 8 MiB response (both Content-Length and chunked streaming without Content-Length).
	for _, chunked := range []bool{false, true} {
		label := "oversized with Content-Length"
		if chunked {
			label = "oversized chunked stream"
		}
		t.Run(label, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if chunked {
					flusher, _ := w.(http.Flusher)
					chunk := bytes.Repeat([]byte("a"), 1<<20) // 1 MiB chunk
					for i := 0; i < 9; i++ {                  // 9 MiB total > 8 MiB limit
						if _, err := w.Write(chunk); err != nil {
							return
						}
						if flusher != nil {
							flusher.Flush()
						}
					}
					return
				}
				w.Header().Set("Content-Length", fmt.Sprintf("%d", MaxStaticAssetBytes+1))
				w.WriteHeader(http.StatusOK)
				_, _ = io.CopyN(w, bytes.NewReader(bytes.Repeat([]byte("a"), 1024)), 1024)
			}))
			defer ts.Close()

			disc := NewFrontendDiscovery(FrontendDiscoveryConfig{
				FetchHome:      func(ctx context.Context, homeURL string) ([]byte, error) { return []byte(baseHTML), nil },
				AssetTransport: newAbsRewriteTransport(t, ts),
			})
			_, err := disc.Ensure(context.Background())
			if !site.IsKind(err, site.ErrKindSchemaChanged) {
				t.Fatalf("%s: error = %v, want schema_changed", label, err)
			}
		})
	}
}

func TestFrontendDiscoveryQuotaHookAbortsBeforeNetwork(t *testing.T) {
	homeBytes := mustReadTestdata(t, "webpack_home.html")
	mainBytes := mustReadTestdata(t, "webpack_main.js")
	ondemandBytes := mustReadTestdata(t, "webpack_ondemand.js")

	var homeHits, mainHits, ondemandHits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/responsive-web/client-web/main.synth01a.js":
			mainHits.Add(1)
			_, _ = w.Write(mainBytes)
		case "/responsive-web/client-web/ondemand.s.synth02a.js":
			ondemandHits.Add(1)
			_, _ = w.Write(ondemandBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	quotaErr := &site.Error{Site: SiteID, Kind: site.ErrKindQuota, Message: "X 每小时额度已用尽"}
	disc := NewFrontendDiscovery(FrontendDiscoveryConfig{
		FetchHome: func(ctx context.Context, homeURL string) ([]byte, error) {
			homeHits.Add(1)
			return homeBytes, nil
		},
		ChargeRequest: func(ctx context.Context, kind FrontendRequestKind, targetURL string) error {
			if kind == FrontendRequestOndemandJS {
				return quotaErr
			}
			return nil
		},
		AssetTransport: newAbsRewriteTransport(t, ts),
	})

	_, err := disc.Ensure(context.Background())
	if !errors.Is(err, quotaErr) {
		t.Fatalf("Ensure error = %v, want quotaErr", err)
	}
	if homeHits.Load() != 1 || mainHits.Load() != 1 || ondemandHits.Load() != 0 {
		t.Fatalf("hits = (%d,%d,%d), want (1,1,0)", homeHits.Load(), mainHits.Load(), ondemandHits.Load())
	}
}

func TestFrontendDiscoveryParseFailuresAndBearerSentinelNeverInErrors(t *testing.T) {
	const bearerSentinel = "SYNTHETIC_WEB_BEARER_TOKEN_0001"
	homeBytes := mustReadTestdata(t, "webpack_home.html")
	mainBytes := mustReadTestdata(t, "webpack_main.js")
	ondemandBytes := mustReadTestdata(t, "webpack_ondemand.js")

	cases := []struct {
		name     string
		home     []byte
		main     []byte
		ondemand []byte
	}{
		{
			name:     "missing verification meta",
			home:     bytes.ReplaceAll(homeBytes, []byte("twitter-site-verification"), []byte("other-meta")),
			main:     mainBytes,
			ondemand: ondemandBytes,
		},
		{
			name:     "missing svg animation frame",
			home:     bytes.ReplaceAll(homeBytes, []byte("loading-x-anim-3"), []byte("other-svg")),
			main:     mainBytes,
			ondemand: ondemandBytes,
		},
		{
			name:     "missing bearer in main bundle",
			home:     homeBytes,
			main:     bytes.ReplaceAll(mainBytes, []byte(bearerSentinel), []byte("short")),
			ondemand: ondemandBytes,
		},
		{
			name:     "missing required core Following operation",
			home:     homeBytes,
			main:     bytes.ReplaceAll(mainBytes, []byte(`operationName:"Following"`), []byte(`operationName:"OtherOp"`)),
			ondemand: ondemandBytes,
		},
		{
			name:     "missing required core TweetDetail operation",
			home:     homeBytes,
			main:     bytes.ReplaceAll(mainBytes, []byte(`operationName:"TweetDetail"`), []byte(`operationName:"OtherOp"`)),
			ondemand: ondemandBytes,
		},
		{
			name:     "malformed ondemand indices",
			home:     homeBytes,
			main:     mainBytes,
			ondemand: []byte(`function noop(){return 0}`),
		},
		{
			name:     "invalid base64 verification key",
			home:     bytes.ReplaceAll(homeBytes, []byte("U1lOVEhFVElDX1hfVkVSSUZJQ0FUSU9OX0tFWV9GSVhUVVJFXzAwMDFfQUJDREVG"), []byte("!!!invalid-base64!!!")),
			main:     mainBytes,
			ondemand: ondemandBytes,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/responsive-web/client-web/main.synth01a.js":
					_, _ = w.Write(tc.main)
				case "/responsive-web/client-web/ondemand.s.synth02a.js":
					_, _ = w.Write(tc.ondemand)
				default:
					http.NotFound(w, r)
				}
			}))
			defer ts.Close()

			disc := NewFrontendDiscovery(FrontendDiscoveryConfig{
				FetchHome:      func(ctx context.Context, homeURL string) ([]byte, error) { return tc.home, nil },
				AssetTransport: newAbsRewriteTransport(t, ts),
			})
			_, err := disc.Ensure(context.Background())
			if !site.IsKind(err, site.ErrKindSchemaChanged) {
				t.Fatalf("%s: error = %v, want schema_changed", tc.name, err)
			}
			if strings.Contains(err.Error(), bearerSentinel) {
				t.Fatalf("%s: Bearer sentinel leaked in error: %v", tc.name, err)
			}
		})
	}

	// Even if an upstream hook accidentally returns an error echoing the Bearer sentinel,
	// sanitizeFrontendError scrubs it before returning to callers.
	disc := NewFrontendDiscovery(FrontendDiscoveryConfig{
		FetchHome: func(ctx context.Context, homeURL string) ([]byte, error) { return homeBytes, nil },
		ChargeRequest: func(ctx context.Context, kind FrontendRequestKind, targetURL string) error {
			if kind == FrontendRequestOndemandJS {
				return &site.Error{Site: SiteID, Kind: site.ErrKindNetwork, Message: "leaked " + bearerSentinel}
			}
			return nil
		},
		AssetTransport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(mainBytes)),
				Header:     make(http.Header),
			}, nil
		}),
	})
	_, err := disc.Ensure(context.Background())
	if err == nil {
		t.Fatalf("expected error from ChargeRequest")
	}
	if strings.Contains(err.Error(), bearerSentinel) {
		t.Fatalf("Bearer sentinel was not scrubbed from error: %v", err)
	}
}

func TestFrontendStripStaticAuthHeadersIncludesRefererAndOrigin(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://abs.twimg.com/responsive-web/client-web/main.synth01a.js", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Cookie", "auth_token=SYNTH; ct0=SYNTH")
	req.Header.Set("Authorization", "Bearer SYNTH")
	req.Header.Set("X-Csrf-Token", "SYNTH")
	req.Header.Set("Referer", "https://abs.twimg.com/responsive-web/client-web/prev.js")
	req.Header.Set("Origin", "https://x.com")
	req.Header.Set("Accept", "*/*")

	stripStaticAuthHeaders(req)

	for _, hdr := range []string{"Cookie", "Authorization", "X-Csrf-Token", "Referer", "Origin"} {
		if got := req.Header.Get(hdr); got != "" {
			t.Errorf("stripStaticAuthHeaders left %s=%q", hdr, got)
		}
	}
	if got := req.Header.Get("Accept"); got != "*/*" {
		t.Errorf("stripStaticAuthHeaders removed unrelated Accept header: %q", got)
	}
}

func TestFrontendDiscoveryHoldsSerialGateAcrossHTTPIO(t *testing.T) {
	homeBytes := mustReadTestdata(t, "webpack_home.html")
	mainBytes := mustReadTestdata(t, "webpack_main.js")
	ondemandBytes := mustReadTestdata(t, "webpack_ondemand.js")

	// 1. Direct FrontendDiscovery.AcquireRequest holds gate across each fetch and releases even on error.
	t.Run("AcquireRequest releases strictly after fetch I/O", func(t *testing.T) {
		var held atomic.Int32
		var observedHeldDuringFetch []int32
		var mu sync.Mutex

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			observedHeldDuringFetch = append(observedHeldDuringFetch, held.Load())
			mu.Unlock()
			switch r.URL.Path {
			case "/responsive-web/client-web/main.synth01a.js":
				_, _ = w.Write(mainBytes)
			case "/responsive-web/client-web/ondemand.s.synth02a.js":
				w.WriteHeader(http.StatusInternalServerError)
			default:
				http.NotFound(w, r)
			}
		}))
		defer ts.Close()

		disc := NewFrontendDiscovery(FrontendDiscoveryConfig{
			FetchHome: func(ctx context.Context, homeURL string) ([]byte, error) {
				mu.Lock()
				observedHeldDuringFetch = append(observedHeldDuringFetch, held.Load())
				mu.Unlock()
				return homeBytes, nil
			},
			AcquireRequest: func(ctx context.Context, kind FrontendRequestKind, targetURL string) (func(), error) {
				if held.Add(1) != 1 {
					t.Errorf("AcquireRequest(%s) overlapped with already-held gate", kind)
				}
				return func() {
					held.Add(-1)
				}, nil
			},
			AssetTransport: newAbsRewriteTransport(t, ts),
		})

		if _, err := disc.Ensure(context.Background()); err == nil {
			t.Fatal("expected error from 500 ondemand.s fetch")
		}
		if held.Load() != 0 {
			t.Fatalf("gate leaked after failed discovery: held=%d", held.Load())
		}
		mu.Lock()
		defer mu.Unlock()
		if len(observedHeldDuringFetch) != 3 {
			t.Fatalf("expected 3 fetches, got %d (%v)", len(observedHeldDuringFetch), observedHeldDuringFetch)
		}
		for i, v := range observedHeldDuringFetch {
			if v != 1 {
				t.Errorf("fetch %d observed held=%d, want 1", i, v)
			}
		}
	})

	// 2. End-to-end Client: slow in-flight frontend JS fetch holds serialGate so concurrent
	// API calls cannot overlap in-flight (strict concurrency = 1, NFR-NET-005).
	t.Run("Client serializes slow frontend fetch against concurrent API call", func(t *testing.T) {
		var inFlight atomic.Int32
		var maxInFlight atomic.Int32
		recordEnter := func() {
			cur := inFlight.Add(1)
			for {
				prev := maxInFlight.Load()
				if cur <= prev || maxInFlight.CompareAndSwap(prev, cur) {
					break
				}
			}
		}

		mainStarted := make(chan struct{})
		releaseMain := make(chan struct{})
		apiEntered := make(chan struct{})

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			recordEnter()
			defer inFlight.Add(-1)

			switch r.URL.Path {
			case "/home":
				w.Header().Set("Content-Type", "text/html")
				_, _ = w.Write(homeBytes)
			case "/responsive-web/client-web/main.synth01a.js":
				close(mainStarted)
				<-releaseMain
				w.Header().Set("Content-Type", "application/javascript")
				_, _ = w.Write(mainBytes)
			case "/responsive-web/client-web/ondemand.s.synth02a.js":
				w.Header().Set("Content-Type", "application/javascript")
				_, _ = w.Write(ondemandBytes)
			case "/i/api/1.1/account/settings.json":
				close(apiEntered)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"screen_name":"serial_user"}`))
			default:
				http.NotFound(w, r)
			}
		}))
		defer ts.Close()

		store := newTestXStore(t, "auth_token=SYNTH_SERIAL_01; ct0=SynthCT0Serial01")
		client := NewClient(ClientOptions{
			CredStore: store,
			Endpoints: Endpoints{
				HomeURL:         ts.URL + "/home",
				AccountSettings: ts.URL + "/i/api/1.1/account/settings.json",
				GraphQLBase:     ts.URL + "/i/api/graphql",
			},
			MinInterval:    -1,
			AllowLocalhost: true,
			AssetTransport: newAbsRewriteTransport(t, ts),
		})

		discErrCh := make(chan error, 1)
		go func() {
			_, err := client.EnsureFrontendMetadata(context.Background())
			discErrCh <- err
		}()

		select {
		case <-mainStarted:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for main.js fetch to start")
		}

		apiErrCh := make(chan error, 1)
		go func() {
			_, err := client.ProbeRaw(context.Background(), http.MethodGet, OpAccountSettings, ts.URL+"/i/api/1.1/account/settings.json", nil, false)
			apiErrCh <- err
		}()

		// Give the concurrent API goroutine a brief window; it MUST remain blocked on serialGate
		// until main.js finishes reading and closes its response body.
		select {
		case <-apiEntered:
			close(releaseMain)
			t.Fatal("concurrent API call entered HTTP transport while frontend main.js fetch was still in-flight")
		case <-time.After(80 * time.Millisecond):
		}

		close(releaseMain)

		if err := <-discErrCh; err != nil {
			t.Fatalf("EnsureFrontendMetadata failed: %v", err)
		}
		if err := <-apiErrCh; err != nil {
			t.Fatalf("concurrent ProbeRaw failed: %v", err)
		}
		if got := maxInFlight.Load(); got != 1 {
			t.Fatalf("max concurrent in-flight HTTP requests = %d, want 1", got)
		}
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestParseHomeHTMLOndemandDecoyQuotedIDAndLegacyPreference(t *testing.T) {
	baseHTML := string(mustReadTestdata(t, "webpack_home.html"))

	t.Run("ignores_earlier_initial_state_decoy_and_prefers_hex_hash_after_ondemand", func(t *testing.T) {
		decoyScript := `<script>window.__INITIAL_STATE__={"entities":{"412":"decoy999","other":{412:"deadbeef"}}};</script>`
		html := strings.Replace(baseHTML, "<script>", decoyScript+"\n  <script>", 1)
		html = strings.Replace(
			html,
			`"ondemand.s": "synth02"`,
			`412:"ondemand.s"}; var meta={412:"not_hex_label"}; var hashes={412:"a1b2c3d4"`,
			1,
		)
		parsed, err := ParseHomeHTML(html)
		if err != nil {
			t.Fatalf("ParseHomeHTML failed: %v", err)
		}
		want := "https://abs.twimg.com/responsive-web/client-web/ondemand.s.a1b2c3d4a.js"
		if parsed.OndemandJSURL != want {
			t.Fatalf("OndemandJSURL = %q, want %q", parsed.OndemandJSURL, want)
		}
	})

	t.Run("quoted_numeric_chunk_id_in_webpack_map", func(t *testing.T) {
		html := strings.Replace(
			baseHTML,
			`"ondemand.s": "synth02"`,
			`"412":"ondemand.s"}; window.__SYNTH_HASHES__ = {"412":"9f8e7d6c"`,
			1,
		)
		parsed, err := ParseHomeHTML(html)
		if err != nil {
			t.Fatalf("ParseHomeHTML failed: %v", err)
		}
		want := "https://abs.twimg.com/responsive-web/client-web/ondemand.s.9f8e7d6ca.js"
		if parsed.OndemandJSURL != want {
			t.Fatalf("OndemandJSURL = %q, want %q", parsed.OndemandJSURL, want)
		}
	})

	t.Run("does_not_bleed_past_enclosing_script_close_tag", func(t *testing.T) {
		html := strings.Replace(
			baseHTML,
			`"ondemand.s": "synth02"`,
			`"412":"ondemand.s"`,
			1,
		)
		html = strings.Replace(
			html,
			`</body>`,
			`<script>var later = {"412":"deadbeef"};</script></body>`,
			1,
		)
		parsed, err := ParseHomeHTML(html)
		if err != nil {
			t.Fatalf("ParseHomeHTML failed: %v", err)
		}
		if parsed.OndemandJSURL != "" {
			t.Fatalf("OndemandJSURL = %q, want empty (must not cross </script>)", parsed.OndemandJSURL)
		}
	})

	t.Run("prefers_modern_client_web_main_over_preceding_legacy_nomodule", func(t *testing.T) {
		legacyLink := `<link rel="preload" as="script" href="https://abs.twimg.com/responsive-web/client-web-legacy/main.old01a.js" />`
		legacyScript := `<script nomodule crossorigin="anonymous" src="https://abs.twimg.com/responsive-web/client-web-legacy/main.old01a.js"></script>`
		html := strings.Replace(
			baseHTML,
			`<link rel="preload" as="script" href="https://abs.twimg.com/responsive-web/client-web/main.synth01a.js" />`,
			legacyLink+"\n  "+legacyScript+"\n  "+`<link rel="preload" as="script" href="https://abs.twimg.com/responsive-web/client-web/main.synth01a.js" />`,
			1,
		)
		parsed, err := ParseHomeHTML(html)
		if err != nil {
			t.Fatalf("ParseHomeHTML failed: %v", err)
		}
		wantMain := "https://abs.twimg.com/responsive-web/client-web/main.synth01a.js"
		if parsed.MainJSURL != wantMain {
			t.Fatalf("MainJSURL = %q, want %q", parsed.MainJSURL, wantMain)
		}
		wantOndemand := "https://abs.twimg.com/responsive-web/client-web/ondemand.s.synth02a.js"
		if parsed.OndemandJSURL != wantOndemand {
			t.Fatalf("OndemandJSURL = %q, want %q", parsed.OndemandJSURL, wantOndemand)
		}
	})
}

func TestParseMainBundleInitialStateDefaultsAndEmptyFeatureSwitches(t *testing.T) {
	homeBytes := mustReadTestdata(t, "webpack_home.html")
	mainBytes := mustReadTestdata(t, "webpack_main.js")
	ondemandBytes := mustReadTestdata(t, "webpack_ondemand.js")

	t.Run("scans_initial_state_featureSwitch_defaults_including_dotted_and_hyphenated_keys", func(t *testing.T) {
		// Remove creator_subscriptions_tweet_preview_api_enabled from main.js defaultConfig
		// and add a dotted/hyphenated feature switch that exists ONLY in __INITIAL_STATE__.
		mutMain := string(mainBytes)
		mutMain = strings.Replace(
			mutMain,
			`creator_subscriptions_tweet_preview_api_enabled:{value:!0},`,
			``,
			1,
		)
		mutMain = strings.Replace(
			mutMain,
			`"longform_notetweets_consumption_enabled"`,
			`"longform_notetweets_consumption_enabled","rweb.custom-flag_1"`,
			1,
		)
		mutMain = strings.Replace(
			mutMain,
			`verified_phone_label_enabled:{value:!1},`,
			`verified_phone_label_enabled:{value:!1},`,
			1,
		)

		initialState := `<script>window.__INITIAL_STATE__={"featureSwitch":{"defaultConfig":{"creator_subscriptions_tweet_preview_api_enabled":{"value":true},"rweb.custom-flag_1":{"value":false},"verified_phone_label_enabled":{"value":false}},"user":{"config":{"rweb.custom-flag_1":{"value":true},"verified_phone_label_enabled":{"value":true}}}}};</script>`
		mutHome := strings.Replace(string(homeBytes), "<script>", initialState+"\n  <script>", 1)

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/responsive-web/client-web/main.synth01a.js":
				_, _ = w.Write([]byte(mutMain))
			case "/responsive-web/client-web/ondemand.s.synth02a.js":
				_, _ = w.Write(ondemandBytes)
			default:
				http.NotFound(w, r)
			}
		}))
		defer ts.Close()

		disc := NewFrontendDiscovery(FrontendDiscoveryConfig{
			FetchHome:      func(ctx context.Context, homeURL string) ([]byte, error) { return []byte(mutHome), nil },
			AssetTransport: newAbsRewriteTransport(t, ts),
		})
		meta, err := disc.Ensure(context.Background())
		if err != nil {
			t.Fatalf("Ensure failed: %v", err)
		}

		ut, ok := meta.Operation(OpUserTweets)
		if !ok {
			t.Fatalf("missing %s operation", OpUserTweets)
		}
		if !ut.Features["creator_subscriptions_tweet_preview_api_enabled"] {
			t.Errorf("expected creator_subscriptions_tweet_preview_api_enabled=true from __INITIAL_STATE__ defaultConfig")
		}
		if !ut.Features["rweb.custom-flag_1"] {
			t.Errorf("expected rweb.custom-flag_1=true from __INITIAL_STATE__ user.config")
		}
		fl, ok := meta.Operation(OpFollowing)
		if !ok {
			t.Fatalf("missing %s operation", OpFollowing)
		}
		if !fl.Features["verified_phone_label_enabled"] {
			t.Errorf("expected verified_phone_label_enabled=true overridden by __INITIAL_STATE__ user.config")
		}
	})

	t.Run("permits_empty_featureSwitches_and_executes_graphql_with_empty_features_object", func(t *testing.T) {
		mutMain := strings.Replace(
			string(mainBytes),
			`featureSwitches:["responsive_web_graphql_exclude_directive_enabled","verified_phone_label_enabled"]`,
			`featureSwitches:[]`,
			1,
		)

		var sawFeaturesParam atomic.Value
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/home":
				w.Header().Set("Content-Type", "text/html")
				_, _ = w.Write(homeBytes)
			case "/responsive-web/client-web/main.synth01a.js":
				w.Header().Set("Content-Type", "application/javascript")
				_, _ = w.Write([]byte(mutMain))
			case "/responsive-web/client-web/ondemand.s.synth02a.js":
				w.Header().Set("Content-Type", "application/javascript")
				_, _ = w.Write(ondemandBytes)
			case "/i/api/graphql/SynthQidFollowing05/Following":
				sawFeaturesParam.Store(r.URL.Query().Get("features"))
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"data":{"user":{"result":{"__typename":"User","timeline":{"timeline":{"instructions":[]}}}}}}`))
			default:
				http.NotFound(w, r)
			}
		}))
		defer ts.Close()

		_, ops, err := ParseMainBundle(mutMain)
		if err != nil {
			t.Fatalf("ParseMainBundle with empty featureSwitches failed: %v", err)
		}
		spec, ok := ops[OpFollowing]
		if !ok || spec.QueryID != "SynthQidFollowing05" {
			t.Fatalf("unexpected Following spec: %+v ok=%v", spec, ok)
		}
		if spec.Features == nil || len(spec.Features) != 0 {
			t.Fatalf("Following.Features = %#v, want non-nil empty map", spec.Features)
		}

		store := newTestXStore(t, "auth_token=SYNTH_EMPTYFEAT_01; ct0=SynthCT0EmptyFeat01")
		client := NewClient(ClientOptions{
			CredStore: store,
			Endpoints: Endpoints{
				HomeURL:         ts.URL + "/home",
				AccountSettings: ts.URL + "/i/api/1.1/account/settings.json",
				GraphQLBase:     ts.URL + "/i/api/graphql",
			},
			MinInterval:    -1,
			AllowLocalhost: true,
			AssetTransport: newAbsRewriteTransport(t, ts),
		})
		if _, err := client.ListFollowing(context.Background(), "1000000000000000101", "", nil); err != nil {
			t.Fatalf("ListFollowing with empty features failed: %v", err)
		}
		gotFeatures, _ := sawFeaturesParam.Load().(string)
		if gotFeatures != "{}" {
			t.Fatalf("GraphQL features query param = %q, want {}", gotFeatures)
		}
	})
}

func TestExtractBearerTokenPrefersClassicWebBearerOverIncidentalProse(t *testing.T) {
	const classicBearer = "AAAAAAAAAAAAAAAAAAAA" + "SynthClassicWebBearerToken0123456789%3Dabcdef0123456789"
	mainJS := string(mustReadTestdata(t, "webpack_main.js"))
	// Inject an incidental "Bearer ..." prose string and a named bearer_token BEFORE the real classic A{16,} bearer.
	prefix := `var helpDoc="Bearer incidental_placeholder_token_not_real"; var bearer_token="not_the_real_web_bearer_value_0001"; var realTok="` + classicBearer + `";`
	combined := prefix + "\n" + mainJS

	got := extractBearerToken(combined)
	if got != classicBearer {
		t.Fatalf("extractBearerToken() = %q, want classic bearer %q", got, classicBearer)
	}

	bearer, _, err := ParseMainBundle(combined)
	if err != nil {
		t.Fatalf("ParseMainBundle failed: %v", err)
	}
	if bearer != classicBearer {
		t.Fatalf("ParseMainBundle bearer = %q, want classic bearer %q", bearer, classicBearer)
	}
}

func stripOptionalOpsFromWebpackMain(mainJS string) string {
	lines := strings.Split(mainJS, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.Contains(line, `operationName:"Bookmarks"`) || strings.Contains(line, `operationName:"Likes"`) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

func TestFrontendDiscoverySplitLazyChunksWebpackAndVite(t *testing.T) {
	baseHome := string(mustReadTestdata(t, "webpack_home.html"))
	baseMain := string(mustReadTestdata(t, "webpack_main.js"))
	ondemandBytes := mustReadTestdata(t, "webpack_ondemand.js")
	coreOnlyMain := stripOptionalOpsFromWebpackMain(baseMain)

	bookmarksChunk := []byte(`(self.webpackChunk_synthetic_web=self.webpackChunk_synthetic_web||[]).push([[203],{` +
		`2006:function(e){e.exports={queryId:"SynthLazyBookmarksQid06",operationName:"Bookmarks",operationType:"query",metadata:{featureSwitches:["responsive_web_graphql_exclude_directive_enabled","longform_notetweets_consumption_enabled","rweb_lazy_bm_flag"]}}}` +
		`}]);`)
	likesChunk := []byte(`(self.webpackChunk_synthetic_web=self.webpackChunk_synthetic_web||[]).push([[315],{` +
		`2007:function(e){e.exports={queryId:"SynthLazyLikesQid07",operationName:"Likes",operationType:"query",metadata:{featureSwitches:["responsive_web_graphql_exclude_directive_enabled","verified_phone_label_enabled"]}}}` +
		`}]);`)

	t.Run("webpack_numeric_chunk_map_resolves_bookmarks_and_likes_lazily_with_singleflight", func(t *testing.T) {
		initialState := `<script>window.__INITIAL_STATE__={"featureSwitch":{"user":{"config":{"rweb_lazy_bm_flag":{"value":true}}}}};</script>`
		splitHome := strings.Replace(baseHome, "<script>", initialState+"\n  <script>", 1)
		splitHome = strings.Replace(
			splitHome,
			`"ondemand.s": "synth02"`,
			`412:"ondemand.s",203:"bundle.Bookmarks",315:"bundle.Likes"}; window.__SYNTH_HASHES__ = {412:"a1b2c3d4",203:"bb11cc22",315:"dd33ee44"`,
			1,
		)

		var bmHits, likesHits atomic.Int32
		var sawAuthHeader atomic.Bool
		bmGate := make(chan struct{})
		bmEntered := make(chan struct{}, 1)

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("X-Csrf-Token") != "" ||
				r.Header.Get("Referer") != "" || r.Header.Get("Origin") != "" {
				sawAuthHeader.Store(true)
			}
			switch r.URL.Path {
			case "/responsive-web/client-web/main.synth01a.js":
				_, _ = w.Write([]byte(coreOnlyMain))
			case "/responsive-web/client-web/ondemand.s.a1b2c3d4a.js":
				_, _ = w.Write(ondemandBytes)
			case "/responsive-web/client-web/bundle.Bookmarks.bb11cc22a.js":
				bmHits.Add(1)
				select {
				case bmEntered <- struct{}{}:
				default:
				}
				<-bmGate
				_, _ = w.Write(bookmarksChunk)
			case "/responsive-web/client-web/bundle.Likes.dd33ee44a.js":
				likesHits.Add(1)
				_, _ = w.Write(likesChunk)
			default:
				http.NotFound(w, r)
			}
		}))
		defer ts.Close()

		var chargeMu sync.Mutex
		var charged []FrontendRequestKind
		disc := NewFrontendDiscovery(FrontendDiscoveryConfig{
			FetchHome: func(ctx context.Context, homeURL string) ([]byte, error) {
				return []byte(splitHome), nil
			},
			ChargeRequest: func(ctx context.Context, kind FrontendRequestKind, targetURL string) error {
				chargeMu.Lock()
				charged = append(charged, kind)
				chargeMu.Unlock()
				return nil
			},
			AssetTransport: newAbsRewriteTransport(t, ts),
		})

		// 1. Core Ensure succeeds with ONLY 3 static fetches and all 5 CoreOperations!
		meta, err := disc.Ensure(context.Background())
		if err != nil {
			t.Fatalf("core Ensure failed on split webpack bundle: %v", err)
		}
		for _, opName := range CoreOperations() {
			if spec, ok := meta.Operation(opName); !ok || spec.QueryID == "" {
				t.Fatalf("missing core operation %s after Ensure", opName)
			}
		}
		if _, ok := meta.Operation(OpBookmarks); ok {
			t.Fatal("Bookmarks should not be present before lazy EnsureOperation")
		}
		if _, ok := meta.Operation(OpLikes); ok {
			t.Fatal("Likes should not be present before lazy EnsureOperation")
		}
		if bmHits.Load() != 0 || likesHits.Load() != 0 {
			t.Fatalf("Ensure eagerly fetched lazy chunks: bm=%d likes=%d", bmHits.Load(), likesHits.Load())
		}

		// 2. 8 concurrent callers for Bookmarks collapse into 1 singleflight lazy fetch!
		const callers = 8
		specs := make([]GraphQLOperationSpec, callers)
		errs := make([]error, callers)
		var wg sync.WaitGroup
		wg.Add(callers)
		for i := 0; i < callers; i++ {
			go func(idx int) {
				defer wg.Done()
				_, specs[idx], errs[idx] = disc.EnsureOperation(context.Background(), OpBookmarks)
			}(i)
		}
		<-bmEntered
		time.Sleep(20 * time.Millisecond)
		close(bmGate)
		wg.Wait()

		for i := 0; i < callers; i++ {
			if errs[i] != nil {
				t.Fatalf("caller %d EnsureOperation(Bookmarks) failed: %v", i, errs[i])
			}
			if specs[i].QueryID != "SynthLazyBookmarksQid06" {
				t.Fatalf("caller %d QueryID = %q, want SynthLazyBookmarksQid06", i, specs[i].QueryID)
			}
		}
		if bmHits.Load() != 1 {
			t.Fatalf("expected 1 singleflight fetch for Bookmarks, got %d", bmHits.Load())
		}
		// Feature defaults from main.*.js AND __INITIAL_STATE__ must be applied to the lazy chunk!
		if !specs[0].Features["responsive_web_graphql_exclude_directive_enabled"] ||
			!specs[0].Features["longform_notetweets_consumption_enabled"] ||
			!specs[0].Features["rweb_lazy_bm_flag"] {
			t.Fatalf("Bookmarks lazy feature defaults missing: %+v", specs[0].Features)
		}

		// Subsequent EnsureOperation(Bookmarks) hits memory with 0 network calls.
		if _, spec2, err := disc.EnsureOperation(context.Background(), OpBookmarks); err != nil || spec2.QueryID != "SynthLazyBookmarksQid06" {
			t.Fatalf("cached EnsureOperation(Bookmarks) = (%+v, %v)", spec2, err)
		}
		if bmHits.Load() != 1 {
			t.Fatalf("cached Bookmarks triggered extra fetch: %d", bmHits.Load())
		}

		// 3. Lazy EnsureOperation(Likes) resolves independently.
		updatedMeta, likesSpec, err := disc.EnsureOperation(context.Background(), OpLikes)
		if err != nil {
			t.Fatalf("EnsureOperation(Likes) failed: %v", err)
		}
		if likesSpec.QueryID != "SynthLazyLikesQid07" || !likesSpec.Features["responsive_web_graphql_exclude_directive_enabled"] || likesSpec.Features["verified_phone_label_enabled"] {
			t.Fatalf("unexpected Likes spec: %+v", likesSpec)
		}
		if likesHits.Load() != 1 {
			t.Fatalf("expected 1 fetch for Likes, got %d", likesHits.Load())
		}
		if _, ok := updatedMeta.Operation(OpBookmarks); !ok {
			t.Fatal("updated metadata lost previously merged Bookmarks")
		}
		if sawAuthHeader.Load() {
			t.Fatal("lazy chunk request leaked Cookie/Authorization/CSRF/Referer/Origin header")
		}

		chargeMu.Lock()
		gotCharged := append([]FrontendRequestKind(nil), charged...)
		chargeMu.Unlock()
		wantCharged := []FrontendRequestKind{
			FrontendRequestHomeHTML,
			FrontendRequestMainBundle,
			FrontendRequestOndemandJS,
			FrontendRequestLazyChunk,
			FrontendRequestLazyChunk,
		}
		if len(gotCharged) != len(wantCharged) {
			t.Fatalf("charged = %v, want %v", gotCharged, wantCharged)
		}
		for i := range wantCharged {
			if gotCharged[i] != wantCharged[i] {
				t.Fatalf("charged[%d] = %q, want %q", i, gotCharged[i], wantCharged[i])
			}
		}
	})

	t.Run("shared_second_candidate_chunk_populates_both_bookmarks_and_likes", func(t *testing.T) {
		splitHome := strings.Replace(
			baseHome,
			`"ondemand.s": "synth02"`,
			`"ondemand.s": "synth02", "bundle.Bookmarks": "empty01", "shared~bundle.Bookmarks~bundle.Likes": "both02", "endpoints": "never03"`,
			1,
		)
		sharedBoth := append(append([]byte{}, bookmarksChunk...), likesChunk...)

		var emptyHits, sharedHits, neverHits atomic.Int32
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/responsive-web/client-web/main.synth01a.js":
				_, _ = w.Write([]byte(coreOnlyMain))
			case "/responsive-web/client-web/ondemand.s.synth02a.js":
				_, _ = w.Write(ondemandBytes)
			case "/responsive-web/client-web/bundle.Bookmarks.empty01a.js":
				emptyHits.Add(1)
				_, _ = w.Write([]byte(`/* UI only, no GraphQL ops */ var x = 1;`))
			case "/responsive-web/client-web/shared~bundle.Bookmarks~bundle.Likes.both02a.js":
				sharedHits.Add(1)
				_, _ = w.Write(sharedBoth)
			case "/responsive-web/client-web/endpoints.never03a.js":
				neverHits.Add(1)
				_, _ = w.Write([]byte(`var noop = 1;`))
			default:
				http.NotFound(w, r)
			}
		}))
		defer ts.Close()

		disc := NewFrontendDiscovery(FrontendDiscoveryConfig{
			FetchHome:      func(ctx context.Context, homeURL string) ([]byte, error) { return []byte(splitHome), nil },
			AssetTransport: newAbsRewriteTransport(t, ts),
		})

		_, bmSpec, err := disc.EnsureOperation(context.Background(), OpBookmarks)
		if err != nil || bmSpec.QueryID != "SynthLazyBookmarksQid06" {
			t.Fatalf("EnsureOperation(Bookmarks) = (%+v, %v)", bmSpec, err)
		}
		if emptyHits.Load() != 1 || sharedHits.Load() != 1 || neverHits.Load() != 0 {
			t.Fatalf("unexpected chunk hits: empty=%d shared=%d never=%d", emptyHits.Load(), sharedHits.Load(), neverHits.Load())
		}

		// Likes was discovered in the same shared chunk -> 0 extra fetches!
		_, likesSpec, err := disc.EnsureOperation(context.Background(), OpLikes)
		if err != nil || likesSpec.QueryID != "SynthLazyLikesQid07" {
			t.Fatalf("EnsureOperation(Likes) after shared chunk = (%+v, %v)", likesSpec, err)
		}
		if emptyHits.Load() != 1 || sharedHits.Load() != 1 || neverHits.Load() != 0 {
			t.Fatalf("Likes triggered unexpected extra fetch: empty=%d shared=%d never=%d", emptyHits.Load(), sharedHits.Load(), neverHits.Load())
		}
	})

	t.Run("vite_dynamic_import_lazy_chunk_resolves_on_demand", func(t *testing.T) {
		viteHome := mustReadTestdata(t, "vite_home.html")
		viteMain := string(mustReadTestdata(t, "vite_main.js"))
		viteOndemand := mustReadTestdata(t, "vite_ondemand.js")
		// Remove BookmarksQuery from vite_main.js and replace with dynamic import("./Bookmarks-vite03.js").
		idxBM := strings.Index(viteMain, "export const BookmarksQuery")
		idxLk := strings.Index(viteMain, "export const LikesQuery")
		if idxBM < 0 || idxLk < 0 || idxBM >= idxLk {
			t.Fatalf("unexpected vite_main.js fixture structure")
		}
		splitViteMain := viteMain[:idxBM] + `const loadBookmarks = () => import("./Bookmarks-vite03.js");` + "\n\n" + viteMain[idxLk:]
		viteBMChunk := viteMain[idxBM:idxLk]

		var bmHits atomic.Int32
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/responsive-web/x-web/assets/main-vite01.js":
				_, _ = w.Write([]byte(splitViteMain))
			case "/responsive-web/x-web/assets/ondemand.s-vite02.js":
				_, _ = w.Write(viteOndemand)
			case "/responsive-web/x-web/assets/Bookmarks-vite03.js":
				bmHits.Add(1)
				_, _ = w.Write([]byte(viteBMChunk))
			default:
				http.NotFound(w, r)
			}
		}))
		defer ts.Close()

		disc := NewFrontendDiscovery(FrontendDiscoveryConfig{
			FetchHome:      func(ctx context.Context, homeURL string) ([]byte, error) { return viteHome, nil },
			AssetTransport: newAbsRewriteTransport(t, ts),
		})
		if _, err := disc.Ensure(context.Background()); err != nil {
			t.Fatalf("Vite core Ensure failed: %v", err)
		}
		if bmHits.Load() != 0 {
			t.Fatalf("Vite Ensure eagerly fetched lazy chunk")
		}
		_, spec, err := disc.EnsureOperation(context.Background(), OpBookmarks)
		if err != nil || spec.QueryID != "SynthViteQidBookmarks06" {
			t.Fatalf("Vite EnsureOperation(Bookmarks) = (%+v, %v)", spec, err)
		}
		if bmHits.Load() != 1 {
			t.Fatalf("expected 1 Vite lazy fetch, got %d", bmHits.Load())
		}
	})
}

func TestFrontendDiscoveryMissingOptionalMineOpsDoNotBreakCoreOrBreaker(t *testing.T) {
	baseHome := string(mustReadTestdata(t, "webpack_home.html"))
	baseMain := string(mustReadTestdata(t, "webpack_main.js"))
	ondemandBytes := mustReadTestdata(t, "webpack_ondemand.js")
	coreOnlyMain := stripOptionalOpsFromWebpackMain(baseMain)

	// Include 3 bookmark candidate chunks (none containing Bookmarks) to verify the <= 2 cap!
	homeWith3EmptyChunks := strings.Replace(
		baseHome,
		`"ondemand.s": "synth02"`,
		`"ondemand.s": "synth02", "bundle.Bookmarks": "miss01", "shared~bundle.Bookmarks": "miss02", "endpoints": "miss03"`,
		1,
	)

	var miss1Hits, miss2Hits, miss3Hits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/home":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(homeWith3EmptyChunks))
		case "/responsive-web/client-web/main.synth01a.js":
			_, _ = w.Write([]byte(coreOnlyMain))
		case "/responsive-web/client-web/ondemand.s.synth02a.js":
			_, _ = w.Write(ondemandBytes)
		case "/responsive-web/client-web/bundle.Bookmarks.miss01a.js":
			miss1Hits.Add(1)
			_, _ = w.Write([]byte(`var empty1 = true;`))
		case "/responsive-web/client-web/shared~bundle.Bookmarks.miss02a.js":
			miss2Hits.Add(1)
			_, _ = w.Write([]byte(`var empty2 = true;`))
		case "/responsive-web/client-web/endpoints.miss03a.js":
			miss3Hits.Add(1)
			_, _ = w.Write([]byte(`var empty3 = true;`))
		case "/i/api/1.1/account/settings.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"screen_name":"core_user"}`))
		case "/i/api/graphql/SynthQidUserByScreenName01/UserByScreenName":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(synthUserByScreenNamePayload("1000000000000000101", "core_user", "Core User", "")))
		case "/i/api/graphql/SynthQidFollowing05/Following":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"user":{"result":{"__typename":"User","timeline":{"timeline":{"instructions":[]}}}}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	var clockMu sync.Mutex
	curTime := time.Unix(1795327200, 0).UTC()
	nowFn := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return curTime
	}

	store := newTestXStore(t, "")
	var breakerCalls atomic.Int32
	client := NewClient(ClientOptions{
		CredStore: store,
		Endpoints: Endpoints{
			HomeURL:         ts.URL + "/home",
			AccountSettings: ts.URL + "/i/api/1.1/account/settings.json",
			GraphQLBase:     ts.URL + "/i/api/graphql",
		},
		MinInterval:             -1,
		AllowLocalhost:          true,
		AssetTransport:          newAbsRewriteTransport(t, ts),
		Now:                     nowFn,
		FrontendRefreshCooldown: DefaultFrontendRefreshCooldown,
	})
	client.SetAutomaticRequestHandlers(nil, func(_ string, _ error) {
		breakerCalls.Add(1)
	})

	// 1. Login succeeds even though Bookmarks and Likes are absent from main.*.js!
	st, err := client.LoginWithCookie(context.Background(), "auth_token=SYNTH_CORE_OK_01; ct0=SynthCT0CoreOk01; twid=u%3D1000000000000000101")
	if err != nil || !st.LoggedIn {
		t.Fatalf("LoginWithCookie failed when optional Bookmarks/Likes absent: st=%+v err=%v", st, err)
	}

	// 2. Core Following succeeds!
	if _, err := client.ListFollowing(context.Background(), "1000000000000000101", "", nil); err != nil {
		t.Fatalf("ListFollowing failed: %v", err)
	}

	// 3. ListBookmarks tries at most 2 candidate chunks, returns schema_changed, and NEVER trips autoBreaker!
	autoCtx := site.WithAutomaticRequests(context.Background(), "mine_bookmarks")
	_, bmErr := client.ListBookmarks(autoCtx, "", nil)
	if !site.IsKind(bmErr, site.ErrKindSchemaChanged) {
		t.Fatalf("ListBookmarks err = %v, want schema_changed", bmErr)
	}
	if miss1Hits.Load() != 1 || miss2Hits.Load() != 1 || miss3Hits.Load() != 0 {
		t.Fatalf("lazy chunk cap violated: miss1=%d miss2=%d miss3=%d (want 1,1,0)", miss1Hits.Load(), miss2Hits.Load(), miss3Hits.Load())
	}
	if breakerCalls.Load() != 0 {
		t.Fatalf("missing optional Bookmarks tripped full X circuit breaker (%d calls)", breakerCalls.Load())
	}
	if client.Discovery().Current() == nil {
		t.Fatal("missing optional Bookmarks cleared core FrontendMetadata")
	}

	// 4. Second ListBookmarks within 10m cooldown makes 0 upstream requests and returns schema_changed!
	clockMu.Lock()
	curTime = curTime.Add(3 * time.Minute)
	clockMu.Unlock()
	if _, err := client.ListBookmarks(autoCtx, "", nil); !site.IsKind(err, site.ErrKindSchemaChanged) {
		t.Fatalf("2nd ListBookmarks within cooldown err = %v, want schema_changed", err)
	}
	if miss1Hits.Load() != 1 || miss2Hits.Load() != 1 || miss3Hits.Load() != 0 {
		t.Fatalf("cooldown miss hit upstream: miss1=%d miss2=%d miss3=%d", miss1Hits.Load(), miss2Hits.Load(), miss3Hits.Load())
	}

	// 5. Core operations remain 100% healthy after Bookmarks failed!
	if _, err := client.ListFollowing(context.Background(), "1000000000000000101", "", nil); err != nil {
		t.Fatalf("ListFollowing after Bookmarks miss failed: %v", err)
	}
	if breakerCalls.Load() != 0 {
		t.Fatalf("unexpected circuit breaker calls: %d", breakerCalls.Load())
	}
}

func TestFrontendDiscoveryLazyChunkSecurityAndQuotaGuards(t *testing.T) {
	baseHome := string(mustReadTestdata(t, "webpack_home.html"))
	baseMain := string(mustReadTestdata(t, "webpack_main.js"))
	ondemandBytes := mustReadTestdata(t, "webpack_ondemand.js")
	coreOnlyMain := stripOptionalOpsFromWebpackMain(baseMain)

	t.Run("disallowed_host_lazy_chunk_in_html_is_ignored_with_zero_hits", func(t *testing.T) {
		evilHome := strings.Replace(
			baseHome,
			"</head>",
			`  <link rel="preload" as="script" href="https://evil.example.com/responsive-web/client-web/bundle.Bookmarks.evil01a.js" />`+"\n</head>",
			1,
		)
		var evilHits atomic.Int32
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/responsive-web/client-web/main.synth01a.js":
				_, _ = w.Write([]byte(coreOnlyMain))
			case "/responsive-web/client-web/ondemand.s.synth02a.js":
				_, _ = w.Write(ondemandBytes)
			default:
				evilHits.Add(1)
				http.NotFound(w, r)
			}
		}))
		defer ts.Close()

		disc := NewFrontendDiscovery(FrontendDiscoveryConfig{
			FetchHome:      func(ctx context.Context, homeURL string) ([]byte, error) { return []byte(evilHome), nil },
			AssetTransport: newAbsRewriteTransport(t, ts),
		})
		if _, err := disc.Ensure(context.Background()); err != nil {
			t.Fatalf("Ensure failed: %v", err)
		}
		_, _, err := disc.EnsureOperation(context.Background(), OpBookmarks)
		if !site.IsKind(err, site.ErrKindSchemaChanged) {
			t.Fatalf("EnsureOperation(Bookmarks) err = %v, want schema_changed", err)
		}
		if evilHits.Load() != 0 {
			t.Fatalf("disallowed lazy host hit upstream: %d", evilHits.Load())
		}
	})

	t.Run("lazy_chunk_redirect_to_disallowed_host_is_blocked_and_quota_aborts_before_fetch", func(t *testing.T) {
		splitHome := strings.Replace(
			baseHome,
			`"ondemand.s": "synth02"`,
			`"ondemand.s": "synth02", "bundle.Bookmarks": "redir01"`,
			1,
		)
		var evilHits atomic.Int32
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/responsive-web/client-web/main.synth01a.js":
				_, _ = w.Write([]byte(coreOnlyMain))
			case "/responsive-web/client-web/ondemand.s.synth02a.js":
				_, _ = w.Write(ondemandBytes)
			case "/responsive-web/client-web/bundle.Bookmarks.redir01a.js":
				w.Header().Set("Location", "https://evil.example.com/responsive-web/client-web/stolen.js")
				w.WriteHeader(http.StatusFound)
			default:
				evilHits.Add(1)
				w.WriteHeader(http.StatusOK)
			}
		}))
		defer ts.Close()

		disc := NewFrontendDiscovery(FrontendDiscoveryConfig{
			FetchHome:      func(ctx context.Context, homeURL string) ([]byte, error) { return []byte(splitHome), nil },
			AssetTransport: newAbsRewriteTransport(t, ts),
		})
		if _, _, err := disc.EnsureOperation(context.Background(), OpBookmarks); !site.IsKind(err, site.ErrKindBlocked) {
			t.Fatalf("redirected lazy chunk err = %v, want blocked", err)
		}
		if evilHits.Load() != 0 {
			t.Fatalf("followed disallowed redirect on lazy chunk")
		}

		// Quota hook rejecting FrontendRequestLazyChunk aborts BEFORE network and releases gate.
		var lazyNetHits atomic.Int32
		var held atomic.Int32
		quotaErr := &site.Error{Site: SiteID, Kind: site.ErrKindQuota, Message: "lazy quota hit"}
		discQuota := NewFrontendDiscovery(FrontendDiscoveryConfig{
			FetchHome: func(ctx context.Context, homeURL string) ([]byte, error) { return []byte(splitHome), nil },
			AcquireRequest: func(ctx context.Context, kind FrontendRequestKind, targetURL string) (func(), error) {
				held.Add(1)
				return func() { held.Add(-1) }, nil
			},
			ChargeRequest: func(ctx context.Context, kind FrontendRequestKind, targetURL string) error {
				if kind == FrontendRequestLazyChunk {
					return quotaErr
				}
				return nil
			},
			AssetTransport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if strings.Contains(req.URL.Path, "Bookmarks") {
					lazyNetHits.Add(1)
				}
				switch req.URL.Path {
				case "/responsive-web/client-web/main.synth01a.js":
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(coreOnlyMain)), Header: make(http.Header)}, nil
				case "/responsive-web/client-web/ondemand.s.synth02a.js":
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(ondemandBytes)), Header: make(http.Header)}, nil
				default:
					return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
				}
			}),
		})
		if _, _, err := discQuota.EnsureOperation(context.Background(), OpBookmarks); !errors.Is(err, quotaErr) {
			t.Fatalf("EnsureOperation with quota err = %v, want quotaErr", err)
		}
		if lazyNetHits.Load() != 0 {
			t.Fatalf("quota-rejected lazy chunk hit network (%d)", lazyNetHits.Load())
		}
		if held.Load() != 0 {
			t.Fatalf("AcquireRequest gate leaked after lazy quota reject: held=%d", held.Load())
		}
	})

	t.Run("oversized_lazy_chunk_exceeding_8mib_fails_without_breaking_core_or_leaking_bearer", func(t *testing.T) {
		const bearerSentinel = "SYNTHETIC_WEB_BEARER_TOKEN_0001"
		splitHome := strings.Replace(
			baseHome,
			`"ondemand.s": "synth02"`,
			`"ondemand.s": "synth02", "bundle.Bookmarks": "huge01"`,
			1,
		)
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/responsive-web/client-web/main.synth01a.js":
				_, _ = w.Write([]byte(coreOnlyMain))
			case "/responsive-web/client-web/ondemand.s.synth02a.js":
				_, _ = w.Write(ondemandBytes)
			case "/responsive-web/client-web/bundle.Bookmarks.huge01a.js":
				w.Header().Set("Content-Length", fmt.Sprintf("%d", MaxStaticAssetBytes+1))
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(bytes.Repeat([]byte("a"), 1024))
			default:
				http.NotFound(w, r)
			}
		}))
		defer ts.Close()

		disc := NewFrontendDiscovery(FrontendDiscoveryConfig{
			FetchHome:      func(ctx context.Context, homeURL string) ([]byte, error) { return []byte(splitHome), nil },
			AssetTransport: newAbsRewriteTransport(t, ts),
		})
		if _, err := disc.Ensure(context.Background()); err != nil {
			t.Fatalf("core Ensure failed: %v", err)
		}
		_, _, err := disc.EnsureOperation(context.Background(), OpBookmarks)
		if !site.IsKind(err, site.ErrKindSchemaChanged) {
			t.Fatalf("oversized lazy chunk err = %v, want schema_changed", err)
		}
		if strings.Contains(err.Error(), bearerSentinel) {
			t.Fatalf("Bearer sentinel leaked into lazy chunk error: %v", err)
		}
		// Core metadata remains completely intact.
		if cur := disc.Current(); cur == nil {
			t.Fatal("oversized lazy chunk cleared core metadata")
		} else if _, ok := cur.Operation(OpTweetDetail); !ok {
			t.Fatal("core TweetDetail missing after oversized lazy chunk")
		}
	})

	t.Run("lazy_bookmarks_empty_404_drift_refreshes_and_re_resolves_new_lazy_chunk", func(t *testing.T) {
		homeV1 := strings.Replace(
			baseHome,
			`"ondemand.s": "synth02"`,
			`"ondemand.s": "synth02", "bundle.Bookmarks": "bmv100"`,
			1,
		)
		homeV2 := strings.Replace(
			baseHome,
			`"ondemand.s": "synth02"`,
			`"ondemand.s": "synth02", "bundle.Bookmarks": "bmv200"`,
			1,
		)
		chunkV1 := []byte(`(self.webpackChunk_synthetic_web=self.webpackChunk_synthetic_web||[]).push([[203],{2006:function(e){e.exports={queryId:"SynthLazyBMV1Qid06",operationName:"Bookmarks",operationType:"query",metadata:{featureSwitches:["responsive_web_graphql_exclude_directive_enabled"]}}}}]);`)
		chunkV2 := []byte(`(self.webpackChunk_synthetic_web=self.webpackChunk_synthetic_web||[]).push([[203],{2006:function(e){e.exports={queryId:"SynthLazyBMV2Qid06",operationName:"Bookmarks",operationType:"query",metadata:{featureSwitches:["responsive_web_graphql_exclude_directive_enabled"]}}}}]);`)

		var ver atomic.Int32
		ver.Store(1)
		var v1Hits, v2Hits atomic.Int32
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/home":
				w.Header().Set("Content-Type", "text/html")
				if ver.Load() == 1 {
					_, _ = w.Write([]byte(homeV1))
				} else {
					_, _ = w.Write([]byte(homeV2))
				}
			case "/responsive-web/client-web/main.synth01a.js":
				_, _ = w.Write([]byte(coreOnlyMain))
			case "/responsive-web/client-web/ondemand.s.synth02a.js":
				_, _ = w.Write(ondemandBytes)
			case "/responsive-web/client-web/bundle.Bookmarks.bmv100a.js":
				_, _ = w.Write(chunkV1)
			case "/responsive-web/client-web/bundle.Bookmarks.bmv200a.js":
				_, _ = w.Write(chunkV2)
			case "/i/api/graphql/SynthLazyBMV1Qid06/Bookmarks":
				v1Hits.Add(1)
				ver.Store(2)
				w.WriteHeader(http.StatusNotFound)
			case "/i/api/graphql/SynthLazyBMV2Qid06/Bookmarks":
				v2Hits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"data":{"bookmark_timeline_v2":{"timeline":{"instructions":[]}}}}`))
			default:
				http.NotFound(w, r)
			}
		}))
		defer ts.Close()

		store := newTestXStore(t, "auth_token=SYNTH_LAZY_DRIFT; ct0=SynthCT0LazyDrift1")
		client := NewClient(ClientOptions{
			CredStore: store,
			Endpoints: Endpoints{
				HomeURL:         ts.URL + "/home",
				AccountSettings: ts.URL + "/i/api/1.1/account/settings.json",
				GraphQLBase:     ts.URL + "/i/api/graphql",
			},
			MinInterval:    -1,
			AllowLocalhost: true,
			AssetTransport: newAbsRewriteTransport(t, ts),
		})
		page, err := client.ListBookmarks(context.Background(), "", nil)
		if err != nil {
			t.Fatalf("lazy Bookmarks drift recovery failed: %v", err)
		}
		if page == nil || v1Hits.Load() != 1 || v2Hits.Load() != 1 {
			t.Fatalf("unexpected drift recovery state: page=%v v1=%d v2=%d", page != nil, v1Hits.Load(), v2Hits.Load())
		}
	})
}

func TestFrontendDiscoveryWebpackRealisticSplitBookmarksAndLikes(t *testing.T) {
	baseHome := string(mustReadTestdata(t, "webpack_home.html"))
	baseMain := string(mustReadTestdata(t, "webpack_main.js"))
	ondemandBytes := mustReadTestdata(t, "webpack_ondemand.js")
	routeChunkBytes := mustReadTestdata(t, "webpack_lazy_bookmark_route.js")
	harmonyBMBytes := mustReadTestdata(t, "webpack_lazy_bookmarks_harmony.js")
	coreOnlyMain := stripOptionalOpsFromWebpackMain(baseMain)

	t.Run("singular_bundle_bookmark_route_wrapper_and_harmony_esm_child_chunk", func(t *testing.T) {
		// Exercises:
		// 1) Hash map BEFORE ternary chunk-name expression in the same <script>
		// 2) Runtime `}[e]+".js"` suffix (hashes already ending in 'a', no double "aa.js")
		// 3) Singular "bundle.Bookmark" + "bundle.BookmarkFolders" + unnamed numeric chunk 4812
		// 4) 2nd-hop child chunk `n.e(4812)` inside webpack_lazy_bookmark_route.js
		// 5) Webpack 5 Harmony `n.d(t, {...})` with hoisted vars & decoy neighbor/mutation modules
		// 6) Nested `data.viewer.bookmark_timeline_v2` response in ListBookmarks
		initialState := `<script>window.__INITIAL_STATE__={"featureSwitch":{"defaultConfig":{"graphql_timeline_v2_bookmark_timeline":{"value":true}}}};</script>`
		runtimeScript := `window.__SYNTH_RUNTIME__ = function(e){` +
			`var h={412:"a1b2c3d4a",203:"11223344a",204:"55667788a",4812:"99aabbcca"};` +
			`return (412===e?"ondemand.s":203===e?"bundle.Bookmark":204===e?"bundle.BookmarkFolders":e)+"."+{412:"a1b2c3d4a",203:"11223344a",204:"55667788a",4812:"99aabbcca"}[e]+".js";` +
			`};`
		mutHome := strings.Replace(baseHome, "<script>", initialState+"\n  <script>", 1)
		mutHome = strings.Replace(
			mutHome,
			`window.__SYNTH_WEBPACK_CHUNKS__ = {
      "vendor": "synth99",
      "ondemand.s": "synth02"
    };`,
			runtimeScript,
			1,
		)

		var routeHits, harmonyHits, foldersHits, gqlHits atomic.Int32
		var sawAssetLeak atomic.Bool
		var sawFeaturesJSON atomic.Value

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/responsive-web/") {
				if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" ||
					r.Header.Get("X-Csrf-Token") != "" || r.Header.Get("Referer") != "" || r.Header.Get("Origin") != "" {
					sawAssetLeak.Store(true)
				}
			}
			switch r.URL.Path {
			case "/home":
				w.Header().Set("Content-Type", "text/html")
				_, _ = w.Write([]byte(mutHome))
			case "/responsive-web/client-web/main.synth01a.js":
				_, _ = w.Write([]byte(coreOnlyMain))
			case "/responsive-web/client-web/ondemand.s.a1b2c3d4a.js":
				_, _ = w.Write(ondemandBytes)
			case "/responsive-web/client-web/bundle.Bookmark.11223344a.js":
				routeHits.Add(1)
				_, _ = w.Write(routeChunkBytes)
			case "/responsive-web/client-web/4812.99aabbcca.js":
				harmonyHits.Add(1)
				_, _ = w.Write(harmonyBMBytes)
			case "/responsive-web/client-web/bundle.BookmarkFolders.55667788a.js":
				foldersHits.Add(1)
				_, _ = w.Write([]byte(`/* folders UI only */ var folders = 1;`))
			case "/i/api/graphql/SynthHarmonyBookmarksQid06/Bookmarks":
				gqlHits.Add(1)
				if r.Header.Get("x-client-transaction-id") == "" {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				sawFeaturesJSON.Store(r.URL.Query().Get("features"))
				w.Header().Set("Content-Type", "application/json")
				tweet := makeTimelineTweet("1791257321273778901", "1000000000000000010", "creator_one", "", "Harmony bookmarked post")
				_, _ = w.Write([]byte(`{"data":{"viewer":{"bookmark_timeline_v2":{"timeline":{"instructions":[{"type":"TimelineAddEntries","entries":[` +
					makeTweetEntry("tweet-1791257321273778901", tweet) +
					`]}]}}}}}`))
			default:
				http.NotFound(w, r)
			}
		}))
		defer ts.Close()

		store := newTestXStore(t, "auth_token=SYNTH_HARMONY_BM; ct0=SynthCT0HarmonyBM01")
		client := NewClient(ClientOptions{
			CredStore: store,
			Endpoints: Endpoints{
				HomeURL:         ts.URL + "/home",
				AccountSettings: ts.URL + "/i/api/1.1/account/settings.json",
				GraphQLBase:     ts.URL + "/i/api/graphql",
			},
			MinInterval:    -1,
			AllowLocalhost: true,
			AssetTransport: newAbsRewriteTransport(t, ts),
		})

		page, err := client.ListBookmarks(context.Background(), "", nil)
		if err != nil {
			t.Fatalf("ListBookmarks failed: %v", err)
		}
		if len(page.Posts) != 1 || page.Posts[0].ID != "1791257321273778901" {
			t.Fatalf("unexpected Bookmarks posts: %+v", page.Posts)
		}
		if sawAssetLeak.Load() {
			t.Fatal("static chunk request leaked auth/cookie/referer/origin headers")
		}
		if routeHits.Load() != 1 || harmonyHits.Load() != 1 || foldersHits.Load() != 0 || gqlHits.Load() != 1 {
			t.Fatalf("unexpected hit counts: route=%d harmony=%d folders=%d gql=%d (want 1,1,0,1)",
				routeHits.Load(), harmonyHits.Load(), foldersHits.Load(), gqlHits.Load())
		}

		rawFeat, _ := sawFeaturesJSON.Load().(string)
		var featMap map[string]bool
		if err := json.Unmarshal([]byte(rawFeat), &featMap); err != nil {
			t.Fatalf("unmarshal features param %q: %v", rawFeat, err)
		}
		if !featMap["graphql_timeline_v2_bookmark_timeline"] ||
			!featMap["responsive_web_graphql_exclude_directive_enabled"] ||
			!featMap["longform_notetweets_consumption_enabled"] {
			t.Fatalf("missing expected Bookmarks features: %+v", featMap)
		}
	})

	t.Run("long_shared_chunk_name_over_160_chars_and_cjs_assigned_likes", func(t *testing.T) {
		longSharedName := "shared~bundle.Account~bundle.Analytics~bundle.Birdwatch~bundle.BookmarkFolders~bundle.Bookmarks~bundle.Communities~bundle.DirectMessages~bundle.Explore~bundle.Lists~bundle.Notifications~bundle.Profile~bundle.Search~bundle.Settings"
		if len(longSharedName) <= 180 {
			t.Fatalf("expected >180 char chunk name, got %d", len(longSharedName))
		}
		splitHome := strings.Replace(
			baseHome,
			`"ondemand.s": "synth02"`,
			`412:"ondemand.s",601:"`+longSharedName+`",702:"loader.Likes"}; window.__SYNTH_HASHES__ = {412:"a1b2c3d4",601:"ab12cd34",702:"ef56ab78"`,
			1,
		)
		cjsLikesChunk := []byte(`(self.webpackChunk_synthetic_web=self.webpackChunk_synthetic_web||[]).push([[702],{` +
			`7020:function(e,t){"use strict";Object.defineProperty(t,"__esModule",{value:!0});t.metadata=t.operationType=t.operationName=t.queryId=void 0;` +
			`const q="SynthCJSLikesQid07",m={featureSwitches:["responsive_web_graphql_exclude_directive_enabled","verified_phone_label_enabled"]};` +
			`t.queryId=q,t.operationName="Likes",t.operationType="query",t.metadata=m;}` +
			`}]);`)

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/responsive-web/client-web/main.synth01a.js":
				_, _ = w.Write([]byte(coreOnlyMain))
			case "/responsive-web/client-web/ondemand.s.a1b2c3d4a.js":
				_, _ = w.Write(ondemandBytes)
			case "/responsive-web/client-web/" + longSharedName + ".ab12cd34a.js":
				_, _ = w.Write(harmonyBMBytes)
			case "/responsive-web/client-web/loader.Likes.ef56ab78a.js":
				_, _ = w.Write(cjsLikesChunk)
			default:
				http.NotFound(w, r)
			}
		}))
		defer ts.Close()

		disc := NewFrontendDiscovery(FrontendDiscoveryConfig{
			FetchHome:      func(ctx context.Context, homeURL string) ([]byte, error) { return []byte(splitHome), nil },
			AssetTransport: newAbsRewriteTransport(t, ts),
		})
		_, bmSpec, err := disc.EnsureOperation(context.Background(), OpBookmarks)
		if err != nil || bmSpec.QueryID != "SynthHarmonyBookmarksQid06" {
			t.Fatalf("EnsureOperation(Bookmarks) on >160-char shared chunk = (%+v, %v)", bmSpec, err)
		}
		_, lkSpec, err := disc.EnsureOperation(context.Background(), OpLikes)
		if err != nil || lkSpec.QueryID != "SynthCJSLikesQid07" {
			t.Fatalf("EnsureOperation(Likes) on CJS assigned chunk = (%+v, %v)", lkSpec, err)
		}
		if !lkSpec.Features["responsive_web_graphql_exclude_directive_enabled"] || lkSpec.Features["verified_phone_label_enabled"] {
			t.Fatalf("unexpected CJS Likes features: %+v", lkSpec.Features)
		}
	})

	t.Run("pure_numeric_route_loader_in_main_js_without_named_bookmark_chunk", func(t *testing.T) {
		numericHome := strings.Replace(
			baseHome,
			`"ondemand.s": "synth02"`,
			`412:"ondemand.s"}; var u=function(e){return e+"."+{412:"a1b2c3d4",7701:"ee11ff22"}[e]+"a.js"}; var unused={0:0`,
			1,
		)
		mainWithRoute := coreOnlyMain + "\n" + `var routes=[{path:"/i/bookmarks",loader:()=>Promise.all([n.e(7701)]).then(n.bind(n,48120))}];`

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/responsive-web/client-web/main.synth01a.js":
				_, _ = w.Write([]byte(mainWithRoute))
			case "/responsive-web/client-web/ondemand.s.a1b2c3d4a.js":
				_, _ = w.Write(ondemandBytes)
			case "/responsive-web/client-web/7701.ee11ff22a.js":
				_, _ = w.Write(harmonyBMBytes)
			default:
				http.NotFound(w, r)
			}
		}))
		defer ts.Close()

		disc := NewFrontendDiscovery(FrontendDiscoveryConfig{
			FetchHome:      func(ctx context.Context, homeURL string) ([]byte, error) { return []byte(numericHome), nil },
			AssetTransport: newAbsRewriteTransport(t, ts),
		})
		_, bmSpec, err := disc.EnsureOperation(context.Background(), OpBookmarks)
		if err != nil || bmSpec.QueryID != "SynthHarmonyBookmarksQid06" {
			t.Fatalf("EnsureOperation(Bookmarks) via /i/bookmarks route numeric chunk = (%+v, %v)", bmSpec, err)
		}
	})
}

func TestFrontendDiscoveryViteMapDepsAndOperationAliases(t *testing.T) {
	viteHome := mustReadTestdata(t, "vite_home.html")
	viteMain := string(mustReadTestdata(t, "vite_main.js"))
	viteOndemand := mustReadTestdata(t, "vite_ondemand.js")
	viteLazyBM := mustReadTestdata(t, "vite_lazy_bookmarks.js")

	idxBM := strings.Index(viteMain, "export const BookmarksQuery")
	idxLk := strings.Index(viteMain, "export const LikesQuery")
	if idxBM < 0 || idxLk < 0 || idxBM >= idxLk {
		t.Fatalf("unexpected vite_main.js structure")
	}

	t.Run("vite_mapdeps_assets_prefix_dedup_and_es6_spread_shorthand", func(t *testing.T) {
		// __vite__mapDeps lists "assets/BookmarksTimeline-vite04.js" without leading "./"
		// while main sits at "/responsive-web/x-web/assets/main-vite01.js".
		mapDeps := `const __vite__mapDeps=(i,m=__vite__mapDeps,d=(m.f||(m.f=["assets/BookmarksTimeline-vite04.js"])))=>i.map(i=>d[i]);`
		splitViteMain := viteMain[:idxBM] + mapDeps + "\n\n" + viteMain[idxLk:]

		var lazyHits atomic.Int32
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/responsive-web/x-web/assets/main-vite01.js":
				_, _ = w.Write([]byte(splitViteMain))
			case "/responsive-web/x-web/assets/ondemand.s-vite02.js":
				_, _ = w.Write(viteOndemand)
			case "/responsive-web/x-web/assets/BookmarksTimeline-vite04.js":
				lazyHits.Add(1)
				_, _ = w.Write(viteLazyBM)
			default:
				http.NotFound(w, r)
			}
		}))
		defer ts.Close()

		disc := NewFrontendDiscovery(FrontendDiscoveryConfig{
			FetchHome:      func(ctx context.Context, homeURL string) ([]byte, error) { return viteHome, nil },
			AssetTransport: newAbsRewriteTransport(t, ts),
		})
		_, spec, err := disc.EnsureOperation(context.Background(), OpBookmarks)
		if err != nil || spec.QueryID != "SynthViteLazyBookmarksQid06" {
			t.Fatalf("Vite __vite__mapDeps EnsureOperation(Bookmarks) = (%+v, %v)", spec, err)
		}
		if spec.OperationName != OpBookmarks {
			t.Fatalf("spec.OperationName = %q, want %q", spec.OperationName, OpBookmarks)
		}
		if lazyHits.Load() != 1 {
			t.Fatalf("expected 1 lazy hit, got %d", lazyHits.Load())
		}
	})

	t.Run("bookmark_timeline_alias_fallback_and_canonical_precedence", func(t *testing.T) {
		aliasOnlyChunk := []byte(`export const BookmarkTimelineQuery = { params: { id: "SynthAliasBookmarkTimeline06", name: "BookmarkTimeline", operationKind: "query", metadata: { featureSwitches: ["responsive_web_graphql_exclude_directive_enabled", "longform_notetweets_consumption_enabled"] } } };`)
		splitViteMain := viteMain[:idxBM] + `const loadBM = () => import("./Bookmarks-alias01.js");` + "\n\n" + viteMain[idxLk:]

		var aliasGQLHits atomic.Int32
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/home":
				w.Header().Set("Content-Type", "text/html")
				_, _ = w.Write(viteHome)
			case "/responsive-web/x-web/assets/main-vite01.js":
				_, _ = w.Write([]byte(splitViteMain))
			case "/responsive-web/x-web/assets/ondemand.s-vite02.js":
				_, _ = w.Write(viteOndemand)
			case "/responsive-web/x-web/assets/Bookmarks-alias01.js":
				_, _ = w.Write(aliasOnlyChunk)
			case "/i/api/graphql/SynthAliasBookmarkTimeline06/BookmarkTimeline":
				aliasGQLHits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"data":{"bookmark_timeline_v2":{"timeline":{"instructions":[]}}}}`))
			default:
				http.NotFound(w, r)
			}
		}))
		defer ts.Close()

		store := newTestXStore(t, "auth_token=SYNTH_ALIAS_BM; ct0=SynthCT0AliasBM01")
		client := NewClient(ClientOptions{
			CredStore: store,
			Endpoints: Endpoints{
				HomeURL:         ts.URL + "/home",
				AccountSettings: ts.URL + "/i/api/1.1/account/settings.json",
				GraphQLBase:     ts.URL + "/i/api/graphql",
			},
			MinInterval:    -1,
			AllowLocalhost: true,
			AssetTransport: newAbsRewriteTransport(t, ts),
		})
		if _, err := client.ListBookmarks(context.Background(), "", nil); err != nil {
			t.Fatalf("ListBookmarks with BookmarkTimeline alias failed: %v", err)
		}
		if aliasGQLHits.Load() != 1 {
			t.Fatalf("expected 1 hit on /BookmarkTimeline alias endpoint, got %d", aliasGQLHits.Load())
		}

		// When canonical "Bookmarks" and alias "BookmarkTimeline" both exist, canonical strictly wins.
		bothSpecs := parseLazyChunkOperations(
			string(aliasOnlyChunk)+"\n"+`export const CanonicalBM = { queryId: "SynthCanonicalBM06", operationName: "Bookmarks", operationType: "query", metadata: { featureSwitches: [] } };`,
			nil,
		)
		gotBM, ok := bothSpecs[OpBookmarks]
		if !ok || gotBM.QueryID != "SynthCanonicalBM06" || gotBM.OperationName != OpBookmarks {
			t.Fatalf("canonical Bookmarks did not win over alias: %+v (ok=%v)", gotBM, ok)
		}
	})
}

func TestGraphQLMissingNullFeaturesSelfHealOnRefreshReplay(t *testing.T) {
	baseHome := string(mustReadTestdata(t, "webpack_home.html"))
	baseMain := string(mustReadTestdata(t, "webpack_main.js"))
	ondemandBytes := mustReadTestdata(t, "webpack_ondemand.js")
	coreOnlyMain := stripOptionalOpsFromWebpackMain(baseMain)

	initialState := `<script>window.__INITIAL_STATE__={"featureSwitch":{"defaultConfig":{"rweb_custom_bm_flag":{"value":true},"rweb_disabled_bm_flag":{"value":false}}}};</script>`
	splitHome := strings.Replace(baseHome, "<script>", initialState+"\n  <script>", 1)
	splitHome = strings.Replace(
		splitHome,
		`"ondemand.s": "synth02"`,
		`"ondemand.s": "synth02", "bundle.Bookmarks": "bmheal01"`,
		1,
	)
	minimalBMChunk := []byte(`(self.webpackChunk_synthetic_web=self.webpackChunk_synthetic_web||[]).push([[203],{` +
		`2006:function(e){e.exports={queryId:"SynthHealBMQid06",operationName:"Bookmarks",operationType:"query",metadata:{featureSwitches:["responsive_web_graphql_exclude_directive_enabled"]}}}` +
		`}]);`)

	var gqlAttempts atomic.Int32
	var breakerCalls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/home":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(splitHome))
		case "/responsive-web/client-web/main.synth01a.js":
			_, _ = w.Write([]byte(coreOnlyMain))
		case "/responsive-web/client-web/ondemand.s.synth02a.js":
			_, _ = w.Write(ondemandBytes)
		case "/responsive-web/client-web/bundle.Bookmarks.bmheal01a.js":
			_, _ = w.Write(minimalBMChunk)
		case "/i/api/graphql/SynthHealBMQid06/Bookmarks":
			attempt := gqlAttempts.Add(1)
			var feats map[string]bool
			_ = json.Unmarshal([]byte(r.URL.Query().Get("features")), &feats)
			_, hasTimelineFlag := feats["graphql_timeline_v2_bookmark_timeline"]
			_, hasCustomFlag := feats["rweb_custom_bm_flag"]
			_, hasDisabledFlag := feats["rweb_disabled_bm_flag"]
			if !hasTimelineFlag || !hasCustomFlag || !hasDisabledFlag {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"errors":[{"code":336,"message":"The following features cannot be null: graphql_timeline_v2_bookmark_timeline, rweb_custom_bm_flag, rweb_disabled_bm_flag"}]}`))
				return
			}
			if !feats["graphql_timeline_v2_bookmark_timeline"] || !feats["rweb_custom_bm_flag"] || feats["rweb_disabled_bm_flag"] {
				t.Errorf("attempt %d unexpected healed feature values: %+v", attempt, feats)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"bookmark_timeline_v2":{"timeline":{"instructions":[]}}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	store := newTestXStore(t, "auth_token=SYNTH_HEAL_BM; ct0=SynthCT0HealBM01")
	client := NewClient(ClientOptions{
		CredStore: store,
		Endpoints: Endpoints{
			HomeURL:         ts.URL + "/home",
			AccountSettings: ts.URL + "/i/api/1.1/account/settings.json",
			GraphQLBase:     ts.URL + "/i/api/graphql",
		},
		MinInterval:    -1,
		AllowLocalhost: true,
		AssetTransport: newAbsRewriteTransport(t, ts),
	})
	client.SetAutomaticRequestHandlers(nil, func(_ string, _ error) {
		breakerCalls.Add(1)
	})

	// First call hits 400 features_cannot_be_null -> refreshes once, heals missing flags from __INITIAL_STATE__, replays once -> succeeds!
	if _, err := client.ListBookmarks(context.Background(), "", nil); err != nil {
		t.Fatalf("first ListBookmarks self-heal failed: %v", err)
	}
	if gqlAttempts.Load() != 2 {
		t.Fatalf("expected 2 GraphQL attempts (initial 400 + 1 healed replay), got %d", gqlAttempts.Load())
	}

	// Second call reuses the merged features in memory immediately without another 400!
	if _, err := client.ListBookmarks(context.Background(), "", nil); err != nil {
		t.Fatalf("second ListBookmarks failed: %v", err)
	}
	if gqlAttempts.Load() != 3 {
		t.Fatalf("expected 3 total GraphQL attempts after cached 2nd call, got %d", gqlAttempts.Load())
	}
	if breakerCalls.Load() != 0 {
		t.Fatalf("unexpected circuit breaker calls: %d", breakerCalls.Load())
	}
}
