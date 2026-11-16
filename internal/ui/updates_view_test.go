package ui

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/update"
	"github.com/egoist/mygo/ui"
)

type fakeUIUpdateEngine struct {
	mu      sync.Mutex
	enabled bool
	release *update.Release
	err     error
	doneCh  chan struct{}
}

func (e *fakeUIUpdateEngine) Enabled() bool { return e.enabled }

func (e *fakeUIUpdateEngine) Check(ctx context.Context) (*update.Release, error) {
	e.mu.Lock()
	rel := e.release
	err := e.err
	ch := e.doneCh
	e.mu.Unlock()
	if ch != nil {
		defer func() {
			select {
			case ch <- struct{}{}:
			default:
			}
		}()
	}
	if err != nil {
		return nil, err
	}
	if rel == nil {
		return nil, nil
	}
	cp := *rel
	return &cp, nil
}

func (e *fakeUIUpdateEngine) Install(ctx context.Context, progress func(int64, int64)) error {
	e.mu.Lock()
	ch := e.doneCh
	e.mu.Unlock()
	if progress != nil {
		progress(100, 100)
	}
	if ch != nil {
		defer func() {
			select {
			case ch <- struct{}{}:
			default:
			}
		}()
	}
	return nil
}

type stubDevBackend struct {
	service.BackendAPI
	diag         service.Diagnostics
	installedVer string
}

func (s *stubDevBackend) DiagnosticsSnapshot() service.Diagnostics {
	d := s.BackendAPI.DiagnosticsSnapshot()
	if s.diag.Version != "" {
		d.Version = s.diag.Version
	}
	if s.diag.Commit != "" {
		d.Commit = s.diag.Commit
	}
	if s.diag.BuildTime != "" {
		d.BuildTime = s.diag.BuildTime
	}
	if s.diag.Channel != "" {
		d.Channel = s.diag.Channel
	}
	if s.diag.DisplayName != "" {
		d.DisplayName = s.diag.DisplayName
	}
	return d
}

func (s *stubDevBackend) InstalledDevVersion() string {
	return s.installedVer
}

func assertSettingsNoOverflow(t *testing.T, tt *ui.Tester) {
	t.Helper()
	for _, w := range []int{1280, 1000, 720} {
		tt.SetSize(w, 860)
		tt.Frame()
		left, okL := tt.Find("左栏导航")
		center, okC := tt.Find("中栏内容")
		if !okL || !okC {
			t.Fatalf("width=%d missing shell landmarks (left=%v center=%v)", w, okL, okC)
		}
		if left.X < -0.5 || center.X+center.W > float32(w)+0.5 {
			t.Fatalf("width=%d horizontal overflow: left=%+v center=%+v", w, left, center)
		}
		for _, lbl := range []string{"版本信息", "当前版本", "更新方式", "检查记录区域", "更新记录区域"} {
			r, ok := tt.Find(lbl)
			if !ok || r.X < center.X-0.5 || r.X+r.W > float32(w)+0.5 {
				t.Fatalf("width=%d section %q clipped/overflowed: ok=%v rect=%+v", w, lbl, ok, r)
			}
		}
	}
}

func TestSettingsUpdateCategoryOrderAndAboutNavigation(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()

	eng := &fakeUIUpdateEngine{enabled: true}
	ctrl := update.NewController(update.Options{
		Engine:         eng,
		Channel:        "stable",
		CurrentVersion: "0.1.2",
		Prefs: func() update.Prefs {
			p := app.backend.GetSettings().Updates
			return update.Prefs{AutoCheck: p.AutoCheck, AutoDownload: p.AutoDownload}
		},
	})
	app.BindUpdater(ctrl)
	app.SettingsCategory = "about"
	app.Navigate(RouteSettings)

	tt := ui.NewTester(app.View, 1280, 860)
	for _, w := range []int{1280, 1000, 720} {
		tt.SetSize(w, 860)
		tt.Frame()
		updRect, okU := tt.Find("设置分类：更新")
		abtRect, okA := tt.Find("设置分类：关于")
		if !okU || !okA {
			t.Fatalf("width=%d missing sidebar categories (update=%v about=%v)", w, okU, okA)
		}
		if updRect.Y >= abtRect.Y {
			t.Fatalf("width=%d expected 更新 above 关于: update.Y=%.1f about.Y=%.1f", w, updRect.Y, abtRect.Y)
		}
	}

	tt.SetSize(1280, 860)
	tt.Frame()
	for _, movedLabel := range []string{"检查更新按钮", "立即检查按钮", "自动检查更新开关", "自动下载更新开关"} {
		if _, ok := tt.Find(movedLabel); ok {
			t.Fatalf("设置 › 关于 must not keep moved control %q", movedLabel)
		}
	}
	if !tt.HasText("THIRD-PARTY-NOTICES.txt") || !tt.HasText("本地媒体库：") {
		t.Fatal("设置 › 关于 lost diagnostics summary or license notice")
	}
	if _, ok := tt.Find("版本与更新见‘更新’"); !ok {
		t.Fatal("设置 › 关于 missing link 版本与更新见‘更新’")
	}

	if err := tt.Click("版本与更新见‘更新’"); err != nil {
		t.Fatalf("click 版本与更新见‘更新’: %v", err)
	}
	tt.Frame()
	if app.SettingsCategory != "update" {
		t.Fatalf("SettingsCategory = %q, want update", app.SettingsCategory)
	}
	for _, label := range []string{"立即检查按钮", "自动检查更新开关", "自动下载更新开关"} {
		if _, ok := tt.Find(label); !ok {
			t.Fatalf("设置 › 更新 missing expected control %q after jump", label)
		}
	}
}

