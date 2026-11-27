package build

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

const (
	// EnvRealMakensis holds the path to the real makensis executable that
	// tools/internal/nsiswrap invokes after patching MyGo's installer.nsi.
	EnvRealMakensis = "TRACE_REAL_MAKENSIS"
	// EnvNSISWrapReceipt is an optional file path written by
	// tools/internal/nsiswrap only after PatchInstallerScript and real
	// makensis both succeed.
	EnvNSISWrapReceipt = "TRACE_NSISWRAP_RECEIPT"
	// EnvNSISWrapDump is an optional file path where tools/internal/nsiswrap
	// writes a copy of the patched installer.nsi for verification.
	EnvNSISWrapDump = "TRACE_NSISWRAP_DUMP"
)

var (
	utf8BOM           = []byte{0xEF, 0xBB, 0xBF}
	startMenuCreateRe = regexp.MustCompile(`^([ \t]*)CreateShortCut[ \t]+"\$SMPROGRAMS\\([^"]+)"[ \t]+"\$INSTDIR\\([^"]+)"[ \t]*$`)
)

// PatchInstallerScript rewrites MyGo's generated installer.nsi so that:
//  1. The unconditional Start Menu CreateShortCut in the install Section
//     delegates to customStartMenuShortcut when defined by installerInclude.
//  2. On non-stable channels (shortcut != "<exeStem>.lnk"), MyGo's automatic
//     nsisRenamedShortcuts block that deletes "$SMPROGRAMS\<exeStem>.lnk" and
//     "$DESKTOP\<exeStem>.lnk" is stripped so dev/test channels never disturb
//     the stable channel's shortcuts.
//
// It preserves UTF-8 BOM and line endings, and fails loudly if the expected
// MyGo template shape is absent or ambiguous.
func PatchInstallerScript(raw []byte) ([]byte, error) {
	hasBOM := bytes.HasPrefix(raw, utf8BOM)
	text := string(raw)
	if hasBOM {
		text = text[len(utf8BOM):]
	}
	eol := "\n"
	if strings.Contains(text, "\r\n") {
		eol = "\r\n"
	}
	norm := strings.ReplaceAll(text, "\r\n", "\n")
	if strings.Contains(norm, "!insertmacro customStartMenuShortcut") {
		return nil, errors.New("installer.nsi already delegates customStartMenuShortcut")
	}

	lines := strings.Split(norm, "\n")

	type match struct {
		idx     int
		indent  string
		lnkName string
		exeName string
	}
	var matches []match
	inInstallSection := false
	installSectionStart := -1
	installSectionEnd := -1

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "Section" || (strings.HasPrefix(trimmed, "Section ") && !strings.Contains(strings.ToLower(trimmed), "uninstall")):
			inInstallSection = true
			if installSectionStart < 0 {
				installSectionStart = i
			}
		case trimmed == "SectionEnd":
			if inInstallSection && installSectionEnd < 0 {
				installSectionEnd = i
			}
			inInstallSection = false
		}
		if !inInstallSection {
			continue
		}
		if m := startMenuCreateRe.FindStringSubmatch(line); m != nil {
			matches = append(matches, match{
				idx:     i,
				indent:  m[1],
				lnkName: m[2],
				exeName: m[3],
			})
		}
	}

	if len(matches) == 0 {
		return nil, errors.New("installer.nsi has no unconditional Start Menu CreateShortCut in install Section")
	}
	if len(matches) > 1 {
		return nil, fmt.Errorf("installer.nsi has %d Start Menu CreateShortCut lines in install Section, want 1", len(matches))
	}

	m := matches[0]
	exeStem := strings.TrimSuffix(m.exeName, filepath.Ext(m.exeName))
	oldLnk := exeStem + ".lnk"
	stripStart := -1
	stripEnd := -1

	if !strings.EqualFold(m.lnkName, oldLnk) && m.idx >= 4 {
		wantBlock := []string{
			`Delete "$SMPROGRAMS\` + oldLnk + `"`,
			`IfFileExists "$DESKTOP\` + oldLnk + `" 0 +3`,
			`Delete "$DESKTOP\` + oldLnk + `"`,
			`Call CreateDesktopShortcut`,
		}
		blockMatches := true
		for j, want := range wantBlock {
			if !strings.EqualFold(strings.TrimSpace(lines[m.idx-4+j]), want) {
				blockMatches = false
				break
			}
		}
		if blockMatches {
			stripStart = m.idx - 4
			stripEnd = m.idx
		}
	}

	// Build patched lines.
	out := make([]string, 0, len(lines)+4)
	for i := 0; i < len(lines); i++ {
		if stripStart >= 0 && i == stripStart {
			out = append(out, m.indent+"; Stripped cross-channel "+oldLnk+" cleanup so this channel never touches another channel's shortcuts.")
			i = stripEnd - 1
			continue
		}
		if i == m.idx {
			out = append(out,
				m.indent+"!ifmacrodef customStartMenuShortcut",
				m.indent+"  !insertmacro customStartMenuShortcut",
				m.indent+"!else",
				m.indent+"  "+strings.TrimSpace(lines[i]),
				m.indent+"!endif",
			)
			continue
		}
		out = append(out, lines[i])
	}

	// Safety check: on non-stable channels, ensure no cross-channel Delete of
	// <exeStem>.lnk remains in the install Section (e.g. if MyGo changes its
	// nsisRenamedShortcuts template shape in a future release).
	if !strings.EqualFold(m.lnkName, oldLnk) {
		inSec := false
		for _, line := range out {
			trimmed := strings.TrimSpace(line)
			switch {
			case trimmed == "Section" || (strings.HasPrefix(trimmed, "Section ") && !strings.Contains(strings.ToLower(trimmed), "uninstall")):
				inSec = true
			case trimmed == "SectionEnd":
				inSec = false
			}
			if !inSec {
				continue
			}
			lower := strings.ToLower(trimmed)
			if strings.HasPrefix(lower, "delete ") &&
				(strings.Contains(lower, strings.ToLower(`$smprograms\`+oldLnk)) || strings.Contains(lower, strings.ToLower(`$desktop\`+oldLnk))) {
				return nil, fmt.Errorf("installer.nsi still contains cross-channel cleanup %q in install Section", trimmed)
			}
		}
	}

	joined := strings.Join(out, eol)
	if hasBOM {
		res := make([]byte, 0, len(utf8BOM)+len(joined))
		res = append(res, utf8BOM...)
		res = append(res, joined...)
		return res, nil
	}
	return []byte(joined), nil
}

// FindRealMakensis locates the real NSIS makensis executable using the same
// search order as MyGo (PATH, Program Files on Windows, and MyGo's cached NSIS).
func FindRealMakensis() string {
	return findRealMakensis(
		os.Getenv("PATH"),
		os.Stat,
		filepath.Glob,
		os.UserCacheDir,
		runtime.GOOS,
		os.Getenv,
		"",
	)
}

func findRealMakensis(
	pathEnv string,
	statFn func(string) (os.FileInfo, error),
	globFn func(string) ([]string, error),
	userCacheDir func() (string, error),
	goos string,
	getenv func(string) string,
	skipDir string,
) string {
	isRegular := func(p string) bool {
		if p == "" {
			return false
		}
		st, err := statFn(p)
		return err == nil && !st.IsDir()
	}
	sameDir := func(a, b string) bool {
		if a == "" || b == "" {
			return false
		}
		ca, cb := filepath.Clean(a), filepath.Clean(b)
		if goos == "windows" {
			return strings.EqualFold(ca, cb)
		}
		return ca == cb
	}

	names := []string{"makensis"}
	if goos == "windows" {
		names = []string{"makensis.exe", "makensis.cmd", "makensis.bat"}
	}
	for _, dir := range filepath.SplitList(pathEnv) {
		dir = strings.Trim(dir, `"`)
		if dir == "" || sameDir(dir, skipDir) {
			continue
		}
		for _, name := range names {
			cand := filepath.Join(dir, name)
			if isRegular(cand) {
				if abs, err := filepath.Abs(cand); err == nil {
					return abs
				}
				return cand
			}
		}
	}

	if goos != "windows" {
		return ""
	}
	for _, envKey := range []string{"ProgramFiles(x86)", "ProgramFiles"} {
		if base := strings.TrimSpace(getenv(envKey)); base != "" {
			cand := filepath.Join(base, "NSIS", "makensis.exe")
			if isRegular(cand) {
				return cand
			}
		}
	}
	if cache, err := userCacheDir(); err == nil && cache != "" {
		for _, rel := range []string{
			filepath.Join("mygo", "nsis-3.13", "Bin", "makensis.exe"),
			filepath.Join("mygo", "nsis-3.13", "makensis.exe"),
		} {
			cand := filepath.Join(cache, rel)
			if isRegular(cand) {
				return cand
			}
		}
		var globs []string
		for _, pattern := range []string{
			filepath.Join(cache, "mygo", "nsis-*", "Bin", "makensis.exe"),
			filepath.Join(cache, "mygo", "nsis-*", "makensis.exe"),
		} {
			if matched, err := globFn(pattern); err == nil {
				globs = append(globs, matched...)
			}
		}
		sort.Sort(sort.Reverse(sort.StringSlice(globs)))
		for _, cand := range globs {
			if isRegular(cand) {
				return cand
			}
		}
	}
	return ""
}

func envKeyEqual(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// GetEnvVar returns the value of key from an environ slice (case-insensitive on Windows).
func GetEnvVar(env []string, key string) string {
	val := ""
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && envKeyEqual(k, key) {
			val = v
		}
	}
	return val
}

// SetEnvVar replaces or appends key=value in an environ slice (case-insensitive on Windows).
func SetEnvVar(env []string, key, value string) []string {
	out := make([]string, 0, len(env)+1)
	replaced := false
	for _, kv := range env {
		if k, _, ok := strings.Cut(kv, "="); ok && envKeyEqual(k, key) {
			if !replaced {
				out = append(out, key+"="+value)
				replaced = true
			}
			continue
		}
		out = append(out, kv)
	}
	if !replaced {
		out = append(out, key+"="+value)
	}
	return out
}

// UnsetEnvVar removes all entries matching key from an environ slice (case-insensitive on Windows).
func UnsetEnvVar(env []string, key string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if k, _, ok := strings.Cut(kv, "="); ok && envKeyEqual(k, key) {
			continue
		}
		out = append(out, kv)
	}
	return out
}
