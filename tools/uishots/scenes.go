package main

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/player"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/chialecode/trace/internal/ui"
	"github.com/chialecode/trace/internal/uistate"
	"github.com/chialecode/trace/internal/update"
	mygoui "github.com/egoist/mygo/ui"
)

type sceneSpec struct {
	Name                 string
	Setup                func()
	RequiredLabels       []string
	PanelRequiredLabels  []string
	NarrowRequiredLabels []string
	ForbiddenLabels      []string
	RequiredTexts        []string
	ForbiddenTexts       []string
	// TitleBar simulates the hidden title bar's window buttons (DEC-046 ④).
	TitleBar mygoui.TitleBar
	// Frames is how many more frames the scene needs to settle, as a
	// restored reading position does (DEC-053 ①).
	Frames int
}

func buildAllScenes(env *fixtureEnv, app *ui.App) []sceneSpec {
	scenes := buildCoreScenes(env, app)
	// 我的 and remote/transfer scenes go before the green-accent preview, which must stay last.
	last := scenes[len(scenes)-1]
	scenes = scenes[:len(scenes)-1]
	lastSetup := last.Setup
	last.Setup = func() {
		env.backend.remote = remoteFixture{}
		app.Transfer, app.SubFile, app.Remote = ui.TransferUIState{}, ui.SubFileUIState{}, ui.RemoteUIState{}
		lastSetup()
	}
	scenes = append(scenes, mineShotScenes(app, env.backend)...)
	scenes = append(scenes, remoteShotScenes(app, env.backend)...)
	scenes = append(scenes, last)
	return scenes
}

