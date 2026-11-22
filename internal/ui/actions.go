package ui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
)

func userErrorText(err error) string {
	if err == nil {
		return ""
	}
	var se *site.Error
	if errors.As(err, &se) && strings.TrimSpace(se.Message) != "" {
		return strings.TrimSpace(se.Message)
	}
	return err.Error()
}

func (a *App) showError(err error) {
	if err == nil {
		return
	}
	a.backend.LogUserError(err)
	if site.IsKind(err, site.ErrKindAuthRequired) {
		errSite := string(site.SiteOf(err))
		if !a.backend.HasAnyLogin() {
			reason := userErrorText(err)
			if reason == "" {
				reason = "登录已失效，请重新登录"
			}
			if a.CurrentRoute != RouteLogin {
				a.TriggerAuthExpired(reason)
			} else {
				a.LoginError = reason
			}
			return
		}
		if errSite != "" {
			st := a.backend.StatusOf(errSite)
			if st.State == "expired" {
				a.handleSiteAccountLoss(errSite, "expired")
				return
			}
			a.BannerLoginSite = errSite
		} else {
			a.BannerLoginSite = ""
		}
		a.BannerError = userErrorText(err)
		return
	}
	a.BannerLoginSite = ""
	a.BannerError = userErrorText(err)
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
	siteID := a.ImportSite
	if siteID != string(site.Weibo) {
		siteID = string(site.Bilibili)
	}
	if !a.backend.StatusOf(siteID).LoggedIn {
		a.ImportError = "请先登录" + siteFullLabel(siteID)
		return
	}
	page := a.ImportPage + 1
	if reset {
		page = 1
		a.importFollowingsSeq++
	}
	seq := a.importFollowingsSeq
	groupID := ""
	if a.ImportGroup != "" && a.ImportGroup != "全部关注" {
		groupID = a.ImportGroup
		for _, g := range a.ImportGroupsDTO {
			if g.Name == a.ImportGroup {
				groupID = g.ID
				break
			}
		}
	}
	group := a.ImportGroup
	reqKey := fmt.Sprintf("followings:%s:%d:%s:%d", siteID, seq, group, page)
	requestGuarded(a, reqKey, func() bool {
		return a.ImportSubModalOpen && a.ImportSite == siteID && a.ImportGroup == group && a.importFollowingsSeq == seq
	}, func(ctx context.Context) (service.FollowingCreatorsPageDTO, error) {
		return a.backend.FollowingCreatorsPage(ctx, siteID, groupID, page)
	}, func(pg service.FollowingCreatorsPageDTO) {
		a.ImportError = ""
		if reset {
			a.ImportCreators = nil
			a.ImportFollowings = nil
			a.ImportSelectedKeys = make(map[string]bool)
			a.ImportSelectedMids = make(map[int64]bool)
		}
		if a.ImportSelectedKeys == nil {
			a.ImportSelectedKeys = make(map[string]bool)
		}
		if a.ImportSelectedMids == nil {
			a.ImportSelectedMids = make(map[int64]bool)
		}
		seen := make(map[string]bool, len(a.ImportCreators))
		for _, c := range a.ImportCreators {
			seen[normalizeSiteIDUI(c.Site)+":"+c.UID] = true
		}
		for _, item := range pg.Items {
			norm := normalizeSiteIDUI(firstNonEmptyStrUI(item.Site, siteID))
			item.Site = norm
			k := norm + ":" + item.UID
			if item.UID == "" || seen[k] {
				continue
			}
			a.ImportCreators = append(a.ImportCreators, item)
			seen[k] = true
			if norm == string(site.Bilibili) {
				mid, _ := strconv.ParseInt(item.UID, 10, 64)
				a.ImportFollowings = append(a.ImportFollowings, bilibili.FollowingUser{
					Mid:   mid,
					Uname: item.Name,
					Face:  item.Face,
					Sign:  item.Sign,
				})
			}
		}
		if pg.Page > 0 {
			a.ImportPage = pg.Page
		} else {
			a.ImportPage = page
		}
		a.ImportHasMore = pg.HasMore
		a.ImportGroupsEnabled = pg.GroupsEnabled && len(a.ImportGroupsDTO) > 0
	}, func(err error) { a.ImportError = userErrorText(err); a.showError(err) })
}

func (a *App) selectedSubscriptionAuthors() []int64 {
	for _, sub := range a.Subscriptions {
		if sub.ID == a.SelectedSubID {
			var ids []int64
			for _, acc := range sub.Accounts {
				if acc.Site == "" || acc.Site == string(site.Bilibili) {
					id, _ := strconv.ParseInt(acc.UID, 10, 64)
					if id > 0 {
						ids = append(ids, id)
					}
				}
			}
			return ids
		}
	}
	return nil
}

func (a *App) selectedSubscriptionAccountKeys() []string {
	for _, sub := range a.Subscriptions {
		if sub.ID == a.SelectedSubID {
			var keys []string
			for _, acc := range sub.Accounts {
				siteID, ok := site.ParseID(firstNonEmptyStrUI(acc.Site, string(site.Bilibili)))
				if !ok || siteID == site.Weibo || strings.TrimSpace(acc.UID) == "" {
					continue
				}
				keys = append(keys, site.AccountKey(siteID, acc.UID))
			}
			return keys
		}
	}
	return nil
}

func (a *App) checkSelectedSubscription() {
	if a.isPending("check-updates") {
		return
	}
	a.CheckDone, a.CheckTotal = 0, 0
	a.checkingAccount = ""
	backend, subID := a.backend, a.SelectedSubID
	requestGuarded(a, "check-updates", nil, func(ctx context.Context) (string, error) {
		sum, err := backend.CheckSubscription(ctx, subID)
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
