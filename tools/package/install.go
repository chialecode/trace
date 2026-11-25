package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The development build installs itself on this machine after packaging:
// it finds TRACE Dev in the per-user uninstall entries (and installs it the
// first time too), waits while its window is open (the user is using it),
// asks a TRACE Dev running in the tray to save its work and quit, installs
// the new package silently, checks the installed version and then starts
// TRACE Dev in the tray (--background), whether it was running or not: the
// user keeps the development build running (2026-11-15). It never touches
// the release or trace-test identities.

const (
	devIdentity        = "trace-dev"
	devIdentifier      = "io.github.chialecode.trace-dev"
	devDisplayName     = "TRACE Dev"
	devExeName         = "trace.exe"
	quitTimeout        = 30 * time.Second
	quitRequestTimeout = 15 * time.Second
	quitPoll           = 250 * time.Millisecond
	windowWait         = 10 * time.Minute
	windowPoll         = 2 * time.Second
	flagQuitUpdate     = "--quit-for-update"
	flagBackground     = "--background"
	installDevEnv      = "TRACE_DEV_AUTO_INSTALL"
	installResultTo    = "artifacts/package/install-dev.json"
	installOnlyCommand = "go run ./tools/package -install-only"
)

// installEntry is what the uninstall entry of an identity says.
type installEntry struct {
	Location string
	Version  string
}

// uninstallReader reads HKCU\Software\Microsoft\Windows\CurrentVersion\Uninstall.
type uninstallReader interface {
	Lookup(identifier string) (installEntry, bool, error)
}

// processes finds, asks to quit and launches TRACE Dev by its executable.
// There is deliberately no way to kill a process. WindowOpen reports whether
// a window of it can be seen (minimized counts, hidden to the tray does
// not). Launch reports whether the new process stayed inside this tool's job
// object, where it may end when the command that ran the tool ends.
type processes interface {
	Running(exe string) (bool, error)
	WindowOpen(exe string) (bool, error)
	RequestQuit(exe string) error
	Launch(exe string, args []string) (inJob bool, err error)
}

// devInstaller holds what an install depends on, so tests can replace the
// registry, the processes, the installer and the clock.
type devInstaller struct {
	Setup      string
	Version    string
	Registry   uninstallReader
	Procs      processes
	RunSetup   func(setup string) error
	Now        func() time.Time
	Sleep      func(time.Duration)
	Timeout    time.Duration // waiting for TRACE Dev to quit
	WindowWait time.Duration // waiting for its window to close
	WindowPoll time.Duration
	Logf       func(format string, args ...any)
}

// installResult is artifacts/package/install-dev.json, in the camelCase
// fields every desktop app of this author writes for its development install.
type installResult struct {
	Identity     string `json:"identity"`
	Installed    bool   `json:"installed"`
	Skipped      bool   `json:"skipped"`
	Reason       string `json:"reason"`
	Error        string `json:"error"`
	Guidance     string `json:"guidance"`
	FirstInstall bool   `json:"firstInstall"`
	OldVersion   string `json:"oldVersion"`
	NewVersion   string `json:"newVersion"`
	Location     string `json:"installLocation"`
	WasRunning   bool   `json:"wasRunning"`
	WindowOpen   bool   `json:"windowOpen"`
	Deferred     bool   `json:"deferred"`
	DeferMs      int64  `json:"deferMs"`
	QuitMs       int64  `json:"quitMs"`
	Relaunched   bool   `json:"relaunched"`
	Background   bool   `json:"background"`
	// InJob: the started TRACE Dev could not leave the job object of the
	// command that ran this tool and may end with it.
	InJob     bool   `json:"inJob"`
	ElapsedMs int64  `json:"elapsedMs"`
	At        string `json:"at"`
}

// installFailure is a failed install: Reason is the receipt's reason code.
type installFailure struct {
	Reason string
	Err    error
}

func (f *installFailure) Error() string { return f.Err.Error() }
func (f *installFailure) Unwrap() error { return f.Err }

func failure(reason string, format string, args ...any) error {
	return &installFailure{Reason: reason, Err: fmt.Errorf(format, args...)}
}

// skipReason tells why the automatic install does not run: a reason code
// and what to tell the user, or "".
func skipReason(getenv func(string) string) (reason, guidance string) {
	if strings.TrimSpace(getenv(installDevEnv)) == "0" {
		return "disabled", installDevEnv + "=0：跳过本机开发版自动安装"
	}
	if strings.EqualFold(strings.TrimSpace(getenv("CI")), "true") {
		return "ci", "CI 上不安装开发版"
	}
	return "", ""
}

func (d *devInstaller) logf(format string, args ...any) {
	if d.Logf != nil {
		d.Logf(format, args...)
	}
}

