// Package companionapi defines the shared, stdlib-only wire contract between
// the desktop TRACE companion server (internal/companion) and mobile clients
// (internal/companionclient, cmd/trace-mobile). See docs/design/mobile.md §4–§6.
package companionapi

import (
	"net/url"
	"strings"
	"time"
)

// Protocol constants (docs/design/mobile.md §3, §4, §5).
const (
	ProtocolVersion = "1"

	HeaderProtocol   = "X-Trace-Companion-Protocol"
	HeaderVersion    = "X-Trace-Version"
	HeaderChannel    = "X-Trace-Channel"
	HeaderPairSecret = "X-Trace-Pair-Secret"

	DefaultStablePort = 47381
	DefaultDevPort    = 47382

	APIPrefix = "/api/v1/"

	// MaxBodyBytes bounds every JSON request body (docs/design/mobile.md §5.1).
	MaxBodyBytes = 64 << 10

	// MaxDevices is the maximum number of simultaneously paired mobile devices.
	MaxDevices = 5

	// Pairing timeouts and retry limits (docs/design/mobile.md §4).
	PairCodeTTL            = 5 * time.Minute
	PairConfirmTTL         = 2 * time.Minute
	PairPollMaxWait        = 60 * time.Second
	PairShortCodeMaxErrors = 5

	// WebSocket event streaming limits (docs/design/mobile.md §5.4).
	EventQueueLimit = 256
	WSPingInterval  = 5 * time.Second
	WSPongTimeout   = 15 * time.Second
)

// Channels permitted to participate in companion pairing (docs/design/mobile.md §10).
// The rehearsal channel "test" never runs the companion service.
const (
	ChannelStable = "stable"
	ChannelDev    = "dev"
)

// NormalizeChannel maps a channel identifier to its canonical companion value
// ("stable" or "dev"). "release" is accepted as an alias for "stable", and
// "development"/"source" for "dev". Any other channel (including "test") is rejected.
func NormalizeChannel(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "stable", "release":
		return ChannelStable, true
	case "dev", "development", "source":
		return ChannelDev, true
	default:
		return "", false
	}
}

// DefaultPortForChannel returns the default listening port for a channel.
func DefaultPortForChannel(channel string) int {
	if ch, ok := NormalizeChannel(channel); ok && ch == ChannelDev {
		return DefaultDevPort
	}
	return DefaultStablePort
}

// Endpoint paths under /api/v1/ (docs/design/mobile.md §4, §5.2, §5.3).
const (
	PathPair           = APIPrefix + "pair"
	PathPairPollPrefix = APIPrefix + "pair/"

	PathStatus            = APIPrefix + "status"
	PathFeed              = APIPrefix + "feed"
	PathFeedSubscriptions = APIPrefix + "feed/subscriptions"
	PathFeedSeen          = APIPrefix + "feed/seen"
	PathFeedPosition      = APIPrefix + "feed/position"

	PathLibrary            = APIPrefix + "library"
	PathLibraryItemsPrefix = APIPrefix + "library/items/"
	PathMediaPrefix        = APIPrefix + "media/"
	PathAvatarPrefix       = APIPrefix + "avatars/"

	PathSubscriptions         = APIPrefix + "subscriptions"
	PathSubscriptionsPrefix   = APIPrefix + "subscriptions/"
	PathSubscriptionsAccounts = APIPrefix + "subscriptions/accounts"

	PathTasksDownloads              = APIPrefix + "tasks/downloads"
	PathTasksDownloadsPrefix        = APIPrefix + "tasks/downloads/"
	PathTasksDownloadsClearFinished = APIPrefix + "tasks/downloads/clear-finished"

	PathTasksQueues       = APIPrefix + "tasks/queues"
	PathTasksQueuesPrefix = APIPrefix + "tasks/queues/"

	PathLiveStart            = APIPrefix + "live/start"
	PathLiveRecordings       = APIPrefix + "live/recordings"
	PathLiveRecordingsPrefix = APIPrefix + "live/recordings/"
	PathLiveIncidents        = APIPrefix + "live/incidents"

	PathChecks        = APIPrefix + "checks"
	PathChecksAccount = APIPrefix + "checks/account"
	PathChecksCancel  = APIPrefix + "checks/cancel"
	PathChecksPlan    = APIPrefix + "checks/plan"

	PathConflicts   = APIPrefix + "conflicts"
	PathTransfers   = APIPrefix + "transfers"
	PathDevicesSelf = APIPrefix + "devices/self"
	PathEvents      = APIPrefix + "events"
)

