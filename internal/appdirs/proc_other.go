//go:build !windows

package appdirs

import "os/exec"

// LowerCurrentProcessPriority is a no-op on non-Windows platforms.
func LowerCurrentProcessPriority() {}

// SetSubprocessQuiet is a no-op on non-Windows platforms.
func SetSubprocessQuiet(cmd *exec.Cmd) {}
