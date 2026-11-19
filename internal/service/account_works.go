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
	seen := map[string]bool{}
	var due []fullCacheCandidate
	for _, s := range subs {
		if !s.Rules.FullCache {
			continue
		}
		for _, acc := range s.Accounts {
			siteID := site.ID(strings.ToLower(strings.TrimSpace(acc.Site)))
			uid := strings.TrimSpace(acc.UID)
			key := string(siteID) + ":" + uid
			if uid == "" || seen[key] || (siteID != site.Bilibili && siteID != site.Douyin) {
				continue
			}
			seen[key] = true
			var last time.Time
			if w, ok := stored[key]; ok && w.FullCacheAt != "" {
				last, _ = time.Parse(time.RFC3339, w.FullCacheAt)
			}
			if !last.IsZero() && now.Sub(last) < subscription.FullCacheInterval {
				continue
			}
			due = append(due, fullCacheCandidate{siteID: siteID, acc: acc, last: last})
		}
	}
	sort.SliceStable(due, func(i, j int) bool { return due[i].last.Before(due[j].last) })
	var started []string
	for _, c := range due {
		if busy[c.siteID] || b.requireSiteLogin(c.siteID) != nil {
			continue
		}
		list := "videos"
		if c.siteID == site.Douyin {
			list = "works"
			b.refreshDouyinWorksTotal(ctx, c.acc.UID, now)
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
	}
	return started
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
