package remoteserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/remote"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/chialecode/trace/internal/upload"
	"github.com/chialecode/trace/internal/upload/fakerclone"
)

func TestMain(m *testing.M) {
	if fakerclone.Enabled() {
		os.Exit(fakerclone.Main(os.Args[1:]))
	}
	os.Exit(m.Run())
}

// Synthetic secrets: none of them may appear in a log, a status or a file
// in plain text.
const (
	sentinelSess = "SENTINEL_SESS_6b1d0c"
	sentinelCSRF = "SENTINEL_CSRF_91c2aa"
)

func sentinelCookie() string {
	return "SESSDATA=" + sentinelSess + "; bili_jct=" + sentinelCSRF + "; DedeUserID=778899"
}

func initServer(t *testing.T) (dataDir string, res InitResult) {
	t.Helper()
	root := t.TempDir()
	dataDir = filepath.Join(root, "data")
	res, err := Init(InitOptions{DataDir: dataDir, ExportDir: filepath.Join(root, "export"), Listen: "127.0.0.1:47380", PublicAddress: "127.0.0.1", Name: "测试远程"})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	return dataDir, res
}

// Init writes private files, stores only the token's hash and
// prints a code that decodes to the same connection; token rotation makes
// the old token fail at once.
func TestInitPrivateFilesCodeAndTokenRotation(t *testing.T) {
	dataDir, res := initServer(t)
	conn, err := remote.DecodeCode(res.Code)
	if err != nil {
		t.Fatalf("DecodeCode: %v", err)
	}
	if conn != res.Conn || conn.Token == "" || len(conn.Token) < 40 {
		t.Fatalf("code connection = %+v, want %+v with a 32-byte token", conn, res.Conn)
	}
	fp, err := CertificateFingerprint(dataDir)
	if err != nil || fp != conn.Fingerprint {
		t.Fatalf("fingerprint = %q (%v), code has %q", fp, err, conn.Fingerprint)
	}
	raw, err := os.ReadFile(filepath.Join(dataDir, ConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), conn.Token) {
		t.Fatal("server.json contains the token itself")
	}
	cfg, err := LoadConfig(dataDir)
	if err != nil || cfg.TokenHash != HashToken(conn.Token) || cfg.Upload.Mode != remote.UploadNone {
		t.Fatalf("config = %+v (%v)", cfg, err)
	}
	if _, err := Init(InitOptions{DataDir: dataDir, ExportDir: cfg.ExportDir}); err == nil {
		t.Fatal("a second init over an initialized data directory must fail")
	}
	if runtime.GOOS != "windows" {
		for _, p := range []string{ConfigFile, filepath.Join(TLSDir, KeyFile), filepath.Join(TLSDir, CertFile), filepath.Join("credentials", credential.KeyFileName)} {
			st, err := os.Stat(filepath.Join(dataDir, p))
			if err != nil || st.Mode().Perm() != 0o600 {
				t.Fatalf("%s mode = %v (%v), want 0600", p, st.Mode().Perm(), err)
			}
		}
		for _, d := range []string{"", TLSDir, "credentials"} {
			st, err := os.Stat(filepath.Join(dataDir, d))
			if err != nil || st.Mode().Perm() != 0o700 {
				t.Fatalf("dir %q mode = %v (%v), want 0700", d, st.Mode().Perm(), err)
			}
		}
	}

	s := New(Options{DataDir: dataDir, TokenHash: cfg.TokenHash, Service: &fakeService{}})
	if !s.tokenOK(conn.Token) || s.tokenOK("") || s.tokenOK(conn.Token+"x") {
		t.Fatal("token check before rotation is wrong")
	}
	rot, err := RotateToken(dataDir)
	if err != nil {
		t.Fatalf("RotateToken: %v", err)
	}
	if rot.Conn.Token == conn.Token || rot.Conn.Fingerprint != conn.Fingerprint || rot.Conn.URL != conn.URL {
		t.Fatalf("rotated connection = %+v", rot.Conn)
	}
	if s.tokenOK(conn.Token) {
		t.Fatal("the old token still works after rotation")
	}
	if !s.tokenOK(rot.Conn.Token) {
		t.Fatal("the new token does not work on the running server")
	}
}

