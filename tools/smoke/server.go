package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/remote"
	"github.com/chialecode/trace/internal/remoteserver"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site/weibo"
	"github.com/chialecode/trace/internal/sitecheck"
	"github.com/chialecode/trace/internal/storage"
	"github.com/chialecode/trace/internal/subscription"
)

// ServerSmokeReceipt is artifacts/smoke-server/smoke.json: trace-server run
// as a real process in a temporary folder — init, start, status with the
// connection code, a synthetic subscription snapshot, one synthetic item
// uploaded through a fake rclone, fake-Weibo credential push & snapshot check,
// then stop (docs/design/remote.md §7).
type ServerSmokeReceipt struct {
	Passed                bool     `json:"passed"`
	OS                    string   `json:"os"`
	Arch                  string   `json:"arch"`
	Binary                string   `json:"binary"`
	Version               string   `json:"version"`
	InitCodePrinted       bool     `json:"initCodePrinted"`
	PrivateFiles          bool     `json:"privateFiles"`
	Started               bool     `json:"started"`
	StatusRead            bool     `json:"statusRead"`
	WeiboSiteReported     bool     `json:"weiboSiteReported"`
	SnapshotSynced        bool     `json:"snapshotSynced"`
	ItemUploaded          bool     `json:"itemUploaded"`
	SourceRemoved         bool     `json:"sourceRemoved"`
	WeiboFakeSiteVerified bool     `json:"weiboFakeSiteVerified"`
	StopHow               string   `json:"stopHow"` // "SIGTERM" (Linux) or "kill+restart" (Windows)
	StoppedCleanly        bool     `json:"stoppedCleanly"`
	LockReleased          bool     `json:"lockReleased"`
	NoTokenInFiles        bool     `json:"noTokenInFiles"`
	NoSecretsInFiles      bool     `json:"noSecretsInFiles"`
	DurationMs            int64    `json:"durationMs"`
	Failures              []string `json:"failures,omitempty"`
}

const smokeServerItem = "video_smoke"

func runServerSmokeMain(bin, out string) int {
	if _, err := validateArtifactsOutDir(defaultArtifactsRoot(), out); err != nil {
		fmt.Fprintf(os.Stderr, "server smoke FAILED: %v\n", err)
		return 1
	}
	started := time.Now()
	rec, err := runServerSmoke(bin)
	rec.DurationMs = time.Since(started).Milliseconds()
	if err != nil {
		rec.Failures = append(rec.Failures, err.Error())
		rec.Passed = false
	}
	if writeErr := writeServerReceiptIn(defaultArtifactsRoot(), out, rec); writeErr != nil {
		fmt.Fprintf(os.Stderr, "server smoke: write receipt error: %v\n", writeErr)
		if !rec.Passed {
			fmt.Fprintf(os.Stderr, "server smoke FAILED (%dms): %v\n", rec.DurationMs, rec.Failures)
		}
		return 1
	}
	if !rec.Passed {
		fmt.Fprintf(os.Stderr, "server smoke FAILED (%dms): %v\n", rec.DurationMs, rec.Failures)
		return 1
	}
	fmt.Printf("server smoke PASSED in %dms (%s/%s, %s, stop=%s)\n", rec.DurationMs, rec.OS, rec.Arch, rec.Version, rec.StopHow)
	return 0
}

func writeServerReceiptIn(allowedRoot, out string, rec ServerSmokeReceipt) error {
	if err := cleanArtifactsOutDirIn(allowedRoot, out); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return storage.WriteFileAtomic(filepath.Join(out, "smoke.json"), raw, 0o644)
}

