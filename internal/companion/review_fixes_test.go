package companion

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/companionapi"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/subscription"
)

func TestPutFeedPositionSaveErrorNeverLeaksLocalPath(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir, "node-pos-leak")
	if err != nil {
		t.Fatal(err)
	}
	dev, token, err := store.AddDevice("Phone", "Model", "android", companionapi.ChannelDev, "192.168.1.50", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	// Replace companion.json with a directory so WriteJSONAtomic fails with an OS path error.
	statePath := filepath.Join(dir, StateFileName)
	_ = os.Remove(statePath)
	if err := os.MkdirAll(statePath, 0o700); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(statePath)

	backend := newTestCompanionBackend(t, true)
	defer backend.Close()
	srv := NewServer(ServerOptions{
		Backend: backend, Store: store, Pairing: newPairingManager(nil, nil),
		Hub: newEventHub(nil, nil), Channel: companionapi.ChannelDev, Version: "0.1.0-dev",
	})

	body, _ := json.Marshal(companionapi.HomePosition{Site: "all", Key: "k1", PubTs: 100})
	req := httptest.NewRequest(http.MethodPut, companionapi.PathFeedPosition, bytes.NewReader(body))
	req.RemoteAddr = "192.168.1.50:45001"
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(companionapi.HeaderProtocol, companionapi.ProtocolVersion)
	req.Header.Set(companionapi.HeaderChannel, companionapi.ChannelDev)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("PUT /feed/position save failure status = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
	respText := rec.Body.String()
	if strings.Contains(respText, dir) || strings.Contains(respText, StateFileName) {
		t.Fatalf("PUT /feed/position leaked local filesystem path in response: %s", respText)
	}
	// Also verify invalid site filter returns 400 bad_request cleanly.
	badBody, _ := json.Marshal(companionapi.HomePosition{Site: "unknown_site", Key: "k1", PubTs: 100})
	req2 := httptest.NewRequest(http.MethodPut, companionapi.PathFeedPosition, bytes.NewReader(badBody))
	req2.RemoteAddr = "192.168.1.50:45001"
	req2.Header.Set("Authorization", "Bearer "+token)
	req2.Header.Set(companionapi.HeaderProtocol, companionapi.ProtocolVersion)
	req2.Header.Set(companionapi.HeaderChannel, companionapi.ChannelDev)
	rec2 := httptest.NewRecorder()
	srv.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("PUT /feed/position invalid site = %d, want 400 (dev=%s)", rec2.Code, dev.ID)
	}
}

