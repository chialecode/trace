package storage

import (
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Data structure versions (FR-UPD-004). Every data file TRACE keeps has a
// version: JSON files in their "version" field, SQLite databases in
// PRAGMA user_version. A file without one is from before versions were
// recorded and has the layout of version 1.
//
// Before a newer TRACE migrates a file to a higher version it copies the
// file to <file>.premigrate-<old version>, keeping only the latest such
// copy per file. An older TRACE that finds a file of a higher version than
// it knows refuses to start instead of rewriting it (NewerDataError).

// SchemaKind is how a data file records its version.
type SchemaKind string

const (
	SchemaJSON   SchemaKind = "json"
	SchemaSQLite SchemaKind = "sqlite"
)

// SchemaFile is a data file and the structure version this build writes.
type SchemaFile struct {
	Name    string
	Kind    SchemaKind
	Current int
}

// DataSchemas are the versioned files of the data directory and the
// versions this build reads and writes.
var DataSchemas = []SchemaFile{
	{Name: "settings.json", Kind: SchemaJSON, Current: 1},
	{Name: "ui-state.json", Kind: SchemaJSON, Current: 1},
	{Name: "history.json", Kind: SchemaJSON, Current: 1},
	{Name: "subscriptions.json", Kind: SchemaJSON, Current: 1},
	{Name: "library.db", Kind: SchemaSQLite, Current: 1},
	{Name: "updates.db", Kind: SchemaSQLite, Current: 1},
}

// SchemaFor returns the entry of DataSchemas for a file name.
func SchemaFor(name string) (SchemaFile, bool) {
	for _, f := range DataSchemas {
		if f.Name == name {
			return f, true
		}
	}
	return SchemaFile{}, false
}

// NewerDataError reports a data file written by a newer TRACE.
type NewerDataError struct {
	File      string
	Found     int
	Supported int
}

// NewerDataMessage is what the user is told when the data is newer than the
// running TRACE.
const NewerDataMessage = "这些数据来自更新的 TRACE 版本，请安装新版"

func (e *NewerDataError) Error() string {
	return fmt.Sprintf("%s（%s 的结构版本是 %d，这个 TRACE 只认识到 %d）", NewerDataMessage, e.File, e.Found, e.Supported)
}

// IsNewerData reports whether err is, or wraps, a NewerDataError.
func IsNewerData(err error) bool {
	var n *NewerDataError
	return errors.As(err, &n)
}

// ReadSchemaVersion returns the structure version recorded in path, 0 when
// the file records none, and exists=false when there is no file. A JSON
// file that does not parse reports version 0: its own loader decides what
// to do with it.
func ReadSchemaVersion(path string, kind SchemaKind) (version int, exists bool, err error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if info.IsDir() {
		return 0, false, fmt.Errorf("storage: %s is a directory", path)
	}
	switch kind {
	case SchemaJSON:
		raw, err := os.ReadFile(path)
		if err != nil {
			return 0, true, err
		}
		var head struct {
			Version json.RawMessage `json:"version"`
		}
		if json.Unmarshal(raw, &head) != nil || len(head.Version) == 0 {
			return 0, true, nil
		}
		v, convErr := strconv.Atoi(strings.TrimSpace(string(head.Version)))
		if convErr != nil {
			return 0, true, nil
		}
		return v, true, nil
	case SchemaSQLite:
		v, ok, err := sqliteHeaderUserVersion(path)
		if err != nil || !ok {
			return 0, true, err
		}
		return v, true, nil
	}
	return 0, true, fmt.Errorf("storage: unknown schema kind %q", kind)
}

// sqliteHeaderUserVersion reads PRAGMA user_version from the database
// header (bytes 60..63, big-endian) without opening the database, so a
// newer file is not touched. ok is false for a file that is not SQLite.
func sqliteHeaderUserVersion(path string) (int, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false, err
	}
	defer f.Close()
	var hdr [100]byte
	n, err := io.ReadFull(f, hdr[:])
	if err != nil && n < 64 {
		return 0, false, nil
	}
	if string(hdr[:16]) != "SQLite format 3\x00" {
		return 0, false, nil
	}
	return int(int32(binary.BigEndian.Uint32(hdr[60:64]))), true, nil
}

// effectiveVersion maps "no version recorded" to the layout of version 1.
func effectiveVersion(v int) int {
	if v <= 0 {
		return 1
	}
	return v
}

// GuardDataSchemas checks every file of schemas in dataDir before TRACE
// opens any of them: a file newer than this build knows stops the start
// with a NewerDataError and nothing is written; a file older than this
// build writes is copied to <file>.premigrate-<old> first. It returns the
// backups it made.
func GuardDataSchemas(dataDir string, schemas []SchemaFile) ([]string, error) {
	var backups []string
	for _, sf := range schemas {
		path := filepath.Join(dataDir, sf.Name)
		v, exists, err := ReadSchemaVersion(path, sf.Kind)
		if err != nil {
			return backups, err
		}
		if !exists {
			continue
		}
		if v > sf.Current {
			return backups, &NewerDataError{File: sf.Name, Found: v, Supported: sf.Current}
		}
		if effectiveVersion(v) < sf.Current {
			backup, err := BackupBeforeMigration(path, effectiveVersion(v))
			if err != nil {
				return backups, fmt.Errorf("storage: back up %s before migration: %w", sf.Name, err)
			}
			backups = append(backups, backup)
		}
	}
	return backups, nil
}

// BackupBeforeMigration copies path to <path>.premigrate-<fromVersion>,
// with the -wal journal of a database beside it, and removes the earlier
// premigrate copies of the same file, so that only the latest remains.
func BackupBeforeMigration(path string, fromVersion int) (string, error) {
	backup := fmt.Sprintf("%s.premigrate-%d", path, fromVersion)
	tmp := backup + ".tmp"
	if err := copyFileSync(path, tmp); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, backup); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	_ = os.Remove(backup + "-wal")
	if _, err := os.Stat(path + "-wal"); err == nil {
		if err := copyFileSync(path+"-wal", backup+"-wal"); err != nil {
			return "", err
		}
	}
	olds, _ := filepath.Glob(path + ".premigrate-*")
	for _, old := range olds {
		if old == backup || old == backup+"-wal" {
			continue
		}
		_ = os.Remove(old)
	}
	return backup, nil
}

func copyFileSync(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// CheckSQLiteSchema reads PRAGMA user_version of an open database, with
// its journal applied, and refuses a version newer than current before any
// table is created or altered.
func CheckSQLiteSchema(db *sql.DB, name string, current int) (int, error) {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return 0, fmt.Errorf("storage: read user_version of %s: %w", name, err)
	}
	if v > current {
		return v, &NewerDataError{File: name, Found: v, Supported: current}
	}
	return v, nil
}

// StampSQLiteSchema records the structure version once the migrations of a
// database are done.
func StampSQLiteSchema(db *sql.DB, version int) error {
	_, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, version))
	return err
}
