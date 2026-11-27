package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/site/weibo"
	"github.com/chialecode/trace/internal/subscription"
)

type dec089RTFunc func(*http.Request) (*http.Response, error)

func (f dec089RTFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestWeiboDedicatedQueuePacingAndQuotaCooldown(t *testing.T) {
	m, _, err := newCacheQueueManager(nil, "", nil, func(int) int { return 0 }, nil)
	if err != nil {
		t.Fatalf("newCacheQueueManager: %v", err)
	}
	wbPacing := m.pacingForSite(site.Weibo)
	if wbPacing.ItemMinDelay != 20*time.Second || wbPacing.ItemMaxDelay != 45*time.Second ||
		wbPacing.ZeroAPIItemMinDelay != 8*time.Second || wbPacing.ZeroAPIItemMaxDelay != 20*time.Second ||
		wbPacing.LongRestMinItems != 30 || wbPacing.LongRestMaxItems != 50 ||
		wbPacing.LongRestMinDelay != 3*time.Minute || wbPacing.LongRestMaxDelay != 8*time.Minute ||
		wbPacing.BackoffCoolMinDelay != 2*time.Minute || wbPacing.BackoffCoolMaxDelay != 5*time.Minute ||
		wbPacing.QuotaCoolMinDelay != 15*time.Second || wbPacing.QuotaCoolMaxDelay != 60*time.Second ||
		wbPacing.StartupMinDelay != 15*time.Second || wbPacing.StartupMaxDelay != 60*time.Second {
		t.Fatalf("unexpected Weibo pacing: %+v", wbPacing)
	}
	for _, other := range []site.ID{site.Bilibili, site.Douyin, site.X} {
		p := m.pacingForSite(other)
		if p.ItemMinDelay != 40*time.Second || p.ItemMaxDelay != 150*time.Second ||
			p.BackoffCoolMinDelay != 10*time.Minute || p.BackoffCoolMaxDelay != 30*time.Minute ||
			p.QuotaCoolMinDelay != 15*time.Second || p.QuotaCoolMaxDelay != 60*time.Second {
			t.Fatalf("unexpected pacing for %s: %+v", other, p)
		}
	}

	baseNow := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	clk := newFakeQueueClock(baseNow)
	m.clock = clk
	e := &cacheQueueEntry{
		rec: cacheQueueRecord{
			ID:            "cq_wb_pacing",
			Site:          string(site.Weibo),
			User:          "1000000001",
			List:          "posts",
			Status:        CacheQueueRunning,
			RestThreshold: 40,
		},
		rt: cacheQueueRuntime{
			lastItemZeroAPI: true,
		},
	}
	recZero := m.finishItemOutcomeLocked(e, true)
	nextZero, _ := time.Parse(time.RFC3339, recZero.NextRunAt)
	if d := nextZero.Sub(baseNow); d != 8*time.Second {
		t.Fatalf("Weibo 0-API item delay (rng=0) = %v, want 8s", d)
	}
	recAPI := m.finishItemOutcomeLocked(e, true)
	nextAPI, _ := time.Parse(time.RFC3339, recAPI.NextRunAt)
	if d := nextAPI.Sub(baseNow); d != 20*time.Second {
		t.Fatalf("Weibo JSON-API item delay (rng=0) = %v, want 20s", d)
	}
}

func TestWeiboSingleAccountUncertainEmptyAndMutedDoNotTripSiteBackoff(t *testing.T) {
	const (
		uidMuted     = "500000071"
		uidUncertain = "500000072"
		uidHealthy   = "500000073"
		midHealthy   = "5000000000000731"
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/ajax/setting/getBasicInfo":
			if !strings.Contains(r.Header.Get("Cookie"), "SUB=") {
				_, _ = w.Write([]byte(`{"ok":-100,"msg":"login required"}`))
				return
			}
			_, _ = w.Write([]byte(`{"ok":1,"data":{"uid":"500000100","screen_name":"自检测试号"}}`))
		case "/ajax/profile/info":
			uid := r.URL.Query().Get("uid")
			screenName := r.URL.Query().Get("screen_name")
			switch {
			case uid == "500000100" || screenName == "自检测试号":
				_, _ = w.Write([]byte(`{"ok":1,"data":{"blockText":"","user":{"idstr":"500000100","screen_name":"自检测试号","statuses_count":5,"is_muteuser":false}}}`))
			case uid == uidMuted:
				_, _ = w.Write([]byte(`{"ok":1,"data":{"blockText":"","user":{"idstr":"` + uidMuted + `","screen_name":"禁言博主","statuses_count":200,"is_muteuser":true}}}`))
			case uid == uidUncertain:
				_, _ = w.Write([]byte(`{"ok":1,"data":{"blockText":"","user":{"idstr":"` + uidUncertain + `","screen_name":"私密博主","statuses_count":150,"is_muteuser":false}}}`))
			default:
				_, _ = w.Write([]byte(`{"ok":1,"data":{"blockText":"","user":{"idstr":"` + uidHealthy + `","screen_name":"正常博主","statuses_count":1,"is_muteuser":false}}}`))
			}
		case "/ajax/statuses/mymblog":
			uid := r.URL.Query().Get("uid")
			switch uid {
			case uidMuted, uidUncertain:
				_, _ = w.Write([]byte(`{"ok":1,"data":{"list":[],"since_id":"","total":150,"bottom_tips_visible":false,"bottom_tips_text":""}}`))
			case uidHealthy:
				_, _ = w.Write([]byte(`{"ok":1,"data":{"list":[{"idstr":"` + midHealthy + `","mid":"` + midHealthy + `","mblogid":"Qhealthy01","created_at":"Sat Nov 21 15:00:00 +0800 2026","text_raw":"正常原创微博正文","text":"正常原创微博正文","user":{"idstr":"` + uidHealthy + `","screen_name":"正常博主"}}],"since_id":"","total":1,"bottom_tips_visible":false,"bottom_tips_text":""}}`))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	loc := testLocations(t)
	wbEndpoints := weibo.DefaultEndpoints()
	wbEndpoints.PCBasicInfo = srv.URL + "/ajax/setting/getBasicInfo"
	wbEndpoints.ProfileInfo = srv.URL + "/ajax/profile/info"
	wbEndpoints.StatusesMyMblog = srv.URL + "/ajax/statuses/mymblog"
	wbEndpoints.StatusesShow = srv.URL + "/ajax/statuses/show"
	wbEndpoints.StatusesLong = srv.URL + "/ajax/statuses/longtext"

	b, err := NewBackend(Options{
		Locations:        loc,
		Desktop:          NewMockDesktop(),
		DisableBgWorkers: true,
		CacheQueuePacing: fastTestQueuePacing(),
		WeiboOpts: weibo.ClientOptions{
			Endpoints:      wbEndpoints,
			AllowLocalhost: true,
			MinInterval:    -1,
			JitterMax:      -1,
		},
	})
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	defer b.Close()

	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	if err := b.dl.Start(runCtx); err != nil {
		t.Fatalf("dl.Start: %v", err)
	}
	b.queue.Start(runCtx)

	ctx := context.Background()
	if _, err := b.LoginWithCookie(ctx, "weibo", "SUB=_2A25SynthMutedTest; SUBP=0033Synth; XSRF-TOKEN=xsrf_synth", ""); err != nil {
		t.Fatalf("LoginWithCookie(weibo): %v", err)
	}

	qMuted, err := b.EnqueueCacheQueue(ctx, CacheQueueRequest{Site: "weibo", List: "posts", User: uidMuted, UserName: "禁言博主"})
	if err != nil {
		t.Fatal(err)
	}
	qUncertain, err := b.EnqueueCacheQueue(ctx, CacheQueueRequest{Site: "weibo", List: "posts", User: uidUncertain, UserName: "私密博主"})
	if err != nil {
		t.Fatal(err)
	}
	qHealthy, err := b.EnqueueCacheQueue(ctx, CacheQueueRequest{Site: "weibo", List: "posts", User: uidHealthy, UserName: "正常博主"})
	if err != nil {
		t.Fatal(err)
	}

	doneMuted := waitQueueTerminal(t, b, qMuted.ID)
	doneUncertain := waitQueueTerminal(t, b, qUncertain.ID)
	doneHealthy := waitQueueTerminal(t, b, qHealthy.ID)

	if doneMuted.Status != CacheQueueStopped || !strings.Contains(doneMuted.StopReason, "禁言") {
		t.Fatalf("muted queue should stop cleanly with 禁言 reason, got %+v", doneMuted)
	}
	if doneUncertain.Status != CacheQueueStopped {
		t.Fatalf("uncertain-empty queue should stop cleanly (not waiting_backoff), got %+v", doneUncertain)
	}
	if doneHealthy.Status != CacheQueueCompleted || doneHealthy.CachedCount != 1 {
		t.Fatalf("healthy Weibo queue must run and complete unaffected after muted/uncertain accounts, got %+v", doneHealthy)
	}
	if inBackoff, _ := b.subs.IsSiteInBackoff("weibo"); inBackoff {
		t.Fatal("single-account muted/uncertain-empty must NEVER put Weibo into site-wide backoff")
	}

	openRes, err := b.OpenLink(ctx, "https://weibo.com/u/"+uidMuted)
	if err != nil {
		t.Fatalf("OpenLink(muted): %v", err)
	}
	if openRes.WeiboUser == nil || !openRes.WeiboUser.IsMuteUser || openRes.WeiboUserPostsError != "" {
		t.Fatalf("expected IsMuteUser=true with empty WeiboUserPostsError, got user=%+v postsErr=%q", openRes.WeiboUser, openRes.WeiboUserPostsError)
	}
}

func TestWeiboBackfillTierBIsolatedFromJSONAPIBackoffAndOptional418(t *testing.T) {
	pngBytes := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
		0xde, 0x00, 0x00, 0x00, 0x0c, 0x49, 0x44, 0x41,
		0x54, 0x08, 0xd7, 0x63, 0xf8, 0xcf, 0xc0, 0x00,
		0x00, 0x03, 0x01, 0x01, 0x00, 0x18, 0xdd, 0x8d,
		0xb0, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e,
		0x44, 0xae, 0x42, 0x60, 0x82,
	}
	var oslarge418Hits atomic.Int32
	var oslargeOKHits atomic.Int32
	cdnRT := dec089RTFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(r.URL.Path, "pic418"):
			oslarge418Hits.Add(1)
			return &http.Response{
				StatusCode: http.StatusTeapot,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    r,
			}, nil
		case strings.Contains(r.URL.Path, "picok"):
			oslargeOKHits.Add(1)
			hdr := make(http.Header)
			hdr.Set("Content-Type", "image/png")
			return &http.Response{
				StatusCode:    http.StatusOK,
				ContentLength: int64(len(pngBytes)),
				Header:        hdr,
				Body:          io.NopCloser(bytes.NewReader(pngBytes)),
				Request:       r,
			}, nil
		default:
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    r,
			}, nil
		}
	})

	biliSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"isLogin":true,"mid":778899,"uname":"B站号","face":"https://i0.hdslb.com/bfs/face/synth.jpg","wbi_img":{"img_url":"https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png","sub_url":"https://i0.hdslb.com/bfs/wbi/4932caff0ff746eab6f01bf08b70ac45.png"}}}`))
	}))
	defer biliSrv.Close()

	loc := testLocations(t)
	wbEndpoints := weibo.DefaultEndpoints()
	pacing := fastTestQueuePacing()
	pacing.FastCDNMinDelay = time.Millisecond
	pacing.FastCDNMaxDelay = time.Millisecond
	b, err := NewBackend(Options{
		Locations:        loc,
		Desktop:          NewMockDesktop(),
		DisableBgWorkers: true,
		CacheQueuePacing: pacing,
		BilibiliOpts: bilibili.ClientOptions{
			Endpoints: bilibili.Endpoints{
				APIBase:      biliSrv.URL,
				PassportBase: biliSrv.URL,
				WWWBase:      biliSrv.URL,
				SpaceBase:    biliSrv.URL,
				DynamicBase:  biliSrv.URL,
				LiveAPIBase:  biliSrv.URL,
				LiveWebBase:  biliSrv.URL,
			},
			PacingInterval: time.Millisecond,
			AllowLocalhost: true,
		},
		WeiboOpts: weibo.ClientOptions{
			Endpoints:      wbEndpoints,
			AllowLocalhost: true,
			MinInterval:    -1,
			JitterMax:      -1,
		},
	})
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	defer b.Close()
	b.dl.SetMediaTransportWrap(func(_ http.RoundTripper) http.RoundTripper { return cdnRT })

	ctx := context.Background()
	if _, err := b.ImportCookie(ctx, "SESSDATA=s; bili_jct=j; DedeUserID=778899", ""); err != nil {
		t.Fatalf("ImportCookie bilibili: %v", err)
	}

	seedWeiboStaticItemForBackfill(t, b, "500000080", "5000000000000801", "pic418", []byte("primary-bytes-418"), 2, nil, "", nil, nil)
	seedWeiboStaticItemForBackfill(t, b, "500000080", "5000000000000802", "picok", []byte("primary-bytes-ok"), 2, nil, "", nil, nil)

	// Put Weibo JSON API into active risk_control backoff BEFORE starting Tier B backfill!
	b.subs.RecordSiteAutomaticStop("weibo", site.ErrKindRiskControl)
	origState := b.subs.GetSiteSchedulerState("weibo")

	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	b.queue.Start(runCtx)
	qTask, err := b.StartWeiboBackfill(ctx)
	if err != nil {
		t.Fatalf("StartWeiboBackfill: %v", err)
	}
	done := waitQueueTerminal(t, b, qTask.ID)
	if done.Status != CacheQueueCompleted {
		t.Fatalf("Tier B backfill must complete even during Weibo JSON API backoff, got %+v", done)
	}
	if oslarge418Hits.Load() < 1 || oslargeOKHits.Load() < 1 {
		t.Fatalf("expected both /oslarge/ requests executed: 418=%d ok=%d", oslarge418Hits.Load(), oslargeOKHits.Load())
	}
	it418, _, _ := b.lib.GetItem("weibo:dynamic:5000000000000801")
	if it418.CacheStandardVersion >= library.WeiboCacheStandardVersion {
		t.Fatalf("item hitting /oslarge/ 418 must not be stamped v3: %+v", it418)
	}
	itOK, _, _ := b.lib.GetItem("weibo:dynamic:5000000000000802")
	if itOK.CacheStandardVersion != library.WeiboCacheStandardVersion || len(itOK.NotePages) == 0 || itOK.NotePages[0].NoWatermark == "" {
		t.Fatalf("item with 200 OK /oslarge/ must be stamped v3 with companion: %+v", itOK)
	}
	afterState := b.subs.GetSiteSchedulerState("weibo")
	if afterState.BackoffLevel != origState.BackoffLevel {
		t.Fatalf("Tier B /oslarge/ 418 escalated Weibo BackoffLevel: before=%d after=%d", origState.BackoffLevel, afterState.BackoffLevel)
	}
}

func TestManualProbeRecoveryAcrossOpenLinkQueueAndSettings(t *testing.T) {
	h := newWeiboThreeSiteHarness(t, func(o *Options) {
		o.CacheQueuePacing = fastTestQueuePacing()
	})
	runCtx, cancelRun := context.WithCancel(context.Background())
	if err := h.backend.dl.Start(runCtx); err != nil {
		cancelRun()
		t.Fatalf("dl.Start: %v", err)
	}
	h.backend.queue.Start(runCtx)
	t.Cleanup(cancelRun)

	ctx := context.Background()
	if _, err := h.backend.LoginWithCookie(ctx, "weibo", synthWeiboCookie(), ""); err != nil {
		t.Fatalf("LoginWithCookie(weibo): %v", err)
	}

	// 1. Trigger Weibo backoff, then probe via Settings ProbeSiteBackoff while still 418 -> preserves backoff without doubling!
	h.wbMode.Store("risk418")
	h.backend.subs.RecordSiteAutomaticStop("weibo", site.ErrKindRiskControl)
	origLevel := h.backend.subs.GetSiteSchedulerState("weibo").BackoffLevel

	stFail, err := h.backend.ProbeSiteBackoff(ctx, "weibo")
	if !site.IsKind(err, site.ErrKindRiskControl) || !strings.Contains(err.Error(), "探测仍处于风控中") {
		t.Fatalf("ProbeSiteBackoff while 418 = (%+v, %v), want wrapped risk_control", stFail, err)
	}
	if !stFail.BackingOff {
		t.Fatalf("expected BackingOff=true after failed probe, got %+v", stFail)
	}
	if got := h.backend.subs.GetSiteSchedulerState("weibo").BackoffLevel; got != origLevel {
		t.Fatalf("failed ProbeSiteBackoff escalated BackoffLevel: got %d, want %d", got, origLevel)
	}

	// 2. Enqueue a Weibo cache queue while Weibo is in backoff -> enters waiting_backoff automatically!
	qTask, err := h.backend.EnqueueCacheQueue(ctx, CacheQueueRequest{
		Site:     "weibo",
		List:     "posts",
		User:     synthWeiboUID,
		UserName: synthWeiboName,
	})
	if err != nil {
		t.Fatalf("EnqueueCacheQueue: %v", err)
	}
	waitQueuePredicate(t, h.backend, qTask.ID, "weibo queue enters waiting_backoff", func(qt CacheQueueTask) bool {
		return qt.Status == CacheQueueWaitingBackoff
	})

	// 3. Click "立即重试" (ResumeCacheQueue on waiting_backoff) while still 418 -> sends 1 probe, returns to waiting_backoff without doubling BackoffLevel!
	reqsBefore := h.wbReqs.Load()
	if _, err := h.backend.ResumeCacheQueue(qTask.ID); err != nil {
		t.Fatalf("ResumeCacheQueue(waiting_backoff while 418): %v", err)
	}
	waitQueuePredicate(t, h.backend, qTask.ID, "weibo queue returns to waiting_backoff after failed probe", func(qt CacheQueueTask) bool {
		return qt.Status == CacheQueueWaitingBackoff && strings.Contains(qt.StopReason, "探测仍处于风控或限流中")
	})
	if h.wbReqs.Load() != reqsBefore+1 {
		t.Fatalf("expected exactly 1 probe request from ResumeCacheQueue, got %d", h.wbReqs.Load()-reqsBefore)
	}
	if got := h.backend.subs.GetSiteSchedulerState("weibo").BackoffLevel; got != origLevel {
		t.Fatalf("failed queue manual probe escalated BackoffLevel: got %d, want %d", got, origLevel)
	}

	// 4. Now Weibo recovers: Settings ProbeSiteBackoff succeeds, clears backoff, and IMMEDIATELY wakes the waiting_backoff queue to completion!
	h.wbMode.Store("ok")
	stOK, err := h.backend.ProbeSiteBackoff(ctx, "weibo")
	if err != nil || stOK.BackingOff {
		t.Fatalf("ProbeSiteBackoff after recovery = (%+v, %v), want BackingOff=false nil err", stOK, err)
	}
	done := waitQueueTerminal(t, h.backend, qTask.ID)
	if done.Status != CacheQueueCompleted {
		t.Fatalf("ProbeSiteBackoff recovery must wake waiting_backoff queue to completion, got %+v", done)
	}

	// 5. Re-enter backoff and verify OpenLink manual probe clears backoff on success too!
	h.backend.subs.RecordSiteAutomaticStop("weibo", site.ErrKindRateLimited)
	if active, _ := h.backend.subs.IsSiteInBackoff("weibo"); !active {
		t.Fatal("expected Weibo backoff active")
	}
	if _, err := h.backend.OpenLink(ctx, "https://weibo.com/u/"+synthWeiboUID); err != nil {
		t.Fatalf("OpenLink manual probe failed: %v", err)
	}
	if active, _ := h.backend.subs.IsSiteInBackoff("weibo"); active {
		t.Fatal("successful OpenLink manual probe must clear Weibo backoff immediately")
	}
	_ = subscription.DefaultWeiboHourlyQuota
}
