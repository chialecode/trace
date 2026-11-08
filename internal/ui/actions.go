package ui

import (
	"context"
	"strconv"

	"github.com/chialecode/trace/internal/site/bilibili"
)

func (a *App) showError(err error) {
	if err == nil {
		return
	}
	if bilibili.IsKind(err, bilibili.ErrKindAuthRequired) {
		if a.CurrentRoute != RouteLogin {
			a.TriggerAuthExpired("登录已失效，请重新登录")
		}
		return
	}
	a.BannerError = err.Error()
}

func (a *App) saveSettings() {
	updated, err := a.backend.UpdateSettings(a.DraftSettings)
	if err != nil {
		a.showError(err)
		return
	}
	a.DraftSettings = updated
}

func (a *App) loadImportPage(reset bool) {
	page := a.ImportPage + 1
	if reset {
		page = 1
	}
	tag, _ := strconv.ParseInt(a.ImportGroup, 10, 64)
	for _, group := range a.ImportGroups {
		if group.Name == a.ImportGroup {
			tag = group.TagID
			break
		}
	}
	group := a.ImportGroup
	requestGuarded(a, "followings:"+group, func() bool { return a.ImportGroup == group }, func(ctx context.Context) ([]bilibili.FollowingUser, error) {
		return a.backend.RelationFollowings(ctx, tag, page, 50)
	}, func(users []bilibili.FollowingUser) {
		a.ImportError = ""
		if reset {
			a.ImportFollowings = nil
			a.ImportSelectedMids = make(map[int64]bool)
		}
		seen := make(map[int64]bool)
		for _, user := range a.ImportFollowings {
			seen[user.Mid] = true
		}
		for _, user := range users {
			if !seen[user.Mid] {
				a.ImportFollowings = append(a.ImportFollowings, user)
				seen[user.Mid] = true
			}
		}
		a.ImportPage, a.ImportHasMore = page, len(users) == 50
	}, func(err error) { a.ImportError = err.Error(); a.showError(err) })
}

func (a *App) selectedSubscriptionAuthors() []int64 {
	for _, sub := range a.Subscriptions {
		if sub.ID == a.SelectedSubID {
			var ids []int64
			for _, acc := range sub.Accounts {
				id, _ := strconv.ParseInt(acc.UID, 10, 64)
				if id > 0 {
					ids = append(ids, id)
				}
			}
			return ids
		}
	}
	return nil
}

func (a *App) checkSelectedSubscription() {
	if a.pending["check-updates"] {
		return
	}
	a.CheckDone, a.CheckTotal = 0, 0
	backend, subID := a.backend, a.SelectedSubID
	request(a, "check-updates", func(ctx context.Context) (string, error) {
		sum, err := backend.CheckSubscription(ctx, subID)
		if err != nil {
			return "", err
		}
		return checkSummaryMessage(sum), nil
	}, func(message string) {
		a.BannerInfo = message
		a.RefreshData()
	})
}
