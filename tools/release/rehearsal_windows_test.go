//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chialecode/trace/tools/internal/build"
)

func TestSplitUninstallCommand(t *testing.T) {
	exe, args := splitCommand(`"C:\Users\u\AppData\Local\Programs\trace-ptest\Uninstall.exe" /S`)
	if exe != `C:\Users\u\AppData\Local\Programs\trace-ptest\Uninstall.exe` || strings.Join(args, " ") != "/S" {
		t.Fatalf("%q %v", exe, args)
	}
	if exe, args := splitCommand(`C:\x\Uninstall.exe`); exe != `C:\x\Uninstall.exe` || len(args) != 0 {
		t.Fatalf("%q %v", exe, args)
	}
}

// The installer rehearsal (W5, M2-K09) installs, updates through a local
// release, rejects a tampered update and uninstalls TRACE under the ptest
// identity. It builds two packages and takes minutes, so it only runs with
// TRACE_RUN_INSTALLER_TEST=1 (go test -timeout 20m); tools/release runs it
// on every release.
func TestInstallerRehearsal(t *testing.T) {
	if os.Getenv("TRACE_RUN_INSTALLER_TEST") != "1" {
		t.Skip("set TRACE_RUN_INSTALLER_TEST=1 to install, update and uninstall TRACE PTest")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	base, err := os.ReadFile(filepath.Join(root, "mygo.json"))
	if err != nil {
		t.Fatal(err)
	}
	commit, dirty, err := build.HeadInfo(root)
	if err != nil {
		t.Fatal(err)
	}
	if dirty {
		commit += "-dirty"
	}
	tags, err := build.LocalTags(root)
	if err != nil {
		t.Fatal(err)
	}
	cfgVersion, _ := build.ConfigVersion(base)
	line, _ := build.VersionLine(cfgVersion)
	res, err := runRehearsal(rehearsalInput{Root: root, Base: base, Version: build.NextStable(line, tags), Commit: commit, Build: build.MygoBuild, Logf: t.Logf})
	if werr := writeJSON(filepath.Join(root, "artifacts", "release", "rehearsal.json"), res); werr != nil {
		t.Error(werr)
	}
	if err != nil {
		t.Fatalf("rehearsal: %v", err)
	}
	if res.Status != "passed" || !res.OthersSame || !res.DataKept || !res.InstallGone {
		t.Fatalf("%+v", res)
	}
	for _, s := range res.Steps {
		if !s.OK {
			t.Fatalf("step %s: %s", s.Name, s.Detail)
		}
	}
}
