//go:build windows

package appdirs

import (
	"os/exec"
	"syscall"
)

var (
	modKernel32Proc       = syscall.NewLazyDLL("kernel32.dll")
	procGetCurrentProc    = modKernel32Proc.NewProc("GetCurrentProcess")
	procSetPriorityClassW = modKernel32Proc.NewProc("SetPriorityClass")
)

const belowNormalPriorityClass = 0x00004000

// LowerCurrentProcessPriority sets the current process priority to BELOW_NORMAL_PRIORITY_CLASS (§6).
func LowerCurrentProcessPriority() {
	h, _, _ := procGetCurrentProc.Call()
	_, _, _ = procSetPriorityClassW.Call(h, uintptr(belowNormalPriorityClass))
}

// SetSubprocessQuiet configures cmd so it hides console windows and runs at BELOW_NORMAL_PRIORITY_CLASS (§6).
func SetSubprocessQuiet(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= belowNormalPriorityClass
}