type fakeService struct {
	mu       sync.Mutex
	puts     int
	pushes   []string
	rejectAs error
}

func (f *fakeService) ServerStatus() remote.Status {
	return remote.Status{Version: "test", Protocol: remote.ProtocolVersion}
}
func (f *fakeService) ServerSubscriptions() remote.SubscriptionsInfo {
	return remote.SubscriptionsInfo{}
}
func (f *fakeService) ApplyRemoteSnapshot(raw []byte) (remote.SubscriptionsInfo, error) {
	f.mu.Lock()
	f.puts++
	f.mu.Unlock()
	if _, err := subscription.ParseSnapshot(raw); err != nil {
		return remote.SubscriptionsInfo{}, err
	}
	return remote.SubscriptionsInfo{Hash: "h"}, nil
}
func (f *fakeService) AcceptPushedCredential(_ context.Context, siteID, _ string) (remote.SiteStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rejectAs != nil {
		return remote.SiteStatus{}, f.rejectAs
	}
	f.pushes = append(f.pushes, siteID)
	return remote.SiteStatus{Site: siteID, State: remote.SiteValid}, nil
}
func (f *fakeService) ClearPushedCredential(string) error { return nil }

type apiHarness struct {
	t     *testing.T
	srv   *httptest.Server
	s     *Server
	token string
	clock atomic.Int64
}

func newAPIHarness(t *testing.T, svc Service, disk Disk) *apiHarness {
	t.Helper()
	dataDir, res := initServer(t)
	cfg, _ := LoadConfig(dataDir)
	h := &apiHarness{t: t, token: res.Conn.Token}
	h.clock.Store(time.Date(2026, 11, 19, 8, 0, 0, 0, time.UTC).UnixNano())
	h.s = New(Options{DataDir: dataDir, TokenHash: cfg.TokenHash, Service: svc, Disk: disk, Now: func() time.Time { return time.Unix(0, h.clock.Load()) }})
	h.srv = httptest.NewServer(h.s)
	t.Cleanup(h.srv.Close)
	return h
}

