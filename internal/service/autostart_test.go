package service

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/settings"
	"github.com/chialecode/trace/internal/site"
)

func installedChannelLocations(t *testing.T, channel string) appdirs.Locations {
	t.Helper()
	tmp := t.TempDir()
	ch := appdirs.EffectiveChannel(channel)
	appName := appdirs.AppNameForChannel(ch)
	dataDir := filepath.Join(tmp, appName)
	return appdirs.Locations{
		Channel:         ch,
		Mode:            "installed",
		AppName:         appName,
		DisplayName:     appdirs.DisplayNameForChannel(ch),
		Identifier:      appdirs.IdentifierForChannel(ch),
		InstallDir:      filepath.Join(tmp, "Programs", appName),
		DataDir:         dataDir,
		DefaultCacheDir: filepath.Join(tmp, "cache", appName),
		CredentialsDir:  filepath.Join(dataDir, "credentials"),
		LogsDir:         filepath.Join(dataDir, "logs"),
		LockFile:        filepath.Join(dataDir, "app.lock"),
	}
}

func TestBuildAutostartCommandValidation(t *testing.T) {
	got, err := BuildAutostartCommand(`C:\Users\demo user\AppData\Local\Programs\trace-dev\trace.exe`, "windows")
	if err != nil {
		t.Fatalf("BuildAutostartCommand valid path: %v", err)
	}
	want := `"C:\Users\demo user\AppData\Local\Programs\trace-dev\trace.exe" --background`
	if got != want {
		t.Fatalf("BuildAutostartCommand = %q, want %q", got, want)
	}

	for _, bad := range []string{
		"",
		"   ",
		`relative\trace.exe`,
		`C:\Program Files\TRACE\trace.test.exe`,
		`C:\bad"path\trace.exe`,
		"C:\\bad\npath\\trace.exe",
		"C:\\bad\x00path\\trace.exe",
	} {
		if cmd, err := BuildAutostartCommand(bad, "windows"); err == nil {
			t.Errorf("BuildAutostartCommand(%q) unexpectedly succeeded: %q", bad, cmd)
		}
	}
}

