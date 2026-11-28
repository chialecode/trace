// Package mediaview provides the reusable full-screen image viewer, video
// player, in-memory decoded image loader, and shared presentation helpers for
// TRACE mobile pages (FR-MOB-011..014, NFR-MOB-DATA-001).
package mediaview

import (
	"fmt"
	"hash/fnv"
	"net/url"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/chialecode/trace/cmd/trace-mobile/internal/theme"
	"github.com/chialecode/trace/internal/companionapi"
	"github.com/egoist/mygo/ui"
)

// SiteLabel maps a canonical site ID to its short Chinese display label (docs/ui.md §3.4).
func SiteLabel(site string) string {
	switch strings.ToLower(strings.TrimSpace(site)) {
	case "bilibili":
		return "B 站"
	case "douyin":
		return "抖音"
	case "weibo":
		return "微博"
	case "x":
		return "X"
	default:
		return strings.TrimSpace(site)
	}
}

// FormatPubTime formats a Unix timestamp (seconds) into desktop-matching Chinese text.
func FormatPubTime(pubTs int64, now time.Time) string {
	if pubTs <= 0 {
		return ""
	}
	t := time.Unix(pubTs, 0).In(now.Location())
	d := now.Sub(t)
	if d >= 0 && d < time.Minute {
		return "刚刚"
	}
	if d >= 0 && d < time.Hour {
		return fmt.Sprintf("%d 分钟前", int(d.Minutes()))
	}
	if d >= 0 && d < 24*time.Hour {
		return fmt.Sprintf("%d 小时前", int(d.Hours()))
	}
	if t.Year() == now.Year() {
		return fmt.Sprintf("%d月%d日 %02d:%02d", int(t.Month()), t.Day(), t.Hour(), t.Minute())
	}
	return fmt.Sprintf("%d年%d月%d日", t.Year(), int(t.Month()), t.Day())
}

// FormatDuration formats seconds as MM:SS or H:MM:SS. Unknown/non-positive
// durations return "" so callers never display fake "0:00".
func FormatDuration(sec int) string {
	if sec <= 0 {
		return ""
	}
	h := sec / 3600
	m := (sec % 3600) / 60
	s := sec % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}

