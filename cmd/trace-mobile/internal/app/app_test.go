package app

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"image/png"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/cmd/trace-mobile/internal/pagekit"
	"github.com/chialecode/trace/internal/companionapi"
	"github.com/chialecode/trace/internal/companionclient"
	"github.com/egoist/mygo/ui"
)

func newTestApp(store Store) *App {
	if store == nil {
		store = NewMemoryStore()
	}
	return New(Options{
		Store:       store,
		Channel:     "dev",
		AppVersion:  "0.1.0-dev",
		DeviceName:  "Pixel Test",
		DeviceModel: "Pixel 9",
		OnlineFn:    func() bool { return true },
		OpenURLFn:   func(string) error { return nil },
	})
}

func assertNoHorizontalOverflow(t *testing.T, tt *ui.Tester, width int) {
	t.Helper()
	for _, info := range tt.AccessAll() {
		if info.Bounds.W <= 0 || info.Bounds.H <= 0 || info.Offscreen {
			continue
		}
		if info.Bounds.X < -1 || info.Bounds.X+info.Bounds.W > float32(width)+1 {
			t.Fatalf("horizontal overflow at width=%d on %q (%s): rect=%+v", width, info.Name, info.Value, info.Bounds)
		}
	}
}

func writeTestShot(t *testing.T, tt *ui.Tester, name string) {
	t.Helper()
	if os.Getenv("TRACE_MOBILE_UISHOTS") != "1" {
		return
	}
	// Write under repo-root/artifacts/mobile-uishots/ (gitignored).
	dir := filepath.Join("..", "..", "..", "..", "artifacts", "mobile-uishots")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, name+".png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, tt.Image()); err != nil {
		t.Fatal(err)
	}
}

type testUpdateQueue struct {
	mu     sync.Mutex
	fns    []func()
	notify chan struct{}
}

func newTestUpdateQueue() *testUpdateQueue {
	return &testUpdateQueue{notify: make(chan struct{}, 1)}
}

func (q *testUpdateQueue) post(fn func()) {
	q.mu.Lock()
	if fn != nil {
		q.fns = append(q.fns, fn)
	}
	q.mu.Unlock()
	select {
	case q.notify <- struct{}{}:
	default:
	}
}

func (q *testUpdateQueue) drain() {
	for {
		q.mu.Lock()
		batch := q.fns
		q.fns = nil
		q.mu.Unlock()
		if len(batch) == 0 {
			return
		}
		for _, fn := range batch {
			fn()
		}
	}
}

