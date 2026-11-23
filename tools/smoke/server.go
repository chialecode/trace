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
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/remote"
	"github.com/chialecode/trace/internal/remoteserver"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site/weibo"
	"github.com/chialecode/trace/internal/site/x"
	"github.com/chialecode/trace/internal/storage"
	"github.com/chialecode/trace/internal/subscription"
)

// ServerSmokeReceipt is artifacts/smoke-server/smoke.json: trace-server run
// as a real process in a temporary folder — init, start, status with the
// connection code, a synthetic subscription snapshot, one synthetic item
// uploaded through a fake rclone, fake-Weibo and fake-X credential push,
// snapshot sync, check & auto-cache, then stop (docs/design/remote.md §7).
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
	XSiteReported         bool     `json:"xSiteReported"`
	SnapshotSynced        bool     `json:"snapshotSynced"`
	ItemUploaded          bool     `json:"itemUploaded"`
	SourceRemoved         bool     `json:"sourceRemoved"`
	WeiboFakeSiteVerified bool     `json:"weiboFakeSiteVerified"`
	XFakeSiteVerified     bool     `json:"xFakeSiteVerified"`
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
			switch s.Site {
			case "weibo":
				rec.WeiboSiteReported = true
			case "x":
				rec.XSiteReported = true
			}
		}
		if !rec.WeiboSiteReported {
			fail("status sites missing weibo row: %+v", st.Sites)
		}
		if !rec.XSiteReported {
			fail("status sites missing x row: %+v", st.Sites)
		}
	}

	snap := subscription.Snapshot{Format: subscription.SnapshotFormat, Version: subscription.SnapshotVersion, Node: &subscription.SnapshotNode{ID: "node-smoke-desktop", Name: "smoke-desktop"},
		Rules: []subscription.CacheRule{{ID: "r-smoke", Name: "冒烟规则"}},
		Subscriptions: []subscription.SnapshotSubscription{{
			ID: "s-smoke", Name: "冒烟订阅", RuleID: "r-smoke",
			Accounts: []subscription.SnapshotAccount{
				{Site: "weibo", UID: "5000000001", Name: "冒烟微博博主"},
				{Site: "x", UID: "1000000000000000099", UniqueID: "smoke_x_creator", Name: "冒烟X创作者"},
			},
		}}}
	raw, _ := json.Marshal(snap)
	if info, err := client.PutSubscriptions(ctx, raw); err != nil {
		fail("sync snapshot: %v", err)
	} else if info.Hash != snap.Hash() || info.Subscriptions != 1 || info.Accounts != 2 {
		fail("synced info = %+v, want hash %s with 2 accounts", info, snap.Hash())
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
	} else if err := scanTextForSecrets(srv.output()+"\n"+restartOutput, "server-output"); err != nil {
		fail("secret scan in server output: %v", err)
	} else {
		rec.NoSecretsInFiles = true
	}

	if err := runWeiboFakeRemoteSmoke(ctx); err != nil {
		fail("weibo fake remote smoke: %v", err)
	} else {
		rec.WeiboFakeSiteVerified = true
	}
	if err := runXFakeRemoteSmoke(ctx); err != nil {
		fail("x fake remote smoke: %v", err)
	} else {
		rec.XFakeSiteVerified = true
	}

	rec.Passed = len(rec.Failures) == 0 && rec.InitCodePrinted && rec.Started && rec.StatusRead && rec.WeiboSiteReported && rec.XSiteReported && rec.SnapshotSynced &&
		rec.ItemUploaded && rec.SourceRemoved && rec.WeiboFakeSiteVerified && rec.XFakeSiteVerified && rec.StoppedCleanly && rec.LockReleased && rec.NoTokenInFiles && rec.NoSecretsInFiles
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
	if err := scanTextForSecrets(combined, "weibo-remote-output"); err != nil {
		return fmt.Errorf("weibo remote output secret scan: %w", err)
	}
	for _, s := range []string{sentinelSub, sentinelSubp, sentinelXsrf, sentinelWbpsess, initRes.Conn.Token} {
		if strings.Contains(combined, s) || treeContains(sandbox, s) {
			return fmt.Errorf("weibo remote leaked sentinel %q", s)
		}
	}
	return nil
}

