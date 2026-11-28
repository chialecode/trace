package ui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/player"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

type failingMoveBackend struct {
	service.BackendAPI
	moveErr      error
	moveToNewErr error
}

func (f *failingMoveBackend) MoveSubscriptionAccount(siteID, uid, toSubID string) (subscription.Subscription, error) {
	if f.moveErr != nil {
		return subscription.Subscription{}, f.moveErr
	}
	return f.BackendAPI.MoveSubscriptionAccount(siteID, uid, toSubID)
}

func (f *failingMoveBackend) MoveSubscriptionAccountToNew(siteID, uid, name, note string, rules subscription.SubRules) (subscription.Subscription, error) {
	if f.moveToNewErr != nil {
		return subscription.Subscription{}, f.moveToNewErr
	}
	return f.BackendAPI.MoveSubscriptionAccountToNew(siteID, uid, name, note, rules)
}

func newMoveTestEnv(t *testing.T) (*App, *service.Backend, *atomic.Int32, func()) {
	t.Helper()
	var siteHTTP atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/x/web-interface/nav", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"code":0,"data":{"isLogin":true,"mid":12345,"uname":"测试用户"}}`)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		siteHTTP.Add(1)
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(mux)

	root := t.TempDir()
	loc := appdirs.Locations{
		Channel:         "dev",
		Mode:            "dev",
		AppName:         "trace-dev",
		DisplayName:     "TRACE Dev",
		DataDir:         filepath.Join(root, "data"),
		DefaultCacheDir: filepath.Join(root, "cache"),
		CredentialsDir:  filepath.Join(root, "data", "credentials"),
		LogsDir:         filepath.Join(root, "data", "logs"),
		LockFile:        filepath.Join(root, "data", "app.lock"),
	}
	raw, err := service.NewBackend(service.Options{
		Locations: loc,
		BilibiliOpts: bilibili.ClientOptions{
			Endpoints: bilibili.Endpoints{
				APIBase:      srv.URL,
				PassportBase: srv.URL,
				WWWBase:      srv.URL,
				SpaceBase:    srv.URL,
				DynamicBase:  srv.URL,
				LiveAPIBase:  srv.URL,
				LiveWebBase:  srv.URL,
			},
			PacingInterval: time.Millisecond,
			AllowLocalhost: true,
		},
		Desktop:          service.NewMockDesktop(),
		DisableBgWorkers: true,
	})
	if err != nil {
		srv.Close()
		t.Fatal(err)
	}
	if _, err := raw.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		_ = raw.Close()
		srv.Close()
		t.Fatal(err)
	}

	app := NewApp(raw, player.NewNullAudioSink())
	cleanup := func() {
		app.Close()
		_ = raw.Close()
		srv.Close()
	}
	return app, raw, &siteHTTP, cleanup
}

