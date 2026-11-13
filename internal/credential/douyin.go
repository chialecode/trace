package credential

import (
	"net/http"
	"strings"
	"time"
)

// AllowedDouyinCookieKeys defines the Douyin identity, companion identity, and access-context
// cookie keys retained from user-pasted Cookie headers (per douyin.md §3.1).
var AllowedDouyinCookieKeys = []string{
	// Core identity (at least one of sessionid or sessionid_ss is required)
	"sessionid",
	"sessionid_ss",
	// Companion Passport identity
	"sid_tt",
	"uid_tt",
	"uid_tt_ss",
	"sid_guard",
	"odin_tt",
	"passport_csrf_token",
	"passport_csrf_token_default",
	"is_staff_user",
	"login_time",
	// Access context and gateway cookies
	"ttwid",
	"UIFID",
	"UIFID_TEMP",
	"uifid",
	"msToken",
	"s_v_web_id",
	"__ac_nonce",
	"__ac_signature",
}

var (
	exactDouyinKeySet = func() map[string]string {
		m := make(map[string]string, len(AllowedDouyinCookieKeys))
		for _, k := range AllowedDouyinCookieKeys {
			m[k] = k
		}
		return m
	}()

	foldDouyinKeyLookup = func() map[string]string {
		m := make(map[string]string, len(AllowedDouyinCookieKeys))
		for _, k := range AllowedDouyinCookieKeys {
			lower := strings.ToLower(k)
			if _, exists := m[lower]; !exists {
				m[lower] = k
			}
		}
		return m
	}()
)

// DouyinProfile holds desensitized account metadata verified via /aweme/v1/web/user/profile/self/.
type DouyinProfile struct {
	UID      string `json:"uid,omitempty"`
	SecUID   string `json:"secUid,omitempty"`
	UniqueID string `json:"uniqueId,omitempty"`
	Uname    string `json:"uname,omitempty"`
	Face     string `json:"face,omitempty"`
}

// IsStrippedDouyinCookieKey reports whether name is a browser-memory-bound dynamic ticket
// that must never be persisted or replayed by TRACE.
func IsStrippedDouyinCookieKey(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" {
		return true
	}
	if strings.HasPrefix(lower, "bd_ticket_guard_") || strings.HasPrefix(lower, "__security_") {
		return true
	}
	switch lower {
	case "_bd_ticket_crypt_cookie", "x_tt_token", "sdk_source_info", "fpk1", "fpk2":
		return true
	default:
		return false
	}
}

func canonicalDouyinCookieKey(rawKey string) (string, bool) {
	trimmed := strings.TrimSpace(rawKey)
	if trimmed == "" || IsStrippedDouyinCookieKey(trimmed) {
		return "", false
	}
	if exact, ok := exactDouyinKeySet[trimmed]; ok {
		return exact, true
	}
	canonical, ok := foldDouyinKeyLookup[strings.ToLower(trimmed)]
	return canonical, ok
}

func hasDouyinSession(cookies map[string]string) bool {
	if len(cookies) == 0 {
		return false
	}
	return cookies["sessionid"] != "" || cookies["sessionid_ss"] != ""
}

// ParseDouyinCookieInput parses and cleans a user-pasted Douyin Cookie header string.
// It enforces the 8 KiB limit, strips dynamic browser-bound tickets (bd_ticket_guard_*,
// _bd_ticket_crypt_cookie, __security_*, x_tt_token, sdk_source_info, fpk1, fpk2),
// retains identity & access-context keys, requires sessionid or sessionid_ss
// (rejecting ttwid/msToken-only guest cookies), and never echoes secret values in errors.
func ParseDouyinCookieInput(rawCookie string) (map[string]string, error) {
	if len(rawCookie) > MaxCookieInputBytes {
		return nil, ErrInputTooLarge
	}
	rawPairs := parseRawCookieHeader(rawCookie)
	cleaned := CleanDouyinCookieMap(rawPairs)
	if !hasDouyinSession(cleaned) {
		return nil, ErrMissingDouyinSession
	}
	return cleaned, nil
}

// CleanDouyinCookieMap filters a map of cookies to Douyin identity and context keys,
// stripping browser-bound dynamic tickets and empty values.
func CleanDouyinCookieMap(in map[string]string) map[string]string {
	out := make(map[string]string)
	// First apply case-folded matches, then exact-case matches so UIFID and uifid never clobber each other.
	for k, v := range in {
		v = strings.TrimSpace(strings.Trim(strings.TrimSpace(v), `"'`))
		if v == "" {
			continue
		}
		trimmedKey := strings.TrimSpace(k)
		if _, isExact := exactDouyinKeySet[trimmedKey]; isExact {
			continue
		}
		if canonical, ok := canonicalDouyinCookieKey(trimmedKey); ok {
			out[canonical] = v
		}
	}
	for k, v := range in {
		v = strings.TrimSpace(strings.Trim(strings.TrimSpace(v), `"'`))
		if v == "" {
			continue
		}
		trimmedKey := strings.TrimSpace(k)
		if exact, isExact := exactDouyinKeySet[trimmedKey]; isExact && !IsStrippedDouyinCookieKey(exact) {
			out[exact] = v
		}
	}
	return out
}

