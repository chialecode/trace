// Command release builds a release of TRACE (W5): from the commit that is
// the latest of origin/main, it works out the version, runs tools/check,
// builds and signs the release package, rehearses installing and updating
// it under the ptest identity, and puts the files in dist/trace/.
//
//	go run ./tools/release                # a release from origin/main
//	go run ./tools/release -allow-dirty   # a rehearsal from the work tree
//	go run ./tools/release -offline       # no requests to GitHub (local tags, no deltas)
//	go run ./tools/release -plan          # CI: is there something to release?
//	go run ./tools/release -publish       # tag and publish; needs TRACE_RELEASE_PUBLISH=1
//
// The update signing key comes from MYGO_UPDATER_PRIVATE_KEY, or here from
// %APPDATA%\trace-release\update-signing.key; without it the release
// fails. The key is never printed or written anywhere else.
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/tools/internal/build"
)

const (
	keyEnv          = "MYGO_UPDATER_PRIVATE_KEY"
	publishAuthEnv  = "TRACE_RELEASE_PUBLISH"
	releaseSums     = "SHA256SUMS"
	resultDir       = "artifacts/release"
	manifestName    = "update-windows-amd64.json"
	changelogName   = "CHANGELOG.md"
	distReleaseDir  = "dist/trace"
	stagingPrefix   = ".release-"
	rehearsalPrefix = ".rehearsal-"
)

type options struct {
	root       string
	allowDirty bool
	offline    bool
	publish    bool
	ci         bool
	getenv     func(string) string
	keyFile    string
	// runCheck runs tools/check; tests replace it.
	runCheck func(root string) error
	// rehearse runs the ptest rehearsal; tests replace it.
	rehearse func(r rehearsalInput) (rehearsalResult, error)
	// build runs the MyGo build; tests replace it.
	build func(root, out, ldflags string, env []string) (string, error)
	// notices writes THIRD-PARTY-NOTICES.txt and VERSION.txt; tests
	// replace it.
	notices func(root, dir string, info build.Info) error
	logf    func(format string, args ...any)
}

func main() {
	appdirs.LowerCurrentProcessPriority()
	opts := options{getenv: takeSigningKeyEnv(), runCheck: runToolsCheck, rehearse: runRehearsal, build: build.MygoBuild, notices: writeNotices,
		logf: func(format string, args ...any) { fmt.Printf(format+"\n", args...) }}
	flag.BoolVar(&opts.allowDirty, "allow-dirty", false, "rehearse from the work tree: skip the clean tree and origin/main checks (never publishes)")
	flag.BoolVar(&opts.offline, "offline", false, "ask GitHub nothing: versions from local tags, no delta updates")
	flag.BoolVar(&opts.publish, "publish", false, "create the tag and the GitHub Release (needs "+publishAuthEnv+"=1, the user's authorization)")
	plan := flag.Bool("plan", false, "print whether main has changes since the last release and the next version (CI)")
	flag.Parse()
	opts.ci = strings.EqualFold(os.Getenv("CI"), "true")
	root, err := os.Getwd()
	if err == nil {
		opts.root = root
		opts.keyFile = defaultKeyFile()
		if *plan {
			err = runPlan(opts)
		} else {
			_, err = run(opts)
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "release failed: %v\n", err)
		os.Exit(1)
	}
}

// takeSigningKeyEnv removes the update signing key from this process's
// environment, so that no child process inherits it (tools/check and the
// tests it runs, git, gh, the rehearsal's TRACE), and returns a getenv
// that still reads it: buildChannel hands it to the MyGo build alone.
func takeSigningKeyEnv() func(string) string {
	secret := os.Getenv(keyEnv)
	_ = os.Unsetenv(keyEnv)
	return func(k string) string {
		if k == keyEnv {
			return secret
		}
		return os.Getenv(k)
	}
}

func defaultKeyFile() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "trace-release", "update-signing.key")
}

