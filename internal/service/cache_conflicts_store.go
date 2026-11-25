package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/storage"
)

const (
	cacheConflictsFileName      = "cache-conflicts.json"
	cacheConflictsSchemaVersion = 1
)

// IgnoredRevision records a sanitized remote revision fingerprint ignored via
// "保留本地" so periodic fullCache sweeps stay silent until a newer V3 appears.
type IgnoredRevision struct {
	RemoteFingerprint string `json:"remoteFingerprint"`
	IgnoredAt         string `json:"ignoredAt"`
}

type cacheConflictsFile struct {
	Version          int                        `json:"version"`
	Conflicts        []cacheConflictRecord      `json:"conflicts"`
	IgnoredRevisions map[string]IgnoredRevision `json:"ignoredRevisions,omitempty"`
}

type cacheConflictRecord struct {
	ID                string           `json:"id"`
	ItemKey           string           `json:"itemKey"`
	Site              string           `json:"site"`
	Kind              string           `json:"kind"`
	TargetID          string           `json:"targetId"`
	AuthorUID         string           `json:"authorUid,omitempty"`
	AuthorName        string           `json:"authorName,omitempty"`
	Title             string           `json:"title"`
	DetectedAt        string           `json:"detectedAt"`
	Origin            string           `json:"origin,omitempty"`
	ReasonKind        string           `json:"reasonKind"`
	LocalSummary      string           `json:"localSummary"`
	RemoteSummary     string           `json:"remoteSummary"`
	RemoteFingerprint string           `json:"remoteFingerprint,omitempty"`
	Via               *library.ItemVia `json:"via,omitempty"`
	State             string           `json:"state"`
	LastError         string           `json:"lastError,omitempty"`
}

type cacheConflictStore struct {
	b     *Backend
	path  string
	clock CacheQueueClock

	mu               sync.Mutex
	records          []cacheConflictRecord
	ignored          map[string]IgnoredRevision
	checkedNonLegacy map[string]string
	fileExists       bool
}

var (
	conflictProtoURLPattern = regexp.MustCompile(`(?i)(?:https?:)?//[^\s"'<>]+`)
	conflictCDNHostPattern  = regexp.MustCompile(`(?i)\b[A-Za-z0-9.-]*(?:hdslb\.com|bilivideo\.(?:com|cn)|biliimg\.com|akamaized\.net|douyinvod\.com|douyinpic\.com|byteimg\.com|zjcdn\.com|snssdk\.com|amemv\.com|weibocdn\.com|sinaimg\.cn|video\.weibo\.com|twimg\.com|ton\.x\.com|ton\.twitter\.com|pscp\.tv|periscope\.tv)(?:/[^\s"'<>]*)?`)
	conflictKVTokenPattern  = regexp.MustCompile(`[^\s"'<>()（），,；;]*=[^\s"'<>()（），,；;]*`)
	conflictCleanIDPattern  = regexp.MustCompile(`^[A-Za-z0-9_-]{3,64}$`)
	conflictCleanFPPattern  = regexp.MustCompile(`^[A-Za-z0-9_-]{4,128}$`)
)

func newCacheConflictStore(b *Backend, path string, clock CacheQueueClock) (*cacheConflictStore, string, error) {
	if clock == nil {
		clock = realCacheQueueClock{}
	}
	s := &cacheConflictStore{
		b:                b,
		path:             path,
		clock:            clock,
		ignored:          make(map[string]IgnoredRevision),
		checkedNonLegacy: make(map[string]string),
	}
	backup, err := s.loadFromDiskLocked()
	if err != nil {
		return nil, backup, err
	}
	return s, backup, nil
}

func (s *cacheConflictStore) nowRFC3339() string {
	return s.clock.Now().UTC().Format(time.RFC3339)
}

func (s *cacheConflictStore) sanitizeText(raw string) string {
	if s != nil && s.b != nil {
		raw = s.b.redactStoredSecrets(raw)
	}
	return sanitizeConflictText(raw)
}

