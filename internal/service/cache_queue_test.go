package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/live"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/site/douyin"
	"github.com/chialecode/trace/internal/storage"
	"github.com/chialecode/trace/internal/subscription"
)

type queueDualHarness struct {
	loc          appdirs.Locations
	clock        *fakeQueueClock
	biliSrv      *httptest.Server
	dySrv        *httptest.Server
	biliAPIReqs  *atomic.Int64
	dyAPIReqs    *atomic.Int64
	biliMode     *atomic.Value // "ok", "risk", "ratelimit", "expired"
	dyMode       *atomic.Value // "ok", "risk", "ratelimit", "expired"
	biliTotal    int
	biliPageSize int
	dyTotal      int
	dyPageSize   int
	blockBiliID  *atomic.Value // string dynamic ID to block inside image download for in-flight testing
	unblockCh    chan struct{}
}

func newQueueDualHarness(t *testing.T, biliTotal, biliPageSize, dyTotal, dyPageSize int) *queueDualHarness {
	t.Helper()

	h := &queueDualHarness{
		loc:          testLocations(t),
		clock:        newFakeQueueClock(time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)),
		biliAPIReqs:  &atomic.Int64{},
		dyAPIReqs:    &atomic.Int64{},
		biliMode:     &atomic.Value{},
		dyMode:       &atomic.Value{},
		biliTotal:    biliTotal,
		biliPageSize: max(biliPageSize, 1),
		dyTotal:      dyTotal,
		dyPageSize:   max(dyPageSize, 1),
		blockBiliID:  &atomic.Value{},
		unblockCh:    make(chan struct{}),
	}
	h.biliMode.Store("ok")
	h.dyMode.Store("ok")
	h.blockBiliID.Store("")

	thumbPNG := historyThumbnailPNG(t, 32, 32)

	h.biliSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/bfs/") {
			if r.Header.Get("Cookie") != "" {
				t.Errorf("bilibili CDN leaked Cookie on %s", r.URL.Path)
			}
			blockID, _ := h.blockBiliID.Load().(string)
			if blockID != "" && strings.Contains(r.URL.Path, "/"+blockID+".png") {
				select {
				case <-h.unblockCh:
				case <-r.Context().Done():
					return
				}
			}
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(thumbPNG)
			return
		}

		h.biliAPIReqs.Add(1)
		w.Header().Set("Content-Type", "application/json")
		mode, _ := h.biliMode.Load().(string)

		if r.URL.Path == "/x/web-interface/nav" {
			ck := r.Header.Get("Cookie")
			if !strings.Contains(ck, "SESSDATA=") || strings.Contains(ck, "SESSDATA=expired") {
				_, _ = fmt.Fprint(w, `{"code":-101,"message":"账号未登录","data":{"isLogin":false}}`)
				return
			}
			_, _ = fmt.Fprintf(w, `{"code":0,"data":{"isLogin":true,"mid":778899,"uname":"测试登录用户","face":%q,"wbi_img":{"img_url":"https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png","sub_url":"https://i0.hdslb.com/bfs/wbi/4932caff0ff746eab6f01bf08b70ac45.png"}}}`,
				h.biliSrv.URL+"/bfs/face/self.png")
			return
		}

		switch mode {
		case "expired":
			_, _ = fmt.Fprint(w, `{"code":-101,"message":"账号未登录"}`)
			return
		case "risk":
			_, _ = fmt.Fprint(w, `{"code":-352,"message":"风控校验失败"}`)
			return
		case "ratelimit":
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = fmt.Fprint(w, `{"code":-412,"message":"请求过于频繁"}`)
			return
		}

		switch r.URL.Path {
		case "/x/polymer/web-dynamic/v1/feed/space":
			midStr := r.URL.Query().Get("host_mid")
			midVal, _ := strconv.ParseInt(midStr, 10, 64)
			if midVal <= 0 {
				midVal = 1001
			}
			startIdx := 0
			if off := strings.TrimSpace(r.URL.Query().Get("offset")); off != "" {
				startIdx, _ = strconv.Atoi(off)
			}
			endIdx := min(startIdx+h.biliPageSize, h.biliTotal)
			items := make([]map[string]any, 0, max(0, endIdx-startIdx))
			for i := startIdx; i < endIdx; i++ {
				dynID := fmt.Sprintf("%d%06d", midVal, i+1)
				imgURL := fmt.Sprintf("%s/bfs/album/%s.png?x-expires=SYNTH_EXP_%d&sign=SYNTH_SIG_%d", h.biliSrv.URL, dynID, i+1, i+1)
				items = append(items, map[string]any{
					"id_str": dynID,
					"type":   "DYNAMIC_TYPE_DRAW",
					"modules": map[string]any{
						"module_author": map[string]any{
							"mid":    midVal,
							"name":   fmt.Sprintf("UP主%d", midVal),
							"face":   h.biliSrv.URL + "/bfs/face/up.png?upsig=SYNTH_UP_FACE_SIG",
							"pub_ts": 1720000000 - int64(i*60),
						},
						"module_dynamic": map[string]any{
							"major": map[string]any{
								"type": "MAJOR_TYPE_OPUS",
								"opus": map[string]any{
									"title":   fmt.Sprintf("第 %d 条动态内容", i+1),
									"summary": map[string]any{"text": fmt.Sprintf("第 %d 条动态内容", i+1)},
									"pics": []map[string]any{
										{"url": imgURL, "width": 32, "height": 32},
									},
								},
							},
						},
					},
				})
			}
			hasMore := endIdx < h.biliTotal
			nextOff := ""
			if hasMore {
				nextOff = strconv.Itoa(endIdx)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"has_more": hasMore,
					"offset":   nextOff,
					"items":    items,
				},
			})

		case "/x/polymer/web-dynamic/v1/detail":
			dynID := strings.TrimSpace(r.URL.Query().Get("id"))
			midVal := int64(1001)
			if len(dynID) > 6 {
				if parsedMid, pErr := strconv.ParseInt(dynID[:len(dynID)-6], 10, 64); pErr == nil && parsedMid > 0 {
					midVal = parsedMid
				}
			}
			seqNum := 1
			if len(dynID) >= 6 {
				if parsedSeq, sErr := strconv.Atoi(dynID[len(dynID)-6:]); sErr == nil && parsedSeq > 0 {
					seqNum = parsedSeq
				}
			}
			imgURL := fmt.Sprintf("%s/bfs/album/%s.png?x-expires=SYNTH_EXP_%d&sign=SYNTH_SIG_%d", h.biliSrv.URL, dynID, seqNum, seqNum)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"item": map[string]any{
						"id_str": dynID,
						"type":   "DYNAMIC_TYPE_DRAW",
						"modules": map[string]any{
							"module_author": map[string]any{
								"mid":    midVal,
								"name":   fmt.Sprintf("UP主%d", midVal),
								"face":   h.biliSrv.URL + "/bfs/face/up.png?upsig=SYNTH_UP_FACE_SIG",
								"pub_ts": 1720000000 - int64((seqNum-1)*60),
							},
							"module_dynamic": map[string]any{
								"major": map[string]any{
									"type": "MAJOR_TYPE_OPUS",
									"opus": map[string]any{
										"title":   fmt.Sprintf("第 %d 条动态内容", seqNum),
										"summary": map[string]any{"text": fmt.Sprintf("第 %d 条动态内容", seqNum)},
										"pics": []map[string]any{
											{"url": imgURL, "width": 32, "height": 32},
										},
									},
								},
							},
						},
					},
				},
			})

		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(h.biliSrv.Close)

	const selfSecUID = "MS4wLjABAAAA_synth_self_queue_0001"
	h.dySrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/obj/") {
			if r.Header.Get("Cookie") != "" {
				t.Errorf("douyin CDN leaked Cookie on %s", r.URL.Path)
			}
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(thumbPNG)
			return
		}

		h.dyAPIReqs.Add(1)
		w.Header().Set("Content-Type", "application/json")
		mode, _ := h.dyMode.Load().(string)

		if r.URL.Path == "/aweme/v1/web/user/profile/self/" {
			ck := r.Header.Get("Cookie")
			if !strings.Contains(ck, "sessionid=") {
				_, _ = fmt.Fprint(w, `{"status_code":8,"status_msg":"请先登录"}`)
				return
			}
			_, _ = fmt.Fprintf(w, `{"status_code":0,"user":{"uid":"88001","sec_uid":%q,"unique_id":"synth_self","nickname":"合成抖音本人","avatar_thumb":{"url_list":[%q]}}}`,
				selfSecUID, h.dySrv.URL+"/obj/avatar.png?x-expires=SYNTH_DY_SELF_EXP")
			return
		}

		switch mode {
		case "expired":
			_, _ = fmt.Fprint(w, `{"status_code":8,"status_msg":"登录已失效"}`)
			return
		case "risk":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = fmt.Fprint(w, `<html><head><title>验证码中间页</title></head><body><div id="verify-captcha">slide</div></body></html>`)
			return
		case "ratelimit":
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = fmt.Fprint(w, `{"status_code":2154,"status_msg":"请求太频繁"}`)
			return
		}

		switch r.URL.Path {
		case "/aweme/v1/web/aweme/post/":
			secUID := r.URL.Query().Get("sec_user_id")
			startIdx := 0
			if cur := strings.TrimSpace(r.URL.Query().Get("max_cursor")); cur != "" && cur != "0" {
				startIdx, _ = strconv.Atoi(cur)
			}
			endIdx := min(startIdx+h.dyPageSize, h.dyTotal)
			awemes := make([]map[string]any, 0, max(0, endIdx-startIdx))
			prefix := "7400000000000"
			if strings.HasSuffix(secUID, "_fault_0") {
				prefix = "7401000000000"
			} else if strings.HasSuffix(secUID, "_fault_1") {
				prefix = "7402000000000"
			}
			for i := startIdx; i < endIdx; i++ {
				awemeID := fmt.Sprintf("%s%06d", prefix, i+1)
				imgURL := fmt.Sprintf("%s/obj/note_%d.png?x-expires=SYNTH_DY_EXP_%d&x-signature=SYNTH_DY_SIG_%d", h.dySrv.URL, i+1, i+1, i+1)
				awemes = append(awemes, map[string]any{
					"aweme_id":    awemeID,
					"aweme_type":  68,
					"desc":        fmt.Sprintf("抖音图文作品 %d", i+1),
					"create_time": 1720000000 - int64(i*60),
					"author": map[string]any{
						"uid":      "99001",
						"sec_uid":  secUID,
						"nickname": "合成抖音作者",
						"avatar_thumb": map[string]any{
							"url_list": []string{h.dySrv.URL + "/obj/avatar.png?x-expires=SYNTH_DY_AVATAR_EXP"},
						},
					},
					"images": []map[string]any{
						{"uri": fmt.Sprintf("tos/note_%d", i+1), "width": 32, "height": 32, "url_list": []string{imgURL}},
					},
				})
			}
			hasMore := 0
			nextCur := 0
			if endIdx < h.dyTotal {
				hasMore = 1
				nextCur = endIdx
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status_code": 0,
				"has_more":    hasMore,
				"max_cursor":  nextCur,
				"aweme_list":  awemes,
			})

		case "/aweme/v1/web/aweme/detail/":
			awemeID := r.URL.Query().Get("aweme_id")
			imgURL := fmt.Sprintf("%s/obj/note_%s.png?x-expires=SYNTH_DY_EXP&x-signature=SYNTH_DY_SIG", h.dySrv.URL, awemeID)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status_code": 0,
				"aweme_detail": map[string]any{
					"aweme_id":    awemeID,
					"aweme_type":  68,
					"desc":        "抖音图文详情 " + awemeID,
					"create_time": 1720000000,
					"author": map[string]any{
						"uid":      "99001",
						"sec_uid":  "MS4wLjABAAAA_synth_creator_queue",
						"nickname": "合成抖音作者",
						"avatar_thumb": map[string]any{
							"url_list": []string{h.dySrv.URL + "/obj/avatar.png?x-expires=SYNTH_DY_AVATAR_EXP"},
						},
					},
					"images": []map[string]any{
						{"uri": "tos/note_" + awemeID, "width": 32, "height": 32, "url_list": []string{imgURL}},
					},
				},
			})

		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(h.dySrv.Close)

	return h
}

