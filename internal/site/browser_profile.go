package site

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

var (
	reChromeVer  = regexp.MustCompile(`(?i)Chrome/(([0-9]+)(?:\.[0-9]+)*)`)
	reChromium   = regexp.MustCompile(`(?i)Chromium/(([0-9]+)(?:\.[0-9]+)*)`)
	reEdgeVer    = regexp.MustCompile(`(?i)Edg/(([0-9]+)(?:\.[0-9]+)*)`)
	reFirefoxVer = regexp.MustCompile(`(?i)Firefox/(([0-9]+)(?:\.[0-9]+)*)`)
	reSafariVer  = regexp.MustCompile(`(?i)Version/(([0-9]+)(?:\.[0-9]+)*).*Safari/`)
	reWebKitVer  = regexp.MustCompile(`(?i)AppleWebKit/([0-9]+(?:\.[0-9]+)*)`)
)

// twoLabelPublicSuffixes covers common multi-part eTLDs needed for stdlib-only
// Fetch Sec-Fetch-Site same-site comparison without external dependencies.
var twoLabelPublicSuffixes = map[string]struct{}{
	"ac.jp":  {},
	"ac.uk":  {},
	"co.in":  {},
	"co.jp":  {},
	"co.kr":  {},
	"co.nz":  {},
	"co.uk":  {},
	"com.au": {},
	"com.br": {},
	"com.cn": {},
	"com.hk": {},
	"com.my": {},
	"com.sg": {},
	"com.tw": {},
	"edu.cn": {},
	"gov.cn": {},
	"gov.uk": {},
	"ne.jp":  {},
	"net.cn": {},
	"or.jp":  {},
	"org.cn": {},
	"org.uk": {},
}

// BrowserProfile holds coherent desktop browser, OS, viewport, and Client Hints
// metadata derived from a single User-Agent string.
// For non-Chromium User-Agents (such as Firefox or Safari), IsChromium is false
// and SecCHUA/SecCHUAMobile/SecCHUAPlatform are strictly empty.
type BrowserProfile struct {
	UserAgent       string
	IsChromium      bool
	BrowserName     string
	BrowserVersion  string
	MajorVersion    string
	EngineName      string
	EngineVersion   string
	OSName          string
	OSVersion       string
	Platform        string
	BrowserPlatform string
	ScreenWidth     string
	ScreenHeight    string
	BrowserInfo     string
	SecCHUA         string
	SecCHUAMobile   string
	SecCHUAPlatform string
}

// DeriveBrowserProfile extracts consistent browser/platform fields and Client Hints from ua.
// When ua is blank, DefaultUserAgent (modern desktop Chrome on Windows) is used.
// Non-Chromium User-Agents (Firefox, Safari, custom clients) leave SecCHUA* empty so
// callers never emit mismatched Chromium Client Hints.
func DeriveBrowserProfile(ua string) BrowserProfile {
	ua = strings.TrimSpace(ua)
	if ua == "" {
		ua = DefaultUserAgent
	}

	osName := "Windows"
	osVer := "10"
	browserPlatform := "Win32"
	secPlatform := `"Windows"`
	lowerUA := strings.ToLower(ua)
	switch {
	case strings.Contains(lowerUA, "macintosh") || strings.Contains(lowerUA, "mac os x"):
		osName = "Mac OS"
		osVer = "10_15_7"
		browserPlatform = "MacIntel"
		secPlatform = `"macOS"`
	case strings.Contains(lowerUA, "linux") && !strings.Contains(lowerUA, "android"):
		osName = "Linux"
		osVer = "x86_64"
		browserPlatform = "Linux x86_64"
		secPlatform = `"Linux"`
	}

	browserInfo := fmt.Sprintf("1920|947|1920|1032|1920|1032|1920|1080|%s", browserPlatform)

	p := BrowserProfile{
		UserAgent:       ua,
		OSName:          osName,
		OSVersion:       osVer,
		Platform:        "PC",
		BrowserPlatform: browserPlatform,
		ScreenWidth:     "1920",
		ScreenHeight:    "1080",
		BrowserInfo:     browserInfo,
	}

	if m := reFirefoxVer.FindStringSubmatch(ua); len(m) == 3 {
		p.BrowserName = "Firefox"
		p.BrowserVersion = m[1]
		p.MajorVersion = m[2]
		p.EngineName = "Gecko"
		p.EngineVersion = m[1]
		return p
	}

	if chromeM := reChromeVer.FindStringSubmatch(ua); len(chromeM) == 3 {
		p.IsChromium = true
		p.EngineName = "Blink"
		p.EngineVersion = chromeM[1]
		p.SecCHUAMobile = "?0"
		p.SecCHUAPlatform = secPlatform
		if edgeM := reEdgeVer.FindStringSubmatch(ua); len(edgeM) == 3 {
			p.BrowserName = "Edge"
			p.BrowserVersion = edgeM[1]
			p.MajorVersion = edgeM[2]
			p.SecCHUA = fmt.Sprintf(`"Chromium";v="%s", "Microsoft Edge";v="%s", "Not.A/Brand";v="99"`, chromeM[2], edgeM[2])
			return p
		}
		p.BrowserName = "Chrome"
		p.BrowserVersion = chromeM[1]
		p.MajorVersion = chromeM[2]
		p.SecCHUA = fmt.Sprintf(`"Chromium";v="%s", "Google Chrome";v="%s", "Not.A/Brand";v="99"`, chromeM[2], chromeM[2])
		return p
	}

	if chromiumM := reChromium.FindStringSubmatch(ua); len(chromiumM) == 3 {
		p.IsChromium = true
		p.BrowserName = "Chromium"
		p.BrowserVersion = chromiumM[1]
		p.MajorVersion = chromiumM[2]
		p.EngineName = "Blink"
		p.EngineVersion = chromiumM[1]
		p.SecCHUA = fmt.Sprintf(`"Chromium";v="%s", "Not.A/Brand";v="99"`, chromiumM[2])
		p.SecCHUAMobile = "?0"
		p.SecCHUAPlatform = secPlatform
		return p
	}

	if safariM := reSafariVer.FindStringSubmatch(ua); len(safariM) == 3 {
		p.BrowserName = "Safari"
		p.BrowserVersion = safariM[1]
		p.MajorVersion = safariM[2]
		p.EngineName = "WebKit"
		if wk := reWebKitVer.FindStringSubmatch(ua); len(wk) == 2 {
			p.EngineVersion = wk[1]
		} else {
			p.EngineVersion = safariM[1]
		}
		return p
	}

	// Non-Chromium custom/unknown User-Agent: do NOT fabricate Chromium Client Hints.
	p.BrowserName = "Mozilla"
	p.BrowserVersion = "5.0"
	p.MajorVersion = "5"
	p.EngineName = "WebKit"
	p.EngineVersion = "537.36"
	return p
}

