package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site/bilibili"
)

// Explicit conflict resolution modes passed into download entrypoints when the
// user resolves a V1-vs-V2 cache conflict from Tasks › 内容冲突.
const (
	ConflictResolveNone      = ""
	ConflictResolveKeepLocal = "keep_local"
	ConflictResolveKeepBoth  = "keep_both"
	ConflictResolveUseRemote = "use_remote"
)

// Sanitized conflict reason kinds emitted by the download-layer tripwire.
const (
	ConflictReasonParts    = "part_changed"
	ConflictReasonMedia    = "media_changed"
	ConflictReasonText     = "text_edited"
	ConflictReasonMismatch = "resource_mismatch"
)

var (
	// ErrRemoteDegradedRefused is returned when remote media is deleted,
	// unavailable, VIP-preview-only, geo-blocked, a placeholder GIF, or empty
	// while local cache has valid media; even use_remote refuses to overwrite V1.
	ErrRemoteDegradedRefused = errors.New("远端内容已失效、受限或降级，已拒绝覆盖本地缓存")

	// ErrConflictDetected is returned when the download-layer tripwire detects
	// a V1-vs-V2 mismatch against an existing cached item without an explicit
	// keep_both or use_remote resolution mode.
	ErrConflictDetected = errors.New("源站内容与本地已缓存版本不一致，已转入任务页“内容冲突”待处理")
)

type conflictResolutionCtxKey struct{}

// WithConflictResolution tags ctx with an explicit conflict resolution mode
// ("keep_both" or "use_remote") so download entrypoints permit staged V2 execution.
func WithConflictResolution(ctx context.Context, mode string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, conflictResolutionCtxKey{}, strings.TrimSpace(mode))
}

// ConflictResolutionFromContext extracts any explicit conflict resolution mode from ctx.
func ConflictResolutionFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(conflictResolutionCtxKey{}).(string)
	return strings.TrimSpace(v)
}

func normalizeConflictResolution(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case ConflictResolveKeepBoth:
		return ConflictResolveKeepBoth
	case ConflictResolveUseRemote:
		return ConflictResolveUseRemote
	case ConflictResolveKeepLocal:
		return ConflictResolveKeepLocal
	default:
		return ConflictResolveNone
	}
}

func effectiveConflictResolution(ctx context.Context, vals ...string) string {
	if m := normalizeConflictResolution(ConflictResolutionFromContext(ctx)); m != "" {
		return m
	}
	for _, v := range vals {
		if m := normalizeConflictResolution(v); m != "" {
			return m
		}
	}
	return ConflictResolveNone
}

// ConflictNotice carries strictly sanitized V1-vs-V2 conflict metadata (zero
// URLs, query parameters, CDN hostnames, or credentials) from the download
// tripwire to the service conflict store.
type ConflictNotice struct {
	ItemKey           string
	Site              string
	Kind              string
	TargetID          string
	AuthorUID         string
	AuthorName        string
	Title             string
	Origin            string
	ReasonKind        string
	LocalSummary      string
	RemoteSummary     string
	RemoteFingerprint string
	Via               *library.ItemVia
}

// SetConflictHandler registers an optional callback invoked when the download
// tripwire aborts an unapproved V1-vs-V2 overwrite so service can persist it
// into cache-conflicts.json.
func (m *Manager) SetConflictHandler(fn func(ConflictNotice)) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.onConflict = fn
	m.mu.Unlock()
}

func (m *Manager) emitConflict(n ConflictNotice) {
	if m == nil {
		return
	}
	n = sanitizeConflictNotice(n)
	m.mu.Lock()
	fn := m.onConflict
	m.mu.Unlock()
	if fn != nil {
		fn(n)
	}
}

func (m *Manager) storeTaskResolutionAndPages(taskID, mode string, pages []int) {
	if m == nil || taskID == "" {
		return
	}
	mode = normalizeConflictResolution(mode)
	m.mu.Lock()
	if m.conflictResolutions == nil {
		m.conflictResolutions = make(map[string]string)
	}
	if mode != "" {
		m.conflictResolutions[taskID] = mode
		if ip, ok := m.initialPlans[taskID]; ok && ip.ConflictResolution == "" {
			ip.ConflictResolution = mode
			m.initialPlans[taskID] = ip
		}
	}
	if len(pages) > 0 {
		if m.conflictPages == nil {
			m.conflictPages = make(map[string][]int)
		}
		m.conflictPages[taskID] = append([]int(nil), pages...)
	}
	m.mu.Unlock()
}

