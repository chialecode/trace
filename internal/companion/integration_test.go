package companion

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net"
	"net/http"
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
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/chialecode/trace/internal/uistate"
)

// TestLoopbackIntegrationEndToEnd exercises the full desktop companion server
// against a real service.Backend and synthetic media library over pinned TLS 1.3
// loopback (docs/design/mobile.md §12, AC-91..97).
func TestLoopbackIntegrationEndToEnd(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	cacheDir := filepath.Join(root, "cache")

	sentinelCookie := "SENTINEL_BILI_SESSDATA_NO_LEAK_42"
	biliHTTP := &http.Client{Transport: synthBiliRT{}}
	loc := appdirs.Locations{
		Mode:            "portable",
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

	backend, err := service.NewBackend(service.Options{
		Locations:        loc,
		DisableBgWorkers: true,
		NodeName:         "IntegrationPC",
		BilibiliOpts: bilibili.ClientOptions{
			WrapTransport: func(http.RoundTripper) http.RoundTripper {
				return biliHTTP.Transport
			},
		},
	})
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	defer backend.Close()

	// Seed login with sentinel cookie.
	if _, err := backend.ImportCookie(context.Background(), "SESSDATA="+sentinelCookie+"; bili_jct=csrf_1; DedeUserID=10001", ""); err != nil {
		t.Fatalf("ImportCookie: %v", err)
	}

	// Seed one subscription and two library items (a video with Range-able bytes and an image post).
	sub, err := backend.CreateSubscription("集成测试频道", "说明", []subscription.AccountRef{
		{Site: "bilibili", UID: "1001", Name: "UP_Alpha"},
		{Site: "weibo", UID: "500000001", Name: "WB_Beta"},
	}, subscription.SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}

	vDir := filepath.Join(cacheDir, "bilibili", "1001", "videos", "BV1LOOPBACK01")
	_ = os.MkdirAll(vDir, 0o700)
	videoData := bytes.Repeat([]byte("MP4-RANGE-SYNTH-"), 256) // 4096 bytes
	imgBytes := synthJPEG(800, 600)
	_ = os.WriteFile(filepath.Join(vDir, "P01.mp4"), videoData, 0o600)
	_ = os.WriteFile(filepath.Join(vDir, "cover.jpg"), imgBytes, 0o600)
	// Also put an unregistered file beside the item to verify MediaPath rejects it!
	_ = os.WriteFile(filepath.Join(vDir, "secret_unregistered.txt"), []byte("MUST_NOT_SERVE"), 0o600)

	videoMeta, err := backend.LibraryStore().SaveItem(vDir, library.ItemMetadata{
		Version:  1,
		ID:       "BV1LOOPBACK01",
		Site:     "bilibili",
		Kind:     "video",
		Status:   "complete",
		Title:    "回环集成测试视频",
		URL:      "https://www.bilibili.com/video/BV1LOOPBACK01",
		PubTs:    1732600000,
		Duration: 30,
		Author:   library.ItemAuthor{Site: "bilibili", UID: "1001", Mid: 1001, Name: "UP_Alpha"},
		Media: []library.MediaFile{
			{Role: "cover", Path: "cover.jpg", Bytes: int64(len(imgBytes)), SHA256: hexSHA256(imgBytes), Width: 800, Height: 600},
			{Role: "video", Path: "P01.mp4", Bytes: int64(len(videoData)), SHA256: hexSHA256(videoData), Duration: 30},
		},
		Parts: []library.VideoPartRecord{{
			CID: 123, Page: 1, Title: "P1", Duration: 30, File: "P01.mp4",
			Video: library.PartVideoInfo{Quality: 80, QualityName: "1080P", Codecs: "avc1"},
		}},
	})
	if err != nil {
		t.Fatalf("SaveItem: %v", err)
	}

	// Set desktop's own home position to ensure mobile device position stays isolated.
	if _, err := backend.MutateUIState(func(st *uistate.State) {
		st.HomePositions = map[string]uistate.HomePosition{
			"all": {Key: "desktop-pos", PubTs: 100, OffsetPx: 12},
		}
	}); err != nil {
		t.Fatalf("MutateUIState: %v", err)
	}

	var logMu sync.Mutex
	var logBuf bytes.Buffer
	mgr, err := NewManager(ManagerOptions{
		DataDir:            dataDir,
		Channel:            companionapi.ChannelDev,
		NodeName:           "IntegrationPC",
		Version:            "0.1.0-dev.test",
		Backend:            backend,
		ListenOverrides:    []string{"127.0.0.1"},
		AdvertiseOverrides: []string{"127.0.0.1"},
		PollMaxWait:        2 * time.Second,
		Logf: func(f string, a ...any) {
			logMu.Lock()
			defer logMu.Unlock()
			fmt.Fprintf(&logBuf, f+"\n", a...)
		},
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer mgr.Shutdown(companionapi.ByeReasonExit)

	// Starts off disabled: zero listeners.
	if st := mgr.Status(); st.Running || st.Enabled {
		t.Fatalf("expected companion disabled by default, got %+v", st)
	}

	// Pick a free loopback port and enable.
	freePort := pickFreeLoopbackPort(t)
	if err := mgr.ApplySettings(true, freePort); err != nil {
		t.Fatalf("ApplySettings(true): %v", err)
	}

	// 1. Pairing flow: QR -> POST /api/v1/pair -> desktop Approve -> GET /api/v1/pair/{pairId}.
	sess, err := mgr.StartPairing()
	if err != nil {
		t.Fatalf("StartPairing: %v", err)
	}
	payload, err := companionapi.DecodePairPayload(sess.Payload)
	if err != nil {
		t.Fatalf("DecodePairPayload: %v", err)
	}

	phone := newPinnedPhoneClient(payload.Fingerprint, fmt.Sprintf("https://127.0.0.1:%d", payload.Port))
	pairSecret := "SENTINEL_LOOPBACK_PAIR_SECRET_0123456789"

	var startResp companionapi.PairStartResponse
	code, rawStart := phone.doJSON(t, http.MethodPost, companionapi.PathPair, "", nil, companionapi.PairRequest{
		Code:       payload.Code,
		DeviceName: "Pixel 9 Test",
		Model:      "Pixel 9",
		Platform:   "android",
		Channel:    companionapi.ChannelDev,
		AppVersion: "0.1.0-dev",
		PairSecret: pairSecret,
	}, &startResp)
	if code != http.StatusOK || startResp.PairID == "" || startResp.Status != companionapi.PairStatusPending {
		t.Fatalf("POST /pair = %d body=%s", code, rawStart)
	}

	// Approve asynchronously while phone long-polls.
	go func() {
		time.Sleep(40 * time.Millisecond)
		if _, err := mgr.ApprovePairing(startResp.PairID); err != nil {
			t.Errorf("ApprovePairing: %v", err)
		}
	}()

	var pollResp companionapi.PairPollResponse
	pollCode, rawPoll := phone.doJSON(t, http.MethodGet, companionapi.PairPollPath(startResp.PairID), "", map[string]string{
		companionapi.HeaderPairSecret: pairSecret,
	}, nil, &pollResp)
	if pollCode != http.StatusOK || pollResp.Status != companionapi.PairStatusApproved || pollResp.Token == "" {
		t.Fatalf("GET /pair/{id} = %d body=%s", pollCode, rawPoll)
	}
	token := pollResp.Token

	// 2. GET /api/v1/status
	var statusResp companionapi.StatusResponse
	sc, rawStatus := phone.doJSON(t, http.MethodGet, companionapi.PathStatus, token, nil, nil, &statusResp)
	if sc != http.StatusOK || !statusResp.LoggedIn || statusResp.Channel != companionapi.ChannelDev || statusResp.Name != "IntegrationPC" {
		t.Fatalf("GET /status = %d body=%s", sc, rawStatus)
	}
	assertNoLeaks(t, rawStatus, sentinelCookie, token, payload.Code, pairSecret, root)

	// 3. Feed, shared read state (POST /feed/seen), and isolated per-device home position.
	var feedResp companionapi.FeedPageResponse
	fc, rawFeed := phone.doJSON(t, http.MethodGet, companionapi.PathFeed+"?limit=30", token, nil, nil, &feedResp)
	if fc != http.StatusOK || len(feedResp.Items) != 1 || feedResp.Items[0].ItemKey != videoMeta.ItemKey {
		t.Fatalf("GET /feed = %d items=%d body=%s", fc, len(feedResp.Items), rawFeed)
	}
	assertNoLeaks(t, rawFeed, sentinelCookie, token, payload.Code, pairSecret, root)

	// Mark subscription seen from phone -> desktop ui-state.json HomeSeen updates!
	seenCode, _ := phone.doJSON(t, http.MethodPost, companionapi.PathFeedSeen, token, nil, companionapi.FeedSeenRequest{
		SubID:  sub.ID,
		SeenTs: 1732600000,
	}, nil)
	if seenCode != http.StatusOK {
		t.Fatalf("POST /feed/seen = %d", seenCode)
	}
	if gotSeen := backend.GetUIState().HomeSeen["sub:"+sub.ID]; gotSeen != 1732600000 {
		t.Fatalf("desktop HomeSeen[sub:%s] = %d, want 1732600000", sub.ID, gotSeen)
	}

	// Save phone home position -> stored per-device in companion.json, desktop position untouched!
	putCode, _ := phone.doJSON(t, http.MethodPut, companionapi.PathFeedPosition, token, nil, companionapi.HomePosition{
		Site: "all", Key: "phone-pos", PubTs: 1732600000, OffsetPx: 88,
	}, nil)
	if putCode != http.StatusOK {
		t.Fatalf("PUT /feed/position = %d", putCode)
	}
	var posResp companionapi.HomePositionsResponse
	getPosCode, _ := phone.doJSON(t, http.MethodGet, companionapi.PathFeedPosition, token, nil, nil, &posResp)
	if getPosCode != http.StatusOK || posResp.Positions["all"].Key != "phone-pos" || posResp.Positions["all"].OffsetPx != 88 {
		t.Fatalf("GET /feed/position = %d %+v", getPosCode, posResp)
	}
	if deskPos := backend.GetUIState().HomePositions["all"]; deskPos.Key != "desktop-pos" {
		t.Fatalf("phone position mutated desktop position: %+v", deskPos)
	}

	// 4. Library list, item detail, Range video read, ?w=480 thumbnail, and MediaPath traversal refusal.
	var libResp companionapi.LibraryListResponse
	lc, rawLib := phone.doJSON(t, http.MethodGet, companionapi.PathLibrary+"?includeAuthors=1", token, nil, nil, &libResp)
	if lc != http.StatusOK || len(libResp.Items) != 1 {
		t.Fatalf("GET /library = %d body=%s", lc, rawLib)
	}
	assertNoLeaks(t, rawLib, sentinelCookie, token, payload.Code, pairSecret, root)

	var itemResp companionapi.LibraryItemDetail
	ic, rawItem := phone.doJSON(t, http.MethodGet, companionapi.LibraryItemPath(videoMeta.ItemKey), token, nil, nil, &itemResp)
	if ic != http.StatusOK || itemResp.ItemKey != videoMeta.ItemKey {
		t.Fatalf("GET /library/items/{key} = %d body=%s", ic, rawItem)
	}
	assertNoLeaks(t, rawItem, sentinelCookie, token, payload.Code, pairSecret, root)

	// Range request on P01.mp4 -> 206 Partial Content + Cache-Control: no-store.
	rangeResp, rangeBody := phone.doRaw(t, http.MethodGet, companionapi.MediaPath(videoMeta.ItemKey, "P01.mp4"), token, map[string]string{
		"Range": "bytes=16-79",
	})
	if rangeResp.StatusCode != http.StatusPartialContent {
		t.Fatalf("Range status = %d, want 206", rangeResp.StatusCode)
	}
	if cc := rangeResp.Header.Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Fatalf("media Cache-Control = %q, want no-store", cc)
	}
	if !bytes.Equal(rangeBody, videoData[16:80]) {
		t.Fatalf("Range bytes mismatch: got %d bytes", len(rangeBody))
	}

	// Thumbnail ?w=480 -> valid resized JPEG <= 480px.
	thumbResp, thumbBody := phone.doRaw(t, http.MethodGet, companionapi.MediaPath(videoMeta.ItemKey, "cover.jpg")+"?w=480", token, nil)
	if thumbResp.StatusCode != http.StatusOK || !strings.HasPrefix(thumbResp.Header.Get("Content-Type"), "image/jpeg") {
		t.Fatalf("thumbnail status=%d ct=%q", thumbResp.StatusCode, thumbResp.Header.Get("Content-Type"))
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(thumbBody))
	if err != nil || cfg.Width > 480 || cfg.Height > 480 {
		t.Fatalf("thumbnail decode = (%dx%d, err=%v), want <=480", cfg.Width, cfg.Height, err)
	}

	// MediaPath whitelist rejects unregistered file and path traversal.
	for _, badRel := range []string{"secret_unregistered.txt", "../item.json", "..%2F..%2Fsettings.json"} {
		badResp, _ := phone.doRaw(t, http.MethodGet, companionapi.PathMediaPrefix+videoMeta.ItemKey+"/"+badRel, token, nil)
		if badResp.StatusCode != http.StatusNotFound && badResp.StatusCode != http.StatusBadRequest {
			t.Fatalf("MediaPath(%q) returned %d, want 404/400", badRel, badResp.StatusCode)
		}
	}

	// 5. Subscriptions, CheckPlan, Weibo live start rejection, unauthenticated site check -> auth_required.
	var subsResp companionapi.SubscriptionsResponse
	if code, _ := phone.doJSON(t, http.MethodGet, companionapi.PathSubscriptions, token, nil, nil, &subsResp); code != http.StatusOK || len(subsResp.Items) != 1 {
		t.Fatalf("GET /subscriptions = %d count=%d", code, len(subsResp.Items))
	}
	// Starting live recording on Weibo must be refused cleanly.
	var liveErr companionapi.ErrorBody
	if code, _ := phone.doJSON(t, http.MethodPost, companionapi.PathLiveStart, token, nil, companionapi.StartLiveRequest{AccountKey: "weibo:500000001"}, &liveErr); code != http.StatusBadRequest {
		t.Fatalf("POST /live/start weibo = %d body=%+v", code, liveErr)
	}
	// Checking an unauthenticated site account (weibo) returns auth_required with site="weibo".
	var checkErr companionapi.ErrorBody
	if code, _ := phone.doJSON(t, http.MethodPost, companionapi.PathChecksAccount, token, nil, companionapi.CheckAccountRequest{SubID: sub.ID, Site: "weibo", UID: "500000001"}, &checkErr); code != http.StatusPreconditionFailed || checkErr.Error.Code != companionapi.CodeAuthRequired || checkErr.Error.Site != "weibo" {
		t.Fatalf("POST /checks/account unauth = %d body=%+v", code, checkErr)
	}

	// 6. Design §4.7 QR re-scan with existing Bearer token consumes one-time code without desktop confirmation.
	sessRescan, err := mgr.StartPairing()
	if err != nil {
		t.Fatalf("StartPairing rescan: %v", err)
	}
	rescanPayload, _ := companionapi.DecodePairPayload(sessRescan.Payload)
	var refreshResp companionapi.PairStartResponse
	rc, rawRef := phone.doJSON(t, http.MethodPost, companionapi.PathPair, token, nil, companionapi.PairRequest{
		Code:    rescanPayload.Code,
		Channel: companionapi.ChannelDev,
	}, &refreshResp)
	if rc != http.StatusOK || refreshResp.Status != companionapi.PairStatusUpdated {
		t.Fatalf("QR rescan refresh = %d body=%s", rc, rawRef)
	}
	if len(mgr.Status().PendingPairs) != 0 {
		t.Fatalf("QR rescan created unexpected desktop confirmation prompt")
	}

	// 7. WebSocket GET /api/v1/events receives bye{reason: "exit"} within 1s on Shutdown!
	wsClientConn := phone.dialWS(t, companionapi.PathEvents, token)
	defer wsClientConn.Close()

	// Wait briefly until server registers the online device.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		snap := mgr.Status()
		if len(snap.Devices) == 1 && snap.Devices[0].Online {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if snap := mgr.Status(); len(snap.Devices) != 1 || !snap.Devices[0].Online {
		t.Fatalf("expected device online after WS connect, got %+v", snap.Devices)
	}

	shutdownStart := time.Now()
	go mgr.Shutdown(companionapi.ByeReasonExit)

	op, rawFrame, err := wsClientConn.ReadFrame(time.Now().Add(2 * time.Second))
	if err != nil || op != wsOpText {
		t.Fatalf("WS ReadFrame bye = (op=%d, err=%v)", op, err)
	}
	var byeEv companionapi.EventFrame
	if err := json.Unmarshal(rawFrame, &byeEv); err != nil {
		t.Fatalf("unmarshal bye frame: %v", err)
	}
	if byeEv.Type != companionapi.EventBye || byeEv.Reason != companionapi.ByeReasonExit {
		t.Fatalf("unexpected bye frame: %+v", byeEv)
	}
	if elapsed := time.Since(shutdownStart); elapsed > 1500*time.Millisecond {
		t.Fatalf("bye frame took too long: %s", elapsed)
	}

	// 8. Verify zero sentinel leaks in companion logs.
	logMu.Lock()
	logSnapshot := logBuf.String()
	logMu.Unlock()
	assertNoLeaks(t, logSnapshot, sentinelCookie, token, payload.Code, pairSecret, root)
}

func assertNoLeaks(t *testing.T, haystack string, secrets ...string) {
	t.Helper()
	for _, s := range secrets {
		if s != "" && strings.Contains(haystack, s) {
			t.Fatalf("leak detected: output contains forbidden secret/path %q", s)
		}
	}
}

type synthBiliRT struct{}

func (synthBiliRT) RoundTrip(req *http.Request) (*http.Response, error) {
	body := `{"code":0,"message":"0","data":{"isLogin":true,"mid":10001,"uname":"集成测试用户","face":""}}`
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}, nil
}

type pinnedPhoneClient struct {
	baseURL string
	fp      string
	tlsCfg  *tls.Config
	httpCli *http.Client
}

func newPinnedPhoneClient(expectedFP, baseURL string) *pinnedPhoneClient {
	tlsCfg := &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, // certificate identity is verified via pinned SHA-256 below
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return errors.New("no peer certificate")
			}
			got := companionapi.Fingerprint(rawCerts[0])
			if got != expectedFP {
				return fmt.Errorf("fingerprint mismatch: got %s want %s", got, expectedFP)
			}
			return nil
		},
	}
	return &pinnedPhoneClient{
		baseURL: baseURL,
		fp:      expectedFP,
		tlsCfg:  tlsCfg,
		httpCli: &http.Client{
			Timeout:   5 * time.Second,
			Transport: &http.Transport{TLSClientConfig: tlsCfg},
		},
	}
}

