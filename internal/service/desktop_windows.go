//go:build windows

package service

import (
	"syscall"
	"unsafe"
)

var (
	modShell32        = syscall.NewLazyDLL("shell32.dll")
	procShellExecuteW = modShell32.NewProc("ShellExecuteW")
)

const swShowNormal = 1

// openExternalURLWindows launches clean (already validated http/https) in the
// user's default browser via Win32 ShellExecuteW so URL fragments (#anchor),
// query separators (&), commas, and percent-encoded characters are preserved
// intact without url.dll/FileProtocolHandler mangling. Falls back to rundll32
// only if ShellExecuteW reports failure.
func openExternalURLWindows(clean string) error {
	verbPtr, err := syscall.UTF16PtrFromString("open")
	if err != nil {
		return startAndRelease("rundll32.exe", "url.dll,FileProtocolHandler", clean)
	}
	urlPtr, err := syscall.UTF16PtrFromString(clean)
	if err != nil {
		return startAndRelease("rundll32.exe", "url.dll,FileProtocolHandler", clean)
	}
	ret, _, _ := procShellExecuteW.Call(
		0,
		uintptr(unsafe.Pointer(verbPtr)),
		uintptr(unsafe.Pointer(urlPtr)),
		0,
		0,
		uintptr(swShowNormal),
	)
	if ret > 32 {
		return nil
	}
	return startAndRelease("rundll32.exe", "url.dll,FileProtocolHandler", clean)
}