func (m *Manager) taskResolutionAndPages(taskID string) (string, []int) {
	if m == nil || taskID == "" {
		return "", nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	mode := m.conflictResolutions[taskID]
	if mode == "" {
		if ip, ok := m.initialPlans[taskID]; ok {
			mode = ip.ConflictResolution
		}
	}
	pages := append([]int(nil), m.conflictPages[taskID]...)
	return normalizeConflictResolution(mode), pages
}

func (m *Manager) clearTaskResolutionStateLocked(taskID string) {
	delete(m.conflictResolutions, taskID)
	delete(m.conflictPages, taskID)
}

func (m *Manager) recordTaskOutcome(taskID string, err error) {
	if m == nil || taskID == "" {
		return
	}
	m.taskWaitMu.Lock()
	if m.taskOutcomes == nil {
		m.taskOutcomes = make(map[string]error)
	}
	if err == nil {
		delete(m.taskOutcomes, taskID)
	} else {
		if len(m.taskOutcomes) >= 1024 {
			for k := range m.taskOutcomes {
				delete(m.taskOutcomes, k)
				break
			}
		}
		m.taskOutcomes[taskID] = err
	}
	m.broadcastTaskWaitLocked()
	m.taskWaitMu.Unlock()
}

func (m *Manager) clearTaskOutcome(taskID string) {
	if m == nil || taskID == "" {
		return
	}
	m.taskWaitMu.Lock()
	delete(m.taskOutcomes, taskID)
	m.taskWaitMu.Unlock()
}

func (m *Manager) getTaskOutcome(taskID string) error {
	if m == nil || taskID == "" {
		return nil
	}
	m.taskWaitMu.Lock()
	defer m.taskWaitMu.Unlock()
	return m.taskOutcomes[taskID]
}

func (m *Manager) notifyTaskWaiters() {
	if m == nil {
		return
	}
	m.taskWaitMu.Lock()
	m.broadcastTaskWaitLocked()
	m.taskWaitMu.Unlock()
}

func (m *Manager) broadcastTaskWaitLocked() {
	if m.taskWaitCh != nil {
		close(m.taskWaitCh)
	}
	m.taskWaitCh = make(chan struct{})
}

func (m *Manager) taskWaitChannel() <-chan struct{} {
	m.taskWaitMu.Lock()
	defer m.taskWaitMu.Unlock()
	if m.taskWaitCh == nil {
		m.taskWaitCh = make(chan struct{})
	}
	return m.taskWaitCh
}

func (m *Manager) isTaskRunning(taskID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.activeRun[taskID]
	return ok
}

// WaitTasks blocks until all specified tasks leave queued/running state (or ctx
// is canceled), waking reactively on task transitions without busy-spinning.
// It returns nil when every task succeeded, or the task error preserving
// ErrRemoteDegradedRefused and ErrConflictDetected sentinel wrapping.
func (m *Manager) WaitTasks(ctx context.Context, taskIDs ...string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if m == nil {
		return ctx.Err()
	}
	seen := make(map[string]bool, len(taskIDs))
	ids := make([]string, 0, len(taskIDs))
	for _, raw := range taskIDs {
		id := strings.TrimSpace(raw)
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return ctx.Err()
	}

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		waitCh := m.taskWaitChannel()

		stillActive := false
		for _, id := range ids {
			if m.isTaskRunning(id) {
				stillActive = true
				break
			}
			t, ok, err := m.GetTask(id)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			switch t.Status {
			case "queued", "running":
				stillActive = true
			case "completed":
				continue
			case "partial":
				if rawErr := m.getTaskOutcome(id); rawErr != nil &&
					(errors.Is(rawErr, ErrRemoteDegradedRefused) || errors.Is(rawErr, ErrConflictDetected)) {
					return rawErr
				}
			case "failed", "canceled", "paused":
				if rawErr := m.getTaskOutcome(id); rawErr != nil {
					return rawErr
				}
				return reconstructStoppedTaskError(t)
			default:
				if strings.HasPrefix(t.Status, "paused_") {
					if rawErr := m.getTaskOutcome(id); rawErr != nil {
						return rawErr
					}
					return reconstructStoppedTaskError(t)
				}
				stillActive = true
			}
			if stillActive {
				break
			}
		}
		if !stillActive {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-waitCh:
		case <-ticker.C:
		}
	}
}

func reconstructStoppedTaskError(t Task) error {
	msg := strings.TrimSpace(firstNonEmptyStr(t.ErrorMessage, t.LastError))
	if strings.Contains(msg, ErrRemoteDegradedRefused.Error()) {
		if msg == ErrRemoteDegradedRefused.Error() {
			return ErrRemoteDegradedRefused
		}
		return fmt.Errorf("%w: %s", ErrRemoteDegradedRefused, msg)
	}
	if strings.Contains(msg, ErrConflictDetected.Error()) {
		if msg == ErrConflictDetected.Error() {
			return ErrConflictDetected
		}
		return fmt.Errorf("%w: %s", ErrConflictDetected, msg)
	}
	if t.Status == "canceled" {
		return context.Canceled
	}
	if msg != "" {
		return errors.New(msg)
	}
	return fmt.Errorf("download task %s ended with status %s", t.TaskID, t.Status)
}

// NormalizeConflictText applies manifest URL/token scrubbing and collapses
// whitespace so download tripwire checks and service list-level inspections
// compare text identically without false positives on scrubbed media/signed URLs.
func NormalizeConflictText(s string) string {
	scrubbed := library.ScrubComparableManifestText(s)
	if scrubbed == "" {
		return ""
	}
	return strings.Join(strings.Fields(scrubbed), " ")
}

