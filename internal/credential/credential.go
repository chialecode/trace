package credential

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Site constants and per-site DPAPI entropy strings.
const (
	SiteBilibili = "bilibili"
	SiteDouyin   = "douyin"

	BilibiliFileName = "bilibili.bin"
	DouyinFileName   = "douyin.bin"

	BilibiliEntropy = "trace:bilibili:credential:v1"
	DouyinEntropy   = "trace:douyin:credential:v1"

	// MaxCookieInputBytes is the maximum allowed byte length for manual cookie import.
	MaxCookieInputBytes = 8192
)

// Errors returned by credential operations (never echoing raw secrets).
var (
	ErrInputTooLarge        = errors.New("credential: cookie input exceeds 8 KiB limit")
	ErrMissingRequiredKeys  = errors.New("credential: missing required cookie key SESSDATA")
	ErrMissingDouyinSession = errors.New("credential: missing required cookie key sessionid")
	ErrNotLoggedIn          = errors.New("credential: no active credential")
	ErrUnknownSite          = errors.New("credential: unsupported site")
)

// AllowedCookieKeys defines the strict whitelist of Bilibili cookie keys stored by TRACE.
var AllowedCookieKeys = []string{
	"SESSDATA",
	"bili_jct",
	"DedeUserID",
	"DedeUserID__ckMd5",
	"sid",
}

var allowedKeyLookup = map[string]string{
	"sessdata":          "SESSDATA",
	"bili_jct":          "bili_jct",
	"dedeuserid":        "DedeUserID",
	"dedeuserid__ckmd5": "DedeUserID__ckMd5",
	"sid":               "sid",
}

// Record is the internal encrypted credential structure persisted in <site>.bin.
// It must NEVER be serialized directly to UI responses or logs.
type Record struct {
	Version                int               `json:"version"`
	Site                   string            `json:"site,omitempty"`
	Cookies                map[string]string `json:"cookies"`
	RefreshToken           string            `json:"refresh_token,omitempty"`
	Source                 string            `json:"source"` // "qr" or "manual"
	UpdatedAt              string            `json:"updatedAt"`
	ObtainedAt             string            `json:"obtained_at,omitempty"`
	RenewedAt              string            `json:"renewed_at,omitempty"`
	LastCheckedAt          string            `json:"lastCheckedAt,omitempty"`
	ConfirmPending         bool              `json:"confirm_pending,omitempty"`
	PendingOldRefreshToken string            `json:"pending_old_refresh_token,omitempty"`
	Mid                    int64             `json:"mid,omitempty"`
	UID                    string            `json:"uid,omitempty"`
	SecUID                 string            `json:"secUid,omitempty"`
	UniqueID               string            `json:"uniqueId,omitempty"`
	Uname                  string            `json:"uname,omitempty"`
	Face                   string            `json:"face,omitempty"`
	State                  string            `json:"state,omitempty"`
	Message                string            `json:"message,omitempty"`
}

// Read older encrypted records, while all new writes use the documented keys.
func (r *Record) UnmarshalJSON(raw []byte) error {
	type canonical Record
	var legacy struct {
		*canonical
		RefreshToken   string `json:"refreshToken"`
		ConfirmPending bool   `json:"confirmPending"`
		OldToken       string `json:"pendingOldRefreshToken"`
	}
	legacy.canonical = (*canonical)(r)
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return err
	}
	if r.RefreshToken == "" {
		r.RefreshToken = legacy.RefreshToken
	}
	if legacy.ConfirmPending {
		r.ConfirmPending = true
	}
	if r.PendingOldRefreshToken == "" {
		r.PendingOldRefreshToken = legacy.OldToken
	}
	if r.ObtainedAt == "" {
		r.ObtainedAt = r.UpdatedAt
	}
	return nil
}