func TestPairingGuideManualIdentityConfirmAndIMEGuard(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	fp := companionclient.Fingerprint(der)
	wantIDCode := companionclient.IdentityCode(fp)

	var pairHits atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/pair":
			pairHits.Add(1)
			_ = json.NewEncoder(w).Encode(companionapi.PairStartResponse{PairID: "pair-1", Status: "pending"})
		case "/api/v1/pair/pair-1":
			_ = json.NewEncoder(w).Encode(companionapi.PairPollResponse{
				Status:  "approved",
				Token:   "tok-32-bytes-secret-abcdef123456",
				Node:    "node-pc-1",
				Name:    "Desk-PC",
				Channel: "dev",
			})
		}
	}))
	srv.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
	}
	srv.StartTLS()
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "https://")

	store := NewMemoryStore()
	a := newTestApp(store)
	tt := ui.NewTester(a.View, 412, 915)
	q := newTestUpdateQueue()
	a.postUpdate = q.post
	pumpUntil := func(cond func() bool) {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			q.drain()
			tt.Frame()
			if cond() {
				return
			}
			select {
			case <-q.notify:
			case <-time.After(10 * time.Millisecond):
			}
		}
	}

	if !tt.HasText("连接你的电脑") {
		t.Fatal("expected pairing guide title")
	}
	assertNoHorizontalOverflow(t, tt, 412)
	writeTestShot(t, tt, "pairing-guide-412x915")

	// Open manual input form.
	if err := tt.Click("手动输入"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("8 位配对码") {
		t.Fatal("expected manual pairing form")
	}

	// Verify IME composition + Enter does NOT submit (NFR-MOB-UI-001).
	_ = tt.Click("配对码输入框")
	tt.Compose("3F7K", 4)
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if a.pair.step != pairStepManualForm {
		t.Fatalf("Enter during IME composition must not submit; step=%v", a.pair.step)
	}
	tt.Compose("", 0)

	// Fill valid address and 8-char code.
	a.pair.manualAddr = addr
	a.pair.manualCode = "3F7K-9Q2M"
	tt.Frame()
	writeTestShot(t, tt, "pairing-manual-412x915")

	// Click 下一步 -> probes identity WITHOUT sending POST /api/v1/pair.
	if err := tt.Click("下一步核对身份码"); err != nil {
		t.Fatal(err)
	}
	pumpUntil(func() bool { return a.pair.step == pairStepIdentityConfirm })
	if a.pair.step != pairStepIdentityConfirm {
		t.Fatalf("expected identity confirm step, got %v (err=%q)", a.pair.step, a.pair.errMsg)
	}
	if pairHits.Load() != 0 {
		t.Fatalf("POST /pair must NOT be sent before identity confirmation, got %d", pairHits.Load())
	}
	if !tt.HasText(wantIDCode) {
		t.Fatalf("expected identity code %q on screen", wantIDCode)
	}
	writeTestShot(t, tt, "pairing-identity-confirm-412x915")

	// Abort on mismatch ("不一致，取消") -> zero /pair hits.
	if err := tt.Click("不一致，取消配对"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.pair.step != pairStepManualForm || pairHits.Load() != 0 {
		t.Fatalf("abort should return to manual form with 0 pair hits; step=%v hits=%d", a.pair.step, pairHits.Load())
	}

	// Re-probe and confirm ("一致，继续").
	if err := tt.Click("下一步核对身份码"); err != nil {
		t.Fatal(err)
	}
	pumpUntil(func() bool { return a.pair.step == pairStepIdentityConfirm })
	if err := tt.Click("一致，继续配对"); err != nil {
		t.Fatal(err)
	}
	pumpUntil(func() bool {
		_, ok := store.LoadPairing()
		return ok
	})
	rec, ok := store.LoadPairing()
	if !ok {
		t.Fatalf("expected pairing record saved after approval (err=%q)", a.pair.errMsg)
	}
	if rec.FP != fp || rec.Node != "node-pc-1" {
		t.Fatalf("unexpected saved pairing record: %+v", rec)
	}
	tok, err := store.LoadToken()
	if err != nil || tok != "tok-32-bytes-secret-abcdef123456" {
		t.Fatalf("unexpected saved token: %q (%v)", tok, err)
	}
	a.SetForeground(false)
}

func TestConnectionGateAllReasonsAndResponsiveSizes(t *testing.T) {
	store := NewMemoryStore()
	_ = store.SavePairing(companionclient.PairingRecord{
		Node:     "node-1",
		Name:     "Studio-PC",
		Channel:  "dev",
		FP:       strings.Repeat("a", 64),
		Addrs:    []string{"192.168.1.20:47382"},
		LastAddr: "192.168.1.20:47382",
	})
	_ = store.SaveToken("tok-123456789012345678901234567890")

	cases := []struct {
		name     string
		state    pagekit.ConnState
		err      *companionclient.Error
		wantText string
		wantBtns []string
		noBtns   []string
	}{
		{
			name:     "connecting",
			state:    pagekit.ConnConnecting,
			wantText: "正在连接 192.168.1.20:47382…",
			wantBtns: []string{"取消连接"},
		},
		{
			name:     "tcp_refused",
			state:    pagekit.ConnError,
			err:      &companionclient.Error{Kind: companionclient.KindConnRefused},
			wantText: "电脑上的 TRACE 没有运行",
			wantBtns: []string{"重试连接", "重新扫码"},
		},
		{
			name:     "unreachable",
			state:    pagekit.ConnError,
			err:      &companionclient.Error{Kind: companionclient.KindUnreachable},
			wantText: "找不到电脑",
			wantBtns: []string{"重试连接", "重新扫码"},
		},
		{
			name:     "phone_offline",
			state:    pagekit.ConnError,
			err:      &companionclient.Error{Kind: companionclient.KindPhoneOffline},
			wantText: "手机没有连接网络",
			noBtns:   []string{"重试连接", "重新扫码"},
		},
		{
			name:     "cert_changed",
			state:    pagekit.ConnError,
			err:      &companionclient.Error{Kind: companionclient.KindCertChanged},
			wantText: "电脑的身份变了",
			wantBtns: []string{"重新扫码"},
			noBtns:   []string{"重试连接"},
		},
		{
			name:     "unauthorized_401",
			state:    pagekit.ConnError,
			err:      &companionclient.Error{Kind: companionclient.KindUnauthorized},
			wantText: "这台电脑已移除本机",
			wantBtns: []string{"重新扫码"},
			noBtns:   []string{"重试连接"},
		},
		{
			name:     "forbidden_source_403",
			state:    pagekit.ConnError,
			err:      &companionclient.Error{Kind: companionclient.KindForbiddenSource},
			wantText: "同一局域网或 VPN",
			wantBtns: []string{"重试连接"},
			noBtns:   []string{"重新扫码"},
		},
		{
			name:  "protocol_mismatch_409",
			state: pagekit.ConnError,
			err: &companionclient.Error{
				Kind:           companionclient.KindProtocolMismatch,
				ServerVersion:  "0.2.0",
				ServerProtocol: "2",
				ClientVersion:  "0.1.0",
				ClientProtocol: "1",
				UpdateSide:     "mobile",
			},
			wantText: "请更新手机上的 TRACE",
			wantBtns: []string{"打开下载页"},
		},
		{
			name:     "rate_limited_429",
			state:    pagekit.ConnError,
			err:      &companionclient.Error{Kind: companionclient.KindRateLimited},
			wantText: "尝试次数过多",
			noBtns:   []string{"重试连接", "重新扫码"},
		},
		{
			name:     "desktop_logged_out",
			state:    pagekit.ConnError,
			err:      &companionclient.Error{Kind: companionclient.KindDesktopLoggedOut},
			wantText: "请先在电脑上登录一个站点",
			wantBtns: []string{"重试连接"},
		},
		{
			name:     "bye_exit",
			state:    pagekit.ConnError,
			err:      companionclient.ByeError("exit"),
			wantText: "电脑上的 TRACE 已退出",
			wantBtns: []string{"重试连接", "重新扫码"},
		},
		{
			name:     "bye_update",
			state:    pagekit.ConnError,
			err:      companionclient.ByeError("update"),
			wantText: "电脑上的 TRACE 正在更新",
			wantBtns: []string{"重试连接", "重新扫码"},
		},
		{
			name:     "bye_disabled",
			state:    pagekit.ConnError,
			err:      companionclient.ByeError("disabled"),
			wantText: "电脑上的 TRACE 已关闭手机连接",
			wantBtns: []string{"重试连接", "重新扫码"},
		},
		{
			name:     "bye_revoked",
			state:    pagekit.ConnError,
			err:      companionclient.ByeError("revoked"),
			wantText: "电脑上的 TRACE 已移除本机",
			wantBtns: []string{"重试连接", "重新扫码"},
		},
	}

	sizes := [][2]int{{360, 800}, {412, 915}, {800, 1280}}
	for _, tc := range cases {
		a := newTestApp(store)
		a.connState = tc.state
		a.lastErr = tc.err
		for _, sz := range sizes {
			tt := ui.NewTester(a.View, sz[0], sz[1])
			if !tt.HasText(tc.wantText) {
				t.Fatalf("%s (%dx%d): missing text %q; got %v", tc.name, sz[0], sz[1], tc.wantText, tt.Texts())
			}
			for _, b := range tc.wantBtns {
				if _, ok := tt.Find(b); !ok {
					t.Fatalf("%s (%dx%d): expected button %q", tc.name, sz[0], sz[1], b)
				}
			}
			for _, nb := range tc.noBtns {
				if _, ok := tt.Find(nb); ok {
					t.Fatalf("%s (%dx%d): unexpected button %q", tc.name, sz[0], sz[1], nb)
				}
			}
			assertNoHorizontalOverflow(t, tt, sz[0])
			if sz[0] == 412 && (tc.name == "tcp_refused" || tc.name == "protocol_mismatch_409") {
				writeTestShot(t, tt, "conn-"+tc.name+"-412x915")
			}
		}
	}
}

func TestConnectedTabsComputerPageAndUnpair(t *testing.T) {
	store := NewMemoryStore()
	_ = store.SavePairing(companionclient.PairingRecord{
		Node:     "node-1",
		Name:     "Workstation-PC",
		Channel:  "dev",
		FP:       strings.Repeat("b", 64),
		Addrs:    []string{"192.168.1.20:47382"},
		LastAddr: "192.168.1.20:47382",
	})
	_ = store.SaveToken("tok-123456789012345678901234567890")

	a := newTestApp(store)
	a.connState = pagekit.ConnConnected
	a.tasksBadge = 3
	a.desktop = pagekit.DesktopSnapshot{
		NodeID:       "node-1",
		ComputerName: "Workstation-PC",
		Version:      "0.1.0-dev",
		Channel:      "dev",
		ChannelLabel: "开发版",
		UptimeText:   "2 小时 15 分钟",
		CurrentAddr:  "192.168.1.20:47382",
		DiskFree:     128 << 30,
		DiskTotal:    512 << 30,
		LoggedIn:     true,
		Sites: []pagekit.SiteAuthStatus{
			{Site: "bilibili", Label: "B 站", State: "logged_in", Nickname: "Alice"},
			{Site: "douyin", Label: "抖音", State: "logged_out"},
			{Site: "weibo", Label: "微博", State: "expired", Nickname: "Bob"},
			{Site: "x", Label: "X", State: "logged_in", Nickname: "Carol"},
		},
	}

	for _, sz := range [][2]int{{360, 800}, {412, 915}, {800, 1280}} {
		a.activeTab = int(pagekit.TabHome)
		tt := ui.NewTester(a.View, sz[0], sz[1])
		a.invalidate = tt.Frame
		if !tt.HasText("主页信息流") {
			t.Fatalf("%dx%d: expected 主页 placeholder", sz[0], sz[1])
		}
		assertNoHorizontalOverflow(t, tt, sz[0])
		writeTestShot(t, tt, "tab-home-"+itoa(sz[0])+"x"+itoa(sz[1]))

		// Switch to 我的电脑 tab
		a.activeTab = int(pagekit.TabComputer)
		tt.Frame()
		for _, want := range []string{"Workstation-PC", "0.1.0-dev（开发版）", "2 小时 15 分钟", "192.168.1.20:47382", "B 站", "抖音", "微博", "X", "已登录 · Alice", "未登录", "已失效 · Bob", "解除配对"} {
			if !tt.HasText(want) {
				t.Fatalf("%dx%d 我的电脑 missing %q", sz[0], sz[1], want)
			}
		}
		assertNoHorizontalOverflow(t, tt, sz[0])
		writeTestShot(t, tt, "tab-computer-"+itoa(sz[0])+"x"+itoa(sz[1]))
	}

	// Confirm two-step Unpair clears store and returns to pairing guide.
	a.activeTab = int(pagekit.TabComputer)
	tt := ui.NewTester(a.View, 412, 915)
	a.invalidate = tt.Frame
	if err := tt.Click("解除配对"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("确认解除配对"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.connState != pagekit.ConnUnpaired {
		t.Fatalf("expected ConnUnpaired after unpair, got %v", a.connState)
	}
	if _, ok := store.LoadPairing(); ok {
		t.Fatal("expected pairing record cleared after unpair")
	}
	if _, err := store.LoadToken(); err == nil {
		t.Fatal("expected token cleared after unpair")
	}
}

func TestForegroundBackgroundAndNetworkLifecycle(t *testing.T) {
	store := NewMemoryStore()
	_ = store.SavePairing(companionclient.PairingRecord{
		Node:     "node-1",
		Name:     "Desk-PC",
		Channel:  "dev",
		FP:       strings.Repeat("c", 64),
		Addrs:    []string{"127.0.0.1:47382"},
		LastAddr: "127.0.0.1:47382",
	})
	_ = store.SaveToken("tok-123456789012345678901234567890")

	online := true
	a := New(Options{
		Store:      store,
		Channel:    "dev",
		AppVersion: "0.1.0-dev",
		OnlineFn:   func() bool { return online },
	})
	a.imgCache.Put("k", []byte("img-bytes"))
	if a.imgCache.Bytes() == 0 {
		t.Fatal("expected cached bytes")
	}

	// Entering background clears in-memory image cache and prevents connecting.
	a.SetForeground(false)
	if a.imgCache.Bytes() != 0 {
		t.Fatalf("expected image cache cleared on suspend, got %d bytes", a.imgCache.Bytes())
	}

	// Going offline reports KindPhoneOffline.
	a.SetForeground(true)
	online = false
	a.OnNetworkChanged(false)
	if a.lastErr == nil || a.lastErr.Kind != companionclient.KindPhoneOffline {
		t.Fatalf("expected KindPhoneOffline, got %+v", a.lastErr)
	}
	a.SetForeground(false)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func makeSelfSignedTLSServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	fp := companionclient.Fingerprint(der)
	srv := httptest.NewUnstartedServer(handler)
	srv.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
	}
	srv.StartTLS()
	return srv, strings.TrimPrefix(srv.URL, "https://"), fp
}

func TestMDNSAutoDiscoveryPinnedCertSuccessAndRogueRejection(t *testing.T) {
	// 1. Legitimate desktop moved to a new address with SAME pinned TLS cert fingerprint.
	goodSrv, goodAddr, goodFP := makeSelfSignedTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case companionapi.PathStatus:
			_ = json.NewEncoder(w).Encode(companionapi.StatusResponse{
				Node:     "node-mdns-1",
				Name:     "Moved-PC",
				Version:  "0.1.0-dev",
				Channel:  "dev",
				Protocol: companionapi.ProtocolVersion,
				LoggedIn: true,
			})
		default:
			http.NotFound(w, r)
		}
	})
	defer goodSrv.Close()

	// Spin up a decoy TCP listener representing the stale pre-IP-change LastAddr
	// so we can assert tryMDNSReconnect NEVER re-dials the stale LastAddr first.
	oldLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer oldLn.Close()
	var oldDialHits atomic.Int32
	go func() {
		for {
			c, acceptErr := oldLn.Accept()
			if acceptErr != nil {
				return
			}
			oldDialHits.Add(1)
			_ = c.Close()
		}
	}()
	oldAddr := oldLn.Addr().String()

	store := NewMemoryStore()
	_ = store.SavePairing(companionclient.PairingRecord{
		Node:     "node-mdns-1",
		Name:     "Moved-PC",
		Channel:  "dev",
		FP:       goodFP,
		Addrs:    []string{oldAddr}, // stale old IP/port
		LastAddr: oldAddr,
	})
	_ = store.SaveToken("tok-mdns-secret-1234567890123456")

	a := newTestApp(store)
	a.mdnsDiscoverFn = func(_ context.Context, nodeID, channel string) ([]string, error) {
		if nodeID != "node-mdns-1" || channel != "dev" {
			t.Fatalf("unexpected mDNS query: node=%q channel=%q", nodeID, channel)
		}
		return []string{goodAddr}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rec, _ := store.LoadPairing()
	tok, _ := store.LoadToken()
	cli, st, activeAddr, found, err := a.tryMDNSReconnect(ctx, rec, tok)
	if err != nil {
		t.Fatalf("expected mDNS reconnect with matching pinned cert to succeed, got %v", err)
	}
	cli.Close()
	if hits := oldDialHits.Load(); hits != 0 {
		t.Fatalf("tryMDNSReconnect must not re-dial stale LastAddr %q, got %d dial(s)", oldAddr, hits)
	}
	if activeAddr != goodAddr || st.Name != "Moved-PC" || len(found) != 1 {
		t.Fatalf("unexpected mDNS reconnect result: addr=%q name=%q found=%v", activeAddr, st.Name, found)
	}
	merged := mergeUniqueAddrs(found, rec.Addrs)
	if len(merged) != 2 || merged[0] != goodAddr || merged[1] != oldAddr {
		t.Fatalf("unexpected merged addrs: %v", merged)
	}

	// 2. Rogue mDNS advertiser presenting a DIFFERENT certificate MUST be rejected with KindCertChanged!
	rogueSrv, rogueAddr, rogueFP := makeSelfSignedTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(companionapi.StatusResponse{
			Node:     "node-mdns-1",
			Name:     "Evil-PC",
			Channel:  "dev",
			Protocol: companionapi.ProtocolVersion,
			LoggedIn: true,
		})
	})
	defer rogueSrv.Close()
	if rogueFP == goodFP {
		t.Fatal("expected distinct cert fingerprint for rogue server")
	}

	aRogue := newTestApp(store)
	aRogue.mdnsDiscoverFn = func(context.Context, string, string) ([]string, error) {
		return []string{rogueAddr}, nil
	}
	_, _, _, _, err = aRogue.tryMDNSReconnect(ctx, rec, tok)
	if err == nil {
		t.Fatal("SECURITY FAILURE: rogue mDNS server with unpinned cert was trusted!")
	}
	if companionclient.KindOf(err) != companionclient.KindCertChanged {
		t.Fatalf("expected KindCertChanged for rogue cert, got %v (%v)", companionclient.KindOf(err), err)
	}
	// Stored fingerprint must remain completely unchanged.
	afterRec, _ := store.LoadPairing()
	if afterRec.FP != goodFP {
		t.Fatalf("stored fingerprint mutated from %q to %q", goodFP, afterRec.FP)
	}
}

