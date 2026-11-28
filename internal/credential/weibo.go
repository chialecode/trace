package credential

import (
	"net/http"
	"regexp"
	"strings"
	"time"
)

// WeiboManualRenewalUnavailableReason explains why pasted Weibo cookies are never renewed automatically.
const WeiboManualRenewalUnavailableReason = "粘贴的 Cookie 不续期"

// WeiboQRRenewalUnavailableReason explains why QR-obtained Weibo web sessions are not renewed automatically.
const WeiboQRRenewalUnavailableReason = "微博扫码登录暂不支持自动续期"

// AllowedWeiboCookieKeys defines the explicit case-sensitive whitelist of Weibo
// identity, companion, and mobile-compat cookie keys retained by TRACE (weibo.md §2.2).
var AllowedWeiboCookieKeys = []string{
	// Core identity (required)
	"SUB",
	// Core companion session cookies
	"SUBP",
	"ALF",
	"SCF",
	"WBPSESS",
	"XSRF-TOKEN",
	"SSOLoginState",
	"SUHB",
	"PC_TOKEN",
	// Mobile H5 compatibility keys (kept when present)
	"MLOGIN",
	"_T_WM",
}

var exactWeiboKeySet = func() map[string]struct{} {
	m := make(map[string]struct{}, len(AllowedWeiboCookieKeys))
	for _, k := range AllowedWeiboCookieKeys {
		m[k] = struct{}{}
	}
	return m
}()

// Weibo sensitive field redaction patterns per weibo.md §2.2 #4:
// covers SUB, SUBP, SCF, ALF, ALC, WBPSESS, XSRF-TOKEN, X-XSRF-TOKEN,
// SSOLoginState, SUHB, PC_TOKEN, _T_WM, plus QR/SSO transient secrets
// (qrid, st, alt, ticket) and URL-encoded forms.
var (
	reWeiboNamedKV = regexp.MustCompile(
		`(^|[?&;,:=\s"'(\[{])(SUB|SUBP|SCF|ALF|ALC|WBPSESS|XSRF-TOKEN|X-XSRF-TOKEN|SSOLoginState|SUHB|PC_TOKEN|_T_WM)=(\[REDACTED\]|"[^"\r\n]*"|'[^'\r\n]*'|[^&\s;'",)\]}<>]+)`,
	)
	reWeiboNamedEncPrefix = regexp.MustCompile(
		`(?i)(^|%3f|%26|%3b|%20|%3d|%3a|%22|%27|[?&;,:=\+\s"'(\[{])((?-i:SUB|SUBP|SCF|ALF|ALC|WBPSESS|XSRF-TOKEN|X-XSRF-TOKEN|SSOLoginState|SUHB|PC_TOKEN|_T_WM))(%3d)`,
	)
	reWeiboNamedJSON = regexp.MustCompile(
		`("(?:SUB|SUBP|SCF|ALF|ALC|WBPSESS|XSRF-TOKEN|X-XSRF-TOKEN|SSOLoginState|SUHB|PC_TOKEN|_T_WM)"\s*:\s*")([^"]+)(")`,
	)
	reWeiboXSRFHeader = regexp.MustCompile(
		`(?i)(^|[\s;:,"'(\[{])(X-XSRF-TOKEN[ \t]*:[ \t]*)(\[REDACTED\]|"[^"\r\n]*"|'[^'\r\n]*'|[^\r\n;\s,"')\]}<>]+)`,
	)
	reWeiboShortTokenKV = regexp.MustCompile(
		`(^|[?&;,:=\s"'(\[{])(st|alt|ticket|qrid|QRID)=(\[REDACTED\]|"[^"\r\n]*"|'[^'\r\n]*'|[^&\s;'",)\]}<>]+)`,
	)
	reWeiboShortTokenEncPrefix = regexp.MustCompile(
		`(?i)(^|%3f|%26|%3b|%20|%3d|%3a|%22|%27|[?&;,:=\+\s"'(\[{])((?-i:st|alt|ticket|qrid|QRID))(%3d)`,
	)
	reWeiboShortTokenJSON = regexp.MustCompile(
		`("(?:st|alt|ticket|qrid|QRID)"\s*:\s*")([^"]+)(")`,
	)
)

// WeiboProfile holds desensitized account metadata verified via Weibo login confirmation.
type WeiboProfile struct {
	UID   string `json:"uid,omitempty"`
	Uname string `json:"uname,omitempty"`
	Face  string `json:"face,omitempty"`
}

func hasWeiboSession(cookies map[string]string) bool {
	return len(cookies) > 0 && cookies["SUB"] != ""
}