// Status is the desensitized account status exposed to the UI and service layer.
type Status struct {
	Site            string `json:"site,omitempty"`
	LoggedIn        bool   `json:"loggedIn"`
	State           string `json:"state"` // "logged_out", "logged_in", "expired", "refresh_failed", "decrypt_failed"
	Source          string `json:"source,omitempty"`
	Mid             int64  `json:"mid,omitempty"`
	UID             string `json:"uid,omitempty"`
	SecUID          string `json:"secUid,omitempty"`
	UniqueID        string `json:"uniqueId,omitempty"`
	Uname           string `json:"uname,omitempty"`
	Face            string `json:"face,omitempty"`
	HasRefreshToken bool   `json:"hasRefreshToken"`
	HasTTWID        bool   `json:"hasTtwid,omitempty"`
	ConfirmPending  bool   `json:"confirmPending,omitempty"`
	UpdatedAt       string `json:"updatedAt,omitempty"`
	LastRenewedAt   string `json:"lastRenewedAt,omitempty"`
	LastCheckedAt   string `json:"lastCheckedAt,omitempty"`
	Message         string `json:"message,omitempty"`
}

// ParseManualCookieInput parses a manual Bilibili Cookie string and optional refresh_token.
// It enforces the 8 KiB limit, filters by the 5-key whitelist, requires SESSDATA,
// and never echoes input values in error messages.
func ParseManualCookieInput(rawCookie string, rawRefreshToken string) (map[string]string, string, error) {
	if len(rawCookie) > MaxCookieInputBytes || len(rawRefreshToken) > MaxCookieInputBytes {
		return nil, "", ErrInputTooLarge
	}

	rawPairs := parseRawCookieHeader(rawCookie)
	cookies := FilterCookieMap(rawPairs)
	if cookies["SESSDATA"] == "" {
		return nil, "", ErrMissingRequiredKeys
	}

	rt := strings.TrimSpace(rawRefreshToken)
	if strings.ContainsAny(rt, " \t\r\n;=") {
		// Reject malformed refresh token strings without echoing them.
		return nil, "", errors.New("credential: invalid refresh_token format")
	}

	return cookies, rt, nil
}

func parseRawCookieHeader(rawCookie string) map[string]string {
	cleaned := strings.TrimSpace(rawCookie)
	if strings.HasPrefix(strings.ToLower(cleaned), "cookie:") {
		cleaned = strings.TrimSpace(cleaned[len("cookie:"):])
	}
	cleaned = strings.ReplaceAll(cleaned, "\r", ";")
	cleaned = strings.ReplaceAll(cleaned, "\n", ";")

	pairs := make(map[string]string)
	for _, part := range strings.Split(cleaned, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		eq := strings.IndexByte(part, '=')
		if eq <= 0 {
			continue
		}
		k := strings.TrimSpace(part[:eq])
		v := strings.TrimSpace(part[eq+1:])
		v = strings.Trim(v, `"'`)
		if k == "" || v == "" {
			continue
		}
		pairs[k] = v
	}
	return pairs
}

// FilterCookieMap filters a map of cookies down to the 5 whitelisted Bilibili keys.
func FilterCookieMap(in map[string]string) map[string]string {
	out := make(map[string]string)
	for k, v := range in {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if canonicalKey, ok := allowedKeyLookup[strings.ToLower(strings.TrimSpace(k))]; ok {
			out[canonicalKey] = v
		}
	}
	return out
}

// Store manages a single site's DPAPI-encrypted credential file on disk.
type Store struct {
	mu         sync.RWMutex
	site       string
	entropy    []byte
	path       string
	rec        *Record
	lastStatus Status
}

// Open loads the Bilibili DPAPI credential store from path using BilibiliEntropy.
// If decryption or JSON parsing fails, the corrupt file is backed up to <path>.corrupt-<timestamp>
// and the store degrades gracefully to "decrypt_failed" (logged_out) without crashing.
func Open(path string) (*Store, string, error) {
	return openWithConfig(SiteBilibili, []byte(BilibiliEntropy), path)
}

// OpenDouyin loads the Douyin DPAPI credential store from path using DouyinEntropy.
func OpenDouyin(path string) (*Store, string, error) {
	return openWithConfig(SiteDouyin, []byte(DouyinEntropy), path)
}

