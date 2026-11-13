// Package douyin implements the Douyin PC Web and Webcast protocol client and site adapter.
package douyin

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/chialecode/trace/internal/site/douyin/sign"
)

const (
	// DefaultUserAgent is the fixed desktop Chrome User-Agent shared across API signing,
	// HTTP headers, and CDN requests so parameters and UA stay consistent.
	DefaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36"

	// DefaultReferer is the Referer header sent on main-site API and VOD/image CDN requests.
	DefaultReferer = "https://www.douyin.com/"

	// DefaultOrigin is the Origin header for main-site requests.
	DefaultOrigin = "https://www.douyin.com"

	// DefaultLiveReferer is the Referer header sent on webcast API and live CDN requests.
	DefaultLiveReferer = "https://live.douyin.com/"

	// DefaultLiveOrigin is the Origin header sent on webcast live stream requests.
	DefaultLiveOrigin = "https://live.douyin.com"
)

var reChromeVersion = regexp.MustCompile(`(?i)Chrome/(([0-9]+)(?:\.[0-9]+)*)`)

// BrowserProfile holds coherent browser/platform metadata derived from a single User-Agent string.
type BrowserProfile struct {
	UserAgent       string
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

// DeriveBrowserProfile extracts consistent client parameters and Client Hints from ua.
func DeriveBrowserProfile(ua string) BrowserProfile {
	ua = strings.TrimSpace(ua)
	if ua == "" {
		ua = DefaultUserAgent
	}

	fullVer := "136.0.0.0"
	majorVer := "136"
	if m := reChromeVersion.FindStringSubmatch(ua); len(m) == 3 {
		fullVer = m[1]
		majorVer = m[2]
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
	secCHUA := fmt.Sprintf(`"Chromium";v="%s", "Google Chrome";v="%s", "Not.A/Brand";v="99"`, majorVer, majorVer)

	return BrowserProfile{
		UserAgent:       ua,
		BrowserName:     "Chrome",
		BrowserVersion:  fullVer,
		MajorVersion:    majorVer,
		EngineName:      "Blink",
		EngineVersion:   fullVer,
		OSName:          osName,
		OSVersion:       osVer,
		Platform:        "PC",
		BrowserPlatform: browserPlatform,
		ScreenWidth:     "1920",
		ScreenHeight:    "1080",
		BrowserInfo:     browserInfo,
		SecCHUA:         secCHUA,
		SecCHUAMobile:   "?0",
		SecCHUAPlatform: secPlatform,
	}
}

// CommonWebParams builds ordered common query parameters for www.douyin.com/aweme/v1/web/* endpoints,
// appending businessParams and placing msToken immediately before signature attachment.
func (p BrowserProfile) CommonWebParams(webID, msToken string, businessParams ...sign.Pair) []sign.Pair {
	pairs := make([]sign.Pair, 0, 26+len(businessParams))
	pairs = append(pairs,
		sign.Pair{Key: "device_platform", Value: "webapp"},
		sign.Pair{Key: "aid", Value: "6383"},
		sign.Pair{Key: "channel", Value: "channel_pc_web"},
		sign.Pair{Key: "pc_client_type", Value: "1"},
		sign.Pair{Key: "version_code", Value: "290100"},
		sign.Pair{Key: "version_name", Value: "29.1.0"},
		sign.Pair{Key: "cookie_enabled", Value: "true"},
		sign.Pair{Key: "screen_width", Value: p.ScreenWidth},
		sign.Pair{Key: "screen_height", Value: p.ScreenHeight},
		sign.Pair{Key: "browser_language", Value: "zh-CN"},
		sign.Pair{Key: "browser_platform", Value: p.BrowserPlatform},
		sign.Pair{Key: "browser_name", Value: p.BrowserName},
		sign.Pair{Key: "browser_version", Value: p.BrowserVersion},
		sign.Pair{Key: "browser_online", Value: "true"},
		sign.Pair{Key: "engine_name", Value: p.EngineName},
		sign.Pair{Key: "engine_version", Value: p.EngineVersion},
		sign.Pair{Key: "os_name", Value: p.OSName},
		sign.Pair{Key: "os_version", Value: p.OSVersion},
		sign.Pair{Key: "cpu_core_num", Value: "12"},
		sign.Pair{Key: "device_memory", Value: "8"},
		sign.Pair{Key: "platform", Value: p.Platform},
		sign.Pair{Key: "downlink", Value: "10"},
		sign.Pair{Key: "effective_type", Value: "4g"},
		sign.Pair{Key: "round_trip_time", Value: "50"},
	)
	if webID != "" {
		pairs = append(pairs, sign.Pair{Key: "webid", Value: webID})
	}
	pairs = append(pairs, businessParams...)
	if msToken != "" {
		pairs = append(pairs, sign.Pair{Key: "msToken", Value: msToken})
	}
	return pairs
}

// CommonLiveEnterParams builds ordered query parameters for live.douyin.com/webcast/room/web/enter/.
func (p BrowserProfile) CommonLiveEnterParams(webRID, msToken string) []sign.Pair {
	pairs := []sign.Pair{
		{Key: "aid", Value: "6383"},
		{Key: "app_name", Value: "douyin_web"},
		{Key: "live_id", Value: "1"},
		{Key: "device_platform", Value: "web"},
		{Key: "language", Value: "zh-CN"},
		{Key: "enter_from", Value: "web_live"},
		{Key: "cookie_enabled", Value: "true"},
		{Key: "screen_width", Value: p.ScreenWidth},
		{Key: "screen_height", Value: p.ScreenHeight},
		{Key: "browser_language", Value: "zh-CN"},
		{Key: "browser_platform", Value: p.BrowserPlatform},
		{Key: "browser_name", Value: p.BrowserName},
		{Key: "browser_version", Value: p.BrowserVersion},
		{Key: "web_rid", Value: webRID},
		{Key: "room_id_str", Value: ""},
		{Key: "enter_source", Value: ""},
		{Key: "is_need_double_stream", Value: "false"},
		{Key: "insert_task_id", Value: ""},
		{Key: "live_reason", Value: ""},
	}
	if msToken != "" {
		pairs = append(pairs, sign.Pair{Key: "msToken", Value: msToken})
	}
	return pairs
}
