package remoteserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/remote"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/subscription"
)

// pairHarness is a desktop backend connected to a real remote API (the
// remote's backend in remote mode), both on the same fake site.
type pairHarness struct {
	desktop *service.Backend
	remote  *service.Backend
	server  *Server
	https   *httptest.Server
	down    atomic.Bool
	code    string
	bili    *fakeBili
}

func newPair(t *testing.T) *pairHarness {
	t.Helper()
	dataDir, res := initServer(t)
	p := &pairHarness{bili: newFakeBili(t)}
	p.remote = remoteBackend(t, dataDir, p.bili)
	cfg, _ := LoadConfig(dataDir)
	p.server = New(Options{DataDir: dataDir, TokenHash: cfg.TokenHash, Service: p.remote})
	tlsCfg, err := TLSConfig(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	p.https = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p.down.Load() {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		p.server.ServeHTTP(w, r)
	}))
	p.https.TLS = tlsCfg
	p.https.StartTLS()
	t.Cleanup(p.https.Close)
	conn := res.Conn
	conn.URL = p.https.URL
	p.code = remote.EncodeCode(conn)

	tmp := t.TempDir()
	loc, err := appdirs.ResolveWithOptions(appdirs.Options{Channel: "dev", ForceMode: "portable", DataDir: filepath.Join(tmp, "data"), CacheDir: filepath.Join(tmp, "cache")})
	if err != nil {
		t.Fatal(err)
	}
	bo := service.Options{Locations: loc, Desktop: service.NewMockDesktop(), DisableBgWorkers: true}
	p.bili.options(&bo)
	p.desktop, err = service.NewBackend(bo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.desktop.Close() })
	return p
}

func (p *pairHarness) remoteSite(t *testing.T) remote.SiteStatus {
	t.Helper()
	for _, r := range p.server.Status().Sites {
		if r.Site == "bilibili" {
			return r
		}
	}
	t.Fatal("no bilibili row")
	return remote.SiteStatus{}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The desktop connects with a code, keeps the token out of
// settings.json, and maps each failure to its own message.
func TestDesktopConnectAndErrorKinds(t *testing.T) {
	p := newPair(t)
	ctx := context.Background()
	if _, err := p.desktop.RemoteStatus(ctx); err != service.ErrNoRemote {
		t.Fatalf("status before connecting = %v, want ErrNoRemote", err)
	}
	view, err := p.desktop.ConnectRemote(ctx, service.RemoteConnectInput{Code: p.code})
	if err != nil {
		t.Fatalf("ConnectRemote: %v", err)
	}
	if view.Connection.Name != "测试远程" || view.Status.Version == "" {
		t.Fatalf("view = %+v", view)
	}
	conn, _ := remote.DecodeCode(p.code)
	s := p.desktop.GetSettings()
	if len(s.Remote.Connections) != 1 || s.Remote.Connections[0].Fingerprint != conn.Fingerprint {
		t.Fatalf("saved connection = %+v", s.Remote)
	}
	if raw := mustReadSettings(t, p.desktop); strings.Contains(raw, conn.Token) {
		t.Fatal("settings.json contains the token")
	}

	// A wrong token is not saved.
	bad := conn
	bad.URL = p.https.URL
	bad.Token = strings.Repeat("z", 43)
	if _, err := p.desktop.ConnectRemote(ctx, service.RemoteConnectInput{Code: remote.EncodeCode(bad)}); remote.KindOf(err) != remote.KindUnauthorized {
		t.Fatalf("wrong token: %v", err)
	}
	if info, _ := p.desktop.RemoteConnection(); info.Fingerprint != conn.Fingerprint {
		t.Fatal("a failed connect replaced the saved connection")
	}

	// Version and rate-limit answers from a server (synthetic responses).
	answers := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/status") {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":{"code":"protocol","message":"x","serverVersion":"9.9.9","serverProtocol":"2"}}`))
			return
		}
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"rate_limited","message":"x","retryAfterSecs":600}}`))
	}))
	defer answers.Close()
	c, _ := remote.NewClient(remote.Connection{URL: answers.URL, Token: conn.Token, Fingerprint: remote.Fingerprint(answers.Certificate().Raw)}, "1.2.3")
	defer c.Close()
	_, err = c.Status(ctx)
	var re *remote.Error
	if remote.KindOf(err) != remote.KindVersion || !asRemoteErr(err, &re) || re.ServerVersion != "9.9.9" || re.LocalVersion != "1.2.3" || !strings.Contains(err.Error(), "9.9.9") || !strings.Contains(err.Error(), "1.2.3") {
		t.Fatalf("version error = %v", err)
	}
	if _, err := c.Subscriptions(ctx); remote.KindOf(err) != remote.KindRateLimited {
		t.Fatalf("rate limited = %v", err)
	}

	if err := p.desktop.RemoveRemote(); err != nil {
		t.Fatal(err)
	}
	if _, ok := p.desktop.RemoteConnection(); ok {
		t.Fatal("RemoveRemote kept the connection")
	}
}

