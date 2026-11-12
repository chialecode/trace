package douyin

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/site"
)

const maxShortLinkHops = 5

var (
	reWorkIDDigits     = regexp.MustCompile(`^[1-9][0-9]{14,19}$`)
	reRoomDigits       = regexp.MustCompile(`^[1-9][0-9]{3,20}$`)
	reReflowRoomDigits = regexp.MustCompile(`^[1-9][0-9]{0,23}$`)
	rePure19Digits     = regexp.MustCompile(`^[1-9][0-9]{18}$`)
	reGluedOral        = regexp.MustCompile(`^(https?://[^/]+/[A-Za-z0-9_-]+)/\d+(?:\.\d+)?/?$`)
)

var allowedDouyinRedirectHosts = []string{
	"douyin.com",
	"www.douyin.com",
	"live.douyin.com",
	"iesdouyin.com",
	"www.iesdouyin.com",
	"amemv.com",
	"v.amemv.com",
	"webcast.amemv.com",
	"mall.douyin.com",
	"jinritemai.com",
	"haohuo.jinritemai.com",
}

var shortLinkHosts = map[string]bool{
	"v.douyin.com": true,
	"v.amemv.com":  true,
}

type localRouteKind string

const (
	routeUnknown     localRouteKind = ""
	routeShortLink   localRouteKind = "short_link"
	routeVideo       localRouteKind = "video"
	routeNote        localRouteKind = "note"
	routeLiveWebRID  localRouteKind = "live_webrid"
	routeLiveReflow  localRouteKind = "live_reflow"
	routeUserSelf    localRouteKind = "user_self"
	routeUser        localRouteKind = "user"
	routeUnsupported localRouteKind = "unsupported"
)

type localRouteMatch struct {
	kind   localRouteKind
	id     string
	secUID string
	webRID string
	roomID string
	reason string
}

// ExtractShareURL extracts and cleans the first Douyin URL from mixed share text,
// stripping glued oral-code tails such as /7.46 on v.douyin.com short links.
func ExtractShareURL(text string) (string, bool) {
	u, ok := site.ExtractShareURL(text)
	if !ok {
		return "", false
	}
	if m := reGluedOral.FindStringSubmatch(u); len(m) == 2 {
		if parsed, err := url.Parse(u); err == nil && shortLinkHosts[strings.ToLower(parsed.Hostname())] {
			u = m[1] + "/"
		}
	}
	return u, true
}

// ResolveLink resolves Douyin share text, short links, video/note/user/live URLs, /user/self,
// or bare sec_uid tokens into a normalized site.ResolveTarget per douyin.md §4.1–§4.3.
func (c *Client) ResolveLink(ctx context.Context, rawInput string) (site.ResolveTarget, error) {
	trimmed := strings.TrimSpace(rawInput)
	if trimmed == "" {
		return site.ResolveTarget{}, &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindBadRequest,
			Message: "请输入有效的抖音分享链接或用户标识",
		}
	}

	// 1. Check URL in share text or direct URL input.
	if extracted, ok := ExtractShareURL(trimmed); ok {
		return c.resolveURL(ctx, extracted)
	}

	// 2. Bare 19-digit pure number -> reject locally with 0 requests (douyin.md §4.1 & §11).
	if rePure19Digits.MatchString(trimmed) {
		return site.ResolveTarget{}, &site.Error{
			Site:        site.Douyin,
			Kind:        site.ErrKindBadRequest,
			Unsupported: true,
			Message:     "纯 19 位数字无法区分抖音作品、用户或直播间，请粘贴完整分享链接",
		}
	}
	if reDigitsOnly.MatchString(trimmed) {
		return site.ResolveTarget{}, &site.Error{
			Site:        site.Douyin,
			Kind:        site.ErrKindBadRequest,
			Unsupported: true,
			Message:     "纯数字编号无法直接识别抖音目标类型，请粘贴完整分享链接",
		}
	}

	// 3. Literal /user/self alias -> map to current logged-in account sec_uid.
	if trimmed == "/user/self" || strings.EqualFold(trimmed, "user/self") {
		return c.resolveUserSelf(ctx)
	}

	// 4. Bare sec_uid (MS4wLjAB...) -> user profile target with 0 network requests.
	if reValidSecUID.MatchString(trimmed) {
		if err := c.RequireAuth("resolve"); err != nil {
			return site.ResolveTarget{}, err
		}
		return site.ResolveTarget{
			Site:         site.Douyin,
			Kind:         "user",
			ID:           trimmed,
			SecUID:       trimmed,
			CanonicalURL: "https://www.douyin.com/user/" + trimmed,
		}, nil
	}

	return site.ResolveTarget{}, &site.Error{
		Site:    site.Douyin,
		Kind:    site.ErrKindBadRequest,
		Message: "无法识别该抖音输入，请粘贴完整的分享口令或链接",
	}
}

