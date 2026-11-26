package ui

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
	"github.com/egoist/mygo/ui"
)

type weiboWatermarkViewerBackend struct {
	service.BackendAPI
	mu        sync.Mutex
	requested []string
	respond   func(rawURL string) ([]byte, error)
}

func (b *weiboWatermarkViewerBackend) FetchImage(_ context.Context, rawURL string) ([]byte, error) {
	b.mu.Lock()
	b.requested = append(b.requested, rawURL)
	fn := b.respond
	b.mu.Unlock()
	return fn(rawURL)
}

func makeValidPNGBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	return makeValidColoredPNGBytes(t, w, h, color.RGBA{R: 50, G: 140, B: 220, A: 255})
}

func makeValidColoredPNGBytes(t *testing.T, w, h int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return buf.Bytes()
}

func seedWeiboDualSavedItem(t *testing.T, app *App) library.ItemMetadata {
	t.Helper()
	b := app.backend.(*service.Backend)
	cacheDir := b.GetSettings().CacheDir
	itemDir := filepath.Join(cacheDir, "weibo", "1000000001", "posts", "5000000000009201")
	if err := os.MkdirAll(filepath.Join(itemDir, "images"), 0o700); err != nil {
		t.Fatal(err)
	}
	orig1 := makeValidColoredPNGBytes(t, 40, 30, color.RGBA{R: 200, G: 60, B: 60, A: 255})
	nowm1 := makeValidColoredPNGBytes(t, 20, 15, color.RGBA{R: 40, G: 180, B: 90, A: 255})
	orig2 := makeValidColoredPNGBytes(t, 36, 24, color.RGBA{R: 60, G: 90, B: 210, A: 255})
	if err := os.WriteFile(filepath.Join(itemDir, "images", "01.png"), orig1, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(itemDir, "images", "01_nowm.png"), nowm1, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(itemDir, "images", "02.png"), orig2, 0o600); err != nil {
		t.Fatal(err)
	}
	s1 := sha256.Sum256(orig1)
	sn1 := sha256.Sum256(nowm1)
	s2 := sha256.Sum256(orig2)

	saved, err := b.LibraryStore().SaveItem(itemDir, library.ItemMetadata{
		Version:      1,
		ID:           "dynamic:5000000000009201",
		Site:         "weibo",
		Kind:         "dynamic",
		Status:       "complete",
		Title:        "微博双份保存看图测试",
		Text:         "微博双份保存看图测试",
		DynamicType:  "draw",
		TypeLabel:    "图文",
		URL:          "https://weibo.com/1000000001/O000002n1",
		PubTs:        1764000500,
		DirectCached: true,
		Author:       library.ItemAuthor{Site: "weibo", UID: "1000000001", Name: "合成博主"},
		Media: []library.MediaFile{
			{Role: "image", Path: "images/01.png", Bytes: int64(len(orig1)), SHA256: hex.EncodeToString(s1[:]), Width: 40, Height: 30},
			{Role: "image_nowm", Path: "images/01_nowm.png", Bytes: int64(len(nowm1)), SHA256: hex.EncodeToString(sn1[:]), Width: 20, Height: 15},
			{Role: "image", Path: "images/02.png", Bytes: int64(len(orig2)), SHA256: hex.EncodeToString(s2[:]), Width: 36, Height: 24},
		},
		NotePages: []library.NotePageRecord{
			{Index: 1, Kind: "image", MediaID: "pic01", Image: "images/01.png", NoWatermark: "images/01_nowm.png", Width: 40, Height: 30},
			{Index: 2, Kind: "image", MediaID: "pic02", Image: "images/02.png", Width: 36, Height: 24},
		},
	})
	if err != nil {
		t.Fatalf("SaveItem: %v", err)
	}
	return saved
}