func (h *queueDualHarness) openBackend(t *testing.T, rng CacheQueueRand) (*Backend, context.CancelFunc) {
	t.Helper()
	b, err := NewBackend(Options{
		Locations:        h.loc,
		Desktop:          NewMockDesktop(),
		DisableBgWorkers: true,
		CacheQueueClock:  h.clock,
		CacheQueueRand:   rng,
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
	})
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	if err := b.dl.Start(runCtx); err != nil {
		cancel()
		_ = b.Close()
		t.Fatalf("dl.Start: %v", err)
	}
	return b, func() {
		cancel()
		_ = b.Close()
	}
}

func waitQueuePredicate(t *testing.T, b *Backend, id string, desc string, pred func(CacheQueueTask) bool) CacheQueueTask {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	var last CacheQueueTask
	for time.Now().Before(deadline) {
		for _, task := range b.ListCacheQueue() {
			if task.ID == id {
				last = task
				if pred(task) {
					return task
				}
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s on task %s; last state = %+v", desc, id, last)
	return last
}

// Under a fake clock and fixed deterministic RNG, a 300-item list advances strictly by
// "enqueue 1 item -> wait for download to finish -> random 40-150s delay", takes a 10-25m long rest
// every 15-25 cached items, and never puts more than 1 active download task into download.Manager.
func TestCacheQueue300ItemsPacingAndSingleDownloadConcurrency(t *testing.T) {
	const totalItems = 300
	h := newQueueDualHarness(t, totalItems, 30, 0, 20)

	var rngMu sync.Mutex
	prng := rand.New(rand.NewPCG(20261115, 42))
	rng := func(n int) int {
		rngMu.Lock()
		defer rngMu.Unlock()
		return prng.IntN(n)
	}
	oracleRNG := rand.New(rand.NewPCG(20261115, 42))
	oracleRandInclusive := func(lo, hi int) int {
		return lo + oracleRNG.IntN(hi-lo+1)
	}

	b, cleanup := h.openBackend(t, rng)
	defer cleanup()

	ctx := context.Background()
	if _, err := b.ImportCookie(ctx, "SESSDATA=synth_bili_m4k24; bili_jct=csrf_m4k24; DedeUserID=778899", ""); err != nil {
		t.Fatalf("ImportCookie: %v", err)
	}

	// Track maximum concurrent queued/running download tasks on every event across all 300 downloads.
	var maxActiveDownloads atomic.Int32
	unsub := b.SubscribeEvents(func(ev Event) {
		if ev.Type != EventTaskUpdated {
			return
		}
		tasks, err := b.DownloadManager().ListTasks()
		if err != nil {
			return
		}
		var active int32
		for _, dt := range tasks {
			if dt.Status == "queued" || dt.Status == "running" {
				active++
			}
		}
		for {
			cur := maxActiveDownloads.Load()
			if active <= cur || maxActiveDownloads.CompareAndSwap(cur, active) {
				break
			}
		}
	})
	defer unsub()

	// Block item #1 in flight briefly to prove the 40-150s timer is NOT armed until the download finishes.
	h.blockBiliID.Store("1001000001")

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	b.queue.Start(runCtx)

	created, err := b.EnqueueCacheQueue(ctx, CacheQueueRequest{
		Site:     "bilibili",
		List:     "dynamics",
		User:     "1001",
		UserName: "三百条测试UP",
	})
	if err != nil {
		t.Fatalf("EnqueueCacheQueue: %v", err)
	}

	// Wait until item #1 is actively downloading, and verify 0 timers are armed while download is running.
	inFlight := waitQueuePredicate(t, b, created.ID, "item #1 active download", func(qt CacheQueueTask) bool {
		return qt.Status == CacheQueueRunning && qt.ActiveDownloadID != ""
	})
	if h.clock.ActiveTimerCount() != 0 {
		t.Fatalf("timer was armed before item #1 download finished! task=%+v", inFlight)
	}

	// Unblock item #1 download; now and only now should the first inter-item timer be armed.
	h.blockBiliID.Store("")
	close(h.unblockCh)

	expectedThreshold := oracleRandInclusive(15, 25) // initial RestThreshold drawn on EnqueueCacheQueue
	sinceRest := 0
	longRestCount := 0
	ordinaryDelaySet := make(map[time.Duration]bool)

	// Drive all 299 waits between items 1..300.
	for itemNum := 1; itemNum < totalItems; itemNum++ {
		dur, _ := h.clock.WaitForPendingTimer(t, 4*time.Second)
		sinceRest++
		if sinceRest >= expectedThreshold {
			wantSec := oracleRandInclusive(int(DefaultCacheQueuePacing.LongRestMinDelay/time.Second), int(DefaultCacheQueuePacing.LongRestMaxDelay/time.Second))
			wantDur := time.Duration(wantSec) * time.Second
			expectedThreshold = oracleRandInclusive(15, 25)
			sinceRest = 0
			longRestCount++
			if dur < 10*time.Minute || dur > 25*time.Minute || dur != wantDur {
				t.Fatalf("item %d long rest = %v, want %v in [10m, 25m]", itemNum, dur, wantDur)
			}
		} else {
			wantSec := oracleRandInclusive(int(DefaultCacheQueuePacing.ItemMinDelay/time.Second), int(DefaultCacheQueuePacing.ItemMaxDelay/time.Second))
			wantDur := time.Duration(wantSec) * time.Second
			ordinaryDelaySet[dur] = true
			if dur < 40*time.Second || dur > 150*time.Second || dur != wantDur {
				t.Fatalf("item %d inter-item delay = %v, want %v in [40s, 150s]", itemNum, dur, wantDur)
			}
		}
		h.clock.Advance(dur)
	}

	final := waitQueuePredicate(t, b, created.ID, "300 items completed", func(qt CacheQueueTask) bool {
		return qt.Status == CacheQueueCompleted
	})

	if final.CachedCount != totalItems || final.SkippedCount != 0 {
		t.Fatalf("CachedCount=%d SkippedCount=%d, want %d and 0", final.CachedCount, final.SkippedCount, totalItems)
	}
	if gotMax := maxActiveDownloads.Load(); gotMax != 1 {
		t.Fatalf("max concurrent active downloads in download.Manager = %d, want strictly 1", gotMax)
	}
	if longRestCount < 11 || longRestCount > 20 {
		t.Fatalf("unexpected longRestCount = %d across 300 items", longRestCount)
	}
	if len(ordinaryDelaySet) < 20 {
		t.Fatalf("ordinary delays were not randomized across [40s, 150s]: distinct=%d", len(ordinaryDelaySet))
	}
	if len(final.RecentItems) != maxCacheQueueRecentItems || final.RecentItems[0].ItemKey != "bilibili:dynamic:1001000300" {
		t.Fatalf("unexpected RecentItems: len=%d first=%+v", len(final.RecentItems), final.RecentItems)
	}
	if _, exists, err := b.GetLibraryItem("bilibili:dynamic:1001000300"); err != nil || !exists {
		t.Fatalf("300th item missing from local library: exists=%v err=%v", exists, err)
	}
}

// Paging charges site hourly quota; risk control, rate limit, and quota exhaustion enter
// waiting_backoff, send zero site requests during backoff, resume after backoff + 10-30m cooldown,
// and leave the other site completely unaffected.
func TestCacheQueuePagingQuotaBackoffAndCrossSiteIsolation(t *testing.T) {
	h := newQueueDualHarness(t, 4, 2, 3, 2)

	// Fixed RNG returning midpoint offset so cooldowns are deterministic (e.g. +15m on [10m, 30m]).
	b, cleanup := h.openBackend(t, func(n int) int { return min(300, n-1) })
	defer cleanup()

	ctx := context.Background()
	if _, err := b.ImportCookie(ctx, "SESSDATA=synth_bili_k25; bili_jct=csrf_k25; DedeUserID=778899", ""); err != nil {
		t.Fatalf("ImportCookie: %v", err)
	}
	if _, err := b.LoginWithCookie(ctx, "douyin", "sessionid=synth_dy_k25; sid_guard=guard_k25; ttwid=ttwid_k25", ""); err != nil {
		t.Fatalf("LoginWithCookie Douyin: %v", err)
	}

	biliQuotaLimit := subscription.SiteHourlyQuotaLimit("bilibili")

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	b.queue.Start(runCtx)

	// Start Bilibili queue with empty quota: Page 1 paging must charge Bilibili's automatic hourly quota!
	biliTask, err := b.EnqueueCacheQueue(ctx, CacheQueueRequest{
		Site: "bilibili",
		List: "dynamics",
		User: "2001",
	})
	if err != nil {
		t.Fatalf("EnqueueCacheQueue bilibili: %v", err)
	}

	// Wait for Item 1's inter-item delay: Page 1 has already been fetched and charged >= 1 hourly quota token.
	d1, _ := h.clock.WaitForPendingTimer(t, 3*time.Second)
	if gotTokens := len(b.subs.GetSiteSchedulerState("bilibili").TokenTimestamps); gotTokens < 1 {
		t.Fatalf("expected bilibili hourly tokens >= 1 after Page 1 paging, got %d", gotTokens)
	}
	h.clock.Advance(d1)

	// Wait for Item 2's inter-item delay; before advancing into Page 2 fetch, fill Bilibili's hourly window to full limit!
	d2, _ := h.clock.WaitForPendingTimer(t, 3*time.Second)
	nowBeforePage2 := h.clock.Now().UTC()
	oldestTokenTS := nowBeforePage2.Add(-20 * time.Minute).Unix()
	tokens := make([]int64, biliQuotaLimit)
	tokens[0] = oldestTokenTS
	for i := 1; i < len(tokens); i++ {
		tokens[i] = nowBeforePage2.Add(-10 * time.Minute).Unix()
	}
	if err := b.subs.SaveSiteSchedulerState("bilibili", subscription.SchedulerState{
		Site:            "bilibili",
		TokenTimestamps: tokens,
	}); err != nil {
		t.Fatalf("SaveSiteSchedulerState: %v", err)
	}
	h.clock.Advance(d2)

	// Now Page 2 paging hits ErrKindQuota -> must enter waiting_backoff!
	backoffState := waitQueuePredicate(t, b, biliTask.ID, "bilibili quota waiting_backoff", func(qt CacheQueueTask) bool {
		return qt.Status == CacheQueueWaitingBackoff
	})
	backoffDur, backoffDeadline := h.clock.WaitForPendingTimer(t, 3*time.Second)
	quotaExpiry := time.Unix(oldestTokenTS+3600, 0).UTC()
	coolAdded := backoffDeadline.Sub(quotaExpiry)
	if coolAdded < 10*time.Minute || coolAdded > 30*time.Minute {
		t.Fatalf("quota backoff cooldown = %v (deadline=%v, quotaExpiry=%v), want in [10m, 30m]; task=%+v",
			coolAdded, backoffDeadline, quotaExpiry, backoffState)
	}

	// Snapshot Bilibili API requests; while Bilibili is in waiting_backoff, Douyin must run & complete unaffected!
	biliReqsDuringBackoff := h.biliAPIReqs.Load()
	dyTask, err := b.EnqueueCacheQueue(ctx, CacheQueueRequest{
		Site: "douyin",
		List: "works",
		User: "MS4wLjABAAAA_synth_creator_k25_a",
	})
	if err != nil {
		t.Fatalf("EnqueueCacheQueue douyin: %v", err)
	}

	// Advance through Douyin's 2 inter-item waits (~90s-300s total << Bilibili's ~55m backoff); Douyin completes while Bilibili sends 0 requests!
	for i := 0; i < 2; i++ {
		dyWait, _ := h.clock.WaitForPendingTimerMax(t, 5*time.Minute, 3*time.Second)
		h.clock.Advance(dyWait)
	}
	dyDone := waitQueuePredicate(t, b, dyTask.ID, "douyin completed during bilibili backoff", func(qt CacheQueueTask) bool {
		return qt.Status == CacheQueueCompleted
	})
	if dyDone.CachedCount != 3 {
		t.Fatalf("douyin CachedCount = %d, want 3", dyDone.CachedCount)
	}
	if h.biliAPIReqs.Load() != biliReqsDuringBackoff {
		t.Fatalf("bilibili sent %d requests during waiting_backoff!", h.biliAPIReqs.Load()-biliReqsDuringBackoff)
	}

	// Advance right up to 1 second before Bilibili's backoff deadline: still ZERO Bilibili requests!
	remToBackoff := backoffDeadline.Sub(h.clock.Now())
	if remToBackoff > time.Second {
		h.clock.Advance(remToBackoff - time.Second)
	}
	time.Sleep(25 * time.Millisecond)
	if h.biliAPIReqs.Load() != biliReqsDuringBackoff {
		t.Fatalf("bilibili sent requests 1s before waiting_backoff ended! got=%d want=%d (dur=%v)",
			h.biliAPIReqs.Load(), biliReqsDuringBackoff, backoffDur)
	}

	// Advance past the final second of Bilibili waiting_backoff -> Bilibili resumes Page 2 and caches items 3 & 4!
	h.clock.Advance(2 * time.Second)
	d3, _ := h.clock.WaitForPendingTimer(t, 3*time.Second)
	h.clock.Advance(d3)
	biliDone := waitQueuePredicate(t, b, biliTask.ID, "bilibili completed after quota recovery", func(qt CacheQueueTask) bool {
		return qt.Status == CacheQueueCompleted
	})
	if biliDone.CachedCount != 4 {
		t.Fatalf("bilibili CachedCount = %d, want 4", biliDone.CachedCount)
	}

	// Part B: Risk control and rate limit on Douyin enter waiting_backoff + 10-30m and recover cleanly.
	for idx, faultMode := range []string{"risk", "ratelimit"} {
		h.dyMode.Store(faultMode)
		userSec := fmt.Sprintf("MS4wLjABAAAA_synth_creator_fault_%d", idx)
		faultTask, err := b.EnqueueCacheQueue(ctx, CacheQueueRequest{
			Site: "douyin",
			List: "works",
			User: userSec,
		})
		if err != nil {
			t.Fatalf("EnqueueCacheQueue(%s): %v", faultMode, err)
		}
		waitQueuePredicate(t, b, faultTask.ID, faultMode+" waiting_backoff", func(qt CacheQueueTask) bool {
			return qt.Status == CacheQueueWaitingBackoff
		})
		inBackoff, siteUntil := b.subs.IsSiteInBackoff("douyin")
		if !inBackoff {
			t.Fatalf("expected douyin IsSiteInBackoff=true after %s", faultMode)
		}
		_, taskResumeDeadline := h.clock.WaitForPendingTimer(t, 3*time.Second)
		cool := taskResumeDeadline.Sub(siteUntil)
		if cool < 10*time.Minute || cool > 30*time.Minute {
			t.Fatalf("%s cooldown = %v (resume=%v, siteUntil=%v), want in [10m, 30m]", faultMode, cool, taskResumeDeadline, siteUntil)
		}

		reqsBefore := h.dyAPIReqs.Load()
		h.clock.Advance(taskResumeDeadline.Sub(h.clock.Now()) - time.Second)
		time.Sleep(20 * time.Millisecond)
		if h.dyAPIReqs.Load() != reqsBefore {
			t.Fatalf("douyin sent requests during %s waiting_backoff!", faultMode)
		}

		// Clear fault and advance past deadline -> Douyin resumes and finishes all 3 items.
		h.dyMode.Store("ok")
		h.clock.Advance(2 * time.Second)
		for step := 0; step < 2; step++ {
			stepDur, _ := h.clock.WaitForPendingTimer(t, 3*time.Second)
			h.clock.Advance(stepDur)
		}
		done := waitQueuePredicate(t, b, faultTask.ID, faultMode+" recovered to completed", func(qt CacheQueueTask) bool {
			return qt.Status == CacheQueueCompleted
		})
		if done.CachedCount != 3 {
			t.Fatalf("recovered %s CachedCount = %d, want 3", faultMode, done.CachedCount)
		}
	}
}

// Restart recovers task & cursor with 1-5m delay without re-enqueuing cached items;
// auth loss pauses and re-login auto-resumes; manual pause, resume, cancel (keeping cached library items);
// duplicate join keeps a single task; schema version > 1 refused and corrupt file backed up.
func TestCacheQueueRestartAuthPauseResumeCancelAndDedupe(t *testing.T) {
	h := newQueueDualHarness(t, 5, 3, 3, 3)

	rng := func(n int) int { return min(60, n-1) }
	b, cleanup := h.openBackend(t, rng)
	ctx := context.Background()

	if _, err := b.ImportCookie(ctx, "SESSDATA=synth_bili_k26; bili_jct=csrf_k26; DedeUserID=778899", ""); err != nil {
		cleanup()
		t.Fatalf("ImportCookie: %v", err)
	}
	if _, err := b.LoginWithCookie(ctx, "douyin", "sessionid=synth_dy_k26; sid_guard=guard_k26; ttwid=ttwid_k26", ""); err != nil {
		cleanup()
		t.Fatalf("LoginWithCookie: %v", err)
	}

	runCtx, runCancel := context.WithCancel(ctx)
	b.queue.Start(runCtx)

	// 1. Deduplication & FIFO queueing on same site.
	t1, err := b.EnqueueCacheQueue(ctx, CacheQueueRequest{Site: "bilibili", List: "dynamics", User: "3001", UserName: "作者一"})
	if err != nil {
		runCancel()
		cleanup()
		t.Fatalf("EnqueueCacheQueue t1: %v", err)
	}
	t1Dup, err := b.EnqueueCacheQueue(ctx, CacheQueueRequest{Site: "bilibili", List: "dynamics", User: "3001", UserName: "作者一更新"})
	if err != nil || t1Dup.ID != t1.ID {
		runCancel()
		cleanup()
		t.Fatalf("duplicate join created new task: %+v vs %+v (err=%v)", t1Dup, t1, err)
	}
	if found, ok := b.FindCacheQueueTask("bilibili", "3001", "dynamics"); !ok || found.ID != t1.ID {
		runCancel()
		cleanup()
		t.Fatalf("FindCacheQueueTask failed: ok=%v found=%+v", ok, found)
	}

	// Second creator on same site stays queued while t1 is active.
	t2, err := b.EnqueueCacheQueue(ctx, CacheQueueRequest{Site: "bilibili", List: "dynamics", User: "3002", UserName: "作者二"})
	if err != nil {
		runCancel()
		cleanup()
		t.Fatalf("EnqueueCacheQueue t2: %v", err)
	}
	// Wait for t1 item #1 to finish and enter waiting.
	_, _ = h.clock.WaitForPendingTimer(t, 3*time.Second)
	waitQueuePredicate(t, b, t1.ID, "t1 cached 1 item", func(qt CacheQueueTask) bool {
		return qt.CachedCount == 1 && qt.Status == CacheQueueWaiting
	})
	if found2, ok := b.FindCacheQueueTask("bilibili", "3002", "dynamics"); !ok || found2.Status != CacheQueueQueued {
		runCancel()
		cleanup()
		t.Fatalf("expected t2 to stay queued while t1 owns bilibili slot, got %+v", found2)
	}

	// 2. Manual Pause on t2, and Auth Logout on Bilibili:
	// Manually pause t2 so we verify re-login auto-resumes auth_required tasks (t1) WITHOUT resuming user-paused tasks (t2)!
	if _, err := b.PauseCacheQueue(t2.ID); err != nil {
		runCancel()
		cleanup()
		t.Fatalf("PauseCacheQueue t2: %v", err)
	}
	if _, err := b.LogoutSite("bilibili"); err != nil {
		runCancel()
		cleanup()
		t.Fatalf("LogoutSite bilibili: %v", err)
	}
	p1 := waitQueuePredicate(t, b, t1.ID, "t1 paused for auth_required", func(qt CacheQueueTask) bool {
		return qt.Status == CacheQueuePaused && qt.PauseReason == "auth_required"
	})
	if !strings.Contains(p1.StatusText, "登录") {
		runCancel()
		cleanup()
		t.Fatalf("expected login reason in StatusText, got %+v", p1)
	}

	// Re-login Bilibili -> t1 auto-resumes, while user-paused t2 stays paused!
	if _, err := b.ImportCookie(ctx, "SESSDATA=synth_bili_k26_relogin; bili_jct=csrf_k26_2; DedeUserID=778899", ""); err != nil {
		runCancel()
		cleanup()
		t.Fatalf("re-ImportCookie: %v", err)
	}
	// t1 immediately caches item #2 and arms timer!
	_, _ = h.clock.WaitForPendingTimer(t, 3*time.Second)
	waitQueuePredicate(t, b, t1.ID, "t1 auto-resumed and cached 2nd item", func(qt CacheQueueTask) bool {
		return qt.CachedCount == 2 && qt.Status == CacheQueueWaiting
	})
	if found2, _ := b.FindCacheQueueTask("bilibili", "3002", "dynamics"); found2.Status != CacheQueuePaused || found2.PauseReason != "user" {
		runCancel()
		cleanup()
		t.Fatalf("user-paused t2 should remain paused after re-login, got %+v", found2)
	}
	// Cancel t2 so we focus on t1 restart recovery.
	if _, err := b.CancelCacheQueue(t2.ID); err != nil {
		runCancel()
		cleanup()
		t.Fatalf("CancelCacheQueue t2: %v", err)
	}

	// 3. App Exit (PauseForExit + Close) and Restart Recovery (1-5m delay, cursor & already-cached items not re-enqueued):
	b.PauseForExit()
	runCancel()
	cleanup()

	// Re-open Backend on exact same Locations!
	b2, cleanup2 := h.openBackend(t, rng)
	defer cleanup2()
	runCtx2, runCancel2 := context.WithCancel(ctx)
	defer runCancel2()
	b2.queue.Start(runCtx2)

	recovered := waitQueuePredicate(t, b2, t1.ID, "t1 waiting startup delay", func(qt CacheQueueTask) bool {
		return qt.Status == CacheQueueWaiting && qt.CachedCount == 2
	})
	startupDur, startupDeadline := h.clock.WaitForPendingTimer(t, 3*time.Second)
	if startupDur < time.Minute || startupDur > 5*time.Minute {
		t.Fatalf("startup delay = %v, want in [1m, 5m]; task=%+v", startupDur, recovered)
	}

	// Before startup delay finishes, 0 Bilibili API requests occur.
	reqsBeforeStartup := h.biliAPIReqs.Load()
	h.clock.Advance(startupDeadline.Sub(h.clock.Now()) - time.Second)
	time.Sleep(20 * time.Millisecond)
	if h.biliAPIReqs.Load() != reqsBeforeStartup {
		t.Fatalf("requests fired before 1-5m startup delay elapsed!")
	}

	// Cross startup delay -> t1 skips already-cached items #1 & #2 without re-enqueuing them, caches #3, #4, #5, and completes!
	h.clock.Advance(2 * time.Second)
	for step := 0; step < 2; step++ {
		d, _ := h.clock.WaitForPendingTimer(t, 3*time.Second)
		h.clock.Advance(d)
	}
	done1 := waitQueuePredicate(t, b2, t1.ID, "t1 completed all 5 items after restart", func(qt CacheQueueTask) bool {
		return qt.Status == CacheQueueCompleted
	})
	if done1.CachedCount != 5 || done1.SkippedCount != 0 {
		t.Fatalf("after restart: CachedCount=%d SkippedCount=%d, want 5 and 0", done1.CachedCount, done1.SkippedCount)
	}

	// Verify download_tasks in DB only has 5 completed tasks for creator 3001 (items #1 & #2 were NEVER re-enqueued).
	dlTasks, err := b2.DownloadManager().ListTasks()
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(dlTasks) != 5 {
		t.Fatalf("expected 5 total download tasks (no duplicate enqueue on restart), got %d", len(dlTasks))
	}

	// 4. Manual Pause, Resume, and Cancel (preserving already-cached items in library) + Restart after Cancel:
	dyQ, err := b2.EnqueueCacheQueue(ctx, CacheQueueRequest{
		Site: "douyin",
		List: "works",
		User: "MS4wLjABAAAA_synth_cancel_test",
	})
	if err != nil {
		t.Fatalf("EnqueueCacheQueue douyin: %v", err)
	}
	_, _ = h.clock.WaitForPendingTimer(t, 3*time.Second)
	waitQueuePredicate(t, b2, dyQ.ID, "douyin cached 1st item", func(qt CacheQueueTask) bool {
		return qt.CachedCount == 1 && qt.Status == CacheQueueWaiting
	})

	// Pause, verify clock advance does nothing, then Resume, then Cancel!
	if _, err := b2.PauseCacheQueue(dyQ.ID); err != nil {
		t.Fatalf("PauseCacheQueue: %v", err)
	}
	h.clock.Advance(10 * time.Minute)
	if cur, _ := b2.FindCacheQueueTask("douyin", "MS4wLjABAAAA_synth_cancel_test", "works"); cur.CachedCount != 1 || cur.Status != CacheQueuePaused {
		t.Fatalf("paused task progressed unexpectedly: %+v", cur)
	}
	if _, err := b2.ResumeCacheQueue(dyQ.ID); err != nil {
		t.Fatalf("ResumeCacheQueue: %v", err)
	}
	_, _ = h.clock.WaitForPendingTimer(t, 3*time.Second)
	waitQueuePredicate(t, b2, dyQ.ID, "douyin cached 2nd item after resume", func(qt CacheQueueTask) bool {
		return qt.CachedCount == 2
	})

	canceled, err := b2.CancelCacheQueue(dyQ.ID)
	if err != nil || canceled.Status != CacheQueueStopped {
		t.Fatalf("CancelCacheQueue: %+v err=%v", canceled, err)
	}
	if _, stillActive := b2.FindCacheQueueTask("douyin", "MS4wLjABAAAA_synth_cancel_test", "works"); stillActive {
		t.Fatal("canceled task still reported as active by FindCacheQueueTask")
	}
	// Already-cached Douyin items #1 and #2 MUST still exist in local library!
	for _, key := range []string{"douyin:dynamic:7400000000000000001", "douyin:dynamic:7400000000000000002"} {
		item, exists, gErr := b2.GetLibraryItem(key)
		if gErr != nil || !exists || item.ItemDir == "" {
			t.Fatalf("CancelCacheQueue deleted or lost cached library item %s: exists=%v err=%v", key, exists, gErr)
		}
		if _, statErr := os.Stat(item.ItemDir); statErr != nil {
			t.Fatalf("cached item directory missing after CancelCacheQueue: %v", statErr)
		}
	}

	// Clicking "全部缓存" again after cancel restarts cleanly without creating duplicate entries!
	restarted, err := b2.EnqueueCacheQueue(ctx, CacheQueueRequest{
		Site: "douyin",
		List: "works",
		User: "MS4wLjABAAAA_synth_cancel_test",
	})
	if err != nil || restarted.ID != dyQ.ID {
		t.Fatalf("restart after cancel failed: %+v err=%v", restarted, err)
	}
	doneDy := waitQueuePredicate(t, b2, restarted.ID, "restarted douyin completed", func(qt CacheQueueTask) bool {
		return qt.Status == CacheQueueCompleted
	})
	if doneDy.CachedCount != 1 || doneDy.SkippedCount != 2 {
		t.Fatalf("restarted douyin counts: Cached=%d Skipped=%d, want 1 and 2", doneDy.CachedCount, doneDy.SkippedCount)
	}
	dyCount := 0
	for _, item := range b2.ListCacheQueue() {
		if item.Site == "douyin" && item.User == "MS4wLjABAAAA_synth_cancel_test" {
			dyCount++
		}
	}
	if dyCount != 1 {
		t.Fatalf("expected 1 queue record after restart, got %d", dyCount)
	}

	// 4b. Finished queues can be removed from the task list (DEC-057); their
	// cached items stay, an unfinished queue is refused, and the removal is
	// on disk.
	before := len(b2.ListCacheQueue())
	if err := b2.RemoveCacheQueue(restarted.ID); err != nil {
		t.Fatalf("RemoveCacheQueue completed: %v", err)
	}
	if _, found := b2.FindCacheQueueTask("douyin", "MS4wLjABAAAA_synth_cancel_test", "works"); found || len(b2.ListCacheQueue()) != before-1 {
		t.Fatalf("removed queue still listed: %+v", b2.ListCacheQueue())
	}
	if _, exists, _ := b2.GetLibraryItem("douyin:dynamic:7400000000000000001"); !exists {
		t.Fatal("removing a queue deleted its cached item")
	}
	if raw, _ := os.ReadFile(filepath.Join(b2.Locations().DataDir, "cache-queue.json")); strings.Contains(string(raw), restarted.ID) {
		t.Fatal("removed queue is still in cache-queue.json")
	}
	if err := b2.RemoveCacheQueue(restarted.ID); err == nil {
		t.Fatal("removing an unknown queue succeeded")
	}
	if n, err := b2.RemoveFinishedCacheQueues(); err != nil || n != before-1 {
		t.Fatalf("RemoveFinishedCacheQueues = %d, %v; want the %d other finished queues", n, err, before-1)
	}
	if left := b2.ListCacheQueue(); len(left) != 0 {
		t.Fatalf("finished queues left: %+v", left)
	}

	// 5. Schema version > 1 refused without overwriting, and corrupt JSON backed up on startup.
	locNewer := testLocations(t)
	newerPath := filepath.Join(locNewer.DataDir, "cache-queue.json")
	_ = os.MkdirAll(locNewer.DataDir, 0o700)
	newerBytes := []byte(`{"version":99,"tasks":[]}`)
	if err := os.WriteFile(newerPath, newerBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	_, newerErr := NewBackend(Options{Locations: locNewer, Desktop: NewMockDesktop(), DisableBgWorkers: true})
	var ndErr *storage.NewerDataError
	if !errors.As(newerErr, &ndErr) || ndErr.File != "cache-queue.json" {
		t.Fatalf("expected NewerDataError for cache-queue.json v99, got %v", newerErr)
	}
	afterRaw, _ := os.ReadFile(newerPath)
	if string(afterRaw) != string(newerBytes) {
		t.Fatalf("v99 cache-queue.json was modified: %s", string(afterRaw))
	}

	locCorrupt := testLocations(t)
	_ = os.MkdirAll(locCorrupt.DataDir, 0o700)
	if err := os.WriteFile(filepath.Join(locCorrupt.DataDir, "cache-queue.json"), []byte(`{"version":1,"tasks":[broken`), 0o600); err != nil {
		t.Fatal(err)
	}
	bCorrupt, cErr := NewBackend(Options{Locations: locCorrupt, Desktop: NewMockDesktop(), DisableBgWorkers: true})
	if cErr != nil {
		t.Fatalf("corrupt cache-queue.json was not recovered: %v", cErr)
	}
	defer bCorrupt.Close()
	if len(bCorrupt.DiagnosticsSnapshot().CorruptRecovered) == 0 {
		t.Fatal("expected corrupt backup recorded in DiagnosticsSnapshot().CorruptRecovered")
	}
}

// Privacy sentinel check verifying cache-queue.json, logs, and events contain zero
// Cookies, signed URLs, or query strings.
func TestCacheQueuePrivacySentinel(t *testing.T) {
	h := newQueueDualHarness(t, 2, 2, 2, 2)
	b, cleanup := h.openBackend(t, func(int) int { return 0 })
	defer cleanup()

	ctx := context.Background()
	const (
		biliSecretCookie = "SYNTH_BILI_COOKIE_QUEUE_SENTINEL_01"
		biliSecretCSRF   = "SYNTH_BILI_CSRF_QUEUE_SENTINEL_01"
		dySecretSession  = "SYNTH_DY_SESSION_QUEUE_SENTINEL_01"
		dySecretTTWID    = "SYNTH_DY_TTWID_QUEUE_SENTINEL_01"
		signedAvatarQS   = "SYNTH_AVATAR_SIGNED_QUERY_01"
		poisonTitleQS    = "SYNTH_TITLE_SIGNED_TOKEN_01"
	)

	if _, err := b.ImportCookie(ctx, "SESSDATA="+biliSecretCookie+"; bili_jct="+biliSecretCSRF+"; DedeUserID=778899", ""); err != nil {
		t.Fatalf("ImportCookie: %v", err)
	}
	if _, err := b.LoginWithCookie(ctx, "douyin", "sessionid="+dySecretSession+"; sid_guard=guard_sentinel; ttwid="+dySecretTTWID, ""); err != nil {
		t.Fatalf("LoginWithCookie: %v", err)
	}

	var liveEvents []Event
	var evMu sync.Mutex
	unsub := b.SubscribeEvents(func(ev Event) {
		evMu.Lock()
		liveEvents = append(liveEvents, ev)
		evMu.Unlock()
	})
	defer unsub()

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	b.queue.Start(runCtx)

	tBili, err := b.EnqueueCacheQueue(ctx, CacheQueueRequest{
		Site:      "bilibili",
		List:      "dynamics",
		User:      "9001",
		UserName:  "测试UP https://cdn.example.invalid/u?token=" + poisonTitleQS + " SESSDATA=" + biliSecretCookie,
		AvatarURL: h.biliSrv.URL + "/bfs/face/up.png?upsig=" + signedAvatarQS,
	})
	if err != nil {
		t.Fatalf("EnqueueCacheQueue bilibili: %v", err)
	}
	tDy, err := b.EnqueueCacheQueue(ctx, CacheQueueRequest{
		Site:      "douyin",
		List:      "works",
		User:      "MS4wLjABAAAA_synth_privacy_sentinel",
		UserName:  "抖音作者 https://cdn.example.invalid/d?x-expires=" + poisonTitleQS,
		AvatarURL: h.dySrv.URL + "/obj/avatar.png?x-expires=" + signedAvatarQS + "&x-signature=SYNTH_SIG_AVATAR",
	})
	if err != nil {
		t.Fatalf("EnqueueCacheQueue douyin: %v", err)
	}

	// Both sites cache 2 items (1 inter-item timer per site).
	for step := 0; step < 2; step++ {
		if h.clock.ActiveTimerCount() == 0 {
			_, _ = h.clock.WaitForPendingTimer(t, 3*time.Second)
		}
		h.clock.Advance(45 * time.Second)
	}
	waitQueuePredicate(t, b, tBili.ID, "bili completed", func(qt CacheQueueTask) bool {
		return qt.Status == CacheQueueCompleted
	})
	waitQueuePredicate(t, b, tDy.ID, "douyin completed", func(qt CacheQueueTask) bool {
		return qt.Status == CacheQueueCompleted
	})

	forbiddenTokens := []string{
		biliSecretCookie,
		biliSecretCSRF,
		dySecretSession,
		dySecretTTWID,
		signedAvatarQS,
		poisonTitleQS,
		"SYNTH_EXP_",
		"SYNTH_SIG_",
		"SYNTH_UP_FACE_SIG",
		"SYNTH_DY_EXP",
		"SYNTH_DY_SIG",
		"SYNTH_DY_AVATAR_EXP",
		"SESSDATA=",
		"sessionid=",
		"x-expires=",
		"x-signature=",
		"upsig=",
	}

	// 1. Inspect cache-queue.json on disk.
	queuePath := filepath.Join(h.loc.DataDir, "cache-queue.json")
	rawJSON, err := os.ReadFile(queuePath)
	if err != nil {
		t.Fatalf("read cache-queue.json: %v", err)
	}
	jsonText := string(rawJSON)
	var parsed cacheQueueFile
	if err := json.Unmarshal(rawJSON, &parsed); err != nil || parsed.Version != cacheQueueSchemaVersion {
		t.Fatalf("invalid cache-queue.json schema: version=%d err=%v", parsed.Version, err)
	}
	for _, tok := range append([]string{"http://", "https://", "?"}, forbiddenTokens...) {
		if strings.Contains(jsonText, tok) {
			t.Fatalf("privacy leak in cache-queue.json: found %q in:\n%s", tok, jsonText)
		}
	}

	// 2. Inspect RecentLogs and raw trace.log.
	lines, err := b.RecentLogs(500)
	if err != nil {
		t.Fatalf("RecentLogs: %v", err)
	}
	joinedLogs := strings.Join(lines, "\n")
	rawLogBytes, _ := os.ReadFile(filepath.Join(h.loc.LogsDir, "trace.log"))
	allLogs := joinedLogs + "\n" + string(rawLogBytes)
	for _, tok := range append([]string{"?"}, forbiddenTokens...) {
		if strings.Contains(allLogs, tok) {
			t.Fatalf("privacy leak in logs: found %q", tok)
		}
	}

	// 3. Inspect DiagnosticsSnapshot().RecentEvents and live subscribed events.
	evMu.Lock()
	allEvents := append([]Event(nil), liveEvents...)
	evMu.Unlock()
	allEvents = append(allEvents, b.DiagnosticsSnapshot().RecentEvents...)
	eventsBytes, _ := json.Marshal(allEvents)
	eventsText := string(eventsBytes)
	for _, tok := range append([]string{"http://", "https://", "?"}, forbiddenTokens...) {
		if strings.Contains(eventsText, tok) {
			t.Fatalf("privacy leak in events: found %q in %s", tok, eventsText)
		}
	}
}

func TestLiveRecordingPreviewPassthrough(t *testing.T) {
	biliRS := live.RoomState{
		Site:             "bilibili",
		UID:              12345,
		RoomID:           889900,
		Uname:            "主播甲",
		Title:            "测试直播间",
		Transport:        "hls_fmp4",
		Recording:        true,
		RecordPartPath:   filepath.Join(t.TempDir(), "part-001.mp4.part"),
		RecordPartIndex:  1,
		RecordPartBytes:  65536,
		RecordPartReady:  true,
		PreviewSupported: true,
	}
	biliDTO := mapRoomToAccountState(biliRS)
	if !biliDTO.PreviewSupported || !biliDTO.RecordPartReady || biliDTO.RecordPartIndex != 1 || biliDTO.RecordPartBytes != 65536 || biliDTO.RecordPartPath != biliRS.RecordPartPath {
		t.Fatalf("unexpected Bilibili LiveAccountState preview fields: %+v", biliDTO)
	}

	dyRS := live.RoomState{
		Site:             "douyin",
		AccountUID:       "MS4wLjABAAAA_synth_live",
		RoomIDStr:        "7300112233",
		WebRID:           "667788",
		Transport:        "http_flv",
		AudioOnly:        true,
		Recording:        true,
		PreviewSupported: false,
		PreviewReason:    "这场录制暂不支持实时预览",
	}
	dyDTO := mapRoomToAccountState(dyRS)
	if !dyDTO.AudioOnly || dyDTO.PreviewSupported || dyDTO.PreviewReason != "这场录制暂不支持实时预览" {
		t.Fatalf("unexpected Douyin LiveAccountState preview fields: %+v", dyDTO)
	}
}
