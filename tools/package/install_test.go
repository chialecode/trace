package main

import (
	"encoding/json"
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
	launchInJob bool
	setups      []string
	setupErr    error
	installsVer string // DisplayVersion the installer writes
	lookups     []string
	// windowUntil: TRACE Dev's window stays open until then (zero: no
	// window, the tray only); windowNever: it never closes.
	windowUntil time.Time
	windowNever bool
	windowCalls int
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

func (m *fakeMachine) WindowOpen(exe string) (bool, error) {
	m.windowCalls++
	if !m.running[exe] {
		return false, nil
	}
	return m.windowNever || m.now.Before(m.windowUntil), nil
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
	if e.Location == "" {
		e.Location = devLoc // a first install goes to the default folder
	}
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
		Timeout: quitTimeout, WindowWait: windowWait, WindowPoll: windowPoll,
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

// A machine without TRACE Dev gets its first install the same way: no quit,
// installed into the default folder, then started in the tray.
func TestInstallDevInstallsTheFirstTimeToo(t *testing.T) {
	m := newMachine()
	res, err := m.installer().Run()
	if err != nil {
		t.Fatal(err)
	}
	if !res.Installed || res.Skipped || !res.FirstInstall || res.OldVersion != "" || res.WasRunning || len(m.quitCalls) != 0 || len(m.setups) != 1 {
		t.Fatalf("%+v quit=%v setups=%v", res, m.quitCalls, m.setups)
	}
	want := filepath.Join(devLoc, devExeName) + " " + flagBackground
	if len(m.launches) != 1 || strings.Join(m.launches[0], " ") != want || !res.Relaunched || !res.Background {
		t.Fatalf("launch %v %+v", m.launches, res)
	}
	if res.Identity != "trace-dev" || res.Location != devLoc || res.NewVersion != "0.1.1-dev.202611090800" {
		t.Fatalf("%+v", res)
	}
	stableUntouched(t, m)
}

// The window open means the user is using TRACE Dev: wait (every 2 s) until
// it is hidden to the tray, then ask it to quit and install.
func TestInstallDevWaitsWhileTheWindowIsOpen(t *testing.T) {
	m := newMachine().withDev("0.1.1-dev.202611080700", true)
	m.windowUntil = m.now.Add(9 * time.Second)
	m.quitAfter = time.Second
	res, err := m.installer().Run()
	if err != nil {
		t.Fatal(err)
	}
	if !res.Installed || !res.WindowOpen || !res.Deferred || !res.WasRunning || len(m.quitCalls) != 1 {
		t.Fatalf("%+v quit=%v", res, m.quitCalls)
	}
	if res.DeferMs != 10000 { // five 2-second polls
		t.Fatalf("deferred %d ms", res.DeferMs)
	}
	stableUntouched(t, m)
}

// Closing TRACE Dev while the install waits for its window: no quit request.
func TestInstallDevInstallsWhenTraceDevExitsDuringTheWait(t *testing.T) {
	m := newMachine().withDev("0.1.1-dev.202611080700", true)
	m.windowNever = true
	exe := filepath.Join(devLoc, devExeName)
	sleeps := 0
	d := m.installer()
	d.Sleep = func(step time.Duration) {
		m.now = m.now.Add(step)
		if sleeps++; sleeps == 3 {
			m.running[exe] = false
		}
	}
	res, err := d.Run()
	if err != nil || !res.Installed || !res.Deferred || len(m.quitCalls) != 0 || res.QuitMs != 0 {
		t.Fatalf("%+v %v quit=%v", res, err, m.quitCalls)
	}
	stableUntouched(t, m)
}

// A window still open after 10 minutes skips this install: not a failure,
// nothing is asked to quit and nothing is installed.
func TestInstallDevSkipsWhenTheWindowStaysOpen(t *testing.T) {
	m := newMachine().withDev("0.1.1-dev.202611080700", true)
	m.windowNever = true
	start := m.now
	res, err := m.installer().Run()
	if err != nil {
		t.Fatal(err)
	}
	if !res.Skipped || res.Installed || res.Reason != "window_open" || !res.WindowOpen || !res.Deferred {
		t.Fatalf("%+v", res)
	}
	if !strings.Contains(string(res.Guidance), installOnlyCommand) {
		t.Fatalf("guidance %q", res.Guidance)
	}
	if waited := m.now.Sub(start); waited != windowWait {
		t.Fatalf("waited %s", waited)
	}
	if m.windowCalls != 301 || len(m.quitCalls) != 0 || len(m.setups) != 0 || len(m.launches) != 0 || !m.running[filepath.Join(devLoc, devExeName)] {
		t.Fatalf("window checks %d quit=%v setups=%v launches=%v", m.windowCalls, m.quitCalls, m.setups, m.launches)
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
	if !res.Installed || res.FirstInstall || res.WasRunning || res.Deferred || !res.Relaunched || !res.Background || len(m.quitCalls) != 0 {
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
	if !res.Installed || !res.WasRunning || res.WindowOpen || res.Deferred || !res.Relaunched || !res.Background || res.InJob {
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
	if err == nil || !strings.Contains(err.Error(), "30 秒内没有退出") || !strings.Contains(err.Error(), "没有强制结束") || res.Reason != "quit_timeout" {
		t.Fatalf("error: %v (%s)", err, res.Reason)
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
	if res, err := m.installer().Run(); err == nil || res.Installed || !strings.Contains(string(res.Error), "静默安装失败") || res.Reason != "installer_failed" {
		t.Fatalf("failed installer: %+v %v", res, err)
	}

	m = newMachine().withDev("0.1.1-dev.202611080700", true)
	m.quitAfter = 0
	m.installsVer = "0.1.1-dev.202611080700" // the installer left the old version
	res, err := m.installer().Run()
	if err == nil || res.Installed || !strings.Contains(err.Error(), "不是 0.1.1-dev.202611090800") || res.Reason != "version_mismatch" {
		t.Fatalf("version check: %+v %v", res, err)
	}
	if len(m.launches) != 0 {
		t.Fatal("reopened after a failed install")
	}
	stableUntouched(t, m)
}

func TestInstallDevSkipsOnRequestAndOnCI(t *testing.T) {
	env := func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }
	if r, g := skipReason(env(map[string]string{installDevEnv: "0"})); r != "disabled" || !strings.Contains(g, installDevEnv) {
		t.Fatalf("TRACE_DEV_AUTO_INSTALL=0: %q %q", r, g)
	}
	if r, _ := skipReason(env(map[string]string{"CI": "true"})); r != "ci" {
		t.Fatalf("CI=true: %q", r)
	}
	if r, _ := skipReason(env(map[string]string{installDevEnv: "1", "CI": ""})); r != "" {
		t.Fatalf("skipped: %q", r)
	}
}

// install-dev.json uses the camelCase fields shared by the author's desktop apps,
// serializing absent optional text fields as JSON null rather than "".
func TestInstallResultFields(t *testing.T) {
	allKeys := []string{
		"identity", "installed", "skipped", "reason", "error", "guidance",
		"firstInstall", "oldVersion", "newVersion", "installLocation",
		"wasRunning", "windowOpen", "deferred", "deferMs", "quitMs",
		"relaunched", "background", "inJob", "elapsedMs", "at",
	}
	readReceiptMap := func(t *testing.T, res installResult) (map[string]any, []byte) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "install-dev.json")
		if err := writeInstallResult(path, res); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		for _, key := range allKeys {
			if _, ok := got[key]; !ok {
				t.Errorf("install-dev.json has no %q: %s", key, raw)
			}
		}
		return got, raw
	}
	assertNull := func(t *testing.T, got map[string]any, raw []byte, keys ...string) {
		t.Helper()
		for _, key := range keys {
			if got[key] != nil {
				t.Errorf("%q = %#v, want null: %s", key, got[key], raw)
			}
		}
	}
	assertNonEmptyString := func(t *testing.T, got map[string]any, raw []byte, keys ...string) {
		t.Helper()
		for _, key := range keys {
			s, ok := got[key].(string)
			if !ok || strings.TrimSpace(s) == "" {
				t.Errorf("%q = %#v, want non-empty string: %s", key, got[key], raw)
			}
		}
	}

	t.Run("upgrade_success", func(t *testing.T) {
		m := newMachine().withDev("0.1.1-dev.202611080700", true)
		m.windowUntil = m.now.Add(time.Second)
		res, err := m.installer().Run()
		if err != nil {
			t.Fatal(err)
		}
		got, raw := readReceiptMap(t, res)
		assertNull(t, got, raw, "reason", "error", "guidance")
		assertNonEmptyString(t, got, raw, "identity", "oldVersion", "newVersion", "installLocation", "at")

		var decoded installResult
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("round-trip unmarshal failed: %v", err)
		}
		if decoded != res {
			t.Fatalf("round-trip mismatch:\n got: %+v\nwant: %+v", decoded, res)
		}
	})

	t.Run("first_install_oldVersion_null", func(t *testing.T) {
		m := newMachine()
		res, err := m.installer().Run()
		if err != nil {
			t.Fatal(err)
		}
		got, raw := readReceiptMap(t, res)
		assertNull(t, got, raw, "reason", "error", "guidance", "oldVersion")
		assertNonEmptyString(t, got, raw, "newVersion", "installLocation")
	})

	t.Run("window_open_skip", func(t *testing.T) {
		m := newMachine().withDev("0.1.1-dev.202611080700", true)
		m.windowNever = true
		res, err := m.installer().Run()
		if err != nil {
			t.Fatal(err)
		}
		got, raw := readReceiptMap(t, res)
		assertNull(t, got, raw, "error")
		assertNonEmptyString(t, got, raw, "reason", "guidance", "oldVersion", "newVersion", "installLocation")
		if got["reason"] != "window_open" {
			t.Fatalf("reason = %#v, want window_open", got["reason"])
		}
	})

	t.Run("early_skip_and_invalid_package", func(t *testing.T) {
		at := "2026-11-09T08:00:00Z"
		disabled := installResult{
			Identity: devIdentity,
			Skipped:  true,
			Reason:   "disabled",
			Guidance: installDevEnv + "=0：跳过本机开发版自动安装",
			At:       at,
		}
		got, raw := readReceiptMap(t, disabled)
		assertNull(t, got, raw, "error", "oldVersion", "newVersion", "installLocation")
		assertNonEmptyString(t, got, raw, "reason", "guidance")

		invalid := installResult{
			Identity: devIdentity,
			Reason:   "package_invalid",
			Error:    "missing VERSION.txt",
			At:       at,
		}
		got, raw = readReceiptMap(t, invalid)
		assertNull(t, got, raw, "guidance", "oldVersion", "newVersion", "installLocation")
		assertNonEmptyString(t, got, raw, "reason", "error")
	})

	t.Run("installer_failure_guidance_null", func(t *testing.T) {
		m := newMachine().withDev("0.1.1-dev.202611080700", false)
		m.setupErr = errors.New("exit status 2")
		res, err := m.installer().Run()
		if err == nil {
			t.Fatal("expected failure")
		}
		got, raw := readReceiptMap(t, res)
		assertNull(t, got, raw, "guidance")
		assertNonEmptyString(t, got, raw, "reason", "error", "oldVersion", "newVersion", "installLocation")
	})

	t.Run("nullableString_edge_cases", func(t *testing.T) {
		b, err := json.Marshal(nullableString("   "))
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != "null" {
			t.Fatalf("whitespace nullableString marshaled as %s, want null", b)
		}
		var s nullableString = "stale"
		if err := json.Unmarshal([]byte("null"), &s); err != nil {
			t.Fatal(err)
		}
		if s != "" {
			t.Fatalf("unmarshal null produced %q, want empty", s)
		}
		if err := json.Unmarshal([]byte("123"), &s); err == nil {
			t.Fatal("expected error unmarshaling number into nullableString")
		}
	})
}
