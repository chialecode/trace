package bilibili

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// ResolveTarget describes a normalized Bilibili target parsed from user input.
type ResolveTarget struct {
	Site         string `json:"site"`
	Kind         string `json:"kind"` // "video", "user", "dynamic", "live", "bangumi", "article"
	ID           string `json:"id"`
	AID          int64  `json:"aid,omitempty"`
	Page         int    `json:"page,omitempty"`
	RoomID       int64  `json:"roomId,omitempty"`
	Mid          int64  `json:"mid,omitempty"`
	CanonicalURL string `json:"canonicalUrl"`
	Unsupported  bool   `json:"unsupported,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

var (
	reURL       = regexp.MustCompile(`https?://[^\s"'<>]+`)
	reBV        = regexp.MustCompile(`(?i)\b(BV1[1-9A-NP-Za-km-z]{9})\b`)
	reAV        = regexp.MustCompile(`(?i)\bav([1-9][0-9]{0,18})\b`)
	reUIDPrefix = regexp.MustCompile(`(?i)^(?:uid[:：\s]*)([1-9][0-9]{0,18})$`)
	rePureNum   = regexp.MustCompile(`^([1-9][0-9]{0,18})$`)
	reLivePref  = regexp.MustCompile(`(?i)^(?:live[:：\s]*)([1-9][0-9]{0,18})$`)
)

const (
	bvXorCode  = int64(23442827791579)
	bvMaskCode = int64(2251799813685247)
	bvMaxAid   = int64(1) << 51
	bvAlphabet = "FcwAPNKTMug3GV5Lj7EJnHpWsx4tb8haYeviqBz6rkCy12mUSDQX9RdoZf"
)

// AvToBv converts a numeric Bilibili aid to a BV1 string.
func AvToBv(aid int64) string {
	if aid <= 0 {
		return ""
	}
	bytes := []byte("BV1000000000")
	bvIdx := 11
	tmp := (bvMaxAid | aid) ^ bvXorCode
	for tmp > 0 && bvIdx >= 3 {
		bytes[bvIdx] = bvAlphabet[tmp%58]
		tmp /= 58
		bvIdx--
	}
	bytes[3], bytes[9] = bytes[9], bytes[3]
	bytes[4], bytes[7] = bytes[7], bytes[4]
	return string(bytes)
}

// BvToAv converts a BV1 string to its numeric aid.
func BvToAv(bvid string) (int64, bool) {
	if len(bvid) != 12 || !strings.EqualFold(bvid[:3], "BV1") {
		return 0, false
	}
	b := []byte(bvid)
	b[3], b[9] = b[9], b[3]
	b[4], b[7] = b[7], b[4]
	var tmp int64
	for _, ch := range b[3:] {
		idx := strings.IndexByte(bvAlphabet, ch)
		if idx < 0 {
			return 0, false
		}
		tmp = tmp*58 + int64(idx)
	}
	aid := (tmp & bvMaskCode) ^ bvXorCode
	if aid <= 0 {
		return 0, false
	}
	return aid, true
}

