package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/subscription"
)

const avatarTestSecUID = "MS4wLjABAAAA_synth_creator_acct_000002"

// A Douyin avatar URL has a query string and is never stored with the
// subscription, so the avatar used to vanish at the next start. A subscribed
// Douyin account keeps a local PNG instead, without its URL, and loses it
// with the subscription.
func TestDouyinSubscriptionKeepsALocalAvatar(t *testing.T) {
	h := newDualSiteTestHarness(t)
	ctx := context.Background()
	if _, err := h.backend.LoginWithCookie(ctx, "douyin", "sessionid=SYNTH_DY_SESSION_01; sid_guard=SYNTH_SID_GUARD_01; ttwid=SYNTH_TTWID_01", ""); err != nil {
		t.Fatalf("login: %v", err)
	}
	face := h.dySrv.URL + "/obj/avatar.png?from=2956013662&x-expires=SYNTH_AVATAR_EXPIRES"
	sub, err := h.backend.CreateSubscription("抖音作者", "", []subscription.AccountRef{{Site: "douyin", UID: avatarTestSecUID, Name: "抖音作者", Face: face}}, subscription.SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}
	var path string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline) && path == ""; time.Sleep(20 * time.Millisecond) {
		path = h.backend.AccountAvatarPath("douyin", avatarTestSecUID)
	}
	if path == "" {
		t.Fatal("no local avatar after subscribing")
	}
	if filepath.Dir(path) != filepath.Join(h.loc.DataDir, "avatars") || strings.Contains(filepath.Base(path), avatarTestSecUID) {
		t.Fatalf("avatar stored at %s", path)
	}
	raw, _ := os.ReadFile(filepath.Join(h.loc.DataDir, "subscriptions.json"))
	if strings.Contains(string(raw), "SYNTH_AVATAR_EXPIRES") || strings.Contains(string(raw), "/obj/avatar.png") {
		t.Fatal("the avatar URL was written to subscriptions.json")
	}
	// An unrelated file in the folder is never removed.
	other := filepath.Join(filepath.Dir(path), "notes.txt")
	if err := os.WriteFile(other, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.backend.DeleteSubscription(sub.ID); err != nil {
		t.Fatalf("DeleteSubscription: %v", err)
	}
	if h.backend.AccountAvatarPath("douyin", avatarTestSecUID) != "" {
		t.Fatal("the avatar outlived its subscription")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("pruning removed a file TRACE did not write")
	}
}

// A Bilibili subscription made from the user page's 订阅 button carries only
// Mid and Face; it keeps a local avatar too, so the grid does not fall back to
// a letter (2026-11-15 feedback: new subscriptions had no avatar).
func TestBilibiliUserPageSubscriptionKeepsALocalAvatar(t *testing.T) {
	h := newDualSiteTestHarness(t)
	if _, err := h.backend.ImportCookie(context.Background(), "SESSDATA=s; bili_jct=j; DedeUserID=778899", ""); err != nil {
		t.Fatalf("login: %v", err)
	}
	sub, err := h.backend.UpsertSubscription(subscription.Subscription{
		Site: "bilibili", Mid: 12345, Uname: "合成UP主", Name: "合成UP主",
		Face: h.biliSrv.URL + "/bfs/face/up.png", Scope: "both", AutoCache: "inherit", Enabled: true,
	})
	if err != nil {
		t.Fatalf("UpsertSubscription: %v", err)
	}
	if len(sub.Accounts) != 1 || sub.Accounts[0].Face == "" {
		t.Fatalf("the first account should carry the avatar URL: %+v", sub.Accounts)
	}
	var path string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline) && path == ""; time.Sleep(20 * time.Millisecond) {
		path = h.backend.AccountAvatarPath("bilibili", "12345")
	}
	if path == "" {
		t.Fatal("no local avatar after subscribing from the user page")
	}
}

