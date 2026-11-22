package live

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/settings"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/subscription"
)

type spyLiveProvider struct {
	siteID      site.ID
	statusCalls atomic.Int32
	streamCalls atomic.Int32
	aliveCalls  atomic.Int32
}

func (p *spyLiveProvider) SiteID() site.ID { return p.siteID }
func (p *spyLiveProvider) HasAuth() bool   { return true }

func (p *spyLiveProvider) CheckLiveStatus(_ context.Context, uid, webRID string) (site.LiveStatus, error) {
	p.statusCalls.Add(1)
	return site.LiveStatus{
		Site:       p.siteID,
		AccountUID: uid,
		WebRID:     webRID,
		Status:     site.LiveStateOffline,
	}, nil
}

func (p *spyLiveProvider) SelectLiveStream(_ context.Context, uid, roomOrWebRID string, _ []string) (site.LiveStreamPlan, error) {
	p.streamCalls.Add(1)
	return site.LiveStreamPlan{
		Site:       p.siteID,
		AccountUID: uid,
		RoomID:     roomOrWebRID,
		Status:     site.LiveStateOffline,
	}, nil
}

func (p *spyLiveProvider) CheckRoomsAlive(_ context.Context, _ []string) (map[string]bool, error) {
	p.aliveCalls.Add(1)
	return map[string]bool{}, nil
}

func (p *spyLiveProvider) totalCalls() int32 {
	return p.statusCalls.Load() + p.streamCalls.Load() + p.aliveCalls.Load()
}

