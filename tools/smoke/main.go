package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/storage"
)

type RuntimeStateSnapshot struct {
	PID                  int      `json:"pid"`
	AppName              string   `json:"appName"`
	DisplayName          string   `json:"displayName"`
	Channel              string   `json:"channel"`
	Mode                 string   `json:"mode"`
	QuietMode            string   `json:"quietMode"`
	DataDir              string   `json:"dataDir"`
	CacheDir             string   `json:"cacheDir"`
	WindowOpen           bool     `json:"windowOpen"`
	TrayActive           bool     `json:"trayActive"`
	ActiveRoute          string   `json:"activeRoute"`
	WorkingSetBytes      int64    `json:"workingSetBytes"`
	PrivateWorkingSet    int64    `json:"privateWorkingSetBytes"`
	PrivateBytes         int64    `json:"privateBytes"`
	WebView2ProcessCount int      `json:"webview2ProcessCount"`
	ForegroundPID        uint32   `json:"foregroundPid"`
	StealFocusDetected   bool     `json:"stealFocusDetected"`
	SecondInstanceArgs   []string `json:"secondInstanceArgs,omitempty"`
	Version              string   `json:"version"`
	Commit               string   `json:"commit"`
	BuildTime            string   `json:"buildTime"`
	Timestamp            string   `json:"timestamp"`
}

type SmokeReceipt struct {
	Passed                   bool                 `json:"passed"`
	PackageDir               string               `json:"packageDir"`
	QuietMode                string               `json:"quietMode"`
	Channel                  string               `json:"channel"`
	AppName                  string               `json:"appName"`
	TempSocketIsolated       bool                 `json:"tempSocketIsolated"`
	ChecksumsVerified        bool                 `json:"checksumsVerified"`
	Version                  string               `json:"version"`
	VersionMatchesFile       bool                 `json:"versionMatchesVersionTxt"`
	DataLockExclusive        bool                 `json:"dataLockExclusive"`
	SecondInstanceForwarded  bool                 `json:"secondInstanceForwarded"`
	SecondInstanceUntouched  bool                 `json:"secondInstanceUntouchedDataDir"`
	WebView2ProcessCount     int                  `json:"webview2ProcessCount"`
	TrayPrivateWorkingSet    int64                `json:"trayPrivateWorkingSetBytes"`
	TrayPrivateCommitBytes   int64                `json:"trayPrivateCommitBytes"`
	TrayMemoryUnder60MB      bool                 `json:"trayMemoryUnder60MB"`
	StealFocusDetected       bool                 `json:"stealFocusDetected"`
	AllPathsLowercase        bool                 `json:"allPathsLowercase"`
	NoStrayAppDataOrHomeDirs bool                 `json:"noStrayAppDataOrHomeDirs"`
	NoStrayInstallDirWrites  bool                 `json:"noStrayInstallDirWrites"`
	NoSentinelSecretsInLogs  bool                 `json:"noSentinelSecretsInLogs"`
	ReadySnapshot            RuntimeStateSnapshot `json:"readySnapshot"`
	TraySnapshot             RuntimeStateSnapshot `json:"traySnapshot"`
	RestoredSnapshot         RuntimeStateSnapshot `json:"restoredSnapshot"`
	DurationMs               int64                `json:"durationMs"`
	Failures                 []string             `json:"failures,omitempty"`
}

func main() {
	appdirs.LowerCurrentProcessPriority()

	pkgFlag := flag.String("pkg", "dist/trace-dev", "packaged application directory")
	outFlag := flag.String("out", "artifacts/smoke", "output directory for smoke receipt")
	flag.Parse()

	started := time.Now()
	receipt, err := runSmoke(*pkgFlag, *outFlag)
	receipt.DurationMs = time.Since(started).Milliseconds()

	if writeErr := writeReceipt(*outFlag, receipt); writeErr != nil {
		fmt.Fprintf(os.Stderr, "smoke: write receipt error: %v\n", writeErr)
		os.Exit(1)
	}
	if err != nil || !receipt.Passed {
		fmt.Fprintf(os.Stderr, "smoke FAILED (%dms): %v\n", receipt.DurationMs, receipt.Failures)
		os.Exit(1)
	}
	fmt.Printf("smoke PASSED in %dms (quietMode=%s, trayPrivateWS=%.2f MiB, trayCommit=%.2f MiB, webview2=%d)\n",
		receipt.DurationMs, receipt.QuietMode, float64(receipt.TrayPrivateWorkingSet)/(1024*1024), float64(receipt.TrayPrivateCommitBytes)/(1024*1024), receipt.WebView2ProcessCount)
}

