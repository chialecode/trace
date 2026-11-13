package ui

import (
	"context"
	"fmt"
	"strconv"

	"github.com/chialecode/trace/internal/service"
	"github.com/egoist/mygo/ui"
)

// cacheOlderMenu is the user page's "⋯ → 缓存更早的内容" (DEC-046 ⑥): how
// many posts one batch queues, 20 by default.
func (a *App) cacheOlderMenu(m *ui.Menu, siteID string) {
	m.Submenu("缓存更早的内容", func(sub *ui.Menu) {
		for _, n := range service.CacheOlderChoices {
			label := fmt.Sprintf("%d 条", n)
			if n == service.DefaultCacheOlderLimit {
				label += "（推荐）"
			}
			if sub.Item(label).Chosen() {
				a.cacheOlder(siteID, n)
			}
		}
	})
}

// cacheOlder queues the next uncached posts of the open user page. The
// loaded list costs no request; pages past it are fetched as automatic work.
func (a *App) cacheOlder(siteID string, n int) {
	req := service.CacheOlderRequest{Site: siteID, Limit: n}
	switch {
	case siteID == "douyin" && a.CurrentDouyinUser != nil:
		req.List, req.User = "works", a.CurrentDouyinUser.SecUID
		req.Works = append(req.Works, a.CurrentDouyinWorks...)
		req.Cursor, req.HasMore = a.DouyinWorksNextCursor, a.DouyinWorksHasMore
	case siteID == "bilibili" && a.CurrentUser != nil:
		req.User = strconv.FormatInt(a.CurrentUser.Mid, 10)
		if a.UserTab == "videos" {
			req.List = "videos"
			req.Videos = append(req.Videos, a.UserVideos...)
			req.Cursor, req.HasMore = strconv.Itoa(max(a.UserVideosPage, 0)+1), a.UserVideosHasMore || len(a.UserVideos) == 0
		} else {
			req.List = "dynamics"
			req.Dynamics = append(req.Dynamics, a.UserDynamics...)
			req.Cursor, req.HasMore = a.UserDynamicsNext, a.UserDynamicsHasMore
		}
	default:
		return
	}
	request(a, "cache-older-"+siteID, func(ctx context.Context) (service.CacheOlderResult, error) {
		return a.backend.CacheOlderUserItems(ctx, req)
	}, func(res service.CacheOlderResult) {
		a.BannerInfo = cacheOlderSummary(res)
		a.RefreshData()
	})
}

func cacheOlderSummary(res service.CacheOlderResult) string {
	msg := fmt.Sprintf("已把 %d 条更早的内容加入缓存队列", res.Enqueued)
	if res.Enqueued == 0 {
		msg = "没有找到可以缓存的更早内容"
	}
	if res.Skipped > 0 {
		msg += fmt.Sprintf("，跳过 %d 条已缓存或不能缓存的", res.Skipped)
	}
	switch {
	case res.Stopped != "":
		msg += "。" + res.Stopped
	case res.Exhausted:
		msg += "。已到列表末尾"
	}
	return msg
}
