package main

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/update"
	"github.com/chialecode/trace/tools/internal/build"
)

const changelogHeader = "# 更新日志\n\nTRACE 正式版每个版本的变更。以版本号为二级标题的一节就是这一版的更新内容：它会写进 GitHub Release，也是 TRACE 里“更新内容”显示的文字。\n"

// changelogSection returns the "## version" section of a changelog
// without its heading, or "".
func changelogSection(text, version string) string {
	var lines []string
	in := false
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if rest, ok := strings.CutPrefix(line, "## "); ok {
			if in {
				break
			}
			fields := strings.Fields(rest)
			in = len(fields) > 0 && strings.TrimPrefix(headingVersion(fields[0]), "v") == version
			continue
		}
		if in {
			lines = append(lines, line)
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// headingVersion reads "0.1.1", "[0.1.1]" and "[0.1.1](link)", as MyGo
// does when it takes the notes of a version.
func headingVersion(s string) string {
	if rest, ok := strings.CutPrefix(s, "["); ok {
		s, _, _ = strings.Cut(rest, "]")
	}
	return s
}

// ensureChangelog returns the notes of version from the changelog; without
// a section it drafts one from the subjects of the commits merged into
// main since lastTag and writes it in, reporting that it drafted.
func ensureChangelog(path, version, lastTag, root string) (string, bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	text := string(raw)
	if notes := changelogSection(text, version); notes != "" {
		return notes, false, nil
	}
	rng := "HEAD"
	if lastTag != "" {
		rng = lastTag + "..HEAD"
	}
	out, err := build.Git(root, "log", "--first-parent", "--format=%s", rng)
	if err != nil {
		return "", false, err
	}
	notes := draftNotes(out)
	if strings.TrimSpace(text) == "" {
		text = changelogHeader
	}
	text = insertSection(text, version, notes)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return "", false, err
	}
	return notes, true, nil
}

// draftNotes turns commit subjects into list items, newest first.
func draftNotes(subjects string) string {
	var items []string
	for _, s := range strings.Split(strings.ReplaceAll(subjects, "\r\n", "\n"), "\n") {
		if s = strings.TrimSpace(s); s != "" {
			items = append(items, "- "+s)
		}
	}
	if len(items) == 0 {
		items = []string{"- 维护更新"}
	}
	return strings.Join(items, "\n")
}

// insertSection puts the section of version above the newest one.
func insertSection(text, version, notes string) string {
	section := "## " + version + "\n\n" + notes + "\n"
	if i := strings.Index(text, "\n## "); i >= 0 {
		return text[:i+1] + section + "\n" + text[i+1:]
	}
	return strings.TrimRight(text, "\n") + "\n\n" + section
}

// attestManifest adds TRACE's attestation, which binds the version of the
// manifest to its archive (internal/update), to the manifest MyGo wrote.
func attestManifest(path string, key ed25519.PrivateKey) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("the build wrote no update manifest: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}
	var m struct {
		Version   string `json:"version"`
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal(raw, &m); err != nil || m.Version == "" || m.Signature == "" {
		return fmt.Errorf("%s has no version or signature: was the build signed?", filepath.Base(path))
	}
	att, _ := json.Marshal(update.SignAttestation(key, m.Version, m.Signature))
	fields[update.AttestationField] = att
	out, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}

// publishRelease creates the GitHub Release of the version as a draft
// with the files, uploads the update manifest last (so that it never names
// a file that is not there), checks the release holds every file and then
// publishes it as the latest release. It only runs with -publish and the
// user's authorization.
func publishRelease(root, version, dir string, files []string, notes string) error {
	tag := "v" + version
	notesFile := filepath.Join(os.TempDir(), "trace-release-notes-"+version+".md")
	if err := os.WriteFile(notesFile, []byte(strings.TrimSpace(notes)+"\n"), 0o644); err != nil {
		return err
	}
	defer os.Remove(notesFile)
	head, err := build.Git(root, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	args := []string{"release", "create", tag, "--repo", build.Repository, "--target", head, "--title", "TRACE " + version, "--notes-file", notesFile, "--draft"}
	for _, f := range files {
		if f != manifestName {
			args = append(args, filepath.Join(dir, f))
		}
	}
	if err := ghRun(root, args...); err != nil {
		return fmt.Errorf("gh release create: %w", err)
	}
	if err := ghRun(root, "release", "upload", tag, "--repo", build.Repository, filepath.Join(dir, manifestName)); err != nil {
		return fmt.Errorf("gh release upload: %w", err)
	}
	out, err := ghOutput(root, "release", "view", tag, "--repo", build.Repository, "--json", "assets", "--jq", ".assets[].name")
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, n := range strings.Fields(out) {
		have[n] = true
	}
	for _, f := range files {
		if !have[f] {
			return fmt.Errorf("the draft release %s lacks %s; it stays a draft", tag, f)
		}
	}
	if err := ghRun(root, "release", "edit", tag, "--repo", build.Repository, "--draft=false", "--latest"); err != nil {
		return fmt.Errorf("publish the release: %w", err)
	}
	return nil
}

func ghRun(root string, args ...string) error {
	_, err := ghOutput(root, args...)
	return err
}

func ghOutput(root string, args ...string) (string, error) {
	cmd := exec.Command("gh", args...)
	cmd.Dir = root
	appdirs.SetSubprocessQuiet(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("gh %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
