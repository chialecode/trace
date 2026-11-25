package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/site/weibo"
	"github.com/chialecode/trace/internal/site/x"
)

func TestFullPipelineConflictKeepBothArchiveMixedReuseAndUpgradeProtection(t *testing.T) {
	vBytes, err := download.BuildSyntheticVideoM4S(60, 30)
	if err != nil {
		t.Fatal(err)
	}
	aBytes, err := download.BuildSyntheticAudioM4S(94)
	if err != nil {
		t.Fatal(err)
	}

	var (
		mu          sync.Mutex
		expiredCID  = map[int64]bool{}
		remotePages = map[string][]map[string]any{
			"BV1e2eArch001": {
				{"cid": 20002, "page": 1, "part": "V2重制分P", "duration": 2},
			},
			"BV1e2eMix0002": {
				{"cid": 30001, "page": 1, "part": "保留原P1", "duration": 2},
				{"cid": 30002, "page": 2, "part": "新增P2", "duration": 2},
			},
			"BV1e2eUpg0003": {
				{"cid": 40001, "page": 1, "part": "同源升画质P1", "duration": 2},
			},
		}
	)

	var biliSrv *httptest.Server
	biliSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/m4s/video.m4s":
			w.Header().Set("Content-Type", "video/mp4")
			http.ServeContent(w, r, "video.m4s", time.Time{}, bytes.NewReader(vBytes))
			return
		case "/m4s/audio.m4s":
			w.Header().Set("Content-Type", "audio/mp4")
			http.ServeContent(w, r, "audio.m4s", time.Time{}, bytes.NewReader(aBytes))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/x/web-interface/nav":
			_, _ = fmt.Fprint(w, `{"code":0,"data":{"isLogin":true,"mid":778899,"uname":"测试用户","wbi_img":{"img_url":"https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png","sub_url":"https://i0.hdslb.com/bfs/wbi/4932caff0ff746eab6f01bf08b70ac45.png"}}}`)
		case "/x/web-interface/wbi/view", "/x/web-interface/view":
			bvid := r.URL.Query().Get("bvid")
			mu.Lock()
			pages := remotePages[bvid]
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"bvid":     bvid,
					"aid":      100,
					"title":    "新版标题_" + bvid,
					"desc":     "新版简介",
					"pubdate":  1763800000,
					"duration": 2 * len(pages),
					"owner":    map[string]any{"mid": 2002, "name": "创作者"},
					"pages":    pages,
				},
			})
		case "/x/player/wbi/playurl":
			var cid int64
			_, _ = fmt.Sscanf(r.URL.Query().Get("cid"), "%d", &cid)
			mu.Lock()
			expired := expiredCID[cid]
			mu.Unlock()
			if expired {
				_, _ = fmt.Fprint(w, `{"code":-404,"message":"过期不可用"}`)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"quality":        80,
					"accept_format":  "hdflv2,flv,mp4",
					"accept_quality": []int{80, 64},
					"dash": map[string]any{
						"duration": 2,
						"video": []map[string]any{
							{"id": 80, "baseUrl": biliSrv.URL + "/m4s/video.m4s", "bandwidth": 1500000, "mimeType": "video/mp4", "codecs": "avc1.64001F", "width": 1920, "height": 1080, "frameRate": "30", "codecid": 7},
						},
						"audio": []map[string]any{
							{"id": 30280, "baseUrl": biliSrv.URL + "/m4s/audio.m4s", "bandwidth": 192000, "mimeType": "audio/mp4", "codecs": "mp4a.40.2"},
						},
					},
				},
			})
		case "/x/player/wbi/v2":
			_, _ = fmt.Fprint(w, `{"code":0,"data":{"subtitle":{"subtitles":[]}}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer biliSrv.Close()

	loc := testLocations(t)
	b, err := NewBackend(Options{
		Locations:        loc,
		Desktop:          NewMockDesktop(),
		DisableBgWorkers: true,
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
	if _, err := b.ImportCookie(ctx, "SESSDATA=synth_e2e; bili_jct=csrf_e2e; DedeUserID=778899", ""); err != nil {
		t.Fatal(err)
	}
	curSet := b.GetSettings()
	curSet.Download.SaveCover = false
	curSet.Download.SaveSubtitles = false
	curSet.Download.SaveDanmaku = true
	curSet.Download.MultiPart = "all"
	if _, err := b.UpdateSettings(curSet); err != nil {
		t.Fatal(err)
	}

	// 1) Manual re-cache tripwire -> conflict recorded -> keep_both archives V1 & commits V2 -> independent delete!
	dir1 := filepath.Join(b.lib.CacheDir(), "bilibili", "2002", "videos", "BV1e2eArch001")
	v1Payload := []byte("E2E_LOCAL_V1_PRECIOUS_VIDEO")
	mf1 := writeVerifiedLocalFile(t, dir1, "P01.mp4", v1Payload, "video", 1280, 720, 2)
	if _, err := b.lib.SaveItem(dir1, library.ItemMetadata{
		Version:      1,
		ID:           "video:BV1e2eArch001",
		Site:         "bilibili",
		Kind:         "video",
		Status:       "complete",
		Title:        "旧版标题V1",
		PubTs:        1763700000,
		Duration:     2,
		DirectCached: true,
		Author:       library.ItemAuthor{Site: "bilibili", Mid: 2002, UID: "2002", Name: "创作者"},
		Media:        []library.MediaFile{mf1},
		Parts: []library.VideoPartRecord{{
			CID: 10001, Page: 1, Title: "旧P1", Duration: 2, File: "P01.mp4",
			Video: library.PartVideoInfo{Quality: 64, QualityName: "720P 高清"},
			Audio: library.PartAudioInfo{Quality: 30280, Name: "192K 高音质"},
		}},
	}); err != nil {
		t.Fatal(err)
	}

	tasks, err := b.EnqueueVideoCache(ctx, download.VideoEnqueueRequest{BVID: "BV1e2eArch001", Source: "manual"})
	if err != nil {
		t.Fatalf("EnqueueVideoCache: %v", err)
	}
	waitErr := b.dl.WaitTasks(ctx, tasks[0].TaskID)
	if !errors.Is(waitErr, download.ErrConflictDetected) {
		t.Fatalf("expected ErrConflictDetected from manual re-cache tripwire, got %v", waitErr)
	}
	if gotV1, _ := os.ReadFile(filepath.Join(dir1, "P01.mp4")); string(gotV1) != string(v1Payload) {
		t.Fatalf("tripwire failed to protect V1 file")
	}

	conflicts, err := b.ListCacheConflicts()
	if err != nil || len(conflicts) != 1 || conflicts[0].ItemKey != "bilibili:video:BV1e2eArch001" {
		t.Fatalf("expected tripwire conflict in cache-conflicts.json, got %+v (err=%v)", conflicts, err)
	}
	if err := b.ResolveCacheConflict(ctx, conflicts[0].ID, ConflictKeepBoth); err != nil {
		t.Fatalf("ResolveCacheConflict(keep_both): %v", err)
	}

	// Verify V2 live item AND independent archived V1 item!
	allItems, err := b.ListLibraryItems(library.FeedFilter{Site: "bilibili", CachedOnly: true, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	var archivedKey string
	for _, it := range allItems {
		if it.ArchivedOf == "bilibili:video:BV1e2eArch001" {
			archivedKey = it.ItemKey
			archBytes, rErr := os.ReadFile(filepath.Join(it.ItemDir, "P01.mp4"))
			if rErr != nil || string(archBytes) != string(v1Payload) {
				t.Fatalf("archived V1 file corrupted: err=%v bytes=%q", rErr, string(archBytes))
			}
		}
	}
	if archivedKey == "" {
		t.Fatalf("expected archived V1 snapshot item with ArchivedOf=bilibili:video:BV1e2eArch001, items=%+v", allItems)
	}
	counts, err := b.lib.CountCachedWorks([]library.AuthorRef{{Site: "bilibili", UID: "2002"}})
	if err != nil {
		t.Fatal(err)
	}
	if counts["bilibili:2002"] != 1 {
		t.Fatalf("CountCachedWorks double-counted archived V1: got %d, want 1", counts["bilibili:2002"])
	}

	// Deleting V2 MUST NOT delete archived V1!
	if err := b.Delete("bilibili:video:BV1e2eArch001"); err != nil {
		t.Fatalf("Delete V2: %v", err)
	}
	archAfterDel, ok, err := b.GetLibraryItem(archivedKey)
	if err != nil || !ok {
		t.Fatalf("archived V1 disappeared after deleting V2: ok=%v err=%v", ok, err)
	}
	if archBytes, rErr := os.ReadFile(filepath.Join(archAfterDel.ItemDir, "P01.mp4")); rErr != nil || string(archBytes) != string(v1Payload) {
		t.Fatalf("archived V1 file lost after deleting V2: err=%v", rErr)
	}

	// 2) Mixed V2 edit (P1 CID 30001 unchanged & expired remotely, P2 CID 30002 added): use_remote reuses verified local P1!
	dir2 := filepath.Join(b.lib.CacheDir(), "bilibili", "2002", "videos", "BV1e2eMix0002")
	mixP1Bytes := []byte("MIXED_EDIT_VERIFIED_LOCAL_P1")
	mfMix := writeVerifiedLocalFile(t, dir2, "P01.mp4", mixP1Bytes, "video", 1920, 1080, 2)
	if _, err := b.lib.SaveItem(dir2, library.ItemMetadata{
		Version: 1, ID: "video:BV1e2eMix0002", Site: "bilibili", Kind: "video", Status: "complete",
		Title: "混合编辑V1", PubTs: 1763700000, Duration: 2, DirectCached: true,
		Author: library.ItemAuthor{Site: "bilibili", Mid: 2002, UID: "2002", Name: "创作者"},
		Media:  []library.MediaFile{mfMix},
		Parts: []library.VideoPartRecord{
			{CID: 30001, Page: 1, Title: "原P1", Duration: 2, File: "P01.mp4", Video: library.PartVideoInfo{Quality: 80, QualityName: "1080P 高清"}, Audio: library.PartAudioInfo{Quality: 30280, Name: "192K 高音质"}},
			{CID: 39999, Page: 2, Title: "被删旧P2", Duration: 2, File: "P01.mp4", Video: library.PartVideoInfo{Quality: 80, QualityName: "1080P 高清"}, Audio: library.PartAudioInfo{Quality: 30280, Name: "192K 高音质"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	b.recordDownloadConflictNotice(download.ConflictNotice{
		ItemKey:           "bilibili:video:BV1e2eMix0002",
		Site:              "bilibili",
		Kind:              "video",
		TargetID:          "BV1e2eMix0002",
		ReasonKind:        ConflictReasonParts,
		LocalSummary:      "2 个分P",
		RemoteSummary:     "P2 换源",
		RemoteFingerprint: "fp_mix_v2",
	})
	mu.Lock()
	expiredCID[30001] = true // P1 expired on CDN, must reuse local verified P01.mp4!
	mu.Unlock()
	mixConflicts, _ := b.ListCacheConflicts()
	if len(mixConflicts) != 1 {
		t.Fatalf("expected 1 conflict for BV1e2eMix0002, got %+v", mixConflicts)
	}
	if err := b.ResolveCacheConflict(ctx, mixConflicts[0].ID, ConflictUseRemote); err != nil {
		t.Fatalf("ResolveCacheConflict(use_remote) on mixed edit with expired P1: %v", err)
	}
	reusedP1, err := os.ReadFile(filepath.Join(dir2, "P01.mp4"))
	if err != nil || string(reusedP1) != string(mixP1Bytes) {
		t.Fatalf("expected unchanged P1 to reuse verified local bytes, err=%v", err)
	}
	if st, err := os.Stat(filepath.Join(dir2, "P02.mp4")); err != nil || st.Size() <= 0 {
		t.Fatalf("expected new P02.mp4 to be downloaded alongside reused P01.mp4: err=%v", err)
	}

	// 3) Same-CID upgrade via queue preserves existing verified P01.danmaku.xml monotonically!
	dir3 := filepath.Join(b.lib.CacheDir(), "bilibili", "2002", "videos", "BV1e2eUpg0003")
	danmakuSnap := []byte("<?xml version=\"1.0\" encoding=\"UTF-8\"?><i><d p=\"1,1,25,16777215,1,0,0,1\">珍贵早期弹幕快照</d></i>")
	mfVid3 := writeVerifiedLocalFile(t, dir3, "P01.mp4", []byte("OLD_720P_STREAM"), "video", 1280, 720, 2)
	mfDan3 := writeVerifiedLocalFile(t, dir3, "P01.danmaku.xml", danmakuSnap, "danmaku", 0, 0, 0)
	if _, err := b.lib.SaveItem(dir3, library.ItemMetadata{
		Version: 1, ID: "video:BV1e2eUpg0003", Site: "bilibili", Kind: "video", Status: "complete",
		Title: "同源升级保留弹幕", PubTs: 1763700000, Duration: 2, DirectCached: true,
		Author: library.ItemAuthor{Site: "bilibili", Mid: 2002, UID: "2002", Name: "创作者"},
		Media:  []library.MediaFile{mfVid3, mfDan3},
		Parts: []library.VideoPartRecord{{
			CID: 40001, Page: 1, Title: "同源升画质P1", Duration: 2, File: "P01.mp4",
			Danmaku: "P01.danmaku.xml", DanmakuState: "saved",
			Video: library.PartVideoInfo{Quality: 64, QualityName: "720P 高清"},
			Audio: library.PartAudioInfo{Quality: 30280, Name: "192K 高音质"},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	upgTask, err := b.dl.UpgradeWithSource(ctx, "bilibili:video:BV1e2eUpg0003", "queue")
	if err != nil {
		t.Fatalf("UpgradeWithSource(queue): %v", err)
	}
	if err := b.dl.WaitTasks(ctx, upgTask.TaskID); err != nil {
		t.Fatalf("WaitTasks(UpgradeWithSource): %v", err)
	}
	afterDanmaku, err := os.ReadFile(filepath.Join(dir3, "P01.danmaku.xml"))
	if err != nil || !strings.Contains(string(afterDanmaku), "珍贵早期弹幕快照") {
		t.Fatalf("monotonic danmaku snapshot lost during queue upgrade: err=%v got=%q", err, string(afterDanmaku))
	}
}

func TestCrossLayerFingerprintKeepLocalSuppressionLongTextEnrichmentAndQueueSupplementQuota(t *testing.T) {
	nowStart := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	clock := newFakeQueueClock(nowStart)

	var (
		mu      sync.Mutex
		curCID  int64 = 50002 // V2 CID (local has V1 CID 50001)
		biliSrv *httptest.Server
	)
	biliSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/x/web-interface/nav":
			_, _ = fmt.Fprint(w, `{"code":0,"data":{"isLogin":true,"mid":778899,"uname":"测试用户","wbi_img":{"img_url":"https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png","sub_url":"https://i0.hdslb.com/bfs/wbi/4932caff0ff746eab6f01bf08b70ac45.png"}}}`)
		case "/1001":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = fmt.Fprint(w, `<html><script id="__RENDER_DATA__" type="application/json">%7B%22access_id%22%3A%22synth_webid%22%7D</script></html>`)
		case "/x/space/wbi/arc/search":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"page": map[string]any{"pn": 1, "ps": 30, "count": 1},
					"list": map[string]any{
						"vlist": []map[string]any{
							{"bvid": "BV1fpSync0001", "aid": 51, "title": "跨层指纹一致性V2", "description": "简介V2", "length": "00:02", "created": nowStart.Add(-20 * 24 * time.Hour).Unix(), "mid": 1001, "author": "UP"},
						},
					},
				},
			})
		case "/x/web-interface/wbi/view", "/x/web-interface/view":
			if r.URL.Query().Get("bvid") == "BV1supQuota01" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code": 0,
					"data": map[string]any{
						"bvid": "BV1supQuota01", "aid": 52, "title": "补弹幕额度", "desc": "",
						"pubdate": nowStart.Add(-10 * 24 * time.Hour).Unix(), "duration": 2,
						"owner": map[string]any{"mid": 1001, "name": "UP"},
						"pages": []map[string]any{
							{"cid": 60001, "page": 1, "part": "P1", "duration": 2},
						},
					},
				})
				return
			}
			mu.Lock()
			cid := curCID
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"bvid": "BV1fpSync0001", "aid": 51, "title": "跨层指纹一致性V2", "desc": "简介V2",
					"pubdate": nowStart.Add(-20 * 24 * time.Hour).Unix(), "duration": 2,
					"owner": map[string]any{"mid": 1001, "name": "UP"},
					"pages": []map[string]any{
						{"cid": cid, "page": 1, "part": "P1", "duration": 2},
					},
				},
			})
		case "/x/v2/dm/web/seg.so":
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(bilibili.EncodeDmSegMobileReply([]bilibili.DanmakuElem{
				{ID: 1, IDStr: "1", Progress: 500, Mode: 1, Fontsize: 25, Color: 0xFFFFFF, Content: "补齐弹幕"},
			}))
		case "/x/player/wbi/v2":
			_, _ = fmt.Fprint(w, `{"code":0,"data":{"subtitle":{"subtitles":[]}}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer biliSrv.Close()

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
	if _, err := b.ImportCookie(ctx, "SESSDATA=synth_fp; bili_jct=csrf_fp; DedeUserID=778899", ""); err != nil {
		t.Fatal(err)
	}

	// 1) Download tripwire V2 -> keep_local -> 7-day fullCache V2 MUST stay suppressed; V3 MUST reopen!
	dir := filepath.Join(b.lib.CacheDir(), "bilibili", "1001", "videos", "BV1fpSync0001")
	mf := writeVerifiedLocalFile(t, dir, "P01.mp4", []byte("V1_FP_SYNC"), "video", 1920, 1080, 2)
	if _, err := b.lib.SaveItem(dir, library.ItemMetadata{
		Version: 1, ID: "video:BV1fpSync0001", Site: "bilibili", Kind: "video", Status: "complete",
		Title: "旧标题V1", Desc: "旧简介", PubTs: nowStart.Add(-20 * 24 * time.Hour).Unix(), Duration: 2,
		CacheStandardVersion: library.CurrentCacheStandardVersion,
		Author:               library.ItemAuthor{Site: "bilibili", Mid: 1001, UID: "1001", Name: "UP"},
		Media:                []library.MediaFile{mf},
		Parts: []library.VideoPartRecord{{
			CID: 50001, Page: 1, Title: "P1", Duration: 2, File: "P01.mp4",
			Video: library.PartVideoInfo{Quality: 80, QualityName: "1080P 高清"},
			Audio: library.PartAudioInfo{Quality: 30280, Name: "192K 高音质"},
		}},
	}); err != nil {
		t.Fatal(err)
	}

	tasks, err := b.EnqueueVideoCache(ctx, download.VideoEnqueueRequest{BVID: "BV1fpSync0001", Source: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if wErr := b.dl.WaitTasks(ctx, tasks[0].TaskID); !errors.Is(wErr, download.ErrConflictDetected) {
		t.Fatalf("expected ErrConflictDetected from tripwire, got %v", wErr)
	}
	list1, _ := b.ListCacheConflicts()
	if len(list1) != 1 {
		t.Fatalf("expected 1 conflict after tripwire, got %+v", list1)
	}
	if err := b.ResolveCacheConflict(ctx, list1[0].ID, ConflictKeepLocal); err != nil {
		t.Fatalf("ResolveCacheConflict(keep_local): %v", err)
	}

	// Run fullCache sweep over unchanged V2: Gate A triggers probeBilibiliCachedVideo, which MUST match ignoredRevisions!
	b.queue.Start(ctx)
	qTask, err := b.EnqueueCacheQueue(ctx, CacheQueueRequest{Site: "bilibili", List: "videos", User: "1001", Origin: QueueOriginFullCache})
	if err != nil {
		t.Fatal(err)
	}
	waitQueuePredicate(t, b, qTask.ID, "fullCache V2 suppressed", func(qt CacheQueueTask) bool {
		return qt.Status == CacheQueueCompleted
	})
	if afterV2, _ := b.ListCacheConflicts(); len(afterV2) != 0 {
		t.Fatalf("cross-layer RemoteFingerprint mismatch falsely re-opened ignored V2 conflict: %+v", afterV2)
	}

	// Advance remote to V3 (CID 50003) and run another sweep -> conflict MUST reopen!
	_ = b.RemoveCacheQueue(qTask.ID)
	mu.Lock()
	curCID = 50003
	mu.Unlock()
	qTaskV3, err := b.EnqueueCacheQueue(ctx, CacheQueueRequest{Site: "bilibili", List: "videos", User: "1001", Origin: QueueOriginFullCache})
	if err != nil {
		t.Fatal(err)
	}
	waitQueuePredicate(t, b, qTaskV3.ID, "fullCache V3 reopened", func(qt CacheQueueTask) bool {
		return qt.Status == CacheQueueCompleted
	})
	if afterV3, _ := b.ListCacheConflicts(); len(afterV3) != 1 {
		t.Fatalf("expected V3 CID change to reopen conflict, got %+v", afterV3)
	}

	// 2) Weibo LongText safe enrichment & scrubbed-URL text comparison false-positive check.
	wbDir := filepath.Join(b.lib.CacheDir(), "weibo", "5001", "posts", "5099001")
	wbImg := writeVerifiedLocalFile(t, wbDir, "images/01.jpg", []byte("WB_IMG"), "image", 800, 600, 0)
	wbTruncMeta, err := b.lib.SaveItem(wbDir, library.ItemMetadata{
		Version: 1, ID: "dynamic:5099001", Site: "weibo", Kind: "dynamic", Status: "complete",
		Title: "微博长文截断", Text: "这是一段完整的微博开头摘要部分...展开",
		Author:    library.ItemAuthor{Site: "weibo", UID: "5001", Name: "博主"},
		Media:     []library.MediaFile{wbImg},
		AssetIDs:  []string{"005XpicLong1"},
		NotePages: []library.NotePageRecord{{Index: 1, Kind: "image", MediaID: "005XpicLong1", Image: "images/01.jpg", Width: 800, Height: 600}},
	})
	if err != nil {
		t.Fatal(err)
	}
	wbLongPost := weibo.Post{
		Mid:             "5099001",
		IsLongText:      true,
		LongTextFetched: true,
		Text:            "这是一段完整的微博开头摘要部分，后面补充了第二段与第三段完整正文。",
		Author:          weibo.AuthorSummary{UID: "5001", ScreenName: "博主"},
		Images:          []weibo.ImageAsset{{Index: 1, PicID: "005XpicLong1", Kind: weibo.MediaKindImage, URL: "https://wx1.sinaimg.cn/large/005XpicLong1.jpg"}},
	}
	if dec := b.inspectWeiboCachedPost(wbLongPost, wbTruncMeta, QueueOriginFullCache); dec.action != inspectEnqueue {
		t.Fatalf("expected truncated Weibo LongText expansion to inspectEnqueue, got action=%v conflict=%+v", dec.action, dec.conflict)
	}

	// Scrubbed media/signed URL in X & Weibo text must NOT trigger false ConflictReasonText.
	xDir := filepath.Join(b.lib.CacheDir(), "x", "9001", "posts", "1899001")
	xMeta, err := b.lib.SaveItem(xDir, library.ItemMetadata{
		Version: 1, ID: "dynamic:1899001", Site: "x", Kind: "dynamic", Status: "complete",
		Title: "X帖", Text: "Hello X post https://video.twimg.com/ext_tw_video/1/pu/vid/v.mp4?tag=12",
		CacheStandardVersion: library.CurrentCacheStandardVersion,
		Author:               library.ItemAuthor{Site: "x", UID: "9001", Name: "XUser"},
	})
	if err != nil {
		t.Fatal(err)
	}
	xSamePost := x.Post{
		ID:     "1899001",
		Text:   "Hello X post https://video.twimg.com/ext_tw_video/1/pu/vid/v.mp4?tag=12",
		Author: x.AuthorSummary{RestID: "9001", Name: "XUser", ScreenName: "xuser"},
	}
	if dec := b.inspectXCachedPost(xSamePost, xMeta, QueueOriginFullCache); dec.action != inspectSkipNoOp {
		t.Fatalf("scrubbed video.twimg.com URL triggered false X conflict/enqueue: action=%v conflict=%+v", dec.action, dec.conflict)
	}

	// 3) Queue-triggered CacheSupplementWithSource(..., "queue") sets Source="queue" and counts automatic quota.
	var autoQuotaCalls atomic.Int32
	b.bili.SetAutomaticRequestHandlers(func() error {
		autoQuotaCalls.Add(1)
		return b.subs.AutomaticRequestAllowed()
	}, b.subs.RecordAutomaticStop)

	supDir := filepath.Join(b.lib.CacheDir(), "bilibili", "1001", "videos", "BV1supQuota01")
	supVid := writeVerifiedLocalFile(t, supDir, "P01.mp4", []byte("SUP_VID"), "video", 1920, 1080, 2)
	if _, err := b.lib.SaveItem(supDir, library.ItemMetadata{
		Version: 1, ID: "video:BV1supQuota01", Site: "bilibili", Kind: "video", Status: "complete",
		Title: "补弹幕额度", PubTs: nowStart.Add(-10 * 24 * time.Hour).Unix(), Duration: 2,
		Author: library.ItemAuthor{Site: "bilibili", Mid: 1001, UID: "1001", Name: "UP"},
		Media:  []library.MediaFile{supVid},
		Parts: []library.VideoPartRecord{{
			CID: 60001, Page: 1, Title: "P1", Duration: 2, File: "P01.mp4",
			Video: library.PartVideoInfo{Quality: 80, QualityName: "1080P 高清"},
			Audio: library.PartAudioInfo{Quality: 30280, Name: "192K 高音质"},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	supTask, err := b.dl.CacheSupplementWithSource(ctx, "BV1supQuota01", "queue")
	if err != nil {
		t.Fatalf("CacheSupplementWithSource(queue): %v", err)
	}
	if supTask.Source != "queue" {
		t.Fatalf("CacheSupplementWithSource Source = %q, want queue", supTask.Source)
	}
	if err := b.dl.WaitTasks(ctx, supTask.TaskID); err != nil {
		t.Fatalf("WaitTasks(CacheSupplementWithSource): %v", err)
	}
	if autoQuotaCalls.Load() == 0 {
		t.Fatalf("expected queue supplement to invoke Bilibili automatic quota handler, got 0 calls")
	}
}
