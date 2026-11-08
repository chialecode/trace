package storage

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// MaxHistoryItems is the maximum number of navigation history entries stored in history.json.
const MaxHistoryItems = 200

// MaxHistoryThumbBytes caps the size of a single cached parse-history thumbnail (R11).
const MaxHistoryThumbBytes = 10 * 1024 * 1024

// HistoryEntry represents a single sanitized navigation history item.
type HistoryEntry struct {
	Site         string `json:"site"`
	Kind         string `json:"kind"`
	ID           string `json:"id"`
	Title        string `json:"title"`
	Subtitle     string `json:"subtitle,omitempty"`
	CanonicalURL string `json:"canonicalUrl"`
	VisitedAt    string `json:"visitedAt"`
	AvatarThumb  string `json:"avatarThumb,omitempty"`
	CoverThumb   string `json:"coverThumb,omitempty"`
	DurationText string `json:"durationText,omitempty"`
}

// HistoryFile is the on-disk schema of history.json.
type HistoryFile struct {
	Version             int            `json:"version"`
	Items               []HistoryEntry `json:"items"`
	PendingThumbDeletes []string       `json:"pendingThumbDeletes,omitempty"`
}

// HistoryThumbnail contains already fetched thumbnail bytes. RecordWithThumbs
// commits these files and their history references under the same store lock.
type HistoryThumbnail struct {
	Role string
	Ext  string
	Data []byte
}

type historyThumbOwner struct {
	Kind string
	ID   string
}

// HistoryStore manages history.json and its local thumbnail cache (<DataDir>/thumbs/) in DataDir.
type HistoryStore struct {
	mu        sync.Mutex
	path      string
	thumbsDir string
	data      HistoryFile
	staged    map[string]historyThumbOwner
}

// OpenHistory loads history.json from path. If the file is corrupted, it backs it up and resets.
func OpenHistory(path string) (*HistoryStore, string, error) {
	s := &HistoryStore{
		path:      path,
		thumbsDir: filepath.Join(filepath.Dir(path), "thumbs"),
		staged:    make(map[string]historyThumbOwner),
		data: HistoryFile{
			Version: 1,
			Items:   []HistoryEntry{},
		},
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, "", nil
		}
		return nil, "", err
	}

	var loaded HistoryFile
	if err := json.Unmarshal(raw, &loaded); err != nil {
		backup, bErr := BackupCorruptFile(path)
		if bErr != nil {
			return nil, "", bErr
		}
		_ = WriteJSONAtomic(path, s.data, 0o600)
		return s, backup, nil
	}

	if loaded.Version <= 0 {
		loaded.Version = 1
	}
	clean := make([]HistoryEntry, 0, len(loaded.Items))
	seen := make(map[string]bool, len(loaded.Items))
	for _, it := range loaded.Items {
		sanitized, ok := sanitizeHistoryEntry(it)
		if !ok {
			continue
		}
		key := historyKey(sanitized)
		if seen[key] {
			continue
		}
		seen[key] = true
		clean = append(clean, sanitized)
		if len(clean) >= MaxHistoryItems {
			break
		}
	}
	s.data.Version = loaded.Version
	s.data.Items = clean
	for _, name := range loaded.PendingThumbDeletes {
		if clean := sanitizeThumbFileName(name); clean != "" {
			s.data.PendingThumbDeletes = append(s.data.PendingThumbDeletes, clean)
		}
	}
	return s, "", nil
}

// List returns a copy of the history entries (newest first).
func (s *HistoryStore) List() []HistoryEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]HistoryEntry, len(s.data.Items))
	copy(out, s.data.Items)
	return out
}

// Record adds or updates an entry at the top of the history list and persists atomically.
func (s *HistoryStore) Record(entry HistoryEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recordLocked(entry)
}

// RecordWithThumbs serializes thumbnail creation with the history commit so
// concurrent clear/remove operations cannot delete a not-yet-committed image.
// Thumbnail failures are optional; the entry keeps its previous image or a placeholder.
func (s *HistoryStore) RecordWithThumbs(entry HistoryEntry, thumbs ...HistoryThumbnail) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := sanitizeHistoryEntry(entry); !ok {
		return nil
	}
	for _, thumb := range thumbs {
		name, err := s.saveThumbLocked(entry.Kind, entry.ID, thumb.Role, thumb.Ext, thumb.Data)
		if err != nil {
			continue
		}
		switch thumb.Role {
		case "avatar":
			entry.AvatarThumb = name
		case "cover":
			entry.CoverThumb = name
		}
	}
	return s.recordLocked(entry)
}