func TestWeiboWatermarkViewerFallbackOn404HTMLAndCorruptImage(t *testing.T) {
	validPNG := makeValidPNGBytes(t, 20, 20)

	cases := []struct {
		name    string
		oslarge func() ([]byte, error)
	}{
		{
			name: "404_error",
			oslarge: func() ([]byte, error) {
				return nil, errors.New("微博图片返回 HTTP 404")
			},
		},
		{
			name: "html_body",
			oslarge: func() ([]byte, error) {
				return []byte("<!doctype html><html><body>404</body></html>"), nil
			},
		},
		{
			name: "corrupt_image_bytes",
			oslarge: func() ([]byte, error) {
				return []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x01, 0x02}, nil
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app, _, cleanup := newTestUIApp(t)
			defer cleanup()
			if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
				t.Fatal(err)
			}
			stub := &weiboWatermarkViewerBackend{
				BackendAPI: app.backend,
				respond: func(rawURL string) ([]byte, error) {
					if strings.Contains(rawURL, "/oslarge/") {
						return tc.oslarge()
					}
					if strings.Contains(rawURL, "/mw2000/") {
						return validPNG, nil
					}
					return nil, errors.New("unexpected url: " + rawURL)
				},
			}
			app.backend = stub
			posted := make(chan func(), 4)
			app.dispatch = func(fn func()) { posted <- fn }

			pages := weiboMediaItemsToRenderPages([]service.WeiboMediaItemDTO{
				{
					Index:            1,
					PicID:            "wm_view_1",
					Kind:             "image",
					Width:            1200,
					Height:           900,
					ImageURL:         "https://wx1.sinaimg.cn/oslarge/wm_view_1.jpg",
					FallbackURLs:     []string{"https://wx1.sinaimg.cn/mw2000/wm_view_1.jpg"},
					PublicPreviewURL: "https://wx1.sinaimg.cn/mw2000/wm_view_1.jpg",
				},
			}, false)
			if len(pages) != 1 || len(pages[0].FallbackImageURLs) != 1 || pages[0].FallbackImageURLs[0] != "https://wx1.sinaimg.cn/mw2000/wm_view_1.jpg" {
				t.Fatalf("unexpected render pages: %+v", pages)
			}

			app.openNotePagesViewer("", "weibo", pages, 0)

			select {
			case apply := <-posted:
				apply()
			case <-time.After(3 * time.Second):
				t.Fatal("viewer image fetch did not finish")
			}

			if len(app.ImageViewer.Bitmaps) != 1 || app.ImageViewer.Bitmaps[0] == nil || app.ImageViewer.Errors[0] != "" {
				t.Fatalf("viewer did not fall back cleanly: bitmaps=%v errors=%v", app.ImageViewer.Bitmaps, app.ImageViewer.Errors)
			}
			stub.mu.Lock()
			gotReqs := append([]string(nil), stub.requested...)
			stub.mu.Unlock()
			if len(gotReqs) != 2 || gotReqs[0] != "https://wx1.sinaimg.cn/oslarge/wm_view_1.jpg" || gotReqs[1] != "https://wx1.sinaimg.cn/mw2000/wm_view_1.jpg" {
				t.Fatalf("viewer fetch sequence = %v, want [oslarge, mw2000]", gotReqs)
			}
		})
	}
}

func TestDownloadSettingsWeiboWatermarkTogglePersists(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()
	app.SettingsCategory = "download"
	app.Navigate(RouteSettings)

	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	if !tt.HasText("微博同时保存无水印版") {
		t.Fatal("settings download page missing 微博同时保存无水印版 text")
	}
	if _, ok := tt.Find("开关微博图片去水印"); !ok {
		t.Fatal("settings download page missing 开关微博图片去水印 switch")
	}
	if !app.DraftSettings.WeiboRemoveWatermark || !app.backend.GetSettings().WeiboRemoveWatermark {
		t.Fatal("expected WeiboRemoveWatermark default true")
	}

	if err := tt.Click("开关微博图片去水印"); err != nil {
		t.Fatalf("click watermark switch: %v", err)
	}
	tt.Frame()
	if app.DraftSettings.WeiboRemoveWatermark || app.backend.GetSettings().WeiboRemoveWatermark {
		t.Fatal("clicking switch did not persist WeiboRemoveWatermark=false")
	}
}

func TestLibraryGridDoesNotDuplicateWeiboNoWatermarkCompanion(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	saved := seedWeiboDualSavedItem(t, app)
	app.CompleteLogin()
	app.Navigate(RouteLibrary)
	app.LibraryItems = []library.ItemMetadata{saved}
	app.cacheStateRev++

	tiles := app.libraryTiles()
	if len(tiles) != 2 {
		t.Fatalf("libraryTiles count = %d, want 2 (only primary images 01.png & 02.png, never 01_nowm.png)", len(tiles))
	}
	for _, tile := range tiles {
		if tile.file.Role != "image" || strings.Contains(tile.file.Path, "_nowm") {
			t.Fatalf("unexpected companion tile in library grid: %+v", tile.file)
		}
	}
}

