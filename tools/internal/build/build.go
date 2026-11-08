// Package build is what tools/package and tools/release share: the version
// of a build (W1), the MyGo configuration of each channel, the -ldflags
// that carry the build information into the program, and the files that
// describe a package.
package build

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
)

// Repository is where releases are published.
const Repository = "chialecode/trace"

// Channels of a build.
const (
	ChannelDev    = "dev"
	ChannelStable = "stable"
	ChannelPtest  = "ptest"
)

// Info is what a build records about itself.
type Info struct {
	Version   string
	Channel   string
	Commit    string // short hash
	BuildTime string // UTC, RFC 3339
}

// Identity is the per-channel naming of the app (DEC-029).
type Identity struct {
	Slug        string
	Identifier  string
	DisplayName string
	InstallDir  string
	Icon        string
}

// IdentityOf returns the naming of a channel.
func IdentityOf(channel string) Identity {
	switch channel {
	case ChannelDev:
		return Identity{Slug: "trace-dev", Identifier: appdirs.IdentifierForChannel("dev"), DisplayName: "TRACE Dev", InstallDir: "trace-dev", Icon: "assets/icon-dev.png"}
	case ChannelPtest:
		return Identity{Slug: "trace-ptest", Identifier: appdirs.IdentifierForChannel("ptest"), DisplayName: "TRACE PTest", InstallDir: "trace-ptest", Icon: "assets/icon-dev.png"}
	default:
		return Identity{Slug: "trace", Identifier: appdirs.IdentifierForChannel("stable"), DisplayName: "TRACE", InstallDir: "trace", Icon: "assets/icon.png"}
	}
}

// Publisher and copyright of the packages (W6).
const (
	Publisher = "chialecode"
	Copyright = "© 2026 chialecode"
)

var tagRe = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)

// VersionLine returns "0.1" of the version "0.1.0" in mygo.json: the
// releases of the line are 0.1.x.
func VersionLine(configVersion string) (string, error) {
	parts := strings.Split(strings.TrimSpace(configVersion), ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("mygo.json version %q is not major.minor.patch", configVersion)
	}
	for _, p := range parts {
		if _, err := strconv.Atoi(p); err != nil {
			return "", fmt.Errorf("mygo.json version %q is not major.minor.patch", configVersion)
		}
	}
	return parts[0] + "." + parts[1], nil
}

// NextStable returns the next release of the line from the release tags:
// one more than the highest vLINE.N tag, or LINE.1 when there is none
// (0.1.0 was what every development build of the first stage was labeled,
// so the first release is 0.1.1, Q-012).
func NextStable(line string, tags []string) string {
	highest := -1
	for _, t := range tags {
		m := tagRe.FindStringSubmatch(strings.TrimSpace(t))
		if m == nil || m[1]+"."+m[2] != line {
			continue
		}
		n, _ := strconv.Atoi(m[3])
		if n > highest {
			highest = n
		}
	}
	if highest < 1 {
		return line + ".1"
	}
	return fmt.Sprintf("%s.%d", line, highest+1)
}

// LatestTag returns the highest release tag of the line, or "".
func LatestTag(line string, tags []string) string {
	latest, highest := "", -1
	for _, t := range tags {
		m := tagRe.FindStringSubmatch(strings.TrimSpace(t))
		if m == nil || m[1]+"."+m[2] != line {
			continue
		}
		if n, _ := strconv.Atoi(m[3]); n > highest {
			latest, highest = strings.TrimSpace(t), n
		}
	}
	return latest
}

// DevVersion is the version of a development build: the next release with
// the UTC minute it was built, as 0.1.1-dev.202611091530.
func DevVersion(nextStable string, t time.Time) string {
	return nextStable + "-dev." + t.UTC().Format("200601021504")
}

