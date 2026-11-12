package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/update"
	"github.com/chialecode/trace/tools/internal/build"
)

func testKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, key
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
	appdirs.SetSubprocessQuiet(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newRepo makes a repository with a mygo.json whose update key is pub.
func newRepo(t *testing.T, pub ed25519.PublicKey) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	cfg := `{"name":"trace","slug":"trace","identifier":"io.github.chialecode.trace","version":"0.1.0",` +
		`"updates":{"publicKey":"` + base64.StdEncoding.EncodeToString(pub) + `","github":"chialecode/trace"},` +
		`"windows":{"displayName":"TRACE","installDirName":"trace"}}`
	if err := os.WriteFile(filepath.Join(dir, "mygo.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "CHANGELOG.md"), []byte(changelogHeader+"\n## 0.1.1\n\n- 第一个正式版\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("dist/\nartifacts/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "first")
	return dir
}

// fakeBuild writes what `mygo build` writes for windows/amd64 with
// updates: the program, the installer, the signed archive, the manifest
// and install.ps1. It checks the build gets the channel configuration, the
// key and the feed.
func fakeBuild(t *testing.T, key ed25519.PrivateKey, seen *[]string) func(root, out, ldflags string, env []string) (string, error) {
	return func(root, out, ldflags string, env []string) (string, error) {
		raw, err := os.ReadFile(filepath.Join(root, "mygo.json"))
		if err != nil {
			return "", err
		}
		var cfg struct {
			Version string         `json:"version"`
			Slug    string         `json:"slug"`
			Updates *build.Updates `json:"updates"`
		}
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return "", err
		}
		*seen = append(*seen, cfg.Version)
		if cfg.Updates == nil || cfg.Updates.Changelog == "" {
			t.Errorf("build without updates or changelog: %s", raw)
		}
		hasKey := false
		for _, kv := range env {
			if strings.HasPrefix(kv, keyEnv+"=") {
				hasKey = true
			}
		}
		if !hasKey || !strings.Contains(ldflags, "internal/update.feedURL=https://github.com/chialecode/trace/releases/latest/download/update-windows-amd64.json") {
			t.Errorf("build without key or feed: %v %s", hasKey, ldflags)
		}
		dir := filepath.Join(out, "windows-amd64")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		archive := []byte("archive of " + cfg.Version)
		sum := sha256.Sum256(archive)
		sig := base64.StdEncoding.EncodeToString(ed25519.Sign(key, sum[:]))
		name := cfg.Slug + "-" + cfg.Version + "-windows-amd64.tar.gz"
		manifest, _ := json.MarshalIndent(map[string]any{
			"version": cfg.Version, "url": "https://github.com/chialecode/trace/releases/download/v" + cfg.Version + "/" + name,
			"size": len(archive), "signature": sig, "notes": "- 第一个正式版", "date": "2026-11-09T00:00:00Z",
		}, "", "  ")
		files := map[string][]byte{
			"trace.exe": []byte("exe"), build.SetupName(build.ChannelStable, cfg.Version): []byte("setup"),
			name: archive, manifestName: manifest, "install.ps1": []byte("# install"), ".mygo-files": []byte("trace.exe"),
		}
		for n, data := range files {
			if err := os.WriteFile(filepath.Join(dir, n), data, 0o644); err != nil {
				return "", err
			}
		}
		return "", nil
	}
}

func testOptions(t *testing.T, root string, key ed25519.PrivateKey, seen *[]string) options {
	return options{
		root:     root,
		getenv:   func(k string) string { return map[string]string{keyEnv: base64.StdEncoding.EncodeToString(key)}[k] },
		runCheck: func(string) error { return nil },
		rehearse: func(in rehearsalInput) (rehearsalResult, error) {
			r := newRehearsalResult(in)
			r.Status = "passed"
			return r, nil
		},
		build: fakeBuild(t, key, seen),
		notices: func(root, dir string, info build.Info) error {
			if err := os.WriteFile(filepath.Join(dir, "THIRD-PARTY-NOTICES.txt"), []byte("notices"), 0o644); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(dir, "VERSION.txt"), []byte(build.VersionFile(info)), 0o644)
		},
		offline: true,
		logf:    func(string, ...any) {},
	}
}

