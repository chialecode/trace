package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
)

var allowedMyGoPackages = map[string]bool{
	"github.com/chialecode/trace/internal/shell":  true,
	"github.com/chialecode/trace/internal/ui":     true,
	"github.com/chialecode/trace/internal/player": true,
	"github.com/chialecode/trace/tools/uishots":   true,
}

var forbiddenBilibiliDeps = []string{
	"github.com/chialecode/trace/internal/storage",
	"github.com/chialecode/trace/internal/library",
	"github.com/chialecode/trace/internal/download",
	"github.com/chialecode/trace/internal/subscription",
	"github.com/chialecode/trace/internal/live",
	"github.com/chialecode/trace/internal/service",
	"github.com/chialecode/trace/internal/ui",
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
		{"dependency direction (INV-05)", checkDependencyDirection},
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
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "TRACE_QUIET=1")
	appdirs.SetSubprocessQuiet(cmd)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

func checkGofmt() error {
	out, err := runCmd("gofmt", "-l", "main.go", "internal", "tools")
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

func checkDependencyDirection() error {
	out, err := runCmd("go", "list", "-f", "{{.ImportPath}}|{{join .Deps \",\"}}", "./internal/...", "./tools/...")
	if err != nil {
		return fmt.Errorf("go list failed: %w\n%s", err, out)
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pkg, depsStr, ok := strings.Cut(line, "|")
		if !ok {
			continue
		}
		deps := strings.Split(depsStr, ",")
		for _, dep := range deps {
			if strings.HasPrefix(dep, "github.com/egoist/mygo") && !allowedMyGoPackages[pkg] {
				return fmt.Errorf("INV-05 violation: package %s depends on %s", pkg, dep)
			}
		}
		if pkg == "github.com/chialecode/trace/internal/site/bilibili" {
			depSet := make(map[string]bool, len(deps))
			for _, d := range deps {
				depSet[d] = true
			}
			for _, forbidden := range forbiddenBilibiliDeps {
				if depSet[forbidden] {
					return fmt.Errorf("layer violation: %s depends on %s", pkg, forbidden)
				}
			}
		}
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
