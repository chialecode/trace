package update

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/chialecode/trace/internal/storage"
)

// Schema and retention bounds for <DataDir>/update-history.json (DEC-051 ①).
const (
	HistoryFileName      = "update-history.json"
	HistorySchemaVersion = 1
	MaxCheckRecords      = 20
	MaxUpdateRecords     = 50

	CheckTriggerAuto   = "auto"
	CheckTriggerManual = "manual"

	UpdateMethodAuto       = "auto_update"
	UpdateMethodDevInstall = "dev_install"
	UpdateMethodManual     = "manual_install"

	msgHistoryFailedPrefix = "检查失败"
)

// CheckRecord records one release-channel update check in update-history.json.
type CheckRecord struct {
	At      string `json:"at"`
	Trigger string `json:"trigger"`
	Result  string `json:"result"`
}

// UpdateRecord records one effective version change observed at startup in update-history.json.
type UpdateRecord struct {
	At          string `json:"at"`
	FromVersion string `json:"fromVersion"`
	ToVersion   string `json:"toVersion"`
	Method      string `json:"method"`
}

// History is the v1 schema of <DataDir>/update-history.json.
type History struct {
	Version int            `json:"version"`
	Checks  []CheckRecord  `json:"checks"`
	Updates []UpdateRecord `json:"updates"`
}

// TriggerLabel returns the user-facing Chinese label for a check trigger.
func TriggerLabel(trigger string) string {
	switch strings.TrimSpace(trigger) {
	case CheckTriggerManual:
		return "手动"
	default:
		return "自动"
	}
}

// MethodLabel returns the user-facing Chinese label for an update installation method.
func MethodLabel(method string) string {
	switch strings.TrimSpace(method) {
	case UpdateMethodAuto:
		return "自动更新"
	case UpdateMethodDevInstall:
		return "本机打包安装"
	default:
		return "手动安装"
	}
}

// FormatCheckResult turns the outcome of Engine.Check into a sanitized check-history result string.
func FormatCheckResult(rel *Release, err error) string {
	if err != nil {
		_, detail := classifyUpdateError(err)
		return msgHistoryFailedPrefix + "：" + detail
	}
	if rel == nil {
		return MsgUpToDate
	}
	if ver := SanitizeVersion(rel.Version); ver != "" {
		return "发现 " + ver
	}
	return "发现新版本"
}

var (
	historySchemeURLPattern    = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s"'<>），。；、]+`)
	historyBareHostPathPattern = regexp.MustCompile(`(?i)\b(?:[a-z0-9-]+\.)+[a-z]{2,}/[^\s"'<>），。；、]*`)
	historyArchivePathPattern  = regexp.MustCompile(`(?i)[^\s"'<>]*(?:/releases/download/|\.(?:zip|exe|msi|nupkg|tar\.gz|7z))\b[^\s"'<>），。；、]*`)
	historyQueryPattern        = regexp.MustCompile(`(?:\?[^\s"'<>），。；、]*|(?:&[A-Za-z0-9_.-]+=[^\s&"'<>），。；、]*)+)`)
	historyBearerPattern       = regexp.MustCompile(`(?i)\b(?:authorization\s*[:=]\s*)?bearer\s+[^\s;"'<>），。；、]+`)
	historyCookieHeaderPattern = regexp.MustCompile(`(?i)\b(?:set-cookie|cookie)\s*[:=]\s*[^"'<>），。]+`)
	historySecretKVPattern     = regexp.MustCompile(`(?i)\b(?:cookie|set-cookie|authorization|sessdata|bili_jct|dedeuserid|dedeuserid__ckmd5|sid|ac_time_value|refresh_token|pending_old_refresh_token|access_key|auth_key|w_rid|upsig|wssecret|wstime|deadline|bwsv|sessionid|sessionid_ss|sid_tt|uid_tt|uid_tt_ss|sid_guard|odin_tt|passport_csrf_token|passport_csrf_token_default|ttwid|uifid|uifid_temp|mstoken|s_v_web_id|__ac_nonce|__ac_signature|a_bogus|x-bogus|x-expires|x-signature|x-secsdk-web-signature|x-secsdk-csrf-token|x-amz-[a-z0-9_-]+|token|sign|sig|secret|password|passwd)["'\\]*\s*[=:][^\s;&"'<>），。；、]*`)
	historyPercentEscape       = regexp.MustCompile(`(?i)%[0-9a-f]{2}`)
	historyGoHTTPVerbArtifact  = regexp.MustCompile(`(?i)\b(?:Get|Head|Post)\s*""\s*:\s*`)
	historySpaceCollapse       = regexp.MustCompile(`\s+`)
)

