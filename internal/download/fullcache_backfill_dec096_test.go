package download

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/settings"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
)

func buildTestJPEGBytes(t *testing.T, r, g, b uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	c := color.RGBA{R: r, G: g, B: b, A: 255}
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func buildTestGIFBytes(t *testing.T) []byte {
	t.Helper()
	pal := color.Palette{color.RGBA{R: 20, G: 120, B: 220, A: 255}, color.RGBA{R: 240, G: 240, B: 240, A: 255}}
	f1 := image.NewPaletted(image.Rect(0, 0, 12, 12), pal)
	f2 := image.NewPaletted(image.Rect(0, 0, 12, 12), pal)
	f2.SetColorIndex(0, 0, 1)
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, &gif.GIF{
		Image: []*image.Paletted{f1, f2},
		Delay: []int{10, 10},
	}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func buildTestMuxedMP4Bytes(t *testing.T) []byte {
	t.Helper()
	vBytes, err := BuildSyntheticVideoM4S(60, 30)
	if err != nil {
		t.Fatal(err)
	}
	aBytes, err := BuildSyntheticAudioM4S(94)
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	vp := filepath.Join(tmp, "v.m4s")
	ap := filepath.Join(tmp, "a.m4s")
	out := filepath.Join(tmp, "out.mp4")
	if err := os.WriteFile(vp, vBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ap, aBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := MuxDASHFilesContext(context.Background(), vp, ap, out); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestDEC096BilibiliChaptersWhenSubtitlesDisabledAndIncludeAIToggle(t *testing.T) {
	vBytes, err := BuildSyntheticVideoM4S(60, 30)
	if err != nil {
		t.Fatal(err)
	}
	aBytes, err := BuildSyntheticAudioM4S(94)
	if err != nil {
		t.Fatal(err)
	}

	var (
		srvURL            string
		viewCalls         atomic.Int32
		playerV2Calls     atomic.Int32
		subtitleBodyCalls atomic.Int32
	)
	mux := http.NewServeMux()
	mux.HandleFunc("/x/web-interface/wbi/view", func(w http.ResponseWriter, r *http.Request) {
		viewCalls.Add(1)
		bvid := r.URL.Query().Get("bvid")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"bvid":     bvid,
				"aid":      77001,
				"title":    "分段跳转测试视频",
				"desc":     "简介",
				"pubdate":  1700000000,
				"duration": 4,
				"owner":    map[string]any{"mid": 5001, "name": "UP"},
				"pages": []map[string]any{
					{"cid": 77101, "page": 1, "part": "P1", "duration": 2},
					{"cid": 77102, "page": 2, "part": "P2", "duration": 2},
				},
			},
		})
	})
	mux.HandleFunc("/x/player/wbi/playurl", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"quality":        80,
				"accept_format":  "hdflv2,flv,mp4",
				"accept_quality": []int{80},
				"dash": map[string]any{
					"duration": 2,
					"video": []map[string]any{
						{"id": 80, "baseUrl": srvURL + "/m4s/v.m4s", "bandwidth": 900000, "mimeType": "video/mp4", "codecs": "avc1.64001F", "width": 1920, "height": 1080, "frameRate": "30", "codecid": 7},
					},
					"audio": []map[string]any{
						{"id": 30280, "baseUrl": srvURL + "/m4s/a.m4s", "bandwidth": 192000, "mimeType": "audio/mp4", "codecs": "mp4a.40.2"},
					},
				},
			},
		})
	})
	mux.HandleFunc("/x/player/wbi/v2", func(w http.ResponseWriter, r *http.Request) {
		playerV2Calls.Add(1)
		bvid := r.URL.Query().Get("bvid")
		cidVal, _ := strconv.ParseInt(r.URL.Query().Get("cid"), 10, 64)
		chapterTitle := "P1 开场章节"
		if cidVal == 77102 {
			chapterTitle = "P2 进阶章节"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"bvid": bvid,
				"cid":  cidVal,
				"subtitle": map[string]any{
					"subtitles": []map[string]any{
						{"lan": "ai-zh", "lan_doc": "中文（自动生成）", "subtitle_url": srvURL + "/sub/ai-zh.json"},
					},
				},
				"view_points": []map[string]any{
					{"type": 2, "from": 0, "to": 60, "content": chapterTitle},
				},
			},
		})
	})
	mux.HandleFunc("/sub/ai-zh.json", func(w http.ResponseWriter, r *http.Request) {
		subtitleBodyCalls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"body": []map[string]any{{"from": 0.0, "to": 1.5, "content": "合成AI字幕"}},
		})
	})
	mux.HandleFunc("/m4s/v.m4s", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(vBytes)))
		_, _ = w.Write(vBytes)
	})
	mux.HandleFunc("/m4s/a.m4s", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(aBytes)))
		_, _ = w.Write(aBytes)
	})

	mgr, libStore, srv := setupDownloadEnv(t, mux, 2)
	srvURL = srv.URL
	cfg := settings.Default("").Download
	cfg.SaveCover = false
	cfg.SaveDanmaku = false
	cfg.SaveSubtitles = false
	cfg.IncludeAI = false
	mgr.UpdateSettings(cfg)

	ctx := context.Background()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	// 1. Fresh 2P video with SaveSubtitles=false MUST still fetch chapters on both parts with 0 subtitle body downloads.
	tasks, err := mgr.EnqueueVideo(ctx, VideoEnqueueRequest{BVID: "BV1chap000001", Source: "manual"})
	if err != nil {
		t.Fatalf("EnqueueVideo: %v", err)
	}
	var ids []string
	for _, tk := range tasks {
		ids = append(ids, tk.TaskID)
	}
	if err := mgr.WaitTasks(ctx, ids...); err != nil {
		t.Fatalf("WaitTasks: %v", err)
	}
	if subtitleBodyCalls.Load() != 0 {
		t.Fatalf("expected 0 subtitle body downloads when SaveSubtitles=false, got %d", subtitleBodyCalls.Load())
	}
	meta, ok, err := libStore.GetItem("bilibili:video:BV1chap000001")
	if err != nil || !ok {
		t.Fatalf("GetItem: ok=%v err=%v", ok, err)
	}
	if meta.CacheStandardVersion != library.BilibiliVideoStandardVersion {
		t.Fatalf("CacheStandardVersion = %d, want %d", meta.CacheStandardVersion, library.BilibiliVideoStandardVersion)
	}
	if meta.RemotePartCount != 2 {
		t.Fatalf("RemotePartCount = %d, want 2", meta.RemotePartCount)
	}
	if len(meta.Parts) != 2 {
		t.Fatalf("expected 2 parts, got %+v", meta.Parts)
	}
	chByPage := map[int]string{}
	for _, p := range meta.Parts {
		if len(p.Chapters) != 1 {
			t.Fatalf("expected 1 chapter on P%d even with SaveSubtitles=false, got %+v", p.Page, p.Chapters)
		}
		chByPage[p.Page] = p.Chapters[0].Title
	}
	if chByPage[1] != "P1 开场章节" || chByPage[2] != "P2 进阶章节" {
		t.Fatalf("unexpected chapter titles by page: %+v", chByPage)
	}

	// 2. CacheSupplementWithDetail on an older v2 2P item reuses pre-fetched VideoDetail (0 extra view calls)
	// and backfills chapters on P1..PN even with SaveSubtitles=false.
	meta.CacheStandardVersion = 2
	meta.RemotePartCount = 0
	meta.Parts[0].Chapters = nil
	meta.Parts[0].ChaptersChecked = false
	meta.Parts[1].Chapters = nil
	meta.Parts[1].ChaptersChecked = false
	if _, err := libStore.SaveItem(meta.ItemDir, meta); err != nil {
		t.Fatal(err)
	}
	viewBefore := viewCalls.Load()
	v2Before := playerV2Calls.Load()
	preDetail := &bilibili.VideoDetail{
		BVID: "BV1chap000001",
		Pages: []bilibili.VideoPage{
			{CID: 77101, Page: 1, Title: "P1", Duration: 2},
			{CID: 77102, Page: 2, Title: "P2", Duration: 2},
		},
	}
	supTask, err := mgr.CacheSupplementWithDetail(ctx, "BV1chap000001", "queue", preDetail)
	if err != nil {
		t.Fatalf("CacheSupplementWithDetail: %v", err)
	}
	if err := mgr.WaitTasks(ctx, supTask.TaskID); err != nil {
		t.Fatalf("WaitTasks supplement: %v", err)
	}
	if viewCalls.Load() != viewBefore {
		t.Fatalf("expected 0 extra VideoDetail calls when preDetail passed, got +%d", viewCalls.Load()-viewBefore)
	}
	if playerV2Calls.Load()-v2Before != 2 {
		t.Fatalf("expected 2 player/wbi/v2 calls (P1+P2), got %d", playerV2Calls.Load()-v2Before)
	}
	if subtitleBodyCalls.Load() != 0 {
		t.Fatalf("expected 0 subtitle body downloads during chapters-only supplement, got %d", subtitleBodyCalls.Load())
	}
	backfilled, _, err := libStore.GetItem("bilibili:video:BV1chap000001")
	if err != nil {
		t.Fatal(err)
	}
	if backfilled.CacheStandardVersion != library.BilibiliVideoStandardVersion || backfilled.RemotePartCount != 2 ||
		len(backfilled.Parts[0].Chapters) != 1 || len(backfilled.Parts[1].Chapters) != 1 {
		t.Fatalf("expected v3 + chapters on both parts after supplement, got %+v", backfilled)
	}

	// 3. Turning on SaveSubtitles + IncludeAI fetches AI tracks and stamps SubtitleIncludesAI=true on all parts.
	cfg.SaveSubtitles = true
	cfg.IncludeAI = false
	mgr.UpdateSettings(cfg)
	supNoAI, err := mgr.CacheSupplementWithDetail(ctx, "BV1chap000001", "queue", preDetail)
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.WaitTasks(ctx, supNoAI.TaskID); err != nil {
		t.Fatal(err)
	}
	afterNoAI, _, _ := libStore.GetItem("bilibili:video:BV1chap000001")
	if afterNoAI.Parts[0].SubtitleIncludesAI || afterNoAI.Parts[1].SubtitleIncludesAI {
		t.Fatalf("expected SubtitleIncludesAI=false when IncludeAI=false, got %+v", afterNoAI.Parts)
	}

	cfg.IncludeAI = true
	mgr.UpdateSettings(cfg)
	supAI, err := mgr.CacheSupplementWithDetail(ctx, "BV1chap000001", "queue", preDetail)
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.WaitTasks(ctx, supAI.TaskID); err != nil {
		t.Fatal(err)
	}
	afterAI, _, _ := libStore.GetItem("bilibili:video:BV1chap000001")
	if !afterAI.Parts[0].SubtitleIncludesAI || !afterAI.Parts[1].SubtitleIncludesAI || subtitleBodyCalls.Load() != 2 {
		t.Fatalf("expected SubtitleIncludesAI=true on P1+P2 and 2 AI subtitle fetches, got P1=%v P2=%v subCalls=%d",
			afterAI.Parts[0].SubtitleIncludesAI, afterAI.Parts[1].SubtitleIncludesAI, subtitleBodyCalls.Load())
	}
}

