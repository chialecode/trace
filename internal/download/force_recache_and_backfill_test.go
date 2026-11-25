package download

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image/color"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
)

func sha256HexBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestEffectiveForceOverwriteManualOnlyContract(t *testing.T) {
	ctx := WithForceOverwrite(context.Background())
	if !ForceOverwriteFromContext(ctx) {
		t.Fatal("ForceOverwriteFromContext should be true")
	}
	if !effectiveForceOverwrite(ctx, "manual") {
		t.Fatal("manual source with WithForceOverwrite must allow forceOverwrite")
	}
	if !effectiveForceOverwrite(ctx, "") {
		t.Fatal("empty manual source with WithForceOverwrite must allow forceOverwrite")
	}
	if effectiveForceOverwrite(ctx, "auto") {
		t.Fatal("auto source must NEVER honor forceOverwrite")
	}
	if effectiveForceOverwrite(ctx, "queue") {
		t.Fatal("queue source must NEVER honor forceOverwrite")
	}
	if effectiveForceOverwrite(site.WithAutomaticRequests(ctx, "queue"), "manual") {
		t.Fatal("automatic request context must NEVER honor forceOverwrite")
	}
	if effectiveForceOverwrite(context.Background(), "manual") {
		t.Fatal("plain context without flag must not enable forceOverwrite")
	}
}

