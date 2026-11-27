package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/site/douyin"
	"github.com/chialecode/trace/internal/site/weibo"
	"github.com/chialecode/trace/internal/site/x"
	"github.com/chialecode/trace/internal/subscription"
)

// DEC-088: All 4 sites (bilibili, douyin, weibo, x) run independent cache queue
// workers (max 1 active task per site). When one site hits risk control / 418 /
// backoff, the other 3 sites continue paging and downloading completely unobstructed.
func TestCacheQueueFourSitesParallelAndCrossSiteBackoffIsolation(t *testing.T) {
	h := newQueueDualHarness(t, 1, 1, 1, 1)
	pngBytes := historyThumbnailPNG(t, 32, 32)

	// Synthetic Weibo server: valid login, triggers HTTP 418 risk control on timeline.
	var wbTimelineHits atomic.Int32
	wbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/ajax/setting/getBasicInfo":
			_, _ = fmt.Fprintf(w, `{"ok":1,"data":{"screen_name":%q,"domain":"synthself"}}`, synthWeiboSelfName)
		case "/ajax/profile/info":
			if r.URL.Query().Get("screen_name") == synthWeiboSelfName {
				_, _ = fmt.Fprintf(w, `{"ok":1,"data":{"user":{"id":5100000001,"idstr":%q,"screen_name":%q,"statuses_count":5}}}`, synthWeiboSelfUID, synthWeiboSelfName)
				return
			}
			_, _ = fmt.Fprintf(w, `{"ok":1,"data":{"user":{"id":5100000002,"idstr":%q,"screen_name":%q,"statuses_count":5}}}`, synthWeiboUID, synthWeiboName)
		case "/ajax/statuses/mymblog":
			wbTimelineHits.Add(1)
			w.WriteHeader(418)
			_, _ = fmt.Fprint(w, `{"ok":0,"msg":"risk control"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(wbSrv.Close)

	// Synthetic X server: valid login, serves 1 image tweet on UserTweets.
	homeBytes := readXSiteTestdata(t, "webpack_home.html")
	mainBytes := readXSiteTestdata(t, "webpack_main.js")
	ondemandBytes := readXSiteTestdata(t, "webpack_ondemand.js")
	var xSrv *httptest.Server
	xSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/home":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(homeBytes)
			return
		case "/responsive-web/client-web/main.synth01a.js":
			w.Header().Set("Content-Type", "application/javascript")
			_, _ = w.Write(mainBytes)
			return
		case "/responsive-web/client-web/ondemand.s.synth02a.js":
			w.Header().Set("Content-Type", "application/javascript")
			_, _ = w.Write(ondemandBytes)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/xcdn/") {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngBytes)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/i/api/1.1/account/settings.json":
			_, _ = w.Write([]byte(`{"screen_name":"` + synthXSelfScreenName + `","protected":false,"language":"en"}`))
		case strings.HasSuffix(r.URL.Path, "/"+x.OpUserByScreenName):
			_, _ = w.Write([]byte(synthXUserResultEnvelope(
				synthXSelfRestID, synthXSelfScreenName, synthXSelfName,
				xSrv.URL+"/xcdn/self.png", "Self bio", 10,
			)))
		case strings.HasSuffix(r.URL.Path, "/"+x.OpUserByRestId):
			_, _ = w.Write([]byte(synthXUserResultEnvelope(
				synthXCreatorRestID, synthXCreatorHandle, synthXCreatorName,
				xSrv.URL+"/xcdn/creator.png", "Creator bio", 10,
			)))
		case strings.HasSuffix(r.URL.Path, "/"+x.OpUserTweets):
			tweetJSON := fmt.Sprintf(`{
				"__typename":"Tweet",
				"rest_id":"1890000000000000001",
				"core":{"user_results":{"result":%s}},
				"legacy":{
					"id_str":"1890000000000000001",
					"created_at":"Mon Nov 18 10:00:00 +0000 2026",
					"full_text":"四站并行合成测试帖子",
					"extended_entities":{"media":[{
						"id_str":"7001",
						"type":"photo",
						"media_url_https":%q,
						"original_info":{"width":32,"height":32}
					}]}
				}
			}`, synthXUserCoreJSON(synthXCreatorRestID, synthXCreatorHandle, synthXCreatorName, xSrv.URL+"/xcdn/creator.png", "Creator bio", 10),
				xSrv.URL+"/xcdn/photo1.png")
			entry := synthXTweetEntry("tweet-1890000000000000001", tweetJSON)
			_, _ = w.Write([]byte(synthXUserTimelineEnvelope(entry)))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(xSrv.Close)

	fixedNow := time.Unix(1795327200, 0).UTC()
	b, err := NewBackend(Options{
		Locations:        h.loc,
		Desktop:          NewMockDesktop(),
		DisableBgWorkers: true,
		CacheQueueClock:  h.clock,
		CacheQueueRand:   func(int) int { return 0 },
		BilibiliOpts: bilibili.ClientOptions{
			Endpoints: bilibili.Endpoints{
				APIBase:      h.biliSrv.URL,
				PassportBase: h.biliSrv.URL,
				WWWBase:      h.biliSrv.URL,
				SpaceBase:    h.biliSrv.URL,
				DynamicBase:  h.biliSrv.URL,
				LiveAPIBase:  h.biliSrv.URL,
				LiveWebBase:  h.biliSrv.URL,
			},
			PacingInterval: -1,
			AllowLocalhost: true,
		},
		DouyinOpts: douyin.ClientOptions{
			Endpoints: douyin.Endpoints{
				ProfileSelf:    h.dySrv.URL + "/aweme/v1/web/user/profile/self/",
				ProfileOther:   h.dySrv.URL + "/aweme/v1/web/user/profile/other/",
				AwemeDetail:    h.dySrv.URL + "/aweme/v1/web/aweme/detail/",
				AwemePost:      h.dySrv.URL + "/aweme/v1/web/aweme/post/",
				LiveWebEnter:   h.dySrv.URL + "/webcast/room/web/enter/",
				LiveReflowInfo: h.dySrv.URL + "/webcast/room/reflow/info/",
				LiveCheckAlive: h.dySrv.URL + "/webcast/room/check_alive/",
			},
			MinInterval:    -1,
			JitterMax:      -1,
			AllowLocalhost: true,
		},
		WeiboOpts: weibo.ClientOptions{
			Endpoints: weibo.Endpoints{
				PCBasicInfo:     wbSrv.URL + "/ajax/setting/getBasicInfo",
				ProfileInfo:     wbSrv.URL + "/ajax/profile/info",
				StatusesMyMblog: wbSrv.URL + "/ajax/statuses/mymblog",
				StatusesShow:    wbSrv.URL + "/ajax/statuses/show",
			},
			MinInterval:    -1,
			JitterMax:      -1,
			AllowLocalhost: true,
		},
		XOpts: x.ClientOptions{
			Endpoints: x.Endpoints{
				HomeURL:         xSrv.URL + "/home",
				AccountSettings: xSrv.URL + "/i/api/1.1/account/settings.json",
				GraphQLBase:     xSrv.URL + "/i/api/graphql",
			},
			MinInterval:        -1,
			RateLimitJitterMax: -1,
			AllowLocalhost:     true,
			AssetTransport:     newXAbsRewriteTransport(t, xSrv),
			Now:                func() time.Time { return fixedNow },
			Sleep:              func(context.Context, time.Duration) error { return nil },
			TxIDOptions: []x.TxIDOption{
				x.WithTxIDClock(func() time.Time { return fixedNow }),
				x.WithTxIDRandByte(func() (byte, error) { return 0x42, nil }),
			},
		},
	})
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	dlCtx, dlCancel := context.WithCancel(context.Background())
	if err := b.dl.Start(dlCtx); err != nil {
		dlCancel()
		_ = b.Close()
		t.Fatalf("dl.Start: %v", err)
	}
	t.Cleanup(func() {
		dlCancel()
		_ = b.Close()
	})

	ctx := context.Background()
	if _, err := b.ImportCookie(ctx, "SESSDATA=synth_bili_4s; bili_jct=csrf_4s; DedeUserID=778899", ""); err != nil {
		t.Fatalf("login bilibili: %v", err)
	}
	if _, err := b.LoginWithCookie(ctx, "douyin", "sessionid=synth_dy_4s; sid_guard=guard_4s; ttwid=ttwid_4s", ""); err != nil {
		t.Fatalf("login douyin: %v", err)
	}
	if _, err := b.LoginWithCookie(ctx, "weibo", synthWeiboCookie(), ""); err != nil {
		t.Fatalf("login weibo: %v", err)
	}
	if _, err := b.LoginWithCookie(ctx, "x", synthXValidCookie, ""); err != nil {
		t.Fatalf("login x: %v", err)
	}

	// Block bilibili task #1 momentarily so we can verify per-site max-1 concurrency
	// while Bilibili #2 stays queued and Weibo enters waiting_backoff.
	h.blockBiliID.Store("4101000001")
	t.Cleanup(func() {
		select {
		case <-h.unblockCh:
		default:
			close(h.unblockCh)
		}
	})

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	b.queue.Start(runCtx)

	wbQ, err := b.EnqueueCacheQueue(ctx, CacheQueueRequest{Site: "weibo", List: "posts", User: synthWeiboUID, UserName: "合成微博博主"})
	if err != nil {
		t.Fatalf("enqueue weibo: %v", err)
	}
	biliQ1, err := b.EnqueueCacheQueue(ctx, CacheQueueRequest{Site: "bilibili", List: "dynamics", User: "4101", UserName: "B站一"})
	if err != nil {
		t.Fatalf("enqueue bili1: %v", err)
	}
	biliQ2, err := b.EnqueueCacheQueue(ctx, CacheQueueRequest{Site: "bilibili", List: "dynamics", User: "4102", UserName: "B站二"})
	if err != nil {
		t.Fatalf("enqueue bili2: %v", err)
	}
	dyQ, err := b.EnqueueCacheQueue(ctx, CacheQueueRequest{Site: "douyin", List: "works", User: "MS4wLjABAAAA_synth_4s_dy", UserName: "抖音一"})
	if err != nil {
		t.Fatalf("enqueue douyin: %v", err)
	}
	xQ, err := b.EnqueueCacheQueue(ctx, CacheQueueRequest{Site: "x", List: "posts", User: synthXCreatorRestID, UserName: synthXCreatorName})
	if err != nil {
		t.Fatalf("enqueue x: %v", err)
	}

	// 1. Weibo hits HTTP 418 risk control -> enters waiting_backoff on Weibo ONLY.
	waitQueuePredicate(t, b, wbQ.ID, "weibo entered waiting_backoff", func(qt CacheQueueTask) bool {
		return qt.Status == CacheQueueWaitingBackoff
	})
	if inWB, _ := b.subs.IsSiteInBackoff(string(site.Weibo)); !inWB {
		t.Fatal("expected Weibo recorded in backoff after HTTP 418")
	}
	for _, healthy := range []site.ID{site.Bilibili, site.Douyin, site.X} {
		if inB, _ := b.subs.IsSiteInBackoff(string(healthy)); inB {
			t.Fatalf("healthy site %s was polluted by Weibo backoff", healthy)
		}
	}

	// 2. While Weibo is in waiting_backoff AND Bilibili #1 is blocked in flight:
	//    - Bilibili #2 MUST stay queued (max 1 active task per site);
	//    - Douyin and X MUST complete completely unobstructed!
	waitQueuePredicate(t, b, dyQ.ID, "douyin completed during weibo backoff", func(qt CacheQueueTask) bool {
		return qt.Status == CacheQueueCompleted && qt.CachedCount == 1
	})
	waitQueuePredicate(t, b, xQ.ID, "x completed during weibo backoff", func(qt CacheQueueTask) bool {
		return qt.Status == CacheQueueCompleted && qt.CachedCount == 1
	})
	if q2, ok := b.FindCacheQueueTask("bilibili", "4102", "dynamics"); !ok || q2.Status != CacheQueueQueued {
		t.Fatalf("expected biliQ2 to remain queued while biliQ1 occupies bilibili slot, got %+v", q2)
	}

	// 3. Unblock Bilibili #1 -> both Bilibili #1 and #2 complete while Weibo stays in waiting_backoff.
	close(h.unblockCh)
	waitQueuePredicate(t, b, biliQ1.ID, "biliQ1 completed", func(qt CacheQueueTask) bool {
		return qt.Status == CacheQueueCompleted && qt.CachedCount == 1
	})
	waitQueuePredicate(t, b, biliQ2.ID, "biliQ2 completed", func(qt CacheQueueTask) bool {
		return qt.Status == CacheQueueCompleted && qt.CachedCount == 1
	})
	if gotWB, ok := b.FindCacheQueueTask("weibo", synthWeiboUID, "posts"); !ok || gotWB.Status != CacheQueueWaitingBackoff {
		t.Fatalf("expected Weibo still waiting_backoff, got %+v", gotWB)
	}
	if wbTimelineHits.Load() != 1 {
		t.Fatalf("expected Weibo to stop after 1 risk-control hit without retrying, got %d", wbTimelineHits.Load())
	}
}

// DEC-088: Manual MoveCacheQueue (top/up/down) persists order in cache-queue.json
// (schema version 3), preserves order across restarts, honors pacing cooldown when
// promoting ahead of a waiting task, and lets an in-flight download finish cleanly
// before switching to the newly prioritized task.
func TestCacheQueueManualReorderPersistsAcrossRestartAndRespectsPacing(t *testing.T) {
	h := newQueueDualHarness(t, 3, 3, 2, 2)
	rng := func(int) int { return 0 } // deterministic min delays (40s item delay, 60s startup delay)
	b, cleanup := h.openBackend(t, rng)

	ctx := context.Background()
	if _, err := b.ImportCookie(ctx, "SESSDATA=synth_bili_reorder; bili_jct=csrf_r; DedeUserID=778899", ""); err != nil {
		cleanup()
		t.Fatalf("ImportCookie: %v", err)
	}
	if _, err := b.LoginWithCookie(ctx, "douyin", "sessionid=synth_dy_reorder; sid_guard=g_r; ttwid=t_r", ""); err != nil {
		cleanup()
		t.Fatalf("LoginWithCookie: %v", err)
	}

	// Enqueue 3 Bilibili tasks + 1 Douyin task before starting workers (all queued).
	q1, err := b.EnqueueCacheQueue(ctx, CacheQueueRequest{Site: "bilibili", List: "dynamics", User: "5001", UserName: "UP一"})
	if err != nil {
		cleanup()
		t.Fatalf("enqueue q1: %v", err)
	}
	q2, err := b.EnqueueCacheQueue(ctx, CacheQueueRequest{Site: "bilibili", List: "dynamics", User: "5002", UserName: "UP二"})
	if err != nil {
		cleanup()
		t.Fatalf("enqueue q2: %v", err)
	}
	q3, err := b.EnqueueCacheQueue(ctx, CacheQueueRequest{Site: "bilibili", List: "dynamics", User: "5003", UserName: "UP三"})
	if err != nil {
		cleanup()
		t.Fatalf("enqueue q3: %v", err)
	}
	dy1, err := b.EnqueueCacheQueue(ctx, CacheQueueRequest{Site: "douyin", List: "works", User: "MS4wLjABAAAA_synth_dy_keep", UserName: "抖音一"})
	if err != nil {
		cleanup()
		t.Fatalf("enqueue dy1: %v", err)
	}

	biliOrder := func(bk *Backend) []string {
		var out []string
		for _, q := range bk.ListCacheQueue() {
			if q.Site == "bilibili" {
				out = append(out, q.ID)
			}
		}
		return out
	}

	// 1. Reorder: top(q3) -> [q3, q1, q2]; up(q2) -> [q3, q2, q1]; down(q3) -> [q2, q3, q1].
	if _, err := b.MoveCacheQueue(q3.ID, QueueMoveTop); err != nil {
		cleanup()
		t.Fatalf("MoveCacheQueue top: %v", err)
	}
	if got := biliOrder(b); !reflect.DeepEqual(got, []string{q3.ID, q1.ID, q2.ID}) {
		cleanup()
		t.Fatalf("after top(q3) = %v, want [%s %s %s]", got, q3.ID, q1.ID, q2.ID)
	}
	if _, err := b.MoveCacheQueue(q2.ID, QueueMoveUp); err != nil {
		cleanup()
		t.Fatalf("MoveCacheQueue up: %v", err)
	}
	if _, err := b.MoveCacheQueue(q3.ID, QueueMoveDown); err != nil {
		cleanup()
		t.Fatalf("MoveCacheQueue down: %v", err)
	}
	if got := biliOrder(b); !reflect.DeepEqual(got, []string{q2.ID, q3.ID, q1.ID}) {
		cleanup()
		t.Fatalf("after down(q3) = %v, want [%s %s %s]", got, q2.ID, q3.ID, q1.ID)
	}
	if _, err := b.MoveCacheQueue(q1.ID, "invalid"); err == nil {
		cleanup()
		t.Fatal("expected error for invalid direction")
	}

	// Verify cache-queue.json on disk keeps schema version 3 and exact new order.
	raw, err := os.ReadFile(filepath.Join(h.loc.DataDir, cacheQueueFileName))
	if err != nil {
		cleanup()
		t.Fatalf("read cache-queue.json: %v", err)
	}
	var doc cacheQueueFile
	if err := json.Unmarshal(raw, &doc); err != nil {
		cleanup()
		t.Fatalf("unmarshal cache-queue.json: %v", err)
	}
	if doc.Version != 3 {
		cleanup()
		t.Fatalf("cache-queue.json version = %d, want 3", doc.Version)
	}
	var diskBili []string
	for _, r := range doc.Tasks {
		if r.Site == "bilibili" {
			diskBili = append(diskBili, r.ID)
		}
	}
	if !reflect.DeepEqual(diskBili, []string{q2.ID, q3.ID, q1.ID}) {
		cleanup()
		t.Fatalf("disk bilibili order = %v, want [%s %s %s]", diskBili, q2.ID, q3.ID, q1.ID)
	}

	// 2. Restart Backend -> order MUST survive restart, and worker MUST execute q2 FIRST!
	cleanup()
	b2, cleanup2 := h.openBackend(t, rng)
	defer cleanup2()

	if got := biliOrder(b2); !reflect.DeepEqual(got, []string{q2.ID, q3.ID, q1.ID}) {
		t.Fatalf("order after restart = %v, want [%s %s %s]", got, q2.ID, q3.ID, q1.ID)
	}
	// Pause Douyin so fakeQueueClock timers belong solely to Bilibili for crisp deterministic assertions.
	if _, err := b2.PauseCacheQueue(dy1.ID); err != nil {
		t.Fatalf("pause dy1: %v", err)
	}

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	b2.queue.Start(runCtx)

	// After restart, Start() applies the 1-5m startup delay to the first runnable task (q2).
	_, _ = h.clock.WaitForPendingTimer(t, 3*time.Second)
	waitQueuePredicate(t, b2, q2.ID, "q2 armed startup delay first after restart", func(qt CacheQueueTask) bool {
		return qt.Status == CacheQueueWaiting && qt.CachedCount == 0
	})
	h.clock.Advance(65 * time.Second)

	// q2 runs first and caches 1 item, then enters waiting (40s pacing cooldown).
	_, _ = h.clock.WaitForPendingTimer(t, 3*time.Second)
	q2Wait := waitQueuePredicate(t, b2, q2.ID, "q2 cached 1 and waiting", func(qt CacheQueueTask) bool {
		return qt.CachedCount == 1 && qt.Status == CacheQueueWaiting
	})
	if q2Wait.NextRunAt == "" {
		t.Fatalf("expected NextRunAt on waiting q2, got %+v", q2Wait)
	}
	if gotQ3, _ := b2.FindCacheQueueTask("bilibili", "5003", "dynamics"); gotQ3.CachedCount != 0 || gotQ3.Status != CacheQueueQueued {
		t.Fatalf("expected q3 still queued with 0 cached, got %+v", gotQ3)
	}

	// 3. Promote q3 to top WHILE q2 is waiting -> q2 demotes to queued preserving
	//    CachedCount=1 & RecentItems; q3 takes active slot in waiting preserving NextRunAt!
	if _, err := b2.MoveCacheQueue(q3.ID, QueueMoveTop); err != nil {
		t.Fatalf("MoveCacheQueue top(q3) while q2 waiting: %v", err)
	}
	q2Demoted := waitQueuePredicate(t, b2, q2.ID, "q2 demoted to queued", func(qt CacheQueueTask) bool {
		return qt.Status == CacheQueueQueued
	})
	if q2Demoted.CachedCount != 1 || len(q2Demoted.RecentItems) != 1 {
		t.Fatalf("demoting q2 lost progress: %+v", q2Demoted)
	}
	q3Promoted := waitQueuePredicate(t, b2, q3.ID, "q3 inherited pacing wait", func(qt CacheQueueTask) bool {
		return qt.Status == CacheQueueWaiting && qt.NextRunAt == q2Wait.NextRunAt
	})
	if q3Promoted.CachedCount != 0 {
		t.Fatalf("q3 bypassed pacing cooldown and cached early: %+v", q3Promoted)
	}

	// Advance clock by 20s (< 40s cooldown): q3 MUST STILL wait!
	h.clock.Advance(20 * time.Second)
	time.Sleep(30 * time.Millisecond)
	if still, _ := b2.FindCacheQueueTask("bilibili", "5003", "dynamics"); still.Status != CacheQueueWaiting || still.CachedCount != 0 {
		t.Fatalf("q3 ran before inherited 40s cooldown elapsed: %+v", still)
	}

	// Advance remaining 25s -> q3 caches item #1 and enters waiting.
	h.clock.Advance(25 * time.Second)
	_, _ = h.clock.WaitForPendingTimer(t, 3*time.Second)
	waitQueuePredicate(t, b2, q3.ID, "q3 cached 1st item", func(qt CacheQueueTask) bool {
		return qt.CachedCount == 1 && qt.Status == CacheQueueWaiting
	})

	// 4. Reorder while q3 is actively RUNNING (in-flight download):
	//    Block q3's 2nd item ("5003000002"), advance clock so q3 starts downloading it,
	//    then move q1 to top. q3 MUST finish item #2 cleanly, then yield to q1!
	h.blockBiliID.Store("5003000002")
	t.Cleanup(func() {
		select {
		case <-h.unblockCh:
		default:
			close(h.unblockCh)
		}
	})
	h.clock.Advance(45 * time.Second)
	waitQueuePredicate(t, b2, q3.ID, "q3 running 2nd item in flight", func(qt CacheQueueTask) bool {
		return qt.Status == CacheQueueRunning && qt.ActiveItemKey == "bilibili:dynamic:5003000002"
	})

	if _, err := b2.MoveCacheQueue(q1.ID, QueueMoveTop); err != nil {
		t.Fatalf("MoveCacheQueue top(q1) during in-flight q3: %v", err)
	}
	// q3 must NOT be aborted mid-download.
	if mid, _ := b2.FindCacheQueueTask("bilibili", "5003", "dynamics"); mid.Status != CacheQueueRunning {
		t.Fatalf("expected in-flight q3 to stay running until current item finishes, got %+v", mid)
	}

	// Unblock q3's 2nd download -> q3 finishes item #2, yields to queued, and q1 enters waiting!
	close(h.unblockCh)
	q3Yielded := waitQueuePredicate(t, b2, q3.ID, "q3 finished item 2 and yielded to queued", func(qt CacheQueueTask) bool {
		return qt.Status == CacheQueueQueued && qt.CachedCount == 2
	})
	if len(q3Yielded.RecentItems) != 2 {
		t.Fatalf("expected 2 recent items preserved on yielded q3, got %+v", q3Yielded)
	}
	waitQueuePredicate(t, b2, q1.ID, "q1 took active slot in waiting cooldown", func(qt CacheQueueTask) bool {
		return qt.Status == CacheQueueWaiting && qt.CachedCount == 0
	})

	// Advance clock past cooldown -> q1 caches item #1!
	h.clock.Advance(45 * time.Second)
	waitQueuePredicate(t, b2, q1.ID, "q1 cached 1st item", func(qt CacheQueueTask) bool {
		return qt.CachedCount == 1
	})

	// 5. Terminal (canceled) task cannot be reordered.
	if _, err := b2.CancelCacheQueue(q2.ID); err != nil {
		t.Fatalf("CancelCacheQueue q2: %v", err)
	}
	if _, err := b2.MoveCacheQueue(q2.ID, QueueMoveTop); err == nil {
		t.Fatal("expected error reordering canceled cache queue task")
	}
}

// Regression for PR #163 review:
//  1. Promoting task B ahead of a waiting_backoff task A must inherit A's future
//     NextRunAt (including the 10-30m BackoffCool window after base backoff ends),
//     status (waiting_backoff), and stop reason without flickering to running or
//     bypassing cooldown.
//  2. Moving a paused task B to #1 ahead of a waiting task A, then calling
//     ResumeCacheQueue(B), must immediately reconcile the site's active slot so A
//     demotes to queued and B inherits the remaining cooldown and runs next.
func TestCacheQueueReorderBackoffInheritanceAndResumePreemptsWaiting(t *testing.T) {
	h := newQueueDualHarness(t, 2, 2, 1, 1)
	h.clock = newFakeQueueClock(time.Now().UTC().Truncate(time.Second))
	rng := func(int) int { return 0 } // deterministic min delays (40s item delay, 10m BackoffCool)
	b, cleanup := h.openBackend(t, rng)
	defer cleanup()

	ctx := context.Background()
	if _, err := b.ImportCookie(ctx, "SESSDATA=synth_bili_bk_inh; bili_jct=csrf_bi; DedeUserID=778899", ""); err != nil {
		t.Fatalf("ImportCookie: %v", err)
	}

	// Part 1: waiting_backoff BackoffCool inheritance.
	// Trigger Bilibili risk control so qA enters waiting_backoff with NextRunAt in the future.
	h.biliMode.Store("risk")
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	b.queue.Start(runCtx)

	qA, err := b.EnqueueCacheQueue(ctx, CacheQueueRequest{Site: "bilibili", List: "dynamics", User: "6001", UserName: "退避UP甲"})
	if err != nil {
		t.Fatalf("enqueue qA: %v", err)
	}
	qB, err := b.EnqueueCacheQueue(ctx, CacheQueueRequest{Site: "bilibili", List: "dynamics", User: "6002", UserName: "优先UP乙"})
	if err != nil {
		t.Fatalf("enqueue qB: %v", err)
	}

	_, _ = h.clock.WaitForPendingTimer(t, 3*time.Second)
	aBackoff := waitQueuePredicate(t, b, qA.ID, "qA entered waiting_backoff", func(qt CacheQueueTask) bool {
		return qt.Status == CacheQueueWaitingBackoff && qt.NextRunAt != ""
	})
	aNextAt, err := time.Parse(time.RFC3339, aBackoff.NextRunAt)
	if err != nil {
		t.Fatalf("parse qA NextRunAt %q: %v", aBackoff.NextRunAt, err)
	}

	// Simulate base backoff just expired while still inside the 10-30m BackoffCool window:
	// clear site backoff in subs, restore server to "ok", and check that moving qB to top
	// STILL preserves qA's future NextRunAt (BackoffCool) without flickering or running early!
	if err := b.subs.SaveSiteSchedulerState("bilibili", subscription.SchedulerState{
		Site: "bilibili",
	}); err != nil {
		t.Fatalf("SaveSiteSchedulerState clear backoff: %v", err)
	}
	h.biliMode.Store("ok")
	reqsBeforeMove := h.biliAPIReqs.Load()

	movedB, err := b.MoveCacheQueue(qB.ID, QueueMoveTop)
	if err != nil {
		t.Fatalf("MoveCacheQueue top(qB) ahead of waiting_backoff qA: %v", err)
	}
	if movedB.Status != CacheQueueWaitingBackoff {
		t.Fatalf("expected promoted qB to inherit waiting_backoff immediately, got %+v", movedB)
	}
	if movedB.NextRunAt != aBackoff.NextRunAt {
		t.Fatalf("qB NextRunAt = %q, want inherited %q", movedB.NextRunAt, aBackoff.NextRunAt)
	}
	if movedB.StopReason == "" {
		t.Fatalf("expected promoted qB to inherit StopReason, got %+v", movedB)
	}
	gotA, ok := b.FindCacheQueueTask("bilibili", "6001", "dynamics")
	if !ok || gotA.Status != CacheQueueQueued || gotA.NextRunAt != "" {
		t.Fatalf("expected qA demoted to queued cleanly, got %+v", gotA)
	}

	// Advance halfway into the inherited BackoffCool window: qB must NOT run early (0 API calls).
	remaining := aNextAt.Sub(h.clock.Now().UTC())
	if remaining < 2*time.Minute {
		t.Fatalf("expected inherited BackoffCool >= 2m, got %v", remaining)
	}
	h.clock.Advance(remaining / 2)
	time.Sleep(40 * time.Millisecond)
	if gotReqs := h.biliAPIReqs.Load(); gotReqs != reqsBeforeMove {
		t.Fatalf("qB bypassed BackoffCool and made %d API requests early", gotReqs-reqsBeforeMove)
	}
	if midB, _ := b.FindCacheQueueTask("bilibili", "6002", "dynamics"); midB.Status != CacheQueueWaitingBackoff || midB.CachedCount != 0 {
		t.Fatalf("expected qB still in waiting_backoff during BackoffCool, got %+v", midB)
	}

	// Advance past inherited NextRunAt -> qB wakes and caches item #1 FIRST (not qA)!
	h.clock.Advance(remaining/2 + 5*time.Second)
	_, _ = h.clock.WaitForPendingTimer(t, 3*time.Second)
	bWait := waitQueuePredicate(t, b, qB.ID, "qB cached 1st item after BackoffCool", func(qt CacheQueueTask) bool {
		return qt.CachedCount == 1 && qt.Status == CacheQueueWaiting && qt.NextRunAt != ""
	})
	if stillA, _ := b.FindCacheQueueTask("bilibili", "6001", "dynamics"); stillA.CachedCount != 0 || stillA.Status != CacheQueueQueued {
		t.Fatalf("expected qA still queued behind qB, got %+v", stillA)
	}

	// Part 2: Move a paused task qC to #1 ahead of waiting qB, then ResumeCacheQueue(qC)
	// must immediately reconcile the site slot so qB demotes to queued and qC inherits qB's
	// remaining pacing cooldown and executes next!
	qC, err := b.EnqueueCacheQueue(ctx, CacheQueueRequest{Site: "bilibili", List: "dynamics", User: "6003", UserName: "恢复优先UP丙"})
	if err != nil {
		t.Fatalf("enqueue qC: %v", err)
	}
	if _, err := b.PauseCacheQueue(qC.ID); err != nil {
		t.Fatalf("PauseCacheQueue qC: %v", err)
	}
	if _, err := b.MoveCacheQueue(qC.ID, QueueMoveTop); err != nil {
		t.Fatalf("MoveCacheQueue top(paused qC): %v", err)
	}
	// While qC is still paused, qB remains the active waiting task.
	if bBeforeResume, _ := b.FindCacheQueueTask("bilibili", "6002", "dynamics"); bBeforeResume.Status != CacheQueueWaiting {
		t.Fatalf("expected qB to stay waiting while #1 qC is paused, got %+v", bBeforeResume)
	}

	resumedC, err := b.ResumeCacheQueue(qC.ID)
	if err != nil {
		t.Fatalf("ResumeCacheQueue qC: %v", err)
	}
	if resumedC.Status != CacheQueueWaiting || resumedC.NextRunAt != bWait.NextRunAt {
		t.Fatalf("expected resumed #1 qC to take active slot inheriting NextRunAt=%q, got %+v", bWait.NextRunAt, resumedC)
	}
	bDemoted, ok := b.FindCacheQueueTask("bilibili", "6002", "dynamics")
	if !ok || bDemoted.Status != CacheQueueQueued || bDemoted.CachedCount != 1 {
		t.Fatalf("expected qB demoted to queued preserving CachedCount=1 after qC resumed, got %+v", bDemoted)
	}

	// Advance past inherited pacing wait -> qC caches item #1 while qB stays at CachedCount=1!
	h.clock.Advance(45 * time.Second)
	waitQueuePredicate(t, b, qC.ID, "qC cached 1st item ahead of qB", func(qt CacheQueueTask) bool {
		return qt.CachedCount == 1
	})
	if bAfter, _ := b.FindCacheQueueTask("bilibili", "6002", "dynamics"); bAfter.CachedCount != 1 {
		t.Fatalf("qB should not have cached a 2nd item after #1 qC resumed; got %+v", bAfter)
	}
}
