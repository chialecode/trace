package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

var errCorruptSQLite = errors.New("storage: sqlite database corrupt")

func isSQLiteCorruptionErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"database disk image is malformed",
		"file is not a database",
		"file is encrypted or is not a database",
		"malformed",
		"sqlite_corrupt",
		"sqlite_notadb",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

// OpenSQLite opens a SQLite database in WAL mode and runs PRAGMA quick_check.
// If the existing database file is verified corrupted, it backs up the file and
// its -wal/-shm sidecars under one shared .corrupt-<timestamp> suffix and opens
// a fresh database, returning recovered=true. Permission, busy/lock, or I/O
// errors are returned directly without renaming or recreating the database.
func OpenSQLite(dbPath string) (db *sql.DB, recovered bool, backupPath string, err error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return nil, false, "", fmt.Errorf("storage: mkdir db dir: %w", err)
	}

	db, err = openAndVerifySQLite(dbPath)
	if err == nil {
		return db, false, "", nil
	}
	if !errors.Is(err, errCorruptSQLite) {
		return nil, false, "", fmt.Errorf("storage: open sqlite %s: %w", dbPath, err)
	}

	// Database is verified corrupted: back up main file and sidecars with one shared suffix.
	suffix := nextCorruptSuffix(dbPath)
	backupPath, bErr := backupCorruptWithSuffix(dbPath, suffix)
	if bErr != nil {
		return nil, false, "", fmt.Errorf("storage: sqlite open failed (%v) and backup failed: %w", err, bErr)
	}
	if _, walErr := backupCorruptWithSuffix(dbPath+"-wal", suffix); walErr != nil {
		return nil, false, backupPath, fmt.Errorf("storage: backup corrupt wal sidecar: %w", walErr)
	}
	if _, shmErr := backupCorruptWithSuffix(dbPath+"-shm", suffix); shmErr != nil {
		_ = os.Remove(dbPath + "-shm")
	}

	db, err = openAndVerifySQLite(dbPath)
	if err != nil {
		return nil, false, backupPath, fmt.Errorf("storage: sqlite reopen after backup failed: %w", err)
	}
	return db, true, backupPath, nil
}

func openAndVerifySQLite(dbPath string) (*sql.DB, error) {
	// Fast header check if file exists and is non-empty.
	info, statErr := os.Stat(dbPath)
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}
	if statErr == nil {
		if info.IsDir() {
			return nil, fmt.Errorf("sqlite path %s is a directory", dbPath)
		}
		if info.Size() > 0 {
			f, err := os.Open(dbPath)
			if err != nil {
				return nil, err
			}
			var hdr [16]byte
			n, rErr := io.ReadFull(f, hdr[:])
			_ = f.Close()
			if rErr != nil {
				if errors.Is(rErr, io.EOF) || errors.Is(rErr, io.ErrUnexpectedEOF) {
					return nil, fmt.Errorf("%w: truncated sqlite header (%d bytes)", errCorruptSQLite, n)
				}
				return nil, rErr
			}
			if string(hdr[:16]) != "SQLite format 3\x00" {
				return nil, fmt.Errorf("%w: invalid sqlite header", errCorruptSQLite)
			}
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
			if isSQLiteCorruptionErr(err) {
				return nil, fmt.Errorf("%w: exec %s: %v", errCorruptSQLite, p, err)
			}
			return nil, fmt.Errorf("exec %s: %w", p, err)
		}
	}

	rows, err := db.Query("PRAGMA quick_check;")
	if err != nil {
		_ = db.Close()
		if isSQLiteCorruptionErr(err) {
			return nil, fmt.Errorf("%w: quick_check query: %v", errCorruptSQLite, err)
		}
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
		if isSQLiteCorruptionErr(err) {
			return nil, fmt.Errorf("%w: quick_check rows: %v", errCorruptSQLite, err)
		}
		return nil, fmt.Errorf("quick_check rows: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(result), "ok") {
		_ = db.Close()
		return nil, fmt.Errorf("%w: quick_check failed: %s", errCorruptSQLite, result)
	}

	return db, nil
}
