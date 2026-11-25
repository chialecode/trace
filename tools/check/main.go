package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
)

const (
	modulePrefix   = "github.com/chialecode/trace/"
	internalPref   = modulePrefix + "internal/"
	sitePkg        = internalPref + "site"
	bilibiliPkg    = internalPref + "site/bilibili"
	douyinPkg      = internalPref + "site/douyin"
	douyinSignPkg  = internalPref + "site/douyin/sign"
	douyinGuardPkg = internalPref + "site/douyin/guard"
	weiboPkg       = internalPref + "site/weibo"
	xPkg           = internalPref + "site/x"
	credPkg        = internalPref + "credential"
	uiPkg          = internalPref + "ui"
	serverCmd      = modulePrefix + "cmd/trace-server"
)

// serverForbidden are the packages trace-server must not link: the window,
// player and shell, so the Linux build stays small and needs no cgo
// (docs/design/remote.md §2).
var serverForbidden = []string{
	"github.com/egoist/mygo",
	internalPref + "ui",
	internalPref + "player",
	internalPref + "shell",
	internalPref + "locales",
}

var allowedMyGoPackages = map[string]bool{
	internalPref + "shell":  true,
	internalPref + "ui":     true,
	internalPref + "player": true,
	// The Chinese catalogs of MyGo's own strings and of the installer.
	internalPref + "locales":       true,
	modulePrefix + "tools/uishots": true,
}

// Legacy packages permitted to directly import internal/site/bilibili.
// No new internal package (including storage, settings, uistate, or subpackages) may be added.
var allowedLegacyBilibiliImporters = map[string]bool{
	internalPref + "download":     true,
	internalPref + "library":      true,
	internalPref + "live":         true,
	internalPref + "service":      true,
	internalPref + "subscription": true,
	internalPref + "ui":           true,
}

var forbiddenUpperSiteDeps = []string{
	internalPref + "storage",
	internalPref + "library",
	internalPref + "download",
	internalPref + "subscription",
	internalPref + "live",
	internalPref + "service",
	internalPref + "ui",
}

var siteAgnosticCorePackages = []string{
	internalPref + "download",
	internalPref + "live",
	internalPref + "subscription",
	internalPref + "library",
	internalPref + "storage",
	internalPref + "settings",
	internalPref + "uistate",
}

var zeroBilibiliDepsPackages = []string{
	internalPref + "storage",
	internalPref + "settings",
	internalPref + "uistate",
}

func main() {
	appdirs.LowerCurrentProcessPriority()
	start := time.Now()

	steps := []struct {
		name string
		fn   func() error
	}{
		{"gofmt -l", checkGofmt},
		{"go vet ./...", checkGoVet},
		{"go test ./...", checkGoTest},
		{"dependency direction", checkDependencyDirection},
		{"trace-server linux build", checkServerBuild},
		{"tools/checkdocs", checkDocs},
		{"git diff --check HEAD", checkGitDiff},
	}

	for _, s := range steps {
		stepStart := time.Now()
		fmt.Printf("==> [check] %s ...\n", s.name)
		if err := s.fn(); err != nil {
			fmt.Fprintf(os.Stderr, "FAIL [check] %s (%s): %v\n", s.name, time.Since(stepStart).Round(time.Millisecond), err)
			os.Exit(1)
		}
		fmt.Printf("OK  [check] %s (%s)\n", s.name, time.Since(stepStart).Round(time.Millisecond))
	}

	fmt.Printf("ALL CHECKS PASSED in %s\n", time.Since(start).Round(time.Millisecond))
}

func runCmd(name string, args ...string) (string, error) {
	return runCmdWithEnv(nil, name, args...)
}

func runCmdWithEnv(extraEnv []string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "TRACE_QUIET=1")
	cmd.Env = append(cmd.Env, extraEnv...)
	appdirs.SetSubprocessQuiet(cmd)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

func checkGofmt() error {
	out, err := runCmd("gofmt", "-l", "main.go", "cmd", "internal", "tools")
	if err != nil {
		return fmt.Errorf("gofmt failed: %w\n%s", err, out)
	}
	if strings.TrimSpace(out) != "" {
		return fmt.Errorf("unformatted Go files:\n%s", out)
	}
	return nil
}

func checkGoVet() error {
	out, err := runCmd("go", "vet", "./...")
	if err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	return nil
}

func checkGoTest() error {
	halfCores := runtime.NumCPU() / 2
	if halfCores < 1 {
		halfCores = 1
	}
	out, err := runCmd("go", "test", "-p", strconv.Itoa(halfCores), "./...")
	if err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	fmt.Print(out)
	return nil
}

func dependencyCheckTargets(hostGOOS string) []string {
	if hostGOOS == "" || hostGOOS == "linux" {
		return []string{"linux"}
	}
	return []string{hostGOOS, "linux"}
}

func checkDependencyDirection() error {
	return checkDependencyDirectionWith(dependencyCheckTargets(runtime.GOOS), func(goos string) (string, error) {
		extra := []string{"GOOS=" + goos}
		if goos == "linux" {
			extra = append(extra, "GOARCH=amd64")
		}
		return runCmdWithEnv(extra, "go", "list", "-f", "{{.ImportPath}}|{{join .Imports \",\"}}|{{join .Deps \",\"}}", "./cmd/...", "./internal/...", "./tools/...")
	})
}

