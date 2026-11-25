//go:build windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/tools/internal/build"
)

type winRegistry struct{}

func (winRegistry) Lookup(identifier string) (installEntry, bool, error) {
	e, ok, err := build.LookupInstall(identifier)
	return installEntry{Location: e.Location, Version: e.Version}, ok, err
}

type winProcesses struct{}

func (winProcesses) Running(exe string) (bool, error) { return build.ProcessRunning(exe) }

func devInstalledDataDir() string {
	localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
	if localAppData == "" {
		home, err := os.UserHomeDir()
		if err != nil || strings.TrimSpace(home) == "" {
			return ""
		}
		localAppData = filepath.Join(home, "AppData", "Local")
	}
	return filepath.Join(localAppData, appdirs.AppNameForChannel("dev"))
}

func (winProcesses) WindowOpen(exe string) (bool, error) {
	return build.WindowOpenForDataDir(exe, devInstalledDataDir())
}

// RequestQuit starts the executable with --quit-for-update: as a second
// instance it hands the flag to the running TRACE Dev and exits. The helper
// call is bounded so a hung single-instance pipe cannot block -install forever.
func (winProcesses) RequestQuit(exe string) error {
	ctx, cancel := context.WithTimeout(context.Background(), quitRequestTimeout)
	defer cancel()
	return build.RunQuietContext(ctx, exe, []string{flagQuitUpdate}, build.AppEnv())
}

// Launch starts TRACE Dev so that it outlives this command: inside a job
// object through the desktop shell first, then by breaking away when the job
// allows it (see build.LaunchInstalledOutsideJob).
func (winProcesses) Launch(exe string, args []string) (bool, error) {
	return build.LaunchInstalledOutsideJob(exe, args, build.AppEnv())
}

func runSetup(setup string) error { return build.RunQuiet(setup, []string{"/S"}, build.AppEnv()) }

func platformInstaller() (uninstallReader, processes, func(string) error, error) {
	return winRegistry{}, winProcesses{}, runSetup, nil
}