func runServerSmoke(bin string) (ServerSmokeReceipt, error) {
	rec := ServerSmokeReceipt{OS: runtime.GOOS, Arch: runtime.GOARCH}
	fail := func(format string, args ...any) { rec.Failures = append(rec.Failures, fmt.Sprintf(format, args...)) }

	sandbox, err := os.MkdirTemp("", "trace-smoke-server-")
	if err != nil {
		return rec, err
	}
	defer os.RemoveAll(sandbox)
	dataDir, exportDir, cloud := filepath.Join(sandbox, "data"), filepath.Join(sandbox, "export"), filepath.Join(sandbox, "cloud")
	if err := os.MkdirAll(cloud, 0o700); err != nil {
		return rec, err
	}

	if bin == "" {
		binDir, err := os.MkdirTemp("", "trace-smoke-bin-*")
		if err != nil {
			return rec, err
		}
		defer os.RemoveAll(binDir)
		bin = filepath.Join(binDir, "trace-server"+exeSuffix())
		cmd := exec.Command("go", "build", "-o", bin, "./cmd/trace-server")
		cmd.Env = append(filterEnv(os.Environ(), "CGO_ENABLED", "TRACE_QUIET"), "CGO_ENABLED=0", "TRACE_QUIET=1")
		appdirs.SetSubprocessQuiet(cmd)
		if out, err := cmd.CombinedOutput(); err != nil {
			return rec, fmt.Errorf("build trace-server: %w\n%s", err, out)
		}
	}
	bin, _ = filepath.Abs(bin)
	rec.Binary = filepath.Base(bin)
	self, err := os.Executable()
	if err != nil {
		return rec, err
	}

	// Any site request fails at a dead local proxy instead of leaving the
	// machine; the remote has no Cookie anyway.
	env := append(filterEnv(os.Environ(), "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "TRACE_QUIET", "TRACE_FAKE_RCLONE", "TRACE_FAKE_RCLONE_ROOT", "TRACE_FAKE_RCLONE_LOG", "TRACE_FAKE_RCLONE_MODE"),
		"HTTP_PROXY=http://127.0.0.1:9", "HTTPS_PROXY=http://127.0.0.1:9", "NO_PROXY=127.0.0.1,localhost",
		"TRACE_QUIET=1", "TRACE_FAKE_RCLONE=1", "TRACE_FAKE_RCLONE_ROOT="+cloud, "TRACE_FAKE_RCLONE_LOG="+filepath.Join(sandbox, "rclone.log"))

	verCmd := exec.Command(bin, "version")
	verCmd.Env = env
	appdirs.SetSubprocessQuiet(verCmd)
	ver, err := verCmd.Output()
	if err != nil {
		return rec, fmt.Errorf("trace-server version: %w", err)
	}
	rec.Version = strings.TrimPrefix(strings.SplitN(strings.TrimSpace(string(ver)), "\n", 2)[0], "trace-server ")

	port, err := freePort()
	if err != nil {
		return rec, err
	}
	initCmd := exec.Command(bin, "init", "--data", dataDir, "--export", exportDir, "--listen", fmt.Sprintf("127.0.0.1:%d", port), "--public-address", "127.0.0.1", "--name", "smoke")
	initCmd.Env = env
	appdirs.SetSubprocessQuiet(initCmd)
	initOut, err := initCmd.CombinedOutput()
	if err != nil {
		return rec, fmt.Errorf("init: %w\n%s", err, initOut)
	}
	var code string
	for _, line := range strings.Split(string(initOut), "\n") {
		if l := strings.TrimSpace(line); strings.HasPrefix(l, "trace-remote:") {
			code = l
		}
	}
	conn, err := remote.DecodeCode(code)
	if err != nil {
		return rec, fmt.Errorf("init printed no usable code: %v", err)
	}
	rec.InitCodePrinted = true
	rec.PrivateFiles = privateFiles(dataDir)
	if !rec.PrivateFiles && runtime.GOOS != "windows" {
		fail("data directory files are not private (0600/0700)")
	}
	if runtime.GOOS == "windows" {
		rec.PrivateFiles = true // ACLs, not POSIX modes; checked by the Linux server smoke
	}

	// Upload through the fake rclone: this smoke binary acting as rclone.
	if err := setUpload(dataDir, self); err != nil {
		return rec, err
	}
	if err := seedServerItem(dataDir, exportDir); err != nil {
		return rec, fmt.Errorf("seed item: %w", err)
	}

	srv, err := startServer(bin, dataDir, env)
	if err != nil {
		return rec, err
	}
	defer srv.kill()
	rec.Started = true

	client, err := remote.NewClient(conn, rec.Version)
	if err != nil {
		return rec, err
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if st, err := client.Status(ctx); err != nil {
		fail("status: %v", err)
	} else {
		rec.StatusRead = true
		for _, s := range st.Sites {
			if s.Site == "weibo" {
				rec.WeiboSiteReported = true
				break
			}
		}
		if !rec.WeiboSiteReported {
			fail("status sites missing weibo row: %+v", st.Sites)
		}
	}

	snap := subscription.Snapshot{Format: subscription.SnapshotFormat, Version: subscription.SnapshotVersion, Node: &subscription.SnapshotNode{ID: "node-smoke-desktop", Name: "smoke-desktop"},
		Rules: []subscription.CacheRule{{ID: "r-smoke", Name: "冒烟规则"}},
		Subscriptions: []subscription.SnapshotSubscription{{
			ID: "s-smoke", Name: "冒烟订阅", RuleID: "r-smoke",
			Accounts: []subscription.SnapshotAccount{{Site: "weibo", UID: "5000000001", Name: "冒烟微博博主"}},
		}}}
	raw, _ := json.Marshal(snap)
	if info, err := client.PutSubscriptions(ctx, raw); err != nil {
		fail("sync snapshot: %v", err)
	} else if info.Hash != snap.Hash() || info.Subscriptions != 1 {
		fail("synced info = %+v, want hash %s", info, snap.Hash())
	} else {
		rec.SnapshotSynced = true
	}

	itemRel := filepath.Join("bilibili", "1001_UP", "videos", smokeServerItem)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		st, err := client.Status(ctx)
		if err == nil && st.Upload.TodayFiles > 0 && !exists(filepath.Join(exportDir, itemRel)) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	rec.ItemUploaded = exists(filepath.Join(cloud, "trace", itemRel, "P01.mp4")) && exists(filepath.Join(cloud, "trace", itemRel, "item.json"))
	rec.SourceRemoved = !exists(filepath.Join(exportDir, itemRel, "P01.mp4"))
	if !rec.ItemUploaded || !rec.SourceRemoved {
		rlog, _ := os.ReadFile(filepath.Join(sandbox, "rclone.log"))
		fail("item not moved to the fake cloud (uploaded=%v removed=%v); rclone log: %s; server: %s", rec.ItemUploaded, rec.SourceRemoved, rlog, srv.output())
	}

	var restartOutput string
	if runtime.GOOS == "windows" {
		// A console-less child cannot get a console signal here: kill it,
		// then check the data lock is free by starting again and stopping.
		rec.StopHow = "kill+restart"
		srv.kill()
		again, err := startServer(bin, dataDir, env)
		if err != nil {
			fail("restart after kill: %v", err)
		} else {
			if _, err := client.Status(ctx); err != nil {
				fail("status after restart: %v", err)
			}
			again.kill()
			restartOutput = again.output()
			rec.StoppedCleanly, rec.LockReleased = true, true
		}
	} else {
		rec.StopHow = "SIGTERM"
		rec.StoppedCleanly = srv.terminate(40 * time.Second)
		if !rec.StoppedCleanly {
			fail("trace-server did not exit after SIGTERM: %s", srv.output())
		}
		lock, err := appdirs.LockDataDir(dataDir)
		if err == nil {
			lock.Release()
			rec.LockReleased = true
		} else {
			fail("data lock still held after exit: %v", err)
		}
	}

	rec.NoTokenInFiles = !hasTokenLeak(sandbox, conn.Token, srv.output(), restartOutput)
	if !rec.NoTokenInFiles {
		fail("the access token was found in a file or the server output")
	}
	if err := scanDirForSecrets(sandbox); err != nil {
		fail("secret scan in server sandbox: %v", err)
	} else if findings := sitecheck.ScanTextForSecrets(srv.output() + "\n" + restartOutput); len(findings) > 0 {
		fail("secret scan in server output: %s", findings[0])
	} else {
		rec.NoSecretsInFiles = true
	}

	if err := runWeiboFakeRemoteSmoke(ctx); err != nil {
		fail("weibo fake remote smoke: %v", err)
	} else {
		rec.WeiboFakeSiteVerified = true
	}

	rec.Passed = len(rec.Failures) == 0 && rec.InitCodePrinted && rec.Started && rec.StatusRead && rec.WeiboSiteReported && rec.SnapshotSynced &&
		rec.ItemUploaded && rec.SourceRemoved && rec.WeiboFakeSiteVerified && rec.StoppedCleanly && rec.LockReleased && rec.NoTokenInFiles && rec.NoSecretsInFiles
	return rec, nil
}

// runWeiboFakeRemoteSmoke exercises remoteserver against a local httptest fake Weibo
// PC server (0 real network requests): verifies PUT /credentials/weibo validates via
// the strict PC 2-step flow, preserves existing credentials on invalid push, accepts
// Weibo subscription snapshots, reports Weibo login in Status, and leaks zero Cookie
// or signed-URL sentinels into files, logs, or Status JSON.
func runWeiboFakeRemoteSmoke(parentCtx context.Context) error {
	const (
		sentinelSub     = "SENTINEL_SMOKE_WB_SUB_01"
		sentinelSubp    = "SENTINEL_SMOKE_WB_SUBP_02"
		sentinelXsrf    = "SENTINEL_SMOKE_WB_XSRF_03"
		sentinelWbpsess = "SENTINEL_SMOKE_WB_SESS_04"
		selfUID         = "5290000001"
		selfNick        = "合成冒烟微博账号"
		creatorUID      = "5290000002"
		creatorNick     = "合成冒烟微博博主"
	)
	validCookie := "SUB=" + sentinelSub + "; SUBP=" + sentinelSubp + "; XSRF-TOKEN=" + sentinelXsrf + "; WBPSESS=" + sentinelWbpsess

	fakeWB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		ck := r.Header.Get("Cookie")
		if !strings.Contains(ck, "SUB="+sentinelSub) {
			_, _ = fmt.Fprint(w, `{"ok":-100,"url":"https://passport.weibo.com/sso/signin"}`)
			return
		}
		switch r.URL.Path {
		case "/ajax/setting/getBasicInfo":
			_, _ = fmt.Fprintf(w, `{"ok":1,"data":{"screen_name":%q,"domain":"smokeself"}}`, selfNick)
		case "/ajax/profile/info":
			if r.URL.Query().Get("screen_name") == selfNick {
				_, _ = fmt.Fprintf(w, `{"ok":1,"data":{"user":{"idstr":%q,"screen_name":%q,"statuses_count":3}}}`, selfUID, selfNick)
				return
			}
			_, _ = fmt.Fprintf(w, `{"ok":1,"data":{"user":{"idstr":%q,"screen_name":%q,"statuses_count":7}}}`, creatorUID, creatorNick)
		case "/ajax/statuses/mymblog":
			_, _ = fmt.Fprintf(w, `{"ok":1,"data":{"list":[{"idstr":"5099000000000001","mid":"5099000000000001","mblogid":"SmokeWb01","created_at":"Sat Nov 21 12:00:00 +0800 2026","text_raw":"冒烟基线","user":{"idstr":%q,"screen_name":%q}}],"since_id":"","total":1}}`, creatorUID, creatorNick)
		default:
			http.NotFound(w, r)
		}
	}))
	defer fakeWB.Close()

	sandbox, err := os.MkdirTemp("", "trace-smoke-weibo-remote-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(sandbox)
	dataDir := filepath.Join(sandbox, "data")
	exportDir := filepath.Join(sandbox, "export")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	initRes, err := remoteserver.Init(remoteserver.InitOptions{
		DataDir:       dataDir,
		ExportDir:     exportDir,
		Listen:        ln.Addr().String(),
		PublicAddress: "127.0.0.1",
		Name:          "weibo-smoke",
	})
	if err != nil {
		_ = ln.Close()
		return fmt.Errorf("init: %w", err)
	}

	var logBuf bytes.Buffer
	var logMu sync.Mutex
	logMirror := &lockedBuf{mu: &logMu, buf: &logBuf}

	srvCtx, srvCancel := context.WithCancel(parentCtx)
	defer srvCancel()
	ready := make(chan struct{}, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- remoteserver.Run(srvCtx, remoteserver.RunOptions{
			DataDir:   dataDir,
			Listener:  ln,
			LogMirror: logMirror,
			Ready:     func(string) { ready <- struct{}{} },
			Backend: func(bo *service.Options) {
				bo.DisableBgWorkers = true
				bo.WeiboOpts = weibo.ClientOptions{
					AllowLocalhost: true,
					MinInterval:    -1,
					JitterMax:      -1,
					Endpoints: weibo.Endpoints{
						PCBasicInfo:     fakeWB.URL + "/ajax/setting/getBasicInfo",
						ProfileInfo:     fakeWB.URL + "/ajax/profile/info",
						StatusesShow:    fakeWB.URL + "/ajax/statuses/show",
						StatusesLong:    fakeWB.URL + "/ajax/statuses/longtext",
						StatusesMyMblog: fakeWB.URL + "/ajax/statuses/mymblog",
						TVComponent:     fakeWB.URL + "/tv/api/component",
					},
				}
			},
		})
	}()

	select {
	case <-ready:
	case runErr := <-errCh:
		return fmt.Errorf("server exited early: %w", runErr)
	case <-time.After(15 * time.Second):
		srvCancel()
		return fmt.Errorf("server did not start within 15s")
	}

	client, err := remote.NewClient(initRes.Conn, "0.1.3-smoke")
	if err != nil {
		srvCancel()
		<-errCh
		return err
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(parentCtx, 20*time.Second)
	defer cancel()

	// 1. Push valid Weibo cookie -> strict PC 2-step validation must succeed.
	siteSt, err := client.PushCredential(ctx, "weibo", remote.CredentialPush{Cookie: validCookie})
	if err != nil {
		srvCancel()
		<-errCh
		return fmt.Errorf("push valid weibo credential: %w", err)
	}
	if siteSt.State != remote.SiteValid || siteSt.Uname != selfNick {
		srvCancel()
		<-errCh
		return fmt.Errorf("unexpected weibo site status after valid push: %+v", siteSt)
	}

	// 2. Push invalid Weibo cookie -> must fail and preserve existing valid login.
	if _, err := client.PushCredential(ctx, "weibo", remote.CredentialPush{Cookie: "SUB=invalid_sub_value_0001"}); err == nil {
		srvCancel()
		<-errCh
		return fmt.Errorf("invalid weibo credential push unexpectedly succeeded")
	}

	// 3. Sync Weibo subscription snapshot.
	snap := subscription.Snapshot{
		Format:  subscription.SnapshotFormat,
		Version: subscription.SnapshotVersion,
		Node:    &subscription.SnapshotNode{ID: "node-weibo-smoke", Name: "weibo-smoke"},
		Rules:   []subscription.CacheRule{{ID: "r-wb", Name: "微博规则"}},
		Subscriptions: []subscription.SnapshotSubscription{{
			ID: "s-wb", Name: "微博冒烟订阅", RuleID: "r-wb",
			Accounts: []subscription.SnapshotAccount{{Site: "weibo", UID: creatorUID, Name: creatorNick}},
		}},
	}
	rawSnap, _ := json.Marshal(snap)
	if info, err := client.PutSubscriptions(ctx, rawSnap); err != nil {
		srvCancel()
		<-errCh
		return fmt.Errorf("sync weibo snapshot: %w", err)
	} else if info.Hash != snap.Hash() {
		srvCancel()
		<-errCh
		return fmt.Errorf("weibo snapshot hash mismatch: got %s want %s", info.Hash, snap.Hash())
	}

	// 4. Verify Status shows Weibo still logged in and leaks zero sentinels.
	st, err := client.Status(ctx)
	if err != nil {
		srvCancel()
		<-errCh
		return fmt.Errorf("status after weibo push: %w", err)
	}
	wbLoggedIn := false
	for _, s := range st.Sites {
		if s.Site == "weibo" && s.State == remote.SiteValid && s.Uname == selfNick {
			wbLoggedIn = true
		}
	}
	if !wbLoggedIn {
		srvCancel()
		<-errCh
		return fmt.Errorf("weibo lost login state after failed push: %+v", st.Sites)
	}
	wbCheckScheduled := false
	for _, c := range st.Checks {
		if c.Site == "weibo" && c.UID == creatorUID {
			wbCheckScheduled = true
		}
	}
	if !wbCheckScheduled {
		srvCancel()
		<-errCh
		return fmt.Errorf("weibo synced account missing from remote checks: %+v", st.Checks)
	}

	srvCancel()
	if runErr := <-errCh; runErr != nil {
		return fmt.Errorf("server shutdown: %w", runErr)
	}

	statusJSON, _ := json.Marshal(st)
	logMu.Lock()
	logs := logBuf.String()
	logMu.Unlock()

	if err := scanDirForSecrets(sandbox); err != nil {
		return fmt.Errorf("weibo remote sandbox secret scan: %w", err)
	}
	combined := string(statusJSON) + "\n" + logs
	if findings := sitecheck.ScanTextForSecrets(combined); len(findings) > 0 {
		return fmt.Errorf("weibo remote output secret scan: %s", findings[0])
	}
	for _, s := range []string{sentinelSub, sentinelSubp, sentinelXsrf, sentinelWbpsess, initRes.Conn.Token} {
		if strings.Contains(combined, s) || treeContains(sandbox, s) {
			return fmt.Errorf("weibo remote leaked sentinel %q", s)
		}
	}
	return nil
}

type lockedBuf struct {
	mu  *sync.Mutex
	buf *bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// privateFiles reports whether everything init wrote is 0600/0700.
func privateFiles(root string) bool {
	ok := true
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			ok = false
			return nil
		}
		if info.Mode().Perm()&0o077 != 0 {
			ok = false
		}
		return nil
	})
	return ok
}