// Planning / caching a co-authored Douyin work whose publisher is NOT in
// subscriptions.json saves the publisher's local avatar PNG; pruneAccountAvatars
// preserves it while the item exists in library.db and removes it once deleted.
func TestCoAuthorDouyinWorkPlannerCachesAndPruneRetainsAvatar(t *testing.T) {
	h := newDualSiteTestHarness(t)
	ctx := context.Background()
	if _, err := h.backend.LoginWithCookie(ctx, "douyin", "sessionid=SYNTH_DY_SESSION_01; sid_guard=SYNTH_SID_GUARD_01; ttwid=SYNTH_TTWID_01", ""); err != nil {
		t.Fatalf("login: %v", err)
	}

	const subscribedSecUID = "MS4wLjABAAAA_synth_subscribed_collab_09"
	if _, err := h.backend.CreateSubscription("订阅共创者", "", []subscription.AccountRef{
		{Site: "douyin", UID: subscribedSecUID, Name: "订阅共创者"},
	}, subscription.DefaultSubRules()); err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}
	if _, owned := h.backend.OwnerOfAccount("douyin", avatarTestSecUID); owned {
		t.Fatalf("publisher %s must NOT be subscribed in this test", avatarTestSecUID)
	}

	// Plan a Douyin work published by non-subscribed co-author avatarTestSecUID.
	planner := &douyinWorkPlanner{backend: h.backend}
	plan, err := planner.PlanWorkDownload(ctx, "7350000000000000101", download.WorkPlanOptions{})
	if err != nil {
		t.Fatalf("PlanWorkDownload: %v", err)
	}
	if plan.AuthorUID != avatarTestSecUID {
		t.Fatalf("plan.AuthorUID = %q, want %q", plan.AuthorUID, avatarTestSecUID)
	}

	var path string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline) && path == ""; time.Sleep(20 * time.Millisecond) {
		path = h.backend.AccountAvatarPath("douyin", avatarTestSecUID)
	}
	if path == "" {
		t.Fatal("PlanWorkDownload did not cache local avatar PNG for non-subscribed co-author")
	}
	if filepath.Dir(path) != filepath.Join(h.loc.DataDir, "avatars") {
		t.Fatalf("unexpected avatar dir: %s", path)
	}

	// Persist a co-authored item under subscribedSecUID's folder with Author = avatarTestSecUID.
	itemDir := filepath.Join(h.backend.lib.CacheDir(), "douyin", "collab_folder", "works", "7350000000000000101")
	if err := os.MkdirAll(itemDir, 0o700); err != nil {
		t.Fatal(err)
	}
	saved, err := h.backend.lib.SaveItem(itemDir, library.ItemMetadata{
		Version: 1,
		Site:    "douyin",
		Kind:    "video",
		ID:      "7350000000000000101",
		Status:  "complete",
		Title:   "共创作品测试",
		PubTs:   1711000100,
		Author: library.ItemAuthor{
			Site: "douyin",
			UID:  avatarTestSecUID,
			Name: "合成抖音创作者",
		},
		Via: &library.ItemVia{
			Reason: library.ViaCollab,
			Site:   "douyin",
			UID:    subscribedSecUID,
			Name:   "订阅共创者",
		},
	})
	if err != nil {
		t.Fatalf("SaveItem: %v", err)
	}

	// Prune must keep the non-subscribed co-author's avatar while the library item exists.
	h.backend.pruneAccountAvatars()
	if got := h.backend.AccountAvatarPath("douyin", avatarTestSecUID); got == "" {
		t.Fatal("pruneAccountAvatars deleted co-author avatar while item still exists in library.db")
	}

	// Deleting the library item leaves no subscription/queue/library reference -> avatar is pruned.
	if err := h.backend.DeleteLibraryItem(saved.ItemKey, true); err != nil {
		t.Fatalf("DeleteLibraryItem: %v", err)
	}
	if got := h.backend.AccountAvatarPath("douyin", avatarTestSecUID); got != "" {
		t.Fatalf("expected co-author avatar pruned after deleting item, still at %s", got)
	}
}

// Opening a Douyin user page via DouyinUser caches the creator's local avatar PNG.
func TestDouyinUserRemembersLocalAvatar(t *testing.T) {
	h := newDualSiteTestHarness(t)
	ctx := context.Background()
	if _, err := h.backend.LoginWithCookie(ctx, "douyin", "sessionid=SYNTH_DY_SESSION_01; sid_guard=SYNTH_SID_GUARD_01; ttwid=SYNTH_TTWID_01", ""); err != nil {
		t.Fatalf("login: %v", err)
	}
	const userOnlySecUID = "MS4wLjABAAAA_synth_user_page_only_03"
	uv, err := h.backend.DouyinUser(ctx, userOnlySecUID)
	if err != nil || uv == nil || uv.SecUID != userOnlySecUID {
		t.Fatalf("DouyinUser: uv=%+v err=%v", uv, err)
	}
	var path string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline) && path == ""; time.Sleep(20 * time.Millisecond) {
		path = h.backend.AccountAvatarPath("douyin", userOnlySecUID)
	}
	if path == "" {
		t.Fatal("DouyinUser did not save local avatar PNG")
	}
}
