package update

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/storage"
)

func TestUpdateHistorySchemaV1AndRetention(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, HistoryFileName)

	store, backup, err := OpenHistoryStore(path, nil)
	if err != nil {
		t.Fatalf("OpenHistoryStore: %v", err)
	}
	if backup != "" {
		t.Fatalf("unexpected backup on fresh store: %q", backup)
	}
	initHist := store.Get()
	if initHist.Version != HistorySchemaVersion || len(initHist.Checks) != 0 || len(initHist.Updates) != 0 {
		t.Fatalf("unexpected initial history: %+v", initHist)
	}

	base := time.Date(2026, 11, 15, 8, 0, 0, 0, time.UTC)
	for i := 1; i <= 25; i++ {
		trig := CheckTriggerAuto
		res := MsgUpToDate
		if i%2 == 0 {
			trig = CheckTriggerManual
			res = fmt.Sprintf("发现 0.1.%d", i)
		}
		if err := store.RecordCheck(CheckRecord{
			At:      base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339),
			Trigger: trig,
			Result:  res,
		}); err != nil {
			t.Fatalf("RecordCheck(%d): %v", i, err)
		}
	}

	for i := 1; i <= 55; i++ {
		method := UpdateMethodAuto
		switch i % 3 {
		case 1:
			method = UpdateMethodDevInstall
		case 2:
			method = UpdateMethodManual
		}
		if err := store.RecordUpdate(UpdateRecord{
			At:          base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339),
			FromVersion: fmt.Sprintf("0.1.%d", i-1),
			ToVersion:   fmt.Sprintf("0.1.%d", i),
			Method:      method,
		}); err != nil {
			t.Fatalf("RecordUpdate(%d): %v", i, err)
		}
	}

	got := store.Get()
	if got.Version != 1 {
		t.Fatalf("Version = %d, want 1", got.Version)
	}
	if len(got.Checks) != MaxCheckRecords {
		t.Fatalf("len(Checks) = %d, want %d", len(got.Checks), MaxCheckRecords)
	}
	if got.Checks[0].Trigger != CheckTriggerAuto || got.Checks[0].Result != MsgUpToDate {
		t.Fatalf("newest check = %+v", got.Checks[0])
	}
	if got.Checks[MaxCheckRecords-1].Result != "发现 0.1.6" {
		t.Fatalf("oldest retained check = %+v, want 发现 0.1.6", got.Checks[MaxCheckRecords-1])
	}

	if len(got.Updates) != MaxUpdateRecords {
		t.Fatalf("len(Updates) = %d, want %d", len(got.Updates), MaxUpdateRecords)
	}
	if got.Updates[0].FromVersion != "0.1.54" || got.Updates[0].ToVersion != "0.1.55" {
		t.Fatalf("newest update = %+v", got.Updates[0])
	}
	if got.Updates[MaxUpdateRecords-1].FromVersion != "0.1.5" || got.Updates[MaxUpdateRecords-1].ToVersion != "0.1.6" {
		t.Fatalf("oldest retained update = %+v", got.Updates[MaxUpdateRecords-1])
	}

	// Verify reopened store and raw JSON version field.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var rawMap map[string]any
	if err := json.Unmarshal(raw, &rawMap); err != nil {
		t.Fatalf("Unmarshal raw json: %v", err)
	}
	if v, ok := rawMap["version"].(float64); !ok || int(v) != 1 {
		t.Fatalf("raw JSON version = %v, want 1", rawMap["version"])
	}

	reopened, reBackup, err := OpenHistoryStore(path, nil)
	if err != nil || reBackup != "" {
		t.Fatalf("reopen: err=%v backup=%q", err, reBackup)
	}
	if len(reopened.Get().Checks) != MaxCheckRecords || len(reopened.Get().Updates) != MaxUpdateRecords {
		t.Fatalf("reopened counts mismatch: %+v", reopened.Get())
	}
}

