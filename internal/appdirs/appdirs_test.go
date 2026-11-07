package appdirs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolvePortableAndInstalledPaths(t *testing.T) {
	tmp := t.TempDir()
	localAppData := filepath.Join(tmp, "LocalAppData")
	docsDir := filepath.Join(tmp, "Documents")
	exeDir := filepath.Join(tmp, "bin", "trace")
	if err := os.MkdirAll(exeDir, 0o700); err != nil {
		t.Fatalf("mkdir exeDir: %v", err)
	}
	exePath := filepath.Join(exeDir, "trace.exe")

	// 1. Installed stable
	stableLoc, err := ResolveWithOptions(Options{
		Channel:      "stable",
		ExePath:      exePath,
		ForceMode:    "installed",
		LocalAppData: localAppData,
		DocumentsDir: docsDir,
	})
	if err != nil {
		t.Fatalf("ResolveWithOptions(stable): %v", err)
	}
	if stableLoc.AppName != "trace" {
		t.Fatalf("expected AppName=trace, got %q", stableLoc.AppName)
	}
	if stableLoc.DisplayName != "TRACE" {
		t.Fatalf("expected DisplayName=TRACE, got %q", stableLoc.DisplayName)
	}
	if stableLoc.Identifier != "io.github.chialecode.trace" {
		t.Fatalf("expected Identifier=io.github.chialecode.trace, got %q", stableLoc.Identifier)
	}
	wantStableData := filepath.Join(localAppData, "trace")
	if stableLoc.DataDir != wantStableData {
		t.Fatalf("DataDir = %q, want %q", stableLoc.DataDir, wantStableData)
	}
	wantStableCache := filepath.Join(docsDir, "trace")
	if stableLoc.DefaultCacheDir != wantStableCache {
		t.Fatalf("DefaultCacheDir = %q, want %q", stableLoc.DefaultCacheDir, wantStableCache)
	}
	assertRelativeLowercase(t, localAppData, stableLoc.DataDir)
	assertRelativeLowercase(t, docsDir, stableLoc.DefaultCacheDir)

	// 2. Installed dev
	devLoc, err := ResolveWithOptions(Options{
		Channel:      "dev",
		ExePath:      filepath.Join(exeDir, "trace-dev.exe"),
		ForceMode:    "installed",
		LocalAppData: localAppData,
		DocumentsDir: docsDir,
	})
	if err != nil {
		t.Fatalf("ResolveWithOptions(dev): %v", err)
	}
	if devLoc.AppName != "trace-dev" {
		t.Fatalf("expected AppName=trace-dev, got %q", devLoc.AppName)
	}
	if devLoc.DisplayName != "TRACE Dev" {
		t.Fatalf("expected DisplayName=TRACE Dev, got %q", devLoc.DisplayName)
	}
	if devLoc.Identifier != "io.github.chialecode.trace-dev" {
		t.Fatalf("expected Identifier=io.github.chialecode.trace-dev, got %q", devLoc.Identifier)
	}
	wantDevData := filepath.Join(localAppData, "trace-dev")
	if devLoc.DataDir != wantDevData {
		t.Fatalf("DataDir = %q, want %q", devLoc.DataDir, wantDevData)
	}
	wantDevCache := filepath.Join(docsDir, "trace-dev")
	if devLoc.DefaultCacheDir != wantDevCache {
		t.Fatalf("DefaultCacheDir = %q, want %q", devLoc.DefaultCacheDir, wantDevCache)
	}
	assertRelativeLowercase(t, localAppData, devLoc.DataDir)
	assertRelativeLowercase(t, docsDir, devLoc.DefaultCacheDir)

	// 3. Portable detected via portable.flag
	flagPath := filepath.Join(exeDir, "portable.flag")
	if err := os.WriteFile(flagPath, []byte("1\n"), 0o600); err != nil {
		t.Fatalf("write portable.flag: %v", err)
	}
	portableLoc, err := ResolveWithOptions(Options{
		Channel:      "dev",
		ExePath:      filepath.Join(exeDir, "trace-dev.exe"),
		LocalAppData: localAppData,
		DocumentsDir: docsDir,
	})
	if err != nil {
		t.Fatalf("ResolveWithOptions(portable): %v", err)
	}
	if portableLoc.Mode != "portable" {
		t.Fatalf("expected Mode=portable, got %q", portableLoc.Mode)
	}
	wantPortableData := filepath.Join(exeDir, "trace-data")
	if portableLoc.DataDir != wantPortableData {
		t.Fatalf("portable DataDir = %q, want %q", portableLoc.DataDir, wantPortableData)
	}
	wantPortableCache := filepath.Join(exeDir, "trace-data", "library")
	if portableLoc.DefaultCacheDir != wantPortableCache {
		t.Fatalf("portable DefaultCacheDir = %q, want %q", portableLoc.DefaultCacheDir, wantPortableCache)
	}
	assertRelativeLowercase(t, exeDir, portableLoc.DataDir)
	assertRelativeLowercase(t, exeDir, portableLoc.DefaultCacheDir)
	assertRelativeLowercase(t, exeDir, portableLoc.CredentialsDir)
	assertRelativeLowercase(t, exeDir, portableLoc.LogsDir)
	assertRelativeLowercase(t, exeDir, portableLoc.TempDir)
	assertRelativeLowercase(t, exeDir, portableLoc.LockFile)
}

func TestLockDataDirExclusive(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "trace-data")
	lock1, err := LockDataDir(dataDir)
	if err != nil {
		t.Fatalf("first LockDataDir failed: %v", err)
	}
	defer func() { _ = lock1.Release() }()

	if _, err := LockDataDir(dataDir); !errors.Is(err, ErrAlreadyLocked) {
		t.Fatalf("second LockDataDir error = %v, want ErrAlreadyLocked", err)
	}

	if err := lock1.Release(); err != nil {
		t.Fatalf("Release lock1: %v", err)
	}

	lock2, err := LockDataDir(dataDir)
	if err != nil {
		t.Fatalf("re-acquire LockDataDir failed: %v", err)
	}
	_ = lock2.Release()
}

func TestLocationsByChannel(t *testing.T) {
	TestResolvePortableAndInstalledPaths(t)
}

func TestDocumentsKnownFolderRedirect(t *testing.T) {
	tmp := t.TempDir()
	redirectedDocs := filepath.Join(tmp, "OneDrive", "Documents")
	loc, err := ResolveWithOptions(Options{
		Channel:      "dev",
		ForceMode:    "installed",
		LocalAppData: filepath.Join(tmp, "LocalAppData"),
		DocumentsDir: redirectedDocs,
	})
	if err != nil {
		t.Fatalf("ResolveWithOptions: %v", err)
	}
	want := filepath.Join(redirectedDocs, "trace-dev")
	if loc.DefaultCacheDir != want {
		t.Fatalf("DefaultCacheDir = %q, want %q", loc.DefaultCacheDir, want)
	}
}

func TestAllTracePathsLowercase(t *testing.T) {
	TestResolvePortableAndInstalledPaths(t)
}

func TestDataDirExclusiveLock(t *testing.T) {
	TestLockDataDirExclusive(t)
}

func assertRelativeLowercase(t *testing.T, base, target string) {
	t.Helper()
	rel, err := filepath.Rel(base, target)
	if err != nil {
		t.Fatalf("filepath.Rel(%q, %q): %v", base, target, err)
	}
	if rel != strings.ToLower(rel) {
		t.Fatalf("TRACE-owned path segment %q contains uppercase characters", rel)
	}
}