// OpenSite loads the credential store for siteID ("bilibili" or "douyin") from path.
func OpenSite(siteID string, path string) (*Store, string, error) {
	switch normalizeSiteID(siteID) {
	case SiteBilibili:
		return Open(path)
	case SiteDouyin:
		return OpenDouyin(path)
	default:
		return nil, "", ErrUnknownSite
	}
}

func openWithConfig(siteID string, entropy []byte, path string) (*Store, string, error) {
	s := &Store{
		site:    siteID,
		entropy: append([]byte{}, entropy...),
		path:    path,
		lastStatus: Status{
			Site:     siteID,
			LoggedIn: false,
			State:    "logged_out",
		},
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, "", nil
		}
		return nil, "", err
	}

	plain, err := decryptBytesWithEntropy(raw, s.entropy)
	if err != nil {
		backup, bErr := backupCorruptFile(path)
		if bErr != nil {
			return nil, "", bErr
		}
		s.lastStatus = Status{
			Site:     siteID,
			LoggedIn: false,
			State:    "decrypt_failed",
			Message:  "凭证无法解密，已备份原文件并降级为未登录状态",
		}
		return s, backup, nil
	}

	var rec Record
	unmarshalErr := json.Unmarshal(plain, &rec)
	for i := range plain {
		plain[i] = 0
	}
	if unmarshalErr != nil {
		backup, bErr := backupCorruptFile(path)
		if bErr != nil {
			return nil, "", bErr
		}
		s.lastStatus = Status{
			Site:     siteID,
			LoggedIn: false,
			State:    "decrypt_failed",
			Message:  "凭证文件损坏，已备份原文件并降级为未登录状态",
		}
		return s, backup, nil
	}

	if rec.Site != "" && normalizeSiteID(rec.Site) != siteID {
		backup, bErr := backupCorruptFile(path)
		if bErr != nil {
			return nil, "", bErr
		}
		s.lastStatus = Status{
			Site:     siteID,
			LoggedIn: false,
			State:    "decrypt_failed",
			Message:  "凭证站点标识不匹配，已备份原文件并降级为未登录状态",
		}
		return s, backup, nil
	}

	rec.Cookies = s.filterCookies(rec.Cookies)
	if !s.hasRequiredCookies(rec.Cookies) {
		s.lastStatus = Status{
			Site:     siteID,
			LoggedIn: false,
			State:    "logged_out",
		}
		return s, "", nil
	}
	if rec.State == "" {
		rec.State = "logged_in"
	}
	s.rec = &rec
	s.lastStatus = s.buildStatus(&rec)
	return s, "", nil
}

// Site returns the site identifier ("bilibili" or "douyin") managed by this Store.
func (s *Store) Site() string {
	if s == nil || s.site == "" {
		return SiteBilibili
	}
	return s.site
}

func (s *Store) filterCookies(in map[string]string) map[string]string {
	if s != nil && s.site == SiteDouyin {
		return CleanDouyinCookieMap(in)
	}
	return FilterCookieMap(in)
}

func (s *Store) hasRequiredCookies(cookies map[string]string) bool {
	if s != nil && s.site == SiteDouyin {
		return hasDouyinSession(cookies)
	}
	return cookies["SESSDATA"] != ""
}

func (s *Store) orderedCookieKeys() []string {
	if s != nil && s.site == SiteDouyin {
		return AllowedDouyinCookieKeys
	}
	return AllowedCookieKeys
}

// Status returns the desensitized account status.
func (s *Store) Status() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.rec == nil {
		return s.lastStatus
	}
	return s.buildStatus(s.rec)
}

// HasActiveCredential reports whether usable cookies are present.
func (s *Store) HasActiveCredential() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.rec == nil {
		return false
	}
	if s.rec.State == "expired" || s.rec.State == "logged_out" || s.rec.State == "decrypt_failed" {
		return false
	}
	return s.hasRequiredCookies(s.rec.Cookies)
}

