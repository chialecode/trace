package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/subscription"
)

// A rule with 全部缓存 starts one background cache queue per site at a time
// for its subscriptions' accounts, least recently swept first, and does not
// sweep an account again within FullCacheInterval (DEC-061). The works total
// stays unknown until a parse records it; reading it sends no request.
func TestFullCacheRuleSweepsAccountsOnePerSite(t *testing.T) {
	h := newQueueDualHarness(t, 3, 3, 3, 3)
	b, cleanup := h.openBackend(t, func(n int) int { return 0 })
	defer cleanup()
	ctx := context.Background()
	if _, err := b.ImportCookie(ctx, "SESSDATA=synth_bili_fc; bili_jct=csrf_fc; DedeUserID=778899", ""); err != nil {
		t.Fatalf("ImportCookie: %v", err)
	}

	accounts := []subscription.AccountRef{
		{Site: "bilibili", UID: "4001", Name: "成员一"},
		{Site: "bilibili", UID: "4002", Name: "成员二"},
	}
	sub, err := b.CreateSubscription("合作", "", accounts, subscription.DefaultSubRules())
	if err != nil {
		t.Fatal(err)
	}
	other, err := b.CreateSubscription("不缓存", "", []subscription.AccountRef{{Site: "bilibili", UID: "4003", Name: "旁人"}}, subscription.DefaultSubRules())
	if err != nil {
		t.Fatal(err)
	}
	_ = other
	rule, err := b.SaveCacheRule(subscription.CacheRule{Name: "全部存", ScheduledCheck: true, FullCache: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(subscription.RuleSummary(rule), "全部缓存") {
		t.Fatalf("summary = %q", subscription.RuleSummary(rule))
	}
	if sub, err = b.SetSubscriptionRule(sub.ID, rule.ID); err != nil || !sub.Rules.FullCache {
		t.Fatalf("rule not applied: %+v %v", sub.Rules, err)
	}

	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	started := b.startDueFullCacheSweeps(ctx, now)
	if len(started) != 1 {
		t.Fatalf("first pass started %v, want one Bilibili sweep", started)
	}
	queues := b.ListCacheQueue()
	if len(queues) != 1 || queues[0].Origin != QueueOriginFullCache || queues[0].List != "videos" || !strings.HasPrefix(queues[0].ListLabel, "定期全部缓存") {
		t.Fatalf("queues = %+v", queues)
	}
	first := queues[0].User
	if first != "4001" && first != "4002" {
		t.Fatalf("swept %s, which is not on the rule", first)
	}
	if again := b.startDueFullCacheSweeps(ctx, now.Add(time.Hour)); len(again) != 0 {
		t.Fatalf("started %v while the site still has a queue", again)
	}

	cancelAndRemoveQueue(t, b, queues[0].ID)
	next := b.startDueFullCacheSweeps(ctx, now.Add(2*time.Hour))
	queues = b.ListCacheQueue()
	if len(next) != 1 || len(queues) != 1 || queues[0].User == first || queues[0].User == "4003" {
		t.Fatalf("second pass = %v %+v, want the other member", next, queues)
	}
	cancelAndRemoveQueue(t, b, queues[0].ID)
	if none := b.startDueFullCacheSweeps(ctx, now.Add(3*24*time.Hour)); len(none) != 0 {
		t.Fatalf("swept again within the interval: %v", none)
	}
	if due := b.startDueFullCacheSweeps(ctx, now.Add(subscription.FullCacheInterval+time.Hour)); len(due) != 1 {
		t.Fatalf("no sweep after the interval: %v", due)
	}

	works := b.SubscriptionAccountWorks()
	w, ok := works["bilibili:4001"]
	if !ok || w.Total != -1 || w.FullCacheAt == "" || w.Cached != 0 {
		t.Fatalf("works before a parse = %+v (ok=%v)", w, ok)
	}
	if _, ok := works["bilibili:4003"]; !ok {
		t.Fatal("an account without the rule still shows its numbers")
	}
	b.recordAccountWorks("bilibili", "4003", 12)
	if w := b.SubscriptionAccountWorks()["bilibili:4003"]; w.Total != 12 || w.TotalAt == "" {
		t.Fatalf("recorded total = %+v", w)
	}
}

func cancelAndRemoveQueue(t *testing.T, b *Backend, id string) {
	t.Helper()
	if _, err := b.CancelCacheQueue(id); err != nil {
		t.Fatal(err)
	}
	if err := b.RemoveCacheQueue(id); err != nil {
		t.Fatal(err)
	}
}