func (h *apiHarness) do(method, path, token, protocol string, body []byte) (int, remote.ErrorBody) {
	h.t.Helper()
	req, _ := http.NewRequest(method, h.srv.URL+path, bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if protocol != "" {
		req.Header.Set(remote.HeaderProtocol, protocol)
	}
	req.Header.Set(remote.HeaderVersion, "0.0.0-client")
	resp, err := h.srv.Client().Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var eb remote.ErrorBody
	_ = json.Unmarshal(raw, &eb)
	if resp.Header.Get(remote.HeaderProtocol) != remote.ProtocolVersion {
		h.t.Fatalf("response without the protocol header: %v", resp.Header)
	}
	return resp.StatusCode, eb
}

// Wrong tokens are 401 and five failures within a minute block the
// address for ten minutes (even with the right token); oversized bodies
// are 413; a different protocol is 409 with both versions.
func TestAPIAuthFailureLimitBodyLimitAndProtocol(t *testing.T) {
	svc := &fakeService{}
	h := newAPIHarness(t, svc, nil)
	p := remote.ProtocolVersion
	if code, _ := h.do("GET", "/api/v1/status", h.token, p, nil); code != 200 {
		t.Fatalf("status with the right token = %d", code)
	}
	if code, _ := h.do("GET", "/api/v1/status", "", p, nil); code != 401 {
		t.Fatalf("no token = %d, want 401", code)
	}
	if code, _ := h.do("GET", "/", h.token, p, nil); code != 404 {
		t.Fatalf("outside the API = %d, want 404", code)
	}
	for i := 0; i < 3; i++ {
		if code, _ := h.do("GET", "/api/v1/status", "wrong-token", p, nil); code != 401 {
			t.Fatalf("wrong token = %d, want 401", code)
		}
	}
	// Five failures in a minute: blocked even with the right token.
	code, eb := h.do("GET", "/api/v1/status", h.token, p, nil)
	if code != 429 || eb.Error.Code != remote.CodeRateLimited || eb.Error.RetryAfterSecs != 600 {
		t.Fatalf("after five failures = %d %+v, want 429", code, eb)
	}
	h.clock.Add(int64(9 * time.Minute))
	if code, eb := h.do("GET", "/api/v1/status", h.token, p, nil); code != 429 || eb.Error.RetryAfterSecs != 60 {
		t.Fatalf("after 9 minutes = %d %+v, want still 429 with retryAfter=60", code, eb)
	}
	h.clock.Add(int64(time.Minute + time.Second))
	if code, _ := h.do("GET", "/api/v1/status", h.token, p, nil); code != 200 {
		t.Fatalf("after 10 minutes = %d, want 200", code)
	}

	big := bytes.Repeat([]byte("x"), remote.MaxBodyBytes+1)
	if code, eb := h.do("PUT", "/api/v1/subscriptions", h.token, p, big); code != 413 || eb.Error.Code != remote.CodeTooLarge {
		t.Fatalf("oversized body = %d %+v, want 413", code, eb)
	}
	if svc.puts != 0 {
		t.Fatal("an oversized snapshot reached the service")
	}
	code, eb = h.do("GET", "/api/v1/status", h.token, "999", nil)
	if code != 409 || eb.Error.Code != remote.CodeProtocol || eb.Error.ServerProtocol != remote.ProtocolVersion || eb.Error.ClientProtocol != "999" || eb.Error.ClientVersion != "0.0.0-client" || eb.Error.ServerVersion == "" {
		t.Fatalf("protocol mismatch = %d %+v", code, eb)
	}
}

// A client pinned to another certificate fails the handshake
// before any request reaches the handler; the right fingerprint works and
// a version difference names both versions.
func TestClientPinsCertificateBeforeAnyRequest(t *testing.T) {
	dataDir, res := initServer(t)
	_, other := initServer(t)
	cfg, _ := LoadConfig(dataDir)
	s := New(Options{DataDir: dataDir, TokenHash: cfg.TokenHash, Service: &fakeService{}})
	tlsCfg, err := TLSConfig(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, ln, tlsCfg) }()
	t.Cleanup(func() { cancel(); <-done })
	url := "https://" + ln.Addr().String()

	bad, _ := remote.NewClient(remote.Connection{URL: url, Token: res.Conn.Token, Fingerprint: other.Conn.Fingerprint}, "1.0.0")
	defer bad.Close()
	_, err = bad.Status(context.Background())
	if remote.KindOf(err) != remote.KindCertChanged {
		t.Fatalf("pinned to another certificate: err = %v (kind %q), want cert_changed", err, remote.KindOf(err))
	}
	if n := s.Requests(); n != 0 {
		t.Fatalf("handler saw %d requests through a rejected certificate", n)
	}

	good, _ := remote.NewClient(remote.Connection{URL: url, Token: res.Conn.Token, Fingerprint: res.Conn.Fingerprint}, "1.0.0")
	defer good.Close()
	if _, err := good.Status(context.Background()); err != nil {
		t.Fatalf("Status with the right fingerprint: %v", err)
	}
	wrongToken, _ := remote.NewClient(remote.Connection{URL: url, Token: strings.Repeat("w", 43), Fingerprint: res.Conn.Fingerprint}, "1.0.0")
	defer wrongToken.Close()
	if _, err := wrongToken.Status(context.Background()); remote.KindOf(err) != remote.KindUnauthorized {
		t.Fatalf("wrong token: %v, want unauthorized", err)
	}
	closed, _ := remote.NewClient(remote.Connection{URL: "https://127.0.0.1:1", Token: res.Conn.Token, Fingerprint: res.Conn.Fingerprint}, "1.0.0")
	defer closed.Close()
	if _, err := closed.Status(context.Background()); remote.KindOf(err) != remote.KindUnreachable {
		t.Fatalf("closed port: %v, want unreachable", err)
	}
}