// Run installs the development package. A skip is not a failure; a failed
// install returns the error with the result.
func (d *devInstaller) Run() (installResult, error) {
	start := d.Now()
	res := installResult{Identity: devIdentity, NewVersion: d.Version, At: start.UTC().Format(time.RFC3339)}
	finish := func(err error) (installResult, error) {
		res.ElapsedMs = d.Now().Sub(start).Milliseconds()
		if err != nil {
			res.Error = err.Error()
			var f *installFailure
			if errors.As(err, &f) {
				res.Reason = f.Reason
			}
		}
		return res, err
	}

	entry, ok, err := d.Registry.Lookup(devIdentifier)
	if err != nil {
		return finish(failure("entry_read_failed", "读取 %s 的卸载项：%w", devDisplayName, err))
	}
	res.FirstInstall = !ok || strings.TrimSpace(entry.Location) == ""
	if !res.FirstInstall {
		res.OldVersion, res.Location = entry.Version, entry.Location
		if err := d.closeInstalled(&res, filepath.Join(entry.Location, devExeName)); err != nil || res.Skipped {
			return finish(err)
		}
	}

	if res.FirstInstall {
		d.logf("==> [install-dev] 本机没有 %s，静默完成首次安装 %s…", devDisplayName, d.Version)
	} else {
		d.logf("==> [install-dev] 静默安装 %s %s…", devDisplayName, d.Version)
	}
	if err := d.RunSetup(d.Setup); err != nil {
		return finish(failure("installer_failed", "静默安装失败：%w", err))
	}
	after, ok, err := d.Registry.Lookup(devIdentifier)
	if err != nil || !ok || strings.TrimSpace(after.Location) == "" {
		return finish(failure("entry_missing", "安装后读不到 %s 的卸载项：%v", devDisplayName, err))
	}
	if after.Version != d.Version {
		return finish(failure("version_mismatch", "安装后的版本是 %q，不是 %s", after.Version, d.Version))
	}
	res.Installed, res.Location = true, after.Location

	inJob, err := d.Procs.Launch(filepath.Join(after.Location, devExeName), []string{flagBackground})
	if err != nil {
		return finish(failure("launch_failed", "已安装 %s，在托盘打开失败：%w", d.Version, err))
	}
	res.Relaunched, res.Background, res.InJob = true, true, inJob
	return finish(nil)
}

// closeInstalled gets the installed TRACE Dev out of the way: it waits while
// its window is open (skipping the install when that lasts), then asks it to
// save and quit. It never ends it by force.
func (d *devInstaller) closeInstalled(res *installResult, exe string) error {
	running, err := d.Procs.Running(exe)
	if err != nil {
		return failure("process_check_failed", "检查 %s 是否在运行：%w", devDisplayName, err)
	}
	res.WasRunning = running
	if !running {
		return nil
	}

	open, err := d.Procs.WindowOpen(exe)
	if err != nil {
		return failure("process_check_failed", "检查 %s 的窗口：%w", devDisplayName, err)
	}
	if open {
		res.WindowOpen, res.Deferred = true, true
		wait, poll := d.WindowWait, d.WindowPoll
		if wait <= 0 {
			wait = windowWait
		}
		if poll <= 0 {
			poll = windowPoll
		}
		d.logf("==> [install-dev] %s 的窗口开着，等它关闭或隐藏到托盘（最多 %d 分钟）…", devDisplayName, int(wait/time.Minute))
		deferStart := d.Now()
		for open && running {
			if d.Now().Sub(deferStart) >= wait {
				res.DeferMs = d.Now().Sub(deferStart).Milliseconds()
				res.Skipped, res.Reason = true, "window_open"
				res.Guidance = fmt.Sprintf("%s 的窗口开着（你可能正在用），等了 %d 分钟仍没关闭，这次没有安装；关掉窗口或隐藏到托盘后运行 %s", devDisplayName, int(wait/time.Minute), installOnlyCommand)
				return nil
			}
			d.Sleep(poll)
			if running, err = d.Procs.Running(exe); err != nil {
				return failure("process_check_failed", "检查 %s 是否在运行：%w", devDisplayName, err)
			}
			if running {
				if open, err = d.Procs.WindowOpen(exe); err != nil {
					return failure("process_check_failed", "检查 %s 的窗口：%w", devDisplayName, err)
				}
			}
		}
		res.DeferMs = d.Now().Sub(deferStart).Milliseconds()
		if !running {
			return nil
		}
	}

	d.logf("==> [install-dev] 请托盘里的 %s 保存并退出…", devDisplayName)
	quitStart := d.Now()
	if err := d.Procs.RequestQuit(exe); err != nil {
		return failure("quit_failed", "请 %s 退出：%w，这次没有安装", devDisplayName, err)
	}
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = quitTimeout
	}
	for {
		still, err := d.Procs.Running(exe)
		if err != nil {
			return failure("process_check_failed", "检查 %s 是否已退出：%w，这次没有安装", devDisplayName, err)
		}
		if !still {
			break
		}
		if d.Now().Sub(quitStart) >= timeout {
			return failure("quit_timeout", "%s 正在收尾（缓存或录制），%d 秒内没有退出，这次没有安装；没有强制结束它", devDisplayName, int(timeout/time.Second))
		}
		d.Sleep(quitPoll)
	}
	res.QuitMs = d.Now().Sub(quitStart).Milliseconds()
	return nil
}

func writeInstallResult(path string, res installResult) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

var errUnsupported = errors.New("本机自动安装只支持 Windows")