func (c *Client) resolveURL(ctx context.Context, rawURL string) (site.ResolveTarget, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return site.ResolveTarget{}, &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindBadRequest,
			Message: "无效的抖音链接格式",
		}
	}
	if err := c.validateResolveHopURL(u, false); err != nil {
		return site.ResolveTarget{}, err
	}

	match := c.classifyLocalURL(u)
	if match.kind == routeUnsupported {
		return site.ResolveTarget{}, &site.Error{
			Site:        site.Douyin,
			Kind:        site.ErrKindBadRequest,
			Unsupported: true,
			Message:     match.reason,
		}
	}

	if match.kind == routeShortLink {
		if err := c.RequireAuth("resolve"); err != nil {
			return site.ResolveTarget{}, err
		}
		var hopErr error
		u, match, hopErr = c.expandShortLink(ctx, u)
		if hopErr != nil {
			return site.ResolveTarget{}, hopErr
		}
		if match.kind == routeUnsupported {
			return site.ResolveTarget{}, &site.Error{
				Site:        site.Douyin,
				Kind:        site.ErrKindBadRequest,
				Unsupported: true,
				Message:     match.reason,
			}
		}
	}

	return c.materializeRouteMatch(ctx, u, match)
}

func (c *Client) validateResolveHopURL(u *url.URL, isRedirect bool) error {
	errKindForScheme := site.ErrKindBlocked
	if u.User != nil {
		return &site.Error{
			Site:        site.Douyin,
			Kind:        site.ErrKindBlocked,
			Unsupported: !isRedirect,
			Message:     "已拦截含认证信息的链接",
		}
	}

	host := strings.ToLower(u.Hostname())
	if c.allowLocalhost && isLoopbackHost(host) {
		if strings.EqualFold(u.Scheme, "http") || strings.EqualFold(u.Scheme, "https") {
			return nil
		}
	}

	if !strings.EqualFold(u.Scheme, "https") {
		return &site.Error{
			Site:        site.Douyin,
			Kind:        errKindForScheme,
			Unsupported: !isRedirect,
			Message:     "仅支持 HTTPS 抖音链接",
		}
	}
	if port := u.Port(); port != "" && port != "443" {
		return &site.Error{
			Site:        site.Douyin,
			Kind:        site.ErrKindBlocked,
			Unsupported: !isRedirect,
			Message:     "已拦截非常规端口链接",
		}
	}
	if ip := net.ParseIP(host); ip != nil {
		return &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindBlocked,
			Message: "已拦截直接 IP 地址访问",
		}
	}
	if !isAllowedDouyinRedirectHost(host) {
		kind := site.ErrKindBadRequest
		if isRedirect {
			kind = site.ErrKindBlocked
		}
		return &site.Error{
			Site:        site.Douyin,
			Kind:        kind,
			Unsupported: !isRedirect,
			Message:     fmt.Sprintf("暂不支持非抖音域名: %s", host),
		}
	}
	return nil
}

func isAllowedDouyinRedirectHost(host string) bool {
	if shortLinkHosts[host] {
		return true
	}
	for _, allowed := range allowedDouyinRedirectHosts {
		if host == allowed || (allowed == "jinritemai.com" && strings.HasSuffix(host, ".jinritemai.com")) {
			return true
		}
	}
	return false
}