func isSafeWeiboCookieValue(v string) bool {
	if v == "" {
		return false
	}
	for _, r := range v {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || r == ';' {
			return false
		}
	}
	return true
}

func isValidCookieTokenName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c <= 0x20 || c >= 0x7f {
			return false
		}
		switch c {
		case '(', ')', '<', '>', '@', ',', ';', ':', '\\', '"', '/', '[', ']', '?', '=', '{', '}':
			return false
		}
	}
	return true
}

func trimMatchingQuotes(v string) string {
	if len(v) >= 2 && ((v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'')) {
		return v[1 : len(v)-1]
	}
	return v
}

// ParseWeiboCookieInput parses and cleans a user-pasted Weibo Cookie string per weibo.md §2.2.
// It enforces the 64 KiB raw input limit, rejects control characters and header-injection line breaks without
// echoing raw input, strips an optional "Cookie:" prefix, splits by ";" or newlines while preserving
// "=" inside values, keeps the last non-empty value per case-sensitive whitelisted key, strips all
// stats/UI fields, enforces the 32 KiB cleaned outbound header limit, and requires a non-empty SUB cookie.
func ParseWeiboCookieInput(rawCookie string) (map[string]string, error) {
	if len(rawCookie) > MaxCookieInputBytes {
		return nil, ErrInputTooLarge
	}

	// Reject control characters (except horizontal tab and CRLF/LF line separators)
	// and reject bare CR not followed by LF.
	for i := 0; i < len(rawCookie); i++ {
		b := rawCookie[i]
		if b == '\r' {
			if i+1 >= len(rawCookie) || rawCookie[i+1] != '\n' {
				return nil, ErrInvalidCookieControlChar
			}
			continue
		}
		if b == '\n' || b == '\t' {
			continue
		}
		if b < 0x20 || b == 0x7f {
			return nil, ErrInvalidCookieControlChar
		}
	}
	for _, r := range rawCookie {
		if r >= 0x80 && r <= 0x9f {
			return nil, ErrInvalidCookieControlChar
		}
	}

	normalized := strings.ReplaceAll(rawCookie, "\r\n", "\n")
	rawLines := strings.Split(normalized, "\n")
	lines := make([]string, 0, len(rawLines))
	for _, line := range rawLines {
		trimmed := strings.Trim(line, " \t")
		if trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	if len(lines) == 0 {
		return nil, ErrMissingWeiboSub
	}

	hasHeaderPrefix := strings.HasPrefix(strings.ToLower(lines[0]), "cookie:")
	if hasHeaderPrefix {
		// An HTTP Cookie: header must not contain embedded line breaks to additional lines.
		if len(lines) > 1 {
			return nil, ErrInvalidCookieControlChar
		}
		lines[0] = strings.Trim(lines[0][len("cookie:"):], " \t")
	}

	multiLine := len(lines) > 1
	out := make(map[string]string)
	for _, line := range lines {
		parts := strings.Split(line, ";")
		lineHasPair := false
		for _, part := range parts {
			part = strings.Trim(part, " \t")
			if part == "" {
				continue
			}
			eq := strings.IndexByte(part, '=')
			if eq <= 0 {
				if multiLine || strings.Contains(part, ":") {
					return nil, ErrInvalidCookieControlChar
				}
				continue
			}
			k := strings.Trim(part[:eq], " \t")
			if k == "" || strings.ContainsAny(k, ": \t") || (multiLine && !isValidCookieTokenName(k)) {
				return nil, ErrInvalidCookieControlChar
			}
			v := trimMatchingQuotes(strings.Trim(part[eq+1:], " \t"))
			lineHasPair = true
			if v == "" {
				// Preserve earlier non-empty value when a duplicate key has an empty value.
				continue
			}
			if !isSafeWeiboCookieValue(v) {
				return nil, ErrInvalidCookieControlChar
			}
			if _, allowed := exactWeiboKeySet[k]; allowed {
				out[k] = v
			}
		}
		if multiLine && !lineHasPair {
			return nil, ErrInvalidCookieControlChar
		}
	}

	if !hasWeiboSession(out) {
		return nil, ErrMissingWeiboSub
	}
	if len(formatOrderedCookieHeader(out, AllowedWeiboCookieKeys)) > MaxCleanedCookieHeaderBytes {
		return nil, ErrCleanedCookieTooLarge
	}
	return out, nil
}

// CleanWeiboCookieMap filters a map of cookies strictly by case-sensitive AllowedWeiboCookieKeys,
// stripping stats/UI keys, empty values, and values with control characters or semicolons.
func CleanWeiboCookieMap(in map[string]string) map[string]string {
	out := make(map[string]string)
	for k, v := range in {
		trimmedKey := strings.Trim(k, " \t")
		if _, allowed := exactWeiboKeySet[trimmedKey]; !allowed {
			continue
		}
		v = trimMatchingQuotes(strings.Trim(v, " \t"))
		if !isSafeWeiboCookieValue(v) {
			continue
		}
		out[trimmedKey] = v
	}
	return out
}

// ExtractWeiboSetCookies extracts whitelisted Weibo cookies from HTTP Set-Cookie headers.
func ExtractWeiboSetCookies(headers []*http.Cookie) map[string]string {
	raw := make(map[string]string, len(headers))
	for _, c := range headers {
		if c == nil || c.Value == "" {
			continue
		}
		raw[c.Name] = c.Value
	}
	return CleanWeiboCookieMap(raw)
}

// SaveWeiboManual parses, cleans, and persists manually pasted Weibo cookies.
func (s *Store) SaveWeiboManual(rawCookie string) (Status, error) {
	cookies, err := ParseWeiboCookieInput(rawCookie)
	if err != nil {
		return Status{}, err
	}
	return s.SaveVerifiedWeiboManual(cookies, WeiboProfile{})
}

// SaveVerifiedWeiboManual persists cleaned Weibo cookies and verified profile metadata
// obtained from a manually pasted browser Cookie header.
func (s *Store) SaveVerifiedWeiboManual(cookies map[string]string, profile WeiboProfile) (Status, error) {
	return s.saveVerifiedWeibo(cookies, profile, "manual")
}

// SaveVerifiedWeiboQR persists cleaned Weibo cookies and verified profile metadata
// obtained from PC Web SSO QR code login. Source is recorded as "qr" with an empty
// RefreshToken because Weibo web sessions are non-renewable (weibo.md §2.3).
func (s *Store) SaveVerifiedWeiboQR(cookies map[string]string, profile WeiboProfile) (Status, error) {
	return s.saveVerifiedWeibo(cookies, profile, "qr")
}

func (s *Store) saveVerifiedWeibo(cookies map[string]string, profile WeiboProfile, source string) (Status, error) {
	if err := s.requireSite(SiteWeibo); err != nil {
		return Status{}, err
	}
	cleaned := CleanWeiboCookieMap(cookies)
	if !hasWeiboSession(cleaned) {
		return Status{}, ErrMissingWeiboSub
	}
	now := time.Now().UTC().Format(time.RFC3339)
	rec := &Record{
		Version:       1,
		Site:          SiteWeibo,
		Cookies:       cleaned,
		RefreshToken:  "",
		Source:        source,
		UpdatedAt:     now,
		ObtainedAt:    now,
		LastCheckedAt: now,
		UID:           strings.TrimSpace(profile.UID),
		Uname:         strings.TrimSpace(profile.Uname),
		Face:          strings.TrimSpace(profile.Face),
		State:         "logged_in",
	}
	if err := s.saveRecord(rec); err != nil {
		return Status{}, err
	}
	return s.Status(), nil
}

// UpdateWeiboProfile updates cached Weibo profile metadata (uid, nickname, avatar) and marks state logged_in.
func (s *Store) UpdateWeiboProfile(profile WeiboProfile) (Status, error) {
	if err := s.requireSite(SiteWeibo); err != nil {
		return Status{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rec == nil {
		return s.lastStatus, ErrNotLoggedIn
	}
	now := time.Now().UTC().Format(time.RFC3339)
	next := s.cloneRecord(s.rec)
	if strings.TrimSpace(profile.UID) != "" {
		next.UID = strings.TrimSpace(profile.UID)
	}
	if strings.TrimSpace(profile.Uname) != "" {
		next.Uname = strings.TrimSpace(profile.Uname)
	}
	if strings.TrimSpace(profile.Face) != "" {
		next.Face = strings.TrimSpace(profile.Face)
	}
	next.LastCheckedAt = now
	next.State = "logged_in"
	next.Message = ""
	if err := s.persistRecordLocked(next); err != nil {
		return s.buildStatus(s.rec), err
	}
	return s.buildStatus(s.rec), nil
}

// XSRFToken returns the active Weibo XSRF-TOKEN cookie value if present, or empty string.
func (s *Store) XSRFToken() string {
	return s.CookieValue("XSRF-TOKEN")
}

func isIgnorableShortTokenValue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "[redacted]", "json", "media", "true", "false", "null":
		return true
	default:
		return false
	}
}

func scanEncodedValueEnd(s string, start int) int {
	i := start
	for i < len(s) {
		c := s[i]
		if c <= 0x20 || c >= 0x7f {
			break
		}
		switch c {
		case '&', ';', '+', '\'', '"', ',', ')', ']', '}', '<', '>':
			return i
		case '%':
			if i+2 < len(s) {
				code := strings.ToLower(s[i : i+3])
				switch code {
				case "%26", "%3b", "%20", "%0a", "%0d", "%22", "%27":
					return i
				}
			}
		}
		i++
	}
	return i
}

func redactEncodedKVByPrefix(in string, prefixRe *regexp.Regexp, skip func(string) bool) string {
	locs := prefixRe.FindAllStringIndex(in, -1)
	if len(locs) == 0 {
		return in
	}
	var b strings.Builder
	b.Grow(len(in))
	cursor := 0
	for _, loc := range locs {
		if loc[0] < cursor {
			continue
		}
		valStart := loc[1]
		if valStart+3 <= len(in) {
			lead := strings.ToLower(in[valStart : valStart+3])
			if lead == "%22" || lead == "%27" {
				valStart += 3
			}
		}
		if strings.HasPrefix(in[valStart:], "[REDACTED]") {
			continue
		}
		valEnd := scanEncodedValueEnd(in, valStart)
		if valEnd <= valStart {
			continue
		}
		val := in[valStart:valEnd]
		if val == "[REDACTED]" || (skip != nil && skip(val)) {
			continue
		}
		b.WriteString(in[cursor:valStart])
		b.WriteString("[REDACTED]")
		cursor = valEnd
	}
	if cursor == 0 {
		return in
	}
	b.WriteString(in[cursor:])
	return b.String()
}

func redactQuotedOrRawValue(val string) (string, bool) {
	if len(val) >= 2 && ((val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'')) {
		inner := val[1 : len(val)-1]
		if inner == "" || inner == "[REDACTED]" {
			return val, false
		}
		q := val[:1]
		return q + "[REDACTED]" + q, true
	}
	if val == "" || val == "[REDACTED]" {
		return val, false
	}
	return "[REDACTED]", true
}

// redactWeiboSensitiveFields masks Weibo cookie and SSO token key=value pairs
// (including URL-encoded and JSON forms) even when not persisted in weibo.bin.
func redactWeiboSensitiveFields(in string) string {
	if in == "" {
		return ""
	}
	out := in
	out = reWeiboNamedKV.ReplaceAllStringFunc(out, func(m string) string {
		sub := reWeiboNamedKV.FindStringSubmatch(m)
		if len(sub) != 4 {
			return m
		}
		masked, changed := redactQuotedOrRawValue(sub[3])
		if !changed {
			return m
		}
		return sub[1] + sub[2] + "=" + masked
	})
	out = redactEncodedKVByPrefix(out, reWeiboNamedEncPrefix, nil)
	out = reWeiboNamedJSON.ReplaceAllStringFunc(out, func(m string) string {
		sub := reWeiboNamedJSON.FindStringSubmatch(m)
		if len(sub) != 4 || sub[2] == "[REDACTED]" {
			return m
		}
		return sub[1] + "[REDACTED]" + sub[3]
	})
	out = reWeiboXSRFHeader.ReplaceAllStringFunc(out, func(m string) string {
		sub := reWeiboXSRFHeader.FindStringSubmatch(m)
		if len(sub) != 4 {
			return m
		}
		masked, changed := redactQuotedOrRawValue(strings.TrimSpace(sub[3]))
		if !changed {
			return m
		}
		return sub[1] + sub[2] + masked
	})
	out = reWeiboShortTokenKV.ReplaceAllStringFunc(out, func(m string) string {
		sub := reWeiboShortTokenKV.FindStringSubmatch(m)
		if len(sub) != 4 {
			return m
		}
		if isIgnorableShortTokenValue(trimMatchingQuotes(sub[3])) {
			return m
		}
		masked, changed := redactQuotedOrRawValue(sub[3])
		if !changed {
			return m
		}
		return sub[1] + sub[2] + "=" + masked
	})
	out = redactEncodedKVByPrefix(out, reWeiboShortTokenEncPrefix, isIgnorableShortTokenValue)
	out = reWeiboShortTokenJSON.ReplaceAllStringFunc(out, func(m string) string {
		sub := reWeiboShortTokenJSON.FindStringSubmatch(m)
		if len(sub) != 4 || isIgnorableShortTokenValue(sub[2]) {
			return m
		}
		return sub[1] + "[REDACTED]" + sub[3]
	})
	return out
}
