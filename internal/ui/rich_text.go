package ui

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/chialecode/trace/internal/service"
	"github.com/egoist/mygo/ui"
)

type richSpanKind uint8

const (
	richSpanPlain richSpanKind = iota
	richSpanLink
	richSpanHashtag
)

type richSpan struct {
	Kind  richSpanKind
	Text  string
	URL   string
	Label string
}

type richParagraph struct {
	Spans []richSpan
}

type richTextOptions struct {
	Label             string
	ClampKey          string
	MaxLines          int
	FontSize          float32
	LineHeight        float32
	TextColor         ui.Color
	HighlightHashtags bool
}

// ValidateSafeJumpURL validates an external jump URL strictly at the UI boundary:
// only clean http:// and https:// URLs with a non-empty host, no userinfo,
// whitespace, backslashes, or control characters, and no forbidden media CDN hosts
// or signed/credential query markers are permitted (DEC-073).
func ValidateSafeJumpURL(rawURL string) (string, bool) {
	s := strings.TrimSpace(rawURL)
	if s == "" {
		return "", false
	}
	for _, r := range s {
		if r <= 0x1f || r == 0x7f || unicode.IsSpace(r) || r == '\\' {
			return "", false
		}
	}
	if hasUnsafeJumpSecretMarker(s) {
		return "", false
	}
	u, err := url.Parse(s)
	if err != nil || !u.IsAbs() || u.Host == "" || strings.TrimSpace(u.Hostname()) == "" || u.User != nil {
		return "", false
	}
	if !strings.EqualFold(u.Scheme, "https") && !strings.EqualFold(u.Scheme, "http") {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	if isForbiddenJumpHost(host) {
		return "", false
	}
	u.Scheme = strings.ToLower(u.Scheme)
	return u.String(), true
}

func isForbiddenJumpHost(host string) bool {
	return host == "video.twimg.com" || strings.HasSuffix(host, ".video.twimg.com") ||
		host == "video-s.twimg.com" ||
		host == "pbs.twimg.com" ||
		host == "ton.x.com" ||
		host == "ton.twitter.com" ||
		host == "pscp.tv" || strings.HasSuffix(host, ".pscp.tv") ||
		host == "periscope.tv" || strings.HasSuffix(host, ".periscope.tv") ||
		host == "weibocdn.com" || strings.HasSuffix(host, ".weibocdn.com") ||
		host == "video.weibo.com" ||
		host == "sinaimg.cn" || strings.HasSuffix(host, ".sinaimg.cn")
}

func isJumpMarkerBoundaryByte(b byte) bool {
	switch b {
	case '?', '&', ';', '#', '/', '%', ':', '=', ' ', '\t':
		return true
	default:
		return false
	}
}

func hasUnsafeJumpSecretMarker(raw string) bool {
	lower := strings.ToLower(strings.TrimSpace(raw))
	if lower == "" {
		return false
	}
	if strings.Contains(lower, "authorization: bearer") ||
		strings.Contains(lower, "authorization:bearer") ||
		strings.Contains(lower, "authorization%3a") {
		return true
	}
	for _, marker := range []string{
		"x-amz-signature=", "x-amz-signature%3d",
		"x-amz-credential=", "x-amz-credential%3d",
		"x-amz-security-token=", "x-amz-security-token%3d",
		"key-pair-id=", "key-pair-id%3d",
		"signature=", "signature%3d",
		"sig=", "sig%3d",
		"ssig=", "ssig%3d",
		"expires=", "expires%3d",
		"x-expires=", "x-expires%3d",
		"x-signature=", "x-signature%3d",
		"policy=", "policy%3d",
		"token=", "token%3d",
		"access_token=", "access_token%3d",
		"auth_token=", "auth_token%3d",
		"ct0=", "ct0%3d",
		"kdt=", "kdt%3d",
		"twid=", "twid%3d",
		"guest_id=", "guest_id%3d",
		"x-csrf-token=", "x-csrf-token%3d", "x-csrf-token:", "x-csrf-token%3a",
		"x-client-transaction-id=", "x-client-transaction-id%3d", "x-client-transaction-id:", "x-client-transaction-id%3a",
		"sessdata=", "sessdata%3d",
		"bili_jct=", "bili_jct%3d",
		"ac_time_value=", "ac_time_value%3d",
		"sessionid=", "sessionid%3d",
		"a_bogus=", "x-bogus=", "w_rid=", "upsig=",
		"sub=", "sub%3d",
		"subp=", "subp%3d",
		"wbpsess=", "wbpsess%3d",
		"xsrf-token=", "xsrf-token%3d",
		"x-xsrf-token=", "x-xsrf-token%3d",
		"scf=", "alf=", "alc=", "ssologinstate=", "suhb=", "pc_token=", "_t_wm=",
	} {
		idx := strings.Index(lower, marker)
		for idx >= 0 {
			if idx == 0 || isJumpMarkerBoundaryByte(lower[idx-1]) {
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

// openExternalURL opens a validated HTTP(S) hyperlink via the backend Desktop
// implementation (recording in MockDesktop during headless UI tests without
// launching a real browser). Unsafe schemes are rejected before reaching Desktop.
func (a *App) openExternalURL(rawURL string) {
	cleanURL, ok := ValidateSafeJumpURL(rawURL)
	if !ok {
		a.showError(errors.New("仅支持打开 http 或 https 网页链接"))
		return
	}
	if err := a.backend.Desktop().OpenExternalURL(cleanURL); err != nil {
		a.showError(err)
	}
}

// FormatConciseLinkLabel builds a concise, human-readable label for a bare
// HTTP(S) URL so post bodies never sprawl raw URL addresses across the layout.
func FormatConciseLinkLabel(rawURL string) string {
	cleanURL, ok := ValidateSafeJumpURL(rawURL)
	if !ok {
		return "网页链接"
	}
	u, err := url.Parse(cleanURL)
	if err != nil || u.Host == "" {
		return "网页链接"
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	switch host {
	case "t.cn", "t.co":
		return "网页链接"
	case "s.weibo.com":
		if q := strings.TrimSpace(u.Query().Get("q")); strings.HasPrefix(q, "#") && strings.HasSuffix(q, "#") && utf8.RuneCountInString(q) <= 32 {
			return q
		}
	case "card.weibo.com":
		if strings.HasPrefix(strings.ToLower(u.Path), "/article/") {
			return "头条文章"
		}
	case "weibo.com", "m.weibo.cn":
		lp := strings.ToLower(u.Path)
		switch {
		case strings.HasPrefix(lp, "/ttarticle/"):
			return "头条文章"
		case strings.HasPrefix(lp, "/tv/show/"):
			return "微博视频"
		case strings.HasPrefix(lp, "/p/100808"):
			return "微博超话"
		}
	}

	cleanPath := strings.TrimRight(u.Path, "/")
	if decoded, dErr := url.PathUnescape(cleanPath); dErr == nil && utf8.ValidString(decoded) && !hasControlOrSpace(decoded) {
		cleanPath = decoded
	}
	if cleanPath == "" {
		return host
	}
	cand := host + cleanPath
	if utf8.RuneCountInString(cand) > 30 {
		runes := []rune(cand)
		return string(runes[:29]) + "…"
	}
	return cand
}

func hasControlOrSpace(s string) bool {
	for _, r := range s {
		if r <= 0x1f || r == 0x7f || unicode.IsSpace(r) {
			return true
		}
	}
	return false
}

// parseRichTextParagraphs tokenizes multi-paragraph post text into plain, hashtag,
// and clickable hyperlink spans, matching:
//  1. Structured links (e.g. Weibo <a href> / url_struct labels, X display_url entities)
//  2. Legacy cached "标题 (https://...)" / "标题（https://...）" patterns
//  3. Inline http:// and https:// URLs across all sites and already-cached items
func parseRichTextParagraphs(text string, links []service.TextLinkDTO, highlightHashtags bool) ([]richParagraph, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil, false
	}

	// Sanitize incoming structured links once and build sequential label list
	// plus both exact and normalized URL -> preferred display label lookup
	// (handling scheme/host case, percent-encoding, and cached fragment stripping).
	validLinks := make([]service.TextLinkDTO, 0, len(links))
	urlToLabel := make(map[string]string, len(links)*3)
	recordURLLabel := func(rawU, lbl string) {
		if rawU == "" || lbl == "" {
			return
		}
		if _, exists := urlToLabel[rawU]; !exists {
			urlToLabel[rawU] = lbl
		}
		if normFull := canonicalJumpURLKey(rawU, false); normFull != "" {
			if _, exists := urlToLabel[normFull]; !exists {
				urlToLabel[normFull] = lbl
			}
		}
		if normBase := canonicalJumpURLKey(rawU, true); normBase != "" {
			if _, exists := urlToLabel[normBase]; !exists {
				urlToLabel[normBase] = lbl
			}
		}
	}
	for _, l := range links {
		cleanU, ok := ValidateSafeJumpURL(l.URL)
		if !ok {
			continue
		}
		lbl := strings.TrimSpace(l.Label)
		if lbl == "" {
			lbl = FormatConciseLinkLabel(cleanU)
		}
		validLinks = append(validLinks, service.TextLinkDTO{Label: lbl, URL: cleanU})
		recordURLLabel(cleanU, lbl)
	}

	// Pre-compute whether each structured link's URL or label appears literally in
	// trimmed text so legacy cached items containing raw t.co links can safely pair
	// with unmatched expanded links without stealing links already present elsewhere.
	lowerTrimmed := strings.ToLower(trimmed)
	explicitlyPresent := make([]bool, len(validLinks))
	for i, vl := range validLinks {
		if strings.Contains(lowerTrimmed, strings.ToLower(vl.URL)) {
			explicitlyPresent[i] = true
			continue
		}
		if vl.Label != "" && !strings.EqualFold(vl.Label, "网页链接") && strings.Contains(lowerTrimmed, strings.ToLower(vl.Label)) {
			explicitlyPresent[i] = true
		}
	}

	usedLinks := make([]bool, len(validLinks))
	rawLines := strings.Split(trimmed, "\n")
	paragraphs := make([]richParagraph, 0, len(rawLines))
	hasRich := false

	for _, rawLine := range rawLines {
		line := strings.TrimRight(rawLine, "\r")
		if strings.TrimSpace(line) == "" {
			paragraphs = append(paragraphs, richParagraph{})
			continue
		}
		spans := tokenizeRichLine(line, validLinks, usedLinks, explicitlyPresent, urlToLabel, highlightHashtags)
		for _, sp := range spans {
			if sp.Kind != richSpanPlain {
				hasRich = true
				break
			}
		}
		paragraphs = append(paragraphs, richParagraph{Spans: spans})
	}
	return paragraphs, hasRich
}

type lineLinkMatch struct {
	Start    int
	End      int
	Label    string
	URL      string
	LinkSlot int // index in validLinks if matched from structured label, else -1
}

func tokenizeRichLine(
	line string,
	validLinks []service.TextLinkDTO,
	usedLinks []bool,
	explicitlyPresent []bool,
	urlToLabel map[string]string,
	highlightHashtags bool,
) []richSpan {
	var matches []lineLinkMatch

	// 1. Scan inline http://, https://, and allowlisted legacy sinaweibo:// URLs
	// (including legacy "Label (https://...)" / "Label (sinaweibo://pageinfo?containerid=...)" wrappers).
	pos := 0
	for pos < len(line) {
		relIdx, isSinaWeibo := indexInlineLinkPrefix(line[pos:])
		if relIdx < 0 {
			break
		}
		urlStart := pos + relIdx
		var rawEnd int
		if !isSinaWeibo {
			rawEnd = matchStructuredURLPrefix(line, urlStart, validLinks)
		}
		if rawEnd <= urlStart {
			rawEnd = scanInlineURLEnd(line, urlStart, isSinaWeibo)
		}
		candURL, cleanEnd := trimTrailingURLPunctuation(line, urlStart, rawEnd)
		resolvedCand := candURL
		if isSinaWeibo {
			resolvedCand = service.NormalizeLegacyWeiboAppURL(candURL)
		}
		cleanURL, ok := ValidateSafeJumpURL(resolvedCand)
		if !ok {
			if isSinaWeibo {
				pos = urlStart + len("sinaweibo://")
			} else {
				pos = urlStart + 4
			}
			continue
		}

		matchStart := urlStart
		matchEnd := cleanEnd
		label := ""

		// First try matching against structured validLinks (exact or normalized URL,
		// including cached items where item.Links stripped #fragment while text kept it).
		matchedSlot := -1
		for i, vl := range validLinks {
			if !usedLinks[i] && urlsMatchForRichLink(vl.URL, cleanURL) {
				matchedSlot = i
				label = vl.Label
				break
			}
		}

		// Legacy cached X item fallback: when text still contains a raw t.co short link
		// while item.Links holds the expanded destination not literally present in text,
		// pair in order so clicking opens the expanded safe URL with its display label.
		if matchedSlot < 0 && !isSinaWeibo && isTCoJumpURL(cleanURL) {
			for i, vl := range validLinks {
				if usedLinks[i] || isTCoJumpURL(vl.URL) {
					continue
				}
				if i < len(explicitlyPresent) && explicitlyPresent[i] {
					continue
				}
				matchedSlot = i
				cleanURL = vl.URL
				label = vl.Label
				break
			}
		}

		mappedLabel := lookupMappedURLLabel(urlToLabel, cleanURL)

		// Recognize legacy pre-DEC-073 "标题 (https://...)" / "标题 (sinaweibo://...)" wrappers
		// ONLY for sinaweibo:// or when no structured link matched this URL and the preceding
		// label looks like a legacy anchor label (never swallowing English prose before "(https://...)").
		if isSinaWeibo || (matchedSlot < 0 && mappedLabel == "") {
			if legStart, legEnd, legLabel, legOK := matchLegacyParenthesizedLabel(line, urlStart, cleanEnd, isSinaWeibo, cleanURL); legOK {
				matchStart = legStart
				matchEnd = legEnd
				label = legLabel
			}
		}

		if label == "" {
			if mappedLabel != "" {
				label = mappedLabel
			} else {
				label = FormatConciseLinkLabel(cleanURL)
			}
		}
		if matchedSlot >= 0 {
			usedLinks[matchedSlot] = true
		}
		matches = append(matches, lineLinkMatch{
			Start:    matchStart,
			End:      matchEnd,
			Label:    label,
			URL:      cleanURL,
			LinkSlot: matchedSlot,
		})
		pos = matchEnd
	}

	// 2. Match remaining structured links (e.g. Weibo "网页链接", article title, "#话题#", "@昵称",
	// or X display_url with optional case/www./ellipsis differences) sequentially left-to-right.
	searchFrom := 0
	for i, vl := range validLinks {
		if usedLinks[i] || vl.Label == "" {
			continue
		}
		idx, spanLen := findUnclaimedLineSpan(line, vl.Label, searchFrom, matches)
		if idx < 0 && searchFrom > 0 {
			idx, spanLen = findUnclaimedLineSpan(line, vl.Label, 0, matches)
		}
		if idx < 0 || spanLen <= 0 {
			continue
		}
		usedLinks[i] = true
		end := idx + spanLen
		matches = append(matches, lineLinkMatch{
			Start:    idx,
			End:      end,
			Label:    vl.Label,
			URL:      vl.URL,
			LinkSlot: i,
		})
		searchFrom = end
	}

	// 3. Detect unambiguous bare web URLs without http(s):// (e.g. historical cached X text
	// containing "github.com/org/repo" or "www.example.com/docs" when item.Links is empty).
	matches = appendBareDomainMatches(line, matches)

	if len(matches) == 0 {
		return buildPlainOrHashtagSpans(line, highlightHashtags)
	}

	// Sort matches by byte offset.
	for i := 1; i < len(matches); i++ {
		cur := matches[i]
		j := i - 1
		for j >= 0 && matches[j].Start > cur.Start {
			matches[j+1] = matches[j]
			j--
		}
		matches[j+1] = cur
	}

	var out []richSpan
	cursor := 0
	for _, m := range matches {
		if m.Start < cursor {
			continue
		}
		if m.Start > cursor {
			out = append(out, buildPlainOrHashtagSpans(line[cursor:m.Start], highlightHashtags)...)
		}
		out = append(out, richSpan{
			Kind:  richSpanLink,
			Text:  m.Label,
			Label: m.Label,
			URL:   m.URL,
		})
		cursor = m.End
	}
	if cursor < len(line) {
		out = append(out, buildPlainOrHashtagSpans(line[cursor:], highlightHashtags)...)
	}
	return out
}

func canonicalJumpURLKey(rawURL string, stripFragment bool) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	hostPart := host
	if port != "" {
		hostPart += ":" + port
	}
	p := strings.TrimRight(u.EscapedPath(), "/")
	key := scheme + "://" + hostPart + p
	if u.RawQuery != "" {
		key += "?" + u.RawQuery
	}
	if !stripFragment && u.Fragment != "" {
		key += "#" + u.Fragment
	}
	return key
}

func urlsMatchForRichLink(a, b string) bool {
	if a == b {
		return true
	}
	if ka, kb := canonicalJumpURLKey(a, false), canonicalJumpURLKey(b, false); ka != "" && ka == kb {
		return true
	}
	ua, errA := url.Parse(a)
	ub, errB := url.Parse(b)
	if errA != nil || errB != nil {
		return false
	}
	// Match when one side dropped #fragment (e.g. item.json SanitizeItemLinks) while the other kept it.
	if (ua.Fragment == "" && ub.Fragment != "") || (ua.Fragment != "" && ub.Fragment == "") {
		ka := canonicalJumpURLKey(a, true)
		kb := canonicalJumpURLKey(b, true)
		return ka != "" && ka == kb
	}
	return false
}

func lookupMappedURLLabel(urlToLabel map[string]string, cleanURL string) string {
	if len(urlToLabel) == 0 || cleanURL == "" {
		return ""
	}
	if lbl, ok := urlToLabel[cleanURL]; ok && lbl != "" {
		return lbl
	}
	if k := canonicalJumpURLKey(cleanURL, false); k != "" {
		if lbl, ok := urlToLabel[k]; ok && lbl != "" {
			return lbl
		}
	}
	if k := canonicalJumpURLKey(cleanURL, true); k != "" {
		if lbl, ok := urlToLabel[k]; ok && lbl != "" {
			return lbl
		}
	}
	return ""
}

func isTCoJumpURL(rawURL string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Hostname(), "t.co")
}

func matchStructuredURLPrefix(line string, urlStart int, validLinks []service.TextLinkDTO) int {
	if len(validLinks) == 0 || urlStart >= len(line) {
		return 0
	}
	rest := line[urlStart:]
	bestEnd := 0
	checkCandidate := func(cand string) {
		if cand == "" || len(cand) > len(rest) {
			return
		}
		if !strings.EqualFold(rest[:len(cand)], cand) {
			return
		}
		end := urlStart + len(cand)
		if end < len(line) && !isStructuredURLMatchEnd(line, end) {
			return
		}
		if end > bestEnd {
			bestEnd = end
		}
	}
	for _, vl := range validLinks {
		checkCandidate(vl.URL)
		if decoded, err := url.PathUnescape(vl.URL); err == nil && decoded != vl.URL && utf8.ValidString(decoded) {
			checkCandidate(decoded)
		}
	}
	return bestEnd
}

func isStructuredURLMatchEnd(line string, end int) bool {
	if end >= len(line) {
		return true
	}
	r, w := utf8.DecodeRuneInString(line[end:])
	if r <= 0x20 || r == 0x7f || unicode.IsSpace(r) {
		return true
	}
	switch r {
	case ')', ']', '}', '"', '\'', '<', '>', '`', '\\':
		return true
	case '.', ',', ';', ':', '!', '?':
		nextPos := end + w
		if nextPos >= len(line) {
			return true
		}
		nr, _ := utf8.DecodeRuneInString(line[nextPos:])
		return nr <= 0x20 || nr == 0x7f || unicode.IsSpace(nr) ||
			nr == ')' || nr == ']' || nr == '}' || nr == '"' || nr == '\'' ||
			isNonASCIIURLDelimiter(nr) || isCJKScriptRune(nr)
	}
	return isNonASCIIURLDelimiter(r) || isCJKScriptRune(r)
}

func buildPlainOrHashtagSpans(segment string, highlightHashtags bool) []richSpan {
	if segment == "" {
		return nil
	}
	if !highlightHashtags {
		return []richSpan{{Kind: richSpanPlain, Text: segment}}
	}
	rawSpans := splitHashtagSpans(segment)
	out := make([]richSpan, 0, len(rawSpans))
	for _, s := range rawSpans {
		if s.Hashtag {
			out = append(out, richSpan{Kind: richSpanHashtag, Text: s.Text})
		} else if s.Text != "" {
			out = append(out, richSpan{Kind: richSpanPlain, Text: s.Text})
		}
	}
	return out
}

func indexInlineLinkPrefix(s string) (idx int, isSinaWeibo bool) {
	best := -1
	bestSina := false
	check := func(pos int, sina bool) {
		if pos >= 0 && (best < 0 || pos < best) {
			best = pos
			bestSina = sina
		}
	}
	for _, pfx := range []string{"https://", "http://", "HTTPS://", "HTTP://"} {
		check(strings.Index(s, pfx), false)
	}
	for _, pfx := range []string{"sinaweibo://", "SINAWEIBO://"} {
		check(strings.Index(s, pfx), true)
	}
	if best >= 0 {
		return best, bestSina
	}
	// Fallback case-insensitive check only if mixed-case scheme exists.
	lower := strings.ToLower(s)
	check(strings.Index(lower, "https://"), false)
	check(strings.Index(lower, "http://"), false)
	check(strings.Index(lower, "sinaweibo://"), true)
	return best, bestSina
}

func scanInlineURLEnd(line string, start int, isSinaWeibo bool) int {
	// Legacy parenthesized URLs like "名称超话 (sinaweibo://pageinfo?containerid=...&extparam=中文)"
	// or "标题 (https://weibo.com/n/中文)" may contain unescaped non-ASCII CJK characters inside parens.
	openASCII := start >= 1 && line[start-1] == '('
	openFull := !openASCII && start >= len("（") && strings.HasSuffix(line[:start], "（")
	if openASCII || openFull {
		i := start
		parenDepth := 0
		for i < len(line) {
			r, w := utf8.DecodeRuneInString(line[i:])
			if r <= 0x20 || r == 0x7f || unicode.IsSpace(r) {
				break
			}
			if r == '"' || r == '\'' || r == '<' || r == '>' || r == '`' || r == '\\' {
				break
			}
			if openASCII {
				if r == '(' {
					parenDepth++
				} else if r == ')' {
					if parenDepth == 0 {
						break
					}
					parenDepth--
				}
			} else if r == '）' {
				break
			}
			i += w
		}
		return i
	}
	return scanRawURLEnd(line, start)
}

func isNonASCIIURLDelimiter(r rune) bool {
	if r <= 0x7e {
		return false
	}
	if r >= 0xFF00 && r <= 0xFFEF {
		return true
	}
	return unicode.IsPunct(r) || unicode.IsSymbol(r) || unicode.IsSpace(r)
}

func isCJKScriptRune(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r)
}

func isURLSegmentDelimiterRune(r rune) bool {
	switch r {
	case '/', '?', '&', '=', '#', '_', '-', '%', '+', ':':
		return true
	default:
		return false
	}
}

func scanRawURLEnd(s string, start int) int {
	i := start
	sep := strings.Index(s[start:], "://")
	afterAuthority := start
	if sep >= 0 {
		afterAuthority = start + sep + 3
	}
	inPathOrQuery := false
	inNonASCIISeg := false
	var prevRune rune
	for i < len(s) {
		r, w := utf8.DecodeRuneInString(s[i:])
		if r <= 0x20 || r == 0x7f || unicode.IsSpace(r) {
			break
		}
		if r == '"' || r == '\'' || r == '<' || r == '>' || r == '`' || r == '\\' {
			break
		}
		if i >= afterAuthority && (r == '/' || r == '?' || r == '#') {
			inPathOrQuery = true
		}
		if r > 0x7e {
			if isNonASCIIURLDelimiter(r) {
				break
			}
			if isCJKScriptRune(r) {
				if !inPathOrQuery || (!inNonASCIISeg && !isURLSegmentDelimiterRune(prevRune)) {
					break
				}
				inNonASCIISeg = true
			} else if inPathOrQuery {
				inNonASCIISeg = true
			} else {
				break
			}
		} else {
			if isURLSegmentDelimiterRune(r) {
				inNonASCIISeg = false
			}
		}
		prevRune = r
		i += w
	}
	return i
}

func trimTrailingURLPunctuation(s string, start, end int) (string, int) {
	openParens := 0
	openBrackets := 0
	for i := start; i < end; i++ {
		switch s[i] {
		case '(':
			openParens++
		case ')':
			openParens--
		case '[':
			openBrackets++
		case ']':
			openBrackets--
		}
	}
	cur := end
	for cur > start {
		last := s[cur-1]
		switch last {
		case '.', ',', ';', ':', '!', '?':
			cur--
		case ')':
			if openParens < 0 {
				openParens++
				cur--
			} else {
				return s[start:cur], cur
			}
		case ']':
			if openBrackets < 0 {
				openBrackets++
				cur--
			} else {
				return s[start:cur], cur
			}
		case '}':
			cur--
		default:
			return s[start:cur], cur
		}
	}
	return s[start:cur], cur
}

// matchLegacyParenthesizedLabel detects "标题 (https://...)" or "标题（https://...）"
// so already-cached Weibo items display clickable "标题" without raw parenthesized URLs,
// while leaving normal English X prose such as "See release (https://example.com)" intact.
func matchLegacyParenthesizedLabel(line string, urlStart, urlEnd int, isSinaWeibo bool, cleanURL string) (start, end int, label string, ok bool) {
	var closeParen string
	openPos := -1
	switch {
	case urlStart >= 1 && line[urlStart-1] == '(' && urlEnd < len(line) && line[urlEnd] == ')':
		openPos = urlStart - 1
		closeParen = ")"
	case urlStart >= len("（") && strings.HasSuffix(line[:urlStart], "（") && strings.HasPrefix(line[urlEnd:], "）"):
		openPos = urlStart - len("（")
		closeParen = "）"
	default:
		return 0, 0, "", false
	}

	// Optional single space before '(' (as produced by legacy "Title (https://...)").
	labelEnd := openPos
	hasSpaceBeforeParen := false
	if labelEnd > 0 && line[labelEnd-1] == ' ' {
		hasSpaceBeforeParen = true
		labelEnd--
	}
	if !hasSpaceBeforeParen && closeParen == ")" {
		// Require space before ASCII '(' so normal prose "说明(https://...)" doesn't swallow whole clauses.
		return 0, 0, "", false
	}
	if labelEnd <= 0 {
		return 0, 0, "", false
	}

	// Scan backwards to find the token boundary (whitespace or sentence punctuation).
	labelStart := labelEnd
	runeCount := 0
	for labelStart > 0 {
		r, w := utf8.DecodeLastRuneInString(line[:labelStart])
		if unicode.IsSpace(r) || isLegacyLabelBoundaryRune(r) {
			break
		}
		labelStart -= w
		runeCount++
		if runeCount > 32 {
			return 0, 0, "", false
		}
	}
	candLabel := strings.TrimSpace(line[labelStart:labelEnd])
	if candLabel == "" || strings.Contains(candLabel, "://") {
		return 0, 0, "", false
	}
	if !isSinaWeibo && !isLikelyLegacyParenthesizedLabel(candLabel, cleanURL) {
		return 0, 0, "", false
	}
	return labelStart, urlEnd + len(closeParen), candLabel, true
}

func isLikelyLegacyParenthesizedLabel(candLabel, cleanURL string) bool {
	if u, err := url.Parse(cleanURL); err == nil {
		host := strings.ToLower(u.Hostname())
		if host == "weibo.com" || strings.HasSuffix(host, ".weibo.com") ||
			host == "weibo.cn" || strings.HasSuffix(host, ".weibo.cn") ||
			host == "t.cn" {
			return true
		}
	}
	for _, r := range candLabel {
		if r > 0x7e {
			return true
		}
	}
	return false
}

func isLegacyLabelBoundaryRune(r rune) bool {
	switch r {
	case '，', '。', '：', '；', '！', '？', '、', '（', '）', '(', ')', '【', '】', '《', '》', '"', '\'', ',', ';', ':':
		return true
	default:
		return false
	}
}

func findUnclaimedLineSpan(line, label string, from int, claimed []lineLinkMatch) (int, int) {
	if label == "" || from < 0 || from >= len(line) {
		return -1, 0
	}
	if idx := findExactUnclaimedSpan(line, label, from, claimed); idx >= 0 {
		return idx, len(label)
	}
	// Domain/display_url fallback: match case-insensitively and tolerate "..." vs "…" or optional "www.".
	if !strings.Contains(label, ".") || strings.ContainsAny(label, " \t\r\n") {
		return -1, 0
	}
	variants := []string{label}
	if strings.HasSuffix(label, "…") {
		variants = append(variants, strings.TrimSuffix(label, "…")+"...")
	} else if strings.HasSuffix(label, "...") {
		variants = append(variants, strings.TrimSuffix(label, "...")+"…")
	}
	lowerLbl := strings.ToLower(label)
	if strings.HasPrefix(lowerLbl, "www.") {
		trimmed := label[4:]
		variants = append(variants, trimmed)
		if strings.HasSuffix(trimmed, "…") {
			variants = append(variants, strings.TrimSuffix(trimmed, "…")+"...")
		}
	} else {
		variants = append(variants, "www."+label)
		if strings.HasSuffix(label, "…") {
			variants = append(variants, "www."+strings.TrimSuffix(label, "…")+"...")
		}
	}
	for _, v := range variants {
		if idx, matchedLen := findCaseInsensitiveUnclaimedSpan(line, v, from, claimed); idx >= 0 {
			return idx, matchedLen
		}
	}
	return -1, 0
}

func findExactUnclaimedSpan(line, needle string, from int, claimed []lineLinkMatch) int {
	if needle == "" || len(needle) > len(line) {
		return -1
	}
	cur := from
	for cur <= len(line)-len(needle) {
		rel := strings.Index(line[cur:], needle)
		if rel < 0 {
			return -1
		}
		idx := cur + rel
		if !spanOverlapsClaimed(idx, idx+len(needle), claimed) {
			return idx
		}
		cur = idx + 1
	}
	return -1
}

func findCaseInsensitiveUnclaimedSpan(line, needle string, from int, claimed []lineLinkMatch) (int, int) {
	if needle == "" {
		return -1, 0
	}
	// Restrict to ASCII-safe byte-length-preserving case-insensitive matching so byte offsets remain exact.
	if !isPureASCIIStr(needle) {
		// Handle trailing Unicode "…" cleanly by comparing ASCII prefix + exact "…".
		if strings.HasSuffix(needle, "…") && isPureASCIIStr(strings.TrimSuffix(needle, "…")) {
			// Safe: "…" is unchanged by strings.ToLower, byte lengths match when line prefix is ASCII.
		} else {
			return -1, 0
		}
	}
	needleLower := strings.ToLower(needle)
	nLen := len(needleLower)
	for cur := from; cur+nLen <= len(line); {
		if strings.EqualFold(line[cur:cur+nLen], needleLower) {
			end := cur + nLen
			if !spanOverlapsClaimed(cur, end, claimed) && isDomainLabelBoundary(line, cur, end) {
				return cur, nLen
			}
		}
		_, w := utf8.DecodeRuneInString(line[cur:])
		if w <= 0 {
			break
		}
		cur += w
	}
	return -1, 0
}

func isPureASCIIStr(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func spanOverlapsClaimed(start, end int, claimed []lineLinkMatch) bool {
	for _, m := range claimed {
		if start < m.End && end > m.Start {
			return true
		}
	}
	return false
}

func isDomainLabelBoundary(line string, start, end int) bool {
	if start > 0 {
		prev, _ := utf8.DecodeLastRuneInString(line[:start])
		if (prev >= 'A' && prev <= 'Z') || (prev >= 'a' && prev <= 'z') || (prev >= '0' && prev <= '9') || prev == '@' || prev == '/' || prev == '.' || prev == '_' || prev == '-' {
			return false
		}
	}
	if end < len(line) {
		next, _ := utf8.DecodeRuneInString(line[end:])
		if (next >= 'A' && next <= 'Z') || (next >= 'a' && next <= 'z') || (next >= '0' && next <= '9') || next == '/' || next == '_' || next == '-' {
			return false
		}
	}
	return true
}

var knownBareWebTLDs = map[string]bool{
	"com": true, "org": true, "net": true, "edu": true, "gov": true,
	"io": true, "dev": true, "app": true, "me": true, "co": true,
	"cn": true, "jp": true, "uk": true, "de": true, "fr": true,
	"kr": true, "tw": true, "hk": true, "sg": true, "tv": true,
	"cc": true, "gg": true, "xyz": true, "info": true, "biz": true,
	"news": true, "site": true, "tech": true, "blog": true, "wiki": true,
	"ee": true, "be": true, "ly": true, "to": true, "so": true,
}

func appendBareDomainMatches(line string, matches []lineLinkMatch) []lineLinkMatch {
	for i := 0; i < len(line); {
		r, w := utf8.DecodeRuneInString(line[i:])
		if w <= 0 {
			break
		}
		if !((r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			i += w
			continue
		}
		if i > 0 {
			prev, _ := utf8.DecodeLastRuneInString(line[:i])
			if (prev >= 'A' && prev <= 'Z') || (prev >= 'a' && prev <= 'z') || (prev >= '0' && prev <= '9') ||
				prev == '@' || prev == '.' || prev == '/' || prev == ':' || prev == '_' || prev == '-' || prev == '#' {
				i += w
				continue
			}
		}
		rawEnd := scanBareDomainCandidateEnd(line, i)
		if rawEnd <= i {
			i += w
			continue
		}
		if spanOverlapsClaimed(i, rawEnd, matches) {
			i = rawEnd
			continue
		}
		// Never synthesize a broken URL from a truncated display_url ending in "…" or "...".
		if strings.HasPrefix(line[rawEnd:], "…") || strings.HasPrefix(line[rawEnd:], "...") {
			i = rawEnd
			continue
		}
		cand, cleanEnd := trimTrailingURLPunctuation(line, i, rawEnd)
		if strings.HasPrefix(line[cleanEnd:], "…") || strings.HasPrefix(line[cleanEnd:], "...") {
			i = rawEnd
			continue
		}
		if !isUnambiguousBareWebURL(cand) {
			i = rawEnd
			continue
		}
		cleanURL, ok := ValidateSafeJumpURL("https://" + cand)
		if !ok {
			i = rawEnd
			continue
		}
		label := cand
		if utf8.RuneCountInString(label) > 36 {
			runes := []rune(label)
			label = string(runes[:35]) + "…"
		}
		matches = append(matches, lineLinkMatch{
			Start:    i,
			End:      cleanEnd,
			Label:    label,
			URL:      cleanURL,
			LinkSlot: -1,
		})
		i = cleanEnd
	}
	return matches
}

func scanBareDomainCandidateEnd(s string, start int) int {
	// Reuse scanRawURLEnd semantics by checking host part first, then optional path/query.
	i := start
	inPathOrQuery := false
	inNonASCIISeg := false
	var prevRune rune
	for i < len(s) {
		r, w := utf8.DecodeRuneInString(s[i:])
		if r <= 0x20 || r == 0x7f || unicode.IsSpace(r) {
			break
		}
		if r == '"' || r == '\'' || r == '<' || r == '>' || r == '`' || r == '\\' || r == '@' {
			break
		}
		if r == '/' || r == '?' || r == '#' {
			inPathOrQuery = true
		}
		if r > 0x7e {
			if isNonASCIIURLDelimiter(r) {
				break
			}
			if isCJKScriptRune(r) {
				if !inPathOrQuery || (!inNonASCIISeg && !isURLSegmentDelimiterRune(prevRune)) {
					break
				}
				inNonASCIISeg = true
			} else if inPathOrQuery {
				inNonASCIISeg = true
			} else {
				break
			}
		} else if isURLSegmentDelimiterRune(r) {
			inNonASCIISeg = false
		}
		prevRune = r
		i += w
	}
	return i
}

func isUnambiguousBareWebURL(cand string) bool {
	if cand == "" || strings.Contains(cand, "://") || strings.Contains(cand, "@") || strings.Contains(cand, "…") {
		return false
	}
	hostEnd := strings.IndexAny(cand, "/?#")
	host := cand
	if hostEnd >= 0 {
		host = cand[:hostEnd]
	}
	if host == "" || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") || strings.Contains(host, "..") {
		return false
	}
	parts := strings.Split(strings.ToLower(host), ".")
	if len(parts) < 2 {
		return false
	}
	tld := parts[len(parts)-1]
	if !knownBareWebTLDs[tld] {
		return false
	}
	for _, p := range parts {
		if p == "" || strings.HasPrefix(p, "-") || strings.HasSuffix(p, "-") {
			return false
		}
		for i := 0; i < len(p); i++ {
			b := p[i]
			if !((b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '-') {
				return false
			}
		}
	}
	// Reject single-char domains except well-known x.com / t.co.
	sld := parts[len(parts)-2]
	if len(sld) == 1 && !(sld == "x" && tld == "com") && !(sld == "t" && tld == "co") {
		return false
	}
	// Require either "www." prefix, an explicit path/query, or a multi-char domain + known TLD.
	return parts[0] == "www" || hostEnd >= 0 || len(sld) >= 2
}

// clampRichParagraphs truncates tokenized paragraphs by visible line count and
// visible rune count without ever splitting a hyperlink URL in half.
func clampRichParagraphs(paras []richParagraph, maxParas, maxRunes int) ([]richParagraph, bool) {
	totalNonEmpty := 0
	totalRunes := 0
	for _, p := range paras {
		if len(p.Spans) == 0 {
			continue
		}
		totalNonEmpty++
		for _, sp := range p.Spans {
			totalRunes += utf8.RuneCountInString(sp.Text)
		}
	}
	needsClamp := (maxParas > 0 && totalNonEmpty > maxParas) || (maxRunes > 0 && totalRunes > maxRunes)
	if !needsClamp {
		return paras, false
	}

	var out []richParagraph
	seenParas := 0
	remRunes := maxRunes
	if remRunes <= 0 {
		remRunes = 1 << 30
	}

	for _, p := range paras {
		if len(p.Spans) == 0 {
			if len(out) > 0 && seenParas < maxParas {
				out = append(out, p)
			}
			continue
		}
		if maxParas > 0 && seenParas >= maxParas {
			appendEllipsisToParagraphs(&out)
			return out, true
		}
		seenParas++

		var clampedSpans []richSpan
		for _, sp := range p.Spans {
			n := utf8.RuneCountInString(sp.Text)
			if n <= remRunes {
				clampedSpans = append(clampedSpans, sp)
				remRunes -= n
				continue
			}
			if sp.Kind == richSpanLink {
				// Keep an in-progress link intact if there is room for at least half its label,
				// otherwise stop cleanly before it rather than corrupting its label/URL.
				if remRunes >= 4 {
					clampedSpans = append(clampedSpans, sp)
				}
			} else if remRunes > 0 {
				r := []rune(sp.Text)
				sp.Text = string(r[:remRunes]) + "…"
				clampedSpans = append(clampedSpans, sp)
			}
			remRunes = 0
			break
		}
		if len(clampedSpans) > 0 {
			out = append(out, richParagraph{Spans: clampedSpans})
		}
		if remRunes <= 0 {
			appendEllipsisToParagraphs(&out)
			return out, true
		}
	}
	return out, true
}

func appendEllipsisToParagraphs(paras *[]richParagraph) {
	if len(*paras) == 0 {
		return
	}
	last := &(*paras)[len(*paras)-1]
	if len(last.Spans) == 0 {
		return
	}
	tail := &last.Spans[len(last.Spans)-1]
	if tail.Kind == richSpanPlain && !strings.HasSuffix(tail.Text, "…") {
		tail.Text += "…"
	} else if tail.Kind != richSpanPlain {
		last.Spans = append(last.Spans, richSpan{Kind: richSpanPlain, Text: "…"})
	}
}

func splitPlainIntoClauses(s string) []string {
	if utf8.RuneCountInString(s) <= 18 {
		return []string{s}
	}
	var out []string
	var buf strings.Builder
	runeLen := 0
	flush := func() {
		if buf.Len() > 0 {
			out = append(out, buf.String())
			buf.Reset()
			runeLen = 0
		}
	}
	for _, r := range s {
		buf.WriteRune(r)
		runeLen++
		switch r {
		case '，', '。', '、', '；', '：', '！', '？', '）':
			flush()
		case ',', ';', '!', '?':
			if runeLen >= 8 {
				flush()
			}
		default:
			if r == ' ' && runeLen >= 16 {
				flush()
			}
		}
	}
	flush()
	return out
}

func shouldShowInlineLinkIcon(label string) bool {
	trimmed := strings.TrimSpace(label)
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, "@") || strings.HasPrefix(trimmed, "#") {
		return false
	}
	return true
}

// renderRichText renders post body text with clickable accent hyperlinks (and optional
// Douyin #hashtag highlights), returning true if a hyperlink was clicked in this frame.
func (a *App) renderRichText(c *ui.Context, text string, links []service.TextLinkDTO, opts richTextOptions) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	fontSize := opts.FontSize
	if fontSize <= 0 {
		fontSize = 15
	}
	lineHeight := opts.LineHeight
	if lineHeight <= 0 {
		lineHeight = LineHeightBody
	}
	textColor := opts.TextColor
	if textColor == (ui.Color{}) {
		textColor = ColorTextMain
	}

	paragraphs, hasRich := parseRichTextParagraphs(text, links, opts.HighlightHashtags)
	if !hasRich {
		return a.renderPlainFastPath(c, text, fontSize, lineHeight, textColor, opts)
	}

	if a.ExpandedPosts == nil && opts.ClampKey != "" {
		a.ExpandedPosts = make(map[string]bool)
	}
	expanded := opts.ClampKey != "" && a.ExpandedPosts[opts.ClampKey]

	shownParas := paragraphs
	needsClamp := false
	if opts.ClampKey != "" {
		var clamped []richParagraph
		clamped, needsClamp = clampRichParagraphs(paragraphs, 6, 280)
		if needsClamp && !expanded {
			shownParas = clamped
		}
	} else if opts.MaxLines > 0 {
		shownParas, _ = clampRichParagraphs(paragraphs, opts.MaxLines, opts.MaxLines*42)
	}

	linkClicked := false
	labelCounts := make(map[string]int)

	col := ui.Column(c).FillWidth().Gap(4).ClipX()
	if opts.Label != "" {
		col.Label(opts.Label)
	}
	col.Children(func() {
		for _, para := range shownParas {
			if len(para.Spans) == 0 {
				continue
			}
			paraHasRich := false
			for _, sp := range para.Spans {
				if sp.Kind != richSpanPlain {
					paraHasRich = true
					break
				}
			}
			if !paraHasRich && len(para.Spans) == 1 {
				ui.Text(c, strings.TrimSpace(para.Spans[0].Text)).
					FontSize(fontSize).
					LineHeight(lineHeight).
					TextColor(textColor)
				continue
			}

			ui.Row(c).FillWidth().Wrap().Gap(0).AlignItems(ui.Center).ClipX().Children(func() {
				for _, sp := range para.Spans {
					span := sp
					switch span.Kind {
					case richSpanHashtag:
						ui.Text(c, span.Text).
							FontSize(fontSize).
							LineHeight(lineHeight).
							FontWeight(600).
							TextColor(ColorAccent)
					case richSpanLink:
						labelCounts[span.Label]++
						accLabel := "打开链接：" + span.Label
						if labelCounts[span.Label] > 1 {
							accLabel = fmt.Sprintf("打开链接：%s (%d)", span.Label, labelCounts[span.Label])
						}
						if a.renderInlineHyperlink(c, accLabel, span.Label, span.URL, fontSize) {
							linkClicked = true
						}
					default:
						for _, clause := range splitPlainIntoClauses(span.Text) {
							ui.Text(c, clause).
								FontSize(fontSize).
								LineHeight(lineHeight).
								TextColor(textColor)
						}
					}
				}
			})
		}

		if needsClamp && opts.ClampKey != "" {
			toggleText := "展开"
			if expanded {
				toggleText = "收起"
			}
			btn := ui.ButtonBase(c).Label("正文展开收起："+opts.ClampKey).Padding(2, 0).Children(func() {
				ui.Text(c, toggleText).FontSize(13).FontWeight(600).TextColor(ColorAccent).SingleLine()
			})
			if btn.Clicked() {
				a.ExpandedPosts[opts.ClampKey] = !expanded
				linkClicked = true
			}
		}
	})
	return linkClicked
}

func (a *App) renderPlainFastPath(
	c *ui.Context,
	text string,
	fontSize, lineHeight float32,
	textColor ui.Color,
	opts richTextOptions,
) bool {
	if opts.ClampKey == "" {
		renderPlain := func() {
			t := ui.Text(c, text).FontSize(fontSize).LineHeight(lineHeight).TextColor(textColor)
			if opts.MaxLines > 0 {
				t.MaxLines(opts.MaxLines)
			}
		}
		if opts.Label != "" {
			ui.Box(c).Label(opts.Label).FillWidth().Children(renderPlain)
		} else {
			renderPlain()
		}
		return false
	}

	if a.ExpandedPosts == nil {
		a.ExpandedPosts = make(map[string]bool)
	}
	lines := strings.Count(text, "\n") + 1
	needsClamp := lines > 6 || utf8.RuneCountInString(text) > 280
	expanded := a.ExpandedPosts[opts.ClampKey]

	col := ui.Column(c).FillWidth().Gap(4)
	if opts.Label != "" {
		col.Label(opts.Label)
	}
	toggled := false
	col.Children(func() {
		t := ui.Text(c, text).FontSize(fontSize).LineHeight(lineHeight).TextColor(textColor)
		if needsClamp && !expanded {
			t.MaxLines(6)
		}
		if needsClamp {
			toggleText := "展开"
			if expanded {
				toggleText = "收起"
			}
			btn := ui.ButtonBase(c).Label("正文展开收起："+opts.ClampKey).Padding(2, 0).Children(func() {
				ui.Text(c, toggleText).FontSize(13).FontWeight(600).TextColor(ColorAccent).SingleLine()
			})
			if btn.Clicked() {
				a.ExpandedPosts[opts.ClampKey] = !expanded
				toggled = true
			}
		}
	})
	return toggled
}

func (a *App) renderInlineHyperlink(c *ui.Context, accessLabel, displayText, targetURL string, fontSize float32) bool {
	showIcon := shouldShowInlineLinkIcon(displayText)
	btn := ui.ButtonBase(c).
		Label(accessLabel).
		Tooltip(targetURL).
		Padding(1, 3).
		Radius(5).
		AlignItems(ui.Center).
		Shrink(1)

	hovered := btn.Hovered()
	col := ColorAccent
	if hovered {
		col = ColorAccentHover
		btn.Background(ColorAccentBg)
	}

	btn.DrawOver(func(p *ui.Painter, r ui.Rect) {
		if hovered && r.W > 6 {
			p.Fill(ui.Rect{
				X: r.X + 3,
				Y: r.Y + r.H - 2,
				W: r.W - 6,
				H: 1.2,
			}, col, 0.6)
		}
	})

	btn.Children(func() {
		if showIcon {
			ui.Row(c).Gap(3).AlignItems(ui.Center).Shrink(1).ClipX().Children(func() {
				DrawIcon(c, "link", max(12, fontSize*0.84), col, 1.8)
				ui.Text(c, displayText).
					FontSize(fontSize).
					FontWeight(500).
					TextColor(col).
					SingleLine().
					Shrink(1)
			})
		} else {
			ui.Text(c, displayText).
				FontSize(fontSize).
				FontWeight(500).
				TextColor(col).
				SingleLine().
				Shrink(1)
		}
	})

	if btn.Clicked() {
		a.openExternalURL(targetURL)
		return true
	}
	return false
}
