package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const stableIdentifier = "io.github.chialecode.trace"

// fakeMachine is a machine with uninstall entries, processes that quit
// when asked (or not), and an installer that writes the uninstall entry.
type fakeMachine struct {
	entries     map[string]installEntry
	running     map[string]bool
	quitAfter   time.Duration // how long a running TRACE Dev takes to quit; <0 never
	quitAsked   time.Time
	now         time.Time
	quitCalls   []string
	launches    [][]string
	setups      []string
	setupErr    error
	installsVer string // DisplayVersion the installer writes
	lookups     []string
}

func (m *fakeMachine) Lookup(id string) (installEntry, bool, error) {
	m.lookups = append(m.lookups, id)
	e, ok := m.entries[id]
	return e, ok, nil
}

func (m *fakeMachine) Running(exe string) (bool, error) {
	if !m.running[exe] {
		return false, nil
	}
	if !m.quitAsked.IsZero() && m.quitAfter >= 0 && m.now.Sub(m.quitAsked) >= m.quitAfter {
		m.running[exe] = false
		return false, nil
	}
	return true, nil
}

func (m *fakeMachine) RequestQuit(exe string) error {
	m.quitCalls = append(m.quitCalls, exe)
	m.quitAsked = m.now
	return nil
}

func (m *fakeMachine) Launch(exe string, args []string) error {
	m.launches = append(m.launches, append([]string{exe}, args...))
	return nil
}

func (m *fakeMachine) runSetup(setup string) error {
	m.setups = append(m.setups, setup)
	if m.setupErr != nil {
		return m.setupErr
	}
	e := m.entries[devIdentifier]
	e.Version = m.installsVer
	m.entries[devIdentifier] = e
	return nil
}

func newMachine() *fakeMachine {
	return &fakeMachine{
		entries: map[string]installEntry{
			stableIdentifier: {Location: `C:\Users\u\AppData\Local\Programs\trace`, Version: "0.1.1"},
		},
		running:     map[string]bool{filepath.Join(`C:\Users\u\AppData\Local\Programs\trace`, devExeName): true},
		now:         time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC),
		installsVer: "0.1.1-dev.202611090800",
	}
}

const devLoc = `C:\Users\u\AppData\Local\Programs\trace-dev`

func (m *fakeMachine) withDev(version string, running bool) *fakeMachine {
	m.entries[devIdentifier] = installEntry{Location: devLoc, Version: version}
	m.running[filepath.Join(devLoc, devExeName)] = running
	return m
}

func (m *fakeMachine) installer(window func() (bool, bool)) *devInstaller {
	return &devInstaller{
		Setup: `D:\repo\dist\trace-dev\trace-dev-setup-0.1.1-dev.202611090800-amd64.exe`, Version: "0.1.1-dev.202611090800",
		Registry: m, Procs: m, RunSetup: m.runSetup, WindowState: window,
		Now: func() time.Time { return m.now }, Sleep: func(d time.Duration) { m.now = m.now.Add(d) },
		Timeout: quitTimeout,
	}
}

// stableUntouched: the release identity is never asked to quit, launched
// or reinstalled, and its entry stays as it was.
func stableUntouched(t *testing.T, m *fakeMachine) {
	t.Helper()
	stableExe := filepath.Join(`C:\Users\u\AppData\Local\Programs\trace`, devExeName)
	for _, c := range m.quitCalls {
		if c == stableExe {
			t.Fatal("asked the release TRACE to quit")
		}
	}
	for _, l := range m.launches {
		if l[0] == stableExe {
			t.Fatal("launched the release TRACE")
		}
	}
	for _, id := range m.lookups {
		if id != devIdentifier {
			t.Fatalf("looked up %s", id)
		}
	}
	if e := m.entries[stableIdentifier]; e.Version != "0.1.1" || !m.running[stableExe] {
		t.Fatal("the release TRACE changed")
	}
}

func TestInstallDevSkipsWhenNotInstalled(t *testing.T) {
	m := newMachine()
	res, err := m.installer(nil).Run()
	if err != nil || !res.Skipped || res.Installed || len(m.setups) != 0 || !strings.Contains(res.Reason, "没有安装") {
		t.Fatalf("%+v %v", res, err)
	}
	stableUntouched(t, m)
}

func TestInstallDevWhenNotRunning(t *testing.T) {
	m := newMachine().withDev("0.1.1-dev.202611080700", false)
	res, err := m.installer(nil).Run()
	if err != nil {
		t.Fatal(err)
	}
	if !res.Installed || res.WasRunning || res.Relaunched || len(m.quitCalls) != 0 || len(m.launches) != 0 {
		t.Fatalf("%+v quit=%v launch=%v", res, m.quitCalls, m.launches)
	}
	if res.OldVersion != "0.1.1-dev.202611080700" || res.NewVersion != "0.1.1-dev.202611090800" || len(m.setups) != 1 {
		t.Fatalf("%+v", res)
	}
	stableUntouched(t, m)
}

