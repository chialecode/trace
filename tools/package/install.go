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
// it finds TRACE Dev in the per-user uninstall entries, asks a
// running TRACE Dev to save its work and quit, installs the new package
// silently, checks the installed version and then starts TRACE Dev in the
// tray (--background), whether it was running or not: the user keeps the
// development build running (2026-11-15). It never touches the release or
// ptest identities.

const (
	devIdentifier      = "io.github.chialecode.trace-dev"
	devDisplayName     = "TRACE Dev"
	devExeName         = "trace.exe"
	quitTimeout        = 30 * time.Second
	quitRequestTimeout = 15 * time.Second
	quitPoll           = 250 * time.Millisecond
	flagQuitUpdate     = "--quit-for-update"
	flagBackground     = "--background"
	installDevEnv      = "TRACE_DEV_AUTO_INSTALL"
	installResultTo    = "artifacts/package/install-dev.json"
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
// There is deliberately no way to kill a process. Launch reports whether the
// new process stayed inside this tool's job object, where it may end when
// the command that ran the tool ends.
type processes interface {
	Running(exe string) (bool, error)
	RequestQuit(exe string) error
	Launch(exe string, args []string) (inJob bool, err error)
}

// devInstaller holds what an install depends on, so tests can replace the
// registry, the processes, the installer and the clock.
type devInstaller struct {
	Setup    string
	Version  string
	Registry uninstallReader
	Procs    processes
	RunSetup func(setup string) error
	Now      func() time.Time
	Sleep    func(time.Duration)
	Timeout  time.Duration
	Logf     func(format string, args ...any)
}

// installResult is artifacts/package/install-dev.json.
type installResult struct {
	Installed  bool   `json:"installed"`
	Skipped    bool   `json:"skipped"`
	Reason     string `json:"reason,omitempty"`
	Error      string `json:"error,omitempty"`
	OldVersion string `json:"oldVersion,omitempty"`
	NewVersion string `json:"newVersion"`
	Location   string `json:"installLocation,omitempty"`
	WasRunning bool   `json:"wasRunning"`
	QuitMs     int64  `json:"quitMs,omitempty"`
	Relaunched bool   `json:"relaunched"`
	Background bool   `json:"background"`
	// InJob: the started TRACE Dev could not leave the job object of the
	// command that ran this tool and may end with it.
	InJob     bool   `json:"inJob,omitempty"`
	ElapsedMs int64  `json:"elapsedMs"`
	At        string `json:"at"`
}

// skipReason tells why the automatic install does not run, or "".
func skipReason(getenv func(string) string) string {
	if strings.TrimSpace(getenv(installDevEnv)) == "0" {
		return installDevEnv + "=0：跳过本机开发版自动安装"
	}
	if strings.EqualFold(strings.TrimSpace(getenv("CI")), "true") {
		return "CI 上不安装开发版"
	}
	return ""
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
	res := installResult{NewVersion: d.Version, At: start.UTC().Format(time.RFC3339)}
	finish := func(err error) (installResult, error) {
		res.ElapsedMs = d.Now().Sub(start).Milliseconds()
		if err != nil {
			res.Error = err.Error()
		}
		return res, err
	}

	entry, ok, err := d.Registry.Lookup(devIdentifier)
	if err != nil {
		return finish(fmt.Errorf("读取 %s 的卸载项：%w", devDisplayName, err))
	}
	if !ok || strings.TrimSpace(entry.Location) == "" {
		res.Skipped = true
		res.Reason = "本机没有安装 " + devDisplayName + "，跳过自动安装（首次安装请运行 dist/trace-dev 里的安装包）"
		return finish(nil)
	}
	res.OldVersion, res.Location = entry.Version, entry.Location
	exe := filepath.Join(entry.Location, devExeName)

	running, err := d.Procs.Running(exe)
	if err != nil {
		return finish(fmt.Errorf("检查 %s 是否在运行：%w", devDisplayName, err))
	}
	res.WasRunning = running
	if running {
		d.logf("==> [install-dev] 请运行中的 %s 保存并退出…", devDisplayName)
		quitStart := d.Now()
		if err := d.Procs.RequestQuit(exe); err != nil {
			return finish(fmt.Errorf("请 %s 退出：%w，这次没有安装", devDisplayName, err))
		}
		timeout := d.Timeout
		if timeout <= 0 {
			timeout = quitTimeout
		}
		for {
			still, err := d.Procs.Running(exe)
			if err != nil {
				return finish(fmt.Errorf("检查 %s 是否已退出：%w，这次没有安装", devDisplayName, err))
			}
			if !still {
				break
			}
			if d.Now().Sub(quitStart) >= timeout {
				return finish(fmt.Errorf("%s 正在收尾（缓存或录制），%d 秒内没有退出，这次没有安装；没有强制结束它", devDisplayName, int(timeout/time.Second)))
			}
			d.Sleep(quitPoll)
		}
		res.QuitMs = d.Now().Sub(quitStart).Milliseconds()
	}

	d.logf("==> [install-dev] 静默安装 %s %s…", devDisplayName, d.Version)
	if err := d.RunSetup(d.Setup); err != nil {
		return finish(fmt.Errorf("静默安装失败：%w", err))
	}
	after, ok, err := d.Registry.Lookup(devIdentifier)
	if err != nil || !ok {
		return finish(fmt.Errorf("安装后读不到 %s 的卸载项：%v", devDisplayName, err))
	}
	if after.Version != d.Version {
		return finish(fmt.Errorf("安装后的版本是 %q，不是 %s", after.Version, d.Version))
	}
	res.Installed, res.Location = true, after.Location

	inJob, err := d.Procs.Launch(filepath.Join(after.Location, devExeName), []string{flagBackground})
	if err != nil {
		return finish(fmt.Errorf("已安装 %s，在托盘打开失败：%w", d.Version, err))
	}
	res.Relaunched, res.Background, res.InJob = true, true, inJob
	return finish(nil)
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
