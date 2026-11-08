package ui

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
	if w, h := bmp.Size(); w > thumbMaxEdge || h > thumbMaxEdge || w != 1280 {
		t.Fatalf("thumbnail is %dx%d, want it scaled to fit %d", w, h, thumbMaxEdge)
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

// DEC-040: cache rules are made once in Settings and shared; a subscription
// only picks one from its rule button.
func TestCacheRulesAreSharedAndChosenPerSubscription(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 1)
	var subs []subscription.Subscription
	for i, uid := range []string{"3001", "3002"} {
		sub, err := app.backend.CreateSubscription(fmt.Sprintf("订阅%d", i), "", []subscription.AccountRef{{Site: "bilibili", UID: uid}}, subscription.SubRules{ScheduledCheck: true})
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
	if _, ok := tt.Find("缓存规则：默认"); !ok {
		t.Fatal("the built-in rule is not listed")
	}
	if err := tt.Click("新建缓存规则"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	app.RuleDraft.Name = "只存视频"
	if err := tt.Click("规则：自动缓存新动态"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("规则类型：图文"); err != nil { // video stays on
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("保存缓存规则"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if app.RuleError != "" || app.RuleDraft != nil {
		t.Fatalf("saving the rule failed: %q", app.RuleError)
	}
	var rule subscription.CacheRule
	for _, r := range app.backend.ListCacheRules() {
		if r.Name == "只存视频" {
			rule = r
		}
	}
	if rule.ID == "" || !rule.AutoCache || strings.Join(rule.Types, ",") != "video" {
		t.Fatalf("saved rule = %+v", rule)
	}

	for _, sub := range subs {
		app.OpenSubscriptionDetail(sub)
		tt.Frame()
		if err := tt.Click("订阅缓存规则：默认"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if err := tt.ChooseMenuItem("只存视频"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
	}
	listed, _ := app.backend.ListSubscriptions()
	for _, s := range listed {
		if s.RuleID != rule.ID || !s.Rules.AutoCache.Enabled {
			t.Fatalf("%s uses %q (%+v), want the shared rule", s.Name, s.RuleID, s.Rules)
		}
	}
	app.SettingsCategory = "rules"
	app.Navigate(RouteSettings)
	tt.Frame()
	if !tt.HasText("定时检查 · 自动缓存视频 · 2 个订阅") {
		t.Fatal("the rule list does not show that two subscriptions share the rule")
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
