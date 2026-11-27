package x

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/chialecode/trace/internal/site"
)

func TestResolveLinkAllShapesMirrorsReservedAndUnsupportedZeroRequests(t *testing.T) {
	store := newTestXStore(t, "auth_token=SYNTH_AUTH_RES; ct0=SynthCT0Res0001")
	var userScreenCalls atomic.Int32
	var totalAPIHits atomic.Int32

	_, client := newSynthFrontendHarnessWithOpts(t, func(w http.ResponseWriter, r *http.Request) {
		totalAPIHits.Add(1)
		if strings.HasSuffix(r.URL.Path, "/"+OpUserByScreenName) {
			userScreenCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(synthUserByScreenNamePayload("1000000000000000042", "example_user", "Example User", "")))
			return
		}
		http.NotFound(w, r)
	}, store, nil)

	// 1. Local-only single post shapes & mirrors (0 API requests!).
	postInputs := []struct {
		name  string
		input string
		want  string
	}{
		{"x.com status", "https://x.com/example_user/status/1791257321273778509", "1791257321273778509"},
		{"twitter.com status", "https://twitter.com/example_user/status/1791257321273778509?s=20&t=synth", "1791257321273778509"},
		{"mobile.twitter.com statuses", "https://mobile.twitter.com/example_user/statuses/1791257321273778509", "1791257321273778509"},
		{"mobile.x.com photo tail", "https://mobile.x.com/example_user/status/1791257321273778509/photo/2", "1791257321273778509"},
		{"m.twitter.com video tail", "https://m.twitter.com/example_user/status/1791257321273778509/video/1", "1791257321273778509"},
		{"quotes tail", "https://x.com/example_user/status/1791257321273778509/quotes", "1791257321273778509"},
		{"i web status", "https://x.com/i/web/status/1791257321273778509", "1791257321273778509"},
		{"i status", "https://x.com/i/status/1791257321273778509", "1791257321273778509"},
		{"share text prefers X over t.co", "快看这条 https://t.co/SynthShort01 原帖 https://x.com/example_user/status/1791257321273778509！", "1791257321273778509"},
		{"mirror fxtwitter with mp4 tail", "https://fxtwitter.com/example_user/status/1791257321273778509.mp4?s=20", "1791257321273778509"},
		{"mirror d.fxtwitter", "https://d.fxtwitter.com/example_user/status/1791257321273778509", "1791257321273778509"},
		{"mirror fixupx", "https://fixupx.com/example_user/status/1791257321273778509", "1791257321273778509"},
		{"mirror vxtwitter", "https://vxtwitter.com/example_user/status/1791257321273778509.jpg", "1791257321273778509"},
		{"mirror fixvx", "https://fixvx.com/example_user/status/1791257321273778509", "1791257321273778509"},
		{"mirror twittpr", "https://twittpr.com/example_user/status/1791257321273778509", "1791257321273778509"},
		{"mirror nitter fragment", "https://nitter.net/example_user/status/1791257321273778509#m", "1791257321273778509"},
		{"mirror xcancel", "https://xcancel.com/example_user/status/1791257321273778509#m", "1791257321273778509"},
	}

	for _, tc := range postInputs {
		t.Run(tc.name, func(t *testing.T) {
			before := totalAPIHits.Load()
			target, err := client.ResolveLink(context.Background(), tc.input)
			if err != nil {
				t.Fatalf("ResolveLink(%q) failed: %v", tc.input, err)
			}
			if target.Site != SiteID || target.Kind != "dynamic" || target.ID != tc.want {
				t.Fatalf("target = %+v, want dynamic/%s", target, tc.want)
			}
			if totalAPIHits.Load() != before {
				t.Fatalf("expected 0 API requests for %s, got %d", tc.name, totalAPIHits.Load()-before)
			}
		})
	}

	// 2. Local-only stable user rest_id inputs (0 API requests!).
	for _, tc := range []struct {
		name  string
		input string
		want  string
	}{
		{"i user", "https://x.com/i/user/1000000000000000042", "1000000000000000042"},
		{"intent user_id", "https://x.com/intent/user?user_id=1000000000000000042", "1000000000000000042"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := totalAPIHits.Load()
			target, err := client.ResolveLink(context.Background(), tc.input)
			if err != nil {
				t.Fatalf("ResolveLink(%q) failed: %v", tc.input, err)
			}
			if target.Kind != "user" || target.ID != tc.want || target.AuthorUID != tc.want {
				t.Fatalf("target = %+v, want user/%s", target, tc.want)
			}
			if totalAPIHits.Load() != before {
				t.Fatalf("expected 0 API requests for %s", tc.name)
			}
		})
	}

	// 3. Reserved paths MUST be rejected locally with 0 API requests!
	reserved := []string{
		"https://x.com/home",
		"https://x.com/explore",
		"https://x.com/notifications",
		"https://x.com/messages",
		"https://x.com/i",
		"https://x.com/settings",
		"https://x.com/search?q=golang",
		"https://x.com/compose/tweet",
		"https://x.com/login",
		"https://x.com/logout",
		"https://x.com/signup",
		"https://x.com/tos",
		"https://x.com/privacy",
		"https://x.com/jobs",
		"https://x.com/hashtag/golang",
		"https://x.com/lists",
		"https://x.com/communities",
		"https://x.com/articles",
		"https://x.com/i/bookmarks",
		"https://x.com/grok",
		"https://x.com/premium",
		"https://x.com/verified-orgs",
	}
	for _, u := range reserved {
		t.Run("reserved_"+u, func(t *testing.T) {
			before := totalAPIHits.Load()
			_, err := client.ResolveLink(context.Background(), u)
			if !site.IsKind(err, site.ErrKindUnsupportedLink) {
				t.Fatalf("reserved %q err = %v, want unsupported_link", u, err)
			}
			if totalAPIHits.Load() != before {
				t.Fatalf("reserved path %q made API requests!", u)
			}
		})
	}

	// 4. Broadcasts & Spaces MUST say exact LiveUnsupportedText with 0 API requests!
	liveInputs := []string{
		"https://x.com/i/broadcasts/1SyntheticBcastId",
		"https://periscope.tv/w/1SyntheticBcastId",
		"https://pscp.tv/w/1SyntheticBcastId",
		"https://x.com/i/spaces/1SyntheticSpaceId",
		"https://x.com/i/spaces/1SyntheticSpaceId/peek",
	}
	for _, u := range liveInputs {
		t.Run("live_"+u, func(t *testing.T) {
			before := totalAPIHits.Load()
			target, err := client.ResolveLink(context.Background(), u)
			if !site.IsKind(err, site.ErrKindUnsupportedLink) {
				t.Fatalf("live %q err = %v, want unsupported_link", u, err)
			}
			if !strings.Contains(err.Error(), LiveUnsupportedText) {
				t.Fatalf("live %q message = %q, want %q", u, err.Error(), LiveUnsupportedText)
			}
			if target.Kind != "live" || !target.Unsupported || target.Reason != LiveUnsupportedText {
				t.Fatalf("live %q target = %+v", u, target)
			}
			if totalAPIHits.Load() != before {
				t.Fatalf("live URL %q made API requests!", u)
			}
		})
	}

	// 5. Unsupported Articles / custom list slugs / Communities / bare digits / bare @handle -> 0 API requests!
	otherUnsupported := []string{
		"https://x.com/i/article/1000000000000000099",
		"https://x.com/example_user/article/1000000000000000099",
		"https://x.com/i/lists",
		"https://x.com/example_user/lists/tech-list",
		"https://x.com/i/communities/1000000000000000066",
		"@example_user",
		"1791257321273778509",
	}
	for _, u := range otherUnsupported {
		t.Run("unsupported_"+u, func(t *testing.T) {
			before := totalAPIHits.Load()
			_, err := client.ResolveLink(context.Background(), u)
			if !site.IsKind(err, site.ErrKindUnsupportedLink) {
				t.Fatalf("%q err = %v, want unsupported_link", u, err)
			}
			if totalAPIHits.Load() != before {
				t.Fatalf("%q made API requests!", u)
			}
		})
	}

	// 6. /<handle> and /intent/user?screen_name= resolve to rest_id in EXACTLY 1 UserByScreenName call each!
	for _, tc := range []struct {
		name  string
		input string
	}{
		{"handle profile", "https://x.com/example_user"},
		{"handle media tab", "https://x.com/example_user/media"},
		{"mirror handle profile", "https://xcancel.com/example_user"},
		{"intent screen_name", "https://x.com/intent/user?screen_name=example_user"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := userScreenCalls.Load()
			target, err := client.ResolveLink(context.Background(), tc.input)
			if err != nil {
				t.Fatalf("ResolveLink(%q) failed: %v", tc.input, err)
			}
			if target.Kind != "user" || target.ID != "1000000000000000042" {
				t.Fatalf("target = %+v", target)
			}
			if userScreenCalls.Load()-before != 1 {
				t.Fatalf("UserByScreenName calls = %d, want 1", userScreenCalls.Load()-before)
			}
		})
	}
}