func TestPairingConcurrentApproveAndCancelledPollPreservesToken(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir, "node-race")
	if err != nil {
		t.Fatal(err)
	}
	pm := newPairingManager(nil, nil)
	sess, err := pm.Start("node-race", "Desk", companionapi.ChannelDev, strings.Repeat("a", 64), "AAAA-BBBB-CCCC-DDDD", 47382, []string{"192.168.1.10"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := companionapi.DecodePairPayload(sess.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := pm.ConsumeCode(payload.Code); err != nil {
		t.Fatal(err)
	}
	pairSecret := strings.Repeat("9", 64)
	pending, err := pm.CreatePending(companionapi.PairRequest{
		Code:       payload.Code,
		PairSecret: pairSecret,
		DeviceName: "Phone-Race",
		Model:      "Pixel",
		Platform:   "android",
		Channel:    companionapi.ChannelDev,
	}, "192.168.1.50")
	if err != nil {
		t.Fatal(err)
	}
	pairID := pending.PairID

	// 1. Concurrent Approve calls on the same pairID must create exactly 1 device.
	var wg sync.WaitGroup
	var mu sync.Mutex
	successes := 0
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := pm.Approve(pairID, store); err == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if successes != 1 || store.Count() != 1 {
		t.Fatalf("concurrent Approve: successes=%d store.Count=%d; want 1 and 1", successes, store.Count())
	}

	// 2. Poll with an already-cancelled context must NOT consume the one-time token.
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := pm.Poll(cancelledCtx, pairID, pairSecret, time.Second); ok {
		t.Fatalf("Poll with cancelled ctx unexpectedly returned ok=true")
	}

	// 3. Subsequent Poll with a live context retrieves the approved token ONCE.
	resp, ok := pm.Poll(context.Background(), pairID, pairSecret, time.Second)
	if !ok || resp.Status != companionapi.PairStatusApproved || resp.Token == "" {
		t.Fatalf("retry Poll after cancelled ctx = (%+v, %v), want approved with token", resp, ok)
	}

	// 4. Third Poll sees pairID consumed (deleted).
	if _, ok2 := pm.Poll(context.Background(), pairID, pairSecret, time.Second); ok2 {
		t.Fatalf("third Poll should return found=false after token was delivered once")
	}
}

func TestMaxDevicesAllowsQRRescanAndRejectsSixthDevice(t *testing.T) {
	dir := t.TempDir()
	backend := newTestCompanionBackend(t, true)
	defer backend.Close()

	mgr, err := NewManager(ManagerOptions{
		DataDir:         dir,
		Channel:         companionapi.ChannelDev,
		NodeName:        "FullPC",
		Version:         "0.1.0-dev",
		Backend:         backend,
		ListenOverrides: []string{"127.0.0.1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Shutdown(companionapi.ByeReasonExit)
	if err := mgr.ApplySettings(true, 0); err != nil {
		t.Fatal(err)
	}

	// Seed MaxDevices (5) paired devices directly into store.
	var firstToken string
	for i := 0; i < companionapi.MaxDevices; i++ {
		_, tok, err := mgr.store.AddDevice("Phone", "Model", "android", companionapi.ChannelDev, "192.168.1.50", time.Now())
		if err != nil {
			t.Fatalf("AddDevice %d: %v", i, err)
		}
		if i == 0 {
			firstToken = tok
		}
	}

	// 1. StartPairing must STILL succeed at 5/5 so paired devices can rescan to update LAN addresses (FR-MOB-007).
	sess, err := mgr.StartPairing()
	if err != nil {
		t.Fatalf("StartPairing at 5/5 failed: %v", err)
	}
	payload, err := companionapi.DecodePairPayload(sess.Payload)
	if err != nil {
		t.Fatal(err)
	}

	// 2. Existing paired device rescanning with Bearer token gets 200 updated without desktop confirmation.
	rescanBody, _ := json.Marshal(companionapi.PairRequest{
		Code:       payload.Code,
		DeviceName: "Phone",
		Platform:   "android",
		Channel:    companionapi.ChannelDev,
	})
	req := httptest.NewRequest(http.MethodPost, companionapi.PathPair, bytes.NewReader(rescanBody))
	req.RemoteAddr = "192.168.1.50:45010"
	req.Header.Set("Authorization", "Bearer "+firstToken)
	req.Header.Set(companionapi.HeaderProtocol, companionapi.ProtocolVersion)
	req.Header.Set(companionapi.HeaderChannel, companionapi.ChannelDev)
	rec := httptest.NewRecorder()
	mgr.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("rescan at 5/5 status = %d body=%s, want 200", rec.Code, rec.Body.String())
	}

	// 3. A 6th brand-new device gets 409 device_limit (AC-91).
	sess2, err := mgr.StartPairing()
	if err != nil {
		t.Fatal(err)
	}
	payload2, _ := companionapi.DecodePairPayload(sess2.Payload)
	newBody, _ := json.Marshal(companionapi.PairRequest{
		Code:       payload2.Code,
		PairSecret: strings.Repeat("5", 64),
		DeviceName: "SixthPhone",
		Platform:   "android",
		Channel:    companionapi.ChannelDev,
	})
	req6 := httptest.NewRequest(http.MethodPost, companionapi.PathPair, bytes.NewReader(newBody))
	req6.RemoteAddr = "192.168.1.51:45011"
	req6.Header.Set(companionapi.HeaderProtocol, companionapi.ProtocolVersion)
	req6.Header.Set(companionapi.HeaderChannel, companionapi.ChannelDev)
	rec6 := httptest.NewRecorder()
	mgr.server.ServeHTTP(rec6, req6)
	if rec6.Code != http.StatusConflict {
		t.Fatalf("6th device pair status = %d body=%s, want 409 device_limit", rec6.Code, rec6.Body.String())
	}
	var errBody companionapi.ErrorBody
	_ = json.Unmarshal(rec6.Body.Bytes(), &errBody)
	if errBody.Error.Code != companionapi.CodeDeviceLimit {
		t.Fatalf("6th device error code = %q, want %q", errBody.Error.Code, companionapi.CodeDeviceLimit)
	}
}

func TestChecksStartAuthRequiredNotFoundAndSubsCheckProgressRouting(t *testing.T) {
	dir := t.TempDir()
	backend := newTestCompanionBackend(t, true) // only bilibili logged in; weibo NOT logged in
	defer backend.Close()

	wbSub, err := backend.CreateSubscription("纯微博订阅", "", []subscription.AccountRef{
		{Site: "weibo", UID: "500000001", Name: "WB_User"},
	}, subscription.SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatal(err)
	}

	mgr, err := NewManager(ManagerOptions{
		DataDir:  dir,
		Channel:  companionapi.ChannelDev,
		NodeName: "CheckPC",
		Version:  "0.1.0-dev",
		Backend:  backend,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Shutdown(companionapi.ByeReasonExit)
	_, token, err := mgr.store.AddDevice("Phone", "Model", "android", companionapi.ChannelDev, "192.168.1.50", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	// 1. Unknown subId -> 404 not_found.
	unknownBody, _ := json.Marshal(companionapi.CheckRequest{SubIDs: []string{"sub-does-not-exist"}})
	req1 := httptest.NewRequest(http.MethodPost, companionapi.PathChecks, bytes.NewReader(unknownBody))
	req1.RemoteAddr = "192.168.1.50:45020"
	req1.Header.Set("Authorization", "Bearer "+token)
	req1.Header.Set(companionapi.HeaderProtocol, companionapi.ProtocolVersion)
	req1.Header.Set(companionapi.HeaderChannel, companionapi.ChannelDev)
	rec1 := httptest.NewRecorder()
	mgr.server.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusNotFound {
		t.Fatalf("POST /checks unknown sub = %d body=%s, want 404", rec1.Code, rec1.Body.String())
	}

	// 2. Weibo-only subscription when Weibo is logged out -> 412 auth_required with site="weibo".
	wbBody, _ := json.Marshal(companionapi.CheckRequest{SubIDs: []string{wbSub.ID}})
	req2 := httptest.NewRequest(http.MethodPost, companionapi.PathChecks, bytes.NewReader(wbBody))
	req2.RemoteAddr = "192.168.1.50:45020"
	req2.Header.Set("Authorization", "Bearer "+token)
	req2.Header.Set(companionapi.HeaderProtocol, companionapi.ProtocolVersion)
	req2.Header.Set(companionapi.HeaderChannel, companionapi.ChannelDev)
	rec2 := httptest.NewRecorder()
	mgr.server.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusPreconditionFailed {
		t.Fatalf("POST /checks unauth weibo sub = %d body=%s, want 412", rec2.Code, rec2.Body.String())
	}
	var errBody companionapi.ErrorBody
	_ = json.Unmarshal(rec2.Body.Bytes(), &errBody)
	if errBody.Error.Code != companionapi.CodeAuthRequired || errBody.Error.Site != "weibo" {
		t.Fatalf("POST /checks unauth error = %+v, want auth_required site=weibo", errBody.Error)
	}

	// 3. handleBackendEvent routes "progress:1/3" to EventSubsCheck with Message preserved.
	cl := &wsClient{
		id:         1,
		deviceID:   "dev-1",
		now:        time.Now,
		coalesce:   0,
		lastProgAt: map[string]time.Time{},
		pendingPrg: map[string]companionapi.EventFrame{},
		notify:     make(chan struct{}, 1),
	}
	mgr.hub.mu.Lock()
	mgr.hub.clients[1] = cl
	mgr.hub.mu.Unlock()
	defer func() {
		mgr.hub.mu.Lock()
		delete(mgr.hub.clients, 1)
		mgr.hub.mu.Unlock()
	}()

	mgr.handleBackendEvent(service.Event{
		Type:      service.EventSubscriptionChecked,
		Site:      "bilibili",
		Message:   "progress:1/3",
		Timestamp: time.Now(),
	})
	mgr.server.publishCheckError("bilibili", &site.Error{Site: site.Bilibili, Kind: site.ErrKindRiskControl, Message: "风控"})

	batch, _ := cl.popBatch()
	if len(batch) != 2 {
		t.Fatalf("expected 2 subs:check frames, got %d: %+v", len(batch), batch)
	}
	if batch[0].Type != companionapi.EventSubsCheck || batch[0].Message != "progress:1/3" {
		t.Fatalf("progress frame = %+v, want type=subs:check message=progress:1/3", batch[0])
	}
	if batch[1].Type != companionapi.EventSubsCheck || batch[1].Site != "bilibili" || batch[1].State != site.ErrKindRiskControl {
		t.Fatalf("error frame = %+v, want type=subs:check site=bilibili state=risk_control", batch[1])
	}
}

func TestMediaAndAvatarRequireAnyLogin(t *testing.T) {
	dir := t.TempDir()
	backend := newTestCompanionBackend(t, false) // ZERO sites logged in!
	defer backend.Close()

	store, err := OpenStore(dir, "node-unauth")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := store.AddDevice("Phone", "Model", "android", companionapi.ChannelDev, "192.168.1.50", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(ServerOptions{
		Backend: backend, Store: store, Pairing: newPairingManager(nil, nil),
		Hub: newEventHub(nil, nil), Channel: companionapi.ChannelDev, Version: "0.1.0-dev",
	})

	for _, path := range []string{
		companionapi.PathMediaPrefix + "bilibili:video:BV1TEST/cover.jpg",
		companionapi.AvatarPath("bilibili", "1001"),
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = "192.168.1.50:45030"
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set(companionapi.HeaderProtocol, companionapi.ProtocolVersion)
		req.Header.Set(companionapi.HeaderChannel, companionapi.ChannelDev)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusPreconditionFailed {
			t.Fatalf("GET %s with 0 logged-in sites = %d body=%s, want 412 auth_required", path, rec.Code, rec.Body.String())
		}
		var errBody companionapi.ErrorBody
		_ = json.Unmarshal(rec.Body.Bytes(), &errBody)
		if errBody.Error.Code != companionapi.CodeAuthRequired {
			t.Fatalf("GET %s error code = %q, want auth_required", path, errBody.Error.Code)
		}
	}
}

type testRTFunc func(*http.Request) (*http.Response, error)

func (f testRTFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func newTestCompanionBackend(t *testing.T, loginBili bool) *service.Backend {
	t.Helper()
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	cacheDir := filepath.Join(root, "cache")
	_ = os.MkdirAll(dataDir, 0o700)
	_ = os.MkdirAll(cacheDir, 0o700)
	loc := appdirs.Locations{
		Channel:         "dev",
		AppName:         "trace-dev",
		DisplayName:     "TRACE Dev",
		Identifier:      "io.github.chialecode.trace-dev",
		DataDir:         dataDir,
		DefaultCacheDir: cacheDir,
		CredentialsDir:  filepath.Join(dataDir, "credentials"),
		LogsDir:         filepath.Join(dataDir, "logs"),
		TempDir:         filepath.Join(root, "tmp"),
		LockFile:        filepath.Join(dataDir, "app.lock"),
	}
	b, err := service.NewBackend(service.Options{
		Locations:        loc,
		DisableBgWorkers: true,
		NodeName:         "TestPC",
		BilibiliOpts: bilibili.ClientOptions{
			WrapTransport: func(http.RoundTripper) http.RoundTripper {
				return testRTFunc(func(req *http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"application/json"}},
						Body:       io.NopCloser(strings.NewReader(`{"code":0,"data":{"isLogin":true,"mid":10001,"uname":"SynthUser","face":""}}`)),
					}, nil
				})
			},
		},
	})
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	if loginBili {
		if _, err := b.ImportCookie(context.Background(), "SESSDATA=synth_test_cookie; bili_jct=csrf_1; DedeUserID=10001", ""); err != nil {
			b.Close()
			t.Fatalf("ImportCookie: %v", err)
		}
	}
	return b
}

func TestCompanionFeedAndLibraryDetailMapXReplyContextThreadOriginAndDepthCap(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir, "node-x-dec091")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := store.AddDevice("Phone", "Model", "android", companionapi.ChannelDev, "192.168.1.50", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	backend := newTestCompanionBackend(t, true)
	defer backend.Close()

	now := time.Date(2026, 11, 27, 16, 0, 0, 0, time.UTC).Unix()
	itemDir := filepath.Join(backend.LibraryStore().CacheDir(), "x", "900090001_x_comp", "forwards", "1900000000000099001")
	saved, err := backend.LibraryStore().SaveItem(itemDir, library.ItemMetadata{
		Version:     1,
		ID:          "1900000000000099001",
		RawID:       "1900000000000099001",
		Site:        "x",
		Kind:        "dynamic",
		DynamicType: "forward",
		TypeLabel:   "引用帖子",
		Status:      "complete",
		Title:       "Companion X 引用串首帖",
		Text:        "Companion X 引用串首帖\n\nCompanion X 引用串续帖",
		URL:         "https://x.com/x_comp/status/1900000000000099001",
		PubTs:       now - 60,
		CachedAt:    time.Now().UTC().Format(time.RFC3339),
		Author:      library.ItemAuthor{Site: "x", UID: "900090001", UniqueID: "x_comp", Name: "X 伴侣作者"},
		InReplyTo: &library.ItemReplyTarget{
			StatusID:   "1900000000000098001",
			UserID:     "900098001",
			ScreenName: "replied_root",
		},
		Origin: &library.ItemOrigin{
			Site:   "x",
			ID:     "1900000000000099100",
			Author: library.ItemAuthor{Site: "x", UID: "900099100", UniqueID: "outer_x", Name: "外层作者"},
			Text:   "外层被引用正文",
			InReplyTo: &library.ItemReplyTarget{
				StatusID:   "1900000000000098100",
				ScreenName: "outer_parent",
			},
			Origin: &library.ItemOrigin{
				Site:   "x",
				ID:     "1900000000000099200",
				Author: library.ItemAuthor{Site: "x", UID: "900099200", UniqueID: "inner_x", Name: "内层作者"},
				Text:   "内层嵌套正文",
			},
		},
		Thread: []library.ItemThreadPart{
			{ID: "1900000000000099001", PubTs: now - 60, Text: "Companion X 引用串首帖"},
			{
				ID:    "1900000000000099002",
				PubTs: now - 30,
				Text:  "Companion X 引用串续帖",
				Origin: &library.ItemOrigin{
					Site:   "x",
					ID:     "1900000000000099300",
					Author: library.ItemAuthor{Site: "x", UID: "900099300", UniqueID: "part_x", Name: "续帖引用作者"},
					Text:   "续帖内嵌引用正文",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("SaveItem: %v", err)
	}

	// Verify mapOrigin strictly enforces nesting depth cap = 1 even on synthetic 3-level input.
	mappedDeep := mapOrigin(&library.ItemOrigin{
		Site: "x",
		ID:   "d0",
		Text: "L0",
		Origin: &library.ItemOrigin{
			Site: "x",
			ID:   "d1",
			Text: "L1",
			Origin: &library.ItemOrigin{
				Site: "x",
				ID:   "d2",
				Text: "L2-should-drop",
			},
		},
	})
	if mappedDeep == nil || mappedDeep.Origin == nil || mappedDeep.Origin.ID != "d1" || mappedDeep.Origin.Origin != nil {
		t.Fatalf("mapOrigin depth cap violated: %+v", mappedDeep)
	}

	srv := NewServer(ServerOptions{
		Backend: backend, Store: store, Pairing: newPairingManager(nil, nil),
		Hub: newEventHub(nil, nil), Channel: companionapi.ChannelDev, Version: "0.1.0-dev",
	})

	// 1. GET /api/v1/feed
	feedReq := httptest.NewRequest(http.MethodGet, companionapi.PathFeed+"?site=x", nil)
	feedReq.RemoteAddr = "192.168.1.50:45040"
	feedReq.Header.Set("Authorization", "Bearer "+token)
	feedReq.Header.Set(companionapi.HeaderProtocol, companionapi.ProtocolVersion)
	feedReq.Header.Set(companionapi.HeaderChannel, companionapi.ChannelDev)
	feedRec := httptest.NewRecorder()
	srv.ServeHTTP(feedRec, feedReq)
	if feedRec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/feed = %d body=%s", feedRec.Code, feedRec.Body.String())
	}
	var feedResp companionapi.FeedPageResponse
	if err := json.Unmarshal(feedRec.Body.Bytes(), &feedResp); err != nil {
		t.Fatalf("unmarshal feed: %v", err)
	}
	if len(feedResp.Items) != 1 {
		t.Fatalf("feed items = %d, want 1", len(feedResp.Items))
	}
	fi := feedResp.Items[0]
	if fi.TypeLabel != "引用帖子" {
		t.Fatalf("feed TypeLabel = %q, want 引用帖子", fi.TypeLabel)
	}
	if fi.InReplyTo == nil || fi.InReplyTo.ScreenName != "replied_root" {
		t.Fatalf("feed InReplyTo = %+v", fi.InReplyTo)
	}
	if fi.Origin == nil || fi.Origin.AuthorUniqueID != "outer_x" || fi.Origin.InReplyTo == nil || fi.Origin.InReplyTo.ScreenName != "outer_parent" {
		t.Fatalf("feed Origin = %+v", fi.Origin)
	}
	if fi.Origin.Origin == nil || fi.Origin.Origin.AuthorUniqueID != "inner_x" || fi.Origin.Origin.Text != "内层嵌套正文" {
		t.Fatalf("feed Origin.Origin = %+v", fi.Origin.Origin)
	}
	if len(fi.Thread) != 2 || fi.Thread[1].Origin == nil || fi.Thread[1].Origin.Text != "续帖内嵌引用正文" {
		t.Fatalf("feed Thread[1].Origin = %+v", fi.Thread)
	}

	// 2. GET /api/v1/library/items/{key}
	detReq := httptest.NewRequest(http.MethodGet, companionapi.PathLibraryItemsPrefix+saved.ItemKey, nil)
	detReq.RemoteAddr = "192.168.1.50:45041"
	detReq.Header.Set("Authorization", "Bearer "+token)
	detReq.Header.Set(companionapi.HeaderProtocol, companionapi.ProtocolVersion)
	detReq.Header.Set(companionapi.HeaderChannel, companionapi.ChannelDev)
	detRec := httptest.NewRecorder()
	srv.ServeHTTP(detRec, detReq)
	if detRec.Code != http.StatusOK {
		t.Fatalf("GET library item = %d body=%s", detRec.Code, detRec.Body.String())
	}
	var det companionapi.LibraryItemDetail
	if err := json.Unmarshal(detRec.Body.Bytes(), &det); err != nil {
		t.Fatalf("unmarshal detail: %v", err)
	}
	if det.TypeLabel != "引用帖子" || det.InReplyTo == nil || det.InReplyTo.ScreenName != "replied_root" {
		t.Fatalf("detail header/reply mismatch: %+v", det)
	}
	if det.Origin == nil || det.Origin.Origin == nil || det.Origin.Origin.Text != "内层嵌套正文" {
		t.Fatalf("detail nested origin mismatch: %+v", det.Origin)
	}
	if len(det.Thread) != 2 || det.Thread[1].Origin == nil || det.Thread[1].Origin.AuthorUniqueID != "part_x" {
		t.Fatalf("detail thread part origin mismatch: %+v", det.Thread)
	}
}