func TestDEC096DouyinOriginalUnavailableStopsLoopAndTransientPreservesRetry(t *testing.T) {
	mp4Bytes := buildTestMuxedMP4Bytes(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/orig-403.mp4", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	mux.HandleFunc("/orig-503.mp4", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	mux.HandleFunc("/fallback.mp4", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		http.ServeContent(w, r, "fallback.mp4", time.Time{}, bytes.NewReader(mp4Bytes))
	})

	mgr, libStore, srv := setupDownloadEnv(t, mux, 2)
	cfg := settings.Default("").Download
	cfg.SaveCover = false
	mgr.UpdateSettings(cfg)

	planner := newFakeGenericWorkPlanner(site.Douyin)
	mgr.RegisterWorkPlanner(planner)

	ctx := context.Background()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	// 1. Permanent 403 on OriginalURLs -> records OriginalState="unavailable" and planUpgradesSingleVideo stops looping!
	plan403 := WorkDownloadPlan{
		Site:         site.Douyin,
		Kind:         "video",
		TargetID:     "7500000000000403",
		Title:        "原画403视频",
		AuthorUID:    "MS4wLjAB_author_403",
		AuthorName:   "合成抖音作者",
		DurationSec:  2,
		OriginalURLs: []string{srv.URL + "/orig-403.mp4"},
		VideoURLs:    []string{srv.URL + "/fallback.mp4"},
		VideoGear:    "normal_1080_0",
		QualityName:  "1080P",
		Width:        320,
		Height:       240,
		Bitrate:      2000000,
		MediaID:      "v_orig_403",
		Transport:    localhostTransport(site.Douyin),
	}
	tk403, err := mgr.EnqueueDouyinWork(ctx, DouyinWorkEnqueueRequest{AwemeID: plan403.TargetID, Plan: &plan403, Source: "manual"})
	if err != nil {
		t.Fatalf("EnqueueDouyinWork 403: %v", err)
	}
	if err := mgr.WaitTasks(ctx, tk403.TaskID); err != nil {
		t.Fatalf("WaitTasks 403: %v", err)
	}
	got403, ok, err := libStore.GetItem("douyin:video:7500000000000403")
	if err != nil || !ok || got403.DouyinVideo == nil {
		t.Fatalf("GetItem 403: ok=%v err=%v meta=%+v", ok, err, got403)
	}
	if got403.DouyinVideo.OriginalState != "unavailable" {
		t.Fatalf("expected OriginalState=unavailable after HTTP 403, got %q", got403.DouyinVideo.OriginalState)
	}
	if got403.CacheStandardVersion != library.DouyinCacheStandardVersion {
		t.Fatalf("expected DouyinCacheStandardVersion=%d, got %d", library.DouyinCacheStandardVersion, got403.CacheStandardVersion)
	}
	if planUpgradesSingleVideo(got403, plan403) {
		t.Fatal("planUpgradesSingleVideo must return false once OriginalState=unavailable")
	}

	// 2. Transient 503 on OriginalURLs -> does NOT mark OriginalState="unavailable", so future retries remain eligible!
	plan503 := plan403
	plan503.TargetID = "7500000000000503"
	plan503.Title = "原画503瞬态视频"
	plan503.OriginalURLs = []string{srv.URL + "/orig-503.mp4"}
	plan503.MediaID = "v_orig_503"
	tk503, err := mgr.EnqueueDouyinWork(ctx, DouyinWorkEnqueueRequest{AwemeID: plan503.TargetID, Plan: &plan503, Source: "manual"})
	if err != nil {
		t.Fatalf("EnqueueDouyinWork 503: %v", err)
	}
	if err := mgr.WaitTasks(ctx, tk503.TaskID); err != nil {
		t.Fatalf("WaitTasks 503: %v", err)
	}
	got503, ok, err := libStore.GetItem("douyin:video:7500000000000503")
	if err != nil || !ok || got503.DouyinVideo == nil {
		t.Fatalf("GetItem 503: ok=%v err=%v", ok, err)
	}
	if got503.DouyinVideo.OriginalState != "" {
		t.Fatalf("transient 503 must NOT set OriginalState=unavailable, got %q", got503.DouyinVideo.OriginalState)
	}
	if !planUpgradesSingleVideo(got503, plan503) {
		t.Fatal("planUpgradesSingleVideo should remain true after transient 503 fallback")
	}

	// 3. notePageVideoNeedsUpgrade detects same-res higher bitrate and higher FPS.
	localPageVid := &library.DouyinVideoInfo{Width: 1080, Height: 1920, Bitrate: 1500000, FPS: 30}
	if !notePageVideoNeedsUpgrade(localPageVid, PlannedNotePage{Width: 1080, Height: 1920, Bitrate: 3000000, FPS: 30}) {
		t.Fatal("expected same-res higher bitrate mixed video page to trigger upgrade")
	}
	if !notePageVideoNeedsUpgrade(localPageVid, PlannedNotePage{Width: 1080, Height: 1920, Bitrate: 1500000, FPS: 60}) {
		t.Fatal("expected same-res higher FPS mixed video page to trigger upgrade")
	}
	if notePageVideoNeedsUpgrade(localPageVid, PlannedNotePage{Width: 1080, Height: 1920, Bitrate: 1500000, FPS: 30}) {
		t.Fatal("identical mixed video page must not trigger upgrade")
	}
}