// CookieHeader returns the formatted Cookie header string for outbound site API requests.
// Returns empty string if not logged in or expired.
func (s *Store) CookieHeader() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.rec == nil || s.rec.State == "expired" || s.rec.State == "logged_out" || s.rec.State == "decrypt_failed" {
		return ""
	}
	var pairs []string
	for _, k := range s.orderedCookieKeys() {
		if v := s.rec.Cookies[k]; v != "" {
			pairs = append(pairs, k+"="+v)
		}
	}
	return strings.Join(pairs, "; ")
}

// CookieValue returns a single stored cookie value when active, or empty string if absent/inactive.
func (s *Store) CookieValue(name string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.rec == nil || s.rec.State == "expired" || s.rec.State == "logged_out" || s.rec.State == "decrypt_failed" {
		return ""
	}
	return s.rec.Cookies[name]
}

// Cookies returns a copy of the stored whitelisted cookies, or nil if not logged in.
func (s *Store) Cookies() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.rec == nil {
		return nil
	}
	out := make(map[string]string, len(s.rec.Cookies))
	for k, v := range s.rec.Cookies {
		out[k] = v
	}
	return out
}

// CSRF returns the current bili_jct token if logged in.
func (s *Store) CSRF() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.rec == nil {
		return ""
	}
	return s.rec.Cookies["bili_jct"]
}

// RefreshState returns the current refresh token and any pending confirmation state.
func (s *Store) RefreshState() (refreshToken string, confirmPending bool, pendingOldRefreshToken string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.rec == nil {
		return "", false, ""
	}
	return s.rec.RefreshToken, s.rec.ConfirmPending, s.rec.PendingOldRefreshToken
}

// SaveManual validates and saves manually imported Bilibili cookies and optional refresh_token.
func (s *Store) SaveManual(rawCookie, rawRefreshToken string) (Status, error) {
	cookies, rt, err := ParseManualCookieInput(rawCookie, rawRefreshToken)
	if err != nil {
		return Status{}, err
	}
	return s.SaveVerifiedManual(cookies, rt, 0, "", "")
}

// SaveVerifiedManual saves already-validated manual Bilibili cookies and profile metadata.
func (s *Store) SaveVerifiedManual(cookies map[string]string, refreshToken string, mid int64, uname, face string) (Status, error) {
	filtered := FilterCookieMap(cookies)
	if filtered["SESSDATA"] == "" {
		return Status{}, ErrMissingRequiredKeys
	}
	now := time.Now().UTC().Format(time.RFC3339)
	rec := &Record{
		Version:       1,
		Cookies:       filtered,
		RefreshToken:  strings.TrimSpace(refreshToken),
		Source:        "manual",
		UpdatedAt:     now,
		ObtainedAt:    now,
		LastCheckedAt: now,
		Mid:           mid,
		Uname:         uname,
		Face:          face,
		State:         "logged_in",
	}
	if err := s.saveRecord(rec); err != nil {
		return Status{}, err
	}
	return s.Status(), nil
}

// MarkChecked updates LastCheckedAt on the active credential without changing cookies.
func (s *Store) MarkChecked() (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rec == nil {
		return s.lastStatus, ErrNotLoggedIn
	}
	next := s.cloneRecord(s.rec)
	next.LastCheckedAt = time.Now().UTC().Format(time.RFC3339)
	if err := s.persistRecordLocked(next); err != nil {
		return s.buildStatus(s.rec), err
	}
	return s.buildStatus(s.rec), nil
}

// SaveQR saves Bilibili credentials obtained from QR code login.
func (s *Store) SaveQR(cookies map[string]string, refreshToken string, mid int64, uname, face string) (Status, error) {
	filtered := FilterCookieMap(cookies)
	if filtered["SESSDATA"] == "" || filtered["bili_jct"] == "" {
		return Status{}, ErrMissingRequiredKeys
	}
	now := time.Now().UTC().Format(time.RFC3339)
	rec := &Record{
		Version:       1,
		Cookies:       filtered,
		RefreshToken:  strings.TrimSpace(refreshToken),
		Source:        "qr",
		UpdatedAt:     now,
		ObtainedAt:    now,
		LastCheckedAt: now,
		Mid:           mid,
		Uname:         uname,
		Face:          face,
		State:         "logged_in",
	}
	if err := s.saveRecord(rec); err != nil {
		return Status{}, err
	}
	return s.Status(), nil
}

