// Package remote holds what the desktop app and trace-server share: the
// protocol types of the remote API, the connection code and the HTTPS
// client that trusts only the pinned certificate (docs/design/remote.md §3).
// It depends on the standard library only.
package remote

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Protocol constants. Both sides are always the same version (DEC-062 ②):
// a different protocol is refused with 409 and both versions.
const (
	ProtocolVersion = "1"
	HeaderProtocol  = "X-Trace-Protocol"
	HeaderVersion   = "X-Trace-Version"
	DefaultPort     = 47380
	// MaxBodyBytes bounds every request body (snapshot, Cookie).
	MaxBodyBytes = 1 << 20
	APIPrefix    = "/api/v1/"
)

// Error codes of the remote API.
const (
	CodeUnauthorized       = "unauthorized"
	CodeRateLimited        = "rate_limited"
	CodeProtocol           = "protocol"
	CodeTooLarge           = "too_large"
	CodeBadRequest         = "bad_request"
	CodeCredentialRejected = "credential_rejected"
	CodeNotFound           = "not_found"
	CodeInternal           = "internal"
)

// ErrorBody is the JSON body of every error response.
type ErrorBody struct {
	Error struct {
		Code            string `json:"code"`
		Message         string `json:"message"`
		ServerVersion   string `json:"serverVersion,omitempty"`
		ServerProtocol  string `json:"serverProtocol,omitempty"`
		ClientVersion   string `json:"clientVersion,omitempty"`
		ClientProtocol  string `json:"clientProtocol,omitempty"`
		RetryAfterSecs  int    `json:"retryAfterSecs,omitempty"`
		CredentialState string `json:"credentialState,omitempty"`
	} `json:"error"`
}

// Fingerprint is the pinned identity of the remote: the lowercase hex
// SHA-256 of its leaf certificate (DER).
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// NormalizeFingerprint accepts a fingerprint with or without colons or
// spaces, in any case, and returns the 64-character lowercase form.
func NormalizeFingerprint(raw string) (string, bool) {
	r := strings.NewReplacer(":", "", " ", "", "-", "")
	fp := strings.ToLower(r.Replace(strings.TrimSpace(raw)))
	if len(fp) != 64 {
		return "", false
	}
	if _, err := hex.DecodeString(fp); err != nil {
		return "", false
	}
	return fp, true
}

// Status is GET status (remote.md §7). It never holds a credential, a
// signed address or a stream address.
type Status struct {
	Version       string             `json:"version"`
	Protocol      string             `json:"protocol"`
	Node          Node               `json:"node"`
	StartedAt     string             `json:"startedAt"`
	Now           string             `json:"now"`
	Disk          DiskStatus         `json:"disk"`
	Sites         []SiteStatus       `json:"sites"`
	Subscriptions SubscriptionsInfo  `json:"subscriptions"`
	Checks        []CheckRow         `json:"checks,omitempty"`
	Cache         CacheStatus        `json:"cache"`
	Recordings    []RecordingRow     `json:"recordings,omitempty"`
	Upload        UploadStatus       `json:"upload"`
	Errors        []ErrorRow         `json:"errors,omitempty"`
	Notes         []string           `json:"notes,omitempty"`
	Extra         map[string]float64 `json:"extra,omitempty"`
}

// Node names a TRACE installation.
type Node struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// DiskStatus is the export folder's space. Warning is set below 5 GiB,
// Paused below 1 GiB (no new cache task starts).
type DiskStatus struct {
	Free    uint64 `json:"free"`
	Total   uint64 `json:"total"`
	Warning bool   `json:"warning,omitempty"`
	Paused  bool   `json:"paused,omitempty"`
}

// Site login states on the remote.
const (
	SiteValid   = "valid"   // 有效
	SiteResync  = "resync"  // 需要重新同步 Cookie
	SiteNone    = "none"    // 没有 Cookie
	SiteBackoff = "backoff" // 退避到某时
	SiteUnknown = "unknown" // 无法确认
)

