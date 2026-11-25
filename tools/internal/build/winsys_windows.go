//go:build windows

package build

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/chialecode/trace/internal/appdirs"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const uninstallKey = `Software\Microsoft\Windows\CurrentVersion\Uninstall\`

// InstallEntry is what the per-user uninstall entry of an identity says.
type InstallEntry struct {
	Location        string
	Version         string
	QuietUninstall  string
	UninstallString string
}

// LookupInstall reads HKCU\...\Uninstall\<identifier>; it only reads.
func LookupInstall(identifier string) (InstallEntry, bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, uninstallKey+identifier, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return InstallEntry{}, false, nil
	}
	if err != nil {
		return InstallEntry{}, false, err
	}
	defer k.Close()
	get := func(name string) string {
		v, _, _ := k.GetStringValue(name)
		return strings.TrimSpace(v)
	}
	return InstallEntry{
		Location:        strings.Trim(get("InstallLocation"), `"`),
		Version:         get("DisplayVersion"),
		QuietUninstall:  get("QuietUninstallString"),
		UninstallString: get("UninstallString"),
	}, true, nil
}

// ProcessRunning reports whether a process runs the executable at exe.
func ProcessRunning(exe string) (bool, error) {
	pids, err := processIDs(exe)
	return len(pids) > 0, err
}

func processImage(pid uint32) (string, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buf[:n]), nil
}

// RunQuiet runs exe with args, without a window, and waits for it.
func RunQuiet(exe string, args, env []string) error {
	return RunQuietContext(context.Background(), exe, args, env)
}