func TestDEC096WeiboV4StampingAcrossPostTypesAndEditHistoryGuard(t *testing.T) {
	jpgPrimary := buildTestJPEGBytes(t, 120, 40, 40)
	jpgNoWM := buildTestJPEGBytes(t, 40, 120, 40)
	gifBytes := buildTestGIFBytes(t)

	mux := http.NewServeMux()
	mux.HandleFunc("/large/001gif.gif", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/gif")
		_, _ = w.Write(gifBytes)
	})
	mux.HandleFunc("/large/001static.jpg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(jpgPrimary)
	})
	mux.HandleFunc("/oslarge/001static.jpg", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(418)
	})
	mux.HandleFunc("/oslarge/001edited.jpg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(jpgNoWM)
	})

	mgr, libStore, srv := setupDownloadEnv(t, mux, 2)
	cfg := settings.Default("").Download
	mgr.UpdateSettings(cfg)
	mgr.RegisterWorkPlanner(newFakeGenericWorkPlanner(site.Weibo))

	ctx := context.Background()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	// 1. Pure text Weibo post -> stamps WeiboCacheStandardVersion = 4.
	textPlan := WorkDownloadPlan{
		Site:       site.Weibo,
		ItemKey:    "weibo:dynamic:5096000000000001",
		Kind:       "dynamic",
		TargetID:   "5096000000000001",
		Title:      "纯文字微博",
		Desc:       "这是一条纯文字测试微博",
		AuthorUID:  "100001",
		AuthorName: "合成博主",
		Transport:  localhostTransport(site.Weibo),
	}
	tkText, err := mgr.EnqueueWeiboWork(ctx, WeiboWorkEnqueueRequest{TargetID: textPlan.TargetID, Plan: &textPlan, Source: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.WaitTasks(ctx, tkText.TaskID); err != nil {
		t.Fatal(err)
	}
	gotText, _, _ := libStore.GetItem(textPlan.ItemKey)
	if gotText.CacheStandardVersion != library.WeiboCacheStandardVersion {
		t.Fatalf("pure text Weibo CacheStandardVersion = %d, want %d", gotText.CacheStandardVersion, library.WeiboCacheStandardVersion)
	}

	// 2. Pure GIF Weibo post -> stamps WeiboCacheStandardVersion = 4.
	gifPlan := WorkDownloadPlan{
		Site:       site.Weibo,
		ItemKey:    "weibo:dynamic:5096000000000002",
		Kind:       "dynamic",
		TargetID:   "5096000000000002",
		Title:      "纯GIF微博",
		Desc:       "这是一条纯GIF测试微博",
		AuthorUID:  "100001",
		AuthorName: "合成博主",
		Pages: []PlannedNotePage{
			{Index: 1, Kind: "gif", MediaID: "001gif", ImageURLs: []string{srv.URL + "/large/001gif.gif"}},
		},
		Transport: localhostTransport(site.Weibo),
	}
	tkGIF, err := mgr.EnqueueWeiboWork(ctx, WeiboWorkEnqueueRequest{TargetID: gifPlan.TargetID, Plan: &gifPlan, Source: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.WaitTasks(ctx, tkGIF.TaskID); err != nil {
		t.Fatal(err)
	}
	gotGIF, _, _ := libStore.GetItem(gifPlan.ItemKey)
	if gotGIF.CacheStandardVersion != library.WeiboCacheStandardVersion {
		t.Fatalf("pure GIF Weibo CacheStandardVersion = %d, want %d", gotGIF.CacheStandardVersion, library.WeiboCacheStandardVersion)
	}

	// 3. Static image post whose /oslarge/ returned 418 -> completes primary image safely WITHOUT falsely stamping v4.
	staticPlan := WorkDownloadPlan{
		Site:       site.Weibo,
		ItemKey:    "weibo:dynamic:5096000000000003",
		Kind:       "dynamic",
		TargetID:   "5096000000000003",
		Title:      "静态图418降级测试",
		Desc:       "静态图测试",
		AuthorUID:  "100001",
		AuthorName: "合成博主",
		Pages: []PlannedNotePage{
			{
				Index:            1,
				Kind:             "image",
				MediaID:          "001static",
				ImageURLs:        []string{srv.URL + "/large/001static.jpg"},
				WatermarkFreeURL: srv.URL + "/oslarge/001static.jpg",
			},
		},
		Transport: localhostTransport(site.Weibo),
	}
	tkStatic, err := mgr.EnqueueWeiboWork(ctx, WeiboWorkEnqueueRequest{TargetID: staticPlan.TargetID, Plan: &staticPlan, Source: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.WaitTasks(ctx, tkStatic.TaskID); err != nil {
		t.Fatal(err)
	}
	gotStatic, _, _ := libStore.GetItem(staticPlan.ItemKey)
	if gotStatic.Status != "complete" {
		t.Fatalf("expected primary image post complete despite /oslarge/ 418, got %q", gotStatic.Status)
	}
	if gotStatic.CacheStandardVersion >= library.WeiboCacheStandardVersion {
		t.Fatalf("deferred /oslarge/ 418 must NOT stamp WeiboCacheStandardVersion=%d, got %d", library.WeiboCacheStandardVersion, gotStatic.CacheStandardVersion)
	}

	// 4. BackfillWeiboNoWMCompanions on an edited post (EditCount>0, len(EditHistory)==0) stamps v3 (WeiboNoWMStandardVersion),
	// NOT v4, so EditHistory backfill remains eligible.
	editDir := filepath.Join(libStore.CacheDir(), "weibo", "100001", "posts", "5096000000000004")
	if err := os.MkdirAll(filepath.Join(editDir, "images"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(editDir, "images", "01.jpg"), jpgPrimary, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(jpgPrimary)
	_, err = libStore.SaveItem(editDir, library.ItemMetadata{
		Version:              1,
		ID:                   "dynamic:5096000000000004",
		Site:                 "weibo",
		Kind:                 "dynamic",
		Status:               "complete",
		Title:                "已编辑微博",
		Text:                 "已编辑微博正文",
		EditCount:            2,
		CacheStandardVersion: 2,
		Author:               library.ItemAuthor{Site: "weibo", UID: "100001", Name: "合成博主"},
		AssetIDs:             []string{"001edited"},
		Media: []library.MediaFile{
			{Role: "image", Path: "images/01.jpg", Bytes: int64(len(jpgPrimary)), SHA256: hex.EncodeToString(sum[:])},
		},
		NotePages: []library.NotePageRecord{
			{Index: 1, Kind: "image", Image: "images/01.jpg", MediaID: "001edited"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	bfRes, err := mgr.BackfillWeiboNoWMCompanions(ctx, "weibo:dynamic:5096000000000004", localhostTransport(site.Weibo), []WeiboBackfillSlotOverride{
		{DisplayIndex: 1, PicID: "001edited", WatermarkFreeURL: srv.URL + "/oslarge/001edited.jpg"},
	}, nil)
	if err != nil {
		t.Fatalf("BackfillWeiboNoWMCompanions: %v", err)
	}
	if bfRes.AddedCompanions != 1 || bfRes.Item.CacheStandardVersion != library.WeiboNoWMStandardVersion {
		t.Fatalf("expected AddedCompanions=1 and CacheStandardVersion=%d (preserving v4 EditHistory eligibility), got %+v",
			library.WeiboNoWMStandardVersion, bfRes)
	}
	if !library.WeiboNeedsEditHistoryBackfill(bfRes.Item) {
		t.Fatal("expected edited post stamped at v3 to still satisfy WeiboNeedsEditHistoryBackfill")
	}
}

func TestDEC096XArticleUpgradeAndThreadAppendClassification(t *testing.T) {
	tmp := t.TempDir()
	// Helper to create a valid local item directory with an item.json and optional verified files.
	makeLocal := func(name string, meta library.ItemMetadata, files map[string][]byte) library.ItemMetadata {
		dir := filepath.Join(tmp, name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		for rel, data := range files {
			full := filepath.Join(dir, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, data, 0o600); err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(data)
			role := "image"
			if strings.HasSuffix(rel, ".mp4") {
				role = "video"
			}
			meta.Media = append(meta.Media, library.MediaFile{
				Role: role, Path: rel, Bytes: int64(len(data)), SHA256: hex.EncodeToString(sum[:]),
			})
		}
		meta.ItemDir = dir
		if err := os.WriteFile(filepath.Join(dir, "item.json"), []byte(`{"version":1}`), 0o600); err != nil {
			t.Fatal(err)
		}
		return meta
	}

	// 1. DEC-093 CardKindArticle -> ItemArticle non-destructive upgrade must NOT trip conflict!
	localArticleStub := makeLocal("x_article", library.ItemMetadata{
		Version:  1,
		Site:     "x",
		Kind:     "dynamic",
		ID:       "dynamic:1900000000000000001",
		RawID:    "1900000000000000001",
		Status:   "complete",
		Text:     "https://t.co/synthArticle",
		Card:     &library.ItemRichCard{Kind: "article", Title: "旧文章卡片"},
		AssetIDs: nil,
	}, nil)
	articlePlan := WorkDownloadPlan{
		Site:     site.X,
		Kind:     "dynamic",
		TargetID: "1900000000000000001",
		Desc:     "完整文章正文第一段\n完整文章正文第二段",
		Article: &PlannedArticle{
			Title:     "长文章完整标题",
			PlainText: "完整文章正文第一段\n完整文章正文第二段",
			Blocks:    []PlannedArticleBlock{{Type: "unstyled", Text: "完整文章正文第一段"}},
		},
		CoverURLs: []string{"http://127.0.0.1/article-cover.jpg"},
	}
	if _, conflict, degErr := classifyWorkPlanVsLocal(localArticleStub, articlePlan, "x:dynamic:1900000000000000001", "1900000000000000001", nil); conflict || degErr != nil {
		t.Fatalf("CardKindArticle -> ItemArticle upgrade must be conflict-free, got conflict=%v err=%v", conflict, degErr)
	}

	// 2. Single-video focal tweet expanding into a multi-page self-reply thread -> safe append!
	localSingleVid := makeLocal("x_single_to_thread", library.ItemMetadata{
		Version:  1,
		Site:     "x",
		Kind:     "dynamic",
		ID:       "dynamic:1900000000000000002",
		RawID:    "1900000000000000002",
		Status:   "complete",
		Text:     "主帖带单视频",
		Duration: 15,
		AssetIDs: []string{"7_1900000000000000002"},
		DouyinVideo: &library.DouyinVideoInfo{
			File:    "video.mp4",
			MediaID: "7_1900000000000000002",
		},
	}, map[string][]byte{"video.mp4": []byte("SYNTH_MP4_DATA")})
	threadFromSingleVid := WorkDownloadPlan{
		Site:     site.X,
		Kind:     "dynamic",
		TargetID: "1900000000000000002",
		Desc:     "主帖带单视频",
		Thread: []PlannedThreadPart{
			{ID: "1900000000000000002", Text: "主帖带单视频", MediaIndexes: []int{1}},
			{ID: "1900000000000000003", Text: "作者后续自回复带图", MediaIndexes: []int{2}},
		},
		Pages: []PlannedNotePage{
			{Index: 1, Kind: "video", MediaID: "7_1900000000000000002", DurationSec: 15, VideoURLs: []string{"http://127.0.0.1/v1.mp4"}},
			{Index: 2, Kind: "image", MediaID: "3_1900000000000000003", ImageURLs: []string{"http://127.0.0.1/img2.jpg"}},
		},
	}
	if !isSafeXThreadAppend(localSingleVid, threadFromSingleVid) {
		t.Fatal("expected single-video -> multi-page thread expansion to satisfy isSafeXThreadAppend")
	}
	if _, conflict, degErr := classifyWorkPlanVsLocal(localSingleVid, threadFromSingleVid, "x:dynamic:1900000000000000002", "1900000000000000002", nil); conflict || degErr != nil {
		t.Fatalf("single-video -> thread expansion must be conflict-free, got conflict=%v err=%v", conflict, degErr)
	}

	// 3. Prepended ThreadAncestors shifting MediaIndexes -> safe append!
	localFocalOnly := makeLocal("x_ancestors", library.ItemMetadata{
		Version: 1,
		Site:    "x",
		Kind:    "dynamic",
		ID:      "dynamic:1900000000000000010",
		RawID:   "1900000000000000010",
		Status:  "complete",
		Text:    "主帖节点",
		Thread: []library.ItemThreadPart{
			{ID: "1900000000000000010", Text: "主帖节点", MediaIndexes: []int{1}},
		},
		NotePages: []library.NotePageRecord{
			{Index: 1, Kind: "image", Image: "images/01.jpg", MediaID: "3_focal_img"},
		},
	}, map[string][]byte{"images/01.jpg": []byte("FOCAL_JPG")})
	planWithAncestor := WorkDownloadPlan{
		Site:     site.X,
		Kind:     "dynamic",
		TargetID: "1900000000000000010",
		Desc:     "主帖节点",
		Thread: []PlannedThreadPart{
			{ID: "1900000000000000009", Text: "上文祖先帖", MediaIndexes: []int{1}},
			{ID: "1900000000000000010", Text: "主帖节点", MediaIndexes: []int{2}},
			{ID: "1900000000000000011", Text: "续写自回复", MediaIndexes: []int{3}},
		},
		Pages: []PlannedNotePage{
			{Index: 1, Kind: "image", MediaID: "3_ancestor_img", ImageURLs: []string{"http://127.0.0.1/anc.jpg"}},
			{Index: 2, Kind: "image", MediaID: "3_focal_img", ImageURLs: []string{"http://127.0.0.1/focal.jpg"}},
			{Index: 3, Kind: "image", MediaID: "3_reply_img", ImageURLs: []string{"http://127.0.0.1/reply.jpg"}},
		},
	}
	if !isSafeXThreadAppend(localFocalOnly, planWithAncestor) {
		t.Fatal("expected prepended ThreadAncestors with shifted MediaIndexes to satisfy isSafeXThreadAppend")
	}
	if _, conflict, degErr := classifyWorkPlanVsLocal(localFocalOnly, planWithAncestor, "x:dynamic:1900000000000000010", "1900000000000000010", nil); conflict || degErr != nil {
		t.Fatalf("prepended ThreadAncestors must be conflict-free, got conflict=%v err=%v", conflict, degErr)
	}

	// 4. Genuine substantive edit inside existing thread node MUST NOT be treated as a safe append and MUST trigger conflict!
	editedPlan := planWithAncestor
	editedPlan.Thread = append([]PlannedThreadPart(nil), planWithAncestor.Thread...)
	editedPlan.Thread[1].Text = "完全改写后的全新正文与原内容不同"
	if isSafeXThreadAppend(localFocalOnly, editedPlan) {
		t.Fatal("edited thread node must NOT satisfy isSafeXThreadAppend")
	}
	if _, conflict, degErr := classifyWorkPlanVsLocal(localFocalOnly, editedPlan, "x:dynamic:1900000000000000010", "1900000000000000010", nil); degErr != nil || !conflict {
		t.Fatalf("expected conflict for edited thread node with extra pages, got conflict=%v err=%v", conflict, degErr)
	}
	samePageEdited := WorkDownloadPlan{
		Site:     site.X,
		Kind:     "dynamic",
		TargetID: "1900000000000000010",
		Desc:     "完全改写后的全新正文与原内容不同",
		Thread: []PlannedThreadPart{
			{ID: "1900000000000000010", Text: "完全改写后的全新正文与原内容不同", MediaIndexes: []int{1}},
		},
		Pages: []PlannedNotePage{
			{Index: 1, Kind: "image", MediaID: "3_focal_img", ImageURLs: []string{"http://127.0.0.1/focal.jpg"}},
		},
	}
	notice, conflict, degErr := classifyWorkPlanVsLocal(localFocalOnly, samePageEdited, "x:dynamic:1900000000000000010", "1900000000000000010", nil)
	if degErr != nil || !conflict || notice.ReasonKind != ConflictReasonText {
		t.Fatalf("expected ConflictReasonText for edited thread node, got conflict=%v reason=%q err=%v", conflict, notice.ReasonKind, degErr)
	}
}

func TestDEC096BilibiliMultiPartV2SinglePartDoesNotStampV3BeforeOldPartChaptersChecked(t *testing.T) {
	vBytes, err := BuildSyntheticVideoM4S(60, 30)
	if err != nil {
		t.Fatal(err)
	}
	aBytes, err := BuildSyntheticAudioM4S(94)
	if err != nil {
		t.Fatal(err)
	}

	var (
		srvURL   string
		v2ByCID  = map[int64]int{}
		v2Mu     atomic.Int32
		v2Counts = func(cid int64) {}
	)
	_ = v2Counts
	var p1V2Calls, p2V2Calls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/x/web-interface/wbi/view", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"bvid": "BV1multiV2001", "aid": 88001, "title": "旧多P视频", "desc": "简介",
				"pubdate": 1700000000, "duration": 4,
				"owner": map[string]any{"mid": 5002, "name": "UP"},
				"pages": []map[string]any{
					{"cid": 88101, "page": 1, "part": "P1", "duration": 2},
					{"cid": 88102, "page": 2, "part": "P2", "duration": 2},
				},
			},
		})
	})
	mux.HandleFunc("/x/player/wbi/playurl", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"quality": 80, "accept_format": "hdflv2,flv,mp4", "accept_quality": []int{80},
				"dash": map[string]any{
					"duration": 2,
					"video": []map[string]any{
						{"id": 80, "baseUrl": srvURL + "/m4s/v.m4s", "bandwidth": 900000, "mimeType": "video/mp4", "codecs": "avc1.64001F", "width": 1920, "height": 1080, "frameRate": "30", "codecid": 7},
					},
					"audio": []map[string]any{
						{"id": 30280, "baseUrl": srvURL + "/m4s/a.m4s", "bandwidth": 192000, "mimeType": "audio/mp4", "codecs": "mp4a.40.2"},
					},
				},
			},
		})
	})
	mux.HandleFunc("/x/player/wbi/v2", func(w http.ResponseWriter, r *http.Request) {
		v2Mu.Add(1)
		cidVal, _ := strconv.ParseInt(r.URL.Query().Get("cid"), 10, 64)
		title := "P1 历史看点"
		if cidVal == 88101 {
			p1V2Calls.Add(1)
		} else if cidVal == 88102 {
			p2V2Calls.Add(1)
			title = "P2 新分P看点"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"bvid":        r.URL.Query().Get("bvid"),
				"cid":         cidVal,
				"subtitle":    map[string]any{"subtitles": []any{}},
				"view_points": []map[string]any{{"type": 2, "from": 0, "to": 30, "content": title}},
			},
		})
	})
	mux.HandleFunc("/m4s/v.m4s", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(vBytes)))
		_, _ = w.Write(vBytes)
	})
	mux.HandleFunc("/m4s/a.m4s", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(aBytes)))
		_, _ = w.Write(aBytes)
	})
	_ = v2ByCID

	mgr, libStore, srv := setupDownloadEnv(t, mux, 2)
	srvURL = srv.URL
	cfg := settings.Default("").Download
	cfg.SaveCover = false
	cfg.SaveDanmaku = false
	cfg.SaveSubtitles = false
	cfg.MultiPart = "all"
	mgr.UpdateSettings(cfg)

	ctx := context.Background()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	// Seed legacy v2 item with only P1 cached (ChaptersChecked=false, Chapters=nil).
	itemDir := filepath.Join(libStore.CacheDir(), "bilibili", "5002", "videos", "BV1multiV2001")
	if err := os.MkdirAll(itemDir, 0o700); err != nil {
		t.Fatal(err)
	}
	p1Raw := buildTestMuxedMP4Bytes(t)
	if err := os.WriteFile(filepath.Join(itemDir, "P01.mp4"), p1Raw, 0o600); err != nil {
		t.Fatal(err)
	}
	p1Sum := sha256.Sum256(p1Raw)
	if _, err := libStore.SaveItem(itemDir, library.ItemMetadata{
		Version: 1, ID: "video:BV1multiV2001", Site: "bilibili", Kind: "video", Status: "complete",
		Title: "旧多P视频", Desc: "简介", PubTs: 1700000000, Duration: 4,
		CacheStandardVersion: 2,
		ExpectedPages:        []int{1},
		Author:               library.ItemAuthor{Site: "bilibili", Mid: 5002, UID: "5002", Name: "UP"},
		Media: []library.MediaFile{
			{Role: "video", Path: "P01.mp4", Bytes: int64(len(p1Raw)), SHA256: hex.EncodeToString(p1Sum[:]), Width: 320, Height: 240, Duration: 2},
		},
		Parts: []library.VideoPartRecord{{
			CID: 88101, Page: 1, Title: "P1", Duration: 2, File: "P01.mp4",
			DanmakuState: "skipped", SubtitleState: "skipped",
			ChaptersChecked: false,
			Video:           library.PartVideoInfo{Quality: 80},
			Audio:           library.PartAudioInfo{Quality: 30280},
		}},
	}); err != nil {
		t.Fatal(err)
	}

	// Step 1: MultiPart switched first->all, enqueue ONLY missing P2!
	tasksP2, err := mgr.EnqueueVideo(ctx, VideoEnqueueRequest{BVID: "BV1multiV2001", Pages: []int{2}, Source: "queue"})
	if err != nil {
		t.Fatalf("EnqueueVideo P2: %v", err)
	}
	if err := mgr.WaitTasks(ctx, tasksP2[0].TaskID); err != nil {
		t.Fatalf("WaitTasks P2: %v", err)
	}

	afterP2, _, err := libStore.GetItem("bilibili:video:BV1multiV2001")
	if err != nil {
		t.Fatal(err)
	}
	// CRITICAL (Review Finding #4): P1 has NOT had chapters checked yet -> MUST NOT stamp v3!
	if afterP2.CacheStandardVersion >= library.BilibiliVideoStandardVersion {
		t.Fatalf("caching only P2 must NOT prematurely stamp v3 while P1 lacks ChaptersChecked, got std=%d parts=%+v",
			afterP2.CacheStandardVersion, afterP2.Parts)
	}
	if p1V2Calls.Load() != 0 || p2V2Calls.Load() != 1 {
		t.Fatalf("expected P1 v2=0 P2 v2=1 after caching P2, got P1=%d P2=%d", p1V2Calls.Load(), p2V2Calls.Load())
	}

	// Step 2: Run supplement -> skips already ChaptersChecked P2 (0 redundant call!), fetches P1 chapters, and NOW stamps v3!
	preDetail := &bilibili.VideoDetail{
		BVID: "BV1multiV2001",
		Pages: []bilibili.VideoPage{
			{CID: 88101, Page: 1, Title: "P1", Duration: 2},
			{CID: 88102, Page: 2, Title: "P2", Duration: 2},
		},
	}
	supTask, err := mgr.CacheSupplementWithDetail(ctx, "BV1multiV2001", "queue", preDetail)
	if err != nil {
		t.Fatalf("CacheSupplementWithDetail: %v", err)
	}
	if err := mgr.WaitTasks(ctx, supTask.TaskID); err != nil {
		t.Fatalf("WaitTasks supplement: %v", err)
	}
	if p1V2Calls.Load() != 1 || p2V2Calls.Load() != 1 {
		t.Fatalf("supplement must fetch P1 chapters once and skip already-checked P2, got P1=%d P2=%d",
			p1V2Calls.Load(), p2V2Calls.Load())
	}
	afterSup, _, err := libStore.GetItem("bilibili:video:BV1multiV2001")
	if err != nil {
		t.Fatal(err)
	}
	if afterSup.CacheStandardVersion != library.BilibiliVideoStandardVersion {
		t.Fatalf("expected v3 after all parts ChaptersChecked, got std=%d", afterSup.CacheStandardVersion)
	}
	for _, p := range afterSup.Parts {
		if !p.ChaptersChecked || len(p.Chapters) != 1 {
			t.Fatalf("expected ChaptersChecked=true and 1 chapter on P%d, got %+v", p.Page, p)
		}
	}
}