func unescapeHistoryCandidate(s string) string {
	cur := s
	for i := 0; i < 3; i++ {
		if !historyPercentEscape.MatchString(cur) {
			break
		}
		next, err := url.QueryUnescape(cur)
		if err != nil {
			next = historyPercentEscape.ReplaceAllStringFunc(cur, func(esc string) string {
				v, uErr := url.QueryUnescape(esc)
				if uErr != nil {
					return esc
				}
				return v
			})
		}
		if next == cur {
			break
		}
		cur = next
	}
	return cur
}

func containsForbiddenHistoryToken(s string) bool {
	for _, candidate := range []string{s, unescapeHistoryCandidate(s)} {
		lower := strings.ToLower(candidate)
		if strings.Contains(lower, "://") ||
			strings.Contains(lower, "http:") ||
			strings.Contains(lower, "https:") ||
			strings.Contains(candidate, "?") ||
			historyBareHostPathPattern.MatchString(candidate) ||
			historyArchivePathPattern.MatchString(candidate) ||
			historyBearerPattern.MatchString(candidate) ||
			historySecretKVPattern.MatchString(candidate) {
			return true
		}
	}
	return false
}

// SanitizeHistoryText removes all URLs, download paths, query strings, and credentials from text.
func SanitizeHistoryText(raw string, redact func(string) string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	hadCheckPrefix := strings.HasPrefix(s, msgHistoryFailedPrefix) || strings.HasPrefix(s, msgCheckFailed)
	if redact != nil {
		s = redact(s)
	}
	if unescaped := unescapeHistoryCandidate(s); unescaped != s && containsForbiddenHistoryToken(unescaped) {
		s = unescaped
		if redact != nil {
			s = redact(s)
		}
	}
	s = strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(s)
	s = historySchemeURLPattern.ReplaceAllString(s, "")
	s = historyBareHostPathPattern.ReplaceAllString(s, "")
	s = historyArchivePathPattern.ReplaceAllString(s, "")
	s = historyQueryPattern.ReplaceAllString(s, "")
	s = historyCookieHeaderPattern.ReplaceAllString(s, "")
	s = historyBearerPattern.ReplaceAllString(s, "")
	s = historySecretKVPattern.ReplaceAllString(s, "")
	s = historyGoHTTPVerbArtifact.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, `""`, "")
	s = strings.ReplaceAll(s, `''`, "")
	if redact != nil {
		s = redact(s)
	}
	s = historySpaceCollapse.ReplaceAllString(s, " ")
	s = strings.Trim(s, " ;,&:")
	if strings.HasSuffix(s, "：") {
		s = strings.TrimSuffix(s, "：")
	}
	if s == msgHistoryFailedPrefix || s == msgCheckFailed {
		s = msgHistoryFailedPrefix + "：请求异常"
	}
	if containsForbiddenHistoryToken(s) || (redact != nil && redact(s) != s) {
		if hadCheckPrefix {
			return msgHistoryFailedPrefix + "：请求异常"
		}
		return ""
	}
	return shorten(s, 120)
}

// SanitizeVersion validates and cleans a version token, rejecting URLs, paths, queries, or secrets.
func SanitizeVersion(raw string) string {
	v := strings.TrimSpace(raw)
	if v == "" || len([]rune(v)) > 64 {
		return ""
	}
	for _, r := range v {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return ""
		}
		switch r {
		case '/', '\\', '?', '&', '=', ':', ';', '"', '\'', '<', '>', '%', '@':
			return ""
		}
	}
	if containsForbiddenHistoryToken(v) {
		return ""
	}
	return v
}

func normalizeHistoryTimestamp(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", false
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC().Format(time.RFC3339), true
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC().Format(time.RFC3339), true
	}
	return "", false
}