// Path builder helpers escape path segments consistently across server and client.

func PairPollPath(pairID string) string {
	return PathPairPollPrefix + url.PathEscape(pairID)
}

func LibraryItemPath(key string) string {
	return PathLibraryItemsPrefix + url.PathEscape(key)
}

func MediaPath(key, relPath string) string {
	clean := strings.TrimPrefix(strings.ReplaceAll(relPath, "\\", "/"), "/")
	parts := strings.Split(clean, "/")
	escaped := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			continue
		}
		escaped = append(escaped, url.PathEscape(p))
	}
	return PathMediaPrefix + url.PathEscape(key) + "/" + strings.Join(escaped, "/")
}

func AvatarPath(site, uid string) string {
	return PathAvatarPrefix + url.PathEscape(site) + "/" + url.PathEscape(uid)
}

func SubscriptionPath(id string) string {
	return PathSubscriptionsPrefix + url.PathEscape(id)
}

func DownloadTaskPath(id string) string {
	return PathTasksDownloadsPrefix + url.PathEscape(id)
}

func DownloadTaskActionPath(id, action string) string {
	return PathTasksDownloadsPrefix + url.PathEscape(id) + "/" + url.PathEscape(action)
}

func CacheQueueActionPath(id, action string) string {
	return PathTasksQueuesPrefix + url.PathEscape(id) + "/" + url.PathEscape(action)
}

func LiveRecordingStopPath(id string) string {
	return PathLiveRecordingsPrefix + url.PathEscape(id) + "/stop"
}

// Machine-readable error codes returned in ErrorBody.Error.Code.
const (
	CodeUnauthorized     = "unauthorized"
	CodeForbiddenSource  = "forbidden_source"
	CodeNotFound         = "not_found"
	CodeProtocolMismatch = "protocol_mismatch"
	CodeChannelMismatch  = "channel_mismatch"
	CodeDeviceLimit      = "device_limit"
	CodeConflict         = "conflict"
	CodeRateLimited      = "rate_limited"
	CodeTooLarge         = "too_large"
	CodeBadRequest       = "bad_request"
	CodeInvalidCode      = "invalid_code"
	CodePairRejected     = "pair_rejected"
	CodePairExpired      = "pair_expired"
	CodeAuthRequired     = "auth_required"
	CodeRiskControl      = "risk_control"
	CodeUnsupported      = "unsupported"
	CodeUnavailable      = "unavailable"
	CodeInternal         = "internal"
)

// ErrorDetail carries structured error fields for client classification (design §5.1, §5.3, §8).
type ErrorDetail struct {
	Code           string `json:"code"`
	Message        string `json:"message"`
	Site           string `json:"site,omitempty"`
	ServerVersion  string `json:"serverVersion,omitempty"`
	ServerProtocol string `json:"serverProtocol,omitempty"`
	ServerChannel  string `json:"serverChannel,omitempty"`
	ClientVersion  string `json:"clientVersion,omitempty"`
	ClientProtocol string `json:"clientProtocol,omitempty"`
	ClientChannel  string `json:"clientChannel,omitempty"`
	RetryAfterSecs int    `json:"retryAfterSecs,omitempty"`
}

// ErrorBody is the JSON envelope of every error response.
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}