func TestSettingsUpdateReleasePageCheckFlowAndHistoryM4K32(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()

	baseNow := time.Date(2026, 11, 15, 9, 30, 0, 0, time.UTC)
	if err := app.backend.RecordUpdateCheck(update.CheckRecord{
		At:      baseNow.Add(-2 * time.Hour).Format(time.RFC3339),
		Trigger: update.CheckTriggerAuto,
		Result:  update.MsgUpToDate,
	}); err != nil {
		t.Fatal(err)
	}
	if err := app.backend.RecordAppUpdate(update.UpdateRecord{
		At:          baseNow.Add(-48 * time.Hour).Format(time.RFC3339),
		FromVersion: "0.1.0",
		ToVersion:   "0.1.1",
		Method:      update.UpdateMethodManual,
	}); err != nil {
		t.Fatal(err)
	}
	if err := app.backend.RecordAppUpdate(update.UpdateRecord{
		At:          baseNow.Add(-24 * time.Hour).Format(time.RFC3339),
		FromVersion: "0.1.1",
		ToVersion:   "0.1.2",
		Method:      update.UpdateMethodAuto,
	}); err != nil {
		t.Fatal(err)
	}

	doneCh := make(chan struct{}, 4)
	eng := &fakeUIUpdateEngine{enabled: true, doneCh: doneCh}
	nowMu := sync.Mutex{}
	curNow := baseNow

	ctrl := update.NewController(update.Options{
		Engine:          eng,
		Channel:         "stable",
		CurrentVersion:  "0.1.2",
		LastCheck:       baseNow.Add(-2 * time.Hour),
		LastCheckResult: update.MsgUpToDate,
		Now: func() time.Time {
			nowMu.Lock()
			defer nowMu.Unlock()
			return curNow
		},
		Prefs: func() update.Prefs {
			p := app.backend.GetSettings().Updates
			return update.Prefs{AutoCheck: p.AutoCheck, AutoDownload: p.AutoDownload}
		},
		RecordCheck: func(rec update.CheckRecord) {
			_ = app.backend.RecordUpdateCheck(rec)
		},
	})
	app.BindUpdater(ctrl)
	app.SettingsCategory = "update"
	app.Navigate(RouteSettings)

	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()

	for _, expectedText := range []string{
		"TRACE",
		"版本 0.1.2（正式版）",
		releaseUpdateMethodLine,
		"已是最新版本",
		"下次检查：",
		"检查记录",
		"更新记录",
		"从 0.1.1 到 0.1.2",
		"自动更新",
		"从 0.1.0 到 0.1.1",
		"手动安装",
	} {
		if !tt.HasText(expectedText) {
			t.Fatalf("release 设置 › 更新 missing text %q", expectedText)
		}
	}

	// Turn off AutoDownload so manual check surfaces "下载并安装".
	if err := tt.Click("自动下载更新开关"); err != nil {
		t.Fatalf("click 自动下载更新开关: %v", err)
	}
	tt.Frame()
	if app.backend.GetSettings().Updates.AutoDownload {
		t.Fatal("expected AutoDownload=false after clicking switch")
	}

	// Click "立即检查" when 0.1.3 is available -> adds a new manual check record.
	eng.mu.Lock()
	eng.release = &update.Release{Version: "0.1.3", Notes: "- 新增更新页"}
	eng.mu.Unlock()
	nowMu.Lock()
	curNow = baseNow.Add(10 * time.Minute)
	nowMu.Unlock()

	if err := tt.Click("立即检查按钮"); err != nil {
		t.Fatalf("click 立即检查按钮: %v", err)
	}
	select {
	case <-doneCh:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for CheckNow")
	}
	// Wait briefly for RecordCheck after Engine.Check returns.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(app.backend.GetUpdateHistory().Checks) >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	tt.Frame()

	hist := app.backend.GetUpdateHistory()
	if len(hist.Checks) != 2 || hist.Checks[0].Trigger != update.CheckTriggerManual || hist.Checks[0].Result != "发现 0.1.3" {
		t.Fatalf("expected manual check record 发现 0.1.3, got %+v", hist.Checks)
	}
	if !tt.HasText("发现 0.1.3") {
		t.Fatal("UI did not display newly added check result 发现 0.1.3")
	}
	if _, ok := tt.Find("更新页下载并安装"); !ok {
		t.Fatal("expected 下载并安装 button after manual check found 0.1.3")
	}

	// Turn off AutoCheck -> next check line reflects disabled automatic checks.
	if err := tt.Click("自动检查更新开关"); err != nil {
		t.Fatalf("click 自动检查更新开关: %v", err)
	}
	tt.Frame()
	if !tt.HasText("下次检查：已关闭自动检查") {
		t.Fatal("expected 下次检查：已关闭自动检查 when AutoCheck is turned off")
	}

	assertSettingsNoOverflow(t, tt)
}