func TestUpdateHistoryRefusesNewerVersionWithoutOverwriting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, HistoryFileName)
	futurePayload := []byte("{\n  \"version\": 2,\n  \"checks\": \"future-layout\",\n  \"futureField\": true\n}\n")
	if err := os.WriteFile(path, futurePayload, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := GuardHistorySchema(dir); !storage.IsNewerData(err) {
		t.Fatalf("GuardHistorySchema err = %v, want NewerDataError", err)
	}
	if _, _, err := OpenHistoryStore(path, nil); !storage.IsNewerData(err) {
		t.Fatalf("OpenHistoryStore err = %v, want NewerDataError", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, futurePayload) {
		t.Fatalf("newer update-history.json was modified:\ngot:  %s\nwant: %s", after, futurePayload)
	}
}

func TestUpdateHistoryCorruptBackupAndRebuild(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"truncated_json", `{"version": 1, "checks": [`},
		{"bad_version_type", `{"version": "oops", "checks": [], "updates": []}`},
		{"bad_checks_type", `{"version": 1, "checks": "not-an-array", "updates": []}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, HistoryFileName)
			if err := os.WriteFile(path, []byte(tc.raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := GuardHistorySchema(dir); err != nil {
				t.Fatalf("GuardHistorySchema should leave corrupt file to loader, got %v", err)
			}

			store, backup, err := OpenHistoryStore(path, nil)
			if err != nil {
				t.Fatalf("OpenHistoryStore: %v", err)
			}
			if backup == "" {
				t.Fatal("expected corrupt backup path, got empty")
			}
			backupBytes, err := os.ReadFile(backup)
			if err != nil {
				t.Fatalf("read backup %s: %v", backup, err)
			}
			if string(backupBytes) != tc.raw {
				t.Fatalf("backup content = %q, want %q", string(backupBytes), tc.raw)
			}

			rebuiltBytes, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read rebuilt file: %v", err)
			}
			var rebuilt History
			if err := json.Unmarshal(rebuiltBytes, &rebuilt); err != nil {
				t.Fatalf("rebuilt JSON invalid: %v", err)
			}
			if rebuilt.Version != 1 || len(rebuilt.Checks) != 0 || len(rebuilt.Updates) != 0 {
				t.Fatalf("unexpected rebuilt content: %+v", rebuilt)
			}
			if got := store.Get(); got.Version != 1 || len(got.Checks) != 0 || len(got.Updates) != 0 {
				t.Fatalf("unexpected store snapshot: %+v", got)
			}
		})
	}
}

func TestUpdateHistoryPrivacyScrubbingReadAndWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, HistoryFileName)

	secretSentinel := "CUSTOM_STORED_SECRET_xyz987"
	redactor := func(s string) string {
		return strings.ReplaceAll(s, secretSentinel, "[REDACTED]")
	}

	// Pre-populate dirty JSON on disk to test read-side sanitization as well.
	dirtyOnDisk := `{
  "version": 1,
  "checks": [
    {
      "at": "2026-11-15T09:00:00Z",
      "trigger": "manual",
      "result": "检查失败：Get \"https://github.com/chialecode/trace/releases/download/v0.1.2/trace.zip?X-Amz-Signature=sig_111&token=tok_222\": Cookie: SESSDATA=sess_333; bili_jct=csrf_444; sessionid=dy_555 TLS handshake timeout"
    }
  ],
  "updates": [
    {
      "at": "2026-11-15T09:01:00Z",
      "fromVersion": "https://evil.example/0.1.1?token=bad",
      "toVersion": "0.1.2",
      "method": "auto_update"
    },
    {
      "at": "2026-11-15T09:02:00Z",
      "fromVersion": "0.1.1",
      "toVersion": "0.1.2",
      "method": "auto_update"
    }
  ]
}`
	if err := os.WriteFile(path, []byte(dirtyOnDisk), 0o600); err != nil {
		t.Fatal(err)
	}

	store, backup, err := OpenHistoryStore(path, redactor)
	if err != nil || backup != "" {
		t.Fatalf("OpenHistoryStore: err=%v backup=%q", err, backup)
	}

	// Also test write-side sanitization with encoded URLs, query strings, Bearer tokens, and stored credentials.
	if err := store.RecordCheck(CheckRecord{
		At:      "2026-11-15T10:00:00Z",
		Trigger: CheckTriggerAuto,
		Result:  "检查失败：https%3A%2F%2Fcdn.example.com%2Fpkg.exe%3Fauth_key%3D999888 Authorization: Bearer jwt_secret_token " + secretSentinel + " 连接中断",
	}); err != nil {
		t.Fatalf("RecordCheck dirty: %v", err)
	}

	if err := store.RecordUpdate(UpdateRecord{
		At:          "2026-11-15T10:05:00Z",
		FromVersion: "0.1.2?SESSDATA=leak",
		ToVersion:   "0.1.3",
		Method:      UpdateMethodManual,
	}); err == nil {
		t.Fatal("expected RecordUpdate with query string in version to be rejected")
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	diskStr := string(raw)
	forbidden := []string{
		"http://",
		"https://",
		"https%3A",
		"github.com",
		"cdn.example.com",
		"releases/download",
		".zip",
		".exe",
		"?",
		"X-Amz-Signature",
		"sig_111",
		"tok_222",
		"SESSDATA",
		"sess_333",
		"bili_jct",
		"csrf_444",
		"sessionid",
		"dy_555",
		"auth_key",
		"999888",
		"Bearer",
		"jwt_secret_token",
		secretSentinel,
	}
	for _, needle := range forbidden {
		if strings.Contains(strings.ToLower(diskStr), strings.ToLower(needle)) {
			t.Fatalf("update-history.json leaked forbidden token %q:\n%s", needle, diskStr)
		}
	}

	snap := store.Get()
	if len(snap.Updates) != 1 || snap.Updates[0].FromVersion != "0.1.1" || snap.Updates[0].ToVersion != "0.1.2" {
		t.Fatalf("unexpected updates after scrubbing invalid version entry: %+v", snap.Updates)
	}
	if len(snap.Checks) != 2 {
		t.Fatalf("expected 2 scrubbed check records, got %+v", snap.Checks)
	}
}

func TestControllerRecordsChecksAndExposesNextCheck(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)}
	eng := &fakeEngine{enabled: true}
	prefs := &Prefs{AutoCheck: true, AutoDownload: false}
	var recorded []CheckRecord

	c := NewController(Options{
		Engine:         eng,
		Channel:        "stable",
		CurrentVersion: "0.1.1",
		Prefs:          func() Prefs { return *prefs },
		Now:            clk.now,
		RecordCheck:    func(rec CheckRecord) { recorded = append(recorded, rec) },
	})

	st := c.State()
	if !st.NextCheck.Equal(clk.t.Add(FirstCheckDelay)) {
		t.Fatalf("initial NextCheck = %v, want %v", st.NextCheck, clk.t.Add(FirstCheckDelay))
	}

	// 1. Automatic scheduled check -> trigger "auto", "已是最新版本", NextCheck = +24h.
	clk.t = clk.t.Add(FirstCheckDelay)
	c.Tick(context.Background())
	st = c.State()
	if len(recorded) != 1 || recorded[0].Trigger != CheckTriggerAuto || recorded[0].Result != MsgUpToDate {
		t.Fatalf("auto check record = %+v", recorded)
	}
	if st.LastCheckResult != MsgUpToDate || !st.NextCheck.Equal(clk.t.Add(CheckInterval)) {
		t.Fatalf("state after auto check: LastCheckResult=%q NextCheck=%v", st.LastCheckResult, st.NextCheck)
	}

	// 2. Manual CheckNow finding new version -> trigger "manual", "发现 0.1.2".
	clk.t = clk.t.Add(5 * time.Minute)
	eng.release = &Release{Version: "0.1.2"}
	c.CheckNow(context.Background())
	if len(recorded) != 2 || recorded[1].Trigger != CheckTriggerManual || recorded[1].Result != "发现 0.1.2" {
		t.Fatalf("manual new-version check record = %+v", recorded)
	}
	if c.State().LastCheckResult != "发现 0.1.2" {
		t.Fatalf("LastCheckResult = %q, want 发现 0.1.2", c.State().LastCheckResult)
	}

	// 3. Manual CheckNow failing with URL + query secret -> sanitized "检查失败：...".
	clk.t = clk.t.Add(5 * time.Minute)
	eng.release = nil
	eng.checkErr = errors.New(`Get "https://github.com/chialecode/trace/releases/download/v0.1.2/latest.json?token=sec123": unexpected EOF`)
	c.CheckNow(context.Background())
	if len(recorded) != 3 || recorded[2].Trigger != CheckTriggerManual || !strings.HasPrefix(recorded[2].Result, "检查失败：") {
		t.Fatalf("manual error check record = %+v", recorded)
	}
	if strings.Contains(recorded[2].Result, "https://") || strings.Contains(recorded[2].Result, "sec123") {
		t.Fatalf("check record leaked URL or token: %+v", recorded[2])
	}

	// 4. Dev disabled controller never writes check records and has zero NextCheck.
	var devRecorded []CheckRecord
	devCtrl := NewController(Options{
		Engine:         &fakeEngine{enabled: false},
		Channel:        "dev",
		CurrentVersion: "0.1.2-dev.1",
		Prefs:          func() Prefs { return *prefs },
		Now:            clk.now,
		RecordCheck:    func(rec CheckRecord) { devRecorded = append(devRecorded, rec) },
	})
	if !devCtrl.State().NextCheck.IsZero() {
		t.Fatalf("dev NextCheck = %v, want zero", devCtrl.State().NextCheck)
	}
	devCtrl.Tick(context.Background())
	devCtrl.CheckNow(context.Background())
	if len(devRecorded) != 0 {
		t.Fatalf("dev controller must not record checks, got %+v", devRecorded)
	}
}
