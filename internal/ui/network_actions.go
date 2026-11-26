package ui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
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
	sub, err := a.backend.CreateSubscription(strings.TrimSpace(a.AddSubName), "", accounts, a.AddSubPreset.SelectedRules())
	if err != nil {
		a.showError(err)
		return
	}
	if input != "" && a.AddSubPreviewOwner != nil {
		sub, err = a.backend.MoveSubscriptionAccount(a.AddSubPreview.Site, a.AddSubPreview.UID, sub.ID)
		if err != nil {
			// Roll back the empty subscription so a retry does not leave orphans.
			_ = a.backend.DeleteSubscription(sub.ID)
			a.showError(err)
			return
		}
	}
	a.syncSelectedSubRules(sub)
	a.AddSubInput, a.AddSubName, a.AddSubPreviewInput = "", "", ""
	a.AddSubPreview, a.AddSubPreviewOwner, a.AddSubDialogOpen = nil, nil, false
	a.AddSubPreset = TrackingPresetPickerState{}
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
		a.LiveRooms = bilibiliLiveRooms(backend.ListLiveRooms())
		a.mergeLiveAccountStates(states)
	}, a.handleVisibleLiveError)
}

func (a *App) startLiveRecording(roomID int64) {
	request(a, fmt.Sprintf("live-start-%d", roomID), func(ctx context.Context) (live.RoomState, error) { return a.backend.StartLiveRecording(ctx, roomID) }, func(state live.RoomState) { a.mergeLiveRooms([]live.RoomState{state}) })
}

func (a *App) stopLiveRecording(roomID int64) {
	a.closeLivePreviewForRoom(roomID)
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
		a.LiveRooms = bilibiliLiveRooms(a.backend.ListLiveRooms())
		a.mergeLiveAccountStates([]service.LiveAccountState{state})
	})
}

func (a *App) stopAccountLiveRecording(recKey string) {
	a.closeLivePreviewForKey(recKey)
	request(a, "live-stop-key-"+recKey, func(context.Context) (service.LiveAccountState, error) {
		return a.backend.StopLiveRecordingByKey(recKey)
	}, func(state service.LiveAccountState) {
		a.LiveRooms = bilibiliLiveRooms(a.backend.ListLiveRooms())
		a.mergeLiveAccountStates([]service.LiveAccountState{state})
	})
}

func (a *App) refreshAccount() {
	request(a, "renew-account", a.backend.RefreshCookie, func(credential.Status) { a.BannerInfo = "已完成登录续期检查" })
}

func siteFullLabel(siteID string) string {
	switch strings.ToLower(strings.TrimSpace(siteID)) {
	case string(site.Douyin):
		return "抖音"
	case string(site.Weibo):
		return "微博"
	case string(site.X):
		return "X"
	default:
		return "哔哩哔哩"
	}
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
	if sum.BaselineInitialized > 0 {
		otherChecked := sum.CheckedCreators - sum.BaselineInitialized - sum.FailedCreators
		if otherChecked < 0 {
			otherChecked = 0
		}
		if otherChecked == 0 && sum.NewDynamics == 0 && sum.NewVideos == 0 && sum.FailedCreators == 0 {
			return fmt.Sprintf("已为 %d 个账号记录检查起点，之后发布的新内容会被追踪", sum.BaselineInitialized)
		}
		msg := fmt.Sprintf("已为 %d 个账号记录检查起点", sum.BaselineInitialized)
		if otherChecked > 0 {
			msg += fmt.Sprintf("，另检查 %d 个账号，新增 %d 条动态", otherChecked, sum.NewDynamics)
		} else if sum.NewDynamics > 0 {
			msg += fmt.Sprintf("，新增 %d 条动态", sum.NewDynamics)
		}
		if sum.AutoCacheTasks > 0 {
			msg += fmt.Sprintf("，已自动入队 %d 条", sum.AutoCacheTasks)
		}
		if sum.FailedCreators > 0 {
			msg += fmt.Sprintf("，失败 %d 个", sum.FailedCreators)
		}
		return msg
	}
	if sum.AutoCacheTasks > 0 && sum.FailedCreators == 0 {
		return fmt.Sprintf("检查 %d 个账号，新增 %d 条动态，已自动入队 %d 条", sum.CheckedCreators, sum.NewDynamics, sum.AutoCacheTasks)
	}
	return fmt.Sprintf("检查 %d 个账号，新增 %d 条动态，失败 %d 个", sum.CheckedCreators, sum.NewDynamics, sum.FailedCreators)
}

