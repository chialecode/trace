package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/subscription"
)

func TestRuntimeLogsRedactSignedURLsAndCredentialFields(t *testing.T) {
	b := &Backend{logPath: filepath.Join(t.TempDir(), "trace.log")}
	for _, line := range []string{
		"subtitle https://fixture.invalid/subtitle.json?auth_key=synthetic-secret",
		"request AUTH_KEY=synthetic-secret",
		"response {\"refresh_token\":\"synthetic-secret\"}",
		"request w_rid=synthetic-secret",
		"request https://fixture.invalid/stream?opaque=synthetic-secret",
		"request auth_key%3Dsynthetic-secret",
		"request %61uth_key%3Dsynthetic-secret progress=50%",
		`response {\"refresh_token\":\"synthetic-secret\"}`,
	} {
		b.logInfo(line)
	}
	b.logInfo("backend initialized: mode=dev channel=source")
	raw, err := os.ReadFile(b.logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "synthetic-secret") {
		t.Fatal("runtime log writer saved a signed URL or credential field")
	}
	if !strings.Contains(string(raw), "mode=dev channel=source") {
		t.Fatal("ordinary runtime status was incorrectly redacted")
	}
	// Old runtime logs also pass through the same boundary when displayed/copied.
	if err := os.WriteFile(b.logPath, []byte("old auth_key=synthetic-secret\nnormal line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lines, err := b.RecentLogs(500)
	if err != nil || len(lines) != 2 || strings.Contains(strings.Join(lines, "\n"), "synthetic-secret") {
		t.Fatalf("old runtime log redaction failed: lines=%d err=%v", len(lines), err)
	}
}

func TestRecentLogsReadOnlyBoundedTailAndRetainLatestLines(t *testing.T) {
	b := &Backend{logPath: filepath.Join(t.TempDir(), "trace.log")}
	raw := strings.Repeat("x", 2<<20) + "\n状态正常\r\n最后一行\r\n"
	if err := os.WriteFile(b.logPath, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	lines, err := b.RecentLogs(500)
	if err != nil || len(lines) != 2 || lines[0] != "状态正常" || lines[1] != "最后一行" {
		t.Fatalf("recent log read included an unbounded old line or lost its Unicode tail: count=%d err=%v", len(lines), err)
	}
	lines, err = b.RecentLogs(1)
	if err != nil || len(lines) != 1 || lines[0] != "最后一行" {
		t.Fatalf("last-line limit was not respected: lines=%v err=%v", lines, err)
	}
	if err := os.WriteFile(b.logPath, []byte(strings.Repeat("normal\n", 600)), 0o600); err != nil {
		t.Fatal(err)
	}
	lines, err = b.RecentLogs(1000)
	if err != nil || len(lines) != 500 {
		t.Fatalf("500-line upper bound was not respected: count=%d err=%v", len(lines), err)
	}
}

func TestDiagnosticsUsesFullIndexCountAndReportsReadFailures(t *testing.T) {
	root := t.TempDir()
	b, err := NewBackend(Options{
		Locations: appdirs.Locations{
			AppName: "trace-dev", Channel: "dev", Mode: "dev", DataDir: filepath.Join(root, "data"),
			DefaultCacheDir: filepath.Join(root, "cache"), CredentialsDir: filepath.Join(root, "data", "credentials"),
			LogsDir: filepath.Join(root, "data", "logs"), LockFile: filepath.Join(root, "data", "app.lock"),
		},
		BilibiliOpts:     bilibili.ClientOptions{AllowLocalhost: true, PacingInterval: -1},
		Desktop:          NewMockDesktop(),
		DisableBgWorkers: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if _, err := b.subs.Create("fixture person", "", []subscription.AccountRef{{Site: "bilibili", UID: "1"}}, subscription.SubRules{}); err != nil {
		t.Fatal(err)
	}
	tx, err := b.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i := 0; i < 1101; i++ {
		if _, err := tx.Exec(`INSERT INTO library_items (item_key, site, kind, raw_id, json_data) VALUES (?, 'bilibili', 'dynamic', ?, '{}')`, fmt.Sprintf("fixture-%d", i), fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	diag := b.DiagnosticsSnapshot()
	if diag.LibraryCount != 1101 || !strings.Contains(diag.IndexStatus, "1101") {
		t.Fatalf("diagnostics used a page limit instead of actual index count: count=%d status=%q", diag.LibraryCount, diag.IndexStatus)
	}
	if _, err := b.updatesDB.Exec(`DROP TABLE update_item`); err != nil {
		t.Fatal(err)
	}
	if diag := b.DiagnosticsSnapshot(); !strings.Contains(diag.SubscriptionCheckStatus, "读取订阅失败") {
		t.Fatal("diagnostics hid the failed subscription/update index read")
	}
	if _, err := b.db.Exec(`DROP TABLE library_items`); err != nil {
		t.Fatal(err)
	}
	if diag := b.DiagnosticsSnapshot(); !strings.Contains(diag.IndexStatus, "读取索引失败") {
		t.Fatal("diagnostics reported a broken library index as healthy")
	}
}
