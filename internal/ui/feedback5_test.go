package ui

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/chialecode/trace/internal/uistate"
	"github.com/egoist/mygo/ui"
)

// DEC-057: on Home, an author's avatar opens their subscription, not the
// user page that a parse opens.
func TestHomeAvatarOpensTheSubscription(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	subs := homeSubsFixture(t, app)
	app.Navigate(RouteHome)
	tt := ui.NewTester(app.View, 1280, 860)
	settle(tt, 2)
	if err := tt.Click("作者头像：bilibili:dynamic:d2"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if app.CurrentRoute != RouteSubscriptions || !app.SubDetailOpen || app.SelectedSubID != subs["未读乙"].ID {
		t.Fatalf("avatar opened route=%s detail=%v sub=%s, want the subscription %s", app.CurrentRoute, app.SubDetailOpen, app.SelectedSubID, subs["未读乙"].ID)
	}
}

// DEC-057: 订阅 › 全部账号 lists every subscribed account by site with the
// subscription it belongs to; a row opens that subscription.
func TestSubscriptionAccountsBySite(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	subs := homeSubsFixture(t, app)
	app.Navigate(RouteSubscriptions)
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	if err := tt.Click("订阅侧栏：全部账号"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("6 个账号") {
		t.Fatal("全部 does not count the six subscribed accounts")
	}
	if err := tt.Click("订阅账号站点筛选：抖音"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("1 个账号") {
		t.Fatal("the Douyin filter does not list exactly the one Douyin account")
	}
	if _, ok := tt.Find("订阅账号：bilibili:3001"); ok {
		t.Fatal("a Bilibili account shows under the Douyin filter")
	}
	if _, ok := tt.Find("所属订阅：douyin:MS4wLjABAAAA_synth_unread_c"); !ok || !tt.HasText("未读丙") {
		t.Fatal("the account does not name its subscription")
	}
	if err := tt.Click("订阅账号：douyin:MS4wLjABAAAA_synth_unread_c"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !app.SubDetailOpen || app.SubAccountsOpen || app.SelectedSubID != subs["未读丙"].ID {
		t.Fatalf("the row opened detail=%v accounts=%v sub=%s", app.SubDetailOpen, app.SubAccountsOpen, app.SelectedSubID)
	}
	// Narrow windows reach it from the page.
	app.SubDetailOpen = false
	narrow := ui.NewTester(app.View, 720, 860)
	narrow.Frame()
	if err := narrow.Click("订阅页全部账号"); err != nil {
		t.Fatal(err)
	}
	narrow.Frame()
	if _, ok := narrow.Find("订阅账号列表"); !ok {
		t.Fatal("the narrow window cannot open 全部账号")
	}
}

// DEC-056 ①: the media library has a 转发 filter, and 图片/视频 list a
// creator's own posts.
func TestLibraryForwardFilter(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)
	lib := app.backend.(*service.Backend).LibraryStore()
	pngBytes := buildTestPNGBytes()
	save := func(id, dynType string, via *library.ItemVia) {
		dir := filepath.Join(lib.CacheDir(), "bilibili", "555", "forwards", id)
		_ = os.MkdirAll(dir, 0o700)
		_ = os.WriteFile(filepath.Join(dir, "01.png"), pngBytes, 0o600)
		if _, err := lib.SaveItem(dir, library.ItemMetadata{Site: "bilibili", Kind: "dynamic", ID: id, Title: id, DynamicType: dynType, PubTs: 10,
			Author: library.ItemAuthor{Mid: 555, Name: "Forwarder"}, Via: via,
			Media: []library.MediaFile{{Role: "image", Path: "01.png"}}}); err != nil {
			t.Fatal(err)
		}
	}
	save("fw1", "DYNAMIC_TYPE_FORWARD", nil)
	save("own1", "DYNAMIC_TYPE_DRAW", nil)
	app.Navigate(RouteLibrary)
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	if err := tt.Click("筛选类型：转发"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(app.LibraryItems) != 1 || app.LibraryItems[0].RawID != "fw1" {
		t.Fatalf("转发 lists %+v", app.LibraryItems)
	}
	if err := tt.Click("筛选类型：图片"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for _, it := range app.LibraryItems {
		if it.RawID == "fw1" {
			t.Fatal("图片 lists forwarded content")
		}
	}
	if err := tt.Click("筛选类型：全部类型"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	found := 0
	for _, it := range app.LibraryItems {
		if it.RawID == "fw1" || it.RawID == "own1" {
			found++
		}
	}
	if found != 2 {
		t.Fatal("全部 does not list both the forward and the creator's own post")
	}
}

// DEC-057 (NFR-PERF-001): switching the Home site filter back to one shown a
// moment ago draws its thumbnails from the cache instead of decoding the
// images again; the first visit decodes them at the size they are drawn.
// (Measured on the synthetic set: 2.4-3.5 s for the first frame before,
// about 10 ms coming back after.)
func TestHomeSiteSwitchBackReusesDecodedImages(t *testing.T) {
	if testing.Short() {
		t.Skip("image fixture")
	}
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedMixedFeed(t, app, 0, 0)
	b := app.backend.(*service.Backend)
	lib := b.LibraryStore()
	var jb bytes.Buffer
	src := image.NewRGBA(image.Rect(0, 0, 1600, 1200))
	for y := 0; y < 1200; y += 3 {
		for x := 0; x < 1600; x++ {
			src.Set(x, y, color.RGBA{uint8(x), uint8(y), 90, 255})
		}
	}
	_ = jpeg.Encode(&jb, src, &jpeg.Options{Quality: 85})
	var pb bytes.Buffer
	_ = png.Encode(&pb, image.NewRGBA(image.Rect(0, 0, 128, 128)))
	avDir := filepath.Join(b.Locations().DataDir, "avatars")
	_ = os.MkdirAll(avDir, 0o700)
	for s := 0; s < 12; s++ {
		siteID, uid := "bilibili", fmt.Sprintf("%d", 5000+s)
		if s%2 == 1 {
			siteID, uid = "douyin", fmt.Sprintf("MS4wLjABAAAA_switch_%02d", s)
		}
		if _, err := app.backend.CreateSubscription("sub"+uid, "", []subscription.AccountRef{{Site: siteID, UID: uid, Name: "n" + uid}}, subscription.SubRules{}); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(siteID + ":" + uid))
		_ = os.WriteFile(filepath.Join(avDir, siteID+"-"+hex.EncodeToString(sum[:10])+".png"), pb.Bytes(), 0o600)
		for i := 0; i < 6; i++ {
			id := fmt.Sprintf("%d%03d", 7+s, i)
			dir := filepath.Join(lib.CacheDir(), siteID, uid, id)
			_ = os.MkdirAll(dir, 0o700)
			var media []library.MediaFile
			for k := 0; k < 3; k++ {
				rel := fmt.Sprintf("%02d.jpg", k+1)
				_ = os.WriteFile(filepath.Join(dir, rel), jb.Bytes(), 0o600)
				media = append(media, library.MediaFile{Role: "image", Path: rel, Width: 1600, Height: 1200})
			}
			meta := library.ItemMetadata{Site: siteID, Kind: "dynamic", ID: id, RawID: id, Title: "p" + id, Text: "body", PubTs: int64(10000 + s*20 + i), Media: media}
			if siteID == "bilibili" {
				meta.Author = library.ItemAuthor{Mid: int64(5000 + s), Name: "n" + uid}
			} else {
				meta.Author = library.ItemAuthor{Site: siteID, UID: uid, Name: "n" + uid}
			}
			if _, err := lib.SaveItem(dir, meta); err != nil {
				t.Fatal(err)
			}
		}
	}
	app.RefreshData()
	app.Navigate(RouteHome)
	tt := ui.NewTester(app.View, 1280, 860)
	thumbsLoading := func() int {
		app.mu.Lock()
		defer app.mu.Unlock()
		n := 0
		for k := range app.pending {
			if strings.HasPrefix(k, "thumb:") {
				n++
			}
		}
		return n
	}
	show := func(site string) {
		app.setFeedSiteFilter(site)
		deadline := time.Now().Add(20 * time.Second)
		for {
			settle(tt, 2)
			if thumbsLoading() == 0 {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: thumbnails still loading", site)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	for _, s := range []string{"all", "bilibili", "douyin"} {
		show(s)
	}
	keys := func() map[string]bool {
		app.mu.Lock()
		defer app.mu.Unlock()
		out := make(map[string]bool, len(app.images.entries))
		for k := range app.images.entries {
			out[k] = true
		}
		return out
	}
	// Coming back to a filter shown a moment ago decodes nothing again:
	// every image it draws is still cached (checked by key rather than
	// timed, so a busy machine cannot fail it).
	for _, s := range []string{"all", "bilibili", "douyin"} {
		before := keys()
		app.setFeedSiteFilter(s)
		settle(tt, 2)
		for k := range keys() {
			if !before[k] {
				t.Fatalf("switching back to %s decoded %s again", s, k)
			}
		}
		if n := thumbsLoading(); n != 0 {
			t.Fatalf("switching back to %s is loading %d images", s, n)
		}
	}
	for key, e := range app.images.entries {
		if e.bmp == nil {
			continue
		}
		if w, h := e.bmp.Size(); w > thumbMaxEdge || h > thumbMaxEdge {
			t.Fatalf("%s decoded at %dx%d", key, w, h)
		}
	}
}

// The task page's 最近缓存的内容 shows each item's cover; a dynamic without
// images shows the cover of the video it links.
func TestQueueRecentItemsShowCovers(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	lib := app.backend.(*service.Backend).LibraryStore()
	pngBytes := buildTestPNGBytes()
	vdir := filepath.Join(lib.CacheDir(), "bilibili", "555", "videos", "BV1cv411w7KC")
	_ = os.MkdirAll(vdir, 0o700)
	_ = os.WriteFile(filepath.Join(vdir, "cover.png"), pngBytes, 0o600)
	if _, err := lib.SaveItem(vdir, library.ItemMetadata{Site: "bilibili", Kind: "video", ID: "BV1cv411w7KC", Title: "v",
		Author: library.ItemAuthor{Mid: 555}, Media: []library.MediaFile{{Role: "cover", Path: "cover.png"}}}); err != nil {
		t.Fatal(err)
	}
	ddir := filepath.Join(lib.CacheDir(), "bilibili", "555", "dynamics", "880001")
	if _, err := lib.SaveItem(ddir, library.ItemMetadata{Site: "bilibili", Kind: "dynamic", ID: "880001", Title: "d",
		Author: library.ItemAuthor{Mid: 555}, Videos: []string{"BV1cv411w7KC"}}); err != nil {
		t.Fatal(err)
	}
	if key, rel := app.queueRecentCover("bilibili:video:BV1cv411w7KC"); key != "bilibili:video:BV1cv411w7KC" || rel != "cover.png" {
		t.Fatalf("video cover = %s %s", key, rel)
	}
	if key, rel := app.queueRecentCover("bilibili:dynamic:880001"); key != "bilibili:video:BV1cv411w7KC" || rel != "cover.png" {
		t.Fatalf("dynamic cover = %s %s, want the linked video's", key, rel)
	}
	done := make(chan func(), 1)
	app.dispatch = func(fn func()) { done <- fn }
	_ = app.lookupLocalBitmap("bilibili:video:BV1cv411w7KC", "cover.png")
	select {
	case fn := <-done:
		fn()
	case <-time.After(5 * time.Second):
		t.Fatal("the cover never loaded")
	}
	if app.lookupLocalBitmap("bilibili:video:BV1cv411w7KC", "cover.png") == nil {
		t.Fatal("the cached video's cover file did not decode")
	}
}

// staleUIStateBackend returns ui-state as it was before a write from another
// goroutine, as the UI thread sees it when the updater writes in between.
type staleUIStateBackend struct {
	service.BackendAPI
	snap uistate.State
}

func (b staleUIStateBackend) GetUIState() uistate.State { return b.snap }

// A write the updater makes while the UI holds an older ui-state (an update
// downloaded and waiting for a restart) survives the UI's own writes: the
// reading position, the route, the site filter and removed recordings.
func TestUIStateWritesKeepTheUpdatersWrite(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	homeSubsFixture(t, app)
	real := app.backend
	stale := real.GetUIState()
	if _, err := real.MutateUIState(func(st *uistate.State) {
		st.Update.ReadyVersion, st.Update.ReadyNotes = "9.9.9", "notes"
	}); err != nil {
		t.Fatal(err)
	}
	app.backend = staleUIStateBackend{BackendAPI: real, snap: stale}
	app.saveHomePosition("all", uistate.HomePosition{Key: "bilibili:dynamic:d1", PubTs: 5000, OffsetPx: 12})
	app.Navigate(RouteLibrary)
	app.setFeedSiteFilter("bilibili")
	app.dismissLiveSessions("bilibili:live:1-1")
	app.setSubDetailTab("accounts")
	got := real.GetUIState()
	if got.Update.ReadyVersion != "9.9.9" {
		t.Fatalf("the UI's writes dropped the update waiting for a restart: %+v", got.Update)
	}
	if got.HomePositions["all"].Key != "bilibili:dynamic:d1" || got.LastRoute != string(RouteLibrary) || got.FeedFilter.Site != "bilibili" || got.DismissedLiveSessions["bilibili:live:1-1"] == "" {
		t.Fatalf("the UI's own writes were not saved: %+v", got)
	}
}

// DEC-058: subscribing to an account asks whether to join an existing
// subscription (found by search) or start a new one.
func TestSubscribeAsksExistingOrNew(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	subs := homeSubsFixture(t, app)
	app.CurrentUser = &bilibili.UserSpace{Mid: 8888, Name: "测试UP主"}
	app.UserTab = "dynamics"
	app.CurrentRoute = RouteUser
	tt := ui.NewTester(app.View, 1280, 860)
	settle(tt, 2)
	if err := tt.Click("订阅该 UP 主"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, ok := tt.Find("加入订阅对话框"); !ok {
		t.Fatal("订阅 did not ask which subscription to join")
	}
	if _, ok := tt.Find("新建订阅"); !ok {
		t.Fatal("the dialog does not offer a new subscription")
	}
	for name, sub := range subs {
		if _, ok := tt.Find("加入现有订阅：" + sub.ID); !ok {
			t.Fatalf("existing subscription %s is not listed", name)
		}
	}
	// Search narrows the list.
	if err := tt.Click("加入订阅搜索框"); err != nil {
		t.Fatal(err)
	}
	tt.Type("未读乙")
	settle(tt, 2)
	if _, ok := tt.Find("加入现有订阅：" + subs["未读甲"].ID); ok {
		t.Fatal("search did not narrow the subscriptions")
	}
	if err := tt.Click("加入现有订阅：" + subs["未读乙"].ID); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if app.SubscribePicker.Open {
		t.Fatalf("the dialog stayed open: %q", app.SubscribePicker.Error)
	}
	owner, ok := app.backend.OwnerOfAccount("bilibili", "8888")
	if !ok || owner.ID != subs["未读乙"].ID || len(owner.Accounts) != 2 {
		t.Fatalf("the account joined %+v (ok=%v), want 未读乙 with two accounts", owner, ok)
	}
	if _, ok := tt.Find("进入所属订阅"); !ok {
		t.Fatal("the user page does not show the account as subscribed")
	}

	// An account that is already in a subscription reports why it cannot
	// join another, and the dialog stays open.
	app.OpenSubscribePicker(subscription.AccountRef{Site: "bilibili", UID: "3001", Name: "未读甲"})
	tt.Frame()
	if err := tt.Click("加入现有订阅：" + subs["未读乙"].ID); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !app.SubscribePicker.Open || app.SubscribePicker.Error == "" {
		t.Fatal("joining with an account owned elsewhere did not report an error")
	}
	if err := tt.Click("取消加入订阅"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if app.SubscribePicker.Open {
		t.Fatal("取消 did not close the dialog")
	}
}
