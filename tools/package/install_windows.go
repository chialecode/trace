//go:build windows

package main

import "github.com/chialecode/trace/tools/internal/build"

type winRegistry struct{}

func (winRegistry) Lookup(identifier string) (installEntry, bool, error) {
	e, ok, err := build.LookupInstall(identifier)
	return installEntry{Location: e.Location, Version: e.Version}, ok, err
}

type winProcesses struct{}

func (winProcesses) Running(exe string) (bool, error) { return build.ProcessRunning(exe) }

// RequestQuit starts the executable with --quit-for-update: as a second
// instance it hands the flag to the running TRACE Dev and exits.
func (winProcesses) RequestQuit(exe string) error {
	return build.RunQuiet(exe, []string{flagQuitUpdate}, build.AppEnv())
}

// Launch starts TRACE Dev detached from this tool and outside its job
// object when the job allows it.
func (winProcesses) Launch(exe string, args []string) (bool, error) {
	return build.LaunchDetachedOutsideJob(exe, args, build.AppEnv())
}

func runSetup(setup string) error { return build.RunQuiet(setup, []string{"/S"}, build.AppEnv()) }

func platformInstaller() (uninstallReader, processes, func(string) error, error) {
	return winRegistry{}, winProcesses{}, runSetup, nil
}
