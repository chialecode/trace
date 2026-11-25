package ui

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"strings"
	"sync"
	"testing"
	"time"

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
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{R: 50, G: 140, B: 220, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return buf.Bytes()
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
	if !tt.HasText("微博图片去平台水印") {
		t.Fatal("settings download page missing 微博图片去平台水印 text")
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
