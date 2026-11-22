package x

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/chialecode/trace/internal/site"
)

const (
	maxShortLinkHops = 5

	// LiveUnsupportedText is the exact user-facing notice for X Broadcasts and Spaces (x.md §15.1 #17).
	LiveUnsupportedText = "X 直播与 Spaces 暂不支持录制，可以在浏览器中打开"
)

var (
	reShortCode     = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	reBroadcastID   = regexp.MustCompile(`^[A-Za-z0-9_-]{3,64}$`)
	reArticleID     = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	rePhotoIndex    = regexp.MustCompile(`^[1-4]$`)
	reVideoIndex    = regexp.MustCompile(`^[1-4]$`)
	reMirrorExtTail = regexp.MustCompile(`(?i)\.(?:mp4|jpe?g|png|webp|gif)$`)
)

// allowedXMainHosts lists the canonical X / Twitter web hosts permitted as final resolution targets.
var allowedXMainHosts = map[string]bool{
	"x.com":              true,
	"www.x.com":          true,
	"mobile.x.com":       true,
	"m.x.com":            true,
	"twitter.com":        true,
	"www.twitter.com":    true,
	"mobile.twitter.com": true,
	"m.twitter.com":      true,
}

// mirrorBaseDomains lists third-party embed/viewer mirrors rewritten locally to x.com with 0 requests (x.md §4.1).
var mirrorBaseDomains = map[string]bool{
	"fxtwitter.com": true,
	"fixupx.com":    true,
	"vxtwitter.com": true,
	"fixvx.com":     true,
	"twittpr.com":   true,
	"nitter.net":    true,
	"xcancel.com":   true,
}

// reservedXRootPaths prevents X product/functional routes from ever falling through to UserByScreenName (x.md §4.4).
var reservedXRootPaths = map[string]bool{
	"home":          true,
	"explore":       true,
	"notifications": true,
	"messages":      true,
	"i":             true,
	"settings":      true,
	"search":        true,
	"compose":       true,
	"intent":        true,
	"tos":           true,
	"privacy":       true,
	"login":         true,
	"logout":        true,
	"signup":        true,
	"hashtag":       true,
	"share":         true,
	"download":      true,
	"about":         true,
	"jobs":          true,
	"communities":   true,
	"lists":         true,
	"bookmarks":     true,
	"grok":          true,
	"premium":       true,
	"verified-orgs": true,
	"articles":      true,
	"article":       true,
	"spaces":        true,
	"broadcasts":    true,
	"account":       true,
	"oauth":         true,
	"oauth2":        true,
	"sessions":      true,
	"who_to_follow": true,
	"trends":        true,
	"topics":        true,
	"moments":       true,
	"help":          true,
	"rules":         true,
	"status":        true,
	"statuses":      true,
	"web":           true,
	"user":          true,
	"users":         true,
}

var allowedUserProfileSubTabs = map[string]bool{
	"with_replies": true,
	"media":        true,
	"highlights":   true,
	"likes":        true,
	"articles":     true,
}

var allowedPostTailSegments = map[string]bool{
	"media_tags": true,
	"retweets":   true,
	"quotes":     true,
	"likes":      true,
	"analytics":  true,
	"history":    true,
}

type localRouteKind string

const (
	routeUnknown      localRouteKind = ""
	routeShortLink    localRouteKind = "short_link"
	routePost         localRouteKind = "post"
	routeUser         localRouteKind = "user"
	routeUserHandle   localRouteKind = "user_handle"
	routeLive         localRouteKind = "live"
	routeUnsupported  localRouteKind = "unsupported"
	routeBlocked      localRouteKind = "blocked"
	routeAuthRequired localRouteKind = "auth_required"
)

type localRouteMatch struct {
	kind         localRouteKind
	statusID     string
	restID       string
	screenName   string
	liveID       string
	canonicalURL string
	reason       string
}

// ExtractShareURL extracts an X, mirror, or t.co URL from mixed share text, preferring
// explicit X or mirror URLs over t.co short links when both appear in the same text.
func ExtractShareURL(text string) (string, bool) {
	return site.ExtractShareURL(text)
}