func TestWeiboExcludedFromAutoPollBatchStatusAndRecording(t *testing.T) {
	var biliCalls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/room/v1/Room/get_status_info_by_uids", func(w http.ResponseWriter, r *http.Request) {
		biliCalls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"1001": map[string]any{
					"uid":         1001,
					"room_id":     91001,
					"uname":       "BiliAnchor",
					"title":       "B站测试房",
					"live_status": 0,
				},
			},
		})
	})

	mgr, _, subMgr, _, _ := setupLiveTestHarness(t, mux)
	mgr.SetDouyinLiveGap(0)
	mgr.SetDouyinPollMinInterval(0)

	dyProvider := &spyLiveProvider{siteID: site.Douyin}
	wbProvider := &spyLiveProvider{siteID: site.Weibo}
	mgr.RegisterSiteProvider(dyProvider)
	// RegisterSiteProvider must ignore site.Weibo; also verify direct map insertion still produces 0 calls.
	mgr.RegisterSiteProvider(wbProvider)
	if _, registered := mgr.getSiteProvider(site.Weibo); registered {
		t.Fatalf("RegisterSiteProvider unexpectedly registered a Weibo LiveProvider")
	}
	mgr.mu.Lock()
	mgr.siteProviders[site.Weibo] = wbProvider
	mgr.mu.Unlock()

	if mgr.hasSiteAuth(site.Weibo) {
		t.Fatalf("hasSiteAuth(weibo) must always be false in internal/live")
	}
	if mgr.hasSiteAuth(site.ID("unknown")) {
		t.Fatalf("hasSiteAuth(unknown) must be false")
	}

	// LiveCheckEvery must return 0 for Weibo and unknown sites (never fall back to Bilibili).
	mgr.UpdateSettings(settings.LiveSettings{PollEnabled: true, PollIntervalSeconds: 180})
	if got := mgr.LiveCheckEvery(site.Bilibili, 3); got != 180*time.Second {
		t.Fatalf("LiveCheckEvery(bilibili) = %v, want 180s", got)
	}
	if got := mgr.LiveCheckEvery(site.Weibo, 3); got != 0 {
		t.Fatalf("LiveCheckEvery(weibo) = %v, want 0", got)
	}
	if got := mgr.LiveCheckEvery(site.ID("unknown"), 3); got != 0 {
		t.Fatalf("LiveCheckEvery(unknown) = %v, want 0", got)
	}

	// Create a Weibo-only subscription AND a mixed 3-site subscription, both with AutoRecord=true.
	if _, err := subMgr.Create("纯微博开播规则", "", []subscription.AccountRef{
		{Site: "weibo", UID: "1000000001", Name: "微博主播甲"},
	}, subscription.SubRules{ScheduledCheck: true, AutoRecord: true}); err != nil {
		t.Fatalf("Create weibo-only sub: %v", err)
	}
	if _, err := subMgr.Create("三站混合开播规则", "", []subscription.AccountRef{
		{Site: "bilibili", UID: "1001", Name: "BiliAnchor"},
		{Site: "douyin", UID: "MS4wLjABAAAA__LiveExclusion_01", Name: "DouyinAnchor"},
		{Site: "weibo", UID: "1000000002", Name: "微博主播乙"},
	}, subscription.SubRules{ScheduledCheck: true, AutoRecord: true}); err != nil {
		t.Fatalf("Create mixed 3-site sub: %v", err)
	}

	ctx := context.Background()

	// 1. PollNow (Bilibili) & PollDouyinAutoRecordNow (Douyin) must make 0 Weibo requests.
	if _, err := mgr.PollNow(ctx); err != nil {
		t.Fatalf("PollNow: %v", err)
	}
	if biliCalls.Load() != 1 {
		t.Fatalf("expected 1 Bilibili batch status call, got %d", biliCalls.Load())
	}
	if _, err := mgr.PollDouyinAutoRecordNow(ctx); err != nil {
		t.Fatalf("PollDouyinAutoRecordNow: %v", err)
	}
	if dyProvider.statusCalls.Load() != 1 {
		t.Fatalf("expected 1 Douyin auto-record poll call, got %d", dyProvider.statusCalls.Load())
	}
	if wbProvider.totalCalls() != 0 {
		t.Fatalf("expected 0 Weibo provider calls during Bilibili/Douyin polls, got %d", wbProvider.totalCalls())
	}

	// 2. Batch StatusByAccountKeys excludes Weibo with 0 requests while querying Bilibili & Douyin.
	mixedStates, err := mgr.StatusByAccountKeys(ctx, []string{
		"bilibili:1001",
		"douyin:MS4wLjABAAAA__LiveExclusion_01",
		"weibo:1000000001",
		"weibo:1000000002",
	})
	if err != nil {
		t.Fatalf("StatusByAccountKeys mixed: %v", err)
	}
	if _, hasWB1 := mixedStates["weibo:1000000001"]; hasWB1 {
		t.Fatalf("StatusByAccountKeys unexpectedly returned state for weibo:1000000001")
	}
	if _, hasWB2 := mixedStates["weibo:1000000002"]; hasWB2 {
		t.Fatalf("StatusByAccountKeys unexpectedly returned state for weibo:1000000002")
	}
	if _, hasBili := mixedStates["bilibili:1001"]; !hasBili {
		t.Fatalf("expected bilibili:1001 in mixed StatusByAccountKeys result: %+v", mixedStates)
	}
	if _, hasDy := mixedStates["douyin:MS4wLjABAAAA__LiveExclusion_01"]; !hasDy {
		t.Fatalf("expected douyin account in mixed StatusByAccountKeys result: %+v", mixedStates)
	}

	biliBeforeWBOnly := biliCalls.Load()
	dyBeforeWBOnly := dyProvider.totalCalls()
	wbOnlyStates, err := mgr.StatusByAccountKeys(ctx, []string{"weibo:1000000001"})
	if err != nil || len(wbOnlyStates) != 0 {
		t.Fatalf("StatusByAccountKeys weibo-only = (%+v, %v), want empty map & nil error", wbOnlyStates, err)
	}
	if biliCalls.Load() != biliBeforeWBOnly || dyProvider.totalCalls() != dyBeforeWBOnly || wbProvider.totalCalls() != 0 {
		t.Fatalf("StatusByAccountKeys(weibo) issued unexpected requests")
	}

	// 3. CachedAccountState, ObserveAccountLiveHint, LiveCheckTimes, ListRooms, RecordingPreview, ClearIdleIncidents.
	mgr.ObserveAccountLiveHint(site.Weibo, "1000000001", "8888", "1022:2321325000000000000001")
	if _, ok := mgr.CachedAccountState(site.Weibo, "1000000001"); ok {
		t.Fatalf("CachedAccountState(weibo) must return false")
	}
	if got := mgr.CachedStatesByAccountKeys([]string{"weibo:1000000001"}); len(got) != 0 {
		t.Fatalf("CachedStatesByAccountKeys(weibo) must be empty, got %+v", got)
	}
	for k := range mgr.LiveCheckTimes() {
		if strings.HasPrefix(k, "weibo:") {
			t.Fatalf("LiveCheckTimes unexpectedly contains Weibo key %q", k)
		}
	}
	for _, r := range mgr.ListRooms() {
		if r.Site == string(site.Weibo) {
			t.Fatalf("ListRooms unexpectedly contains Weibo room: %+v", r)
		}
	}
	if _, ok := mgr.RecordingPreview("weibo:1000000001"); ok {
		t.Fatalf("RecordingPreview(weibo:1000000001) must return false")
	}
	if _, ok := mgr.RecordingPreview("weibo:live:1022:2321325000000000000001"); ok {
		t.Fatalf("RecordingPreview(weibo:live:...) must return false")
	}

	// ClearIdleIncidents for weibo or unknown site must NEVER clear Bilibili's incidents.
	mgr.mu.Lock()
	biliSt := mgr.states[91001]
	biliSt.Incidents = []library.LiveIncident{{Kind: library.LiveIncidentReconnect, Reason: "keep"}}
	mgr.states[91001] = biliSt
	mgr.mu.Unlock()
	if mgr.ClearIdleIncidents("weibo", "91001", "1001") {
		t.Fatalf("ClearIdleIncidents(weibo) must not clear Bilibili room incidents")
	}
	if mgr.ClearIdleIncidents("unknown", "91001", "1001") {
		t.Fatalf("ClearIdleIncidents(unknown) must not clear Bilibili room incidents")
	}
	mgr.mu.Lock()
	remainingInc := len(mgr.states[91001].Incidents)
	mgr.mu.Unlock()
	if remainingInc != 1 {
		t.Fatalf("Bilibili incidents were unexpectedly cleared: count=%d", remainingInc)
	}

	// CancelRecordingsForSite & StopForSite for Weibo are safe no-ops.
	mgr.CancelRecordingsForSite(site.Weibo, "logout")
	mgr.StopForSite(site.Weibo, "logout")

	// 4. Direct status and start/stop recording calls for Weibo MUST be rejected with "微博直播暂不支持录制" and 0 requests!
	assertWeiboRejected := func(name string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s: expected error, got nil", name)
		}
		var se *site.Error
		if !errors.As(err, &se) {
			t.Fatalf("%s: expected *site.Error, got %T (%v)", name, err, err)
		}
		if se.Site != site.Weibo || !se.Unsupported || !strings.Contains(se.Message, WeiboLiveUnsupportedMessage) {
			t.Fatalf("%s: unexpected error = %+v, want site=weibo unsupported=true msg containing %q", name, se, WeiboLiveUnsupportedMessage)
		}
	}

	biliBefore := biliCalls.Load()
	dyBefore := dyProvider.totalCalls()

	_, err = mgr.CheckAccountLiveStatus(ctx, site.Weibo, "1000000001", "", true)
	assertWeiboRejected("CheckAccountLiveStatus(weibo)", err)

	for _, key := range []string{
		"weibo:1000000001",
		"weibo:user:1000000001",
		"weibo:live:1022:2321325000000000000001",
		"WEIBO:1000000001",
	} {
		_, err = mgr.StartRecordingByKey(ctx, key)
		assertWeiboRejected("StartRecordingByKey("+key+")", err)
		_, err = mgr.StopRecordingByKey(key)
		assertWeiboRejected("StopRecordingByKey("+key+")", err)
	}

	// Unknown sites must also be rejected with 0 requests (never fall back to Bilibili).
	if _, err := mgr.CheckAccountLiveStatus(ctx, site.ID("unknown"), "1001", "", true); !errors.Is(err, site.ErrUnknownSite) {
		t.Fatalf("CheckAccountLiveStatus(unknown) = %v, want ErrUnknownSite", err)
	}
	if _, err := mgr.StartRecordingByKey(ctx, "unknown:1001"); err == nil {
		t.Fatalf("StartRecordingByKey(unknown:1001) should fail")
	}
	if _, err := mgr.StopRecordingByKey("unknown:1001"); err == nil {
		t.Fatalf("StopRecordingByKey(unknown:1001) should fail")
	}

	if biliCalls.Load() != biliBefore || dyProvider.totalCalls() != dyBefore || wbProvider.totalCalls() != 0 {
		t.Fatalf("Weibo/unknown live calls leaked network requests: bili=%d->%d dy=%d->%d wb=%d",
			biliBefore, biliCalls.Load(), dyBefore, dyProvider.totalCalls(), wbProvider.totalCalls())
	}
}

