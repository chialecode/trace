//go:build windows

package build

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	want, err := filepath.Abs(exe)
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
		if !strings.EqualFold(windows.UTF16ToString(pe.ExeFile[:]), filepath.Base(want)) {
			continue
		}
		path, perr := processImage(pe.ProcessID)
		if perr == nil && strings.EqualFold(filepath.Clean(path), filepath.Clean(want)) {
			return true, nil
		}
	}
	if errors.Is(err, syscall.ERROR_NO_MORE_FILES) {
		return false, nil
	}
	return false, err
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
		case "TRACE_CHANNEL", "TRACE_DATA_DIR", "TRACE_CACHE_DIR", "TRACE_PORTABLE", "TRACE_QUIET", "TRACE_PTEST_UPDATE_URL", "TRACE_DOCUMENTS_DIR", "MYGO_UPDATER_PRIVATE_KEY":
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
