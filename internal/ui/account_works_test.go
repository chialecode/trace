package ui

import (
	"testing"

	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

type fixedAccountWorksBackend struct {
	service.BackendAPI
	works map[string]service.AccountWorks
	calls int
}

func (b *fixedAccountWorksBackend) SubscriptionAccountWorks() map[string]service.AccountWorks {
	b.calls++
	return b.works
}

// 订阅 › 全部账号 and a subscription's 账号 tab show each account's works
// total (from its last parse) and cached count (DEC-061).
func TestSubscribedAccountsShowWorksNumbers(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)
	sub, err := app.backend.CreateSubscription("合作", "", []subscription.AccountRef{
		{Site: "bilibili", UID: "4001", Name: "成员一"},
		{Site: "bilibili", UID: "4002", Name: "成员二"},
	}, subscription.DefaultSubRules())
	if err != nil {
		t.Fatal(err)
	}
	backend := &fixedAccountWorksBackend{BackendAPI: app.backend, works: map[string]service.AccountWorks{
		"bilibili:4001": {Total: 12, TotalAt: "2026-11-18T12:00:00Z", Cached: 3},
		"bilibili:4002": {Total: -1, Cached: 5},
	}}
	app.backend = backend
	app.refreshSubscriptions()
	app.Navigate(RouteSubscriptions)
	app.openSubscriptionAccounts()
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	if !tt.HasText("作品 12 · 已缓存 3") || !tt.HasText("已缓存 5 · 作品总数在打开主页后统计") {
		t.Fatalf("全部账号 does not show the works numbers: %v", tt.Texts())
	}

	app.OpenSubscriptionDetail(sub)
	app.setSubDetailTab("accounts")
	tt.Frame()
	if !tt.HasText("作品 12 · 已缓存 3") || !tt.HasText("关联账号（2 个）") {
		t.Fatalf("账号 tab does not show the works numbers: %v", tt.Texts())
	}
}

// A video opened from a creator's page is cached for that creator, as its
// row's "缓存" is; opened elsewhere it is cached for its uploader (DEC-061).
func TestVideoPageCachesForCreatorPageItCameFrom(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)
	app.openVideoFor("BV1xx411c7mD", "视频", "成员", 4001, "", "", 4001)
	if got := app.cacheForMid("BV1xx411c7mD"); got != 4001 {
		t.Fatalf("cacheForMid = %d, want the creator page", got)
	}
	if got := app.cacheForMid("BV1other"); got != 0 {
		t.Fatalf("another video is cached for %d", got)
	}
	app.OpenVideo("BV1xx411c7mD", "视频", "UP", 8888, "", "")
	if got := app.cacheForMid("BV1xx411c7mD"); got != 0 {
		t.Fatalf("a video opened from a card is cached for %d", got)
	}
}
