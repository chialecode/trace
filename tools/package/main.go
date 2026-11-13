// Command package builds the development package of TRACE into
// dist/trace-dev/ and, when asked, installs it on this machine (W2):
//
//	go run ./tools/package               # build only
//	go run ./tools/package -install      # build, then install TRACE Dev here
//	go run ./tools/package -install-only # install the package in dist/trace-dev
//
// The agent installs whenever the next step is the person's manual check (a
// stage handed over, or fixes handed back for another look), so the person
// never installs by hand; packaging only for automated checks does not install
// (the person's rule of 2026-11-13, corrected 2026-11-14). The install is skipped with TRACE_DEV_AUTO_INSTALL=0, on CI
// (CI=true) and when TRACE Dev is not installed. Release packages are built
// by tools/release.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/tools/internal/build"
)

func main() {
	appdirs.LowerCurrentProcessPriority()
	install := flag.Bool("install", false, "after building, install the package on this machine (before handing over for the person's checks)")
	installOnly := flag.Bool("install-only", false, "install the package already in dist/trace-dev without building")
	flag.Parse()

	started := time.Now()
	if !*installOnly {
		if err := buildDev(started); err != nil {
			fmt.Fprintf(os.Stderr, "package failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("package (dev) built in %s\n", time.Since(started).Round(time.Millisecond))
	}
	if !*install && !*installOnly {
		fmt.Println("==> [install-dev] not asked (-install): TRACE Dev on this machine is left as it is")
		return
	}
	if err := installDev(filepath.Join("dist", "trace-dev")); err != nil {
		fmt.Fprintf(os.Stderr, "install-dev failed: %v\n", err)
		os.Exit(1)
	}
}

// buildDev builds the development package with its version (W1) and
// replaces the registered files of dist/trace-dev/.
func buildDev(now time.Time) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	base, err := os.ReadFile("mygo.json")
	if err != nil {
		return fmt.Errorf("read mygo.json (run from the repository root): %w", err)
	}
	cfgVersion, err := build.ConfigVersion(base)
	if err != nil {
		return err
	}
	line, err := build.VersionLine(cfgVersion)
	if err != nil {
		return err
	}
	// Development builds use the tags this clone knows; `git fetch --tags`
	// brings in new releases. Nothing here asks GitHub.
	tags, err := build.LocalTags(root)
	if err != nil {
		return err
	}
	commit, dirty, err := build.HeadInfo(root)
	if err != nil {
		return err
	}
	if dirty {
		commit += "-dirty"
	}
	info := build.Info{
		Version:   build.DevVersion(build.NextStable(line, tags), now),
		Channel:   build.ChannelDev,
		Commit:    commit,
		BuildTime: now.UTC().Format(time.RFC3339),
	}
	cfg, err := build.ChannelConfig(base, build.ChannelDev, info.Version, "dist", nil)
	if err != nil {
		return err
	}

	if err := os.MkdirAll("dist", 0o755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp("dist", ".staging-"+now.UTC().Format("20060102-150405")+"-")
	if err != nil {
		return fmt.Errorf("create staging dir: %w", err)
	}
	defer os.RemoveAll(staging)
	stagingAbs, err := filepath.Abs(staging)
	if err != nil {
		return err
	}

	fmt.Printf("==> [package] building TRACE Dev %s (%s)...\n", info.Version, info.Commit)
	err = build.WithConfig("mygo.json", cfg, func() error {
		out, err := build.MygoBuild(root, stagingAbs, build.LDFlags(info, "", ""), nil)
		fmt.Print(out)
		return err
	})
	if err != nil {
		return err
	}

	builtDir := filepath.Join(staging, "windows-amd64")
	setup := build.SetupName(build.ChannelDev, info.Version)
	for _, name := range []string{"trace.exe", setup} {
		st, err := os.Stat(filepath.Join(builtDir, name))
		if err != nil || st.Size() == 0 {
			return fmt.Errorf("missing or empty artifact %s: %v", name, err)
		}
	}
	if err := build.ThirdPartyNotices(root, filepath.Join(builtDir, "THIRD-PARTY-NOTICES.txt")); err != nil {
		return fmt.Errorf("generate THIRD-PARTY-NOTICES.txt: %w", err)
	}
	if err := os.WriteFile(filepath.Join(builtDir, "VERSION.txt"), []byte(build.VersionFile(info)), 0o644); err != nil {
		return err
	}
	files := []string{"trace.exe", setup, "THIRD-PARTY-NOTICES.txt", "VERSION.txt"}
	sums, err := build.WriteSums(builtDir, "SHA256SUMS.txt", files)
	if err != nil {
		return err
	}
	dest := filepath.Join("dist", "trace-dev")
	if err := build.PublishBundle(builtDir, dest, append(files, "SHA256SUMS.txt"), true); err != nil {
		return err
	}
	fmt.Printf("==> [package] staged and verified into %s:\n%s", dest, sums)
	return nil
}

// installDev installs the package of dist/trace-dev on this machine and
// writes artifacts/package/install-dev.json.
func installDev(dir string) error {
	resultPath := filepath.FromSlash(installResultTo)
	if reason := skipReason(os.Getenv); reason != "" {
		fmt.Println("==> [install-dev] " + reason)
		return writeInstallResult(resultPath, installResult{Skipped: true, Reason: reason, At: time.Now().UTC().Format(time.RFC3339)})
	}
	raw, err := os.ReadFile(filepath.Join(dir, "VERSION.txt"))
	if err != nil {
		return fmt.Errorf("no package in %s: %w", dir, err)
	}
	fields := build.ParseVersionFile(string(raw))
	version := fields["version"]
	if fields["channel"] != build.ChannelDev || version == "" {
		return fmt.Errorf("%s/VERSION.txt is not a development package", dir)
	}
	if _, err := build.VerifySums(dir, "SHA256SUMS.txt"); err != nil {
		return fmt.Errorf("package in %s does not match SHA256SUMS.txt: %w", dir, err)
	}
	setup, err := filepath.Abs(filepath.Join(dir, build.SetupName(build.ChannelDev, version)))
	if err != nil {
		return err
	}
	reg, procs, run, err := platformInstaller()
	if err != nil {
		reason := err.Error() + "，跳过"
		fmt.Println("==> [install-dev] " + reason)
		return writeInstallResult(resultPath, installResult{Skipped: true, Reason: reason, NewVersion: version, At: time.Now().UTC().Format(time.RFC3339)})
	}
	// The installed TRACE Dev keeps its data in %LOCALAPPDATA%	race-dev.
	uiState := filepath.Join(os.Getenv("LOCALAPPDATA"), appdirs.AppNameForChannel(build.ChannelDev), "ui-state.json")
	d := &devInstaller{
		Setup: setup, Version: version, Registry: reg, Procs: procs, RunSetup: run,
		WindowState: func() (bool, bool) { return restartWindowState(uiState, time.Now()) },
		Now:         time.Now, Sleep: time.Sleep, Timeout: quitTimeout,
		Logf: func(format string, args ...any) { fmt.Printf(format+"\n", args...) },
	}
	res, runErr := d.Run()
	if err := writeInstallResult(resultPath, res); err != nil {
		return err
	}
	switch {
	case runErr != nil:
		return runErr
	case res.Skipped:
		fmt.Println("==> [install-dev] " + res.Reason)
	default:
		how := "未在运行，未打开"
		if res.Relaunched && res.Background {
			how = "已在托盘重新打开"
		} else if res.Relaunched {
			how = "已重新打开窗口"
		}
		fmt.Printf("==> [install-dev] %s %s → %s，%s（%d ms）\n", devDisplayName, strings.TrimSpace(res.OldVersion), res.NewVersion, how, res.ElapsedMs)
	}
	return nil
}