// sanitizeConflictText scrubs URLs, CDN hostnames, query strings (?, &, =),
// cookies, and tokens so cache-conflicts.json satisfies NFR-SEC-001~005.
func sanitizeConflictText(raw string) string {
	txt := strings.TrimSpace(raw)
	if txt == "" {
		return ""
	}
	txt = conflictProtoURLPattern.ReplaceAllString(txt, " ")
	txt = queueFullURLPattern.ReplaceAllString(txt, " ")
	txt = queueQueryTailPattern.ReplaceAllString(txt, " ")
	txt = queueSecretKVPattern.ReplaceAllString(txt, " ")
	txt = conflictCDNHostPattern.ReplaceAllString(txt, " ")
	txt = conflictKVTokenPattern.ReplaceAllString(txt, " ")
	txt = strings.NewReplacer("?", " ", "&", " ", "=", " ").Replace(txt)
	return strings.Join(strings.Fields(txt), " ")
}

func sanitizeConflictFingerprint(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	lower := strings.ToLower(trimmed)
	isClean := conflictCleanFPPattern.MatchString(trimmed)
	if isClean {
		for _, bad := range []string{
			"cookie", "bearer", "sessdata", "bili_jct", "sessionid",
			"auth_token", "xsrf", "wbpsess", "bogus", "ssig", "upsig",
			"weibocdn", "sinaimg", "twimg", "douyinvod", "byteimg", "hdslb", "bilivideo",
		} {
			if strings.Contains(lower, bad) {
				isClean = false
				break
			}
		}
	}
	if isClean {
		return trimmed
	}
	sum := sha256.Sum256([]byte(trimmed))
	return "fp_" + hex.EncodeToString(sum[:16])
}

func makeConflictID(itemKey string) string {
	sum := sha256.Sum256([]byte(itemKey))
	return "cf_" + hex.EncodeToString(sum[:10])
}

func sanitizeConflictReasonKind(raw string) string {
	switch strings.TrimSpace(raw) {
	case ConflictReasonParts:
		return ConflictReasonParts
	case ConflictReasonMedia:
		return ConflictReasonMedia
	case ConflictReasonText:
		return ConflictReasonText
	case ConflictReasonMismatch:
		return ConflictReasonMismatch
	default:
		return ConflictReasonMismatch
	}
}

func sanitizeConflictOrigin(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case QueueOriginFullCache, "cache_queue", "queue", "manual", "upgrade", "supplement", "download", "auto":
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return ""
	}
}

func sanitizeConflictState(raw string, recoverResolving bool) string {
	switch strings.TrimSpace(raw) {
	case ConflictStatePending:
		return ConflictStatePending
	case ConflictStateResolving:
		if recoverResolving {
			return ConflictStatePending
		}
		return ConflictStateResolving
	case ConflictStateFailed:
		return ConflictStateFailed
	default:
		return ConflictStatePending
	}
}

func sanitizeConflictCleanID(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" || len(s) > 128 {
		return ""
	}
	if strings.Contains(s, "://") || strings.Contains(s, "..") || strings.ContainsAny(s, "?#&=;/\\ \t\r\n\"'<>:") {
		return ""
	}
	if conflictCDNHostPattern.MatchString(s) || queueSecretKVPattern.MatchString(s) {
		return ""
	}
	return s
}

func sanitizeConflictTargetID(raw, siteName, kind, fallbackRawID string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		s = fallbackRawID
	}
	if siteName == "weibo" && kind == "video" {
		if strings.HasPrefix(s, "1034:") {
			rest := sanitizeConflictCleanID(strings.TrimPrefix(s, "1034:"))
			if rest != "" {
				return "1034:" + rest
			}
		}
		if strings.HasPrefix(s, "1034_") {
			rest := sanitizeConflictCleanID(strings.TrimPrefix(s, "1034_"))
			if rest != "" {
				return "1034:" + rest
			}
		}
	}
	if clean := sanitizeConflictCleanID(s); clean != "" {
		return clean
	}
	return sanitizeConflictCleanID(fallbackRawID)
}