// RunQuietContext runs exe with args, without a window, and waits for it
// until ctx is done.
func RunQuietContext(ctx context.Context, exe string, args, env []string) error {
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Dir = filepath.Dir(exe)
	cmd.Env = env
	cmd.WaitDelay = 2 * time.Second
	appdirs.SetSubprocessQuiet(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("%s: %w", filepath.Base(exe), ctx.Err())
		}
		return fmt.Errorf("%s: %w %s", filepath.Base(exe), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// LaunchDetached starts exe detached from this tool; its window shows as
// usual unless args keep it in the tray.
func LaunchDetached(exe string, args, env []string) error {
	_, err := LaunchDetachedOutsideJob(exe, args, env)
	return err
}

// LaunchDetachedOutsideJob starts exe detached from this tool and, when this
// tool runs inside a job object (as an agent's command sandbox does), asks
// for the new process to break away from it so that closing the job does not
// end it. A job that forbids breaking away refuses the request; exe then
// starts inside the job and inJob reports that it may end with the command.
func LaunchDetachedOutsideJob(exe string, args, env []string) (inJob bool, err error) {
	start := func(flags uint32) (*exec.Cmd, error) {
		cmd := exec.Command(exe, args...)
		cmd.Dir = filepath.Dir(exe)
		cmd.Env = env
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags}
		return cmd, cmd.Start()
	}
	base := uint32(windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS)
	cmd, err := start(base | windows.CREATE_BREAKAWAY_FROM_JOB)
	if err != nil {
		var errno syscall.Errno
		if !errors.As(err, &errno) || errno != windows.ERROR_ACCESS_DENIED {
			return false, err
		}
		if cmd, err = start(base); err != nil {
			return false, err
		}
	}
	inJob = processInJob(uint32(cmd.Process.Pid))
	return inJob, cmd.Process.Release()
}

type desktopShellDeps struct {
	selfInJob        func() bool
	launchViaShell   func(exe string, args []string) (pid, ppid uint32, err error)
	parentIsExplorer func(pid, reportedPPID uint32) bool
	pidInJob         func(pid uint32) bool
	launchDetached   func(exe string, args, env []string) (bool, error)
}

// LaunchInstalledOutsideJob starts an installed program so that it outlives
// this tool's command. Outside a job object it is LaunchDetachedOutsideJob.
// Inside one (an agent's command sandbox may forbid breaking away and end
// every process when the command returns) it first asks the desktop shell
// (explorer.exe) to start exe, so the program is explorer's child and in no
// job of this command; it then has the shell's environment, not env. When
// the shell cannot start it, it falls back to LaunchDetachedOutsideJob.
func LaunchInstalledOutsideJob(exe string, args, env []string) (inJob bool, err error) {
	return launchInstalledOutsideJob(exe, args, env, desktopShellDeps{
		selfInJob:        func() bool { return processInJob(uint32(os.Getpid())) },
		launchViaShell:   func(exe string, args []string) (uint32, uint32, error) { return launchViaDesktopShell(exe, args, 1) },
		parentIsExplorer: parentIsExplorer,
		pidInJob:         processInJob,
		launchDetached:   LaunchDetachedOutsideJob,
	})
}

func launchInstalledOutsideJob(exe string, args, env []string, deps desktopShellDeps) (inJob bool, err error) {
	if deps.selfInJob() {
		if pid, ppid, serr := deps.launchViaShell(exe, args); serr == nil {
			// On Windows 10/11, explorer.exe / Program Compatibility Assistant
			// routinely assigns GUI apps started via ShellExecute to a system
			// shell job object. Because IsProcessInJob(h, NULL, &in) checks
			// membership in ANY job object, processInJob(pid) false-positives
			// even though the launched process is explorer.exe's child and NOT
			// in this command's job.
			if deps.parentIsExplorer(pid, ppid) {
				return false, nil
			}
			// Launched, but could not confirm explorer.exe as parent: fall back
			// to conservative job reporting without starting a second copy.
			return deps.pidInJob(pid), nil
		}
	}
	return deps.launchDetached(exe, args, env)
}

// desktopShellLaunchScript asks explorer.exe (Shell.Application of the
// desktop window, SWC_DESKTOP) to ShellExecute the program, then waits up to
// 15 s for the new process of that executable (preferring one whose parent is
// explorer.exe and whose parent is not the same executable) and prints
// "<pid> <ppid>".
const desktopShellLaunchScript = `$ErrorActionPreference = 'Stop'
$exe = $env:TRACE_LAUNCH_EXE; $argLine = $env:TRACE_LAUNCH_ARGS; $dir = $env:TRACE_LAUNCH_DIR; $show = [int]$env:TRACE_LAUNCH_SHOW
if (-not (Test-Path -LiteralPath $exe -PathType Leaf)) { exit 2 }
$name = [IO.Path]::GetFileName($exe).Replace("'", "''")
function Get-Same { @(Get-CimInstance Win32_Process -Filter "Name='$name'" | Where-Object { $_.ExecutablePath -and [string]::Equals($_.ExecutablePath, $exe, [StringComparison]::OrdinalIgnoreCase) }) }
$before = @{}; foreach ($p in Get-Same) { $before[[int]$p.ProcessId] = $true }
$exp = @{}; foreach ($p in @(Get-CimInstance Win32_Process -Filter "Name='explorer.exe'")) { $exp[[int]$p.ProcessId] = $true }
$f = [Reflection.BindingFlags]; $m = [Reflection.Missing]::Value
$t = [Type]::GetTypeFromCLSID([Guid]'9BA05972-F6A8-11CF-A442-00A0C90A8F39')
$sw = [Activator]::CreateInstance($t)
$d = $t.InvokeMember('FindWindowSW', $f::InvokeMethod, $null, $sw, @($m, $m, 8, 0, 1))
if ($null -eq $d) { exit 3 }
$doc = $d.GetType().InvokeMember('Document', $f::GetProperty, $null, $d, $null)
$app = $doc.GetType().InvokeMember('Application', $f::GetProperty, $null, $doc, $null)
[void]$app.GetType().InvokeMember('ShellExecute', $f::InvokeMethod, $null, $app, @($exe, $argLine, $dir, 'open', $show))
$deadline = (Get-Date).AddSeconds(15)
while ((Get-Date) -lt $deadline) {
  $now = Get-Same; $ids = @{}; foreach ($p in $now) { $ids[[int]$p.ProcessId] = $true }
  $cand = @($now | Where-Object { -not $before.ContainsKey([int]$_.ProcessId) -and -not $ids.ContainsKey([int]$_.ParentProcessId) })
  $root = @($cand | Where-Object { $exp.ContainsKey([int]$_.ParentProcessId) }) | Select-Object -First 1
  if (-not $root) { $root = $cand | Select-Object -First 1 }
  if ($root) { [Console]::Out.Write("$([int]$root.ProcessId) $([int]$root.ParentProcessId)"); exit 0 }
  Start-Sleep -Milliseconds 200
}
exit 4`

// launchViaDesktopShell starts exe through explorer.exe (see
// desktopShellLaunchScript) with the ShowWindow command show and returns
// the new process id and its parent process id.
func launchViaDesktopShell(exe string, args []string, show int) (pid, ppid uint32, err error) {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = syscall.EscapeArg(a)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Sta", "-ExecutionPolicy", "Bypass", "-Command", desktopShellLaunchScript)
	cmd.Env = append(os.Environ(),
		"TRACE_LAUNCH_EXE="+exe,
		"TRACE_LAUNCH_ARGS="+strings.Join(quoted, " "),
		"TRACE_LAUNCH_DIR="+filepath.Dir(exe),
		fmt.Sprintf("TRACE_LAUNCH_SHOW=%d", show),
	)
	cmd.WaitDelay = 2 * time.Second
	appdirs.SetSubprocessQuiet(cmd)
	out, err := cmd.Output()
	if err != nil {
		return 0, 0, fmt.Errorf("desktop shell launch: %w", err)
	}
	return parseDesktopShellOutput(string(out))
}

// parseDesktopShellOutput parses "<pid> <ppid>" printed by desktopShellLaunchScript.
func parseDesktopShellOutput(out string) (pid, ppid uint32, err error) {
	fields := strings.Fields(out)
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("desktop shell launch: expected \"<pid> <ppid>\", got %q", strings.TrimSpace(out))
	}
	p1, err1 := strconv.ParseUint(fields[0], 10, 32)
	p2, err2 := strconv.ParseUint(fields[1], 10, 32)
	if err1 != nil || p1 == 0 || err2 != nil || p2 == 0 {
		return 0, 0, fmt.Errorf("desktop shell launch: invalid pid/ppid in %q", strings.TrimSpace(out))
	}
	return uint32(p1), uint32(p2), nil
}