func TestAutostartChannelIsolationAndToggle(t *testing.T) {
	reg := NewMemoryAutostartRegistry()
	stableVal := appdirs.AutostartValueNameForChannel("stable")
	devVal := appdirs.AutostartValueNameForChannel("dev")
	testVal := appdirs.AutostartValueNameForChannel("test")

	// Pre-populate sibling channels to prove dev never touches stable or test.
	if err := reg.Set(stableVal, `"C:\Apps\trace\trace.exe" --background`); err != nil {
		t.Fatal(err)
	}
	if err := reg.Set(testVal, `"C:\Apps\trace-test\trace.exe" --background`); err != nil {
		t.Fatal(err)
	}

	loc := installedChannelLocations(t, "dev")
	exePath := `C:\Users\demo\AppData\Local\Programs\trace-dev\trace.exe`
	b, err := NewBackend(Options{
		Locations:         loc,
		Desktop:           NewMockDesktop(),
		DisableBgWorkers:  true,
		AutostartRegistry: reg,
		AutostartExePath:  exePath,
		AutostartVersion:  "0.1.3-dev.202611280100",
		AutostartGOOS:     "windows",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	st := b.AutostartStatus()
	if !st.Supported || st.Enabled || st.Registered {
		t.Fatalf("initial status = %+v", st)
	}
	if st.ValueName != devVal {
		t.Fatalf("ValueName = %q, want %q", st.ValueName, devVal)
	}

	// Toggle ON: writes only dev channel value and keeps settings schema v1.
	next := b.GetSettings()
	next.Window.AutoStart = true
	saved, err := b.UpdateSettings(next)
	if err != nil {
		t.Fatalf("enable autostart: %v", err)
	}
	if !saved.Window.AutoStart {
		t.Fatal("saved.Window.AutoStart = false, want true")
	}

	snap := reg.Snapshot()
	wantDevCmd := `"` + exePath + `" --background`
	if snap[devVal] != wantDevCmd {
		t.Fatalf("dev Run entry = %q, want %q", snap[devVal], wantDevCmd)
	}
	if snap[stableVal] != `"C:\Apps\trace\trace.exe" --background` {
		t.Fatalf("stable Run entry clobbered: %q", snap[stableVal])
	}
	if snap[testVal] != `"C:\Apps\trace-test\trace.exe" --background` {
		t.Fatalf("test Run entry clobbered: %q", snap[testVal])
	}

	st = b.AutostartStatus()
	if !st.Supported || !st.Enabled || !st.Registered {
		t.Fatalf("enabled status = %+v", st)
	}

	raw, err := os.ReadFile(filepath.Join(loc.DataDir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if v, ok := doc["version"].(float64); !ok || int(v) != 1 {
		t.Fatalf("settings.json version = %v, want 1", doc["version"])
	}

	// Toggle OFF: removes only dev channel value; siblings remain intact.
	next = b.GetSettings()
	next.Window.AutoStart = false
	saved, err = b.UpdateSettings(next)
	if err != nil {
		t.Fatalf("disable autostart: %v", err)
	}
	if saved.Window.AutoStart {
		t.Fatal("saved.Window.AutoStart = true, want false")
	}

	snap = reg.Snapshot()
	if _, exists := snap[devVal]; exists {
		t.Fatalf("dev Run entry still exists after disable: %+v", snap)
	}
	if snap[stableVal] != `"C:\Apps\trace\trace.exe" --background` {
		t.Fatalf("stable Run entry removed when disabling dev: %+v", snap)
	}
	if snap[testVal] != `"C:\Apps\trace-test\trace.exe" --background` {
		t.Fatalf("test Run entry removed when disabling dev: %+v", snap)
	}
}

func TestAutostartUnsupportedEnvironments(t *testing.T) {
	cases := []struct {
		name      string
		mutateLoc func(*appdirs.Locations)
		opts      func(*Options, *MemoryAutostartRegistry)
		wantSub   string
	}{
		{
			name: "non_windows",
			opts: func(o *Options, reg *MemoryAutostartRegistry) {
				o.AutostartRegistry = reg
				o.AutostartExePath = "/usr/local/bin/trace"
				o.AutostartVersion = "0.1.3"
				o.AutostartGOOS = "linux"
			},
			wantSub: "仅支持 Windows",
		},
		{
			name: "portable_mode",
			mutateLoc: func(loc *appdirs.Locations) {
				loc.Mode = "portable"
			},
			opts: func(o *Options, reg *MemoryAutostartRegistry) {
				o.AutostartRegistry = reg
				o.AutostartExePath = `C:\Portable\trace.exe`
				o.AutostartVersion = "0.1.3"
				o.AutostartGOOS = "windows"
			},
			wantSub: "安装版",
		},
		{
			name: "unversioned_source_build",
			opts: func(o *Options, reg *MemoryAutostartRegistry) {
				o.AutostartRegistry = reg
				o.AutostartExePath = `C:\Users\demo\AppData\Local\Programs\trace-dev\trace.exe`
				o.AutostartVersion = appdirs.UnversionedBuild
				o.AutostartGOOS = "windows"
			},
			wantSub: "源码运行",
		},
		{
			name: "remote_profile",
			opts: func(o *Options, reg *MemoryAutostartRegistry) {
				o.Profile = ProfileRemote
				o.AutostartRegistry = reg
				o.AutostartExePath = `C:\Program Files\TRACE\trace-server.exe`
				o.AutostartVersion = "0.1.3"
				o.AutostartGOOS = "windows"
			},
			wantSub: "远程",
		},
		{
			name: "nil_registry_adapter",
			opts: func(o *Options, _ *MemoryAutostartRegistry) {
				o.AutostartRegistry = nil
				o.AutostartExePath = `C:\Users\demo\AppData\Local\Programs\trace\trace.exe`
				o.AutostartVersion = "0.1.3"
				o.AutostartGOOS = "windows"
			},
			wantSub: "启动项接口",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg := NewMemoryAutostartRegistry()
			loc := installedChannelLocations(t, "stable")
			if tc.mutateLoc != nil {
				tc.mutateLoc(&loc)
			}
			opts := Options{
				Locations:        loc,
				Desktop:          NewMockDesktop(),
				DisableBgWorkers: true,
			}
			tc.opts(&opts, reg)
			b, err := NewBackend(opts)
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()

			st := b.AutostartStatus()
			if st.Supported {
				t.Fatalf("expected Supported=false, got %+v", st)
			}
			if !strings.Contains(st.Reason, tc.wantSub) {
				t.Fatalf("Reason = %q, want substring %q", st.Reason, tc.wantSub)
			}

			if opts.Profile == ProfileRemote {
				// Remote profile forces Window.AutoStart = false cleanly.
				next := b.GetSettings()
				next.Window.AutoStart = true
				got, err := b.UpdateSettings(next)
				if err != nil {
					t.Fatalf("remote UpdateSettings: %v", err)
				}
				if got.Window.AutoStart {
					t.Fatal("remote profile must keep Window.AutoStart=false")
				}
				if len(reg.Snapshot()) != 0 {
					t.Fatalf("remote profile touched registry: %+v", reg.Snapshot())
				}
				return
			}

			next := b.GetSettings()
			next.Window.AutoStart = true
			if _, err := b.UpdateSettings(next); err == nil {
				t.Fatal("expected error enabling autostart in unsupported environment")
			} else if !site.IsKind(err, site.ErrKindBadRequest) {
				t.Fatalf("expected ErrKindBadRequest, got %v", err)
			}
			if b.GetSettings().Window.AutoStart {
				t.Fatal("settings.Window.AutoStart mutated despite unsupported enable failure")
			}
			if len(reg.Snapshot()) != 0 {
				t.Fatalf("registry mutated despite unsupported enable failure: %+v", reg.Snapshot())
			}
		})
	}

	// Disabling a pre-existing autoStart=true in an unsupported environment still
	// succeeds so settings.json cannot get stuck on true.
	t.Run("disable_preexisting_true_when_unsupported", func(t *testing.T) {
		loc := installedChannelLocations(t, "stable")
		loc.Mode = "portable"
		if err := appdirs.EnsureDirectories(loc); err != nil {
			t.Fatal(err)
		}
		pre := settings.Default(loc.DefaultCacheDir)
		pre.Window.AutoStart = true
		rawPre, err := json.MarshalIndent(pre, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(loc.DataDir, "settings.json"), rawPre, 0o600); err != nil {
			t.Fatal(err)
		}

		b, err := NewBackend(Options{
			Locations:        loc,
			Desktop:          NewMockDesktop(),
			DisableBgWorkers: true,
			AutostartGOOS:    "linux",
		})
		if err != nil {
			t.Fatal(err)
		}
		defer b.Close()

		if !b.GetSettings().Window.AutoStart {
			t.Fatal("expected pre-existing AutoStart=true loaded")
		}
		next := b.GetSettings()
		next.Window.AutoStart = false
		saved, err := b.UpdateSettings(next)
		if err != nil {
			t.Fatalf("disabling pre-existing AutoStart in unsupported mode failed: %v", err)
		}
		if saved.Window.AutoStart {
			t.Fatal("expected AutoStart=false after disable")
		}
	})

	// Regression (P0): disabling a pre-existing autoStart=true in portable or
	// unversioned source (0.0.0-dev) mode on Windows — even if a non-nil registry
	// adapter holding the installed stable Run entry is present — MUST save
	// settings.json as false without ever deleting the installed channel's Run entry.
	for _, sub := range []struct {
		name    string
		mode    string
		version string
	}{
		{"portable_preserves_host_stable_run", "portable", "0.1.3"},
		{"source_unversioned_preserves_host_stable_run", "installed", appdirs.UnversionedBuild},
	} {
		t.Run(sub.name, func(t *testing.T) {
			reg := NewMemoryAutostartRegistry()
			stableVal := appdirs.AutostartValueNameForChannel("stable")
			const stableCmd = `"C:\Users\demo\AppData\Local\Programs\trace\trace.exe" --background`
			if err := reg.Set(stableVal, stableCmd); err != nil {
				t.Fatal(err)
			}

			loc := installedChannelLocations(t, "stable")
			loc.Mode = sub.mode
			if err := appdirs.EnsureDirectories(loc); err != nil {
				t.Fatal(err)
			}
			pre := settings.Default(loc.DefaultCacheDir)
			pre.Window.AutoStart = true
			rawPre, err := json.MarshalIndent(pre, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(loc.DataDir, "settings.json"), rawPre, 0o600); err != nil {
				t.Fatal(err)
			}

			b, err := NewBackend(Options{
				Locations:         loc,
				Desktop:           NewMockDesktop(),
				DisableBgWorkers:  true,
				AutostartRegistry: reg,
				AutostartExePath:  `C:\Temp\trace.exe`,
				AutostartVersion:  sub.version,
				AutostartGOOS:     "windows",
			})
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()

			next := b.GetSettings()
			next.Window.AutoStart = false
			saved, err := b.UpdateSettings(next)
			if err != nil {
				t.Fatalf("disable in %s failed: %v", sub.name, err)
			}
			if saved.Window.AutoStart {
				t.Fatal("expected AutoStart=false persisted")
			}
			if got, ok, _ := reg.Get(stableVal); !ok || got != stableCmd {
				t.Fatalf("unsupported %s mode clobbered host stable Run entry: ok=%v got=%q", sub.name, ok, got)
			}
		})
	}
}

func TestAutostartRegistryFailureAndRollback(t *testing.T) {
	reg := NewMemoryAutostartRegistry()
	loc := installedChannelLocations(t, "stable")
	exePath := `C:\Users\demo\AppData\Local\Programs\trace\trace.exe`
	b, err := NewBackend(Options{
		Locations:         loc,
		Desktop:           NewMockDesktop(),
		DisableBgWorkers:  true,
		AutostartRegistry: reg,
		AutostartExePath:  exePath,
		AutostartVersion:  "0.1.3",
		AutostartGOOS:     "windows",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	// 1. Registry Set failure aborts UpdateSettings without mutating settings.json.
	reg.SetErr = errors.New("registry write blocked")
	next := b.GetSettings()
	next.Window.AutoStart = true
	if _, err := b.UpdateSettings(next); err == nil || !site.IsKind(err, site.ErrKindStorage) {
		t.Fatalf("expected ErrKindStorage on SetErr, got %v", err)
	}
	if b.GetSettings().Window.AutoStart {
		t.Fatal("AutoStart should stay false after registry Set failure")
	}

	// 2. Enable succeeds once SetErr cleared; Delete failure keeps AutoStart=true.
	reg.SetErr = nil
	if _, err := b.UpdateSettings(next); err != nil {
		t.Fatalf("enable after clearing SetErr: %v", err)
	}
	reg.DelErr = errors.New("registry delete blocked")
	next = b.GetSettings()
	next.Window.AutoStart = false
	if _, err := b.UpdateSettings(next); err == nil || !site.IsKind(err, site.ErrKindStorage) {
		t.Fatalf("expected ErrKindStorage on DelErr, got %v", err)
	}
	if !b.GetSettings().Window.AutoStart {
		t.Fatal("AutoStart should stay true after registry Delete failure")
	}
	reg.DelErr = nil

	// 3. Settings persistence failure rolls back registry deletion.
	settingsPath := filepath.Join(loc.DataDir, "settings.json")
	if err := os.Remove(settingsPath); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(settingsPath, "blocker"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := b.UpdateSettings(next); err == nil {
		t.Fatal("expected UpdateSettings error when settings.json path is blocked by a directory")
	}
	valName := appdirs.AutostartValueNameForChannel("stable")
	if cmd, ok, _ := reg.Get(valName); !ok || cmd != `"`+exePath+`" --background` {
		t.Fatalf("expected registry rollback to restore %q, got ok=%v cmd=%q", valName, ok, cmd)
	}
}

func TestAutostartDisableHandlesRegistryGetError(t *testing.T) {
	reg := NewMemoryAutostartRegistry()
	loc := installedChannelLocations(t, "stable")
	exePath := `C:\Users\demo\AppData\Local\Programs\trace\trace.exe`
	valName := appdirs.AutostartValueNameForChannel("stable")

	b, err := NewBackend(Options{
		Locations:         loc,
		Desktop:           NewMockDesktop(),
		DisableBgWorkers:  true,
		AutostartRegistry: reg,
		AutostartExePath:  exePath,
		AutostartVersion:  "0.1.3",
		AutostartGOOS:     "windows",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	// Enable cleanly first.
	next := b.GetSettings()
	next.Window.AutoStart = true
	if _, err := b.UpdateSettings(next); err != nil {
		t.Fatal(err)
	}

	// Case 1: GetErr != nil AND DelErr != nil (e.g. access denied) -> must NOT
	// silently skip Delete or mark AutoStart=false.
	reg.GetErr = errors.New("access denied on read")
	reg.DelErr = errors.New("access denied on delete")
	next = b.GetSettings()
	next.Window.AutoStart = false
	if _, err := b.UpdateSettings(next); err == nil || !site.IsKind(err, site.ErrKindStorage) {
		t.Fatalf("expected ErrKindStorage when Get and Delete both fail, got %v", err)
	}
	if !b.GetSettings().Window.AutoStart {
		t.Fatal("AutoStart should stay true when Delete fails after GetErr")
	}
	if snap := reg.Snapshot(); snap[valName] == "" {
		t.Fatal("registry entry unexpectedly missing")
	}

	// Case 2: GetErr != nil (e.g. ErrUnexpectedType) AND DelErr == nil -> must
	// still call Delete to clean up the residual value and persist AutoStart=false.
	reg.DelErr = nil
	saved, err := b.UpdateSettings(next)
	if err != nil {
		t.Fatalf("expected Delete to clean up residual value despite GetErr: %v", err)
	}
	if saved.Window.AutoStart {
		t.Fatal("expected AutoStart=false after Delete succeeded")
	}
	if snap := reg.Snapshot(); snap[valName] != "" {
		t.Fatalf("residual registry entry was not deleted when GetErr occurred: %+v", snap)
	}
}

func TestReconcileAutostartUpdatesMovedPath(t *testing.T) {
	reg := NewMemoryAutostartRegistry()
	loc := installedChannelLocations(t, "dev")
	devVal := appdirs.AutostartValueNameForChannel("dev")
	stableVal := appdirs.AutostartValueNameForChannel("stable")
	oldExe := `C:\OldDir\trace-dev\trace.exe`
	newExe := `C:\NewDir\trace-dev\trace.exe`

	if err := reg.Set(stableVal, `"C:\Apps\trace\trace.exe" --background`); err != nil {
		t.Fatal(err)
	}
	if err := reg.Set(devVal, `"`+oldExe+`" --background`); err != nil {
		t.Fatal(err)
	}

	b, err := NewBackend(Options{
		Locations:         loc,
		Desktop:           NewMockDesktop(),
		DisableBgWorkers:  true,
		AutostartRegistry: reg,
		AutostartExePath:  newExe,
		AutostartVersion:  "0.1.3-dev.202611280200",
		AutostartGOOS:     "windows",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	// Disabled in settings: ReconcileAutostart is a no-op and does not overwrite.
	if wrote, err := b.ReconcileAutostart(); err != nil || wrote {
		t.Fatalf("ReconcileAutostart while disabled = (%v, %v), want (false, nil)", wrote, err)
	}

	// Enable in settings (which writes newExe), then overwrite registry with oldExe
	// to simulate relocating or reinstalling the binary.
	next := b.GetSettings()
	next.Window.AutoStart = true
	if _, err := b.UpdateSettings(next); err != nil {
		t.Fatal(err)
	}
	if err := reg.Set(devVal, `"`+oldExe+`" --background`); err != nil {
		t.Fatal(err)
	}

	wrote, err := b.ReconcileAutostart()
	if err != nil || !wrote {
		t.Fatalf("ReconcileAutostart after move = (%v, %v), want (true, nil)", wrote, err)
	}
	wantCmd := `"` + newExe + `" --background`
	if got, ok, _ := reg.Get(devVal); !ok || got != wantCmd {
		t.Fatalf("reconciled dev command = %q (ok=%v), want %q", got, ok, wantCmd)
	}
	if got, ok, _ := reg.Get(stableVal); !ok || got != `"C:\Apps\trace\trace.exe" --background` {
		t.Fatalf("reconcile touched stable value: ok=%v got=%q", ok, got)
	}

	// Second reconcile when already matching is a no-op.
	if wrote, err := b.ReconcileAutostart(); err != nil || wrote {
		t.Fatalf("second ReconcileAutostart = (%v, %v), want (false, nil)", wrote, err)
	}
}