func TestInstallDevQuitsInstallsAndReopensAsItWas(t *testing.T) {
	for _, tc := range []struct {
		name       string
		window     func() (bool, bool)
		background bool
	}{
		{"window open", func() (bool, bool) { return true, true }, false},
		{"in the tray", func() (bool, bool) { return false, true }, true},
		{"unknown: TRACE decides from its own record", func() (bool, bool) { return false, false }, false},
	} {
		m := newMachine().withDev("0.1.1-dev.202611080700", true)
		m.quitAfter = 3 * time.Second
		res, err := m.installer(tc.window).Run()
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if !res.Installed || !res.WasRunning || !res.Relaunched || res.Background != tc.background {
			t.Fatalf("%s: %+v", tc.name, res)
		}
		if len(m.quitCalls) != 1 || m.quitCalls[0] != filepath.Join(devLoc, devExeName) {
			t.Fatalf("%s: quit %v", tc.name, m.quitCalls)
		}
		want := []string{filepath.Join(devLoc, devExeName)}
		if tc.background {
			want = append(want, flagBackground)
		}
		if len(m.launches) != 1 || strings.Join(m.launches[0], " ") != strings.Join(want, " ") {
			t.Fatalf("%s: launch %v", tc.name, m.launches)
		}
		if res.QuitMs < 3000 || res.QuitMs > 4000 {
			t.Fatalf("%s: quit took %d ms", tc.name, res.QuitMs)
		}
		stableUntouched(t, m)
	}
}

func TestInstallDevGivesUpWhenTraceDevDoesNotQuit(t *testing.T) {
	m := newMachine().withDev("0.1.1-dev.202611080700", true)
	m.quitAfter = -1
	start := m.now
	res, err := m.installer(nil).Run()
	if err == nil || !strings.Contains(err.Error(), "30 秒内没有退出") || !strings.Contains(err.Error(), "没有强制结束") {
		t.Fatalf("error: %v", err)
	}
	if res.Installed || len(m.setups) != 0 || len(m.launches) != 0 {
		t.Fatalf("installed anyway: %+v", res)
	}
	if waited := m.now.Sub(start); waited < quitTimeout || waited > quitTimeout+time.Second {
		t.Fatalf("waited %s", waited)
	}
	if !m.running[filepath.Join(devLoc, devExeName)] {
		t.Fatal("TRACE Dev was stopped")
	}
	stableUntouched(t, m)
}

func TestInstallDevFailures(t *testing.T) {
	m := newMachine().withDev("0.1.1-dev.202611080700", false)
	m.setupErr = errors.New("exit status 2")
	if res, err := m.installer(nil).Run(); err == nil || res.Installed || !strings.Contains(res.Error, "静默安装失败") {
		t.Fatalf("failed installer: %+v %v", res, err)
	}

	m = newMachine().withDev("0.1.1-dev.202611080700", true)
	m.quitAfter = 0
	m.installsVer = "0.1.1-dev.202611080700" // the installer left the old version
	res, err := m.installer(nil).Run()
	if err == nil || res.Installed || !strings.Contains(err.Error(), "不是 0.1.1-dev.202611090800") {
		t.Fatalf("version check: %+v %v", res, err)
	}
	if len(m.launches) != 0 {
		t.Fatal("reopened after a failed install")
	}
	stableUntouched(t, m)
}

func TestInstallDevSkipsOnRequestAndOnCI(t *testing.T) {
	env := func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }
	if r := skipReason(env(map[string]string{installDevEnv: "0"})); !strings.Contains(r, installDevEnv) {
		t.Fatalf("TRACE_DEV_AUTO_INSTALL=0: %q", r)
	}
	if r := skipReason(env(map[string]string{"CI": "true"})); r == "" {
		t.Fatal("CI=true not skipped")
	}
	if r := skipReason(env(map[string]string{installDevEnv: "1", "CI": ""})); r != "" {
		t.Fatalf("skipped: %q", r)
	}
}

func TestRestartWindowStateReadsRecentRecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ui-state.json")
	now := time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC)
	write := func(s string) {
		if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, known := restartWindowState(path, now); known {
		t.Fatal("missing file known")
	}
	write(`{"update":{"restart":{"reason":"dev-install","windowOpen":false,"at":"2026-11-09T07:59:40Z"}}}`)
	if open, known := restartWindowState(path, now); !known || open {
		t.Fatalf("tray record: %v %v", open, known)
	}
	write(`{"update":{"restart":{"reason":"dev-install","windowOpen":true,"at":"2026-11-09T07:59:40Z"}}}`)
	if open, known := restartWindowState(path, now); !known || !open {
		t.Fatalf("window record: %v %v", open, known)
	}
	write(`{"update":{"restart":{"reason":"dev-install","windowOpen":false,"at":"2026-11-09T06:00:00Z"}}}`)
	if _, known := restartWindowState(path, now); known {
		t.Fatal("an old record decides")
	}
}