// classifyLocalURL applies the strict priority table in douyin.md §4.3 without network I/O.
func (c *Client) classifyLocalURL(u *url.URL) localRouteMatch {
	host := strings.ToLower(u.Hostname())
	segments := splitPathSegments(u.Path)
	lowerPath := "/" + strings.ToLower(strings.Join(segments, "/"))
	if len(segments) > 0 {
		lowerPath += "/"
	}

	// E-commerce domains & paths are always unsupported (even if modal_id were present).
	if host == "mall.douyin.com" || host == "jinritemai.com" || strings.HasSuffix(host, ".jinritemai.com") ||
		strings.Contains(lowerPath, "/shop/") || strings.Contains(lowerPath, "/goods/") {
		return localRouteMatch{
			kind:   routeUnsupported,
			reason: "暂不支持抖音商城或商品链接",
		}
	}

	// Priority 1: Short-link domains (v.douyin.com, v.amemv.com, or synthetic loopback /short/<code>).
	if shortLinkHosts[host] || (c.allowLocalhost && isLoopbackHost(host) && len(segments) >= 2 && strings.EqualFold(segments[0], "short")) {
		if len(segments) == 0 || (strings.EqualFold(segments[0], "short") && len(segments) < 2) {
			return localRouteMatch{
				kind:   routeUnsupported,
				reason: "短链缺少口令路径标识",
			}
		}
		return localRouteMatch{kind: routeShortLink}
	}

	q := u.Query()

	// Priority 2: modal_id or vid query parameter BEFORE /user/ or discovery pages (douyin.md §4.3).
	if modalID := strings.TrimSpace(q.Get("modal_id")); reWorkIDDigits.MatchString(modalID) {
		return localRouteMatch{kind: routeVideo, id: modalID}
	}
	if vid := strings.TrimSpace(q.Get("vid")); reWorkIDDigits.MatchString(vid) {
		return localRouteMatch{kind: routeVideo, id: vid}
	}

	// Priority 3: /video/<id>, /note/<id>, /slides/<id>, and /share/{video,note,slides}/<id>.
	if len(segments) >= 2 {
		s0 := strings.ToLower(segments[0])
		switch s0 {
		case "video":
			if reWorkIDDigits.MatchString(segments[1]) {
				return localRouteMatch{kind: routeVideo, id: segments[1]}
			}
		case "note", "slides":
			if reWorkIDDigits.MatchString(segments[1]) {
				return localRouteMatch{kind: routeNote, id: segments[1]}
			}
		case "share":
			if len(segments) >= 3 {
				s1 := strings.ToLower(segments[1])
				switch s1 {
				case "video":
					if reWorkIDDigits.MatchString(segments[2]) {
						return localRouteMatch{kind: routeVideo, id: segments[2]}
					}
				case "note", "slides":
					if reWorkIDDigits.MatchString(segments[2]) {
						return localRouteMatch{kind: routeNote, id: segments[2]}
					}
				case "live":
					if reRoomDigits.MatchString(segments[2]) {
						return localRouteMatch{kind: routeLiveWebRID, webRID: segments[2]}
					}
				case "user":
					if strings.EqualFold(segments[2], "self") {
						return localRouteMatch{kind: routeUserSelf}
					}
					if reValidSecUID.MatchString(segments[2]) {
						return localRouteMatch{kind: routeUser, secUID: segments[2]}
					}
				}
			} else if len(segments) == 2 && strings.EqualFold(segments[1], "user") {
				sec := firstValidSecUID(q.Get("sec_uid"), q.Get("sec_user_id"))
				if sec != "" {
					return localRouteMatch{kind: routeUser, secUID: sec}
				}
			}
		case "follow":
			if len(segments) >= 3 && strings.EqualFold(segments[1], "live") && reRoomDigits.MatchString(segments[2]) {
				return localRouteMatch{kind: routeLiveWebRID, webRID: segments[2]}
			}
		}
	}

	// Priority 4: live.douyin.com/<digits> (or loopback /live/<digits> in tests).
	if host == "live.douyin.com" && len(segments) == 1 && reRoomDigits.MatchString(segments[0]) {
		return localRouteMatch{kind: routeLiveWebRID, webRID: segments[0]}
	}
	if c.allowLocalhost && isLoopbackHost(host) && len(segments) == 2 && strings.EqualFold(segments[0], "live") && reRoomDigits.MatchString(segments[1]) {
		return localRouteMatch{kind: routeLiveWebRID, webRID: segments[1]}
	}

	// Priority 5: webcast.amemv.com/douyin/webcast/reflow/<room_id>.
	if len(segments) >= 4 &&
		strings.EqualFold(segments[0], "douyin") &&
		strings.EqualFold(segments[1], "webcast") &&
		strings.EqualFold(segments[2], "reflow") &&
		reRoomDigits.MatchString(segments[3]) {
		sec := firstValidSecUID(q.Get("sec_user_id"), q.Get("sec_uid"))
		return localRouteMatch{kind: routeLiveReflow, roomID: segments[3], secUID: sec}
	}

	// Priority 6 & 7: /user/self and /user/<sec_uid> (or query sec_uid).
	if len(segments) >= 1 && strings.EqualFold(segments[0], "user") {
		if len(segments) >= 2 {
			if strings.EqualFold(segments[1], "self") {
				return localRouteMatch{kind: routeUserSelf}
			}
			if reValidSecUID.MatchString(segments[1]) {
				return localRouteMatch{kind: routeUser, secUID: segments[1]}
			}
		}
		if sec := firstValidSecUID(q.Get("sec_uid"), q.Get("sec_user_id")); sec != "" {
			return localRouteMatch{kind: routeUser, secUID: sec}
		}
	}

	// Priority 8: Known unsupported Douyin aggregation/discovery/miniapp paths.
	if q.Get("mix_id") != "" {
		return localRouteMatch{kind: routeUnsupported, reason: "暂不支持抖音合集链接"}
	}
	if len(segments) >= 1 {
		switch strings.ToLower(segments[0]) {
		case "collection", "mix":
			return localRouteMatch{kind: routeUnsupported, reason: "暂不支持抖音合集链接"}
		case "music":
			return localRouteMatch{kind: routeUnsupported, reason: "暂不支持抖音音乐聚合页链接"}
		case "hashtag", "challenge":
			return localRouteMatch{kind: routeUnsupported, reason: "暂不支持抖音话题挑战页链接"}
		case "search":
			return localRouteMatch{kind: routeUnsupported, reason: "暂不支持抖音搜索页链接"}
		case "discover", "recommend", "hot", "jingxuan":
			return localRouteMatch{kind: routeUnsupported, reason: "暂不支持抖音发现或热榜聚合链接"}
		case "lvdetail", "vsdetail", "playlet", "series":
			return localRouteMatch{kind: routeUnsupported, reason: "暂不支持抖音放映厅或短剧链接"}
		case "microapp":
			return localRouteMatch{kind: routeUnsupported, reason: "暂不支持抖音小程序分享链接"}
		}
	}
	if strings.Contains(lowerPath, "/microapp/") {
		return localRouteMatch{kind: routeUnsupported, reason: "暂不支持抖音小程序分享链接"}
	}

	return localRouteMatch{
		kind:   routeUnsupported,
		reason: "暂不支持这种抖音链接",
	}
}