func sanitizeConflictVia(via *library.ItemVia, textFn func(string) string) *library.ItemVia {
	if via == nil {
		return nil
	}
	reason := strings.TrimSpace(via.Reason)
	if reason != library.ViaCollab && reason != library.ViaForward {
		return nil
	}
	sid, ok := site.ParseID(via.Site)
	if !ok {
		return nil
	}
	uid := sanitizeConflictCleanID(via.UID)
	if uid == "" {
		return nil
	}
	dynID := sanitizeConflictCleanID(via.DynamicID)
	if reason == library.ViaForward && dynID == "" {
		return nil
	}
	name := sanitizeConflictText(via.Name)
	if textFn != nil {
		name = textFn(via.Name)
	}
	return &library.ItemVia{
		Reason:    reason,
		Site:      string(sid),
		UID:       uid,
		Name:      name,
		DynamicID: dynID,
	}
}

func (s *cacheConflictStore) sanitizeRecord(r cacheConflictRecord, recoverResolving bool) (cacheConflictRecord, bool) {
	itemKey := strings.TrimSpace(r.ItemKey)
	if itemKey == "" && strings.TrimSpace(r.Site) != "" && strings.TrimSpace(r.Kind) != "" && strings.TrimSpace(r.TargetID) != "" {
		itemKey = library.CanonicalItemKey(r.Site, r.Kind, r.TargetID)
	}
	canonKey, ok := library.ValidateCanonicalItemKey(itemKey)
	if !ok {
		return cacheConflictRecord{}, false
	}
	parts := strings.SplitN(canonKey, ":", 3)
	if len(parts) != 3 {
		return cacheConflictRecord{}, false
	}
	siteName, kind, rawID := parts[0], parts[1], parts[2]
	targetID := sanitizeConflictTargetID(r.TargetID, siteName, kind, rawID)
	if targetID == "" {
		return cacheConflictRecord{}, false
	}

	id := strings.TrimSpace(r.ID)
	if !conflictCleanIDPattern.MatchString(id) || conflictCDNHostPattern.MatchString(id) {
		id = makeConflictID(canonKey)
	}

	reasonKind := sanitizeConflictReasonKind(r.ReasonKind)
	localSum := s.sanitizeText(r.LocalSummary)
	if localSum == "" {
		localSum = "本地已缓存版本"
	}
	remoteSum := s.sanitizeText(r.RemoteSummary)
	if remoteSum == "" {
		remoteSum = "源站内容已变更"
	}
	fp := sanitizeConflictFingerprint(r.RemoteFingerprint)
	if fp == "" {
		fp = sanitizeConflictFingerprint(reasonKind + "|" + remoteSum)
	}
	detectedAt := sanitizeCursorFallbackRFC3339(r.DetectedAt)
	if detectedAt == "" {
		detectedAt = s.nowRFC3339()
	}
	title := s.sanitizeText(r.Title)
	if title == "" {
		title = targetID
	}
	state := sanitizeConflictState(r.State, recoverResolving)
	lastErr := ""
	if state == ConflictStateFailed {
		lastErr = s.sanitizeText(r.LastError)
	}

	return cacheConflictRecord{
		ID:                id,
		ItemKey:           canonKey,
		Site:              siteName,
		Kind:              kind,
		TargetID:          targetID,
		AuthorUID:         sanitizeConflictCleanID(r.AuthorUID),
		AuthorName:        s.sanitizeText(r.AuthorName),
		Title:             title,
		DetectedAt:        detectedAt,
		Origin:            sanitizeConflictOrigin(r.Origin),
		ReasonKind:        reasonKind,
		LocalSummary:      localSum,
		RemoteSummary:     remoteSum,
		RemoteFingerprint: fp,
		Via:               sanitizeConflictVia(r.Via, s.sanitizeText),
		State:             state,
		LastError:         lastErr,
	}, true
}