// IsMirrorHost reports whether host is a recognized third-party X embed/viewer mirror
// that TRACE rewrites locally to x.com without making any HTTP request to the mirror.
func IsMirrorHost(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "" {
		return false
	}
	if mirrorBaseDomains[h] {
		return true
	}
	// Allow www., d., i., g., m. subdomains on fxtwitter/fixupx/vxtwitter/fixvx/twittpr
	// and www. on nitter.net / xcancel.com.
	idx := strings.IndexByte(h, '.')
	if idx <= 0 || idx+1 >= len(h) {
		return false
	}
	sub := h[:idx]
	base := h[idx+1:]
	if !mirrorBaseDomains[base] {
		return false
	}
	if base == "nitter.net" || base == "xcancel.com" {
		return sub == "www"
	}
	switch sub {
	case "www", "d", "i", "g", "m":
		return true
	default:
		return false
	}
}

// RewriteMirrorURL rewrites a recognized third-party mirror URL locally to https://x.com/...,
// stripping mirror file-extension tails (.mp4/.jpg), fragments (#m), and tracking query params.
// It returns (rewritten, true) when rewritten, or (u, false) when u is not a mirror URL.
func RewriteMirrorURL(u *url.URL) (*url.URL, bool) {
	if u == nil {
		return nil, false
	}
	host := strings.ToLower(u.Hostname())
	if !IsMirrorHost(host) {
		return u, false
	}
	cloned := *u
	cloned.Scheme = "https"
	cloned.Host = "x.com"
	cloned.Fragment = ""

	// Strip mirror media extension tail (e.g. /user/status/123.mp4 or /user/status/123.jpg)
	// from the final path segment.
	segs := site.SplitCleanPath(cloned.Path)
	if len(segs) > 0 {
		last := segs[len(segs)-1]
		if reMirrorExtTail.MatchString(last) {
			segs[len(segs)-1] = reMirrorExtTail.ReplaceAllString(last, "")
		}
		// fxtwitter / fixupx occasionally append a 2-letter translation code after status/<id>/<lang>.
		if len(segs) == 4 && (strings.EqualFold(segs[1], "status") || strings.EqualFold(segs[1], "statuses")) && len(segs[3]) == 2 {
			if _, err := ParseSnowflakeID(segs[2]); err == nil {
				segs = segs[:3]
			}
		}
		cloned.Path = "/" + strings.Join(segs, "/")
	}

	// Strip tracking query params while preserving user_id / screen_name for /intent/user.
	q := cloned.Query()
	for k := range q {
		lk := strings.ToLower(k)
		if lk == "s" || lk == "t" || lk == "ref_src" || lk == "ref_url" || strings.HasPrefix(lk, "utm_") {
			q.Del(k)
		}
	}
	cloned.RawQuery = q.Encode()
	return &cloned, true
}

// ResolveLink parses and resolves X share text, x.com/twitter.com/mobile.*/m.* links,
// third-party mirrors (0 requests to mirror), t.co short links (cookieless, <=5 hops,
// GuardedMediaDialer, X-domain allowlist), single posts, users (/i/user/, /intent/user,
// /<handle> -> 1 UserByScreenName call), and rejects reserved/unsupported targets locally
// with 0 API requests (x.md §4.1, §4.4, §15.1 #15, #17).
func (c *Client) ResolveLink(ctx context.Context, rawInput string) (site.ResolveTarget, error) {
	trimmed := strings.TrimSpace(rawInput)
	if trimmed == "" {
		return site.ResolveTarget{}, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindBadRequest,
			Message: "请输入有效的 X 链接或分享文本",
		}
	}

	extracted, ok := ExtractShareURL(trimmed)
	if !ok {
		return site.ResolveTarget{}, &site.Error{
			Site:        SiteID,
			Kind:        site.ErrKindUnsupportedLink,
			Unsupported: true,
			Message:     "无法识别该 X 输入，请粘贴完整的 X 链接或分享文本",
		}
	}

	return c.resolveURL(ctx, extracted)
}

func (c *Client) resolveURL(ctx context.Context, rawURL string) (site.ResolveTarget, error) {
	u, err := c.parseAndValidateInputURL(rawURL)
	if err != nil {
		return site.ResolveTarget{}, err
	}

	// Step 2: Local mirror rewrite (0 HTTP requests to mirror).
	if rewritten, isMirror := RewriteMirrorURL(u); isMirror {
		u = rewritten
	}

	match := c.classifyLocalURL(u)

	// Reserved / unsupported / blocked / live routes stop immediately with 0 API requests!
	if target, outErr, handled := c.handleImmediateMatch(match); handled {
		return target, outErr
	}

	// Step 3: t.co short link expansion (INV-07 requires X login before outbound network work,
	// while the t.co HTTP requests themselves NEVER carry Cookie/Authorization/CSRF!).
	if match.kind == routeShortLink {
		if err := c.RequireAuth("resolve"); err != nil {
			return site.ResolveTarget{}, err
		}
		finalURL, finalMatch, hopErr := c.expandShortLink(ctx, u)
		if hopErr != nil {
			return site.ResolveTarget{}, hopErr
		}
		u = finalURL
		match = finalMatch
		if target, outErr, handled := c.handleImmediateMatch(match); handled {
			return target, outErr
		}
	}

	return c.materializeRouteMatch(ctx, match)
}