func firstValidSecUID(candidates ...string) string {
	for _, raw := range candidates {
		trimmed := strings.TrimSpace(raw)
		if reValidSecUID.MatchString(trimmed) {
			return trimmed
		}
	}
	return ""
}

func splitPathSegments(p string) []string {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	out := make([]string, 0, len(parts))
	for _, seg := range parts {
		if seg != "" {
			out = append(out, seg)
		}
	}
	return out
}

func (c *Client) expandShortLink(ctx context.Context, startURL *url.URL) (*url.URL, localRouteMatch, error) {
	cur := startURL
	for hop := 0; hop < maxShortLinkHops; hop++ {
		if err := c.checkAutomaticBudget(ctx, sanitizeEndpointLabel(cur)); err != nil {
			return nil, localRouteMatch{}, err
		}
		release, err := c.acquireSlotAndPace(ctx)
		if err != nil {
			return nil, localRouteMatch{}, err
		}

		req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, cur.String(), nil)
		if reqErr != nil {
			release()
			return nil, localRouteMatch{}, &site.Error{
				Site:    site.Douyin,
				Kind:    site.ErrKindBadRequest,
				Message: "构造抖音短链请求失败",
				Err:     reqErr,
			}
		}
		req.Header.Set("User-Agent", c.profile.UserAgent)
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")

		resp, doErr := c.redirectClient.Do(req)
		if doErr != nil {
			release()
			return nil, localRouteMatch{}, &site.Error{
				Site:     site.Douyin,
				Kind:     site.ErrKindNetwork,
				Endpoint: sanitizeEndpointLabel(cur),
				Message:  "展开抖音短链失败",
				Err:      doErr,
			}
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		release()

		if resp.StatusCode < 300 || resp.StatusCode >= 400 {
			return nil, localRouteMatch{}, &site.Error{
				Site:       site.Douyin,
				Kind:       site.ErrKindUnavailable,
				HTTPStatus: resp.StatusCode,
				Endpoint:   sanitizeEndpointLabel(cur),
				Message:    fmt.Sprintf("短链未返回有效跳转（HTTP %d）", resp.StatusCode),
			}
		}

		loc := strings.TrimSpace(resp.Header.Get("Location"))
		if loc == "" {
			return nil, localRouteMatch{}, &site.Error{
				Site:       site.Douyin,
				Kind:       site.ErrKindUnavailable,
				HTTPStatus: resp.StatusCode,
				Endpoint:   sanitizeEndpointLabel(cur),
				Message:    "短链响应缺少跳转地址",
			}
		}

		nextURL, pErr := cur.Parse(loc)
		if pErr != nil || nextURL.Host == "" {
			return nil, localRouteMatch{}, &site.Error{
				Site:     site.Douyin,
				Kind:     site.ErrKindBlocked,
				Endpoint: sanitizeEndpointLabel(cur),
				Message:  "短链重定向地址格式无效",
			}
		}
		if err := c.validateResolveHopURL(nextURL, true); err != nil {
			return nil, localRouteMatch{}, err
		}

		match := c.classifyLocalURL(nextURL)
		// Early-stop as soon as target ID or explicit unsupported route is extracted!
		if match.kind != routeShortLink && match.kind != routeUnknown {
			return nextURL, match, nil
		}
		if canonicalHopKey(nextURL) == canonicalHopKey(cur) {
			return nil, localRouteMatch{}, &site.Error{
				Site:     site.Douyin,
				Kind:     site.ErrKindBlocked,
				Endpoint: sanitizeEndpointLabel(cur),
				Message:  "短链重定向陷入原地循环",
			}
		}
		cur = nextURL
	}

	return nil, localRouteMatch{}, &site.Error{
		Site:    site.Douyin,
		Kind:    site.ErrKindBlocked,
		Message: "短链跳转超过 5 跳安全上限",
	}
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
	case routeVideo:
		return site.ResolveTarget{
			Site:         site.Douyin,
			Kind:         "video",
			ID:           match.id,
			Page:         1,
			CanonicalURL: "https://www.douyin.com/video/" + match.id,
		}, nil

	case routeNote:
		return site.ResolveTarget{
			Site:         site.Douyin,
			Kind:         "note",
			ID:           match.id,
			Page:         1,
			CanonicalURL: "https://www.douyin.com/note/" + match.id,
		}, nil

	case routeUser:
		return site.ResolveTarget{
			Site:         site.Douyin,
			Kind:         "user",
			ID:           match.secUID,
			SecUID:       match.secUID,
			CanonicalURL: "https://www.douyin.com/user/" + match.secUID,
		}, nil

	case routeUserSelf:
		return c.resolveUserSelf(ctx)

	case routeLiveWebRID:
		return c.resolveLiveByWebRID(ctx, match.webRID)

	case routeLiveReflow:
		return c.resolveLiveByReflow(ctx, match.roomID, match.secUID)

	default:
		return site.ResolveTarget{}, &site.Error{
			Site:        site.Douyin,
			Kind:        site.ErrKindBadRequest,
			Unsupported: true,
			Message:     "暂不支持这种抖音链接",
		}
	}
}

