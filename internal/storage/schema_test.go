package storage

import (
	"database/sql"
	"encoding/binary"
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
		"ui-state.json":      `{"version":4,"feedFilter":{"kind":"all","site":"x"}}`,
		"subscriptions.json": `{"version":4,"subscriptions":[]}`,
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
	if tmps, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".trace-"+filepath.Base(path)+".*.tmp")); len(tmps) != 0 {
		t.Fatalf("temp files leaked after pre-rename fault: %v", tmps)
	}
}

func TestSchemaGuardCorruptFileDoesNotOverwritePremigrateBackup(t *testing.T) {
	dir := t.TempDir()
	uiPath := filepath.Join(dir, "ui-state.json")
	origBackup := uiPath + ".premigrate-1"
	const savedV1 = `{"version":1,"lastRoute":"/saved-v1"}`
	if err := os.WriteFile(origBackup, []byte(savedV1), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(uiPath, []byte("{corrupt json"), 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "library.db")
	origDBBackup := dbPath + ".premigrate-1"
	if err := os.WriteFile(origDBBackup, []byte("precious-v1-db"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dbPath, []byte("not-a-sqlite-file"), 0o600); err != nil {
		t.Fatal(err)
	}

	backups, err := GuardDataSchemas(dir, DataSchemas)
	if err != nil {
		t.Fatalf("GuardDataSchemas on corrupt files: %v", err)
	}
	if len(backups) != 0 {
		t.Fatalf("expected 0 premigrate backups for corrupt files, got %v", backups)
	}
	gotUI, _ := os.ReadFile(origBackup)
	if string(gotUI) != savedV1 {
		t.Fatalf("existing ui-state .premigrate-1 was clobbered: %q", gotUI)
	}
	gotDB, _ := os.ReadFile(origDBBackup)
	if string(gotDB) != "precious-v1-db" {
		t.Fatalf("existing library.db .premigrate-1 was clobbered: %q", gotDB)
	}
}

func TestSchemaGuardReadsUncheckpointedWALUserVersion(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "library.db")
	db, _, _, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := StampSQLiteSchema(db, 1); err != nil {
		t.Fatal(err)
	}
	// Hold a read transaction on a second connection so page 1 stays in -wal
	// when the first connection commits user_version = 5.
	reader, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	tx, err := reader.Begin()
	if err != nil {
		t.Fatal(err)
	}
	var cnt int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM t`).Scan(&cnt); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(`PRAGMA wal_autocheckpoint = 0`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 5`); err != nil {
		t.Fatal(err)
	}
	// Copy main + wal files to a second directory while the WAL is uncheckpointed
	// to simulate a crash before checkpoint without touching the live handles.
	snapDir := t.TempDir()
	for _, suffix := range []string{"", "-wal"} {
		data, rErr := os.ReadFile(dbPath + suffix)
		if rErr != nil {
			t.Fatalf("read %s: %v", dbPath+suffix, rErr)
		}
		if wErr := os.WriteFile(filepath.Join(snapDir, "library.db"+suffix), data, 0o600); wErr != nil {
			t.Fatal(wErr)
		}
	}
	_ = tx.Rollback()
	_ = reader.Close()
	_ = db.Close()

	// Verify main DB header still reads version 1 so the test truly exercises WAL parsing.
	mainBytes, err := os.ReadFile(filepath.Join(snapDir, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	if mainVer := int(int32(binary.BigEndian.Uint32(mainBytes[60:64]))); mainVer != 1 {
		t.Fatalf("expected uncheckpointed main header version 1, got %d", mainVer)
	}

	ver, exists, err := ReadSchemaVersion(filepath.Join(snapDir, "library.db"), SchemaSQLite)
	if err != nil || !exists || ver != 5 {
		t.Fatalf("ReadSchemaVersion(uncheckpointed WAL) = (%d, %v, %v), want (5, true, nil)", ver, exists, err)
	}
	if _, err := GuardDataSchemas(snapDir, DataSchemas); !IsNewerData(err) {
		t.Fatalf("GuardDataSchemas on uncheckpointed v5 WAL = %v, want NewerDataError", err)
	}
}

func TestSchemaGuardV2ToV3BackupIdempotencyAndNewerDataRejection(t *testing.T) {
	v3Schemas := []SchemaFile{
		{Name: "settings.json", Kind: SchemaJSON, Current: 1},
		{Name: "ui-state.json", Kind: SchemaJSON, Current: 3},
		{Name: "history.json", Kind: SchemaJSON, Current: 1},
		{Name: "subscriptions.json", Kind: SchemaJSON, Current: 3},
		{Name: "library.db", Kind: SchemaSQLite, Current: 2},
		{Name: "updates.db", Kind: SchemaSQLite, Current: 2},
	}

	dir := t.TempDir()
	uiV2 := `{"version":2,"feedFilter":{"kind":"all","site":"bilibili"}}`
	subsV2 := `{"version":2,"subscriptions":[]}`
	if err := os.WriteFile(filepath.Join(dir, "ui-state.json"), []byte(uiV2), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ui-state.json.premigrate-1"), []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "subscriptions.json"), []byte(subsV2), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "subscriptions.json.premigrate-1"), []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
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

	backups, err := GuardDataSchemas(dir, v3Schemas)
	if err != nil {
		t.Fatalf("GuardDataSchemas v2->v3: %v", err)
	}
	if len(backups) != 2 {
		t.Fatalf("expected 2 backups (ui-state.json + subscriptions.json), got %v", backups)
	}
	for name, orig := range map[string]string{
		"ui-state.json":      uiV2,
		"subscriptions.json": subsV2,
	} {
		matches, _ := filepath.Glob(filepath.Join(dir, name+".premigrate-*"))
		if len(matches) != 1 || filepath.Base(matches[0]) != name+".premigrate-2" {
			t.Fatalf("%s premigrate backups = %v, want only .premigrate-2", name, matches)
		}
		got, err := os.ReadFile(matches[0])
		if err != nil || string(got) != orig {
			t.Fatalf("%s backup content = %q err=%v, want %q", name, got, err, orig)
		}
	}

	// Once files are upgraded to v3, v3 GuardDataSchemas is a no-op (idempotent).
	uiV3 := `{"version":3,"feedFilter":{"kind":"all","site":"weibo"}}`
	subsV3 := `{"version":3,"subscriptions":[]}`
	if err := os.WriteFile(filepath.Join(dir, "ui-state.json"), []byte(uiV3), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "subscriptions.json"), []byte(subsV3), 0o600); err != nil {
		t.Fatal(err)
	}
	backups2, err := GuardDataSchemas(dir, v3Schemas)
	if err != nil || len(backups2) != 0 {
		t.Fatalf("idempotent GuardDataSchemas on v3 = (%v, %v), want (nil, nil)", backups2, err)
	}

	// Older v2 build refuses v3 data without modifying any file.
	v2Schemas := []SchemaFile{
		{Name: "settings.json", Kind: SchemaJSON, Current: 1},
		{Name: "ui-state.json", Kind: SchemaJSON, Current: 2},
		{Name: "history.json", Kind: SchemaJSON, Current: 1},
		{Name: "subscriptions.json", Kind: SchemaJSON, Current: 2},
		{Name: "library.db", Kind: SchemaSQLite, Current: 2},
		{Name: "updates.db", Kind: SchemaSQLite, Current: 2},
	}
	if _, err := GuardDataSchemas(dir, v2Schemas); !IsNewerData(err) {
		t.Fatalf("v2 GuardDataSchemas on v3 data = %v, want NewerDataError", err)
	}
	gotUI, _ := os.ReadFile(filepath.Join(dir, "ui-state.json"))
	if string(gotUI) != uiV3 {
		t.Fatalf("v2 guard modified v3 ui-state.json: %s", gotUI)
	}

	// Simulated v3 build refuses v4+ files without writing backups.
	v4Dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(v4Dir, "ui-state.json"), []byte(`{"version":4,"feedFilter":{"kind":"all","site":"x"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(v4Dir, "subscriptions.json"), []byte(`{"version":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := GuardDataSchemas(v4Dir, v3Schemas); !IsNewerData(err) {
		t.Fatalf("v3 GuardDataSchemas on v4 ui-state = %v, want NewerDataError", err)
	}
	if matches, _ := filepath.Glob(filepath.Join(v4Dir, "*.premigrate-*")); len(matches) != 0 {
		t.Fatalf("GuardDataSchemas created backups before rejecting v4: %v", matches)
	}
}

func TestSchemaGuardV3ToV4BackupIdempotencyAndDowngradeRejection(t *testing.T) {
	wantVersions := map[string]int{
		"settings.json":      1,
		"ui-state.json":      4,
		"history.json":       1,
		"subscriptions.json": 4,
		"library.db":         2,
		"updates.db":         2,
	}
	for name, want := range wantVersions {
		sf, ok := SchemaFor(name)
		if !ok || sf.Current != want {
			t.Fatalf("SchemaFor(%q) = (%+v, %v), want Current=%d", name, sf, ok, want)
		}
	}

	dir := t.TempDir()
	uiV3 := `{"version":3,"feedFilter":{"kind":"all","site":"weibo"}}`
	subsV3 := `{"version":3,"subscriptions":[]}`
	if err := os.WriteFile(filepath.Join(dir, "ui-state.json"), []byte(uiV3), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ui-state.json.premigrate-2"), []byte(`{"version":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "subscriptions.json"), []byte(subsV3), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "subscriptions.json.premigrate-2"), []byte(`{"version":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "history.json"), []byte(`{"version":1,"items":[{"site":"x","kind":"dynamic","id":"1000000000000000009","title":"synth","canonicalUrl":"https://x.com/i/web/status/1000000000000000009"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
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
	if err != nil {
		t.Fatalf("GuardDataSchemas v3->v4: %v", err)
	}
	if len(backups) != 2 {
		t.Fatalf("expected 2 backups (ui-state.json + subscriptions.json), got %v", backups)
	}
	for name, orig := range map[string]string{
		"ui-state.json":      uiV3,
		"subscriptions.json": subsV3,
	} {
		matches, _ := filepath.Glob(filepath.Join(dir, name+".premigrate-*"))
		if len(matches) != 1 || filepath.Base(matches[0]) != name+".premigrate-3" {
			t.Fatalf("%s premigrate backups = %v, want only .premigrate-3", name, matches)
		}
		got, err := os.ReadFile(matches[0])
		if err != nil || string(got) != orig {
			t.Fatalf("%s backup content = %q err=%v, want %q", name, got, err, orig)
		}
	}
	for _, unchanged := range []string{"history.json", "settings.json", "library.db", "updates.db"} {
		if matches, _ := filepath.Glob(filepath.Join(dir, unchanged+".premigrate-*")); len(matches) != 0 {
			t.Fatalf("unexpected premigrate backup for unchanged %s: %v", unchanged, matches)
		}
	}

	// Mid-migration failure after .premigrate-3 keeps original v3 file and backup intact.
	restore := SetBeforeMigrationRenameHookForTest(func(targetPath, tmpPath string) error {
		return errors.New("simulated v3->v4 pre-rename failure")
	})
	if err := WriteMigratedJSONAtomic(filepath.Join(dir, "ui-state.json"), map[string]any{"version": 4}, 0o600); err == nil {
		restore()
		t.Fatal("expected pre-rename failure")
	}
	restore()
	if stillV3, _ := os.ReadFile(filepath.Join(dir, "ui-state.json")); string(stillV3) != uiV3 {
		t.Fatalf("v3 ui-state.json altered on mid-migration failure: %s", stillV3)
	}
	if bakStillV3, _ := os.ReadFile(filepath.Join(dir, "ui-state.json.premigrate-3")); string(bakStillV3) != uiV3 {
		t.Fatalf("ui-state.json.premigrate-3 altered on mid-migration failure: %s", bakStillV3)
	}

	// Once files are upgraded to v4, GuardDataSchemas is a no-op (idempotent).
	uiV4 := `{"version":4,"feedFilter":{"kind":"all","site":"x"}}`
	subsV4 := `{"version":4,"subscriptions":[{"id":"s1","accounts":[{"site":"x","uid":"1000000000000000001"}]}]}`
	if err := os.WriteFile(filepath.Join(dir, "ui-state.json"), []byte(uiV4), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "subscriptions.json"), []byte(subsV4), 0o600); err != nil {
		t.Fatal(err)
	}
	backups2, err := GuardDataSchemas(dir, DataSchemas)
	if err != nil || len(backups2) != 0 {
		t.Fatalf("idempotent GuardDataSchemas on v4 = (%v, %v), want (nil, nil)", backups2, err)
	}

	// Older v3 build refuses v4 data without modifying any file.
	v3Schemas := []SchemaFile{
		{Name: "settings.json", Kind: SchemaJSON, Current: 1},
		{Name: "ui-state.json", Kind: SchemaJSON, Current: 3},
		{Name: "history.json", Kind: SchemaJSON, Current: 1},
		{Name: "subscriptions.json", Kind: SchemaJSON, Current: 3},
		{Name: "library.db", Kind: SchemaSQLite, Current: 2},
		{Name: "updates.db", Kind: SchemaSQLite, Current: 2},
	}
	if _, err := GuardDataSchemas(dir, v3Schemas); !IsNewerData(err) {
		t.Fatalf("v3 GuardDataSchemas on v4 data = %v, want NewerDataError", err)
	}
	if gotUI, _ := os.ReadFile(filepath.Join(dir, "ui-state.json")); string(gotUI) != uiV4 {
		t.Fatalf("v3 guard modified v4 ui-state.json: %s", gotUI)
	}
	if gotSubs, _ := os.ReadFile(filepath.Join(dir, "subscriptions.json")); string(gotSubs) != subsV4 {
		t.Fatalf("v3 guard modified v4 subscriptions.json: %s", gotSubs)
	}

	// Current v4 build refuses v5+ files without writing backups.
	v5Dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(v5Dir, "ui-state.json"), []byte(`{"version":5}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(v5Dir, "subscriptions.json"), []byte(`{"version":3}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := GuardDataSchemas(v5Dir, DataSchemas); !IsNewerData(err) {
		t.Fatalf("v4 GuardDataSchemas on v5 ui-state = %v, want NewerDataError", err)
	}
	if matches, _ := filepath.Glob(filepath.Join(v5Dir, "*.premigrate-*")); len(matches) != 0 {
		t.Fatalf("GuardDataSchemas created backups before rejecting v5: %v", matches)
	}
}
