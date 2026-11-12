package site

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
)

var (
	reBareKnownDomain = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9._/-])((?:v\.douyin\.com|www\.douyin\.com|live\.douyin\.com|iesdouyin\.com|www\.iesdouyin\.com|v\.amemv\.com|webcast\.amemv\.com|mall\.douyin\.com|haohuo\.jinritemai\.com|b23\.tv|www\.b23\.tv|www\.bilibili\.com|m\.bilibili\.com|space\.bilibili\.com|live\.bilibili\.com|t\.bilibili\.com)/[^\s"'<>` + "`" + `]+)`)
	reDouyinGluedCode = regexp.MustCompile(`^(https?://v\.(?:douyin|amemv)\.com/[A-Za-z0-9_-]+)/\d+(?:\.\d+)?/?$`)
	reDouyinSecUID    = regexp.MustCompile(`^MS4wLjAB[A-Za-z0-9_-]+$`)
	reBiliBV          = regexp.MustCompile(`(?i)\bBV1[1-9A-NP-Za-km-z]{9}\b`)
	reBiliAV          = regexp.MustCompile(`(?i)\bav[1-9][0-9]{0,18}\b`)
	reBiliUIDPref     = regexp.MustCompile(`(?i)^uid[:：\s]*[1-9][0-9]{0,18}$`)
	reBiliLivePref    = regexp.MustCompile(`(?i)^live[:：\s]*[1-9][0-9]{0,18}$`)
	rePureDigits      = regexp.MustCompile(`^[1-9][0-9]*$`)
)

var (
	bilibiliDispatchDomains = []string{
		"bilibili.com",
		"b23.tv",
		"acg.tv",
	}
	douyinDispatchDomains = []string{
		"douyin.com",
		"iesdouyin.com",
		"amemv.com",
		"jinritemai.com",
	}
)

// ExtractShareURL extracts the first HTTP(S) or known bare-domain URL from mixed share text,
// stripping glued Chinese/Emoji suffixes, trailing sentence punctuation, and Douyin oral-code tails.
func ExtractShareURL(text string) (string, bool) {
	s := strings.TrimSpace(text)
	if s == "" {
		return "", false
	}

	lower := strings.ToLower(s)
	start := -1
	httpsIdx := strings.Index(lower, "https://")
	httpIdx := strings.Index(lower, "http://")
	switch {
	case httpsIdx >= 0 && (httpIdx < 0 || httpsIdx <= httpIdx):
		start = httpsIdx
	case httpIdx >= 0:
		start = httpIdx
	}

	var candidate string
	if start >= 0 {
		candidate = scanASCIIURLToken(s[start:])
	} else if loc := reBareKnownDomain.FindStringSubmatchIndex(s); len(loc) >= 4 {
		candidate = "https://" + scanASCIIURLToken(s[loc[2]:])
	}
	if candidate == "" {
		return "", false
	}

	candidate = strings.TrimRight(candidate, ".,;:!?)]}")
	if candidate == "https://" || candidate == "http://" {
		return "", false
	}

	if m := reDouyinGluedCode.FindStringSubmatch(candidate); len(m) == 2 {
		candidate = m[1] + "/"
	}
	return candidate, true
}

func scanASCIIURLToken(s string) string {
	for i, r := range s {
		if r <= 0x20 || r >= 0x7f {
			return s[:i]
		}
		switch r {
		case '"', '\'', '<', '>', '`', '\\':
			return s[:i]
		}
	}
	return s
}

// DetectHostSite maps a URL hostname to a supported site ID, or returns ("", false).
func DetectHostSite(host string) (ID, bool) {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "" {
		return "", false
	}
	if MatchDomainSuffix(h, bilibiliDispatchDomains) {
		return Bilibili, true
	}
	if MatchDomainSuffix(h, douyinDispatchDomains) {
		return Douyin, true
	}
	return "", false
}

// Registry dispatches pasted share text, URLs, and site tokens to per-site Resolvers.
type Registry struct {
	mu        sync.RWMutex
	resolvers map[ID]Resolver
}

