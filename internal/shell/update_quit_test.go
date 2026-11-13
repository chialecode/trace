package shell

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/ui"
	"github.com/chialecode/trace/internal/uistate"
	"github.com/chialecode/trace/internal/update"
	"github.com/egoist/mygo"
)

type quitHelperResult struct {
	Restart    *uistate.UpdateRestart `json:"restart"`
	LastRoute  string                 `json:"lastRoute"`
	TaskStatus string                 `json:"taskStatus"`
	TaskReason string                 `json:"taskReason"`
	Elapsed    int64                  `json:"elapsedMs"`
}

// runQuitForUpdateHelper runs TRACE's shell with a hidden window and a
// running cache task, forwards --quit-for-update as a second instance
// would, and records what the quit saved.
func runQuitForUpdateHelper(outPath string) error {
	_ = os.Setenv("TRACE_QUIET", "1")
	root, err := os.MkdirTemp("", "trace-quit-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	loc := appdirs.Locations{
		Channel: "dev", Mode: "installed", AppName: "trace-dev", DisplayName: "TRACE Dev",
		DataDir:         filepath.Join(root, "trace-dev"),
		DefaultCacheDir: filepath.Join(root, "cache"),
		CredentialsDir:  filepath.Join(root, "trace-dev", "credentials"),
		LogsDir:         filepath.Join(root, "trace-dev", "logs"),
		LockFile:        filepath.Join(root, "trace-dev", "app.lock"),
	}
	if err := appdirs.EnsureDirectories(loc); err != nil {
		return err
	}
	mygo.App.SetName(loc.AppName)
	mygo.App.SetPath(mygo.PathUserData, filepath.Join(loc.DataDir, "mygo"))
	mygo.App.SetPath(mygo.PathCache, filepath.Join(loc.DataDir, "cache"))
	mygo.App.SetPath(mygo.PathLogs, loc.LogsDir)

	b, err := service.NewBackend(service.Options{Locations: loc, Desktop: service.NewMockDesktop(), DisableBgWorkers: true})
	if err != nil {
		return err
	}
	db, err := sql.Open("sqlite", filepath.Join(loc.DataDir, "library.db"))
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO download_tasks (task_id, site, kind, target_id, status, created_at, updated_at) VALUES ('t1','bilibili','video','BV1','running',?,?)`, now, now); err != nil {
		return err
	}
	db.Close()

	sh := NewShell(loc, b)
	mygo.App.OnWindowAllClosed(func() {})
	started := time.Now()
	mygo.App.WhenReady(func() {
		sh.setupTray()
		sh.openOrRestoreMainWindow()
		if sh.uiApp != nil {
			sh.uiApp.Navigate(ui.RouteSettings)
		}
		go func() {
			time.Sleep(50 * time.Millisecond)
			mygo.RunOnMain(func() { sh.HandleSecondInstance([]string{FlagQuitForUpdate}) })
		}()
	})
	if err := mygo.App.Run(); err != nil {
		return err
	}
	elapsed := time.Since(started)
	st := b.GetUIState()
	b.Close()

	db, err = sql.Open("sqlite", filepath.Join(loc.DataDir, "library.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	var res quitHelperResult
	if err := db.QueryRow(`SELECT status, pause_reason FROM download_tasks WHERE task_id='t1'`).Scan(&res.TaskStatus, &res.TaskReason); err != nil {
		return err
	}
	res.Restart, res.LastRoute, res.Elapsed = st.Update.Restart, st.LastRoute, elapsed.Milliseconds()
	raw, _ := json.Marshal(res)
	return os.WriteFile(outPath, raw, 0o600)
}

// --quit-for-update, forwarded to the running TRACE, pauses its tasks as
// the tray's Quit does, saves the route and records that the window was
// open, then quits (M2-K04).
func TestQuitForUpdateSavesWorkAndWindowState(t *testing.T) {
	if runtime.GOOS != "windows" && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		t.Skip("no display: the real window helper is not run")
	}
	tmpDir := t.TempDir()
	outPath := filepath.Join(tmpDir, "quit.json")
	sock := filepath.Join(tmpDir, "temp")
	_ = os.MkdirAll(sock, 0o700)
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "TRACE_QUIET=1", "TRACE_SHELL_QUIT_HELPER_OUT="+outPath, "TEMP="+sock, "TMP="+sock)
	appdirs.SetSubprocessQuiet(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("quit helper failed: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	var res quitHelperResult
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	if res.Restart == nil || res.Restart.Reason != "dev-install" || !res.Restart.WindowOpen {
		t.Fatalf("restart record: %+v", res.Restart)
	}
	if res.LastRoute != ui.RouteSettings {
		t.Fatalf("route not saved: %q", res.LastRoute)
	}
	if res.TaskStatus != "paused" || res.TaskReason != "app_closed" {
		t.Fatalf("task after quit for update: %s/%s", res.TaskStatus, res.TaskReason)
	}
	if res.Elapsed > 30000 {
		t.Fatalf("quit took %d ms", res.Elapsed)
	}
}

func TestParseLaunchArgs(t *testing.T) {
	if la := ParseLaunchArgs([]string{"--quit-for-update"}); !la.QuitForUpdate || la.Background {
		t.Fatalf("%+v", la)
	}
	if la := ParseLaunchArgs([]string{"BV1xx", "--BACKGROUND"}); la.QuitForUpdate || !la.Background {
		t.Fatalf("%+v", la)
	}
	if la := ParseLaunchArgs(nil); la.QuitForUpdate || la.Background {
		t.Fatalf("%+v", la)
	}
}

func TestDecideStartupAfterUpdate(t *testing.T) {
	now := time.Date(2026, 11, 9, 16, 0, 0, 0, time.UTC)
	marker := func(open bool, at time.Time) *uistate.UpdateRestart {
		return &uistate.UpdateRestart{Reason: "dev-install", WindowOpen: open, At: at.Format(time.RFC3339)}
	}
	updateMarker := func(open bool, at time.Time) *uistate.UpdateRestart {
		return &uistate.UpdateRestart{Reason: "update", WindowOpen: open, At: at.Format(time.RFC3339)}
	}
	cases := []struct {
		name string
		st   uistate.State
		args LaunchArgs
		open bool
	}{
		{"plain launch opens the window", uistate.State{}, LaunchArgs{}, true},
		{"background launch stays in the tray", uistate.State{}, LaunchArgs{Background: true}, false},
		{"reopen after quit from the tray", uistate.State{Update: uistate.UpdateState{Restart: marker(false, now.Add(-20*time.Second))}}, LaunchArgs{}, false},
		{"reopen after quit with the window open", uistate.State{Update: uistate.UpdateState{Restart: marker(true, now.Add(-20*time.Second))}}, LaunchArgs{}, true},
		{"dev install reopened in background stays in the tray even if the window was open", uistate.State{Update: uistate.UpdateState{Restart: marker(true, now.Add(-20*time.Second))}}, LaunchArgs{Background: true}, false},
		{"app update reopens the window as it was, even launched in background", uistate.State{Update: uistate.UpdateState{Restart: updateMarker(true, now.Add(-20*time.Second))}}, LaunchArgs{Background: true}, true},
		{"an old record does not hide the window", uistate.State{Update: uistate.UpdateState{Restart: marker(false, now.Add(-time.Hour))}}, LaunchArgs{}, true},
	}
	for _, tc := range cases {
		d, next := DecideStartup(tc.st, tc.args, "0.1.2", now)
		if d.OpenWindow != tc.open {
			t.Errorf("%s: OpenWindow=%v", tc.name, d.OpenWindow)
		}
		if next.Update.Restart != nil {
			t.Errorf("%s: restart record not consumed", tc.name)
		}
	}

	st := uistate.State{Update: uistate.UpdateState{LastRunVersion: "0.1.1", ReadyVersion: "0.1.2", ReadyNotes: "## 0.1.2\n- 新功能"}}
	d, next := DecideStartup(st, LaunchArgs{}, "0.1.2", now)
	if d.UpdatedTo != "0.1.2" || d.UpdatedNotes == "" {
		t.Fatalf("first launch after the update: %+v", d)
	}
	if next.Update.LastRunVersion != "0.1.2" || next.Update.ReadyVersion != "" {
		t.Fatalf("state after first launch: %+v", next.Update)
	}
	d, _ = DecideStartup(next, LaunchArgs{}, "0.1.2", now)
	if d.UpdatedTo != "" {
		t.Fatal("the update notice shows only once")
	}
	if d, _ := DecideStartup(uistate.State{}, LaunchArgs{}, "0.1.2", now); d.UpdatedTo != "" {
		t.Fatal("a first install is not an update")
	}
	if d, _ := DecideStartup(uistate.State{Update: uistate.UpdateState{LastRunVersion: "0.1.3"}}, LaunchArgs{}, "0.1.2", now); d.UpdatedTo != "" {
		t.Fatal("an older version is not an update")
	}
	if d, n := DecideStartup(uistate.State{Update: uistate.UpdateState{LastRunVersion: "0.1.1"}}, LaunchArgs{}, appdirs.UnversionedBuild, now); d.UpdatedTo != "" || n.Update.LastRunVersion != "0.1.1" {
		t.Fatal("a source build does not change the record")
	}
}

func TestDecideStartupRecordsVersionChangeMethods(t *testing.T) {
	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)

	// 1. Dev channel package install (with or without restart marker) -> dev_install ("本机打包安装")
	devSt := uistate.State{
		Update: uistate.UpdateState{
			LastRunVersion: "0.1.2-dev.202611150900",
			Restart: &uistate.UpdateRestart{
				Reason:     "dev-install",
				WindowOpen: true,
				At:         now.Add(-15 * time.Second).Format(time.RFC3339),
			},
		},
	}
	d, next := DecideStartupForChannel(devSt, LaunchArgs{}, "dev", "0.1.2-dev.202611151001", now)
	if d.UpdateRecord == nil || d.UpdateRecord.Method != update.UpdateMethodDevInstall || update.MethodLabel(d.UpdateRecord.Method) != "本机打包安装" {
		t.Fatalf("dev startup update record = %+v", d.UpdateRecord)
	}
	if d.UpdateRecord.FromVersion != "0.1.2-dev.202611150900" || d.UpdateRecord.ToVersion != "0.1.2-dev.202611151001" {
		t.Fatalf("dev version span = %+v", d.UpdateRecord)
	}

	// Dev version change without restart marker also records dev_install.
	d, _ = DecideStartupForChannel(next, LaunchArgs{}, "dev", "0.1.1-dev.202611151100", now.Add(time.Minute))
	if d.UpdateRecord == nil || d.UpdateRecord.Method != update.UpdateMethodDevInstall {
		t.Fatalf("dev rollback/repack update record = %+v", d.UpdateRecord)
	}
	if d.UpdatedTo != "" {
		t.Fatalf("downgrade should not set UpdatedTo banner: %q", d.UpdatedTo)
	}

	// 2. Release channel with valid restart marker -> auto_update ("自动更新")
	relAutoSt := uistate.State{
		Update: uistate.UpdateState{
			LastRunVersion: "0.1.1",
			ReadyVersion:   "0.1.2",
			Restart: &uistate.UpdateRestart{
				Reason:     "update",
				WindowOpen: true,
				At:         now.Add(-30 * time.Second).Format(time.RFC3339),
			},
		},
	}
	d, _ = DecideStartupForChannel(relAutoSt, LaunchArgs{}, "stable", "0.1.2", now)
	if d.UpdateRecord == nil || d.UpdateRecord.Method != update.UpdateMethodAuto || update.MethodLabel(d.UpdateRecord.Method) != "自动更新" {
		t.Fatalf("release auto-update record = %+v", d.UpdateRecord)
	}

	// 3. Release channel without restart marker (or expired restart marker) -> manual_install ("手动安装")
	relManualSt := uistate.State{
		Update: uistate.UpdateState{
			LastRunVersion: "0.1.2",
		},
	}
	d, _ = DecideStartupForChannel(relManualSt, LaunchArgs{}, "stable", "0.1.3", now)
	if d.UpdateRecord == nil || d.UpdateRecord.Method != update.UpdateMethodManual || update.MethodLabel(d.UpdateRecord.Method) != "手动安装" {
		t.Fatalf("release manual-install record = %+v", d.UpdateRecord)
	}

	relExpiredMarkerSt := uistate.State{
		Update: uistate.UpdateState{
			LastRunVersion: "0.1.3",
			Restart: &uistate.UpdateRestart{
				Reason:     "update",
				WindowOpen: false,
				At:         now.Add(-2 * time.Hour).Format(time.RFC3339),
			},
		},
	}
	d, _ = DecideStartupForChannel(relExpiredMarkerSt, LaunchArgs{}, "stable", "0.1.4", now)
	if d.UpdateRecord == nil || d.UpdateRecord.Method != update.UpdateMethodManual {
		t.Fatalf("expired restart marker must fall back to manual_install: %+v", d.UpdateRecord)
	}

	// 4. First launch, same version, or unversioned builds do not write an update record.
	if d, _ := DecideStartupForChannel(uistate.State{}, LaunchArgs{}, "stable", "0.1.1", now); d.UpdateRecord != nil {
		t.Fatalf("first launch must not write update record: %+v", d.UpdateRecord)
	}
	if d, _ := DecideStartupForChannel(uistate.State{Update: uistate.UpdateState{LastRunVersion: "0.1.1"}}, LaunchArgs{}, "stable", "0.1.1", now); d.UpdateRecord != nil {
		t.Fatalf("same version must not write update record: %+v", d.UpdateRecord)
	}
	if d, _ := DecideStartupForChannel(uistate.State{Update: uistate.UpdateState{LastRunVersion: appdirs.UnversionedBuild}}, LaunchArgs{}, "dev", "0.1.2-dev.1", now); d.UpdateRecord != nil {
		t.Fatalf("unversioned previous build must not write update record: %+v", d.UpdateRecord)
	}
	if d, _ := DecideStartupForChannel(uistate.State{Update: uistate.UpdateState{LastRunVersion: "0.1.1"}}, LaunchArgs{}, "dev", appdirs.UnversionedBuild, now); d.UpdateRecord != nil {
		t.Fatalf("unversioned current build must not write update record: %+v", d.UpdateRecord)
	}

	// 5. End-to-end persistence through recordStartupDecision -> <DataDir>/update-history.json
	root := t.TempDir()
	loc := appdirs.Locations{
		Channel:         "stable",
		Mode:            "installed",
		AppName:         "trace",
		DisplayName:     "TRACE",
		DataDir:         filepath.Join(root, "data"),
		DefaultCacheDir: filepath.Join(root, "cache"),
		CredentialsDir:  filepath.Join(root, "data", "credentials"),
		LogsDir:         filepath.Join(root, "data", "logs"),
		LockFile:        filepath.Join(root, "data", "app.lock"),
	}
	b, err := service.NewBackend(service.Options{Locations: loc, Desktop: service.NewMockDesktop(), DisableBgWorkers: true})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	// Seed initial version 0.1.0
	recordStartupDecision(b, "stable", "0.1.0", LaunchArgs{}, now)
	// Manual install to 0.1.1
	recordStartupDecision(b, "stable", "0.1.1", LaunchArgs{}, now.Add(time.Minute))
	// Auto update with valid restart marker to 0.1.2
	_, _ = b.MutateUIState(func(st *uistate.State) {
		st.Update.Restart = &uistate.UpdateRestart{
			Reason:     "update",
			WindowOpen: true,
			At:         now.Add(2*time.Minute - 10*time.Second).Format(time.RFC3339),
		}
	})
	recordStartupDecision(b, "stable", "0.1.2", LaunchArgs{}, now.Add(2*time.Minute))
	// Dev install to 0.1.3-dev.1
	recordStartupDecision(b, "dev", "0.1.3-dev.1", LaunchArgs{}, now.Add(3*time.Minute))

	hist := b.GetUpdateHistory()
	if hist.Version != 1 || len(hist.Updates) != 3 {
		t.Fatalf("persisted update history = %+v", hist)
	}
	if hist.Updates[0].Method != update.UpdateMethodDevInstall ||
		hist.Updates[1].Method != update.UpdateMethodAuto ||
		hist.Updates[2].Method != update.UpdateMethodManual {
		t.Fatalf("unexpected persisted update methods: %+v", hist.Updates)
	}
}

func init() {
	if out := os.Getenv("TRACE_SHELL_QUIT_HELPER_OUT"); out != "" {
		if err := runQuitForUpdateHelper(out); err != nil {
			fmt.Fprintf(os.Stderr, "quit helper: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
}
