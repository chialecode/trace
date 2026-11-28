package download

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
)

func synthSolidPNG(w, h int, c color.RGBA) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func TestWeiboDownload_EditHistoryArchivesUniqueImagesAndDegradesSafely(t *testing.T) {
	curPNG := synthSolidPNG(320, 200, color.RGBA{R: 30, G: 110, B: 220, A: 255})
	oldPNG := synthSolidPNG(240, 320, color.RGBA{R: 220, G: 120, B: 40, A: 255})

	var curHits atomic.Int32
	var oldHits atomic.Int32
	var missingHits atomic.Int32

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/large/picCur01.png":
			curHits.Add(1)
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Content-Length", strconv.Itoa(len(curPNG)))
			_, _ = w.Write(curPNG)
		case "/large/picOld01.png":
			oldHits.Add(1)
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Content-Length", strconv.Itoa(len(oldPNG)))
			_, _ = w.Write(oldPNG)
		case "/large/picGone404.png":
			missingHits.Add(1)
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	mgr, libStore, _, _, _ := setupDouyinDownloadTestEnv(t, http.NotFoundHandler(), 2)
	mgr.SetMediaTransportWrap(func(_ http.RoundTripper) http.RoundTripper {
		return roundTripFunc(func(req *http.Request) (*http.Response, error) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			resp := rec.Result()
			resp.Request = req
			return resp, nil
		})
	})

	strictTransport := site.WeiboMediaTransport("TRACE-Test-UA", false)
	const mid = "5008001002003001"

	plan := &WorkDownloadPlan{
		Site:         site.Weibo,
		ItemKey:      "weibo:dynamic:" + mid,
		Kind:         "dynamic",
		TargetID:     mid,
		Title:        "修订后正文",
		Desc:         "修订后正文",
		CanonicalURL: "https://weibo.com/1000000001/" + mid,
		PubTs:        1764203600,
		EditCount:    2,
		AuthorUID:    "1000000001",
		AuthorName:   "合成博主",
		Pages: []PlannedNotePage{
			{
				Index:     1,
				Kind:      "image",
				MediaID:   "picCur01",
				Width:     320,
				Height:    200,
				ImageURLs: []string{"https://wx1.sinaimg.cn/large/picCur01.png"},
			},
		},
		EditHistory: []PlannedEditRevision{
			{
				Version: 3,
				EditTs:  1764203600,
				Text:    "修订后正文",
				Links:   []site.TextLink{{Label: "说明", URL: "https://example.org/v3"}},
				Images: []PlannedEditImage{
					// Same pic_id as current post -> must reuse images/01.png with 0 duplicate download!
					{PicID: "picCur01", Kind: "image", Width: 320, Height: 200, ImageURLs: []string{"https://wx1.sinaimg.cn/large/picCur01.png"}},
				},
			},
			{
				Version: 2,
				EditTs:  1764201800,
				Text:    "中间修订版（含已替换旧图与已失效图）",
				Images: []PlannedEditImage{
					{PicID: "picOld01", Kind: "image", Width: 240, Height: 320, ImageURLs: []string{"https://wx2.sinaimg.cn/large/picOld01.png"}},
					// Failing historical image -> must skip cleanly without degrading main post!
					{PicID: "picGone404", Kind: "image", Width: 200, Height: 200, ImageURLs: []string{"https://wx3.sinaimg.cn/large/picGone404.png"}},
				},
			},
			{
				Version: 1,
				EditTs:  1764200000,
				Text:    "原始版本（复用 v2 历史旧图）",
				Images: []PlannedEditImage{
					// Same pic_id as v2 -> must reuse v2's downloaded edit_image without downloading twice!
					{PicID: "picOld01", Kind: "image", Width: 240, Height: 320, ImageURLs: []string{"https://wx2.sinaimg.cn/large/picOld01.png"}},
				},
			},
		},
		Transport: strictTransport,
	}

	task, err := mgr.EnqueueWeiboWork(context.Background(), WeiboWorkEnqueueRequest{
		TargetID: mid,
		Kind:     "dynamic",
		Source:   "manual",
		Plan:     plan,
	})
	if err != nil {
		t.Fatalf("EnqueueWeiboWork: %v", err)
	}
	done := waitForTaskTerminal(t, mgr, task.TaskID, 10*time.Second)
	if done.Status != "completed" {
		t.Fatalf("expected completed despite missing historical image, got status=%s err=%s", done.Status, done.ErrorMessage)
	}

	item, ok, err := libStore.GetItem("weibo:dynamic:" + mid)
	if err != nil || !ok {
		t.Fatalf("GetItem failed: ok=%v err=%v", ok, err)
	}
	if item.Version != 1 {
		t.Fatalf("item.json version changed: got %d, want 1", item.Version)
	}
	if item.Status != "complete" || len(item.Missing) != 0 {
		t.Fatalf("expected complete with 0 missing, got status=%s missing=%+v", item.Status, item.Missing)
	}
	if len(item.EditHistory) != 3 {
		t.Fatalf("expected 3 edit revisions persisted, got %+v", item.EditHistory)
	}

	// Verify current pic reused once and old unique pic downloaded once with role="edit_image".
	if curHits.Load() != 1 {
		t.Fatalf("expected picCur01 downloaded once (reused in v3), got %d hits", curHits.Load())
	}
	if oldHits.Load() != 1 {
		t.Fatalf("expected picOld01 downloaded once (shared by v2 & v1), got %d hits", oldHits.Load())
	}
	if missingHits.Load() == 0 {
		t.Fatalf("expected missing historical image attempted at least once")
	}

	if len(item.EditHistory[0].MediaPaths) != 1 || item.EditHistory[0].MediaPaths[0] != "images/01.png" {
		t.Fatalf("expected v3 to reuse images/01.png, got %+v", item.EditHistory[0].MediaPaths)
	}
	if len(item.EditHistory[1].MediaPaths) != 1 || item.EditHistory[1].MediaPaths[0] != "images/edit_v02_01.png" {
		t.Fatalf("expected v2 to store images/edit_v02_01.png, got %+v", item.EditHistory[1].MediaPaths)
	}
	if len(item.EditHistory[2].MediaPaths) != 1 || item.EditHistory[2].MediaPaths[0] != "images/edit_v02_01.png" {
		t.Fatalf("expected v1 to reuse images/edit_v02_01.png, got %+v", item.EditHistory[2].MediaPaths)
	}

	// Verify role isolation: main gallery has 1 "image", historical photo is strictly "edit_image".
	imgRoles, editRoles := 0, 0
	for _, mf := range item.Media {
		if mf.Role == "image" {
			imgRoles++
		}
		if mf.Role == "edit_image" {
			editRoles++
		}
	}
	if imgRoles != 1 || editRoles != 1 {
		t.Fatalf("expected 1 image role and 1 edit_image role, got image=%d edit_image=%d media=%+v", imgRoles, editRoles, item.Media)
	}
	if _, err := os.Stat(filepath.Join(item.ItemDir, "images", "edit_v02_01.png")); err != nil {
		t.Fatalf("expected historical image file on disk: %v", err)
	}

	// Re-cache with empty EditHistory (simulating degraded API on re-check) -> preserves previous EditHistory & edit_image!
	degradedPlan := *plan
	degradedPlan.EditHistory = nil
	task2, err := mgr.EnqueueWeiboWork(context.Background(), WeiboWorkEnqueueRequest{
		TargetID: mid,
		Kind:     "dynamic",
		Source:   "auto",
		Plan:     &degradedPlan,
	})
	if err != nil {
		t.Fatalf("EnqueueWeiboWork re-cache: %v", err)
	}
	done2 := waitForTaskTerminal(t, mgr, task2.TaskID, 10*time.Second)
	if done2.Status != "completed" {
		t.Fatalf("re-cache status=%s err=%s", done2.Status, done2.ErrorMessage)
	}
	item2, ok, err := libStore.GetItem("weibo:dynamic:" + mid)
	if err != nil || !ok {
		t.Fatalf("GetItem after re-cache: ok=%v err=%v", ok, err)
	}
	if len(item2.EditHistory) != 3 {
		t.Fatalf("expected previous EditHistory preserved across degraded re-cache, got %+v", item2.EditHistory)
	}
	if _, err := os.Stat(filepath.Join(item2.ItemDir, "images", "edit_v02_01.png")); err != nil {
		t.Fatalf("expected preserved edit_image on disk after staged swap: %v", err)
	}
}

