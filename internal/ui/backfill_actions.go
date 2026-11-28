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
		a.backfillSummaryDirty = false
		a.backfillSummaryRequestedGen = a.backfillSummaryDirtyGen
		return
	}
	if sum, err := a.backend.BackfillSummary(); err == nil {
		a.BackfillSummary = sum
		a.backfillSummaryDirty = false
		a.backfillSummaryRequestedGen = a.backfillSummaryDirtyGen
	}
}

func isBackfillQueueTask(q service.CacheQueueTask) bool {
	return q.Kind == service.CacheQueueKindWeiboBackfill ||
		q.List == service.CacheQueueListWeiboBackfill ||
		q.Kind == service.CacheQueueKindSiteBackfill ||
		q.List == service.CacheQueueListSiteBackfill
}

func (a *App) activeBackfillQueues() []service.CacheQueueTask {
	var out []service.CacheQueueTask
	for _, q := range a.CacheQueue {
		if isBackfillQueueTask(q) && isUnfinishedCacheQueueStatus(q.Status) {
			out = append(out, q)
		}
	}
	return out
}

func (a *App) activeBackfillQueue() (service.CacheQueueTask, bool) {
	active := a.activeBackfillQueues()
	if len(active) == 0 {
		return service.CacheQueueTask{}, false
	}
	for _, q := range active {
		if q.Status == service.CacheQueueRunning {
			return q, true
		}
	}
	return active[0], true
}

func backfillQueueSiteLabel(siteID string) string {
	switch normalizeSiteIDUI(siteID) {
	case "bilibili":
		return "B站"
	case "douyin":
		return "抖音"
	case "weibo":
		return "微博"
	case "x":
		return "X"
	default:
		return "全部站点"
	}
}

func backfillQueueCardTitle(siteID string) string {
	return "历史缓存补全 · " + backfillQueueSiteLabel(siteID)
}

func (a *App) startBackfill() {
	if a.backend == nil {
		return
	}
	backend := a.backend
	requestGuarded(a, "start-backfill", nil, func(ctx context.Context) ([]service.CacheQueueTask, error) {
		return backend.StartBackfill(ctx)
	}, func(tasks []service.CacheQueueTask) {
		a.CacheQueue = a.backend.ListCacheQueue()
		a.markBackfillSummaryDirty()
		a.ensureBackfillSummaryFresh()
		if len(tasks) > 1 {
			a.notifyInfo(fmt.Sprintf("已启动 %d 个站点的历史缓存补全队列", len(tasks)))
		} else {
			a.notifyInfo("已启动历史缓存补全队列")
		}
		if len(tasks) > 0 && tasks[0].ID != "" {
			a.OpenCacheQueueTask(tasks[0].ID)
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
		a.notifyInfo("已加入强制重新下载任务")
		a.RefreshData()
	}, func(err error) {
		a.showError(err)
	})
}

func formatBackfillSummaryHeadline(sum service.BackfillSummaryDTO) string {
	return fmt.Sprintf("发现 %d 条历史缓存可补全", sum.TotalItems)
}

func formatBackfillSummaryBreakdown(sum service.BackfillSummaryDTO) string {
	var parts []string
	if sum.BilibiliItems > 0 {
		parts = append(parts, fmt.Sprintf("B站 %d 条", sum.BilibiliItems))
	}
	if sum.DouyinItems > 0 {
		parts = append(parts, fmt.Sprintf("抖音 %d 条", sum.DouyinItems))
	}
	if sum.WeiboMissingNoWM > 0 {
		parts = append(parts, fmt.Sprintf("微博缺无水印伴侣图 %d 条", sum.WeiboMissingNoWM))
	}
	if sum.WeiboLegacyLinks > 0 {
		parts = append(parts, fmt.Sprintf("微博旧格式链接 %d 条", sum.WeiboLegacyLinks))
	}
	if sum.WeiboMissingEditHistory > 0 {
		parts = append(parts, fmt.Sprintf("微博缺编辑历史 %d 条", sum.WeiboMissingEditHistory))
	}
	if sum.XItems > 0 {
		parts = append(parts, fmt.Sprintf("X %d 条", sum.XItems))
	}
	if sum.WeiboFastCDNCandidates > 0 {
		parts = append(parts, fmt.Sprintf("%d 条可快速直连补齐", sum.WeiboFastCDNCandidates))
	}
	return strings.Join(parts, "，")
}

func formatBackfillSummaryDetail(sum service.BackfillSummaryDTO) string {
	headline := formatBackfillSummaryHeadline(sum)
	detail := formatBackfillSummaryBreakdown(sum)
	if detail == "" {
		return headline
	}
	return fmt.Sprintf("%s（%s）", headline, detail)
}