func buildCoreScenes(env *fixtureEnv, app *ui.App) []sceneSpec {
	b := env.rawBackend
	fixtureBackend := env.backend
	douyinSecUID := env.douyinSecUID
	weiboUID := env.weiboUID
	xUID := env.xUID
	xHandle := env.xHandle
	xOnlyUID := env.xOnlyUID
	xOnlyHandle := env.xOnlyHandle
	primarySub := env.primarySub
	auroraSub := env.auroraSub
	xOnlySub := env.xOnlySub
	savedItem := env.savedItem
	savedLiveItem := env.savedLiveItem
	savedPartialItem := env.savedPartialItem
	savedDynamicItem := env.savedDynamicItem
	savedDouyinVideo := env.savedDouyinVideo
	savedDouyinNote := env.savedDouyinNote
	savedWeiboPost := env.savedWeiboPost
	savedWeiboRetweet := env.savedWeiboRetweet
	savedXPost := env.savedXPost
	savedXRetweet := env.savedXRetweet
	seedRecentAndLive := func() { env.seedRecentAndLive(app) }
	injectMixedHomeFeed := func() { env.injectMixedHomeFeed(app) }

	return []sceneSpec{
		{
			Name: "home",
			Setup: func() {
				app.ImageViewer.Open = false
				app.PlayerOverlay.Open = false
				app.DeleteConfirm.Open = false
				app.FeedSiteFilter = "all"
				app.Navigate(ui.RouteHome)
				seedRecentAndLive()
				injectMixedHomeFeed()
				st := b.GetUIState()
				st.HomeSeen = nil
				st.Anchor.Order = "newest"
				// Between the cached posts: newer ones are unread.
				st.Anchor.Key = ""
				st.Anchor.PubTs = 1710000500
				_, _ = b.UpdateUIState(st)
				app.RestartHomeSession()
			},
			RequiredLabels: []string{
				"主页刷新",
				"主页站点筛选：全部",
				"主页站点筛选：B 站",
				"主页站点筛选：抖音",
				"主页站点筛选：微博",
				"主页站点筛选：X",
				"主页作者站点：bilibili:video:SYNTH_0001",
				"主页作者站点：" + savedWeiboPost.ItemKey,
				"主页作者站点：" + savedXPost.ItemKey,
				"主页作者站点：" + savedDouyinNote.ItemKey,
			},
			// DEC-053 ②: the subscriptions in the panel, or the avatar
			// strip in a narrow window; no section titles.
			PanelRequiredLabels:  []string{"主页订阅列表"},
			NarrowRequiredLabels: []string{"主页订阅头像条", "主页订阅头像：全部动态"},
			ForbiddenLabels:      []string{"正在直播", "有更新", "快速解析输入框", "快速解析提交"},
		},
		{
			// DEC-053 ①: a new session back where the timeline was read.
			Name: "home-restored",
			Setup: func() {
				app.ImageViewer.Open = false
				app.PlayerOverlay.Open = false
				app.DeleteConfirm.Open = false
				app.Navigate(ui.RouteParse)
				app.FeedSiteFilter = "all"
				page, err := b.FeedPage(library.FeedPageRequest{Order: "newest", CachedOnly: true, Limit: 3})
				st := b.GetUIState()
				if err == nil && len(page.Items) == 3 {
					st.HomePositions = map[string]uistate.HomePosition{"all": {Key: page.Items[2].FeedKey, PubTs: page.Items[2].PubTs, OffsetPx: 40}}
				}
				_, _ = b.UpdateUIState(st)
				app.RestartHomeSession()
				app.FeedItems = nil
				app.RefreshData()
				app.Navigate(ui.RouteHome)
			},
			Frames:              4,
			RequiredLabels:      []string{"主页信息流列表"},
			PanelRequiredLabels: []string{"主页订阅列表"},
		},
		{
			Name: "home-sub",
			Setup: func() {
				app.ImageViewer.Open = false
				app.PlayerOverlay.Open = false
				app.DeleteConfirm.Open = false
				app.Navigate(ui.RouteHome)
				seedRecentAndLive()
				injectMixedHomeFeed()
				st := b.GetUIState()
				st.HomeSeen = nil
				st.Anchor.Order = "newest"
				st.Anchor.Key = app.FeedItems[12].FeedKey
				st.Anchor.PubTs = app.FeedItems[12].PubTs
				_, _ = b.UpdateUIState(st)
				app.RestartHomeSession()
				app.OpenHomeUpdatedActor("技术频道_Alpha")
			},
			RequiredLabels: []string{"返回主页时间线"},
		},
		{
			Name: "subscriptions",
			Setup: func() {
				app.SubDetailOpen = false
				app.SubAccountsOpen = false
				app.SubChecksOpen = false
				app.Navigate(ui.RouteSubscriptions)
				seedRecentAndLive()
			},
			RequiredLabels: []string{"选择订阅 " + primarySub.ID},
			PanelRequiredLabels: []string{
				"订阅侧栏：全部订阅",
				"订阅侧栏：全部账号",
				"订阅侧栏：检查计划",
			},
			NarrowRequiredLabels: []string{
				"订阅页全部订阅",
				"订阅页全部账号",
				"订阅页检查计划",
			},
			ForbiddenLabels: []string{
				"订阅详情账号站点：bilibili:20001",
				"订阅详情账号站点：douyin:" + douyinSecUID,
				"订阅详情账号站点：weibo:" + weiboUID,
				"订阅详情账号站点：x:" + xUID,
				"订阅详情账号站点：x:" + xOnlyUID,
				"订阅账号站点：x:" + xUID,
				"订阅账号站点：x:" + xOnlyUID,
				"订阅网格站点",
				"用户页站点标记",
			},
		},
		{
			Name: "add-sub-preset-modal",
			Setup: func() {
				seedRecentAndLive()
				app.SubscribePicker.Open = false
				app.ImportSubModalOpen = false
				app.Navigate(ui.RouteSubscriptions)
				app.SubDetailOpen, app.SubAccountsOpen, app.SubChecksOpen = false, false, false
				app.AddSubName = "合成新创作者"
				app.AddSubInput = ""
				app.AddSubPreview = nil
				app.AddSubPreviewOwner = nil
				app.AddSubPreset.ResetDefault()
				app.AddSubPreset.CustomExpanded = true
				app.AddSubDialogOpen = true
			},
			RequiredLabels: []string{
				"添加订阅对话框",
				"新订阅名称",
				"新订阅追踪模式选择区",
				"新订阅预设：full_archive",
				"新订阅预设：follow_cache",
				"新订阅预设：check_only",
				"新订阅预设：manual",
				"新订阅录直播",
				"新订阅自定义",
				"新订阅自定义原子标签区",
				"新订阅原子标签：scheduled_check",
				"新订阅原子标签：full_cache",
				"确认添加订阅",
			},
			RequiredTexts: []string{
				"完整存档（默认）",
				"追更并保存",
				"只看新动态",
				"仅手动",
				"录直播",
			},
		},
		{
			Name: "subscribe-picker",
			Setup: func() {
				seedRecentAndLive()
				app.AddSubDialogOpen = false
				app.Navigate(ui.RouteSubscriptions)
				app.SubDetailOpen, app.SubAccountsOpen, app.SubChecksOpen = false, false, false
				app.OpenSubscribePicker(subscription.AccountRef{Site: "bilibili", UID: "30001", Name: "新发现的UP主"})
			},
			RequiredLabels: []string{
				"加入订阅对话框",
				"加入订阅追踪模式选择区",
				"加入订阅预设：full_archive",
				"新建订阅",
				"加入订阅搜索框",
				"加入订阅列表",
				"加入现有订阅：" + primarySub.ID,
			},
		},
		{
			Name: "move-account-picker",
			Setup: func() {
				seedRecentAndLive()
				app.Navigate(ui.RouteSubscriptions)
				app.SubDetailOpen, app.SubAccountsOpen, app.SubChecksOpen = false, false, false
				app.OpenMoveAccountPicker(primarySub, subscription.AccountRef{Site: "weibo", UID: weiboUID, Name: "技术频道_微博"})
			},
			RequiredLabels: []string{
				"更改关联订阅对话框",
				"更改关联账号站点",
				"移到新建订阅",
				"更改关联搜索框",
				"更改关联订阅列表",
				"移到现有订阅：" + auroraSub.ID,
			},
			ForbiddenLabels: []string{
				"移到现有订阅：" + primarySub.ID,
			},
		},
		{
			Name: "subscriptions-all-accounts",
			Setup: func() {
				seedRecentAndLive()
				app.SubscribePicker.Open = false
				app.Navigate(ui.RouteSubscriptions)
				app.SubDetailOpen = false
				app.SubAccountsOpen = true
				app.SubChecksOpen = false
				app.SubAccountsSite = "all"
			},
			RequiredLabels: []string{
				"订阅账号站点筛选：全部",
				"订阅账号站点筛选：B 站",
				"订阅账号站点筛选：抖音",
				"订阅账号站点筛选：微博",
				"订阅账号站点筛选：X",
				"订阅账号列表",
				"订阅账号：bilibili:20001",
				"所属订阅：bilibili:20001",
				"全部账号更多：bilibili:20001",
				"订阅账号：x:" + xUID,
				"订阅账号站点：x:" + xUID,
			},
			PanelRequiredLabels:  []string{"订阅侧栏：全部账号", "订阅侧栏：检查计划"},
			NarrowRequiredLabels: []string{"订阅页全部订阅", "订阅页全部账号", "订阅页检查计划"},
		},
		{
			Name: "subscriptions-checks",
			Setup: func() {
				seedRecentAndLive()
				app.SubscribePicker.Open = false
				app.OpenSubscriptionChecks()
			},
			RequiredLabels: []string{
				"检查计划列表",
				"检查计划行 bilibili:20001",
				"检查计划行 douyin:" + douyinSecUID,
				"检查计划行 weibo:" + weiboUID,
				"检查计划行 x:" + xUID,
			},
			PanelRequiredLabels: []string{
				"订阅侧栏：全部订阅",
				"订阅侧栏：全部账号",
				"订阅侧栏：检查计划",
			},
			NarrowRequiredLabels: []string{
				"订阅页全部订阅",
				"订阅页全部账号",
				"订阅页检查计划",
			},
		},
		{
			Name: "subscription-detail",
			Setup: func() {
				seedRecentAndLive()
				if primarySub.ID != "" {
					app.SubDetailTab = "content"
					app.OpenSubscriptionDetail(primarySub)
					app.SubDetailTab = "content"
				}
			},
			RequiredLabels: []string{
				"订阅标签：内容",
				"订阅标签：账号",
				"订阅缓存标签菜单",
				"订阅详情缓存标签栏",
				"订阅管理菜单",
				"订阅自定义链接栏",
				"打开订阅链接：link3.cc/alpha_tech",
				"打开订阅链接：创作手记",
				"管理订阅自定义链接",
				"订阅内容信息流列表",
				"主页作者站点：" + savedDynamicItem.ItemKey,
				"主页作者站点：" + savedDouyinVideo.ItemKey,
			},
			ForbiddenLabels: []string{
				"关联账号输入",
				"开始录制 9001",
			},
		},
		{
			Name: "subscription-links-dialog",
			Setup: func() {
				seedRecentAndLive()
				if primarySub.ID != "" {
					app.SubDetailTab = "content"
					app.OpenSubscriptionDetail(primarySub)
					app.OpenSubLinksModal(primarySub)
				}
			},
			RequiredLabels: []string{
				"订阅自定义链接对话框",
				"对话框打开订阅链接 1",
				"对话框打开订阅链接 2",
				"编辑订阅链接 1",
				"删除订阅链接 1",
				"新订阅链接名称",
				"新订阅链接地址",
				"确认添加订阅链接",
				"关闭自定义链接对话框",
			},
		},
		{
			Name: "subscription-content-empty",
			Setup: func() {
				seedRecentAndLive()
				if auroraSub.ID != "" {
					app.SubCustomTagsSubID = ""
					app.SubDetailTab = "content"
					app.OpenSubscriptionDetail(auroraSub)
					app.SubDetailTab = "content"
				}
			},
			RequiredLabels: []string{
				"订阅标签：内容",
				"订阅标签：账号",
				"订阅管理菜单",
				"订阅内容空状态说明",
			},
			ForbiddenLabels: []string{
				"订阅自定义链接栏",
				"订阅内容信息流列表",
				"关联账号输入",
			},
		},
		{
			Name: "subscription-detail-dep-warning",
			Setup: func() {
				if auroraSub.ID != "" {
					if updated, err := b.SetSubscriptionTags(auroraSub.ID, []string{subscription.TagCacheVideo}); err == nil {
						app.RefreshData()
						seedRecentAndLive()
						app.SubDetailTab = "content"
						app.OpenSubscriptionDetail(updated)
						app.SubDetailTab = "content"
						app.SubCustomTagsSubID = updated.ID
					}
				}
			},
			RequiredLabels: []string{
				"订阅缓存标签菜单",
				"订阅详情缓存标签栏",
				"订阅详情自定义原子标签栏",
				"订阅原子标签：定时检查",
				"订阅原子标签：自动缓存视频",
				"自动缓存缺少定时检查提示",
				"一键开启定时检查",
				"订阅内容空状态说明",
			},
			RequiredTexts: []string{
				"追踪：自定义",
				"已开启自动缓存但未开启「定时检查」：仅在手动检查更新时生效。",
			},
		},
		{
			Name: "subscription-accounts",
			Setup: func() {
				if auroraSub.ID != "" {
					_, _ = b.SetSubscriptionTags(auroraSub.ID, []string{
						subscription.TagScheduledCheck,
						subscription.TagCacheVideo,
						subscription.TagCacheDraw,
					})
					app.RefreshData()
				}
				seedRecentAndLive()
				app.SubCustomTagsSubID = ""
				if primarySub.ID != "" {
					app.OpenSubscriptionDetail(primarySub)
					app.SubDetailTab = "accounts"
				}
			},
			RequiredLabels: []string{
				"订阅标签：内容",
				"订阅标签：账号",
				"订阅直播更多",
				"订阅详情账号站点：bilibili:20001",
				"订阅详情账号站点：douyin:" + douyinSecUID,
				"订阅详情账号站点：weibo:" + weiboUID,
				"订阅详情账号站点：x:" + xUID,
				"开始录制 9001",
				"停止录制 douyin:" + douyinSecUID,
				"订阅账号更多：bilibili:20001",
				"订阅账号更多：douyin:" + douyinSecUID,
				"订阅账号更多：weibo:" + weiboUID,
				"订阅账号更多：x:" + xUID,
				"关联账号输入",
				"预览关联账号",
			},
			ForbiddenLabels: []string{
				"订阅内容信息流列表",
				"开始录制 weibo:" + weiboUID,
				"停止录制 weibo:" + weiboUID,
				"开始录制 x:" + xUID,
				"停止录制 x:" + xUID,
			},
		},
		{
			Name: "subscription-accounts-x",
			Setup: func() {
				seedRecentAndLive()
				if xOnlySub.ID != "" {
					app.OpenSubscriptionDetail(xOnlySub)
					app.SubDetailTab = "accounts"
				}
			},
			RequiredLabels: []string{
				"订阅标签：内容",
				"订阅标签：账号",
				"订阅详情账号站点：x:" + xOnlyUID,
				"订阅账号更多：x:" + xOnlyUID,
			},
			RequiredTexts: []string{
				"@" + xOnlyHandle,
			},
			ForbiddenLabels: []string{
				"订阅内容信息流列表",
				"开始录制 x:" + xOnlyUID,
				"停止录制 x:" + xOnlyUID,
			},
			ForbiddenTexts: []string{
				"未开播",
				"直播中",
				"录制中",
				"未开通直播间",
			},
		},
		{
			Name: "import-followings-weibo",
			Setup: func() {
				seedRecentAndLive()
				app.Navigate(ui.RouteSubscriptions)
				app.SubDetailOpen, app.SubAccountsOpen = false, false
				app.ImportSubModalOpen = true
				app.ImportSite = "weibo"
				app.ImportGroup = "全部关注"
				app.ImportSearch = ""
				app.ImportError = ""
				app.ImportGroupsDTO = nil
				app.ImportGroupsEnabled = false
				app.ImportCreators = []service.FollowingCreatorDTO{
					{Site: "weibo", UID: weiboUID, Name: "技术频道_微博", Sign: "系统工程与媒体工具开发笔记", Subscribed: true, OwnerSubName: "技术频道_Alpha"},
					{Site: "weibo", UID: "500000002", Name: "合成摄影日记", Sign: "胶片与风光摄影日常"},
					{Site: "weibo", UID: "500000003", Name: "合成设计周刊", Sign: "每周分享桌面界面与字体排印"},
				}
				app.ImportSelectedKeys = map[string]bool{"weibo:500000002": true}
				app.ImportHasMore = false
			},
			RequiredLabels: []string{
				"关注导入对话框",
				"关注导入站点：哔哩哔哩",
				"关注导入站点：微博",
				"关注导入站点：X",
				"关注导入虚拟列表",
				"勾选导入 500000002",
			},
			ForbiddenLabels: []string{
				"关注分组",
				"勾选导入 " + weiboUID,
			},
		},
		{
			Name: "import-followings-x",
			Setup: func() {
				seedRecentAndLive()
				app.Navigate(ui.RouteSubscriptions)
				app.SubDetailOpen, app.SubAccountsOpen = false, false
				app.ImportSubModalOpen = true
				app.ImportSite = "x"
				app.ImportGroup = "全部关注"
				app.ImportSearch = ""
				app.ImportError = ""
				app.ImportGroupsDTO = nil
				app.ImportGroupsEnabled = false
				app.ImportCreators = []service.FollowingCreatorDTO{
					{Site: "x", UID: xUID, UniqueID: xHandle, Name: "技术频道_X", Sign: "系统工程与跨平台媒体工具实践", Subscribed: true, OwnerSubName: "技术频道_Alpha"},
					{Site: "x", UID: "1000000000000000004", UniqueID: "synth_x_dev", Name: "合成X开发者", Sign: "每周分享 Go 与桌面界面工程"},
					{Site: "x", UID: "1000000000000000005", UniqueID: "synth_x_photo", Name: "合成X摄影师", Sign: "胶片与风光摄影归档"},
				}
				app.ImportSelectedKeys = map[string]bool{"x:1000000000000000004": true}
				app.ImportHasMore = false
			},
			RequiredLabels: []string{
				"关注导入对话框",
				"关注导入站点：哔哩哔哩",
				"关注导入站点：微博",
				"关注导入站点：X",
				"关注导入虚拟列表",
				"勾选导入 1000000000000000004",
			},
			RequiredTexts: []string{
				"@" + xHandle,
				"@synth_x_dev",
				"已在订阅「技术频道_Alpha」中",
			},
			ForbiddenLabels: []string{
				"关注分组",
				"勾选导入 " + xUID,
			},
		},
		{
			Name: "settings-rules",
			Setup: func() {
				if auroraSub.ID != "" {
					_, _ = b.SetSubscriptionTags(auroraSub.ID, []string{
						subscription.TagScheduledCheck,
						subscription.TagCacheVideo,
						subscription.TagCacheDraw,
					})
					app.RefreshData()
				}
				seedRecentAndLive()
				app.ImportSubModalOpen = false
				app.RuleBatch.Open = false
				app.MultiTagBatch.Open = false
				app.SettingsCategory = "rules"
				app.Navigate(ui.RouteSettings)
			},
			RequiredLabels: []string{
				"打开批量设置订阅标签",
				"缓存标签快速预设栏",
				"快速应用预设：full_archive",
				"缓存标签行：scheduled_check",
				"缓存标签行：cache_video",
				"缓存标签行：cache_draw",
				"缓存标签行：cache_word",
				"缓存标签行：cache_forward",
				"缓存标签行：full_cache",
				"缓存标签行：auto_record",
				"管理标签订阅：cache_draw",
			},
			ForbiddenTexts: []string{
				"一个标签对应一项缓存能力",
				"按设置的间隔自动检查关联账号的新内容与开播状态",
				"检查更新发现新发布的视频动态",
				"检查更新发现新发布的图文",
				"检查更新发现新发布的纯文字",
				"检查更新发现新发布的转发",
				"每 7 天检查一次每个账号的全部作品",
				"发现哔哩哔哩或抖音账号正在直播时自动开始录制",
			},
		},
		{
			Name: "settings-rules-batch",
			Setup: func() {
				seedRecentAndLive()
				app.SettingsCategory = "rules"
				app.Navigate(ui.RouteSettings)
				app.RuleBatch.Open = false
				app.MultiTagBatch.Open = false
				openTester := mygoui.NewTester(app.View, 1280, 860)
				_ = openTester.Click("管理标签订阅：cache_draw")
			},
			RequiredLabels: []string{
				"选择订阅对话框",
				"规则订阅搜索框",
				"全选",
				"全不选",
				"勾选规则订阅：技术频道_Alpha",
				"勾选规则订阅：独立游戏工坊",
				"勾选规则订阅：数字影像志",
				"应用到 2 个订阅",
				"取消选择规则订阅",
			},
		},
		{
			Name: "settings-tags-multi-batch",
			Setup: func() {
				seedRecentAndLive()
				app.SettingsCategory = "rules"
				app.Navigate(ui.RouteSettings)
				app.RuleBatch.Open = false
				app.MultiTagBatch.Open = false
				openTester := mygoui.NewTester(app.View, 1280, 860)
				_ = openTester.Click("打开批量设置订阅标签")
				app.MultiTagBatch.SelectedTags[subscription.TagCacheVideo] = true
				app.MultiTagBatch.SelectedTags[subscription.TagCacheDraw] = true
				if primarySub.ID != "" {
					app.MultiTagBatch.SelectedSubs[primarySub.ID] = true
				}
				if auroraSub.ID != "" {
					app.MultiTagBatch.SelectedSubs[auroraSub.ID] = true
				}
			},
			RequiredLabels: []string{
				"批量设置缓存标签对话框",
				"批量模式：add",
				"批量模式：remove",
				"批量模式：replace",
				"批量选择标签：自动缓存视频",
				"批量订阅搜索框",
				"批量订阅全选",
				"批量订阅全不选",
				"勾选批量订阅：技术频道_Alpha",
				"确认批量设置标签",
				"取消批量设置标签",
			},
		},
		{
			Name: "settings-account",
			Setup: func() {
				app.RuleBatch.Open = false
				app.MultiTagBatch.Open = false
				seedRecentAndLive()
				app.SettingsCategory = "account"
				app.Navigate(ui.RouteSettings)
			},
			RequiredLabels: []string{
				"账号分组：哔哩哔哩",
				"账号分组：抖音",
				"账号分组：微博",
				"账号分组：X",
				"重新粘贴抖音Cookie",
				"退出抖音登录",
				"重新粘贴微博Cookie",
				"退出微博登录",
				"重新粘贴XCookie",
				"退出X登录",
			},
			RequiredTexts: []string{
				"X（原 Twitter）",
				"@trace_x_demo",
				"粘贴的 Cookie 不续期",
				"风险说明",
			},
		},
		{
			Name: "settings-download",
			Setup: func() {
				seedRecentAndLive()
				app.SettingsCategory = "download"
				app.Navigate(ui.RouteSettings)
			},
			RequiredTexts:  []string{"微博同时保存无水印版"},
			RequiredLabels: []string{"开关微博图片去水印"},
		},
		{
			Name: "settings-subscription",
			Setup: func() {
				seedRecentAndLive()
				app.SettingsCategory = "subscription"
				app.Navigate(ui.RouteSettings)
			},
			RequiredLabels: []string{
				"选择订阅检查间隔",
				"定时检查状态摘要",
				"退避状态：douyin",
			},
			RequiredTexts: []string{
				"检查间隔",
				"已开启定时检查：",
			},
			ForbiddenLabels: []string{
				"开关定时订阅",
			},
		},
		{
			Name: "settings-storage",
			Setup: func() {
				seedRecentAndLive()
				app.SettingsCategory = "storage"
				app.Navigate(ui.RouteSettings)
			},
			RequiredLabels: []string{
				"缓存目录输入框",
				"选择缓存保存位置",
				"应用缓存目录",
				"添加已有文件夹输入框",
				"选择已有缓存文件夹",
				"从已有文件夹添加",
			},
		},
		{
			Name: "parse",
			Setup: func() {
				app.Navigate(ui.RouteParse)
				app.LastParseError = ""
				app.HistoryItems = b.ListHistory()
				app.ParsePageInput = "https://www.douyin.com/video/7388001001"
				app.HistoryItemMenuID = "bilibili:video:BV1xx411c7mD"
			},
			RequiredLabels: []string{
				"链接解析输入框",
				"立即解析",
				"历史站点：x:1000000000000000009",
			},
			PanelRequiredLabels: []string{
				"最近解析更多",
				"历史打开：BV1xx411c7mD",
				"历史更多：BV1xx411c7mD",
				"历史站点：bilibili:BV1xx411c7mD",
				"历史站点：douyin:7388001001",
				"历史站点：weibo:5000000000000001",
				"历史站点：x:1000000000000000009",
				"历史站点：x:" + xUID,
			},
			RequiredTexts: []string{
				"帖子",
				"X 用户",
			},
			ForbiddenLabels: []string{"快速解析输入框"},
		},
		{
			Name: "parse-weibo-live",
			Setup: func() {
				app.Navigate(ui.RouteParse)
				app.HistoryItems = b.ListHistory()
				app.ParsePageInput = "https://weibo.com/l/wblive/p/show/1022:2321325000000000000099"
				app.LastParseError = "微博直播暂不支持录制，可以在浏览器中打开"
			},
			RequiredLabels: []string{
				"链接解析输入框",
				"立即解析",
			},
			ForbiddenLabels: []string{"快速解析输入框"},
		},
		{
			Name: "parse-x-spaces",
			Setup: func() {
				app.Navigate(ui.RouteParse)
				app.HistoryItems = b.ListHistory()
				app.ParsePageInput = "https://x.com/i/spaces/1SyntheticSpaceId"
				app.LastParseError = "X 直播与 Spaces 暂不支持录制，可以在浏览器中打开"
			},
			RequiredLabels: []string{
				"链接解析输入框",
				"立即解析",
			},
			RequiredTexts: []string{
				"X 直播与 Spaces 暂不支持录制，可以在浏览器中打开",
			},
			ForbiddenLabels: []string{"快速解析输入框"},
		},
		{
			Name: "video",
			Setup: func() {
				app.CurrentLibraryDetail = nil
				app.CurrentVideoItemKey = ""
				app.CurrentVideo = &bilibili.VideoDetail{
					BVID:         "BV1xx411c7mD",
					AID:          10001,
					Title:        "示例多 P 视频详情：纯 Go 桌面架构全链路解析",
					TName:        "科技",
					Desc:         "演示分 P 列表勾选、最高画质/无损音轨缓存入队与封面原图查看。",
					Cover:        "https://i0.hdslb.com/bfs/archive/cover.jpg",
					PubTs:        1710000000,
					Author:       bilibili.AuthorInfo{Mid: 20001, Name: "技术频道_Alpha"},
					Stat:         bilibili.VideoStat{View: 128000, Danmaku: 3420, Like: 9600, Coin: 4800, Favorite: 6200, Share: 1100, Reply: 850},
					CanonicalURL: "https://www.bilibili.com/video/BV1xx411c7mD",
					Pages: []bilibili.VideoPage{
						{CID: 101, Page: 1, Title: "第一部分：三栏自适应布局", Duration: 180},
						{CID: 102, Page: 2, Title: "第二部分：fMP4 无重编码混流", Duration: 240},
					},
				}
				app.SelectedPartPages = map[int]bool{1: true, 2: true}
				app.CurrentRoute = ui.RouteVideo
			},
			RequiredLabels: []string{"视频详情站点标记"},
		},
		{
			Name: "library-item",
			Setup: func() {
				app.Navigate(ui.RouteLibrary)
				app.OpenLibraryItemDetail(savedItem)
			},
			RequiredLabels: []string{
				"视频详情站点标记",
				"本机详情更多",
				"播放分P 1",
			},
			PanelRequiredLabels: []string{
				"媒体库站点筛选：全部",
				"媒体库站点筛选：B 站",
				"媒体库站点筛选：抖音",
				"媒体库站点筛选：微博",
				"媒体库站点筛选：X",
			},
			ForbiddenLabels: []string{
				"缓存视频",
				"视频详情升级缓存",
			},
		},
		{
			Name: "cached-video",
			Setup: func() {
				app.OpenLibraryItemDetail(savedPartialItem)
			},
			RequiredLabels:      []string{"视频详情站点标记", "播放分P 1", "本机详情更多"},
			PanelRequiredLabels: []string{"媒体库站点筛选：全部"},
			ForbiddenLabels:     []string{"缓存视频", "选择分P 2"},
		},
		{
			Name: "cached-dynamic",
			Setup: func() {
				app.OpenLibraryItemDetail(savedDynamicItem)
			},
			RequiredLabels: []string{"查看图片 1", "播放视频卡片：" + savedItem.ItemKey},
		},
		{
			Name: "recording-detail",
			Setup: func() {
				app.OpenLibraryItemDetail(savedLiveItem)
			},
			RequiredLabels: []string{"直播录像详情", "播放录像片段：" + savedLiveItem.ItemKey + ":P01_第一集.mp4"},
		},
		{
			Name: "user",
			Setup: func() {
				app.CurrentUser = &bilibili.UserSpace{
					Mid:          20001,
					Name:         "技术频道_Alpha",
					Level:        6,
					Fans:         58200,
					Friend:       128,
					ArchiveCount: 96,
					Sign:         "专注系统编程与多媒体工程",
					CanonicalURL: "https://space.bilibili.com/20001",
				}
				dyns := make([]bilibili.DynamicItem, 500)
				for i := 0; i < 500; i++ {
					var imgs []bilibili.DynamicMedia
					if i == 0 {
						imgs = []bilibili.DynamicMedia{
							{URL: "https://i0.hdslb.com/bfs/album/sample1.jpg", Width: 1920, Height: 1080},
							{URL: "https://i0.hdslb.com/bfs/album/sample2.jpg", Width: 1920, Height: 1080},
							{URL: "https://i0.hdslb.com/bfs/album/sample3.jpg", Width: 1920, Height: 1080},
						}
					}
					dyns[i] = bilibili.DynamicItem{
						ID:        fmt.Sprintf("dyn_%04d", i+1),
						Type:      "DYNAMIC_TYPE_DRAW",
						TypeLabel: "图文动态",
						Title:     fmt.Sprintf("创作者动态 #%03d", i+1),
						Text:      "验证 500 条空间动态虚拟列表与翻页错误保留已加载内容。",
						Images:    imgs,
					}
				}
				app.UserDynamics = dyns
				app.UserDynamicsError = "加载下一页失败：bilibili [risk_control code=-352]: 请求触发风控拦截（已保留第 1 页内容，可点击重试）"
				app.UserTab = "dynamics"
				app.CurrentRoute = ui.RouteUser
				app.PostMenuKey = "dyn:dyn_0001"
			},
			RequiredLabels: []string{"用户页站点标记", "空间更多", "帖子更多：dyn:dyn_0001", "UP主动态虚拟列表", "用户页筛选：全部", "用户页筛选：未缓存"},
		},
		{
			Name: "douyin-user",
			Setup: func() {
				app.Opening = nil
				app.LiveAccounts = []service.LiveAccountState{
					{
						Site:        "douyin",
						AccountKey:  "douyin:" + douyinSecUID,
						UID:         douyinSecUID,
						RoomID:      "73990011",
						WebRID:      "668899",
						Uname:       "技术频道_抖音号",
						Title:       "高原星空实拍 HTTP-FLV 原画直播",
						LiveStatus:  1,
						StatusText:  "直播中",
						QualityName: "原画",
						Codec:       "hevc",
					},
				}
				app.CurrentDouyinUser = &service.DouyinUserView{
					SecUID:         douyinSecUID,
					UID:            "8800101",
					UniqueID:       "alpha_douyin",
					Nickname:       "技术频道_抖音号",
					Signature:      "系统编程与高码率竖屏风光摄影记录",
					AwemeCount:     48,
					FollowerCount:  186000,
					FollowingCount: 64,
					TotalFavorited: 1420000,
					LiveStatus:     1,
					RoomID:         "73990011",
					WebRID:         "668899",
					CanonicalURL:   "https://www.douyin.com/user/" + douyinSecUID,
				}
				app.CurrentDouyinWorks = []service.DouyinWorkCardDTO{
					{
						ID:           "7388001001",
						Kind:         "video",
						Title:        "竖屏高原雪山延时摄影记录",
						DurationSec:  95,
						IsTop:        true,
						CacheKey:     savedDouyinVideo.ItemKey,
						CanonicalURL: "https://www.douyin.com/video/7388001001",
					},
					{
						ID:           "7388001002",
						Kind:         "note",
						Title:        "山谷图文实况与视频混排手记",
						PageCount:    3,
						CacheKey:     savedDouyinNote.ItemKey,
						CanonicalURL: "https://www.douyin.com/note/7388001002",
					},
					{
						ID:           "7388001003",
						Kind:         "video",
						Title:        "冰川航拍无水印原片测试",
						DurationSec:  62,
						CacheKey:     "douyin:video:7388001003",
						CanonicalURL: "https://www.douyin.com/video/7388001003",
					},
				}
				app.DouyinWorksHasMore = false
				app.DouyinWorksError = ""
				app.DouyinWorksAuthExpired = false
				app.DouyinCardMenuID = "7388001001"
				app.CurrentRoute = ui.RouteDouyinUser
			},
			RequiredLabels: []string{
				"用户页站点标记",
				"抖音主页直播横幅",
				"抖音主页开始录制",
				"抖音置顶标记：7388001001",
				"抖音图文标记：7388001002",
				"作品更多：7388001001",
				"抖音作品虚拟列表",
			},
			ForbiddenLabels: []string{"UP主动态虚拟列表", "UP主投稿虚拟列表"},
		},
		{
			Name: "douyin-video",
			Setup: func() {
				app.Opening = nil
				app.CurrentLibraryDetail = nil
				app.CurrentDouyinWork = &service.DouyinWorkView{
					ID:           "7388001001",
					Kind:         "video",
					Title:        "竖屏高原雪山延时摄影记录",
					Desc:         "竖屏高原雪山延时摄影记录\n无水印高码率原片归档 #延时摄影 #风光",
					Hashtags:     []string{"延时摄影", "风光"},
					CreateTime:   1710003000,
					DurationSec:  95,
					Author:       service.DouyinAuthorDTO{SecUID: douyinSecUID, UniqueID: "alpha_douyin", Nickname: "技术频道_抖音号"},
					QualityLabel: "1080P",
					VideoCodec:   "hevc",
					Width:        1080,
					Height:       1440,
					Music:        &service.DouyinMusicDTO{Title: "高原风声", Author: "技术频道_抖音号", IsOriginalSound: true},
					Stats:        service.DouyinStatsDTO{DiggCount: 18200, CommentCount: 640, CollectCount: 3100, ShareCount: 420},
					CanonicalURL: "https://www.douyin.com/video/7388001001",
					CacheKey:     savedDouyinVideo.ItemKey,
					CachedItem:   &savedDouyinVideo,
				}
				app.CurrentRoute = ui.RouteDouyinWork
			},
			RequiredLabels:  []string{"抖音作品作者站点标记", "缓存视频", "抖音视频封面播放"},
			ForbiddenLabels: []string{"升级缓存", "补存弹幕和字幕"},
		},
		{
			Name: "douyin-video-uncached",
			Setup: func() {
				app.Opening = nil
				app.CurrentLibraryDetail = nil
				app.PlayerOverlay.Open = false
				app.PlayerOverlay.PreviewMode = false
				app.PlayerOverlay.PreviewOnCache = nil
				app.CurrentDouyinWork = &service.DouyinWorkView{
					ID:           "7388001099",
					Kind:         "video",
					Title:        "川西日照金山短视频（未缓存可临时预览）",
					Desc:         "川西日照金山短视频（未缓存可临时预览）\n点击封面直接零缓存预览，喜欢再一键缓存到媒体库 #川西 #延时摄影",
					Hashtags:     []string{"川西", "延时摄影"},
					CreateTime:   1710003600,
					DurationSec:  42,
					Author:       service.DouyinAuthorDTO{SecUID: douyinSecUID, UniqueID: "alpha_douyin", Nickname: "技术频道_抖音号"},
					VideoURL:     "https://v26-web.douyinvod.com/synth/preview_7388001099.mp4",
					QualityLabel: "1080P",
					VideoCodec:   "avc",
					Width:        1080,
					Height:       1440,
					Music:        &service.DouyinMusicDTO{Title: "雪山晨风", Author: "技术频道_抖音号", IsOriginalSound: true},
					Stats:        service.DouyinStatsDTO{DiggCount: 6420, CommentCount: 198, CollectCount: 1120, ShareCount: 84},
					CanonicalURL: "https://www.douyin.com/video/7388001099",
					CacheKey:     library.CanonicalItemKey("douyin", "video", "7388001099"),
				}
				app.CurrentRoute = ui.RouteDouyinWork
			},
			RequiredLabels:  []string{"抖音作品作者站点标记", "缓存视频", "抖音视频封面播放", "抖音视频预览提示"},
			ForbiddenLabels: []string{"升级缓存", "补存弹幕和字幕", "抖音本机缓存规格"},
		},
		{
			Name: "cached-douyin-video",
			Setup: func() {
				app.OpenLibraryItemDetail(savedDouyinVideo)
			},
			RequiredLabels:      []string{"抖音作品作者站点标记", "本机详情更多", "抖音视频封面播放"},
			PanelRequiredLabels: []string{"媒体库站点筛选：全部"},
			ForbiddenLabels:     []string{"缓存视频", "升级缓存", "补存弹幕和字幕", "抖音作品统计行"},
		},
		{
			Name: "douyin-note",
			Setup: func() {
				app.OpenLibraryItemDetail(savedDouyinNote)
			},
			RequiredLabels: []string{
				"抖音作品作者站点标记",
				"本机详情更多",
				"查看图文页 1",
				"实况标记 2",
				"查看图文页 3",
				"视频页标记 3",
				"配乐栏：douyin-note:7388001002",
				"配乐播放暂停：douyin-note:7388001002",
				"配乐进度条：douyin-note:7388001002",
			},
			ForbiddenLabels: []string{"缓存图文"},
		},
		{
			Name: "weibo-user",
			Setup: func() {
				app.Opening = nil
				app.CurrentLibraryDetail = nil
				app.UserListFilter = "all"
				app.CurrentWeiboUser = &service.WeiboUserView{
					UID:            weiboUID,
					ScreenName:     "技术频道_微博",
					Description:    "系统工程、纯 Go 桌面架构与摄影日常记录",
					StatusesCount:  342,
					FollowersCount: 92400,
					FriendsCount:   186,
					Verified:       true,
					VerifiedReason: "数码科技领域创作者",
					Domain:         "techchannel",
					CanonicalURL:   "https://weibo.com/u/" + weiboUID,
				}
				app.CurrentWeiboPosts = []service.WeiboPostView{
					{
						Mid:       "5000000000000001",
						Author:    service.WeiboAuthorDTO{UID: weiboUID, ScreenName: "技术频道_微博"},
						CreatedAt: 1710004500,
						Text:      "置顶公告：本账号整理系统编程、多媒体封装与跨平台桌面工具实践笔记，索引汇总见 专栏目录 与 #系统工程#。\n定期更新离线归档与界面排印优化记录。",
						Links: []service.TextLinkDTO{
							{Label: "专栏目录", URL: "https://weibo.com/ttarticle/p/show?id=2309405000000000000001"},
							{Label: "#系统工程#", URL: "https://s.weibo.com/weibo?q=%23%E7%B3%BB%E7%BB%9F%E5%B7%A5%E7%A8%8B%23"},
						},
						IsTop:        true,
						CanonicalURL: "https://weibo.com/" + weiboUID + "/5000000000000001",
						CacheKey:     savedWeiboPost.ItemKey,
					},
					{
						Mid:              "5000000000000015",
						Author:           service.WeiboAuthorDTO{UID: weiboUID, ScreenName: "技术频道_微博"},
						CreatedAt:        1710004700,
						Text:             "转发推荐：主页列表也能直接完整预览原微博正文与附带媒体。",
						IsRetweet:        true,
						OriginIncomplete: true,
						CanonicalURL:     "https://weibo.com/" + weiboUID + "/5000000000000015",
						CacheKey:         savedWeiboRetweet.ItemKey,
						Retweeted: &service.WeiboPostView{
							Mid:       "5000000000000088",
							Author:    service.WeiboAuthorDTO{UID: "500000002", ScreenName: "合成摄影日记"},
							CreatedAt: 1710004100,
							Text:      "原微博节选：川西高原实况照片与单文件 HEVC 航拍视频归档说明。",
							Images:    []service.WeiboMediaItemDTO{{Index: 1, Kind: "image", Width: 480, Height: 200}},
						},
					},
				}
				app.WeiboPostsHasMore = false
				app.WeiboPostsError = ""
				app.WeiboPostsAuthExpired = false
				app.WeiboPostsUncertainEmpty = false
				app.WeiboBottomTipsVisible = true
				app.WeiboBottomTipsText = "博主设置了仅展示半年内的微博"
				app.WeiboUserSubMenuOpen = false
				app.WeiboUserListState = &mygoui.ListState{}
				app.CurrentRoute = ui.RouteWeiboUser
			},
			RequiredLabels: []string{
				"用户页站点标记",
				"微博总数",
				"微博主页更多",
				"用户页筛选：全部",
				"用户页筛选：未缓存",
				"微博用户列表",
				"微博可见范围提示",
			},
		},
		{
			Name: "weibo-user-empty-shell",
			Setup: func() {
				app.Opening = nil
				app.CurrentLibraryDetail = nil
				app.UserListFilter = "all"
				app.CurrentWeiboUser = &service.WeiboUserView{
					UID:            "500000009",
					ScreenName:     "受限测试博主",
					Description:    "用于验证风控空壳与零内容区别",
					StatusesCount:  128,
					FollowersCount: 4500,
					FriendsCount:   32,
					CanonicalURL:   "https://weibo.com/u/500000009",
				}
				app.CurrentWeiboPosts = nil
				app.WeiboPostsHasMore = false
				app.WeiboPostsAuthExpired = false
				app.WeiboPostsUncertainEmpty = true
				app.WeiboPostsError = "微博返回了异常空列表（账号标注有 128 条微博），可能是风控或受限，可稍后重试"
				app.WeiboBottomTipsVisible = false
				app.WeiboBottomTipsText = ""
				app.WeiboUserListState = &mygoui.ListState{}
				app.CurrentRoute = ui.RouteWeiboUser
			},
			RequiredLabels: []string{
				"用户页站点标记",
				"微博空壳提示",
				"重试加载微博",
			},
			ForbiddenLabels: []string{
				"微博零内容提示",
			},
		},
		{
			Name: "weibo-post",
			Setup: func() {
				app.Opening = nil
				app.CurrentLibraryDetail = nil
				app.CurrentWeiboVideo = nil
				imgs := make([]service.WeiboMediaItemDTO, 11)
				for i := range imgs {
					kind := "image"
					if i == 1 {
						kind = "live_photo"
					} else if i == 2 {
						kind = "gif"
					} else if i == 3 {
						kind = "video"
					}
					imgs[i] = service.WeiboMediaItemDTO{
						Index:       i + 1,
						PicID:       fmt.Sprintf("synthpic%02d", i+1),
						Kind:        kind,
						Width:       1080,
						Height:      1080,
						DurationSec: 5,
					}
				}
				app.CurrentWeiboPost = &service.WeiboPostView{
					Mid:       "5000000000000010",
					Author:    service.WeiboAuthorDTO{UID: weiboUID, ScreenName: "技术频道_微博", VerifiedReason: "数码科技领域创作者"},
					CreatedAt: 1710004800,
					Text:      "长微博正文示例：完整保留长微博全文、超过 9 张图按原始顺序展示（共 11 项，含实况照片、GIF 动图与混排视频页），配套文档见 网页链接 与 #桌面客户端#，同时保留可见范围标记与转发原微博降级提示。",
					Links: []service.TextLinkDTO{
						{Label: "网页链接", URL: "https://example.com/weibo-longtext-guide"},
						{Label: "#桌面客户端#", URL: "https://s.weibo.com/weibo?q=%23%E6%A1%8C%E9%9D%A2%E5%AE%A2%E6%88%B7%E7%AB%AF%23"},
					},
					IsLongText:      true,
					LongTextFetched: true,
					Source:          "微博网页版",
					RegionName:      "发布于 上海",
					VisibleType:     10,
					VisibleLabel:    "仅粉丝可见",
					Images:          imgs,
					Retweeted: &service.WeiboPostView{
						Unavailable:       true,
						UnavailableReason: "抱歉，此微博已被作者删除。",
					},
					Stats:        service.WeiboStatsDTO{RepostsCount: 312, CommentsCount: 84, AttitudesCount: 1620},
					CanonicalURL: "https://weibo.com/" + weiboUID + "/5000000000000010",
					CacheKey:     "weibo:dynamic:5000000000000010",
				}
				app.CurrentRoute = ui.RouteWeiboPost
			},
			RequiredLabels: []string{
				"微博详情站点标记",
				"微博可见范围标记",
				"微博正文",
				"打开链接：网页链接",
				"打开链接：#桌面客户端#",
				"查看图文页 1",
				"实况标记 2",
				"动图标记 3",
				"视频页标记 4",
			},
		},
		{
			Name: "weibo-post-video",
			Setup: func() {
				app.Opening = nil
				app.CurrentLibraryDetail = nil
				app.CurrentWeiboVideo = nil
				app.CurrentWeiboPost = &service.WeiboPostView{
					Mid:        "5000000000000011",
					Author:     service.WeiboAuthorDTO{UID: weiboUID, ScreenName: "技术频道_微博"},
					CreatedAt:  1710004900,
					Text:       "单条视频微博示例：自动选择 1080P HEVC 流，并按单文件 MP4 归档。",
					Source:     "微博视频号",
					RegionName: "发布于 北京",
					Video: &service.WeiboVideoDTO{
						ObjectID:     "1034:5000000000000099",
						Title:        "4K 航拍云海与纯 Go 容器重排演示",
						DurationSec:  142,
						QualityLabel: "1080P",
						Codec:        "hevc",
						Width:        1920,
						Height:       1080,
						FPS:          60,
					},
					Stats:        service.WeiboStatsDTO{RepostsCount: 540, CommentsCount: 129, AttitudesCount: 4820},
					CanonicalURL: "https://weibo.com/" + weiboUID + "/5000000000000011",
					CacheKey:     "weibo:dynamic:5000000000000011",
				}
				app.CurrentRoute = ui.RouteWeiboPost
			},
			RequiredLabels: []string{
				"微博详情站点标记",
				"缓存当前微博",
				"微博视频卡片",
				"微博视频规格",
				"微博在线统计",
			},
		},
		{
			Name: "weibo-post-cards",
			Setup: func() {
				app.Opening = nil
				app.CurrentLibraryDetail = nil
				app.CurrentWeiboVideo = nil
				app.CurrentWeiboPost = &service.WeiboPostView{
					Mid:       "5000000000000012",
					Author:    service.WeiboAuthorDTO{UID: weiboUID, ScreenName: "技术频道_微博"},
					CreatedAt: 1710005000,
					Text:      "分享一篇头条文章并转发已删除的原微博，验证降级展示且不提供视频缓存按钮。",
					Source:    "微博网页版",
					Card: &service.WeiboRichCardDTO{
						Kind:        "article",
						Label:       "头条文章",
						Title:       "跨平台桌面客户端的离线缓存设计",
						Desc:        "探讨媒体目录组织、索引重建与多站点凭证隔离。",
						URL:         "https://weibo.com/ttarticle/p/show?id=2309405000000000000012",
						Unsupported: true,
					},
					Retweeted: &service.WeiboPostView{
						Unavailable:       true,
						UnavailableReason: "原微博已删除或不可查看",
					},
					Stats:        service.WeiboStatsDTO{RepostsCount: 88, CommentsCount: 23, AttitudesCount: 410},
					CanonicalURL: "https://weibo.com/" + weiboUID + "/5000000000000012",
					CacheKey:     "weibo:dynamic:5000000000000012",
				}
				app.CurrentRoute = ui.RouteWeiboPost
			},
			RequiredLabels: []string{
				"微博详情站点标记",
				"微博卡片：article",
				"微博卡片在浏览器中打开：article",
				"转发原微博不可用提示",
				"微博在线统计",
			},
			ForbiddenLabels: []string{
				"缓存当前微博",
			},
		},
		{
			Name: "weibo-post-retweet",
			Setup: func() {
				app.Opening = nil
				app.CurrentLibraryDetail = nil
				app.CurrentWeiboVideo = nil
				app.CurrentWeiboPost = &service.WeiboPostView{
					Mid:        "5000000000000015",
					Author:     service.WeiboAuthorDTO{UID: weiboUID, ScreenName: "技术频道_微博", VerifiedReason: "数码科技领域创作者"},
					CreatedAt:  1710004700,
					Text:       "转发推荐：这套多段落排印与原微博媒体归档方案非常完整。\n在线详情可同时浏览外层评论与原微博图文、实况和视频卡片，并支持一键打开原微博。",
					Source:     "微博网页版",
					RegionName: "发布于 上海",
					IsRetweet:  true,
					Retweeted: &service.WeiboPostView{
						Mid:          "5000000000000088",
						Author:       service.WeiboAuthorDTO{UID: "500000002", ScreenName: "合成摄影日记"},
						CreatedAt:    1710004100,
						Text:         "第一段：今天整理完高原风光的实况照片与单文件 MP4 视频归档。\n第二段：正文采用 1.6 倍行高排印后，多行中文动态在桌面端阅读更加舒展。",
						CanonicalURL: "https://weibo.com/500000002/5000000000000088",
						Images: []service.WeiboMediaItemDTO{
							{Index: 1, Kind: "image", FromOrigin: true, Width: 480, Height: 200},
							{Index: 2, Kind: "live_photo", FromOrigin: true, Width: 240, Height: 320, DurationSec: 3},
						},
						Video: &service.WeiboVideoDTO{
							ObjectID:     "1034:5000000000000088",
							Title:        "川西高原延时航拍成片（1080P HEVC）",
							DurationSec:  118,
							QualityLabel: "1080P",
							Codec:        "hevc",
							Width:        1920,
							Height:       1080,
							FPS:          60,
						},
					},
					Stats:        service.WeiboStatsDTO{RepostsCount: 215, CommentsCount: 64, AttitudesCount: 1280},
					CanonicalURL: "https://weibo.com/" + weiboUID + "/5000000000000015",
					CacheKey:     savedWeiboRetweet.ItemKey,
					CachedItem:   &savedWeiboRetweet,
				}
				app.CurrentRoute = ui.RouteWeiboPost
			},
			RequiredLabels: []string{
				"微博详情站点标记",
				"缓存当前微博",
				"微博正文",
				"转发原微博卡片",
				"转发原微博正文",
				"打开原微博",
				"查看图文页 1",
				"实况标记 2",
				"微博视频卡片",
			},
		},
		{
			Name: "weibo-post-retweet-offline",
			Setup: func() {
				app.Opening = nil
				app.OpenLibraryItemDetail(savedWeiboRetweet)
			},
			RequiredLabels: []string{
				"本机微博详情站点标记",
				"本机转发微博标记",
				"微博正文",
				"转发原微博卡片",
				"转发原微博正文",
				"查看图文页 1",
				"实况标记 2",
				"微博视频卡片",
			},
			ForbiddenLabels: []string{
				"缓存当前微博",
				"打开原微博",
				"微博在线统计",
			},
		},
		{
			Name: "home-weibo-retweet",
			Setup: func() {
				app.ImageViewer.Open = false
				app.PlayerOverlay.Open = false
				app.DeleteConfirm.Open = false
				app.Navigate(ui.RouteHome)
				seedRecentAndLive()
				app.FeedSiteFilter = "weibo"
				app.FeedItems = []library.FeedEntry{
					{
						FeedKey:      savedWeiboRetweet.ItemKey,
						ItemKey:      savedWeiboRetweet.ItemKey,
						Site:         "weibo",
						Kind:         "dynamic",
						RawID:        savedWeiboRetweet.RawID,
						AuthorUID:    weiboUID,
						AuthorName:   "技术频道_微博",
						Title:        savedWeiboRetweet.Title,
						Summary:      savedWeiboRetweet.Text,
						CanonicalURL: savedWeiboRetweet.URL,
						PubTs:        savedWeiboRetweet.PubTs,
						Cached:       true,
						CacheStatus:  "green",
					},
					{
						FeedKey:      savedWeiboPost.ItemKey,
						ItemKey:      savedWeiboPost.ItemKey,
						Site:         "weibo",
						Kind:         "dynamic",
						RawID:        savedWeiboPost.RawID,
						AuthorUID:    weiboUID,
						AuthorName:   "技术频道_微博",
						Title:        savedWeiboPost.Title,
						Summary:      savedWeiboPost.Text,
						CanonicalURL: savedWeiboPost.URL,
						PubTs:        savedWeiboPost.PubTs,
						Cached:       true,
						CacheStatus:  "green",
					},
				}
				app.FeedListState = &mygoui.ListState{}
			},
			RequiredLabels: []string{
				"主页站点筛选：微博",
				"主页作者站点：" + savedWeiboRetweet.ItemKey,
				"转发原内容：" + savedWeiboRetweet.ItemKey,
				"查看图文页 1",
				"实况标记 2",
				"播放原视频卡片：" + savedWeiboRetweet.ItemKey,
			},
		},
		{
			Name: "x-user",
			Setup: func() {
				app.Opening = nil
				app.CurrentLibraryDetail = nil
				app.UserListFilter = "all"
				app.CurrentXUser = &service.XUserDTO{
					RestID:         xUID,
					ScreenName:     xHandle,
					Name:           "技术频道_X",
					Description:    "系统工程、纯 Go 桌面架构与四站点离线归档笔记",
					IsBlueVerified: true,
					FollowersCount: 128400,
					FriendsCount:   95,
					StatusesCount:  3240,
					CanonicalURL:   "https://x.com/" + xHandle,
				}
				app.CurrentXPosts = []service.XPostDTO{
					{
						ID:           "1000000000000000009",
						Author:       service.XAuthorDTO{RestID: xUID, ScreenName: xHandle, Name: "技术频道_X"},
						CreatedAt:    1710004600,
						Text:         "置顶公告：本账号整理系统编程、多媒体封装与跨平台离线归档实践，支持四站点独立凭证与限流退避。",
						Pinned:       true,
						CanonicalURL: "https://x.com/" + xHandle + "/status/1000000000000000009",
						CacheKey:     savedXPost.ItemKey,
					},
					{
						ID:           "1000000000000000010",
						Author:       service.XAuthorDTO{RestID: xUID, ScreenName: xHandle, Name: "技术频道_X"},
						CreatedAt:    1710004200,
						Text:         "时间线翻页与去重说明：沿用游标分页并在到达平台可见上限时给出明确提示。",
						CanonicalURL: "https://x.com/" + xHandle + "/status/1000000000000000010",
						CacheKey:     "x:dynamic:1000000000000000010",
					},
				}
				app.XPostsHasMore = false
				app.XPostsError = ""
				app.XPostsAuthExpired = false
				app.XPostsUncertainEmpty = false
				app.XBottomTipsVisible = true
				app.XBottomTipsText = "已到达 X 时间线可见上限"
				app.XUserSubMenuOpen = false
				app.XUserListState = &mygoui.ListState{}
				app.CurrentRoute = ui.RouteXUser
			},
			RequiredLabels: []string{
				"用户页站点标记",
				"X认证标记",
				"X帖子总数",
				"X主页更多",
				"用户页筛选：全部",
				"用户页筛选：未缓存",
				"X用户列表",
				"X时间线可见上限提示",
			},
			RequiredTexts: []string{
				"@" + xHandle,
				"置顶",
				"已到达 X 时间线可见上限",
			},
		},
		{
			Name: "x-user-protected",
			Setup: func() {
				app.Opening = nil
				app.CurrentLibraryDetail = nil
				app.UserListFilter = "all"
				app.CurrentXUser = &service.XUserDTO{
					RestID:         "1000000000000000002",
					ScreenName:     "protected_synth",
					Name:           "受保护合成账号",
					Description:    "仅限已获批准的关注者查看帖子",
					Protected:      true,
					FollowersCount: 42,
					FriendsCount:   15,
					StatusesCount:  18,
					CanonicalURL:   "https://x.com/protected_synth",
				}
				app.CurrentXPosts = nil
				app.XPostsHasMore = false
				app.XPostsError = ""
				app.XPostsAuthExpired = false
				app.XPostsUncertainEmpty = false
				app.XBottomTipsVisible = false
				app.XBottomTipsText = ""
				app.XUserSubMenuOpen = false
				app.XUserListState = &mygoui.ListState{}
				app.CurrentRoute = ui.RouteXUser
			},
			RequiredLabels: []string{
				"用户页站点标记",
				"X受保护账号提示",
				"X用户列表",
			},
			RequiredTexts: []string{
				"@protected_synth",
				"受保护的账号",
				"该账号帖子受保护，当前不可查看",
			},
			ForbiddenLabels: []string{
				"X空壳提示",
				"X时间线可见上限提示",
			},
		},
		{
			Name: "x-user-empty-shell",
			Setup: func() {
				app.Opening = nil
				app.CurrentLibraryDetail = nil
				app.UserListFilter = "all"
				app.CurrentXUser = &service.XUserDTO{
					RestID:         "1000000000000000003",
					ScreenName:     "limited_synth",
					Name:           "风控测试X账号",
					Description:    "用于验证假空壳/风控拦截错误与原地重试按钮",
					FollowersCount: 3100,
					FriendsCount:   40,
					StatusesCount:  256,
					CanonicalURL:   "https://x.com/limited_synth",
				}
				app.CurrentXPosts = nil
				app.XPostsHasMore = false
				app.XPostsAuthExpired = false
				app.XPostsUncertainEmpty = true
				app.XPostsError = "X 返回了异常空列表（账号标注有 256 条帖子），可能是风控或限流，可稍后重试"
				app.XBottomTipsVisible = false
				app.XBottomTipsText = ""
				app.XUserSubMenuOpen = false
				app.XUserListState = &mygoui.ListState{}
				app.CurrentRoute = ui.RouteXUser
			},
			RequiredLabels: []string{
				"用户页站点标记",
				"X空壳提示",
				"重试加载X帖子",
			},
			RequiredTexts: []string{
				"@limited_synth",
				"异常空列表",
			},
			ForbiddenLabels: []string{
				"X时间线可见上限提示",
				"X受保护账号提示",
			},
		},
		{
			Name: "x-post",
			Setup: func() {
				app.Opening = nil
				app.CurrentLibraryDetail = nil
				app.CurrentXPost = &service.XPostDTO{
					ID:              "1000000000000000011",
					Author:          service.XAuthorDTO{RestID: xUID, ScreenName: xHandle, Name: "技术频道_X"},
					CreatedAt:       1710004800,
					Text:            "长帖 note_tweet 正文示例：完整展开超过 280 字符的长文内容，不截断末尾段落，同时按原始顺序展示最多 4 项混排附件（含原图、静音循环 GIF 与视频页）。",
					IsLongText:      true,
					IsEdited:        true,
					VisibilityLabel: "仅关注者可回复",
					Media: []service.XMediaItemDTO{
						{Index: 1, Kind: "photo", ImageURL: "https://pbs.twimg.com/media/Synthetic01?format=jpg&name=orig", Width: 1200, Height: 900},
						{Index: 2, Kind: "gif", GIF: true, ImageURL: "https://pbs.twimg.com/tweet_video_thumb/SyntheticGif02.jpg", VideoURL: "https://video.twimg.com/tweet_video/SyntheticGif02.mp4", Width: 640, Height: 360},
						{Index: 3, Kind: "video", ImageURL: "https://pbs.twimg.com/ext_tw_video_thumb/SyntheticVid03.jpg", VideoURL: "https://video.twimg.com/ext_tw_video/1000000000000000011/pu/vid/avc1/1280x720/synth.mp4", DurationSec: 42, Width: 1280, Height: 720},
						{Index: 4, Kind: "photo", ImageURL: "https://pbs.twimg.com/media/Synthetic04?format=jpg&name=orig", Width: 1080, Height: 1080},
					},
					Stats:        service.XStatsDTO{ReplyCount: 18, RetweetCount: 64, LikeCount: 920, QuoteCount: 9, BookmarkCount: 145, ViewCount: 24800},
					CanonicalURL: "https://x.com/" + xHandle + "/status/1000000000000000011",
					CacheKey:     "x:dynamic:1000000000000000011",
				}
				app.CurrentRoute = ui.RouteXPost
			},
			RequiredLabels: []string{
				"X详情站点标记",
				"X已编辑标记",
				"X可见性说明",
				"缓存当前X帖子",
				"X帖子正文",
				"查看图文页 1",
				"动图标记 2",
				"视频页标记 3",
				"查看图文页 4",
			},
			RequiredTexts: []string{
				"@" + xHandle,
				"note_tweet",
				"GIF",
			},
		},
		{
			Name: "x-post-thread",
			Setup: func() {
				app.Opening = nil
				app.CurrentLibraryDetail = nil
				app.CurrentXPost = &service.XPostDTO{
					ID:         "1000000000000000012",
					Author:     service.XAuthorDTO{RestID: xUID, ScreenName: xHandle, Name: "技术频道_X"},
					CreatedAt:  1710004900,
					Text:       "长帖线程主帖（note_tweet）：第 1 条详细阐述四站点独立凭证隔离、前端元数据运行时发现与 64 位雪花 ID 校验方案，后续自回复紧随其后一并展示并缓存。",
					IsLongText: true,
					Thread: []service.XPostDTO{
						{
							ID:           "1000000000000000013",
							Author:       service.XAuthorDTO{RestID: xUID, ScreenName: xHandle, Name: "技术频道_X"},
							CreatedAt:    1710004960,
							Text:         "自回复 #2：前端元数据仅驻留内存、单飞刷新且 10 分钟冷却，abs.twimg.com 请求恒不带 Cookie。",
							CanonicalURL: "https://x.com/" + xHandle + "/status/1000000000000000013",
						},
						{
							ID:           "1000000000000000014",
							Author:       service.XAuthorDTO{RestID: xUID, ScreenName: xHandle, Name: "技术频道_X"},
							CreatedAt:    1710005020,
							Text:         "自回复 #3：缓存主帖时将此刻的作者自回复串一并写入同一条离线清单，避免碎片化条目。",
							CanonicalURL: "https://x.com/" + xHandle + "/status/1000000000000000014",
						},
					},
					Stats:        service.XStatsDTO{ReplyCount: 25, RetweetCount: 80, LikeCount: 1120, QuoteCount: 14, BookmarkCount: 210, ViewCount: 38200},
					CanonicalURL: "https://x.com/" + xHandle + "/status/1000000000000000012",
					CacheKey:     "x:dynamic:1000000000000000012",
				}
				app.CurrentRoute = ui.RouteXPost
			},
			RequiredLabels: []string{
				"X详情站点标记",
				"缓存当前X帖子",
				"X帖子正文",
				"X作者自回复串",
				"X串条目：1000000000000000013",
				"查看X串帖子：1000000000000000013",
				"X串条目：1000000000000000014",
				"X在线统计",
			},
			RequiredTexts: []string{
				"作者自回复串（2 条）",
				"自回复 #2",
				"自回复 #3",
			},
		},
		{
			Name: "x-post-retweet-deleted",
			Setup: func() {
				app.Opening = nil
				app.CurrentLibraryDetail = nil
				app.CurrentXPost = &service.XPostDTO{
					ID:        "1000000000000000017",
					Author:    service.XAuthorDTO{RestID: xUID, ScreenName: xHandle, Name: "技术频道_X"},
					CreatedAt: 1710005050,
					Text:      "转发附言：当原帖已被作者删除、受保护或地区屏蔽时，保留外层转发内容并安全降级展示不可用提示。",
					IsRetweet: true,
					Retweeted: &service.XPostDTO{
						ID:                "1000000000000000099",
						Unavailable:       true,
						UnavailableReason: "原帖已被作者删除或不可查看",
					},
					Stats:        service.XStatsDTO{ReplyCount: 6, RetweetCount: 19, LikeCount: 240, ViewCount: 5400},
					CanonicalURL: "https://x.com/" + xHandle + "/status/1000000000000000017",
					CacheKey:     "x:dynamic:1000000000000000017",
				}
				app.CurrentRoute = ui.RouteXPost
			},
			RequiredLabels: []string{
				"X详情站点标记",
				"X帖子正文",
				"X原帖不可用提示",
				"X在线统计",
			},
			RequiredTexts: []string{
				"原帖已被作者删除或不可查看",
			},
		},
		{
			Name: "x-post-quote",
			Setup: func() {
				app.Opening = nil
				app.CurrentLibraryDetail = nil
				app.CurrentXPost = &service.XPostDTO{
					ID:        "1000000000000000018",
					Author:    service.XAuthorDTO{RestID: xUID, ScreenName: xHandle, Name: "技术频道_X"},
					CreatedAt: 1710005100,
					Text:      "引用评论：这套纯标准库 MP4 重排与零 Cookie 媒体白名单设计非常克制，适合长期本地归档。",
					IsQuote:   true,
					Quoted: &service.XPostDTO{
						ID:           "1000000000000000088",
						Author:       service.XAuthorDTO{RestID: "1000000000000000004", ScreenName: "synth_x_dev", Name: "合成X开发者"},
						CreatedAt:    1710004100,
						Text:         "被引用的原帖正文：支持原图、静音循环 GIF 与最高码率 MP4，本地离线随时回看。",
						CanonicalURL: "https://x.com/synth_x_dev/status/1000000000000000088",
						Media: []service.XMediaItemDTO{
							{Index: 1, Kind: "photo", ImageURL: "https://pbs.twimg.com/media/SyntheticQuote01?format=jpg&name=orig", Width: 1200, Height: 675},
						},
					},
					Stats:        service.XStatsDTO{ReplyCount: 11, RetweetCount: 45, LikeCount: 680, QuoteCount: 7, BookmarkCount: 92, ViewCount: 19400},
					CanonicalURL: "https://x.com/" + xHandle + "/status/1000000000000000018",
					CacheKey:     "x:dynamic:1000000000000000018",
				}
				app.CurrentRoute = ui.RouteXPost
			},
			RequiredLabels: []string{
				"X详情站点标记",
				"缓存当前X帖子",
				"X帖子正文",
				"X原帖卡片",
				"打开X原帖：1000000000000000088",
				"X在线统计",
			},
			RequiredTexts: []string{
				"@synth_x_dev",
				"引用帖子",
			},
		},
		{
			Name: "x-post-video",
			Setup: func() {
				app.Opening = nil
				app.CurrentLibraryDetail = nil
				app.CurrentXPost = &service.XPostDTO{
					ID:        "1000000000000000019",
					Author:    service.XAuthorDTO{RestID: xUID, ScreenName: xHandle, Name: "技术频道_X"},
					CreatedAt: 1710005150,
					Text:      "单视频帖示例：自动挑选最高码率单文件 MP4，CDN 请求不带 Cookie 与 Referer。",
					Media: []service.XMediaItemDTO{
						{
							Index:        1,
							Kind:         "video",
							ImageURL:     "https://pbs.twimg.com/ext_tw_video_thumb/SyntheticVid19.jpg",
							VideoURL:     "https://video.twimg.com/ext_tw_video/1000000000000000019/pu/vid/avc1/1920x1080/synth.mp4",
							DurationSec:  128,
							Width:        1920,
							Height:       1080,
							VideoBitrate: 5120000,
							QualityLabel: "1080P",
						},
					},
					Stats:        service.XStatsDTO{ReplyCount: 34, RetweetCount: 120, LikeCount: 2450, BookmarkCount: 310, ViewCount: 64200},
					CanonicalURL: "https://x.com/" + xHandle + "/status/1000000000000000019",
					CacheKey:     "x:dynamic:1000000000000000019",
				}
				app.CurrentRoute = ui.RouteXPost
			},
			RequiredLabels: []string{
				"X详情站点标记",
				"缓存当前X帖子",
				"X视频卡片",
				"X视频规格",
				"X在线统计",
			},
			RequiredTexts: []string{
				"X 视频",
				"单视频帖示例",
			},
		},
		{
			Name: "x-post-cards",
			Setup: func() {
				app.Opening = nil
				app.CurrentLibraryDetail = nil
				app.CurrentXPost = &service.XPostDTO{
					ID:        "1000000000000000020",
					Author:    service.XAuthorDTO{RestID: xUID, ScreenName: xHandle, Name: "技术频道_X"},
					CreatedAt: 1710005200,
					Text:      "投票、X Articles 与外链卡片安全降级示例：只展示摘要与“在浏览器中打开”，不出现视频缓存入口。",
					Poll: &service.XPollSummaryDTO{
						Options: []service.XPollOptionDTO{
							{Position: 1, Label: "纯标准库协议客户端", Votes: 780},
							{Position: 2, Label: "第三方重依赖框架", Votes: 220},
						},
						EndTime:    "2026-11-23 12:00 UTC",
						Final:      true,
						TotalVotes: 1000,
					},
					Card: &service.XRichCardDTO{
						Kind:        "article",
						Label:       "X Articles",
						Title:       "桌面端多站点离线媒体库与凭证隔离实践",
						Desc:        "探讨四站点独立凭证加密、限流刹车与前端元数据运行时发现。",
						URL:         "https://x.com/i/article/1000000000000000020",
						Unsupported: true,
					},
					Quoted: &service.XPostDTO{
						ID:           "1000000000000000089",
						Author:       service.XAuthorDTO{RestID: "1000000000000000004", ScreenName: "synth_x_dev", Name: "合成X开发者"},
						CreatedAt:    1710004500,
						Text:         "附外链卡片摘要示例。",
						CanonicalURL: "https://x.com/synth_x_dev/status/1000000000000000089",
						Card: &service.XRichCardDTO{
							Kind:  "webpage",
							Label: "链接卡片",
							Title: "开放标准与协议说明文档",
							Desc:  "示例外链预览卡片，仅在浏览器中打开。",
							URL:   "https://example.invalid/trace-spec",
						},
					},
					Stats:        service.XStatsDTO{ReplyCount: 15, RetweetCount: 42, LikeCount: 510, ViewCount: 11200},
					CanonicalURL: "https://x.com/" + xHandle + "/status/1000000000000000020",
					CacheKey:     "x:dynamic:1000000000000000020",
				}
				app.CurrentRoute = ui.RouteXPost
			},
			RequiredLabels: []string{
				"X详情站点标记",
				"X投票卡片",
				"X投票浏览器打开",
				"X卡片：article",
				"X卡片浏览器打开：article",
				"X卡片：webpage",
				"X卡片浏览器打开：webpage",
				"X在线统计",
			},
			RequiredTexts: []string{
				"纯标准库协议客户端",
				"X Articles",
				"链接卡片",
			},
			ForbiddenLabels: []string{
				"缓存当前X帖子",
			},
		},
		{
			Name: "x-post-live-card",
			Setup: func() {
				app.Opening = nil
				app.CurrentLibraryDetail = nil
				app.CurrentXPost = &service.XPostDTO{
					ID:        "1000000000000000025",
					Author:    service.XAuthorDTO{RestID: xUID, ScreenName: xHandle, Name: "技术频道_X"},
					CreatedAt: 1710005250,
					Text:      "正在开启技术架构交流 Spaces，点击下方卡片可在浏览器中收听。",
					Card: &service.XRichCardDTO{
						Kind:        "live",
						Label:       "直播 / Spaces",
						Title:       "合成技术交流 Spaces 语音间",
						Desc:        "讨论四站点只读协议隔离与离线归档策略。",
						URL:         "https://x.com/i/spaces/1SyntheticSpaceId",
						Unsupported: true,
					},
					Stats:        service.XStatsDTO{ReplyCount: 9, RetweetCount: 22, LikeCount: 310, ViewCount: 8200},
					CanonicalURL: "https://x.com/" + xHandle + "/status/1000000000000000025",
					CacheKey:     "x:dynamic:1000000000000000025",
				}
				app.CurrentRoute = ui.RouteXPost
			},
			RequiredLabels: []string{
				"X详情站点标记",
				"X卡片：live",
				"X卡片浏览器打开：live",
				"X在线统计",
			},
			RequiredTexts: []string{
				"直播 / Spaces",
				"X 直播与 Spaces 暂不支持录制，可以在浏览器中打开。",
			},
			ForbiddenLabels: []string{
				"缓存当前X帖子",
			},
		},
		{
			Name: "x-post-offline",
			Setup: func() {
				app.Opening = nil
				app.OpenLibraryItemDetail(savedXRetweet)
			},
			RequiredLabels: []string{
				"本机详情站点标记",
				"X离线正文",
				"X离线缺失提示：1",
				"X离线原帖卡片",
				"X作者自回复串",
				"X离线串条目：1",
			},
			RequiredTexts: []string{
				"@" + xHandle,
				"这条视频只有分段流，暂不支持缓存",
				"@synth_x_dev",
				"自回复补充",
			},
			ForbiddenLabels: []string{
				"缓存当前X帖子",
				"X在线统计",
			},
		},
		{
			Name: "home-x",
			Setup: func() {
				app.ImageViewer.Open = false
				app.PlayerOverlay.Open = false
				app.DeleteConfirm.Open = false
				app.Navigate(ui.RouteHome)
				seedRecentAndLive()
				app.FeedSiteFilter = "x"
				app.FeedItems = []library.FeedEntry{
					{
						FeedKey:      savedXRetweet.ItemKey,
						ItemKey:      savedXRetweet.ItemKey,
						Site:         "x",
						Kind:         "dynamic",
						RawID:        savedXRetweet.RawID,
						AuthorUID:    xUID,
						AuthorName:   "技术频道_X",
						Title:        savedXRetweet.Title,
						Summary:      savedXRetweet.Text,
						CanonicalURL: savedXRetweet.URL,
						PubTs:        savedXRetweet.PubTs,
						Cached:       true,
						CacheStatus:  "yellow",
					},
					{
						FeedKey:      savedXPost.ItemKey,
						ItemKey:      savedXPost.ItemKey,
						Site:         "x",
						Kind:         "dynamic",
						RawID:        savedXPost.RawID,
						AuthorUID:    xOnlyUID,
						AuthorName:   "X 合成专栏",
						Title:        savedXPost.Title,
						Summary:      savedXPost.Text,
						CanonicalURL: savedXPost.URL,
						PubTs:        savedXPost.PubTs,
						Cached:       true,
						CacheStatus:  "green",
					},
				}
				app.FeedListState = &mygoui.ListState{}
			},
			RequiredLabels: []string{
				"主页站点筛选：X",
				"主页作者站点：" + savedXRetweet.ItemKey,
				"转发原内容：" + savedXRetweet.ItemKey,
				"查看图文页 1",
				"动图标记 2",
			},
		},
		{
			Name: "library",
			Setup: func() {
				app.DeleteConfirm.Open = false
				app.Transfer.ExportOpen = false
				app.LibrarySelectMode = false
				app.LibrarySelectedKeys = map[string]bool{}
				app.LibrarySourceSortBySize = false
				app.LibrarySiteFilter = "all"
				app.Navigate(ui.RouteLibrary)
				app.ExpandedItemKey = savedDynamicItem.ItemKey
			},
			RequiredLabels: []string{
				"媒体库多选",
				"媒体库站点筛选：全部",
				"媒体库站点筛选：B 站",
				"媒体库站点筛选：抖音",
				"媒体库站点筛选：微博",
				"媒体库站点筛选：X",
				"媒体更多：" + savedDynamicItem.ItemKey,
				"媒体库站点：" + savedDynamicItem.ItemKey,
				"媒体库站点：" + savedDouyinVideo.ItemKey,
				"媒体库站点：" + savedWeiboPost.ItemKey,
				"媒体库站点：" + savedXPost.ItemKey,
			},
			PanelRequiredLabels: []string{"媒体库来源按占用空间排序"},
		},
		{
			Name: "library-multiselect",
			Setup: func() {
				app.DeleteConfirm.Open = false
				app.Transfer.ExportOpen = false
				app.LibrarySiteFilter = "all"
				app.LibrarySourceSortBySize = true
				app.Navigate(ui.RouteLibrary)
				app.ExpandedItemKey = ""
				app.LibrarySelectMode = true
				app.LibrarySelectedKeys = map[string]bool{
					savedDynamicItem.ItemKey: true,
					savedDouyinVideo.ItemKey: true,
				}
			},
			RequiredLabels: []string{
				"媒体库多选",
				"媒体库批量操作栏",
				"媒体库批量导出",
				"媒体库批量删除",
				"退出多选",
				"已选媒体：" + savedDynamicItem.ItemKey,
				"已选媒体：" + savedDouyinVideo.ItemKey,
			},
			PanelRequiredLabels: []string{"媒体库来源按占用空间排序"},
			ForbiddenLabels:     []string{"媒体更多：" + savedDynamicItem.ItemKey},
		},
		{
			Name: "library-delete",
			Setup: func() {
				app.Transfer.ExportOpen = false
				app.LibrarySelectMode = false
				app.LibrarySelectedKeys = map[string]bool{}
				app.LibrarySourceSortBySize = false
				app.Navigate(ui.RouteLibrary)
				app.ExpandedItemKey = ""
				plan, _ := b.PlanDelete(savedItem.ItemKey)
				app.DeleteConfirm = ui.DeleteConfirmState{
					Open:        true,
					Item:        savedItem,
					TargetKeys:  plan.TargetKeys,
					Plan:        plan,
					FileCount:   len(plan.RegisteredFiles),
					TotalBytes:  savedItem.TotalBytes,
					DeleteFiles: true,
				}
			},
		},
		{
			Name: "library-batch-delete",
			Setup: func() {
				app.Transfer.ExportOpen = false
				app.LibrarySiteFilter = "all"
				app.LibrarySourceSortBySize = true
				app.Navigate(ui.RouteLibrary)
				app.ExpandedItemKey = ""
				app.LibrarySelectMode = true
				app.LibrarySelectedKeys = map[string]bool{
					savedDynamicItem.ItemKey: true,
					savedDouyinVideo.ItemKey: true,
				}
				plan, _ := b.PlanDeleteBatch([]string{savedDynamicItem.ItemKey, savedDouyinVideo.ItemKey})
				var totalBytes int64
				for _, k := range plan.ItemKeys {
					if it, ok, err := b.GetLibraryItemMetaOnly(k); err == nil && ok {
						totalBytes += it.TotalBytes
					}
				}
				app.DeleteConfirm = ui.DeleteConfirmState{
					Open:        true,
					Batch:       true,
					TargetKeys:  plan.TargetKeys,
					Plan:        plan,
					FileCount:   len(plan.RegisteredFiles),
					TotalBytes:  totalBytes,
					DeleteFiles: true,
				}
			},
			RequiredLabels: []string{"删除确认对话框", "确认删除", "取消删除"},
			RequiredTexts:  []string{"批量删除缓存确认", "已选择 2 个媒体库条目"},
		},
		{
			Name: "library-batch-export",
			Setup: func() {
				app.DeleteConfirm.Open = false
				app.LibrarySiteFilter = "all"
				app.LibrarySourceSortBySize = true
				app.Navigate(ui.RouteLibrary)
				app.ExpandedItemKey = ""
				app.LibrarySelectMode = true
				app.LibrarySelectedKeys = map[string]bool{
					savedDynamicItem.ItemKey: true,
					savedDouyinVideo.ItemKey: true,
				}
				app.Transfer.ExportOpen = true
				app.Transfer.ExportScope = "selected"
				app.Transfer.ExportPath = `E:\trace-export-selected`
				app.Transfer.ExportErr = ""
			},
			RequiredLabels: []string{"导出媒体库对话框", "导出范围", "选择导出文件夹", "开始导出"},
			RequiredTexts:  []string{"导出媒体库", "已选 2 项"},
		},
		{
			Name: "tasks",
			Setup: func() {
				app.DeleteConfirm.Open = false
				app.Transfer.ExportOpen = false
				app.LibrarySelectMode = false
				app.LibrarySelectedKeys = map[string]bool{}
				app.LibrarySourceSortBySize = false
				seedRecentAndLive()
				app.Navigate(ui.RouteTasks)
				app.TasksSection = ui.TasksSectionActive
				app.TasksActiveTab = "downloads"
				app.Tasks = []download.Task{
					{
						TaskID:          "task_demo_x",
						Site:            "x",
						Kind:            "dynamic",
						TargetID:        "1000000000000000019",
						Title:           "正在缓存：X 1080P 单文件 MP4 与长帖线程归档",
						Status:          "running",
						QualityHint:     "1080P · 5120 kbps",
						BytesDownloaded: 3145728,
						TotalBytes:      6291456,
						Progress:        0.50,
					},
					{
						TaskID:          "task_demo_wb",
						Site:            "weibo",
						Kind:            "dynamic",
						TargetID:        "5000000000000011",
						Title:           "正在缓存：4K 航拍云海与纯 Go 容器重排演示",
						Status:          "running",
						QualityHint:     "1080P · HEVC",
						BytesDownloaded: 4194304,
						TotalBytes:      8388608,
						Progress:        0.50,
					},
					{
						TaskID:          "task_demo_dy",
						Site:            "douyin",
						Kind:            "video",
						TargetID:        "7388001001",
						Title:           "正在缓存：竖屏高原雪山延时摄影记录",
						Status:          "running",
						QualityHint:     "1080P · HEVC",
						BytesDownloaded: 5242880,
						TotalBytes:      8388608,
						Progress:        0.62,
					},
					{
						TaskID:          "task_demo_run",
						Site:            "bilibili",
						Kind:            "video",
						TargetID:        "BV1run411c7mA",
						Title:           "正在缓存：渲染管线深度剖析与实测",
						Status:          "running",
						QualityHint:     "4K 超清 (120) · AVC + Hi-Res 无损 (30251)",
						BytesDownloaded: 3774873,
						TotalBytes:      8388608,
						Progress:        0.45,
					},
					{
						TaskID:          "task_demo_fail",
						Site:            "bilibili",
						Kind:            "video",
						TargetID:        "BV1yy411c7mE",
						Title:           "示例分段失败任务（带原因与移除确认）",
						Status:          "failed",
						ErrorKind:       "network",
						ErrorMessage:    "远端分段读取超时，已保留 .part 断点文件可重试",
						LastError:       "远端分段读取超时，已保留 .part 断点文件可重试",
						BytesDownloaded: 2097152,
						TotalBytes:      8388608,
						Progress:        0.25,
					},
				}
				app.TaskRemoveConfirmID = "task_demo_fail"
				app.ExpandedTaskID = ""
			},
			RequiredLabels: []string{
				"进行中子标签：下载任务",
				"进行中子标签：缓存队列",
				"任务站点：task_demo_x",
				"任务站点：task_demo_wb",
				"任务站点：task_demo_dy",
				"任务站点：task_demo_run",
			},
			PanelRequiredLabels: []string{
				"任务侧栏：进行中",
				"任务侧栏：直播",
				"任务侧栏：待处理",
				"任务侧栏：记录",
			},
			NarrowRequiredLabels: []string{
				"任务分段：进行中",
				"任务分段：直播",
				"任务分段：待处理",
				"任务分段：记录",
			},
			ForbiddenLabels: []string{
				"录制站点：bilibili:9002",
				"任务侧栏：检查计划",
			},
		},
		{
			Name: "tasks-queue",
			Setup: func() {
				seedRecentAndLive()
				app.OpenCacheQueueTask("cq_demo_bili")
			},
			RequiredLabels: []string{
				"进行中子标签：下载任务",
				"进行中子标签：缓存队列",
				"缓存队列卡片：cq_demo_bili",
				"缓存队列卡片：cq_demo_dy",
				"缓存队列卡片：cq_demo_wb",
				"缓存队列卡片：cq_demo_x",
				"展开缓存队列：cq_demo_bili",
				"队列详情站点：cq_demo_bili",
				"队列详情站点：cq_demo_wb",
				"队列详情站点：cq_demo_x",
				"队列详情计数：cq_demo_bili",
				"暂停缓存队列：cq_demo_bili",
				"取消缓存队列：cq_demo_bili",
				"队列最近缓存：" + savedItem.ItemKey,
			},
			PanelRequiredLabels: []string{
				"任务侧栏：进行中",
				"任务侧栏：直播",
				"任务侧栏：待处理",
				"任务侧栏：记录",
			},
		},
		{
			Name: "tasks-live",
			Setup: func() {
				seedRecentAndLive()
				app.Navigate(ui.RouteTasks)
				app.ExpandedLiveIncidentKeys = map[string]bool{}
				app.ExpandLiveRowIncidents("bilibili:live:9002")
				_ = app.OpenLivePreviewWithDecoder(
					"bilibili:live:9002",
					"独立游戏工坊 · 渲染管线与着色器实时调试",
					filepath.Join(env.tmpRoot, "part-001.mp4.part"),
					1,
					player.NewSynthLiveDecoder(640, 360, 30, nil),
				)
			},
			RequiredLabels: []string{
				"录制站点：bilibili:9002",
				"录制站点：douyin:" + douyinSecUID,
				"录制行异常：bilibili:live:9002",
				"行内录制异常列表：bilibili:live:9002",
				"行内异常条目：bilibili:live:9002#0",
				"录制行异常：douyin:live:73990011",
				"直播实时预览面板",
				"直播实时预览标记",
				"直播实时预览画面",
				"关闭直播实时预览",
			},
			PanelRequiredLabels: []string{
				"任务侧栏：进行中",
				"任务侧栏：直播",
				"任务侧栏：待处理",
				"任务侧栏：记录",
			},
			NarrowRequiredLabels: []string{
				"任务分段：进行中",
				"任务分段：直播",
				"任务分段：待处理",
				"任务分段：记录",
			},
			ForbiddenLabels: []string{
				"弹幕开关",
				"行内删除录制异常记录：bilibili:live:9002",
			},
		},
		{
			Name: "tasks-attention",
			Setup: func() {
				seedRecentAndLive()
				app.ConflictBatchChoice = service.DefaultConflictChoice
				app.ConflictBatchConfirmOpen = false
				app.ConflictReplaceConfirmID = ""
				app.ConflictActionError = ""
				app.TaskActionError = ""
				app.TaskRemoveConfirmID = ""
				app.Tasks = []download.Task{
					{
						TaskID:          "task_demo_fail",
						Site:            "bilibili",
						Kind:            "video",
						TargetID:        "BV1yy411c7mE",
						Title:           "示例分段失败任务（待处理汇总展示）",
						Status:          "failed",
						ErrorKind:       "network",
						ErrorMessage:    "远端分段读取超时，已保留 .part 断点文件可重试",
						LastError:       "远端分段读取超时，已保留 .part 断点文件可重试",
						BytesDownloaded: 2097152,
						TotalBytes:      8388608,
						Progress:        0.25,
					},
				}
				app.OpenTasksConflicts()
				if len(app.CacheConflicts) > 1 {
					app.CacheConflicts = app.CacheConflicts[:1]
					app.CacheConflictPendingCount = 1
				}
				app.LiveAccounts = nil
				app.SelectedLiveIncidentKey = "bilibili:live:9002"
			},
			RequiredLabels: []string{
				"待处理分节：内容冲突",
				"内容冲突顶部栏",
				"内容冲突卡片：conflict_shot_bili",
				"待处理分节：失败下载",
				"待处理：批量重试",
				"待处理下载卡片：task_demo_fail",
				"待处理分节：录制异常",
				"录制异常场次：bilibili:live:9002",
				"异常场次站点：bilibili:live:9002",
				"查看异常正在录制：bilibili:live:9002",
			},
			PanelRequiredLabels: []string{
				"任务侧栏：进行中",
				"任务侧栏：直播",
				"任务侧栏：待处理",
				"任务侧栏：记录",
			},
			NarrowRequiredLabels: []string{
				"任务分段：进行中",
				"任务分段：直播",
				"任务分段：待处理",
				"任务分段：记录",
			},
			ForbiddenLabels: []string{
				"待处理空状态",
				"直播实时预览面板",
			},
		},
		{
			Name: "tasks-conflicts",
			Setup: func() {
				seedRecentAndLive()
				app.Tasks = nil
				app.LiveRooms = nil
				app.LiveAccounts = nil
				app.ConflictBatchChoice = service.DefaultConflictChoice
				app.ConflictBatchConfirmOpen = false
				app.ConflictActionError = ""
				app.OpenTasksConflicts()
				app.ConflictReplaceConfirmID = "conflict_shot_wb"
			},
			RequiredLabels: []string{
				"内容冲突顶部栏",
				"内容冲突保护说明",
				"全部待处理方式",
				"批量应用冲突处理",
				"内容冲突列表",
				"内容冲突卡片：conflict_shot_bili",
				"冲突原因：conflict_shot_bili",
				"冲突站点：conflict_shot_bili",
				"冲突对比：conflict_shot_bili",
				"保留两版：conflict_shot_bili",
				"保留本地：conflict_shot_bili",
				"用新版替换：conflict_shot_bili",
				"查看本地冲突条目：conflict_shot_bili",
				"内容冲突卡片：conflict_shot_wb",
				"冲突原因：conflict_shot_wb",
				"冲突站点：conflict_shot_wb",
				"确认替换冲突栏：conflict_shot_wb",
				"取消替换冲突：conflict_shot_wb",
				"确认替换冲突：conflict_shot_wb",
				"内容冲突卡片：conflict_shot_x",
				"冲突原因：conflict_shot_x",
				"冲突站点：conflict_shot_x",
				"冲突失败提示：conflict_shot_x",
			},
			PanelRequiredLabels: []string{
				"任务侧栏：进行中",
				"任务侧栏：直播",
				"任务侧栏：待处理",
				"任务侧栏：记录",
			},
			NarrowRequiredLabels: []string{
				"任务分段：进行中",
				"任务分段：直播",
				"任务分段：待处理",
				"任务分段：记录",
			},
			RequiredTexts: []string{
				"内容冲突（3）",
				"源站内容与本地已缓存版本不一致时不会自动覆盖，默认可同时保留本地旧版与源站新版",
				"保留两版（推荐）",
				"分P变更",
				"媒体替换",
				"正文编辑",
				"确认用源站新版替换本地已缓存内容？",
				"（本地原文件未改动）",
			},
			ForbiddenLabels: []string{
				"待处理空状态",
			},
		},
		{
			Name: "tasks-conflicts-diff",
			Setup: func() {
				seedRecentAndLive()
				app.ConflictBatchChoice = service.DefaultConflictChoice
				app.ConflictBatchConfirmOpen = false
				app.ConflictReplaceConfirmID = ""
				app.ConflictActionError = ""
				app.OpenTasksConflicts()
				if diff, err := env.backend.InspectCacheConflictDiff(context.Background(), "conflict_shot_bili"); err == nil {
					app.OpenConflictDiffModalWithData(diff)
				}
			},
			RequiredLabels: []string{
				"内容冲突对比弹窗",
				"内容冲突并列栏头",
				"左侧列头：本地已缓存",
				"右侧列头：源站当前",
				"文字差异对比区",
				"图片与视频并行视图区",
				"内容冲突对比底部操作栏",
			},
			RequiredTexts: []string{
				"本地已缓存（旧版 V1）",
				"源站当前（新版 V2）",
				"文字差异对比",
				"图片与视频并列对比",
				"保留两版（推荐）",
			},
		},
		{
			Name: "settings",
			Setup: func() {
				app.Navigate(ui.RouteSettings)
				app.SettingsCategory = "storage"
			},
		},
		{
			Name: "settings-network",
			Setup: func() {
				app.Navigate(ui.RouteSettings)
				app.SettingsCategory = "network"
			},
			RequiredLabels: []string{"代理方式选择", "跟随系统代理说明"},
		},
		{
			Name: "settings-logs",
			Setup: func() {
				app.Navigate(ui.RouteSettings)
				app.SettingsCategory = "logs"
			},
		},
		{
			Name: "login",
			Setup: func() {
				app.LoginSite = "bilibili"
				app.Navigate(ui.RouteLogin)
				app.SavedRoute = ui.RouteVideo
				app.QRBitmap = mygoui.NewBitmap(bilibili.RenderQRRGBA("https://passport.bilibili.com/h5-app/passport/login/scan?qrcode_key=demo", 6))
				app.QRStatusText = "等待扫码（86101）"
			},
			RequiredLabels: []string{"登录站点：哔哩哔哩", "登录站点：抖音", "登录站点：微博", "登录站点：X"},
		},
		{
			Name: "login-douyin",
			Setup: func() {
				app.LoginSite = "douyin"
				app.DouyinCookieInput = ""
				app.Navigate(ui.RouteLogin)
			},
			RequiredLabels: []string{
				"登录站点：哔哩哔哩",
				"登录站点：抖音",
				"登录站点：微博",
				"登录站点：X",
				"抖音已登录摘要",
				"抖音Cookie输入框",
				"验证并登录抖音",
			},
			ForbiddenLabels: []string{"刷新登录二维码"},
		},
		{
			Name: "login-weibo",
			Setup: func() {
				app.LoginSite = "weibo"
				app.WeiboCookieInput = ""
				app.Navigate(ui.RouteLogin)
			},
			RequiredLabels: []string{
				"登录站点：哔哩哔哩",
				"登录站点：抖音",
				"登录站点：微博",
				"登录站点：X",
				"微博已登录摘要",
				"微博Cookie输入框",
				"验证并登录微博",
			},
			ForbiddenLabels: []string{"刷新登录二维码"},
		},
		{
			Name: "login-x",
			Setup: func() {
				app.LoginSite = "x"
				app.XCookieInput = ""
				app.LoginError = ""
				app.SavedRoute = ""
				app.Navigate(ui.RouteLogin)
			},
			RequiredLabels: []string{
				"登录站点：哔哩哔哩",
				"登录站点：抖音",
				"登录站点：微博",
				"登录站点：X",
				"X已登录摘要",
				"XCookie输入框",
				"X登录风险提示",
				"验证并登录X",
			},
			RequiredTexts: []string{
				"登录 X（原 Twitter）",
				"使用前请注意",
				"不是官方开放 API",
				"服务条款",
				"限流",
				"临时锁号",
				"封号",
				"专门账号",
				"只读不写",
				"不保存账号密码",
				"自动续期",
			},
			ForbiddenLabels: []string{"刷新登录二维码"},
		},
		{
			Name: "image-viewer",
			Setup: func() {
				app.Navigate(ui.RouteHome)
				app.OpenItemImageViewer(savedItem.ItemKey, []string{"https://i0.hdslb.com/bfs/archive/sample.jpg"}, []string{"cover.png"}, 0)
			},
		},
		{
			Name: "image-viewer-live",
			Setup: func() {
				app.Navigate(ui.RouteHome)
				app.OpenItemImageViewer(savedDynamicItem.ItemKey, nil, []string{"image-1.png", "image-2.png", "image-3.png"}, 0)
			},
			RequiredLabels: []string{"看图播放实况", "关闭看图"},
		},
		{
			// Immersive title bar (DEC-046 ④): window buttons over the page's top-right.
			Name: "library-titlebar",
			Setup: func() {
				app.ImageViewer.Open = false
				app.Navigate(ui.RouteLibrary)
			},
			TitleBar:        mygoui.TitleBar{Height: 32, Right: 138},
			RequiredLabels:  []string{"窗口拖动区", "页面顶部栏", "媒体库更多菜单"},
			ForbiddenLabels: []string{"快速解析输入框", "快速解析提交"},
		},
		{
			Name: "player",
			Setup: func() {
				app.ImageViewer.Open = false
				app.Navigate(ui.RouteLibrary)
				app.OpenPlayerOverlay(savedItem)
			},
		},
		{
			Name: "player-missing-decoder",
			Setup: func() {
				app.ImageViewer.Open = false
				app.Navigate(ui.RouteLibrary)
				app.OpenPlayerOverlay(savedItem)
				app.PlayerOverlay.DecoderMissing = true
				app.PlayerOverlay.MissingReason = "系统缺少 AV1 (av01) 解码器扩展，请点击下方按钮使用系统播放器打开。"
			},
		},
		{
			Name: "player-options",
			Setup: func() {
				app.ImageViewer.Open = false
				app.Navigate(ui.RouteLibrary)
				app.OpenPlayerOverlay(savedItem)
				app.PlayerOverlay.MoreMenuOpen = true
			},
			RequiredLabels: []string{"弹幕开关", "弹幕透明度", "弹幕显示区域", "弹幕密度选择", "字幕语言选择", "用系统播放器打开"},
		},
		{
			Name: "player-volume",
			Setup: func() {
				app.ImageViewer.Open = false
				app.Navigate(ui.RouteLibrary)
				app.OpenPlayerOverlay(savedItem)
				app.PlayerOverlay.VolumeMenuOpen = true
			},
			RequiredLabels: []string{"播放器音量调节", "播放器静音"},
		},
		{
			Name: "player-temp-preview",
			Setup: func() {
				app.ImageViewer.Open = false
				app.Navigate(ui.RouteDouyinWork)
				app.OpenPlayerOverlay(savedItem)
				app.PlayerOverlay.ItemKey = ""
				app.PlayerOverlay.Segments = nil
				app.PlayerOverlay.Chapters = nil
				app.PlayerOverlay.DecoderMissing = false
				app.PlayerOverlay.MissingReason = ""
				app.PlayerOverlay.LoadError = ""
				app.PlayerOverlay.Loading = false
				app.PlayerOverlay.Title = "川西日照金山短视频（未缓存可临时预览）"
				app.PlayerOverlay.PreviewMode = true
				app.PlayerOverlay.PreviewSite = "douyin"
				app.PlayerOverlay.PreviewCacheQueued = false
				app.PlayerOverlay.PreviewOnCache = func(context.Context) error { return nil }
				app.PlayerOverlay.MoreMenuOpen = false
				app.PlayerOverlay.VolumeMenuOpen = false
			},
			RequiredLabels:  []string{"临时预览标记", "临时预览缓存到媒体库", "播放器更多菜单", "关闭播放"},
			ForbiddenLabels: []string{"播放层打开文件夹", "用系统播放器打开", "临时预览错误说明"},
		},
		{
			Name: "player-temp-preview-error",
			Setup: func() {
				app.ImageViewer.Open = false
				app.Navigate(ui.RouteDouyinWork)
				app.OpenPlayerOverlay(savedItem)
				app.PlayerOverlay.ItemKey = ""
				app.PlayerOverlay.MediaPath = ""
				app.PlayerOverlay.Segments = nil
				app.PlayerOverlay.Chapters = nil
				app.PlayerOverlay.DecoderMissing = false
				app.PlayerOverlay.MissingReason = ""
				app.PlayerOverlay.Loading = false
				app.PlayerOverlay.Title = "超长 4K 原片临时预览超限示例"
				app.PlayerOverlay.PreviewMode = true
				app.PlayerOverlay.PreviewSite = "douyin"
				app.PlayerOverlay.PreviewCacheQueued = false
				app.PlayerOverlay.PreviewOnCache = func(context.Context) error { return nil }
				app.PlayerOverlay.LoadError = "无法预览此视频：视频体积超过临时预览上限（128 MiB），请点击「缓存到媒体库」完整下载后观看"
				app.PlayerOverlay.MoreMenuOpen = false
				app.PlayerOverlay.VolumeMenuOpen = false
			},
			RequiredLabels:  []string{"临时预览标记", "临时预览缓存到媒体库", "临时预览错误说明", "预览失败缓存到媒体库"},
			ForbiddenLabels: []string{"播放层打开文件夹", "用系统播放器打开"},
		},
		{
			// "设置 › 更新" of a release build that updates itself.
			Name: "settings-update",
			Setup: func() {
				app.PlayerOverlay.Open = false
				app.PlayerOverlay.PreviewMode = false
				app.PlayerOverlay.PreviewOnCache = nil
				app.PlayerOverlay.VolumeMenuOpen = false
				fixtureBackend.installedDevVer = ""
				fixtureBackend.updateHist = &update.History{
					Version: update.HistorySchemaVersion,
					Checks: []update.CheckRecord{
						{
							At:      "2026-11-09T08:00:00Z",
							Trigger: update.CheckTriggerAuto,
							Result:  update.MsgUpToDate,
						},
						{
							At:      "2026-11-08T08:00:00Z",
							Trigger: update.CheckTriggerManual,
							Result:  "有新版本 0.1.1",
						},
					},
					Updates: []update.UpdateRecord{
						{
							At:          "2026-11-08T08:05:00Z",
							FromVersion: "0.1.0",
							ToVersion:   "0.1.1",
							Method:      update.UpdateMethodAuto,
						},
					},
				}
				app.DraftSettings.Updates.AutoCheck = true
				app.DraftSettings.Updates.AutoDownload = true
				app.BindUpdater(&shotUpdater{st: update.State{
					Enabled: true, Channel: "stable", CurrentVersion: "0.1.1", Phase: update.PhaseIdle,
					LastCheck:       time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC),
					NextCheck:       time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC),
					LastCheckResult: update.MsgUpToDate,
					Message:         update.MsgUpToDate,
					Prefs:           update.Prefs{AutoCheck: true, AutoDownload: true},
				}})
				app.Navigate(ui.RouteSettings)
				app.SettingsCategory = "update"
			},
			RequiredLabels: []string{
				"当前版本",
				"提交与构建时间",
				"更新方式",
				"上次检查",
				"下次检查时间",
				"立即检查按钮",
				"自动检查更新开关",
				"自动下载更新开关",
				"检查记录区域",
				"更新记录区域",
			},
			ForbiddenLabels: []string{
				"本机安装的开发版",
				"检查记录空提示",
				"更新记录空提示",
			},
		},
		{
			// "设置 › 更新" of a local dev build (DEC-051 ①, Q-018 #1).
			Name: "settings-update-dev",
			Setup: func() {
				app.PlayerOverlay.Open = false
				app.PlayerOverlay.VolumeMenuOpen = false
				fixtureBackend.installedDevVer = "0.1.2-dev.202611151001"
				fixtureBackend.updateHist = &update.History{
					Version: update.HistorySchemaVersion,
					Checks:  nil,
					Updates: []update.UpdateRecord{
						{
							At:          "2026-11-15T10:01:00Z",
							FromVersion: "0.1.2-dev.202611150921",
							ToVersion:   "0.1.2-dev.202611151001",
							Method:      update.UpdateMethodDevInstall,
						},
						{
							At:          "2026-11-15T09:21:00Z",
							FromVersion: "0.1.2-dev.202611150644",
							ToVersion:   "0.1.2-dev.202611150921",
							Method:      update.UpdateMethodDevInstall,
						},
					},
				}
				app.BindUpdater(&shotUpdater{st: update.State{
					Enabled:        false,
					Channel:        "dev",
					CurrentVersion: "0.1.2-dev.202611150921",
				}})
				app.Navigate(ui.RouteSettings)
				app.SettingsCategory = "update"
			},
			RequiredLabels: []string{
				"当前版本",
				"提交与构建时间",
				"更新方式",
				"本机安装的开发版",
				"开发版重启生效提示",
				"检查记录区域",
				"检查记录空提示",
				"更新记录区域",
			},
			ForbiddenLabels: []string{
				"立即检查按钮",
				"自动检查更新开关",
				"自动下载更新开关",
				"上次检查",
				"下次检查时间",
				"更新记录空提示",
			},
		},
		{
			// "设置 › 关于" links to "设置 › 更新" for version updates.
			Name: "settings-about",
			Setup: func() {
				app.PlayerOverlay.Open = false
				app.PlayerOverlay.VolumeMenuOpen = false
				fixtureBackend.installedDevVer = ""
				app.BindUpdater(&shotUpdater{st: update.State{
					Enabled: true, Channel: "stable", CurrentVersion: "0.1.1", Phase: update.PhaseIdle,
					LastCheck: time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC), Message: update.MsgUpToDate,
					Prefs: update.Prefs{AutoCheck: true, AutoDownload: true},
				}})
				app.Navigate(ui.RouteSettings)
				app.SettingsCategory = "about"
			},
			RequiredLabels:  []string{"当前版本", "提交与构建时间", "版本与更新见‘更新’"},
			ForbiddenLabels: []string{"立即检查按钮", "自动检查更新开关"},
		},
		{
			// Floating update bubble in the bottom-right corner once an update
			// is ready, with its release notes expanded (DEC-086).
			Name: "update-ready",
			Setup: func() {
				app.ConflictDiff.Open = false
				app.PlayerOverlay.Open = false
				app.PlayerOverlay.VolumeMenuOpen = false
				app.Navigate(ui.RouteHome)
				app.BannerError = ""
				app.BannerLoginSite = ""
				app.BannerInfo = ""
				app.BindUpdater(&shotUpdater{st: update.State{
					Enabled: true, Channel: "stable", CurrentVersion: "0.1.1", Phase: update.PhaseReady,
					Release: &update.Release{Version: "0.1.2", Notes: "- 订阅支持合集\n- 修复直播录制在断线后的分段"},
					Prefs:   update.Prefs{AutoCheck: true, AutoDownload: true},
				}})
				app.UpdateNotesOpen = true
			},
			RequiredLabels: []string{"悬浮通知区", "更新提示", "更新内容", "关闭更新提示"},
		},
		{
			// Stacked error (with login action) and info floating notification
			// bubbles anchored in the bottom-right corner without shifting page layout (DEC-086).
			Name: "floating-notifications",
			Setup: func() {
				app.ConflictDiff.Open = false
				app.PlayerOverlay.Open = false
				app.PlayerOverlay.VolumeMenuOpen = false
				app.BindUpdater(nil)
				app.UpdateNotesOpen = false
				app.Navigate(ui.RouteHome)
				app.BannerError = "抖音登录已失效，请重新粘贴 Cookie"
				app.BannerLoginSite = "douyin"
				app.BannerInfo = "已加入缓存队列：BV1xx411c7mD"
			},
			RequiredLabels: []string{"悬浮通知区", "全局错误提示", "提示去登录", "关闭错误提示", "全局消息提示", "关闭消息提示"},
		},
		{
			// Preview of the green accent (Q-011); the last scene, so the
			// others keep the default blue.
			Name: "home-green-accent",
			Setup: func() {
				app.ConflictDiff.Open = false
				app.BindUpdater(nil)
				app.UpdateNotesOpen = false
				app.BannerError = ""
				app.BannerLoginSite = ""
				app.BannerInfo = ""
				app.PlayerOverlay.Open = false
				app.PlayerOverlay.VolumeMenuOpen = false
				ui.UseAccent("green")
				app.Navigate(ui.RouteHome)
			},
			RequiredLabels: []string{"左栏导航", "中栏内容"},
		},
	}
}
