//go:build windows

package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/update"
	"github.com/chialecode/trace/tools/internal/build"
)

const (
	ptestIdentifier = "io.github.chialecode.trace-ptest"
	exitTimeout     = 30 * time.Second
	readyTimeout    = 3 * time.Minute
)

// rehearsal installs, updates, rejects a tampered update and uninstalls
// TRACE under the ptest identity only (W5). The release and development
// identities are only read, before and after, to show they did not change.
type rehearsal struct {
	in      rehearsalInput
	res     *rehearsalResult
	work    string
	dataDir string
	env     []string
	exe     string
	dir     string
	server  *localRelease
}

func runRehearsal(in rehearsalInput) (rehearsalResult, error) {
	start := time.Now()
	res := newRehearsalResult(in)
	r := &rehearsal{in: in, res: &res}
	err := r.run()
	r.cleanup()
	res.ElapsedMs = time.Since(start).Milliseconds()
	if r.server != nil {
		res.Requests = r.server.log()
	}
	if err != nil {
		res.Status, res.Reason = "failed", err.Error()
		return res, err
	}
	res.Status = "passed"
	return res, nil
}

func (r *rehearsal) logf(format string, args ...any) {
	if r.in.Logf != nil {
		r.in.Logf("    [rehearsal] "+format, args...)
	}
}

