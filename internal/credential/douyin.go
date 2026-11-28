package credential

import (
	"net/http"
	"sort"
	"strings"
	"time"
)

// AllowedDouyinCookieKeys defines the prioritized primary Douyin identity, companion identity,
// and access-context cookie keys emitted first in outbound Cookie headers (per douyin.md §3.1).
// Additional safe RFC 6265 cookies not matching IsStrippedDouyinCookieKey (such as UCP/SSO
// sid_ucp_v1, ssid_ucp_v1, sso_uid_tt, d_ticket, x-web-secsdk-uid, _waftokenid, UIFIDTEMP)
// are also retained with original case and appended in deterministic lexicographical order.
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

// douyinUIFIDKeys mirrors sign.ExtractUIFID's exact 6-key lookup priority (#sign-spec §A.7.2).
var douyinUIFIDKeys = [...]string{
	"uifid",
	"uifid_temp",
	"uifidtemp",
	"UIFID",
	"UIFID_TEMP",
	"UIFIDTEMP",
}

// douyinMergeableContextKeys lists non-identity access-context and gateway cookies that may
// be refreshed from response Set-Cookie headers without ever overwriting core identity keys.
var douyinMergeableContextKeys = [...]string{
	"ttwid",
	"UIFID",
	"UIFID_TEMP",
	"UIFIDTEMP",
	"uifid",
	"uifid_temp",
	"uifidtemp",
	"msToken",
	"s_v_web_id",
	"__ac_nonce",
	"__ac_signature",
	"d_ticket",
	"x-web-secsdk-uid",
	"_waftokenid",
	"ttcid",
	"tt_scid",
}