// UpdateProfile updates the cached Bilibili account profile (mid, uname, face) and state.
func (s *Store) UpdateProfile(mid int64, uname, face string) (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rec == nil {
		return s.lastStatus, ErrNotLoggedIn
	}
	now := time.Now().UTC().Format(time.RFC3339)
	next := s.cloneRecord(s.rec)
	if mid > 0 {
		next.Mid = mid
	}
	if uname != "" {
		next.Uname = uname
	}
	if face != "" {
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

// MarkState updates the credential state ("logged_in", "expired", "refresh_failed") and message.
func (s *Store) MarkState(state, message string) (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rec == nil {
		s.lastStatus.State = state
		s.lastStatus.Message = message
		return s.lastStatus, nil
	}
	next := s.cloneRecord(s.rec)
	next.State = state
	next.Message = message
	next.LastCheckedAt = time.Now().UTC().Format(time.RFC3339)
	if err := s.persistRecordLocked(next); err != nil {
		if state == "expired" {
			s.rec = next
			return s.buildStatus(next), err
		}
		return s.buildStatus(s.rec), err
	}
	return s.buildStatus(s.rec), nil
}

// CommitRefreshStep4 atomically saves new cookies and newRefreshToken after cookie/refresh succeeds,
// while recording confirmPending=true and pendingOldRefreshToken=oldRefreshToken until confirm/refresh completes.
func (s *Store) CommitRefreshStep4(newCookies map[string]string, newRefreshToken, oldRefreshToken string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rec == nil {
		return ErrNotLoggedIn
	}
	if s.rec.Source != "qr" || s.rec.RefreshToken != oldRefreshToken || s.rec.State == "expired" {
		return ErrNotLoggedIn
	}
	next := s.cloneRecord(s.rec)
	filtered := FilterCookieMap(newCookies)
	for k, v := range filtered {
		next.Cookies[k] = v
	}
	if next.Cookies["SESSDATA"] == "" || next.Cookies["bili_jct"] == "" {
		return ErrMissingRequiredKeys
	}
	next.RefreshToken = strings.TrimSpace(newRefreshToken)
	next.ConfirmPending = true
	next.PendingOldRefreshToken = strings.TrimSpace(oldRefreshToken)
	next.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	next.RenewedAt = next.UpdatedAt
	next.State = "logged_in"
	next.Message = ""
	return s.persistRecordLocked(next)
}

// CompleteRefreshStep5 clears confirmPending and pendingOldRefreshToken after confirm/refresh succeeds.
func (s *Store) CompleteRefreshStep5() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rec == nil {
		return ErrNotLoggedIn
	}
	next := s.cloneRecord(s.rec)
	next.ConfirmPending = false
	next.PendingOldRefreshToken = ""
	next.LastCheckedAt = time.Now().UTC().Format(time.RFC3339)
	next.State = "logged_in"
	next.Message = ""
	return s.persistRecordLocked(next)
}

// Clear removes the credential file from disk and resets memory state to logged_out.
func (s *Store) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("credential: remove file: %w", err)
	}
	s.rec = nil
	s.lastStatus = Status{
		Site:     s.Site(),
		LoggedIn: false,
		State:    "logged_out",
	}
	return nil
}

// ExtractSetCookies extracts whitelisted Bilibili cookies from HTTP Set-Cookie headers.
func ExtractSetCookies(headers []*http.Cookie) map[string]string {
	raw := make(map[string]string, len(headers))
	for _, c := range headers {
		if c == nil || c.Value == "" {
			continue
		}
		raw[c.Name] = c.Value
	}
	return FilterCookieMap(raw)
}