func TestMoveAccountFromSubscriptionDetailAllThreeSitesZeroNetwork(t *testing.T) {
	app, raw, siteHTTP, cleanup := newMoveTestEnv(t)
	defer cleanup()

	dyUID := "MS4wLjABAAAA__UIMove_01"
	wbUID := "5000001234"

	subSrc, err := raw.CreateSubscription("原混合订阅", "原订阅备注", []subscription.AccountRef{
		{Site: "bilibili", UID: "7001", Name: "B站创作者甲"},
		{Site: "douyin", UID: dyUID, UniqueID: "dy_ui_alpha", WebRID: "889900", Name: "抖音创作者甲"},
		{Site: "weibo", UID: wbUID, Name: "微博创作者甲"},
	}, subscription.SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatal(err)
	}
	subTargetA, err := raw.CreateSubscription("目标订阅甲", "包含摄影关键词", []subscription.AccountRef{
		{Site: "bilibili", UID: "7002", Name: "已有号甲"},
	}, subscription.DefaultSubRules())
	if err != nil {
		t.Fatal(err)
	}
	subTargetB, err := raw.CreateSubscription("目标订阅乙", "科技专栏", []subscription.AccountRef{
		{Site: "bilibili", UID: "7003", Name: "已有号乙"},
	}, subscription.DefaultSubRules())
	if err != nil {
		t.Fatal(err)
	}

	// Seed local cached items for all 3 accounts in subSrc so we verify content feed updates immediately.
	lib := raw.LibraryStore()
	saveCached := func(siteID, kind, id, uid, authorName, title string, mid, pubTs int64) {
		t.Helper()
		dir := filepath.Join(lib.CacheDir(), siteID, uid, id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := lib.SaveItem(dir, library.ItemMetadata{
			Site:   siteID,
			Kind:   kind,
			ID:     id,
			Title:  title,
			PubTs:  pubTs,
			Author: library.ItemAuthor{Site: siteID, Mid: mid, UID: uid, Name: authorName},
		}); err != nil {
			t.Fatal(err)
		}
	}
	saveCached("bilibili", "dynamic", "bili_7001_post", "7001", "B站创作者甲", "甲的B站缓存动态", 7001, 1710000300)
	saveCached("douyin", "video", "dy_7001_work", dyUID, "抖音创作者甲", "甲的抖音缓存作品", 0, 1710000200)
	saveCached("weibo", "dynamic", "wb_7001_post", wbUID, "微博创作者甲", "甲的微博缓存帖子", 0, 1710000100)

	app.RefreshData()
	siteHTTP.Store(0)

	tt := ui.NewTester(app.View, 1280, 900)
	app.OpenSubscriptionDetail(subSrc)
	tt.Frame()
	if len(app.SubFeedItems) != 3 {
		t.Fatalf("expected 3 cached items in subSrc content feed initially, got %d", len(app.SubFeedItems))
	}

	// Switch to "账号" tab (which triggers visible live check once on tab open).
	if err := tt.Click("订阅标签：账号"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	// Reset HTTP counter after the accounts tab settles so we strictly verify 0 network calls during all move flows!
	siteHTTP.Store(0)

	// --- 1. Weibo account: open "⋯ -> 更改关联订阅", verify exclusion + search, move to subTargetB ---
	if got := strings.Join(menuItems(t, tt, "订阅账号更多：weibo:"+wbUID), ","); got != "打开账号主页,用作头像,更改关联订阅,-,解除关联" {
		t.Fatalf("weibo account menu = %q", got)
	}
	chooseMenuItem(t, tt, "订阅账号更多：weibo:"+wbUID, "更改关联订阅")
	tt.Frame()

	if !app.SubscribePicker.Open || app.SubscribePicker.FromSubID != subSrc.ID {
		t.Fatalf("expected move picker open from %s, got %+v", subSrc.ID, app.SubscribePicker)
	}
	if _, ok := tt.Find("更改关联订阅对话框"); !ok {
		t.Fatal("missing 更改关联订阅对话框")
	}
	if !tt.HasText("更改关联：微博创作者甲") {
		t.Fatal("missing move dialog title 更改关联：微博创作者甲")
	}
	if !tt.HasText("当前属于订阅“原混合订阅”。选择另一个订阅，或移到新建订阅（保留已缓存内容与检查起点）。") {
		t.Fatal("missing move dialog subtitle")
	}
	if _, ok := tt.Find("更改关联账号站点"); !ok {
		t.Fatal("missing site badge in move dialog")
	}
	// Current owner MUST be excluded; other subscriptions MUST appear.
	if _, ok := tt.Find("移到现有订阅：" + subSrc.ID); ok {
		t.Fatal("current subscription must be excluded from move candidates")
	}
	if _, ok := tt.Find("移到现有订阅：" + subTargetA.ID); !ok {
		t.Fatal("missing target subscription A")
	}
	if _, ok := tt.Find("移到现有订阅：" + subTargetB.ID); !ok {
		t.Fatal("missing target subscription B")
	}

	// Search filters target subscriptions (by note "科技专栏").
	if err := tt.Click("更改关联搜索框"); err != nil {
		t.Fatal(err)
	}
	tt.Type("科技专栏")
	settle(tt, 2)
	if _, ok := tt.Find("移到现有订阅：" + subTargetA.ID); ok {
		t.Fatal("search did not filter out 目标订阅甲")
	}
	if err := tt.Click("移到现有订阅：" + subTargetB.ID); err != nil {
		t.Fatal(err)
	}
	tt.Frame()

	if app.SubscribePicker.Open {
		t.Fatalf("move dialog stayed open: %q", app.SubscribePicker.Error)
	}
	if app.BannerInfo != "已把 微博创作者甲 移到订阅“目标订阅乙”" {
		t.Fatalf("unexpected BannerInfo = %q", app.BannerInfo)
	}
	if owner, ok := raw.OwnerOfAccount("weibo", wbUID); !ok || owner.ID != subTargetB.ID {
		t.Fatalf("weibo account owner = (%v, %+v), want %s", ok, owner, subTargetB.ID)
	}
	// Both detail accounts tab AND detail content feed must refresh immediately!
	if _, ok := tt.Find("订阅账号更多：weibo:" + wbUID); ok {
		t.Fatal("moved weibo account still rendered in old subscription detail")
	}
	if !tt.HasText("关联账号（2 个）") {
		t.Fatal("expected old subscription account count updated to 2")
	}
	if len(app.SubFeedItems) != 2 {
		t.Fatalf("expected old subscription content feed updated immediately to 2 items, got %d", len(app.SubFeedItems))
	}
	for _, it := range app.SubFeedItems {
		if it.Title == "甲的微博缓存帖子" {
			t.Fatal("moved weibo post still in old subscription feed")
		}
	}

	// --- 2. Douyin account: open "⋯ -> 更改关联订阅", move to brand-new subscription ---
	chooseMenuItem(t, tt, "订阅账号更多：douyin:"+dyUID, "更改关联订阅")
	tt.Frame()
	if !tt.HasText("更改关联：抖音创作者甲") || !tt.HasText("移到新建订阅“抖音创作者甲”") {
		t.Fatal("missing Douyin move-to-new dialog header or primary button text")
	}
	if err := tt.Click("移到新建订阅"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()

	if app.SubscribePicker.Open {
		t.Fatalf("move-to-new dialog stayed open: %q", app.SubscribePicker.Error)
	}
	if app.BannerInfo != "已移到新建订阅：抖音创作者甲" {
		t.Fatalf("unexpected BannerInfo = %q", app.BannerInfo)
	}
	dyOwner, ok := raw.OwnerOfAccount("douyin", dyUID)
	if !ok || dyOwner.ID == subSrc.ID || dyOwner.Name != "抖音创作者甲" || len(dyOwner.Accounts) != 1 || dyOwner.Accounts[0].UniqueID != "dy_ui_alpha" || dyOwner.Accounts[0].WebRID != "889900" {
		t.Fatalf("unexpected new Douyin owner: ok=%v owner=%+v", ok, dyOwner)
	}
	if !tt.HasText("关联账号（1 个）") || len(app.SubFeedItems) != 1 || app.SubFeedItems[0].Title != "甲的B站缓存动态" {
		t.Fatalf("old subscription detail/feed did not refresh after Douyin move-to-new: feed=%+v", app.SubFeedItems)
	}

	// --- 3. Bilibili account: open "⋯ -> 更改关联订阅", move to subTargetA ---
	chooseMenuItem(t, tt, "订阅账号更多：bilibili:7001", "更改关联订阅")
	tt.Frame()
	if !tt.HasText("更改关联：B站创作者甲") {
		t.Fatal("missing Bilibili move dialog title")
	}
	if err := tt.Click("移到现有订阅：" + subTargetA.ID); err != nil {
		t.Fatal(err)
	}
	tt.Frame()

	if app.SubscribePicker.Open {
		t.Fatalf("bilibili move dialog stayed open: %q", app.SubscribePicker.Error)
	}
	if owner, ok := raw.OwnerOfAccount("bilibili", "7001"); !ok || owner.ID != subTargetA.ID {
		t.Fatalf("bilibili owner = (%v, %+v), want %s", ok, owner, subTargetA.ID)
	}
	if !tt.HasText("关联账号（0 个）") || len(app.SubFeedItems) != 0 {
		t.Fatalf("expected old subscription empty (0 accounts, 0 feed items), got feed=%d", len(app.SubFeedItems))
	}

	// Opening the newly created Douyin subscription shows its cached work immediately.
	app.OpenSubscriptionDetail(dyOwner)
	app.SubDetailTab = "content"
	tt.Frame()
	if len(app.SubFeedItems) != 1 || app.SubFeedItems[0].Title != "甲的抖音缓存作品" {
		t.Fatalf("expected new Douyin subscription content feed to show cached work, got %+v", app.SubFeedItems)
	}

	// Strictly zero network requests throughout every move flow!
	if got := siteHTTP.Load(); got != 0 {
		t.Fatalf("expected 0 site HTTP requests across all account moves, got %d", got)
	}
}

func TestMoveAccountFromAllAccountsAndEmptyOtherSubsAndErrorHandling(t *testing.T) {
	app, raw, siteHTTP, cleanup := newMoveTestEnv(t)
	defer cleanup()

	subOnly, err := raw.CreateSubscription("唯一订阅", "", []subscription.AccountRef{
		{Site: "bilibili", UID: "8101", Name: "独苗B站"},
		{Site: "weibo", UID: "5000008102", Name: "独苗微博"},
	}, subscription.DefaultSubRules())
	if err != nil {
		t.Fatal(err)
	}
	app.RefreshData()
	siteHTTP.Store(0)

	app.Navigate(RouteSubscriptions)
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	if err := tt.Click("订阅侧栏：全部账号"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	// Reset HTTP counter after leaving the subscriptions grid so we strictly verify 0 network calls on 全部账号 and all moves!
	siteHTTP.Store(0)

	// Verify "全部账号更多" menu items and empty-other-subscriptions state.
	if got := strings.Join(menuItems(t, tt, "全部账号更多：weibo:5000008102"), ","); got != "更改关联订阅,打开订阅,打开账号主页" {
		t.Fatalf("all-accounts more menu = %q", got)
	}
	chooseMenuItem(t, tt, "全部账号更多：weibo:5000008102", "更改关联订阅")
	tt.Frame()
	if !tt.HasText("还没有其他订阅，可以移到新建订阅") {
		t.Fatal("expected empty other-subscriptions prompt when only 1 subscription exists")
	}

	// Backend failure keeps move dialog open with error banner and leaves ownership intact.
	app.backend = &failingMoveBackend{
		BackendAPI:   raw,
		moveToNewErr: errors.New("模拟移到新建失败"),
	}
	if err := tt.Click("移到新建订阅"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !app.SubscribePicker.Open || !strings.Contains(app.SubscribePicker.Error, "模拟移到新建失败") {
		t.Fatalf("expected dialog open with error banner on failure, open=%v err=%q", app.SubscribePicker.Open, app.SubscribePicker.Error)
	}
	if owner, ok := raw.OwnerOfAccount("weibo", "5000008102"); !ok || owner.ID != subOnly.ID {
		t.Fatalf("failed move-to-new changed owner: ok=%v owner=%+v", ok, owner)
	}

	// Restore working backend and complete move-to-new from 全部账号.
	app.backend = raw
	if err := tt.Click("移到新建订阅"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if app.SubscribePicker.Open {
		t.Fatalf("dialog stayed open after move-to-new: %q", app.SubscribePicker.Error)
	}
	newWbSub, ok := raw.OwnerOfAccount("weibo", "5000008102")
	if !ok || newWbSub.ID == subOnly.ID || newWbSub.Name != "独苗微博" {
		t.Fatalf("expected new sub 独苗微博, got ok=%v sub=%+v", ok, newWbSub)
	}
	// All-accounts list immediately updates the account's subscription pill.
	if !tt.HasText("独苗微博") {
		t.Fatal("all-accounts list did not refresh after move-to-new")
	}

	// Now move bilibili:8101 from subOnly into newWbSub right from 全部账号!
	chooseMenuItem(t, tt, "全部账号更多：bilibili:8101", "更改关联订阅")
	tt.Frame()
	if _, ok := tt.Find("移到现有订阅：" + subOnly.ID); ok {
		t.Fatal("source subscription must be excluded from move candidates")
	}
	if err := tt.Click("移到现有订阅：" + newWbSub.ID); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if owner, ok := raw.OwnerOfAccount("bilibili", "8101"); !ok || owner.ID != newWbSub.ID || len(owner.Accounts) != 2 {
		t.Fatalf("expected bilibili:8101 moved to %s with 2 accounts, got ok=%v owner=%+v", newWbSub.ID, ok, owner)
	}

	// Ensure add mode (OpenSubscribePicker) remains 100% unchanged after move mode was used.
	app.OpenSubscribePicker(subscription.AccountRef{Site: "bilibili", UID: "9999", Name: "全新UP"})
	tt.Frame()
	if app.SubscribePicker.FromSubID != "" {
		t.Fatalf("expected empty FromSubID in add mode, got %q", app.SubscribePicker.FromSubID)
	}
	if _, ok := tt.Find("加入订阅对话框"); !ok {
		t.Fatal("add mode dialog label changed")
	}
	if _, ok := tt.Find("新建订阅"); !ok {
		t.Fatal("add mode primary button label changed")
	}
	if _, ok := tt.Find("加入现有订阅：" + newWbSub.ID); !ok {
		t.Fatal("add mode should list all existing subscriptions")
	}
	if err := tt.Click("取消加入订阅"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if app.SubscribePicker.Open {
		t.Fatal("add mode cancel did not close dialog")
	}
	if got := siteHTTP.Load(); got != 0 {
		t.Fatalf("expected 0 HTTP requests, got %d", got)
	}
}
