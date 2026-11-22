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

func TestXExcludedFromAutoPollBatchStatusAndRecording(t *testing.T) {
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
	xProvider := &spyLiveProvider{siteID: site.X}
	mgr.RegisterSiteProvider(dyProvider)
	// RegisterSiteProvider must ignore site.X; also verify direct map insertion still produces 0 calls.
	mgr.RegisterSiteProvider(xProvider)
	if _, registered := mgr.getSiteProvider(site.X); registered {
		t.Fatalf("RegisterSiteProvider unexpectedly registered an X LiveProvider")
	}
	mgr.mu.Lock()
	mgr.siteProviders[site.X] = xProvider
	mgr.mu.Unlock()

	if mgr.hasSiteAuth(site.X) {
		t.Fatalf("hasSiteAuth(x) must always be false in internal/live")
	}
	if mgr.hasSiteAuth(site.ID("unknown")) {
		t.Fatalf("hasSiteAuth(unknown) must be false")
	}

	// LiveCheckEvery must return 0 for X and unknown sites (never fall back to Bilibili).
	mgr.UpdateSettings(settings.LiveSettings{PollEnabled: true, PollIntervalSeconds: 180})
	if got := mgr.LiveCheckEvery(site.Bilibili, 3); got != 180*time.Second {
		t.Fatalf("LiveCheckEvery(bilibili) = %v, want 180s", got)
	}
	if got := mgr.LiveCheckEvery(site.X, 3); got != 0 {
		t.Fatalf("LiveCheckEvery(x) = %v, want 0", got)
	}
	if got := mgr.LiveCheckEvery(site.ID("unknown"), 3); got != 0 {
		t.Fatalf("LiveCheckEvery(unknown) = %v, want 0", got)
	}

	const (
		xUID1 = "180000000000000001"
		xUID2 = "180000000000000002"
	)

	// Create an X-only subscription AND a mixed 4-site subscription, both with AutoRecord=true.
	if _, err := subMgr.Create("纯X开播规则", "", []subscription.AccountRef{
		{Site: "x", UID: xUID1, UniqueID: "synth_x_live_1", Name: "X主播甲"},
	}, subscription.SubRules{ScheduledCheck: true, AutoRecord: true}); err != nil {
		t.Fatalf("Create x-only sub: %v", err)
	}
	if _, err := subMgr.Create("四站混合开播规则", "", []subscription.AccountRef{
		{Site: "bilibili", UID: "1001", Name: "BiliAnchor"},
		{Site: "douyin", UID: "MS4wLjABAAAA__XLiveExclusion_01", Name: "DouyinAnchor"},
		{Site: "weibo", UID: "1000000001", Name: "WeiboAnchor"},
		{Site: "x", UID: xUID2, UniqueID: "synth_x_live_2", Name: "X主播乙"},
	}, subscription.SubRules{ScheduledCheck: true, AutoRecord: true}); err != nil {
		t.Fatalf("Create mixed 4-site sub: %v", err)
	}

	ctx := context.Background()

	// 1. PollNow (Bilibili) & PollDouyinAutoRecordNow (Douyin) must make 0 X requests.
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
	if xProvider.totalCalls() != 0 {
		t.Fatalf("expected 0 X provider calls during Bilibili/Douyin polls, got %d", xProvider.totalCalls())
	}

	// 2. Batch StatusByAccountKeys excludes X with 0 requests while querying Bilibili & Douyin.
	mixedStates, err := mgr.StatusByAccountKeys(ctx, []string{
		"bilibili:1001",
		"douyin:MS4wLjABAAAA__XLiveExclusion_01",
		"x:" + xUID1,
		"x:" + xUID2,
	})
	if err != nil {
		t.Fatalf("StatusByAccountKeys mixed: %v", err)
	}
	if _, hasX1 := mixedStates["x:"+xUID1]; hasX1 {
		t.Fatalf("StatusByAccountKeys unexpectedly returned state for x:%s", xUID1)
	}
	if _, hasX2 := mixedStates["x:"+xUID2]; hasX2 {
		t.Fatalf("StatusByAccountKeys unexpectedly returned state for x:%s", xUID2)
	}
	if _, hasBili := mixedStates["bilibili:1001"]; !hasBili {
		t.Fatalf("expected bilibili:1001 in mixed StatusByAccountKeys result: %+v", mixedStates)
	}
	if _, hasDy := mixedStates["douyin:MS4wLjABAAAA__XLiveExclusion_01"]; !hasDy {
		t.Fatalf("expected douyin account in mixed StatusByAccountKeys result: %+v", mixedStates)
	}

	biliBeforeXOnly := biliCalls.Load()
	dyBeforeXOnly := dyProvider.totalCalls()
	xOnlyStates, err := mgr.StatusByAccountKeys(ctx, []string{"x:" + xUID1})
	if err != nil || len(xOnlyStates) != 0 {
		t.Fatalf("StatusByAccountKeys x-only = (%+v, %v), want empty map & nil error", xOnlyStates, err)
	}
	if biliCalls.Load() != biliBeforeXOnly || dyProvider.totalCalls() != dyBeforeXOnly || xProvider.totalCalls() != 0 {
		t.Fatalf("StatusByAccountKeys(x) issued unexpected requests")
	}

	// 3. CachedAccountState, ObserveAccountLiveHint, LiveCheckTimes, ListRooms, RecordingPreview, ClearIdleIncidents.
	mgr.ObserveAccountLiveHint(site.X, xUID1, "8888", "1SynthBroadcastID")
	if _, ok := mgr.CachedAccountState(site.X, xUID1); ok {
		t.Fatalf("CachedAccountState(x) must return false")
	}
	if got := mgr.CachedStatesByAccountKeys([]string{"x:" + xUID1}); len(got) != 0 {
		t.Fatalf("CachedStatesByAccountKeys(x) must be empty, got %+v", got)
	}
	for k := range mgr.LiveCheckTimes() {
		if strings.HasPrefix(k, "x:") {
			t.Fatalf("LiveCheckTimes unexpectedly contains X key %q", k)
		}
	}
	for _, r := range mgr.ListRooms() {
		if r.Site == string(site.X) {
			t.Fatalf("ListRooms unexpectedly contains X room: %+v", r)
		}
	}
	for _, previewKey := range []string{
		"x:" + xUID1,
		"x:user:" + xUID1,
		"x:live:1SynthBroadcastID",
		"X:" + xUID1,
	} {
		if _, ok := mgr.RecordingPreview(previewKey); ok {
			t.Fatalf("RecordingPreview(%q) must return false", previewKey)
		}
	}

	// ClearIdleIncidents for x or unknown site must NEVER clear Bilibili's incidents.
	mgr.mu.Lock()
	biliSt := mgr.states[91001]
	biliSt.Incidents = []library.LiveIncident{{Kind: library.LiveIncidentReconnect, Reason: "keep"}}
	mgr.states[91001] = biliSt
	mgr.mu.Unlock()
	if mgr.ClearIdleIncidents("x", "91001", "1001") {
		t.Fatalf("ClearIdleIncidents(x) must not clear Bilibili room incidents")
	}
	if mgr.ClearIdleIncidents("X", "91001", "1001") {
		t.Fatalf("ClearIdleIncidents(X) must not clear Bilibili room incidents")
	}
	mgr.mu.Lock()
	remainingInc := len(mgr.states[91001].Incidents)
	mgr.mu.Unlock()
	if remainingInc != 1 {
		t.Fatalf("Bilibili incidents were unexpectedly cleared: count=%d", remainingInc)
	}

	// CancelRecordingsForSite & StopForSite for X are safe no-ops.
	mgr.CancelRecordingsForSite(site.X, "logout")
	mgr.StopForSite(site.X, "logout")

	// 4. Direct status and start/stop recording calls for X MUST be rejected with
	// "X 直播与 Spaces 暂不支持录制" and 0 requests!
	assertXRejected := func(name string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s: expected error, got nil", name)
		}
		var se *site.Error
		if !errors.As(err, &se) {
			t.Fatalf("%s: expected *site.Error, got %T (%v)", name, err, err)
		}
		if se.Site != site.X || !se.Unsupported || !strings.Contains(se.Message, XLiveUnsupportedMessage) {
			t.Fatalf("%s: unexpected error = %+v, want site=x unsupported=true msg containing %q", name, se, XLiveUnsupportedMessage)
		}
	}

	biliBefore := biliCalls.Load()
	dyBefore := dyProvider.totalCalls()

	_, err = mgr.CheckAccountLiveStatus(ctx, site.X, xUID1, "", true)
	assertXRejected("CheckAccountLiveStatus(x)", err)

	for _, key := range []string{
		"x:" + xUID1,
		"x:user:" + xUID1,
		"x:live:1SynthBroadcastID",
		"x:space:1SynthSpaceID",
		"X:" + xUID1,
	} {
		_, err = mgr.StartRecordingByKey(ctx, key)
		assertXRejected("StartRecordingByKey("+key+")", err)
		_, err = mgr.StopRecordingByKey(key)
		assertXRejected("StopRecordingByKey("+key+")", err)
	}

	if biliCalls.Load() != biliBefore || dyProvider.totalCalls() != dyBefore || xProvider.totalCalls() != 0 {
		t.Fatalf("X live calls leaked network requests: bili=%d->%d dy=%d->%d x=%d",
			biliBefore, biliCalls.Load(), dyBefore, dyProvider.totalCalls(), xProvider.totalCalls())
	}
}