func TestShareIntentDisconnectedBannerAndConnectedAutoSubmit(t *testing.T) {
	var (
		mu          sync.Mutex
		parsedTexts []string
	)
	srv, addr, fp := makeSelfSignedTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/parse-and-cache":
			var req struct {
				Input string `json:"input"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			mu.Lock()
			parsedTexts = append(parsedTexts, req.Input)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true, "taskId": "dl-share-1", "site": "bilibili", "title": "分享测试视频",
			})
		default:
			http.NotFound(w, r)
		}
	})
	defer srv.Close()

	store := NewMemoryStore()
	rec := companionclient.PairingRecord{
		Node: "node-share", Name: "Desk-PC", Channel: "dev", FP: fp, Addrs: []string{addr}, LastAddr: addr,
	}
	_ = store.SavePairing(rec)
	_ = store.SaveToken("tok-share-secret-123456789012345")

	a := newTestApp(store)
	a.connState = pagekit.ConnError
	a.lastErr = &companionclient.Error{Kind: companionclient.KindConnRefused}

	// 1. Receive Android ACTION_SEND share while disconnected -> shows pending share banner on connection gate.
	a.HandleSharedText("【分享】https://www.bilibili.com/video/BV1SHARE")
	tt := ui.NewTester(a.View, 360, 800)
	if !tt.HasText("已收到分享链接，连接电脑后将自动发送缓存") {
		t.Fatalf("expected pending share banner on connection gate, texts=%v", tt.Texts())
	}
	assertNoHorizontalOverflow(t, tt, 360)

	// Dismiss pending share banner.
	if err := tt.Click("取消待发送的分享链接"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.pendingShareSnapshot() != "" {
		t.Fatal("expected pending share text cleared after dismiss")
	}

	// 2. Connected state -> HandleSharedText immediately submits to POST /api/v1/parse-and-cache!
	cli, err := companionclient.NewClient(rec, "tok-share-secret-123456789012345", "0.1.0-dev", "dev")
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	a.client = cli
	a.connState = pagekit.ConnConnected

	q := newTestUpdateQueue()
	a.postUpdate = q.post
	a.HandleSharedText("https://www.bilibili.com/video/BV1LIVE")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		q.drain()
		tt.Frame()
		mu.Lock()
		n := len(parsedTexts)
		mu.Unlock()
		if n == 1 && tt.HasText("已加入电脑缓存队列") {
			break
		}
		select {
		case <-q.notify:
		case <-time.After(10 * time.Millisecond):
		}
	}
	q.drain()
	for range 12 {
		tt.Advance(100 * time.Millisecond)
		q.drain()
	}
	tt.Frame()
	mu.Lock()
	got := append([]string(nil), parsedTexts...)
	mu.Unlock()
	if len(got) != 1 || got[0] != "https://www.bilibili.com/video/BV1LIVE" {
		t.Fatalf("expected share text auto-submitted to desktop, got %v", got)
	}
	if !tt.HasText("已加入电脑缓存队列") || !tt.HasText("分享测试视频") {
		t.Fatalf("expected share sheet result card, texts=%v", tt.Texts())
	}
	assertNoHorizontalOverflow(t, tt, 360)
}
