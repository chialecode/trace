package download

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/site"
)

func makeWatermarkTestJPEG(t *testing.T, w, h int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("jpeg.Encode: %v", err)
	}
	return buf.Bytes()
}

func makeWatermarkTestPlaceholderGIF(t *testing.T) []byte {
	t.Helper()
	img := image.NewPaletted(image.Rect(0, 0, 480, 360), color.Palette{
		color.RGBA{R: 235, G: 235, B: 235, A: 255},
		color.RGBA{R: 160, G: 160, B: 160, A: 255},
	})
	var buf bytes.Buffer
	if err := gif.Encode(&buf, img, nil); err != nil {
		t.Fatalf("gif.Encode: %v", err)
	}
	return buf.Bytes()
}

func TestWeiboDownloadOSLargeSuccessAndAutomaticFallback(t *testing.T) {
	validJPG := makeWatermarkTestJPEG(t, 24, 18, color.RGBA{R: 20, G: 120, B: 200, A: 255})
	fallbackJPG := makeWatermarkTestJPEG(t, 32, 24, color.RGBA{R: 200, G: 80, B: 40, A: 255})
	placeholderGIF := makeWatermarkTestPlaceholderGIF(t)

	cases := []struct {
		name          string
		oslargeStatus int
		oslargeCT     string
		oslargeBody   []byte
		wantBody      []byte
		wantFallback  bool
	}{
		{
			name:          "oslarge_success",
			oslargeStatus: http.StatusOK,
			oslargeCT:     "image/jpeg",
			oslargeBody:   validJPG,
			wantBody:      validJPG,
			wantFallback:  false,
		},
		{
			name:          "oslarge_404_falls_back_to_largest",
			oslargeStatus: http.StatusNotFound,
			oslargeCT:     "text/plain",
			oslargeBody:   []byte("not found"),
			wantBody:      fallbackJPG,
			wantFallback:  true,
		},
		{
			name:          "oslarge_html_falls_back_to_largest",
			oslargeStatus: http.StatusOK,
			oslargeCT:     "text/html; charset=utf-8",
			oslargeBody:   []byte("<!doctype html><html><body>blocked</body></html>"),
			wantBody:      fallbackJPG,
			wantFallback:  true,
		},
		{
			name:          "oslarge_corrupt_bytes_falls_back_to_largest",
			oslargeStatus: http.StatusOK,
			oslargeCT:     "image/jpeg",
			oslargeBody:   []byte("NOT_A_VALID_IMAGE_HEADER_BYTES_1234567890"),
			wantBody:      fallbackJPG,
			wantFallback:  true,
		},
		{
			name:          "oslarge_truncated_jpeg_falls_back_to_largest",
			oslargeStatus: http.StatusOK,
			oslargeCT:     "image/jpeg",
			oslargeBody:   []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x01, 0x02},
			wantBody:      fallbackJPG,
			wantFallback:  true,
		},
		{
			name:          "oslarge_placeholder_gif_falls_back_to_largest",
			oslargeStatus: http.StatusOK,
			oslargeCT:     "image/gif",
			oslargeBody:   placeholderGIF,
			wantBody:      fallbackJPG,
			wantFallback:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var hitPaths []string
			var sawCookie bool

			rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				mu.Lock()
				hitPaths = append(hitPaths, req.URL.Path)
				if req.Header.Get("Cookie") != "" {
					sawCookie = true
				}
				mu.Unlock()

				switch req.URL.Path {
				case "/oslarge/001SynWmPic.jpg":
					return &http.Response{
						StatusCode:    tc.oslargeStatus,
						ContentLength: int64(len(tc.oslargeBody)),
						Header:        http.Header{"Content-Type": []string{tc.oslargeCT}},
						Body:          io.NopCloser(bytes.NewReader(tc.oslargeBody)),
					}, nil
				case "/mw2000/001SynWmPic.jpg":
					return &http.Response{
						StatusCode:    http.StatusOK,
						ContentLength: int64(len(fallbackJPG)),
						Header:        http.Header{"Content-Type": []string{"image/jpeg"}},
						Body:          io.NopCloser(bytes.NewReader(fallbackJPG)),
					}, nil
				default:
					return &http.Response{
						StatusCode: http.StatusNotFound,
						Header:     http.Header{"Content-Type": []string{"text/plain"}},
						Body:       io.NopCloser(strings.NewReader("missing")),
					}, nil
				}
			})

			mgr, libStore, _, _, _ := setupDouyinDownloadTestEnv(t, http.NotFoundHandler(), 2)
			mgr.SetMediaTransportWrap(func(_ http.RoundTripper) http.RoundTripper {
				return rt
			})
			planner := newFakeWeiboSitePlanner()
			mgr.RegisterSitePlanner(planner)

			plan := WorkDownloadPlanFromSite(site.DownloadPlan{
				Site:         site.Weibo,
				ItemKey:      "weibo:dynamic:5000000000009001",
				TargetID:     "5000000000009001",
				Kind:         "image_note",
				CanonicalURL: "https://weibo.com/1000000001/O000002lH",
				AuthorUID:    "1000000001",
				AuthorName:   "合成博主",
				Title:        "去水印回退单测",
				Desc:         "去水印回退单测",
				Images: []site.MediaImageAsset{
					{
						Index:     1,
						MediaRole: "image",
						Width:     1200,
						Height:    900,
						ImageURLs: []string{"https://wx1.sinaimg.cn/oslarge/001SynWmPic.jpg", "https://wx1.sinaimg.cn/mw2000/001SynWmPic.jpg"},
					},
				},
				Media: site.WeiboMediaTransport("Mozilla/5.0 Test", false),
			})

			task, err := mgr.EnqueueWeiboWork(context.Background(), WeiboWorkEnqueueRequest{
				TargetID: plan.TargetID,
				Kind:     "dynamic",
				Plan:     &plan,
			})
			if err != nil {
				t.Fatalf("EnqueueWeiboWork: %v", err)
			}
			finalTask := waitForTaskTerminal(t, mgr, task.TaskID, 10*time.Second)
			if finalTask.Status != "completed" {
				t.Fatalf("task status = %q err=%q (hits=%v)", finalTask.Status, finalTask.ErrorMessage, hitPaths)
			}
			if sawCookie {
				t.Fatal("Weibo CDN download sent Cookie header")
			}

			meta, ok, err := libStore.GetItem(plan.ItemKey)
			if err != nil || !ok || len(meta.NotePages) != 1 || meta.NotePages[0].Image == "" {
				t.Fatalf("saved item missing image: ok=%v err=%v meta=%+v", ok, err, meta)
			}
			gotBytes, err := os.ReadFile(filepath.Join(meta.ItemDir, filepath.FromSlash(meta.NotePages[0].Image)))
			if err != nil {
				t.Fatalf("read saved image: %v", err)
			}
			if !bytes.Equal(gotBytes, tc.wantBody) {
				t.Fatalf("saved image bytes len=%d, want len=%d", len(gotBytes), len(tc.wantBody))
			}

			if len(hitPaths) == 0 || hitPaths[0] != "/oslarge/001SynWmPic.jpg" {
				t.Fatalf("first requested path = %v, want /oslarge/001SynWmPic.jpg", hitPaths)
			}
			if tc.wantFallback {
				foundFallback := false
				for _, p := range hitPaths[1:] {
					if p == "/mw2000/001SynWmPic.jpg" {
						foundFallback = true
						break
					}
				}
				if !foundFallback {
					t.Fatalf("expected fallback to /mw2000/001SynWmPic.jpg, got %v", hitPaths)
				}
			} else if len(hitPaths) != 1 {
				t.Fatalf("successful /oslarge/ should not hit fallback: %v", hitPaths)
			}
		})
	}
}