type xSmokeAbsRewriteTransport struct {
	target *url.URL
	inner  http.RoundTripper
}

func (rt *xSmokeAbsRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = rt.target.Scheme
	clone.URL.Host = rt.target.Host
	clone.Host = rt.target.Host
	return rt.inner.RoundTrip(clone)
}

const xSmokeHomeHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <meta name="twitter-site-verification" content="U1lOVEhFVElDX1hfVkVSSUZJQ0FUSU9OX0tFWV9GSVhUVVJFXzAwMDFfQUJDREVG" />
  <link rel="preload" as="script" href="https://abs.twimg.com/responsive-web/client-web/main.synth01a.js" />
</head>
<body>
  <div id="placeholder">
    <svg id="loading-x-anim-0" viewBox="0 0 100 100"><g><path d="M 0 0 L 1 1" /><path d="M 10 10 C 10 20 30 180 190 200 64 40 51 170 210 C 17 29 41 185 193 202 77 48 63 175 213 C 24 38 52 190 196 204 90 56 75 180 216 C 31 47 63 195 199 206 103 64 87 185 219 C 38 56 74 200 202 208 116 72 99 190 222 C 45 65 85 205 205 210 129 80 111 195 225 C 52 74 96 210 208 212 142 88 123 200 228 C 59 83 107 215 211 214 155 96 135 205 231 C 66 92 118 220 214 216 168 104 147 210 234 C 73 101 129 225 217 218 181 112 159 215 237 C 80 110 140 230 220 220 194 120 171 220 240 C 87 119 151 235 223 222 207 128 183 225 243 C 94 128 162 240 226 224 220 136 195 230 246 C 101 137 173 245 229 226 233 144 207 235 249 C 108 146 184 250 232 228 246 152 219 240 252 C 115 155 195 255 235 230 12 160 231 245 255" /></g></svg>
    <svg id="loading-x-anim-1" viewBox="0 0 100 100"><g><path d="M 0 0 L 2 2" /><path d="M 10 10 C 25 37 49 191 203 207 87 51 60 177 215 C 32 46 60 196 206 209 100 59 72 182 218 C 39 55 71 201 209 211 113 67 84 187 221 C 46 64 82 206 212 213 126 75 96 192 224 C 53 73 93 211 215 215 139 83 108 197 227 C 60 82 104 216 218 217 152 91 120 202 230 C 67 91 115 221 221 219 165 99 132 207 233 C 74 100 126 226 224 221 178 107 144 212 236 C 81 109 137 231 227 223 191 115 156 217 239 C 88 118 148 236 230 225 204 123 168 222 242 C 95 127 159 241 233 227 217 131 180 227 245 C 102 136 170 246 236 229 230 139 192 232 248 C 109 145 181 251 239 231 243 147 204 237 251 C 116 154 192 0 242 233 0 155 216 242 254 C 123 163 203 5 245 235 13 163 228 247 1 C 130 172 214 10 248 237 26 171 240 252 4" /></g></svg>
    <svg id="loading-x-anim-2" viewBox="0 0 100 100"><g><path d="M 0 0 L 3 3" /><path d="M 10 10 C 40 54 68 202 216 214 110 62 69 184 220 C 47 63 79 207 219 216 123 70 81 189 223 C 54 72 90 212 222 218 136 78 93 194 226 C 61 81 101 217 225 220 149 86 105 199 229 C 68 90 112 222 228 222 162 94 117 204 232 C 75 99 123 227 231 224 175 102 129 209 235 C 82 108 134 232 234 226 188 110 141 214 238 C 89 117 145 237 237 228 201 118 153 219 241 C 96 126 156 242 240 230 214 126 165 224 244 C 103 135 167 247 243 232 227 134 177 229 247 C 110 144 178 252 246 234 240 142 189 234 250 C 117 153 189 1 249 236 253 150 201 239 253 C 124 162 200 6 252 238 10 158 213 244 0 C 131 171 211 11 255 240 23 166 225 249 3 C 138 180 222 16 2 242 36 174 237 254 6 C 145 189 233 21 5 244 49 182 249 3 9" /></g></svg>
    <svg id="loading-x-anim-3" viewBox="0 0 100 100"><g><path d="M 0 0 L 4 4" /><path d="M 10 10 C 55 71 87 213 229 221 133 73 78 191 225 C 62 80 98 218 232 223 146 81 90 196 228 C 69 89 109 223 235 225 159 89 102 201 231 C 76 98 120 228 238 227 172 97 114 206 234 C 83 107 131 233 241 229 185 105 126 211 237 C 90 116 142 238 244 231 198 113 138 216 240 C 97 125 153 243 247 233 211 121 150 221 243 C 104 134 164 248 250 235 224 129 162 226 246 C 111 143 175 253 253 237 237 137 174 231 249 C 118 152 186 2 0 239 250 145 186 236 252 C 125 161 197 7 3 241 7 153 198 241 255 C 132 170 208 12 6 243 20 161 210 246 2 C 139 179 219 17 9 245 33 169 222 251 5 C 146 188 230 22 12 247 46 177 234 0 8 C 153 197 241 27 15 249 59 185 246 5 11 C 160 206 252 32 18 251 72 193 2 10 14" /></g></svg>
  </div>
  <script>
    window.__SYNTH_WEBPACK_CHUNKS__ = {
      "vendor": "synth99",
      "ondemand.s": "synth02"
    };
  </script>
  <script crossorigin="anonymous" src="https://abs.twimg.com/responsive-web/client-web/main.synth01a.js"></script>