// ExtractDouyinSetCookies extracts allowed Douyin cookies from HTTP Set-Cookie headers.
func ExtractDouyinSetCookies(headers []*http.Cookie) map[string]string {
	raw := make(map[string]string, len(headers))
	for _, c := range headers {
		if c == nil || c.Value == "" {
			continue
		}
		raw[c.Name] = c.Value
	}
	return CleanDouyinCookieMap(raw)
}

// SaveDouyinManual parses, cleans, and persists manually pasted Douyin cookies.
func (s *Store) SaveDouyinManual(rawCookie string) (Status, error) {
	cookies, err := ParseDouyinCookieInput(rawCookie)
	if err != nil {
		return Status{}, err
	}
	return s.SaveVerifiedDouyinManual(cookies, DouyinProfile{})
}

// SaveVerifiedDouyinManual persists cleaned Douyin cookies and verified profile metadata.
func (s *Store) SaveVerifiedDouyinManual(cookies map[string]string, profile DouyinProfile) (Status, error) {
	cleaned := CleanDouyinCookieMap(cookies)
	if !hasDouyinSession(cleaned) {
		return Status{}, ErrMissingDouyinSession
	}
	now := time.Now().UTC().Format(time.RFC3339)
	rec := &Record{
		Version:       1,
		Site:          SiteDouyin,
		Cookies:       cleaned,
		Source:        "manual",
		UpdatedAt:     now,
		ObtainedAt:    now,
		LastCheckedAt: now,
		UID:           strings.TrimSpace(profile.UID),
		SecUID:        strings.TrimSpace(profile.SecUID),
		UniqueID:      strings.TrimSpace(profile.UniqueID),
		Uname:         strings.TrimSpace(profile.Uname),
		Face:          strings.TrimSpace(profile.Face),
		State:         "logged_in",
	}
	if err := s.saveRecord(rec); err != nil {
		return Status{}, err
	}
	return s.Status(), nil
}

// UpdateDouyinProfile updates cached Douyin profile metadata (uid, sec_uid, unique_id, nickname, avatar) and marks state logged_in.
func (s *Store) UpdateDouyinProfile(profile DouyinProfile) (Status, error) {
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
	if strings.TrimSpace(profile.SecUID) != "" {
		next.SecUID = strings.TrimSpace(profile.SecUID)
	}
	if strings.TrimSpace(profile.UniqueID) != "" {
		next.UniqueID = strings.TrimSpace(profile.UniqueID)
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

// MergeContextCookies merges non-identity context cookies (ttwid, msToken, UIFID, __ac_*)
// received from response Set-Cookie headers into an active Douyin credential.
func (s *Store) MergeContextCookies(cookies map[string]string) error {
	cleaned := CleanDouyinCookieMap(cookies)
	if len(cleaned) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rec == nil || s.rec.State == "expired" || s.rec.State == "logged_out" || s.rec.State == "decrypt_failed" {
		return ErrNotLoggedIn
	}
	next := s.cloneRecord(s.rec)
	changed := false
	for _, k := range []string{"ttwid", "UIFID", "UIFID_TEMP", "uifid", "msToken", "s_v_web_id", "__ac_nonce", "__ac_signature"} {
		if v := cleaned[k]; v != "" && next.Cookies[k] != v {
			next.Cookies[k] = v
			changed = true
		}
	}
	if !changed {
		return nil
	}
	next.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return s.persistRecordLocked(next)
}

// UIFID returns the active uifid / UIFID / UIFID_TEMP token for ArgusSecurityPlugin signing, or empty string.
func (s *Store) UIFID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.rec == nil || s.rec.State == "expired" || s.rec.State == "logged_out" || s.rec.State == "decrypt_failed" {
		return ""
	}
	for _, k := range []string{"uifid", "UIFID", "UIFID_TEMP"} {
		if v := s.rec.Cookies[k]; v != "" {
			return v
		}
	}
	return ""
}

// MSToken returns the active msToken cookie value if present, or empty string.
func (s *Store) MSToken() string {
	return s.CookieValue("msToken")
}

// TTWID returns the active ttwid cookie value if present, or empty string.
func (s *Store) TTWID() string {
	return s.CookieValue("ttwid")
}