func (s *Store) collectSecrets() []string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.rec == nil {
		return nil
	}
	var secrets []string
	for k, v := range s.rec.Cookies {
		if len(v) >= 4 && !isNonSecretCookieValue(k, v) {
			secrets = append(secrets, v)
		}
	}
	if len(s.rec.RefreshToken) >= 4 {
		secrets = append(secrets, s.rec.RefreshToken)
	}
	if len(s.rec.PendingOldRefreshToken) >= 4 {
		secrets = append(secrets, s.rec.PendingOldRefreshToken)
	}
	return secrets
}

// isNonSecretCookieValue reports cookie values that carry no session secret and
// would otherwise turn ordinary log text into redaction noise: Douyin's
// is_staff_user flag and login_time timestamp, and bare boolean literals.
func isNonSecretCookieValue(key, val string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "is_staff_user", "login_time":
		return true
	}
	switch strings.ToLower(strings.TrimSpace(val)) {
	case "true", "false", "null":
		return true
	}
	return false
}

// RedactString replaces any stored cookie values or refresh tokens present in in with [REDACTED].
func (s *Store) RedactString(in string) string {
	if in == "" {
		return ""
	}
	return redactWithSecrets(in, s.collectSecrets())
}

func redactWithSecrets(in string, secrets []string) string {
	if in == "" || len(secrets) == 0 {
		return in
	}
	sort.Slice(secrets, func(i, j int) bool {
		return len(secrets[i]) > len(secrets[j])
	})
	out := in
	for _, sec := range secrets {
		out = strings.ReplaceAll(out, sec, "[REDACTED]")
	}
	return out
}

func (s *Store) saveRecord(rec *Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.persistRecordLocked(rec)
}

func (s *Store) cloneRecord(rec *Record) *Record {
	next := *rec
	next.Cookies = s.filterCookies(rec.Cookies)
	return &next
}

func (s *Store) persistRecordLocked(next *Record) error {
	plain, err := json.Marshal(next)
	if err != nil {
		return fmt.Errorf("credential: marshal record: %w", err)
	}
	entropy := s.entropy
	if len(entropy) == 0 {
		entropy = []byte(BilibiliEntropy)
	}
	cipherData, err := encryptBytesWithEntropy(plain, entropy)
	for i := range plain {
		plain[i] = 0
	}
	if err != nil {
		return err
	}
	if err := writeFileAtomic(s.path, cipherData, 0o600); err != nil {
		return err
	}
	s.rec = next
	s.lastStatus = s.buildStatus(next)
	return nil
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-cred-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	_ = os.Chmod(tmpName, perm)
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

func backupCorruptFile(path string) (string, error) {
	backup := fmt.Sprintf("%s.corrupt-%s", path, time.Now().UTC().Format("20060102T150405Z"))
	if err := os.Rename(path, backup); err != nil {
		return "", err
	}
	return backup, nil
}

func (s *Store) buildStatus(rec *Record) Status {
	siteID := s.Site()
	if rec == nil {
		return Status{
			Site:     siteID,
			LoggedIn: false,
			State:    "logged_out",
		}
	}
	state := rec.State
	if state == "" {
		state = "logged_in"
	}
	loggedIn := state == "logged_in" || state == "refresh_failed" || state == "unknown"
	return Status{
		Site:            siteID,
		LoggedIn:        loggedIn,
		State:           state,
		Source:          rec.Source,
		Mid:             rec.Mid,
		UID:             rec.UID,
		SecUID:          rec.SecUID,
		UniqueID:        rec.UniqueID,
		Uname:           rec.Uname,
		Face:            rec.Face,
		HasRefreshToken: rec.RefreshToken != "",
		HasTTWID:        rec.Cookies["ttwid"] != "",
		ConfirmPending:  rec.ConfirmPending,
		UpdatedAt:       rec.UpdatedAt,
		LastRenewedAt:   rec.RenewedAt,
		LastCheckedAt:   rec.LastCheckedAt,
		Message:         rec.Message,
	}
}