// ExpandShortLink resolves a t.co short link using the dedicated cookieless redirect client,
// following at most 5 manual hops (10s per hop, GuardedMediaDialer, HTTPS-443, X-domain allowlist).
// Exposed for unit tests and ACT-024 V-03 probes.
func (c *Client) ExpandShortLink(ctx context.Context, rawShortURL string) (finalURL string, hops int, err error) {
	u, err := c.parseAndValidateInputURL(rawShortURL)
	if err != nil {
		return "", 0, err
	}
	if !c.isShortLinkURL(u) {
		return "", 0, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindBadRequest,
			Message: "仅支持展开 t.co 短链",
		}
	}
	landed, _, hopCount, hopErr := c.expandShortLinkWithCount(ctx, u)
	if hopErr != nil {
		return "", hopCount, hopErr
	}
	return landed.String(), hopCount, nil
}

func (c *Client) handleImmediateMatch(match localRouteMatch) (site.ResolveTarget, error, bool) {
	switch match.kind {
	case routeLive:
		target := site.ResolveTarget{
			Site:         SiteID,
			Kind:         "live",
			ID:           match.liveID,
			CanonicalURL: match.canonicalURL,
			Unsupported:  true,
			Reason:       LiveUnsupportedText,
		}
		return target, &site.Error{
			Site:        SiteID,
			Kind:        site.ErrKindUnsupportedLink,
			Unsupported: true,
			Message:     LiveUnsupportedText,
		}, true

	case routeUnsupported:
		reason := match.reason
		if reason == "" {
			reason = "暂不支持这种 X 链接"
		}
		return site.ResolveTarget{}, &site.Error{
			Site:        SiteID,
			Kind:        site.ErrKindUnsupportedLink,
			Unsupported: true,
			Message:     reason,
		}, true

	case routeBlocked:
		reason := match.reason
		if reason == "" {
			reason = "已拦截不安全的 X 链接"
		}
		return site.ResolveTarget{}, &site.Error{
			Site:        SiteID,
			Kind:        site.ErrKindBlocked,
			Unsupported: true,
			Message:     reason,
		}, true

	case routeAuthRequired:
		reason := match.reason
		if reason == "" {
			reason = "X 链接跳转至登录页，请先登录 X"
		}
		return site.ResolveTarget{}, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindAuthRequired,
			Message: reason,
		}, true
	}
	return site.ResolveTarget{}, nil, false
}

func (c *Client) parseAndValidateInputURL(rawURL string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		return nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindBadRequest,
			Message: "无效的 X 链接格式",
		}
	}
	if u.User != nil {
		return nil, &site.Error{
			Site:        SiteID,
			Kind:        site.ErrKindBlocked,
			Unsupported: true,
			Message:     "已拦截含认证信息的链接",
		}
	}

	host := strings.ToLower(u.Hostname())
	if c.allowLocalhost && site.IsLoopbackHost(host) {
		if strings.EqualFold(u.Scheme, "http") || strings.EqualFold(u.Scheme, "https") {
			return u, nil
		}
	}

	// Reject non-443 explicit ports before any scheme upgrade.
	if port := u.Port(); port != "" && port != "443" {
		return nil, &site.Error{
			Site:        SiteID,
			Kind:        site.ErrKindBlocked,
			Unsupported: true,
			Message:     "已拦截非常规端口链接",
		}
	}

	// Only http://t.co/<alnum> is locally upgraded to https://t.co/<alnum> (x.md §4.1).
	if strings.EqualFold(u.Scheme, "http") {
		if host == "t.co" {
			cloned := *u
			cloned.Scheme = "https"
			u = &cloned
		} else {
			return nil, &site.Error{
				Site:        SiteID,
				Kind:        site.ErrKindBlocked,
				Unsupported: true,
				Message:     "仅支持 HTTPS 的 X 链接（仅 http://t.co/ 短链允许自动升级）",
			}
		}
	} else if !strings.EqualFold(u.Scheme, "https") {
		return nil, &site.Error{
			Site:        SiteID,
			Kind:        site.ErrKindBlocked,
			Unsupported: true,
			Message:     "仅支持 HTTPS 的 X 链接",
		}
	}

	if net.ParseIP(host) != nil {
		return nil, &site.Error{
			Site:        SiteID,
			Kind:        site.ErrKindBlocked,
			Unsupported: true,
			Message:     "已拦截直接 IP 地址访问",
		}
	}

	if !isKnownXOrSupportedInputHost(host) {
		return nil, &site.Error{
			Site:        SiteID,
			Kind:        site.ErrKindUnsupportedLink,
			Unsupported: true,
			Message:     fmt.Sprintf("暂不支持非 X 域名: %s", host),
		}
	}

	return u, nil
}

