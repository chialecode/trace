package subscription

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// MaxSubscriptionCustomLinks caps how many custom external links one subscription may keep.
	MaxSubscriptionCustomLinks = 20
	// MaxCustomLinkLabelRunes caps the user-chosen label length of a custom link.
	MaxCustomLinkLabelRunes = 48
	// MaxCustomLinkURLBytes caps the byte length of a custom link URL.
	MaxCustomLinkURLBytes = 2048
	// MaxDerivedCustomLinkLabelRunes caps an automatically derived host/path display label.
	MaxDerivedCustomLinkLabelRunes = 40
)

// CustomLink represents an optional external link attached to a subscription,
// such as link3.cc, linktr.ee, or a personal webpage (DEC-074).
type CustomLink struct {
	Label string `json:"label,omitempty"`
	URL   string `json:"url"`
}

// DisplayLabel returns the user-chosen label if non-empty, or a clean derived
// host/path summary of the URL.
func (l CustomLink) DisplayLabel() string {
	if lbl := strings.TrimSpace(l.Label); lbl != "" {
		return lbl
	}
	return DeriveCustomLinkLabel(l.URL)
}

func cloneCustomLinks(links []CustomLink) []CustomLink {
	if len(links) == 0 {
		return nil
	}
	return slices.Clone(links)
}

// ValidateCustomLinkURL strictly validates that rawURL is a clean http:// or
// https:// URL with a non-empty hostname, no whitespace/control characters,
// no userinfo credentials, and no session cookie/token query markers.
func ValidateCustomLinkURL(rawURL string) (string, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return "", fmt.Errorf("subscription: 链接地址不能为空")
	}
	if len(trimmed) > MaxCustomLinkURLBytes {
		return "", fmt.Errorf("subscription: 链接地址过长（最多 %d 字节）", MaxCustomLinkURLBytes)
	}
	if strings.HasPrefix(trimmed, `\\`) || strings.HasPrefix(trimmed, "//") {
		return "", fmt.Errorf("subscription: 链接必须以 http:// 或 https:// 开头")
	}
	for _, r := range trimmed {
		if unicode.IsSpace(r) || r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("subscription: 链接地址不能包含空白或控制字符")
		}
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("subscription: 链接地址格式无效")
	}
	scheme := strings.ToLower(strings.TrimSpace(u.Scheme))
	if scheme != "http" && scheme != "https" {
		if scheme == "" {
			return "", fmt.Errorf("subscription: 链接必须以 http:// 或 https:// 开头")
		}
		return "", fmt.Errorf("subscription: 不支持的链接协议 %q（仅支持 http:// 或 https://）", u.Scheme)
	}
	if u.User != nil {
		return "", fmt.Errorf("subscription: 链接不能包含账号或密码")
	}
	hostname := strings.TrimSpace(u.Hostname())
	if u.Host == "" || hostname == "" {
		return "", fmt.Errorf("subscription: 链接缺少有效域名")
	}
	for _, r := range hostname {
		if unicode.IsSpace(r) || r < 0x20 || r == 0x7f || r == '\\' || r == '/' {
			return "", fmt.Errorf("subscription: 链接域名格式无效")
		}
	}
	if containsSensitiveLinkSecret(trimmed) {
		return "", fmt.Errorf("subscription: 链接不能包含登录凭证或临时签名参数")
	}
	u.Scheme = scheme
	lowerHost := strings.ToLower(hostname)
	if port := u.Port(); port != "" {
		if strings.Contains(lowerHost, ":") {
			u.Host = "[" + lowerHost + "]:" + port
		} else {
			u.Host = lowerHost + ":" + port
		}
	} else if strings.Contains(lowerHost, ":") {
		u.Host = "[" + lowerHost + "]"
	} else {
		u.Host = lowerHost
	}
	return u.String(), nil
}

func containsSensitiveLinkSecret(raw string) bool {
	lower := strings.ToLower(raw)
	unescaped := lower
	if dec, err := url.QueryUnescape(lower); err == nil {
		unescaped = strings.ToLower(dec)
	}
	for _, marker := range []string{
		"sessdata=", "bili_jct=", "sessionid=", "sessionid_ss=", "sid_guard=",
		"auth_token=", "ct0=", "kdt=", "wbpsess=", "xsrf-token=",
		"a_bogus=", "x-bogus=", "x-signature=", "ssig=",
	} {
		if strings.Contains(lower, marker) || strings.Contains(unescaped, marker) {
			return true
		}
	}
	return false
}

// NormalizeAndValidateCustomLink validates and normalizes one CustomLink.
func NormalizeAndValidateCustomLink(in CustomLink) (CustomLink, error) {
	cleanURL, err := ValidateCustomLinkURL(in.URL)
	if err != nil {
		return CustomLink{}, err
	}
	label, err := normalizeCustomLinkLabel(in.Label)
	if err != nil {
		return CustomLink{}, err
	}
	return CustomLink{
		Label: label,
		URL:   cleanURL,
	}, nil
}

func normalizeCustomLinkLabel(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	for _, r := range trimmed {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("subscription: 链接名称不能包含换行或控制字符")
		}
	}
	if utf8.RuneCountInString(trimmed) > MaxCustomLinkLabelRunes {
		return "", fmt.Errorf("subscription: 链接名称最多 %d 个字", MaxCustomLinkLabelRunes)
	}
	return trimmed, nil
}