func TestBackfillWeiboNoWMCompanionsDirectCDNAndRateLimitStop(t *testing.T) {
	orig1 := makeWatermarkTestJPEG(t, 48, 36, color.RGBA{R: 200, G: 60, B: 40, A: 255})
	nowm1 := makeWatermarkTestJPEG(t, 32, 24, color.RGBA{R: 30, G: 130, B: 210, A: 255})
	orig2 := makeWatermarkTestJPEG(t, 40, 30, color.RGBA{R: 60, G: 180, B: 80, A: 255})
	orig3 := makeWatermarkTestJPEG(t, 44, 32, color.RGBA{R: 150, G: 90, B: 200, A: 255})
	gif4 := makeWatermarkTestAnimatedGIF(t)

	var (
		mu        sync.Mutex
		hitPaths  []string
		sawCookie bool
		sawRef    bool
		mode429   atomic.Bool
	)

	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		mu.Lock()
		hitPaths = append(hitPaths, req.URL.Path)
		if req.Header.Get("Cookie") != "" {
			sawCookie = true
		}
		if req.Header.Get("Referer") == "https://weibo.com/" {
			sawRef = true
		}
		mu.Unlock()

		if mode429.Load() {
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Header:     http.Header{"Content-Type": []string{"text/plain"}},
				Body:       io.NopCloser(strings.NewReader("rate limited")),
			}, nil
		}

		switch req.URL.Path {
		case "/oslarge/001picSlot1.jpg":
			return &http.Response{
				StatusCode:    http.StatusOK,
				ContentLength: int64(len(nowm1)),
				Header:        http.Header{"Content-Type": []string{"image/jpeg"}},
				Body:          io.NopCloser(bytes.NewReader(nowm1)),
			}, nil
		case "/oslarge/002picSlot2.jpg":
			// Identical SHA-256 to primary -> deduped, no companion saved.
			return &http.Response{
				StatusCode:    http.StatusOK,
				ContentLength: int64(len(orig2)),
				Header:        http.Header{"Content-Type": []string{"image/jpeg"}},
				Body:          io.NopCloser(bytes.NewReader(orig2)),
			}, nil
		case "/oslarge/003picSlot3.jpg":
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Header:     http.Header{"Content-Type": []string{"text/plain"}},
				Body:       io.NopCloser(strings.NewReader("404")),
			}, nil
		default:
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Header:     http.Header{"Content-Type": []string{"text/plain"}},
				Body:       io.NopCloser(strings.NewReader("unexpected")),
			}, nil
		}
	})

	mgr, libStore, _, dbPath, _ := setupDouyinDownloadTestEnv(t, http.NotFoundHandler(), 2)
	cacheDir := filepath.Join(filepath.Dir(dbPath), "cache")
	mgr.SetMediaTransportWrap(func(_ http.RoundTripper) http.RoundTripper {
		return rt
	})

	itemDir := filepath.Join(cacheDir, "weibo", "100001", "posts", "5091000000000001")
	imgDir := filepath.Join(itemDir, "images")
	if err := os.MkdirAll(imgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(imgDir, "01.jpg"), orig1, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(imgDir, "02.jpg"), orig2, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(imgDir, "03.jpg"), orig3, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(imgDir, "04.gif"), gif4, 0o600); err != nil {
		t.Fatal(err)
	}
	userNote := filepath.Join(itemDir, "notes.txt")
	if err := os.WriteFile(userNote, []byte("keep-me"), 0o600); err != nil {
		t.Fatal(err)
	}

	saved, err := libStore.SaveItem(itemDir, library.ItemMetadata{
		Site:                 "weibo",
		Kind:                 "dynamic",
		ID:                   "5091000000000001",
		RawID:                "5091000000000001",
		Title:                "历史微博",
		Text:                 "历史微博",
		Status:               "complete",
		CacheStandardVersion: 2,
		Author:               library.ItemAuthor{Site: "weibo", UID: "100001", Name: "合成博主"},
		AssetIDs:             []string{"001picSlot1", "002picSlot2", "003picSlot3", "004picGif"},
		Media: []library.MediaFile{
			{Role: "image", Path: "images/01.jpg", Bytes: int64(len(orig1)), SHA256: sha256HexBytes(orig1), Width: 48, Height: 36},
			{Role: "image", Path: "images/02.jpg", Bytes: int64(len(orig2)), SHA256: sha256HexBytes(orig2), Width: 40, Height: 30},
			{Role: "image", Path: "images/03.jpg", Bytes: int64(len(orig3)), SHA256: sha256HexBytes(orig3), Width: 44, Height: 32},
			{Role: "image", Path: "images/04.gif", Bytes: int64(len(gif4)), SHA256: sha256HexBytes(gif4), Width: 16, Height: 16},
		},
		NotePages: []library.NotePageRecord{
			{Index: 1, Kind: "image", Image: "images/01.jpg", MediaID: "001picSlot1"},
			{Index: 2, Kind: "image", Image: "images/02.jpg", MediaID: "002picSlot2"},
			{Index: 3, Kind: "image", Image: "images/03.jpg", MediaID: "003picSlot3"},
			{Index: 4, Kind: "image", Image: "images/04.gif", MediaID: "004picGif", GIF: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// First test 429 abort: must NOT stamp WeiboCacheStandardVersion = 3!
	mode429.Store(true)
	_, err429 := mgr.BackfillWeiboNoWMCompanions(context.Background(), saved.ItemKey, site.WeiboMediaTransport("Mozilla/5.0 Test", false), nil, nil)
	var sErr *site.Error
	if !errors.As(err429, &sErr) || sErr.Kind != site.ErrKindRateLimited {
		t.Fatalf("expected rate_limited error on HTTP 429, got %v", err429)
	}
	after429, ok, _ := libStore.GetItem(saved.ItemKey)
	if !ok || after429.CacheStandardVersion >= library.WeiboCacheStandardVersion {
		t.Fatalf("429 must NOT stamp WeiboCacheStandardVersion=3, got %d", after429.CacheStandardVersion)
	}

	// Now run normally.
	mode429.Store(false)
	mu.Lock()
	hitPaths = nil
	mu.Unlock()

	res, err := mgr.BackfillWeiboNoWMCompanions(context.Background(), saved.ItemKey, site.WeiboMediaTransport("Mozilla/5.0 Test", false), nil, nil)
	if err != nil {
		t.Fatalf("BackfillWeiboNoWMCompanions: %v", err)
	}
	if res.AddedCompanions != 1 || !res.StampedStandard {
		t.Fatalf("unexpected backfill result: %+v", res)
	}
	if sawCookie {
		t.Fatal("CDN backfill must never send Cookie header")
	}
	if !sawRef {
		t.Fatal("CDN backfill should send Referer: https://weibo.com/")
	}
	for _, p := range hitPaths {
		if strings.Contains(p, "004picGif") {
			t.Fatalf("GIF slot must be skipped, got request %q", p)
		}
	}

	after, ok, err := libStore.GetItem(saved.ItemKey)
	if err != nil || !ok {
		t.Fatalf("GetItem after backfill: ok=%v err=%v", ok, err)
	}
	if after.CacheStandardVersion != library.WeiboCacheStandardVersion {
		t.Fatalf("CacheStandardVersion = %d, want %d", after.CacheStandardVersion, library.WeiboCacheStandardVersion)
	}
	if library.WeiboNeedsNoWMBackfill(after, true) {
		t.Fatal("item must no longer need backfill after completion")
	}
	if after.Status != "complete" || len(after.Missing) != 0 {
		t.Fatalf("item must stay green complete: status=%q missing=%+v", after.Status, after.Missing)
	}
	if after.NotePages[0].NoWatermark != "images/01_nowm.jpg" {
		t.Fatalf("slot 1 NoWatermark = %q, want images/01_nowm.jpg", after.NotePages[0].NoWatermark)
	}
	if after.NotePages[1].NoWatermark != "" || after.NotePages[2].NoWatermark != "" || after.NotePages[3].NoWatermark != "" {
		t.Fatalf("slots 2/3/4 should not have companions: %+v", after.NotePages)
	}
	gotOrig1, _ := os.ReadFile(filepath.Join(itemDir, "images", "01.jpg"))
	if !bytes.Equal(gotOrig1, orig1) {
		t.Fatal("primary image 01.jpg must NOT be modified by companion backfill")
	}
	gotNoWM1, err := os.ReadFile(filepath.Join(itemDir, "images", "01_nowm.jpg"))
	if err != nil || !bytes.Equal(gotNoWM1, nowm1) {
		t.Fatalf("companion 01_nowm.jpg mismatch: err=%v len=%d want=%d", err, len(gotNoWM1), len(nowm1))
	}
	gotNote, err := os.ReadFile(userNote)
	if err != nil || string(gotNote) != "keep-me" {
		t.Fatalf("unregistered user file was touched: err=%v content=%q", err, string(gotNote))
	}
}

func TestForceOverwriteAcrossSitesAndDegradedRefusal(t *testing.T) {
	v1Img := makeWatermarkTestJPEG(t, 40, 30, color.RGBA{R: 210, G: 40, B: 40, A: 255})
	v1NoWM := makeWatermarkTestJPEG(t, 28, 21, color.RGBA{R: 180, G: 80, B: 20, A: 255})
	v2Img := makeWatermarkTestJPEG(t, 44, 34, color.RGBA{R: 30, G: 160, B: 220, A: 255})
	v2NoWM := makeWatermarkTestJPEG(t, 30, 22, color.RGBA{R: 40, G: 200, B: 90, A: 255})
	placeholderGIF := makeWatermarkTestPlaceholderGIF(t)

	var (
		mu       sync.RWMutex
		curImg   = v1Img
		curNoWM  = v1NoWM
		imgMode  = "ok" // "ok" | "placeholder" | "404"
		nowmMode = "ok"
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/img/main.jpg", func(w http.ResponseWriter, r *http.Request) {
		mu.RLock()
		mode := imgMode
		data := curImg
		mu.RUnlock()
		switch mode {
		case "404":
			http.NotFound(w, r)
		case "placeholder":
			w.Header().Set("Content-Type", "image/gif")
			_, _ = w.Write(placeholderGIF)
		default:
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(data)
		}
	})
	mux.HandleFunc("/img/nowm.jpg", func(w http.ResponseWriter, r *http.Request) {
		mu.RLock()
		mode := nowmMode
		data := curNoWM
		mu.RUnlock()
		if mode != "ok" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(data)
	})

	mgr, libStore, srv := setupDownloadEnv(t, mux, 2)
	weiboPlanner := newFakeGenericWorkPlanner(site.Weibo)
	xPlanner := newFakeGenericWorkPlanner(site.X)
	douyinPlanner := newFakeGenericWorkPlanner(site.Douyin)
	mgr.RegisterWorkPlanner(weiboPlanner)
	mgr.RegisterWorkPlanner(xPlanner)
	mgr.RegisterWorkPlanner(douyinPlanner)
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	ctx := context.Background()

	// --- 1. Weibo post: normal recache preserves V1 primary, ForceOverwrite overwrites BOTH primary + companion, degraded refuses ---
	wbTarget := "5092000000000001"
	weiboPlanner.setPlan(wbTarget, func(_ context.Context, _ int) (WorkDownloadPlan, error) {
		return WorkDownloadPlan{
			Site:       site.Weibo,
			Kind:       "dynamic",
			TargetID:   wbTarget,
			AuthorUID:  "1700000009",
			AuthorName: "WBForce",
			Title:      "微博重下测试",
			Desc:       "微博重下测试",
			Transport:  localhostTransport(site.Weibo),
			Pages: []PlannedNotePage{
				{
					Index:            1,
					Kind:             "image",
					MediaID:          "wb_same_pic",
					Width:            40,
					Height:           30,
					ImageURLs:        []string{srv.URL + "/img/main.jpg"},
					WatermarkFreeURL: srv.URL + "/img/nowm.jpg",
				},
			},
		}, nil
	})
	tk1, err := mgr.EnqueueWeiboWork(ctx, WeiboWorkEnqueueRequest{TargetID: wbTarget})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.WaitTasks(ctx, tk1.TaskID); err != nil {
		t.Fatalf("Weibo V1 cache failed: %v", err)
	}
	wbItem, _, _ := libStore.GetItem("weibo:dynamic:" + wbTarget)
	if wbItem.CacheStandardVersion != library.WeiboCacheStandardVersion {
		t.Fatalf("Weibo dual-save CacheStandardVersion = %d, want %d", wbItem.CacheStandardVersion, library.WeiboCacheStandardVersion)
	}

	// Switch CDN payload to V2 with same MediaID.
	mu.Lock()
	curImg = v2Img
	curNoWM = v2NoWM
	mu.Unlock()

	// Normal recache (without ForceOverwrite) reuses valid local primary image!
	tkSafe, err := mgr.EnqueueWeiboWork(ctx, WeiboWorkEnqueueRequest{TargetID: wbTarget})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.WaitTasks(ctx, tkSafe.TaskID); err != nil {
		t.Fatalf("Weibo safe recache failed: %v", err)
	}
	gotSafe, _ := os.ReadFile(filepath.Join(wbItem.ItemDir, "images", "01.jpg"))
	if !bytes.Equal(gotSafe, v1Img) {
		t.Fatal("normal safe recache unexpectedly replaced valid primary image")
	}

	// Explicit manual ForceOverwrite skips reuse and atomically overwrites BOTH primary + companion!
	tkForce, err := mgr.EnqueueWeiboWork(WithForceOverwrite(ctx), WeiboWorkEnqueueRequest{TargetID: wbTarget, Source: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.WaitTasks(ctx, tkForce.TaskID); err != nil {
		t.Fatalf("Weibo ForceOverwrite failed: %v", err)
	}
	gotForcedOrig, _ := os.ReadFile(filepath.Join(wbItem.ItemDir, "images", "01.jpg"))
	gotForcedNoWM, _ := os.ReadFile(filepath.Join(wbItem.ItemDir, "images", "01_nowm.jpg"))
	if !bytes.Equal(gotForcedOrig, v2Img) {
		t.Fatal("ForceOverwrite did not overwrite Weibo primary image with fresh V2 bytes")
	}
	if !bytes.Equal(gotForcedNoWM, v2NoWM) {
		t.Fatal("ForceOverwrite did not overwrite Weibo no-watermark companion with fresh V2 bytes")
	}

	// Now source returns Sina placeholder GIF -> ForceOverwrite MUST refuse and preserve local V2!
	mu.Lock()
	imgMode = "placeholder"
	mu.Unlock()
	tkDegraded, err := mgr.EnqueueWeiboWork(WithForceOverwrite(ctx), WeiboWorkEnqueueRequest{TargetID: wbTarget, Source: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if waitErr := mgr.WaitTasks(ctx, tkDegraded.TaskID); !errors.Is(waitErr, ErrRemoteDegradedRefused) {
		t.Fatalf("expected ErrRemoteDegradedRefused on Weibo ForceOverwrite placeholder, got %v", waitErr)
	}
	stillOrig, _ := os.ReadFile(filepath.Join(wbItem.ItemDir, "images", "01.jpg"))
	stillNoWM, _ := os.ReadFile(filepath.Join(wbItem.ItemDir, "images", "01_nowm.jpg"))
	if !bytes.Equal(stillOrig, v2Img) || !bytes.Equal(stillNoWM, v2NoWM) {
		t.Fatal("Weibo local files were damaged after ForceOverwrite degraded refusal!")
	}

	// --- 2. X post ForceOverwrite & 404 degraded refusal ---
	mu.Lock()
	imgMode = "ok"
	curImg = v1Img
	mu.Unlock()
	xTarget := "1892000000000000001"
	xPlanner.setPlan(xTarget, func(_ context.Context, _ int) (WorkDownloadPlan, error) {
		return WorkDownloadPlan{
			Site:       site.X,
			Kind:       "dynamic",
			TargetID:   xTarget,
			AuthorUID:  "1900000009",
			AuthorName: "XForce",
			Title:      "X重下测试",
			Desc:       "X重下测试",
			Transport:  localhostTransport(site.X),
			Pages: []PlannedNotePage{
				{Index: 1, Kind: "image", MediaID: "x_same_pic", Width: 40, Height: 30, ImageURLs: []string{srv.URL + "/img/main.jpg"}},
			},
		}, nil
	})
	tX1, err := mgr.EnqueueXWork(ctx, XWorkEnqueueRequest{TargetID: xTarget})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.WaitTasks(ctx, tX1.TaskID); err != nil {
		t.Fatal(err)
	}
	xItem, _, _ := libStore.GetItem("x:dynamic:" + xTarget)

	mu.Lock()
	curImg = v2Img
	mu.Unlock()
	tXForce, err := mgr.EnqueueXWork(WithForceOverwrite(ctx), XWorkEnqueueRequest{TargetID: xTarget, Source: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.WaitTasks(ctx, tXForce.TaskID); err != nil {
		t.Fatalf("X ForceOverwrite failed: %v", err)
	}
	gotX, _ := os.ReadFile(filepath.Join(xItem.ItemDir, "images", "01.jpg"))
	if !bytes.Equal(gotX, v2Img) {
		t.Fatal("X ForceOverwrite did not overwrite local image")
	}
	mu.Lock()
	imgMode = "404"
	mu.Unlock()
	tX404, err := mgr.EnqueueXWork(WithForceOverwrite(ctx), XWorkEnqueueRequest{TargetID: xTarget, Source: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if waitErr := mgr.WaitTasks(ctx, tX404.TaskID); !errors.Is(waitErr, ErrRemoteDegradedRefused) {
		t.Fatalf("expected ErrRemoteDegradedRefused on X ForceOverwrite 404, got %v", waitErr)
	}
	stillX, _ := os.ReadFile(filepath.Join(xItem.ItemDir, "images", "01.jpg"))
	if !bytes.Equal(stillX, v2Img) {
		t.Fatal("X local file was damaged on ForceOverwrite 404 refusal")
	}

	// --- 3. Douyin note ForceOverwrite & 404 degraded refusal ---
	mu.Lock()
	imgMode = "ok"
	curImg = v1Img
	mu.Unlock()
	dyTarget := "7392000000000000001"
	douyinPlanner.setPlan(dyTarget, func(_ context.Context, _ int) (WorkDownloadPlan, error) {
		return WorkDownloadPlan{
			Site:       site.Douyin,
			Kind:       "note",
			TargetID:   dyTarget,
			AuthorUID:  "MS4wLjABAAAA_force",
			AuthorName: "DYForce",
			Title:      "抖音图文重下",
			Desc:       "抖音图文重下",
			Transport:  localhostTransport(site.Douyin),
			Pages: []PlannedNotePage{
				{Index: 1, Kind: "image", MediaID: "dy_same_pic", Width: 40, Height: 30, ImageURLs: []string{srv.URL + "/img/main.jpg"}},
			},
		}, nil
	})
	tDY1, err := mgr.EnqueueDouyinWork(ctx, DouyinWorkEnqueueRequest{AwemeID: dyTarget})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.WaitTasks(ctx, tDY1.TaskID); err != nil {
		t.Fatal(err)
	}
	dyItem, _, _ := libStore.GetItem("douyin:dynamic:" + dyTarget)

	mu.Lock()
	curImg = v2Img
	mu.Unlock()
	tDYForce, err := mgr.EnqueueDouyinWork(WithForceOverwrite(ctx), DouyinWorkEnqueueRequest{AwemeID: dyTarget, Source: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.WaitTasks(ctx, tDYForce.TaskID); err != nil {
		t.Fatalf("Douyin ForceOverwrite failed: %v", err)
	}
	gotDY, _ := os.ReadFile(filepath.Join(dyItem.ItemDir, "images", "01.jpg"))
	if !bytes.Equal(gotDY, v2Img) {
		t.Fatal("Douyin ForceOverwrite did not overwrite local image")
	}
	mu.Lock()
	imgMode = "404"
	mu.Unlock()
	tDY404, err := mgr.EnqueueDouyinWork(WithForceOverwrite(ctx), DouyinWorkEnqueueRequest{AwemeID: dyTarget, Source: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if waitErr := mgr.WaitTasks(ctx, tDY404.TaskID); !errors.Is(waitErr, ErrRemoteDegradedRefused) {
		t.Fatalf("expected ErrRemoteDegradedRefused on Douyin ForceOverwrite 404, got %v", waitErr)
	}
	stillDY, _ := os.ReadFile(filepath.Join(dyItem.ItemDir, "images", "01.jpg"))
	if !bytes.Equal(stillDY, v2Img) {
		t.Fatal("Douyin local file was damaged on ForceOverwrite 404 refusal")
	}

	// Verify no .trace-stage-* or .trace-prev-* leftovers exist in any item dir.
	for _, dir := range []string{wbItem.ItemDir, xItem.ItemDir, dyItem.ItemDir} {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".trace-") {
				t.Fatalf("leftover temp dir %q in %s", e.Name(), dir)
			}
		}
	}
}

func TestForceOverwriteBilibiliVideoAndDegradedRefusal(t *testing.T) {
	v1Bytes, err := BuildSyntheticVideoM4S(60, 30)
	if err != nil {
		t.Fatal(err)
	}
	v2Bytes, err := BuildSyntheticVideoM4S(90, 30)
	if err != nil {
		t.Fatal(err)
	}
	a1Bytes, err := BuildSyntheticAudioM4S(94)
	if err != nil {
		t.Fatal(err)
	}
	a2Bytes, err := BuildSyntheticAudioM4S(141)
	if err != nil {
		t.Fatal(err)
	}
	coverV1 := makeWatermarkTestJPEG(t, 40, 30, color.RGBA{R: 200, G: 60, B: 60, A: 255})
	coverV2 := makeWatermarkTestJPEG(t, 44, 34, color.RGBA{R: 40, G: 160, B: 220, A: 255})

	var (
		mu       sync.RWMutex
		phase    = 1 // 1=V1, 2=V2 same cid fresh bytes, 3=degraded trial/empty
		srvURL   string
		bvidTest = "BV17x411w7FR"
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/x/web-interface/wbi/view", func(w http.ResponseWriter, r *http.Request) {
		mu.RLock()
		ph := phase
		mu.RUnlock()
		dur := 2
		if ph == 2 {
			dur = 3
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"bvid":     bvidTest,
				"aid":      170099,
				"title":    "B站强制重下视频",
				"desc":     "Desc",
				"pic":      srvURL + "/media/cover.jpg",
				"pubdate":  1700000000,
				"duration": dur,
				"owner":    map[string]any{"mid": 889, "name": "Creator889"},
				"pages": []map[string]any{
					{"cid": 1009, "page": 1, "part": "P1", "duration": dur, "dimension": map[string]any{"width": 16, "height": 16}},
				},
			},
		})
	})
	mux.HandleFunc("/x/player/wbi/playurl", func(w http.ResponseWriter, r *http.Request) {
		mu.RLock()
		ph := phase
		mu.RUnlock()
		if ph == 3 {
			// Return trial-only / degraded lower quality stream (360p) -> must be refused!
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"quality":        16,
					"accept_format":  "dash",
					"accept_quality": []int{16},
					"is_preview":     true,
					"dash": map[string]any{
						"duration": 1,
						"video": []map[string]any{
							{"id": 16, "baseUrl": srvURL + "/media/v1.m4s", "codecid": 7, "codecs": "avc1.42c00a", "width": 16, "height": 16, "frameRate": "30.0", "bandwidth": 100000},
						},
						"audio": []map[string]any{
							{"id": 30216, "baseUrl": srvURL + "/media/a1.m4s", "codecs": "mp4a.40.2", "bandwidth": 64000},
						},
					},
				},
			})
			return
		}
		vPath, aPath, dur := "/media/v1.m4s", "/media/a1.m4s", 2
		if ph == 2 {
			vPath, aPath, dur = "/media/v2.m4s", "/media/a2.m4s", 3
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"quality":        80,
				"accept_format":  "dash",
				"accept_quality": []int{80},
				"dash": map[string]any{
					"duration": dur,
					"video": []map[string]any{
						{"id": 80, "baseUrl": srvURL + vPath, "codecid": 7, "codecs": "avc1.42c00a", "width": 16, "height": 16, "frameRate": "30.0", "bandwidth": 500000},
					},
					"audio": []map[string]any{
						{"id": 30280, "baseUrl": srvURL + aPath, "codecs": "mp4a.40.2", "bandwidth": 192000},
					},
				},
			},
		})
	})
	mux.HandleFunc("/x/v2/dm/web/seg.so", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bilibili.EncodeDmSegMobileReply([]bilibili.DanmakuElem{
			{ID: 1, IDStr: "1", Progress: 300, Mode: 1, Fontsize: 25, Color: 0xFFFFFF, Content: "dm"},
		}))
	})
	mux.HandleFunc("/x/player/wbi/v2", func(w http.ResponseWriter, r *http.Request) {
		cidInt, _ := strconv.ParseInt(r.URL.Query().Get("cid"), 10, 64)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"bvid": bvidTest, "cid": cidInt, "subtitle": map[string]any{"subtitles": []any{}}},
		})
	})
	mux.HandleFunc("/media/cover.jpg", func(w http.ResponseWriter, r *http.Request) {
		mu.RLock()
		ph := phase
		mu.RUnlock()
		w.Header().Set("Content-Type", "image/jpeg")
		if ph == 2 {
			_, _ = w.Write(coverV2)
		} else {
			_, _ = w.Write(coverV1)
		}
	})
	mux.HandleFunc("/media/v1.m4s", func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "v1.m4s", time.Time{}, bytes.NewReader(v1Bytes))
	})
	mux.HandleFunc("/media/v2.m4s", func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "v2.m4s", time.Time{}, bytes.NewReader(v2Bytes))
	})
	mux.HandleFunc("/media/a1.m4s", func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "a1.m4s", time.Time{}, bytes.NewReader(a1Bytes))
	})
	mux.HandleFunc("/media/a2.m4s", func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "a2.m4s", time.Time{}, bytes.NewReader(a2Bytes))
	})

	mgr, libStore, srv := setupDownloadEnv(t, mux, 2)
	srvURL = srv.URL
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	ctx := context.Background()
	tk1, err := mgr.EnqueueVideo(ctx, VideoEnqueueRequest{BVID: bvidTest, Source: "manual"})
	if err != nil || len(tk1) == 0 {
		t.Fatalf("EnqueueVideo V1: err=%v len=%d", err, len(tk1))
	}
	if err := mgr.WaitTasks(ctx, tk1[0].TaskID); err != nil {
		t.Fatalf("Bilibili V1 failed: %v", err)
	}
	biliItem, ok, err := libStore.GetItem("bilibili:video:" + bvidTest)
	if err != nil || !ok {
		t.Fatalf("GetItem V1: ok=%v err=%v", ok, err)
	}
	p1Before, err := os.ReadFile(filepath.Join(biliItem.ItemDir, "P01.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	covBefore, _ := os.ReadFile(filepath.Join(biliItem.ItemDir, "cover.jpg"))
	if !bytes.Equal(covBefore, coverV1) {
		t.Fatal("expected V1 cover")
	}

	// Switch to V2 (same CID, fresh stream & cover bytes) and run manual ForceOverwrite!
	mu.Lock()
	phase = 2
	mu.Unlock()
	tkForce, err := mgr.EnqueueVideo(WithForceOverwrite(ctx), VideoEnqueueRequest{BVID: bvidTest, Source: "manual"})
	if err != nil || len(tkForce) == 0 {
		t.Fatalf("EnqueueVideo ForceOverwrite: err=%v len=%d", err, len(tkForce))
	}
	if err := mgr.WaitTasks(ctx, tkForce[0].TaskID); err != nil {
		t.Fatalf("Bilibili ForceOverwrite failed: %v", err)
	}
	p1After, err := os.ReadFile(filepath.Join(biliItem.ItemDir, "P01.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	covAfter, _ := os.ReadFile(filepath.Join(biliItem.ItemDir, "cover.jpg"))
	if bytes.Equal(p1After, p1Before) {
		t.Fatal("Bilibili ForceOverwrite did not overwrite P01.mp4 with fresh V2 stream")
	}
	if !bytes.Equal(covAfter, coverV2) {
		t.Fatal("Bilibili ForceOverwrite did not overwrite cover.jpg with fresh V2 cover")
	}

	// Switch to degraded preview-only stream -> ForceOverwrite MUST refuse & preserve V2!
	mu.Lock()
	phase = 3
	mu.Unlock()
	tkDeg, err := mgr.EnqueueVideo(WithForceOverwrite(ctx), VideoEnqueueRequest{BVID: bvidTest, Source: "manual"})
	if err != nil || len(tkDeg) == 0 {
		t.Fatalf("EnqueueVideo Degraded: err=%v len=%d", err, len(tkDeg))
	}
	if waitErr := mgr.WaitTasks(ctx, tkDeg[0].TaskID); !errors.Is(waitErr, ErrRemoteDegradedRefused) {
		t.Fatalf("expected ErrRemoteDegradedRefused on degraded Bilibili preview stream, got %v", waitErr)
	}
	p1Still, _ := os.ReadFile(filepath.Join(biliItem.ItemDir, "P01.mp4"))
	covStill, _ := os.ReadFile(filepath.Join(biliItem.ItemDir, "cover.jpg"))
	if !bytes.Equal(p1Still, p1After) || !bytes.Equal(covStill, coverV2) {
		t.Fatal("Bilibili V2 local files were damaged after ForceOverwrite degraded refusal!")
	}
}