func isKnownXOrSupportedInputHost(host string) bool {
	if allowedXMainHosts[host] || host == "t.co" || IsMirrorHost(host) {
		return true
	}
	return isPeriscopeHost(host)
}

func isPeriscopeHost(host string) bool {
	return host == "periscope.tv" || host == "www.periscope.tv" || host == "pscp.tv" || host == "www.pscp.tv"
}

func (c *Client) isShortLinkURL(u *url.URL) bool {
	if u == nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "t.co" {
		return true
	}
	if c.allowLocalhost && site.IsLoopbackHost(host) {
		segs := site.SplitCleanPath(u.Path)
		return len(segs) >= 2 && strings.EqualFold(segs[0], "tco")
	}
	return false
}

// classifyLocalURL implements the strict matching priority from docs/research/x.md §4.4.
func (c *Client) classifyLocalURL(u *url.URL) localRouteMatch {
	host := strings.ToLower(u.Hostname())
	segs := site.SplitCleanPath(u.Path)
	q := u.Query()
	isLoopback := c.allowLocalhost && site.IsLoopbackHost(host)

	// 1. t.co short link.
	if host == "t.co" {
		if len(segs) != 1 || !reShortCode.MatchString(segs[0]) {
			return localRouteMatch{kind: routeUnsupported, reason: "t.co 短链格式无效"}
		}
		return localRouteMatch{kind: routeShortLink}
	}
	if isLoopback && len(segs) >= 1 && strings.EqualFold(segs[0], "tco") {
		if len(segs) < 2 || !reShortCode.MatchString(segs[1]) {
			return localRouteMatch{kind: routeUnsupported, reason: "t.co 短链格式无效"}
		}
		return localRouteMatch{kind: routeShortLink}
	}

	// 2. Periscope / pscp.tv legacy broadcast links -> unsupported live (0 API requests).
	if isPeriscopeHost(host) {
		liveID := ""
		if len(segs) >= 2 && strings.EqualFold(segs[0], "w") && reBroadcastID.MatchString(segs[1]) {
			liveID = segs[1]
		} else if len(segs) >= 1 && reBroadcastID.MatchString(segs[len(segs)-1]) {
			liveID = segs[len(segs)-1]
		}
		return localRouteMatch{
			kind:         routeLive,
			liveID:       liveID,
			canonicalURL: u.String(),
			reason:       LiveUnsupportedText,
		}
	}

	// Remaining routes apply to allowed X main hosts (or loopback in tests).
	if !allowedXMainHosts[host] && !isLoopback {
		return localRouteMatch{
			kind:   routeUnsupported,
			reason: fmt.Sprintf("暂不支持非 X 域名: %s", host),
		}
	}

	if len(segs) == 0 {
		return localRouteMatch{kind: routeUnsupported, reason: "暂不支持 X 首页链接"}
	}

	s0 := strings.ToLower(segs[0])

	// 3. Explicit /i/... routes (MUST run before reserved blacklist so /i/web/status, /i/status, /i/user work!).
	if s0 == "i" {
		return matchIRoute(segs, u.String())
	}

	// 4. Explicit /intent/user routes (MUST run before reserved blacklist!).
	if s0 == "intent" {
		return matchIntentRoute(segs, q)
	}

	// 5. Reserved paths & unsupported functional routes (BEFORE <handle> matching! 0 API requests!).
	if rejected, ok := matchReservedOrUnsupportedXPath(segs); ok {
		return rejected
	}

	// 6. Single post: /<handle>/status/<id>[/photo/N|/video/N|/quotes|...]
	if len(segs) >= 3 && (strings.EqualFold(segs[1], "status") || strings.EqualFold(segs[1], "statuses")) {
		handle := segs[0]
		if !reValidXScreenName.MatchString(handle) {
			return localRouteMatch{kind: routeUnsupported, reason: "X 帖子链接中的用户名格式无效"}
		}
		return matchStatusSegments(handle, segs[2:])
	}

	// 7. Unsupported per-user sub-routes: /<handle>/article/<id>, /<handle>/articles/<id>, /<handle>/lists[/*]
	if len(segs) >= 2 && reValidXScreenName.MatchString(segs[0]) {
		sub := strings.ToLower(segs[1])
		if sub == "article" || (sub == "articles" && len(segs) >= 3) {
			return localRouteMatch{
				kind:   routeUnsupported,
				reason: "X Articles 独立文章页暂不支持解析，可以在浏览器中打开",
			}
		}
		if sub == "lists" {
			return localRouteMatch{
				kind:   routeUnsupported,
				reason: "X 列表页暂不支持解析，可以在浏览器中打开",
			}
		}
	}

	// 8. User handle profile (lowest priority, strictly non-reserved handle):
	// /<handle> or /<handle>/(with_replies|media|highlights|likes|articles)
	if reValidXScreenName.MatchString(segs[0]) && !reservedXRootPaths[s0] {
		if len(segs) == 1 || (len(segs) == 2 && allowedUserProfileSubTabs[strings.ToLower(segs[1])]) {
			return localRouteMatch{
				kind:       routeUserHandle,
				screenName: segs[0],
			}
		}
	}

	// 9. Fallback reject (0 API requests).
	return localRouteMatch{
		kind:   routeUnsupported,
		reason: "暂不支持这种 X 链接",
	}
}