// AttachThumbs adds thumbnails fetched after an entry was recorded. The entry
// keeps its place; when it was removed or cleared meanwhile, nothing is
// written and the new files are discarded.
func (s *HistoryStore) AttachThumbs(site, kind, id string, thumbs ...HistoryThumbnail) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := historyKey(HistoryEntry{Site: site, Kind: kind, ID: id})
	idx := -1
	for i, existing := range s.data.Items {
		if historyKey(existing) == key {
			idx = i
			break
		}
	}
	if idx < 0 || len(thumbs) == 0 {
		return nil
	}
	entry := s.data.Items[idx]
	changed := false
	for _, thumb := range thumbs {
		name, err := s.saveThumbLocked(entry.Kind, entry.ID, thumb.Role, thumb.Ext, thumb.Data)
		if err != nil {
			continue
		}
		switch thumb.Role {
		case "avatar":
			changed = changed || entry.AvatarThumb != name
			entry.AvatarThumb = name
		case "cover":
			changed = changed || entry.CoverThumb != name
			entry.CoverThumb = name
		}
	}
	if !changed {
		return nil
	}
	next := append([]HistoryEntry(nil), s.data.Items...)
	next[idx] = entry
	pending := s.pendingThumbsLocked(next)
	for name, owner := range s.staged {
		if owner.Kind == strings.ToLower(strings.TrimSpace(entry.Kind)) && owner.ID == strings.TrimSpace(entry.ID) {
			pending = append(pending, name)
		}
	}
	updated := HistoryFile{Version: s.data.Version, Items: next, PendingThumbDeletes: unreferencedThumbs(next, pending)}
	if err := WriteJSONAtomic(s.path, updated, 0o600); err != nil {
		for _, name := range []string{entry.AvatarThumb, entry.CoverThumb} {
			if _, staged := s.staged[historyThumbKey(name)]; staged && !thumbReferences(s.data.Items)[historyThumbKey(name)] {
				err = errors.Join(err, s.removeThumbFileLocked(name))
			}
		}
		return err
	}
	s.data = updated
	return s.cleanupThumbsLocked()
}

func (s *HistoryStore) recordLocked(entry HistoryEntry) error {
	sanitized, ok := sanitizeHistoryEntry(entry)
	if !ok {
		return nil
	}
	if sanitized.VisitedAt == "" {
		sanitized.VisitedAt = time.Now().UTC().Format(time.RFC3339)
	}

	key := historyKey(sanitized)
	var evicted []HistoryEntry
	for _, existing := range s.data.Items {
		if historyKey(existing) == key {
			if sanitized.AvatarThumb == "" && existing.AvatarThumb != "" {
				sanitized.AvatarThumb = existing.AvatarThumb
			}
			if sanitized.CoverThumb == "" && existing.CoverThumb != "" {
				sanitized.CoverThumb = existing.CoverThumb
			}
			if sanitized.DurationText == "" && existing.DurationText != "" {
				sanitized.DurationText = existing.DurationText
			}
			break
		}
	}

	next := make([]HistoryEntry, 0, len(s.data.Items)+1)
	next = append(next, sanitized)
	for _, existing := range s.data.Items {
		if historyKey(existing) == key {
			continue
		}
		if len(next) < MaxHistoryItems {
			next = append(next, existing)
		} else {
			evicted = append(evicted, existing)
		}
	}
	pending := s.pendingThumbsLocked(next)
	for name, owner := range s.staged {
		if owner.Kind == sanitized.Kind && owner.ID == sanitized.ID {
			pending = append(pending, name)
		}
		for _, old := range evicted {
			if owner.Kind == old.Kind && owner.ID == old.ID {
				pending = append(pending, name)
			}
		}
	}
	updated := HistoryFile{Version: s.data.Version, Items: next, PendingThumbDeletes: unreferencedThumbs(next, pending)}
	if err := WriteJSONAtomic(s.path, updated, 0o600); err != nil {
		// Only discard files created by this store and absent from committed history.
		for _, name := range []string{sanitized.AvatarThumb, sanitized.CoverThumb} {
			if _, staged := s.staged[historyThumbKey(name)]; staged && !thumbReferences(s.data.Items)[historyThumbKey(name)] {
				err = errors.Join(err, s.removeThumbFileLocked(name))
			}
		}
		return err
	}
	s.data = updated
	return s.cleanupThumbsLocked()
}

