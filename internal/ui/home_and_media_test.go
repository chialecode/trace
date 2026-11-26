package ui

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

type libraryCountingBackend struct {
	service.BackendAPI
	gets int
}

func (b *libraryCountingBackend) GetLibraryItem(key string) (library.ItemMetadata, bool, error) {
	b.gets++
	return b.BackendAPI.GetLibraryItem(key)
}

func (b *libraryCountingBackend) GetLibraryItemMetaOnly(key string) (library.ItemMetadata, bool, error) {
	b.gets++
	return b.BackendAPI.GetLibraryItemMetaOnly(key)
}

// DEC-040 (frame drops while scrolling Home): redrawing the timeline does
// not query the library for every visible post on every frame; a task or
// library event rebuilds the cards once.
func TestHomeRedrawsDoNotQueryTheLibraryPerFrame(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 40)
	backend := &libraryCountingBackend{BackendAPI: app.backend}
	app.backend = backend
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	viewport, _ := tt.Find("主页信息流列表")
	tt.Scroll(viewport.X+viewport.W/2, viewport.Y+300, 0, 200)
	tt.Frame()
	before := backend.gets
	for i := 0; i < 10; i++ {
		tt.Scroll(viewport.X+viewport.W/2, viewport.Y+300, 0, 1)
		tt.Scroll(viewport.X+viewport.W/2, viewport.Y+300, 0, -1)
		tt.Frame()
	}
	if backend.gets != before {
		t.Fatalf("redrawing the same posts queried the library %d times", backend.gets-before)
	}
	app.handleBackendEvent(service.Event{Type: service.EventLibraryChanged})
	tt.Frame()
	if backend.gets == before {
		t.Fatal("a library change did not rebuild the visible cards")
	}
}

// Local thumbnails are decoded off the UI thread and scaled down: a large
// original is not drawn at full size, and the first frame draws the
// placeholder instead of waiting.
func TestLocalThumbnailsLoadInTheBackgroundAndAreScaled(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 1)
	lib := app.backend.(interface{ LibraryStore() *library.Store }).LibraryStore()
	dir := filepath.Join(lib.CacheDir(), "dynamic", "thumbsrc")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 3000, 2000))
	for y := 0; y < 2000; y += 50 {
		for x := 0; x < 3000; x++ {
			img.Set(x, y, color.RGBA{200, 80, 40, 255})
		}
	}
	f, err := os.Create(filepath.Join(dir, "big.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	f.Close()
	item, err := lib.SaveItem(dir, library.ItemMetadata{Site: "bilibili", Kind: "dynamic", ID: "thumbsrc", Title: "big image", PubTs: 99,
		Media: []library.MediaFile{{Role: "image", Path: "big.png", Width: 3000, Height: 2000}}})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan func(), 1)
	app.dispatch = func(fn func()) { done <- fn }
	if bmp := app.lookupLocalBitmap(item.ItemKey, "big.png"); bmp != nil {
		t.Fatal("the first request decoded the image on the calling thread")
	}
	if app.lookupLocalBitmap(item.ItemKey, "big.png") != nil || len(app.pending) != 1 {
		t.Fatal("a repeated request started a second decode")
	}
	// The decode runs on its own goroutine and posts the result back.
	(<-done)()
	bmp := app.lookupLocalBitmap(item.ItemKey, "big.png")
	if bmp == nil {
		t.Fatal("the decoded thumbnail was not kept")
	}
	if w, h := bmp.Size(); w > thumbMaxEdge || h > thumbMaxEdge || w != thumbMaxEdge {
		t.Fatalf("thumbnail is %dx%d, want it scaled to fit %d", w, h, thumbMaxEdge)
	}
	// A grid cell asks for a smaller decode, cached apart (DEC-057).
	if app.lookupLocalBitmapEdge(item.ItemKey, "big.png", gridThumbEdge) != nil {
		t.Fatal("the grid size reused the larger decode")
	}
	(<-done)()
	if small := app.lookupLocalBitmapEdge(item.ItemKey, "big.png", gridThumbEdge); small == nil {
		t.Fatal("the grid thumbnail was not kept")
	} else if w, _ := small.Size(); w != gridThumbEdge {
		t.Fatalf("grid thumbnail is %d wide, want %d", w, gridThumbEdge)
	}
}

