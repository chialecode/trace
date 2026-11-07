package ui

import (
	"context"
	"strconv"
	"time"

	"github.com/chialecode/trace/internal/live"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

const visibleLiveRefreshInterval = 60 * time.Second

func subscriptionAuthors(sub subscription.Subscription) []int64 {
	var ids []int64
	for _, account := range sub.Accounts {
		if account.Site == "bilibili" {
			if id, err := strconv.ParseInt(account.UID, 10, 64); err == nil && id > 0 {
				ids = append(ids, id)
			}
		}
	}
	if sub.Mid > 0 {
		ids = append(ids, sub.Mid)
	}
	return ids
}

func (a *App) refreshGridLive(c *ui.Context, subs []subscription.Subscription, cols int) {
	first, last := a.SubListState.Visible()
	if last < first {
		return // MyGo rebuilds after the first layout establishes the visible range.
	}
	var ids []int64
	for i := max(0, first*cols); i < min(len(subs), (last+1)*cols); i++ {
		ids = append(ids, subscriptionAuthors(subs[i])...)
	}
	a.refreshLiveAccounts(c, ids)
}

func (a *App) refreshLiveAccounts(c *ui.Context, ids []int64) {
	if a.visibleLiveStopped || !a.backend.AccountStatus().LoggedIn {
		return
	}
	if a.visibleLiveNextRead == nil {
		a.visibleLiveNextRead = make(map[int64]time.Time)
	}
	now := c.Now()
	var due []int64
	seen := make(map[int64]bool)
	for _, id := range ids {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		next := a.visibleLiveNextRead[id]
		if next.After(now) {
			c.After(next.Sub(now))
		} else if len(due) < 50 {
			due = append(due, id)
		}
	}
	if len(due) == 0 || a.pending["visible-live"] {
		return
	}
	// Failed attempts also wait for the next interval; a repaint never retries.
	for _, id := range due {
		a.visibleLiveNextRead[id] = now.Add(visibleLiveRefreshInterval)
	}
	c.After(visibleLiveRefreshInterval)
	backend := a.backend
	requestGuarded(a, "visible-live", func() bool {
		return a.CurrentRoute == RouteSubscriptions && backend.AccountStatus().LoggedIn
	}, func(ctx context.Context) ([]live.RoomState, error) {
		return backend.LiveStatus(ctx, due)
	}, a.mergeLiveRooms, a.handleVisibleLiveError)
	c.Invalidate()
}

func (a *App) handleVisibleLiveError(err error) {
	switch bilibili.KindOf(err) {
	case bilibili.ErrKindRiskControl, bilibili.ErrKindRateLimited, bilibili.ErrKindAuthRequired:
		a.visibleLiveStopped = true
	}
	a.showError(err)
}