func checkDependencyDirectionWith(targets []string, listFn func(goos string) (string, error)) error {
	for _, goos := range targets {
		out, err := listFn(goos)
		if err != nil {
			return fmt.Errorf("go list (GOOS=%s) failed: %w\n%s", goos, err, out)
		}
		if err := validateDependencyOutput(out); err != nil {
			return fmt.Errorf("GOOS=%s: %w", goos, err)
		}
	}
	return nil
}

// validateDependencyOutput validates newline-separated `go list` records formatted either as
// "pkg|imports|deps" (3 columns) or synthetic "pkg|deps" (2 columns).
func validateDependencyOutput(raw string) error {
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) < 2 {
			continue
		}
		pkg := strings.TrimSpace(parts[0])
		if pkg == "" {
			continue
		}
		var imports, deps []string
		if len(parts) == 2 {
			deps = splitCSVPackages(parts[1])
			imports = deps
		} else {
			imports = splitCSVPackages(parts[1])
			deps = unionPackages(imports, splitCSVPackages(parts[2]))
		}
		if err := validatePackageDeps(pkg, imports, deps); err != nil {
			return err
		}
	}
	return nil
}

func splitCSVPackages(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	raw := strings.Split(s, ",")
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		trimmed := strings.TrimSpace(item)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func unionPackages(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, list := range [][]string{a, b} {
		for _, item := range list {
			if !seen[item] {
				seen[item] = true
				out = append(out, item)
			}
		}
	}
	return out
}

func hasPkgOrSubpkg(target string, base string) bool {
	return target == base || strings.HasPrefix(target, base+"/")
}

func isStdlibImportPath(path string) bool {
	if strings.HasPrefix(path, modulePrefix) {
		return false
	}
	first, _, _ := strings.Cut(path, "/")
	return !strings.Contains(first, ".")
}

func validatePackageDeps(pkg string, imports []string, deps []string) error {
	// 1. MyGo UI boundary (INV-05): only shell, ui, player, locales, and tools/uishots may depend on MyGo.
	for _, dep := range deps {
		if strings.HasPrefix(dep, "github.com/egoist/mygo") && !allowedMyGoPackages[pkg] {
			return fmt.Errorf("INV-05 violation: package %s depends on %s", pkg, dep)
		}
	}

	// 2. internal/site must use the Go standard library only (zero internal/... or external module deps).
	if pkg == sitePkg {
		for _, dep := range deps {
			if dep == pkg {
				continue
			}
			if !isStdlibImportPath(dep) {
				return fmt.Errorf("site stdlib-only violation: %s depends on %s", pkg, dep)
			}
		}
	}

	// 3. internal/site/bilibili (and subpackages): no upper-layer deps and no cross-site deps.
	if hasPkgOrSubpkg(pkg, bilibiliPkg) {
		for _, dep := range deps {
			for _, forbidden := range forbiddenUpperSiteDeps {
				if hasPkgOrSubpkg(dep, forbidden) {
					return fmt.Errorf("layer violation: %s depends on %s", pkg, dep)
				}
			}
			if hasPkgOrSubpkg(dep, douyinPkg) || hasPkgOrSubpkg(dep, weiboPkg) || hasPkgOrSubpkg(dep, xPkg) {
				return fmt.Errorf("cross-site violation: %s depends on %s", pkg, dep)
			}
		}
	}

	// 4. internal/site/douyin (and subpackages, including sign): no upper-layer deps and no cross-site deps.
	if hasPkgOrSubpkg(pkg, douyinPkg) {
		for _, dep := range deps {
			for _, forbidden := range forbiddenUpperSiteDeps {
				if hasPkgOrSubpkg(dep, forbidden) {
					return fmt.Errorf("layer violation: %s depends on %s", pkg, dep)
				}
			}
			if hasPkgOrSubpkg(dep, bilibiliPkg) || hasPkgOrSubpkg(dep, weiboPkg) || hasPkgOrSubpkg(dep, xPkg) {
				return fmt.Errorf("cross-site violation: %s depends on %s", pkg, dep)
			}
		}
	}

	// 4b. internal/site/weibo (and subpackages): no upper-layer deps and no cross-site deps.
	if hasPkgOrSubpkg(pkg, weiboPkg) {
		for _, dep := range deps {
			for _, forbidden := range forbiddenUpperSiteDeps {
				if hasPkgOrSubpkg(dep, forbidden) {
					return fmt.Errorf("layer violation: %s depends on %s", pkg, dep)
				}
			}
			if hasPkgOrSubpkg(dep, bilibiliPkg) || hasPkgOrSubpkg(dep, douyinPkg) || hasPkgOrSubpkg(dep, xPkg) {
				return fmt.Errorf("cross-site violation: %s depends on %s", pkg, dep)
			}
		}
	}

	// 4c. internal/site/x (and subpackages): no upper-layer deps and no cross-site deps.
	if hasPkgOrSubpkg(pkg, xPkg) {
		for _, dep := range deps {
			for _, forbidden := range forbiddenUpperSiteDeps {
				if hasPkgOrSubpkg(dep, forbidden) {
					return fmt.Errorf("layer violation: %s depends on %s", pkg, dep)
				}
			}
			if hasPkgOrSubpkg(dep, bilibiliPkg) || hasPkgOrSubpkg(dep, douyinPkg) || hasPkgOrSubpkg(dep, weiboPkg) {
				return fmt.Errorf("cross-site violation: %s depends on %s", pkg, dep)
			}
		}
	}

	// 5. internal/site/douyin/sign and internal/site/douyin/guard must remain pure computation
	// (no internal/credential and no net/http).
	if hasPkgOrSubpkg(pkg, douyinSignPkg) || hasPkgOrSubpkg(pkg, douyinGuardPkg) {
		for _, dep := range deps {
			if hasPkgOrSubpkg(dep, credPkg) {
				return fmt.Errorf("sign purity violation: %s depends on %s", pkg, dep)
			}
			if hasPkgOrSubpkg(dep, "net/http") {
				return fmt.Errorf("sign purity violation: %s depends on %s", pkg, dep)
			}
		}
	}

	// 6. Core managers & storage packages must never depend on internal/site/douyin, internal/site/weibo, or internal/site/x.
	for _, corePkg := range siteAgnosticCorePackages {
		if hasPkgOrSubpkg(pkg, corePkg) {
			for _, dep := range deps {
				if hasPkgOrSubpkg(dep, douyinPkg) || hasPkgOrSubpkg(dep, weiboPkg) || hasPkgOrSubpkg(dep, xPkg) {
					return fmt.Errorf("decoupling violation: %s depends on %s", pkg, dep)
				}
			}
		}
	}

	// 7. Lock internal/site/bilibili importers: no new internal package may import internal/site/bilibili,
	// and storage/settings/uistate must not depend on internal/site/bilibili at all.
	if strings.HasPrefix(pkg, internalPref) && !hasPkgOrSubpkg(pkg, bilibiliPkg) {
		if !allowedLegacyBilibiliImporters[pkg] {
			for _, imp := range imports {
				if hasPkgOrSubpkg(imp, bilibiliPkg) {
					return fmt.Errorf("locked bilibili-importer violation: %s directly imports %s", pkg, imp)
				}
			}
		}
		for _, cleanPkg := range zeroBilibiliDepsPackages {
			if hasPkgOrSubpkg(pkg, cleanPkg) {
				for _, dep := range deps {
					if hasPkgOrSubpkg(dep, bilibiliPkg) {
						return fmt.Errorf("locked bilibili-dependency violation: %s depends on %s", pkg, dep)
					}
				}
			}
		}
	}

	// 8. internal/ui must not import internal/site/douyin, internal/site/weibo, or internal/site/x directly (only service DTOs).
	// Transitive dependency via internal/service is permitted.
	if hasPkgOrSubpkg(pkg, uiPkg) {
		for _, imp := range imports {
			if hasPkgOrSubpkg(imp, douyinPkg) || hasPkgOrSubpkg(imp, weiboPkg) || hasPkgOrSubpkg(imp, xPkg) {
				return fmt.Errorf("ui decoupling violation: %s directly imports %s", pkg, imp)
			}
		}
	}

	// 9. trace-server links no window, player or shell code.
	if pkg == serverCmd {
		for _, dep := range deps {
			for _, forbidden := range serverForbidden {
				if hasPkgOrSubpkg(dep, forbidden) {
					return fmt.Errorf("server size violation: %s depends on %s", pkg, dep)
				}
			}
		}
	}

	return nil
}

// checkServerBuild cross-compiles trace-server for linux/amd64 and
// linux/arm64 without cgo and vets the tree for Linux.
func checkServerBuild() error {
	dir, err := os.MkdirTemp("", "trace-check-server-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	for _, arch := range []string{"amd64", "arm64"} {
		cmd := exec.Command("go", "build", "-trimpath", "-o", filepath.Join(dir, "trace-server-"+arch), "./cmd/trace-server")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+arch, "TRACE_QUIET=1")
		appdirs.SetSubprocessQuiet(cmd)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("linux/%s: %w\n%s", arch, err, out)
		}
	}
	cmd := exec.Command("go", "vet", "./...")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64", "TRACE_QUIET=1")
	appdirs.SetSubprocessQuiet(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("GOOS=linux go vet: %w\n%s", err, out)
	}
	return nil
}

func checkDocs() error {
	out, err := runCmd("go", "run", "./tools/checkdocs")
	if err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	fmt.Print(out)
	return nil
}

func checkGitDiff() error {
	out, err := runCmd("git", "diff", "--check", "HEAD")
	if err != nil {
		return fmt.Errorf("git diff --check HEAD found whitespace issues:\n%s", out)
	}
	if strings.TrimSpace(out) != "" {
		return fmt.Errorf("git diff --check HEAD output:\n%s", out)
	}
	return nil
}
