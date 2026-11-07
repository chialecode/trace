package storage

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// OpenSQLite opens a SQLite database in WAL mode and runs PRAGMA quick_check.
// If the existing database file is corrupted, it backs up the file (and any -wal/-shm sidecars)
// to <dbPath>.corrupt-<timestamp> and opens a fresh database, returning recovered=true.
func OpenSQLite(dbPath string) (db *sql.DB, recovered bool, backupPath string, err error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return nil, false, "", fmt.Errorf("storage: mkdir db dir: %w", err)
	}

	db, err = openAndVerifySQLite(dbPath)
	if err == nil {
		return db, false, "", nil
	}

	// Database failed verification or could not be opened: back up corrupt file and retry once.
	backupPath, bErr := BackupCorruptFile(dbPath)
	if bErr != nil {
		return nil, false, "", fmt.Errorf("storage: sqlite open failed (%v) and backup failed: %w", err, bErr)
	}
	_, _ = BackupCorruptFile(dbPath + "-wal")
	_, _ = BackupCorruptFile(dbPath + "-shm")

	db, err = openAndVerifySQLite(dbPath)
	if err != nil {
		return nil, false, backupPath, fmt.Errorf("storage: sqlite reopen after backup failed: %w", err)
	}
	return db, true, backupPath, nil
}

func openAndVerifySQLite(dbPath string) (*sql.DB, error) {
	// Fast header check if file exists and is non-empty.
	if info, err := os.Stat(dbPath); err == nil && info.Size() > 0 {
		f, err := os.Open(dbPath)
		if err != nil {
			return nil, err
		}
		var hdr [16]byte
		n, _ := f.Read(hdr[:])
		_ = f.Close()
		if n < 16 || string(hdr[:16]) != "SQLite format 3\x00" {
			return nil, fmt.Errorf("invalid sqlite header")
		}
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)

	pragmas := []string{
		"PRAGMA busy_timeout = 5000;",
		"PRAGMA journal_mode = WAL;",
		"PRAGMA synchronous = NORMAL;",
		"PRAGMA foreign_keys = ON;",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("exec %s: %w", p, err)
		}
	}

	rows, err := db.Query("PRAGMA quick_check;")
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("quick_check query: %w", err)
	}
	defer rows.Close()

	var result string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("quick_check scan: %w", err)
		}
		if result == "" {
			result = line
		} else {
			result += "; " + line
		}
	}
	if err := rows.Err(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("quick_check rows: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(result), "ok") {
		_ = db.Close()
		return nil, fmt.Errorf("quick_check failed: %s", result)
	}

	return db, nil
}
