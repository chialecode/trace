// Command package builds the development package of TRACE into
// dist/trace-dev/ and, when asked, installs it on this machine:
//
//	go run ./tools/package               # build only
//	go run ./tools/package -install      # build, then install TRACE Dev here
//	go run ./tools/package -install-only # install the package in dist/trace-dev
//	go run ./tools/package -server       # build trace-server for Linux into dist/trace-server
//
// The agent installs after every merge into main and before the person's
// manual checks, so the person never installs by hand; packaging only for
// automated checks does not install (engineering.md 2.2). A machine without
// TRACE Dev gets its first install the same way. The install is skipped with
// TRACE_DEV_AUTO_INSTALL=0, on CI (CI=true) and while a TRACE Dev window
// stays open for 10 minutes. Release packages are built by tools/release.
package main

import (
	"errors"
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
	server := flag.Bool("server", false, "build the trace-server packages for linux/amd64 and linux/arm64 into dist/trace-server")
	flag.Parse()

	started := time.Now()
	if *server {
		if *install || *installOnly {
			fmt.Fprintln(os.Stderr, "package: -server cannot be combined with -install")
			os.Exit(2)
		}
		if err := buildServer(started); err != nil {
			fmt.Fprintf(os.Stderr, "package -server failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("package (server) built in %s\n", time.Since(started).Round(time.Millisecond))
		return
	}
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

// devInfo is the version of a development build made now, from mygo.json
// and the tags this clone knows.
func devInfo(root string, now time.Time) (build.Info, []byte, error) {
	base, err := os.ReadFile(filepath.Join(root, "mygo.json"))
	if err != nil {
		return build.Info{}, nil, fmt.Errorf("read mygo.json (run from the repository root): %w", err)
	}
	cfgVersion, err := build.ConfigVersion(base)
	if err != nil {
		return build.Info{}, nil, err
	}
	line, err := build.VersionLine(cfgVersion)
	if err != nil {
		return build.Info{}, nil, err
	}
	// Development builds use the tags this clone knows; `git fetch --tags`
	// brings in new releases. Nothing here asks GitHub.
	tags, err := build.LocalTags(root)
	if err != nil {
		return build.Info{}, nil, err
	}
	commit, dirty, err := build.HeadInfo(root)
	if err != nil {
		return build.Info{}, nil, err
	}
	if dirty {
		commit += "-dirty"
	}
	return build.Info{
		Version:   build.DevVersion(build.NextStable(line, tags), now),
		Channel:   build.ChannelDev,
		Commit:    commit,
		BuildTime: now.UTC().Format(time.RFC3339),
	}, base, nil
}

// buildDev builds the development package with its version and
// replaces the registered files of dist/trace-dev/.
func buildDev(now time.Time) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	info, base, err := devInfo(root, now)
	if err != nil {
		return err
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
	at := time.Now().UTC().Format(time.RFC3339)
	skip := func(reason, guidance, version string) error {
		fmt.Println("==> [install-dev] " + guidance)
		return writeInstallResult(resultPath, installResult{Identity: devIdentity, Skipped: true, Reason: reason, Guidance: guidance, NewVersion: version, At: at})
	}
	invalid := func(err error) error {
		res := installResult{Identity: devIdentity, Reason: "package_invalid", Error: err.Error(), At: at}
		if werr := writeInstallResult(resultPath, res); werr != nil {
			return errors.Join(err, werr)
		}
		return err
	}
	if reason, guidance := skipReason(os.Getenv); reason != "" {
		return skip(reason, guidance, "")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "VERSION.txt"))
	if err != nil {
		return invalid(fmt.Errorf("no package in %s: %w", dir, err))
	}
	fields := build.ParseVersionFile(string(raw))
	version := fields["version"]
	if fields["channel"] != build.ChannelDev || version == "" {
		return invalid(fmt.Errorf("%s/VERSION.txt is not a development package", dir))
	}
	if _, err := build.VerifySums(dir, "SHA256SUMS.txt"); err != nil {
		return invalid(fmt.Errorf("package in %s does not match SHA256SUMS.txt: %w", dir, err))
	}
	setup, err := filepath.Abs(filepath.Join(dir, build.SetupName(build.ChannelDev, version)))
	if err != nil {
		return invalid(err)
	}
	reg, procs, run, err := platformInstaller()
	if err != nil {
		return skip("unsupported_platform", err.Error()+"，跳过", version)
	}
	d := &devInstaller{
		Setup: setup, Version: version, Registry: reg, Procs: procs, RunSetup: run,
		Now: time.Now, Sleep: time.Sleep, Timeout: quitTimeout, WindowWait: windowWait, WindowPoll: windowPoll,
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
		fmt.Println("==> [install-dev] " + res.Guidance)
	default:
		how := "已在托盘打开"
		if res.InJob {
			how += "；它仍在运行这个命令的作业对象里，命令结束后可能被一起结束，请从开始菜单打开一次"
		}
		from := strings.TrimSpace(res.OldVersion)
		if res.FirstInstall {
			from = "首次安装"
		}
		fmt.Printf("==> [install-dev] %s %s → %s，%s（%d ms）\n", devDisplayName, from, res.NewVersion, how, res.ElapsedMs)
	}
	return nil
}
