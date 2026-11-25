package main

import (
	"context"
	"fmt"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/ui"
)

// 我的 screenshot fixtures: synthetic lists answered from memory.

func (b *screenshotBackend) AccountSections(siteID string) []service.AccountSection {
	if siteID == "douyin" && b.mineDouyinOut {
		return []service.AccountSection{{Site: siteID, Kind: service.AccountLogin, Label: "未登录", Reason: "未登录"}}
	}
	if siteID == "bilibili" {
		return []service.AccountSection{
			{Site: siteID, Kind: service.AccountFollowing, Label: "关注", Available: true},
			{Site: siteID, Kind: service.AccountFavorites, Label: "收藏夹", Available: true},
			{Site: siteID, Kind: service.AccountToView, Label: "稍后再看", Available: true},
			{Site: siteID, Kind: service.AccountHistory, Label: "历史记录", Available: true},
		}
	}
	if siteID == "weibo" {
		return []service.AccountSection{
			{Site: siteID, Kind: service.AccountFollowing, Label: "关注", Available: true},
			{Site: siteID, Kind: service.AccountFavorites, Label: "收藏", Available: true},
		}
	}
	if siteID == "x" {
		return []service.AccountSection{
			{Site: siteID, Kind: service.AccountFollowing, Label: "关注", Available: true},
			{Site: siteID, Kind: service.AccountBookmarks, Label: "书签", Available: true},
			{Site: siteID, Kind: service.AccountLikes, Label: "喜欢", Available: true},
		}
	}
	return []service.AccountSection{
		{Site: siteID, Kind: service.AccountFollowing, Label: "关注", Available: true},
		{Site: siteID, Kind: service.AccountLikes, Label: "喜欢", Available: true},
		{Site: siteID, Kind: service.AccountCollection, Label: "收藏", Available: true},
		{Site: siteID, Kind: service.AccountWatchHistory, Label: "观看历史", Available: true},
	}
}

func (b *screenshotBackend) AccountFolders(_ context.Context, siteID, kind string) ([]service.AccountFolder, error) {
	switch siteID + "/" + kind {
	case "bilibili/following":
		return []service.AccountFolder{{ID: "all", Title: "全部关注", IsDefault: true}, {ID: "-10", Title: "特别关注", Count: 3}, {ID: "7", Title: "游戏", Count: 12}}, nil
	case "bilibili/favorites":
		return []service.AccountFolder{{ID: "11", Title: "默认收藏夹", Count: 128, IsDefault: true}, {ID: "12", Title: "稍后学习", Count: 9, Private: true}}, nil
	case "bilibili/toview":
		return []service.AccountFolder{{ID: "all", Title: "全部", IsDefault: true}, {ID: "unfinished", Title: "未看完"}}, nil
	case "douyin/collection":
		return []service.AccountFolder{{ID: "all", Title: "全部收藏", IsDefault: true}, {ID: "7300000000000000127", Title: "做饭", Count: 12}}, nil
	}
	return nil, nil
}

var mineShotTitles = []string{"4K HDR 渲染管线拆解", "独立游戏开发日志 第 12 期", "Hi-Res 音频封装入门", "一小时看懂 MP4 容器", "直播推流参数怎么选", "弹幕与字幕的时间轴", "合成测试视频七", "合成测试视频八"}

func mineShotVideo(i int, title string) service.AccountItem {
	id := fmt.Sprintf("BV1sh%07d", i)
	return service.AccountItem{Kind: service.AccountItemVideo, Site: "bilibili", ID: id, Title: title, Author: "技术频道_Alpha", AuthorID: "20001",
		Duration: 300 + i*41, OpenURL: "https://www.bilibili.com/video/" + id, BrowserURL: "https://www.bilibili.com/video/" + id}
}