// ResolveLink parses and resolves a user input string (URL, mixed share text, BV/av, UID, dynamic ID, live room)
// into a normalized ResolveTarget.
func (c *Client) ResolveLink(ctx context.Context, rawInput string) (ResolveTarget, error) {
	if err := c.RequireAuth("resolve"); err != nil {
		return ResolveTarget{}, err
	}

	input := strings.TrimSpace(rawInput)
	if input == "" {
		return ResolveTarget{}, &SiteError{
			Kind:    ErrKindBadRequest,
			Message: "请输入哔哩哔哩视频、用户空间、动态或直播链接 / ID",
		}
	}

	// 1. Check if input contains an explicit http(s) URL (including inside mixed share text).
	if m := reURL.FindString(input); m != "" {
		cleanURL := strings.TrimRight(m, ".,;!?，。；！？】)）]")
		return c.resolveURL(ctx, cleanURL, true)
	}

	// 2. Check UID:<mid> prefix.
	if m := reUIDPrefix.FindStringSubmatch(input); len(m) == 2 {
		mid, _ := strconv.ParseInt(m[1], 10, 64)
		return ResolveTarget{
			Site:         "bilibili",
			Kind:         "user",
			ID:           m[1],
			Mid:          mid,
			CanonicalURL: "https://space.bilibili.com/" + m[1],
		}, nil
	}

	// 3. Check live:<room_id> prefix.
	if m := reLivePref.FindStringSubmatch(input); len(m) == 2 {
		roomID, _ := strconv.ParseInt(m[1], 10, 64)
		return c.resolveLiveRoom(ctx, roomID)
	}

	// 4. Check BV ID inside input text.
	if m := reBV.FindStringSubmatch(input); len(m) == 2 {
		bvid := normalizeBVID(m[1])
		aid, _ := BvToAv(bvid)
		return ResolveTarget{
			Site:         "bilibili",
			Kind:         "video",
			ID:           bvid,
			AID:          aid,
			Page:         1,
			CanonicalURL: "https://www.bilibili.com/video/" + bvid,
		}, nil
	}

	// 5. Check av<aid> inside input text.
	if m := reAV.FindStringSubmatch(input); len(m) == 2 {
		aid, err := strconv.ParseInt(m[1], 10, 64)
		if err == nil && aid > 0 {
			bvid := AvToBv(aid)
			return ResolveTarget{
				Site:         "bilibili",
				Kind:         "video",
				ID:           bvid,
				AID:          aid,
				Page:         1,
				CanonicalURL: "https://www.bilibili.com/video/" + bvid,
			}, nil
		}
	}

	// 6. Check pure numeric ID: >= 16 digits is treated as a dynamic ID; shorter digits as UID.
	if m := rePureNum.FindStringSubmatch(input); len(m) == 2 {
		digits := m[1]
		if len(digits) >= 16 {
			return ResolveTarget{
				Site:         "bilibili",
				Kind:         "dynamic",
				ID:           digits,
				CanonicalURL: "https://t.bilibili.com/" + digits,
			}, nil
		}
		mid, _ := strconv.ParseInt(digits, 10, 64)
		return ResolveTarget{
			Site:         "bilibili",
			Kind:         "user",
			ID:           digits,
			Mid:          mid,
			CanonicalURL: "https://space.bilibili.com/" + digits,
		}, nil
	}

	return ResolveTarget{}, &SiteError{
		Kind:    ErrKindBadRequest,
		Message: "无法识别该输入，请输入有效的哔哩哔哩链接、BV/av 号或 UID",
	}
}