func runSmoke(pkgDir, outDir string) (SmokeReceipt, error) {
	var receipt SmokeReceipt
	var failures []string
	fail := func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		failures = append(failures, msg)
	}

	absPkg, err := filepath.Abs(pkgDir)
	if err != nil {
		return receipt, err
	}
	receipt.PackageDir = filepath.ToSlash(pkgDir)

	if err := cleanArtifactsOutDir(outDir); err != nil {
		return receipt, err
	}

	// 1. Verify package files and SHA256SUMS.txt
	exePath := filepath.Join(absPkg, "trace.exe")
	if st, err := os.Stat(exePath); err != nil || st.IsDir() {
		return receipt, fmt.Errorf("missing trace.exe in %s", absPkg)
	}
	if err := verifyPackageChecksums(absPkg); err != nil {
		fail("checksum verification: %v", err)
	} else {
		receipt.ChecksumsVerified = true
	}

	beforePkgEntries, _ := listDirNames(absPkg)

	// 2. Create isolated sandbox directories (including TEMP/TMP for single-instance socket)
	sandbox, err := os.MkdirTemp("", "trace-smoke-")
	if err != nil {
		return receipt, err
	}
	defer os.RemoveAll(sandbox)

	localAppData := filepath.Join(sandbox, "localappdata")
	appData := filepath.Join(sandbox, "appdata")
	userProfile := filepath.Join(sandbox, "userprofile")
	docsDir := filepath.Join(userProfile, "Documents")
	tempDir := filepath.Join(sandbox, "temp")
	smokeCtrlDir := filepath.Join(sandbox, "smoke-ctrl")

	for _, d := range []string{localAppData, appData, userProfile, docsDir, tempDir, smokeCtrlDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return receipt, err
		}
	}

	relTemp, err := filepath.Rel(sandbox, tempDir)
	if err != nil || strings.HasPrefix(relTemp, "..") {
		return receipt, fmt.Errorf("tempDir %s is not isolated inside sandbox %s", tempDir, sandbox)
	}
	receipt.TempSocketIsolated = true

	quietEnv := os.Getenv("TRACE_QUIET")
	if quietEnv == "" {
		quietEnv = "1"
	}

	baseEnv := filterEnv(os.Environ(),
		"LOCALAPPDATA", "APPDATA", "USERPROFILE", "HOME",
		"TEMP", "TMP", "TMPDIR",
		"TRACE_DATA_DIR", "TRACE_CACHE_DIR", "TRACE_DOCUMENTS_DIR",
		"TRACE_QUIET", "TRACE_SMOKE_DIR",
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY",
	)
	childEnv := append(baseEnv,
		"LOCALAPPDATA="+localAppData,
		"APPDATA="+appData,
		"USERPROFILE="+userProfile,
		"HOME="+userProfile,
		"TRACE_DOCUMENTS_DIR="+docsDir,
		"TEMP="+tempDir,
		"TMP="+tempDir,
		"TRACE_QUIET="+quietEnv,
		"TRACE_SMOKE_DIR="+smokeCtrlDir,
		"HTTP_PROXY=http://127.0.0.1:1",
		"HTTPS_PROXY=http://127.0.0.1:1",
		"NO_PROXY=127.0.0.1,localhost",
	)

	// 3. Start primary trace.exe in quiet mode
	cmd := exec.Command(exePath)
	cmd.Dir = absPkg
	cmd.Env = childEnv
	appdirs.SetSubprocessQuiet(cmd)
	if err := cmd.Start(); err != nil {
		return receipt, fmt.Errorf("start trace.exe: %w", err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}()

	readySnap, err := waitForSnapshot(filepath.Join(smokeCtrlDir, "ready.json"), 15*time.Second)
	if err != nil {
		fail("wait ready.json: %v", err)
		receipt.Failures = failures
		return receipt, err
	}
	receipt.ReadySnapshot = readySnap
	receipt.QuietMode = readySnap.QuietMode
	receipt.Channel = readySnap.Channel
	receipt.AppName = readySnap.AppName

	if !readySnap.WindowOpen {
		fail("ready.WindowOpen = false, want true")
	}
	// The program reports the version, commit and build time of
	// VERSION.txt (W1).
	receipt.Version = readySnap.Version
	if raw, err := os.ReadFile(filepath.Join(absPkg, "VERSION.txt")); err != nil {
		fail("read VERSION.txt: %v", err)
	} else {
		fields := map[string]string{}
		for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
			if k, v, ok := strings.Cut(line, "="); ok {
				fields[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
		if fields["version"] == "" || fields["version"] != readySnap.Version || fields["commit"] != readySnap.Commit || fields["built_at"] != readySnap.BuildTime || fields["channel"] != readySnap.Channel {
			fail("the program reports %s/%s/%s/%s, VERSION.txt %s/%s/%s/%s", readySnap.Version, readySnap.Commit, readySnap.BuildTime, readySnap.Channel, fields["version"], fields["commit"], fields["built_at"], fields["channel"])
		} else {
			receipt.VersionMatchesFile = true
		}
	}
	if readySnap.WebView2ProcessCount != 0 {
		fail("ready.WebView2ProcessCount = %d, want 0", readySnap.WebView2ProcessCount)
	}
	if readySnap.StealFocusDetected {
		fail("ready.StealFocusDetected = true")
	}

	// 4. Verify exclusive app.lock while primary instance is running (INV-14, C36)
	if lock, lockErr := appdirs.LockDataDir(readySnap.DataDir); lockErr == nil {
		_ = lock.Release()
		fail("LockDataDir succeeded while trace.exe was running (app.lock not held)")
	} else {
		receipt.DataLockExclusive = true
	}

	// 5. Launch second instance with argument BV1xx411c7mD and verify forwarding + untouched DataDir (C35, C36)
	secondLocalAppData := filepath.Join(sandbox, "second-localappdata")
	secondEnv := filterEnv(childEnv, "LOCALAPPDATA")
	secondEnv = append(secondEnv, "LOCALAPPDATA="+secondLocalAppData)

	secondCmd := exec.Command(exePath, "BV1xx411c7mD")
	secondCmd.Dir = absPkg
	secondCmd.Env = secondEnv
	appdirs.SetSubprocessQuiet(secondCmd)
	if err := secondCmd.Run(); err != nil {
		fail("second instance run failed: %v", err)
	}
	if _, statErr := os.Stat(secondLocalAppData); os.IsNotExist(statErr) {
		receipt.SecondInstanceUntouched = true
	} else {
		fail("second instance created data directory %s before exiting", secondLocalAppData)
	}

	secondSnap, err := waitForSnapshot(filepath.Join(smokeCtrlDir, "second_instance.json"), 8*time.Second)
	if err != nil {
		fail("wait second_instance.json: %v", err)
	} else {
		for _, a := range secondSnap.SecondInstanceArgs {
			if strings.Contains(a, "BV1xx411c7mD") {
				receipt.SecondInstanceForwarded = true
				break
			}
		}
		if !receipt.SecondInstanceForwarded {
			fail("second instance args %v did not contain BV1xx411c7mD", secondSnap.SecondInstanceArgs)
		}
		if secondSnap.StealFocusDetected {
			fail("second instance stole foreground focus")
		}
	}

	// 6. Close to tray and verify memory < 60 MiB & 0 WebView2 processes (DEC-030, INV-13, C35)
	if err := sendSmokeCommand(smokeCtrlDir, "close_to_tray"); err != nil {
		fail("send close_to_tray: %v", err)
	}
	traySnap, err := waitForSnapshot(filepath.Join(smokeCtrlDir, "tray_state.json"), 10*time.Second)
	if err != nil {
		fail("wait tray_state.json: %v", err)
	} else {
		receipt.TraySnapshot = traySnap
		receipt.TrayPrivateWorkingSet = traySnap.PrivateWorkingSet
		receipt.TrayPrivateCommitBytes = traySnap.PrivateBytes
		receipt.WebView2ProcessCount = traySnap.WebView2ProcessCount
		if traySnap.WindowOpen {
			fail("traySnap.WindowOpen = true, want false")
		}
		if traySnap.WebView2ProcessCount != 0 {
			fail("traySnap.WebView2ProcessCount = %d, want 0", traySnap.WebView2ProcessCount)
		}
		const maxTrayBytes = 60 * 1024 * 1024
		if traySnap.PrivateWorkingSet > 0 && traySnap.PrivateWorkingSet < maxTrayBytes {
			receipt.TrayMemoryUnder60MB = true
		} else {
			fail("tray private working set = %d (%.2f MiB), want < 60 MiB", traySnap.PrivateWorkingSet, float64(traySnap.PrivateWorkingSet)/(1024*1024))
		}
		if traySnap.StealFocusDetected {
			fail("traySnap.StealFocusDetected = true")
		}
	}

	// 7. Restore window from tray (C35)
	if err := sendSmokeCommand(smokeCtrlDir, "restore_window"); err != nil {
		fail("send restore_window: %v", err)
	}
	restoredSnap, err := waitForSnapshot(filepath.Join(smokeCtrlDir, "restored_state.json"), 10*time.Second)
	if err != nil {
		fail("wait restored_state.json: %v", err)
	} else {
		receipt.RestoredSnapshot = restoredSnap
		if !restoredSnap.WindowOpen {
			fail("restoredSnap.WindowOpen = false, want true")
		}
		if restoredSnap.StealFocusDetected {
			fail("restoredSnap.StealFocusDetected = true")
		}
	}

	// 8. Quit cleanly
	_ = sendSmokeCommand(smokeCtrlDir, "quit")
	waitDone := make(chan error, 1)
	go func() {
		waitDone <- cmd.Wait()
	}()
	select {
	case <-waitDone:
	case <-time.After(8 * time.Second):
		_ = cmd.Process.Kill()
		fail("trace.exe did not exit within 8s after quit command")
	}

	receipt.StealFocusDetected = readySnap.StealFocusDetected || traySnap.StealFocusDetected || restoredSnap.StealFocusDetected

	// 9. Verify data files, default cache path, lowercase naming, no stray folders, and no sentinel secrets (C36, C37)
	expectedFiles := []string{"library.db", "updates.db", "mygo", "cache", "logs"}
	for _, name := range expectedFiles {
		if _, err := os.Stat(filepath.Join(readySnap.DataDir, name)); err != nil {
			fail("missing expected data entry %s in %s: %v", name, readySnap.DataDir, err)
		}
	}

	expectedCacheDir := filepath.Join(docsDir, readySnap.AppName)
	if !strings.EqualFold(filepath.Clean(readySnap.CacheDir), filepath.Clean(expectedCacheDir)) {
		fail("CacheDir = %q, want %q", readySnap.CacheDir, expectedCacheDir)
	}

	if err := verifyNoUppercaseTrace(sandbox); err != nil {
		fail("lowercase path check: %v", err)
	} else {
		receipt.AllPathsLowercase = true
	}

	appDataEntries, _ := listDirNames(appData)
	userProfileEntries, _ := listDirNames(userProfile)
	var strayHome []string
	for _, name := range userProfileEntries {
		if strings.EqualFold(name, "Documents") {
			continue
		}
		if strings.EqualFold(name, "AppData") {
			// Windows D3D11 shader cache may create <USERPROFILE>\AppData\Local\D3DSCache; ensure no trace/mygo entries exist there.
			_ = filepath.WalkDir(filepath.Join(userProfile, name), func(p string, d os.DirEntry, wErr error) error {
				if wErr == nil && (strings.Contains(strings.ToLower(d.Name()), "trace") || strings.Contains(strings.ToLower(d.Name()), "mygo")) {
					strayHome = append(strayHome, p)
				}
				return nil
			})
			continue
		}
		strayHome = append(strayHome, name)
	}
	if len(appDataEntries) > 0 || len(strayHome) > 0 {
		fail("stray entries in APPDATA=%v or USERPROFILE=%v", appDataEntries, strayHome)
	} else {
		receipt.NoStrayAppDataOrHomeDirs = true
	}

	afterPkgEntries, _ := listDirNames(absPkg)
	if !slices.Equal(beforePkgEntries, afterPkgEntries) {
		fail("install/package directory modified during run: before=%v after=%v", beforePkgEntries, afterPkgEntries)
	} else {
		receipt.NoStrayInstallDirWrites = true
	}

	if err := scanDirForSecrets(readySnap.DataDir); err != nil {
		fail("secret scan in DataDir: %v", err)
	} else {
		receipt.NoSentinelSecretsInLogs = true
	}

	sanitizeSnap := func(s *RuntimeStateSnapshot) {
		if s.DataDir != "" {
			s.DataDir = strings.ReplaceAll(filepath.ToSlash(s.DataDir), filepath.ToSlash(sandbox), "<SANDBOX>")
		}
		if s.CacheDir != "" {
			s.CacheDir = strings.ReplaceAll(filepath.ToSlash(s.CacheDir), filepath.ToSlash(sandbox), "<SANDBOX>")
		}
	}
	sanitizeSnap(&receipt.ReadySnapshot)
	sanitizeSnap(&receipt.TraySnapshot)
	sanitizeSnap(&receipt.RestoredSnapshot)

	receipt.Failures = failures
	receipt.Passed = len(failures) == 0
	return receipt, nil
}

func sendSmokeCommand(smokeDir, cmd string) error {
	return storage.WriteFileAtomic(filepath.Join(smokeDir, "command.txt"), []byte(cmd+"\n"), 0o600)
}

func waitForSnapshot(path string, timeout time.Duration) (RuntimeStateSnapshot, error) {
	deadline := time.Now().Add(timeout)
	var snap RuntimeStateSnapshot
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil && len(raw) > 0 {
			if jErr := json.Unmarshal(raw, &snap); jErr == nil && snap.PID != 0 {
				return snap, nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return snap, fmt.Errorf("timed out waiting for %s", filepath.Base(path))
}

func verifyPackageChecksums(pkgDir string) error {
	raw, err := os.ReadFile(filepath.Join(pkgDir, "SHA256SUMS.txt"))
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) == 0 {
		return fmt.Errorf("empty SHA256SUMS.txt")
	}
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		wantSum := strings.ToLower(fields[0])
		fileName := fields[len(fields)-1]
		gotSum, err := sha256Hex(filepath.Join(pkgDir, fileName))
		if err != nil {
			return fmt.Errorf("hash %s: %w", fileName, err)
		}
		if gotSum != wantSum {
			return fmt.Errorf("%s sha256=%s, want %s", fileName, gotSum, wantSum)
		}
	}
	return nil
}

func sha256Hex(path string) (string, error) {
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

func verifyNoUppercaseTrace(root string) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if strings.Contains(name, "TRACE") {
			return fmt.Errorf("uppercase TRACE found in path entry %s", path)
		}
		return nil
	})
}

