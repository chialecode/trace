package weibo

import (
	"html"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/chialecode/trace/internal/site"
)

var (
	reAnchorTag       = regexp.MustCompile(`(?is)<a\b([^>]*)>(.*?)</a>`)
	reAnchorHrefAttr  = regexp.MustCompile(`(?is)\bhref\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>"']+))`)
	reAnchorDataURL   = regexp.MustCompile(`(?is)\bdata-url\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>"']+))`)
	rePostPathRel     = regexp.MustCompile(`^/\d{5,20}/[A-Za-z0-9]{6,24}/?$`)
	reSafeContainerID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{5,63}$`)
	reSafeWeiboUID    = regexp.MustCompile(`^[1-9][0-9]{4,18}$`)
)

type parsedHTMLAnchor struct {
	Label   string
	Href    string
	DataURL string
}

type shortURLResolution struct {
	ShortURL        string
	Label           string
	TargetURL       string
	MediaAttachment bool
	Consumed        bool
}

type directAnchorCandidate struct {
	Link     site.TextLink
	ShortKey string
}

type positionedLink struct {
	Start int
	End   int
	Link  site.TextLink
}

// RewriteSafeSinaWeiboAppURL converts strictly allowlisted Weibo mobile deep links
// into canonical desktop HTTPS URLs:
//   - sinaweibo://pageinfo?containerid=<cid>... -> https://weibo.com/p/<cid>
//   - sinaweibo://userinfo?uid=<5..19 digits>   -> https://weibo.com/u/<uid>
//
// Any other sinaweibo:// path, non-sinaweibo scheme, userinfo credentials,
// control characters, whitespace, backslashes, or signed/credential query
// parameters are rejected with "".
func RewriteSafeSinaWeiboAppURL(raw string) string {
	s := strings.TrimSpace(html.UnescapeString(strings.TrimSpace(raw)))
	if s == "" || !strings.HasPrefix(strings.ToLower(s), "sinaweibo://") {
		return ""
	}
	for _, r := range s {
		if r <= 0x1f || r == 0x7f || unicode.IsSpace(r) || r == '\\' {
			return ""
		}
	}
	if HasSignedQueryMarker(s) || hasUnsafeWeiboCredentialQueryMarker(s) {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil || !strings.EqualFold(u.Scheme, "sinaweibo") || u.User != nil || u.Port() != "" {
		return ""
	}
	if u.Path != "" && u.Path != "/" {
		return ""
	}
	q := u.Query()
	for key, vals := range q {
		if isUnsafeWeiboCredentialQueryKey(key) {
			return ""
		}
		for _, v := range vals {
			if HasSignedQueryMarker(v) || hasUnsafeWeiboCredentialQueryMarker(v) {
				return ""
			}
		}
	}
	switch strings.ToLower(u.Host) {
	case "pageinfo":
		cids := q["containerid"]
		if len(cids) != 1 {
			return ""
		}
		cid := strings.TrimSpace(cids[0])
		if !reSafeContainerID.MatchString(cid) || HasSignedQueryMarker(cid) {
			return ""
		}
		return "https://weibo.com/p/" + cid
	case "userinfo":
		uids := q["uid"]
		if len(uids) != 1 {
			return ""
		}
		uid := strings.TrimSpace(uids[0])
		if !reSafeWeiboUID.MatchString(uid) {
			return ""
		}
		return "https://weibo.com/u/" + uid
	default:
		return ""
	}
}

func isUnsafeWeiboCredentialQueryKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "ssig", "expires", "kid",
		"sub", "subp", "wbpsess", "xsrf-token", "x-xsrf-token",
		"gsid", "scf", "alf", "alc", "suhb", "pc_token", "ssologinstate", "_t_wm",
		"auth_token", "token", "access_token", "sessionid", "sessdata",
		"bili_jct", "cookie", "password", "passwd", "secret":
		return true
	default:
		return false
	}
}

func hasUnsafeWeiboCredentialQueryMarker(raw string) bool {
	lower := strings.ToLower(strings.TrimSpace(raw))
	if lower == "" {
		return false
	}
	for _, marker := range []string{
		"sub=", "sub%3d", "subp=", "subp%3d",
		"wbpsess=", "wbpsess%3d", "xsrf-token=", "xsrf-token%3d",
		"gsid=", "gsid%3d", "scf=", "scf%3d", "alf=", "alf%3d",
		"suhb=", "suhb%3d", "pc_token=", "pc_token%3d",
		"ssologinstate=", "ssologinstate%3d",
		"auth_token=", "auth_token%3d", "access_token=", "access_token%3d",
		"sessionid=", "sessionid%3d", "sessdata=", "sessdata%3d",
	} {
		idx := strings.Index(lower, marker)
		for idx >= 0 {
			if idx == 0 || isQueryBoundaryByte(lower[idx-1]) {
				return true
			}
			next := strings.Index(lower[idx+len(marker):], marker)
			if next < 0 {
				break
			}
			idx += len(marker) + next
		}
	}
	return false
}

func isQueryBoundaryByte(b byte) bool {
	switch b {
	case '?', '&', ';', '#', '/', '%':
		return true
	default:
		return false
	}
}

// NormalizeWeiboJumpURL resolves a Weibo HTML href or url_struct URL into a safe
// external HTTP(S) jump URL, converting allowlisted sinaweibo:// app links
// (pageinfo?containerid=... and userinfo?uid=...), unwrapping sinaurl redirects,
// and resolving protocol-less or root-relative Weibo links while rejecting other
// non-HTTP(S) schemes, userinfo, control characters, media CDN assets, and signed
// media tokens.
func NormalizeWeiboJumpURL(rawHref string, allowLocalhost bool) string {
	s := strings.TrimSpace(html.UnescapeString(strings.TrimSpace(rawHref)))
	if s == "" {
		return ""
	}
	for _, r := range s {
		if r <= 0x1f || r == 0x7f || unicode.IsSpace(r) || r == '\\' {
			return ""
		}
	}

	if strings.HasPrefix(strings.ToLower(s), "sinaweibo://") {
		return RewriteSafeSinaWeiboAppURL(s)
	}

	if strings.HasPrefix(s, "//") {
		s = "https:" + s
	} else if strings.HasPrefix(s, "/") {
		if !isAllowedWeiboRelativePath(s) {
			return ""
		}
		s = "https://weibo.com" + s
	}

	// Unwrap up to 2 levels of Weibo redirect wrappers (e.g. weibo.cn/sinaurl?u=...).
	for hop := 0; hop < 2; hop++ {
		if strings.HasPrefix(strings.ToLower(s), "sinaweibo://") {
			return RewriteSafeSinaWeiboAppURL(s)
		}
		u, err := url.Parse(s)
		if err != nil || u.Host == "" || u.User != nil {
			return ""
		}
		if !strings.EqualFold(u.Scheme, "https") && !strings.EqualFold(u.Scheme, "http") {
			return ""
		}
		host := strings.ToLower(u.Hostname())
		if isSinaURLRedirect(host, u.Path) {
			inner := extractSinaURLTarget(u.Query())
			if inner == "" {
				return ""
			}
			inner = strings.TrimSpace(html.UnescapeString(inner))
			for _, r := range inner {
				if r <= 0x1f || r == 0x7f || unicode.IsSpace(r) || r == '\\' {
					return ""
				}
			}
			if strings.HasPrefix(inner, "//") {
				inner = "https:" + inner
			}
			s = inner
			continue
		}
		break
	}

	if strings.HasPrefix(strings.ToLower(s), "sinaweibo://") {
		return RewriteSafeSinaWeiboAppURL(s)
	}

	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.User != nil {
		return ""
	}
	if !strings.EqualFold(u.Scheme, "https") && !strings.EqualFold(u.Scheme, "http") {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if strings.EqualFold(u.Scheme, "http") && (!allowLocalhost || !site.IsLoopbackHost(host)) {
		if shouldUpgradeWeiboHostHTTPS(host) {
			u.Scheme = "https"
		}
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Fragment = ""

	if host == "video.weibo.com" {
		if strings.EqualFold(u.Path, "/show") {
			fid := strings.TrimSpace(u.Query().Get("fid"))
			if reVideoOID.MatchString(fid) && !HasSignedQueryMarker(fid) {
				return "https://weibo.com/tv/show/" + fid
			}
		}
		return ""
	}
	if isWeiboAssetCDNHost(host) {
		return ""
	}
	finalStr := u.String()
	if HasSignedQueryMarker(finalStr) {
		return ""
	}
	return finalStr
}

func isAllowedWeiboRelativePath(p string) bool {
	if strings.HasPrefix(p, "//") || p == "/" {
		return false
	}
	lower := strings.ToLower(p)
	for _, prefix := range []string{
		"/u/", "/n/", "/p/", "/detail/", "/status/", "/statuses/",
		"/tv/show/", "/ttarticle/", "/k/", "/weibo?", "/profile/",
	} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	pathOnly := p
	if q := strings.IndexByte(pathOnly, '?'); q >= 0 {
		pathOnly = pathOnly[:q]
	}
	return rePostPathRel.MatchString(pathOnly)
}

func isSinaURLRedirect(host, pathStr string) bool {
	switch host {
	case "weibo.cn", "m.weibo.cn", "weibo.com", "www.weibo.com", "t.cn":
	default:
		return false
	}
	lp := strings.ToLower(strings.TrimRight(pathStr, "/"))
	return lp == "/sinaurl" || lp == "/sinaurl/u"
}

func extractSinaURLTarget(q url.Values) string {
	for _, key := range []string{"u", "toasturl", "url", "target"} {
		if v := strings.TrimSpace(q.Get(key)); v != "" {
			return v
		}
	}
	return ""
}

func shouldUpgradeWeiboHostHTTPS(host string) bool {
	if host == "t.cn" || host == "weibo.cn" || strings.HasSuffix(host, ".weibo.cn") ||
		host == "weibo.com" || strings.HasSuffix(host, ".weibo.com") {
		return true
	}
	return false
}

func isWeiboAssetCDNHost(host string) bool {
	return host == "weibocdn.com" || strings.HasSuffix(host, ".weibocdn.com") ||
		host == "sinaimg.cn" || strings.HasSuffix(host, ".sinaimg.cn")
}

func isWeiboMediaOrSignedStreamURL(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return false
	}
	if HasSignedQueryMarker(trimmed) {
		return true
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if isWeiboAssetCDNHost(host) {
		return true
	}
	if host == "video.weibo.com" && strings.EqualFold(u.Path, "/media/play") {
		return true
	}
	return false
}

func cleanWeiboLinkLabel(raw string) string {
	s := strings.TrimSpace(html.UnescapeString(raw))
	if s == "" {
		return ""
	}
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\u200b', '\u200c', '\u200d', '\ufeff':
			return -1
		case '\r', '\n', '\t':
			return ' '
		default:
			return r
		}
	}, s)
	fields := strings.Fields(s)
	s = strings.Join(fields, " ")
	if utf8.RuneCountInString(s) > 120 {
		runes := []rune(s)
		s = string(runes[:120]) + "…"
	}
	return s
}

func isExpandControlAnchorLabel(label string) bool {
	switch strings.TrimSpace(label) {
	case "", "全文", "...全文", "…全文", "展开", "收起":
		return true
	default:
		return false
	}
}

func extractHTMLAnchors(rawHTML string) []parsedHTMLAnchor {
	if strings.TrimSpace(rawHTML) == "" || !strings.Contains(rawHTML, "<") {
		return nil
	}
	matches := reAnchorTag.FindAllStringSubmatch(rawHTML, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]parsedHTMLAnchor, 0, len(matches))
	for _, m := range matches {
		attrs := m[1]
		inner := m[2]
		href := firstAttrMatch(reAnchorHrefAttr.FindStringSubmatch(attrs))
		dataURL := firstAttrMatch(reAnchorDataURL.FindStringSubmatch(attrs))
		if href == "" && dataURL == "" {
			continue
		}
		innerPlain := reHTMLTag.ReplaceAllString(inner, "")
		innerPlain = strings.ReplaceAll(innerPlain, "&nbsp;", " ")
		label := cleanWeiboLinkLabel(innerPlain)
		if isExpandControlAnchorLabel(label) {
			continue
		}
		out = append(out, parsedHTMLAnchor{
			Label:   label,
			Href:    strings.TrimSpace(href),
			DataURL: strings.TrimSpace(dataURL),
		})
	}
	return out
}

func firstAttrMatch(m []string) string {
	for i := 1; i < len(m); i++ {
		if m[i] != "" {
			return m[i]
		}
	}
	return ""
}

func normalizeShortURLKey(raw string) string {
	s := strings.TrimSpace(raw)
	if strings.HasPrefix(strings.ToLower(s), "https://t.cn/") {
		return "http://t.cn/" + s[len("https://t.cn/"):]
	}
	return s
}

// extractWeiboTextAndLinks builds clean human-readable post text (replacing raw t.cn
// short links and Weibo super-topic markup with readable labels rather than raw URLs)
// alongside an ordered list of safe HTTP(S) hyperlinks from HTML <a href> and url_struct.
func extractWeiboTextAndLinks(rawHTML, rawText string, urlMappings []URLStructItem, allowLocalhost bool) (string, []site.TextLink) {
	shortMap := make(map[string]*shortURLResolution, len(urlMappings))
	shortOrder := make([]string, 0, len(urlMappings))

	registerShort := func(shortU string) *shortURLResolution {
		key := normalizeShortURLKey(shortU)
		if key == "" {
			return nil
		}
		if existing, ok := shortMap[key]; ok {
			return existing
		}
		res := &shortURLResolution{ShortURL: shortU}
		shortMap[key] = res
		shortOrder = append(shortOrder, key)
		return res
	}

	var urlStructAnchors []directAnchorCandidate

	for _, item := range urlMappings {
		shortU := strings.TrimSpace(item.ShortURL)
		label := normalizeSuperTopicLabel(cleanWeiboLinkLabel(item.URLTitle))
		longMedia := isWeiboMediaOrSignedStreamURL(item.LongURL)
		oriMedia := isWeiboMediaOrSignedStreamURL(item.OriURL)

		var target string
		if !longMedia {
			target = NormalizeWeiboJumpURL(item.LongURL, allowLocalhost)
		}
		if (target == "" || isWeiboShortTCNURL(target)) && !oriMedia {
			if oriTarget := NormalizeWeiboJumpURL(item.OriURL, allowLocalhost); oriTarget != "" {
				target = oriTarget
			}
		}
		isMedia := (longMedia || oriMedia) && target == ""
		if target == "" && !isMedia && shortU != "" {
			target = NormalizeWeiboJumpURL(shortU, allowLocalhost)
		}
		shortKey := ""
		if shortU != "" {
			shortKey = normalizeShortURLKey(shortU)
			res := registerShort(shortU)
			if res != nil {
				if res.Label == "" && label != "" {
					res.Label = label
				}
				if isMedia {
					res.MediaAttachment = true
					res.TargetURL = ""
				} else if (res.TargetURL == "" || isWeiboShortTCNURL(res.TargetURL)) && target != "" {
					res.TargetURL = target
				}
			}
		}
		if !isMedia && label != "" && target != "" {
			urlStructAnchors = append(urlStructAnchors, directAnchorCandidate{
				Link:     site.TextLink{Label: label, URL: target},
				ShortKey: shortKey,
			})
		}
	}

	anchors := extractHTMLAnchors(rawHTML)
	var directCandidates []directAnchorCandidate
	for _, a := range anchors {
		candShort := ""
		if strings.Contains(strings.ToLower(a.DataURL), "://t.cn/") {
			candShort = a.DataURL
		} else if strings.Contains(strings.ToLower(a.Href), "://t.cn/") {
			candShort = a.Href
		}

		resolvedHref := NormalizeWeiboJumpURL(a.Href, allowLocalhost)
		if (resolvedHref == "" || isWeiboShortTCNURL(resolvedHref)) && a.DataURL != "" && candShort == "" {
			if alt := NormalizeWeiboJumpURL(a.DataURL, allowLocalhost); alt != "" {
				resolvedHref = alt
			}
		}
		anchorLabel := normalizeSuperTopicLabel(a.Label)

		if candShort != "" {
			shortKey := normalizeShortURLKey(candShort)
			res := registerShort(candShort)
			if res != nil {
				if !res.MediaAttachment {
					if (res.TargetURL == "" || isWeiboShortTCNURL(res.TargetURL)) && resolvedHref != "" {
						res.TargetURL = resolvedHref
					}
					if res.TargetURL == "" {
						res.TargetURL = NormalizeWeiboJumpURL(candShort, allowLocalhost)
					}
				}
				if anchorLabel != "" && (res.Label == "" || (res.Label == "网页链接" && anchorLabel != "网页链接" && !strings.HasPrefix(strings.ToLower(anchorLabel), "http"))) {
					res.Label = anchorLabel
				}
				// Also keep as fallback direct anchor in case baseText came from stripHTMLText(rawHTML)
				// or text_raw uses "#名称[超话]#" / "名称超话" instead of the raw t.cn short URL.
				if !res.MediaAttachment && res.TargetURL != "" {
					lbl := res.Label
					if lbl == "" {
						lbl = anchorLabel
					}
					if lbl != "" {
						directCandidates = append(directCandidates, directAnchorCandidate{
							Link:     site.TextLink{Label: lbl, URL: res.TargetURL},
							ShortKey: shortKey,
						})
					}
				}
				continue
			}
		}

		if resolvedHref != "" && anchorLabel != "" {
			directCandidates = append(directCandidates, directAnchorCandidate{
				Link: site.TextLink{
					Label: anchorLabel,
					URL:   resolvedHref,
				},
			})
		}
	}

	baseText := strings.TrimSpace(rawText)
	if baseText == "" && strings.TrimSpace(rawHTML) != "" {
		baseText = stripHTMLText(rawHTML)
	}
	baseText = scrubSignedURLTokensInText(baseText)
	if baseText == "" {
		return "", nil
	}

	// Replace t.cn short URLs left-to-right while tracking exact byte spans of emitted link labels.
	cleanText, positioned := replaceShortURLsWithLabels(baseText, shortMap, shortOrder)

	// Build active direct anchors from unconsumed HTML anchors and unconsumed url_struct entries
	// (e.g. Weibo super-topics where url_struct carries ori_url sinaweibo://pageinfo?containerid=...
	// while text_raw carries "#名称[超话]#" or "名称超话" without a raw t.cn token).
	directAnchors := make([]site.TextLink, 0, len(directCandidates)+len(urlStructAnchors))
	seenDirectKey := make(map[string]bool, len(directCandidates)+len(urlStructAnchors))
	appendDirect := func(cand directAnchorCandidate) {
		if cand.Link.Label == "" || cand.Link.URL == "" {
			return
		}
		if cand.ShortKey != "" {
			if res, ok := shortMap[cand.ShortKey]; ok && res != nil && res.Consumed {
				return
			}
		}
		dedupKey := cand.Link.Label + "\x00" + cand.Link.URL
		if cand.ShortKey != "" {
			dedupKey = cand.ShortKey + "\x00" + dedupKey
		}
		if seenDirectKey[dedupKey] {
			return
		}
		seenDirectKey[dedupKey] = true
		directAnchors = append(directAnchors, cand.Link)
	}
	for _, cand := range directCandidates {
		appendDirect(cand)
	}
	for _, cand := range urlStructAnchors {
		// Skip url_struct fallback if an HTML anchor already registered the exact same (label, url) or shortKey.
		if cand.ShortKey != "" && seenDirectKey[cand.ShortKey+"\x00"+cand.Link.Label+"\x00"+cand.Link.URL] {
			continue
		}
		if seenDirectKey[cand.Link.Label+"\x00"+cand.Link.URL] {
			continue
		}
		appendDirect(cand)
	}

	// Normalize Weibo super-topic markup "#名称[超话]#" -> "名称超话" when matching a super-topic anchor.
	cleanText, positioned = normalizeSuperTopicMarkup(cleanText, positioned, directAnchors)

	// Match remaining HTML anchors (e.g. #话题#, @昵称, or HTML-only 网页链接) sequentially in cleanText.
	searchFrom := 0
	for _, da := range directAnchors {
		if da.Label == "" || da.URL == "" {
			continue
		}
		idx := findUnclaimedLabel(cleanText, da.Label, searchFrom, positioned)
		if idx < 0 && searchFrom > 0 {
			idx = findUnclaimedLabel(cleanText, da.Label, 0, positioned)
		}
		if idx < 0 {
			continue
		}
		end := idx + len(da.Label)
		positioned = append(positioned, positionedLink{
			Start: idx,
			End:   end,
			Link:  da,
		})
		searchFrom = end
	}

	if len(positioned) == 0 {
		return cleanText, nil
	}
	sort.SliceStable(positioned, func(i, j int) bool {
		if positioned[i].Start != positioned[j].Start {
			return positioned[i].Start < positioned[j].Start
		}
		return positioned[i].End > positioned[j].End
	})

	links := make([]site.TextLink, 0, len(positioned))
	lastEnd := -1
	for _, pl := range positioned {
		if pl.Start < lastEnd {
			continue
		}
		if pl.Link.Label != "" && pl.Link.URL != "" {
			links = append(links, pl.Link)
			lastEnd = pl.End
		}
	}
	if len(links) == 0 {
		return cleanText, nil
	}
	return cleanText, links
}

func replaceShortURLsWithLabels(text string, shortMap map[string]*shortURLResolution, shortOrder []string) (string, []positionedLink) {
	if text == "" || len(shortOrder) == 0 {
		return text, nil
	}
	type candToken struct {
		raw string
		res *shortURLResolution
	}
	var tokens []candToken
	for _, key := range shortOrder {
		res := shortMap[key]
		if res == nil {
			continue
		}
		tokens = append(tokens, candToken{raw: key, res: res})
		if strings.HasPrefix(key, "http://t.cn/") {
			httpsVer := "https://t.cn/" + key[len("http://t.cn/"):]
			tokens = append(tokens, candToken{raw: httpsVer, res: res})
		}
	}
	// Prefer longer tokens first so prefix-sharing t.cn codes never partially match.
	sort.SliceStable(tokens, func(i, j int) bool {
		return len(tokens[i].raw) > len(tokens[j].raw)
	})

	var sb strings.Builder
	sb.Grow(len(text))
	var positioned []positionedLink

	pos := 0
	for pos < len(text) {
		bestIdx := -1
		var bestTok candToken
		for _, tok := range tokens {
			if tok.raw == "" {
				continue
			}
			rel := strings.Index(text[pos:], tok.raw)
			if rel < 0 {
				continue
			}
			absIdx := pos + rel
			afterIdx := absIdx + len(tok.raw)
			if afterIdx < len(text) && isShortURLCodeByte(text[afterIdx]) {
				continue
			}
			if bestIdx < 0 || absIdx < bestIdx || (absIdx == bestIdx && len(tok.raw) > len(bestTok.raw)) {
				bestIdx = absIdx
				bestTok = tok
			}
		}
		if bestIdx < 0 {
			sb.WriteString(text[pos:])
			break
		}
		sb.WriteString(text[pos:bestIdx])
		res := bestTok.res
		res.Consumed = true
		switch {
		case res.MediaAttachment:
			if res.Label != "" {
				sb.WriteString(res.Label)
			}
		case res.TargetURL != "":
			lbl := res.Label
			if lbl == "" {
				lbl = "网页链接"
			}
			startOut := sb.Len()
			sb.WriteString(lbl)
			endOut := sb.Len()
			positioned = append(positioned, positionedLink{
				Start: startOut,
				End:   endOut,
				Link: site.TextLink{
					Label: lbl,
					URL:   res.TargetURL,
				},
			})
		default:
			if res.Label != "" {
				sb.WriteString(res.Label)
			} else {
				sb.WriteString(bestTok.raw)
			}
		}
		pos = bestIdx + len(bestTok.raw)
	}
	return strings.TrimSpace(adjustPositionedAfterTrim(sb.String(), &positioned)), positioned
}

func adjustPositionedAfterTrim(raw string, positioned *[]positionedLink) string {
	trimmedLeft := strings.TrimLeftFunc(raw, unicode.IsSpace)
	leftCut := len(raw) - len(trimmedLeft)
	trimmed := strings.TrimRightFunc(trimmedLeft, unicode.IsSpace)
	if leftCut == 0 && len(trimmed) == len(raw) {
		return raw
	}
	kept := (*positioned)[:0]
	for _, p := range *positioned {
		p.Start -= leftCut
		p.End -= leftCut
		if p.Start >= 0 && p.End <= len(trimmed) && p.Start < p.End {
			kept = append(kept, p)
		}
	}
	*positioned = kept
	return trimmed
}

func normalizeSuperTopicMarkup(text string, positioned []positionedLink, anchors []site.TextLink) (string, []positionedLink) {
	if !strings.Contains(text, "[超话]#") || len(anchors) == 0 {
		return text, positioned
	}
	out := text
	for _, a := range anchors {
		if !strings.HasSuffix(a.Label, "超话") || a.URL == "" {
			continue
		}
		topicName := strings.TrimSuffix(a.Label, "超话")
		if topicName == "" {
			continue
		}
		markup := "#" + topicName + "[超话]#"
		for {
			idx := findUnclaimedLabel(out, markup, 0, positioned)
			if idx < 0 {
				break
			}
			delta := len(a.Label) - len(markup)
			out = out[:idx] + a.Label + out[idx+len(markup):]
			for i := range positioned {
				if positioned[i].Start > idx {
					positioned[i].Start += delta
					positioned[i].End += delta
				}
			}
			positioned = append(positioned, positionedLink{
				Start: idx,
				End:   idx + len(a.Label),
				Link:  a,
			})
		}
	}
	return out, positioned
}

func findUnclaimedLabel(text, label string, from int, claimed []positionedLink) int {
	if label == "" || from < 0 || from >= len(text) {
		return -1
	}
	cur := from
	for cur <= len(text)-len(label) {
		rel := strings.Index(text[cur:], label)
		if rel < 0 {
			return -1
		}
		idx := cur + rel
		end := idx + len(label)
		overlaps := false
		for _, c := range claimed {
			if idx < c.End && end > c.Start {
				overlaps = true
				break
			}
		}
		if !overlaps {
			return idx
		}
		cur = idx + 1
	}
	return -1
}

func isWeiboShortTCNURL(raw string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(raw)), "://t.cn/")
}

func normalizeSuperTopicLabel(label string) string {
	s := strings.TrimSpace(label)
	if strings.HasPrefix(s, "#") && strings.HasSuffix(s, "[超话]#") {
		topic := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(s, "#"), "[超话]#"))
		if topic != "" {
			return topic + "超话"
		}
	}
	return s
}

func isShortURLCodeByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// sanitizeSnapshotLinks returns a copy of links filtered through SafePublicWebURL
// so subscription snapshots never persist signed or unsafe URLs.
func sanitizeSnapshotLinks(links []site.TextLink) []site.TextLink {
	if len(links) == 0 {
		return nil
	}
	out := make([]site.TextLink, 0, len(links))
	for _, l := range links {
		lbl := cleanWeiboLinkLabel(l.Label)
		u := SafePublicWebURL(l.URL)
		if lbl == "" || u == "" {
			continue
		}
		out = append(out, site.TextLink{Label: lbl, URL: u})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
