package storage

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSchemaGuardRefusesNewerJSONWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	raw := []byte(`{"version": 7, "cacheRoot": "kept"}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := GuardDataSchemas(dir, DataSchemas)
	if !IsNewerData(err) {
		t.Fatalf("newer settings.json: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(raw) {
		t.Fatal("newer file was changed")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("guard wrote files: %v", entries)
	}
}

func TestSchemaGuardRefusesNewerSQLite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "library.db")
	db, _, _, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE t (x INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if err := StampSQLiteSchema(db, 5); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckSQLiteSchema(db, "library.db", 1); !IsNewerData(err) {
		t.Fatalf("open database of a newer version: %v", err)
	}
	db.Close()
	if _, err := GuardDataSchemas(dir, DataSchemas); !IsNewerData(err) {
		t.Fatalf("header of a newer database: %v", err)
	}
}

func TestSchemaGuardBacksUpBeforeMigrationKeepingLatest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ui-state.json")
	schemas := []SchemaFile{{Name: "ui-state.json", Kind: SchemaJSON, Current: 3}}

	if err := os.WriteFile(path, []byte(`{"lastRoute":"/home"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	backups, err := GuardDataSchemas(dir, schemas)
	if err != nil || len(backups) != 1 || filepath.Base(backups[0]) != "ui-state.json.premigrate-1" {
		t.Fatalf("unversioned file is version 1 and is backed up: %v %v", backups, err)
	}
	// A later migration from version 2 replaces the copy of version 1.
	if err := os.WriteFile(path, []byte(`{"version":2,"lastRoute":"/library"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	backups, err = GuardDataSchemas(dir, schemas)
	if err != nil || len(backups) != 1 {
		t.Fatalf("second migration: %v %v", backups, err)
	}
	matches, _ := filepath.Glob(path + ".premigrate-*")
	if len(matches) != 1 || filepath.Base(matches[0]) != "ui-state.json.premigrate-2" {
		t.Fatalf("only the latest premigrate copy stays: %v", matches)
	}
	got, _ := os.ReadFile(matches[0])
	if string(got) != `{"version":2,"lastRoute":"/library"}` {
		t.Fatalf("backup content %q", got)
	}
	// Current files are left alone.
	if err := os.WriteFile(path, []byte(`{"version":3}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if backups, err := GuardDataSchemas(dir, schemas); err != nil || len(backups) != 0 {
		t.Fatalf("current file: %v %v", backups, err)
	}
}

func TestSchemaGuardCurrentDataUntouched(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"settings.json":      `{"version":1}`,
		"ui-state.json":      `{"version":2,"feedFilter":{"kind":"all","site":"all"}}`,
		"subscriptions.json": `{"version":2,"subscriptions":[]}`,
		"history.json":       `not json at all`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, dbName := range []string{"library.db", "updates.db"} {
		db, _, _, err := OpenSQLite(filepath.Join(dir, dbName))
		if err != nil {
			t.Fatal(err)
		}
		if err := StampSQLiteSchema(db, 2); err != nil {
			t.Fatal(err)
		}
		_ = db.Close()
	}
	backups, err := GuardDataSchemas(dir, DataSchemas)
	if err != nil || len(backups) != 0 {
		t.Fatalf("current data: %v %v", backups, err)
	}
}

func TestSchemaGuardBacksUpSQLiteWALAndSHM(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "library.db")
	db, _, _, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE sample (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := StampSQLiteSchema(db, 1); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	if err := os.WriteFile(dbPath+"-wal", []byte("synthetic-wal-v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dbPath+"-shm", []byte("synthetic-shm-v1"), 0o600); err != nil {
		t.Fatal(err)
	}

	backups, err := GuardDataSchemas(dir, DataSchemas)
	if err != nil || len(backups) != 1 {
		t.Fatalf("expected 1 backup, got %v err=%v", backups, err)
	}
	for _, side := range []string{"", "-wal", "-shm"} {
		if _, err := os.Stat(dbPath + ".premigrate-1" + side); err != nil {
			t.Fatalf("missing backup artifact %s: %v", dbPath+".premigrate-1"+side, err)
		}
	}

	// Simulate a future v2 -> v3 upgrade where -wal/-shm no longer exist:
	// old .premigrate-1* files are pruned so only .premigrate-2 remains.
	_ = os.Remove(dbPath + "-wal")
	_ = os.Remove(dbPath + "-shm")
	db2, _, _, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := StampSQLiteSchema(db2, 2); err != nil {
		t.Fatal(err)
	}
	_ = db2.Close()

	v3Schemas := []SchemaFile{{Name: "library.db", Kind: SchemaSQLite, Current: 3}}
	if _, err := GuardDataSchemas(dir, v3Schemas); err != nil {
		t.Fatalf("v2 -> v3 backup: %v", err)
	}
	matches, _ := filepath.Glob(dbPath + ".premigrate-*")
	if len(matches) != 1 || filepath.Base(matches[0]) != "library.db.premigrate-2" {
		t.Fatalf("expected only library.db.premigrate-2, got %v", matches)
	}
}

func TestSchemaGuardBackupFailureAbortsAndMixedNewerWritesNothing(t *testing.T) {
	t.Run("backup failure aborts before migration", func(t *testing.T) {
		dir := t.TempDir()
		uiPath := filepath.Join(dir, "ui-state.json")
		orig := `{"version":1,"feedFilter":{"kind":"all","site":"bilibili"}}`
		if err := os.WriteFile(uiPath, []byte(orig), 0o600); err != nil {
			t.Fatal(err)
		}
		// Force BackupBeforeMigration to fail by blocking target path with a directory.
		if err := os.Mkdir(uiPath+".premigrate-1", 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := GuardDataSchemas(dir, DataSchemas); err == nil {
			t.Fatal("expected backup failure error")
		}
		got, _ := os.ReadFile(uiPath)
		if string(got) != orig {
			t.Fatalf("source file modified on backup failure: %s", got)
		}
	})

	t.Run("mixed v1 and newer file writes nothing", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "ui-state.json"), []byte(`{"version":1}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "subscriptions.json"), []byte(`{"version":9}`), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := GuardDataSchemas(dir, DataSchemas)
		if !IsNewerData(err) {
			t.Fatalf("expected NewerDataError, got %v", err)
		}
		if matches, _ := filepath.Glob(filepath.Join(dir, "*.premigrate-*")); len(matches) != 0 {
			t.Fatalf("GuardDataSchemas created backups before rejecting newer file: %v", matches)
		}
	})

	t.Run("all-v1 DataSchemas rejects v2 data without writing", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "ui-state.json"), []byte(`{"version":2}`), 0o600); err != nil {
			t.Fatal(err)
		}
		db, _, _, err := OpenSQLite(filepath.Join(dir, "library.db"))
		if err != nil {
			t.Fatal(err)
		}
		if err := StampSQLiteSchema(db, 2); err != nil {
			t.Fatal(err)
		}
		_ = db.Close()

		v1Schemas := []SchemaFile{
			{Name: "settings.json", Kind: SchemaJSON, Current: 1},
			{Name: "ui-state.json", Kind: SchemaJSON, Current: 1},
			{Name: "history.json", Kind: SchemaJSON, Current: 1},
			{Name: "subscriptions.json", Kind: SchemaJSON, Current: 1},
			{Name: "library.db", Kind: SchemaSQLite, Current: 1},
			{Name: "updates.db", Kind: SchemaSQLite, Current: 1},
		}
		entriesBefore, _ := os.ReadDir(dir)
		_, err = GuardDataSchemas(dir, v1Schemas)
		if !IsNewerData(err) {
			t.Fatalf("expected all-v1 schemas to reject v2 data, got %v", err)
		}
		entriesAfter, _ := os.ReadDir(dir)
		if len(entriesAfter) != len(entriesBefore) {
			t.Fatalf("all-v1 guard wrote files: before=%d after=%d", len(entriesBefore), len(entriesAfter))
		}
	})
}

func TestWriteMigratedJSONAtomicPreRenameFaultCleansTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ui-state.json")
	orig := `{"version":1}`
	if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	restore := SetBeforeMigrationRenameHookForTest(func(targetPath, tmpPath string) error {
		if _, err := os.Stat(tmpPath); err != nil {
			t.Fatalf("expected synced temp file before rename: %v", err)
		}
		return errors.New("injected pre-rename fault")
	})
	defer restore()

	err := WriteMigratedJSONAtomic(path, map[string]any{"version": 2}, 0o600)
	if err == nil {
		t.Fatal("expected pre-rename fault error")
	}
	got, _ := os.ReadFile(path)
	if string(got) != orig {
		t.Fatalf("original file altered after pre-rename fault: %s", got)
	}
	if tmps, _ := filepath.Glob(path + ".tmp-*"); len(tmps) != 0 {
		t.Fatalf("temp files leaked after pre-rename fault: %v", tmps)
	}
}
