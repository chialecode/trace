//go:build windows

package build

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows/registry"

	"github.com/chialecode/trace/internal/appdirs"
)

// TestCompiledNSISShortcutBehavior compiles the real channel installer scripts
// (assets/installer/{dev,stable,test}.nsh + shortcuts.nsh) through
// PatchInstallerScript and real makensis, then executes isolated installers
// redirected exclusively to temporary Start Menu / Desktop directories and a
// dedicated test HKCU uninstall key so the host user's real shortcuts and
// installations are never touched.
func TestCompiledNSISShortcutBehavior(t *testing.T) {
	makensis := FindRealMakensis()
	if makensis == "" {
		t.Skip("makensis not installed on this host")
	}
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	// 1. Verify all three real channel scripts compile cleanly without warnings
	//    after PatchInstallerScript (zero execution, compile-only check).
	for _, ch := range []string{ChannelStable, ChannelDev, ChannelTest} {
		t.Run("compile_"+ch, func(t *testing.T) {
			id := IdentityOf(ch)
			work := t.TempDir()
			dummyExe := filepath.Join(work, "trace.exe")
			if err := os.WriteFile(dummyExe, []byte("MZ"), 0o644); err != nil {
				t.Fatal(err)
			}
			outExe := filepath.Join(work, "setup-"+ch+".exe")
			includeAbs := filepath.Join(root, filepath.FromSlash(id.InstallerInclude))
			nsi := buildMyGoShapedNSI(id.DisplayName, id.DisplayName, id.Identifier, dummyExe, outExe, includeAbs, ch != ChannelStable)
			patched, err := PatchInstallerScript(nsi)
			if err != nil {
				t.Fatalf("PatchInstallerScript(%s): %v", ch, err)
			}
			nsiPath := filepath.Join(work, "installer.nsi")
			if err := os.WriteFile(nsiPath, patched, 0o644); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(makensis, "-V2", nsiPath)
			appdirs.SetSubprocessQuiet(cmd)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("makensis(%s) failed: %v\n%s", ch, err, out)
			}
			if strings.Contains(strings.ToLower(string(out)), "warning:") {
				t.Fatalf("makensis(%s) emitted warnings:\n%s", ch, out)
			}
		})
	}

	// 2. Execute isolated dev and stable installers to verify runtime behavior
	//    in temporary Start Menu / Desktop folders (never touching real ones).
	work := t.TempDir()
	fakeSM := filepath.Join(work, "sm")
	fakeDT := filepath.Join(work, "dt")
	if err := os.MkdirAll(fakeSM, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fakeDT, 0o755); err != nil {
		t.Fatal(err)
	}

	const devTestID = "io.github.chialecode.trace-nsis-unit-dev"
	const stableTestID = "io.github.chialecode.trace-nsis-unit-stable"
	t.Cleanup(func() {
		_ = registry.DeleteKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Uninstall\`+devTestID)
		_ = registry.DeleteKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Uninstall\`+stableTestID)
	})

	devSetup := compileIsolatedSetup(t, makensis, root, work, ChannelDev, devTestID, fakeSM, fakeDT)
	stableSetup := compileIsolatedSetup(t, makensis, root, work, ChannelStable, stableTestID, fakeSM, fakeDT)

	// Pre-place stable shortcut sentinels to prove dev never clobbers TRACE.lnk.
	stableSM := filepath.Join(fakeSM, "TRACE.lnk")
	stableDT := filepath.Join(fakeDT, "TRACE.lnk")
	past := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	for _, p := range []string{stableSM, stableDT} {
		if err := os.WriteFile(p, []byte("stable-sentinel"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, past, past); err != nil {
			t.Fatal(err)
		}
	}

	devInstDir := filepath.Join(work, "inst-dev")
	devSM := filepath.Join(fakeSM, "TRACE Dev.lnk")
	devDT := filepath.Join(fakeDT, "TRACE Dev.lnk")

	// 2a. Dev silent FIRST install: creates Start Menu + Desktop, leaves stable TRACE.lnk untouched.
	runQuietSetup(t, devSetup, "/S", "/D="+devInstDir)
	assertFileExists(t, devSM, true)
	assertFileExists(t, devDT, true)
	assertSentinelUnchanged(t, stableSM, "stable-sentinel", past)
	assertSentinelUnchanged(t, stableDT, "stable-sentinel", past)

	// 2b. Dev silent OVER-INSTALL when user deleted Start Menu & Desktop:
	//     Start Menu stays ABSENT; Desktop is RE-CREATED; stable untouched.
	if err := os.Remove(devSM); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(devDT); err != nil {
		t.Fatal(err)
	}
	runQuietSetup(t, devSetup, "/S", "/D="+devInstDir)
	assertFileExists(t, devSM, false)
	assertFileExists(t, devDT, true)
	assertSentinelUnchanged(t, stableSM, "stable-sentinel", past)
	assertSentinelUnchanged(t, stableDT, "stable-sentinel", past)

	// 2c. Dev silent OVER-INSTALL via InstallDirRegKey (plain /S without /D=, matching
	//     tools/package -install) when Start Menu IS PRESENT:
	//     Start Menu is NOT overwritten/touched (exact sentinel bytes & ModTime);
	//     Desktop IS re-created.
	keepTime := time.Now().Add(-90 * time.Minute).Truncate(time.Second)
	if err := os.WriteFile(devSM, []byte("keep-dev-start-menu"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(devSM, keepTime, keepTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(devDT); err != nil {
		t.Fatal(err)
	}
	runQuietSetup(t, devSetup, "/S")
	assertSentinelUnchanged(t, devSM, "keep-dev-start-menu", keepTime)
	assertFileExists(t, devDT, true)

	// 2d. Dev silent OVER-INSTALL with /NODESKTOP:
	//     Desktop stays absent; Start Menu still untouched.
	if err := os.Remove(devDT); err != nil {
		t.Fatal(err)
	}
	runQuietSetup(t, devSetup, "/S", "/NODESKTOP", "/D="+devInstDir)
	assertFileExists(t, devDT, false)
	assertSentinelUnchanged(t, devSM, "keep-dev-start-menu", keepTime)

	// 2e. Dev full uninstall removes both shortcuts, install dir, and registry entry.
	runQuietSetup(t, devSetup, "/S", "/D="+devInstDir) // restore devDT so uninstall deletes both
	assertFileExists(t, devDT, true)
	runQuietSetup(t, filepath.Join(devInstDir, "Uninstall.exe"), "/S")
	waitUntilRemoved(t, devInstDir)
	assertFileExists(t, devSM, false)
	assertFileExists(t, devDT, false)
	if _, ok, _ := LookupInstall(devTestID); ok {
		t.Fatal("dev uninstall left registry entry")
	}

	// 2f. Stable silent first install & silent over-install (neither Start Menu nor Desktop regenerated).
	_ = os.Remove(stableSM)
	_ = os.Remove(stableDT)
	stableInstDir := filepath.Join(work, "inst-stable")
	runQuietSetup(t, stableSetup, "/S", "/D="+stableInstDir)
	assertFileExists(t, stableSM, true)
	assertFileExists(t, stableDT, true)

	// Delete both and silently over-install stable: neither should be created.
	if err := os.Remove(stableSM); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(stableDT); err != nil {
		t.Fatal(err)
	}
	runQuietSetup(t, stableSetup, "/S", "/D="+stableInstDir)
	assertFileExists(t, stableSM, false)
	assertFileExists(t, stableDT, false)

	// Present Start Menu on stable over-install stays untouched with exact timestamp.
	if err := os.WriteFile(stableSM, []byte("keep-stable-sm"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(stableSM, keepTime, keepTime); err != nil {
		t.Fatal(err)
	}
	runQuietSetup(t, stableSetup, "/S", "/D="+stableInstDir)
	assertSentinelUnchanged(t, stableSM, "keep-stable-sm", keepTime)
	assertFileExists(t, stableDT, false)

	// Clean uninstall of stable test install.
	runQuietSetup(t, filepath.Join(stableInstDir, "Uninstall.exe"), "/S")
	waitUntilRemoved(t, stableInstDir)
	assertFileExists(t, stableSM, false)
	if _, ok, _ := LookupInstall(stableTestID); ok {
		t.Fatal("stable uninstall left registry entry")
	}
}

func compileIsolatedSetup(t *testing.T, makensis, root, work, ch, testID, fakeSM, fakeDT string) string {
	t.Helper()
	id := IdentityOf(ch)
	chWork := filepath.Join(work, "build-"+ch)
	if err := os.MkdirAll(chWork, 0o755); err != nil {
		t.Fatal(err)
	}
	dummyExe := filepath.Join(chWork, "trace.exe")
	if err := os.WriteFile(dummyExe, []byte("MZ"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Copy the channel .nsh and shortcuts.nsh into chWork, redirecting only
	// $SMPROGRAMS and $DESKTOP to isolated temp dirs while keeping 100% of the
	// control flow verbatim.
	redirect := func(raw []byte) []byte {
		raw = bytes.ReplaceAll(raw, []byte("$SMPROGRAMS"), []byte(fakeSM))
		raw = bytes.ReplaceAll(raw, []byte("$DESKTOP"), []byte(fakeDT))
		return raw
	}
	shortcutsRaw, err := os.ReadFile(filepath.Join(root, "assets", "installer", "shortcuts.nsh"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chWork, "shortcuts.nsh"), redirect(shortcutsRaw), 0o644); err != nil {
		t.Fatal(err)
	}
	chanBase := filepath.Base(id.InstallerInclude)
	chanRaw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(id.InstallerInclude)))
	if err != nil {
		t.Fatal(err)
	}
	chanInclude := filepath.Join(chWork, chanBase)
	if err := os.WriteFile(chanInclude, chanRaw, 0o644); err != nil {
		t.Fatal(err)
	}

	outExe := filepath.Join(chWork, "setup-"+ch+".exe")
	nsi := buildMyGoShapedNSI(id.DisplayName, id.DisplayName, testID, dummyExe, outExe, chanInclude, ch != ChannelStable)
	patched, err := PatchInstallerScript(nsi)
	if err != nil {
		t.Fatalf("PatchInstallerScript(%s): %v", ch, err)
	}
	isolatedNSI := redirect(patched)
	nsiPath := filepath.Join(chWork, "installer.nsi")
	if err := os.WriteFile(nsiPath, isolatedNSI, 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(makensis, "-V2", nsiPath)
	appdirs.SetSubprocessQuiet(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("makensis(%s): %v\n%s", ch, err, out)
	}
	return outExe
}

func buildMyGoShapedNSI(displayName, shortcut, identifier, dummyExe, outExe, includeAbs string, renamed bool) []byte {
	uninstallKey := `Software\Microsoft\Windows\CurrentVersion\Uninstall\` + identifier
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
OutFile "` + outExe + `"
InstallDir "$LOCALAPPDATA\Programs\trace-nsis-unit-default"
InstallDirRegKey HKCU "` + uninstallKey + `" "InstallLocation"
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
!include "` + includeAbs + `"
!ifmacrodef customInit
Function .onInit
  !insertmacro customInit
FunctionEnd
!endif

Section
  SetOutPath "$INSTDIR"
  File "` + dummyExe + `"
  WriteUninstaller "$INSTDIR\Uninstall.exe"
` + renamedBlock + `  CreateShortCut "$SMPROGRAMS\` + shortcut + `.lnk" "$INSTDIR\trace.exe"
  WriteRegStr HKCU "` + uninstallKey + `" "DisplayName" "` + displayName + `"
  WriteRegStr HKCU "` + uninstallKey + `" "DisplayVersion" "0.1.0-test"
  WriteRegStr HKCU "` + uninstallKey + `" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "` + uninstallKey + `" "UninstallString" '"$INSTDIR\Uninstall.exe"'
  WriteRegStr HKCU "` + uninstallKey + `" "QuietUninstallString" '"$INSTDIR\Uninstall.exe" /S'
  !ifmacrodef customInstall
    !insertmacro customInstall
  !endif
SectionEnd

Function CreateDesktopShortcut
  SetOutPath "$INSTDIR"
  CreateShortCut "$DESKTOP\` + shortcut + `.lnk" "$INSTDIR\trace.exe"
FunctionEnd

Function un.DeleteShortcut
  Exch $0
  Delete $0
  Pop $0
FunctionEnd

Section "Uninstall"
  Push "$SMPROGRAMS\` + shortcut + `.lnk"
  Call un.DeleteShortcut
  Push "$DESKTOP\` + shortcut + `.lnk"
  Call un.DeleteShortcut
  DeleteRegKey HKCU "` + uninstallKey + `"
  RMDir /r "$INSTDIR"
SectionEnd
`
	return append([]byte{0xEF, 0xBB, 0xBF}, script...)
}

func runQuietSetup(t *testing.T, exe string, args ...string) {
	t.Helper()
	cmd := exec.Command(exe, args...)
	appdirs.SetSubprocessQuiet(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v failed: %v\n%s", filepath.Base(exe), args, err, out)
	}
}

func assertFileExists(t *testing.T, path string, want bool) {
	t.Helper()
	_, err := os.Stat(path)
	got := err == nil
	if got != want {
		t.Fatalf("%s exists=%v, want %v (err=%v)", filepath.Base(path), got, want, err)
	}
}

func assertSentinelUnchanged(t *testing.T, path, wantBody string, wantModTime time.Time) {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", filepath.Base(path), err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Base(path), err)
	}
	if string(raw) != wantBody {
		t.Fatalf("%s content changed: got %q, want %q", filepath.Base(path), string(raw), wantBody)
	}
	if !st.ModTime().Equal(wantModTime) {
		t.Fatalf("%s ModTime changed: got %s, want %s", filepath.Base(path), st.ModTime().UTC().Format(time.RFC3339Nano), wantModTime.UTC().Format(time.RFC3339Nano))
	}
}

func waitUntilRemoved(t *testing.T, dir string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("uninstaller left %s behind", dir)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