// LineTags returns the release tags of the line, sorted.
func LineTags(line string, tags []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range tags {
		t = strings.TrimSpace(t)
		m := tagRe.FindStringSubmatch(t)
		if m == nil || m[1]+"."+m[2] != line || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// CheckTagsAgree fails when the release tags of the line in Git and the
// published GitHub Releases differ: the next version would be ambiguous.
func CheckTagsAgree(line string, gitTags, releaseTags []string) error {
	a, b := LineTags(line, gitTags), LineTags(line, releaseTags)
	inA := map[string]bool{}
	for _, t := range a {
		inA[t] = true
	}
	inB := map[string]bool{}
	for _, t := range b {
		inB[t] = true
	}
	var onlyGit, onlyRelease []string
	for _, t := range a {
		if !inB[t] {
			onlyGit = append(onlyGit, t)
		}
	}
	for _, t := range b {
		if !inA[t] {
			onlyRelease = append(onlyRelease, t)
		}
	}
	if len(onlyGit) == 0 && len(onlyRelease) == 0 {
		return nil
	}
	return fmt.Errorf("the tags and the releases of %s differ (tags without a release: %v; releases without a tag: %v): fix the repository before releasing", line, onlyGit, onlyRelease)
}

// LDFlags carries the build information into the program, and with a
// feed, TRACE's copy of the update feed and key (internal/update).
func LDFlags(info Info, feed, publicKey string) string {
	set := func(pkg, name, value string) string {
		if strings.ContainsAny(value, " \t\"'") {
			value = strconv.Quote(value)
		}
		return " -X github.com/chialecode/trace/internal/" + pkg + "." + name + "=" + value
	}
	ch := info.Channel
	if ch == ChannelStable {
		ch = "release"
	}
	flags := set("appdirs", "channel", ch) + set("appdirs", "version", info.Version) +
		set("appdirs", "commit", info.Commit) + set("appdirs", "buildTime", info.BuildTime)
	if feed != "" && publicKey != "" {
		flags += set("update", "feedURL", feed) + set("update", "publicKey", publicKey)
	}
	return strings.TrimSpace(flags)
}

// Updates is the updates section of a channel's configuration.
type Updates struct {
	PublicKey string `json:"publicKey"`
	GitHub    string `json:"github,omitempty"`
	URL       string `json:"url,omitempty"`
	Changelog string `json:"changelog,omitempty"`
	Deltas    *int   `json:"deltas,omitempty"`
}

// Feed returns the manifest feed that MyGo injects for windows/amd64.
func (u *Updates) Feed() string {
	const manifest = "update-windows-amd64.json"
	if u == nil {
		return ""
	}
	if u.GitHub != "" {
		return "https://github.com/" + u.GitHub + "/releases/latest/download/" + manifest
	}
	return strings.TrimSuffix(u.URL, "/") + "/" + manifest
}

// ChannelConfig returns the mygo.json of a channel's build from the
// repository's mygo.json: the channel's identity, the build's version, the
// icon, publisher, copyright and Chinese catalogs, and updates only where
// given (the release channel and the ptest rehearsal).
func ChannelConfig(base []byte, channel, version, out string, updates *Updates) ([]byte, error) {
	var cfg map[string]any
	if err := json.Unmarshal(base, &cfg); err != nil {
		return nil, fmt.Errorf("parse mygo.json: %w", err)
	}
	id := IdentityOf(channel)
	cfg["name"] = "trace"
	cfg["slug"] = id.Slug
	cfg["identifier"] = id.Identifier
	cfg["version"] = version
	cfg["main"] = "."
	cfg["out"] = out
	cfg["icon"] = id.Icon
	cfg["publisher"] = Publisher
	cfg["copyright"] = Copyright
	cfg["i18n"] = map[string]any{"dir": "internal/locales", "source": "zh-Hans"}
	win, _ := cfg["windows"].(map[string]any)
	if win == nil {
		win = map[string]any{}
	}
	win["displayName"] = id.DisplayName
	win["installDirName"] = id.InstallDir
	cfg["windows"] = win
	delete(cfg, "updates")
	if updates != nil {
		cfg["updates"] = updates
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

// ConfigUpdates reads the updates section of the repository's mygo.json.
func ConfigUpdates(base []byte) (*Updates, error) {
	var cfg struct {
		Updates *Updates `json:"updates"`
	}
	if err := json.Unmarshal(base, &cfg); err != nil {
		return nil, err
	}
	if cfg.Updates == nil || cfg.Updates.PublicKey == "" {
		return nil, errors.New("mygo.json has no updates.publicKey")
	}
	return cfg.Updates, nil
}

// ConfigVersion reads the version of the repository's mygo.json.
func ConfigVersion(base []byte) (string, error) {
	var cfg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(base, &cfg); err != nil {
		return "", err
	}
	if cfg.Version == "" {
		return "", errors.New("mygo.json has no version")
	}
	return cfg.Version, nil
}

// SetupName is the installer of a channel and version.
func SetupName(channel, version string) string {
	return IdentityOf(channel).Slug + "-setup-" + version + "-amd64.exe"
}

// VersionFile is VERSION.txt of a package: the same fields the program
// reports in "设置 › 关于".
func VersionFile(info Info) string {
	return fmt.Sprintf("version=%s\nchannel=%s\ncommit=%s\nbuilt_at=%s\nos=windows\narch=amd64\n", info.Version, info.Channel, info.Commit, info.BuildTime)
}

// ParseVersionFile reads VERSION.txt.
func ParseVersionFile(raw string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}

// Git runs git in dir and returns its trimmed output.
func Git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	appdirs.SetSubprocessQuiet(cmd)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// LocalTags lists the tags of the local repository.
func LocalTags(dir string) ([]string, error) {
	out, err := Git(dir, "tag", "--list", "v*")
	if err != nil {
		return nil, err
	}
	return splitLines(out), nil
}

// RemoteTags lists the tags of the origin repository (git ls-remote).
func RemoteTags(dir string) ([]string, error) {
	out, err := Git(dir, "ls-remote", "--tags", "--refs", "origin", "v*")
	if err != nil {
		return nil, err
	}
	var tags []string
	for _, line := range splitLines(out) {
		if _, ref, ok := strings.Cut(line, "\t"); ok {
			tags = append(tags, strings.TrimPrefix(ref, "refs/tags/"))
		}
	}
	return tags, nil
}

// ReleaseTags lists the tags of the published (not draft) GitHub releases
// with gh.
func ReleaseTags(dir string) ([]string, error) {
	cmd := exec.Command("gh", "release", "list", "--repo", Repository, "--limit", "1000", "--json", "tagName,isDraft")
	cmd.Dir = dir
	appdirs.SetSubprocessQuiet(cmd)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh release list: %w", err)
	}
	var rels []struct {
		TagName string `json:"tagName"`
		IsDraft bool   `json:"isDraft"`
	}
	if err := json.Unmarshal(out, &rels); err != nil {
		return nil, fmt.Errorf("gh release list: %w", err)
	}
	var tags []string
	for _, r := range rels {
		if !r.IsDraft {
			tags = append(tags, r.TagName)
		}
	}
	return tags, nil
}

// HeadInfo returns the short commit and whether the work tree has
// changes.
func HeadInfo(dir string) (short string, dirty bool, err error) {
	short, err = Git(dir, "rev-parse", "--short=12", "HEAD")
	if err != nil {
		return "", false, err
	}
	status, err := Git(dir, "status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return "", false, err
	}
	return short, status != "", nil
}

func splitLines(s string) []string {
	var out []string
	for _, l := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// WithConfig writes the channel configuration as mygo.json for the time of
// run and puts the repository's back afterwards, also when run fails:
// `mygo build` reads mygo.json from the project root only.
func WithConfig(path string, channelConfig []byte, run func() error) (err error) {
	orig, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := os.WriteFile(path, channelConfig, 0o644); err != nil {
		return fmt.Errorf("write channel %s: %w", path, err)
	}
	defer func() {
		if rerr := os.WriteFile(path, orig, 0o644); rerr != nil && err == nil {
			err = fmt.Errorf("restore %s: %w", path, rerr)
		}
	}()
	return run()
}