var (
	exactDouyinKeySet = func() map[string]string {
		m := make(map[string]string, len(AllowedDouyinCookieKeys)+len(douyinUIFIDKeys))
		for _, k := range AllowedDouyinCookieKeys {
			m[k] = k
		}
		for _, k := range douyinUIFIDKeys {
			m[k] = k
		}
		return m
	}()

	foldDouyinKeyLookup = func() map[string]string {
		m := make(map[string]string, len(AllowedDouyinCookieKeys))
		for _, k := range AllowedDouyinCookieKeys {
			lower := strings.ToLower(k)
			if strings.HasPrefix(lower, "uifid") {
				// Preserve exact case across all 6 UIFID/uifid* variants without folding.
				continue
			}
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

// DouyinGuardState holds client-owned ECDSA P-256 keypair material and server-issued
// bd-ticket-guard tokens bound to the active Douyin login session inside douyin.bin.
// It is stored encrypted via DPAPI and must never appear in Status, logs, errors, or receipts.
type DouyinGuardState struct {
	PrivateKeyPKCS8 string `json:"private_key_pkcs8,omitempty"`
	PublicKeySEC1   string `json:"public_key_sec1,omitempty"`
	Ticket          string `json:"ticket,omitempty"`
	TsSign          string `json:"ts_sign,omitempty"`
	ClientCert      string `json:"client_cert,omitempty"`
	UpdatedAt       string `json:"updated_at,omitempty"`
}

func cloneDouyinGuardState(in *DouyinGuardState) *DouyinGuardState {
	if in == nil {
		return nil
	}
	cp := &DouyinGuardState{
		PrivateKeyPKCS8: strings.TrimSpace(in.PrivateKeyPKCS8),
		PublicKeySEC1:   strings.TrimSpace(in.PublicKeySEC1),
		Ticket:          strings.TrimSpace(in.Ticket),
		TsSign:          strings.TrimSpace(in.TsSign),
		ClientCert:      strings.TrimSpace(in.ClientCert),
		UpdatedAt:       strings.TrimSpace(in.UpdatedAt),
	}
	if cp.PrivateKeyPKCS8 == "" && cp.PublicKeySEC1 == "" && cp.Ticket == "" && cp.TsSign == "" && cp.ClientCert == "" {
		return nil
	}
	return cp
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

func isDouyinIdentityCookieKey(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if strings.HasPrefix(lower, "passport_") || strings.HasPrefix(lower, "sso_") || strings.HasPrefix(lower, "toutiao_sso_") {
		return true
	}
	switch lower {
	case "sessionid", "sessionid_ss", "sid_tt", "uid_tt", "uid_tt_ss",
		"sid_guard", "odin_tt", "is_staff_user", "login_time",
		"sid_ucp_v1", "ssid_ucp_v1", "n_mh":
		return true
	default:
		return false
	}
}

func isSafeDouyinCookieValue(v string) bool {
	if v == "" {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c <= 0x20 || c >= 0x7F || c == ';' {
			return false
		}
	}
	return true
}

func canonicalDouyinCookieKey(rawKey string) (string, bool) {
	trimmed := strings.TrimSpace(rawKey)
	if !isSafeCookieKey(trimmed) || IsStrippedDouyinCookieKey(trimmed) {
		return "", false
	}
	if exact, ok := exactDouyinKeySet[trimmed]; ok {
		return exact, true
	}
	if canonical, ok := foldDouyinKeyLookup[strings.ToLower(trimmed)]; ok {
		return canonical, true
	}
	return trimmed, true
}

func hasDouyinSession(cookies map[string]string) bool {
	if len(cookies) == 0 {
		return false
	}
	return cookies["sessionid"] != "" || cookies["sessionid_ss"] != ""
}

// FormatDouyinCookieHeader serializes cleaned Douyin cookies for outbound Cookie headers:
// primary keys in AllowedDouyinCookieKeys order first, followed by remaining non-blacklisted
// safe RFC 6265 keys in deterministic lexicographical order.
func FormatDouyinCookieHeader(cookies map[string]string) string {
	if len(cookies) == 0 {
		return ""
	}
	emitted := make(map[string]bool, len(cookies))
	pairs := make([]string, 0, len(cookies))
	for _, k := range AllowedDouyinCookieKeys {
		v := cookies[k]
		if v == "" || !isSafeCookieKey(k) || !isSafeDouyinCookieValue(v) || IsStrippedDouyinCookieKey(k) {
			continue
		}
		pairs = append(pairs, k+"="+v)
		emitted[k] = true
	}
	var extraKeys []string
	for k, v := range cookies {
		if emitted[k] || v == "" || !isSafeCookieKey(k) || !isSafeDouyinCookieValue(v) || IsStrippedDouyinCookieKey(k) {
			continue
		}
		extraKeys = append(extraKeys, k)
	}
	sort.Strings(extraKeys)
	for _, k := range extraKeys {
		pairs = append(pairs, k+"="+cookies[k])
	}
	return strings.Join(pairs, "; ")
}

// ParseDouyinCookieInput parses and cleans a user-pasted Douyin Cookie header string.
// It enforces the 64 KiB raw input limit, strips dynamic browser-bound tickets (bd_ticket_guard_*,
// _bd_ticket_crypt_cookie, __security_*, x_tt_token, sdk_source_info, fpk1, fpk2),
// validates RFC 6265 safe key/value characters, retains all non-blacklisted cookies,
// requires sessionid or sessionid_ss (rejecting ttwid/msToken-only guest cookies),
// enforces the 32 KiB cleaned outbound header limit, and never echoes secret values in errors.
func ParseDouyinCookieInput(rawCookie string) (map[string]string, error) {
	if len(rawCookie) > MaxCookieInputBytes {
		return nil, ErrInputTooLarge
	}
	rawPairs := parseRawCookieHeader(rawCookie)
	cleaned := CleanDouyinCookieMap(rawPairs)
	if !hasDouyinSession(cleaned) {
		return nil, ErrMissingDouyinSession
	}
	if len(FormatDouyinCookieHeader(cleaned)) > MaxCleanedCookieHeaderBytes {
		return nil, ErrCleanedCookieTooLarge
	}
	return cleaned, nil
}

// CleanDouyinCookieMap filters a map of Douyin cookies by stripping browser-bound dynamic
// tickets (IsStrippedDouyinCookieKey), rejecting unsafe RFC 6265 keys or values, and retaining
// all other valid identity and context cookies while preserving original key case.
func CleanDouyinCookieMap(in map[string]string) map[string]string {
	out := make(map[string]string)
	// First apply case-folded backbone matches and arbitrary extension keys,
	// then apply exact-case backbone/UIFID matches so exact keys win on case collisions.
	for k, v := range in {
		v = strings.TrimSpace(strings.Trim(strings.TrimSpace(v), `"'`))
		if !isSafeDouyinCookieValue(v) {
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
		if !isSafeDouyinCookieValue(v) {
			continue
		}
		trimmedKey := strings.TrimSpace(k)
		if exact, isExact := exactDouyinKeySet[trimmedKey]; isExact && isSafeCookieKey(exact) && !IsStrippedDouyinCookieKey(exact) {
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

// SaveVerifiedDouyinManual persists cleaned Douyin cookies and verified profile metadata,
// clearing any previous session's ticket-guard keypair and tokens.
func (s *Store) SaveVerifiedDouyinManual(cookies map[string]string, profile DouyinProfile) (Status, error) {
	return s.SaveVerifiedDouyinManualWithGuard(cookies, profile, nil)
}

// SaveVerifiedDouyinManualWithGuard persists cleaned Douyin cookies, verified profile metadata,
// and an optional client-owned bd-ticket-guard state bound to this session.
func (s *Store) SaveVerifiedDouyinManualWithGuard(cookies map[string]string, profile DouyinProfile, guardState *DouyinGuardState) (Status, error) {
	if err := s.requireSite(SiteDouyin); err != nil {
		return Status{}, err
	}
	cleaned := CleanDouyinCookieMap(cookies)
	if !hasDouyinSession(cleaned) {
		return Status{}, ErrMissingDouyinSession
	}
	now := time.Now().UTC().Format(time.RFC3339)
	s.mu.Lock()
	defer s.mu.Unlock()
	prevWebID := ""
	if s.rec != nil {
		prevWebID = strings.TrimSpace(s.rec.DouyinWebID)
	}
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
		DouyinGuard:   cloneDouyinGuardState(guardState),
		DouyinWebID:   prevWebID,
	}
	if err := s.persistRecordLocked(rec); err != nil {
		return Status{}, err
	}
	return s.buildStatus(s.rec), nil
}

// DouyinWebID returns the persisted Douyin webid for an active Douyin store, or empty string.
func (s *Store) DouyinWebID() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.site != SiteDouyin || !s.isActiveRecordLocked() {
		return ""
	}
	return strings.TrimSpace(s.rec.DouyinWebID)
}

// SaveDouyinWebID persists webID on an active Douyin credential store without changing schema version.
func (s *Store) SaveDouyinWebID(webID string) error {
	if err := s.requireSite(SiteDouyin); err != nil {
		return err
	}
	webID = strings.TrimSpace(webID)
	if webID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.isActiveRecordLocked() {
		return ErrNotLoggedIn
	}
	if strings.TrimSpace(s.rec.DouyinWebID) == webID {
		return nil
	}
	next := s.cloneRecord(s.rec)
	next.DouyinWebID = webID
	next.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return s.persistRecordLocked(next)
}

// EnsureDouyinWebID returns the persisted Douyin webid when an active Douyin credential exists,
// or generates and persists one via generate on first use. Returns ("", nil) when logged out.
func (s *Store) EnsureDouyinWebID(generate func() (string, error)) (string, error) {
	if s == nil {
		return "", nil
	}
	if err := s.requireSite(SiteDouyin); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.isActiveRecordLocked() {
		return "", nil
	}
	if existing := strings.TrimSpace(s.rec.DouyinWebID); existing != "" {
		return existing, nil
	}
	if generate == nil {
		return "", nil
	}
	gen, err := generate()
	if err != nil {
		return "", err
	}
	gen = strings.TrimSpace(gen)
	if gen == "" {
		return "", nil
	}
	next := s.cloneRecord(s.rec)
	next.DouyinWebID = gen
	next.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := s.persistRecordLocked(next); err != nil {
		return "", err
	}
	return gen, nil
}

// DouyinGuard returns a copy of the stored bd-ticket-guard state for an active Douyin store, or nil.
func (s *Store) DouyinGuard() *DouyinGuardState {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.site != SiteDouyin || !s.isActiveRecordLocked() || s.rec.DouyinGuard == nil {
		return nil
	}
	return cloneDouyinGuardState(s.rec.DouyinGuard)
}

// SaveDouyinGuard updates the encrypted bd-ticket-guard session state on an active Douyin store.
func (s *Store) SaveDouyinGuard(guardState *DouyinGuardState) error {
	if err := s.requireSite(SiteDouyin); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.isActiveRecordLocked() {
		return ErrNotLoggedIn
	}
	next := s.cloneRecord(s.rec)
	next.DouyinGuard = cloneDouyinGuardState(guardState)
	next.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return s.persistRecordLocked(next)
}

// DouyinGuardSecrets returns all non-empty raw guard secret strings currently stored
// for use by in-process redactors and receipt secret scanners (never exposed to UI or logs).
func (s *Store) DouyinGuardSecrets() []string {
	g := s.DouyinGuard()
	if g == nil {
		return nil
	}
	var out []string
	for _, v := range []string{g.PrivateKeyPKCS8, g.PublicKeySEC1, g.Ticket, g.TsSign, g.ClientCert} {
		if len(v) >= 4 {
			out = appendSecretVariants(out, v)
		}
	}
	return out
}

// UpdateDouyinProfile updates cached Douyin profile metadata (uid, sec_uid, unique_id, nickname, avatar) and marks state logged_in.
func (s *Store) UpdateDouyinProfile(profile DouyinProfile) (Status, error) {
	if err := s.requireSite(SiteDouyin); err != nil {
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

// MergeContextCookies merges non-identity context cookies received from response
// Set-Cookie headers into an active credential without touching core identity cookies:
//   - douyin: ttwid, UIFID*, uifid*, msToken, s_v_web_id, __ac_nonce, __ac_signature,
//     d_ticket, x-web-secsdk-uid, _waftokenid, ttcid, tt_scid
//   - weibo:  XSRF-TOKEN only
//   - x:      ct0 only (validated against ^[A-Za-z0-9]+$; invalid values return ErrInvalidXCT0)
func (s *Store) MergeContextCookies(cookies map[string]string) error {
	switch s.Site() {
	case SiteDouyin:
		cleaned := CleanDouyinCookieMap(cookies)
		if len(cleaned) == 0 {
			return nil
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if !s.isActiveRecordLocked() {
			return ErrNotLoggedIn
		}
		next := s.cloneRecord(s.rec)
		changed := false
		for _, k := range douyinMergeableContextKeys {
			if isDouyinIdentityCookieKey(k) {
				continue
			}
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

	case SiteWeibo:
		cleaned := CleanWeiboCookieMap(cookies)
		xsrf := cleaned["XSRF-TOKEN"]
		if xsrf == "" {
			return nil
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if !s.isActiveRecordLocked() {
			return ErrNotLoggedIn
		}
		if s.rec.Cookies["XSRF-TOKEN"] == xsrf {
			return nil
		}
		next := s.cloneRecord(s.rec)
		next.Cookies["XSRF-TOKEN"] = xsrf
		next.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		return s.persistRecordLocked(next)

	case SiteX:
		rawCT0, hasCT0 := cookies["ct0"]
		if !hasCT0 {
			return nil
		}
		return s.SyncXCT0(rawCT0)

	default:
		return s.requireSite(SiteDouyin)
	}
}

// UIFID returns the active uifid / uifid_temp / uifidtemp / UIFID / UIFID_TEMP / UIFIDTEMP
// token for ArgusSecurityPlugin signing (#sign-spec §A.7.2), or empty string.
func (s *Store) UIFID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.isActiveRecordLocked() {
		return ""
	}
	for _, k := range douyinUIFIDKeys {
		if v := strings.TrimSpace(s.rec.Cookies[k]); v != "" {
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