func (c *Client) resolveUserSelf(ctx context.Context) (site.ResolveTarget, error) {
	if err := c.RequireAuth("resolve"); err != nil {
		return site.ResolveTarget{}, err
	}
	if c.cred != nil {
		if sec := strings.TrimSpace(c.cred.Status().SecUID); reValidSecUID.MatchString(sec) {
			return site.ResolveTarget{
				Site:         site.Douyin,
				Kind:         "user",
				ID:           sec,
				SecUID:       sec,
				CanonicalURL: "https://www.douyin.com/user/" + sec,
			}, nil
		}
	}
	prof, err := c.GetSelfProfile(ctx)
	if err != nil {
		return site.ResolveTarget{}, err
	}
	if c.cred != nil {
		_, _ = c.cred.UpdateDouyinProfile(credential.DouyinProfile{
			UID:      prof.UID,
			SecUID:   prof.SecUID,
			UniqueID: prof.UniqueID,
			Uname:    prof.Nickname,
			Face:     prof.AvatarURL,
		})
	}
	return site.ResolveTarget{
		Site:         site.Douyin,
		Kind:         "user",
		ID:           prof.SecUID,
		SecUID:       prof.SecUID,
		CanonicalURL: "https://www.douyin.com/user/" + prof.SecUID,
	}, nil
}