func TestDEC096BilibiliCompleteDynamicPreservedWhenLinkedVideoUpowerLockedOrDeleted(t *testing.T) {
	jpgBytes := buildTestJPEGBytes(t, 60, 120, 180)
	var srvURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/img/dyn1.jpg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(jpgBytes)
	})
	mux.HandleFunc("/x/polymer/web-dynamic/v1/detail", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"item": map[string]any{
					"id_str": "9500301",
					"type":   "DYNAMIC_TYPE_AV",
					"modules": map[string]any{
						"module_author": map[string]any{"mid": 5003, "name": "UP", "pub_ts": 1700000000},
						"module_dynamic": map[string]any{
							"desc": map[string]any{"text": "完整动态正文"},
							"major": map[string]any{
								"type": "MAJOR_TYPE_ARCHIVE",
								"archive": map[string]any{
									"bvid":  "BV1upowerLk02",
									"title": "完整动态",
									"cover": srvURL + "/img/dyn1.jpg",
								},
							},
						},
					},
				},
			},
		})
	})
	mux.HandleFunc("/x/web-interface/wbi/view", func(w http.ResponseWriter, r *http.Request) {
		bvid := r.URL.Query().Get("bvid")
		if bvid == "BV1deadLink01" {
			_, _ = w.Write([]byte(`{"code":-404,"message":"啥都木有"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"bvid": bvid, "aid": 91001, "title": "充电专属关联视频",
				"is_upower_exclusive": true, "is_upower_play": false, "is_upower_preview": true,
				"owner": map[string]any{"mid": 5003, "name": "UP"},
				"pages": []map[string]any{{"cid": 91101, "page": 1, "part": "P1", "duration": 60}},
			},
		})
	})

	mgr, libStore, srv := setupDownloadEnv(t, mux, 2)
	srvURL = srv.URL
	ctx := context.Background()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	// Seed an already-complete Bilibili video dynamic referencing a upower-locked video.
	dynDir := filepath.Join(libStore.CacheDir(), "bilibili", "5003", "dynamics", "9500301")
	if err := os.MkdirAll(dynDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := libStore.SaveItem(dynDir, library.ItemMetadata{
		Version: 1, ID: "dynamic:9500301", Site: "bilibili", Kind: "dynamic", Status: "complete",
		Title: "完整动态", Text: "完整动态正文", PubTs: 1700000000, CacheStandardVersion: 2,
		Author: library.ItemAuthor{Site: "bilibili", Mid: 5003, UID: "5003", Name: "UP"},
		Videos: []string{"video:BV1upowerLk02"},
	}); err != nil {
		t.Fatal(err)
	}

	dynItem := bilibili.DynamicItem{
		ID: "9500301", Title: "完整动态", Text: "完整动态正文", PubTs: 1700000000,
		Author: bilibili.AuthorInfo{Mid: 5003, Name: "UP"},
		Videos: []bilibili.DynamicVideoRef{{BVID: "BV1upowerLk02"}},
	}
	tk, err := mgr.EnqueueDynamic(ctx, DynamicEnqueueRequest{
		DynamicID: "9500301", Item: &dynItem, CacheLinkedVideos: true, Source: "queue",
	})
	if err != nil {
		t.Fatalf("EnqueueDynamic: %v", err)
	}
	if err := mgr.WaitTasks(ctx, tk.TaskID); err != nil {
		t.Fatalf("complete dynamic must not fail when linked video is upower-locked: %v", err)
	}
	after, _, err := libStore.GetItem("bilibili:dynamic:9500301")
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != "complete" || len(after.Missing) != 0 {
		t.Fatalf("complete dynamic was degraded to status=%q missing=%+v", after.Status, after.Missing)
	}
	_ = jpgBytes
}
