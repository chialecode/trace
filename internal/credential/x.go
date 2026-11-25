package credential

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// XManualRenewalUnavailableReason explains why pasted X cookies are never renewed automatically.
const XManualRenewalUnavailableReason = "粘贴的 Cookie 不续期"

// AllowedXCookieKeys defines the explicit case-sensitive whitelist of X
// cookie keys retained by TRACE (x.md §2.1).
var AllowedXCookieKeys = []string{
	// Core identity & CSRF (both required locally)
	"auth_token",
	"ct0",
	// Identity cross-check, companion, and device trust keys (retained when present)
	"twid",
	"guest_id",
	"kdt",
}

var exactXKeySet = func() map[string]struct{} {
	m := make(map[string]struct{}, len(AllowedXCookieKeys))
	for _, k := range AllowedXCookieKeys {
		m[k] = struct{}{}
	}
	return m
}()

// X sensitive field redaction patterns per x.md §2.1 #4 & §15.1 #20:
// covers auth_token, ct0, kdt, twid, guest_id, x-csrf-token headers/JSON/KV,
// and Authorization: Bearer values in raw, JSON, and URL-encoded forms.
var (
	reXNamedKV = regexp.MustCompile(
		`(^|[?&;,:=\s"'(\[{])(auth_token|ct0|kdt|twid|guest_id|(?i:x-csrf-token))=(\[REDACTED\]|"[^"\r\n]*"|'[^'\r\n]*'|[^&\s;'",)\]}<>]+)`,
	)
	reXNamedEncPrefix = regexp.MustCompile(
		`(?i)(^|%3f|%26|%3b|%20|%3d|%3a|%22|%27|[?&;,:=\+\s"'(\[{])((?-i:auth_token|ct0|kdt|twid|guest_id)|x-csrf-token)(%3d)`,
	)
	reXNamedJSON = regexp.MustCompile(
		`("(?:auth_token|ct0|kdt|twid|guest_id|(?i:x-csrf-token))"\s*:\s*")([^"]+)(")`,
	)
	reXCSRFHeader = regexp.MustCompile(
		`(?i)(^|[\s;:,"'(\[{])(x-csrf-token[ \t]*:[ \t]*)(\[REDACTED\]|"[^"\r\n]*"|'[^'\r\n]*'|[^\r\n;\s,"')\]}<>]+)`,
	)
	reXCSRFHeaderEncPrefix = regexp.MustCompile(
		`(?i)(^|%3f|%26|%3b|%20|%3a|%22|%27|[?&;,:=\+\s"'(\[{])(x-csrf-token%3a(?:%20|\+)*)`,
	)
	reXAuthBearerHeader = regexp.MustCompile(
		`(?i)(^|[\s;:,"'(\[{])(authorization[ \t]*:[ \t]*["']?bearer[ \t]+)(\[REDACTED\]|[^\r\n;\s,"')\]}<>]+)`,
	)
	reXAuthBearerJSON = regexp.MustCompile(
		`(?i)("authorization"\s*:\s*"bearer[ \t]+)([^"]+)(")`,
	)
	reXAuthBearerEncPrefix = regexp.MustCompile(
		`(?i)(^|%3f|%26|%3b|%20|%3a|%22|%27|[?&;,:=\+\s"'(\[{])(authorization%3a(?:%20|\+)*bearer(?:%20|\+)+)`,
	)
)

// XProfile holds desensitized account metadata verified via X login confirmation.
type XProfile struct {
	RestID     string `json:"restId,omitempty"`
	ScreenName string `json:"screenName,omitempty"`
	Name       string `json:"name,omitempty"`
	Face       string `json:"face,omitempty"`
}

