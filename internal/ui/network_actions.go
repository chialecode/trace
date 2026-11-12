package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/live"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/subscription"
)

type accountPreviewResult struct {
	account subscription.AccountRef
	owner   *subscription.Subscription
}

func (a *App) previewNewAccount() {
	input := a.AddSubInput
	requestGuarded(a, "preview-new-account", func() bool { return a.AddSubInput == input }, func(ctx context.Context) (accountPreviewResult, error) {
		account, owner, err := a.backend.PreviewAccount(ctx, input)
		return accountPreviewResult{account, owner}, err
	}, func(result accountPreviewResult) {
		a.AddSubPreview, a.AddSubPreviewOwner, a.AddSubPreviewInput = &result.account, result.owner, input
		if strings.TrimSpace(a.AddSubName) == "" {
			a.AddSubName = result.account.Name
		}
	}, func(err error) {
		a.showError(err)
	})
}

func (a *App) previewSubAccount(subID string) {
	input := a.SubAccountInput
	requestGuarded(a, "preview-sub-account", func() bool { return a.SubAccountInput == input && a.SelectedSubID == subID }, func(ctx context.Context) (accountPreviewResult, error) {
		account, owner, err := a.backend.PreviewAccount(ctx, input)
		return accountPreviewResult{account, owner}, err
	}, func(result accountPreviewResult) {
		a.AccountPreview, a.AccountPreviewSubID = &result.account, subID
		a.AccountPreviewOwner = ""
		if result.owner != nil {
			a.AccountPreviewOwner = result.owner.Name
		}
	}, func(err error) {
		a.showError(err)
	})
}

func (a *App) createPreviewedSubscription() {
	input := strings.TrimSpace(a.AddSubInput)
	if input != "" && (a.AddSubPreview == nil || a.AddSubPreviewInput != a.AddSubInput) {
		a.previewNewAccount()
		a.BannerInfo = "请确认预览中的账号，再创建订阅"
		return
	}
	var accounts []subscription.AccountRef
	if input != "" && a.AddSubPreviewOwner == nil {
		accounts = append(accounts, *a.AddSubPreview)
	}
	sub, err := a.backend.CreateSubscription(strings.TrimSpace(a.AddSubName), "", accounts, subscription.SubRules{ScheduledCheck: true})
	if err != nil {
		a.showError(err)
		return
	}
	if input != "" && a.AddSubPreviewOwner != nil {
		sub, err = a.backend.MoveSubscriptionAccount(a.AddSubPreview.Site, a.AddSubPreview.UID, sub.ID)
		if err != nil {
			a.showError(err)
			return
		}
	}
	a.syncSelectedSubRules(sub)
	a.AddSubInput, a.AddSubName, a.AddSubPreviewInput = "", "", ""
	a.AddSubPreview, a.AddSubPreviewOwner, a.AddSubDialogOpen = nil, nil, false
	a.RefreshData()
}

func (a *App) refreshVisibleLive() {
	keys := a.selectedSubscriptionAccountKeys()
	a.visibleLiveStopped = false
	backend := a.backend
	requestGuarded(a, "visible-live", func() bool {
		return a.CurrentRoute == RouteSubscriptions && backend.HasAnyLogin()
	}, func(ctx context.Context) ([]service.LiveAccountState, error) {
		if len(keys) == 0 {
			return nil, nil
		}
		return backend.LiveStatuses(ctx, keys)
	}, func(states []service.LiveAccountState) {
		a.LiveRooms = backend.ListLiveRooms()
		a.mergeLiveAccountStates(states)
	}, a.handleVisibleLiveError)
}

func (a *App) startLiveRecording(roomID int64) {
	request(a, fmt.Sprintf("live-start-%d", roomID), func(ctx context.Context) (live.RoomState, error) { return a.backend.StartLiveRecording(ctx, roomID) }, func(state live.RoomState) { a.mergeLiveRooms([]live.RoomState{state}) })
}

func (a *App) stopLiveRecording(roomID int64) {
	request(a, fmt.Sprintf("live-stop-%d", roomID), func(context.Context) (live.RoomState, error) { return a.backend.StopLiveRecording(roomID) }, func(state live.RoomState) { a.mergeLiveRooms([]live.RoomState{state}) })
}

func (a *App) startAccountLiveRecording(siteID, uid, roomOrWebRID string) {
	normSite := normalizeSiteIDUI(siteID)
	accountKey := normSite + ":" + strings.TrimSpace(uid)
	reqKey := "live-start-acct-" + accountKey + "-" + roomOrWebRID
	request(a, reqKey, func(ctx context.Context) (service.LiveAccountState, error) {
		if strings.TrimSpace(roomOrWebRID) != "" {
			return a.backend.StartLiveRecordingByRoom(ctx, normSite, roomOrWebRID)
		}
		return a.backend.StartLiveRecordingByAccount(ctx, accountKey)
	}, func(state service.LiveAccountState) {
		a.LiveRooms = a.backend.ListLiveRooms()
		a.mergeLiveAccountStates([]service.LiveAccountState{state})
	})
}

func (a *App) stopAccountLiveRecording(recKey string) {
	request(a, "live-stop-key-"+recKey, func(context.Context) (service.LiveAccountState, error) {
		return a.backend.StopLiveRecordingByKey(recKey)
	}, func(state service.LiveAccountState) {
		a.LiveRooms = a.backend.ListLiveRooms()
		a.mergeLiveAccountStates([]service.LiveAccountState{state})
	})
}

