package shell

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/chialecode/trace/internal/uistate"
	"github.com/egoist/mygo"
	mygoui "github.com/egoist/mygo/ui"
)

func TestDecideStartupCapturesPreviousLastActiveAtAndUpdatesTimestamp(t *testing.T) {
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)

	// First run: no prior LastActiveAt, records current launch timestamp.
	d1, st1 := DecideStartupForChannel(uistate.State{}, LaunchArgs{}, "stable", "0.1.3", now)
	if d1.PreviousLastActiveAt != "" || d1.ValidRestart {
		t.Fatalf("first run decision = %+v", d1)
	}
	if st1.LastActiveAt != now.Format(time.RFC3339) {
		t.Fatalf("first run LastActiveAt = %q, want %q", st1.LastActiveAt, now.Format(time.RFC3339))
	}

	// Normal launch after 2h offline: captures previous timestamp, ValidRestart=false.
	later := now.Add(2 * time.Hour)
	d2, st2 := DecideStartupForChannel(st1, LaunchArgs{Background: true}, "stable", "0.1.3", later)
	if d2.OpenWindow {
		t.Fatal("--background should keep OpenWindow=false")
	}
	if d2.PreviousLastActiveAt != now.Format(time.RFC3339) {
		t.Fatalf("PreviousLastActiveAt = %q, want %q", d2.PreviousLastActiveAt, now.Format(time.RFC3339))
	}
	if d2.ValidRestart {
		t.Fatal("expected ValidRestart=false on normal launch")
	}
	if st2.LastActiveAt != later.Format(time.RFC3339) {
		t.Fatalf("updated LastActiveAt = %q, want %q", st2.LastActiveAt, later.Format(time.RFC3339))
	}

	// Valid update restart within TTL marks ValidRestart=true.
	restartAt := later.Add(2 * time.Hour)
	stWithRestart := st2
	stWithRestart.Update.Restart = &uistate.UpdateRestart{
		Reason:     "update",
		WindowOpen: true,
		At:         restartAt.Add(-30 * time.Second).Format(time.RFC3339),
	}
	d3, st3 := DecideStartupForChannel(stWithRestart, LaunchArgs{}, "stable", "0.1.4", restartAt)
	if !d3.ValidRestart {
		t.Fatal("expected ValidRestart=true for fresh update restart marker")
	}
	if st3.LastActiveAt != restartAt.Format(time.RFC3339) {
		t.Fatalf("restart LastActiveAt = %q, want %q", st3.LastActiveAt, restartAt.Format(time.RFC3339))
	}
}