func (c *Client) resolveURL(ctx context.Context, rawURL string, allowShortHop bool) (ResolveTarget, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return ResolveTarget{}, &SiteError{
			Kind:    ErrKindBadRequest,
			Message: "无效的链接格式",
		}
	}

	host := strings.ToLower(u.Hostname())
	// Handle b23.tv short link (or localhost shortlink endpoint in unit tests).
	if host == "b23.tv" || host == "www.b23.tv" || (c.allowLocalhost && (host == "127.0.0.1" || host == "localhost") && strings.HasPrefix(u.Path, "/b23/")) {
		if !allowShortHop {
			return ResolveTarget{}, &SiteError{
				Kind:    ErrKindBlocked,
				Message: "短链重定向层数超限（仅允许 1 跳）",
			}
		}
		return c.resolveShortLink(ctx, rawURL)
	}

	if !isBilibiliHost(host) {
		return ResolveTarget{}, &SiteError{
			Kind:    ErrKindBadRequest,
			Message: fmt.Sprintf("不支持非哔哩哔哩域名: %s", host),
		}
	}

	pathParts := splitCleanPath(u.Path)
	page := 1
	if pStr := u.Query().Get("p"); pStr != "" {
		if p, err := strconv.Atoi(pStr); err == nil && p > 0 {
			page = p
		}
	}

	switch {
	case host == "space.bilibili.com":
		if len(pathParts) >= 1 && rePureNum.MatchString(pathParts[0]) {
			midStr := pathParts[0]
			mid, _ := strconv.ParseInt(midStr, 10, 64)
			return ResolveTarget{
				Site:         "bilibili",
				Kind:         "user",
				ID:           midStr,
				Mid:          mid,
				CanonicalURL: "https://space.bilibili.com/" + midStr,
			}, nil
		}

	case host == "t.bilibili.com":
		if len(pathParts) >= 1 && rePureNum.MatchString(pathParts[0]) {
			dynID := pathParts[0]
			return ResolveTarget{
				Site:         "bilibili",
				Kind:         "dynamic",
				ID:           dynID,
				CanonicalURL: "https://t.bilibili.com/" + dynID,
			}, nil
		}

	case host == "live.bilibili.com":
		if len(pathParts) >= 1 {
			first := pathParts[0]
			if first == "blanc" || first == "h5" {
				if len(pathParts) >= 2 {
					first = pathParts[1]
				}
			}
			if rePureNum.MatchString(first) {
				roomOrShortID, _ := strconv.ParseInt(first, 10, 64)
				return c.resolveLiveRoom(ctx, roomOrShortID)
			}
		}
	}

	// www.bilibili.com / m.bilibili.com paths
	if len(pathParts) >= 2 {
		prefix := strings.ToLower(pathParts[0])
		seg := pathParts[1]
		switch prefix {
		case "video":
			if m := reBV.FindStringSubmatch(seg); len(m) == 2 {
				bvid := normalizeBVID(m[1])
				aid, _ := BvToAv(bvid)
				return ResolveTarget{
					Site:         "bilibili",
					Kind:         "video",
					ID:           bvid,
					AID:          aid,
					Page:         page,
					CanonicalURL: "https://www.bilibili.com/video/" + bvid,
				}, nil
			}
			if m := reAV.FindStringSubmatch(seg); len(m) == 2 {
				aid, _ := strconv.ParseInt(m[1], 10, 64)
				bvid := AvToBv(aid)
				return ResolveTarget{
					Site:         "bilibili",
					Kind:         "video",
					ID:           bvid,
					AID:          aid,
					Page:         page,
					CanonicalURL: "https://www.bilibili.com/video/" + bvid,
				}, nil
			}
		case "opus":
			if rePureNum.MatchString(seg) {
				return ResolveTarget{
					Site:         "bilibili",
					Kind:         "dynamic",
					ID:           seg,
					CanonicalURL: "https://t.bilibili.com/" + seg,
				}, nil
			}
		case "space":
			if rePureNum.MatchString(seg) {
				mid, _ := strconv.ParseInt(seg, 10, 64)
				return ResolveTarget{
					Site:         "bilibili",
					Kind:         "user",
					ID:           seg,
					Mid:          mid,
					CanonicalURL: "https://space.bilibili.com/" + seg,
				}, nil
			}
		case "bangumi":
			return ResolveTarget{
				Site:         "bilibili",
				Kind:         "bangumi",
				ID:           seg,
				CanonicalURL: "https://www.bilibili.com/" + strings.Join(pathParts, "/"),
				Unsupported:  true,
				Reason:       "当前版本暂不支持番剧/影视解析，仅支持普通投稿视频、用户、动态与直播",
			}, &SiteError{
				Kind:    ErrKindBadRequest,
				Message: "当前版本暂不支持番剧/影视解析，仅支持普通投稿视频、用户、动态与直播",
			}
		case "read":
			return ResolveTarget{
				Site:         "bilibili",
				Kind:         "article",
				ID:           seg,
				CanonicalURL: "https://www.bilibili.com/" + strings.Join(pathParts, "/"),
				Unsupported:  true,
				Reason:       "当前版本暂不支持专栏文章独立解析",
			}, &SiteError{
				Kind:    ErrKindBadRequest,
				Message: "当前版本暂不支持专栏文章独立解析",
			}
		}
	}

	return ResolveTarget{}, &SiteError{
		Kind:    ErrKindBadRequest,
		Message: "无法从该哔哩哔哩链接提取受支持的目标类型",
	}
}

