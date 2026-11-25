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

	"github.com/chialecode/trace/internal/library"
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

func makeWatermarkTestAnimatedGIF(t *testing.T) []byte {
	t.Helper()
	pal := color.Palette{
		color.RGBA{R: 30, G: 160, B: 220, A: 255},
		color.RGBA{R: 240, G: 90, B: 40, A: 255},
	}
	f1 := image.NewPaletted(image.Rect(0, 0, 16, 16), pal)
	f2 := image.NewPaletted(image.Rect(0, 0, 16, 16), pal)
	for i := range f2.Pix {
		f2.Pix[i] = 1
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, &gif.GIF{
		Image: []*image.Paletted{f1, f2},
		Delay: []int{10, 10},
	}); err != nil {
		t.Fatalf("gif.EncodeAll: %v", err)
	}
	return buf.Bytes()
}

func TestWeiboDualSaveCompanionDedupAndSilentSkip(t *testing.T) {
	origJPG := makeWatermarkTestJPEG(t, 40, 30, color.RGBA{R: 200, G: 80, B: 40, A: 255})
	nowmJPG := makeWatermarkTestJPEG(t, 24, 18, color.RGBA{R: 20, G: 120, B: 200, A: 255})
	placeholderGIF := makeWatermarkTestPlaceholderGIF(t)

	cases := []struct {
		name          string
		oslargeStatus int
		oslargeCT     string
		oslargeBody   []byte
		wantNoWM      bool
		wantNoWMBody  []byte
		wantNoWMW     int
		wantNoWMH     int
	}{
		{
			name:          "dual_save_distinct_oslarge",
			oslargeStatus: http.StatusOK,
			oslargeCT:     "image/jpeg",
			oslargeBody:   nowmJPG,
			wantNoWM:      true,
			wantNoWMBody:  nowmJPG,
			wantNoWMW:     24,
			wantNoWMH:     18,
		},
		{
			name:          "identical_sha256_deduplicated",
			oslargeStatus: http.StatusOK,
			oslargeCT:     "image/jpeg",
			oslargeBody:   origJPG,
			wantNoWM:      false,
		},
		{
			name:          "oslarge_404_silent_skip_primary_green",
			oslargeStatus: http.StatusNotFound,
			oslargeCT:     "text/plain",
			oslargeBody:   []byte("not found"),
			wantNoWM:      false,
		},
		{
			name:          "oslarge_403_silent_skip_primary_green",
			oslargeStatus: http.StatusForbidden,
			oslargeCT:     "text/plain",
			oslargeBody:   []byte("forbidden"),
			wantNoWM:      false,
		},
		{
			name:          "oslarge_html_silent_skip_primary_green",
			oslargeStatus: http.StatusOK,
			oslargeCT:     "text/html; charset=utf-8",
			oslargeBody:   []byte("<!doctype html><html><body>blocked</body></html>"),
			wantNoWM:      false,
		},
		{
			name:          "oslarge_corrupt_silent_skip_primary_green",
			oslargeStatus: http.StatusOK,
			oslargeCT:     "image/jpeg",
			oslargeBody:   []byte("NOT_A_VALID_IMAGE_HEADER_BYTES_1234567890"),
			wantNoWM:      false,
		},
		{
			name:          "oslarge_truncated_jpeg_silent_skip_primary_green",
			oslargeStatus: http.StatusOK,
			oslargeCT:     "image/jpeg",
			oslargeBody:   []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x01, 0x02},
			wantNoWM:      false,
		},
		{
			name:          "oslarge_placeholder_gif_silent_skip_primary_green",
			oslargeStatus: http.StatusOK,
			oslargeCT:     "image/gif",
			oslargeBody:   placeholderGIF,
			wantNoWM:      false,
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
				case "/large/001SynWmPic.jpg":
					return &http.Response{
						StatusCode:    http.StatusOK,
						ContentLength: int64(len(origJPG)),
						Header:        http.Header{"Content-Type": []string{"image/jpeg"}},
						Body:          io.NopCloser(bytes.NewReader(origJPG)),
					}, nil
				case "/oslarge/001SynWmPic.jpg":
					return &http.Response{
						StatusCode:    tc.oslargeStatus,
						ContentLength: int64(len(tc.oslargeBody)),
						Header:        http.Header{"Content-Type": []string{tc.oslargeCT}},
						Body:          io.NopCloser(bytes.NewReader(tc.oslargeBody)),
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
				Title:        "双份无水印单测",
				Desc:         "双份无水印单测",
				Images: []site.MediaImageAsset{
					{
						Index:            1,
						MediaRole:        "image",
						Width:            1200,
						Height:           900,
						ImageURLs:        []string{"https://wx1.sinaimg.cn/large/001SynWmPic.jpg"},
						WatermarkFreeURL: "https://wx1.sinaimg.cn/oslarge/001SynWmPic.jpg",
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
				t.Fatalf("saved item missing primary image: ok=%v err=%v meta=%+v", ok, err, meta)
			}
			if meta.Status != "complete" || len(meta.Missing) != 0 {
				t.Fatalf("primary item must stay green complete with 0 missing: status=%q missing=%+v", meta.Status, meta.Missing)
			}

			gotPrimary, err := os.ReadFile(filepath.Join(meta.ItemDir, filepath.FromSlash(meta.NotePages[0].Image)))
			if err != nil {
				t.Fatalf("read saved primary image: %v", err)
			}
			if !bytes.Equal(gotPrimary, origJPG) {
				t.Fatalf("primary image bytes len=%d, want official original len=%d", len(gotPrimary), len(origJPG))
			}

			var nowmMedia *library.MediaFile
			for i := range meta.Media {
				if meta.Media[i].Role == "image_nowm" {
					cp := meta.Media[i]
					nowmMedia = &cp
					break
				}
			}

			if tc.wantNoWM {
				if meta.NotePages[0].NoWatermark != "images/01_nowm.jpg" {
					t.Fatalf("NotePages[0].NoWatermark = %q, want images/01_nowm.jpg", meta.NotePages[0].NoWatermark)
				}
				if nowmMedia == nil || nowmMedia.Path != "images/01_nowm.jpg" || nowmMedia.SHA256 == "" ||
					nowmMedia.Width != tc.wantNoWMW || nowmMedia.Height != tc.wantNoWMH {
					t.Fatalf("unexpected image_nowm MediaFile: %+v", nowmMedia)
				}
				gotNoWM, err := os.ReadFile(filepath.Join(meta.ItemDir, filepath.FromSlash(nowmMedia.Path)))
				if err != nil {
					t.Fatalf("read companion image: %v", err)
				}
				if !bytes.Equal(gotNoWM, tc.wantNoWMBody) {
					t.Fatalf("companion bytes mismatch: got=%d want=%d", len(gotNoWM), len(tc.wantNoWMBody))
				}
			} else {
				if meta.NotePages[0].NoWatermark != "" || nowmMedia != nil {
					t.Fatalf("expected no companion saved, got NoWatermark=%q media=%+v", meta.NotePages[0].NoWatermark, nowmMedia)
				}
				if _, statErr := os.Stat(filepath.Join(meta.ItemDir, "images", "01_nowm.jpg")); !os.IsNotExist(statErr) {
					t.Fatalf("expected no stray 01_nowm.jpg on disk, statErr=%v", statErr)
				}
			}

			if len(hitPaths) != 2 || hitPaths[0] != "/large/001SynWmPic.jpg" || hitPaths[1] != "/oslarge/001SynWmPic.jpg" {
				t.Fatalf("requested paths = %v, want [/large/001SynWmPic.jpg, /oslarge/001SynWmPic.jpg]", hitPaths)
			}
		})
	}
}