func TestShellBackgroundLaunchDeliversOfflineLiveNoticeOnceWithZeroRequests(t *testing.T) {
	var blockNet atomic.Bool
	var blockedCalls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if blockNet.Load() {
			blockedCalls.Add(1)
			http.Error(w, "unexpected network call", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path == "/x/web-interface/nav" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"code":0,"data":{"isLogin":true,"mid":12345,"uname":"测试用户","wbi_img":{"img_url":"https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png","sub_url":"https://i0.hdslb.com/bfs/wbi/4932caff0ff746eab6f01bf08b70ac45.png"}}}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	root := t.TempDir()
	loc := appdirs.Locations{
		Channel:         "dev",
		Mode:            "installed",
		AppName:         "trace-dev",
		DisplayName:     "TRACE Dev",
		Identifier:      appdirs.IdentifierForChannel("dev"),
		InstallDir:      filepath.Join(root, "Programs", "trace-dev"),
		DataDir:         filepath.Join(root, "trace-dev"),
		DefaultCacheDir: filepath.Join(root, "cache"),
		CredentialsDir:  filepath.Join(root, "trace-dev", "credentials"),
		LogsDir:         filepath.Join(root, "trace-dev", "logs"),
		LockFile:        filepath.Join(root, "trace-dev", "app.lock"),
	}
	if err := appdirs.EnsureDirectories(loc); err != nil {
		t.Fatal(err)
	}

	b, err := service.NewBackend(service.Options{
		Locations:        loc,
		Desktop:          service.NewMockDesktop(),
		DisableBgWorkers: true,
		BilibiliOpts: bilibili.ClientOptions{
			Endpoints: bilibili.Endpoints{
				APIBase:      srv.URL,
				PassportBase: srv.URL,
				WWWBase:      srv.URL,
				SpaceBase:    srv.URL,
				DynamicBase:  srv.URL,
				LiveAPIBase:  srv.URL,
				LiveWebBase:  srv.URL,
			},
			PacingInterval: time.Millisecond,
			AllowLocalhost: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	if _, err := b.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatalf("ImportCookie: %v", err)
	}
	if _, err := b.CreateSubscription("测试主播", "", []subscription.AccountRef{
		{Site: "bilibili", UID: "20001", Name: "测试主播"},
	}, subscription.SubRules{AutoRecord: true}); err != nil {
		t.Fatal(err)
	}

	// Simulate previous clean exit 3 hours ago, then block ALL network traffic.
	now := time.Date(2026, 11, 28, 15, 0, 0, 0, time.UTC)
	prevActive := now.Add(-3 * time.Hour).Format(time.RFC3339)
	if _, err := b.MutateUIState(func(st *uistate.State) {
		st.LastActiveAt = prevActive
	}); err != nil {
		t.Fatal(err)
	}
	blockNet.Store(true)

	s := NewShell(loc, b)
	s.quiet = "hidden"
	suppressTestTrayNotification(t, s)
	s.newMainWindow = func(opts mygo.WindowOptions) shellWindow {
		return &fakeShellWindow{opts: opts}
	}

	// 1. Startup with --background stays in tray, queues offline notice locally with zero requests.
	decision := recordStartupDecision(b, loc.Channel, "0.1.3-dev.202611280100", LaunchArgs{Background: true}, now)
	if decision.OpenWindow {
		t.Fatal("expected OpenWindow=false on --background launch")
	}
	if !s.evaluateStartupOfflineLiveGap(decision, now) {
		t.Fatal("expected offline live gap notice to be queued")
	}
	if s.pendingOfflineLiveNotice == nil {
		t.Fatal("expected pendingOfflineLiveNotice to stay queued until window opens")
	}

	// 2. User opens the window from tray: notice is shown in UI and consumed once.
	s.RestoreFromTray()
	if s.uiApp == nil {
		t.Fatal("expected uiApp created on RestoreFromTray")
	}
	if s.pendingOfflineLiveNotice != nil {
		t.Fatal("expected pendingOfflineLiveNotice cleared once delivered to UI")
	}
	tt := mygoui.NewTester(s.uiApp.View, 1280, 860)
	tt.Frame()
	if _, ok := tt.Find("离线开播提示"); !ok || !tt.HasText(service.OfflineLiveGapMessage) {
		t.Fatal("expected offline live gap notice visible on first window open")
	}

	// 3. Closing to tray and reopening during the same run does NOT repeat the notice.
	s.CloseToTray()
	s.RestoreFromTray()
	if s.uiApp == nil {
		t.Fatal("expected uiApp recreated on second RestoreFromTray")
	}
	tt2 := mygoui.NewTester(s.uiApp.View, 1280, 860)
	tt2.Frame()
	if _, ok := tt2.Find("离线开播提示"); ok {
		t.Fatal("offline live gap notice must not repeat after close-to-tray and reopen")
	}

	// 4. Teardown from tray still updates LastActiveAt in ui-state.json.
	s.CloseToTray()
	if _, err := b.MutateUIState(func(st *uistate.State) { st.LastActiveAt = prevActive }); err != nil {
		t.Fatal(err)
	}
	s.teardown()
	if got := b.GetUIState().LastActiveAt; got == "" || got == prevActive {
		t.Fatalf("teardown from tray did not update LastActiveAt: got %q", got)
	}

	if calls := blockedCalls.Load(); calls != 0 {
		t.Fatalf("expected ZERO outbound requests during startup/gap/shell flow, got %d", calls)
	}
}

func TestSelectAutostartRegistryDefenseInDepth(t *testing.T) {
	installedLoc := appdirs.Locations{Channel: "stable", Mode: "installed"}
	portableLoc := appdirs.Locations{Channel: "stable", Mode: "portable"}

	if reg := selectAutostartRegistry(installedLoc, "0.1.3", "hidden"); reg == nil {
		t.Fatal("expected in-memory registry in quiet mode")
	} else if _, ok := reg.(*service.MemoryAutostartRegistry); !ok {
		t.Fatalf("expected *service.MemoryAutostartRegistry in quiet mode, got %T", reg)
	}
	if reg := selectAutostartRegistry(portableLoc, "0.1.3", ""); reg != nil {
		t.Fatalf("portable mode must not connect to real system registry, got %T", reg)
	}
	if reg := selectAutostartRegistry(installedLoc, appdirs.UnversionedBuild, ""); reg != nil {
		t.Fatalf("unversioned source build must not connect to real system registry, got %T", reg)
	}
	if reg := selectAutostartRegistry(installedLoc, "", ""); reg != nil {
		t.Fatalf("empty build version must not connect to real system registry, got %T", reg)
	}
}