func matchIRoute(segs []string, rawURL string) localRouteMatch {
	if len(segs) < 2 {
		return localRouteMatch{kind: routeUnsupported, reason: "暂不支持该 X 功能页链接"}
	}
	sub := strings.ToLower(segs[1])
	switch sub {
	case "status", "statuses":
		return matchStatusSegments("", segs[2:])

	case "web":
		if len(segs) >= 4 && (strings.EqualFold(segs[2], "status") || strings.EqualFold(segs[2], "statuses")) {
			return matchStatusSegments("", segs[3:])
		}
		return localRouteMatch{kind: routeUnsupported, reason: "暂不支持该 X 链接"}

	case "user":
		if len(segs) >= 3 {
			if normUID, err := site.NormalizeUID(SiteID, segs[2]); err == nil {
				return localRouteMatch{
					kind:         routeUser,
					restID:       normUID,
					canonicalURL: "https://x.com/i/user/" + normUID,
				}
			}
		}
		return localRouteMatch{kind: routeUnsupported, reason: "X 用户链接中的 rest_id 无效"}

	case "broadcasts":
		liveID := ""
		if len(segs) >= 3 && reBroadcastID.MatchString(segs[2]) {
			liveID = segs[2]
		}
		canon := rawURL
		if liveID != "" {
			canon = "https://x.com/i/broadcasts/" + liveID
		}
		return localRouteMatch{
			kind:         routeLive,
			liveID:       liveID,
			canonicalURL: canon,
			reason:       LiveUnsupportedText,
		}

	case "spaces":
		spaceID := ""
		if len(segs) >= 3 && reBroadcastID.MatchString(segs[2]) {
			spaceID = segs[2]
		}
		canon := rawURL
		if spaceID != "" {
			canon = "https://x.com/i/spaces/" + spaceID
		}
		return localRouteMatch{
			kind:         routeLive,
			liveID:       spaceID,
			canonicalURL: canon,
			reason:       LiveUnsupportedText,
		}

	case "article", "articles":
		return localRouteMatch{
			kind:   routeUnsupported,
			reason: "X Articles 独立文章页暂不支持解析，可以在浏览器中打开",
		}

	case "lists":
		return localRouteMatch{
			kind:   routeUnsupported,
			reason: "X 列表页暂不支持解析，可以在浏览器中打开",
		}

	case "communities":
		return localRouteMatch{
			kind:   routeUnsupported,
			reason: "X 社区页暂不支持解析，可以在浏览器中打开",
		}

	case "bookmarks":
		return localRouteMatch{
			kind:   routeUnsupported,
			reason: "暂不支持直接解析书签页链接，请在“我的 › X › 书签”中查看",
		}

	default:
		return localRouteMatch{
			kind:   routeUnsupported,
			reason: "暂不支持该 X 功能页链接",
		}
	}
}

