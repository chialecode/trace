package x

import (
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/chialecode/trace/internal/site"
)

// BuildPostLinks converts a Post's expanded URL entities into safe, human-readable
// cross-site TextLink items (excluding self-truncation status links and forbidden media hosts).
func BuildPostLinks(post *Post) []site.TextLink {
	if post == nil || len(post.URLs) == 0 {
		return nil
	}
	out := make([]site.TextLink, 0, len(post.URLs))
	seen := make(map[string]bool, len(post.URLs))
	for _, ue := range post.URLs {
		exp := strings.TrimSpace(ue.ExpandedURL)
		if exp == "" {
			exp = strings.TrimSpace(ue.ShortURL)
		}
		if exp == "" || isSelfStatusURL(exp, post.ID) {
			continue
		}
		cleanURL := SafePublicWebURL(exp)
		if cleanURL == "" {
			continue
		}
		label := cleanXDisplayLabel(ue.DisplayURL, cleanURL)
		if label == "" {
			continue
		}
		key := label + "\x00" + cleanURL
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, site.TextLink{
			Label: label,
			URL:   cleanURL,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

var xDisplayURLUnescaper = strings.NewReplacer(
	"&amp;", "&",
	"&lt;", "<",
	"&gt;", ">",
	"&quot;", "\"",
	"&#39;", "'",
	"&#x27;", "'",
)

// unescapeXDisplayURL decodes only explicit XML/HTML entities in display_url without
// invoking html.UnescapeString (which corrupts query params like "&copy=" into "©=").
func unescapeXDisplayURL(raw string) string {
	return xDisplayURLUnescaper.Replace(strings.TrimSpace(raw))
}

func cleanXDisplayLabel(displayURL, expandedURL string) string {
	disp := strings.TrimSpace(unescapeXDisplayURL(displayURL))
	lowerDisp := strings.ToLower(disp)
	if disp != "" &&
		!strings.HasPrefix(lowerDisp, "http://") &&
		!strings.HasPrefix(lowerDisp, "https://") &&
		!strings.HasPrefix(lowerDisp, "t.co/") &&
		lowerDisp != "t.co" {
		if utf8.RuneCountInString(disp) > 48 {
			runes := []rune(disp)
			disp = string(runes[:47]) + "…"
		}
		return disp
	}
	u, err := url.Parse(expandedURL)
	if err != nil || u.Host == "" {
		return "网页链接"
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	if host == "t.co" {
		return "网页链接"
	}
	p := strings.TrimRight(u.Path, "/")
	if decoded, dErr := url.PathUnescape(p); dErr == nil && utf8.ValidString(decoded) && !strings.ContainsAny(decoded, "\r\n\t ") {
		p = decoded
	}
	if p == "" {
		return host
	}
	cand := host + p
	if utf8.RuneCountInString(cand) > 36 {
		runes := []rune(cand)
		cand = string(runes[:35]) + "…"
	}
	return cand
}
