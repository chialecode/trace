package build

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/chialecode/trace/internal/appdirs"
)

// The v1.0.0 module archive omits LICENSE. The next upstream commit adds only
// this license to the same source; provenance is recorded beside the file.
//
//go:embed licenses/reedsolomon-v1.0.0.txt
var reedSolomonLicense string

type goModuleInfo struct {
	Path    string `json:"Path"`
	Version string `json:"Version"`
	Dir     string `json:"Dir"`
	Main    bool   `json:"Main"`
}

// MygoBuild runs `go tool mygo build` for windows/amd64 into out with the
// mygo.json in place and the given -ldflags. env adds variables, such as
// the update signing key, to the build alone.
func MygoBuild(root, out, ldflags string, env []string) (string, error) {
	cmd := exec.Command("go", "tool", "mygo", "build", "-platform", "windows/amd64", "-o", out)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOFLAGS=-buildvcs=false", "MYGO_LDFLAGS="+ldflags)
	cmd.Env = append(cmd.Env, env...)
	appdirs.SetSubprocessQuiet(cmd)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		return buf.String(), fmt.Errorf("mygo build failed: %w\n%s", err, buf.String())
	}
	return buf.String(), nil
}

// ThirdPartyNotices writes THIRD-PARTY-NOTICES.txt: the licenses of the Go
// standard library, the bundled font and every module the build uses.
func ThirdPartyNotices(root, outPath string) error {
	var b strings.Builder
	b.WriteString("TRACE Third-Party Notices\n")
	b.WriteString("=========================\n\n")

	goRootCmd := exec.Command("go", "env", "GOROOT")
	appdirs.SetSubprocessQuiet(goRootCmd)
	if goRootOut, err := goRootCmd.Output(); err == nil {
		goRoot := strings.TrimSpace(string(goRootOut))
		for _, candidate := range []string{filepath.Join(goRoot, "LICENSE"), filepath.Join(filepath.Dir(goRoot), "LICENSE")} {
			if raw, rErr := os.ReadFile(candidate); rErr == nil {
				b.WriteString("------------------------------------------------------------------------\n")
				b.WriteString(fmt.Sprintf("Component: Go Standard Library (%s)\n", runtime.Version()))
				b.WriteString("------------------------------------------------------------------------\n")
				b.WriteString(strings.TrimSpace(string(raw)) + "\n\n")
				break
			}
		}
	}

	ofl, err := os.ReadFile(filepath.Join(root, "internal", "ui", "fonts", "OFL.txt"))
	if err != nil || len(bytes.TrimSpace(ofl)) == 0 {
		return fmt.Errorf("missing license for the bundled Inter font: %v", err)
	}
	b.WriteString("------------------------------------------------------------------------\n")
	b.WriteString("Component: Inter 4.1 font (internal/ui/fonts/InterVariable.ttf)\n")
	b.WriteString("------------------------------------------------------------------------\n")
	b.WriteString(strings.TrimSpace(string(ofl)) + "\n\n")

	cmd := exec.Command("go", "list", "-m", "-json", "all")
	cmd.Dir = root
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
	licenseNames := []string{"LICENSE", "LICENSE.md", "LICENSE.txt", "COPYING", "COPYING.md", "COPYING.txt", "NOTICE", "NOTICE.md", "NOTICE.txt", "PATENTS"}
	for _, m := range mods {
		b.WriteString("------------------------------------------------------------------------\n")
		b.WriteString(fmt.Sprintf("Module: %s %s\n", m.Path, m.Version))
		b.WriteString("------------------------------------------------------------------------\n")
		foundAny := false
		for _, lname := range licenseNames {
			if raw, err := os.ReadFile(filepath.Join(m.Dir, lname)); err == nil && len(raw) > 0 {
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

// SHA256File returns the hex SHA-256 of a file.
func SHA256File(path string) (string, error) {
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

// WriteSums writes "<sha256>  <name>" lines of the files of dir, sorted by
// name, to dir/sumsName and returns its content.
func WriteSums(dir, sumsName string, names []string) (string, error) {
	names = append([]string(nil), names...)
	sort.Strings(names)
	var lines []string
	for _, name := range names {
		sum, err := SHA256File(filepath.Join(dir, name))
		if err != nil {
			return "", fmt.Errorf("hash %s: %w", name, err)
		}
		lines = append(lines, fmt.Sprintf("%s  %s", sum, name))
	}
	content := strings.Join(lines, "\n") + "\n"
	return content, os.WriteFile(filepath.Join(dir, sumsName), []byte(content), 0o644)
}

// VerifySums checks the "<sha256>  <name>" lines of dir/sumsName.
func VerifySums(dir, sumsName string) ([]string, error) {
	raw, err := os.ReadFile(filepath.Join(dir, sumsName))
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || !validBundleName(fields[1]) {
			return nil, fmt.Errorf("%s: invalid line %q", sumsName, line)
		}
		got, err := SHA256File(filepath.Join(dir, fields[1]))
		if err != nil {
			return nil, err
		}
		if got != fields[0] {
			return nil, fmt.Errorf("%s: %s does not match", sumsName, fields[1])
		}
		names = append(names, fields[1])
	}
	return names, nil
}

// CopyFile copies src to dst and syncs it.
func CopyFile(src, dst string) error { return copyFile(src, dst) }

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