// Without the key the release fails before building, and the key must be
// the one of mygo.json; nothing generates a key (M2-K08).
func TestSigningKeyRequired(t *testing.T) {
	pub, key := testKey(t)
	none := func(string) string { return "" }
	if _, err := signingKey(none, filepath.Join(t.TempDir(), "absent.key"), base64.StdEncoding.EncodeToString(pub)); err == nil || !strings.Contains(err.Error(), "no update signing key") {
		t.Fatalf("missing key: %v", err)
	}
	_, other := testKey(t)
	env := func(k string) string {
		if k == keyEnv {
			return base64.StdEncoding.EncodeToString(other)
		}
		return ""
	}
	if _, err := signingKey(env, "", base64.StdEncoding.EncodeToString(pub)); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("other key: %v", err)
	}
	file := filepath.Join(t.TempDir(), "update-signing.key")
	if err := os.WriteFile(file, []byte(base64.StdEncoding.EncodeToString(key)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := signingKey(none, file, base64.StdEncoding.EncodeToString(pub)); err != nil || !got.Equal(key) {
		t.Fatalf("key file: %v", err)
	}

	root := newRepo(t, pub)
	var seen []string
	o := testOptions(t, root, key, &seen)
	o.getenv, o.allowDirty = none, true
	if _, err := run(o); err == nil || !strings.Contains(err.Error(), "no update signing key") {
		t.Fatalf("release without key: %v", err)
	}
	if len(seen) != 0 {
		t.Fatal("built without a key")
	}
}

func TestReleaseNeedsCleanTreeOnOriginMain(t *testing.T) {
	pub, key := testKey(t)
	root := newRepo(t, pub)
	var seen []string
	o := testOptions(t, root, key, &seen)
	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(o); err == nil || !strings.Contains(err.Error(), "work tree has changes") {
		t.Fatalf("dirty tree: %v", err)
	}
	os.Remove(filepath.Join(root, "new.txt"))
	if _, err := run(o); err == nil || !strings.Contains(err.Error(), "origin/main") {
		t.Fatalf("no origin/main: %v", err)
	}
	head := git(t, root, "rev-parse", "HEAD")
	git(t, root, "update-ref", "refs/remotes/origin/main", head)
	if err := os.WriteFile(filepath.Join(root, "later.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", ".")
	git(t, root, "commit", "-q", "-m", "later")
	if _, err := run(o); err == nil || !strings.Contains(err.Error(), "not the latest commit of origin/main") {
		t.Fatalf("ahead of origin/main: %v", err)
	}
	if len(seen) != 0 {
		t.Fatal("built from a tree that is not main")
	}
	git(t, root, "update-ref", "refs/remotes/origin/main", git(t, root, "rev-parse", "HEAD"))
	if _, err := run(o); err != nil {
		t.Fatalf("clean tree on origin/main: %v", err)
	}
}

func TestPublishNeedsAuthorization(t *testing.T) {
	pub, key := testKey(t)
	root := newRepo(t, pub)
	var seen []string
	o := testOptions(t, root, key, &seen)
	o.publish = true
	if _, err := run(o); err == nil || !strings.Contains(err.Error(), publishAuthEnv+"=1") {
		t.Fatalf("publish without authorization: %v", err)
	}
	o.allowDirty = true
	if _, err := run(o); err == nil || !strings.Contains(err.Error(), "never published") {
		t.Fatalf("publish a rehearsal: %v", err)
	}
	// Offline the version is not checked against GitHub: never published.
	o.allowDirty, o.offline = false, true
	o.getenv = func(k string) string {
		return map[string]string{keyEnv: base64.StdEncoding.EncodeToString(key), publishAuthEnv: "1"}[k]
	}
	if _, err := run(o); err == nil || !strings.Contains(err.Error(), "-offline") {
		t.Fatalf("publish offline: %v", err)
	}
	if len(seen) != 0 {
		t.Fatal("built")
	}
}

// The signing key leaves the environment that child processes inherit
// and is still read by the release itself.
func TestSigningKeyKeptFromChildProcesses(t *testing.T) {
	t.Setenv(keyEnv, "secret-for-test")
	getenv := takeSigningKeyEnv()
	if v, ok := os.LookupEnv(keyEnv); ok {
		t.Fatalf("the key stays in the environment: %q", v)
	}
	if getenv(keyEnv) != "secret-for-test" {
		t.Fatal("the release lost the key")
	}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(strings.ToUpper(kv), keyEnv+"=") {
			t.Fatal("a child process would inherit the key")
		}
	}
}

// A rehearsal from the work tree: the files of W5 in dist/trace, the
// SHA256SUMS right, the manifest attested, nothing published (M2-K08).
func TestReleaseFiles(t *testing.T) {
	pub, key := testKey(t)
	root := newRepo(t, pub)
	git(t, root, "tag", "v0.1.1")
	git(t, root, "tag", "v0.1.2")
	var seen []string
	o := testOptions(t, root, key, &seen)
	o.allowDirty = true
	if err := os.WriteFile(filepath.Join(root, "CHANGELOG.md"), []byte(changelogHeader+"\n## 0.1.3\n\n- 第三版\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := run(o)
	if err != nil {
		t.Fatal(err)
	}
	if res.Version != "0.1.3" || res.Published || !strings.HasSuffix(res.Commit, "-dirty") || res.Rehearsal != "passed" {
		t.Fatalf("%+v", res)
	}
	if len(seen) != 1 || seen[0] != "0.1.3" {
		t.Fatalf("built %v", seen)
	}
	dist := filepath.Join(root, "dist", "trace")
	names, err := build.VerifySums(dist, releaseSums)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"THIRD-PARTY-NOTICES.txt", "VERSION.txt", "install.ps1", "trace-0.1.3-windows-amd64.tar.gz", "trace-setup-0.1.3-amd64.exe", manifestName}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("SHA256SUMS lists %v", names)
	}
	if _, err := os.Stat(filepath.Join(dist, "trace.exe")); err == nil {
		t.Fatal("the release ships the unpacked program")
	}
	raw, _ := os.ReadFile(filepath.Join(dist, manifestName))
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	att, _ := base64.StdEncoding.DecodeString(m[update.AttestationField].(string))
	if !ed25519.Verify(pub, update.AttestationMessage("0.1.3", m["signature"].(string)), att) {
		t.Fatal("manifest without a valid attestation")
	}
	src := &update.Source{PublicKey: pub, Current: "0.1.2"}
	if got, err := src.Verify(raw); err != nil || got == nil || got.Version != "0.1.3" {
		t.Fatalf("the app would not accept the manifest: %v", err)
	}
	if v := build.ParseVersionFile(string(must(os.ReadFile(filepath.Join(dist, "VERSION.txt"))))); v["version"] != "0.1.3" || v["channel"] != "stable" {
		t.Fatalf("VERSION.txt %v", v)
	}
	for _, f := range []string{"release.json", "rehearsal.json"} {
		if _, err := os.Stat(filepath.Join(root, "artifacts", "release", f)); err != nil {
			t.Fatalf("no %s", f)
		}
	}

	// A failed rehearsal stops the release before dist/trace changes.
	o.rehearse = func(in rehearsalInput) (rehearsalResult, error) {
		r := newRehearsalResult(in)
		r.Status = "failed"
		return r, os.ErrInvalid
	}
	before := must(os.ReadFile(filepath.Join(dist, releaseSums)))
	if _, err := run(o); err == nil || !strings.Contains(err.Error(), "rehearsal") {
		t.Fatalf("failed rehearsal: %v", err)
	}
	if string(must(os.ReadFile(filepath.Join(dist, releaseSums)))) != string(before) {
		t.Fatal("dist/trace changed after a failed rehearsal")
	}
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}

func TestChangelog(t *testing.T) {
	text := changelogHeader + "\n## 0.1.2\n\n- 二\n\n## [0.1.1](link)\n\n- 一\n"
	if got := changelogSection(text, "0.1.1"); got != "- 一" {
		t.Fatalf("section 0.1.1: %q", got)
	}
	if got := changelogSection(text, "0.1.3"); got != "" {
		t.Fatalf("absent section: %q", got)
	}
	next := insertSection(text, "0.1.3", "- 三")
	if !strings.HasPrefix(next[strings.Index(next, "## "):], "## 0.1.3\n\n- 三\n\n## 0.1.2") || changelogSection(next, "0.1.2") != "- 二" {
		t.Fatalf("inserted:\n%s", next)
	}

	pub, _ := testKey(t)
	root := newRepo(t, pub)
	git(t, root, "tag", "v0.1.1")
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", ".")
	git(t, root, "commit", "-q", "-m", "订阅支持合集 (#7)")
	notes, drafted, err := ensureChangelog(filepath.Join(root, "CHANGELOG.md"), "0.1.2", "v0.1.1", root)
	if err != nil || !drafted || notes != "- 订阅支持合集 (#7)" {
		t.Fatalf("draft: %q %v %v", notes, drafted, err)
	}
	if again, drafted, _ := ensureChangelog(filepath.Join(root, "CHANGELOG.md"), "0.1.2", "v0.1.1", root); drafted || again != notes {
		t.Fatal("drafted twice")
	}
	// A drafted section stops a release from this machine until it is
	// reviewed and committed.
	if ok, reason, err := hasChanges(root, "v0.1.1"); err != nil || !ok {
		t.Fatalf("changes since v0.1.1: %v %s %v", ok, reason, err)
	}
	git(t, root, "tag", "v0.1.2")
	if ok, _, _ := hasChanges(root, "v0.1.2"); ok {
		t.Fatal("no changes since v0.1.2 still releases")
	}
	if ok, _, _ := hasChanges(root, ""); !ok {
		t.Fatal("the first release is skipped")
	}
}
