// Package douyin implements the Douyin PC Web and Webcast protocol client and site adapter.
package douyin

import (
	"net/http"

	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/douyin/sign"
)

const (
	// DefaultUserAgent is the unified desktop Chrome User-Agent shared across API signing,
	// HTTP headers, and CDN requests so parameters and UA stay consistent.
	DefaultUserAgent = site.DefaultUserAgent

	// DefaultReferer is the Referer header sent on main-site API and VOD/image CDN requests.
	DefaultReferer = "https://www.douyin.com/"

	// DefaultOrigin is the Origin header for main-site requests.
	DefaultOrigin = "https://www.douyin.com"

	// DefaultLiveReferer is the Referer header sent on webcast API and live CDN requests.
	DefaultLiveReferer = "https://live.douyin.com/"

	// DefaultLiveOrigin is the Origin header sent on webcast live stream requests.
	DefaultLiveOrigin = "https://live.douyin.com"
)

// BrowserProfile holds coherent browser/platform metadata derived from a single User-Agent string.
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

// DeriveBrowserProfile extracts consistent client parameters and Client Hints from ua
// via the shared internal/site profile helper (leaving Sec-Ch-Ua* empty for non-Chromium UAs).
func DeriveBrowserProfile(ua string) BrowserProfile {
	bp := site.DeriveBrowserProfile(ua)
	return BrowserProfile{
		UserAgent:       bp.UserAgent,
		IsChromium:      bp.IsChromium,
		BrowserName:     bp.BrowserName,
		BrowserVersion:  bp.BrowserVersion,
		MajorVersion:    bp.MajorVersion,
		EngineName:      bp.EngineName,
		EngineVersion:   bp.EngineVersion,
		OSName:          bp.OSName,
		OSVersion:       bp.OSVersion,
		Platform:        bp.Platform,
		BrowserPlatform: bp.BrowserPlatform,
		ScreenWidth:     bp.ScreenWidth,
		ScreenHeight:    bp.ScreenHeight,
		BrowserInfo:     bp.BrowserInfo,
		SecCHUA:         bp.SecCHUA,
		SecCHUAMobile:   bp.SecCHUAMobile,
		SecCHUAPlatform: bp.SecCHUAPlatform,
	}
}

// ApplyClientHints applies Chromium Sec-Ch-Ua* headers on h and strips them for non-Chromium UAs.
func (p BrowserProfile) ApplyClientHints(h http.Header) {
	site.BrowserProfile{
		UserAgent:       p.UserAgent,
		IsChromium:      p.IsChromium,
		SecCHUA:         p.SecCHUA,
		SecCHUAMobile:   p.SecCHUAMobile,
		SecCHUAPlatform: p.SecCHUAPlatform,
	}.ApplyClientHints(h)
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
