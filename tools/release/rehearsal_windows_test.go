//go:build windows

package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chialecode/trace/internal/update"
	"github.com/chialecode/trace/tools/internal/build"
)

func TestSplitUninstallCommand(t *testing.T) {
	exe, args := splitCommand(`"C:\Users\u\AppData\Local\Programs\trace-ptest\Uninstall.exe" /S`)
	if exe != `C:\Users\u\AppData\Local\Programs\trace-ptest\Uninstall.exe` || strings.Join(args, " ") != "/S" {
		t.Fatalf("%q %v", exe, args)
	}
	if exe, args := splitCommand(`C:\x\Uninstall.exe`); exe != `C:\x\Uninstall.exe` || len(args) != 0 {
		t.Fatalf("%q %v", exe, args)
	}
}

func TestRecordInstalledOldSetsDirBeforeVersionCheck(t *testing.T) {
	res := newRehearsalResult(rehearsalInput{Version: "0.1.2"})
	r := &rehearsal{res: &res}
	_, err := r.recordInstalledOld(build.InstallEntry{
		Location: `C:\Users\u\AppData\Local\Programs\trace-ptest`,
		Version:  "0.1.1-wrong",
	})
	if err == nil || !strings.Contains(err.Error(), "DisplayVersion") {
		t.Fatalf("expected DisplayVersion mismatch error, got %v", err)
	}
	if r.dir != `C:\Users\u\AppData\Local\Programs\trace-ptest` || r.exe != filepath.Join(r.dir, "trace.exe") {
		t.Fatalf("r.dir=%q r.exe=%q not set before version failure", r.dir, r.exe)
	}
	if r.res.InstallDir != r.dir {
		t.Fatalf("res.InstallDir=%q, want %q", r.res.InstallDir, r.dir)
	}
}

func TestRehearsalChecksOthersSameAfterCleanup(t *testing.T) {
	calls := 0
	cleanupRan := false
	res := newRehearsalResult(rehearsalInput{Version: "0.1.2"})
	r := &rehearsal{
		res: &res,
		snapshotOthersFn: func() identitySnapshot {
			calls++
			if !cleanupRan {
				return identitySnapshot{"io.github.chialecode.trace": {Version: "0.1.1"}}
			}
			// Simulate cleanup accidentally mutating release/dev entry.
			return identitySnapshot{"io.github.chialecode.trace": {Version: "0.1.9"}}
		},
	}
	got, err := r.execute(
		func() error { return nil },
		func() { cleanupRan = true },
	)
	if !cleanupRan {
		t.Fatal("cleanup did not run")
	}
	if calls != 2 {
		t.Fatalf("snapshot calls = %d, want 2 (before run and after cleanup)", calls)
	}
	if got.OthersSame || got.Status != "failed" || err == nil {
		t.Fatalf("expected failure when OthersSame changes during cleanup: got=%+v err=%v", got, err)
	}
}

func TestLocalReleaseServesAndTampers(t *testing.T) {
	srv, err := startLocalRelease()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.close()

	dir := t.TempDir()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	archiveName := "trace-ptest-0.1.2-rehearsal.2-windows-amd64.tar.gz"
	origArchive := []byte("0123456789abcdef")
	if err := os.WriteFile(filepath.Join(dir, archiveName), origArchive, 0o644); err != nil {
		t.Fatal(err)
	}
	manifestRaw, _ := json.Marshal(map[string]any{
		"version":   "0.1.2-rehearsal.2",
		"signature": "c2ln",
		"url":       "https://rehearsal.invalid/trace-ptest/" + archiveName,
	})
	if err := os.WriteFile(filepath.Join(dir, manifestName), manifestRaw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := srv.serveDir(dir); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(srv.base + "/" + archiveName)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != string(origArchive) || !srv.served(archiveName) {
		t.Fatalf("served archive mismatch: body=%q served=%v", body, srv.served(archiveName))
	}

	if err := srv.serveTampered(dir, "0.1.2-rehearsal.2", "0.1.2-rehearsal.3", key); err != nil {
		t.Fatal(err)
	}
	mResp, err := http.Get(srv.base + "/" + manifestName)
	if err != nil {
		t.Fatal(err)
	}
	mRaw, _ := io.ReadAll(mResp.Body)
	mResp.Body.Close()
	var m map[string]any
	if err := json.Unmarshal(mRaw, &m); err != nil {
		t.Fatal(err)
	}
	att, _ := base64.StdEncoding.DecodeString(m[update.AttestationField].(string))
	if !ed25519.Verify(pub, update.AttestationMessage("0.1.2-rehearsal.3", "c2ln"), att) {
		t.Fatal("tampered manifest attestation invalid")
	}
	tResp, err := http.Get(srv.base + "/" + srv.tamperedArchive)
	if err != nil {
		t.Fatal(err)
	}
	tBody, _ := io.ReadAll(tResp.Body)
	tResp.Body.Close()
	if string(tBody) == string(origArchive) {
		t.Fatal("tampered archive bytes were not modified")
	}
}

// The installer rehearsal installs, updates through a local
// release, rejects a tampered update and uninstalls TRACE under the ptest
// identity. It builds two packages and takes minutes, so it only runs with
// TRACE_RUN_INSTALLER_TEST=1 (go test -timeout 20m); tools/release runs it
// on every release.
func TestInstallerRehearsal(t *testing.T) {
	if os.Getenv("TRACE_RUN_INSTALLER_TEST") != "1" {
		t.Skip("set TRACE_RUN_INSTALLER_TEST=1 to install, update and uninstall TRACE PTest")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	base, err := os.ReadFile(filepath.Join(root, "mygo.json"))
	if err != nil {
		t.Fatal(err)
	}
	commit, dirty, err := build.HeadInfo(root)
	if err != nil {
		t.Fatal(err)
	}
	if dirty {
		commit += "-dirty"
	}
	tags, err := build.LocalTags(root)
	if err != nil {
		t.Fatal(err)
	}
	cfgVersion, _ := build.ConfigVersion(base)
	line, _ := build.VersionLine(cfgVersion)
	res, err := runRehearsal(rehearsalInput{Root: root, Base: base, Version: build.NextStable(line, tags), Commit: commit, Build: build.MygoBuild, Logf: t.Logf})
	if werr := writeJSON(filepath.Join(root, "artifacts", "release", "rehearsal.json"), res); werr != nil {
		t.Error(werr)
	}
	if err != nil {
		t.Fatalf("rehearsal: %v", err)
	}
	if res.Status != "passed" || !res.OthersSame || !res.DataKept || !res.InstallGone {
		t.Fatalf("%+v", res)
	}
	for _, s := range res.Steps {
		if !s.OK {
			t.Fatalf("step %s: %s", s.Name, s.Detail)
		}
	}
}
