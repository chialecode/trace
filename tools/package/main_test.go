package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/tools/internal/build"
)

func runTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	// Clear inherited worktree git env vars (e.g. from pre-push hooks) so temp
	// repos stay isolated.
	env := os.Environ()
	for _, k := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR"} {
		env = build.UnsetEnvVar(env, k)
	}
	env = append(env,
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@example.invalid",
	)
	cmd.Env = env
	appdirs.SetSubprocessQuiet(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestDevInfoAndInstallSupportCleanPRBranch verifies that tools/package builds
// and installs TRACE Dev from a clean non-main PR branch (so local dev builds
// can be installed for user testing before PR review/merge).
func TestDevInfoAndInstallSupportCleanPRBranch(t *testing.T) {
	dir := t.TempDir()
	runTestGit(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "mygo.json"), []byte(`{"name":"trace","version":"0.1.0"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, dir, "add", "mygo.json")
	runTestGit(t, dir, "commit", "-q", "-m", "initial")
	runTestGit(t, dir, "tag", "v0.1.2")

	// Switch to a PR branch and add a clean commit.
	runTestGit(t, dir, "checkout", "-q", "-b", "fix/152-skip-start-menu-on-dev-reinstall")
	if err := os.WriteFile(filepath.Join(dir, "change.txt"), []byte("pr change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, dir, "add", "change.txt")
	runTestGit(t, dir, "commit", "-q", "-m", "fix: sample pr change")
	headShort := runTestGit(t, dir, "rev-parse", "--short=12", "HEAD")

	now := time.Date(2026, 11, 27, 4, 30, 0, 0, time.UTC)
	info, _, err := devInfo(dir, now)
	if err != nil {
		t.Fatalf("devInfo on clean PR branch failed: %v", err)
	}
	if info.Channel != build.ChannelDev || info.Version != "0.1.3-dev.202611270430" || info.Commit != headShort {
		t.Fatalf("unexpected devInfo on PR branch: %+v (want commit %s)", info, headShort)
	}
	if strings.HasSuffix(info.Commit, "-dirty") {
		t.Fatalf("clean PR branch marked dirty: %+v", info)
	}

	// Verify devInstaller upgrades an existing TRACE Dev install to the PR branch build.
	m := newMachine().withDev("0.1.3-dev.202611261215", true)
	m.installsVer = info.Version
	m.quitAfter = time.Second
	d := m.installer()
	d.Version = info.Version
	res, err := d.Run()
	if err != nil {
		t.Fatalf("devInstaller on PR branch build failed: %v", err)
	}
	if !res.Installed || res.FirstInstall || string(res.NewVersion) != info.Version || !res.Relaunched || !res.Background {
		t.Fatalf("unexpected install result: %+v", res)
	}
	stableUntouched(t, m)

	// Uncommitted edits are marked -dirty in commit metadata.
	if err := os.WriteFile(filepath.Join(dir, "change.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirtyInfo, _, err := devInfo(dir, now)
	if err != nil {
		t.Fatalf("devInfo on dirty PR branch failed: %v", err)
	}
	if dirtyInfo.Commit != headShort+"-dirty" {
		t.Fatalf("dirty commit = %q, want %q", dirtyInfo.Commit, headShort+"-dirty")
	}
}