// A local login pushes the Cookie to the remote automatically; a
// push that cannot reach the remote is kept as "Cookie 待同步" and sent on
// the next retry; a Cookie the remote rejects is not retried.
func TestDesktopPushesCookieAfterLoginAndRetriesPending(t *testing.T) {
	p := newPair(t)
	ctx := context.Background()
	if _, err := p.desktop.ConnectRemote(ctx, service.RemoteConnectInput{Code: p.code}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.desktop.ImportCookie(ctx, sentinelCookie(), ""); err != nil {
		t.Fatalf("local login: %v", err)
	}
	waitFor(t, "the automatic push", func() bool { return p.remoteSite(t).State == remote.SiteValid })
	if acc := p.remote.AccountStatus(); acc.Source != service.CredentialSourceRemotePush || acc.HasRefreshToken {
		t.Fatalf("remote credential = %+v, want pushed without a refresh token", acc)
	}

	p.down.Store(true)
	if _, err := p.desktop.PushCredentialToRemote(ctx, "bilibili"); err == nil {
		t.Fatal("push to a down remote succeeded")
	}
	if info, _ := p.desktop.RemoteConnection(); len(info.PendingCookies) != 1 || info.PendingCookies[0] != "bilibili" {
		t.Fatalf("pending = %+v, want bilibili", info.PendingCookies)
	}
	if p.desktop.RetryPendingRemoteCookies(ctx) {
		t.Fatal("retry while down reported success")
	}
	p.down.Store(false)
	if !p.desktop.RetryPendingRemoteCookies(ctx) {
		t.Fatal("retry after the remote came back did not push")
	}
	if info, _ := p.desktop.RemoteConnection(); len(info.PendingCookies) != 0 {
		t.Fatalf("pending after a successful retry = %+v", info.PendingCookies)
	}

	// The remote's site check refuses the Cookie: the reason comes back,
	// the remote keeps its login and nothing stays pending.
	p.bili.expired.Store(true)
	_, err := p.desktop.PushCredentialToRemote(ctx, "bilibili")
	if remote.KindOf(err) != remote.KindRejected || !strings.Contains(err.Error(), "远程拒绝了 Cookie") {
		t.Fatalf("rejected push = %v", err)
	}
	if info, _ := p.desktop.RemoteConnection(); len(info.PendingCookies) != 0 {
		t.Fatalf("a rejected Cookie stays pending: %+v", info.PendingCookies)
	}
	if acc := p.remote.AccountStatus(); !acc.LoggedIn {
		t.Fatal("the rejected push removed the remote's login")
	}

	if err := p.desktop.ClearRemoteCredential(ctx, "bilibili"); err != nil {
		t.Fatal(err)
	}
	if p.remoteSite(t).State != remote.SiteNone {
		t.Fatal("清除远程登录 kept the remote's login")
	}
}

// 有改动未同步 follows the snapshot hash: a sync makes both sides
// equal; a rename makes them differ; a refreshed account name does not.
// The remote replaces its subscriptions with the desktop's.
func TestDesktopSyncStateFollowsSnapshotHash(t *testing.T) {
	p := newPair(t)
	ctx := context.Background()
	if _, err := p.desktop.ConnectRemote(ctx, service.RemoteConnectInput{Code: p.code}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.desktop.ImportCookie(ctx, sentinelCookie(), ""); err != nil {
		t.Fatal(err)
	}
	sub, err := p.desktop.CreateSubscription("合成订阅", "", []subscription.AccountRef{{Site: "bilibili", UID: "1001", Name: "UP"}}, subscription.DefaultSubRules())
	if err != nil {
		t.Fatal(err)
	}
	view, err := p.desktop.RemoteStatus(ctx)
	if err != nil || view.InSync {
		t.Fatalf("before sync: InSync=%v err=%v", view.InSync, err)
	}
	info, err := p.desktop.SyncSubscriptionsToRemote(ctx)
	if err != nil || info.Subscriptions != 1 || info.Accounts != 1 {
		t.Fatalf("sync = %+v, %v", info, err)
	}
	view, _ = p.desktop.RemoteStatus(ctx)
	if !view.InSync || view.LocalHash != view.Status.Subscriptions.Hash {
		t.Fatalf("after sync: %+v", view)
	}
	remoteSubs, _ := p.remote.ListSubscriptions()
	if len(remoteSubs) != 1 || remoteSubs[0].ID != sub.ID || remoteSubs[0].Name != "合成订阅" {
		t.Fatalf("remote subscriptions = %+v", remoteSubs)
	}

	if err := p.desktop.SubscriptionManager().UpdateAccountSnapshot("bilibili", "1001", "UP 新名字", "", ""); err != nil {
		t.Fatal(err)
	}
	if view, _ = p.desktop.RemoteStatus(ctx); !view.InSync {
		t.Fatal("a refreshed account name marked the subscriptions as changed")
	}
	sub.Name = "改过的名字"
	if _, err := p.desktop.UpsertSubscription(sub); err != nil {
		t.Fatal(err)
	}
	if view, _ = p.desktop.RemoteStatus(ctx); view.InSync {
		t.Fatal("a rename did not mark the subscriptions as changed")
	}
	if err := p.desktop.DeleteSubscription(sub.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := p.desktop.SyncSubscriptionsToRemote(ctx); err != nil {
		t.Fatal(err)
	}
	if remoteSubs, _ = p.remote.ListSubscriptions(); len(remoteSubs) != 0 {
		t.Fatalf("a sync of no subscriptions left %d on the remote", len(remoteSubs))
	}
}

func asRemoteErr(err error, out **remote.Error) bool {
	re, ok := err.(*remote.Error)
	if ok {
		*out = re
	}
	return ok
}

func mustReadSettings(t *testing.T, b *service.Backend) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(b.Locations().DataDir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