func (c *Client) canCallEndpoint(ep string) bool {
	if !c.allowLocalhost {
		return true
	}
	u, err := url.Parse(ep)
	return err == nil && isLoopbackHost(strings.ToLower(u.Hostname()))
}

func (c *Client) resolveLiveByWebRID(ctx context.Context, webRID string) (site.ResolveTarget, error) {
	if !c.canCallEndpoint(c.endpoints.LiveWebEnter) {
		return site.ResolveTarget{
			Site:         site.Douyin,
			Kind:         "live",
			ID:           webRID,
			WebRID:       webRID,
			CanonicalURL: "https://live.douyin.com/" + webRID,
		}, nil
	}

	room, err := c.EnterLiveRoom(ctx, webRID, "")
	if err != nil {
		return site.ResolveTarget{}, err
	}
	canonicalWebRID := room.PreferredWebRID()
	if canonicalWebRID == "" {
		canonicalWebRID = webRID
	}
	id := canonicalWebRID
	if room.RoomID != "" {
		id = room.RoomID
	}
	return site.ResolveTarget{
		Site:         site.Douyin,
		Kind:         "live",
		ID:           id,
		SecUID:       room.OwnerSecUID,
		WebRID:       canonicalWebRID,
		RoomID:       room.RoomID,
		CanonicalURL: "https://live.douyin.com/" + canonicalWebRID,
	}, nil
}

func (c *Client) resolveLiveByReflow(ctx context.Context, roomID, secUID string) (site.ResolveTarget, error) {
	if !c.canCallEndpoint(c.endpoints.LiveReflowInfo) {
		return site.ResolveTarget{
			Site:         site.Douyin,
			Kind:         "live",
			ID:           roomID,
			SecUID:       secUID,
			RoomID:       roomID,
			CanonicalURL: "https://live.douyin.com/",
		}, nil
	}

	room, err := c.ReflowRoomInfo(ctx, roomID, secUID)
	if err != nil {
		return site.ResolveTarget{}, err
	}
	canonicalWebRID := room.PreferredWebRID()
	canonicalURL := "https://live.douyin.com/"
	if canonicalWebRID != "" {
		canonicalURL += canonicalWebRID
	}
	return site.ResolveTarget{
		Site:         site.Douyin,
		Kind:         "live",
		ID:           room.RoomID,
		SecUID:       room.OwnerSecUID,
		WebRID:       canonicalWebRID,
		RoomID:       room.RoomID,
		CanonicalURL: canonicalURL,
	}, nil
}