// fakeBili answers nav for synthetic Cookies; expired=true makes every
// Cookie expire.
type fakeBili struct {
	srv     *httptest.Server
	reqs    atomic.Int64
	expired atomic.Bool
}

func newFakeBili(t *testing.T) *fakeBili {
	t.Helper()
	f := &fakeBili{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.reqs.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/x/web-interface/nav":
			ck := r.Header.Get("Cookie")
			if f.expired.Load() || !strings.Contains(ck, "SESSDATA=") || strings.Contains(ck, "SESSDATA=expired") {
				_, _ = fmt.Fprint(w, `{"code":-101,"message":"账号未登录","data":{"isLogin":false}}`)
				return
			}
			_, _ = fmt.Fprint(w, `{"code":0,"data":{"isLogin":true,"mid":778899,"uname":"合成远程用户","face":"","wbi_img":{"img_url":"https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png","sub_url":"https://i0.hdslb.com/bfs/wbi/4932caff0ff746eab6f01bf08b70ac45.png"}}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeBili) options(bo *service.Options) {
	u := f.srv.URL
	bo.BilibiliOpts = bilibili.ClientOptions{
		Endpoints:      bilibili.Endpoints{APIBase: u, PassportBase: u, WWWBase: u, SpaceBase: u, DynamicBase: u, LiveAPIBase: u, LiveWebBase: u},
		PacingInterval: -1,
		AllowLocalhost: true,
	}
}

func remoteBackend(t *testing.T, dataDir string, bili *fakeBili) *service.Backend {
	t.Helper()
	cfg, err := LoadConfig(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	bo := service.Options{Locations: service.RemoteLocations(dataDir, cfg.ExportDir), Desktop: service.NewMockDesktop(), Profile: service.ProfileRemote, NodeName: cfg.Name, DisableBgWorkers: true}
	bili.options(&bo)
	b, err := service.NewBackend(bo)
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

// A pushed Cookie is checked with the site before it replaces the
// stored one; a rejected push keeps the old login and returns the site's
// reason; a pushed login that expires shows as 需要重新同步.
func TestCredentialPushVerifiesBeforeSaving(t *testing.T) {
	dataDir, res := initServer(t)
	bili := newFakeBili(t)
	b := remoteBackend(t, dataDir, bili)
	cfg, _ := LoadConfig(dataDir)
	s := New(Options{DataDir: dataDir, TokenHash: cfg.TokenHash, Service: b})
	h := httptest.NewServer(s)
	t.Cleanup(h.Close)

	push := func(cookie string) (int, remote.ErrorBody, []byte) {
		body, _ := json.Marshal(remote.CredentialPush{Cookie: cookie})
		req, _ := http.NewRequest("PUT", h.URL+"/api/v1/credentials/bilibili", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+res.Conn.Token)
		req.Header.Set(remote.HeaderProtocol, remote.ProtocolVersion)
		resp, err := h.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var eb remote.ErrorBody
		_ = json.Unmarshal(raw, &eb)
		return resp.StatusCode, eb, raw
	}

	code, _, raw := push(sentinelCookie())
	if code != 200 {
		t.Fatalf("valid push = %d %s", code, raw)
	}
	var st remote.SiteStatus
	_ = json.Unmarshal(raw, &st)
	if st.State != remote.SiteValid || st.Uname != "合成远程用户" || st.CookieSyncedAt == "" {
		t.Fatalf("pushed site status = %+v", st)
	}
	if strings.Contains(string(raw), sentinelSess) {
		t.Fatal("the response echoes the Cookie")
	}
	acc := b.AccountStatus()
	if !acc.LoggedIn || acc.Source != service.CredentialSourceRemotePush {
		t.Fatalf("stored credential = %+v, want logged in with source remote_push", acc)
	}
	before := bili.reqs.Load()

	code, eb, raw := push("SESSDATA=expired; bili_jct=x")
	if code != 422 || eb.Error.Code != remote.CodeCredentialRejected || eb.Error.Message == "" {
		t.Fatalf("rejected push = %d %s", code, raw)
	}
	if bili.reqs.Load() == before {
		t.Fatal("the rejected Cookie was not checked with the site")
	}
	if acc := b.AccountStatus(); !acc.LoggedIn || acc.Mid != 778899 {
		t.Fatalf("a rejected push replaced the stored login: %+v", acc)
	}

	bili.expired.Store(true)
	_, _ = b.VerifyAccount(context.Background())
	var row remote.SiteStatus
	for _, r := range s.Status().Sites {
		if r.Site == "bilibili" {
			row = r
		}
	}
	if row.State != remote.SiteResync || row.Message == "" {
		t.Fatalf("expired pushed login = %+v, want resync", row)
	}
}

// Trace-server run end to end over loopback HTTPS: sync a
// snapshot, push a Cookie, read the status, upload an item through the
// fake rclone, stop on cancel. The token and the Cookie appear in no log,
// status or file.
func TestRunEndToEndWithoutLeakingSecrets(t *testing.T) {
	dataDir, res := initServer(t)
	cfg, _ := LoadConfig(dataDir)
	root := filepath.Dir(dataDir)
	cloud := filepath.Join(root, "cloud")
	rlog := filepath.Join(root, "rclone.log")
	cfg.Upload = upload.Config{Mode: remote.UploadRclone, Remote: "fake:trace", Rclone: os.Args[0], DeleteAfterUpload: true}
	if err := SaveConfig(dataDir, cfg); err != nil {
		t.Fatal(err)
	}
	bili := newFakeBili(t)

	// An item already in the export folder (a finished cache of a previous
	// run) is uploaded and removed after the upload.
	{
		b := remoteBackend(t, dataDir, bili)
		itemDir := filepath.Join(cfg.ExportDir, "bilibili", "1001_UP", "videos", "video_e2e")
		_ = os.MkdirAll(itemDir, 0o700)
		_ = os.WriteFile(filepath.Join(itemDir, "P01.mp4"), []byte("video"), 0o600)
		if _, err := b.LibraryStore().SaveItem(itemDir, library.ItemMetadata{ID: "e2e", Site: "bilibili", Kind: "video", Title: "t", Author: library.ItemAuthor{Mid: 1001, Name: "UP"},
			Media: []library.MediaFile{{Role: "video", Path: "P01.mp4", Bytes: 5}}}); err != nil {
			t.Fatal(err)
		}
		_ = b.Close()
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var logBuf syncBuffer
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	done := make(chan error, 1)
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			cancel()
			<-done
		}
	})
	go func() {
		done <- Run(ctx, RunOptions{
			DataDir: dataDir, Listener: ln, Backend: bili.options, LogMirror: &logBuf,
			UploadEnv:   append(os.Environ(), "TRACE_FAKE_RCLONE=1", "TRACE_FAKE_RCLONE_ROOT="+cloud, "TRACE_FAKE_RCLONE_LOG="+rlog),
			Ready:       func(addr string) { ready <- addr },
			ExitTimeout: 10 * time.Second,
		})
	}()
	var addr string
	select {
	case addr = <-ready:
	case err := <-done:
		stopped = true
		t.Fatalf("Run returned early: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not become ready")
	}
	conn := res.Conn
	conn.URL = "https://" + addr
	c, err := remote.NewClient(conn, appdirs.BuildInfo().Version)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	bg := context.Background()

	snap := subscription.Snapshot{Format: subscription.SnapshotFormat, Version: subscription.SnapshotVersion,
		Subscriptions: []subscription.SnapshotSubscription{{ID: "s1", Name: "合成订阅", Tags: []string{subscription.TagFullCache}, Accounts: []subscription.SnapshotAccount{{Site: "bilibili", UID: "1001", Name: "UP"}}}}}
	raw, _ := json.Marshal(snap)
	info, err := c.PutSubscriptions(bg, raw)
	if err != nil {
		t.Fatalf("PutSubscriptions: %v", err)
	}
	if info.Hash != snap.Hash() || info.Subscriptions != 1 || info.Accounts != 1 {
		t.Fatalf("synced info = %+v, want hash %s", info, snap.Hash())
	}
	if _, err := c.PushCredential(bg, "bilibili", remote.CredentialPush{Cookie: sentinelCookie()}); err != nil {
		t.Fatalf("PushCredential: %v", err)
	}

	var st remote.Status
	deadline := time.Now().Add(20 * time.Second)
	for {
		st, err = c.Status(bg)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		gone := !exists(filepath.Join(cfg.ExportDir, "bilibili", "1001_UP", "videos", "video_e2e"))
		if (gone && st.Upload.TodayFiles > 0) || time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if st.Upload.Mode != remote.UploadRclone || st.Upload.TodayFiles == 0 {
		t.Fatalf("upload status = %+v, want the item uploaded", st.Upload)
	}
	if !exists(filepath.Join(cloud, "trace", "bilibili", "1001_UP", "videos", "video_e2e", "P01.mp4")) || exists(filepath.Join(cfg.ExportDir, "bilibili", "1001_UP", "videos", "video_e2e", "P01.mp4")) {
		raw, _ := os.ReadFile(rlog)
		t.Logf("rclone: %s", raw)
		t.Fatal("the item was not moved to the cloud folder")
	}
	if len(st.Sites) == 0 || st.Sites[0].State != remote.SiteValid {
		t.Fatalf("sites = %+v", st.Sites)
	}
	if len(st.Notes) == 0 {
		t.Fatal("a rule with 全部缓存 must be noted as not run on the remote")
	}
	stRaw, _ := json.Marshal(st)

	cancel()
	select {
	case err := <-done:
		stopped = true
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not stop after cancel")
	}

	secrets := []string{res.Conn.Token, sentinelSess, sentinelCSRF}
	check := func(where string, data []byte) {
		for _, s := range secrets {
			if bytes.Contains(data, []byte(s)) {
				t.Errorf("%s contains a secret %q", where, s[:8])
			}
		}
	}
	check("status", stRaw)
	check("log mirror", []byte(logBuf.String()))
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		data, _ := os.ReadFile(p)
		check(p, data)
		return nil
	})
	if logBuf.String() == "" {
		t.Fatal("the log mirror received nothing")
	}
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestRateLimiterPrunesAndCapsTrackedIPs(t *testing.T) {
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	s := New(Options{Service: &fakeService{}, Now: func() time.Time { return now }})
	for i := 0; i < maxTrackedIPs+50; i++ {
		s.recordFailure(fmt.Sprintf("10.%d.%d.%d", (i>>16)&255, (i>>8)&255, i&255), now)
	}
	s.mu.Lock()
	gotLen := len(s.failures)
	s.mu.Unlock()
	if gotLen > maxTrackedIPs {
		t.Fatalf("tracked failures = %d, want <= %d", gotLen, maxTrackedIPs)
	}
	// Advance past failWindow + pruneInterval and confirm expired entries are pruned.
	now = now.Add(2 * time.Minute)
	s.recordFailure("192.0.2.1", now)
	s.mu.Lock()
	afterPrune := len(s.failures)
	s.mu.Unlock()
	if afterPrune != 1 {
		t.Fatalf("tracked failures after prune = %d, want 1", afterPrune)
	}
}

func TestDiskErrorFailsClosedInStatusAndCacheGate(t *testing.T) {
	failDisk := func() (uint64, uint64, error) { return 0, 0, os.ErrNotExist }
	s := New(Options{Service: &fakeService{}, Disk: failDisk})
	st := s.Status()
	if !st.Disk.Warning || !st.Disk.Paused {
		t.Fatalf("Status().Disk on error = %+v, want Warning=true Paused=true", st.Disk)
	}
	if err := CacheGate(failDisk)(); err == nil {
		t.Fatal("CacheGate on disk error = nil, want non-nil pause error")
	}
}

func TestInitValidatesBeforeCreatingDirs(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	exportDir := filepath.Join(root, "export")
	if _, err := Init(InitOptions{DataDir: dataDir, ExportDir: exportDir, Listen: ":notaport"}); err == nil {
		t.Fatal("Init accepted non-numeric port")
	}
	if exists(dataDir) || exists(exportDir) {
		t.Fatal("Init created directories despite invalid --listen")
	}
}