func matchIntentRoute(segs []string, q url.Values) localRouteMatch {
	if len(segs) >= 2 && strings.EqualFold(segs[1], "user") {
		if rawUID := strings.TrimSpace(q.Get("user_id")); rawUID != "" {
			if normUID, err := site.NormalizeUID(SiteID, rawUID); err == nil {
				return localRouteMatch{
					kind:         routeUser,
					restID:       normUID,
					canonicalURL: "https://x.com/i/user/" + normUID,
				}
			}
			return localRouteMatch{kind: routeUnsupported, reason: "X intent 用户链接中的 user_id 无效"}
		}
		if rawHandle := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(q.Get("screen_name")), "@")); rawHandle != "" {
			if reValidXScreenName.MatchString(rawHandle) && !reservedXRootPaths[strings.ToLower(rawHandle)] {
				return localRouteMatch{
					kind:       routeUserHandle,
					screenName: rawHandle,
				}
			}
			return localRouteMatch{kind: routeUnsupported, reason: "X intent 用户链接中的 screen_name 无效"}
		}
		return localRouteMatch{kind: routeUnsupported, reason: "X intent 用户链接缺少 user_id 或 screen_name"}
	}
	return localRouteMatch{kind: routeUnsupported, reason: "暂不支持该 X intent 链接"}
}

func matchReservedOrUnsupportedXPath(segs []string) (localRouteMatch, bool) {
	s0 := strings.ToLower(segs[0])
	switch s0 {
	case "search", "hashtag":
		return localRouteMatch{kind: routeUnsupported, reason: "X 搜索与话题页暂不支持解析"}, true
	case "bookmarks":
		return localRouteMatch{kind: routeUnsupported, reason: "暂不支持直接解析书签页链接，请在“我的 › X › 书签”中查看"}, true
	case "lists":
		return localRouteMatch{kind: routeUnsupported, reason: "X 列表页暂不支持解析，可以在浏览器中打开"}, true
	case "communities":
		return localRouteMatch{kind: routeUnsupported, reason: "X 社区页暂不支持解析，可以在浏览器中打开"}, true
	case "articles", "article":
		return localRouteMatch{kind: routeUnsupported, reason: "X Articles 独立文章页暂不支持解析，可以在浏览器中打开"}, true
	case "spaces", "broadcasts":
		return localRouteMatch{
			kind:         routeLive,
			canonicalURL: "https://x.com/" + strings.Join(segs, "/"),
			reason:       LiveUnsupportedText,
		}, true
	}
	if reservedXRootPaths[s0] {
		return localRouteMatch{
			kind:   routeUnsupported,
			reason: fmt.Sprintf("暂不支持 X 功能页链接（/%s）", s0),
		}, true
	}
	return localRouteMatch{}, false
}

func matchStatusSegments(handle string, rest []string) localRouteMatch {
	if len(rest) == 0 {
		return localRouteMatch{kind: routeUnsupported, reason: "X 帖子链接缺少状态 ID"}
	}
	sf, err := ParseSnowflakeID(rest[0])
	if err != nil {
		return localRouteMatch{kind: routeUnsupported, reason: "X 帖子链接中的状态 ID 无效"}
	}
	if len(rest) > 1 {
		tail := strings.ToLower(rest[1])
		switch tail {
		case "photo":
			if len(rest) >= 3 && !rePhotoIndex.MatchString(rest[2]) {
				return localRouteMatch{kind: routeUnsupported, reason: "X 帖子图片序号无效"}
			}
		case "video":
			if len(rest) >= 3 && !reVideoIndex.MatchString(rest[2]) {
				return localRouteMatch{kind: routeUnsupported, reason: "X 帖子视频序号无效"}
			}
		default:
			if !allowedPostTailSegments[tail] {
				return localRouteMatch{kind: routeUnsupported, reason: "暂不支持该 X 帖子子路径"}
			}
		}
	}
	canon := canonicalStatusURL(handle, sf.ID)
	return localRouteMatch{
		kind:         routePost,
		statusID:     sf.ID,
		screenName:   handle,
		canonicalURL: canon,
	}
}

func canonicalStatusURL(handle, statusID string) string {
	h := strings.TrimSpace(handle)
	if h != "" && !strings.EqualFold(h, "i") && reValidXScreenName.MatchString(h) {
		return "https://x.com/" + h + "/status/" + statusID
	}
	return "https://x.com/i/status/" + statusID
}

func (c *Client) expandShortLink(ctx context.Context, startURL *url.URL) (*url.URL, localRouteMatch, error) {
	u, m, _, err := c.expandShortLinkWithCount(ctx, startURL)
	return u, m, err
}