func TestBackgroundPollLoopSendsZeroRequestsForWeiboOnlySubscriptions(t *testing.T) {
	var anyHTTP atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		anyHTTP.Add(1)
		http.NotFound(w, r)
	})

	mgr, _, subMgr, _, _ := setupLiveTestHarness(t, mux)
	mgr.UpdateSettings(settings.LiveSettings{PollEnabled: true, PollIntervalSeconds: 60})

	dyProvider := &spyLiveProvider{siteID: site.Douyin}
	wbProvider := &spyLiveProvider{siteID: site.Weibo}
	mgr.RegisterSiteProvider(dyProvider)
	mgr.mu.Lock()
	mgr.siteProviders[site.Weibo] = wbProvider
	mgr.mu.Unlock()

	if _, err := subMgr.Create("仅微博自动录制", "", []subscription.AccountRef{
		{Site: "weibo", UID: "1000000001", Name: "微博博主"},
	}, subscription.SubRules{ScheduledCheck: true, AutoRecord: true}); err != nil {
		t.Fatalf("Create weibo sub: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	mgr.Start(ctx)
	// Trigger a wake cycle and verify 0 requests are sent before stopping cleanly.
	mgr.UpdateSettings(settings.LiveSettings{PollEnabled: true, PollIntervalSeconds: 60})
	time.Sleep(50 * time.Millisecond)
	cancel()
	mgr.Stop()

	if anyHTTP.Load() != 0 || dyProvider.totalCalls() != 0 || wbProvider.totalCalls() != 0 {
		t.Fatalf("background pollLoop sent requests for Weibo-only subscriptions: http=%d dy=%d wb=%d",
			anyHTTP.Load(), dyProvider.totalCalls(), wbProvider.totalCalls())
	}
}