// Clear empties the history, removes all cached history thumbnails, and persists atomically.
func (s *HistoryStore) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending := s.pendingThumbsLocked(nil)
	for name := range s.staged {
		pending = append(pending, name)
	}
	next := HistoryFile{Version: s.data.Version, Items: []HistoryEntry{}, PendingThumbDeletes: unreferencedThumbs(nil, pending)}
	if err := WriteJSONAtomic(s.path, next, 0o600); err != nil {
		return err
	}
	s.data = next
	return s.cleanupThumbsLocked()
}

func historyKey(e HistoryEntry) string {
	return strings.ToLower(strings.TrimSpace(e.Site)) + ":" +
		strings.ToLower(strings.TrimSpace(e.Kind)) + ":" +
		strings.TrimSpace(e.ID)
}

var sensitiveMarkers = []string{
	"sessdata=",
	"bili_jct=",
	"dedeuserid=",
	"refresh_token=",
	"access_key=",
	"w_rid=",
	"upsig=",
}

func sanitizeHistoryEntry(e HistoryEntry) (HistoryEntry, bool) {
	e.Site = strings.ToLower(strings.TrimSpace(e.Site))
	if e.Site == "" {
		e.Site = "bilibili"
	}
	e.Kind = strings.ToLower(strings.TrimSpace(e.Kind))
	e.ID = strings.TrimSpace(e.ID)
	if e.Kind == "" || e.ID == "" {
		return HistoryEntry{}, false
	}

	// Reject IDs that look like raw credential strings or query strings.
	lowerID := strings.ToLower(e.ID)
	for _, m := range sensitiveMarkers {
		if strings.Contains(lowerID, m) {
			return HistoryEntry{}, false
		}
	}

	e.Title = sanitizeText(e.Title)
	e.Subtitle = sanitizeText(e.Subtitle)
	e.DurationText = sanitizeText(e.DurationText)
	e.AvatarThumb = sanitizeThumbFileName(e.AvatarThumb)
	e.CoverThumb = sanitizeThumbFileName(e.CoverThumb)
	e.CanonicalURL = sanitizeCanonicalURL(e.CanonicalURL, e.Kind, e.ID)
	return e, true
}

func sanitizeText(s string) string {
	s = strings.TrimSpace(s)
	lower := strings.ToLower(s)
	for _, m := range sensitiveMarkers {
		if strings.Contains(lower, m) {
			return "[redacted]"
		}
	}
	return s
}

func sanitizeCanonicalURL(rawURL, kind, id string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL != "" {
		if u, err := url.Parse(rawURL); err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" {
			u.Scheme = "https"
			u.User = nil
			u.RawQuery = ""
			u.Fragment = ""
			return u.String()
		}
	}
	switch kind {
	case "video":
		return "https://www.bilibili.com/video/" + id
	case "user":
		return "https://space.bilibili.com/" + id
	case "dynamic":
		return "https://t.bilibili.com/" + id
	case "live":
		cleanID := strings.TrimPrefix(id, "live:")
		return "https://live.bilibili.com/" + cleanID
	default:
		return ""
	}
}

// Remove deletes exactly one kind/id record, removes its cached thumbnails, and commits before changing memory.
func (s *HistoryStore) Remove(kind, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	kind, id = strings.ToLower(strings.TrimSpace(kind)), strings.TrimSpace(id)
	next := HistoryFile{Version: s.data.Version, Items: []HistoryEntry{}}
	for _, it := range s.data.Items {
		if it.Kind != kind || it.ID != id {
			next.Items = append(next.Items, it)
		}
	}
	pending := s.pendingThumbsLocked(next.Items)
	for name, owner := range s.staged {
		if owner.Kind == kind && owner.ID == id {
			pending = append(pending, name)
		}
	}
	next.PendingThumbDeletes = unreferencedThumbs(next.Items, pending)
	if err := WriteJSONAtomic(s.path, next, 0o600); err != nil {
		return err
	}
	s.data = next
	return s.cleanupThumbsLocked()
}