func (b *screenshotBackend) AccountPage(_ context.Context, siteID, kind, folder, cursor string) (service.AccountPage, error) {
	if err := b.mineErr[siteID+"/"+kind]; err != nil {
		return service.AccountPage{}, err
	}
	now := time.Now()
	var items []service.AccountItem
	switch siteID + "/" + kind {
	case "bilibili/favorites":
		for i, t := range mineShotTitles {
			it := mineShotVideo(i, t)
			it.FavAt = now.Add(-time.Duration(i) * 30 * time.Hour).Unix()
			items = append(items, it)
		}
		items[2] = service.AccountItem{Kind: service.AccountItemUnsupported, Site: "bilibili", ID: "gone", Title: "已失效", Invalid: true}
		items[4] = service.AccountItem{Kind: service.AccountItemUnsupported, Site: "bilibili", ID: "au1", Title: "合成音频收藏", Author: "音乐人", TypeLabel: "音频", BrowserURL: "https://www.bilibili.com/audio/au1"}
		return service.AccountPage{Items: items, HasMore: true, Cursor: "2|", Total: 128}, nil
	case "bilibili/toview":
		for i, t := range mineShotTitles[:5] {
			it := mineShotVideo(i, t)
			it.Progress = (i*97 + 40) % it.Duration
			it.Finished = i == 1
			items = append(items, it)
		}
		return service.AccountPage{Items: items, Total: 5}, nil
	case "bilibili/history":
		for i, t := range mineShotTitles {
			it := mineShotVideo(i, t)
			it.ViewedAt = now.Add(-time.Duration(i) * 11 * time.Hour).Unix()
			it.Progress = 120
			items = append(items, it)
		}
		items[1] = service.AccountItem{Kind: service.AccountItemUser, Site: "bilibili", ID: "213", Title: "合成直播间：渲染实况", Author: "独立游戏工坊", AuthorID: "20002",
			TypeLabel: "直播", Live: true, ViewedAt: now.Add(-2 * time.Hour).Unix(), OpenURL: "https://space.bilibili.com/20002"}
		items[3] = service.AccountItem{Kind: service.AccountItemUnsupported, Site: "bilibili", ID: "cv1", Title: "合成专栏：编码器笔记", Author: "技术频道_Alpha", TypeLabel: "专栏",
			ViewedAt: now.Add(-40 * time.Hour).Unix(), BrowserURL: "https://www.bilibili.com/read/cv1"}
		return service.AccountPage{Items: items, HasMore: true, Cursor: "2|1:1:archive"}, nil
	case "bilibili/following", "douyin/following":
		names := []string{"技术频道_Alpha", "独立游戏工坊", "合成纪录片", "音乐人小林", "合成测评室"}
		for i, n := range names {
			id := fmt.Sprintf("%d", 20001+i)
			it := service.AccountItem{Kind: service.AccountItemUser, Site: siteID, ID: id, Title: n, Signature: "每周更新，合成签名第一行",
				OpenURL: "https://space.bilibili.com/" + id, BrowserURL: "https://space.bilibili.com/" + id}
			if siteID == "douyin" {
				it.ID = fmt.Sprintf("MS4wLjABAAAA_uishots_follow_%04d", i)
				it.UniqueID = fmt.Sprintf("synth_%d", i)
				it.OpenURL = "https://www.douyin.com/user/" + it.ID
				it.BrowserURL = it.OpenURL
			}
			items = append(items, it)
		}
		return service.AccountPage{Items: items, Total: len(items)}, nil
	case "weibo/following":
		users := []struct{ uid, name, sign string }{
			{"500000001", "技术频道_微博", "系统工程与媒体工具开发笔记"},
			{"500000002", "微博科技博主", "每周分享桌面架构与图形渲染"},
			{"500000003", "合成摄影日记", "胶片风光与长文笔记"},
		}
		for _, u := range users {
			items = append(items, service.AccountItem{
				Kind:       service.AccountItemUser,
				Site:       "weibo",
				ID:         u.uid,
				Title:      u.name,
				Signature:  u.sign,
				OpenURL:    "https://weibo.com/u/" + u.uid,
				BrowserURL: "https://weibo.com/u/" + u.uid,
			})
		}
		return service.AccountPage{Items: items, Total: len(items)}, nil
	case "weibo/favorites":
		for i := 0; i < 4; i++ {
			mid := fmt.Sprintf("50000000000000%02d", 11+i)
			p := service.WeiboPostView{
				Mid:          mid,
				Author:       service.WeiboAuthorDTO{UID: "500000002", ScreenName: "微博科技博主"},
				CreatedAt:    now.Add(-time.Duration(i+1) * 4 * time.Hour).Unix(),
				Text:         fmt.Sprintf("收藏示例微博 #%d：分享一套跨平台媒体缓存与离线归档工具链实践笔记。", i+1),
				CanonicalURL: "https://weibo.com/500000002/" + mid,
				CacheKey:     "weibo:dynamic:" + mid,
			}
			switch i {
			case 0:
				p.Images = []service.WeiboMediaItemDTO{
					{Index: 1, Kind: "image", Width: 800, Height: 600},
					{Index: 2, Kind: "live_photo", Width: 600, Height: 800},
				}
				p.CachedItem = &library.ItemMetadata{
					ItemKey: p.CacheKey,
					NotePages: []library.NotePageRecord{
						{Index: 1, Kind: "image", Image: "images/01.png", Width: 800, Height: 600},
						{Index: 2, Kind: "motion", Image: "images/02.png", Motion: "images/02_live.mp4", Width: 600, Height: 800, Duration: 3},
					},
				}
			case 1:
				p.IsRetweet = true
				p.Text = "转发收藏这条原图与实况照片配图微博，方便后续对照。"
				p.Retweeted = &service.WeiboPostView{
					Mid:       "5000000000000088",
					Author:    service.WeiboAuthorDTO{UID: "500000003", ScreenName: "合成设计周刊"},
					CreatedAt: now.Add(-12 * time.Hour).Unix(),
					Text:      "原微博正文：桌面端中文排印建议将正文行高设为 1.6 倍，原帖与转发帖配图在收藏列表中直接展开预览。",
					Images: []service.WeiboMediaItemDTO{
						{Index: 1, Kind: "image", FromOrigin: true, Width: 800, Height: 600},
						{Index: 2, Kind: "live_photo", FromOrigin: true, Width: 600, Height: 800},
					},
				}
				p.CachedItem = &library.ItemMetadata{
					ItemKey: p.CacheKey,
					NotePages: []library.NotePageRecord{
						{Index: 1, Kind: "image", FromOrigin: true, Image: "images/01.png", Width: 800, Height: 600},
						{Index: 2, Kind: "motion", FromOrigin: true, Image: "images/02.png", Motion: "images/02_live.mp4", Width: 600, Height: 800, Duration: 3},
					},
				}
			case 2:
				p.IsRetweet = true
				p.Text = "转发收藏这条高清演示视频，点开即可进入详情或离线缓存。"
				p.Retweeted = &service.WeiboPostView{
					Mid:       "5000000000000089",
					Author:    service.WeiboAuthorDTO{UID: "500000001", ScreenName: "技术频道_微博"},
					CreatedAt: now.Add(-18 * time.Hour).Unix(),
					Text:      "原微博视频：单文件 MP4 封装与多清晰度选流实战演示。",
					Video:     &service.WeiboVideoDTO{ObjectID: "1034:5000000089", Title: "桌面字体排印实战演示", DurationSec: 84, QualityLabel: "1080P", Codec: "avc"},
				}
				p.CachedItem = &library.ItemMetadata{
					ItemKey: p.CacheKey,
					Media: []library.MediaFile{
						{Role: "cover", Path: "cover.png", FromOrigin: true, Width: 480, Height: 200},
					},
				}
			}
			items = append(items, service.AccountItem{
				Kind:       service.AccountItemPost,
				Site:       "weibo",
				ID:         mid,
				Title:      p.Text,
				Author:     p.Author.ScreenName,
				AuthorID:   p.Author.UID,
				FavAt:      p.CreatedAt,
				OpenURL:    p.CanonicalURL,
				BrowserURL: p.CanonicalURL,
				CacheKey:   p.CacheKey,
				Post:       &p,
			})
		}
		return service.AccountPage{Items: items, HasMore: false, Total: len(items)}, nil
	case "x/following":
		users := []struct{ uid, handle, name, sign string }{
			{"1000000000000000001", "alpha_x", "技术频道_X", "系统工程与跨平台媒体工具实践"},
			{"1000000000000000004", "synth_x_dev", "合成X开发者", "每周分享 Go 桌面应用与协议解析笔记"},
			{"1000000000000000005", "synth_x_photo", "合成X摄影师", "原图风光与胶片摄影归档"},
		}
		for _, u := range users {
			items = append(items, service.AccountItem{
				Kind:       service.AccountItemUser,
				Site:       "x",
				ID:         u.uid,
				UniqueID:   u.handle,
				Title:      u.name,
				Signature:  u.sign,
				OpenURL:    "https://x.com/" + u.handle,
				BrowserURL: "https://x.com/" + u.handle,
			})
		}
		return service.AccountPage{Items: items, HasMore: false, Total: len(items)}, nil
	case "x/bookmarks":
		for i := 0; i < 3; i++ {
			id := fmt.Sprintf("10000000000000000%02d", 21+i)
			p := service.XPostDTO{
				ID:           id,
				Author:       service.XAuthorDTO{RestID: "1000000000000000004", ScreenName: "synth_x_dev", Name: "合成X开发者"},
				CreatedAt:    now.Add(-time.Duration(i+1) * 3 * time.Hour).Unix(),
				Text:         fmt.Sprintf("书签示例帖子 #%d：整理 X 长帖、原图与 GIF 静音循环离线归档方案。", i+1),
				CanonicalURL: "https://x.com/synth_x_dev/status/" + id,
				CacheKey:     "x:dynamic:" + id,
			}
			if i == 1 {
				p.IsQuote = true
				p.Text = "引用收藏这条架构总结，便于后续反复查阅。"
				p.Quoted = &service.XPostDTO{
					ID:           "1000000000000000088",
					Author:       service.XAuthorDTO{RestID: "1000000000000000005", ScreenName: "synth_x_photo", Name: "合成X摄影师"},
					CreatedAt:    now.Add(-10 * time.Hour).Unix(),
					Text:         "原帖内容：四站点采用独立加密文件与独立限流退避，单站风控不拖累其他站点。",
					CanonicalURL: "https://x.com/synth_x_photo/status/1000000000000000088",
				}
			}
			items = append(items, service.AccountItem{
				Kind:       service.AccountItemPost,
				Site:       "x",
				ID:         id,
				Title:      p.Text,
				Author:     p.Author.Name,
				AuthorID:   p.Author.RestID,
				FavAt:      p.CreatedAt,
				OpenURL:    p.CanonicalURL,
				BrowserURL: p.CanonicalURL,
				CacheKey:   p.CacheKey,
				XPost:      &p,
			})
		}
		return service.AccountPage{Items: items, HasMore: false, Total: len(items)}, nil
	case "x/likes":
		for i := 0; i < 3; i++ {
			id := fmt.Sprintf("10000000000000000%02d", 31+i)
			p := service.XPostDTO{
				ID:           id,
				Author:       service.XAuthorDTO{RestID: "1000000000000000005", ScreenName: "synth_x_photo", Name: "合成X摄影师"},
				CreatedAt:    now.Add(-time.Duration(i+2) * 5 * time.Hour).Unix(),
				Text:         fmt.Sprintf("喜欢示例帖子 #%d：原图摄影与 MP4 封装质量核验记录。", i+1),
				CanonicalURL: "https://x.com/synth_x_photo/status/" + id,
				CacheKey:     "x:dynamic:" + id,
			}
			items = append(items, service.AccountItem{
				Kind:       service.AccountItemPost,
				Site:       "x",
				ID:         id,
				Title:      p.Text,
				Author:     p.Author.Name,
				AuthorID:   p.Author.RestID,
				FavAt:      p.CreatedAt,
				OpenURL:    p.CanonicalURL,
				BrowserURL: p.CanonicalURL,
				CacheKey:   p.CacheKey,
				XPost:      &p,
			})
		}
		return service.AccountPage{Items: items, HasMore: false, Total: len(items)}, nil
	case "douyin/likes", "douyin/collection", "douyin/watch_history":
		for i := 0; i < 8; i++ {
			id := fmt.Sprintf("73500000000000%04d", i)
			if kind == service.AccountWatchHistory {
				id = fmt.Sprintf("75500000000000%04d", i)
			}
			k := "video"
			if i%3 == 1 {
				k = "note"
			}
			card := service.DouyinWorkCardDTO{ID: id, Kind: k, Title: fmt.Sprintf("合成作品 %d：%s", i+1, mineShotTitles[i]), DurationSec: 15 + i*7,
				CanonicalURL: "https://www.douyin.com/video/" + id, CacheKey: "douyin:video:" + id}
			items = append(items, service.AccountItem{Kind: service.AccountItemWork, Site: "douyin", ID: id, Title: card.Title, Author: "技术频道_抖音号",
				AuthorID: "MS4wLjABAAAA_uishots_author", OpenURL: card.CanonicalURL, CacheKey: card.CacheKey, Work: &card})
		}
		return service.AccountPage{Items: items, HasMore: true, Cursor: "2|18"}, nil
	}
	return service.AccountPage{Items: []service.AccountItem{}}, nil
}

