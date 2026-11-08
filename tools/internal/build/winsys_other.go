//go:build !windows

package build

import "errors"

// InstallEntry is what the per-user uninstall entry of an identity says.
type InstallEntry struct {
	Location        string
	Version         string
	QuietUninstall  string
	UninstallString string
}

var errWindowsOnly = errors.New("installing TRACE is only supported on Windows")

func LookupInstall(string) (InstallEntry, bool, error) { return InstallEntry{}, false, errWindowsOnly }
func ProcessRunning(string) (bool, error)              { return false, errWindowsOnly }
func RunQuiet(string, []string, []string) error        { return errWindowsOnly }
func LaunchDetached(string, []string, []string) error  { return errWindowsOnly }
func AppEnv(extra ...string) []string                  { return extra }

func RunningIn(string) (bool, error) { return false, errWindowsOnly }
