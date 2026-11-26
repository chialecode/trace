package companionapi

// WebSocket event types pushed on GET /api/v1/events (docs/design/mobile.md §5.4).
const (
	EventTaskChanged       = "task:changed"
	EventCacheChanged      = "cache:changed"
	EventCacheQueueUpdated = "cache_queue.updated"
	EventFeedChanged       = "feed:changed"
	EventSubsChanged       = "subs:changed"
	EventSubsCheck         = "subs:check"
	EventLiveChanged       = "live:changed"
	EventAuthChanged       = "auth:changed"
	EventConflictsChanged  = "conflicts:changed"
	EventTransfersChanged  = "transfers:changed"
	EventBye               = "bye"
	EventResync            = "resync"
)

// Bye reasons sent in EventFrame.Reason when Type == EventBye (docs/design/mobile.md §3, §4.6, §8).
const (
	ByeReasonExit     = "exit"
	ByeReasonUpdate   = "update"
	ByeReasonDisabled = "disabled"
	ByeReasonRevoked  = "revoked"
)

// EventFrame is a single JSON message sent over the GET /api/v1/events WebSocket.
// It never carries cookies, tokens, signed URLs, or local absolute paths.
type EventFrame struct {
	Type      string `json:"type"`
	Site      string `json:"site,omitempty"`
	State     string `json:"state,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Message   string `json:"message,omitempty"`
	Key       string `json:"key,omitempty"`
	NeedsSync bool   `json:"needsSync,omitempty"`
	Timestamp string `json:"timestamp"`
}