// IsValidXCT0 reports whether v is a non-empty ASCII alphanumeric X CSRF token (^[A-Za-z0-9]+$).
func IsValidXCT0(v string) bool {
	if v == "" {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}

func hasXSession(cookies map[string]string) bool {
	return len(cookies) > 0 && cookies["auth_token"] != "" && IsValidXCT0(cookies["ct0"])
}

func isSafeXCookieValue(v string) bool {
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

// ParseXTWID parses an optional X twid cookie value ("u=<rest_id>" or URL-encoded "u%3D<rest_id>")
// and returns the normalized 1..20 digit positive uint64 rest_id (leading zeros stripped).
// It never echoes raw input in error messages.
func ParseXTWID(raw string) (string, error) {
	cleaned := trimMatchingQuotes(strings.Trim(raw, " \t"))
	if cleaned == "" || !isSafeXCookieValue(cleaned) {
		return "", ErrInvalidXTWID
	}
	decoded, err := url.PathUnescape(cleaned)
	if err != nil {
		return "", ErrInvalidXTWID
	}
	if !strings.HasPrefix(decoded, "u=") {
		return "", ErrInvalidXTWID
	}
	rawID := decoded[2:]
	if rawID == "" {
		return "", ErrInvalidXTWID
	}
	for i := 0; i < len(rawID); i++ {
		if rawID[i] < '0' || rawID[i] > '9' {
			return "", ErrInvalidXTWID
		}
	}
	norm := strings.TrimLeft(rawID, "0")
	if len(norm) == 0 || len(norm) > 20 {
		return "", ErrInvalidXTWID
	}
	if _, err := strconv.ParseUint(norm, 10, 64); err != nil {
		return "", ErrInvalidXTWID
	}
	return norm, nil
}

// ParseXCookieInput parses and cleans a user-pasted X Cookie string per x.md §2.1.
// It enforces the 64 KiB raw input limit, rejects control characters and header-injection line breaks without
// echoing raw input, strips an optional "Cookie:" prefix, splits by ";" or newlines while preserving
// "=" inside values, keeps the last non-empty value per case-sensitive whitelisted key, strips all
// temporary/tracking/WAF keys (att, gt, personalization_id, guest_id_marketing, guest_id_ads, lang,
// external_referer, __cf_bm, etc.), enforces the 32 KiB cleaned outbound header limit,
// and requires non-empty auth_token and valid alphanumeric ct0.
func ParseXCookieInput(rawCookie string) (map[string]string, error) {
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
		return nil, ErrMissingXAuthToken
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
			if !isSafeXCookieValue(v) {
				return nil, ErrInvalidCookieControlChar
			}
			if _, allowed := exactXKeySet[k]; allowed {
				out[k] = v
			}
		}
		if multiLine && !lineHasPair {
			return nil, ErrInvalidCookieControlChar
		}
	}

	if out["auth_token"] == "" {
		return nil, ErrMissingXAuthToken
	}
	if out["ct0"] == "" {
		return nil, ErrMissingXCT0
	}
	if !IsValidXCT0(out["ct0"]) {
		return nil, ErrInvalidXCT0
	}
	if len(formatOrderedCookieHeader(out, AllowedXCookieKeys)) > MaxCleanedCookieHeaderBytes {
		return nil, ErrCleanedCookieTooLarge
	}
	return out, nil
}

// CleanXCookieMap filters a map of cookies strictly by case-sensitive AllowedXCookieKeys,
// stripping non-whitelisted keys, empty values, control-character values, and malformed ct0 values.
func CleanXCookieMap(in map[string]string) map[string]string {
	out := make(map[string]string)
	for k, v := range in {
		trimmedKey := strings.Trim(k, " \t")
		if _, allowed := exactXKeySet[trimmedKey]; !allowed {
			continue
		}
		v = trimMatchingQuotes(strings.Trim(v, " \t"))
		if !isSafeXCookieValue(v) {
			continue
		}
		if trimmedKey == "ct0" && !IsValidXCT0(v) {
			continue
		}
		out[trimmedKey] = v
	}
	return out
}

// ExtractXSetCookies extracts whitelisted X cookies from HTTP Set-Cookie headers.
func ExtractXSetCookies(headers []*http.Cookie) map[string]string {
	raw := make(map[string]string, len(headers))
	for _, c := range headers {
		if c == nil || c.Value == "" || c.MaxAge < 0 {
			continue
		}
		raw[c.Name] = c.Value
	}
	return CleanXCookieMap(raw)
}

// ExtractXCT0FromSetCookies extracts and validates a server-issued ct0 cookie from
// HTTP Set-Cookie headers, using the last non-empty non-deleting entry matching ^[A-Za-z0-9]+$.
func ExtractXCT0FromSetCookies(headers []*http.Cookie) (string, bool) {
	var candidate string
	for _, c := range headers {
		if c == nil || c.Name != "ct0" || c.MaxAge < 0 {
			continue
		}
		v := trimMatchingQuotes(strings.Trim(c.Value, " \t"))
		if v == "" {
			continue
		}
		candidate = v
	}
	if !IsValidXCT0(candidate) {
		return "", false
	}
	return candidate, true
}

// SaveXManual parses, cleans, and persists manually pasted X cookies.
func (s *Store) SaveXManual(rawCookie string) (Status, error) {
	cookies, err := ParseXCookieInput(rawCookie)
	if err != nil {
		return Status{}, err
	}
	return s.SaveVerifiedXManual(cookies, XProfile{})
}

// SaveVerifiedXManual persists cleaned X cookies and verified profile metadata.
func (s *Store) SaveVerifiedXManual(cookies map[string]string, profile XProfile) (Status, error) {
	if err := s.requireSite(SiteX); err != nil {
		return Status{}, err
	}
	cleaned := CleanXCookieMap(cookies)
	if cleaned["auth_token"] == "" {
		return Status{}, ErrMissingXAuthToken
	}
	if cleaned["ct0"] == "" {
		if rawCT0 := trimMatchingQuotes(strings.Trim(cookies["ct0"], " \t")); rawCT0 != "" {
			return Status{}, ErrInvalidXCT0
		}
		return Status{}, ErrMissingXCT0
	}
	now := time.Now().UTC().Format(time.RFC3339)
	rec := &Record{
		Version:       1,
		Site:          SiteX,
		Cookies:       cleaned,
		Source:        "manual",
		UpdatedAt:     now,
		ObtainedAt:    now,
		LastCheckedAt: now,
		UID:           strings.TrimSpace(profile.RestID),
		UniqueID:      strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(profile.ScreenName), "@")),
		Uname:         strings.TrimSpace(profile.Name),
		Face:          strings.TrimSpace(profile.Face),
		State:         "logged_in",
	}
	if err := s.saveRecord(rec); err != nil {
		return Status{}, err
	}
	return s.Status(), nil
}