// FormatBytes formats a byte count into a compact human-readable string.
func FormatBytes(b int64) string {
	if b <= 0 {
		return ""
	}
	const (
		kib = 1024
		mib = 1024 * kib
		gib = 1024 * mib
	)
	switch {
	case b >= gib:
		return fmt.Sprintf("%.2f GB", float64(b)/float64(gib))
	case b >= mib:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(mib))
	case b >= kib:
		return fmt.Sprintf("%.0f KB", float64(b)/float64(kib))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// 8 deterministic avatar placeholder colors per docs/ui.md §5 (no pink).
var avatarPalette = []ui.Color{
	ui.Hex("#2563eb"), // blue
	ui.Hex("#0d9488"), // teal
	ui.Hex("#4f46e5"), // indigo
	ui.Hex("#d97706"), // amber
	ui.Hex("#16a34a"), // green
	ui.Hex("#0891b2"), // cyan
	ui.Hex("#7c3aed"), // purple
	ui.Hex("#475569"), // slate
}

// AvatarPlaceholderColor deterministically selects a non-pink placeholder color from seed.
func AvatarPlaceholderColor(seed string) ui.Color {
	h := fnv.New32a()
	_, _ = h.Write([]byte(seed))
	return avatarPalette[int(h.Sum32())%len(avatarPalette)]
}

// AvatarInitial returns the first visible rune of name for avatar placeholders.
func AvatarInitial(name string) string {
	s := strings.TrimSpace(name)
	if s == "" {
		return "?"
	}
	r, _ := utf8.DecodeRuneInString(s)
	return strings.ToUpper(string(r))
}

// ExtractLinks collects safe http(s):// links from explicit DTO links and inline
// text URLs, deduplicating by normalized URL (handling fragment-stripped item.Links
// and balanced parentheses / IRI paths) while rejecting unsafe schemes, credentials,
// and signed media CDN URLs (DEC-073).
func ExtractLinks(text string, explicit []companionapi.TextLink) []companionapi.TextLink {
	seen := make(map[string]bool)
	seenBase := make(map[string]bool)
	out := make([]companionapi.TextLink, 0, len(explicit))
	add := func(label, rawURL string, isInline bool) {
		cleanU, ok := normalizeSafeExternalURL(rawURL)
		if !ok {
			return
		}
		normFull := canonicalMobileURLKey(cleanU, false)
		normBase := canonicalMobileURLKey(cleanU, true)
		if seen[normFull] || (isInline && seenBase[normBase]) {
			return
		}
		seen[normFull] = true
		seenBase[normBase] = true
		lbl := strings.TrimSpace(label)
		if lbl == "" || lbl == cleanU || strings.HasPrefix(strings.ToLower(lbl), "http://") || strings.HasPrefix(strings.ToLower(lbl), "https://") {
			lbl = compactURLDisplay(cleanU)
		}
		out = append(out, companionapi.TextLink{Label: lbl, URL: cleanU})
	}
	for _, l := range explicit {
		add(l.Label, l.URL, false)
	}
	for _, cand := range scanMobileInlineURLs(text) {
		add("", cand, true)
	}
	return out
}

// IsSafeExternalURL reports whether rawURL is a clean http:// or https:// web URL
// without userinfo, control/whitespace/backslash chars, forbidden media CDN hosts,
// or signed/credential query markers.
func IsSafeExternalURL(rawURL string) bool {
	_, ok := normalizeSafeExternalURL(rawURL)
	return ok
}

func normalizeSafeExternalURL(rawURL string) (string, bool) {
	s := strings.TrimSpace(rawURL)
	if s == "" {
		return "", false
	}
	for _, r := range s {
		if r <= 0x1f || r == 0x7f || unicode.IsSpace(r) || r == '\\' {
			return "", false
		}
	}
	if hasUnsafeMobileSecretMarker(s) {
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
	if isForbiddenMobileMediaHost(host) {
		return "", false
	}
	u.Scheme = strings.ToLower(u.Scheme)
	return u.String(), true
}

func isForbiddenMobileMediaHost(host string) bool {
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

func hasUnsafeMobileSecretMarker(raw string) bool {
	lower := strings.ToLower(strings.TrimSpace(raw))
	if lower == "" {
		return false
	}
	if strings.Contains(lower, "authorization: bearer") || strings.Contains(lower, "authorization:bearer") {
		return true
	}
	for _, marker := range []string{
		"x-amz-signature=", "x-amz-signature%3d",
		"x-amz-credential=", "x-amz-credential%3d",
		"x-amz-security-token=", "x-amz-security-token%3d",
		"key-pair-id=", "signature=", "sig=", "ssig=", "expires=",
		"x-expires=", "x-signature=", "policy=", "token=", "access_token=",
		"auth_token=", "ct0=", "kdt=", "twid=", "guest_id=",
		"x-csrf-token=", "x-client-transaction-id=",
		"sessdata=", "bili_jct=", "ac_time_value=", "sessionid=",
		"a_bogus=", "x-bogus=", "w_rid=", "upsig=",
		"sub=", "subp=", "wbpsess=", "xsrf-token=", "x-xsrf-token=",
		"scf=", "alf=", "alc=", "ssologinstate=", "suhb=", "pc_token=", "_t_wm=",
	} {
		idx := strings.Index(lower, marker)
		for idx >= 0 {
			if idx == 0 {
				return true
			}
			prev := lower[idx-1]
			if prev == '?' || prev == '&' || prev == ';' || prev == '#' || prev == '/' || prev == '%' || prev == ':' || prev == '=' {
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

func canonicalMobileURLKey(rawURL string, stripFragment bool) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return rawURL
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

// asciiLower lowercases only ASCII bytes 'A'..'Z' -> 'a'..'z', leaving every
// non-ASCII UTF-8 byte untouched so len(asciiLower(s)) == len(s) always holds
// and byte offsets computed on the lowered string can safely slice s.
func asciiLower(s string) string {
	hasUpper := false
	for i := 0; i < len(s); i++ {
		if s[i] >= 'A' && s[i] <= 'Z' {
			hasUpper = true
			break
		}
	}
	if !hasUpper {
		return s
	}
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

func scanMobileInlineURLs(text string) []string {
	var out []string
	lower := asciiLower(text)
	pos := 0
	for pos < len(text) {
		pHTTPS := strings.Index(lower[pos:], "https://")
		pHTTP := strings.Index(lower[pos:], "http://")
		rel := -1
		if pHTTPS >= 0 {
			rel = pHTTPS
		}
		if pHTTP >= 0 && (rel < 0 || pHTTP < rel) {
			rel = pHTTP
		}
		if rel < 0 {
			break
		}
		start := pos + rel
		end := scanMobileURLEnd(text, start)
		cand, cleanEnd := trimMobileTrailingURLPunct(text, start, end)
		if cand != "" {
			out = append(out, cand)
		}
		if cleanEnd <= start {
			pos = start + 4
		} else {
			pos = cleanEnd
		}
	}
	return out
}

func scanMobileURLEnd(s string, start int) int {
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
		if r <= 0x20 || r == 0x7f || unicode.IsSpace(r) || r == '"' || r == '\'' || r == '<' || r == '>' || r == '`' || r == '\\' {
			break
		}
		if i >= afterAuthority && (r == '/' || r == '?' || r == '#') {
			inPathOrQuery = true
		}
		if r > 0x7e {
			if (r >= 0xFF00 && r <= 0xFFEF) || unicode.IsPunct(r) || unicode.IsSymbol(r) || unicode.IsSpace(r) {
				break
			}
			isCJK := unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r)
			if isCJK {
				isDelim := prevRune == '/' || prevRune == '?' || prevRune == '&' || prevRune == '=' || prevRune == '#' || prevRune == '_' || prevRune == '-' || prevRune == '%' || prevRune == '+'
				if !inPathOrQuery || (!inNonASCIISeg && !isDelim) {
					break
				}
				inNonASCIISeg = true
			} else if inPathOrQuery {
				inNonASCIISeg = true
			} else {
				break
			}
		} else if r == '/' || r == '?' || r == '&' || r == '=' || r == '#' || r == '_' || r == '-' || r == '%' || r == '+' {
			inNonASCIISeg = false
		}
		prevRune = r
		i += w
	}
	return i
}

func trimMobileTrailingURLPunct(s string, start, end int) (string, int) {
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
		switch s[cur-1] {
		case '.', ',', ';', ':', '!', '?', '}':
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
		default:
			return s[start:cur], cur
		}
	}
	return s[start:cur], cur
}

func compactURLDisplay(rawURL string) string {
	s := strings.TrimPrefix(rawURL, "https://")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimSuffix(s, "/")
	if len([]rune(s)) > 42 {
		r := []rune(s)
		return string(r[:40]) + "…"
	}
	return s
}

// MediaThumbPath builds the authenticated desktop URL path for a 480px thumbnail.
// GIFs are requested as original files per docs/design/mobile.md §6.
func MediaThumbPath(itemKey, relPath string) string {
	if strings.TrimSpace(itemKey) == "" || strings.TrimSpace(relPath) == "" {
		return ""
	}
	base := companionapi.MediaPath(itemKey, relPath)
	if strings.EqualFold(filepath.Ext(relPath), ".gif") {
		return base
	}
	return base + "?w=480"
}

// MediaViewerPath builds the authenticated desktop URL path for the image viewer.
// Uses ?w=2560 by default, or the unmodified original when fullOriginal or gif is true.
func MediaViewerPath(itemKey, relPath string, fullOriginal, gif bool) string {
	if strings.TrimSpace(itemKey) == "" || strings.TrimSpace(relPath) == "" {
		return ""
	}
	base := companionapi.MediaPath(itemKey, relPath)
	if fullOriginal || gif || strings.EqualFold(filepath.Ext(relPath), ".gif") {
		return base
	}
	return base + "?w=2560"
}

// DrawIcon draws additional linear vector icons for home, library, viewer, and player
// matching docs/ui.md §5 icon conventions.
func DrawIcon(c *ui.Context, name string, size float32, col ui.Color, strokeWidth float32) ui.Element {
	if size <= 0 {
		size = 20
	}
	if strokeWidth <= 0 {
		strokeWidth = 1.8
	}
	return ui.Box(c).Size(size, size).Shrink(0).Draw(func(p *ui.Painter, r ui.Rect) {
		PaintIcon(p, r, name, col, strokeWidth)
	})
}

// PaintIcon paints a linear vector icon on a 24x24 grid into r.
func PaintIcon(p *ui.Painter, r ui.Rect, name string, col ui.Color, strokeWidth float32) {
	if strokeWidth <= 0 {
		strokeWidth = 1.8
	}
	s := func(v float32) float32 { return v * r.W / 24.0 }
	x := func(v float32) float32 { return r.X + s(v) }
	y := func(v float32) float32 { return r.Y + s(v) }

	switch name {
	case "close":
		p.StrokePath((&ui.Path{}).MoveTo(x(6), y(6)).LineTo(x(18), y(18)).MoveTo(x(18), y(6)).LineTo(x(6), y(18)), strokeWidth, col)
	case "play":
		p.FillPath((&ui.Path{}).MoveTo(x(7.5), y(5.5)).LineTo(x(19), y(12)).LineTo(x(7.5), y(18.5)).Close(), col)
	case "pause":
		p.Fill(ui.Rect{X: x(6.5), Y: y(5.5), W: s(3.8), H: s(13)}, col, s(1))
		p.Fill(ui.Rect{X: x(13.7), Y: y(5.5), W: s(3.8), H: s(13)}, col, s(1))
	case "more":
		p.Fill(ui.Rect{X: x(4.5), Y: y(10.6), W: s(2.8), H: s(2.8)}, col, s(1.4))
		p.Fill(ui.Rect{X: x(10.6), Y: y(10.6), W: s(2.8), H: s(2.8)}, col, s(1.4))
		p.Fill(ui.Rect{X: x(16.7), Y: y(10.6), W: s(2.8), H: s(2.8)}, col, s(1.4))
	case "filter":
		p.StrokePath((&ui.Path{}).
			MoveTo(x(4), y(7)).LineTo(x(20), y(7)).
			MoveTo(x(7), y(12)).LineTo(x(17), y(12)).
			MoveTo(x(10), y(17)).LineTo(x(14), y(17)), strokeWidth, col)
	case "chevron-left":
		p.StrokePath((&ui.Path{}).MoveTo(x(15), y(5.5)).LineTo(x(8.5), y(12)).LineTo(x(15), y(18.5)), strokeWidth, col)
	case "chevron-right":
		p.StrokePath((&ui.Path{}).MoveTo(x(9), y(5.5)).LineTo(x(15.5), y(12)).LineTo(x(9), y(18.5)), strokeWidth, col)
	case "fullscreen":
		p.StrokePath((&ui.Path{}).
			MoveTo(x(4), y(9)).LineTo(x(4), y(4)).LineTo(x(9), y(4)).
			MoveTo(x(15), y(4)).LineTo(x(20), y(4)).LineTo(x(20), y(9)).
			MoveTo(x(20), y(15)).LineTo(x(20), y(20)).LineTo(x(15), y(20)).
			MoveTo(x(9), y(20)).LineTo(x(4), y(20)).LineTo(x(4), y(15)), strokeWidth, col)
	case "volume":
		p.StrokePath((&ui.Path{}).
			MoveTo(x(4), y(9.5)).LineTo(x(8.5), y(9.5)).LineTo(x(13), y(5.5)).LineTo(x(13), y(18.5)).LineTo(x(8.5), y(14.5)).LineTo(x(4), y(14.5)).Close(), strokeWidth, col)
		p.StrokePath((&ui.Path{}).MoveTo(x(16.5), y(9)).QuadTo(x(19), y(12), x(16.5), y(15)), strokeWidth, col)
	case "volume-mute":
		p.StrokePath((&ui.Path{}).
			MoveTo(x(4), y(9.5)).LineTo(x(8.5), y(9.5)).LineTo(x(13), y(5.5)).LineTo(x(13), y(18.5)).LineTo(x(8.5), y(14.5)).LineTo(x(4), y(14.5)).Close(), strokeWidth, col)
		p.StrokePath((&ui.Path{}).MoveTo(x(16), y(9.5)).LineTo(x(20.5), y(14.5)).MoveTo(x(20.5), y(9.5)).LineTo(x(16), y(14.5)), strokeWidth, col)
	case "external-link":
		p.StrokePath((&ui.Path{}).
			MoveTo(x(14), y(4.5)).LineTo(x(19.5), y(4.5)).LineTo(x(19.5), y(10)).
			MoveTo(x(11), y(13)).LineTo(x(19.5), y(4.5)), strokeWidth, col)
		p.StrokePath((&ui.Path{}).
			MoveTo(x(17), y(13.5)).LineTo(x(17), y(19.5)).LineTo(x(4.5), y(19.5)).LineTo(x(4.5), y(7)).LineTo(x(10.5), y(7)), strokeWidth, col)
	case "video":
		p.StrokePath((&ui.Path{}).MoveTo(x(3.5), y(5.5)).LineTo(x(20.5), y(5.5)).LineTo(x(20.5), y(18.5)).LineTo(x(3.5), y(18.5)).Close(), strokeWidth, col)
		p.FillPath((&ui.Path{}).MoveTo(x(10), y(9)).LineTo(x(15.5), y(12)).LineTo(x(10), y(15)).Close(), col)
	case "image":
		p.StrokePath((&ui.Path{}).MoveTo(x(3.5), y(5)).LineTo(x(20.5), y(5)).LineTo(x(20.5), y(19)).LineTo(x(3.5), y(19)).Close(), strokeWidth, col)
		p.StrokePath((&ui.Path{}).MoveTo(x(4.5), y(17)).LineTo(x(9.5), y(12)).LineTo(x(13), y(15.5)).LineTo(x(16), y(12.5)).LineTo(x(19.5), y(17)), strokeWidth, col)
	case "live":
		p.Fill(ui.Rect{X: x(10.2), Y: y(10.2), W: s(3.6), H: s(3.6)}, col, s(1.8))
		p.StrokePath((&ui.Path{}).MoveTo(x(7.5), y(8)).QuadTo(x(5.2), y(12), x(7.5), y(16)), strokeWidth, col)
		p.StrokePath((&ui.Path{}).MoveTo(x(16.5), y(8)).QuadTo(x(18.8), y(12), x(16.5), y(16)), strokeWidth, col)
	case "list":
		p.StrokePath((&ui.Path{}).
			MoveTo(x(4), y(7)).LineTo(x(20), y(7)).
			MoveTo(x(4), y(12)).LineTo(x(20), y(12)).
			MoveTo(x(4), y(17)).LineTo(x(20), y(17)), strokeWidth, col)
	default:
		theme.PaintIcon(p, r, name, col, strokeWidth)
	}
}