// signingKey returns the update key from the environment or the key file,
// and checks it is the key of publicKey.
func signingKey(getenv func(string) string, keyFile, publicKey string) (ed25519.PrivateKey, error) {
	text := strings.TrimSpace(getenv(keyEnv))
	if text == "" && keyFile != "" {
		raw, err := os.ReadFile(keyFile)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("read the update signing key: %w", err)
		}
		text = strings.TrimSpace(string(raw))
	}
	if text == "" {
		return nil, fmt.Errorf("no update signing key: set %s (CI: the release environment's secret) or keep it in %%APPDATA%%\\trace-release\\update-signing.key; a release is never built unsigned and the key is never generated here", keyEnv)
	}
	b, err := base64.StdEncoding.DecodeString(text)
	if err != nil || len(b) != ed25519.PrivateKeySize {
		return nil, errors.New("the update signing key is not a MyGo update key")
	}
	key := ed25519.PrivateKey(b)
	if base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey)) != strings.TrimSpace(publicKey) {
		return nil, errors.New("the update signing key does not match updates.publicKey of mygo.json")
	}
	return key, nil
}

// releaseResult is artifacts/release/release.json.
type releaseResult struct {
	Version    string   `json:"version"`
	Commit     string   `json:"commit"`
	BuildTime  string   `json:"buildTime"`
	Offline    bool     `json:"offline"`
	AllowDirty bool     `json:"allowDirty"`
	Files      []string `json:"files"`
	Rehearsal  string   `json:"rehearsal"`
	Published  bool     `json:"published"`
	ElapsedMs  int64    `json:"elapsedMs"`
}

func run(o options) (releaseResult, error) {
	started := time.Now()
	res := releaseResult{Offline: o.offline, AllowDirty: o.allowDirty}
	logf := o.logf
	if o.publish && o.allowDirty {
		return res, errors.New("-publish cannot be used with -allow-dirty: a rehearsal is never published")
	}
	if o.publish && strings.TrimSpace(o.getenv(publishAuthEnv)) != "1" {
		return res, fmt.Errorf("publishing needs the user's authorization (USER-ACTIONS ACT-009, or for this one release in the conversation), given as %s=1", publishAuthEnv)
	}
	if o.publish && o.offline {
		return res, errors.New("-publish cannot be used with -offline: a release checks its version against the tags and Releases on GitHub")
	}
	base, err := os.ReadFile(filepath.Join(o.root, "mygo.json"))
	if err != nil {
		return res, fmt.Errorf("read mygo.json (run from the repository root): %w", err)
	}
	up, err := build.ConfigUpdates(base)
	if err != nil {
		return res, err
	}
	cfgVersion, err := build.ConfigVersion(base)
	if err != nil {
		return res, err
	}
	line, err := build.VersionLine(cfgVersion)
	if err != nil {
		return res, err
	}

	commit, dirty, err := build.HeadInfo(o.root)
	if err != nil {
		return res, err
	}
	if !o.allowDirty {
		if dirty {
			return res, errors.New("the work tree has changes: a release is built from committed code only (-allow-dirty rehearses without publishing)")
		}
		if err := checkOriginMain(o.root, o.offline); err != nil {
			return res, err
		}
	} else if dirty {
		commit += "-dirty"
	}

	tags, err := releaseTags(o.root, line, o.offline)
	if err != nil {
		return res, err
	}
	version := build.NextStable(line, tags)
	now := time.Now().UTC()
	info := build.Info{Version: version, Channel: build.ChannelStable, Commit: commit, BuildTime: now.Format(time.RFC3339)}
	res.Version, res.Commit, res.BuildTime = version, commit, info.BuildTime
	logf("==> [release] TRACE %s from %s", version, commit)

	key, err := signingKey(o.getenv, o.keyFile, up.PublicKey)
	if err != nil {
		return res, err
	}

	notes, drafted, err := ensureChangelog(filepath.Join(o.root, changelogName), version, lastTag(line, tags), o.root)
	if err != nil {
		return res, err
	}
	if drafted {
		logf("==> [release] %s had no section for %s; drafted it from the merged changes:\n%s", changelogName, version, notes)
		if !o.ci && !o.allowDirty {
			return res, fmt.Errorf("review the drafted section of %s in %s, commit it and run again", version, changelogName)
		}
	}

	// The notes of the GitHub Release (the publish job reads them).
	if err := os.MkdirAll(filepath.Join(o.root, filepath.FromSlash(resultDir)), 0o755); err != nil {
		return res, err
	}
	if err := os.WriteFile(filepath.Join(o.root, filepath.FromSlash(resultDir), "notes.md"), []byte(strings.TrimSpace(notes)+"\n"), 0o644); err != nil {
		return res, err
	}

	logf("==> [release] go run ./tools/check")
	if err := o.runCheck(o.root); err != nil {
		return res, fmt.Errorf("tools/check: %w", err)
	}

	if err := os.MkdirAll(filepath.Join(o.root, "dist"), 0o755); err != nil {
		return res, err
	}
	staging, err := os.MkdirTemp(filepath.Join(o.root, "dist"), stagingPrefix+now.Format("20060102-150405")+"-")
	if err != nil {
		return res, err
	}
	defer os.RemoveAll(staging)

	var deltas *int
	if o.offline {
		zero := 0
		deltas = &zero
	}
	cfgUpdates := *up
	cfgUpdates.Deltas = deltas
	cfgUpdates.Changelog = filepath.Join(o.root, changelogName)
	logf("==> [release] building and signing TRACE %s...", version)
	builtDir, files, err := buildChannel(o.root, o.build, base, info, &cfgUpdates, key, staging)
	if err != nil {
		return res, err
	}
	if err := o.notices(o.root, builtDir, info); err != nil {
		return res, err
	}
	files = append(files, "VERSION.txt", "THIRD-PARTY-NOTICES.txt")
	if _, err := build.WriteSums(builtDir, releaseSums, files); err != nil {
		return res, err
	}
	if err := checkReleaseFiles(builtDir, version); err != nil {
		return res, err
	}

	logf("==> [release] rehearsing install, update and uninstall under the ptest identity...")
	rr, err := o.rehearse(rehearsalInput{Root: o.root, Base: base, Version: version, Commit: commit, Build: o.build, Logf: logf})
	if werr := writeJSON(filepath.Join(o.root, filepath.FromSlash(resultDir), "rehearsal.json"), rr); werr != nil && err == nil {
		err = werr
	}
	if err != nil {
		return res, fmt.Errorf("rehearsal: %w", err)
	}
	res.Rehearsal = rr.Status

	all := append(append([]string(nil), files...), releaseSums)
	if err := build.PublishBundleWith(builtDir, filepath.Join(o.root, filepath.FromSlash(distReleaseDir)), releaseSums, all, true); err != nil {
		return res, err
	}
	res.Files = all
	logf("==> [release] %s is ready in %s", version, distReleaseDir)

	if o.publish {
		if err := publishRelease(o.root, version, filepath.Join(o.root, filepath.FromSlash(distReleaseDir)), all, notes); err != nil {
			return res, err
		}
		res.Published = true
	} else {
		logf("==> [release] not published (publishing needs -publish and the user's authorization)")
	}
	res.ElapsedMs = time.Since(started).Milliseconds()
	return res, writeJSON(filepath.Join(o.root, filepath.FromSlash(resultDir), "release.json"), res)
}