// SiteStatus is one site's login on the remote.
type SiteStatus struct {
	Site           string `json:"site"`
	State          string `json:"state"`
	Uname          string `json:"uname,omitempty"`
	CookieSyncedAt string `json:"cookieSyncedAt,omitempty"`
	BackoffUntil   string `json:"backoffUntil,omitempty"`
	Message        string `json:"message,omitempty"`
}

// SubscriptionsInfo is the remote's synced subscriptions: counts, the time
// of the last sync and the hash of the snapshot it applied.
type SubscriptionsInfo struct {
	Subscriptions int    `json:"subscriptions"`
	Accounts      int    `json:"accounts"`
	SyncedAt      string `json:"syncedAt,omitempty"`
	Hash          string `json:"hash,omitempty"`
}

// CheckRow is one account of the remote's check plan.
type CheckRow struct {
	Site          string `json:"site"`
	UID           string `json:"uid"`
	Name          string `json:"name"`
	Subscription  string `json:"subscription,omitempty"`
	LastCheckedAt string `json:"lastCheckedAt,omitempty"`
	LastResult    string `json:"lastResult,omitempty"`
	NextAt        string `json:"nextAt,omitempty"`
	Baseline      bool   `json:"baseline,omitempty"`
	Scheduled     bool   `json:"scheduled"`
}

// CacheStatus is the remote's cache tasks.
type CacheStatus struct {
	Running int       `json:"running"`
	Queued  int       `json:"queued"`
	Recent  []TaskRow `json:"recent,omitempty"`
}

// TaskRow is a recently finished or failed cache task.
type TaskRow struct {
	Site   string `json:"site"`
	Title  string `json:"title"`
	Status string `json:"status"`
	At     string `json:"at,omitempty"`
	Error  string `json:"error,omitempty"`
}

// RecordingRow is a live recording in progress on the remote.
type RecordingRow struct {
	Site        string `json:"site"`
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	StartedAt   string `json:"startedAt,omitempty"`
	DurationSec int64  `json:"durationSec"`
	Bytes       int64  `json:"bytes"`
	Segments    int    `json:"segments"`
	Incidents   int    `json:"incidents"`
}

// Upload modes.
const (
	UploadRclone = "rclone"
	UploadNone   = "none"
)

// Upload error kinds.
const (
	UploadErrNotFound = "rclone_missing" // rclone not found
	UploadErrConfig   = "config"         // configuration or remote error
	UploadErrQuota    = "quota"          // quota or rate limit (e.g. 750 GiB a day)
	UploadErrNetwork  = "network"
	UploadErrGone     = "file_gone" // the local file disappeared before upload
	UploadErrOther    = "other"
)

// UploadStatus is the remote's built-in upload.
type UploadStatus struct {
	Mode          string `json:"mode"`
	Target        string `json:"target,omitempty"`
	DeleteAfter   bool   `json:"deleteAfter"`
	QueuedItems   int    `json:"queuedItems"`
	QueuedBytes   int64  `json:"queuedBytes"`
	Uploading     string `json:"uploading,omitempty"`
	UploadingSize int64  `json:"uploadingSize,omitempty"`
	TodayBytes    int64  `json:"todayBytes"`
	TodayFiles    int    `json:"todayFiles"`
	LastError     string `json:"lastError,omitempty"`
	LastErrorKind string `json:"lastErrorKind,omitempty"`
	LastErrorAt   string `json:"lastErrorAt,omitempty"`
	NextRetryAt   string `json:"nextRetryAt,omitempty"`
	GoneFiles     int    `json:"goneFiles,omitempty"`
}

// ErrorRow is one of the remote's recent errors (sanitized).
type ErrorRow struct {
	At       string `json:"at"`
	Category string `json:"category"`
	Message  string `json:"message"`
}

// CredentialPush is PUT credentials/{site}: the site Cookie and nothing
// else; a refresh token is never pushed (DEC-062 ③).
type CredentialPush struct {
	Cookie string `json:"cookie"`
}
