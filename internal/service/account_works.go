package service

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/subscription"
)

// AccountWorks is shown for each subscribed account (订阅 › 全部账号 and a
// subscription's 账号 tab, DEC-061): the works total from the last parse of
// the account (-1 when never parsed) and how many of them are cached. Reading
// it sends no site request.
type AccountWorks struct {
	Total   int    `json:"total"`
	TotalAt string `json:"totalAt,omitempty"`
	Cached  int    `json:"cached"`
	// FullCacheAt is when a full-cache rule last swept the account.
	FullCacheAt string `json:"fullCacheAt,omitempty"`
}

// fullCacheSweepEvery is how often the backend looks for accounts due a
// full-cache sweep; the sweep itself runs at most once per FullCacheInterval.
const fullCacheSweepEvery = 10 * time.Minute

// recordAccountWorks keeps an account's works total from a parse of it.
func (b *Backend) recordAccountWorks(siteID site.ID, uid string, total int) {
	if b == nil || b.subs == nil {
		return
	}
	if err := b.subs.RecordAccountWorks(string(siteID), uid, total); err != nil {
		b.logError("RecordAccountWorks", err)
	}
}

// SubscriptionAccountWorks returns AccountWorks for every subscribed
// account, keyed "site:uid".
func (b *Backend) SubscriptionAccountWorks() map[string]AccountWorks {
	out := map[string]AccountWorks{}
	if b.requireAnyLogin() != nil || b.subs == nil || b.lib == nil {
		return out
	}
	subs, err := b.subs.List()
	if err != nil {
		return out
	}
	var refs []library.AuthorRef
	for _, s := range subs {
		for _, acc := range s.Accounts {
			refs = append(refs, library.AuthorRef{Site: strings.ToLower(strings.TrimSpace(acc.Site)), UID: strings.TrimSpace(acc.UID)})
		}
	}
	stored, err := b.subs.ListAccountWorks()
	if err != nil {
		b.logError("ListAccountWorks", err)
	}
	cached, err := b.lib.CountCachedWorks(refs)
	if err != nil {
		b.logError("CountCachedWorks", err)
	}
	for _, r := range refs {
		key := r.Site + ":" + r.UID
		w := AccountWorks{Total: -1, Cached: cached[key]}
		if s, ok := stored[key]; ok {
			w.Total, w.TotalAt, w.FullCacheAt = s.Total, s.TotalAt, s.FullCacheAt
		}
		out[key] = w
	}
	return out
}

func (b *Backend) runFullCacheLoop(ctx context.Context) {
	defer b.bgWg.Done()
	wait := 3 * time.Minute
	for {
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		b.startDueFullCacheSweeps(ctx, time.Now())
		wait = fullCacheSweepEvery
	}
}

type fullCacheCandidate struct {
	siteID site.ID
	acc    subscription.AccountRef
	last   time.Time
}

// startDueFullCacheSweeps starts the background cache queue for accounts of
// subscriptions whose rule has 全部缓存 and whose last sweep is at least
// FullCacheInterval old, least recently swept first (DEC-061). A site starts
// one sweep at a time and only while it has no other unfinished cache queue,
// so sweeps never run alongside each other or a queue the user started. The
// queue itself walks the works newest first, skips what is cached and caches
// the rest slowly. It returns the queue IDs it started.
func (b *Backend) startDueFullCacheSweeps(ctx context.Context, now time.Time) []string {
	if b.profile == ProfileRemote || b.subs == nil || b.queue == nil || b.requireAnyLogin() != nil {
		return nil
	}
	subs, err := b.subs.List()
	if err != nil {
		return nil
	}
	stored, _ := b.subs.ListAccountWorks()
	busy := map[site.ID]bool{}
	for _, t := range b.ListCacheQueue() {
		if isUnfinishedQueueStatus(t.Status) {
			busy[site.ID(t.Site)] = true
		}
	}
	seen := map[string]int{}
	var due []fullCacheCandidate
	biliHasDynRule := map[string]bool{}
	var dueBiliUIDs []string
	for _, s := range subs {
		if !s.Rules.FullCache {
			continue
		}
		dynRule := subscriptionHasBilibiliDynamicAutoCache(s)
		for _, acc := range s.Accounts {
			siteID := site.ID(strings.ToLower(strings.TrimSpace(acc.Site)))
			uid := strings.TrimSpace(acc.UID)
			key := string(siteID) + ":" + uid
			if uid == "" || !siteID.Valid() {
				continue
			}
			if siteID == site.Bilibili && dynRule {
				_, normUID := library.NormalizeAuthorUID("bilibili", uid, 0)
				if normUID != "" {
					biliHasDynRule[normUID] = true
				}
			}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = len(due)
			var last time.Time
			if w, ok := stored[key]; ok && w.FullCacheAt != "" {
				last, _ = time.Parse(time.RFC3339, w.FullCacheAt)
			}
			if !last.IsZero() && now.Sub(last) < subscription.FullCacheInterval {
				continue
			}
			if siteID == site.Bilibili {
				dueBiliUIDs = append(dueBiliUIDs, uid)
			}
			due = append(due, fullCacheCandidate{siteID: siteID, acc: acc, last: last})
		}
	}
	var cachedBiliDynUIDs map[string]bool
	if b.lib != nil && len(dueBiliUIDs) > 0 {
		if m, err := b.lib.CachedBilibiliDynamicUIDs(dueBiliUIDs); err == nil {
			cachedBiliDynUIDs = m
		}
	}
	sort.SliceStable(due, func(i, j int) bool { return due[i].last.Before(due[j].last) })
	var started []string
	for _, c := range due {
		if busy[c.siteID] || b.requireSiteLogin(c.siteID) != nil {
			continue
		}
		var list string
		switch c.siteID {
		case site.Bilibili:
			list = "videos"
		case site.Douyin:
			list = "works"
			b.refreshDouyinWorksTotal(ctx, c.acc.UID, now)
		case site.Weibo:
			list = "posts"
			b.refreshWeiboWorksTotal(ctx, c.acc.UID, now)
		case site.X:
			list = "posts"
			b.refreshXWorksTotal(ctx, c.acc.UID, now)
		default:
			continue
		}
		task, err := b.EnqueueCacheQueue(ctx, CacheQueueRequest{
			Site:      string(c.siteID),
			List:      list,
			User:      c.acc.UID,
			UserName:  c.acc.Name,
			AvatarURL: c.acc.Face,
			Origin:    QueueOriginFullCache,
		})
		if err != nil {
			b.logError("FullCacheSweep", err)
			continue
		}
		busy[c.siteID] = true
		if err := b.subs.MarkFullCacheSweep(string(c.siteID), c.acc.UID, now); err != nil {
			b.logError("MarkFullCacheSweep", err)
		}
		started = append(started, task.ID)

		// DEC-096 Scope Decision #1: for Bilibili accounts with any dynamic
		// auto-cache tag configured OR existing local cached dynamics, also enqueue
		// a periodic fullCache dynamics sweep behind videos on the same site queue.
		if c.siteID == site.Bilibili {
			_, normUID := library.NormalizeAuthorUID("bilibili", c.acc.UID, 0)
			if normUID != "" && (biliHasDynRule[normUID] || cachedBiliDynUIDs[normUID]) {
				dynTask, dynErr := b.EnqueueCacheQueue(ctx, CacheQueueRequest{
					Site:      string(site.Bilibili),
					List:      "dynamics",
					User:      c.acc.UID,
					UserName:  c.acc.Name,
					AvatarURL: c.acc.Face,
					Origin:    QueueOriginFullCache,
				})
				if dynErr != nil {
					b.logError("FullCacheSweep.BilibiliDynamics", dynErr)
				} else {
					started = append(started, dynTask.ID)
				}
			}
		}
	}
	return started
}

