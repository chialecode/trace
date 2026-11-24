package download

import (
	"context"
	"errors"
	"strings"

	"github.com/chialecode/trace/internal/library"
)

// Explicit conflict resolution modes passed into download entrypoints when the
// user resolves a V1-vs-V2 cache conflict from Tasks › 内容冲突.
const (
	ConflictResolveNone      = ""
	ConflictResolveKeepLocal = "keep_local"
	ConflictResolveKeepBoth  = "keep_both"
	ConflictResolveUseRemote = "use_remote"
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
	m.mu.Lock()
	fn := m.onConflict
	m.mu.Unlock()
	if fn != nil {
		fn(n)
	}
}
