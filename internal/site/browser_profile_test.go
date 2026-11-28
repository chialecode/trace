package site

import (
	"net/http"
	"strings"
	"testing"
)

func TestDeriveBrowserProfileDefaultAndChromiumVariants(t *testing.T) {
	p := DeriveBrowserProfile("")
	if p.UserAgent != DefaultUserAgent {
		t.Fatalf("UserAgent = %q, want %q", p.UserAgent, DefaultUserAgent)
	}
	if !strings.Contains(p.UserAgent, "Chrome/144.0.0.0") || !strings.Contains(p.UserAgent, "Windows NT 10.0; Win64; x64") {
		t.Fatalf("DefaultUserAgent not modern Windows Chrome 144: %q", p.UserAgent)
	}
	if !p.IsChromium || p.BrowserName != "Chrome" || p.BrowserVersion != "144.0.0.0" || p.MajorVersion != "144" {
		t.Fatalf("unexpected Chrome profile: %+v", p)
	}
	if p.EngineName != "Blink" || p.EngineVersion != "144.0.0.0" || p.OSName != "Windows" || p.BrowserPlatform != "Win32" {
		t.Fatalf("unexpected engine/OS fields: %+v", p)
	}
	if p.SecCHUA != `"Chromium";v="144", "Google Chrome";v="144", "Not.A/Brand";v="99"` ||
		p.SecCHUAMobile != "?0" || p.SecCHUAPlatform != `"Windows"` {
		t.Fatalf("unexpected Client Hints: ch=%q mob=%q plat=%q", p.SecCHUA, p.SecCHUAMobile, p.SecCHUAPlatform)
	}

	macUA := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36"
	mac := DeriveBrowserProfile(macUA)
	if !mac.IsChromium || mac.MajorVersion != "142" || mac.OSName != "Mac OS" || mac.BrowserPlatform != "MacIntel" || mac.SecCHUAPlatform != `"macOS"` {
		t.Fatalf("unexpected macOS Chrome profile: %+v", mac)
	}
	if !strings.HasSuffix(mac.BrowserInfo, "|MacIntel") {
		t.Fatalf("macOS BrowserInfo = %q", mac.BrowserInfo)
	}

	edgeUA := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/144.0.0.0 Safari/537.36 Edg/144.0.1.2"
	edge := DeriveBrowserProfile(edgeUA)
	if !edge.IsChromium || edge.BrowserName != "Edge" || !strings.Contains(edge.SecCHUA, `"Microsoft Edge";v="144"`) {
		t.Fatalf("unexpected Edge profile: %+v", edge)
	}

	linuxUA := "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chromium/141.0.0.0 Safari/537.36"
	lin := DeriveBrowserProfile(linuxUA)
	if !lin.IsChromium || lin.OSName != "Linux" || lin.BrowserPlatform != "Linux x86_64" || lin.SecCHUAPlatform != `"Linux"` {
		t.Fatalf("unexpected Linux Chromium profile: %+v", lin)
	}
}

