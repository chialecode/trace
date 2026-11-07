//go:build windows

package shell

import (
	"strings"
	"syscall"
	"unsafe"
)

var (
	modKernel32              = syscall.NewLazyDLL("kernel32.dll")
	procGetCurrentProcess    = modKernel32.NewProc("GetCurrentProcess")
	procSetPriorityClass     = modKernel32.NewProc("SetPriorityClass")
	procK32GetProcessMemInfo = modKernel32.NewProc("K32GetProcessMemoryInfo")
	procK32EmptyWorkingSet   = modKernel32.NewProc("K32EmptyWorkingSet")
	procCreateToolhelp32     = modKernel32.NewProc("CreateToolhelp32Snapshot")
	procProcess32FirstW      = modKernel32.NewProc("Process32FirstW")
	procProcess32NextW       = modKernel32.NewProc("Process32NextW")

	modUser32               = syscall.NewLazyDLL("user32.dll")
	procGetForegroundWindow = modUser32.NewProc("GetForegroundWindow")
	procGetWindowThreadPID  = modUser32.NewProc("GetWindowThreadProcessId")
)

const (
	belowNormalPriorityClass = 0x00004000
	th32csSnapProcess        = 0x00000002
)

type processMemoryCountersEx struct {
	CB                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
	PrivateUsage               uintptr
	PrivateWorkingSetSize      uintptr // PROCESS_MEMORY_COUNTERS_EX2 (Windows 10 1809+)
	SharedCommitUsage          uint64
}

type processEntry32W struct {
	Size            uint32
	Usage           uint32
	ProcessID       uint32
	DefaultHeapID   uintptr
	ModuleID        uint32
	Threads         uint32
	ParentProcessID uint32
	PriClassBase    int32
	Flags           uint32
	ExeFile         [260]uint16
}

// SetBelowNormalPriority sets the current process priority to BELOW_NORMAL_PRIORITY_CLASS (§6.10).
func SetBelowNormalPriority() error {
	h, _, _ := procGetCurrentProcess.Call()
	r1, _, err := procSetPriorityClass.Call(h, uintptr(belowNormalPriorityClass))
	if r1 == 0 {
		return err
	}
	return nil
}

// GetForegroundWindowHandle returns the current Win32 foreground window HWND and its owning PID.
func GetForegroundWindowHandle() (hwnd uintptr, pid uint32) {
	h, _, _ := procGetForegroundWindow.Call()
	if h == 0 {
		return 0, 0
	}
	var p uint32
	procGetWindowThreadPID.Call(h, uintptr(unsafe.Pointer(&p)))
	return h, p
}

// TrimWorkingSet trims unused pages from the current process working set after window destruction.
func TrimWorkingSet() {
	h, _, _ := procGetCurrentProcess.Call()
	_, _, _ = procK32EmptyWorkingSet.Call(h)
}

// CurrentProcessMemoryBytes returns the resident working set, the private
// working set (Task Manager "Memory", the NFR-RES-001 metric; 0 when the OS
// predates PROCESS_MEMORY_COUNTERS_EX2) and the committed private bytes.
func CurrentProcessMemoryBytes() (workingSet, privateWorkingSet, privateBytes int64) {
	h, _, _ := procGetCurrentProcess.Call()
	var pmc processMemoryCountersEx
	pmc.CB = uint32(unsafe.Sizeof(pmc))
	r1, _, _ := procK32GetProcessMemInfo.Call(h, uintptr(unsafe.Pointer(&pmc)), uintptr(pmc.CB))
	if r1 == 0 {
		return 0, 0, 0
	}
	return int64(pmc.WorkingSetSize), int64(pmc.PrivateWorkingSetSize), int64(pmc.PrivateUsage)
}

// CountWebView2Processes counts running msedgewebview2.exe child processes owned by parentPID (or any if parentPID == 0).
func CountWebView2Processes(parentPID uint32) int {
	snap, _, _ := procCreateToolhelp32.Call(th32csSnapProcess, 0)
	if snap == 0 || snap == ^uintptr(0) {
		return 0
	}
	defer syscall.CloseHandle(syscall.Handle(snap))

	var entry processEntry32W
	entry.Size = uint32(unsafe.Sizeof(entry))
	r1, _, _ := procProcess32FirstW.Call(snap, uintptr(unsafe.Pointer(&entry)))
	if r1 == 0 {
		return 0
	}

	count := 0
	for {
		name := strings.ToLower(syscall.UTF16ToString(entry.ExeFile[:]))
		if name == "msedgewebview2.exe" {
			if parentPID == 0 || entry.ParentProcessID == parentPID {
				count++
			}
		}
		entry.Size = uint32(unsafe.Sizeof(entry))
		rNext, _, _ := procProcess32NextW.Call(snap, uintptr(unsafe.Pointer(&entry)))
		if rNext == 0 {
			break
		}
	}
	return count
}