// ThumbsDir returns the local directory used for recent parse thumbnails (<DataDir>/thumbs/).
func (s *HistoryStore) ThumbsDir() string {
	return s.thumbsDir
}

// SaveThumb writes a sanitized thumbnail file into <DataDir>/thumbs/ atomically and returns its base filename (R11).
// The filename never contains query strings, signatures, or path separators.
func (s *HistoryStore) SaveThumb(kind, id, role, ext string, data []byte) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveThumbLocked(kind, id, role, ext, data)
}

func (s *HistoryStore) saveThumbLocked(kind, id, role, ext string, data []byte) (string, error) {
	if len(data) == 0 || len(data) > MaxHistoryThumbBytes {
		return "", fmt.Errorf("storage: invalid thumbnail size %d", len(data))
	}
	fileName := buildThumbFileName(kind, id, role, ext, data)
	if fileName == "" {
		return "", fmt.Errorf("storage: invalid thumbnail identifiers")
	}
	if s.thumbsDir == "" {
		return "", fmt.Errorf("storage: thumbsDir not configured")
	}
	if err := os.MkdirAll(s.thumbsDir, 0o700); err != nil {
		return "", err
	}
	info, err := os.Lstat(s.thumbsDir)
	if err != nil || !info.IsDir() || info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return "", fmt.Errorf("storage: invalid thumbsDir")
	}
	fullPath := filepath.Join(s.thumbsDir, fileName)
	if linfo, lerr := os.Lstat(fullPath); lerr == nil {
		if !linfo.Mode().IsRegular() || linfo.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return "", fmt.Errorf("storage: refusing to overwrite link or directory in thumbsDir")
		}
		_, staged := s.staged[historyThumbKey(fileName)]
		if !staged && !thumbReferences(s.data.Items)[historyThumbKey(fileName)] {
			return "", fmt.Errorf("storage: refusing to overwrite unregistered thumbnail")
		}
		return fileName, nil
	} else if !os.IsNotExist(lerr) {
		return "", lerr
	}
	if err := WriteFileAtomic(fullPath, data, 0o600); err != nil {
		return "", err
	}
	s.staged[historyThumbKey(fileName)] = historyThumbOwner{Kind: strings.ToLower(strings.TrimSpace(kind)), ID: strings.TrimSpace(id)}
	return fileName, nil
}

// ThumbPath validates and resolves a cached history thumbnail filename inside <DataDir>/thumbs/ (R11).
func (s *HistoryStore) ThumbPath(name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	clean := sanitizeThumbFileName(name)
	if clean == "" || s.thumbsDir == "" {
		return "", fmt.Errorf("storage: invalid history thumbnail name")
	}
	if _, staged := s.staged[historyThumbKey(clean)]; !staged && !thumbReferences(s.data.Items)[historyThumbKey(clean)] {
		return "", fmt.Errorf("storage: history thumbnail is not registered")
	}
	dirInfo, err := os.Lstat(s.thumbsDir)
	if err != nil || !dirInfo.IsDir() || dirInfo.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return "", fmt.Errorf("storage: thumbsDir unavailable")
	}
	fullPath := filepath.Join(s.thumbsDir, clean)
	fileInfo, err := os.Lstat(fullPath)
	if err != nil || !fileInfo.Mode().IsRegular() || fileInfo.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return "", fmt.Errorf("storage: thumbnail file not found")
	}
	return fullPath, nil
}

func (s *HistoryStore) removeThumbFileLocked(name string) error {
	clean := sanitizeThumbFileName(name)
	if clean == "" || s.thumbsDir == "" {
		return fmt.Errorf("storage: invalid thumbnail cleanup name")
	}
	dirInfo, err := os.Lstat(s.thumbsDir)
	if os.IsNotExist(err) {
		delete(s.staged, historyThumbKey(clean))
		return nil
	}
	if err != nil || !dirInfo.IsDir() || dirInfo.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return fmt.Errorf("storage: refusing thumbnail cleanup in invalid directory")
	}
	fullPath := filepath.Join(s.thumbsDir, clean)
	info, err := os.Lstat(fullPath)
	if os.IsNotExist(err) {
		delete(s.staged, historyThumbKey(clean))
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return fmt.Errorf("storage: refusing to delete thumbnail link or directory")
	}
	if err := os.Remove(fullPath); err != nil {
		return err
	}
	delete(s.staged, historyThumbKey(clean))
	return nil
}

