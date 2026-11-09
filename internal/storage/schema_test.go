package storage

import (
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
		"subscriptions.json": `{"version":1,"subscriptions":[]}`,
		"history.json":       `not json at all`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	backups, err := GuardDataSchemas(dir, DataSchemas)
	if err != nil || len(backups) != 0 {
		t.Fatalf("current data: %v %v", backups, err)
	}
}
