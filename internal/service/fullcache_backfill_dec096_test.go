package service

import (
	"context"
	"encoding/json"
	"fmt"
	"image/color"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/site/douyin"
	"github.com/chialecode/trace/internal/site/weibo"
	"github.com/chialecode/trace/internal/site/x"
	"github.com/chialecode/trace/internal/subscription"
)

func TestDEC096BilibiliOldVideoChaptersBackfillZeroPlayURLAndUnavailableAntiLoop(t *testing.T) {
	nowStart := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	clock := newFakeQueueClock(nowStart)

	var (
		viewCalls         atomic.Int32
		playURLCalls      atomic.Int32
		playerV2Calls     atomic.Int32
		subtitleBodyCalls atomic.Int32
		srvURL            string
	)
	biliSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/x/web-interface/nav":
			_, _ = fmt.Fprint(w, `{"code":0,"data":{"isLogin":true,"mid":778899,"uname":"测试用户","wbi_img":{"img_url":"https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png","sub_url":"https://i0.hdslb.com/bfs/wbi/4932caff0ff746eab6f01bf08b70ac45.png"}}}`)
		case "/3001":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = fmt.Fprint(w, `<html><script id="__RENDER_DATA__" type="application/json">%7B%22access_id%22%3A%22synth_webid%22%7D</script></html>`)
		case "/x/space/wbi/arc/search":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"page": map[string]any{"pn": 1, "ps": 30, "count": 2},
					"list": map[string]any{
						"vlist": []map[string]any{
							{"bvid": "BV1chap000001", "aid": 101, "title": "旧视频补分段跳转", "description": "简介", "length": "02:00", "created": nowStart.Add(-30 * 24 * time.Hour).Unix(), "mid": 3001, "author": "UP"},
							{"bvid": "BV1dead000002", "aid": 102, "title": "已失效视频防循环", "description": "简介", "length": "01:00", "created": nowStart.Add(-30 * 24 * time.Hour).Unix(), "mid": 3001, "author": "UP"},
						},
					},
				},
			})
		case "/x/web-interface/wbi/view", "/x/web-interface/view":
			viewCalls.Add(1)
			bvid := r.URL.Query().Get("bvid")
			if bvid == "BV1dead000002" {
				_, _ = fmt.Fprint(w, `{"code":-404,"message":"啥都木有"}`)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"bvid":     bvid,
					"aid":      101,
					"title":    "旧视频补分段跳转",
					"desc":     "简介",
					"pubdate":  nowStart.Add(-30 * 24 * time.Hour).Unix(),
					"duration": 120,
					"owner":    map[string]any{"mid": 3001, "name": "UP"},
					"pages": []map[string]any{
						{"cid": 30101, "page": 1, "part": "P1", "duration": 60},
						{"cid": 30102, "page": 2, "part": "P2", "duration": 60},
					},
				},
			})
		case "/x/player/wbi/playurl":
			playURLCalls.Add(1)
			_, _ = fmt.Fprint(w, `{"code":-404,"message":"unexpected PlayURL call"}`)
		case "/x/player/wbi/v2":
			playerV2Calls.Add(1)
			bvid := r.URL.Query().Get("bvid")
			cidVal, _ := strconv.ParseInt(r.URL.Query().Get("cid"), 10, 64)
			chTitle := "P1 核心分段"
			if cidVal == 30102 {
				chTitle = "P2 结尾总结"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"bvid": bvid,
					"cid":  cidVal,
					"subtitle": map[string]any{
						"subtitles": []map[string]any{
							{"lan": "zh-CN", "lan_doc": "中文", "subtitle_url": srvURL + "/sub/zh.json"},
						},
					},
					"view_points": []map[string]any{
						{"type": 2, "from": 0, "to": 60, "content": chTitle},
					},
				},
			})
		case "/sub/zh.json":
			subtitleBodyCalls.Add(1)
			_, _ = fmt.Fprint(w, `{"body":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer biliSrv.Close()
	srvURL = biliSrv.URL

	loc := testLocations(t)
	b, err := NewBackend(Options{
		Locations:        loc,
		Desktop:          NewMockDesktop(),
		DisableBgWorkers: true,
		CacheQueueClock:  clock,
		CacheQueueRand:   func(int) int { return 0 },
		BilibiliOpts: bilibili.ClientOptions{
			Endpoints: bilibili.Endpoints{
				APIBase: biliSrv.URL, PassportBase: biliSrv.URL, WWWBase: biliSrv.URL,
				SpaceBase: biliSrv.URL, DynamicBase: biliSrv.URL, LiveAPIBase: biliSrv.URL, LiveWebBase: biliSrv.URL,
			},
			PacingInterval: -1,
			AllowLocalhost: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel()
		_ = b.Close()
	}()
	if err := b.dl.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ImportCookie(ctx, "SESSDATA=synth_096; bili_jct=csrf_096; DedeUserID=778899", ""); err != nil {
		t.Fatal(err)
	}
	curSet := b.GetSettings()
	curSet.Download.SaveDanmaku = false
	curSet.Download.SaveSubtitles = false
	curSet.Download.SaveCover = false
	curSet.Download.VideoQuality = "best"
	if _, err := b.UpdateSettings(curSet); err != nil {
		t.Fatal(err)
	}

	// Verify 116 (1080P60) with unprobed BestVideo is NOT falsely treated as ceiling under "best".
	meta116 := library.ItemMetadata{
		Site: "bilibili", Kind: "video", Status: "complete",
		PubTs:                nowStart.Add(-30 * 24 * time.Hour).Unix(),
		CacheStandardVersion: library.BilibiliVideoStandardVersion,
		Parts: []library.VideoPartRecord{{
			CID: 9901, Page: 1, File: "P01.mp4",
			Video: library.PartVideoInfo{Quality: 116, QualityName: "1080P60"},
			Audio: library.PartAudioInfo{Quality: 30280},
		}},
	}
	if bilibiliVideoAtCeiling(meta116, curSet.Download, nowStart) {
		t.Fatal("qn=116 (1080P60) with unprobed BestVideo must NOT be treated as ceiling under VideoQuality=best")
	}

	// Seed v2 2P video at probed stream ceiling (BestVideo=80, ProbedAt set) lacking chapters.
	dir1 := filepath.Join(b.lib.CacheDir(), "bilibili", "3001", "videos", "BV1chap000001")
	p1mf := writeVerifiedLocalFile(t, dir1, "P01.mp4", []byte("OLD_V2_P1"), "video", 1920, 1080, 60)
	p2mf := writeVerifiedLocalFile(t, dir1, "P02.mp4", []byte("OLD_V2_P2"), "video", 1920, 1080, 60)
	if _, err := b.lib.SaveItem(dir1, library.ItemMetadata{
		Version: 1, ID: "video:BV1chap000001", Site: "bilibili", Kind: "video", Status: "complete",
		Title: "旧视频补分段跳转", Desc: "简介", PubTs: nowStart.Add(-30 * 24 * time.Hour).Unix(), Duration: 120,
		CacheStandardVersion: 2,
		Author:               library.ItemAuthor{Site: "bilibili", Mid: 3001, UID: "3001", Name: "UP"},
		Media:                []library.MediaFile{p1mf, p2mf},
		Parts: []library.VideoPartRecord{
			{
				CID: 30101, Page: 1, Title: "P1", Duration: 60, File: "P01.mp4",
				DanmakuState: "skipped", SubtitleState: "skipped",
				Video:     library.PartVideoInfo{Quality: 80},
				Audio:     library.PartAudioInfo{Quality: 30280},
				Available: library.PartAvailableInfo{BestVideo: 80, BestAudio: 30280, ProbedAt: nowStart.Add(-15 * 24 * time.Hour).Format(time.RFC3339)},
			},
			{
				CID: 30102, Page: 2, Title: "P2", Duration: 60, File: "P02.mp4",
				DanmakuState: "skipped", SubtitleState: "skipped",
				Video:     library.PartVideoInfo{Quality: 80},
				Audio:     library.PartAudioInfo{Quality: 30280},
				Available: library.PartAvailableInfo{BestVideo: 80, BestAudio: 30280, ProbedAt: nowStart.Add(-15 * 24 * time.Hour).Format(time.RFC3339)},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// Seed v2 unavailable (-404) video with no RecheckedAt yet.
	dir2 := filepath.Join(b.lib.CacheDir(), "bilibili", "3001", "videos", "BV1dead000002")
	deadMF := writeVerifiedLocalFile(t, dir2, "P01.mp4", []byte("DEAD_V1_KEEP"), "video", 1920, 1080, 60)
	if _, err := b.lib.SaveItem(dir2, library.ItemMetadata{
		Version: 1, ID: "video:BV1dead000002", Site: "bilibili", Kind: "video", Status: "complete",
		Title: "已失效视频防循环", Desc: "简介", PubTs: nowStart.Add(-30 * 24 * time.Hour).Unix(), Duration: 60,
		CacheStandardVersion: 2,
		Author:               library.ItemAuthor{Site: "bilibili", Mid: 3001, UID: "3001", Name: "UP"},
		Media:                []library.MediaFile{deadMF},
		Parts: []library.VideoPartRecord{{
			CID: 30201, Page: 1, Title: "P1", Duration: 60, File: "P01.mp4",
			DanmakuState: "skipped", SubtitleState: "skipped",
			Video: library.PartVideoInfo{Quality: 80}, Audio: library.PartAudioInfo{Quality: 30280},
		}},
	}); err != nil {
		t.Fatal(err)
	}

	b.queue.Start(ctx)
	q1, err := b.EnqueueCacheQueue(ctx, CacheQueueRequest{Site: "bilibili", List: "videos", User: "3001", Origin: QueueOriginFullCache})
	if err != nil {
		t.Fatal(err)
	}
	d1, _ := clock.WaitForPendingTimer(t, 20*time.Second)
	clock.Advance(d1)
	waitQueuePredicate(t, b, q1.ID, "sweep 1 completed", func(qt CacheQueueTask) bool {
		return qt.Status == CacheQueueCompleted
	})

	if playURLCalls.Load() != 0 {
		t.Fatalf("expected 0 PlayURL calls for ceiling chapters-only backfill, got %d", playURLCalls.Load())
	}
	if subtitleBodyCalls.Load() != 0 {
		t.Fatalf("expected 0 subtitle body downloads when SaveSubtitles=false, got %d", subtitleBodyCalls.Load())
	}
	if viewCalls.Load() != 2 || playerV2Calls.Load() != 2 {
		t.Fatalf("expected viewCalls=2 and playerV2Calls=2 (P1+P2), got view=%d v2=%d", viewCalls.Load(), playerV2Calls.Load())
	}

	gotChap, _, err := b.GetLibraryItem("bilibili:video:BV1chap000001")
	if err != nil {
		t.Fatal(err)
	}
	if gotChap.CacheStandardVersion != library.BilibiliVideoStandardVersion || gotChap.RemotePartCount != 2 ||
		len(gotChap.Parts) != 2 || len(gotChap.Parts[0].Chapters) != 1 || len(gotChap.Parts[1].Chapters) != 1 {
		t.Fatalf("expected v3 + chapters on P1 & P2, got %+v", gotChap)
	}
	gotDead, _, err := b.GetLibraryItem("bilibili:video:BV1dead000002")
	if err != nil {
		t.Fatal(err)
	}
	if gotDead.RecheckedAt == "" || gotDead.CacheStandardVersion != 2 {
		t.Fatalf("unavailable video must stamp RecheckedAt while keeping v2 untouched, got std=%d rechecked=%q",
			gotDead.CacheStandardVersion, gotDead.RecheckedAt)
	}
	if rawDead, _ := os.ReadFile(filepath.Join(dir2, "P01.mp4")); string(rawDead) != "DEAD_V1_KEEP" {
		t.Fatalf("unavailable video local file was modified: %q", string(rawDead))
	}

	// Second sweep 1 hour later: BV1chap000001 skips at Gate B (v3 + ceiling) and BV1dead000002 skips at Gate C (7d cooldown) -> ZERO API probes!
	clock.Advance(1 * time.Hour)
	viewBefore := viewCalls.Load()
	v2Before := playerV2Calls.Load()
	q2, err := b.EnqueueCacheQueue(ctx, CacheQueueRequest{Site: "bilibili", List: "videos", User: "3001", Origin: QueueOriginFullCache})
	if err != nil {
		t.Fatal(err)
	}
	waitQueuePredicate(t, b, q2.ID, "sweep 2 completed", func(qt CacheQueueTask) bool {
		return qt.Status == CacheQueueCompleted
	})
	if viewCalls.Load() != viewBefore || playURLCalls.Load() != 0 || playerV2Calls.Load() != v2Before {
		t.Fatalf("second sweep must make 0 probes (Gate B + Gate C anti-loop), got +view=%d playurl=%d +v2=%d",
			viewCalls.Load()-viewBefore, playURLCalls.Load(), playerV2Calls.Load()-v2Before)
	}
}

func TestDEC096BilibiliPeriodicFullCacheAutoIncludesDynamicsAndInspect(t *testing.T) {
	loc := testLocations(t)
	biliSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/x/web-interface/nav" {
			_, _ = fmt.Fprint(w, `{"code":0,"data":{"isLogin":true,"mid":778899,"uname":"测试用户"}}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer biliSrv.Close()

	b, err := NewBackend(Options{
		Locations:        loc,
		Desktop:          NewMockDesktop(),
		DisableBgWorkers: true,
		BilibiliOpts: bilibili.ClientOptions{
			Endpoints: bilibili.Endpoints{
				APIBase: biliSrv.URL, PassportBase: biliSrv.URL, WWWBase: biliSrv.URL,
				SpaceBase: biliSrv.URL, DynamicBase: biliSrv.URL, LiveAPIBase: biliSrv.URL, LiveWebBase: biliSrv.URL,
			},
			PacingInterval: -1, AllowLocalhost: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	ctx := context.Background()
	if _, err := b.ImportCookie(ctx, "SESSDATA=synth_dyn; bili_jct=csrf_dyn; DedeUserID=778899", ""); err != nil {
		t.Fatal(err)
	}

	// Sub A (mid=2001): has dynamic auto-cache tag (TagCacheDraw) + full_cache -> should enqueue videos + dynamics.
	if _, err := b.subs.Create("UP动态标签", "", []subscription.AccountRef{{Site: "bilibili", UID: "2001", Name: "UP1"}},
		subscription.SubRules{FullCache: true, AutoCache: subscription.AutoCacheRule{Enabled: true, Types: []string{"draw"}}}); err != nil {
		t.Fatal(err)
	}
	// Sub B (mid=2002): video-only tags, BUT has local cached dynamic -> should enqueue videos + dynamics.
	if _, err := b.subs.Create("UP本地动态", "", []subscription.AccountRef{{Site: "bilibili", UID: "2002", Name: "UP2"}},
		subscription.SubRules{FullCache: true, AutoCache: subscription.AutoCacheRule{Enabled: true, Types: []string{"video"}}}); err != nil {
		t.Fatal(err)
	}
	dynDir := filepath.Join(b.lib.CacheDir(), "bilibili", "2002", "dynamics", "9200201")
	dynImgMF := writeVerifiedLocalFile(t, dynDir, "images/01.jpg", makeServiceTestJPEG(t, 16, 16, color.RGBA{10, 80, 120, 255}), "image", 16, 16, 0)
	dynImgMF.Source = "https://i0.hdslb.com/bfs/new_dyn/picA.jpg"
	if _, err := b.lib.SaveItem(dynDir, library.ItemMetadata{
		Version: 1, ID: "dynamic:9200201", Site: "bilibili", Kind: "dynamic", Status: "complete",
		Title: "旧动态", Text: "查看链接 (https://example.com/old)",
		CacheStandardVersion: 2,
		Author:               library.ItemAuthor{Site: "bilibili", Mid: 2002, UID: "2002", Name: "UP2"},
		Media:                []library.MediaFile{dynImgMF},
	}); err != nil {
		t.Fatal(err)
	}
	// Sub C (mid=2003): video-only tags AND no local cached dynamic -> should enqueue videos ONLY.
	if _, err := b.subs.Create("UP仅视频", "", []subscription.AccountRef{{Site: "bilibili", UID: "2003", Name: "UP3"}},
		subscription.SubRules{FullCache: true, AutoCache: subscription.AutoCacheRule{Enabled: true, Types: []string{"video"}}}); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	// Run 3 sweeps (clearing queue between accounts since Bilibili allows 1 active account per sweep).
	seenLists := map[string][]string{}
	for range 3 {
		started := b.startDueFullCacheSweeps(ctx, now)
		if len(started) == 0 {
			break
		}
		tasks := b.ListCacheQueue()
		for _, tk := range tasks {
			if tk.Status == CacheQueueCompleted || tk.Status == CacheQueueStopped {
				continue
			}
			seenLists[tk.User] = append(seenLists[tk.User], tk.List)
			_, _ = b.CancelCacheQueue(tk.ID)
			_ = b.RemoveCacheQueue(tk.ID)
		}
	}
	if strings.Join(seenLists["2001"], ",") != "videos,dynamics" {
		t.Fatalf("UP 2001 (TagCacheDraw) lists = %v, want [videos dynamics]", seenLists["2001"])
	}
	if strings.Join(seenLists["2002"], ",") != "videos,dynamics" {
		t.Fatalf("UP 2002 (existing local dynamic) lists = %v, want [videos dynamics]", seenLists["2002"])
	}
	if strings.Join(seenLists["2003"], ",") != "videos" {
		t.Fatalf("UP 2003 (video-only, no local dynamic) lists = %v, want [videos]", seenLists["2003"])
	}

	// Verify Bilibili dynamic deferred lock-free Tier 0 postInspect + missing orig.json + path-aligned motion + linked video check.
	savedDyn, _, _ := b.GetLibraryItem("bilibili:dynamic:9200201")
	remoteDyn := bilibili.DynamicItem{
		ID: "9200201", Title: "旧动态", Text: "查看链接",
		Images: []bilibili.DynamicMedia{{URL: "https://i0.hdslb.com/bfs/new_dyn/picA.jpg"}},
	}
	dec := b.inspectBilibiliCachedDynamicDeferred(remoteDyn, savedDyn, QueueOriginFullCache)
	if dec.action != inspectSkipNoOp || dec.postInspect == nil {
		t.Fatalf("expected inspectSkipNoOp with deferred postInspect for Tier 0 Bilibili dynamic, got %+v", dec)
	}
	// Before calling postInspect, disk manifest must still be v2 with empty AssetIDs (lock-free guarantee).
	before, _, _ := b.GetLibraryItem("bilibili:dynamic:9200201")
	if before.CacheStandardVersion != 2 || len(before.AssetIDs) != 0 {
		t.Fatalf("Tier 0 must NOT write before postInspect runs, got std=%d assets=%v", before.CacheStandardVersion, before.AssetIDs)
	}
	dec.postInspect()
	after, _, _ := b.GetLibraryItem("bilibili:dynamic:9200201")
	if after.CacheStandardVersion != library.BilibiliDynamicStandardVersion || len(after.AssetIDs) != 1 || len(after.Links) == 0 {
		t.Fatalf("postInspect did not backfill v3/AssetIDs/Links, got std=%d assets=%v links=%+v",
			after.CacheStandardVersion, after.AssetIDs, after.Links)
	}

	// Missing orig.json -> inspectEnqueue.
	remoteWithOrig := remoteDyn
	remoteWithOrig.Origin = &bilibili.DynamicItem{ID: "888001", Text: "原动态"}
	if dOrig := b.inspectBilibiliCachedDynamicDeferred(remoteWithOrig, after, QueueOriginFullCache); dOrig.action != inspectEnqueue {
		t.Fatalf("missing orig.json must trigger inspectEnqueue, got %+v", dOrig)
	}

	// Linked video below BilibiliVideoStandardVersion -> inspectEnqueue with CacheLinkedVideos=true.
	vidDir := filepath.Join(b.lib.CacheDir(), "bilibili", "2002", "videos", "BV1link000001")
	vmf := writeVerifiedLocalFile(t, vidDir, "P01.mp4", []byte("LINKED_VID"), "video", 1920, 1080, 10)
	if _, err := b.lib.SaveItem(vidDir, library.ItemMetadata{
		Version: 1, ID: "video:BV1link000001", Site: "bilibili", Kind: "video", Status: "complete",
		CacheStandardVersion: 2,
		Author:               library.ItemAuthor{Site: "bilibili", Mid: 2002, UID: "2002", Name: "UP2"},
		Media:                []library.MediaFile{vmf},
		Parts:                []library.VideoPartRecord{{CID: 1, Page: 1, File: "P01.mp4"}},
	}); err != nil {
		t.Fatal(err)
	}
	afterWithVid := after
	afterWithVid.Videos = []string{"video:BV1link000001"}
	remoteWithLinked := remoteDyn
	remoteWithLinked.Videos = []bilibili.DynamicVideoRef{{BVID: "BV1link000001"}}
	if dLink := b.inspectBilibiliCachedDynamicDeferred(remoteWithLinked, afterWithVid, QueueOriginFullCache); dLink.action != inspectEnqueue || dLink.enqueue == nil {
		t.Fatalf("linked video < v3 must trigger inspectEnqueue with custom enqueue, got %+v", dLink)
	}
}

func TestDEC096DouyinInspectionAntiLoopBitrateAndLockFreePostInspect(t *testing.T) {
	loc := testLocations(t)
	b, err := NewBackend(Options{Locations: loc, Desktop: NewMockDesktop(), DisableBgWorkers: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()

	// 1. OriginalState="unavailable" stops infinite original retry + Tier 0 postInspect runs lock-free.
	dyDir := filepath.Join(b.lib.CacheDir(), "douyin", "abc123456789", "works", "7509600000000001")
	vmf := writeVerifiedLocalFile(t, dyDir, "video.mp4", []byte("DY_MP4"), "video", 1080, 1920, 10)
	covMF := writeVerifiedLocalFile(t, dyDir, "cover.jpg", makeServiceTestJPEG(t, 16, 16, color.RGBA{10, 20, 30, 255}), "cover", 16, 16, 0)
	savedDY, err := b.lib.SaveItem(dyDir, library.ItemMetadata{
		Version: 1, ID: "video:7509600000000001", Site: "douyin", Kind: "video", Status: "complete",
		Title: "原画不可用抖音视频", Desc: "原画不可用抖音视频", Duration: 10, CacheStandardVersion: 2,
		Author: library.ItemAuthor{Site: "douyin", UID: "MS4wLjAB_dy1", Name: "合成抖音"},
		Media:  []library.MediaFile{vmf, covMF},
		DouyinVideo: &library.DouyinVideoInfo{
			File: "video.mp4", Gear: "normal_1080_0", OriginalState: "unavailable",
			Width: 1080, Height: 1920, Bitrate: 2000000,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	remoteDY := douyin.WorkDetail{
		ID: "7509600000000001", Kind: douyin.WorkKindVideo, Desc: "原画不可用抖音视频", DurationSec: 10,
		Author:       douyin.AuthorSummary{SecUID: "MS4wLjAB_dy1", Nickname: "合成抖音"},
		CoverURLs:    []string{"https://p3.douyinpic.com/cov.jpg"},
		OriginalURLs: []string{"https://v3.douyinvod.com/orig.mp4"},
		MediaID:      "v_dy_media_1",
		Video: &douyin.SelectedVODStream{
			URI: "v_dy_media_1", GearName: "normal_1080_0", Width: 1080, Height: 1920, BitRate: 2000000,
			URLs: []string{"https://v3.douyinvod.com/v1080.mp4"},
		},
	}
	dec := b.inspectDouyinCachedWorkDeferred(remoteDY, savedDY, QueueOriginFullCache, "", "")
	if dec.action != inspectSkipNoOp || dec.postInspect == nil {
		t.Fatalf("OriginalState=unavailable must stop original retry loop (with Tier 0 postInspect), got %+v", dec)
	}
	before, _, _ := b.lib.GetItem(savedDY.ItemKey)
	if before.CacheStandardVersion != 2 || before.DouyinVideo == nil || before.DouyinVideo.MediaID != "" {
		t.Fatalf("Douyin Tier 0 must NOT write before postInspect, got std=%d video=%+v", before.CacheStandardVersion, before.DouyinVideo)
	}
	dec.postInspect()
	after, _, _ := b.lib.GetItem(savedDY.ItemKey)
	if after.CacheStandardVersion != library.DouyinCacheStandardVersion || after.DouyinVideo == nil || after.DouyinVideo.MediaID != "v_dy_media_1" || len(after.AssetIDs) != 1 {
		t.Fatalf("Douyin postInspect did not write v3/MediaID/AssetIDs, got std=%d video=%+v assets=%v",
			after.CacheStandardVersion, after.DouyinVideo, after.AssetIDs)
	}

	// 2. Same-resolution higher BitRate -> inspectEnqueue.
	higherBR := remoteDY
	higherBR.Video = &douyin.SelectedVODStream{
		URI: "v_dy_media_1", GearName: "normal_1080_0", Width: 1080, Height: 1920, BitRate: 4000000,
		URLs: []string{"https://v3.douyinvod.com/v1080_hi.mp4"},
	}
	if decHi := b.inspectDouyinCachedWorkDeferred(higherBR, after, QueueOriginFullCache, "", ""); decHi.action != inspectEnqueue {
		t.Fatalf("same-res higher BitRate must trigger inspectEnqueue, got %+v", decHi)
	}

	// 3. Copyright-restricted empty music exempt from NeedsCompletion retry loop!
	noteDir := filepath.Join(b.lib.CacheDir(), "douyin", "abc123456789", "works", "7509600000000002")
	imgMF := writeVerifiedLocalFile(t, noteDir, "images/01.jpg", makeServiceTestJPEG(t, 16, 16, color.RGBA{40, 50, 60, 255}), "image", 16, 16, 0)
	noteCov := writeVerifiedLocalFile(t, noteDir, "cover.jpg", makeServiceTestJPEG(t, 16, 16, color.RGBA{40, 50, 60, 255}), "cover", 16, 16, 0)
	savedNote, err := b.lib.SaveItem(noteDir, library.ItemMetadata{
		Version: 1, ID: "dynamic:7509600000000002", Site: "douyin", Kind: "dynamic", Status: "partial",
		Title: "版权空配乐图文", Desc: "版权空配乐图文", CacheStandardVersion: library.DouyinCacheStandardVersion,
		Author:    library.ItemAuthor{Site: "douyin", UID: "MS4wLjAB_dy1", Name: "合成抖音"},
		Media:     []library.MediaFile{imgMF, noteCov},
		NotePages: []library.NotePageRecord{{Index: 1, Kind: "image", Image: "images/01.jpg", MediaID: "img_uri_1"}},
		Missing:   []library.MissingRecord{{Item: "music", Reason: "配乐因版权限制暂无可下载链接"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	remoteNote := douyin.WorkDetail{
		ID: "7509600000000002", Kind: douyin.WorkKindNote, Desc: "版权空配乐图文",
		Author:    douyin.AuthorSummary{SecUID: "MS4wLjAB_dy1", Nickname: "合成抖音"},
		CoverURLs: []string{"https://p3.douyinpic.com/cov2.jpg"},
		Pages:     []douyin.NotePage{{Index: 1, Kind: douyin.NotePageImage, URI: "img_uri_1", ImageURLs: []string{"https://p3.douyinpic.com/1.jpg"}}},
		Music:     &douyin.MusicInfo{ID: "m1", Title: "受限配乐", PlayURLs: nil},
	}
	if decMusic := b.inspectDouyinCachedWorkDeferred(remoteNote, savedNote, QueueOriginFullCache, "", ""); decMusic.action != inspectSkipNoOp {
		t.Fatalf("copyright-restricted empty music must NOT loop NeedsCompletion, got %+v", decMusic)
	}

	// 4. Mixed note video page upgrade on higher FPS / BitRate.
	mixNote := savedNote
	mixNote.Status = "complete"
	mixNote.Missing = nil
	mixNote.NotePages = []library.NotePageRecord{{
		Index: 1, Kind: "video", Video: "images/01.mp4", MediaID: "vid_uri_1",
		VideoInfo: &library.DouyinVideoInfo{Width: 1080, Height: 1920, Bitrate: 1500000, FPS: 30},
	}}
	remoteMix := remoteNote
	remoteMix.Pages = []douyin.NotePage{{
		Index: 1, Kind: douyin.NotePageVideo, URI: "vid_uri_1",
		VideoStream: &douyin.SelectedVODStream{
			URI: "vid_uri_1", Width: 1080, Height: 1920, BitRate: 3000000, FPS: 60,
			URLs: []string{"https://v3.douyinvod.com/mix60.mp4"},
		},
	}}
	if decMix := b.inspectDouyinCachedWorkDeferred(remoteMix, mixNote, QueueOriginFullCache, "", ""); decMix.action != inspectEnqueue {
		t.Fatalf("mixed note higher bitrate/FPS video page must trigger inspectEnqueue, got %+v", decMix)
	}
}

func TestDEC096WeiboV4InspectionMediaIncompleteTruncationEditHistoryAndPostInspect(t *testing.T) {
	loc := testLocations(t)
	b, err := NewBackend(Options{Locations: loc, Desktop: NewMockDesktop(), DisableBgWorkers: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()

	// 1. Pure text Weibo post at v2 -> Tier 0 lock-free postInspect syncs EditCount, structured Links, and stamps v4.
	wbDir := filepath.Join(b.lib.CacheDir(), "weibo", "100001", "posts", "5096100000000001")
	if err := os.MkdirAll(wbDir, 0o700); err != nil {
		t.Fatal(err)
	}
	savedText, err := b.lib.SaveItem(wbDir, library.ItemMetadata{
		Version: 1, ID: "dynamic:5096100000000001", Site: "weibo", Kind: "dynamic", Status: "complete",
		Title: "纯文字微博看详情", Text: "纯文字微博看详情", CacheStandardVersion: 2,
		Author: library.ItemAuthor{Site: "weibo", UID: "100001", Name: "合成博主"},
	})
	if err != nil {
		t.Fatal(err)
	}
	remoteText := weibo.Post{
		Mid: "5096100000000001", Author: weibo.AuthorSummary{UID: "100001", ScreenName: "合成博主"},
		Text:  "纯文字微博看详情",
		Links: []site.TextLink{{Label: "详情链接", URL: "https://example.com/wb-detail"}},
	}
	decText := b.inspectWeiboCachedPostDeferred(remoteText, savedText, QueueOriginFullCache)
	if decText.action != inspectSkipNoOp || decText.postInspect == nil {
		t.Fatalf("pure text Weibo v2 must return inspectSkipNoOp + deferred postInspect, got %+v", decText)
	}
	beforeText, _, _ := b.lib.GetItem(savedText.ItemKey)
	if beforeText.CacheStandardVersion != 2 || len(beforeText.Links) != 0 {
		t.Fatalf("Weibo Tier 0 must NOT write before postInspect, got std=%d links=%+v", beforeText.CacheStandardVersion, beforeText.Links)
	}
	decText.postInspect()
	afterText, _, _ := b.lib.GetItem(savedText.ItemKey)
	if afterText.CacheStandardVersion != library.WeiboCacheStandardVersion || len(afterText.Links) != 1 {
		t.Fatalf("Weibo Tier 0 postInspect did not stamp v4 + structured Links, got std=%d links=%+v",
			afterText.CacheStandardVersion, afterText.Links)
	}

	// 2. EditCount > 0 without EditHistory at v3 -> triggers inspectEnqueue for DEC-094 EditHistory backfill!
	editedLocal := afterText
	editedLocal.CacheStandardVersion = library.WeiboNoWMStandardVersion
	editedLocal.EditCount = 2
	remoteEdited := remoteText
	remoteEdited.EditCount = 2
	if decEdit := b.inspectWeiboCachedPostDeferred(remoteEdited, editedLocal, QueueOriginFullCache); decEdit.action != inspectEnqueue {
		t.Fatalf("Weibo v3 EditCount>0 missing EditHistory must trigger inspectEnqueue, got %+v", decEdit)
	}

	// 3. MediaIncomplete=true must NOT raise false ConflictReasonMedia!
	imgLocal := seedWeiboStaticItemForBackfill(t, b, "100001", "5096100000000002", "001picA",
		makeServiceTestJPEG(t, 24, 24, color.RGBA{100, 100, 100, 255}), library.WeiboCacheStandardVersion, nil, "完整正文内容第一句和第二句", nil, nil)
	remoteIncomplete := weibo.Post{
		Mid: "5096100000000002", Author: weibo.AuthorSummary{UID: "100001", ScreenName: "合成博主"},
		Text:            "完整正文内容第一句和第二句",
		MediaIncomplete: true,
		Images:          nil, // list omitted >9 pics / incomplete media
	}
	if decInc := b.inspectWeiboCachedPostDeferred(remoteIncomplete, imgLocal, QueueOriginFullCache); decInc.action == inspectConflict {
		t.Fatalf("MediaIncomplete=true must NOT raise false media conflict, got %+v", decInc)
	}

	// 4. Expanded long-text truncation markers ("...全文", "展开全文") must NOT raise ConflictReasonText and should enqueue when local is truncated!
	truncLocal := seedWeiboStaticItemForBackfill(t, b, "100001", "5096100000000003", "", nil, 2, nil, "长微博开头片段...全文", nil, nil)
	remoteFull := weibo.Post{
		Mid: "5096100000000003", Author: weibo.AuthorSummary{UID: "100001", ScreenName: "合成博主"},
		Text: "长微博开头片段以及后续非常完整的全文正文内容",
	}
	if decLong := b.inspectWeiboCachedPostDeferred(remoteFull, truncLocal, QueueOriginFullCache); decLong.action != inspectEnqueue {
		t.Fatalf("local truncated '...全文' with full remote must trigger inspectEnqueue (not conflict), got %+v", decLong)
	}
}

func TestDEC096XV3InspectionArticleMetadataNoteTweetSelfThreadAndPostInspect(t *testing.T) {
	loc := testLocations(t)
	b, err := NewBackend(Options{Locations: loc, Desktop: NewMockDesktop(), DisableBgWorkers: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()

	// 1. DEC-093 CardKindArticle -> ItemArticle upgrade trigger at stdVer < 3.
	artDir := filepath.Join(b.lib.CacheDir(), "x", "9001", "posts", "1909600000000000001")
	if err := os.MkdirAll(artDir, 0o700); err != nil {
		t.Fatal(err)
	}
	savedArt, err := b.lib.SaveItem(artDir, library.ItemMetadata{
		Version: 1, ID: "dynamic:1909600000000000001", Site: "x", Kind: "dynamic", Status: "complete",
		Title: "旧文章卡片", Text: "https://t.co/articleStub", CacheStandardVersion: 2,
		Author: library.ItemAuthor{Site: "x", UID: "9001", UniqueID: "xauthor", Name: "XAuthor"},
		Card:   &library.ItemRichCard{Kind: "article", Title: "旧文章卡片"},
	})
	if err != nil {
		t.Fatal(err)
	}
	remoteArt := x.Post{
		ID: "1909600000000000001", Author: x.AuthorSummary{RestID: "9001", ScreenName: "xauthor", Name: "XAuthor"},
		Text: "https://t.co/articleStub",
		Card: &x.RichCard{Kind: x.CardKindArticle, Title: "旧文章卡片"},
	}
	if decArt := b.inspectXCachedPostDeferred(remoteArt, savedArt, QueueOriginFullCache); decArt.action != inspectEnqueue {
		t.Fatalf("CardKindArticle stub < v3 must trigger inspectEnqueue for ItemArticle upgrade, got %+v", decArt)
	}

	// 2. DEC-091 Tier 0 metadata backfill (InReplyTo, Origin, TypeLabel "转发"->"引用帖子", Links, v3) runs lock-free via postInspect.
	quoteDir := filepath.Join(b.lib.CacheDir(), "x", "9001", "forwards", "1909600000000000002")
	if err := os.MkdirAll(quoteDir, 0o700); err != nil {
		t.Fatal(err)
	}
	savedQuote, err := b.lib.SaveItem(quoteDir, library.ItemMetadata{
		Version: 1, ID: "dynamic:1909600000000000002", Site: "x", Kind: "dynamic", Status: "complete",
		Title: "引用一条推文 example.com/post", Text: "引用一条推文 example.com/post", TypeLabel: "转发", CacheStandardVersion: 2,
		Author: library.ItemAuthor{Site: "x", UID: "9001", UniqueID: "xauthor", Name: "XAuthor"},
		Via:    &library.ItemVia{Reason: library.ViaForward, Site: "x", UID: "9001", Name: "XAuthor"},
	})
	if err != nil {
		t.Fatal(err)
	}
	remoteQuote := x.Post{
		ID: "1909600000000000002", Author: x.AuthorSummary{RestID: "9001", ScreenName: "xauthor", Name: "XAuthor"},
		Text:                "引用一条推文 example.com/post",
		IsQuote:             true,
		InReplyToStatusID:   "1909600000000000000",
		InReplyToUserID:     "8001",
		InReplyToScreenName: "parentuser",
		URLs:                []x.URLEntity{{ShortURL: "https://t.co/synthPost", ExpandedURL: "https://example.com/post", DisplayURL: "example.com/post"}},
		Quoted: &x.Post{
			ID: "1909600000000000099", Author: x.AuthorSummary{RestID: "8002", ScreenName: "origuser", Name: "OrigUser"},
			Text: "原帖正文",
		},
	}
	decQuote := b.inspectXCachedPostDeferred(remoteQuote, savedQuote, QueueOriginFullCache)
	if decQuote.action != inspectSkipNoOp || decQuote.postInspect == nil {
		t.Fatalf("DEC-091 quote metadata backfill must return inspectSkipNoOp + postInspect, got %+v", decQuote)
	}
	beforeQ, _, _ := b.lib.GetItem(savedQuote.ItemKey)
	if beforeQ.CacheStandardVersion != 2 || beforeQ.InReplyTo != nil || beforeQ.TypeLabel != "转发" {
		t.Fatalf("X Tier 0 must NOT write before postInspect, got std=%d reply=%+v type=%q",
			beforeQ.CacheStandardVersion, beforeQ.InReplyTo, beforeQ.TypeLabel)
	}
	decQuote.postInspect()
	afterQ, _, _ := b.lib.GetItem(savedQuote.ItemKey)
	if afterQ.CacheStandardVersion != library.XCacheStandardVersion ||
		afterQ.InReplyTo == nil || afterQ.InReplyTo.ScreenName != "parentuser" ||
		afterQ.Origin == nil || afterQ.Origin.ID != "1909600000000000099" ||
		afterQ.TypeLabel != "引用帖子" || len(afterQ.Links) != 1 {
		t.Fatalf("X Tier 0 postInspect incomplete: %+v", afterQ)
	}

	// 3. note_tweet truncated prefix expansion must NOT raise ConflictReasonText!
	noteLocal := savedArt
	noteLocal.ItemKey = "x:dynamic:1909600000000000003"
	noteLocal.Card = nil
	noteLocal.Text = "这是一段很长的推文开头被时间线截断了..."
	noteLocal.CacheStandardVersion = library.XCacheStandardVersion
	remoteNoteFull := x.Post{
		ID: "1909600000000000003", Author: x.AuthorSummary{RestID: "9001", ScreenName: "xauthor"},
		Text: "这是一段很长的推文开头被时间线截断了，但是 note_tweet 返回了完整正文！",
	}
	if decNT := b.inspectXCachedPostDeferred(remoteNoteFull, noteLocal, QueueOriginFullCache); decNT.action != inspectEnqueue {
		t.Fatalf("note_tweet prefix expansion must trigger inspectEnqueue (not conflict), got %+v", decNT)
	}

	// 4. SelfThreadID dead-code fix: single tweet with SelfThreadID != "" at stdVer < 3 triggers inspectEnqueue!
	selfLocal := afterQ
	selfLocal.ItemKey = "x:dynamic:1909600000000000004"
	selfLocal.CacheStandardVersion = 2
	selfLocal.Thread = nil
	remoteSelf := x.Post{
		ID: "1909600000000000004", Author: x.AuthorSummary{RestID: "9001", ScreenName: "xauthor"},
		Text: "引用一条推文 example.com/post", SelfThreadID: "1909600000000000004",
	}
	if decSelf := b.inspectXCachedPostDeferred(remoteSelf, selfLocal, QueueOriginFullCache); decSelf.action != inspectEnqueue {
		t.Fatalf("SelfThreadID != '' at stdVer < 3 must trigger inspectEnqueue, got %+v", decSelf)
	}
}

func TestDEC096CrossSiteFirstPageCacheMissCachedForwardsAndStartBackfill(t *testing.T) {
	nowStart := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	clock := newFakeQueueClock(nowStart)

	var (
		biliPlayURLCalls atomic.Int32
		biliV2Calls      atomic.Int32
		wbEditHistCalls  atomic.Int32
	)

	// Synthetic Bilibili server (for StartBackfill historical unsubscribed v2 video -> chapters + v3).
	biliSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/x/web-interface/nav":
			_, _ = fmt.Fprint(w, `{"code":0,"data":{"isLogin":true,"mid":778899,"uname":"测试用户","wbi_img":{"img_url":"https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png","sub_url":"https://i0.hdslb.com/bfs/wbi/4932caff0ff746eab6f01bf08b70ac45.png"}}}`)
		case "/x/web-interface/wbi/view", "/x/web-interface/view":
			bvid := r.URL.Query().Get("bvid")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"bvid": bvid, "aid": 999, "title": "未订阅旧视频", "desc": "简介", "pubdate": nowStart.Add(-40 * 24 * time.Hour).Unix(), "duration": 60,
					"owner": map[string]any{"mid": 8888, "name": "未订阅UP"},
					"pages": []map[string]any{{"cid": 88801, "page": 1, "part": "P1", "duration": 60}},
				},
			})
		case "/x/player/wbi/playurl":
			biliPlayURLCalls.Add(1)
			_, _ = fmt.Fprint(w, `{"code":-404}`)
		case "/x/player/wbi/v2":
			biliV2Calls.Add(1)
			cidVal, _ := strconv.ParseInt(r.URL.Query().Get("cid"), 10, 64)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"bvid":        r.URL.Query().Get("bvid"),
					"cid":         cidVal,
					"subtitle":    map[string]any{"subtitles": []any{}},
					"view_points": []map[string]any{{"type": 2, "from": 0, "to": 60, "content": "历史视频章节"}},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer biliSrv.Close()

	// Synthetic Weibo server (for StartBackfill historical unsubscribed forward post missing EditHistory).
	wbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/ajax/setting/getBasicInfo":
			_, _ = fmt.Fprint(w, `{"ok":1,"data":{"screen_name":"合成登录用户"}}`)
		case "/ajax/profile/info":
			_, _ = fmt.Fprint(w, `{"ok":1,"data":{"user":{"idstr":"777001","screen_name":"合成登录用户","statuses_count":5}}}`)
		case "/ajax/statuses/show":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":         1,
				"idstr":      "5096200000000001",
				"mblogid":    "OtestFwd1",
				"text_raw":   "历史转发已编辑微博",
				"created_at": "Mon Jan 01 12:00:00 +0800 2024",
				"edit_count": 1,
				"user":       map[string]any{"idstr": "777001", "screen_name": "未订阅微博博主"},
			})
		case "/ajax/statuses/editHistory":
			wbEditHistCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":    1,
				"total": 1,
				"statuses": []map[string]any{
					{"idstr": "5096200000000001", "text_raw": "历史第1版原稿", "created_at": "Mon Jan 01 11:00:00 +0800 2024"},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer wbSrv.Close()

	loc := testLocations(t)
	b, err := NewBackend(Options{
		Locations:        loc,
		Desktop:          NewMockDesktop(),
		DisableBgWorkers: true,
		CacheQueueClock:  clock,
		CacheQueueRand:   func(int) int { return 0 },
		BilibiliOpts: bilibili.ClientOptions{
			Endpoints: bilibili.Endpoints{
				APIBase: biliSrv.URL, PassportBase: biliSrv.URL, WWWBase: biliSrv.URL,
				SpaceBase: biliSrv.URL, DynamicBase: biliSrv.URL, LiveAPIBase: biliSrv.URL, LiveWebBase: biliSrv.URL,
			},
			PacingInterval: -1, AllowLocalhost: true,
		},
		WeiboOpts: weibo.ClientOptions{
			Endpoints: weibo.Endpoints{
				PCBasicInfo:         wbSrv.URL + "/ajax/setting/getBasicInfo",
				ProfileInfo:         wbSrv.URL + "/ajax/profile/info",
				StatusesShow:        wbSrv.URL + "/ajax/statuses/show",
				StatusesEditHistory: wbSrv.URL + "/ajax/statuses/editHistory",
			},
			MinInterval: -1, JitterMax: -1, AllowLocalhost: true,
		},
		DouyinOpts: douyin.ClientOptions{AllowLocalhost: true},
		XOpts:      x.ClientOptions{AllowLocalhost: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel()
		_ = b.Close()
	}()
	if err := b.dl.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ImportCookie(ctx, "SESSDATA=synth_bf; bili_jct=csrf_bf; DedeUserID=778899", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := b.LoginWithCookie(ctx, "weibo", "SUB=synth_wb_sub; SUBP=synth_wb_subp; XSRF-TOKEN=synth_xsrf", ""); err != nil {
		t.Fatal(err)
	}
	curSet := b.GetSettings()
	curSet.Download.SaveCover = false
	curSet.Download.SaveDanmaku = false
	curSet.Download.SaveSubtitles = false
	if _, err := b.UpdateSettings(curSet); err != nil {
		t.Fatal(err)
	}

	// 1. First-page DTO cache-miss fallback: stale WeiboPosts NOT in memory cache -> resets Cursor="" and HasMore=true!
	qMiss, err := b.EnqueueCacheQueue(ctx, CacheQueueRequest{
		Site: "weibo", List: "posts", User: "100001", UserName: "合成博主",
		WeiboPosts:     []WeiboPostView{{Mid: "5096999999999999", Text: "过期快照"}},
		Cursor:         "2:5096999999999999",
		HasMore:        false,
		CompletionNote: "旧提示",
	})
	if err != nil {
		t.Fatalf("EnqueueCacheQueue with cache-miss DTO: %v", err)
	}
	if qMiss.Cursor != "" || !qMiss.HasMore || qMiss.Note != "" {
		t.Fatalf("expected stale first-page DTO cache miss to reset Cursor='' HasMore=true Note='', got cursor=%q hasMore=%v note=%q",
			qMiss.Cursor, qMiss.HasMore, qMiss.Note)
	}
	_, _ = b.CancelCacheQueue(qMiss.ID)
	_ = b.RemoveCacheQueue(qMiss.ID)

	// 2. Scope Decision #2 (lists): uncached forwards stay uncacheable, while ALREADY-CACHED forwards populate inspectCached!
	wbFwdPost := weibo.Post{
		Mid: "5096200000000009", IsRetweet: true, Text: "转发正文",
		Author: weibo.AuthorSummary{UID: "100001", ScreenName: "转发者"},
		Retweeted: &weibo.Post{
			Mid: "5096200000000099", Text: "原微博正文",
			Author: weibo.AuthorSummary{UID: "200001", ScreenName: "原作者"},
		},
	}
	b.weiboPostCache.put(&wbFwdPost, b.preferredWeiboCodec(), b.currentWeiboMaxResolution(), b.weiboRemoveWatermarkEnabled())
	candWB, hitWB := b.weiboPostViewQueueCandidateWithHit(WeiboPostView{Mid: wbFwdPost.Mid, IsRetweet: true, Text: "转发正文"})
	if !hitWB || !candWB.uncacheable || candWB.inspectCached == nil {
		t.Fatalf("Weibo forward candidate must have uncacheable=true (for uncached) AND inspectCached!=nil (for cached), got hit=%v uncacheable=%v inspect=%v",
			hitWB, candWB.uncacheable, candWB.inspectCached != nil)
	}
	xFwdPost := x.Post{
		ID: "1909620000000000009", IsRetweet: true, Text: "RT 原帖",
		Author: x.AuthorSummary{RestID: "9001", ScreenName: "rtuser"},
		Retweeted: &x.Post{
			ID: "1909620000000000099", Text: "原帖正文",
			Author: x.AuthorSummary{RestID: "9002", ScreenName: "origuser"},
		},
	}
	b.xPostCache.put(&xFwdPost, false)
	candX, hitX := b.xPostDTOQueueCandidateWithHit(XPostDTO{ID: xFwdPost.ID, IsRetweet: true, Text: "RT 原帖"})
	if !hitX || !candX.uncacheable || candX.inspectCached == nil {
		t.Fatalf("X retweet candidate must have uncacheable=true AND inspectCached!=nil, got hit=%v uncacheable=%v inspect=%v",
			hitX, candX.uncacheable, candX.inspectCached != nil)
	}

	// 3. Scope Decision #2 (StartBackfill): covers historical unsubscribed Bilibili video (< v3) + historical unsubscribed Weibo forward (< v4, EditCount=1)!
	biliDir := filepath.Join(b.lib.CacheDir(), "bilibili", "8888", "videos", "BV1hist000001")
	bmf := writeVerifiedLocalFile(t, biliDir, "P01.mp4", []byte("UNSUB_BILI_MP4"), "video", 1920, 1080, 60)
	if _, err := b.lib.SaveItem(biliDir, library.ItemMetadata{
		Version: 1, ID: "video:BV1hist000001", Site: "bilibili", Kind: "video", Status: "complete",
		Title: "未订阅旧视频", Desc: "简介", PubTs: nowStart.Add(-40 * 24 * time.Hour).Unix(), Duration: 60,
		CacheStandardVersion: 2,
		Author:               library.ItemAuthor{Site: "bilibili", Mid: 8888, UID: "8888", Name: "未订阅UP"},
		Media:                []library.MediaFile{bmf},
		Parts: []library.VideoPartRecord{{
			CID: 88801, Page: 1, Title: "P1", Duration: 60, File: "P01.mp4",
			DanmakuState: "skipped", SubtitleState: "skipped",
			Video:     library.PartVideoInfo{Quality: 80},
			Audio:     library.PartAudioInfo{Quality: 30280},
			Available: library.PartAvailableInfo{BestVideo: 80, BestAudio: 30280, ProbedAt: nowStart.Add(-20 * 24 * time.Hour).Format(time.RFC3339)},
		}},
	}); err != nil {
		t.Fatal(err)
	}

	fwdVia := &library.ItemVia{Reason: library.ViaForward, Site: "weibo", UID: "777001", Name: "转发订阅者"}
	wbFwdItem := seedWeiboStaticItemForBackfill(t, b, "777001", "5096200000000001", "", nil, library.WeiboNoWMStandardVersion, fwdVia, "历史转发已编辑微博", nil, nil)
	wbFwdItem.EditCount = 1
	if _, err := b.lib.SaveItem(wbFwdItem.ItemDir, wbFwdItem); err != nil {
		t.Fatal(err)
	}

	summary, err := b.BackfillSummary()
	if err != nil {
		t.Fatalf("BackfillSummary: %v", err)
	}
	if summary.BilibiliItems != 1 || summary.WeiboMissingEditHistory != 1 || summary.TotalItems != 2 {
		t.Fatalf("BackfillSummary mismatch: %+v", summary)
	}

	b.queue.Start(ctx)
	startedTasks, err := b.StartBackfill(ctx)
	if err != nil {
		t.Fatalf("StartBackfill: %v", err)
	}
	if len(startedTasks) != 2 {
		t.Fatalf("expected 2 backfill tasks (weibo + bilibili), got %+v", startedTasks)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		allDone := true
		for _, tk := range startedTasks {
			cur := b.ListCacheQueue()
			for _, qt := range cur {
				if qt.ID == tk.ID && qt.Status != CacheQueueCompleted {
					allDone = false
					if qt.Status == CacheQueueStopped || qt.Status == CacheQueuePaused {
						t.Fatalf("backfill task %s (%s) stopped unexpectedly: %+v", qt.ID, qt.Site, qt)
					}
				}
			}
		}
		if allDone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for StartBackfill tasks; current=%+v", b.ListCacheQueue())
		}
		if _, ok := clock.AdvanceToNextTimer(); !ok {
			time.Sleep(20 * time.Millisecond)
		}
	}

	if biliPlayURLCalls.Load() != 0 || biliV2Calls.Load() != 1 {
		t.Fatalf("Bilibili site_backfill expected 0 PlayURL and 1 player/wbi/v2, got playurl=%d v2=%d", biliPlayURLCalls.Load(), biliV2Calls.Load())
	}
	biliAfter, _, _ := b.GetLibraryItem("bilibili:video:BV1hist000001")
	if biliAfter.CacheStandardVersion != library.BilibiliVideoStandardVersion || len(biliAfter.Parts[0].Chapters) != 1 {
		t.Fatalf("unsubscribed historical Bilibili video not upgraded to v3 + chapters: %+v", biliAfter)
	}

	wbAfter, _, _ := b.GetLibraryItem("weibo:dynamic:5096200000000001")
	if wbEditHistCalls.Load() != 1 || wbAfter.CacheStandardVersion != library.WeiboCacheStandardVersion ||
		len(wbAfter.EditHistory) != 1 || wbAfter.Via == nil || wbAfter.Via.Reason != library.ViaForward {
		t.Fatalf("historical Weibo forward not upgraded to v4 with EditHistory & preserved Via: calls=%d meta=%+v", wbEditHistCalls.Load(), wbAfter)
	}

	// Verify cache-queue.json remains at schema version 3!
	rawQueue, err := os.ReadFile(filepath.Join(loc.DataDir, "cache-queue.json"))
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(rawQueue, &env); err != nil || env.Version != 3 {
		t.Fatalf("cache-queue.json must remain version 3, got %d (err=%v)", env.Version, err)
	}
}
