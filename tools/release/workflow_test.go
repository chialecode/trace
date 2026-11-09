package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// workflowJobs splits release.yml into its top-level parts and its jobs.
func workflowJobs(t *testing.T) (string, map[string]string) {
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
	return head, jobs
}

// release.yml (W8, M2-K11): weekly and manual; every action pinned to a
// full commit; no setup-go; least permissions per job; the signing secret
// only in the build step of the release environment; publishing only
// with the repository variable, and from a job that runs no repository
// code; nothing when main has no new changes.
func TestReleaseWorkflow(t *testing.T) {
	head, jobs := workflowJobs(t)
	if !strings.Contains(head, `cron: "0 0 * * 1"`) || !strings.Contains(head, "workflow_dispatch:") {
		t.Fatal("not weekly on Monday 00:00 UTC and manual")
	}
	if strings.Contains(head, "push:") || strings.Contains(head, "pull_request") {
		t.Fatal("runs on pushes or pull requests")
	}
	if !strings.Contains(head, "permissions: {}") {
		t.Fatal("the workflow grants permissions by default")
	}
	if !regexp.MustCompile(`publish:\n(?:.*\n)*?\s+default: false`).MatchString(head) {
		t.Fatal("a manual run publishes by default")
	}
	for _, name := range []string{"check", "build", "publish"} {
		if jobs[name] == "" {
			t.Fatalf("no %s job", name)
		}
	}
	all := strings.Join([]string{jobs["check"], jobs["build"], jobs["publish"]}, "")
	uses := regexp.MustCompile(`uses:\s*(\S+)`).FindAllStringSubmatch(all, -1)
	if len(uses) == 0 {
		t.Fatal("no actions")
	}
	pinned := regexp.MustCompile(`^actions/(checkout|upload-artifact|download-artifact)@[0-9a-f]{40}$`)
	for _, u := range uses {
		if !pinned.MatchString(u[1]) {
			t.Errorf("action not pinned to a full commit or not allowed: %s", u[1])
		}
	}
	if strings.Contains(all, "setup-go") {
		t.Error("setup-go is used")
	}

	perm := func(job, want string) {
		if !strings.Contains(jobs[job], "permissions:\n      contents: "+want+"\n") {
			t.Errorf("%s job: contents is not %s", job, want)
		}
	}
	perm("check", "read")
	perm("build", "read")
	perm("publish", "write")

	if n := strings.Count(all, "secrets."); n != 1 {
		t.Fatalf("%d secrets", n)
	}
	if !strings.Contains(jobs["build"], "environment: release") ||
		!regexp.MustCompile(`MYGO_UPDATER_PRIVATE_KEY: \$\{\{ secrets\.MYGO_UPDATER_PRIVATE_KEY \}\}\n\s+run: go run ./tools/release\n`).MatchString(jobs["build"]) {
		t.Fatal("the signing key is not only in the release step of the release environment")
	}

	if !strings.Contains(jobs["check"], "go run ./tools/release -plan") || !strings.Contains(jobs["build"], "if: needs.check.outputs.release == 'true'") {
		t.Fatal("the build does not depend on changes since the last release")
	}
	pub := jobs["publish"]
	if !strings.Contains(pub, "vars.TRACE_RELEASE_PUBLISH == '1'") || !strings.Contains(pub, "needs.check.outputs.release == 'true'") {
		t.Fatal("publishing is not behind the repository variable")
	}
	if strings.Contains(pub, "checkout") || strings.Contains(pub, "go run") {
		t.Fatal("the publish job runs repository code")
	}
	if !strings.Contains(pub, "sha256sum -c SHA256SUMS") || !strings.Contains(pub, "--draft") {
		t.Fatal("the publish job does not verify before it publishes")
	}
	if strings.Contains(jobs["check"]+jobs["build"], "contents: write") {
		t.Fatal("a job before publish can write")
	}
}
