package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/site/douyin"
	"github.com/chialecode/trace/internal/storage"
	"github.com/chialecode/trace/internal/update"
)

func TestBackendUpdateHistoryNewerVersionRefused(t *testing.T) {
	loc := testLocations(t)
	if err := os.MkdirAll(loc.DataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	uhPath := filepath.Join(loc.DataDir, update.HistoryFileName)
	futurePayload := []byte("{\n  \"version\": 2,\n  \"checks\": [],\n  \"updates\": []\n}\n")
	if err := os.WriteFile(uhPath, futurePayload, 0o600); err != nil {
		t.Fatal(err)
	}

	b, err := NewBackend(Options{
		Locations:        loc,
		Desktop:          NewMockDesktop(),
		DisableBgWorkers: true,
	})
	if b != nil {
		_ = b.Close()
		t.Fatal("expected NewBackend to refuse update-history.json version 2")
	}
	if !storage.IsNewerData(err) {
		t.Fatalf("NewBackend err = %v, want *storage.NewerDataError", err)
	}

	after, err := os.ReadFile(uhPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, futurePayload) {
		t.Fatalf("update-history.json was overwritten on NewerDataError:\ngot:  %s\nwant: %s", after, futurePayload)
	}
	if _, statErr := os.Stat(filepath.Join(loc.DataDir, "library.db")); !os.IsNotExist(statErr) {
		t.Fatalf("library.db must not be created when update-history.json is refused: %v", statErr)
	}
}

func TestBackendUpdateHistoryCorruptRecoveryAndExistingSchemasUnchanged(t *testing.T) {
	// Verify current data schema versions match storage.DataSchemas (ui-state.json & subscriptions.json bumped to v4 for X).
	wantSchemas := map[string]int{
		"settings.json":      1,
		"ui-state.json":      4,
		"history.json":       1,
		"subscriptions.json": 4,
		"library.db":         2,
		"updates.db":         2,
	}
	if len(storage.DataSchemas) != len(wantSchemas) {
		t.Fatalf("DataSchemas count = %d, want %d", len(storage.DataSchemas), len(wantSchemas))
	}
	for _, sc := range storage.DataSchemas {
		if want, ok := wantSchemas[sc.Name]; !ok || sc.Current != want {
			t.Fatalf("DataSchema %s = %d, want %d", sc.Name, sc.Current, want)
		}
	}

	loc := testLocations(t)
	if err := os.MkdirAll(loc.DataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	uhPath := filepath.Join(loc.DataDir, update.HistoryFileName)
	corruptRaw := `{"version": 1, "checks": [BROKEN_JSON`
	if err := os.WriteFile(uhPath, []byte(corruptRaw), 0o600); err != nil {
		t.Fatal(err)
	}

	b, err := NewBackend(Options{
		Locations:        loc,
		Desktop:          NewMockDesktop(),
		DisableBgWorkers: true,
	})
	if err != nil {
		t.Fatalf("NewBackend with corrupt update-history.json: %v", err)
	}
	defer b.Close()

	diag := b.DiagnosticsSnapshot()
	foundBackup := ""
	for _, p := range diag.CorruptRecovered {
		if strings.Contains(filepath.Base(p), "update-history.json.corrupt-") {
			foundBackup = p
		}
	}
	if foundBackup == "" {
		t.Fatalf("Diagnostics.CorruptRecovered missing update-history backup: %+v", diag.CorruptRecovered)
	}
	backupData, err := os.ReadFile(foundBackup)
	if err != nil || string(backupData) != corruptRaw {
		t.Fatalf("corrupt backup content = %q (err=%v), want %q", string(backupData), err, corruptRaw)
	}

	rebuiltRaw, err := os.ReadFile(uhPath)
	if err != nil {
		t.Fatalf("read rebuilt update-history.json: %v", err)
	}
	var rebuilt update.History
	if err := json.Unmarshal(rebuiltRaw, &rebuilt); err != nil {
		t.Fatalf("rebuilt update-history.json invalid: %v", err)
	}
	if rebuilt.Version != 1 || len(rebuilt.Checks) != 0 || len(rebuilt.Updates) != 0 {
		t.Fatalf("unexpected rebuilt update-history.json: %+v", rebuilt)
	}

	logs, err := b.RecentLogs(50)
	if err != nil {
		t.Fatalf("RecentLogs: %v", err)
	}
	loggedRecovery := false
	for _, ln := range logs {
		if strings.Contains(ln, "recovered corrupted update-history.json") {
			loggedRecovery = true
			break
		}
	}
	if !loggedRecovery {
		t.Fatalf("expected recovery line in trace.log, got %v", logs)
	}
}

func TestBackendUpdateHistoryPrivacySentinelAndInstalledDevVersion(t *testing.T) {
	const (
		biliSessSentinel = "SENTINEL_BILI_SESS_u1_901"
		biliCsrfSentinel = "SENTINEL_BILI_CSRF_u1_902"
		dySessSentinel   = "SENTINEL_DY_SESS_u1_903"
		downloadURL      = "https://github.com/chialecode/trace/releases/download/v0.1.9/trace-windows-amd64.zip?X-Amz-Signature=SENTINEL_URL_SIG_904"
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/x/web-interface/nav":
			_, _ = fmt.Fprint(w, `{"code":0,"data":{"isLogin":true,"mid":10001,"uname":"隐私测试B站","wbi_img":{"img_url":"https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png","sub_url":"https://i0.hdslb.com/bfs/wbi/4932caff0ff746eab6f01bf08b70ac45.png"}}}`)
		case "/aweme/v1/web/user/profile/self/":
			_, _ = fmt.Fprint(w, `{"status_code":0,"user":{"uid":"20002","sec_uid":"MS4wLjABAAAA_synth_self_u1_0001","unique_id":"dy_u1","nickname":"隐私测试抖音"}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	loc := testLocations(t)
	injectedDevVer := "0.1.2-dev.202611151001"
	b, err := NewBackend(Options{
		Locations:        loc,
		Desktop:          NewMockDesktop(),
		DisableBgWorkers: true,
		InstalledDevVersion: func() string {
			return injectedDevVer
		},
		BilibiliOpts: bilibili.ClientOptions{
			Endpoints: bilibili.Endpoints{
				APIBase: srv.URL, PassportBase: srv.URL, WWWBase: srv.URL,
				SpaceBase: srv.URL, DynamicBase: srv.URL, LiveAPIBase: srv.URL, LiveWebBase: srv.URL,
			},
			AllowLocalhost: true,
		},
		DouyinOpts: douyin.ClientOptions{
			Endpoints: douyin.Endpoints{
				ProfileSelf: srv.URL + "/aweme/v1/web/user/profile/self/",
			},
			MinInterval:    -1,
			JitterMax:      -1,
			AllowLocalhost: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	if got := b.InstalledDevVersion(); got != "0.1.2-dev.202611151001" {
		t.Fatalf("InstalledDevVersion = %q, want 0.1.2-dev.202611151001", got)
	}
	injectedDevVer = "https://evil.example/0.1.3?token=bad"
	if got := b.InstalledDevVersion(); got != "" {
		t.Fatalf("dirty InstalledDevVersion should sanitize to empty, got %q", got)
	}

	if _, err := b.ImportCookie(context.Background(), fmt.Sprintf("SESSDATA=%s; bili_jct=%s; DedeUserID=10001", biliSessSentinel, biliCsrfSentinel), ""); err != nil {
		t.Fatalf("ImportCookie bilibili: %v", err)
	}
	if _, err := b.LoginWithCookie(context.Background(), "douyin", "sessionid="+dySessSentinel, ""); err != nil {
		t.Fatalf("LoginWithCookie douyin: %v", err)
	}

	now := time.Date(2026, 11, 15, 11, 0, 0, 0, time.UTC).Format(time.RFC3339)
	dirtyResult := fmt.Sprintf("检查失败：Get %q Cookie=%s %s %s 连接超时", downloadURL, biliSessSentinel, biliCsrfSentinel, dySessSentinel)
	if err := b.RecordUpdateCheck(update.CheckRecord{
		At:      now,
		Trigger: update.CheckTriggerManual,
		Result:  dirtyResult,
	}); err != nil {
		t.Fatalf("RecordUpdateCheck: %v", err)
	}
	if err := b.RecordAppUpdate(update.UpdateRecord{
		At:          now,
		FromVersion: "0.1.1",
		ToVersion:   "0.1.2",
		Method:      update.UpdateMethodAuto,
	}); err != nil {
		t.Fatalf("RecordAppUpdate: %v", err)
	}

	uhRaw, err := os.ReadFile(filepath.Join(loc.DataDir, update.HistoryFileName))
	if err != nil {
		t.Fatalf("read update-history.json: %v", err)
	}
	sentinels := []string{
		biliSessSentinel,
		biliCsrfSentinel,
		dySessSentinel,
		"SENTINEL_URL_SIG_904",
		"https://",
		"github.com",
		".zip",
		"?",
	}
	for _, s := range sentinels {
		if strings.Contains(string(uhRaw), s) {
			t.Fatalf("update-history.json leaked sentinel %q:\n%s", s, string(uhRaw))
		}
	}
	if logRaw, lErr := os.ReadFile(filepath.Join(loc.LogsDir, "trace.log")); lErr == nil {
		for _, s := range sentinels[:4] {
			if strings.Contains(string(logRaw), s) {
				t.Fatalf("trace.log leaked sentinel %q:\n%s", s, string(logRaw))
			}
		}
	}
}
