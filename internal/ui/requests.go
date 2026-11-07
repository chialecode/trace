package ui

import "context"

func request[T any](a *App, key string, work func(context.Context) (T, error), apply func(T)) {
	requestGuarded(a, key, nil, work, apply, nil)
}

func requestGuarded[T any](a *App, key string, current func() bool, work func(context.Context) (T, error), apply func(T), failure func(error)) {
	a.mu.Lock()
	if a.closed || a.pending[key] {
		a.mu.Unlock()
		return
	}
	a.pending[key] = true
	dispatch := a.dispatch
	a.mu.Unlock()
	finish := func(result T, err error) {
		a.mu.Lock()
		closed := a.closed
		delete(a.pending, key)
		a.mu.Unlock()
		if closed || current != nil && !current() {
			return
		}
		if err != nil {
			if failure != nil {
				failure(err)
				return
			}
			switch key {
			case "parse":
				a.LastParseError = err.Error()
			case "user-dynamics":
				a.UserDynamicsError = err.Error()
			case "user-videos":
				a.UserVideosError = err.Error()
			}
			a.showError(err)
			return
		}
		apply(result)
	}
	if dispatch == nil {
		result, err := work(a.ctx)
		finish(result, err)
		return
	}
	go func() { result, err := work(a.ctx); dispatch(func() { finish(result, err) }) }()
}
func (a *App) checkAllSubscriptions() {
	if a.pending["check-updates"] {
		return
	}
	a.CheckDone, a.CheckTotal = 0, 0
	backend := a.backend
	request(a, "check-updates", func(ctx context.Context) (string, error) {
		sum, err := backend.CheckSubscriptionsNow(ctx, 0)
		if err != nil {
			return "", err
		}
		return checkSummaryMessage(sum), nil
	}, func(message string) { a.BannerInfo = message; a.RefreshData() })
}