func TestWeiboImageViewerToggleOriginalAndNoWatermark(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	saved := seedWeiboDualSavedItem(t, app)
	app.CompleteLogin()
	app.Navigate(RouteLibrary)
	app.LibraryItems = []library.ItemMetadata{saved}
	app.cacheStateRev++

	posted := make(chan func(), 4)
	app.dispatch = func(fn func()) { posted <- fn }

	tiles := app.libraryTiles()
	if len(tiles) != 2 {
		t.Fatalf("tiles len = %d, want 2", len(tiles))
	}
	app.openLibraryTile(tiles[0])

	select {
	case apply := <-posted:
		apply()
	case <-time.After(3 * time.Second):
		t.Fatal("viewer bitmaps did not finish decoding")
	}

	if !app.ImageViewer.Open || app.ImageViewer.ShowNoWatermark {
		t.Fatalf("expected viewer open on original by default: open=%v showNoWM=%v", app.ImageViewer.Open, app.ImageViewer.ShowNoWatermark)
	}
	if len(app.ImageViewer.NoWatermarkPaths) != 2 || app.ImageViewer.NoWatermarkPaths[0] == "" || app.ImageViewer.NoWatermarkPaths[1] != "" {
		t.Fatalf("unexpected NoWatermarkPaths: %+v", app.ImageViewer.NoWatermarkPaths)
	}
	if len(app.ImageViewer.Bitmaps) != 2 || app.ImageViewer.Bitmaps[0] == nil || app.ImageViewer.Bitmaps[1] == nil {
		t.Fatalf("primary bitmaps missing: %+v", app.ImageViewer.Bitmaps)
	}
	if len(app.ImageViewer.NoWatermarkBitmaps) != 2 || app.ImageViewer.NoWatermarkBitmaps[0] == nil || app.ImageViewer.NoWatermarkBitmaps[1] != nil {
		t.Fatalf("companion bitmaps unexpected: %+v", app.ImageViewer.NoWatermarkBitmaps)
	}
	bw0, bh0 := app.ImageViewer.Bitmaps[0].Size()
	nw0, nh0 := app.ImageViewer.NoWatermarkBitmaps[0].Size()
	if bw0 != 40 || bh0 != 30 || nw0 != 20 || nh0 != 15 {
		t.Fatalf("decoded sizes mismatch: orig=%dx%d nowm=%dx%d", bw0, bh0, nw0, nh0)
	}

	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	if _, ok := tt.Find("看图切换原图无水印"); !ok {
		t.Fatal("expected 原图/无水印 toggle capsule on slide 1")
	}
	if !tt.HasText("原图") || !tt.HasText("无水印") {
		t.Fatal("expected both 原图 and 无水印 labels in top floating bar")
	}

	// Click "无水印" -> switches immediately to companion.
	if err := tt.Click("看图显示无水印"); err != nil {
		t.Fatalf("click 看图显示无水印: %v", err)
	}
	tt.Frame()
	if !app.ImageViewer.ShowNoWatermark {
		t.Fatal("expected ShowNoWatermark=true after clicking 看图显示无水印")
	}

	// Advance to slide 2 (has no companion) -> toggle hides, shows slide 2 original cleanly with zero error.
	if err := tt.Click("看图下一张"); err != nil {
		t.Fatalf("click 看图下一张: %v", err)
	}
	tt.Frame()
	if app.ImageViewer.Index != 1 {
		t.Fatalf("Index = %d, want 1", app.ImageViewer.Index)
	}
	if _, ok := tt.Find("看图切换原图无水印"); ok {
		t.Fatal("toggle capsule must not render on slide 2 without companion")
	}
	if app.ImageViewer.Errors[1] != "" {
		t.Fatalf("unexpected error on slide 2: %q", app.ImageViewer.Errors[1])
	}

	// Return to slide 1 and toggle back to "原图".
	if err := tt.Click("看图上一张"); err != nil {
		t.Fatalf("click 看图上一张: %v", err)
	}
	tt.Frame()
	if err := tt.Click("看图显示原图"); err != nil {
		t.Fatalf("click 看图显示原图: %v", err)
	}
	tt.Frame()
	if app.ImageViewer.ShowNoWatermark {
		t.Fatal("expected ShowNoWatermark=false after clicking 看图显示原图")
	}

	// Verify OpenItemImageViewer (stem fallback path) also associates 01_nowm.png cleanly.
	app.OpenItemImageViewer(saved.ItemKey, nil, []string{"images/01.png", "images/02.png"}, 0)
	select {
	case apply := <-posted:
		apply()
	case <-time.After(3 * time.Second):
		t.Fatal("OpenItemImageViewer bitmaps did not finish decoding")
	}
	if len(app.ImageViewer.NoWatermarkPaths) != 2 || app.ImageViewer.NoWatermarkPaths[0] == "" || app.ImageViewer.NoWatermarkPaths[1] != "" {
		t.Fatalf("OpenItemImageViewer NoWatermarkPaths = %+v", app.ImageViewer.NoWatermarkPaths)
	}
}