func mineShotScenes(app *ui.App, fb *screenshotBackend) []sceneSpec {
	open := func(siteID, kind string) func() {
		return func() {
			app.PlayerOverlay.Open, app.ImageViewer.Open = false, false
			app.BindUpdater(nil)
			app.UpdateNotesOpen = false
			fb.mineDouyinOut, fb.mineErr = false, nil
			app.Mine = ui.MineState{}
			app.OpenMine(siteID, kind)
		}
	}
	return []sceneSpec{
		{Name: "mine-folded", Setup: open("", ""),
			RequiredLabels:  []string{"我的站点：哔哩哔哩", "我的站点：抖音", "我的站点：微博", "我的站点：X"},
			ForbiddenLabels: []string{"我的功能：哔哩哔哩 收藏夹", "我的功能：抖音 喜欢", "我的功能：抖音 观看历史", "我的功能：微博 关注", "我的功能：X 关注", "我的功能：X 书签", "我的功能：X 喜欢", "我的虚拟列表"}},
		{Name: "mine-bili-favorites", Setup: open("bilibili", service.AccountFavorites),
			RequiredLabels:  []string{"收藏夹选择", "我的虚拟列表", "我的条目：已失效", "我的更多菜单"},
			ForbiddenLabels: []string{"我的条目更多：gone"}},
		{Name: "mine-bili-toview", Setup: open("bilibili", service.AccountToView),
			RequiredLabels: []string{"稍后再看筛选：全部", "稍后再看筛选：未看完", "稍后再看总数", "我的虚拟列表"}},
		{Name: "mine-bili-history", Setup: open("bilibili", service.AccountHistory),
			RequiredLabels: []string{"历史分组：今天", "我的虚拟列表"}},
		{Name: "mine-bili-following", Setup: open("bilibili", service.AccountFollowing),
			RequiredLabels: []string{"关注分组选择", "我的用户：技术频道_Alpha"}},
		{Name: "mine-douyin-likes", Setup: open("douyin", service.AccountLikes),
			RequiredLabels: []string{"我的作品虚拟列表"}},
		{Name: "mine-douyin-collection", Setup: open("douyin", service.AccountCollection),
			RequiredLabels: []string{"收藏夹选择", "我的作品虚拟列表"}},
		{Name: "mine-douyin-watch-history", Setup: open("douyin", service.AccountWatchHistory),
			RequiredLabels:  []string{"我的作品虚拟列表", "我的更多菜单"},
			ForbiddenLabels: []string{"收藏夹选择"}},
		{Name: "mine-weibo-following", Setup: open("weibo", service.AccountFollowing),
			RequiredLabels:  []string{"我的虚拟列表", "我的用户：微博科技博主"},
			ForbiddenLabels: []string{"关注分组选择", "我的功能：微博 赞过"}},
		{Name: "mine-weibo-favorites", Setup: open("weibo", service.AccountFavorites),
			RequiredLabels:  []string{"我的虚拟列表", "我的微博行：5000000000000011", "我的微博：5000000000000011"},
			ForbiddenLabels: []string{"我的功能：微博 赞过"}},
		{Name: "mine-x-following", Setup: open("x", service.AccountFollowing),
			RequiredLabels:  []string{"我的虚拟列表", "我的用户：技术频道_X"},
			RequiredTexts:   []string{"@alpha_x", "@synth_x_dev"},
			ForbiddenLabels: []string{"关注分组选择", "我的功能：X 列表"}},
		{Name: "mine-x-bookmarks", Setup: open("x", service.AccountBookmarks),
			RequiredLabels:  []string{"我的虚拟列表", "我的X帖子行：1000000000000000021", "我的X帖子：1000000000000000021"},
			RequiredTexts:   []string{"书签示例帖子 #1", "@synth_x_dev"},
			ForbiddenLabels: []string{"收藏夹选择", "我的功能：X 列表"}},
		{Name: "mine-x-likes", Setup: open("x", service.AccountLikes),
			RequiredLabels:  []string{"我的虚拟列表", "我的X帖子行：1000000000000000031", "我的X帖子：1000000000000000031"},
			RequiredTexts:   []string{"喜欢示例帖子 #1", "@synth_x_photo"},
			ForbiddenLabels: []string{"我的功能：X 列表"}},
		{Name: "mine-logged-out", Setup: func() {
			open("douyin", "")()
			fb.mineDouyinOut = true
		}, RequiredLabels: []string{"我的登录：抖音"}, ForbiddenLabels: []string{"我的功能：抖音 喜欢", "我的功能：抖音 观看历史"}},
		{Name: "mine-risk-error", Setup: func() {
			fb.mineDouyinOut = false
			fb.mineErr = map[string]error{"bilibili/history": &site.Error{Site: site.Bilibili, Kind: site.ErrKindRiskControl, Code: -352, Message: "请求触发风控拦截，已停止自动请求"}}
			app.Mine = ui.MineState{}
			app.OpenMine("bilibili", service.AccountHistory)
		}, RequiredLabels: []string{"我的重试"}, ForbiddenLabels: []string{"我的列表为空"}},
		// A narrow window shows the chooser in the page; wide ones keep it in the panel.
		{Name: "mine-narrow-chooser", Setup: func() {
			open("bilibili", service.AccountFavorites)()
			app.Mine.Picker = true
		}, RequiredLabels: []string{"我的站点：哔哩哔哩", "我的功能：哔哩哔哩 收藏夹"}},
	}
}
