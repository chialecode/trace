package ui

import (
	"context"
	"strconv"
	"strings"

	"github.com/chialecode/trace/internal/service"
	"github.com/egoist/mygo/ui"
)

// cacheOlderMenu renders the user page's "⋯" background list caching entry (DEC-050):
// either "全部缓存" or "在缓存队列中查看" when the open list is already unfinished in the queue.
// Private Douyin accounts do not expose this entry.
func (a *App) cacheOlderMenu(m *ui.Menu, siteID string) {
	normSite, user, list, ok := a.currentUserQueueTarget(siteID)
	if !ok {
		return
	}
	if task, exists := a.findUnfinishedQueueTask(normSite, user, list); exists {
		if m.Item("在缓存队列中查看").Chosen() {
			a.OpenCacheQueueTask(task.ID)
		}
		return
	}
	if m.Item("全部缓存").Chosen() {
		a.enqueueCacheAll(normSite)
	}
}

func (a *App) currentUserQueueTarget(siteID string) (normSite, user, list string, ok bool) {
	normSite = normalizeSiteIDUI(siteID)
	switch {
	case normSite == "douyin" && a.CurrentDouyinUser != nil:
		if a.CurrentDouyinUser.IsPrivate {
			return "", "", "", false
		}
		user = strings.TrimSpace(a.CurrentDouyinUser.SecUID)
		if user == "" {
			return "", "", "", false
		}
		return "douyin", user, "works", true
	case normSite == "bilibili" && a.CurrentUser != nil:
		if a.CurrentUser.Mid <= 0 {
			return "", "", "", false
		}
		user = strconv.FormatInt(a.CurrentUser.Mid, 10)
		list = "dynamics"
		if a.UserTab == "videos" {
			list = "videos"
		}
		return "bilibili", user, list, true
	default:
		return "", "", "", false
	}
}

func (a *App) findUnfinishedQueueTask(siteID, user, list string) (service.CacheQueueTask, bool) {
	if a.backend != nil {
		if task, ok := a.backend.FindCacheQueueTask(siteID, user, list); ok {
			return task, true
		}
	}
	for _, q := range a.CacheQueue {
		if normalizeSiteIDUI(q.Site) == siteID && q.User == user && q.List == list && isUnfinishedCacheQueueStatus(q.Status) {
			return q, true
		}
	}
	return service.CacheQueueTask{}, false
}

func isUnfinishedCacheQueueStatus(status string) bool {
	switch status {
	case service.CacheQueueQueued,
		service.CacheQueueRunning,
		service.CacheQueueWaiting,
		service.CacheQueueWaitingBackoff,
		service.CacheQueuePaused:
		return true
	default:
		return false
	}
}

func (a *App) enqueueCacheAll(siteID string) {
	req := service.CacheQueueRequest{Site: siteID}
	switch {
	case siteID == "douyin" && a.CurrentDouyinUser != nil:
		if a.CurrentDouyinUser.IsPrivate {
			return
		}
		req.List = "works"
		req.User = a.CurrentDouyinUser.SecUID
		req.UserName = firstNonEmptyStrUI(a.CurrentDouyinUser.Nickname, a.CurrentDouyinUser.SecUID)
		req.AvatarURL = a.CurrentDouyinUser.AvatarURL
		req.Works = append(req.Works, a.CurrentDouyinWorks...)
		req.Cursor = a.DouyinWorksNextCursor
		req.HasMore = a.DouyinWorksHasMore
	case siteID == "bilibili" && a.CurrentUser != nil:
		req.User = strconv.FormatInt(a.CurrentUser.Mid, 10)
		req.UserName = firstNonEmptyStrUI(a.CurrentUser.Name, req.User)
		req.AvatarURL = a.CurrentUser.Face
		if a.UserTab == "videos" {
			req.List = "videos"
			req.Videos = append(req.Videos, a.UserVideos...)
			req.Cursor = strconv.Itoa(max(a.UserVideosPage, 0) + 1)
			req.HasMore = a.UserVideosHasMore || len(a.UserVideos) == 0
		} else {
			req.List = "dynamics"
			req.Dynamics = append(req.Dynamics, a.UserDynamics...)
			req.Cursor = a.UserDynamicsNext
			req.HasMore = a.UserDynamicsHasMore
		}
	default:
		return
	}
	request(a, "cache-queue-"+siteID+"-"+req.List, func(ctx context.Context) (service.CacheQueueTask, error) {
		return a.backend.EnqueueCacheQueue(ctx, req)
	}, func(service.CacheQueueTask) {
		a.BannerInfo = "已加入缓存队列，会在后台慢慢缓存"
		a.RefreshData()
	})
}
