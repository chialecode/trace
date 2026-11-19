package ui

import (
	"testing"

	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

// A post's avatar on a subscription's feed opens the subscription; leaving
// Home closes the feed and replaces the timeline's list state while that
// list's rows are still building. The list being built keeps its own state,
// so the frame finishes instead of dereferencing a cleared one (dev crash
// 2026-11-18 in ui.listInput).
func TestHomeAvatarClickInSubscriptionFeedDoesNotCrash(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 12)
	sub, err := app.backend.CreateSubscription("synthetic author", "", []subscription.AccountRef{{Site: "bilibili", UID: "1001", Name: "synthetic author"}}, subscription.SubRules{})
	if err != nil {
		t.Fatal(err)
	}
	app.refreshSubscriptions()
	app.openHomeActorFeed(HomeUpdatedActor{Key: "sub:" + sub.ID, SubID: sub.ID, Mid: 1001, Name: "synthetic author", Authors: []int64{1001}})
	if app.HomeActorFilter == nil || len(app.FeedItems) == 0 {
		t.Fatal("the subscription's feed did not open")
	}
	tt := ui.NewTester(app.View, 1000, 860)
	settle(tt, 2)
	avatar, ok := tt.Find("作者头像：" + app.FeedItems[0].FeedKey)
	if !ok {
		t.Fatal("post avatar not found")
	}
	tt.ClickAt(avatar.X+avatar.W/2, avatar.Y+avatar.H/2)
	settle(tt, 2)
	if app.CurrentRoute != RouteSubscriptions || !app.SubDetailOpen || app.SelectedSubID != sub.ID {
		t.Fatalf("the avatar opened %s (detail %v, sub %q), want the subscription", app.CurrentRoute, app.SubDetailOpen, app.SelectedSubID)
	}
	if app.HomeActorFilter != nil {
		t.Fatal("leaving Home kept the subscription's feed")
	}
}