func (c *Client) resolveShortLink(ctx context.Context, shortURL string) (ResolveTarget, error) {
	targetURL := shortURL
	if c.allowLocalhost {
		if u, err := url.Parse(shortURL); err == nil && (u.Hostname() == "b23.tv" || u.Hostname() == "www.b23.tv") {
			// Redirect b23.tv to test APIBase /b23/<path> when running against local httptest server.
			targetURL = strings.TrimRight(c.endpoints.APIBase, "/") + "/b23/" + strings.TrimLeft(u.Path, "/")
		}
	}

	resp, _, err := c.doAPIRequest(ctx, http.MethodGet, targetURL, nil, "", false)
	if err != nil && resp == nil {
		return ResolveTarget{}, err
	}
	if resp.StatusCode < 300 || resp.StatusCode >= 400 {
		return ResolveTarget{}, &SiteError{
			Kind:       ErrKindBlocked,
			HTTPStatus: resp.StatusCode,
			Endpoint:   shortURL,
			Message:    "短链未返回有效的重定向响应",
		}
	}

	loc := strings.TrimSpace(resp.Header.Get("Location"))
	if loc == "" {
		return ResolveTarget{}, &SiteError{
			Kind:     ErrKindBlocked,
			Endpoint: shortURL,
			Message:  "短链响应缺少 Location 重定向目标",
		}
	}

	locURL, err := url.Parse(loc)
	if err != nil || locURL.Host == "" {
		return ResolveTarget{}, &SiteError{
			Kind:     ErrKindBlocked,
			Endpoint: shortURL,
			Message:  "短链重定向目标格式无效",
		}
	}

	// Enforce HTTPS and *.bilibili.com target host on redirect Location (C02).
	if !strings.EqualFold(locURL.Scheme, "https") {
		return ResolveTarget{}, &SiteError{
			Kind:     ErrKindBlocked,
			Endpoint: shortURL,
			Message:  "已拦截非 HTTPS 短链重定向目标",
		}
	}
	locHost := strings.ToLower(locURL.Hostname())
	if !isBilibiliHost(locHost) {
		return ResolveTarget{}, &SiteError{
			Kind:     ErrKindBlocked,
			Endpoint: shortURL,
			Message:  fmt.Sprintf("已拦截非哔哩哔哩短链重定向域名: %s", locHost),
		}
	}

	return c.resolveURL(ctx, locURL.String(), false)
}

func (c *Client) resolveLiveRoom(ctx context.Context, roomOrShortID int64) (ResolveTarget, error) {
	info, err := c.RoomInit(ctx, roomOrShortID)
	if err != nil {
		return ResolveTarget{}, err
	}
	canonicalID := fmt.Sprintf("live:%d", info.RoomID)
	return ResolveTarget{
		Site:         "bilibili",
		Kind:         "live",
		ID:           canonicalID,
		RoomID:       info.RoomID,
		Mid:          info.UID,
		CanonicalURL: fmt.Sprintf("https://live.bilibili.com/%d", info.RoomID),
	}, nil
}

func isBilibiliHost(host string) bool {
	host = strings.ToLower(host)
	return host == "bilibili.com" || strings.HasSuffix(host, ".bilibili.com")
}

func splitCleanPath(p string) []string {
	raw := strings.Split(strings.Trim(p, "/"), "/")
	out := make([]string, 0, len(raw))
	for _, s := range raw {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func normalizeBVID(s string) string {
	if len(s) == 12 && strings.EqualFold(s[:3], "BV1") {
		return "BV1" + s[3:]
	}
	return s
}