func subscriptionHasBilibiliDynamicAutoCache(s subscription.Subscription) bool {
	for _, tag := range s.Tags {
		switch strings.ToLower(strings.TrimSpace(tag)) {
		case subscription.TagCacheDraw, subscription.TagCacheWord, subscription.TagCacheForward:
			return true
		}
	}
	if s.Rules.AutoCache.Enabled {
		for _, typ := range s.Rules.AutoCache.Types {
			switch strings.ToLower(strings.TrimSpace(typ)) {
			case "draw", "word", "forward", "dynamic":
				return true
			}
		}
	}
	return false
}

// refreshDouyinWorksTotal reads a Douyin account's works total for its sweep
// (the works list does not report one). It is an automatic request: it is
// skipped while the site is backing off or out of its hourly allowance.
func (b *Backend) refreshDouyinWorksTotal(ctx context.Context, secUID string, now time.Time) {
	if b.douyin == nil || b.queue == nil {
		return
	}
	if blocked, _ := b.queue.siteBackoffOrQuotaEnd(site.Douyin, now); blocked {
		return
	}
	prof, err := b.douyin.GetUserProfile(site.WithAutomaticRequests(ctx, "full_cache"), secUID)
	if err != nil {
		b.logError("FullCacheSweep.Profile", err)
		return
	}
	b.recordAccountWorks(site.Douyin, firstNonEmpty(prof.SecUID, secUID), prof.AwemeCount)
}

// refreshWeiboWorksTotal reads a Weibo account's statuses_count before its periodic
// full-cache sweep while honoring Weibo's backoff and hourly quota.
func (b *Backend) refreshWeiboWorksTotal(ctx context.Context, uid string, now time.Time) {
	if b.weibo == nil || b.queue == nil {
		return
	}
	if blocked, _ := b.queue.siteBackoffOrQuotaEnd(site.Weibo, now); blocked {
		return
	}
	prof, err := b.weibo.GetUserProfile(site.WithAutomaticRequests(ctx, "full_cache"), uid)
	if err != nil {
		b.logError("FullCacheSweep.WeiboProfile", err)
		return
	}
	b.recordAccountWorks(site.Weibo, firstNonEmpty(prof.UID, uid), prof.StatusesCount)
}

// refreshXWorksTotal reads an X account's statuses_count before its periodic
// full-cache sweep while honoring X's backoff and hourly quota.
func (b *Backend) refreshXWorksTotal(ctx context.Context, restID string, now time.Time) {
	if b.x == nil || b.queue == nil {
		return
	}
	if blocked, _ := b.queue.siteBackoffOrQuotaEnd(site.X, now); blocked {
		return
	}
	prof, err := b.x.GetUserByRestId(site.WithAutomaticRequests(ctx, "full_cache"), restID)
	if err != nil {
		b.logError("FullCacheSweep.XProfile", err)
		return
	}
	b.recordAccountWorks(site.X, firstNonEmpty(prof.RestID, restID), prof.StatusesCount)
}
