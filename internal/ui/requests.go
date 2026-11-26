package ui

import (
	"context"
	"strings"
	"time"
)

func (a *App) isPending(key string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.pending[key]
}

func (a *App) isClosed() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.closed
}

// isBackgroundPendingKey reports keys belonging to silent background refreshes
// or media decodes that should not trigger the top-bar "正在加载…" banner.
func isBackgroundPendingKey(key string) bool {
	return key == "runtime-logs" ||
		key == "visible-live" ||
		key == "remote-status" ||
		key == "file-dialog" ||
		strings.HasPrefix(key, "thumb:") ||
		strings.HasPrefix(key, "avatar:") ||
		strings.HasPrefix(key, "hist-thumb:") ||
		strings.HasPrefix(key, "public-image:") ||
		strings.HasPrefix(key, "history-thumbs-") ||
		strings.HasPrefix(key, "open-upgrade-") ||
		strings.HasPrefix(key, "conflict-diff:") ||
		strings.HasPrefix(key, "visible-live:") ||
		strings.HasPrefix(key, "user-live-")
}

func (a *App) hasForegroundPending() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for k := range a.pending {
		if !isBackgroundPendingKey(k) {
			return true
		}
	}
	return false
}

func request[T any](a *App, key string, work func(context.Context) (T, error), apply func(T)) {
	requestGuarded(a, key, nil, work, apply, nil)
}

func requestGuarded[T any](a *App, key string, current func() bool, work func(context.Context) (T, error), apply func(T), failure func(error)) {
	requestGuardedCleanup(a, key, current, work, apply, failure, nil)
}

// requestGuardedCleanup runs work off the UI thread (or inline when no
// dispatcher is bound) and invokes onDiscard(result) if work succeeded but
// the result is dropped because the app closed or current() returned false.
func requestGuardedCleanup[T any](
	a *App,
	key string,
	current func() bool,
	work func(context.Context) (T, error),
	apply func(T),
	failure func(error),
	onDiscard func(T),
) {
	a.mu.Lock()
	if a.closed || a.pending[key] {
		a.mu.Unlock()
		return
	}
	a.pending[key] = true
	dispatch := a.dispatch
	ctx := a.ctx
	a.mu.Unlock()

	finish := func(result T, err error) {
		a.mu.Lock()
		closed := a.closed
		delete(a.pending, key)
		a.mu.Unlock()
		if closed || (current != nil && !current()) {
			if err == nil && onDiscard != nil {
				onDiscard(result)
			}
			return
		}
		if err != nil {
			if failure != nil {
				failure(err)
				return
			}
			a.showError(err)
			return
		}
		if apply != nil {
			apply(result)
		}
	}
	if dispatch == nil {
		result, err := work(ctx)
		finish(result, err)
		return
	}
	go func() {
		result, err := work(ctx)
		a.mu.Lock()
		closed := a.closed
		if closed {
			delete(a.pending, key)
		}
		a.mu.Unlock()
		if closed {
			if err == nil && onDiscard != nil {
				onDiscard(result)
			}
			return
		}
		dispatch(func() { finish(result, err) })
	}()
}

func (a *App) checkAllSubscriptions() {
	if a.isPending("check-updates") {
		return
	}
	a.CheckDone, a.CheckTotal = 0, 0
	a.checkingAccount = ""
	backend := a.backend
	requestGuarded(a, "check-updates", nil, func(ctx context.Context) (string, error) {
		sum, err := backend.CheckSubscriptionsNow(ctx, 0)
		if err != nil {
			return "", err
		}
		return checkSummaryMessage(sum), nil
	}, func(message string) {
		a.checkingAccount = ""
		a.BannerInfo = message
		a.checkPlanAt = time.Time{}
		a.RefreshData()
	}, func(err error) {
		a.checkingAccount = ""
		a.checkPlanAt = time.Time{}
		a.showError(err)
	})
}

// checkAccountNow checks one account of a subscription for updates at once
// (Tasks › 检查计划).
func (a *App) checkAccountNow(subID, siteName, uid string) {
	if a.isPending("check-updates") {
		return
	}
	a.CheckDone, a.CheckTotal = 0, 0
	a.checkingAccount = siteName + ":" + uid
	backend := a.backend
	requestGuarded(a, "check-updates", nil, func(ctx context.Context) (string, error) {
		sum, err := backend.CheckAccount(ctx, subID, siteName, uid)
		if err != nil {
			return "", err
		}
		return checkSummaryMessage(sum), nil
	}, func(message string) {
		a.checkingAccount = ""
		a.BannerInfo = message
		a.checkPlanAt = time.Time{}
		a.RefreshData()
	}, func(err error) {
		a.checkingAccount = ""
		a.checkPlanAt = time.Time{}
		a.showError(err)
	})
}