func TestBackgroundPollLoopSendsZeroRequestsForXOnlySubscriptions(t *testing.T) {
	var anyHTTP atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		anyHTTP.Add(1)
		http.NotFound(w, r)
	})

	mgr, _, subMgr, _, _ := setupLiveTestHarness(t, mux)
	mgr.UpdateSettings(settings.LiveSettings{PollEnabled: true, PollIntervalSeconds: 60})

	dyProvider := &spyLiveProvider{siteID: site.Douyin}
	xProvider := &spyLiveProvider{siteID: site.X}
	mgr.RegisterSiteProvider(dyProvider)
	mgr.mu.Lock()
	mgr.siteProviders[site.X] = xProvider
	mgr.mu.Unlock()

	if _, err := subMgr.Create("仅X自动录制", "", []subscription.AccountRef{
		{Site: "x", UID: "180000000000000001", UniqueID: "synth_x_bg", Name: "X博主"},
	}, subscription.SubRules{ScheduledCheck: true, AutoRecord: true}); err != nil {
		t.Fatalf("Create x sub: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	mgr.Start(ctx)
	mgr.UpdateSettings(settings.LiveSettings{PollEnabled: true, PollIntervalSeconds: 60})
	time.Sleep(50 * time.Millisecond)
	cancel()
	mgr.Stop()

	if anyHTTP.Load() != 0 || dyProvider.totalCalls() != 0 || xProvider.totalCalls() != 0 {
		t.Fatalf("background pollLoop sent requests for X-only subscriptions: http=%d dy=%d x=%d",
			anyHTTP.Load(), dyProvider.totalCalls(), xProvider.totalCalls())
	}
}
