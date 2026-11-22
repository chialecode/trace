package weibo

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
	maxShortLinkHops  = 5
	maxSinaURLDepth   = 3
	liveUnsupportText = "微博直播暂不支持录制，可以在浏览器中打开"
)

var (
	reUIDDigits       = regexp.MustCompile(`^[1-9][0-9]{4,18}$`)
	reMidDigits       = regexp.MustCompile(`^[1-9][0-9]{14,19}$`)
	reMblogIDToken    = regexp.MustCompile(`^[0-9a-zA-Z]{7,12}$`)
	reVideoOID        = regexp.MustCompile(`^1034:[A-Za-z0-9_-]{6,64}$`)
	reCustomDomain    = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,64}$`)
	reAppShareMidFile = regexp.MustCompile(`,([1-9][0-9]{14,19})\.html$`)
	reContainerUser   = regexp.MustCompile(`^(?:100505|100606)([1-9][0-9]{4,18})$`)
	rePureDigits      = regexp.MustCompile(`^[0-9]+$`)
)

// allowedWeiboRedirectHosts matches docs/research/weibo.md §3.1 #3 strictly.
var allowedWeiboRedirectHosts = map[string]bool{
	"t.cn":                    true,
	"weibo.com":               true,
	"www.weibo.com":           true,
	"m.weibo.cn":              true,
	"weibo.cn":                true,
	"video.weibo.com":         true,
	"share.api.weibo.cn":      true,
	"weibointl.api.weibo.com": true,
	"live.weibo.com":          true,
}

var loginWallHosts = map[string]bool{
	"passport.weibo.com":        true,
	"passport.weibo.cn":         true,
	"visitor.passport.weibo.cn": true,
	"login.sina.com.cn":         true,
}

// reservedWeiboRootPaths strictly prevents reserved Weibo routes from falling through to custom-domain lookup (§3.4 #9).
var reservedWeiboRootPaths = map[string]bool{
	"home":      true,
	"hot":       true,
	"fav":       true,
	"favorites": true,
	"like":      true,
	"likes":     true,
	"mygroups":  true,
	"groups":    true,
	"search":    true,
	"tv":        true,
	"l":         true,
	"p":         true,
	"u":         true,
	"n":         true,
	"a":         true,
	"z":         true,
	"k":         true,
	"s":         true,
	"m":         true,
	"w":         true,
	"c":         true,
	"d":         true,
	"t":         true,
	"detail":    true,
	"status":    true,
	"statuses":  true,
	"ajax":      true,
	"newlogin":  true,
	"login":     true,
	"login.php": true,
	"signup":    true,
	"logout":    true,
	"ttarticle": true,
	"article":   true,
	"at":        true,
	"comment":   true,
	"dm":        true,
	"message":   true,
	"settings":  true,
	"set":       true,
	"verify":    true,
	"about":     true,
	"help":      true,
	"rank":      true,
	"discover":  true,
	"friends":   true,
	"follow":    true,
	"fans":      true,
	"album":     true,
	"video":     true,
	"live":      true,
	"sinaurl":   true,
	"profile":   true,
	"share":     true,
	"feed":      true,
	"index":     true,
	"myfollow":  true,
	"myfans":    true,
	"explore":   true,
	"topic":     true,
	"super":     true,
	"huati":     true,
	"card":      true,
	"mall":      true,
	"vote":      true,
	"pay":       true,
	"vip":       true,
	"member":    true,
	"security":  true,
	"privacy":   true,
	"terms":     true,
	"app":       true,
	"download":  true,
	"client":    true,
	"2":         true,
}

var reservedUserSubPaths = map[string]bool{
	"home":      true,
	"profile":   true,
	"info":      true,
	"fans":      true,
	"follow":    true,
	"follows":   true,
	"album":     true,
	"video":     true,
	"live":      true,
	"about":     true,
	"manage":    true,
	"myfollow":  true,
	"myfans":    true,
	"pic":       true,
	"article":   true,
	"feed":      true,
	"photos":    true,
	"favorites": true,
	"likes":     true,
}

type localRouteKind string

const (
	routeUnknown      localRouteKind = ""
	routeShortLink    localRouteKind = "short_link"
	routeSinaURL      localRouteKind = "sinaurl"
	routePost         localRouteKind = "post"
	routeVideo        localRouteKind = "video"
	routeLive         localRouteKind = "live"
	routeUser         localRouteKind = "user"
	routeUserNick     localRouteKind = "user_nick"
	routeUserCustom   localRouteKind = "user_custom"
	routeUnsupported  localRouteKind = "unsupported"
	routeBlocked      localRouteKind = "blocked"
	routeAuthRequired localRouteKind = "auth_required"
)

type localRouteMatch struct {
	kind         localRouteKind
	mid          string
	mblogID      string
	authorUID    string
	videoOID     string
	liveID       string
	uid          string
	screenName   string
	customDomain string
	canonicalURL string
	reason       string
}

// ExtractShareURL extracts a Weibo URL from pasted share text, delegating to site.ExtractShareURL
// which prefers explicit in-site Weibo URLs over t.cn short links when both appear in the same text.
func ExtractShareURL(text string) (string, bool) {
	return site.ExtractShareURL(text)
}

// ResolveLink parses and resolves Weibo share text, t.cn short links, sinaurl wrappers, single posts,
// independent 1034: video pages, user profiles (/u/, /profile/, /p/100505, /p/100606, /n/, custom domain),
// live links, and unsupported links strictly following docs/research/weibo.md §3.1 and §3.4.
func (c *Client) ResolveLink(ctx context.Context, rawInput string) (site.ResolveTarget, error) {
	trimmed := strings.TrimSpace(rawInput)
	if trimmed == "" {
		return site.ResolveTarget{}, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindBadRequest,
			Message: "请输入有效的微博链接或分享文本",
		}
	}

	extracted, ok := ExtractShareURL(trimmed)
	if !ok {
		return site.ResolveTarget{}, &site.Error{
			Site:        site.Weibo,
			Kind:        site.ErrKindUnsupportedLink,
			Unsupported: true,
			Message:     "无法识别该微博输入，请粘贴完整的微博链接或分享文本",
		}
	}

	return c.resolveURL(ctx, extracted, 0)
}

func (c *Client) resolveURL(ctx context.Context, rawURL string, sinaDepth int) (site.ResolveTarget, error) {
	u, err := c.parseAndValidateInputURL(rawURL)
	if err != nil {
		return site.ResolveTarget{}, err
	}

	match := c.classifyLocalURL(u)

	// Unpack weibo.cn/sinaurl locally with 0 network requests (weibo.md §3.1 #4, §3.4 #2).
	for match.kind == routeSinaURL {
		if sinaDepth >= maxSinaURLDepth {
			return site.ResolveTarget{}, &site.Error{
				Site:        site.Weibo,
				Kind:        site.ErrKindBlocked,
				Unsupported: true,
				Message:     "短链中转嵌套层数过多，已拒绝解析",
			}
		}
		sinaDepth++
		unpacked, uErr := c.unpackSinaURL(u)
		if uErr != nil {
			return site.ResolveTarget{}, uErr
		}
		u = unpacked
		match = c.classifyLocalURL(u)
	}

	// Unsupported / blocked / live routes stop immediately with 0 API requests!
	if target, outErr, handled := c.handleImmediateMatch(match); handled {
		return target, outErr
	}

	// Short link t.cn expansion: requires Weibo login before outbound network work (INV-07),
	// but the t.cn HTTP hops themselves NEVER carry Cookie (weibo.md §3.1 #2).
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

	return c.materializeRouteMatch(ctx, u, match)
}

// ExpandShortLink resolves a t.cn short link without sending any Cookie header, following at most 5
// manual hops inside Weibo's HTTPS allowlist with early convergence. Exposed for ACT-022 V-02 probes.
func (c *Client) ExpandShortLink(ctx context.Context, rawShortURL string) (finalURL string, hops int, err error) {
	u, err := c.parseAndValidateInputURL(rawShortURL)
	if err != nil {
		return "", 0, err
	}
	if !c.isShortLinkURL(u) {
		return "", 0, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindBadRequest,
			Message: "仅支持展开 t.cn 短链",
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
			Site:         site.Weibo,
			Kind:         "live",
			ID:           match.liveID,
			CanonicalURL: match.canonicalURL,
			Unsupported:  true,
			Reason:       liveUnsupportText,
		}
		return target, &site.Error{
			Site:        site.Weibo,
			Kind:        site.ErrKindUnsupportedLink,
			Unsupported: true,
			Message:     liveUnsupportText,
		}, true

	case routeUnsupported:
		reason := match.reason
		if reason == "" {
			reason = "暂不支持这种微博链接"
		}
		return site.ResolveTarget{}, &site.Error{
			Site:        site.Weibo,
			Kind:        site.ErrKindUnsupportedLink,
			Unsupported: true,
			Message:     reason,
		}, true

	case routeBlocked:
		reason := match.reason
		if reason == "" {
			reason = "已拦截不安全的微博链接"
		}
		return site.ResolveTarget{}, &site.Error{
			Site:        site.Weibo,
			Kind:        site.ErrKindBlocked,
			Unsupported: true,
			Message:     reason,
		}, true

	case routeAuthRequired:
		reason := match.reason
		if reason == "" {
			reason = "微博链接跳转至登录或访客页，请先登录微博"
		}
		return site.ResolveTarget{}, &site.Error{
			Site:    site.Weibo,
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
			Site:    site.Weibo,
			Kind:    site.ErrKindBadRequest,
			Message: "无效的微博链接格式",
		}
	}
	if u.User != nil {
		return nil, &site.Error{
			Site:        site.Weibo,
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
			Site:        site.Weibo,
			Kind:        site.ErrKindBlocked,
			Unsupported: true,
			Message:     "已拦截非常规端口链接",
		}
	}

	// Only http://t.cn/... is locally upgraded to https://t.cn/... (weibo.md §3.1 #1).
	if strings.EqualFold(u.Scheme, "http") {
		if host == "t.cn" {
			cloned := *u
			cloned.Scheme = "https"
			u = &cloned
		} else {
			return nil, &site.Error{
				Site:        site.Weibo,
				Kind:        site.ErrKindBlocked,
				Unsupported: true,
				Message:     "仅支持 HTTPS 微博链接（仅 http://t.cn/ 短链允许自动升级）",
			}
		}
	} else if !strings.EqualFold(u.Scheme, "https") {
		return nil, &site.Error{
			Site:        site.Weibo,
			Kind:        site.ErrKindBlocked,
			Unsupported: true,
			Message:     "仅支持 HTTPS 微博链接",
		}
	}

	if net.ParseIP(host) != nil {
		return nil, &site.Error{
			Site:        site.Weibo,
			Kind:        site.ErrKindBlocked,
			Unsupported: true,
			Message:     "已拦截直接 IP 地址访问",
		}
	}

	if !isKnownWeiboOrRejectHost(host) {
		return nil, &site.Error{
			Site:        site.Weibo,
			Kind:        site.ErrKindUnsupportedLink,
			Unsupported: true,
			Message:     fmt.Sprintf("暂不支持非微博域名: %s", host),
		}
	}
	return u, nil
}

func isKnownWeiboOrRejectHost(host string) bool {
	if allowedWeiboRedirectHosts[host] || loginWallHosts[host] {
		return true
	}
	switch host {
	case "s.weibo.com", "card.weibo.com", "vote.weibo.com", "mall.weibo.com", "e.weibo.com", "zhibo.sina.com.cn":
		return true
	}
	return site.MatchDomainSuffix(host, []string{"weibo.com", "weibo.cn"})
}

func (c *Client) isShortLinkURL(u *url.URL) bool {
	if u == nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "t.cn" {
		return true
	}
	if c.allowLocalhost && site.IsLoopbackHost(host) {
		segs := site.SplitCleanPath(u.Path)
		return len(segs) >= 2 && strings.EqualFold(segs[0], "tcn")
	}
	return false
}

// classifyLocalURL implements the strict 10-step matching priority from docs/research/weibo.md §3.4.
func (c *Client) classifyLocalURL(u *url.URL) localRouteMatch {
	host := strings.ToLower(u.Hostname())
	segs := site.SplitCleanPath(u.Path)
	q := u.Query()
	isLoopback := c.allowLocalhost && site.IsLoopbackHost(host)

	// Step 1: t.cn short link.
	if host == "t.cn" {
		if len(segs) == 0 {
			return localRouteMatch{kind: routeUnsupported, reason: "t.cn 短链缺少路径代码"}
		}
		return localRouteMatch{kind: routeShortLink}
	}
	if isLoopback && len(segs) >= 1 && strings.EqualFold(segs[0], "tcn") {
		if len(segs) < 2 {
			return localRouteMatch{kind: routeUnsupported, reason: "t.cn 短链缺少路径代码"}
		}
		return localRouteMatch{kind: routeShortLink}
	}

	// Step 2: weibo.cn/sinaurl local unpack (0 network requests).
	if (host == "weibo.cn" || host == "m.weibo.cn" || host == "weibo.com" || host == "www.weibo.com" || isLoopback) &&
		len(segs) >= 1 && strings.EqualFold(segs[0], "sinaurl") {
		return localRouteMatch{kind: routeSinaURL}
	}

	// Step 3: Unsupported subdomains & paths前置拒绝 (BEFORE custom domain!).
	if rejected, ok := matchUnsupportedSubdomainOrPath(host, segs, q); ok {
		return rejected
	}

	// Step 4: App share pages (share.api.weibo.cn, weibointl.api.weibo.com).
	if host == "share.api.weibo.cn" || host == "weibointl.api.weibo.com" ||
		(isLoopback && len(segs) >= 2 && strings.EqualFold(segs[0], "share")) {
		return matchAppSharePage(segs, q)
	}

	// Step 5: Independent video pages (1034:...) — never misroute media_id as post mid!
	if vidMatch, ok := matchVideoPage(host, segs, q, isLoopback); ok {
		return vidMatch
	}

	// Step 6: Live pages (weibo.com/l/wblive/..., live.weibo.com/...).
	if liveMatch, ok := matchLivePage(host, segs, q, u.String(), isLoopback); ok {
		return liveMatch
	}

	// Step 7: Single posts on weibo.com / www.weibo.com / weibo.cn / m.weibo.cn (or loopback).
	if isWeiboMainHost(host, isLoopback) {
		if postMatch, ok := matchSinglePost(segs, q); ok {
			return postMatch
		}
	}

	// Step 8: Explicit user profiles (/u/, /profile/, /p/100505, /p/100606, single numeric /<uid>, /n/<nick>).
	if isWeiboMainHost(host, isLoopback) {
		if userMatch, ok := matchExplicitUser(segs, q); ok {
			return userMatch
		}
	}

	// Step 9: Custom domain user profile (lowest priority, strictly excluding reserved blacklist).
	if host == "weibo.com" || host == "www.weibo.com" || isLoopback {
		if len(segs) == 1 {
			cand := segs[0]
			lowerCand := strings.ToLower(cand)
			if !reservedWeiboRootPaths[lowerCand] && !rePureDigits.MatchString(cand) && reCustomDomain.MatchString(cand) {
				return localRouteMatch{
					kind:         routeUserCustom,
					customDomain: cand,
				}
			}
		}
	}

	// Step 10: Fallback reject (0 API requests).
	return localRouteMatch{
		kind:   routeUnsupported,
		reason: "暂不支持这种微博链接",
	}
}

func isWeiboMainHost(host string, isLoopback bool) bool {
	if isLoopback {
		return true
	}
	switch host {
	case "weibo.com", "www.weibo.com", "weibo.cn", "m.weibo.cn":
		return true
	default:
		return false
	}
}

func matchUnsupportedSubdomainOrPath(host string, segs []string, q url.Values) (localRouteMatch, bool) {
	switch host {
	case "s.weibo.com":
		return localRouteMatch{kind: routeUnsupported, reason: "暂不支持微博搜索或话题页链接"}, true
	case "card.weibo.com":
		return localRouteMatch{kind: routeUnsupported, reason: "暂不支持独立头条文章或卡片页链接"}, true
	case "vote.weibo.com":
		return localRouteMatch{kind: routeUnsupported, reason: "暂不支持微博投票链接"}, true
	case "mall.weibo.com", "e.weibo.com":
		return localRouteMatch{kind: routeUnsupported, reason: "暂不支持微博电商或商业页链接"}, true
	}
	if loginWallHosts[host] {
		return localRouteMatch{kind: routeUnsupported, reason: "暂不支持微博登录或访客验证页面链接"}, true
	}

	if len(segs) == 0 {
		return localRouteMatch{kind: routeUnsupported, reason: "暂不支持微博首页链接"}, true
	}

	s0 := strings.ToLower(segs[0])
	switch s0 {
	case "ttarticle", "article":
		return localRouteMatch{kind: routeUnsupported, reason: "暂不支持独立头条文章链接"}, true
	case "k":
		return localRouteMatch{kind: routeUnsupported, reason: "暂不支持微博超话链接"}, true
	case "hot":
		return localRouteMatch{kind: routeUnsupported, reason: "暂不支持微博热榜链接"}, true
	case "a":
		if len(segs) >= 2 && strings.EqualFold(segs[1], "hot") {
			return localRouteMatch{kind: routeUnsupported, reason: "暂不支持微博热榜链接"}, true
		}
		return localRouteMatch{kind: routeUnsupported, reason: "暂不支持微博发现或热榜链接"}, true
	case "search":
		return localRouteMatch{kind: routeUnsupported, reason: "暂不支持微博搜索链接"}, true
	case "home":
		return localRouteMatch{kind: routeUnsupported, reason: "暂不支持微博首页信息流链接"}, true
	case "mygroups", "groups":
		return localRouteMatch{kind: routeUnsupported, reason: "暂不支持微博分组信息流链接"}, true
	case "fav", "favorites", "like", "likes":
		return localRouteMatch{kind: routeUnsupported, reason: "暂不支持直接解析微博个人收藏或点赞页链接"}, true
	case "newlogin", "login", "login.php", "signup", "logout":
		return localRouteMatch{kind: routeUnsupported, reason: "暂不支持微博登录或通行证页面链接"}, true
	case "p":
		if len(segs) >= 2 {
			p1 := strings.ToLower(segs[1])
			if strings.HasPrefix(p1, "100808") {
				return localRouteMatch{kind: routeUnsupported, reason: "暂不支持微博超话链接"}, true
			}
			if strings.HasPrefix(p1, "searchall") {
				return localRouteMatch{kind: routeUnsupported, reason: "暂不支持微博搜索链接"}, true
			}
			if p1 == "index" {
				cid := strings.TrimSpace(q.Get("containerid"))
				if strings.HasPrefix(cid, "100808") {
					return localRouteMatch{kind: routeUnsupported, reason: "暂不支持微博超话链接"}, true
				}
				if !reContainerUser.MatchString(cid) {
					return localRouteMatch{kind: routeUnsupported, reason: "暂不支持该微博容器聚合页链接"}, true
				}
			}
		}
	case "tv":
		// Only /tv/show/1034:... is supported; reject collections/channels/home.
		if len(segs) < 3 || !strings.EqualFold(segs[1], "show") {
			return localRouteMatch{kind: routeUnsupported, reason: "暂不支持微博视频合集或频道页链接"}, true
		}
	}
	return localRouteMatch{}, false
}

func matchAppSharePage(segs []string, q url.Values) localRouteMatch {
	if rawMid := strings.TrimSpace(q.Get("weibo_id")); reMidDigits.MatchString(rawMid) {
		mblogID, _ := MidToMblogID(rawMid)
		return localRouteMatch{
			kind:         routePost,
			mid:          rawMid,
			mblogID:      mblogID,
			canonicalURL: canonicalPostURL("", rawMid, mblogID),
		}
	}
	if len(segs) > 0 {
		last := segs[len(segs)-1]
		if m := reAppShareMidFile.FindStringSubmatch(last); len(m) == 2 {
			mid := m[1]
			mblogID, _ := MidToMblogID(mid)
			return localRouteMatch{
				kind:         routePost,
				mid:          mid,
				mblogID:      mblogID,
				canonicalURL: canonicalPostURL("", mid, mblogID),
			}
		}
	}
	return localRouteMatch{
		kind:   routeUnsupported,
		reason: "该微博分享页链接缺少可识别的微博编号",
	}
}

func matchVideoPage(host string, segs []string, q url.Values, isLoopback bool) (localRouteMatch, bool) {
	extractOID := func(raw string) string {
		s := strings.TrimSpace(raw)
		if decoded, err := url.QueryUnescape(s); err == nil {
			s = strings.TrimSpace(decoded)
		}
		if reVideoOID.MatchString(s) {
			return s
		}
		return ""
	}

	var oid string
	switch {
	case host == "video.weibo.com":
		if len(segs) >= 1 && strings.EqualFold(segs[0], "show") {
			oid = extractOID(q.Get("fid"))
		}
		if oid == "" {
			return localRouteMatch{kind: routeUnsupported, reason: "暂不支持该微博视频页链接"}, true
		}

	case (host == "weibo.com" || host == "www.weibo.com" || isLoopback) && len(segs) >= 3 && strings.EqualFold(segs[0], "tv") && strings.EqualFold(segs[1], "show"):
		oid = extractOID(segs[2])
		if oid == "" {
			return localRouteMatch{kind: routeUnsupported, reason: "暂不支持该微博视频页链接"}, true
		}

	case (host == "m.weibo.cn" || isLoopback) && len(segs) >= 3 && strings.EqualFold(segs[0], "s") && strings.EqualFold(segs[1], "video") && (strings.EqualFold(segs[2], "index") || strings.EqualFold(segs[2], "show")):
		oid = extractOID(q.Get("object_id"))
		if oid == "" {
			oid = extractOID(q.Get("fid"))
		}
		if oid == "" {
			return localRouteMatch{kind: routeUnsupported, reason: "暂不支持该微博视频页链接"}, true
		}

	default:
		return localRouteMatch{}, false
	}

	// If URL explicitly carries host post mid, associate directly to host post without misrouting media_id.
	if rawMid := strings.TrimSpace(q.Get("mid")); reMidDigits.MatchString(rawMid) {
		mblogID, _ := MidToMblogID(rawMid)
		return localRouteMatch{
			kind:         routePost,
			mid:          rawMid,
			mblogID:      mblogID,
			canonicalURL: canonicalPostURL("", rawMid, mblogID),
		}, true
	}

	return localRouteMatch{
		kind:         routeVideo,
		videoOID:     oid,
		canonicalURL: "https://weibo.com/tv/show/" + oid,
	}, true
}

func matchLivePage(host string, segs []string, q url.Values, rawURL string, isLoopback bool) (localRouteMatch, bool) {
	normalizeLiveID := func(raw string) string {
		s := strings.TrimSpace(raw)
		if decoded, err := url.QueryUnescape(s); err == nil {
			s = strings.TrimSpace(decoded)
		}
		return s
	}

	if host == "live.weibo.com" || host == "zhibo.sina.com.cn" {
		liveID := normalizeLiveID(q.Get("id"))
		if liveID == "" && len(segs) >= 2 && strings.EqualFold(segs[0], "show") {
			liveID = normalizeLiveID(segs[1])
		}
		canon := rawURL
		if liveID != "" {
			canon = "https://live.weibo.com/show?id=" + url.QueryEscape(liveID)
		}
		return localRouteMatch{
			kind:         routeLive,
			liveID:       liveID,
			canonicalURL: canon,
			reason:       liveUnsupportText,
		}, true
	}

	if (host == "weibo.com" || host == "www.weibo.com" || host == "m.weibo.cn" || isLoopback) &&
		len(segs) >= 2 && strings.EqualFold(segs[0], "l") && strings.EqualFold(segs[1], "wblive") {
		var liveID string
		if len(segs) >= 5 && strings.EqualFold(segs[3], "show") {
			liveID = normalizeLiveID(segs[4])
		} else if len(segs) >= 4 && strings.EqualFold(segs[2], "show") {
			liveID = normalizeLiveID(segs[3])
		} else {
			liveID = normalizeLiveID(q.Get("live_id"))
		}
		canon := rawURL
		if liveID != "" {
			canon = "https://weibo.com/l/wblive/p/show/" + liveID
		}
		return localRouteMatch{
			kind:         routeLive,
			liveID:       liveID,
			canonicalURL: canon,
			reason:       liveUnsupportText,
		}, true
	}

	return localRouteMatch{}, false
}

func matchSinglePost(segs []string, q url.Values) (localRouteMatch, bool) {
	if len(segs) == 0 {
		return localRouteMatch{}, false
	}
	s0 := strings.ToLower(segs[0])

	// /detail/<id>, /status/<id>
	if (s0 == "detail" || s0 == "status") && len(segs) >= 2 {
		if mid, mblogID, ok := parsePostIdentifier(segs[1]); ok {
			return localRouteMatch{
				kind:         routePost,
				mid:          mid,
				mblogID:      mblogID,
				canonicalURL: canonicalPostURL("", mid, mblogID),
			}, true
		}
		return localRouteMatch{kind: routeUnsupported, reason: "微博详情链接编号无效"}, true
	}

	// /2/detail/<id>
	if s0 == "2" && len(segs) >= 3 && strings.EqualFold(segs[1], "detail") {
		if mid, mblogID, ok := parsePostIdentifier(segs[2]); ok {
			return localRouteMatch{
				kind:         routePost,
				mid:          mid,
				mblogID:      mblogID,
				canonicalURL: canonicalPostURL("", mid, mblogID),
			}, true
		}
		return localRouteMatch{kind: routeUnsupported, reason: "微博详情链接编号无效"}, true
	}

	// /statuses/show?id=<id> or /ajax/statuses/show?id=<id>
	if (s0 == "statuses" && len(segs) >= 2 && strings.EqualFold(segs[1], "show")) ||
		(s0 == "ajax" && len(segs) >= 3 && strings.EqualFold(segs[1], "statuses") && strings.EqualFold(segs[2], "show")) {
		if mid, mblogID, ok := parsePostIdentifier(q.Get("id")); ok {
			return localRouteMatch{
				kind:         routePost,
				mid:          mid,
				mblogID:      mblogID,
				canonicalURL: canonicalPostURL("", mid, mblogID),
			}, true
		}
		return localRouteMatch{kind: routeUnsupported, reason: "微博详情链接缺少有效编号"}, true
	}

	// /<uid>/<mid_or_mblogid> or /<uid>/<mid_or_mblogid>/<channel>
	if len(segs) >= 2 {
		if normUID, err := site.NormalizeUID(site.Weibo, segs[0]); err == nil {
			seg2 := segs[1]
			if reservedUserSubPaths[strings.ToLower(seg2)] {
				return localRouteMatch{}, false
			}
			if mid, mblogID, ok := parsePostIdentifier(seg2); ok {
				return localRouteMatch{
					kind:         routePost,
					mid:          mid,
					mblogID:      mblogID,
					authorUID:    normUID,
					canonicalURL: canonicalPostURL(normUID, mid, mblogID),
				}, true
			}
		}
	}

	return localRouteMatch{}, false
}

func matchExplicitUser(segs []string, q url.Values) (localRouteMatch, bool) {
	if len(segs) == 0 {
		return localRouteMatch{}, false
	}
	s0 := strings.ToLower(segs[0])

	// /u/<uid> or /profile/<uid>
	if (s0 == "u" || s0 == "profile") && len(segs) >= 2 {
		if normUID, err := site.NormalizeUID(site.Weibo, segs[1]); err == nil {
			return localRouteMatch{
				kind:         routeUser,
				uid:          normUID,
				canonicalURL: "https://weibo.com/u/" + normUID,
			}, true
		}
		return localRouteMatch{kind: routeUnsupported, reason: "微博用户链接中的 UID 无效"}, true
	}

	// /p/100505<uid> or /p/100606<uid> (with optional /home, /info) or /p/index?containerid=100505<uid>
	if s0 == "p" && len(segs) >= 2 {
		if m := reContainerUser.FindStringSubmatch(segs[1]); len(m) == 2 {
			if normUID, err := site.NormalizeUID(site.Weibo, m[1]); err == nil {
				return localRouteMatch{
					kind:         routeUser,
					uid:          normUID,
					canonicalURL: "https://weibo.com/u/" + normUID,
				}, true
			}
		}
		if strings.EqualFold(segs[1], "index") {
			if m := reContainerUser.FindStringSubmatch(strings.TrimSpace(q.Get("containerid"))); len(m) == 2 {
				if normUID, err := site.NormalizeUID(site.Weibo, m[1]); err == nil {
					return localRouteMatch{
						kind:         routeUser,
						uid:          normUID,
						canonicalURL: "https://weibo.com/u/" + normUID,
					}, true
				}
			}
		}
		return localRouteMatch{kind: routeUnsupported, reason: "暂不支持该微博容器页链接"}, true
	}

	// /n/<screen_name> (u.Path is already percent-decoded once by url.Parse).
	if s0 == "n" && len(segs) >= 2 {
		nick := strings.TrimSpace(segs[1])
		if nick != "" {
			return localRouteMatch{
				kind:       routeUserNick,
				screenName: nick,
			}, true
		}
		return localRouteMatch{kind: routeUnsupported, reason: "微博昵称链接缺少用户昵称"}, true
	}

	// Single-segment numeric /<uid> (or /<uid>/home, /<uid>/profile, /<uid>/info).
	if normUID, err := site.NormalizeUID(site.Weibo, segs[0]); err == nil {
		if len(segs) == 1 || (len(segs) == 2 && reservedUserSubPaths[strings.ToLower(segs[1])]) {
			return localRouteMatch{
				kind:         routeUser,
				uid:          normUID,
				canonicalURL: "https://weibo.com/u/" + normUID,
			}, true
		}
	}

	return localRouteMatch{}, false
}

// parsePostIdentifier normalizes either a 15..20 digit numeric mid or a Base62 mblogid into (mid, mblogid).
func parsePostIdentifier(raw string) (mid string, mblogID string, ok bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", "", false
	}
	if reMidDigits.MatchString(s) {
		mb, err := MidToMblogID(s)
		if err != nil {
			return "", "", false
		}
		return s, mb, true
	}
	if reMblogIDToken.MatchString(s) {
		m, err := MblogIDToMid(s)
		if err != nil {
			return "", "", false
		}
		mb, err := MidToMblogID(m)
		if err != nil {
			return "", "", false
		}
		return m, mb, true
	}
	return "", "", false
}

func canonicalPostURL(authorUID, mid, mblogID string) string {
	if authorUID != "" && mblogID != "" {
		return "https://weibo.com/" + authorUID + "/" + mblogID
	}
	if mid != "" {
		return "https://weibo.com/detail/" + mid
	}
	return "https://weibo.com/"
}

func (c *Client) unpackSinaURL(u *url.URL) (*url.URL, error) {
	q := u.Query()
	rawTarget := strings.TrimSpace(q.Get("u"))
	if rawTarget == "" {
		rawTarget = strings.TrimSpace(q.Get("toasturl"))
	}
	if rawTarget == "" {
		return nil, &site.Error{
			Site:        site.Weibo,
			Kind:        site.ErrKindUnsupportedLink,
			Unsupported: true,
			Message:     "微博中转链接缺少目标地址",
		}
	}

	targetURL, err := url.Parse(rawTarget)
	if err != nil || targetURL.Host == "" {
		return nil, &site.Error{
			Site:        site.Weibo,
			Kind:        site.ErrKindBlocked,
			Unsupported: true,
			Message:     "微博中转链接目标格式无效",
		}
	}
	if targetURL.User != nil {
		return nil, &site.Error{
			Site:        site.Weibo,
			Kind:        site.ErrKindBlocked,
			Unsupported: true,
			Message:     "已拦截含认证信息的中转链接",
		}
	}

	host := strings.ToLower(targetURL.Hostname())
	if loginWallHosts[host] || (isWeiboMainHost(host, false) && isLoginPath(targetURL.Path)) {
		return nil, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindAuthRequired,
			Message: "该中转链接指向微博登录或访客页，请先登录微博",
		}
	}
	if c.allowLocalhost && site.IsLoopbackHost(host) {
		if strings.EqualFold(targetURL.Scheme, "http") || strings.EqualFold(targetURL.Scheme, "https") {
			return targetURL, nil
		}
	}
	if !strings.EqualFold(targetURL.Scheme, "https") {
		return nil, &site.Error{
			Site:        site.Weibo,
			Kind:        site.ErrKindBlocked,
			Unsupported: true,
			Message:     "该短链指向非 HTTPS 目标地址，不支持解析",
		}
	}
	if port := targetURL.Port(); port != "" && port != "443" {
		return nil, &site.Error{
			Site:        site.Weibo,
			Kind:        site.ErrKindBlocked,
			Unsupported: true,
			Message:     "该短链指向非常规端口，不支持解析",
		}
	}
	if net.ParseIP(host) != nil || !allowedWeiboRedirectHosts[host] {
		return nil, &site.Error{
			Site:        site.Weibo,
			Kind:        site.ErrKindBlocked,
			Unsupported: true,
			Message:     "该短链指向微博以外的网站，不支持解析",
		}
	}
	return targetURL, nil
}

func isLoginPath(p string) bool {
	lower := strings.ToLower(strings.TrimSpace(p))
	return strings.HasPrefix(lower, "/newlogin") || strings.HasPrefix(lower, "/login")
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
				Site:    site.Weibo,
				Kind:    site.ErrKindBadRequest,
				Message: "构造 t.cn 短链请求失败",
				Err:     site.UnwrapURLError(reqErr),
			}
		}
		req.Header.Set("User-Agent", c.profile.UserAgent)
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
		// Strictly never send Cookie to t.cn or redirect hops (weibo.md §3.1 #2).
		req.Header.Del("Cookie")

		resp, doErr := c.redirectClient.Do(req)
		hops++
		if doErr != nil {
			release()
			if site.IsKind(doErr, site.ErrKindBlocked) {
				return nil, localRouteMatch{}, hops, doErr
			}
			netErr := &site.Error{
				Site:     site.Weibo,
				Kind:     site.ErrKindNetwork,
				Endpoint: sanitizeEndpointLabel(cur),
				Message:  "展开微博短链失败",
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
			case http.StatusRequestURITooLong, http.StatusTooManyRequests:
				kind = site.ErrKindRateLimited
				msg = fmt.Sprintf("微博短链请求过于频繁（HTTP %d），已停止请求", resp.StatusCode)
			case http.StatusTeapot, 432, http.StatusForbidden:
				kind = site.ErrKindRiskControl
				msg = fmt.Sprintf("微博短链触发风控拦截（HTTP %d），已停止请求", resp.StatusCode)
			case http.StatusUnauthorized:
				kind = site.ErrKindAuthRequired
				msg = "微博短链请求需要登录（HTTP 401）"
			default:
				kind = site.ErrKindUnavailable
				msg = fmt.Sprintf("微博短链未返回有效跳转（HTTP %d）", resp.StatusCode)
			}
			se := &site.Error{
				Site:       site.Weibo,
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
				Site:       site.Weibo,
				Kind:       site.ErrKindUnavailable,
				HTTPStatus: resp.StatusCode,
				Endpoint:   sanitizeEndpointLabel(cur),
				Message:    "微博短链响应缺少跳转地址",
			}
		}

		nextURL, pErr := cur.Parse(rawLoc)
		if pErr != nil || nextURL.Host == "" {
			return nil, localRouteMatch{}, hops, &site.Error{
				Site:     site.Weibo,
				Kind:     site.ErrKindBlocked,
				Endpoint: sanitizeEndpointLabel(cur),
				Message:  "微博短链重定向地址格式无效",
			}
		}

		// Validate hop target (checks login/visitor wall first, then strict HTTPS/443/allowlist).
		validatedURL, vErr := c.validateShortLinkHopTarget(nextURL)
		if vErr != nil {
			return nil, localRouteMatch{}, hops, vErr
		}

		// Unpack sinaurl locally with 0 extra network requests.
		match := c.classifyLocalURL(validatedURL)
		sinaDepth := 0
		for match.kind == routeSinaURL {
			if sinaDepth >= maxSinaURLDepth {
				return nil, localRouteMatch{}, hops, &site.Error{
					Site:        site.Weibo,
					Kind:        site.ErrKindBlocked,
					Unsupported: true,
					Message:     "短链中转嵌套层数过多，已拒绝解析",
				}
			}
			sinaDepth++
			unpacked, uErr := c.unpackSinaURL(validatedURL)
			if uErr != nil {
				return nil, localRouteMatch{}, hops, uErr
			}
			validatedURL = unpacked
			match = c.classifyLocalURL(validatedURL)
		}

		// Early convergence: stop immediately once any target ID or explicit unsupported route is reached!
		if match.kind != routeShortLink && match.kind != routeUnknown {
			return validatedURL, match, hops, nil
		}

		if canonicalHopKey(validatedURL) == canonicalHopKey(cur) {
			return nil, localRouteMatch{}, hops, &site.Error{
				Site:     site.Weibo,
				Kind:     site.ErrKindBlocked,
				Endpoint: sanitizeEndpointLabel(cur),
				Message:  "微博短链重定向陷入原地循环",
			}
		}
		cur = validatedURL
	}

	return nil, localRouteMatch{}, hops, &site.Error{
		Site:    site.Weibo,
		Kind:    site.ErrKindBlocked,
		Message: "微博短链跳转超过 5 跳安全上限",
	}
}

func (c *Client) validateShortLinkHopTarget(u *url.URL) (*url.URL, error) {
	if u == nil || u.Host == "" || u.User != nil {
		return nil, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindBlocked,
			Message: "已拦截不安全的短链跳转目标",
		}
	}

	host := strings.ToLower(u.Hostname())

	// Distinguish login/visitor redirects from generic external blocks (weibo.md §3.1 #5).
	if loginWallHosts[host] || (isWeiboMainHost(host, c.allowLocalhost && site.IsLoopbackHost(host)) && isLoginPath(u.Path)) {
		return nil, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindAuthRequired,
			Message: "该短链跳转至微博登录或访客页，请先登录微博",
		}
	}

	if c.allowLocalhost && site.IsLoopbackHost(host) {
		if strings.EqualFold(u.Scheme, "http") || strings.EqualFold(u.Scheme, "https") {
			return u, nil
		}
	}

	if port := u.Port(); port != "" && port != "443" {
		return nil, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindBlocked,
			Message: "已拦截短链跳转到非常规端口",
		}
	}

	// Only http://t.cn/... may upgrade to https://t.cn/...; other http hops are blocked.
	if strings.EqualFold(u.Scheme, "http") {
		if host == "t.cn" {
			cloned := *u
			cloned.Scheme = "https"
			u = &cloned
		} else {
			return nil, &site.Error{
				Site:    site.Weibo,
				Kind:    site.ErrKindBlocked,
				Message: "已拦截短链跳转到非 HTTPS 地址",
			}
		}
	} else if !strings.EqualFold(u.Scheme, "https") {
		return nil, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindBlocked,
			Message: "已拦截短链跳转到非 HTTPS 地址",
		}
	}

	if net.ParseIP(host) != nil {
		return nil, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindBlocked,
			Message: "已拦截短链跳转到直接 IP 地址",
		}
	}

	// Check known unsupported Weibo subdomains (e.g. s.weibo.com, card.weibo.com) so they classify as unsupported_link.
	if host == "s.weibo.com" || host == "card.weibo.com" || host == "vote.weibo.com" || host == "mall.weibo.com" || host == "e.weibo.com" {
		return u, nil
	}

	if !allowedWeiboRedirectHosts[host] {
		return nil, &site.Error{
			Site:        site.Weibo,
			Kind:        site.ErrKindBlocked,
			Unsupported: true,
			Message:     "该短链指向微博以外的网站，不支持解析",
		}
	}
	return u, nil
}

func canonicalHopKey(u *url.URL) string {
	if u == nil {
		return ""
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host) + strings.TrimRight(u.Path, "/") + "?" + u.RawQuery
}

func (c *Client) materializeRouteMatch(ctx context.Context, _ *url.URL, match localRouteMatch) (site.ResolveTarget, error) {
	if err := c.RequireAuth("resolve"); err != nil {
		return site.ResolveTarget{}, err
	}

	switch match.kind {
	case routePost:
		return site.ResolveTarget{
			Site:          site.Weibo,
			Kind:          "dynamic",
			ID:            match.mid,
			AuthorUID:     match.authorUID,
			HintAuthorUID: match.authorUID,
			MblogID:       match.mblogID,
			CanonicalURL:  match.canonicalURL,
		}, nil

	case routeVideo:
		return site.ResolveTarget{
			Site:         site.Weibo,
			Kind:         "video",
			ID:           match.videoOID,
			CanonicalURL: match.canonicalURL,
		}, nil

	case routeUser:
		return site.ResolveTarget{
			Site:         site.Weibo,
			Kind:         "user",
			ID:           match.uid,
			AuthorUID:    match.uid,
			CanonicalURL: match.canonicalURL,
		}, nil

	case routeUserNick:
		prof, err := c.GetUserProfileByScreenName(ctx, match.screenName)
		if err != nil {
			return site.ResolveTarget{}, err
		}
		return site.ResolveTarget{
			Site:         site.Weibo,
			Kind:         "user",
			ID:           prof.UID,
			AuthorUID:    prof.UID,
			CanonicalURL: "https://weibo.com/u/" + prof.UID,
		}, nil

	case routeUserCustom:
		prof, err := c.GetUserProfileByCustom(ctx, match.customDomain)
		if err != nil {
			return site.ResolveTarget{}, err
		}
		return site.ResolveTarget{
			Site:         site.Weibo,
			Kind:         "user",
			ID:           prof.UID,
			AuthorUID:    prof.UID,
			CanonicalURL: "https://weibo.com/u/" + prof.UID,
		}, nil

	default:
		return site.ResolveTarget{}, &site.Error{
			Site:        site.Weibo,
			Kind:        site.ErrKindUnsupportedLink,
			Unsupported: true,
			Message:     "暂不支持这种微博链接",
		}
	}
}
