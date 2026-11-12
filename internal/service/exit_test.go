package service

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/storage"
)

func testLocations(t *testing.T) appdirs.Locations {
	t.Helper()
	tmp := t.TempDir()
	loc, err := appdirs.ResolveWithOptions(appdirs.Options{
		Channel:   "dev",
		ForceMode: "portable",
		DataDir:   filepath.Join(tmp, "trace-data"),
		CacheDir:  filepath.Join(tmp, "trace-cache"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestBackendRefusesDataOfNewerTrace(t *testing.T) {
	loc := testLocations(t)
	if err := appdirs.EnsureDirectories(loc); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(loc.DataDir, "settings.json")
	newer := []byte(`{"version": 2, "videoQuality": "a value this build does not know"}`)
	if err := os.WriteFile(settingsPath, newer, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewBackend(Options{Locations: loc, Desktop: NewMockDesktop(), DisableBgWorkers: true})
	if !storage.IsNewerData(err) {
		t.Fatalf("expected the newer-data refusal, got %v", err)
	}
	got, _ := os.ReadFile(settingsPath)
	if string(got) != string(newer) {
		t.Fatal("settings.json of a newer TRACE was rewritten")
	}
	for _, name := range []string{"library.db", "updates.db", "ui-state.json"} {
		if _, err := os.Stat(filepath.Join(loc.DataDir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s was created although the start was refused", name)
		}
	}
}

func TestBackendStampsAndRefusesNewerDatabase(t *testing.T) {
	loc := testLocations(t)
	b, err := NewBackend(Options{Locations: loc, Desktop: NewMockDesktop(), DisableBgWorkers: true})
	if err != nil {
		t.Fatal(err)
	}
	b.Close()
	dbPath := filepath.Join(loc.DataDir, "library.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != 2 {
		t.Fatalf("library.db user_version = %d (%v), want 2", v, err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 9`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := NewBackend(Options{Locations: loc, Desktop: NewMockDesktop(), DisableBgWorkers: true}); !storage.IsNewerData(err) {
		t.Fatalf("library.db of a newer TRACE: %v", err)
	}
}

func TestPauseForExitKeepsTasksPausedForRestart(t *testing.T) {
	loc := testLocations(t)
	b, err := NewBackend(Options{Locations: loc, Desktop: NewMockDesktop(), DisableBgWorkers: true})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	db, err := sql.Open("sqlite", filepath.Join(loc.DataDir, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, row := range []struct{ id, status string }{{"t-run", "running"}, {"t-queue", "queued"}, {"t-done", "completed"}, {"t-user", "paused"}} {
		if _, err := db.Exec(`INSERT INTO download_tasks (task_id, site, kind, target_id, status, created_at, updated_at) VALUES (?, 'bilibili', 'video', ?, ?, ?, ?)`, row.id, row.id, row.status, now, now); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	if tasks, recs := b.ActiveWork(); tasks != 2 || recs != 0 {
		t.Fatalf("ActiveWork = %d, %d", tasks, recs)
	}
	b.PauseForExit()
	list, err := b.DownloadManager().ListTasks()
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range list {
		switch task.TaskID {
		case "t-run", "t-queue":
			if task.Status != "paused" || task.PauseReason != "app_closed" || task.ErrorMessage != "已暂停（应用关闭）" {
				t.Fatalf("%s after exit: %+v", task.TaskID, task)
			}
		case "t-done":
			if task.Status != "completed" {
				t.Fatalf("completed task changed: %+v", task)
			}
		case "t-user":
			if task.Status != "paused" {
				t.Fatalf("paused task changed: %+v", task)
			}
		}
	}
	if tasks, _ := b.ActiveWork(); tasks != 0 {
		t.Fatalf("active tasks after exit: %d", tasks)
	}
}
