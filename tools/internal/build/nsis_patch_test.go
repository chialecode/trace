package build

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sampleMyGoNSI(displayName, shortcut, includePath string, renamed bool) []byte {
	renamedBlock := ""
	if renamed {
		renamedBlock = "  Delete \"$SMPROGRAMS\\trace.lnk\"\n" +
			"  IfFileExists \"$DESKTOP\\trace.lnk\" 0 +3\n" +
			"  Delete \"$DESKTOP\\trace.lnk\"\n" +
			"  Call CreateDesktopShortcut\n"
	}
	script := `Unicode true
ManifestDPIAware true
SetCompressor /SOLID lzma
Name "` + displayName + `"
OutFile "C:\out\setup.exe"
InstallDir "$LOCALAPPDATA\Programs\trace-dev"
InstallDirRegKey HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\io.github.chialecode.trace-dev" "InstallLocation"
RequestExecutionLevel user
BrandingText " "
!define MUI_FINISHPAGE_RUN "$INSTDIR\trace.exe"
!define MUI_FINISHPAGE_SHOWREADME
!define MUI_FINISHPAGE_SHOWREADME_TEXT "$(MYGO_DESKTOP_SHORTCUT)"
!define MUI_FINISHPAGE_SHOWREADME_FUNCTION CreateDesktopShortcut
!include "MUI2.nsh"
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "SimpChinese"
LangString MYGO_DESKTOP_SHORTCUT ${LANG_SIMPCHINESE} "创建桌面快捷方式"
!include "` + includePath + `"
!ifmacrodef customInit
Function .onInit
  !insertmacro customInit
FunctionEnd
!endif

Section
  SetOutPath "$INSTDIR"
  File "C:\stage\trace.exe"
  WriteUninstaller "$INSTDIR\Uninstall.exe"
` + renamedBlock + `  CreateShortCut "$SMPROGRAMS\` + shortcut + `.lnk" "$INSTDIR\trace.exe"
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\io.github.chialecode.trace-dev" "DisplayName" "` + displayName + `"
  !ifmacrodef customInstall
    !insertmacro customInstall
  !endif
SectionEnd

Function CreateDesktopShortcut
  SetOutPath "$INSTDIR"
  CreateShortCut "$DESKTOP\` + shortcut + `.lnk" "$INSTDIR\trace.exe"
FunctionEnd

Section "Uninstall"
  Push "$SMPROGRAMS\` + shortcut + `.lnk"
  Call un.DeleteShortcut
  Push "$DESKTOP\` + shortcut + `.lnk"
  Call un.DeleteShortcut
  DeleteRegKey HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\io.github.chialecode.trace-dev"
  RMDir /r "$INSTDIR"
SectionEnd
`
	return append([]byte{0xEF, 0xBB, 0xBF}, script...)
}

func TestPatchInstallerScriptDevAndTestChannels(t *testing.T) {
	for _, tc := range []struct {
		name        string
		displayName string
		shortcut    string
		include     string
	}{
		{"dev", "TRACE Dev", "TRACE Dev", `assets\installer\dev.nsh`},
		{"test", "TRACE Test", "TRACE Test", `assets\installer\test.nsh`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := sampleMyGoNSI(tc.displayName, tc.shortcut, tc.include, true)
			got, err := PatchInstallerScript(raw)
			if err != nil {
				t.Fatalf("PatchInstallerScript: %v", err)
			}
			if !bytes.HasPrefix(got, utf8BOM) {
				t.Fatal("patched script lost UTF-8 BOM")
			}
			text := string(got[len(utf8BOM):])
			wantHook := "  !ifmacrodef customStartMenuShortcut\n" +
				"    !insertmacro customStartMenuShortcut\n" +
				"  !else\n" +
				"    CreateShortCut \"$SMPROGRAMS\\" + tc.shortcut + ".lnk\" \"$INSTDIR\\trace.exe\"\n" +
				"  !endif\n"
			if !strings.Contains(text, wantHook) {
				t.Fatalf("missing customStartMenuShortcut hook:\n%s", text)
			}
			for _, forbidden := range []string{
				`Delete "$SMPROGRAMS\trace.lnk"`,
				`IfFileExists "$DESKTOP\trace.lnk"`,
				`Delete "$DESKTOP\trace.lnk"`,
			} {
				if strings.Contains(text, forbidden) {
					t.Errorf("patched %s script still contains cross-channel cleanup %q", tc.name, forbidden)
				}
			}
			// Uninstall section and desktop helper remain intact.
			for _, keep := range []string{
				`CreateShortCut "$DESKTOP\` + tc.shortcut + `.lnk" "$INSTDIR\trace.exe"`,
				`Push "$SMPROGRAMS\` + tc.shortcut + `.lnk"`,
				`Push "$DESKTOP\` + tc.shortcut + `.lnk"`,
				`RMDir /r "$INSTDIR"`,
			} {
				if !strings.Contains(text, keep) {
					t.Errorf("patched %s script lost %q", tc.name, keep)
				}
			}
		})
	}
}

func TestPatchInstallerScriptStableChannelAndCRLF(t *testing.T) {
	raw := sampleMyGoNSI("TRACE", "TRACE", `assets\installer\stable.nsh`, false)
	crlf := bytes.ReplaceAll(raw, []byte("\n"), []byte("\r\n"))

	got, err := PatchInstallerScript(crlf)
	if err != nil {
		t.Fatalf("PatchInstallerScript: %v", err)
	}
	if !bytes.HasPrefix(got, utf8BOM) {
		t.Fatal("lost UTF-8 BOM")
	}
	text := string(got[len(utf8BOM):])
	if !strings.Contains(text, "!ifmacrodef customStartMenuShortcut\r\n    !insertmacro customStartMenuShortcut\r\n") {
		t.Fatalf("did not preserve CRLF in patched hook:\n%s", text)
	}
	if strings.Contains(text, "Stripped cross-channel") {
		t.Fatal("stable channel should not log stripped cross-channel block")
	}
}

func TestPatchInstallerScriptFailsLoudlyOnUnexpectedTemplate(t *testing.T) {
	t.Run("missing_start_menu_shortcut", func(t *testing.T) {
		raw := []byte("Section\n  WriteUninstaller \"$INSTDIR\\Uninstall.exe\"\nSectionEnd\n")
		if _, err := PatchInstallerScript(raw); err == nil {
			t.Fatal("expected error when Start Menu CreateShortCut is missing")
		}
	})

	t.Run("duplicate_start_menu_shortcut", func(t *testing.T) {
		raw := []byte("Section\n" +
			"  CreateShortCut \"$SMPROGRAMS\\TRACE.lnk\" \"$INSTDIR\\trace.exe\"\n" +
			"  CreateShortCut \"$SMPROGRAMS\\TRACE.lnk\" \"$INSTDIR\\trace.exe\"\n" +
			"SectionEnd\n")
		if _, err := PatchInstallerScript(raw); err == nil {
			t.Fatal("expected error when multiple Start Menu CreateShortCut lines exist")
		}
	})

	t.Run("already_patched", func(t *testing.T) {
		raw := sampleMyGoNSI("TRACE", "TRACE", `assets\installer\stable.nsh`, false)
		first, err := PatchInstallerScript(raw)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := PatchInstallerScript(first); err == nil {
			t.Fatal("expected error when patching an already-patched script")
		}
	})

	t.Run("unrecognized_cross_channel_cleanup", func(t *testing.T) {
		raw := []byte("Section\n" +
			"  Delete \"$SMPROGRAMS\\trace.lnk\"\n" +
			"  WriteUninstaller \"$INSTDIR\\Uninstall.exe\"\n" +
			"  CreateShortCut \"$SMPROGRAMS\\TRACE Dev.lnk\" \"$INSTDIR\\trace.exe\"\n" +
			"SectionEnd\n")
		if _, err := PatchInstallerScript(raw); err == nil {
			t.Fatal("expected error when unexpected cross-channel Delete remains in Section")
		}
	})
}

type fakeFileInfo struct {
	name string
	dir  bool
}

func (f fakeFileInfo) Name() string       { return f.name }
func (f fakeFileInfo) Size() int64        { return 1 }
func (f fakeFileInfo) Mode() os.FileMode  { return 0o755 }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return f.dir }
func (f fakeFileInfo) Sys() any           { return nil }

func TestFindRealMakensisSearchOrder(t *testing.T) {
	exists := map[string]bool{}
	statFn := func(p string) (os.FileInfo, error) {
		if exists[filepath.Clean(p)] {
			return fakeFileInfo{name: filepath.Base(p)}, nil
		}
		return nil, os.ErrNotExist
	}
	globFn := func(pattern string) ([]string, error) {
		var out []string
		for k := range exists {
			if ok, _ := filepath.Match(pattern, k); ok {
				out = append(out, k)
			}
		}
		return out, nil
	}

	wrapDir := filepath.Join(t.TempDir(), "wrap")
	binDir := filepath.Join(t.TempDir(), "bin")
	pf86 := filepath.Join(t.TempDir(), "pf86")
	cache := filepath.Join(t.TempDir(), "cache")

	wrapExe := filepath.Join(wrapDir, "makensis.exe")
	pathExe := filepath.Join(binDir, "makensis.exe")
	pfExe := filepath.Join(pf86, "NSIS", "makensis.exe")
	cache313 := filepath.Join(cache, "mygo", "nsis-3.13", "Bin", "makensis.exe")
	cache314 := filepath.Join(cache, "mygo", "nsis-3.14", "Bin", "makensis.exe")

	exists[filepath.Clean(wrapExe)] = true
	exists[filepath.Clean(cache314)] = true

	getenv := func(k string) string {
		if k == "ProgramFiles(x86)" {
			return pf86
		}
		return ""
	}
	userCache := func() (string, error) { return cache, nil }

	// Skipping wrapDir falls back to cached nsis-3.14 glob when 3.13 absent.
	got := findRealMakensis(wrapDir, statFn, globFn, userCache, "windows", getenv, wrapDir)
	if !strings.EqualFold(got, cache314) {
		t.Fatalf("got %q, want %q", got, cache314)
	}

	// Pinned nsis-3.13 takes precedence over generic glob.
	exists[filepath.Clean(cache313)] = true
	got = findRealMakensis(wrapDir, statFn, globFn, userCache, "windows", getenv, wrapDir)
	if !strings.EqualFold(got, cache313) {
		t.Fatalf("got %q, want %q", got, cache313)
	}

	// Program Files takes precedence over cache.
	exists[filepath.Clean(pfExe)] = true
	got = findRealMakensis(wrapDir, statFn, globFn, userCache, "windows", getenv, wrapDir)
	if !strings.EqualFold(got, pfExe) {
		t.Fatalf("got %q, want %q", got, pfExe)
	}

	// PATH entry (other than wrapDir) takes precedence over Program Files.
	exists[filepath.Clean(pathExe)] = true
	got = findRealMakensis(wrapDir+string(os.PathListSeparator)+binDir, statFn, globFn, userCache, "windows", getenv, wrapDir)
	if !strings.EqualFold(got, pathExe) {
		t.Fatalf("got %q, want %q", got, pathExe)
	}
}

func TestEnvVarHelpers(t *testing.T) {
	env := []string{"FOO=1", "PATH=/old", "BAR=2"}
	env = SetEnvVar(env, "PATH", "/new:/old")
	if got := GetEnvVar(env, "PATH"); got != "/new:/old" {
		t.Fatalf("GetEnvVar(PATH) = %q", got)
	}
	env = UnsetEnvVar(env, "FOO")
	if got := GetEnvVar(env, "FOO"); got != "" {
		t.Fatalf("FOO not unset: %q", got)
	}
}