// DEC-040: a video opens in a floating panel over the app (not the whole
// window) and starts playing; clicking outside the panel closes it.
func TestPlayerFloatsOverTheAppAndAutoplays(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("needs Media Foundation")
	}
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)
	item := savePlayableItem(t, app)
	posted := make(chan func(), 4)
	app.dispatch = func(fn func()) { posted <- fn }
	app.OpenPlayerOverlay(item)
	nextPosted(t, posted)()
	if !app.player.Snapshot(640, 360).Playing {
		t.Fatal("the video did not start playing when it opened")
	}
	app.dispatch = nil
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	panel, ok := tt.Find("播放层")
	if !ok || panel.W >= 1280 || panel.H >= 860 || panel.X <= 0 || panel.Y <= 0 {
		t.Fatalf("the player is not a floating panel: %+v (%v)", panel, ok)
	}
	tt.ClickAt(panel.X/2, panel.Y+panel.H/2) // the dimmed app beside the panel
	tt.Frame()
	if app.PlayerOverlay.Open {
		t.Fatal("clicking outside the panel did not close the player")
	}
}

// DEC-070 & DEC-080: new subscriptions default to "完整存档" ("追踪：完整存档");
// presets, overlayable "录直播", and expandable 7 atomic tags can be toggled directly
// in subscription detail, and Settings → 缓存标签 reflects per-tag counts immediately.
func TestCapabilityTagsToggleInSubscriptionDetailAndCountInSettings(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 1)
	var subs []subscription.Subscription
	for i, uid := range []string{"3001", "3002"} {
		sub, err := app.backend.CreateSubscription(fmt.Sprintf("订阅%d", i), "", []subscription.AccountRef{{Site: "bilibili", UID: uid}}, subscription.EmptySubRules())
		if err != nil {
			t.Fatal(err)
		}
		subs = append(subs, sub)
	}
	app.RefreshData()
	app.SettingsCategory = "rules"
	app.Navigate(RouteSettings)
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()

	for _, tag := range subscription.AllCapabilityTags() {
		if _, ok := tt.Find("缓存标签行：" + tag.ID); !ok {
			t.Fatalf("missing capability tag row %s in Settings", tag.ID)
		}
	}
	for _, legacy := range []string{"缓存规则：默认", "新建缓存规则", "规则名称", "保存缓存规则"} {
		if _, ok := tt.Find(legacy); ok {
			t.Fatalf("legacy named cache rule control %q still rendered", legacy)
		}
	}

	// Empty subscription shows "追踪：仅手动"; expand custom atomic tags from menu and toggle tags.
	app.OpenSubscriptionDetail(subs[0])
	tt.Frame()
	if !tt.HasText("追踪：仅手动") {
		t.Fatalf("empty subscription should display 追踪：仅手动, texts=%v", tt.Texts())
	}
	if err := tt.Click("订阅缓存标签菜单"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.ChooseMenuItem("自定义原子标签"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for _, label := range []string{"订阅原子标签：定时检查", "订阅原子标签：自动缓存视频", "订阅原子标签：自动缓存图文"} {
		if err := tt.Click(label); err != nil {
			t.Fatalf("click %s: %v", label, err)
		}
		tt.Frame()
	}
	// [scheduled_check, cache_video, cache_draw] matches preset "追更并保存"!
	if !tt.HasText("追踪：追更并保存") {
		t.Fatalf("expected matched preset 追踪：追更并保存, texts=%v", tt.Texts())
	}
	// Removing 自动缓存图文 makes it a custom combination ([scheduled_check, cache_video]).
	if err := tt.Click("订阅原子标签：自动缓存图文"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("追踪：自定义") {
		t.Fatalf("expected custom tracking mode after removing 自动缓存图文, texts=%v", tt.Texts())
	}
	if _, ok := tt.Find("订阅缓存标签：自动缓存视频"); !ok {
		t.Fatalf("expected active chip 订阅缓存标签：自动缓存视频, texts=%v", tt.Texts())
	}

	// On subs[1], enable only cache_video (without scheduled_check) -> shows dependency warning!
	app.OpenSubscriptionDetail(subs[1])
	tt.Frame()
	if err := tt.Click("订阅缓存标签菜单"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.ChooseMenuItem("自定义原子标签"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("订阅原子标签：自动缓存视频"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, ok := tt.Find("订阅缓存标签：自动缓存视频"); !ok {
		t.Fatalf("expected active chip 订阅缓存标签：自动缓存视频, texts=%v", tt.Texts())
	}
	if _, ok := tt.Find("自动缓存缺少定时检查提示"); !ok {
		t.Fatalf("expected dependency warning when cache_video is on without scheduled_check, texts=%v", tt.Texts())
	}

	listed, _ := app.backend.ListSubscriptions()
	for _, s := range listed {
		if !subscription.HasTag(s.Tags, subscription.TagCacheVideo) || !s.Rules.AutoCache.Enabled || s.Rules.AutoCache.Since == "" {
			t.Fatalf("%s missing cache_video tag or AutoCache.Since: tags=%v rules=%+v", s.Name, s.Tags, s.Rules)
		}
	}

	// Jump to Settings → 缓存标签 from the subscription tag menu item.
	if err := tt.Click("订阅缓存标签菜单"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.ChooseMenuItem("批量管理缓存标签"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if app.CurrentRoute != RouteSettings || app.SettingsCategory != "rules" {
		t.Fatalf("expected navigation to Settings rules, got route=%s cat=%s", app.CurrentRoute, app.SettingsCategory)
	}
	if app.tagUseCount(subscription.TagCacheVideo) != 2 || app.tagUseCount(subscription.TagScheduledCheck) != 1 || app.tagUseCount(subscription.TagAutoRecord) != 0 {
		t.Fatalf("unexpected tag counts: video=%d sched=%d rec=%d",
			app.tagUseCount(subscription.TagCacheVideo),
			app.tagUseCount(subscription.TagScheduledCheck),
			app.tagUseCount(subscription.TagAutoRecord))
	}

	// "清空全部标签" in Subscription Detail resets tags to empty ("仅手动").
	app.OpenSubscriptionDetail(subs[1])
	tt.Frame()
	if err := tt.Click("订阅缓存标签菜单"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.ChooseMenuItem("清空全部标签"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("追踪：仅手动") || app.tagUseCount(subscription.TagCacheVideo) != 1 {
		t.Fatalf("expected 清空全部标签 to clear sub[1] tags; count=%d texts=%v", app.tagUseCount(subscription.TagCacheVideo), tt.Texts())
	}
}

// DEC-040: posts show images and videos as the site does. One image keeps its
// own proportions (a portrait is a fixed height and as wide as it is); several
// are square cells; a live photo is marked; a video's cover sits left of its
// title and description.
func TestPostImagesKeepTheirShapeAndVideosSitBesideTheirText(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	render := func(post PostCardData) *ui.Tester {
		return ui.NewTester(func(c *ui.Context) {
			ui.Column(c).Width(720).Children(func() { app.renderPostCard(c, post) })
		}, 900, 1200)
	}

	tt := render(PostCardData{Key: "portrait", Images: []PostImage{{Width: 1080, Height: 1440, Live: true}}})
	cell, ok := tt.Find("查看图片 1")
	if !ok || cell.H != singleImageHeight || cell.W != singleImageHeight*3/4 {
		t.Fatalf("portrait image cell %+v, want %vx%v", cell, singleImageHeight*3/4, singleImageHeight)
	}
	if !tt.HasText("实况") {
		t.Fatal("a live photo is not marked")
	}

	tt = render(PostCardData{Key: "wide", Images: []PostImage{{Width: 1600, Height: 900}}})
	cell, _ = tt.Find("查看图片 1")
	if cell.W != singleImageMaxWidth || cell.H < 269 || cell.H > 271 || tt.HasText("实况") {
		t.Fatalf("landscape image cell %+v, want %v wide at 16:9", cell, singleImageMaxWidth)
	}

	tt = render(PostCardData{Key: "grid", Images: []PostImage{{}, {}, {}, {}, {}}})
	first, _ := tt.Find("查看图片 1")
	fifth, _ := tt.Find("查看图片 5")
	if first.W != first.H || first.W < 150 || fifth.Y <= first.Y || fifth.X <= first.X {
		t.Fatalf("five images are not square cells in three columns: %+v %+v", first, fifth)
	}

	tt = render(PostCardData{Key: "video", Kind: "video", Video: &PostVideo{BVID: "BV1test", Title: "合成视频标题", Desc: "合成视频简介", Duration: "03:21"}})
	card, ok := tt.Find("播放视频卡片：BV1test")
	if !ok || card.H > videoCoverWidth*9/16+4 {
		t.Fatalf("video card is not one row: %+v", card)
	}
	if !tt.HasText("合成视频标题") || !tt.HasText("合成视频简介") || tt.HasText("播放 0") {
		t.Fatal("video card lacks its title or description, or shows counts")
	}
}
