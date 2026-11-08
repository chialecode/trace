package ui

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/live"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

type visibleStatusBackend struct {
	service.BackendAPI
	requests [][]int64
	failure  error
	rooms    map[int64]live.RoomState
}

func (b *visibleStatusBackend) LiveStatus(_ context.Context, ids []int64) ([]live.RoomState, error) {
	b.requests = append(b.requests, append([]int64(nil), ids...))
	if b.failure != nil {
		return nil, b.failure
	}
	var rooms []live.RoomState
	for _, id := range ids {
		state := live.RoomState{UID: id, RoomID: id + 10000, LiveStatus: 1}
		if b.rooms != nil {
			state = b.rooms[id]
			state.UID = id
		}
		rooms = append(rooms, state)
	}
	return rooms, nil
}

func TestSubscriptionGridRefreshesOnlyVisibleAccountsAndCachesAttempts(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()
	backend := &visibleStatusBackend{BackendAPI: app.backend}
	app.backend = backend
	for i := int64(1); i <= 1000; i++ {
		app.Subscriptions = append(app.Subscriptions, subscription.Subscription{ID: fmt.Sprintf("person-%d", i), Mid: i, Name: fmt.Sprintf("订阅%d", i), Accounts: []subscription.AccountRef{{Site: "bilibili", UID: fmt.Sprint(i)}}})
	}
	app.Navigate(RouteSubscriptions)
	tester := ui.NewTester(app.View, 720, 560)
	if len(backend.requests) == 0 {
		t.Fatal("newly opened subscription avatars never query their live status")
	}
	queried := make(map[int64]bool)
	for _, ids := range backend.requests {
		if len(ids) > 50 {
			t.Fatal("visible live lookup exceeded one batch")
		}
		for _, id := range ids {
			queried[id] = true
		}
	}
	if len(queried) >= 100 || !queried[1] {
		t.Fatalf("grid queried offscreen subscriptions or skipped visible accounts: %d", len(queried))
	}
	if _, ok := tester.Find("网格开始录制 10001"); !ok {
		t.Fatal("live refresh did not make the one-click recording badge available")
	}
	initial := len(backend.requests)
	for i := 0; i < 10; i++ {
		tester.Frame()
	}
	if len(backend.requests) != initial {
		t.Fatal("visible live status was queried on every repaint")
	}
	app.SubListState.ScrollTo(180, ui.Start)
	tester.Frame()
	if len(backend.requests) <= initial {
		t.Fatal("newly visible avatars after scrolling did not refresh their status")
	}
	for _, ids := range backend.requests[initial:] {
		for _, id := range ids {
			if queried[id] {
				t.Fatalf("scrolling requeried cached account %d", id)
			}
		}
	}
}

func TestSubscriptionGridStopsAutomaticLiveRefreshAfterRiskControl(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()
	backend := &visibleStatusBackend{BackendAPI: app.backend, failure: &bilibili.SiteError{Kind: bilibili.ErrKindRiskControl, Message: "synthetic live challenge"}}
	app.backend = backend
	app.Subscriptions = []subscription.Subscription{{ID: "person-1", Mid: 1, Name: "订阅1", Accounts: []subscription.AccountRef{{Site: "bilibili", UID: "1"}}}}
	app.Navigate(RouteSubscriptions)
	tester := ui.NewTester(app.View, 720, 560)
	if len(backend.requests) != 1 || app.BannerError == "" {
		t.Fatal("live query failure was hidden or no query was made")
	}
	tester.Advance(2 * time.Minute)
	for i := 0; i < 10; i++ {
		tester.Frame()
	}
	if len(backend.requests) != 1 {
		t.Fatal("live status retried a challenge without a manual action")
	}
}

func TestUserLiveFailureIsVisibleOnlyOnItsCurrentPage(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()
	app.backend = &visibleStatusBackend{BackendAPI: app.backend, failure: &bilibili.SiteError{Kind: bilibili.ErrKindRiskControl, Message: "synthetic live challenge"}}
	result := &service.OpenLinkResult{Target: bilibili.ResolveTarget{Kind: "user"}, User: &bilibili.UserSpace{Mid: 42}}
	app.applyParseResult(result)
	if app.BannerError == "" || app.CurrentRoute != RouteUser || app.CurrentUser == nil {
		t.Fatal("a live status failure was hidden or destroyed the loaded user page")
	}
	app.BannerError = ""
	posted := make(chan func(), 1)
	app.dispatch = func(fn func()) { posted <- fn }
	app.applyParseResult(result)
	app.Navigate(RouteLibrary)
	select {
	case apply := <-posted:
		apply()
	case <-time.After(time.Second):
		t.Fatal("live request did not finish")
	}
	if app.BannerError != "" {
		t.Fatal("a late live error changed a page after the user left")
	}
}

type failedSubscriptionListBackend struct{ service.BackendAPI }

func (b failedSubscriptionListBackend) ListSubscriptions() ([]subscription.Subscription, error) {
	return nil, fmt.Errorf("synthetic update index failure")
}

func TestSubscriptionReadFailurePreservesViewAndShowsReason(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()
	app.backend = failedSubscriptionListBackend{app.backend}
	app.Subscriptions = []subscription.Subscription{{ID: "retained", Name: "retained subscription"}}
	app.RefreshData()
	if len(app.Subscriptions) != 1 || app.BannerError == "" {
		t.Fatal("failed subscription refresh cleared the view and hid its error")
	}
}