func thumbReferences(items []HistoryEntry) map[string]bool {
	refs := make(map[string]bool)
	for _, entry := range items {
		for _, name := range []string{entry.AvatarThumb, entry.CoverThumb} {
			if name != "" {
				refs[historyThumbKey(name)] = true
			}
		}
	}
	return refs
}

func historyThumbKey(name string) string {
	if runtime.GOOS == "windows" {
		return strings.ToLower(name)
	}
	return name
}

func unreferencedThumbs(items []HistoryEntry, candidates []string) []string {
	refs, pending := thumbReferences(items), make(map[string]bool)
	for _, name := range candidates {
		if clean := sanitizeThumbFileName(name); clean != "" && !refs[historyThumbKey(clean)] {
			pending[historyThumbKey(clean)] = true
		}
	}
	var out []string
	for name := range pending {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (s *HistoryStore) pendingThumbsLocked(nextItems []HistoryEntry) []string {
	pending := append([]string(nil), s.data.PendingThumbDeletes...)
	for name := range thumbReferences(s.data.Items) {
		pending = append(pending, name)
	}
	return unreferencedThumbs(nextItems, pending)
}

func (s *HistoryStore) cleanupThumbsLocked() error {
	var remaining []string
	var cleanupErr error
	for _, name := range s.data.PendingThumbDeletes {
		if err := s.removeThumbFileLocked(name); err != nil {
			remaining = append(remaining, name)
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	if len(remaining) != len(s.data.PendingThumbDeletes) {
		next := s.data
		next.PendingThumbDeletes = remaining
		if err := WriteJSONAtomic(s.path, next, 0o600); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		} else {
			s.data = next
		}
	}
	if cleanupErr != nil {
		return fmt.Errorf("storage: history saved; thumbnail cleanup must be retried: %w", cleanupErr)
	}
	return nil
}

func buildThumbFileName(kind, id, role, ext string, data []byte) string {
	safeKind := sanitizeThumbToken(kind)
	safeID := sanitizeThumbToken(id)
	safeRole := sanitizeThumbToken(role)
	if safeKind == "" || safeID == "" || (safeRole != "avatar" && safeRole != "cover") {
		return ""
	}
	safeExt := sanitizeThumbExt(ext)
	// Both identity and image contents are hashed. Different case-sensitive IDs
	// cannot collide on Windows, and refreshing a cover never changes an old file.
	hash := sha256.New()
	_, _ = hash.Write([]byte(strings.ToLower(strings.TrimSpace(kind)) + "\x00" + strings.TrimSpace(id) + "\x00" + safeRole + "\x00"))
	_, _ = hash.Write(data)
	return sanitizeThumbFileName(fmt.Sprintf("%s_%x_%s%s", strings.ToLower(safeKind), hash.Sum(nil), safeRole, safeExt))
}

func sanitizeThumbToken(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	lower := strings.ToLower(s)
	for _, m := range sensitiveMarkers {
		if strings.Contains(lower, m) {
			return ""
		}
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ':':
			b.WriteByte('_')
		}
		if b.Len() >= 80 {
			break
		}
	}
	return strings.Trim(b.String(), "_-")
}

func sanitizeThumbExt(ext string) string {
	switch strings.ToLower(strings.TrimSpace(ext)) {
	case ".png", "png":
		return ".png"
	case ".webp", "webp":
		return ".webp"
	case ".gif", "gif":
		return ".gif"
	default:
		return ".jpg"
	}
}

func sanitizeThumbFileName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 140 {
		return ""
	}
	if strings.ContainsAny(name, `/\:?&=%#`) || strings.Contains(name, "..") {
		return ""
	}
	lower := strings.ToLower(name)
	for _, m := range sensitiveMarkers {
		if strings.Contains(lower, m) {
			return ""
		}
	}
	if filepath.Base(name) != name {
		return ""
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif":
	default:
		return ""
	}
	for _, r := range name {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-') {
			return ""
		}
	}
	return name
}
