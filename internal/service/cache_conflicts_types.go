package service

import (
	"context"
	"strings"

	"github.com/chialecode/trace/internal/download"
)

// Cache conflict resolution choices (Tasks › 内容冲突).
// ConflictKeepBoth ("保留两版（推荐）") is the archive-first default batch choice.
const (
	ConflictKeepLocal     = "keep_local"
	ConflictKeepBoth      = "keep_both"
	ConflictUseRemote     = "use_remote"
	DefaultConflictChoice = ConflictKeepBoth
)

// Cache conflict reason kinds.
const (
	ConflictReasonParts    = "part_changed"
	ConflictReasonMedia    = "media_changed"
	ConflictReasonText     = "text_edited"
	ConflictReasonMismatch = "resource_mismatch"
)

// Cache conflict lifecycle states.
const (
	ConflictStatePending   = "pending"
	ConflictStateResolving = "resolving"
	ConflictStateFailed    = "failed"
)

// ErrRemoteDegradedRefused reports that remote content was deleted, restricted,
// preview-only, or degraded at resolution time so V1 was preserved untouched.
var ErrRemoteDegradedRefused = download.ErrRemoteDegradedRefused

// ConflictReasonLabel returns the human-readable Chinese badge label for reasonKind.
func ConflictReasonLabel(reasonKind string) string {
	switch strings.TrimSpace(reasonKind) {
	case ConflictReasonParts:
		return "分P变更"
	case ConflictReasonMedia:
		return "媒体替换"
	case ConflictReasonText:
		return "正文编辑"
	case ConflictReasonMismatch:
		return "资源不一致"
	default:
		return "资源不一致"
	}
}

// CacheConflictDTO describes one V1-vs-V2 cache conflict surfaced on Tasks › 内容冲突.
// It never contains media URLs, query parameters, CDN hostnames, or credentials.
type CacheConflictDTO struct {
	ID                string `json:"id"`
	ItemKey           string `json:"itemKey"`
	Site              string `json:"site"`
	Kind              string `json:"kind"`
	TargetID          string `json:"targetId"`
	AuthorUID         string `json:"authorUid,omitempty"`
	AuthorName        string `json:"authorName,omitempty"`
	AvatarKey         string `json:"avatarKey,omitempty"`
	Title             string `json:"title"`
	DetectedAt        string `json:"detectedAt"`
	Origin            string `json:"origin,omitempty"`
	ReasonKind        string `json:"reasonKind"`
	ReasonLabel       string `json:"reasonLabel"`
	LocalSummary      string `json:"localSummary"`
	RemoteSummary     string `json:"remoteSummary"`
	RemoteFingerprint string `json:"remoteFingerprint,omitempty"`
	State             string `json:"state"`
	LastError         string `json:"lastError,omitempty"`
}

// ConflictBatchResult summarizes a batch conflict resolution run.
type ConflictBatchResult struct {
	Applied     int `json:"applied"`
	Failed      int `json:"failed"`
	SkippedAuth int `json:"skippedAuth"`
	SkippedBusy int `json:"skippedBusy"`
}

// ConflictService manages V1-vs-V2 cache conflicts on desktop installations.
type ConflictService interface {
	ListCacheConflicts() ([]CacheConflictDTO, error)
	PendingCacheConflictCount() int
	ResolveCacheConflict(ctx context.Context, id, choice string) error
	ResolveAllCacheConflicts(ctx context.Context, choice string) (ConflictBatchResult, error)
}

var _ ConflictService = (*Backend)(nil)