func TestDeriveBrowserProfileNonChromiumGuard(t *testing.T) {
	cases := []struct {
		name        string
		ua          string
		wantBrowser string
		wantEngine  string
	}{
		{
			name:        "firefox_windows",
			ua:          "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:138.0) Gecko/20100101 Firefox/138.0",
			wantBrowser: "Firefox",
			wantEngine:  "Gecko",
		},
		{
			name:        "safari_macos",
			ua:          "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.3 Safari/605.1.15",
			wantBrowser: "Safari",
			wantEngine:  "WebKit",
		},
		{
			name:        "custom_non_chromium",
			ua:          "CustomClient/2.0 (Linux x86_64)",
			wantBrowser: "Mozilla",
			wantEngine:  "WebKit",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := DeriveBrowserProfile(tc.ua)
			if p.IsChromium {
				t.Fatalf("IsChromium = true for %s", tc.name)
			}
			if p.BrowserName != tc.wantBrowser || p.EngineName != tc.wantEngine {
				t.Fatalf("browser/engine = (%q, %q), want (%q, %q)", p.BrowserName, p.EngineName, tc.wantBrowser, tc.wantEngine)
			}
			if p.SecCHUA != "" || p.SecCHUAMobile != "" || p.SecCHUAPlatform != "" {
				t.Fatalf("non-Chromium UA emitted Client Hints: %+v", p)
			}

			hdr := make(http.Header)
			hdr.Set("Sec-Ch-Ua", `"Chromium";v="144"`)
			hdr.Set("Sec-Ch-Ua-Mobile", "?0")
			hdr.Set("Sec-Ch-Ua-Platform", `"Windows"`)
			p.ApplyClientHints(hdr)
			if hdr.Get("Sec-Ch-Ua") != "" || hdr.Get("Sec-Ch-Ua-Mobile") != "" || hdr.Get("Sec-Ch-Ua-Platform") != "" {
				t.Fatalf("ApplyClientHints did not clear Chromium hints for non-Chromium UA: %v", hdr)
			}
		})
	}
}

func TestDeriveSecFetchSite(t *testing.T) {
	cases := []struct {
		name    string
		referer string
		target  string
		want    string
	}{
		{"empty_referer", "", "https://x.com/home", "none"},
		{"invalid_referer", "not-a-url", "https://x.com/home", "none"},
		{"x_same_origin", "https://x.com/", "https://x.com/i/api/graphql/qid/UserTweets", "same-origin"},
		{"x_default_port_same_origin", "https://x.com:443/", "https://x.com/i/api/graphql/qid/UserTweets", "same-origin"},
		{"x_api_same_site", "https://x.com/", "https://api.x.com/1.1/account/settings.json", "same-site"},
		{"x_abs_cross_site", "https://x.com/", "https://abs.twimg.com/responsive-web/client-web/main.js", "cross-site"},
		{"weibo_same_origin", "https://weibo.com/u/1000000001", "https://weibo.com/ajax/profile/info", "same-origin"},
		{"weibo_tv_same_origin", "https://weibo.com/tv/show/1034:1", "https://weibo.com/tv/api/component", "same-origin"},
		{"douyin_same_origin", "https://www.douyin.com/", "https://www.douyin.com/aweme/v1/web/aweme/detail/", "same-origin"},
		{"douyin_live_same_site", "https://www.douyin.com/", "https://live.douyin.com/webcast/room/web/enter/", "same-site"},
		{"douyin_amemv_cross_site", "https://live.douyin.com/", "https://webcast.amemv.com/webcast/room/reflow/info/", "cross-site"},
		{"bili_space_to_api_same_site", "https://space.bilibili.com/123/", "https://api.bilibili.com/x/space/wbi/arc/search", "same-site"},
		{"bili_live_to_live_api_same_site", "https://live.bilibili.com/", "https://api.live.bilibili.com/room/v1/Room/room_init", "same-site"},
		{"bili_www_correspond_same_origin", "https://www.bilibili.com/", "https://www.bilibili.com/correspond/1/abcd", "same-origin"},
		{"scheme_downgrade_cross_site", "http://x.com/", "https://x.com/", "cross-site"},
		{"different_port_same_site", "https://x.com:8443/", "https://x.com/", "same-site"},
		{"two_label_public_suffix_same_site", "https://a.example.com.cn/", "https://b.example.com.cn/", "same-site"},
		{"two_label_public_suffix_cross_site", "https://a.com.cn/", "https://b.com.cn/", "cross-site"},
		{"ip_different_cross_site", "http://127.0.0.1:8080/", "http://192.0.2.1:8080/", "cross-site"},
		{"ip_same_different_port_same_site", "http://127.0.0.1:8080/", "http://127.0.0.1:9090/", "same-site"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DeriveSecFetchSite(tc.referer, tc.target); got != tc.want {
				t.Fatalf("DeriveSecFetchSite(%q, %q) = %q, want %q", tc.referer, tc.target, got, tc.want)
			}
		})
	}
}