// SanitizeCheckRecord validates and scrubs a CheckRecord before reading or writing update-history.json.
func SanitizeCheckRecord(rec CheckRecord, redact func(string) string) (CheckRecord, bool) {
	at, ok := normalizeHistoryTimestamp(rec.At)
	if !ok {
		return CheckRecord{}, false
	}
	trigger := strings.TrimSpace(rec.Trigger)
	if trigger != CheckTriggerAuto && trigger != CheckTriggerManual {
		return CheckRecord{}, false
	}
	result := SanitizeHistoryText(rec.Result, redact)
	if result == "" {
		return CheckRecord{}, false
	}
	return CheckRecord{
		At:      at,
		Trigger: trigger,
		Result:  result,
	}, true
}

// SanitizeUpdateRecord validates and scrubs an UpdateRecord before reading or writing update-history.json.
func SanitizeUpdateRecord(rec UpdateRecord, redact func(string) string) (UpdateRecord, bool) {
	at, ok := normalizeHistoryTimestamp(rec.At)
	if !ok {
		return UpdateRecord{}, false
	}
	fromVer := SanitizeVersion(rec.FromVersion)
	toVer := SanitizeVersion(rec.ToVersion)
	if fromVer == "" || toVer == "" {
		return UpdateRecord{}, false
	}
	if redact != nil && (redact(fromVer) != fromVer || redact(toVer) != toVer) {
		return UpdateRecord{}, false
	}
	method := strings.TrimSpace(rec.Method)
	switch method {
	case UpdateMethodAuto, UpdateMethodDevInstall, UpdateMethodManual:
	default:
		return UpdateRecord{}, false
	}
	return UpdateRecord{
		At:          at,
		FromVersion: fromVer,
		ToVersion:   toVer,
		Method:      method,
	}, true
}

// GuardHistorySchema refuses <dataDir>/update-history.json written by a newer TRACE version
// before any database or state file is opened or migrated. Corrupt files are left for OpenHistoryStore
// to back up and rebuild.
func GuardHistorySchema(dataDir string) error {
	if strings.TrimSpace(dataDir) == "" {
		return nil
	}
	path := filepath.Join(dataDir, HistoryFileName)
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var head map[string]json.RawMessage
	if err := json.Unmarshal(raw, &head); err != nil {
		return nil
	}
	rawVer, ok := head["version"]
	if !ok || len(rawVer) == 0 {
		return nil
	}
	var v int
	if err := json.Unmarshal(rawVer, &v); err != nil {
		return nil
	}
	if v > HistorySchemaVersion {
		return &storage.NewerDataError{
			File:      HistoryFileName,
			Found:     v,
			Supported: HistorySchemaVersion,
		}
	}
	return nil
}

// HistoryStore manages <DataDir>/update-history.json atomically (mode 0600).
type HistoryStore struct {
	mu     sync.Mutex
	path   string
	redact func(string) string
	data   History
}

func emptyHistory() History {
	return History{
		Version: HistorySchemaVersion,
		Checks:  []CheckRecord{},
		Updates: []UpdateRecord{},
	}
}