// NewRegistry creates an empty site Registry.
func NewRegistry(resolvers ...Resolver) *Registry {
	r := &Registry{
		resolvers: make(map[ID]Resolver, len(resolvers)),
	}
	for _, res := range resolvers {
		r.Register(res)
	}
	return r
}

// Register adds or replaces a site Resolver in the registry.
func (r *Registry) Register(res Resolver) {
	if res == nil {
		return
	}
	r.mu.Lock()
	r.resolvers[res.SiteID()] = res
	r.mu.Unlock()
}

// Resolver returns the registered Resolver for id, if present.
func (r *Registry) Resolver(id ID) (Resolver, bool) {
	r.mu.RLock()
	res, ok := r.resolvers[id]
	r.mu.RUnlock()
	return res, ok
}

// DetectSite determines which site owns rawInput and returns the normalized input passed to that site's Resolver.
// Ambiguous bare 19-digit numbers and unsupported external domains are rejected locally with zero network requests.
func (r *Registry) DetectSite(rawInput string) (ID, string, error) {
	input := strings.TrimSpace(rawInput)
	if input == "" {
		return "", "", &Error{
			Kind:    ErrKindBadRequest,
			Message: "请输入有效的分享链接或站点标识",
		}
	}

	if extracted, ok := ExtractShareURL(input); ok {
		u, err := url.Parse(extracted)
		if err != nil || u.Host == "" {
			return "", "", &Error{
				Kind:    ErrKindBadRequest,
				Message: "无效的链接格式",
			}
		}

		// Allow registered resolvers to claim loopback fixture URLs first.
		r.mu.RLock()
		for _, id := range All() {
			if res, exists := r.resolvers[id]; exists {
				if matcher, ok := res.(URLMatcher); ok && matcher.MatchURL(u) {
					r.mu.RUnlock()
					return id, extracted, nil
				}
			}
		}
		r.mu.RUnlock()

		host := strings.ToLower(u.Hostname())
		if siteID, matched := DetectHostSite(host); matched {
			return siteID, extracted, nil
		}
		return "", "", &Error{
			Kind:        ErrKindBadRequest,
			Unsupported: true,
			Message:     fmt.Sprintf("暂不支持该站点域名: %s", host),
		}
	}

	// Non-URL bare tokens: check unambiguous site prefixes and identifiers.
	if input == "/user/self" || strings.EqualFold(input, "user/self") || reDouyinSecUID.MatchString(input) {
		return Douyin, input, nil
	}
	if reBiliUIDPref.MatchString(input) || reBiliLivePref.MatchString(input) || reBiliBV.MatchString(input) || reBiliAV.MatchString(input) {
		return Bilibili, input, nil
	}
	if rePureDigits.MatchString(input) {
		if len(input) == 19 {
			return "", "", &Error{
				Kind:        ErrKindBadRequest,
				Unsupported: true,
				Message:     "纯 19 位数字无法区分作品、直播场次或动态，请粘贴完整分享链接",
			}
		}
		if len(input) < 19 {
			return Bilibili, input, nil
		}
		return "", "", &Error{
			Kind:        ErrKindBadRequest,
			Unsupported: true,
			Message:     "无法识别过长的纯数字编号，请粘贴完整分享链接",
		}
	}

	return "", "", &Error{
		Kind:    ErrKindBadRequest,
		Message: "无法识别该输入，请粘贴完整的分享链接",
	}
}

// Resolve extracts and dispatches rawInput to the owning site's registered Resolver.
func (r *Registry) Resolve(ctx context.Context, rawInput string) (ResolveTarget, error) {
	siteID, normalizedInput, err := r.DetectSite(rawInput)
	if err != nil {
		return ResolveTarget{}, err
	}
	res, ok := r.Resolver(siteID)
	if !ok {
		return ResolveTarget{}, &Error{
			Site:        siteID,
			Kind:        ErrKindUnavailable,
			Unsupported: true,
			Message:     fmt.Sprintf("当前尚未启用 %s 站点解析器", siteID.DisplayName()),
		}
	}
	target, err := res.Resolve(ctx, normalizedInput)
	if err != nil {
		return target, err
	}
	if target.Site == "" {
		target.Site = siteID
	}
	return target, nil
}