func (a *App) refreshAccount() {
	request(a, "renew-account", a.backend.RefreshCookie, func(credential.Status) { a.BannerInfo = "已完成登录续期检查" })
}

func siteFullLabel(siteID string) string {
	if strings.EqualFold(strings.TrimSpace(siteID), string(site.Douyin)) {
		return "抖音"
	}
	return "哔哩哔哩"
}

func (a *App) logoutSiteAccount(siteID string) {
	targetSite := strings.ToLower(strings.TrimSpace(siteID))
	if targetSite == "" {
		targetSite = string(site.Bilibili)
	}
	request(a, "logout-account-"+targetSite, func(context.Context) (credential.Status, error) {
		return a.backend.LogoutSite(targetSite)
	}, func(credential.Status) {
		a.ConfirmLogoutOpen = false
		a.ConfirmLogoutSite = ""
		if !a.backend.HasAnyLogin() {
			a.LoginSite = targetSite
			a.Navigate(RouteLogin)
			return
		}
		a.RefreshData()
		a.BannerInfo = "已退出" + siteFullLabel(targetSite) + "登录"
	})
}

func (a *App) logoutAccount() {
	a.logoutSiteAccount(firstNonEmptyStrUI(a.ConfirmLogoutSite, string(site.Bilibili)))
}

func checkSummaryMessage(sum subscription.CheckSummary) string {
	if sum.Canceled {
		return fmt.Sprintf("已取消检查（%d/%d 个账号）", sum.CheckedCreators, sum.TotalCreators)
	}
	if sum.DeferredByQuota {
		if sum.CheckedCreators > 0 {
			return fmt.Sprintf("检查在 %d/%d 处停止：本小时自动请求额度已用尽", sum.CheckedCreators, sum.TotalCreators)
		}
		return "本小时自动请求额度已用尽，检查未开始"
	}
	if sum.DeferredByBackoff {
		if sum.CheckedCreators > 0 {
			return fmt.Sprintf("检查在 %d/%d 处停止：自动请求进入退避期", sum.CheckedCreators, sum.TotalCreators)
		}
		return "自动请求仍在退避期，检查未开始"
	}
	if len(sum.Errors) > 0 {
		if sum.StoppedByRiskControl || sum.CheckedCreators < sum.TotalCreators {
			return fmt.Sprintf("检查在 %d/%d 处停止：%s", sum.CheckedCreators, sum.TotalCreators, sum.Errors[0])
		}
		return fmt.Sprintf("已检查 %d 个账号，失败 %d 个：%s", sum.CheckedCreators, sum.FailedCreators, sum.Errors[0])
	}
	return fmt.Sprintf("检查 %d 个账号，新增 %d 条动态，失败 %d 个", sum.CheckedCreators, sum.NewDynamics, sum.FailedCreators)
}

func (a *App) checkQualityUpgrades() {
	if a.pending["upgrade-check"] {
		return
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.upgradeCancel, a.UpgradeDone, a.UpgradeTotal = cancel, 0, 0
	backend := a.backend
	progress := func(done, total int) {
		apply := func() {
			if !a.closed && a.pending["upgrade-check"] {
				a.UpgradeDone, a.UpgradeTotal = done, total
			}
		}
		if a.dispatch != nil {
			a.dispatch(apply)
		} else {
			apply()
		}
	}
	requestGuarded(a, "upgrade-check", nil, func(context.Context) ([]library.ItemMetadata, error) {
		defer cancel()
		candidates, err := backend.UpgradeCandidates(50)
		if err != nil {
			return nil, err
		}
		progress(0, len(candidates))
		var changed []library.ItemMetadata
		for i, item := range candidates {
			if err := ctx.Err(); err != nil {
				return changed, err
			}
			items, err := backend.CheckUpgradeKeys(ctx, []string{item.ItemKey}, true)
			if err != nil {
				return changed, err
			}
			changed = append(changed, items...)
			progress(i+1, len(candidates))
		}
		return changed, nil
	}, func(items []library.ItemMetadata) {
		a.upgradeCancel, a.UpgradeCandidates = nil, items
		upgrades := 0
		for _, item := range items {
			if item.UpgradeAvailable {
				upgrades++
			}
		}
		a.BannerInfo = fmt.Sprintf("已完成画质检查：发现 %d 个可升级视频", upgrades)
	}, func(err error) {
		a.upgradeCancel = nil
		if errors.Is(err, context.Canceled) {
			a.BannerInfo = fmt.Sprintf("已取消画质检查（%d/%d）", a.UpgradeDone, a.UpgradeTotal)
		} else {
			a.showError(err)
		}
	})
}

func (a *App) openImportFollowingsModal() {
	if !a.backend.StatusOf(string(site.Bilibili)).LoggedIn {
		a.OpenLoginForSite(string(site.Bilibili))
		a.LoginError = "请先登录哔哩哔哩账号后再从 B 站关注列表导入"
		return
	}
	a.ImportSubModalOpen, a.ImportGroup = true, "全部关注"
	request(a, "following-groups", a.backend.RelationTags, func(groups []bilibili.RelationTag) { a.ImportGroups = groups; a.loadImportPage(true) })
}