// OpenHistoryStore opens <path> (update-history.json):
//   - version > 1 returns *storage.NewerDataError without modifying the file;
//   - corrupt JSON is backed up via storage.BackupCorruptFile and rebuilt as an empty v1 file;
//   - valid v1 files have all records sanitized and capped on load.
func OpenHistoryStore(path string, redact func(string) string) (*HistoryStore, string, error) {
	s := &HistoryStore{
		path:   path,
		redact: redact,
		data:   emptyHistory(),
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, "", nil
		}
		return nil, "", err
	}

	rebuildCorrupt := func() (*HistoryStore, string, error) {
		backup, bErr := storage.BackupCorruptFile(path)
		if bErr != nil {
			return nil, "", bErr
		}
		if wErr := storage.WriteJSONAtomic(path, s.data, 0o600); wErr != nil {
			return nil, backup, wErr
		}
		return s, backup, nil
	}

	var head map[string]json.RawMessage
	if err := json.Unmarshal(raw, &head); err != nil {
		return rebuildCorrupt()
	}
	hasVersion := false
	versionVal := 0
	if rawVer, ok := head["version"]; ok && len(rawVer) > 0 && string(rawVer) != "null" {
		if err := json.Unmarshal(rawVer, &versionVal); err != nil {
			return rebuildCorrupt()
		}
		if versionVal > HistorySchemaVersion {
			return nil, "", &storage.NewerDataError{
				File:      HistoryFileName,
				Found:     versionVal,
				Supported: HistorySchemaVersion,
			}
		}
		if versionVal <= 0 {
			return rebuildCorrupt()
		}
		hasVersion = true
	}

	var loaded History
	if err := json.Unmarshal(raw, &loaded); err != nil {
		return rebuildCorrupt()
	}

	dirty := !hasVersion || versionVal != HistorySchemaVersion

	cleanChecks := make([]CheckRecord, 0, min(len(loaded.Checks), MaxCheckRecords))
	for _, c := range loaded.Checks {
		cleaned, ok := SanitizeCheckRecord(c, redact)
		if !ok {
			dirty = true
			continue
		}
		if cleaned != c {
			dirty = true
		}
		if len(cleanChecks) < MaxCheckRecords {
			cleanChecks = append(cleanChecks, cleaned)
		} else {
			dirty = true
		}
	}

	cleanUpdates := make([]UpdateRecord, 0, min(len(loaded.Updates), MaxUpdateRecords))
	for _, u := range loaded.Updates {
		cleaned, ok := SanitizeUpdateRecord(u, redact)
		if !ok {
			dirty = true
			continue
		}
		if cleaned != u {
			dirty = true
		}
		if len(cleanUpdates) < MaxUpdateRecords {
			cleanUpdates = append(cleanUpdates, cleaned)
		} else {
			dirty = true
		}
	}

	s.data = History{
		Version: HistorySchemaVersion,
		Checks:  cleanChecks,
		Updates: cleanUpdates,
	}
	if dirty {
		if err := storage.WriteJSONAtomic(path, s.data, 0o600); err != nil {
			return nil, "", err
		}
	}
	return s, "", nil
}

// Get returns a copy of the current update and check history (newest first).
func (s *HistoryStore) Get() History {
	if s == nil {
		return emptyHistory()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := History{
		Version: s.data.Version,
		Checks:  make([]CheckRecord, len(s.data.Checks)),
		Updates: make([]UpdateRecord, len(s.data.Updates)),
	}
	copy(out.Checks, s.data.Checks)
	copy(out.Updates, s.data.Updates)
	return out
}

// RecordCheck prepends a sanitized check record (retaining latest 20) and writes update-history.json atomically.
func (s *HistoryStore) RecordCheck(rec CheckRecord) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cleaned, ok := SanitizeCheckRecord(rec, s.redact)
	if !ok {
		return fmt.Errorf("update: invalid check record")
	}
	next := make([]CheckRecord, 0, min(len(s.data.Checks)+1, MaxCheckRecords))
	next = append(next, cleaned)
	for _, existing := range s.data.Checks {
		if len(next) >= MaxCheckRecords {
			break
		}
		next = append(next, existing)
	}
	prev := s.data.Checks
	s.data.Version = HistorySchemaVersion
	s.data.Checks = next
	if err := storage.WriteJSONAtomic(s.path, s.data, 0o600); err != nil {
		s.data.Checks = prev
		return err
	}
	return nil
}

// RecordUpdate prepends a sanitized update record (retaining latest 50) and writes update-history.json atomically.
func (s *HistoryStore) RecordUpdate(rec UpdateRecord) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cleaned, ok := SanitizeUpdateRecord(rec, s.redact)
	if !ok {
		return fmt.Errorf("update: invalid update record")
	}
	next := make([]UpdateRecord, 0, min(len(s.data.Updates)+1, MaxUpdateRecords))
	next = append(next, cleaned)
	for _, existing := range s.data.Updates {
		if len(next) >= MaxUpdateRecords {
			break
		}
		next = append(next, existing)
	}
	prev := s.data.Updates
	s.data.Version = HistorySchemaVersion
	s.data.Updates = next
	if err := storage.WriteJSONAtomic(s.path, s.data, 0o600); err != nil {
		s.data.Updates = prev
		return err
	}
	return nil
}
