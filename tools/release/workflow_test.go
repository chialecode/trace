package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// workflowJobs splits release.yml into its full text, top-level parts, and jobs.
func workflowJobs(t *testing.T) (string, string, map[string]string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	head, body, ok := strings.Cut(text, "\njobs:\n")
	if !ok {
		t.Fatal("release.yml has no jobs")
	}
	jobs := map[string]string{}
	name := ""
	for _, line := range strings.Split(body, "\n") {
		if m := regexp.MustCompile(`^  ([a-z-]+):\s*$`).FindStringSubmatch(line); m != nil {
			name = m[1]
			continue
		}
		if name != "" {
			jobs[name] += line + "\n"
		}
	}
	return text, head, jobs
}

// workflowTriggers parses the trigger keys under the top-level `on:` block and
// returns each trigger's indented body.
func workflowTriggers(t *testing.T, head string) map[string]string {
	t.Helper()
	_, afterOn, ok := strings.Cut(head, "\non:\n")
	if !ok {
		t.Fatal("release.yml has no on: section")
	}
	triggers := map[string]string{}
	cur := ""
	for _, line := range strings.Split(afterOn, "\n") {
		if line != "" && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "#") {
			break
		}
		if m := regexp.MustCompile(`^  ([a-z_]+):(.*)$`).FindStringSubmatch(line); m != nil {
			cur = m[1]
			triggers[cur] = strings.TrimSpace(m[2])
			continue
		}
		if cur != "" && strings.TrimSpace(line) != "" && !strings.HasPrefix(strings.TrimSpace(line), "#") {
			if triggers[cur] != "" {
				triggers[cur] += "\n"
			}
			triggers[cur] += line
		}
	}
	return triggers
}