// UpdateXProfile updates cached X profile metadata (rest_id, screen_name, name, avatar) and marks state logged_in.
func (s *Store) UpdateXProfile(profile XProfile) (Status, error) {
	if err := s.requireSite(SiteX); err != nil {
		return Status{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rec == nil {
		return s.lastStatus, ErrNotLoggedIn
	}
	now := time.Now().UTC().Format(time.RFC3339)
	next := s.cloneRecord(s.rec)
	if restID := strings.TrimSpace(profile.RestID); restID != "" {
		next.UID = restID
	}
	if handle := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(profile.ScreenName), "@")); handle != "" {
		next.UniqueID = handle
	}
	if name := strings.TrimSpace(profile.Name); name != "" {
		next.Uname = name
	}
	if face := strings.TrimSpace(profile.Face); face != "" {
		next.Face = face
	}
	next.LastCheckedAt = now
	next.State = "logged_in"
	next.Message = ""
	if err := s.persistRecordLocked(next); err != nil {
		return s.buildStatus(s.rec), err
	}
	return s.buildStatus(s.rec), nil
}

// SyncXCT0 validates a server-issued ct0 token (^[A-Za-z0-9]+$) and atomically updates
// ONLY the ct0 cookie in an active X store, rejecting invalid characters without mutating state.
func (s *Store) SyncXCT0(newCT0 string) error {
	if err := s.requireSite(SiteX); err != nil {
		return err
	}
	ct0 := trimMatchingQuotes(strings.Trim(newCT0, " \t"))
	if !IsValidXCT0(ct0) {
		return ErrInvalidXCT0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.isActiveRecordLocked() {
		return ErrNotLoggedIn
	}
	if s.rec.Cookies["ct0"] == ct0 {
		return nil
	}
	next := s.cloneRecord(s.rec)
	next.Cookies["ct0"] = ct0
	next.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return s.persistRecordLocked(next)
}

// CT0 returns the active X ct0 cookie value if present, or empty string.
func (s *Store) CT0() string {
	return s.CookieValue("ct0")
}

// redactXSensitiveFields masks X cookie values (auth_token, ct0, kdt, twid, guest_id),
// x-csrf-token headers/JSON/KV, and Authorization: Bearer values (raw, JSON, and URL-encoded)
// even when not persisted in x.bin.
func redactXSensitiveFields(in string) string {
	if in == "" {
		return ""
	}
	out := in
	out = reXNamedKV.ReplaceAllStringFunc(out, func(m string) string {
		sub := reXNamedKV.FindStringSubmatch(m)
		if len(sub) != 4 {
			return m
		}
		masked, changed := redactQuotedOrRawValue(sub[3])
		if !changed {
			return m
		}
		return sub[1] + sub[2] + "=" + masked
	})
	out = redactEncodedKVByPrefix(out, reXNamedEncPrefix, nil)
	out = reXNamedJSON.ReplaceAllStringFunc(out, func(m string) string {
		sub := reXNamedJSON.FindStringSubmatch(m)
		if len(sub) != 4 || sub[2] == "[REDACTED]" {
			return m
		}
		return sub[1] + "[REDACTED]" + sub[3]
	})
	out = reXCSRFHeader.ReplaceAllStringFunc(out, func(m string) string {
		sub := reXCSRFHeader.FindStringSubmatch(m)
		if len(sub) != 4 {
			return m
		}
		masked, changed := redactQuotedOrRawValue(strings.TrimSpace(sub[3]))
		if !changed {
			return m
		}
		return sub[1] + sub[2] + masked
	})
	out = redactEncodedKVByPrefix(out, reXCSRFHeaderEncPrefix, nil)
	out = reXAuthBearerHeader.ReplaceAllStringFunc(out, func(m string) string {
		sub := reXAuthBearerHeader.FindStringSubmatch(m)
		if len(sub) != 4 || sub[3] == "[REDACTED]" {
			return m
		}
		return sub[1] + sub[2] + "[REDACTED]"
	})
	out = reXAuthBearerJSON.ReplaceAllStringFunc(out, func(m string) string {
		sub := reXAuthBearerJSON.FindStringSubmatch(m)
		if len(sub) != 4 || sub[2] == "[REDACTED]" {
			return m
		}
		return sub[1] + "[REDACTED]" + sub[3]
	})
	out = redactEncodedKVByPrefix(out, reXAuthBearerEncPrefix, nil)
	return out
}