func TestShortLinkTCoExpansionSecurityGuards(t *testing.T) {
	store := newTestXStore(t, "auth_token=SYNTH_AUTH_TCO; ct0=SynthCT0Tco0001")

	var sawAuthLeak atomic.Bool
	var finalPageHits atomic.Int32

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/tco/") {
			if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" ||
				r.Header.Get("x-csrf-token") != "" || r.Header.Get("x-twitter-auth-type") != "" {
				sawAuthLeak.Store(true)
			}
		}
		switch r.URL.Path {
		case "/tco/hop1":
			http.Redirect(w, r, "/tco/hop2", http.StatusFound)
		case "/tco/hop2":
			// Redirects directly to canonical X post; resolver MUST converge early without hitting x.com!
			http.Redirect(w, r, "https://x.com/example_user/status/1791257321273778509", http.StatusFound)
		case "/tco/ext":
			http.Redirect(w, r, "https://evil.example.com/phish", http.StatusFound)
		case "/tco/httpdowngrade":
			http.Redirect(w, r, "http://x.com/example_user/status/1791257321273778509", http.StatusFound)
		case "/tco/badport":
			http.Redirect(w, r, "https://x.com:8443/example_user/status/1791257321273778509", http.StatusFound)
		case "/tco/userinfo":
			http.Redirect(w, r, "https://user:pass@x.com/example_user/status/1791257321273778509", http.StatusFound)
		case "/tco/loop":
			http.Redirect(w, r, "/tco/loop", http.StatusFound)
		case "/tco/chain1":
			http.Redirect(w, r, "/tco/chain2", http.StatusFound)
		case "/tco/chain2":
			http.Redirect(w, r, "/tco/chain3", http.StatusFound)
		case "/tco/chain3":
			http.Redirect(w, r, "/tco/chain4", http.StatusFound)
		case "/tco/chain4":
			http.Redirect(w, r, "/tco/chain5", http.StatusFound)
		case "/tco/chain5":
			http.Redirect(w, r, "/tco/chain6", http.StatusFound)
		case "/tco/chain6":
			http.Redirect(w, r, "https://x.com/example_user/status/1791257321273778509", http.StatusFound)
		default:
			finalPageHits.Add(1)
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	client := NewClient(ClientOptions{
		CredStore:      store,
		MinInterval:    -1,
		AllowLocalhost: true,
	})

	// 1. Valid 2-hop expansion converges early on X post with 0 auth headers leaked.
	target, err := client.ResolveLink(context.Background(), ts.URL+"/tco/hop1")
	if err != nil {
		t.Fatalf("t.co 2-hop resolve failed: %v", err)
	}
	if target.Kind != "dynamic" || target.ID != "1791257321273778509" {
		t.Fatalf("target = %+v", target)
	}
	if sawAuthLeak.Load() {
		t.Fatalf("t.co expansion leaked Cookie/Authorization/CSRF header")
	}
	if finalPageHits.Load() != 0 {
		t.Fatalf("t.co expansion should converge early without fetching final page")
	}

	// 2. Security blocks: external host, HTTP downgrade, non-443 port, userinfo, loop, >5 hops.
	for _, path := range []string{"/tco/ext", "/tco/httpdowngrade", "/tco/badport", "/tco/userinfo", "/tco/loop", "/tco/chain1"} {
		t.Run("block_"+path, func(t *testing.T) {
			if _, err := client.ResolveLink(context.Background(), ts.URL+path); !site.IsKind(err, site.ErrKindBlocked) {
				t.Fatalf("ResolveLink(%s) err = %v, want blocked", path, err)
			}
		})
	}

	// 3. Input-level guards (non-localhost client): reject userinfo, non-443 port, non-HTTPS x.com, private IP.
	strictClient := NewClient(ClientOptions{CredStore: store, MinInterval: -1, AllowLocalhost: false})
	for _, bad := range []string{
		"https://user:pass@t.co/Synth01",
		"https://t.co:8443/Synth01",
		"http://x.com/example_user/status/1791257321273778509",
		"https://127.0.0.1/example_user/status/1791257321273778509",
		"https://192.168.1.1/example_user/status/1791257321273778509",
	} {
		t.Run("strict_reject_"+bad, func(t *testing.T) {
			if _, err := strictClient.ResolveLink(context.Background(), bad); !site.IsKind(err, site.ErrKindBlocked) {
				t.Fatalf("strictClient.ResolveLink(%q) err = %v, want blocked", bad, err)
			}
		})
	}

	// 4. Unauthenticated client must reject online ResolveLink with auth_required and 0 hits!
	loggedOutClient := NewClient(ClientOptions{CredStore: newTestXStore(t, ""), MinInterval: -1, AllowLocalhost: true})
	if _, err := loggedOutClient.ResolveLink(context.Background(), "https://x.com/example_user/status/1791257321273778509"); !site.IsKind(err, site.ErrKindAuthRequired) {
		t.Fatalf("logged-out ResolveLink err = %v, want auth_required", err)
	}
}