</body>
</html>`

const xSmokeOndemandJS = `(self.webpackChunk_synthetic_web=self.webpackChunk_synthetic_web||[]).push([[500],{
  5001:function(e,t,n){
    function s(k){
      const r=parseInt(k[7],16);
      const a=parseInt(k[12], 16);
      const b=parseInt(k[19],16);
      const c=parseInt(k[25],16);
      return r+a*b*c;
    }
    e.exports=s;
  }
}]);`

func xSmokeMainJS(bearer string) string {
	return `(self.webpackChunk_synthetic_web=self.webpackChunk_synthetic_web||[]).push([[100],{
  1001:function(e){e.exports={authorization:"Bearer ` + bearer + `"}},
  2001:function(e){e.exports={queryId:"SynthQidUserByScreenName01",operationName:"UserByScreenName",operationType:"query",metadata:{featureSwitches:["hidden_profile_subscriptions_enabled","responsive_web_graphql_exclude_directive_enabled"],fieldToggles:["withAuxiliaryUserLabels"]}}},
  2002:function(e){e.exports={queryId:"SynthQidUserByRestId02",operationName:"UserByRestId",operationType:"query",metadata:{featureSwitches:["hidden_profile_subscriptions_enabled","responsive_web_graphql_exclude_directive_enabled"],fieldToggles:["withAuxiliaryUserLabels"]}}},
  2003:function(e){e.exports={queryId:"SynthQidUserTweets03",operationName:"UserTweets",operationType:"query",metadata:{featureSwitches:["creator_subscriptions_tweet_preview_api_enabled","responsive_web_graphql_exclude_directive_enabled","longform_notetweets_consumption_enabled"],fieldToggles:["withArticlePlainText"]}}},
  2004:function(e){e.exports={queryId:"SynthQidTweetDetail04",operationName:"TweetDetail",operationType:"query",metadata:{featureSwitches:["creator_subscriptions_tweet_preview_api_enabled","responsive_web_graphql_exclude_directive_enabled","longform_notetweets_consumption_enabled"],fieldToggles:["withArticleRichContentState","withArticlePlainText"]}}},
  2005:function(e){e.exports={queryId:"SynthQidFollowing05",operationName:"Following",operationType:"query",metadata:{featureSwitches:["responsive_web_graphql_exclude_directive_enabled","verified_phone_label_enabled"]}}},
  2006:function(e){e.exports={queryId:"SynthQidBookmarks06",operationName:"Bookmarks",operationType:"query",metadata:{featureSwitches:["responsive_web_graphql_exclude_directive_enabled","longform_notetweets_consumption_enabled"]}}},
  2007:function(e){e.exports={queryId:"SynthQidLikes07",operationName:"Likes",operationType:"query",metadata:{featureSwitches:["responsive_web_graphql_exclude_directive_enabled","verified_phone_label_enabled"]}}},
  9001:function(e){e.exports={defaultConfig:{
    hidden_profile_subscriptions_enabled:{value:!0},
    responsive_web_graphql_exclude_directive_enabled:{value:!0},
    creator_subscriptions_tweet_preview_api_enabled:{value:!0},
    longform_notetweets_consumption_enabled:{value:!0},
    verified_phone_label_enabled:{value:!1},
    withAuxiliaryUserLabels:{value:!1},
    withArticleRichContentState:{value:!0},
    withArticlePlainText:{value:!1}
  }}}
}]);`
}

// runXFakeRemoteSmoke exercises remoteserver against a local httptest fake X server
// (0 real network requests): verifies manual PUT /credentials/x 2-step validation,
// rejection preserving existing credentials, X subscription sync, scheduled X check
// and auto-cache (never full-cache), and zero X Cookie/CSRF/Bearer/token sentinel leaks.
func runXFakeRemoteSmoke(parentCtx context.Context) error {
	const (
		sentinelAuth   = "0123456789abcdef0123456789abcdef01234567"
		sentinelCT0    = "abcdef0123456789abcdef0123456789"
		sentinelKDT    = "SynthSmokeXKdt0123456789"
		sentinelBearer = "SYNTH_SMOKE_X_BEARER_TOKEN_0001"
		selfRestID     = "1000000000000000201"
		selfHandle     = "smoke_x_self"
		selfName       = "合成冒烟X账号"
		creatorRestID  = "1000000000000000202"
		creatorHandle  = "smoke_x_user"
		creatorName    = "合成冒烟X创作者"
		baseTweetID    = "1791257321273791001"
		newTweetID     = "1791257321273791002"
	)
	validCookie := "auth_token=" + sentinelAuth + "; ct0=" + sentinelCT0 + "; twid=u%3D" + selfRestID + "; guest_id=v1%3A90000201; kdt=" + sentinelKDT + "; __cf_bm=DROP_ME_SMOKE_CF"

	pngBytes := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
		0xde, 0x00, 0x00, 0x00, 0x0c, 0x49, 0x44, 0x41,
		0x54, 0x08, 0xd7, 0x63, 0xf8, 0xff, 0xff, 0x3f,
		0x00, 0x05, 0xfe, 0x02, 0xfe, 0xdc, 0xcc, 0x59,
		0xe7, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e,
		0x44, 0xae, 0x42, 0x60, 0x82,
	}

	var settingsReqs atomic.Int64
	var userByScreenNameReqs atomic.Int64
	var publishNew atomic.Bool
	var hdrMu sync.Mutex
	var hdrViolations []string
	recordViolation := func(msg string) {
		hdrMu.Lock()
		hdrViolations = append(hdrViolations, msg)
		hdrMu.Unlock()
	}

	userJSON := func(restID, handle, name, avatar string, statuses int) string {
		return fmt.Sprintf(`{"__typename":"User","rest_id":%q,"is_blue_verified":false,"core":{"name":%q,"screen_name":%q},"avatar":{"image_url":%q},"profile_bio":{"description":"冒烟X简介"},"legacy":{"statuses_count":%d,"followers_count":50,"friends_count":5,"protected":false}}`,
			restID, name, handle, avatar, statuses)
	}
	tweetJSON := func(id, avatar, text, photoURL string) string {
		author := userJSON(creatorRestID, creatorHandle, creatorName, avatar, 12)
		return fmt.Sprintf(`{"__typename":"Tweet","rest_id":%q,"core":{"user_results":{"result":%s}},"legacy":{"id_str":%q,"user_id_str":%q,"created_at":"Sat Nov 21 12:00:00 +0000 2026","full_text":%q,"extended_entities":{"media":[{"media_key":"3_%s","type":"photo","media_url_https":%q,"original_info":{"width":1,"height":1}}]}}}`,
			id, author, id, creatorRestID, text, id, photoURL)
	}
	tweetEntry := func(id, tw string) string {
		return fmt.Sprintf(`{"entryId":"tweet-%s","content":{"entryType":"TimelineTimelineItem","itemContent":{"itemType":"TimelineTweet","tweetDisplayType":"Tweet","tweet_results":{"result":%s}}}}`, id, tw)
	}

	mainJS := xSmokeMainJS(sentinelBearer)
	var fakeX *httptest.Server
	fakeX = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/home":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(xSmokeHomeHTML))
			return
		case "/responsive-web/client-web/main.synth01a.js":
			if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("X-Csrf-Token") != "" {
				recordViolation("static main.js leaked auth headers")
			}
			w.Header().Set("Content-Type", "application/javascript")
			_, _ = w.Write([]byte(mainJS))
			return
		case "/responsive-web/client-web/ondemand.s.synth02a.js":
			if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("X-Csrf-Token") != "" {
				recordViolation("static ondemand.js leaked auth headers")
			}
			w.Header().Set("Content-Type", "application/javascript")
			_, _ = w.Write([]byte(xSmokeOndemandJS))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/xcdn/") {
			if r.Header.Get("Cookie") != "" || r.Header.Get("Referer") != "" || r.Header.Get("Authorization") != "" {
				recordViolation("xcdn request leaked headers on " + r.URL.Path)
			}
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngBytes)
			return
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		ck := r.Header.Get("Cookie")
		if strings.Contains(ck, "__cf_bm") {
			recordViolation("non-whitelisted __cf_bm cookie sent to " + r.URL.Path)
		}
		if !strings.Contains(ck, "auth_token="+sentinelAuth) || r.Header.Get("X-Csrf-Token") != sentinelCT0 || r.Header.Get("Authorization") != "Bearer "+sentinelBearer {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = fmt.Fprint(w, `{"errors":[{"code":32,"message":"Could not authenticate you."}]}`)
			return
		}

		cdn := fakeX.URL + "/xcdn"
		switch {
		case r.URL.Path == "/i/api/1.1/account/settings.json":
			settingsReqs.Add(1)
			_, _ = fmt.Fprintf(w, `{"screen_name":%q,"protected":false,"language":"en"}`, selfHandle)
		case strings.HasSuffix(r.URL.Path, "/"+x.OpUserByScreenName):
			userByScreenNameReqs.Add(1)
			if r.Header.Get("X-Client-Transaction-Id") == "" {
				recordViolation("UserByScreenName missing x-client-transaction-id")
			}
			_, _ = fmt.Fprintf(w, `{"data":{"user":{"result":%s}}}`, userJSON(selfRestID, selfHandle, selfName, cdn+"/self.png", 4))
		case strings.HasSuffix(r.URL.Path, "/"+x.OpUserByRestId):
			_, _ = fmt.Fprintf(w, `{"data":{"user":{"result":%s}}}`, userJSON(creatorRestID, creatorHandle, creatorName, cdn+"/creator.png", 12))
		case strings.HasSuffix(r.URL.Path, "/"+x.OpUserTweets):
			base := tweetJSON(baseTweetID, cdn+"/creator.png", "冒烟X基线帖子", cdn+"/base.png")
			entries := tweetEntry(baseTweetID, base)
			if publishNew.Load() {
				newest := tweetJSON(newTweetID, cdn+"/creator.png", "冒烟X自动缓存新帖子", cdn+"/new.png")
				entries = tweetEntry(newTweetID, newest) + "," + entries
			}
			_, _ = fmt.Fprintf(w, `{"data":{"user":{"result":{"__typename":"User","timeline_v2":{"timeline":{"instructions":[{"type":"TimelineAddEntries","entries":[%s]}]}}}}}}`, entries)
		case strings.HasSuffix(r.URL.Path, "/"+x.OpTweetDetail):
			newest := tweetJSON(newTweetID, cdn+"/creator.png", "冒烟X自动缓存新帖子", cdn+"/new.png")
			_, _ = fmt.Fprintf(w, `{"data":{"threaded_conversation_with_injections_v2":{"instructions":[{"type":"TimelineAddEntries","entries":[%s]}]}}}`, tweetEntry(newTweetID, newest))
		default:
			http.NotFound(w, r)
		}
	}))
	defer fakeX.Close()

	fakeXURL, err := url.Parse(fakeX.URL)
	if err != nil {
		return err
	}
	fixedNow := time.Unix(1795327200, 0).UTC()
	wireXOpts := func(bo *service.Options) {
		bo.XOpts = x.ClientOptions{
			Endpoints: x.Endpoints{
				HomeURL:         fakeX.URL + "/home",
				AccountSettings: fakeX.URL + "/i/api/1.1/account/settings.json",
				GraphQLBase:     fakeX.URL + "/i/api/graphql",
			},
			MinInterval:        -1,
			RateLimitJitterMax: -1,
			AllowLocalhost:     true,
			AssetTransport:     &xSmokeAbsRewriteTransport{target: fakeXURL, inner: fakeX.Client().Transport},
			Now:                func() time.Time { return fixedNow },
			Sleep:              func(context.Context, time.Duration) error { return nil },
			TxIDOptions: []x.TxIDOption{
				x.WithTxIDClock(func() time.Time { return fixedNow }),
				x.WithTxIDRandByte(func() (byte, error) { return 0x42, nil }),
			},
		}
	}

	sandbox, err := os.MkdirTemp("", "trace-smoke-x-remote-")
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
		Name:          "x-smoke",
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
				wireXOpts(bo)
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

	ctx, cancel := context.WithTimeout(parentCtx, 25*time.Second)
	defer cancel()

	// 1. Manual push of valid X cookie -> strict 2-step validation must pass.
	siteSt, err := client.PushCredential(ctx, "x", remote.CredentialPush{Cookie: validCookie})
	if err != nil {
		srvCancel()
		<-errCh
		return fmt.Errorf("push valid x credential: %w", err)
	}
	if siteSt.Site != "x" || siteSt.State != remote.SiteValid || siteSt.Uname != selfName {
		srvCancel()
		<-errCh
		return fmt.Errorf("unexpected x site status after valid push: %+v", siteSt)
	}
	if settingsReqs.Load() < 1 || userByScreenNameReqs.Load() < 1 {
		srvCancel()
		<-errCh
		return fmt.Errorf("x push did not run 2-step verify (settings=%d userByScreenName=%d)", settingsReqs.Load(), userByScreenNameReqs.Load())
	}

	// 2. Push invalid X cookie -> must fail and preserve existing valid X login.
	if _, err := client.PushCredential(ctx, "x", remote.CredentialPush{Cookie: "auth_token=9999888877776666555544443333222211110000; ct0=11112222333344445555666677778888"}); err == nil {
		srvCancel()
		<-errCh
		return fmt.Errorf("invalid x credential push unexpectedly succeeded")
	}

	// 3. Sync X subscription snapshot with ScheduledCheck + AutoCache + FullCache=true.
	snap := subscription.Snapshot{
		Format:  subscription.SnapshotFormat,
		Version: subscription.SnapshotVersion,
		Node:    &subscription.SnapshotNode{ID: "node-x-smoke", Name: "x-smoke"},
		Rules: []subscription.CacheRule{{
			ID:             "r-x",
			Name:           "X规则",
			ScheduledCheck: true,
			AutoCache:      true,
			Types:          []string{"draw", "video", "word"},
			FullCache:      true,
		}},
		Subscriptions: []subscription.SnapshotSubscription{{
			ID:             "s-x",
			Name:           "X冒烟订阅",
			RuleID:         "r-x",
			AutoCacheSince: "2026-11-18T00:00:00Z",
			Accounts: []subscription.SnapshotAccount{{
				Site:     "x",
				UID:      creatorRestID,
				UniqueID: creatorHandle,
				Name:     creatorName,
			}},
		}},
	}
	rawSnap, _ := json.Marshal(snap)
	if info, err := client.PutSubscriptions(ctx, rawSnap); err != nil {
		srvCancel()
		<-errCh
		return fmt.Errorf("sync x snapshot: %w", err)
	} else if info.Hash != snap.Hash() {
		srvCancel()
		<-errCh
		return fmt.Errorf("x snapshot hash mismatch: got %s want %s", info.Hash, snap.Hash())
	}

	// 4. Verify Status shows X still logged in, synced X account scheduled, and full-cache disabled note.
	st1, err := client.Status(ctx)
	if err != nil {
		srvCancel()
		<-errCh
		return fmt.Errorf("status after x push: %w", err)
	}
	xLoggedIn := false
	for _, s := range st1.Sites {
		if s.Site == "x" && s.State == remote.SiteValid && s.Uname == selfName {
			xLoggedIn = true
		}
	}
	if !xLoggedIn {
		srvCancel()
		<-errCh
		return fmt.Errorf("x lost login state after failed push: %+v", st1.Sites)
	}
	xCheckScheduled := false
	for _, c := range st1.Checks {
		if c.Site == "x" && c.UID == creatorRestID {
			xCheckScheduled = true
		}
	}
	if !xCheckScheduled {
		srvCancel()
		<-errCh
		return fmt.Errorf("x synced account missing from remote checks: %+v", st1.Checks)
	}

	srvCancel()
	if runErr := <-errCh; runErr != nil {
		return fmt.Errorf("server shutdown: %w", runErr)
	}

	// 5. Reopen remote backend to verify persisted X credential + subscription, scheduled check,
	// and rule-based auto-cache into ExportDir (with zero full-cache queues).
	bo := service.Options{
		Locations:        service.RemoteLocations(dataDir, exportDir),
		Desktop:          service.NewMockDesktop(),
		Profile:          service.ProfileRemote,
		NodeName:         "x-smoke",
		DisableBgWorkers: false,
		LogMirror:        logMirror,
	}
	wireXOpts(&bo)
	remB, err := service.NewBackend(bo)
	if err != nil {
		return fmt.Errorf("reopen remote backend: %w", err)
	}
	sum1, err := remB.SubscriptionManager().RunScheduledRound(ctx)
	if err != nil {
		_ = remB.Close()
		return fmt.Errorf("x smoke baseline round: %w", err)
	}
	if sum1.NewVideos+sum1.NewDynamics != 0 {
		_ = remB.Close()
		return fmt.Errorf("x smoke baseline round reported %+v, want 0 new items", sum1)
	}

	publishNew.Store(true)
	sum2, err := remB.SubscriptionManager().RunScheduledRound(ctx)
	if err != nil {
		_ = remB.Close()
		return fmt.Errorf("x smoke update round: %w", err)
	}
	if sum2.NewVideos+sum2.NewDynamics != 1 {
		_ = remB.Close()
		return fmt.Errorf("x smoke update round reported %+v, want 1 new item", sum2)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, ok, _ := remB.LibraryStore().GetItem("x:dynamic:" + newTweetID); ok {
			break
		}
		tasks, _ := remB.ListDownloadTasks()
		for _, tk := range tasks {
			if tk.Status == "failed" {
				_ = remB.Close()
				return fmt.Errorf("x smoke auto-cache task failed: %+v", tk)
			}
		}
		if time.Now().After(deadline) {
			_ = remB.Close()
			return fmt.Errorf("x smoke auto-cached post did not complete in time; tasks=%+v", tasks)
		}
		time.Sleep(25 * time.Millisecond)
	}
	cachedItem, ok, err := remB.LibraryStore().GetItem("x:dynamic:" + newTweetID)
	if err != nil || !ok || !strings.HasPrefix(filepath.Clean(cachedItem.ItemDir), filepath.Clean(exportDir)) {
		_ = remB.Close()
		return fmt.Errorf("x smoke cached item = %+v ok=%v err=%v (want inside %s)", cachedItem, ok, err, exportDir)
	}
	if len(remB.ListCacheQueue()) != 0 {
		_ = remB.Close()
		return fmt.Errorf("x smoke remote unexpectedly created full-cache queue: %+v", remB.ListCacheQueue())
	}
	st2 := remB.ServerStatus()
	if err := remB.Close(); err != nil {
		return fmt.Errorf("close reopened remote backend: %w", err)
	}

	hdrMu.Lock()
	violations := append([]string(nil), hdrViolations...)
	hdrMu.Unlock()
	if len(violations) > 0 {
		return fmt.Errorf("x fake remote header violations: %v", violations)
	}

	status1JSON, _ := json.Marshal(st1)
	status2JSON, _ := json.Marshal(st2)
	logMu.Lock()
	logs := logBuf.String()
	logMu.Unlock()

	if err := scanDirForSecrets(sandbox); err != nil {
		return fmt.Errorf("x remote sandbox secret scan: %w", err)
	}
	combined := string(status1JSON) + "\n" + string(status2JSON) + "\n" + logs
	if err := scanTextForSecrets(combined, "x-remote-output"); err != nil {
		return fmt.Errorf("x remote output secret scan: %w", err)
	}
	for _, s := range []string{sentinelAuth, sentinelCT0, sentinelKDT, sentinelBearer, initRes.Conn.Token} {
		if strings.Contains(combined, s) || treeContains(sandbox, s) {
			return fmt.Errorf("x remote leaked sentinel %q", s[:min(8, len(s))])
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