func (c *Client) expandShortLinkWithCount(ctx context.Context, startURL *url.URL) (*url.URL, localRouteMatch, int, error) {
	cur := startURL
	hops := 0

	for hop := 0; hop < maxShortLinkHops; hop++ {
		if err := c.checkAutomaticBudget(ctx, sanitizeEndpointLabel(cur)); err != nil {
			return nil, localRouteMatch{}, hops, err
		}
		release, err := c.acquireSlotAndPace(ctx)
		if err != nil {
			return nil, localRouteMatch{}, hops, err
		}

		req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, cur.String(), nil)
		if reqErr != nil {
			release()
			return nil, localRouteMatch{}, hops, &site.Error{
				Site:    SiteID,
				Kind:    site.ErrKindBadRequest,
				Message: "构造 t.co 短链请求失败",
				Err:     site.UnwrapURLError(reqErr),
			}
		}
		req.Header.Set("User-Agent", c.profile.UserAgent)
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		req.Header.Set("Accept-Language", "en-US,en;q=0.9")
		// Strictly NEVER send Cookie, Authorization, x-csrf-token, or x-twitter-auth-type on t.co hops!
		req.Header.Del("Cookie")
		req.Header.Del("Authorization")
		req.Header.Del("X-Csrf-Token")
		req.Header.Del("X-Twitter-Auth-Type")
		req.Header.Del("X-Client-Transaction-Id")

		resp, doErr := c.redirectClient.Do(req)
		hops++
		if doErr != nil {
			release()
			if site.IsKind(doErr, site.ErrKindBlocked) {
				return nil, localRouteMatch{}, hops, doErr
			}
			netErr := &site.Error{
				Site:     SiteID,
				Kind:     site.ErrKindNetwork,
				Endpoint: sanitizeEndpointLabel(cur),
				Message:  "展开 t.co 短链失败",
				Err:      site.UnwrapURLError(doErr),
			}
			c.ReportAutomaticError(ctx, netErr)
			return nil, localRouteMatch{}, hops, netErr
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		release()

		if resp.StatusCode < 300 || resp.StatusCode >= 400 {
			var kind, msg string
			switch resp.StatusCode {
			case http.StatusTooManyRequests:
				kind = site.ErrKindRateLimited
				msg = "X 短链请求过于频繁（HTTP 429），已停止请求"
			case http.StatusForbidden:
				kind = site.ErrKindRiskControl
				msg = "X 短链触发风控拦截（HTTP 403），已停止请求"
			case http.StatusUnauthorized:
				kind = site.ErrKindAuthRequired
				msg = "X 短链请求需要登录（HTTP 401）"
			default:
				kind = site.ErrKindUnavailable
				msg = fmt.Sprintf("t.co 短链未返回有效跳转（HTTP %d）", resp.StatusCode)
			}
			se := &site.Error{
				Site:       SiteID,
				Kind:       kind,
				HTTPStatus: resp.StatusCode,
				Endpoint:   sanitizeEndpointLabel(cur),
				Message:    msg,
			}
			c.ReportAutomaticError(ctx, se)
			return nil, localRouteMatch{}, hops, se
		}

		rawLoc := strings.TrimSpace(resp.Header.Get("Location"))
		if rawLoc == "" {
			return nil, localRouteMatch{}, hops, &site.Error{
				Site:       SiteID,
				Kind:       site.ErrKindUnavailable,
				HTTPStatus: resp.StatusCode,
				Endpoint:   sanitizeEndpointLabel(cur),
				Message:    "t.co 短链响应缺少跳转地址",
			}
		}

		nextURL, pErr := cur.Parse(rawLoc)
		if pErr != nil || nextURL.Host == "" {
			return nil, localRouteMatch{}, hops, &site.Error{
				Site:     SiteID,
				Kind:     site.ErrKindBlocked,
				Endpoint: sanitizeEndpointLabel(cur),
				Message:  "t.co 短链重定向地址格式无效",
			}
		}

		validatedURL, vErr := c.validateShortLinkHopTarget(nextURL)
		if vErr != nil {
			return nil, localRouteMatch{}, hops, vErr
		}

		// Local mirror rewrite if a t.co hop points at a known mirror (still 0 requests to mirror).
		if rewritten, isMirror := RewriteMirrorURL(validatedURL); isMirror {
			validatedURL = rewritten
		}

		match := c.classifyLocalURL(validatedURL)
		// Early convergence: stop immediately once an X target (or explicit unsupported/live route) is reached!
		if match.kind != routeShortLink && match.kind != routeUnknown {
			return validatedURL, match, hops, nil
		}

		if canonicalHopKey(validatedURL) == canonicalHopKey(cur) {
			return nil, localRouteMatch{}, hops, &site.Error{
				Site:     SiteID,
				Kind:     site.ErrKindBlocked,
				Endpoint: sanitizeEndpointLabel(cur),
				Message:  "t.co 短链重定向陷入原地循环",
			}
		}
		cur = validatedURL
	}

	return nil, localRouteMatch{}, hops, &site.Error{
		Site:    SiteID,
		Kind:    site.ErrKindBlocked,
		Message: "t.co 短链跳转超过 5 跳安全上限",
	}
}