// buildChannel builds a channel's package with updates signed by key and
// TRACE's attestation in the manifest; it returns the directory of the
// files and their names.
func buildChannel(root string, buildFn func(root, out, ldflags string, env []string) (string, error), base []byte, info build.Info, up *build.Updates, key ed25519.PrivateKey, out string) (string, []string, error) {
	cfg, err := build.ChannelConfig(base, info.Channel, info.Version, "dist", up)
	if err != nil {
		return "", nil, err
	}
	outAbs, err := filepath.Abs(out)
	if err != nil {
		return "", nil, err
	}
	ldflags := build.LDFlags(info, up.Feed(), up.PublicKey)
	env := []string{keyEnv + "=" + base64.StdEncoding.EncodeToString(key)}
	err = build.WithConfig(filepath.Join(root, "mygo.json"), cfg, func() error {
		_, err := buildFn(root, outAbs, ldflags, env)
		return err
	})
	if err != nil {
		return "", nil, err
	}
	dir := filepath.Join(outAbs, "windows-amd64")
	if err := attestManifest(filepath.Join(dir, manifestName), key); err != nil {
		return "", nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", nil, err
	}
	var files []string
	for _, e := range entries {
		// The unpacked program is in the installer and the update archive;
		// the release does not ship it on its own.
		// Nor the list of installed files (.mygo-files), which the archive
		// and the installer carry.
		if e.Type().IsRegular() && e.Name() != releaseSums && e.Name() != "trace.exe" && !strings.HasPrefix(e.Name(), ".") {
			files = append(files, e.Name())
		}
	}
	return dir, files, nil
}