func (c *pinnedPhoneClient) doJSON(t *testing.T, method, path, token string, extraHeaders map[string]string, reqBody any, out any) (int, string) {
	t.Helper()
	var body io.Reader
	if reqBody != nil {
		raw, err := json.Marshal(reqBody)
		if err != nil {
			t.Fatalf("marshal req: %v", err)
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.baseURL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(companionapi.HeaderProtocol, companionapi.ProtocolVersion)
	req.Header.Set(companionapi.HeaderVersion, "0.1.0-dev")
	req.Header.Set(companionapi.HeaderChannel, companionapi.ChannelDev)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	resp, err := c.httpCli.Do(req)
	if err != nil {
		t.Fatalf("%s %s failed: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if out != nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, out)
	}
	return resp.StatusCode, string(raw)
}

func (c *pinnedPhoneClient) doRaw(t *testing.T, method, path, token string, extraHeaders map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, c.baseURL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(companionapi.HeaderProtocol, companionapi.ProtocolVersion)
	req.Header.Set(companionapi.HeaderVersion, "0.1.0-dev")
	req.Header.Set(companionapi.HeaderChannel, companionapi.ChannelDev)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	resp, err := c.httpCli.Do(req)
	if err != nil {
		t.Fatalf("%s %s failed: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, raw
}

func (c *pinnedPhoneClient) dialWS(t *testing.T, path, token string) *wsConn {
	t.Helper()
	hostPort := strings.TrimPrefix(c.baseURL, "https://")
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 3 * time.Second}, "tcp", hostPort, c.tlsCfg.Clone())
	if err != nil {
		t.Fatalf("tls dial ws: %v", err)
	}
	var keyBytes [16]byte
	_, _ = rand.Read(keyBytes[:])
	wsKey := base64.StdEncoding.EncodeToString(keyBytes[:])

	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: %s\r\n%s: %s\r\n%s: 0.1.0-dev\r\n%s: %s\r\nAuthorization: Bearer %s\r\n\r\n",
		path, hostPort, wsKey,
		companionapi.HeaderProtocol, companionapi.ProtocolVersion,
		companionapi.HeaderVersion,
		companionapi.HeaderChannel, companionapi.ChannelDev,
		token,
	)
	if _, err := conn.Write([]byte(req)); err != nil {
		_ = conn.Close()
		t.Fatalf("ws write handshake: %v", err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
	if err != nil || resp.StatusCode != http.StatusSwitchingProtocols {
		_ = conn.Close()
		t.Fatalf("ws handshake failed: resp=%v err=%v", resp, err)
	}
	return &wsConn{conn: conn, br: br, isServer: false, closed: make(chan struct{})}
}

func pickFreeLoopbackPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

func synthJPEG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{0x1a, 0x73, 0xe8, 0xff})
		}
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80})
	return buf.Bytes()
}

func hexSHA256(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