func TestWeiboDownload_PartialFailureDeferPreservesPreviousEditHistory(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(418)
		_, _ = w.Write([]byte("blocked"))
	})

	mgr, libStore, _, _, _ := setupDouyinDownloadTestEnv(t, http.NotFoundHandler(), 2)
	mgr.SetMediaTransportWrap(func(_ http.RoundTripper) http.RoundTripper {
		return roundTripFunc(func(req *http.Request) (*http.Response, error) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			resp := rec.Result()
			resp.Request = req
			return resp, nil
		})
	})

	const (
		mid       = "5008001002003099"
		authorUID = "1000000001"
	)
	itemDir := filepath.Join(libStore.CacheDir(), "weibo", authorUID, "posts", mid)
	if err := os.MkdirAll(itemDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Seed an existing partial post (no valid local media -> hasProtectedLocal == false)
	// that already carries cached EditHistory metadata.
	_, err := libStore.SaveItem(itemDir, library.ItemMetadata{
		Version:     1,
		ID:          "dynamic:" + mid,
		Site:        string(site.Weibo),
		Kind:        "dynamic",
		DynamicType: "draw",
		TypeLabel:   "图文",
		Status:      "partial",
		Title:       "修订后正文",
		Desc:        "修订后正文",
		Text:        "修订后正文",
		URL:         "https://weibo.com/" + authorUID + "/" + mid,
		PubTs:       1764203600,
		EditCount:   1,
		Author: library.ItemAuthor{
			Site: string(site.Weibo),
			UID:  authorUID,
			Name: "合成博主",
		},
		Missing: []library.MissingRecord{{Item: "page_01", Reason: "上次下载未完成"}},
		EditHistory: []library.ItemEditRevision{
			{Version: 2, EditTs: 1764203600, Text: "修订后正文"},
			{Version: 1, EditTs: 1764200000, Text: "首发原始版本"},
		},
	})
	if err != nil {
		t.Fatalf("SaveItem seed: %v", err)
	}

	strictTransport := site.WeiboMediaTransport("TRACE-Test-UA", false)
	plan := &WorkDownloadPlan{
		Site:         site.Weibo,
		ItemKey:      "weibo:dynamic:" + mid,
		Kind:         "dynamic",
		TargetID:     mid,
		Title:        "修订后正文",
		Desc:         "修订后正文",
		CanonicalURL: "https://weibo.com/" + authorUID + "/" + mid,
		PubTs:        1764203600,
		EditCount:    1,
		AuthorUID:    authorUID,
		AuthorName:   "合成博主",
		Pages: []PlannedNotePage{
			{
				Index:     1,
				Kind:      "image",
				MediaID:   "picBlocked01",
				Width:     320,
				Height:    200,
				ImageURLs: []string{"https://wx1.sinaimg.cn/large/picBlocked01.png"},
			},
		},
		Transport: strictTransport,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("mgr.Start: %v", err)
	}

	task, err := mgr.EnqueueWeiboWork(context.Background(), WeiboWorkEnqueueRequest{
		TargetID: mid,
		Kind:     "dynamic",
		Source:   "manual",
		Plan:     plan,
	})
	if err != nil {
		t.Fatalf("EnqueueWeiboWork: %v", err)
	}
	done := waitForTaskTerminal(t, mgr, task.TaskID, 10*time.Second)
	if done.Status == "completed" {
		t.Fatalf("expected retry to abort on HTTP 418, got completed")
	}

	item, ok, err := libStore.GetItem("weibo:dynamic:" + mid)
	if err != nil || !ok {
		t.Fatalf("GetItem after partial failure: ok=%v err=%v", ok, err)
	}
	if item.Status != "partial" {
		t.Fatalf("expected partial status, got %q", item.Status)
	}
	if len(item.EditHistory) != 2 || item.EditHistory[0].Text != "修订后正文" || item.EditHistory[1].Text != "首发原始版本" {
		t.Fatalf("expected previous EditHistory preserved by partialMeta defer, got %+v", item.EditHistory)
	}
}