// LooksLikeTruncatedTextPrefix reports whether shortText is a truncated snippet
// (e.g. Weibo "...展开" / "…全文") of fullText.
func LooksLikeTruncatedTextPrefix(shortText, fullText string) bool {
	shortNorm := NormalizeConflictText(shortText)
	fullNorm := NormalizeConflictText(fullText)
	if shortNorm == "" || fullNorm == "" || len([]rune(fullNorm)) <= len([]rune(shortNorm)) {
		return false
	}
	trimmed := strings.TrimSpace(strings.TrimRight(shortNorm, ".…"))
	for _, suf := range []string{"查看全文", "全文", "展开"} {
		trimmed = strings.TrimSpace(strings.TrimSuffix(trimmed, suf))
	}
	trimmed = strings.TrimSpace(strings.TrimRight(trimmed, ".…"))
	if len([]rune(trimmed)) < 4 {
		return false
	}
	return strings.HasPrefix(fullNorm, trimmed)
}

// ComputeConflictFingerprint returns a canonical 64-character lowercase hex
// SHA-256 structural fingerprint shared by download tripwires and service sweeps.
func ComputeConflictFingerprint(parts ...string) string {
	h := sha256.New()
	for i, p := range parts {
		if i > 0 {
			_, _ = h.Write([]byte{'\x1f'})
		}
		_, _ = h.Write([]byte(strings.TrimSpace(p)))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// BilibiliVideoConflictFingerprint computes the canonical structural fingerprint
// for a Bilibili video across service probes and download tripwires.
func BilibiliVideoConflictFingerprint(bvid string, pages []bilibili.VideoPage, title, desc string) string {
	tokens := make([]string, 0, len(pages))
	for _, pg := range pages {
		tokens = append(tokens, fmt.Sprintf("P%02d:%d:%d", pg.Page, pg.CID, pg.Duration))
	}
	return ComputeConflictFingerprint(
		"bilibili:video:"+strings.TrimSpace(bvid),
		strings.Join(tokens, ","),
		NormalizeConflictText(title),
		NormalizeConflictText(desc),
	)
}

// BilibiliDynamicConflictFingerprint computes the canonical structural
// fingerprint for a Bilibili dynamic (including flattened origin media).
func BilibiliDynamicConflictFingerprint(dynID string, imageURLs []string, videoBVIDs []string, title, text string) string {
	seenImg := make(map[string]bool, len(imageURLs))
	imgPaths := make([]string, 0, len(imageURLs))
	for _, raw := range imageURLs {
		p := cleanDynamicURLPathForFingerprint(raw)
		if p != "" && !seenImg[p] {
			seenImg[p] = true
			imgPaths = append(imgPaths, p)
		}
	}
	seenVid := make(map[string]bool, len(videoBVIDs))
	vidRefs := make([]string, 0, len(videoBVIDs))
	for _, raw := range videoBVIDs {
		bvid := strings.TrimPrefix(strings.TrimSpace(raw), "video:")
		if bvid != "" && !seenVid[bvid] {
			seenVid[bvid] = true
			vidRefs = append(vidRefs, "video:"+bvid)
		}
	}
	return ComputeConflictFingerprint(
		"bilibili:dynamic:"+strings.TrimSpace(dynID),
		strings.Join(imgPaths, ","),
		strings.Join(vidRefs, ","),
		NormalizeConflictText(title),
		NormalizeConflictText(text),
	)
}

func cleanDynamicURLPathForFingerprint(rawURL string) string {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return ""
	}
	if u, err := url.Parse(trimmed); err == nil && u.Path != "" {
		return u.Path
	}
	if idx := strings.IndexAny(trimmed, "?#"); idx >= 0 {
		trimmed = trimmed[:idx]
	}
	return trimmed
}

// WorkConflictFingerprint computes the canonical structural fingerprint for a
// Douyin, Weibo, or X work shared by service list-diff sweeps and download tripwires.
func WorkConflictFingerprint(siteID, remoteKind, targetID string, assetIDs []string, editCount int, pageCount int, singleVideoDurationSec int, focalText string) string {
	normSite := strings.ToLower(strings.TrimSpace(siteID))
	normKind := "dynamic"
	if strings.EqualFold(strings.TrimSpace(remoteKind), "video") || strings.EqualFold(strings.TrimSpace(remoteKind), "single_mp4") {
		normKind = "video"
	}
	normTarget := strings.TrimSpace(targetID)
	if normSite == "weibo" && normKind == "video" {
		normTarget = strings.ReplaceAll(normTarget, ":", "_")
	}
	cleaned := make([]string, 0, len(assetIDs))
	for _, raw := range assetIDs {
		if tok := library.SanitizeMediaToken(raw); tok != "" {
			cleaned = append(cleaned, tok)
		}
	}
	if pageCount > 0 {
		singleVideoDurationSec = 0
	}
	return ComputeConflictFingerprint(
		normSite,
		normKind,
		normTarget,
		strings.Join(cleaned, ","),
		fmt.Sprintf("%d", max(0, editCount)),
		fmt.Sprintf("%d", max(0, pageCount)),
		fmt.Sprintf("%d", max(0, singleVideoDurationSec)),
		NormalizeConflictText(focalText),
	)
}