func setUpload(dataDir, rclone string) error {
	path := filepath.Join(dataDir, "server.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return err
	}
	cfg["upload"] = map[string]any{"mode": "rclone", "remote": "fake:trace", "rclone": rclone, "deleteAfterUpload": true}
	out, _ := json.MarshalIndent(cfg, "", "  ")
	return storage.WriteFileAtomic(path, out, 0o600)
}

// seedServerItem puts a finished synthetic item into the export folder and
// the remote's library, as a previous run would have left it.
func seedServerItem(dataDir, exportDir string) error {
	b, err := service.NewBackend(service.Options{
		Locations: service.RemoteLocations(dataDir, exportDir), Desktop: service.NewMockDesktop(),
		Profile: service.ProfileRemote, DisableBgWorkers: true,
	})
	if err != nil {
		return err
	}
	defer b.Close()
	dir := filepath.Join(exportDir, "bilibili", "1001_UP", "videos", smokeServerItem)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data := []byte("synthetic smoke video")
	if err := os.WriteFile(filepath.Join(dir, "P01.mp4"), data, 0o600); err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	_, err = b.LibraryStore().SaveItem(dir, library.ItemMetadata{Version: 1, ID: "smoke", Site: "bilibili", Kind: "video", Title: "冒烟条目", Status: "complete",
		Author: library.ItemAuthor{Mid: 1001, Name: "UP"},
		Media:  []library.MediaFile{{Role: "video", Path: "P01.mp4", Bytes: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}}})
	return err
}

