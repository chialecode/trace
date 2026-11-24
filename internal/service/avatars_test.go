package service

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/gif"
	"net/http"
	"net/http/httptest"
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

func TestStaleLocalAvatarAndHistoryThumbNeverOverwrittenByExpiredPlaceholder(t *testing.T) {
	h := newWeiboThreeSiteHarness(t)
	ctx := context.Background()
	if _, err := h.backend.LoginWithCookie(ctx, "weibo", synthWeiboCookie(), ""); err != nil {
		t.Fatalf("weibo login: %v", err)
	}

	// Build a synthetic Sina 480x360 grayscale deleted-image placeholder GIF served on a local server.
	palette := color.Palette{
		color.RGBA{R: 240, G: 240, B: 240, A: 255},
		color.RGBA{R: 120, G: 120, B: 120, A: 255},
	}
	phImg := image.NewPaletted(image.Rect(0, 0, 480, 360), palette)
	var phBuf bytes.Buffer
	if err := gif.Encode(&phBuf, phImg, nil); err != nil {
		t.Fatal(err)
	}
	placeholderGIF := phBuf.Bytes()

	deadSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/placeholder.jpg") {
			w.Header().Set("Content-Type", "image/gif")
			_, _ = w.Write(placeholderGIF)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer deadSrv.Close()

	// 1. Seed a valid Weibo subscription avatar, backdate ModTime past 7 days, and refresh against placeholder/404.
	validFace := h.wbSrv.URL + "/cdn/avatar_creator.png"
	if _, err := h.backend.CreateSubscription("合成微博创作者", "", []subscription.AccountRef{{
		Site: "weibo",
		UID:  synthWeiboUID,
		Name: synthWeiboName,
		Face: validFace,
	}}, subscription.DefaultSubRules()); err != nil {
		t.Fatalf("CreateSubscription(weibo): %v", err)
	}

	var avatarPath string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline) && avatarPath == ""; time.Sleep(20 * time.Millisecond) {
		avatarPath = h.backend.AccountAvatarPath("weibo", synthWeiboUID)
	}
	if avatarPath == "" {
		t.Fatal("initial Weibo local avatar was not saved")
	}
	origAvatarBytes, err := os.ReadFile(avatarPath)
	if err != nil || len(origAvatarBytes) == 0 {
		t.Fatalf("read initial avatar: err=%v len=%d", err, len(origAvatarBytes))
	}

	staleTime := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(avatarPath, staleTime, staleTime); err != nil {
		t.Fatalf("Chtimes(avatarPath): %v", err)
	}

	// Trigger background avatar refresh with both Sina placeholder GIF and HTTP 404.
	h.backend.rememberAccountAvatar("weibo", synthWeiboUID, deadSrv.URL+"/placeholder.jpg")
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if _, busy := h.backend.avatarInflight.Load(filepath.Base(avatarPath)); !busy {
			break
		}
	}
	h.backend.rememberAccountAvatar("weibo", synthWeiboUID, deadSrv.URL+"/missing404.jpg")
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if _, busy := h.backend.avatarInflight.Load(filepath.Base(avatarPath)); !busy {
			break
		}
	}

	afterAvatarBytes, err := os.ReadFile(avatarPath)
	if err != nil || !bytes.Equal(afterAvatarBytes, origAvatarBytes) {
		t.Fatalf("stale valid local avatar was overwritten or deleted by expired placeholder/404: err=%v len=%d", err, len(afterAvatarBytes))
	}
	if got := h.backend.AccountAvatarPath("weibo", synthWeiboUID); got != avatarPath {
		t.Fatalf("AccountAvatarPath after expired refresh = %q, want %q", got, avatarPath)
	}

	// 2. Seed valid history cover & avatar thumbs via OpenLink + FetchHistoryThumbs, then re-run FetchHistoryThumbs with placeholder/404 URLs.
	res, err := h.backend.OpenLink(ctx, "https://weibo.com/"+synthWeiboUID+"/"+synthWeiboMidPlain)
	if err != nil || res.HistoryThumbs == nil {
		t.Fatalf("OpenLink(weibo): res=%+v err=%v", res, err)
	}
	if err := h.backend.FetchHistoryThumbs(ctx, *res.HistoryThumbs); err != nil {
		t.Fatalf("FetchHistoryThumbs(initial): %v", err)
	}
	histBefore := h.backend.ListHistory()
	if len(histBefore) == 0 || histBefore[0].CoverThumb == "" {
		t.Fatalf("expected initial CoverThumb in history: %+v", histBefore)
	}
	origCoverName := histBefore[0].CoverThumb
	origCoverPath, err := h.backend.HistoryThumbPath(origCoverName)
	if err != nil {
		t.Fatalf("HistoryThumbPath: %v", err)
	}
	origCoverBytes, err := os.ReadFile(origCoverPath)
	if err != nil || len(origCoverBytes) == 0 {
		t.Fatalf("read initial history cover thumb: %v", err)
	}

	expiredJob := *res.HistoryThumbs
	expiredJob.CoverURL = deadSrv.URL + "/placeholder.jpg"
	expiredJob.AvatarURL = deadSrv.URL + "/missing404.jpg"
	if err := h.backend.FetchHistoryThumbs(ctx, expiredJob); err != nil {
		t.Fatalf("FetchHistoryThumbs(expired placeholder): %v", err)
	}
	histAfter := h.backend.ListHistory()
	if len(histAfter) == 0 || histAfter[0].CoverThumb != origCoverName {
		t.Fatalf("history CoverThumb degraded after expired FetchHistoryThumbs: got %+v, want %q", histAfter, origCoverName)
	}
	afterCoverBytes, err := os.ReadFile(origCoverPath)
	if err != nil || !bytes.Equal(afterCoverBytes, origCoverBytes) {
		t.Fatalf("existing history cover thumb file overwritten or deleted: err=%v", err)
	}
}
