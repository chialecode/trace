//go:build windows

package build

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"github.com/chialecode/trace/internal/appdirs"
	"golang.org/x/sys/windows"
)

var (
	user32                    = windows.NewLazySystemDLL("user32.dll")
	procEnumWindows           = user32.NewProc("EnumWindows")
	procGetWindow             = user32.NewProc("GetWindow")
	procGetWindowLongPtrW     = user32.NewProc("GetWindowLongPtrW")
	procDwmGetWindowAttribute = windows.NewLazySystemDLL("dwmapi.dll").NewProc("DwmGetWindowAttribute")
)

const (
	gwOwner        = 4
	gwlExStyle     = ^uintptr(19) // GWL_EXSTYLE, -20
	wsExToolWindow = 0x00000080
	dwmwaCloaked   = 14
	dwmCloakedApp  = 0x1 // the app cloaked it; DWM_CLOAKED_SHELL (another virtual desktop) still counts
)

// One callback for every WindowOpen call: syscall.NewCallback slots are
// never freed, and -install polls every 2 s for up to 10 minutes.
var (
	windowMu        sync.Mutex
	windowPIDs      map[uint32]bool
	windowFound     bool
	windowEnumCount int
	windowEnumCB    uintptr
	windowCBSetup   sync.Once
)

func enumWindow(hwnd uintptr, _ uintptr) uintptr {
	windowEnumCount++
	var pid uint32
	if _, err := windows.GetWindowThreadProcessId(windows.HWND(hwnd), &pid); err != nil || !windowPIDs[pid] {
		return 1
	}
	if windowPerceivable(hwnd) {
		windowFound = true
		return 0
	}
	return 1
}

// windowPerceivable: a visible, unowned top-level window that is not a tool
// window and not cloaked by the app itself. A minimized window is still
// visible, and so is one on another virtual desktop (the shell cloaks it);
// a window hidden to the tray is not.
func windowPerceivable(hwnd uintptr) bool {
	if !windows.IsWindowVisible(windows.HWND(hwnd)) {
		return false
	}
	if owner, _, _ := procGetWindow.Call(hwnd, gwOwner); owner != 0 {
		return false
	}
	if ex, _, _ := procGetWindowLongPtrW.Call(hwnd, gwlExStyle); ex&wsExToolWindow != 0 {
		return false
	}
	var cloaked uint32
	if r, _, _ := procDwmGetWindowAttribute.Call(hwnd, dwmwaCloaked, uintptr(unsafe.Pointer(&cloaked)), unsafe.Sizeof(cloaked)); r == 0 && cloaked&dwmCloakedApp != 0 {
		return false
	}
	return true
}

// interpretEnumWindowsResult interprets the raw Win32 EnumWindows return value
// without mapping (r1=0, GetLastError=0) to syscall.EINVAL. On an isolated
// empty Windows desktop (zero top-level windows), EnumWindows returns FALSE
// with ERROR_SUCCESS (0); golang.org/x/sys/windows.EnumWindows turns that into
// syscall.EINVAL ("invalid argument").
func interpretEnumWindowsResult(r1 uintptr, e1 syscall.Errno, found bool, enumCount int) (bool, error) {
	if found {
		return true, nil
	}
	if uint32(r1) != 0 {
		return false, nil
	}
	if e1 == 0 {
		_ = enumCount
		return false, nil
	}
	return false, e1
}

// WindowOpen reports whether a process running the executable at exe shows
// a window the user can see (minimized counts, hidden to the tray does not).
func WindowOpen(exe string) (bool, error) {
	return WindowOpenForDataDir(exe, "")
}

// WindowOpenForDataDir reports whether a process running exe shows a window
// the user can see. When dataDir holds a live runtime-window.json written by
// one of those running PIDs, it trusts that state directly so cross-desktop
// sandboxes accurately observe the interactive desktop's window state.
func WindowOpenForDataDir(exe, dataDir string) (bool, error) {
	pids, err := processIDs(exe)
	if err != nil || len(pids) == 0 {
		return false, err
	}
	if dataDir != "" {
		if st, ok := appdirs.ReadRuntimeWindowState(dataDir); ok && pids[uint32(st.PID)] {
			return st.WindowOpen, nil
		}
	}
	windowCBSetup.Do(func() { windowEnumCB = syscall.NewCallback(enumWindow) })
	windowMu.Lock()
	defer windowMu.Unlock()
	windowPIDs, windowFound, windowEnumCount = pids, false, 0
	r1, _, lastErr := procEnumWindows.Call(windowEnumCB, 0)
	found := windowFound
	count := windowEnumCount
	windowPIDs = nil
	var errno syscall.Errno
	if e, ok := lastErr.(syscall.Errno); ok {
		errno = e
	}
	return interpretEnumWindowsResult(r1, errno, found, count)
}

// processIDs lists the processes that run the executable at exe.
func processIDs(exe string) (map[uint32]bool, error) {
	want, err := filepath.Abs(exe)
	if err != nil {
		return nil, err
	}
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap)
	pids := map[uint32]bool{}
	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	for err = windows.Process32First(snap, &pe); err == nil; err = windows.Process32Next(snap, &pe) {
		if !strings.EqualFold(windows.UTF16ToString(pe.ExeFile[:]), filepath.Base(want)) {
			continue
		}
		path, perr := processImage(pe.ProcessID)
		if perr == nil && strings.EqualFold(filepath.Clean(path), filepath.Clean(want)) {
			pids[pe.ProcessID] = true
		}
	}
	if errors.Is(err, syscall.ERROR_NO_MORE_FILES) {
		return pids, nil
	}
	return nil, err
}