func hasTokenLeak(root, token string, outputs ...string) bool {
	if token == "" {
		return false
	}
	if treeContains(root, token) {
		return true
	}
	for _, out := range outputs {
		if strings.Contains(out, token) {
			return true
		}
	}
	return false
}

func treeContains(root, needle string) bool {
	found := false
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || found || info.Size() > 64<<20 {
			return nil
		}
		if raw, err := os.ReadFile(p); err == nil && bytes.Contains(raw, []byte(needle)) {
			found = true
		}
		return nil
	})
	return found
}

type serverProc struct {
	cmd  *exec.Cmd
	mu   sync.Mutex
	out  bytes.Buffer
	done chan struct{}
}

func (s *serverProc) output() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.out.String()
}

func (s *serverProc) kill() {
	select {
	case <-s.done:
		return
	default:
	}
	_ = s.cmd.Process.Kill()
	<-s.done
}

// startServer runs `trace-server run` and waits for its ready line.
func startServer(bin, dataDir string, env []string) (*serverProc, error) {
	cmd := exec.Command(bin, "run", "--data", dataDir)
	cmd.Env = env
	appdirs.SetSubprocessQuiet(cmd)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdout = nil
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	s := &serverProc{cmd: cmd, done: make(chan struct{})}
	ready := make(chan struct{}, 1)
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			line := sc.Text()
			s.mu.Lock()
			s.out.WriteString(line + "\n")
			s.mu.Unlock()
			if strings.Contains(line, "已启动") {
				select {
				case ready <- struct{}{}:
				default:
				}
			}
		}
		_ = cmd.Wait()
		close(s.done)
	}()
	select {
	case <-ready:
		return s, nil
	case <-s.done:
		return nil, fmt.Errorf("trace-server exited early: %s", s.output())
	case <-time.After(30 * time.Second):
		s.kill()
		return nil, fmt.Errorf("trace-server did not start: %s", s.output())
	}
}