func (s *cacheConflictStore) loadFromDiskLocked() (string, error) {
	if strings.TrimSpace(s.path) == "" {
		return "", nil
	}
	raw, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var doc cacheConflictsFile
	if jErr := json.Unmarshal(raw, &doc); jErr != nil {
		backup, bErr := storage.BackupCorruptFile(s.path)
		if bErr != nil {
			return "", bErr
		}
		_ = os.Remove(s.path)
		return backup, nil
	}
	if doc.Version > cacheConflictsSchemaVersion {
		return "", &storage.NewerDataError{
			File:      cacheConflictsFileName,
			Found:     doc.Version,
			Supported: cacheConflictsSchemaVersion,
		}
	}

	s.fileExists = true
	s.ignored = make(map[string]IgnoredRevision, len(doc.IgnoredRevisions))
	for rawKey, rev := range doc.IgnoredRevisions {
		canonKey, ok := library.ValidateCanonicalItemKey(rawKey)
		if !ok {
			continue
		}
		fp := sanitizeConflictFingerprint(rev.RemoteFingerprint)
		if fp == "" {
			continue
		}
		ignoredAt := sanitizeCursorFallbackRFC3339(rev.IgnoredAt)
		if ignoredAt == "" {
			ignoredAt = s.nowRFC3339()
		}
		s.ignored[canonKey] = IgnoredRevision{
			RemoteFingerprint: fp,
			IgnoredAt:         ignoredAt,
		}
	}

	seenKeys := make(map[string]int, len(doc.Conflicts))
	s.records = make([]cacheConflictRecord, 0, len(doc.Conflicts))
	recoveredResolving := false
	for _, r := range doc.Conflicts {
		wasResolving := strings.TrimSpace(r.State) == ConflictStateResolving
		clean, ok := s.sanitizeRecord(r, true)
		if !ok {
			continue
		}
		if wasResolving {
			recoveredResolving = true
		}
		if ign, hasIgn := s.ignored[clean.ItemKey]; hasIgn && ign.RemoteFingerprint == clean.RemoteFingerprint {
			continue
		}
		if idx, exists := seenKeys[clean.ItemKey]; exists {
			s.records[idx] = clean
			continue
		}
		seenKeys[clean.ItemKey] = len(s.records)
		s.records = append(s.records, clean)
	}
	if recoveredResolving {
		_ = s.saveLocked()
	}
	return "", nil
}

func (s *cacheConflictStore) buildPersistDocLocked() cacheConflictsFile {
	doc := cacheConflictsFile{
		Version:   cacheConflictsSchemaVersion,
		Conflicts: make([]cacheConflictRecord, 0, len(s.records)),
	}
	for _, r := range s.records {
		if clean, ok := s.sanitizeRecord(r, false); ok {
			doc.Conflicts = append(doc.Conflicts, clean)
		}
	}
	if len(s.ignored) > 0 {
		ignored := make(map[string]IgnoredRevision, len(s.ignored))
		for k, v := range s.ignored {
			canonKey, ok := library.ValidateCanonicalItemKey(k)
			if !ok {
				continue
			}
			fp := sanitizeConflictFingerprint(v.RemoteFingerprint)
			if fp == "" {
				continue
			}
			ignoredAt := sanitizeCursorFallbackRFC3339(v.IgnoredAt)
			if ignoredAt == "" {
				ignoredAt = s.nowRFC3339()
			}
			ignored[canonKey] = IgnoredRevision{
				RemoteFingerprint: fp,
				IgnoredAt:         ignoredAt,
			}
		}
		if len(ignored) > 0 {
			doc.IgnoredRevisions = ignored
		}
	}
	return doc
}

func (s *cacheConflictStore) saveLocked() error {
	if strings.TrimSpace(s.path) == "" {
		return nil
	}
	if len(s.records) == 0 && len(s.ignored) == 0 && !s.fileExists {
		return nil
	}
	doc := s.buildPersistDocLocked()
	if err := storage.WriteJSONAtomic(s.path, doc, 0o600); err != nil {
		return err
	}
	s.fileExists = true
	return nil
}