func TestWeiboDualSaveSkipsGIFAndBackfillsOldCachedItem(t *testing.T) {
	origJPG := makeWatermarkTestJPEG(t, 40, 30, color.RGBA{R: 180, G: 60, B: 40, A: 255})
	nowmJPG := makeWatermarkTestJPEG(t, 24, 18, color.RGBA{R: 30, G: 130, B: 210, A: 255})
	animGIF := makeWatermarkTestAnimatedGIF(t)

	var mu sync.Mutex
	var hitPaths []string
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		mu.Lock()
		hitPaths = append(hitPaths, req.URL.Path)
		mu.Unlock()
		switch req.URL.Path {
		case "/large/pic01.jpg":
			return &http.Response{
				StatusCode:    http.StatusOK,
				ContentLength: int64(len(origJPG)),
				Header:        http.Header{"Content-Type": []string{"image/jpeg"}},
				Body:          io.NopCloser(bytes.NewReader(origJPG)),
			}, nil
		case "/oslarge/pic01.jpg":
			return &http.Response{
				StatusCode:    http.StatusOK,
				ContentLength: int64(len(nowmJPG)),
				Header:        http.Header{"Content-Type": []string{"image/jpeg"}},
				Body:          io.NopCloser(bytes.NewReader(nowmJPG)),
			}, nil
		case "/large/anim02.gif":
			return &http.Response{
				StatusCode:    http.StatusOK,
				ContentLength: int64(len(animGIF)),
				Header:        http.Header{"Content-Type": []string{"image/gif"}},
				Body:          io.NopCloser(bytes.NewReader(animGIF)),
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
	mgr.SetMediaTransportWrap(func(_ http.RoundTripper) http.RoundTripper { return rt })
	planner := newFakeWeiboSitePlanner()
	mgr.RegisterSitePlanner(planner)

	// Step 1: Initial cache simulating an older cached item (WatermarkFreeURL empty -> no companion yet).
	planV1 := WorkDownloadPlanFromSite(site.DownloadPlan{
		Site:         site.Weibo,
		ItemKey:      "weibo:dynamic:5000000000009002",
		TargetID:     "5000000000009002",
		Kind:         "image_note",
		CanonicalURL: "https://weibo.com/1000000001/O000002lI",
		AuthorUID:    "1000000001",
		AuthorName:   "合成博主",
		Title:        "旧条目补齐无水印与GIF跳过",
		Desc:         "旧条目补齐无水印与GIF跳过",
		Images: []site.MediaImageAsset{
			{Index: 1, MediaRole: "image", Width: 1200, Height: 900, ImageURLs: []string{"https://wx1.sinaimg.cn/large/pic01.jpg"}},
			{Index: 2, MediaRole: "gif", Width: 16, Height: 16, ImageURLs: []string{"https://wx1.sinaimg.cn/large/anim02.gif"}},
		},
		Media: site.WeiboMediaTransport("Mozilla/5.0 Test", false),
	})
	planV1.Pages[0].MediaID = "pic01"
	planV1.Pages[1].MediaID = "anim02"

	task1, err := mgr.EnqueueWeiboWork(context.Background(), WeiboWorkEnqueueRequest{
		TargetID: planV1.TargetID,
		Kind:     "dynamic",
		Plan:     &planV1,
	})
	if err != nil {
		t.Fatalf("EnqueueWeiboWork initial: %v", err)
	}
	if ft := waitForTaskTerminal(t, mgr, task1.TaskID, 10*time.Second); ft.Status != "completed" {
		t.Fatalf("initial task = %+v", ft)
	}
	meta1, ok, err := libStore.GetItem(planV1.ItemKey)
	if err != nil || !ok || len(meta1.NotePages) != 2 || meta1.NotePages[0].NoWatermark != "" {
		t.Fatalf("initial item unexpected: ok=%v err=%v meta=%+v", ok, err, meta1)
	}

	// Step 2: Re-cache same item with WatermarkFreeURL enabled -> MUST reuse primary images/01.jpg
	// (0 requests to /large/pic01.jpg or /large/anim02.gif!), fetch ONLY /oslarge/pic01.jpg,
	// skip GIF completely, and write images/01_nowm.jpg into item.json!
	mu.Lock()
	hitPaths = nil
	mu.Unlock()

	planV2 := planV1
	planV2.Pages = append([]PlannedNotePage(nil), planV1.Pages...)
	planV2.Pages[0].WatermarkFreeURL = "https://wx1.sinaimg.cn/oslarge/pic01.jpg"
	// Deliberately also set a bogus WatermarkFreeURL on the GIF page to confirm case "gif" ignores it.
	planV2.Pages[1].WatermarkFreeURL = "https://wx1.sinaimg.cn/oslarge/anim02.gif"

	task2, err := mgr.EnqueueWeiboWork(context.Background(), WeiboWorkEnqueueRequest{
		TargetID: planV2.TargetID,
		Kind:     "dynamic",
		Plan:     &planV2,
	})
	if err != nil {
		t.Fatalf("EnqueueWeiboWork backfill: %v", err)
	}
	if ft := waitForTaskTerminal(t, mgr, task2.TaskID, 10*time.Second); ft.Status != "completed" {
		t.Fatalf("backfill task = %+v (hits=%v)", ft, hitPaths)
	}

	mu.Lock()
	gotBackfillHits := append([]string(nil), hitPaths...)
	mu.Unlock()
	if len(gotBackfillHits) != 1 || gotBackfillHits[0] != "/oslarge/pic01.jpg" {
		t.Fatalf("backfill hits = %v, want ONLY [/oslarge/pic01.jpg] (primary reused, GIF skipped)", gotBackfillHits)
	}

	meta2, ok, err := libStore.GetItem(planV2.ItemKey)
	if err != nil || !ok || meta2.Status != "complete" || len(meta2.Missing) != 0 {
		t.Fatalf("backfilled item unexpected: ok=%v err=%v status=%q missing=%+v", ok, err, meta2.Status, meta2.Missing)
	}
	if len(meta2.NotePages) != 2 || meta2.NotePages[0].Image != "images/01.jpg" || meta2.NotePages[0].NoWatermark != "images/01_nowm.jpg" {
		t.Fatalf("backfilled NotePages[0] = %+v", meta2.NotePages)
	}
	if meta2.NotePages[1].Image != "images/02.gif" || meta2.NotePages[1].NoWatermark != "" {
		t.Fatalf("GIF page must never save companion: %+v", meta2.NotePages[1])
	}
	gotNoWM, err := os.ReadFile(filepath.Join(meta2.ItemDir, "images", "01_nowm.jpg"))
	if err != nil || !bytes.Equal(gotNoWM, nowmJPG) {
		t.Fatalf("backfilled companion read = (len=%d, err=%v), want len=%d", len(gotNoWM), err, len(nowmJPG))
	}

	// Step 3: Re-cache again with WatermarkFreeURL empty (setting turned off) ->
	// monotonically preserves existing verified companion without extra requests!
	mu.Lock()
	hitPaths = nil
	mu.Unlock()

	task3, err := mgr.EnqueueWeiboWork(context.Background(), WeiboWorkEnqueueRequest{
		TargetID: planV1.TargetID,
		Kind:     "dynamic",
		Plan:     &planV1,
	})
	if err != nil {
		t.Fatalf("EnqueueWeiboWork preserve: %v", err)
	}
	if ft := waitForTaskTerminal(t, mgr, task3.TaskID, 10*time.Second); ft.Status != "completed" {
		t.Fatalf("preserve task = %+v", ft)
	}
	mu.Lock()
	gotPreserveHits := append([]string(nil), hitPaths...)
	mu.Unlock()
	if len(gotPreserveHits) != 0 {
		t.Fatalf("expected 0 network requests when reusing both primary & companion, got %v", gotPreserveHits)
	}
	meta3, ok, err := libStore.GetItem(planV1.ItemKey)
	if err != nil || !ok || meta3.NotePages[0].NoWatermark != "images/01_nowm.jpg" {
		t.Fatalf("companion was not monotonically preserved: ok=%v err=%v pages=%+v", ok, err, meta3.NotePages)
	}
}

func TestWeiboConflictDeleteReorderDoesNotMisbindCompanion(t *testing.T) {
	picAOrig := makeWatermarkTestJPEG(t, 40, 30, color.RGBA{R: 210, G: 40, B: 40, A: 255})
	picANoWM := makeWatermarkTestJPEG(t, 24, 18, color.RGBA{R: 250, G: 120, B: 40, A: 255})
	picBOrig := makeWatermarkTestJPEG(t, 44, 32, color.RGBA{R: 40, G: 90, B: 210, A: 255})
	picCOrig := makeWatermarkTestJPEG(t, 38, 28, color.RGBA{R: 40, G: 180, B: 90, A: 255})
	picCNoWM := makeWatermarkTestJPEG(t, 22, 16, color.RGBA{R: 90, G: 220, B: 140, A: 255})

	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/large/picA.jpg":
			return &http.Response{StatusCode: http.StatusOK, ContentLength: int64(len(picAOrig)), Header: http.Header{"Content-Type": []string{"image/jpeg"}}, Body: io.NopCloser(bytes.NewReader(picAOrig))}, nil
		case "/oslarge/picA.jpg":
			return &http.Response{StatusCode: http.StatusOK, ContentLength: int64(len(picANoWM)), Header: http.Header{"Content-Type": []string{"image/jpeg"}}, Body: io.NopCloser(bytes.NewReader(picANoWM))}, nil
		case "/large/picB.jpg":
			return &http.Response{StatusCode: http.StatusOK, ContentLength: int64(len(picBOrig)), Header: http.Header{"Content-Type": []string{"image/jpeg"}}, Body: io.NopCloser(bytes.NewReader(picBOrig))}, nil
		case "/large/picC.jpg":
			return &http.Response{StatusCode: http.StatusOK, ContentLength: int64(len(picCOrig)), Header: http.Header{"Content-Type": []string{"image/jpeg"}}, Body: io.NopCloser(bytes.NewReader(picCOrig))}, nil
		case "/oslarge/picC.jpg":
			return &http.Response{StatusCode: http.StatusOK, ContentLength: int64(len(picCNoWM)), Header: http.Header{"Content-Type": []string{"image/jpeg"}}, Body: io.NopCloser(bytes.NewReader(picCNoWM))}, nil
		default:
			return &http.Response{StatusCode: http.StatusNotFound, Header: http.Header{"Content-Type": []string{"text/plain"}}, Body: io.NopCloser(strings.NewReader("missing"))}, nil
		}
	})

	t.Run("keep_both_delete_first_image_does_not_steal_picA_companion_for_picB", func(t *testing.T) {
		mgr, libStore, _, _, _ := setupDouyinDownloadTestEnv(t, http.NotFoundHandler(), 2)
		mgr.SetMediaTransportWrap(func(_ http.RoundTripper) http.RoundTripper { return rt })
		mgr.RegisterSitePlanner(newFakeWeiboSitePlanner())

		// V1: [picA (has 01_nowm), picB (/oslarge/picB -> 404, no 02_nowm)].
		planV1 := WorkDownloadPlanFromSite(site.DownloadPlan{
			Site:         site.Weibo,
			ItemKey:      "weibo:dynamic:5000000000009003",
			TargetID:     "5000000000009003",
			Kind:         "image_note",
			CanonicalURL: "https://weibo.com/1000000001/O000002lJ",
			AuthorUID:    "1000000001",
			AuthorName:   "合成博主",
			Title:        "冲突删图重排回归",
			Desc:         "冲突删图重排回归",
			Images: []site.MediaImageAsset{
				{Index: 1, MediaRole: "image", Width: 40, Height: 30, ImageURLs: []string{"https://wx1.sinaimg.cn/large/picA.jpg"}, WatermarkFreeURL: "https://wx1.sinaimg.cn/oslarge/picA.jpg"},
				{Index: 2, MediaRole: "image", Width: 44, Height: 32, ImageURLs: []string{"https://wx1.sinaimg.cn/large/picB.jpg"}, WatermarkFreeURL: "https://wx1.sinaimg.cn/oslarge/picB.jpg"},
			},
			Media: site.WeiboMediaTransport("Mozilla/5.0 Test", false),
		})
		planV1.Pages[0].MediaID = "picA"
		planV1.Pages[1].MediaID = "picB"

		task1, err := mgr.EnqueueWeiboWork(context.Background(), WeiboWorkEnqueueRequest{TargetID: planV1.TargetID, Kind: "dynamic", Plan: &planV1})
		if err != nil {
			t.Fatalf("EnqueueWeiboWork V1: %v", err)
		}
		if ft := waitForTaskTerminal(t, mgr, task1.TaskID, 10*time.Second); ft.Status != "completed" {
			t.Fatalf("V1 task = %+v", ft)
		}
		v1Meta, ok, err := libStore.GetItem(planV1.ItemKey)
		if err != nil || !ok || len(v1Meta.NotePages) != 2 || v1Meta.NotePages[0].NoWatermark != "images/01_nowm.jpg" || v1Meta.NotePages[1].NoWatermark != "" {
			t.Fatalf("unexpected V1 state: ok=%v err=%v pages=%+v", ok, err, v1Meta.NotePages)
		}

		// V2 (keep_both): picA deleted, picB shifts to index 1 while /oslarge/picB still 404s.
		planV2 := WorkDownloadPlanFromSite(site.DownloadPlan{
			Site:         site.Weibo,
			ItemKey:      planV1.ItemKey,
			TargetID:     planV1.TargetID,
			Kind:         "image_note",
			CanonicalURL: planV1.CanonicalURL,
			AuthorUID:    planV1.AuthorUID,
			AuthorName:   planV1.AuthorName,
			Title:        planV1.Title,
			Desc:         planV1.Desc,
			Images: []site.MediaImageAsset{
				{Index: 1, MediaRole: "image", Width: 44, Height: 32, ImageURLs: []string{"https://wx1.sinaimg.cn/large/picB.jpg"}, WatermarkFreeURL: "https://wx1.sinaimg.cn/oslarge/picB.jpg"},
			},
			Media: site.WeiboMediaTransport("Mozilla/5.0 Test", false),
		})
		planV2.Pages[0].MediaID = "picB"
		planV2.ConflictResolution = ConflictResolveKeepBoth

		task2, err := mgr.EnqueueWeiboWork(WithConflictResolution(context.Background(), ConflictResolveKeepBoth), WeiboWorkEnqueueRequest{
			TargetID: planV2.TargetID,
			Kind:     "dynamic",
			Plan:     &planV2,
		})
		if err != nil {
			t.Fatalf("EnqueueWeiboWork V2: %v", err)
		}
		if ft := waitForTaskTerminal(t, mgr, task2.TaskID, 10*time.Second); ft.Status != "completed" {
			t.Fatalf("V2 task = %+v", ft)
		}

		v2Meta, ok, err := libStore.GetItem(planV2.ItemKey)
		if err != nil || !ok || len(v2Meta.NotePages) != 1 {
			t.Fatalf("unexpected V2 meta: ok=%v err=%v meta=%+v", ok, err, v2Meta)
		}
		if v2Meta.NotePages[0].NoWatermark != "" {
			t.Fatalf("picB must NOT inherit picA's 01_nowm.jpg, got NoWatermark=%q", v2Meta.NotePages[0].NoWatermark)
		}
		for _, mf := range v2Meta.Media {
			if mf.Role == "image_nowm" {
				t.Fatalf("V2 unexpectedly kept image_nowm %+v from deleted picA", mf)
			}
		}
		if _, statErr := os.Stat(filepath.Join(v2Meta.ItemDir, "images", "01_nowm.jpg")); !os.IsNotExist(statErr) {
			t.Fatalf("V2 directory unexpectedly contains images/01_nowm.jpg: statErr=%v", statErr)
		}
		gotPrimary, err := os.ReadFile(filepath.Join(v2Meta.ItemDir, filepath.FromSlash(v2Meta.NotePages[0].Image)))
		if err != nil || !bytes.Equal(gotPrimary, picBOrig) {
			t.Fatalf("V2 primary mismatch: err=%v len=%d want=%d", err, len(gotPrimary), len(picBOrig))
		}
	})

	t.Run("use_remote_shift_recovers_own_previous_companion_by_original_image_stem", func(t *testing.T) {
		mgr, libStore, _, _, _ := setupDouyinDownloadTestEnv(t, http.NotFoundHandler(), 2)
		oslargeCDown := false
		var mu sync.Mutex
		rt2 := roundTripFunc(func(req *http.Request) (*http.Response, error) {
			mu.Lock()
			down := oslargeCDown
			mu.Unlock()
			if down && req.URL.Path == "/oslarge/picC.jpg" {
				return &http.Response{StatusCode: http.StatusNotFound, Header: http.Header{"Content-Type": []string{"text/plain"}}, Body: io.NopCloser(strings.NewReader("gone"))}, nil
			}
			return rt(req)
		})
		mgr.SetMediaTransportWrap(func(_ http.RoundTripper) http.RoundTripper { return rt2 })
		mgr.RegisterSitePlanner(newFakeWeiboSitePlanner())

		// V1: [picA (has 01_nowm), picC (has 02_nowm)].
		planV1 := WorkDownloadPlanFromSite(site.DownloadPlan{
			Site:         site.Weibo,
			ItemKey:      "weibo:dynamic:5000000000009004",
			TargetID:     "5000000000009004",
			Kind:         "image_note",
			CanonicalURL: "https://weibo.com/1000000001/O000002lK",
			AuthorUID:    "1000000001",
			AuthorName:   "合成博主",
			Title:        "冲突重排精准复用自身伴侣图",
			Desc:         "冲突重排精准复用自身伴侣图",
			Images: []site.MediaImageAsset{
				{Index: 1, MediaRole: "image", Width: 40, Height: 30, ImageURLs: []string{"https://wx1.sinaimg.cn/large/picA.jpg"}, WatermarkFreeURL: "https://wx1.sinaimg.cn/oslarge/picA.jpg"},
				{Index: 2, MediaRole: "image", Width: 38, Height: 28, ImageURLs: []string{"https://wx1.sinaimg.cn/large/picC.jpg"}, WatermarkFreeURL: "https://wx1.sinaimg.cn/oslarge/picC.jpg"},
			},
			Media: site.WeiboMediaTransport("Mozilla/5.0 Test", false),
		})
		planV1.Pages[0].MediaID = "picA"
		planV1.Pages[1].MediaID = "picC"

		task1, err := mgr.EnqueueWeiboWork(context.Background(), WeiboWorkEnqueueRequest{TargetID: planV1.TargetID, Kind: "dynamic", Plan: &planV1})
		if err != nil {
			t.Fatalf("EnqueueWeiboWork V1: %v", err)
		}
		if ft := waitForTaskTerminal(t, mgr, task1.TaskID, 10*time.Second); ft.Status != "completed" {
			t.Fatalf("V1 task = %+v", ft)
		}
		// Simulate an item where NotePages[1].NoWatermark was empty so stem lookup from reusableLP.Image is exercised.
		v1Meta, ok, err := libStore.GetItem(planV1.ItemKey)
		if err != nil || !ok || len(v1Meta.NotePages) != 2 {
			t.Fatalf("load V1: ok=%v err=%v", ok, err)
		}
		v1Meta.NotePages[1].NoWatermark = ""
		if _, err := libStore.SaveItem(v1Meta.ItemDir, v1Meta); err != nil {
			t.Fatalf("SaveItem V1: %v", err)
		}

		// Now /oslarge/picC expires (404), and V2 deletes picA shifting picC to index 1.
		mu.Lock()
		oslargeCDown = true
		mu.Unlock()

		planV2 := WorkDownloadPlanFromSite(site.DownloadPlan{
			Site:         site.Weibo,
			ItemKey:      planV1.ItemKey,
			TargetID:     planV1.TargetID,
			Kind:         "image_note",
			CanonicalURL: planV1.CanonicalURL,
			AuthorUID:    planV1.AuthorUID,
			AuthorName:   planV1.AuthorName,
			Title:        planV1.Title,
			Desc:         planV1.Desc,
			Images: []site.MediaImageAsset{
				{Index: 1, MediaRole: "image", Width: 38, Height: 28, ImageURLs: []string{"https://wx1.sinaimg.cn/large/picC.jpg"}, WatermarkFreeURL: "https://wx1.sinaimg.cn/oslarge/picC.jpg"},
			},
			Media: site.WeiboMediaTransport("Mozilla/5.0 Test", false),
		})
		planV2.Pages[0].MediaID = "picC"
		planV2.ConflictResolution = ConflictResolveUseRemote

		task2, err := mgr.EnqueueWeiboWork(WithConflictResolution(context.Background(), ConflictResolveUseRemote), WeiboWorkEnqueueRequest{
			TargetID: planV2.TargetID,
			Kind:     "dynamic",
			Plan:     &planV2,
		})
		if err != nil {
			t.Fatalf("EnqueueWeiboWork V2: %v", err)
		}
		if ft := waitForTaskTerminal(t, mgr, task2.TaskID, 10*time.Second); ft.Status != "completed" {
			t.Fatalf("V2 task = %+v", ft)
		}

		v2Meta, ok, err := libStore.GetItem(planV2.ItemKey)
		if err != nil || !ok || len(v2Meta.NotePages) != 1 || v2Meta.NotePages[0].NoWatermark != "images/01_nowm.jpg" {
			t.Fatalf("expected picC's own companion recovered at images/01_nowm.jpg, got ok=%v err=%v pages=%+v", ok, err, v2Meta.NotePages)
		}
		gotNoWM, err := os.ReadFile(filepath.Join(v2Meta.ItemDir, "images", "01_nowm.jpg"))
		if err != nil || !bytes.Equal(gotNoWM, picCNoWM) {
			t.Fatalf("V2 companion bytes mismatch: err=%v len=%d want picCNoWM len=%d (must not be picANoWM len=%d)", err, len(gotNoWM), len(picCNoWM), len(picANoWM))
		}
	})
}