func (c *Client) validateShortLinkHopTarget(u *url.URL) (*url.URL, error) {
	if u == nil || u.Host == "" || u.User != nil {
		return nil, &site.Error{
			Site:        SiteID,
			Kind:        site.ErrKindBlocked,
			Unsupported: true,
			Message:     "已拦截不安全的短链跳转目标",
		}
	}

	host := strings.ToLower(u.Hostname())
	isLoopback := c.allowLocalhost && site.IsLoopbackHost(host)

	if port := u.Port(); !isLoopback && port != "" && port != "443" {
		return nil, &site.Error{
			Site:        SiteID,
			Kind:        site.ErrKindBlocked,
			Unsupported: true,
			Message:     "已拦截短链跳转到非常规端口",
		}
	}

	if isLoopback {
		if strings.EqualFold(u.Scheme, "http") || strings.EqualFold(u.Scheme, "https") {
			return u, nil
		}
		return nil, &site.Error{
			Site:        SiteID,
			Kind:        site.ErrKindBlocked,
			Unsupported: true,
			Message:     "已拦截非 HTTP(S) 短链跳转地址",
		}
	}

	// Only http://t.co/<alnum> may upgrade to https://t.co/<alnum>; all other hops MUST be HTTPS.
	if strings.EqualFold(u.Scheme, "http") {
		if host == "t.co" {
			cloned := *u
			cloned.Scheme = "https"
			u = &cloned
		} else {
			return nil, &site.Error{
				Site:        SiteID,
				Kind:        site.ErrKindBlocked,
				Unsupported: true,
				Message:     "已拦截短链跳转到非 HTTPS 地址",
			}
		}
	} else if !strings.EqualFold(u.Scheme, "https") {
		return nil, &site.Error{
			Site:        SiteID,
			Kind:        site.ErrKindBlocked,
			Unsupported: true,
			Message:     "已拦截短链跳转到非 HTTPS 地址",
		}
	}

	if net.ParseIP(host) != nil {
		return nil, &site.Error{
			Site:        SiteID,
			Kind:        site.ErrKindBlocked,
			Unsupported: true,
			Message:     "已拦截短链跳转到直接 IP 地址",
		}
	}

	// Stop immediately on any non-X external host (0 requests to external site!).
	if host == "t.co" || allowedXMainHosts[host] || isPeriscopeHost(host) {
		return u, nil
	}

	return nil, &site.Error{
		Site:        SiteID,
		Kind:        site.ErrKindBlocked,
		Unsupported: true,
		Message:     fmt.Sprintf("该 t.co 短链指向 X 以外的网站（%s），不支持解析", host),
	}
}

func canonicalHopKey(u *url.URL) string {
	if u == nil {
		return ""
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host) + strings.TrimRight(u.Path, "/") + "?" + u.RawQuery
}

func (c *Client) materializeRouteMatch(ctx context.Context, match localRouteMatch) (site.ResolveTarget, error) {
	if err := c.RequireAuth("resolve"); err != nil {
		return site.ResolveTarget{}, err
	}

	switch match.kind {
	case routePost:
		return site.ResolveTarget{
			Site:         SiteID,
			Kind:         "dynamic",
			ID:           match.statusID,
			CanonicalURL: match.canonicalURL,
		}, nil

	case routeUser:
		return site.ResolveTarget{
			Site:         SiteID,
			Kind:         "user",
			ID:           match.restID,
			AuthorUID:    match.restID,
			CanonicalURL: match.canonicalURL,
		}, nil

	case routeUserHandle:
		prof, err := c.GetUserByScreenName(ctx, match.screenName)
		if err != nil {
			return site.ResolveTarget{}, err
		}
		return site.ResolveTarget{
			Site:         SiteID,
			Kind:         "user",
			ID:           prof.RestID,
			AuthorUID:    prof.RestID,
			CanonicalURL: "https://x.com/i/user/" + prof.RestID,
		}, nil

	default:
		return site.ResolveTarget{}, &site.Error{
			Site:        SiteID,
			Kind:        site.ErrKindUnsupportedLink,
			Unsupported: true,
			Message:     "暂不支持这种 X 链接",
		}
	}
}