// recordOrUpdate records a conflict in cache-conflicts.json unless its
// remoteFingerprint matches ignoredRevisions[itemKey]. Returns (rec, changed, err).
func (s *cacheConflictStore) recordOrUpdate(in cacheConflictRecord) (cacheConflictRecord, bool, error) {
	if s == nil {
		return cacheConflictRecord{}, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if strings.TrimSpace(in.DetectedAt) == "" {
		in.DetectedAt = s.nowRFC3339()
	}
	if strings.TrimSpace(in.State) == "" {
		in.State = ConflictStatePending
	}
	clean, ok := s.sanitizeRecord(in, false)
	if !ok {
		return cacheConflictRecord{}, false, nil
	}

	// Suppress re-alerts when the user already chose "保留本地" for this exact revision.
	if ign, exists := s.ignored[clean.ItemKey]; exists {
		if ign.RemoteFingerprint != "" && ign.RemoteFingerprint == clean.RemoteFingerprint {
			return cacheConflictRecord{}, false, nil
		}
		// Newer V3 edit has a different fingerprint: clear stale ignore and reopen!
		delete(s.ignored, clean.ItemKey)
	}

	for i := range s.records {
		cur := &s.records[i]
		if cur.ItemKey != clean.ItemKey {
			continue
		}
		if cur.State == ConflictStateResolving {
			return *cur, false, nil
		}
		if cur.RemoteFingerprint == clean.RemoteFingerprint &&
			cur.ReasonKind == clean.ReasonKind &&
			cur.LocalSummary == clean.LocalSummary &&
			cur.RemoteSummary == clean.RemoteSummary {
			updatedMeta := false
			if cur.Title == cur.TargetID && clean.Title != "" && clean.Title != clean.TargetID {
				cur.Title = clean.Title
				updatedMeta = true
			}
			if cur.AuthorName == "" && clean.AuthorName != "" {
				cur.AuthorName = clean.AuthorName
				updatedMeta = true
			}
			if cur.AuthorUID == "" && clean.AuthorUID != "" {
				cur.AuthorUID = clean.AuthorUID
				updatedMeta = true
			}
			if updatedMeta {
				if err := s.saveLocked(); err != nil {
					return *cur, false, err
				}
			}
			return *cur, updatedMeta, nil
		}
		prev := *cur
		clean.ID = cur.ID
		clean.State = ConflictStatePending
		clean.LastError = ""
		*cur = clean
		delete(s.checkedNonLegacy, clean.ID)
		if err := s.saveLocked(); err != nil {
			*cur = prev
			return prev, false, err
		}
		return *cur, true, nil
	}

	delete(s.checkedNonLegacy, clean.ID)
	s.records = append(s.records, clean)
	if err := s.saveLocked(); err != nil {
		s.records = s.records[:len(s.records)-1]
		return cacheConflictRecord{}, false, err
	}
	return clean, true, nil
}

func (s *cacheConflictStore) pendingTextConflictCandidates() []cacheConflictRecord {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []cacheConflictRecord
	for _, r := range s.records {
		if r.State != ConflictStatePending || r.ReasonKind != ConflictReasonText {
			continue
		}
		if checkedFP, ok := s.checkedNonLegacy[r.ID]; ok && checkedFP == r.RemoteFingerprint {
			continue
		}
		out = append(out, r)
	}
	return out
}

func (s *cacheConflictStore) markNonLegacyTextConflict(id, fingerprint string) {
	if s == nil || id == "" {
		return
	}
	s.mu.Lock()
	if s.checkedNonLegacy == nil {
		s.checkedNonLegacy = make(map[string]string)
	}
	s.checkedNonLegacy[id] = fingerprint
	s.mu.Unlock()
}

func (s *cacheConflictStore) pendingTextConflictForItem(itemKey string) (cacheConflictRecord, bool) {
	if s == nil || strings.TrimSpace(itemKey) == "" {
		return cacheConflictRecord{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.records {
		if r.ItemKey == itemKey && r.State == ConflictStatePending && r.ReasonKind == ConflictReasonText {
			return r, true
		}
	}
	return cacheConflictRecord{}, false
}

func (s *cacheConflictStore) hasPendingTextConflictForItem(itemKey string) bool {
	_, ok := s.pendingTextConflictForItem(itemKey)
	return ok
}

func (s *cacheConflictStore) removePendingIfMatching(id, fingerprint string) bool {
	if s == nil || strings.TrimSpace(id) == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.records {
		r := s.records[i]
		if r.ID != id || r.State != ConflictStatePending || r.ReasonKind != ConflictReasonText {
			continue
		}
		if fingerprint != "" && r.RemoteFingerprint != fingerprint {
			return false
		}
		prev := append([]cacheConflictRecord(nil), s.records...)
		s.records = append(s.records[:i], s.records[i+1:]...)
		delete(s.checkedNonLegacy, id)
		if err := s.saveLocked(); err != nil {
			s.records = prev
			return false
		}
		return true
	}
	return false
}

func (s *cacheConflictStore) count() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.records)
}

func (s *cacheConflictStore) avatarKeys() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, r := range s.records {
		if name := avatarFileName(r.Site, r.AuthorUID); name != "" {
			out = append(out, name)
		}
	}
	return out
}

