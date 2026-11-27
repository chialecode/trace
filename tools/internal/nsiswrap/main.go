// Command nsiswrap is a build-time makensis shim invoked by
// tools/internal/build.MygoBuild. It patches MyGo's generated installer.nsi
// via build.PatchInstallerScript (so Start Menu shortcut creation delegates to
// customStartMenuShortcut and dev/test channels never strip stable shortcuts)
// before forwarding all arguments to the real makensis binary.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/tools/internal/build"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	realMakensis := strings.TrimSpace(os.Getenv(build.EnvRealMakensis))
	if realMakensis == "" {
		fmt.Fprintf(os.Stderr, "nsiswrap: %s is not set\n", build.EnvRealMakensis)
		return 1
	}
	if self, err := os.Executable(); err == nil && sameExecutable(self, realMakensis) {
		fmt.Fprintf(os.Stderr, "nsiswrap: %s points to wrapper itself (%s)\n", build.EnvRealMakensis, realMakensis)
		return 1
	}

	patchedCount := 0
	dumpPath := strings.TrimSpace(os.Getenv(build.EnvNSISWrapDump))
	for _, arg := range args {
		if !isNSIScriptArg(arg, runtime.GOOS) {
			continue
		}
		raw, err := os.ReadFile(arg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "nsiswrap: read %s: %v\n", arg, err)
			return 1
		}
		patched, err := build.PatchInstallerScript(raw)
		if err != nil {
			fmt.Fprintf(os.Stderr, "nsiswrap: patch %s: %v\n", arg, err)
			return 1
		}
		if err := os.WriteFile(arg, patched, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "nsiswrap: write %s: %v\n", arg, err)
			return 1
		}
		if dumpPath != "" {
			if err := os.MkdirAll(filepath.Dir(dumpPath), 0o755); err != nil {
				fmt.Fprintf(os.Stderr, "nsiswrap: mkdir dump dir: %v\n", err)
				return 1
			}
			if err := os.WriteFile(dumpPath, patched, 0o644); err != nil {
				fmt.Fprintf(os.Stderr, "nsiswrap: write dump %s: %v\n", dumpPath, err)
				return 1
			}
		}
		patchedCount++
	}

	cmd := exec.Command(realMakensis, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	env := os.Environ()
	env = build.UnsetEnvVar(env, build.EnvRealMakensis)
	env = build.UnsetEnvVar(env, build.EnvNSISWrapReceipt)
	env = build.UnsetEnvVar(env, build.EnvNSISWrapDump)
	env = build.UnsetEnvVar(env, "MYGO_UPDATER_PRIVATE_KEY")
	cmd.Env = env
	appdirs.SetSubprocessQuiet(cmd)

	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "nsiswrap: run %s: %v\n", realMakensis, err)
		return 1
	}

	if patchedCount > 0 {
		if receipt := strings.TrimSpace(os.Getenv(build.EnvNSISWrapReceipt)); receipt != "" {
			if err := os.WriteFile(receipt, []byte("patched\n"), 0o644); err != nil {
				fmt.Fprintf(os.Stderr, "nsiswrap: write receipt %s: %v\n", receipt, err)
				return 1
			}
		}
	}
	return 0
}

func isNSIScriptArg(arg, goos string) bool {
	if arg == "" || !strings.EqualFold(filepath.Ext(arg), ".nsi") {
		return false
	}
	if strings.HasPrefix(arg, "-") {
		return false
	}
	// On Windows, NSIS accepts "/" flags (e.g. /DFOO=bar.nsi); on Unix, "/"
	// begins absolute paths such as /tmp/.../installer.nsi.
	if goos == "windows" && strings.HasPrefix(arg, "/") {
		return false
	}
	return true
}

func sameExecutable(a, b string) bool {
	resolve := func(p string) string {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		if eval, err := filepath.EvalSymlinks(p); err == nil {
			p = eval
		}
		return filepath.Clean(p)
	}
	ra, rb := resolve(a), resolve(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(ra, rb)
	}
	return ra == rb
}
