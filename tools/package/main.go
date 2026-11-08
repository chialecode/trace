package main

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
)

type mygoConfig struct {
	Name       string      `json:"name"`
	Slug       string      `json:"slug"`
	Identifier string      `json:"identifier"`
	Version    string      `json:"version"`
	Main       string      `json:"main"`
	Out        string      `json:"out"`
	Windows    mygoWindows `json:"windows"`
}

type mygoWindows struct {
	DisplayName    string `json:"displayName"`
	InstallDirName string `json:"installDirName"`
}

type goModuleInfo struct {
	Path    string `json:"Path"`
	Version string `json:"Version"`
	Dir     string `json:"Dir"`
	Main    bool   `json:"Main"`
}

// The v1.0.0 module archive omits LICENSE. The next upstream commit adds only
// this license to the same source; provenance is recorded beside the file.
//
//go:embed licenses/reedsolomon-v1.0.0.txt
var reedSolomonLicense string

func main() {
	appdirs.LowerCurrentProcessPriority()

	channelFlag := flag.String("channel", "dev", "build channel: dev or release")
	installFlag := flag.Bool("install", false, "silently install package after building (only when requested by user)")
	refreshVersionFlag := flag.Bool("refresh-version", false, "refresh VERSION.txt and SHA256SUMS.txt in dist/<channel>/ without rebuilding binaries")
	flag.Parse()

	ch := strings.ToLower(strings.TrimSpace(*channelFlag))
	if ch != "dev" && ch != "release" {
		fmt.Fprintf(os.Stderr, "package: unsupported -channel %q (want dev or release)\n", ch)
		os.Exit(1)
	}

	started := time.Now()
	var err error
	if *refreshVersionFlag {
		err = refreshVersionAndSums(ch)
	} else {
		err = runPackage(ch, *installFlag)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "package failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("package (%s) completed in %s\n", ch, time.Since(started).Round(time.Millisecond))
}

func refreshVersionAndSums(channel string) error {
	origConfigBytes, err := os.ReadFile("mygo.json")
	if err != nil {
		return fmt.Errorf("read mygo.json: %w", err)
	}
	var cfg mygoConfig
	if err := json.Unmarshal(origConfigBytes, &cfg); err != nil {
		return fmt.Errorf("parse mygo.json: %w", err)
	}
	if cfg.Version == "" {
		cfg.Version = "0.1.0"
	}
	targetFolder := "trace"
	slug := "trace"
	if channel == "dev" {
		targetFolder = "trace-dev"
		slug = "trace-dev"
	}
	destDir := filepath.Join("dist", targetFolder)
	exeName := "trace.exe"
	setupName := fmt.Sprintf("%s-setup-%s-amd64.exe", slug, cfg.Version)

	stagedDir, err := os.MkdirTemp("dist", ".version-"+targetFolder+"-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stagedDir)
	versionPath := filepath.Join(stagedDir, "VERSION.txt")
	if err := generateVersionFile(versionPath, cfg.Version, channel); err != nil {
		return fmt.Errorf("generate VERSION.txt: %w", err)
	}

	filesToHash := []string{
		exeName,
		setupName,
		"THIRD-PARTY-NOTICES.txt",
		"VERSION.txt",
	}
	sort.Strings(filesToHash)
	var sumLines []string
	for _, name := range filesToHash {
		path := filepath.Join(destDir, name)
		if name == "VERSION.txt" {
			path = versionPath
		}
		sum, err := sha256File(path)
		if err != nil {
			return fmt.Errorf("hash %s: %w", name, err)
		}
		sumLines = append(sumLines, fmt.Sprintf("%s  %s", sum, name))
	}
	sumsContent := strings.Join(sumLines, "\n") + "\n"
	sumsPath := filepath.Join(stagedDir, "SHA256SUMS.txt")
	if err := os.WriteFile(sumsPath, []byte(sumsContent), 0o644); err != nil {
		return fmt.Errorf("write SHA256SUMS.txt: %w", err)
	}
	if err := publishBundle(stagedDir, destDir, []string{"VERSION.txt", "SHA256SUMS.txt"}, false); err != nil {
		return err
	}
	fmt.Printf("==> [package] refreshed VERSION.txt and SHA256SUMS.txt in %s:\n%s", destDir, sumsContent)
	return nil
}

func runPackage(channel string, install bool) error {
	origConfigBytes, err := os.ReadFile("mygo.json")
	if err != nil {
		return fmt.Errorf("read mygo.json: %w", err)
	}
	defer func() {
		_ = os.WriteFile("mygo.json", origConfigBytes, 0o644)
	}()

	var cfg mygoConfig
	if err := json.Unmarshal(origConfigBytes, &cfg); err != nil {
		return fmt.Errorf("parse mygo.json: %w", err)
	}
	if cfg.Version == "" {
		cfg.Version = "0.1.0"
	}

	targetFolder := "trace"
	cfg.Name = "trace"
	cfg.Main = "."
	cfg.Out = "dist"
	if channel == "dev" {
		targetFolder = "trace-dev"
		cfg.Slug = "trace-dev"
		cfg.Identifier = "io.github.chialecode.trace-dev"
		cfg.Windows.DisplayName = "TRACE Dev"
		cfg.Windows.InstallDirName = "trace-dev"
	} else {
		targetFolder = "trace"
		cfg.Slug = "trace"
		cfg.Identifier = "io.github.chialecode.trace"
		cfg.Windows.DisplayName = "TRACE"
		cfg.Windows.InstallDirName = "trace"
	}

	cfgBytes, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	cfgBytes = append(cfgBytes, '\n')
	if err := os.WriteFile("mygo.json", cfgBytes, 0o644); err != nil {
		return fmt.Errorf("write channel mygo.json: %w", err)
	}

	if err := os.MkdirAll("dist", 0o755); err != nil {
		return err
	}
	stagingBase, err := os.MkdirTemp("dist", fmt.Sprintf(".staging-%s-", time.Now().UTC().Format("20060102-150405")))
	if err != nil {
		return fmt.Errorf("create staging dir: %w", err)
	}
	defer os.RemoveAll(stagingBase)

	ldflags := fmt.Sprintf("-X github.com/chialecode/trace/internal/appdirs.channel=%s -X github.com/chialecode/trace/internal/appdirs.version=%s", channel, cfg.Version)
	cmd := exec.Command("go", "tool", "mygo", "build", "-platform", "windows/amd64", "-o", stagingBase)
	cmd.Env = append(os.Environ(),
		"CGO_ENABLED=0",
		"GOFLAGS=-buildvcs=false",
		"MYGO_LDFLAGS="+ldflags,
	)
	appdirs.SetSubprocessQuiet(cmd)
	var buildOut bytes.Buffer
	cmd.Stdout = &buildOut
	cmd.Stderr = &buildOut
	fmt.Printf("==> [package] running go tool mygo build (%s)...\n", channel)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("mygo build failed: %w\n%s", err, buildOut.String())
	}
	fmt.Print(buildOut.String())

	builtDir := filepath.Join(stagingBase, "windows-amd64")
	exeName := "trace.exe"
	setupName := fmt.Sprintf("%s-setup-%s-amd64.exe", cfg.Slug, cfg.Version)

	exePath := filepath.Join(builtDir, exeName)
	setupPath := filepath.Join(builtDir, setupName)
	for _, p := range []string{exePath, setupPath} {
		st, err := os.Stat(p)
		if err != nil || st.Size() == 0 {
			return fmt.Errorf("missing or empty artifact %s: %v", p, err)
		}
	}

	// Generate THIRD-PARTY-NOTICES.txt
	noticesPath := filepath.Join(builtDir, "THIRD-PARTY-NOTICES.txt")
	if err := generateThirdPartyNotices(noticesPath); err != nil {
		return fmt.Errorf("generate THIRD-PARTY-NOTICES.txt: %w", err)
	}

	// Generate VERSION.txt
	versionPath := filepath.Join(builtDir, "VERSION.txt")
	if err := generateVersionFile(versionPath, cfg.Version, channel); err != nil {
		return fmt.Errorf("generate VERSION.txt: %w", err)
	}

	// Generate SHA256SUMS.txt
	filesToHash := []string{
		exeName,
		setupName,
		"THIRD-PARTY-NOTICES.txt",
		"VERSION.txt",
	}
	sort.Strings(filesToHash)
	var sumLines []string
	for _, name := range filesToHash {
		sum, err := sha256File(filepath.Join(builtDir, name))
		if err != nil {
			return fmt.Errorf("hash %s: %w", name, err)
		}
		sumLines = append(sumLines, fmt.Sprintf("%s  %s", sum, name))
	}
	sumsContent := strings.Join(sumLines, "\n") + "\n"
	sumsPath := filepath.Join(builtDir, "SHA256SUMS.txt")
	if err := os.WriteFile(sumsPath, []byte(sumsContent), 0o644); err != nil {
		return fmt.Errorf("write SHA256SUMS.txt: %w", err)
	}

	// Replace registered files in dist/<targetFolder>/ without touching other dist/* folders
	destDir := filepath.Join("dist", targetFolder)
	allFiles := append(append([]string{}, filesToHash...), "SHA256SUMS.txt")
	if err := publishBundle(builtDir, destDir, allFiles, true); err != nil {
		return err
	}

	fmt.Printf("==> [package] staged and verified into %s:\n%s", destDir, sumsContent)

	if install {
		installer := filepath.Join(destDir, setupName)
		fmt.Printf("==> [package] running silent installer %s /S ...\n", installer)
		icmd := exec.Command(installer, "/S")
		appdirs.SetSubprocessQuiet(icmd)
		if err := icmd.Run(); err != nil {
			return fmt.Errorf("silent install failed: %w", err)
		}
	}
	return nil
}

func generateVersionFile(path, version, channel string) error {
	commit := "unknown"
	if out, err := exec.Command("git", "rev-parse", "HEAD").Output(); err == nil {
		commit = strings.TrimSpace(string(out))
	}
	content := fmt.Sprintf("version=%s\nchannel=%s\ncommit=%s\ngo=%s\nos=%s\narch=%s\nbuilt_at=%s\n",
		version, channel, commit, runtime.Version(), "windows", "amd64", time.Now().UTC().Format(time.RFC3339))
	return os.WriteFile(path, []byte(content), 0o644)
}

func generateThirdPartyNotices(outPath string) error {
	var b strings.Builder
	b.WriteString("TRACE Third-Party Notices\n")
	b.WriteString("=========================\n\n")

	// 1. Go standard library license
	goRootOut, err := exec.Command("go", "env", "GOROOT").Output()
	if err == nil {
		goRoot := strings.TrimSpace(string(goRootOut))
		for _, candidate := range []string{
			filepath.Join(goRoot, "LICENSE"),
			filepath.Join(filepath.Dir(goRoot), "LICENSE"),
		} {
			if raw, rErr := os.ReadFile(candidate); rErr == nil {
				b.WriteString("------------------------------------------------------------------------\n")
				b.WriteString(fmt.Sprintf("Component: Go Standard Library (%s)\n", runtime.Version()))
				b.WriteString("------------------------------------------------------------------------\n")
				b.WriteString(strings.TrimSpace(string(raw)) + "\n\n")
				break
			}
		}
	}

	// 2. Bundled assets that are not Go modules: the interface font.
	ofl, err := os.ReadFile(filepath.Join("internal", "ui", "fonts", "OFL.txt"))
	if err != nil || len(bytes.TrimSpace(ofl)) == 0 {
		return fmt.Errorf("missing license for the bundled Inter font: %v", err)
	}
	b.WriteString("------------------------------------------------------------------------\n")
	b.WriteString("Component: Inter 4.1 font (internal/ui/fonts/InterVariable.ttf)\n")
	b.WriteString("------------------------------------------------------------------------\n")
	b.WriteString(strings.TrimSpace(string(ofl)) + "\n\n")

	// 3. All Go modules from `go list -m -json all`
	cmd := exec.Command("go", "list", "-m", "-json", "all")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=windows", "GOARCH=amd64")
	appdirs.SetSubprocessQuiet(cmd)
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("go list -m -json all: %w", err)
	}

	dec := json.NewDecoder(bytes.NewReader(out))
	var mods []goModuleInfo
	for {
		var m goModuleInfo
		if err := dec.Decode(&m); err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
		if m.Main || m.Dir == "" {
			continue
		}
		mods = append(mods, m)
	}
	sort.Slice(mods, func(i, j int) bool { return mods[i].Path < mods[j].Path })

	licenseNames := []string{
		"LICENSE", "LICENSE.md", "LICENSE.txt",
		"COPYING", "COPYING.md", "COPYING.txt",
		"NOTICE", "NOTICE.md", "NOTICE.txt",
		"PATENTS",
	}

	for _, m := range mods {
		b.WriteString("------------------------------------------------------------------------\n")
		b.WriteString(fmt.Sprintf("Module: %s %s\n", m.Path, m.Version))
		b.WriteString("------------------------------------------------------------------------\n")
		foundAny := false
		for _, lname := range licenseNames {
			lpath := filepath.Join(m.Dir, lname)
			if raw, err := os.ReadFile(lpath); err == nil && len(raw) > 0 {
				foundAny = true
				b.WriteString(fmt.Sprintf("[%s]\n%s\n\n", lname, strings.TrimSpace(string(raw))))
			}
		}
		if !foundAny {
			if m.Path == "github.com/yeqown/reedsolomon" && m.Version == "v1.0.0" {
				b.WriteString("[LICENSE (upstream c5f4bc9af094852b52e593a5f964647c43028c51)]\n")
				b.WriteString(reedSolomonLicense + "\n")
			} else {
				return fmt.Errorf("missing license files for module %s %s", m.Path, m.Version)
			}
		}
	}

	return os.WriteFile(outPath, []byte(b.String()), 0o644)
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