func (a *App) checkQualityUpgrades() {
	a.mu.Lock()
	if a.closed || a.pending["upgrade-check"] {
		a.mu.Unlock()
		return
	}
	dispatch := a.dispatch
	baseCtx := a.ctx
	a.mu.Unlock()

	ctx, cancel := context.WithCancel(baseCtx)
	a.upgradeCancel, a.UpgradeDone, a.UpgradeTotal = cancel, 0, 0
	backend := a.backend
	progress := func(done, total int) {
		apply := func() {
			if !a.isClosed() && a.isPending("upgrade-check") {
				a.UpgradeDone, a.UpgradeTotal = done, total
			}
		}
		if dispatch != nil {
			dispatch(apply)
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
	biliIn := a.backend.StatusOf(string(site.Bilibili)).LoggedIn
	wbIn := a.backend.StatusOf(string(site.Weibo)).LoggedIn
	xIn := a.backend.StatusOf(string(site.X)).LoggedIn
	if !biliIn && !wbIn && !xIn {
		a.OpenLoginForSite(string(site.Bilibili))
		a.LoginError = "请先登录哔哩哔哩、微博或 X 账号后再从关注列表导入"
		return
	}
	targetSite := string(site.Bilibili)
	if !biliIn {
		if wbIn {
			targetSite = string(site.Weibo)
		} else if xIn {
			targetSite = string(site.X)
		}
	}
	a.ImportPreset.ResetDefault()
	a.ImportSubModalOpen = true
	a.selectImportSite(targetSite)
}

func (a *App) selectImportSite(siteID string) {
	norm := normalizeImportSite(siteID)
	a.importFollowingsSeq++
	a.ImportSite = norm
	a.ImportGroup = "全部关注"
	a.ImportSearch = ""
	a.ImportError = ""
	a.ImportCreators = nil
	a.ImportFollowings = nil
	a.ImportGroupsDTO = nil
	a.ImportGroups = nil
	a.ImportGroupsEnabled = false
	a.ImportPage = 0
	a.ImportCursor = ""
	a.ImportHasMore = false
	a.ImportSelectedKeys = make(map[string]bool)
	a.ImportSelectedMids = make(map[int64]bool)

	if !a.backend.StatusOf(norm).LoggedIn {
		if norm == string(site.X) {
			a.ImportError = "请先登录 X"
		} else {
			a.ImportError = "请先登录" + siteFullLabel(norm)
		}
		return
	}
	if norm == string(site.X) {
		a.loadImportPage(true)
		return
	}
	seq := a.importFollowingsSeq
	requestGuarded(a, fmt.Sprintf("following-groups:%s:%d", norm, seq), func() bool {
		return a.ImportSubModalOpen && a.ImportSite == norm && a.importFollowingsSeq == seq
	}, func(ctx context.Context) ([]service.FollowingGroupDTO, error) {
		return a.backend.FollowingGroups(ctx, norm)
	}, func(groups []service.FollowingGroupDTO) {
		a.ImportGroupsDTO = groups
		a.ImportGroupsEnabled = len(groups) > 0
		if norm == string(site.Bilibili) {
			tags := make([]bilibili.RelationTag, 0, len(groups))
			for _, g := range groups {
				tid, _ := strconv.ParseInt(g.ID, 10, 64)
				tags = append(tags, bilibili.RelationTag{TagID: tid, Name: g.Name, Count: g.Count})
			}
			a.ImportGroups = tags
		}
		a.loadImportPage(true)
	}, func(err error) {
		a.ImportError = userErrorText(err)
		a.showError(err)
	})
}