// step runs one step and records it.
func (r *rehearsal) step(name string, fn func() (string, error)) error {
	t := time.Now()
	detail, err := fn()
	s := rehearsalStep{Name: name, OK: err == nil, Detail: detail, Ms: time.Since(t).Milliseconds()}
	if err != nil {
		s.Detail = strings.TrimSpace(detail + " " + err.Error())
	}
	r.res.Steps = append(r.res.Steps, s)
	r.logf("%s: %v %s", name, map[bool]string{true: "ok", false: "FAILED"}[err == nil], s.Detail)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

type identitySnapshot map[string]build.InstallEntry

func snapshotOthers() identitySnapshot {
	snap := identitySnapshot{}
	for _, ch := range []string{"stable", "dev"} {
		id := appdirs.IdentifierForChannel(ch)
		if e, ok, err := build.LookupInstall(id); err == nil && ok {
			snap[id] = e
		} else {
			snap[id] = build.InstallEntry{}
		}
	}
	return snap
}

func (r *rehearsal) run() error {
	others := snapshotOthers()
	defer func() { r.res.OthersSame = fmt.Sprint(snapshotOthers()) == fmt.Sprint(others) }()

	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData == "" {
		return errors.New("LOCALAPPDATA is not set")
	}
	r.dataDir = filepath.Join(localAppData, appdirs.AppNameForChannel("ptest"))
	var err error
	if err := os.MkdirAll(filepath.Join(r.in.Root, "dist"), 0o755); err != nil {
		return err
	}
	if r.work, err = os.MkdirTemp(filepath.Join(r.in.Root, "dist"), rehearsalPrefix); err != nil {
		return err
	}

	if err := r.step("清理以前的 ptest 安装", r.clearEarlier); err != nil {
		return err
	}

	// A key that exists only in this process, for this rehearsal.
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	pubText := base64.StdEncoding.EncodeToString(pub)
	changelog := filepath.Join(r.work, "CHANGELOG.md")
	notes := fmt.Sprintf("# 演练\n\n## %s\n\n- 升级演练的第二个版本\n\n## %s\n\n- 升级演练的第一个版本\n", r.res.NewVersion, r.res.OldVersion)
	if err := os.WriteFile(changelog, []byte(notes), 0o644); err != nil {
		return err
	}
	zero := 0
	up := &build.Updates{PublicKey: pubText, URL: "https://rehearsal.invalid/trace-ptest", Changelog: changelog, Deltas: &zero}
	built := map[string]string{}
	for _, v := range []string{r.res.OldVersion, r.res.NewVersion} {
		v := v
		if err := r.step("构建 ptest "+v, func() (string, error) {
			info := build.Info{Version: v, Channel: build.ChannelPtest, Commit: r.in.Commit, BuildTime: time.Now().UTC().Format(time.RFC3339)}
			dir, _, err := buildChannel(r.in.Root, r.in.Build, r.in.Base, info, up, key, filepath.Join(r.work, v))
			built[v] = dir
			return dir, err
		}); err != nil {
			return err
		}
	}
	oldDir, newDir := built[r.res.OldVersion], built[r.res.NewVersion]

	r.server, err = startLocalRelease()
	if err != nil {
		return err
	}
	if err := r.server.serveDir(newDir); err != nil {
		return err
	}
	r.env = build.AppEnv(
		"TRACE_PTEST_UPDATE_URL="+r.server.base,
		"TRACE_QUIET=1",
		"TRACE_CACHE_DIR="+filepath.Join(r.work, "cache"),
	)

	if err := r.step("静默安装旧版", func() (string, error) {
		if err := build.RunQuiet(filepath.Join(oldDir, build.SetupName(build.ChannelPtest, r.res.OldVersion)), []string{"/S"}, build.AppEnv()); err != nil {
			return "", err
		}
		e, ok, err := build.LookupInstall(ptestIdentifier)
		if err != nil || !ok {
			return "", fmt.Errorf("no uninstall entry after the install: %v", err)
		}
		if e.Version != r.res.OldVersion {
			return "", fmt.Errorf("DisplayVersion %q", e.Version)
		}
		r.dir, r.exe = e.Location, filepath.Join(e.Location, "trace.exe")
		r.res.InstallDir = e.Location
		return "DisplayVersion " + e.Version + " · " + e.Location, nil
	}); err != nil {
		return err
	}

	if err := r.step("启动旧版，从本机假发布源下载并校验新版", func() (string, error) {
		if err := build.LaunchDetached(r.exe, []string{"--background"}, r.env); err != nil {
			return "", err
		}
		if err := r.waitState(time.Minute, "旧版运行", func(st uiUpdate) bool { return st.LastRunVersion == r.res.OldVersion }); err != nil {
			return "", err
		}
		if err := r.waitState(readyTimeout, "新版就绪", func(st uiUpdate) bool { return st.ReadyVersion == r.res.NewVersion }); err != nil {
			return "", err
		}
		return "ui-state: readyVersion " + r.res.NewVersion, nil
	}); err != nil {
		return err
	}

	// The next launch meets a tampered update: a newer version whose
	// archive was changed after it was signed.
	if err := r.serveTampered(newDir, key); err != nil {
		return err
	}

	if err := r.step("请旧版为更新退出（--quit-for-update）", r.quit); err != nil {
		return err
	}

	if err := r.step("重启后运行新版", func() (string, error) {
		if err := build.LaunchDetached(r.exe, []string{"--background"}, r.env); err != nil {
			return "", err
		}
		if err := r.waitState(time.Minute, "新版运行", func(st uiUpdate) bool { return st.LastRunVersion == r.res.NewVersion }); err != nil {
			return "", err
		}
		deadline := time.Now().Add(20 * time.Second)
		for {
			e, ok, err := build.LookupInstall(ptestIdentifier)
			if err == nil && ok && e.Version == r.res.NewVersion {
				break
			}
			if time.Now().After(deadline) {
				return "", fmt.Errorf("the uninstall entry still names %q", e.Version)
			}
			time.Sleep(250 * time.Millisecond)
		}
		for _, name := range []string{"library.db", "settings.json", "ui-state.json"} {
			if _, err := os.Stat(filepath.Join(r.dataDir, name)); err != nil {
				return "", fmt.Errorf("data file %s missing after the update", name)
			}
		}
		return "ui-state lastRunVersion 与卸载项 DisplayVersion 都是 " + r.res.NewVersion + "，数据仍在", nil
	}); err != nil {
		return err
	}

	if err := r.step("拒绝被篡改的更新包", func() (string, error) {
		want, err := build.SHA256File(filepath.Join(newDir, "trace.exe"))
		if err != nil {
			return "", err
		}
		logPath := filepath.Join(r.dataDir, "logs", "trace.log")
		marker := "update " + r.res.Tampered + " not installed"
		deadline := time.Now().Add(2 * time.Minute)
		for {
			raw, _ := os.ReadFile(logPath)
			if bytes.Contains(raw, []byte(marker)) {
				break
			}
			if time.Now().After(deadline) {
				return "", fmt.Errorf("no rejection of %s in the log", r.res.Tampered)
			}
			time.Sleep(500 * time.Millisecond)
		}
		if !r.server.served(path.Base(r.server.tamperedArchive)) {
			return "", errors.New("the tampered archive was never downloaded")
		}
		st, _ := r.readState()
		if st.ReadyVersion != "" {
			return "", fmt.Errorf("the tampered %s is ready", st.ReadyVersion)
		}
		got, err := build.SHA256File(r.exe)
		if err != nil {
			return "", err
		}
		if got != want {
			return "", errors.New("trace.exe changed after the tampered update")
		}
		return "日志记下拒绝，程序文件仍是 " + r.res.NewVersion, nil
	}); err != nil {
		return err
	}

	if err := r.step("退出新版", r.quit); err != nil {
		return err
	}

	return r.step("卸载并保留数据", func() (string, error) {
		if err := r.uninstall(); err != nil {
			return "", err
		}
		r.res.InstallGone = true
		if _, err := os.Stat(filepath.Join(r.dataDir, "library.db")); err != nil {
			return "", errors.New("the uninstall removed the data")
		}
		r.res.DataKept = true
		return "卸载项与安装目录已删除，" + r.dataDir + " 保留", nil
	})
}

// clearEarlier stops and uninstalls a ptest installation left from an
// earlier rehearsal and puts its data aside, so this one starts fresh.
func (r *rehearsal) clearEarlier() (string, error) {
	var notes []string
	if e, ok, err := build.LookupInstall(ptestIdentifier); err != nil {
		return "", err
	} else if ok {
		r.dir, r.exe = e.Location, filepath.Join(e.Location, "trace.exe")
		if running, _ := build.RunningIn(r.dir); running {
			if _, err := r.quit(); err != nil {
				return "", err
			}
		}
		if err := r.uninstall(); err != nil {
			return "", err
		}
		notes = append(notes, "卸载了以前的 ptest "+e.Version)
	}
	if _, err := os.Stat(r.dataDir); err == nil {
		aside := r.dataDir + ".previous"
		if err := os.RemoveAll(aside); err != nil {
			return "", err
		}
		if err := os.Rename(r.dataDir, aside); err != nil {
			return "", fmt.Errorf("put the earlier ptest data aside: %w", err)
		}
		notes = append(notes, "以前的 ptest 数据移到 "+filepath.Base(aside))
	}
	if len(notes) == 0 {
		return "没有以前的 ptest 安装", nil
	}
	return strings.Join(notes, "；"), nil
}

// quit asks the running ptest TRACE to quit for the update and waits.
func (r *rehearsal) quit() (string, error) {
	start := time.Now()
	if err := build.RunQuiet(r.exe, []string{"--quit-for-update"}, r.env); err != nil {
		return "", err
	}
	for {
		running, err := build.RunningIn(r.dir)
		if err != nil {
			return "", err
		}
		if !running {
			return fmt.Sprintf("%d ms 内退出", time.Since(start).Milliseconds()), nil
		}
		if time.Since(start) > exitTimeout {
			return "", fmt.Errorf("TRACE PTest did not quit within %s (not killed)", exitTimeout)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// uninstall runs the ptest uninstaller silently and waits until the entry
// and the install directory are gone.
func (r *rehearsal) uninstall() error {
	e, ok, err := build.LookupInstall(ptestIdentifier)
	if err != nil || !ok {
		return err
	}
	exe, args := splitCommand(e.QuietUninstall)
	if exe == "" {
		exe, args = splitCommand(e.UninstallString)
		args = append(args, "/S")
	}
	if exe == "" || !strings.EqualFold(filepath.Dir(exe), filepath.Clean(e.Location)) {
		return fmt.Errorf("unexpected uninstaller %q", exe)
	}
	if err := build.RunQuiet(exe, args, build.AppEnv()); err != nil {
		return err
	}
	deadline := time.Now().Add(time.Minute)
	for {
		_, still, _ := build.LookupInstall(ptestIdentifier)
		_, statErr := os.Stat(filepath.Join(e.Location, "trace.exe"))
		if !still && errors.Is(statErr, os.ErrNotExist) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the uninstall did not finish (entry present: %v)", still)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// splitCommand splits `"C:\path\uninstall.exe" /S` into the program and
// its arguments.
func splitCommand(s string) (string, []string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if strings.HasPrefix(s, `"`) {
		if end := strings.Index(s[1:], `"`); end >= 0 {
			return s[1 : end+1], strings.Fields(s[end+2:])
		}
	}
	f := strings.Fields(s)
	return f[0], f[1:]
}

type uiUpdate struct {
	LastRunVersion string `json:"lastRunVersion"`
	ReadyVersion   string `json:"readyVersion"`
}

func (r *rehearsal) readState() (uiUpdate, error) {
	raw, err := os.ReadFile(filepath.Join(r.dataDir, "ui-state.json"))
	if err != nil {
		return uiUpdate{}, err
	}
	var st struct {
		Update uiUpdate `json:"update"`
	}
	err = json.Unmarshal(raw, &st)
	return st.Update, err
}

func (r *rehearsal) waitState(timeout time.Duration, what string, ok func(uiUpdate) bool) error {
	deadline := time.Now().Add(timeout)
	for {
		st, err := r.readState()
		if err == nil && ok(st) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s: not reached within %s (ui-state %+v)", what, timeout, st)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// serveTampered makes the local release offer a newer version whose
// archive is the new one with one byte changed: its attestation is valid,
// its signature no longer matches the content.
func (r *rehearsal) serveTampered(newDir string, key ed25519.PrivateKey) error {
	raw, err := os.ReadFile(filepath.Join(newDir, manifestName))
	if err != nil {
		return err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	archiveName := "trace-ptest-" + r.res.NewVersion + "-windows-amd64.tar.gz"
	archive, err := os.ReadFile(filepath.Join(newDir, archiveName))
	if err != nil {
		return err
	}
	archive[len(archive)/2] ^= 0xff
	tamperedName := "trace-ptest-" + r.res.Tampered + "-windows-amd64.tar.gz"
	sig, _ := m["signature"].(string)
	m["version"] = r.res.Tampered
	m["notes"] = "- 被篡改的更新包"
	m["url"] = "https://rehearsal.invalid/trace-ptest/" + tamperedName
	m[update.AttestationField] = update.SignAttestation(key, r.res.Tampered, sig)
	manifest, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	r.server.set(map[string][]byte{manifestName: manifest, tamperedName: archive})
	r.server.tamperedArchive = tamperedName
	return nil
}

func (r *rehearsal) cleanup() {
	if r.dir != "" {
		if running, _ := build.RunningIn(r.dir); running && r.exe != "" {
			_, _ = r.quit()
		}
		if _, ok, _ := build.LookupInstall(ptestIdentifier); ok {
			if err := r.uninstall(); err != nil {
				r.logf("cleanup: %v", err)
			}
		}
	}
	if r.server != nil {
		r.server.close()
	}
	if r.work != "" {
		_ = os.RemoveAll(r.work)
	}
}

// localRelease serves the files of a release on the loopback interface,
// as GitHub would: the ptest identity alone may be pointed at it.
type localRelease struct {
	base            string
	srv             *http.Server
	mu              sync.Mutex
	files           map[string][]byte
	requests        []string
	tamperedArchive string
}

func startLocalRelease() (*localRelease, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	l := &localRelease{base: "http://" + ln.Addr().String() + "/trace-ptest", files: map[string][]byte{}}
	l.srv = &http.Server{Handler: http.HandlerFunc(l.handle), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = l.srv.Serve(ln) }()
	return l, nil
}

func (l *localRelease) handle(w http.ResponseWriter, req *http.Request) {
	name := path.Base(req.URL.Path)
	l.mu.Lock()
	l.requests = append(l.requests, req.Method+" "+name)
	data, ok := l.files[name]
	l.mu.Unlock()
	if !ok {
		http.NotFound(w, req)
		return
	}
	http.ServeContent(w, req, name, time.Time{}, bytes.NewReader(data))
}

func (l *localRelease) serveDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	files := map[string][]byte{}
	for _, e := range entries {
		if n := e.Name(); e.Type().IsRegular() && (n == manifestName || strings.HasSuffix(n, ".tar.gz")) {
			raw, err := os.ReadFile(filepath.Join(dir, n))
			if err != nil {
				return err
			}
			files[n] = raw
		}
	}
	l.set(files)
	return nil
}

func (l *localRelease) set(files map[string][]byte) {
	l.mu.Lock()
	l.files = files
	l.mu.Unlock()
}

func (l *localRelease) served(name string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, r := range l.requests {
		if strings.HasSuffix(r, " "+name) {
			return true
		}
	}
	return false
}

func (l *localRelease) log() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.requests...)
}

func (l *localRelease) close() { _ = l.srv.Close() }
