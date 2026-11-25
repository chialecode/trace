//go:build windows

package build

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32                    = windows.NewLazySystemDLL("user32.dll")
	procGetWindow             = user32.NewProc("GetWindow")
	procGetWindowLongPtrW     = user32.NewProc("GetWindowLongPtrW")
	procDwmGetWindowAttribute = windows.NewLazySystemDLL("dwmapi.dll").NewProc("DwmGetWindowAttribute")
)

const (
	gwOwner        = 4
	gwlExStyle     = ^uintptr(19) // GWL_EXSTYLE, -20
	wsExToolWindow = 0x00000080
	dwmwaCloaked   = 14
)

// One callback for every WindowOpen call: syscall.NewCallback slots are
// never freed, and -install polls every 2 s for up to 10 minutes.
var (
	windowMu      sync.Mutex
	windowPIDs    map[uint32]bool
	windowFound   bool
	windowEnumCB  uintptr
	windowCBSetup sync.Once
)

func enumWindow(hwnd uintptr, _ uintptr) uintptr {
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
// window and not cloaked. A minimized window is still visible; a window
// hidden to the tray is not.
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
	if r, _, _ := procDwmGetWindowAttribute.Call(hwnd, dwmwaCloaked, uintptr(unsafe.Pointer(&cloaked)), unsafe.Sizeof(cloaked)); r == 0 && cloaked != 0 {
		return false
	}
	return true
}

// WindowOpen reports whether a process running the executable at exe shows
// a window the user can see (minimized counts, hidden to the tray does not).
func WindowOpen(exe string) (bool, error) {
	pids, err := processIDs(exe)
	if err != nil || len(pids) == 0 {
		return false, err
	}
	windowCBSetup.Do(func() { windowEnumCB = syscall.NewCallback(enumWindow) })
	windowMu.Lock()
	defer windowMu.Unlock()
	windowPIDs, windowFound = pids, false
	// EnumWindows reports an error when the callback stops it early.
	_ = windows.EnumWindows(windowEnumCB, nil)
	found := windowFound
	windowPIDs = nil
	return found, nil
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