func (s *cacheConflictStore) snapshotSorted() []cacheConflictRecord {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	cp := make([]cacheConflictRecord, len(s.records))
	copy(cp, s.records)
	s.mu.Unlock()

	sort.SliceStable(cp, func(i, j int) bool {
		ti, errI := time.Parse(time.RFC3339, cp[i].DetectedAt)
		tj, errJ := time.Parse(time.RFC3339, cp[j].DetectedAt)
		if errI == nil && errJ == nil && !ti.Equal(tj) {
			return ti.After(tj)
		}
		if cp[i].DetectedAt != cp[j].DetectedAt {
			return cp[i].DetectedAt > cp[j].DetectedAt
		}
		return cp[i].ID < cp[j].ID
	})
	return cp
}

func (b *Backend) recordDownloadConflictNotice(n download.ConflictNotice) {
	if b == nil || b.profile == ProfileRemote || b.conflicts == nil {
		return
	}
	rec := cacheConflictRecord{
		ItemKey:           n.ItemKey,
		Site:              n.Site,
		Kind:              n.Kind,
		TargetID:          n.TargetID,
		AuthorUID:         n.AuthorUID,
		AuthorName:        n.AuthorName,
		Title:             n.Title,
		Origin:            firstNonEmpty(n.Origin, "download"),
		ReasonKind:        n.ReasonKind,
		LocalSummary:      n.LocalSummary,
		RemoteSummary:     n.RemoteSummary,
		RemoteFingerprint: n.RemoteFingerprint,
		Via:               n.Via,
	}
	if rec.AuthorUID == "" || rec.AuthorName == "" || rec.Title == "" {
		if b.lib != nil && rec.ItemKey != "" {
			if meta, ok, err := b.lib.GetItemMetaOnly(rec.ItemKey); err == nil && ok {
				if rec.AuthorUID == "" {
					rec.AuthorUID = itemAuthorUIDString(meta)
				}
				if rec.AuthorName == "" {
					rec.AuthorName = meta.Author.Name
				}
				if rec.Title == "" {
					rec.Title = firstNonEmpty(meta.Title, meta.Desc, meta.Text)
				}
				if rec.Via == nil && meta.Via != nil {
					cp := *meta.Via
					rec.Via = &cp
				}
			}
		}
	}
	saved, changed, err := b.conflicts.recordOrUpdate(rec)
	if err != nil {
		b.logError("conflicts.record_notice", err)
		return
	}
	if changed && b.events != nil {
		b.events.PublishSite(EventCacheConflictsChanged, saved.Site, saved.ItemKey)
	}
}

func itemAuthorUIDString(meta library.ItemMetadata) string {
	if strings.TrimSpace(meta.Author.UID) != "" {
		return strings.TrimSpace(meta.Author.UID)
	}
	if meta.Author.Mid > 0 {
		return strconv.FormatInt(meta.Author.Mid, 10)
	}
	return ""
}
