//go:build !windows

package shell

import "runtime"

func SetBelowNormalPriority() error {
	return nil
}

func TrimWorkingSet() {}

func GetForegroundWindowHandle() (hwnd uintptr, pid uint32) {
	return 0, 0
}

func CurrentProcessMemoryBytes() (workingSet, privateWorkingSet, privateBytes int64) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return int64(ms.Sys), int64(ms.Alloc), int64(ms.Sys)
}

func CountWebView2Processes(parentPID uint32) int {
	return 0
}