// ApplyClientHints sets Sec-Ch-Ua, Sec-Ch-Ua-Mobile, and Sec-Ch-Ua-Platform on h
// for Chromium-based profiles and deletes them for non-Chromium profiles.
func (p BrowserProfile) ApplyClientHints(h http.Header) {
	if h == nil {
		return
	}
	if !p.IsChromium || p.SecCHUA == "" {
		h.Del("Sec-Ch-Ua")
		h.Del("Sec-Ch-Ua-Mobile")
		h.Del("Sec-Ch-Ua-Platform")
		return
	}
	h.Set("Sec-Ch-Ua", p.SecCHUA)
	if p.SecCHUAMobile != "" {
		h.Set("Sec-Ch-Ua-Mobile", p.SecCHUAMobile)
	} else {
		h.Del("Sec-Ch-Ua-Mobile")
	}
	if p.SecCHUAPlatform != "" {
		h.Set("Sec-Ch-Ua-Platform", p.SecCHUAPlatform)
	} else {
		h.Del("Sec-Ch-Ua-Platform")
	}
}

// DeriveSecFetchSite computes the W3C Fetch Sec-Fetch-Site header value
// ("none", "same-origin", "same-site", or "cross-site") between referer and targetURL.
func DeriveSecFetchSite(referer, targetURL string) string {
	refStr := strings.TrimSpace(referer)
	if refStr == "" {
		return "none"
	}
	refURL, err := url.Parse(refStr)
	if err != nil || refURL.Scheme == "" || refURL.Hostname() == "" {
		return "none"
	}
	tgtURL, err := url.Parse(strings.TrimSpace(targetURL))
	if err != nil || tgtURL.Scheme == "" || tgtURL.Hostname() == "" {
		return "cross-site"
	}

	refScheme := strings.ToLower(refURL.Scheme)
	tgtScheme := strings.ToLower(tgtURL.Scheme)
	refHost := normalizeHostForFetchSite(refURL.Hostname())
	tgtHost := normalizeHostForFetchSite(tgtURL.Hostname())
	if refHost == "" || tgtHost == "" {
		return "cross-site"
	}

	if refScheme != tgtScheme {
		return "cross-site"
	}
	if refHost == tgtHost && effectivePort(refScheme, refURL.Port()) == effectivePort(tgtScheme, tgtURL.Port()) {
		return "same-origin"
	}
	if refHost == tgtHost {
		return "same-site"
	}
	if net.ParseIP(refHost) != nil || net.ParseIP(tgtHost) != nil {
		return "cross-site"
	}
	refSite := registrableSite(refHost)
	tgtSite := registrableSite(tgtHost)
	if refSite != "" && refSite == tgtSite {
		return "same-site"
	}
	return "cross-site"
}

func normalizeHostForFetchSite(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

func effectivePort(scheme, port string) string {
	if port != "" {
		return port
	}
	switch scheme {
	case "http":
		return "80"
	case "https":
		return "443"
	default:
		return ""
	}
}

func registrableSite(host string) string {
	if host == "" || net.ParseIP(host) != nil {
		return ""
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return host
	}
	lastTwo := labels[len(labels)-2] + "." + labels[len(labels)-1]
	if _, isTwoLabelSuffix := twoLabelPublicSuffixes[lastTwo]; isTwoLabelSuffix {
		if len(labels) >= 3 {
			return labels[len(labels)-3] + "." + lastTwo
		}
		return ""
	}
	return lastTwo
}
