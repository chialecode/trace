package main

import (
	"fmt"
	"path/filepath"
	"slices"
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
	primarySub := env.primarySub
	auroraSub := env.auroraSub
	batchRule := env.batchRule
	savedItem := env.savedItem
	savedLiveItem := env.savedLiveItem
	savedPartialItem := env.savedPartialItem
	savedDynamicItem := env.savedDynamicItem
	savedDouyinVideo := env.savedDouyinVideo
	savedDouyinNote := env.savedDouyinNote
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
				"主页站点筛选：全部",
				"主页站点筛选：B 站",
				"主页站点筛选：抖音",
				"主页作者站点：bilibili:video:SYNTH_0001",
				"主页作者站点：" + savedDouyinNote.ItemKey,
			},
			// DEC-053 ②: the subscriptions in the panel, or the avatar
			// strip in a narrow window; no section titles.
			PanelRequiredLabels:  []string{"主页订阅列表"},
			NarrowRequiredLabels: []string{"主页订阅头像条", "主页订阅头像：全部动态"},
			ForbiddenLabels:      []string{"正在直播", "有更新"},
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
				app.Navigate(ui.RouteSubscriptions)
				seedRecentAndLive()
			},
			RequiredLabels: []string{"选择订阅 " + primarySub.ID},
			ForbiddenLabels: []string{
				"订阅详情账号站点：bilibili:20001",
				"订阅详情账号站点：douyin:" + douyinSecUID,
				"用户页站点标记",
			},
		},
		{
			Name: "subscribe-picker",
			Setup: func() {
				seedRecentAndLive()
				app.Navigate(ui.RouteSubscriptions)
				app.SubDetailOpen, app.SubAccountsOpen = false, false
				app.OpenSubscribePicker(subscription.AccountRef{Site: "bilibili", UID: "30001", Name: "新发现的UP主"})
			},
			RequiredLabels: []string{
				"加入订阅对话框",
				"新建订阅",
				"加入订阅搜索框",
				"加入订阅列表",
				"加入现有订阅：" + primarySub.ID,
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
				app.SubAccountsSite = "all"
			},
			RequiredLabels: []string{
				"订阅账号列表",
				"订阅账号：bilibili:20001",
				"所属订阅：bilibili:20001",
			},
			PanelRequiredLabels: []string{"订阅侧栏：全部账号"},
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
				"订阅管理菜单",
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
			Name: "subscription-content-empty",
			Setup: func() {
				seedRecentAndLive()
				if auroraSub.ID != "" {
					app.SubDetailTab = "content"
					app.OpenSubscriptionDetail(auroraSub)
					app.SubDetailTab = "content"
				}
			},
			RequiredLabels: []string{
				"订阅标签：内容",
				"订阅标签：账号",
				"订阅管理菜单",
			},
			ForbiddenLabels: []string{
				"订阅内容信息流列表",
				"关联账号输入",
			},
		},
		{
			Name: "subscription-accounts",
			Setup: func() {
				seedRecentAndLive()
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
				"开始录制 9001",
				"停止录制 douyin:" + douyinSecUID,
				"订阅账号更多：bilibili:20001",
				"订阅账号更多：douyin:" + douyinSecUID,
				"关联账号输入",
				"预览关联账号",
			},
			ForbiddenLabels: []string{
				"订阅内容信息流列表",
			},
		},
		{
			Name: "settings-rules",
			Setup: func() {
				seedRecentAndLive()
				app.RuleBatch.Open = false
				app.RuleDraft = nil
				app.SettingsCategory = "rules"
				app.Navigate(ui.RouteSettings)
			},
			RequiredLabels: []string{"缓存规则：默认", "缓存规则：自动存视频与图文", "新建缓存规则"},
		},
		{
			Name: "settings-rules-batch",
			Setup: func() {
				seedRecentAndLive()
				app.SettingsCategory = "rules"
				app.Navigate(ui.RouteSettings)
				draft := batchRule
				draft.Types = slices.Clone(batchRule.Types)
				app.RuleDraft = &draft
				app.RuleBatch.Open = false
				openTester := mygoui.NewTester(app.View, 1280, 860)
				_ = openTester.Click("选择订阅…")
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
			Name: "settings-account",
			Setup: func() {
				app.RuleBatch.Open = false
				app.RuleDraft = nil
				seedRecentAndLive()
				app.SettingsCategory = "account"
				app.Navigate(ui.RouteSettings)
			},
			RequiredLabels: []string{
				"账号分组：哔哩哔哩",
				"账号分组：抖音",
				"重新粘贴抖音Cookie",
				"退出抖音登录",
			},
		},
		{
			Name: "settings-subscription",
			Setup: func() {
				seedRecentAndLive()
				app.SettingsCategory = "subscription"
				app.Navigate(ui.RouteSettings)
			},
			RequiredLabels: []string{"退避状态：douyin"},
		},
		{
			Name: "parse",
			Setup: func() {
				app.Navigate(ui.RouteParse)
				app.HistoryItems = b.ListHistory()
				app.ParsePageInput = "https://www.douyin.com/video/7388001001"
				app.HistoryItemMenuID = "bilibili:video:BV1xx411c7mD"
			},
			RequiredLabels: []string{
				"链接解析输入框",
				"立即解析",
			},
			PanelRequiredLabels: []string{
				"最近解析更多",
				"历史打开：BV1xx411c7mD",
				"历史更多：BV1xx411c7mD",
				"历史站点：bilibili:BV1xx411c7mD",
				"历史站点：douyin:7388001001",
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
			Name: "library",
			Setup: func() {
				app.LibrarySiteFilter = "all"
				app.Navigate(ui.RouteLibrary)
				app.ExpandedItemKey = savedDynamicItem.ItemKey
			},
			RequiredLabels: []string{
				"媒体库站点筛选：全部",
				"媒体库站点筛选：B 站",
				"媒体库站点筛选：抖音",
				"媒体更多：" + savedDynamicItem.ItemKey,
				"媒体库站点：" + savedDynamicItem.ItemKey,
				"媒体库站点：" + savedDouyinVideo.ItemKey,
			},
		},
		{
			Name: "library-delete",
			Setup: func() {
				app.Navigate(ui.RouteLibrary)
				app.ExpandedItemKey = ""
				plan, _ := b.PlanDelete(savedItem.ItemKey)
				app.DeleteConfirm = ui.DeleteConfirmState{
					Open:        true,
					Item:        savedItem,
					Plan:        plan,
					FileCount:   len(plan.RegisteredFiles),
					TotalBytes:  savedItem.TotalBytes,
					DeleteFiles: true,
				}
			},
		},
		{
			Name: "tasks",
			Setup: func() {
				app.DeleteConfirm.Open = false
				seedRecentAndLive()
				app.Navigate(ui.RouteTasks)
				app.TasksSection = "downloads"
				app.Tasks = []download.Task{
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
				"任务站点：task_demo_dy",
				"任务站点：task_demo_run",
			},
			PanelRequiredLabels: []string{
				"任务侧栏：下载任务",
				"任务侧栏：缓存队列",
				"任务侧栏：直播录制",
				"任务侧栏：录制异常",
			},
			ForbiddenLabels: []string{
				"录制站点：bilibili:9002",
			},
		},
		{
			Name: "tasks-queue",
			Setup: func() {
				seedRecentAndLive()
				app.OpenCacheQueueTask("cq_demo_bili")
			},
			RequiredLabels: []string{
				"缓存队列卡片：cq_demo_bili",
				"缓存队列卡片：cq_demo_dy",
				"展开缓存队列：cq_demo_bili",
				"队列详情站点：cq_demo_bili",
				"队列详情计数：cq_demo_bili",
				"暂停缓存队列：cq_demo_bili",
				"取消缓存队列：cq_demo_bili",
				"队列最近缓存：" + savedItem.ItemKey,
			},
			PanelRequiredLabels: []string{
				"任务侧栏：下载任务",
				"任务侧栏：缓存队列",
				"任务侧栏：直播录制",
				"任务侧栏：录制异常",
			},
		},
		{
			Name: "tasks-live",
			Setup: func() {
				seedRecentAndLive()
				app.Navigate(ui.RouteTasks)
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
				"录制行异常：douyin:live:73990011",
				"直播实时预览面板",
				"直播实时预览标记",
				"直播实时预览画面",
				"关闭直播实时预览",
			},
			PanelRequiredLabels: []string{
				"任务侧栏：下载任务",
				"任务侧栏：直播录制",
				"任务侧栏：录制异常",
			},
			NarrowRequiredLabels: []string{
				"任务分段：录制异常",
			},
			ForbiddenLabels: []string{
				"弹幕开关",
			},
		},
		{
			Name: "tasks-incidents",
			Setup: func() {
				seedRecentAndLive()
				app.Navigate(ui.RouteTasks)
				app.TasksSection = "incidents"
				app.SelectedLiveIncidentKey = "bilibili:live:9002"
			},
			RequiredLabels: []string{
				"录制异常场次：bilibili:live:9002",
				"异常场次站点：bilibili:live:9002",
				"查看异常正在录制：bilibili:live:9002",
				"异常条目：bilibili:live:9002#0",
				"异常条目：bilibili:live:9002#1",
				"录制异常场次：douyin:live:73990011",
				"异常场次站点：douyin:live:73990011",
				"异常条目：douyin:live:73990011#0",
				"录制异常场次：" + savedLiveItem.ItemKey,
				"打开异常录像详情：" + savedLiveItem.ItemKey,
			},
			PanelRequiredLabels: []string{
				"任务侧栏：下载任务",
				"任务侧栏：直播录制",
				"任务侧栏：录制异常",
			},
			NarrowRequiredLabels: []string{
				"任务分段：录制异常",
			},
			ForbiddenLabels: []string{
				"录制异常空状态",
				"直播实时预览面板",
			},
		},
		{
			Name: "tasks-checks",
			Setup: func() {
				app.Navigate(ui.RouteTasks)
				app.TasksSection = "checks"
			},
			RequiredLabels: []string{
				"检查计划行 bilibili:20001",
				"检查计划行 douyin:" + douyinSecUID,
			},
			PanelRequiredLabels: []string{
				"任务侧栏：录制异常",
				"任务侧栏：检查计划",
			},
			NarrowRequiredLabels: []string{
				"任务分段：检查计划",
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
			RequiredLabels: []string{"登录站点：哔哩哔哩", "登录站点：抖音"},
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
				"抖音已登录摘要",
				"抖音Cookie输入框",
				"验证并登录抖音",
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
			TitleBar:       mygoui.TitleBar{Height: 32, Right: 138},
			RequiredLabels: []string{"窗口拖动区", "页面顶部栏", "快速解析输入框"},
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
			// "设置 › 更新" of a release build that updates itself.
			Name: "settings-update",
			Setup: func() {
				app.PlayerOverlay.Open = false
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
			// The line below the top bar once an update is ready, its
			// notes open.
			Name: "update-ready",
			Setup: func() {
				app.BindUpdater(&shotUpdater{st: update.State{
					Enabled: true, Channel: "stable", CurrentVersion: "0.1.1", Phase: update.PhaseReady,
					Release: &update.Release{Version: "0.1.2", Notes: "- 订阅支持合集\n- 修复直播录制在断线后的分段"},
					Prefs:   update.Prefs{AutoCheck: true, AutoDownload: true},
				}})
				app.UpdateNotesOpen = true
				app.Navigate(ui.RouteHome)
			},
			RequiredLabels: []string{"更新提示", "更新内容", "关闭更新提示"},
		},
		{
			// Preview of the green accent (Q-011); the last scene, so the
			// others keep the default blue.
			Name: "home-green-accent",
			Setup: func() {
				app.BindUpdater(nil)
				app.UpdateNotesOpen = false
				app.PlayerOverlay.Open = false
				app.PlayerOverlay.VolumeMenuOpen = false
				ui.UseAccent("green")
				app.Navigate(ui.RouteHome)
			},
			RequiredLabels: []string{"左栏导航", "中栏内容"},
		},
	}
}