// NormalizeAndValidateCustomLinks validates, normalizes, and deduplicates a
// subscription's custom links slice (enforcing <= MaxSubscriptionCustomLinks).
func NormalizeAndValidateCustomLinks(links []CustomLink) ([]CustomLink, error) {
	if len(links) == 0 {
		return nil, nil
	}
	if len(links) > MaxSubscriptionCustomLinks {
		return nil, fmt.Errorf("subscription: 一个订阅最多添加 %d 个自定义链接", MaxSubscriptionCustomLinks)
	}
	out := make([]CustomLink, 0, len(links))
	seen := make(map[string]int, len(links))
	for _, item := range links {
		norm, err := NormalizeAndValidateCustomLink(item)
		if err != nil {
			return nil, err
		}
		if idx, exists := seen[norm.URL]; exists {
			if out[idx].Label == "" && norm.Label != "" {
				out[idx].Label = norm.Label
			}
			continue
		}
		seen[norm.URL] = len(out)
		out = append(out, norm)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// normalizeCustomLinksForLoad sanitizes persisted or imported custom links on
// read, dropping any invalid or non-HTTP(S) entries rather than failing startup.
func normalizeCustomLinksForLoad(links []CustomLink) []CustomLink {
	if len(links) == 0 {
		return nil
	}
	out := make([]CustomLink, 0, min(len(links), MaxSubscriptionCustomLinks))
	seen := make(map[string]int, len(links))
	for _, item := range links {
		cleanURL, err := ValidateCustomLinkURL(item.URL)
		if err != nil {
			continue
		}
		lbl := strings.Map(func(r rune) rune {
			if r < 0x20 || r == 0x7f {
				return -1
			}
			return r
		}, strings.TrimSpace(item.Label))
		if utf8.RuneCountInString(lbl) > MaxCustomLinkLabelRunes {
			runes := []rune(lbl)
			lbl = string(runes[:MaxCustomLinkLabelRunes])
		}
		if idx, exists := seen[cleanURL]; exists {
			if out[idx].Label == "" && lbl != "" {
				out[idx].Label = lbl
			}
			continue
		}
		if len(out) >= MaxSubscriptionCustomLinks {
			break
		}
		seen[cleanURL] = len(out)
		out = append(out, CustomLink{Label: lbl, URL: cleanURL})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// MergeCustomLinks merges imported into base, keeping base's order and labels
// while appending new valid non-duplicate links up to MaxSubscriptionCustomLinks.
func MergeCustomLinks(base, imported []CustomLink) []CustomLink {
	cleanBase := normalizeCustomLinksForLoad(base)
	cleanImp := normalizeCustomLinksForLoad(imported)
	if len(cleanImp) == 0 {
		return cleanBase
	}
	combined := make([]CustomLink, 0, len(cleanBase)+len(cleanImp))
	combined = append(combined, cleanBase...)
	combined = append(combined, cleanImp...)
	return normalizeCustomLinksForLoad(combined)
}

// DeriveCustomLinkLabel builds a clean, concise "host/path" display label when
// a custom link omits Label (e.g. "https://www.linktr.ee/alice/" -> "linktr.ee/alice").
func DeriveCustomLinkLabel(rawURL string) string {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return ""
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Hostname() == "" {
		return clampCustomLinkRunes(trimmed, MaxDerivedCustomLinkLabelRunes)
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	host = strings.TrimPrefix(host, "www.")
	if port := u.Port(); port != "" && port != "80" && port != "443" {
		host = host + ":" + port
	}
	cleanPath := strings.Trim(u.EscapedPath(), "/")
	if cleanPath != "" {
		if unescaped, uErr := url.PathUnescape(cleanPath); uErr == nil && utf8.ValidString(unescaped) {
			hasCtl := false
			for _, r := range unescaped {
				if r < 0x20 || r == 0x7f {
					hasCtl = true
					break
				}
			}
			if !hasCtl {
				cleanPath = unescaped
			}
		}
	}
	display := host
	if cleanPath != "" {
		display = host + "/" + cleanPath
	}
	return clampCustomLinkRunes(display, MaxDerivedCustomLinkLabelRunes)
}

func clampCustomLinkRunes(s string, maxRunes int) string {
	if maxRunes <= 0 || utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	runes := []rune(s)
	if maxRunes <= 1 {
		return string(runes[:maxRunes])
	}
	return string(runes[:maxRunes-1]) + "…"
}

// SetLinks atomically validates and replaces the custom external links of subID,
// rolling back SQLite and in-memory state if disk persistence fails.
func (m *Manager) SetLinks(subID string, links []CustomLink) (result Subscription, resultErr error) {
	normLinks, err := NormalizeAndValidateCustomLinks(links)
	if err != nil {
		return Subscription{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	before, snapshotErr := m.listSubsOnlyLocked()
	if snapshotErr != nil {
		return Subscription{}, snapshotErr
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, m.restoreSubscriptionViewLocked(before))
		}
	}()

	sub, ok, err := m.findByIDOrSubIDLocked(subID)
	if err != nil || !ok {
		return Subscription{}, fmt.Errorf("subscription: %s not found", subID)
	}
	now := m.nowFunc()
	sub.Links = normLinks
	sub.UpdatedAt = now.UTC().Format(time.RFC3339)
	syncSubFields(&sub, now)
	if err := m.upsertRowLocked(sub); err != nil {
		return Subscription{}, err
	}
	if err := m.writeSubscriptionsToDiskLocked(); err != nil {
		return Subscription{}, err
	}
	return sub, nil
}