func scanDirForSecrets(root string) error {
	markers := []string{"SESSDATA=", "bili_jct=", "DedeUserID__ckMd5=", "SENTINEL_"}
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, rErr := os.ReadFile(path)
		if rErr != nil {
			return nil
		}
		text := string(raw)
		for _, m := range markers {
			if strings.Contains(text, m) {
				return fmt.Errorf("found secret marker %q in %s", m, filepath.Base(path))
			}
		}
		return nil
	})
}

func listDirNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	return names, nil
}

func filterEnv(env []string, removeKeys ...string) []string {
	rm := make(map[string]bool, len(removeKeys))
	for _, k := range removeKeys {
		rm[strings.ToUpper(k)] = true
	}
	var out []string
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if !rm[strings.ToUpper(k)] {
			out = append(out, kv)
		}
	}
	return out
}

func cleanArtifactsOutDir(outDir string) error {
	absOut, err := filepath.Abs(outDir)
	if err != nil {
		return err
	}
	if st, err := os.Lstat(absOut); err == nil {
		if st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to clean symlinked output directory %s", absOut)
		}
		entries, err := os.ReadDir(absOut)
		if err != nil {
			return err
		}
		for _, e := range entries {
			_ = os.RemoveAll(filepath.Join(absOut, e.Name()))
		}
	}
	return os.MkdirAll(absOut, 0o755)
}

func writeReceipt(outDir string, receipt SmokeReceipt) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	return storage.WriteFileAtomic(filepath.Join(outDir, "smoke.json"), raw, 0o644)
}
