package main

import (
	"context"
	"fmt"
	"time"

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
	return []service.AccountSection{
		{Site: siteID, Kind: service.AccountFollowing, Label: "关注", Available: true},
		{Site: siteID, Kind: service.AccountLikes, Label: "喜欢", Available: true},
		{Site: siteID, Kind: service.AccountCollection, Label: "收藏", Available: true},
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
	case "douyin/likes", "douyin/collection":
		for i := 0; i < 8; i++ {
			id := fmt.Sprintf("73500000000000%04d", i)
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

// mineShotScene is one 我的 screenshot scene.
type mineShotScene struct {
	Name            string
	Setup           func()
	RequiredLabels  []string
	ForbiddenLabels []string
}

func mineShotScenes(app *ui.App, fb *screenshotBackend) []mineShotScene {
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
	return []mineShotScene{
		{Name: "mine-folded", Setup: open("", ""),
			RequiredLabels:  []string{"我的站点：哔哩哔哩", "我的站点：抖音"},
			ForbiddenLabels: []string{"我的功能：哔哩哔哩 收藏夹", "我的功能：抖音 喜欢", "我的虚拟列表"}},
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
		{Name: "mine-logged-out", Setup: func() {
			open("douyin", "")()
			fb.mineDouyinOut = true
		}, RequiredLabels: []string{"我的登录：抖音"}, ForbiddenLabels: []string{"我的功能：抖音 喜欢"}},
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
