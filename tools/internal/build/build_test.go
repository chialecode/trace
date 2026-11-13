package build

import (
	"bytes"
	"encoding/json"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNextStableVersion(t *testing.T) {
	cases := []struct {
		tags []string
		want string
	}{
		{nil, "0.1.1"},
		{[]string{"v0.1.0"}, "0.1.1"},
		{[]string{"v0.1.1", "v0.1.3", "v0.1.2"}, "0.1.4"},
		{[]string{"v0.1.9", "v0.1.10"}, "0.1.11"},
		{[]string{"v0.2.5", "v1.0.0", "desktop-v0.1.7", "v0.1.2-rc.1", "v0.1.2"}, "0.1.3"},
	}
	for _, c := range cases {
		if got := NextStable("0.1", c.tags); got != c.want {
			t.Errorf("NextStable(%v) = %s, want %s", c.tags, got, c.want)
		}
	}
	if got := LatestTag("0.1", []string{"v0.1.9", "v0.1.10", "v0.2.0"}); got != "v0.1.10" {
		t.Fatalf("LatestTag = %s", got)
	}
	if got := LatestTag("0.1", nil); got != "" {
		t.Fatalf("LatestTag without tags = %s", got)
	}
	if line, err := VersionLine("0.1.0"); err != nil || line != "0.1" {
		t.Fatalf("VersionLine: %s %v", line, err)
	}
	if _, err := VersionLine("0.1"); err == nil {
		t.Fatal("VersionLine accepted 0.1")
	}
}

func TestDevVersionUsesUTCMinute(t *testing.T) {
	shanghai := time.FixedZone("CST", 8*3600)
	at := time.Date(2026, 11, 9, 23, 30, 59, 0, shanghai) // 15:30 UTC
	if got := DevVersion("0.1.1", at); got != "0.1.1-dev.202611091530" {
		t.Fatalf("DevVersion = %s", got)
	}
}

func TestTagsAndReleasesMustAgree(t *testing.T) {
	if err := CheckTagsAgree("0.1", []string{"v0.1.1", "v0.1.2", "v0.2.0"}, []string{"v0.1.2", "v0.1.1"}); err != nil {
		t.Fatalf("agreeing sets: %v", err)
	}
	err := CheckTagsAgree("0.1", []string{"v0.1.1", "v0.1.2"}, []string{"v0.1.1"})
	if err == nil || !strings.Contains(err.Error(), "v0.1.2") {
		t.Fatalf("tag without a release: %v", err)
	}
	err = CheckTagsAgree("0.1", []string{"v0.1.1"}, []string{"v0.1.1", "v0.1.3"})
	if err == nil || !strings.Contains(err.Error(), "v0.1.3") {
		t.Fatalf("release without a tag: %v", err)
	}
}

func TestLDFlagsCarryBuildInfo(t *testing.T) {
	info := Info{Version: "0.1.1-dev.202611091530", Channel: ChannelDev, Commit: "1a2b3c4d5e6f", BuildTime: "2026-11-09T15:30:00Z"}
	flags := LDFlags(info, "", "")
	for _, want := range []string{
		"internal/appdirs.channel=dev", "internal/appdirs.version=0.1.1-dev.202611091530",
		"internal/appdirs.commit=1a2b3c4d5e6f", "internal/appdirs.buildTime=2026-11-09T15:30:00Z",
	} {
		if !strings.Contains(flags, want) {
			t.Errorf("missing %s in %s", want, flags)
		}
	}
	if strings.Contains(flags, "internal/update") {
		t.Fatal("development build carries an update feed")
	}
	stable := LDFlags(Info{Version: "0.1.1", Channel: ChannelStable}, "https://github.com/chialecode/trace/releases/latest/download/update-windows-amd64.json", "KEY=")
	if !strings.Contains(stable, "appdirs.channel=release") || !strings.Contains(stable, "update.feedURL=https://github.com/") || !strings.Contains(stable, "update.publicKey=KEY=") {
		t.Fatalf("release flags: %s", stable)
	}
}

func repoMygoJSON(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "mygo.json"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestChannelConfigs(t *testing.T) {
	base := repoMygoJSON(t)
	up, err := ConfigUpdates(base)
	if err != nil || up.GitHub != Repository {
		t.Fatalf("repository mygo.json updates: %+v %v", up, err)
	}
	type cfg struct {
		Slug       string `json:"slug"`
		Identifier string `json:"identifier"`
		Version    string `json:"version"`
		Icon       string `json:"icon"`
		Publisher  string `json:"publisher"`
		Copyright  string `json:"copyright"`
		I18n       struct {
			Dir, Source string
		} `json:"i18n"`
		Updates *Updates `json:"updates"`
		Windows struct {
			DisplayName    string `json:"displayName"`
			InstallDirName string `json:"installDirName"`
		} `json:"windows"`
	}
	parse := func(raw []byte) cfg {
		var c cfg
		if err := json.Unmarshal(raw, &c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	devRaw, err := ChannelConfig(base, ChannelDev, "0.1.1-dev.202611091530", "dist/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	dev := parse(devRaw)
	if dev.Slug != "trace-dev" || dev.Identifier != "io.github.chialecode.trace-dev" || dev.Windows.DisplayName != "TRACE Dev" || dev.Windows.InstallDirName != "trace-dev" {
		t.Fatalf("dev identity: %+v", dev)
	}
	if dev.Updates != nil {
		t.Fatal("development config must not have updates")
	}
	stableRaw, err := ChannelConfig(base, ChannelStable, "0.1.1", "dist/x", up)
	if err != nil {
		t.Fatal(err)
	}
	stable := parse(stableRaw)
	if stable.Slug != "trace" || stable.Windows.DisplayName != "TRACE" || stable.Updates == nil || stable.Updates.PublicKey != up.PublicKey {
		t.Fatalf("stable config: %+v", stable)
	}
	for _, c := range []cfg{dev, stable} {
		if c.Publisher != "chialecode" || c.Copyright != "© 2026 chialecode" || c.I18n.Source != "zh-Hans" || c.I18n.Dir != "internal/locales" {
			t.Fatalf("publisher, copyright and Chinese catalogs: %+v", c)
		}
	}
	if dev.Icon == stable.Icon {
		t.Fatal("the two channels share an icon")
	}
	if SetupName(ChannelDev, "0.1.1-dev.1") != "trace-dev-setup-0.1.1-dev.1-amd64.exe" || SetupName(ChannelStable, "0.1.1") != "trace-setup-0.1.1-amd64.exe" {
		t.Fatal("installer names")
	}
	for channel, raw := range map[string][]byte{ChannelDev: devRaw, ChannelStable: stableRaw} {
		var c struct {
			Windows struct {
				InstallerInclude string `json:"installerInclude"`
			} `json:"windows"`
		}
		if err := json.Unmarshal(raw, &c); err != nil {
			t.Fatal(err)
		}
		if c.Windows.InstallerInclude != IdentityOf(channel).InstallerInclude {
			t.Fatalf("%s installerInclude %q", channel, c.Windows.InstallerInclude)
		}
	}
}

// Each channel's installer include names the channel's shortcuts and
// brings in the shared shortcut rules; only dev makes the desktop shortcut
// again on a silent over-install. The files are UTF-8 with a BOM, which
// makensis needs to read the Chinese text.
func TestInstallerIncludes(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	read := func(rel string) string {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.HasPrefix(raw, []byte{0xEF, 0xBB, 0xBF}) {
			t.Fatalf("%s has no UTF-8 BOM", rel)
		}
		return string(raw)
	}
	common := read("assets/installer/shortcuts.nsh")
	for _, rule := range []string{
		"!macro customInit",
		"!macro customInstall",
		`${If} ${FileExists} "$INSTDIR\Uninstall.exe"`,
		`Delete "$SMPROGRAMS\${TRACE_SHORTCUT}.lnk"`,
		"!ifdef TRACE_DESKTOP_ON_UPDATE",
		"Call CreateDesktopShortcut",
	} {
		if !strings.Contains(common, rule) {
			t.Fatalf("shortcuts.nsh lacks %q", rule)
		}
	}
	for _, channel := range []string{ChannelStable, ChannelDev, ChannelPtest} {
		id := IdentityOf(channel)
		text := read(id.InstallerInclude)
		if !strings.Contains(text, `!define TRACE_SHORTCUT "`+id.DisplayName+`"`) || !strings.Contains(text, `!include "${__FILEDIR__}\shortcuts.nsh"`) {
			t.Fatalf("%s: %s", id.InstallerInclude, text)
		}
		if strings.Contains(text, "!define TRACE_DESKTOP_ON_UPDATE") != (channel == ChannelDev) {
			t.Fatalf("%s: only dev makes the desktop shortcut again", id.InstallerInclude)
		}
	}
}

// The icons of the two channels are different 1024x1024 PNGs, and the
// Chinese catalog names the app and the desktop shortcut (M2-K10).
func TestIconsAndChineseCatalog(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	var images [][]byte
	for _, ch := range []string{ChannelStable, ChannelDev} {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(IdentityOf(ch).Icon)))
		if err != nil {
			t.Fatal(err)
		}
		img, err := png.Decode(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		if b := img.Bounds(); b.Dx() != 1024 || b.Dy() != 1024 {
			t.Fatalf("%s icon is %v", ch, b)
		}
		images = append(images, raw)
	}
	if bytes.Equal(images[0], images[1]) {
		t.Fatal("identical channel icons")
	}
	raw, err := os.ReadFile(filepath.Join(root, "internal", "locales", "zh-Hans.json"))
	if err != nil {
		t.Fatal(err)
	}
	var catalog map[string]string
	if err := json.Unmarshal(raw, &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog["@@locale"] != "zh-Hans" || catalog["mygo.appName"] != "TRACE" || catalog["mygo.desktopShortcut"] != "创建桌面快捷方式" || !strings.Contains(catalog["mygo.appDescription"], "哔哩哔哩") {
		t.Fatalf("catalog: %v", catalog)
	}
}

func TestVersionFileRoundTrip(t *testing.T) {
	info := Info{Version: "0.1.1", Channel: ChannelStable, Commit: "abc", BuildTime: "2026-11-09T15:30:00Z"}
	got := ParseVersionFile(VersionFile(info))
	if got["version"] != "0.1.1" || got["channel"] != "stable" || got["commit"] != "abc" || got["built_at"] != "2026-11-09T15:30:00Z" {
		t.Fatalf("%v", got)
	}
}

func TestWithConfigRestoresOnFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mygo.json")
	if err := os.WriteFile(path, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := WithConfig(path, []byte("channel"), func() error {
		raw, _ := os.ReadFile(path)
		if string(raw) != "channel" {
			t.Fatal("channel config not in place")
		}
		return os.ErrInvalid
	})
	if err != os.ErrInvalid {
		t.Fatalf("error not passed on: %v", err)
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "original" {
		t.Fatal("mygo.json not restored")
	}
}