// processParentID looks up pid in a TH32CS_SNAPPROCESS snapshot and returns
// its parent process id. found is false when pid is no longer running.
func processParentID(pid uint32) (ppid uint32, found bool, err error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, false, err
	}
	defer windows.CloseHandle(snap)
	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	for err = windows.Process32First(snap, &pe); err == nil; err = windows.Process32Next(snap, &pe) {
		if pe.ProcessID == pid {
			return pe.ParentProcessID, true, nil
		}
	}
	if errors.Is(err, syscall.ERROR_NO_MORE_FILES) {
		return 0, false, nil
	}
	return 0, false, err
}

func windowsSystemRoot() string {
	if r := strings.TrimSpace(os.Getenv("SystemRoot")); r != "" {
		return r
	}
	if r := strings.TrimSpace(os.Getenv("WINDIR")); r != "" {
		return r
	}
	if dir, err := windows.GetSystemWindowsDirectory(); err == nil {
		return strings.TrimSpace(dir)
	}
	return ""
}

// isExplorerImage reports whether path is %SystemRoot%\explorer.exe.
func isExplorerImage(path, systemRoot string) bool {
	path = strings.TrimSpace(path)
	systemRoot = strings.TrimSpace(systemRoot)
	if path == "" || systemRoot == "" {
		return false
	}
	want := filepath.Clean(filepath.Join(systemRoot, "explorer.exe"))
	return strings.EqualFold(filepath.Clean(path), want)
}

// parentIsExplorer reports whether pid was started by %SystemRoot%\explorer.exe
// (outside this process's tree).
func parentIsExplorer(pid, reportedPPID uint32) bool {
	return verifyParentIsExplorer(
		pid,
		reportedPPID,
		uint32(os.Getpid()),
		windowsSystemRoot(),
		processParentID,
		processImage,
	)
}

func verifyParentIsExplorer(
	pid, reportedPPID, selfPID uint32,
	systemRoot string,
	lookupParent func(uint32) (uint32, bool, error),
	lookupImage func(uint32) (string, error),
) bool {
	if pid == 0 {
		return false
	}
	actualPPID, found, err := lookupParent(pid)
	if err != nil {
		return false
	}
	parentPID := reportedPPID
	if found {
		if reportedPPID != 0 && actualPPID != reportedPPID {
			return false
		}
		parentPID = actualPPID
	}
	if parentPID == 0 || parentPID == selfPID || parentPID == pid {
		return false
	}
	img, err := lookupImage(parentPID)
	if err != nil {
		return false
	}
	return isExplorerImage(img, systemRoot)
}

var procIsProcessInJob = windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")

// processInJob reports whether the process pid belongs to any job object.
func processInJob(pid uint32) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var in int32
	if r, _, _ := procIsProcessInJob.Call(uintptr(h), 0, uintptr(unsafe.Pointer(&in))); r == 0 {
		return false
	}
	return in != 0
}

// AppEnv is this process's environment without the variables that would
// point an installed TRACE at another identity, data or cache directory,
// plus extra.
func AppEnv(extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch strings.ToUpper(k) {
		case "TRACE_CHANNEL", "TRACE_DATA_DIR", "TRACE_CACHE_DIR", "TRACE_PORTABLE", "TRACE_QUIET", "TRACE_TEST_UPDATE_URL", "TRACE_DOCUMENTS_DIR", "MYGO_UPDATER_PRIVATE_KEY":
			continue
		}
		env = append(env, kv)
	}
	return append(env, extra...)
}

// RunningIn reports whether a process runs an executable of dir: the
// program, or the copy an update renamed aside while it ran.
func RunningIn(dir string) (bool, error) {
	want, err := filepath.Abs(dir)
	if err != nil {
		return false, err
	}
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return false, err
	}
	defer windows.CloseHandle(snap)
	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	for err = windows.Process32First(snap, &pe); err == nil; err = windows.Process32Next(snap, &pe) {
		if !strings.Contains(strings.ToLower(windows.UTF16ToString(pe.ExeFile[:])), "trace") {
			continue
		}
		path, perr := processImage(pe.ProcessID)
		if perr == nil && strings.EqualFold(filepath.Dir(filepath.Clean(path)), filepath.Clean(want)) {
			return true, nil
		}
	}
	if errors.Is(err, syscall.ERROR_NO_MORE_FILES) {
		return false, nil
	}
	return false, err
}