func writeNotices(root, dir string, info build.Info) error {
	if err := build.ThirdPartyNotices(root, filepath.Join(dir, "THIRD-PARTY-NOTICES.txt")); err != nil {
		return fmt.Errorf("generate THIRD-PARTY-NOTICES.txt: %w", err)
	}
	return os.WriteFile(filepath.Join(dir, "VERSION.txt"), []byte(build.VersionFile(info)), 0o644)
}

// checkReleaseFiles checks that a release has what W5 lists.
func checkReleaseFiles(dir, version string) error {
	want := []string{
		build.SetupName(build.ChannelStable, version), "trace-" + version + "-windows-amd64.tar.gz",
		manifestName, "install.ps1", "VERSION.txt", "THIRD-PARTY-NOTICES.txt", releaseSums,
	}
	for _, name := range want {
		st, err := os.Stat(filepath.Join(dir, name))
		if err != nil || st.Size() == 0 {
			return fmt.Errorf("the release lacks %s", name)
		}
	}
	if _, err := build.VerifySums(dir, releaseSums); err != nil {
		return err
	}
	return nil
}

// checkOriginMain fails unless HEAD is the latest commit of origin/main.
func checkOriginMain(root string, offline bool) error {
	head, err := build.Git(root, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	var main string
	if offline {
		main, err = build.Git(root, "rev-parse", "refs/remotes/origin/main")
	} else {
		var out string
		out, err = build.Git(root, "ls-remote", "origin", "refs/heads/main")
		main, _, _ = strings.Cut(out, "\t")
	}
	if err != nil {
		return fmt.Errorf("find origin/main: %w", err)
	}
	if strings.TrimSpace(main) != head {
		return fmt.Errorf("HEAD %s is not the latest commit of origin/main (%s): releases are built from main", short(head), short(main))
	}
	return nil
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// releaseTags are the tags of the line, from the remote, checked against
// the published releases; offline, those of this clone.
func releaseTags(root, line string, offline bool) ([]string, error) {
	if offline {
		return build.LocalTags(root)
	}
	tags, err := build.RemoteTags(root)
	if err != nil {
		return nil, err
	}
	releases, err := build.ReleaseTags(root)
	if err != nil {
		return nil, err
	}
	if err := build.CheckTagsAgree(line, tags, releases); err != nil {
		return nil, err
	}
	return tags, nil
}

// lastTag is the highest release tag of the line, or "".
func lastTag(line string, tags []string) string { return build.LatestTag(line, tags) }

// runPlan prints, for the weekly workflow, whether main has commits since
// the last release and the version the release would have.
func runPlan(o options) error {
	base, err := os.ReadFile(filepath.Join(o.root, "mygo.json"))
	if err != nil {
		return err
	}
	cfgVersion, err := build.ConfigVersion(base)
	if err != nil {
		return err
	}
	line, err := build.VersionLine(cfgVersion)
	if err != nil {
		return err
	}
	tags, err := releaseTags(o.root, line, o.offline)
	if err != nil {
		return err
	}
	release, reason, err := hasChanges(o.root, lastTag(line, tags))
	if err != nil {
		return err
	}
	out := fmt.Sprintf("release=%t\nversion=%s\n", release, build.NextStable(line, tags))
	fmt.Print(out)
	fmt.Fprintln(os.Stderr, reason)
	if path := o.getenv("GITHUB_OUTPUT"); path != "" {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = f.WriteString(out)
		return err
	}
	return nil
}

// hasChanges reports whether main has commits since tag (always without a
// tag: the first release).
func hasChanges(root, tag string) (bool, string, error) {
	if tag == "" {
		return true, "no release yet: building the first one", nil
	}
	n, err := build.Git(root, "rev-list", "--first-parent", "--count", tag+"..HEAD")
	if err != nil {
		return false, "", err
	}
	if strings.TrimSpace(n) == "0" {
		return false, "no changes merged into main since " + tag + ": skipping this week's release", nil
	}
	return true, n + " change(s) merged since " + tag, nil
}

func runToolsCheck(root string) error {
	cmd := exec.Command("go", "run", "./tools/check")
	cmd.Dir = root
	appdirs.SetSubprocessQuiet(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		tail := string(out)
		if len(tail) > 4000 {
			tail = tail[len(tail)-4000:]
		}
		return fmt.Errorf("%w\n%s", err, tail)
	}
	return nil
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}