func TestSettingsUpdateDevPageNoCheckControlsAndInstalledVersionPromptM4K32(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()

	if err := app.backend.RecordAppUpdate(update.UpdateRecord{
		At:          "2026-11-15T10:01:00Z",
		FromVersion: "0.1.2-dev.202611150900",
		ToVersion:   "0.1.2-dev.202611151001",
		Method:      update.UpdateMethodDevInstall,
	}); err != nil {
		t.Fatal(err)
	}

	stub := &stubDevBackend{
		BackendAPI: app.backend,
		diag: service.Diagnostics{
			Version:     "0.1.2-dev.202611151001",
			Commit:      "7c164c9",
			BuildTime:   "2026-11-15T10:01:00Z",
			Channel:     "dev",
			DisplayName: "TRACE Dev",
		},
		installedVer: "",
	}
	app.backend = stub

	devCtrl := update.NewController(update.Options{
		Engine:         &fakeUIUpdateEngine{enabled: false},
		Channel:        "dev",
		CurrentVersion: "0.1.2-dev.202611151001",
	})
	app.BindUpdater(devCtrl)
	app.SettingsCategory = "update"
	app.Navigate(RouteSettings)

	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()

	// Dev build must NOT render check button or automatic switches.
	for _, forbiddenLabel := range []string{"立即检查按钮", "检查更新按钮", "自动检查更新开关", "自动下载更新开关", "更新页下载并安装", "更新页重启以更新"} {
		if _, ok := tt.Find(forbiddenLabel); ok {
			t.Fatalf("dev 设置 › 更新 must not render %q", forbiddenLabel)
		}
	}

	// Case 1: Not installed locally.
	for _, expected := range []string{
		"TRACE Dev",
		"版本 0.1.2-dev.202611151001（开发版）",
		devUpdateMethodLine,
		"本机安装的开发版：未在本机安装",
		"开发版不会自己联网检查更新，无检查记录",
		"从 0.1.2-dev.202611150900 到 0.1.2-dev.202611151001",
		"本机打包安装",
	} {
		if !tt.HasText(expected) {
			t.Fatalf("dev 设置 › 更新 (uninstalled) missing text %q", expected)
		}
	}
	if tt.HasText("已安装更新的版本，重启 TRACE Dev 后生效") {
		t.Fatal("uninstalled dev state must not show restart prompt")
	}

	// Case 2: Installed version matches running version -> no restart prompt.
	stub.installedVer = "0.1.2-dev.202611151001"
	tt.Frame()
	if !tt.HasText("本机安装的开发版：0.1.2-dev.202611151001") {
		t.Fatal("missing matching installed dev version")
	}
	if tt.HasText("已安装更新的版本，重启 TRACE Dev 后生效") {
		t.Fatal("matching installed dev version must not show restart prompt")
	}

	// Case 3: Installed version is older than running version (e.g. unpacked newer build) -> no restart prompt.
	stub.installedVer = "0.1.2-dev.202611150900"
	tt.Frame()
	if !tt.HasText("本机安装的开发版：0.1.2-dev.202611150900") {
		t.Fatal("missing older installed dev version")
	}
	if tt.HasText("已安装更新的版本，重启 TRACE Dev 后生效") {
		t.Fatal("older installed dev version must not claim a newer version is installed")
	}

	// Case 4: Installed version is newer than running version -> shows restart prompt.
	stub.installedVer = "0.1.2-dev.202611151230"
	tt.Frame()
	if !tt.HasText("本机安装的开发版：0.1.2-dev.202611151230") {
		t.Fatal("missing newer installed dev version")
	}
	if !tt.HasText("已安装更新的版本，重启 TRACE Dev 后生效") {
		t.Fatal("missing prompt 已安装更新的版本，重启 TRACE Dev 后生效 when installed dev version is newer")
	}

	// Case 5: Unversioned source run (0.0.0-dev) -> no restart prompt.
	unverCtrl := update.NewController(update.Options{
		Engine:         &fakeUIUpdateEngine{enabled: false},
		Channel:        "dev",
		CurrentVersion: appdirs.UnversionedBuild,
	})
	app.BindUpdater(unverCtrl)
	stub.diag.Version = appdirs.UnversionedBuild
	tt.Frame()
	if tt.HasText("已安装更新的版本，重启 TRACE Dev 后生效") {
		t.Fatal("unversioned source build must not show restart prompt")
	}
	app.BindUpdater(devCtrl)
	stub.diag.Version = "0.1.2-dev.202611151001"
	tt.Frame()

	assertSettingsNoOverflow(t, tt)
}
