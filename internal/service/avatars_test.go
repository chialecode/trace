package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
