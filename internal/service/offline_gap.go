package service

import (
	"context"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/chialecode/trace/internal/uistate"
)

const (
	// DefaultHeartbeatInterval is how often the desktop backend records a
	// lightweight local LastActiveAt timestamp in ui-state.json so an unexpected
	// shutdown or power loss still yields an accurate offline gap on next launch.
	DefaultHeartbeatInterval = 15 * time.Minute

	// OfflineLiveGapThreshold is the minimum gap between the previous
	// LastActiveAt (clean exit or local heartbeat) and the current startup before
	// displaying the gentle zero-request offline live notice (FR-RUN-002).
	OfflineLiveGapThreshold = 30 * time.Minute

	// OfflineLiveGapMessage is the gentle local notice shown when TRACE was not
	// running for a meaningful interval while live auto-recording is configured.
	OfflineLiveGapMessage = "TRACE 未运行期间可能有主播开播，可前往订阅或直播查看"
)

// OfflineLiveGapNotice describes a purely local startup notice indicating TRACE
// was offline while at least one subscription had live auto-recording enabled.
type OfflineLiveGapNotice struct {
	Message      string
	LastActiveAt time.Time
	Gap          time.Duration
}

// HasAutoRecordLiveSubscriptions reports locally (zero network requests)
// whether any subscription enables auto-recording on a live-capable site
// (Bilibili or Douyin; Weibo and X do not support live recording).
func HasAutoRecordLiveSubscriptions(subs []subscription.Subscription) bool {
	for _, sub := range subs {
		if !sub.Rules.AutoRecord && !sub.LiveAutoRecord && !subscription.HasTag(sub.Tags, subscription.TagAutoRecord) {
			continue
		}
		if len(sub.Accounts) == 0 {
			if sub.Mid > 0 {
				return true
			}
			continue
		}
		for _, acc := range sub.Accounts {
			s := strings.ToLower(strings.TrimSpace(acc.Site))
			if s == "" || s == string(site.Bilibili) || s == string(site.Douyin) {
				return true
			}
		}
	}
	return false
}

// HasAutoRecordLiveSubscriptions reads local subscriptions from disk/DB with
// zero site requests and reports whether any live-capable subscription has
// auto-recording enabled.
func (b *Backend) HasAutoRecordLiveSubscriptions() bool {
	if b == nil || b.subs == nil {
		return false
	}
	subs, err := b.subs.List()
	if err != nil {
		return false
	}
	return HasAutoRecordLiveSubscriptions(subs)
}

// EvaluateOfflineLiveGap decides purely from local timestamps and local
// subscription flags whether to display the gentle offline live notice on
// startup. It makes zero network requests and suppresses notice on first run,
// short restarts, valid update/dev-install restarts, backward clock jumps,
// unauthenticated sessions, or when no live-capable subscription enables
// auto-recording.
func EvaluateOfflineLiveGap(lastActiveAt string, now time.Time, hasAutoRecord, hasAnyLogin, validRestart bool, threshold time.Duration) (OfflineLiveGapNotice, bool) {
	if !hasAutoRecord || !hasAnyLogin || validRestart {
		return OfflineLiveGapNotice{}, false
	}
	raw := strings.TrimSpace(lastActiveAt)
	if raw == "" {
		return OfflineLiveGapNotice{}, false
	}
	prev, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return OfflineLiveGapNotice{}, false
	}
	if threshold <= 0 {
		threshold = OfflineLiveGapThreshold
	}
	gap := now.Sub(prev)
	if gap < threshold {
		return OfflineLiveGapNotice{}, false
	}
	return OfflineLiveGapNotice{
		Message:      OfflineLiveGapMessage,
		LastActiveAt: prev.UTC(),
		Gap:          gap,
	}, true
}

// RecordLastActiveAt records a lightweight local heartbeat / clean-exit
// timestamp in ui-state.json without making any network requests.
func (b *Backend) RecordLastActiveAt(now time.Time) error {
	if b == nil || b.uiState == nil || b.profile == ProfileRemote {
		return nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	stamp := now.UTC().Format(time.RFC3339)
	_, err := b.uiState.Mutate(func(st *uistate.State) {
		st.LastActiveAt = stamp
	})
	return err
}

func (b *Backend) runRuntimeHeartbeat(ctx context.Context) {
	defer b.bgWg.Done()
	interval := b.heartbeatInterval
	if interval <= 0 {
		interval = DefaultHeartbeatInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case t := <-ticker.C:
			if err := b.RecordLastActiveAt(t.UTC()); err != nil {
				b.logError("heartbeat.record", err)
			}
		}
	}
}
