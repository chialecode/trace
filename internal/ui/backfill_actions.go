package ui

import (
	"context"
	"fmt"
	"strings"

	"github.com/chialecode/trace/internal/service"
)

func (a *App) refreshBackfillSummary() {
	if a.backend == nil {
		a.BackfillSummary = service.BackfillSummaryDTO{}
		return
	}
	if sum, err := a.backend.BackfillSummary(); err == nil {
		a.BackfillSummary = sum
	}
}

func (a *App) activeWeiboBackfillQueue() (service.CacheQueueTask, bool) {
	for _, q := range a.CacheQueue {
		if (q.Kind == service.CacheQueueKindWeiboBackfill || q.List == service.CacheQueueListWeiboBackfill) && isUnfinishedCacheQueueStatus(q.Status) {
			return q, true
		}
	}
	return service.CacheQueueTask{}, false
}

func (a *App) startWeiboBackfill() {
	if a.backend == nil {
		return
	}
	backend := a.backend
	requestGuarded(a, "start-weibo-backfill", nil, func(ctx context.Context) (service.CacheQueueTask, error) {
		return backend.StartWeiboBackfill(ctx)
	}, func(task service.CacheQueueTask) {
		a.CacheQueue = a.backend.ListCacheQueue()
		a.refreshBackfillSummary()
		a.BannerInfo = "已启动历史缓存补全队列"
		if task.ID != "" {
			a.OpenCacheQueueTask(task.ID)
		}
	}, func(err error) {
		a.showError(err)
	})
}

func (a *App) openForceRecacheConfirm(itemKey, title string) {
	itemKey = strings.TrimSpace(itemKey)
	if itemKey == "" {
		return
	}
	a.ForceRecacheConfirm = ForceRecacheConfirmState{
		Open:    true,
		ItemKey: itemKey,
		Title:   strings.TrimSpace(title),
	}
}

func (a *App) confirmForceRecache() {
	if a.backend == nil {
		return
	}
	itemKey := strings.TrimSpace(a.ForceRecacheConfirm.ItemKey)
	if itemKey == "" {
		return
	}
	backend := a.backend
	requestGuarded(a, "force-recache:"+itemKey, nil, func(ctx context.Context) (struct{}, error) {
		_, err := backend.ForceRecacheItem(ctx, itemKey)
		return struct{}{}, err
	}, func(struct{}) {
		a.BannerInfo = "已加入强制重新下载任务"
		a.RefreshData()
	}, func(err error) {
		a.showError(err)
	})
}

func formatBackfillSummaryBreakdown(sum service.BackfillSummaryDTO) string {
	var parts []string
	if sum.WeiboMissingNoWM > 0 {
		parts = append(parts, fmt.Sprintf("缺无水印伴侣图 %d 条", sum.WeiboMissingNoWM))
	}
	if sum.WeiboLegacyLinks > 0 {
		parts = append(parts, fmt.Sprintf("旧格式链接 %d 条", sum.WeiboLegacyLinks))
	}
	if sum.WeiboFastCDNCandidates > 0 {
		parts = append(parts, fmt.Sprintf("%d 条可快速直连补齐", sum.WeiboFastCDNCandidates))
	}
	return strings.Join(parts, "，")
}

func formatBackfillSummaryDetail(sum service.BackfillSummaryDTO) string {
	detail := formatBackfillSummaryBreakdown(sum)
	if detail == "" {
		return fmt.Sprintf("发现 %d 条历史微博可补全", sum.TotalItems)
	}
	return fmt.Sprintf("发现 %d 条历史微博可补全（%s）", sum.TotalItems, detail)
}
