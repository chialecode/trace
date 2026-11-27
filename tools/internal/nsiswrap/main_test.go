package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chialecode/trace/tools/internal/build"
)

func TestMain(m *testing.M) {
	if os.Getenv("TRACE_TEST_FAKE_MAKENSIS") == "1" {
		os.Exit(runFakeMakensis())
	}
	os.Exit(m.Run())
}

// runFakeMakensis acts as the real makensis executable during unit tests: it
// records received args, checks sensitive env vars were stripped, verifies the
// .nsi file passed to it was already patched, and returns a configurable exit
// code.
func runFakeMakensis() int {
	logPath := os.Getenv("TRACE_TEST_MAKENSIS_LOG")
	var b strings.Builder
	b.WriteString("args=" + strings.Join(os.Args[1:], "|") + "\n")
	b.WriteString("key=" + os.Getenv("MYGO_UPDATER_PRIVATE_KEY") + "\n")
	b.WriteString("real=" + os.Getenv(build.EnvRealMakensis) + "\n")
	for _, arg := range os.Args[1:] {
		if strings.EqualFold(filepath.Ext(arg), ".nsi") {
			raw, err := os.ReadFile(arg)
			if err == nil {
				b.WriteString("nsi_has_hook=" + boolStr(bytes.Contains(raw, []byte("!insertmacro customStartMenuShortcut"))) + "\n")
				b.WriteString("nsi_has_cross_delete=" + boolStr(bytes.Contains(raw, []byte(`Delete "$SMPROGRAMS\trace.lnk"`))) + "\n")
			}
		}
	}
	if logPath != "" {
		_ = os.WriteFile(logPath, []byte(b.String()), 0o644)
	}
	if os.Getenv("TRACE_TEST_MAKENSIS_FAIL") == "1" {
		return 7
	}
	return 0
}

func boolStr(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func TestRunPatchesNSIAndWritesReceipt(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// Make a distinct hardlink/copy so sameExecutable(self, real) is false.
	dir := t.TempDir()
	fakeExe := filepath.Join(dir, filepath.Base(self)+".fake")
	rawSelf, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fakeExe, rawSelf, 0o755); err != nil {
		t.Fatal(err)
	}

	nsi := filepath.Join(dir, "installer.nsi")
	orig := append([]byte{0xEF, 0xBB, 0xBF}, []byte(
		"Section\n"+
			"  WriteUninstaller \"$INSTDIR\\Uninstall.exe\"\n"+
			"  Delete \"$SMPROGRAMS\\trace.lnk\"\n"+
			"  IfFileExists \"$DESKTOP\\trace.lnk\" 0 +3\n"+
			"  Delete \"$DESKTOP\\trace.lnk\"\n"+
			"  Call CreateDesktopShortcut\n"+
			"  CreateShortCut \"$SMPROGRAMS\\TRACE Dev.lnk\" \"$INSTDIR\\trace.exe\"\n"+
			"SectionEnd\n",
	)...)
	if err := os.WriteFile(nsi, orig, 0o644); err != nil {
		t.Fatal(err)
	}

	logPath := filepath.Join(dir, "fake.log")
	receipt := filepath.Join(dir, "patched.ok")
	dump := filepath.Join(dir, "dump.nsi")

	t.Setenv("TRACE_TEST_FAKE_MAKENSIS", "1")
	t.Setenv("TRACE_TEST_MAKENSIS_LOG", logPath)
	t.Setenv("MYGO_UPDATER_PRIVATE_KEY", "must-not-leak")
	t.Setenv(build.EnvRealMakensis, fakeExe)
	t.Setenv(build.EnvNSISWrapReceipt, receipt)
	t.Setenv(build.EnvNSISWrapDump, dump)

	// -VERSION forwards without touching receipt.
	if code := run([]string{"-VERSION"}); code != 0 {
		t.Fatalf("-VERSION exit code = %d, want 0", code)
	}
	if _, err := os.Stat(receipt); !os.IsNotExist(err) {
		t.Fatalf("receipt should not be written for -VERSION: %v", err)
	}

	// -V2 installer.nsi patches script, strips secret, writes dump & receipt.
	if code := run([]string{"-V2", nsi}); code != 0 {
		t.Fatalf("-V2 nsi exit code = %d, want 0", code)
	}
	logRaw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(logRaw)
	for _, want := range []string{
		"args=-V2|" + nsi,
		"key=\n",
		"real=\n",
		"nsi_has_hook=true\n",
		"nsi_has_cross_delete=false\n",
	} {
		if !strings.Contains(logText, want) {
			t.Errorf("fake makensis log lacks %q:\n%s", want, logText)
		}
	}
	if _, err := os.Stat(receipt); err != nil {
		t.Fatalf("receipt not written: %v", err)
	}
	dumpRaw, err := os.ReadFile(dump)
	if err != nil || !bytes.Contains(dumpRaw, []byte("!insertmacro customStartMenuShortcut")) {
		t.Fatalf("dump missing patched script: err=%v", err)
	}

	// Failing real makensis forwards exit code and does not write receipt.
	_ = os.Remove(receipt)
	if err := os.WriteFile(nsi, orig, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TRACE_TEST_MAKENSIS_FAIL", "1")
	if code := run([]string{"-V2", nsi}); code != 7 {
		t.Fatalf("failed makensis exit code = %d, want 7", code)
	}
	if _, err := os.Stat(receipt); !os.IsNotExist(err) {
		t.Fatalf("receipt written despite makensis failure: %v", err)
	}

	// Unpatchable script fails before calling real makensis.
	t.Setenv("TRACE_TEST_MAKENSIS_FAIL", "")
	_ = os.Remove(logPath)
	if err := os.WriteFile(nsi, []byte("Section\nSectionEnd\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"-V2", nsi}); code == 0 {
		t.Fatal("expected non-zero exit for unpatchable .nsi")
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatal("real makensis was called despite patch error")
	}
}

func TestRunRejectsMissingOrSelfRealMakensis(t *testing.T) {
	t.Setenv(build.EnvRealMakensis, "")
	if code := run([]string{"-VERSION"}); code == 0 {
		t.Fatal("expected error when TRACE_REAL_MAKENSIS is empty")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(build.EnvRealMakensis, self)
	if code := run([]string{"-VERSION"}); code == 0 {
		t.Fatal("expected error when TRACE_REAL_MAKENSIS points to wrapper itself")
	}
}