// release.yml: weekly Release Build Check on main only (Monday 00:00 UTC +
// bare workflow_dispatch); check (ubuntu-24.04) and build (windows-2025,
// release environment) only, no publish job; every action pinned to a full
// 40-char commit SHA with persist-credentials: false and no setup-go;
// top-level permissions: {} and contents: read only on every job (zero
// contents: write); signing secret strictly once in build's release step;
// retains 7-day upload-artifact with no download-artifact, gh release, or
// TRACE_RELEASE_PUBLISH.
func TestReleaseWorkflow(t *testing.T) {
	full, head, jobs := workflowJobs(t)

	if !regexp.MustCompile(`(?m)^name: Release Build Check$`).MatchString(head) {
		t.Fatal("workflow name must be Release Build Check")
	}
	if !strings.Contains(head, "concurrency:\n  group: release-build-check\n  cancel-in-progress: false\n") {
		t.Fatal("concurrency must use group release-build-check with cancel-in-progress: false")
	}
	if !strings.Contains(head, "permissions: {}\n") {
		t.Fatal("the workflow must set top-level permissions: {}")
	}

	triggers := workflowTriggers(t, head)
	if len(triggers) != 2 {
		t.Fatalf("expected exactly 2 triggers (schedule, workflow_dispatch), got %v", triggers)
	}
	sched, hasSched := triggers["schedule"]
	if !hasSched || !strings.Contains(sched, `cron: "0 0 * * 1"`) {
		t.Fatalf("schedule trigger must run Monday 00:00 UTC, got %q", sched)
	}
	dispatch, hasDispatch := triggers["workflow_dispatch"]
	if !hasDispatch || dispatch != "" {
		t.Fatalf("workflow_dispatch must be bare with no inputs, got %q", dispatch)
	}
	for _, forbidden := range []string{"inputs:", "publish:", "push:", "pull_request"} {
		if strings.Contains(head, forbidden) {
			t.Fatalf("workflow head must not contain %q", forbidden)
		}
	}

	if len(jobs) != 2 || jobs["check"] == "" || jobs["build"] == "" {
		t.Fatalf("expected only check and build jobs, got %d jobs", len(jobs))
	}
	if jobs["publish"] != "" {
		t.Fatal("release.yml must not have a publish job")
	}

	checkJob := jobs["check"]
	buildJob := jobs["build"]
	if !strings.Contains(checkJob, "    runs-on: ubuntu-24.04\n") || strings.Contains(checkJob, "ubuntu-latest") {
		t.Fatal("check job must run on ubuntu-24.04, not ubuntu-latest")
	}
	if !strings.Contains(buildJob, "    runs-on: windows-2025\n") {
		t.Fatal("build job must run on windows-2025")
	}
	if !strings.Contains(buildJob, "    needs: [check]\n") {
		t.Fatal("build job must depend on check")
	}
	if !strings.Contains(buildJob, "    environment: release\n") {
		t.Fatal("build job must run in the release environment")
	}

	const mainGuard = "    if: github.ref == 'refs/heads/main'\n"
	if !strings.Contains(checkJob, mainGuard) {
		t.Fatal("check job must guard with if: github.ref == 'refs/heads/main'")
	}
	if !strings.Contains(buildJob, mainGuard) {
		t.Fatal("build job must guard with if: github.ref == 'refs/heads/main'")
	}
	for _, skipGate := range []string{"needs.check.outputs.release", "steps.plan.outputs.release"} {
		if strings.Contains(full, skipGate) {
			t.Fatalf("release.yml must not contain skip gate %q", skipGate)
		}
	}

	if !strings.Contains(checkJob, "go run ./tools/release -plan") {
		t.Fatal("check job must run go run ./tools/release -plan")
	}
	if !regexp.MustCompile(`(?m)^      - name: Run quality gates\n        run: go run \./tools/check$`).MatchString(checkJob) {
		t.Fatal("check job must unconditionally run go run ./tools/check without an if gate")
	}

	for name, body := range jobs {
		if !strings.Contains(body, "    permissions:\n      contents: read\n") {
			t.Errorf("%s job must set permissions: contents: read", name)
		}
	}
	if strings.Contains(full, "contents: write") {
		t.Fatal("release.yml must never grant contents: write")
	}

	all := checkJob + buildJob
	uses := regexp.MustCompile(`uses:\s*(\S+)`).FindAllStringSubmatch(all, -1)
	if len(uses) == 0 {
		t.Fatal("no actions")
	}
	pinned := regexp.MustCompile(`^actions/(checkout|upload-artifact)@[0-9a-f]{40}$`)
	checkoutCount := 0
	uploadCount := 0
	for _, u := range uses {
		if !pinned.MatchString(u[1]) {
			t.Errorf("action not pinned to a full commit or not allowed in release.yml: %s", u[1])
		}
		if strings.HasPrefix(u[1], "actions/checkout@") {
			checkoutCount++
		}
		if strings.HasPrefix(u[1], "actions/upload-artifact@") {
			uploadCount++
		}
	}
	if checkoutCount != 2 {
		t.Fatalf("expected 2 actions/checkout steps, got %d", checkoutCount)
	}
	if n := strings.Count(all, "persist-credentials: false"); n != checkoutCount {
		t.Fatalf("every actions/checkout step must set persist-credentials: false (checkouts=%d, persist-credentials=%d)", checkoutCount, n)
	}
	if uploadCount != 1 {
		t.Fatalf("expected 1 actions/upload-artifact step in build, got %d", uploadCount)
	}
	if !strings.Contains(buildJob, "uses: actions/upload-artifact@") ||
		!strings.Contains(buildJob, "retention-days: 7\n") ||
		!strings.Contains(buildJob, "if-no-files-found: error\n") {
		t.Fatal("build job must retain actions/upload-artifact with retention-days: 7 and if-no-files-found: error")
	}

	for _, forbidden := range []string{"setup-go", "download-artifact", "gh release", "TRACE_RELEASE_PUBLISH"} {
		if strings.Contains(full, forbidden) {
			t.Fatalf("release.yml must not contain %q", forbidden)
		}
	}

	if n := strings.Count(full, "secrets."); n != 1 {
		t.Fatalf("expected exactly 1 secret reference, got %d", n)
	}
	if strings.Contains(checkJob, "secrets.") {
		t.Fatal("check job must not reference any secrets")
	}
	if !regexp.MustCompile(`MYGO_UPDATER_PRIVATE_KEY: \$\{\{ secrets\.MYGO_UPDATER_PRIVATE_KEY \}\}\n\s+run: go run \./tools/release\n`).MatchString(buildJob) {
		t.Fatal("MYGO_UPDATER_PRIVATE_KEY must be used only in the go run ./tools/release step of build")
	}
}

func TestCIWorkflowHardening(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if !strings.Contains(text, "persist-credentials: false") {
		t.Fatal("ci.yml checkout must set persist-credentials: false")
	}
}
