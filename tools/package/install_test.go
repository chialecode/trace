package main

import (
	"errors"
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
	launchInJob bool
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

func (m *fakeMachine) Launch(exe string, args []string) (bool, error) {
	m.launches = append(m.launches, append([]string{exe}, args...))
	return m.launchInJob, nil
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

func (m *fakeMachine) installer() *devInstaller {
	return &devInstaller{
		Setup: `D:\repo\dist\trace-dev\trace-dev-setup-0.1.1-dev.202611090800-amd64.exe`, Version: "0.1.1-dev.202611090800",
		Registry: m, Procs: m, RunSetup: m.runSetup,
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
	res, err := m.installer().Run()
	if err != nil || !res.Skipped || res.Installed || len(m.setups) != 0 || !strings.Contains(res.Reason, "没有安装") {
		t.Fatalf("%+v %v", res, err)
	}
	stableUntouched(t, m)
}

// The user keeps TRACE Dev running: after every install it is started in
// the tray, whether it was running before or not (2026-11-15).
func TestInstallDevWhenNotRunningStartsInTray(t *testing.T) {
	m := newMachine().withDev("0.1.1-dev.202611080700", false)
	res, err := m.installer().Run()
	if err != nil {
		t.Fatal(err)
	}
	if !res.Installed || res.WasRunning || !res.Relaunched || !res.Background || len(m.quitCalls) != 0 {
		t.Fatalf("%+v quit=%v", res, m.quitCalls)
	}
	want := filepath.Join(devLoc, devExeName) + " " + flagBackground
	if len(m.launches) != 1 || strings.Join(m.launches[0], " ") != want {
		t.Fatalf("launch %v", m.launches)
	}
	if res.OldVersion != "0.1.1-dev.202611080700" || res.NewVersion != "0.1.1-dev.202611090800" || len(m.setups) != 1 {
		t.Fatalf("%+v", res)
	}
	stableUntouched(t, m)
}

func TestInstallDevQuitsInstallsAndReopensInTray(t *testing.T) {
	m := newMachine().withDev("0.1.1-dev.202611080700", true)
	m.quitAfter = 3 * time.Second
	res, err := m.installer().Run()
	if err != nil {
		t.Fatal(err)
	}
	if !res.Installed || !res.WasRunning || !res.Relaunched || !res.Background || res.InJob {
		t.Fatalf("%+v", res)
	}
	if len(m.quitCalls) != 1 || m.quitCalls[0] != filepath.Join(devLoc, devExeName) {
		t.Fatalf("quit %v", m.quitCalls)
	}
	want := filepath.Join(devLoc, devExeName) + " " + flagBackground
	if len(m.launches) != 1 || strings.Join(m.launches[0], " ") != want {
		t.Fatalf("launch %v", m.launches)
	}
	if res.QuitMs < 3000 || res.QuitMs > 4000 {
		t.Fatalf("quit took %d ms", res.QuitMs)
	}
	stableUntouched(t, m)
}

// A command sandbox that keeps its processes in a job object it closes may
// end the started TRACE Dev; the result says so instead of claiming it runs.
func TestInstallDevReportsLaunchInsideJob(t *testing.T) {
	m := newMachine().withDev("0.1.1-dev.202611080700", false)
	m.launchInJob = true
	res, err := m.installer().Run()
	if err != nil || !res.Relaunched || !res.InJob {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestInstallDevGivesUpWhenTraceDevDoesNotQuit(t *testing.T) {
	m := newMachine().withDev("0.1.1-dev.202611080700", true)
	m.quitAfter = -1
	start := m.now
	res, err := m.installer().Run()
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
	if res, err := m.installer().Run(); err == nil || res.Installed || !strings.Contains(res.Error, "静默安装失败") {
		t.Fatalf("failed installer: %+v %v", res, err)
	}

	m = newMachine().withDev("0.1.1-dev.202611080700", true)
	m.quitAfter = 0
	m.installsVer = "0.1.1-dev.202611080700" // the installer left the old version
	res, err := m.installer().Run()
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
