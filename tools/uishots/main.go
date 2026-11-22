package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/chialecode/trace/internal/player"
	"github.com/chialecode/trace/internal/shell"
	"github.com/chialecode/trace/internal/ui"
)

func main() {
	outDir := flag.String("out", "artifacts/uishots", "output directory for UI screenshots and assertions.json")
	flag.Parse()

	_ = os.Setenv("TRACE_QUIET", "1")
	_ = shell.SetBelowNormalPriority()

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "uishots failed: resolve cwd: %v\n", err)
		os.Exit(1)
	}
	repoRoot, err := findRepoRoot(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "uishots failed: %v\n", err)
		os.Exit(1)
	}
	rawOut := *outDir
	if rawOut != "" && !filepath.IsAbs(rawOut) {
		rawOut = filepath.Join(cwd, rawOut)
	}
	validatedOut, err := validateCLIOutDir(repoRoot, rawOut)
	if err != nil {
		fmt.Fprintf(os.Stderr, "uishots failed: %v\n", err)
		os.Exit(1)
	}

	if err := runUIShots(validatedOut); err != nil {
		fmt.Fprintf(os.Stderr, "uishots failed: %v\n", err)
		os.Exit(1)
	}
}

func findRepoRoot(startDir string) (string, error) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", err
	}
	for {
		if fi, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil && !fi.IsDir() {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not locate repository root (go.mod) from %s", startDir)
		}
		dir = parent
	}
}

func strictSubpath(base, target string) (string, bool) {
	rel, err := filepath.Rel(base, target)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return rel, true
}

func sameCleanPath(a, b string) bool {
	ca, cb := filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(ca, cb)
	}
	return ca == cb
}

func checkComponentSafeDir(pathAbs, expectedReal string) error {
	fi, err := os.Lstat(pathAbs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("output path %q must not be or traverse a symlink", pathAbs)
	}
	if !fi.IsDir() {
		return fmt.Errorf("output path %q is not a directory", pathAbs)
	}
	actualReal, err := filepath.EvalSymlinks(pathAbs)
	if err != nil {
		return fmt.Errorf("resolve output path %q: %w", pathAbs, err)
	}
	if !sameCleanPath(actualReal, expectedReal) {
		return fmt.Errorf("output path %q resolves through a symlink or junction", pathAbs)
	}
	return nil
}

// validateCLIOutDir enforces engineering.md §5: the CLI -out target must resolve
// strictly inside <repo>/artifacts/<category>/ and must not be or traverse a symlink.
func validateCLIOutDir(repoRoot, outDir string) (string, error) {
	if strings.TrimSpace(outDir) == "" {
		return "", errors.New("output directory must not be empty")
	}
	repoAbs, err := filepath.Abs(repoRoot)
	if err != nil {
		return "", err
	}
	repoReal, err := filepath.EvalSymlinks(repoAbs)
	if err != nil {
		return "", fmt.Errorf("resolve repository root: %w", err)
	}

	candAbs := outDir
	if !filepath.IsAbs(candAbs) {
		candAbs = filepath.Join(repoAbs, candAbs)
	}
	candAbs = filepath.Clean(candAbs)

	artifactsAbs := filepath.Join(repoAbs, "artifacts")
	artifactsReal := filepath.Join(repoReal, "artifacts")

	rel, ok := strictSubpath(artifactsAbs, candAbs)
	if !ok {
		rel, ok = strictSubpath(artifactsReal, candAbs)
	}
	if !ok {
		return "", fmt.Errorf("output directory %q must be a subdirectory inside %s", outDir, artifactsAbs)
	}

	if err := checkComponentSafeDir(artifactsAbs, artifactsReal); err != nil {
		return "", err
	}
	curAbs := artifactsAbs
	curReal := artifactsReal
	for _, seg := range strings.Split(rel, string(filepath.Separator)) {
		if seg == "" || seg == "." {
			continue
		}
		curAbs = filepath.Join(curAbs, seg)
		curReal = filepath.Join(curReal, seg)
		if err := checkComponentSafeDir(curAbs, curReal); err != nil {
			return "", err
		}
	}
	return filepath.Join(artifactsAbs, rel), nil
}

func isGeneratedShotFile(name string) bool {
	lower := strings.ToLower(name)
	return lower == "assertions.json" || strings.HasSuffix(lower, ".png")
}

// prepareCleanOutDir rejects symlinks/junctions at outDir and deletes only known
// generated output files (*.png and assertions.json), preserving all other files.
func prepareCleanOutDir(outDir string) error {
	if strings.TrimSpace(outDir) == "" {
		return errors.New("output directory must not be empty")
	}
	absOut, err := filepath.Abs(outDir)
	if err != nil {
		return err
	}
	if fi, err := os.Lstat(absOut); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("output directory %q must not be a symlink", outDir)
		}
		if !fi.IsDir() {
			return fmt.Errorf("output path %q is not a directory", outDir)
		}
		parentReal, pErr := filepath.EvalSymlinks(filepath.Dir(absOut))
		selfReal, sErr := filepath.EvalSymlinks(absOut)
		if pErr == nil && sErr == nil && !sameCleanPath(selfReal, filepath.Join(parentReal, filepath.Base(absOut))) {
			return fmt.Errorf("output directory %q must not be a symlink or junction", outDir)
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	if err := os.MkdirAll(absOut, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(absOut)
	if err != nil {
		return err
	}
	for _, ent := range entries {
		if !ent.Type().IsRegular() || !isGeneratedShotFile(ent.Name()) {
			continue
		}
		if err := os.Remove(filepath.Join(absOut, ent.Name())); err != nil {
			return err
		}
	}
	return nil
}

func runUIShots(outDir string) error {
	defer ui.UseAccent("blue")

	if err := prepareCleanOutDir(outDir); err != nil {
		return err
	}

	env, err := seedFixtures()
	if err != nil {
		return err
	}
	defer env.close()

	app := ui.NewApp(env.backend, player.NewNullAudioSink())
	defer app.Close()

	env.seedRecentAndLive(app)
	composeGuardPassed := verifyComposeGuard(app)
	scenes := buildAllScenes(env, app)
	return runScenesAndWriteReport(outDir, env, app, scenes, composeGuardPassed)
}
