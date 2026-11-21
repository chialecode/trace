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
	"sync"
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
	{Name: "ui-state.json", Kind: SchemaJSON, Current: 3},
	{Name: "history.json", Kind: SchemaJSON, Current: 1},
	{Name: "subscriptions.json", Kind: SchemaJSON, Current: 3},
	{Name: "library.db", Kind: SchemaSQLite, Current: 2},
	{Name: "updates.db", Kind: SchemaSQLite, Current: 2},
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
	v, ex, _, err := readSchemaFileState(path, kind)
	return v, ex, err
}

func readSchemaFileState(path string, kind SchemaKind) (version int, exists bool, valid bool, err error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, false, false, nil
	}
	if err != nil {
		return 0, false, false, err
	}
	if info.IsDir() {
		return 0, false, false, fmt.Errorf("storage: %s is a directory", path)
	}
	switch kind {
	case SchemaJSON:
		raw, err := os.ReadFile(path)
		if err != nil {
			return 0, true, false, err
		}
		var head struct {
			Version json.RawMessage `json:"version"`
		}
		if json.Unmarshal(raw, &head) != nil {
			return 0, true, false, nil
		}
		if len(head.Version) == 0 {
			return 0, true, true, nil
		}
		v, convErr := strconv.Atoi(strings.TrimSpace(string(head.Version)))
		if convErr != nil {
			return 0, true, false, nil
		}
		return v, true, true, nil
	case SchemaSQLite:
		v, ok, err := sqliteHeaderUserVersion(path)
		if err != nil || !ok {
			return 0, true, false, err
		}
		return v, true, true, nil
	}
	return 0, true, false, fmt.Errorf("storage: unknown schema kind %q", kind)
}

// sqliteHeaderUserVersion reads PRAGMA user_version from the database
// header (bytes 60..63, big-endian) and any committed page-1 frame in its
// -wal sidecar without opening or modifying the database, so a newer file
// is not touched. ok is false for a file that is not SQLite.
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
	v := int(int32(binary.BigEndian.Uint32(hdr[60:64])))
	if walVer, ok := sqliteWALUserVersion(path + "-wal"); ok {
		v = walVer
	}
	return v, true, nil
}

// sqliteWALUserVersion inspects committed frames in a SQLite WAL file for
// page 1 and returns its latest committed user_version without mutating any
// file. Malformed or synthetic WAL files are ignored (ok=false).
func sqliteWALUserVersion(walPath string) (int, bool) {
	f, err := os.Open(walPath)
	if err != nil {
		return 0, false
	}
	defer f.Close()

	var walHdr [32]byte
	if _, err := io.ReadFull(f, walHdr[:]); err != nil {
		return 0, false
	}
	magic := binary.BigEndian.Uint32(walHdr[0:4])
	var bigEndian bool
	switch magic {
	case 0x377f0682:
		bigEndian = false
	case 0x377f0683:
		bigEndian = true
	default:
		return 0, false
	}
	pageSize := binary.BigEndian.Uint32(walHdr[8:12])
	if pageSize < 512 || pageSize > 65536 || (pageSize&(pageSize-1)) != 0 {
		return 0, false
	}
	salt1 := binary.BigEndian.Uint32(walHdr[16:20])
	salt2 := binary.BigEndian.Uint32(walHdr[20:24])
	wantCksum1 := binary.BigEndian.Uint32(walHdr[24:28])
	wantCksum2 := binary.BigEndian.Uint32(walHdr[28:32])
	s0, s1 := sqliteWALChecksum(walHdr[:24], 0, 0, bigEndian)
	if s0 != wantCksum1 || s1 != wantCksum2 {
		return 0, false
	}

	frameHdr := make([]byte, 24)
	pageBuf := make([]byte, pageSize)
	var (
		committedVer  int
		hasCommitted  bool
		pendingVer    int
		hasPendingVer bool
	)
	for {
		if _, err := io.ReadFull(f, frameHdr); err != nil {
			break
		}
		if _, err := io.ReadFull(f, pageBuf); err != nil {
			break
		}
		if binary.BigEndian.Uint32(frameHdr[8:12]) != salt1 || binary.BigEndian.Uint32(frameHdr[12:16]) != salt2 {
			break
		}
		s0, s1 = sqliteWALChecksum(frameHdr[:8], s0, s1, bigEndian)
		s0, s1 = sqliteWALChecksum(pageBuf, s0, s1, bigEndian)
		if s0 != binary.BigEndian.Uint32(frameHdr[16:20]) || s1 != binary.BigEndian.Uint32(frameHdr[20:24]) {
			break
		}
		pageNum := binary.BigEndian.Uint32(frameHdr[0:4])
		dbSize := binary.BigEndian.Uint32(frameHdr[4:8])
		if pageNum == 1 && len(pageBuf) >= 64 && string(pageBuf[:16]) == "SQLite format 3\x00" {
			pendingVer = int(int32(binary.BigEndian.Uint32(pageBuf[60:64])))
			hasPendingVer = true
		}
		if dbSize > 0 && hasPendingVer {
			committedVer = pendingVer
			hasCommitted = true
			hasPendingVer = false
		}
	}
	return committedVer, hasCommitted
}

func sqliteWALChecksum(data []byte, s0, s1 uint32, bigEndian bool) (uint32, uint32) {
	for i := 0; i+8 <= len(data); i += 8 {
		var x0, x1 uint32
		if bigEndian {
			x0 = binary.BigEndian.Uint32(data[i : i+4])
			x1 = binary.BigEndian.Uint32(data[i+4 : i+8])
		} else {
			x0 = binary.LittleEndian.Uint32(data[i : i+4])
			x1 = binary.LittleEndian.Uint32(data[i+4 : i+8])
		}
		s0 += x0 + s1
		s1 += x1 + s0
	}
	return s0, s1
}

// effectiveVersion maps "no version recorded" to the layout of version 1.
func effectiveVersion(v int) int {
	if v <= 0 {
		return 1
	}
	return v
}

var (
	schemaHooksMu             sync.Mutex
	afterBackupHook           func(fileName, backupPath string) error
	beforeMigrationRenameHook func(targetPath, tmpPath string) error
)

// SetAfterBackupHookForTest installs a fault-injection callback invoked right
// after GuardDataSchemas finishes creating a .premigrate-<v> backup.
func SetAfterBackupHookForTest(fn func(fileName, backupPath string) error) func() {
	schemaHooksMu.Lock()
	prev := afterBackupHook
	afterBackupHook = fn
	schemaHooksMu.Unlock()
	return func() {
		schemaHooksMu.Lock()
		afterBackupHook = prev
		schemaHooksMu.Unlock()
	}
}

// SetBeforeMigrationRenameHookForTest installs a fault-injection callback
// invoked after a migrated JSON temp file is synced and closed, right before
// os.Rename replaces the target file.
func SetBeforeMigrationRenameHookForTest(fn func(targetPath, tmpPath string) error) func() {
	schemaHooksMu.Lock()
	prev := beforeMigrationRenameHook
	beforeMigrationRenameHook = fn
	schemaHooksMu.Unlock()
	return func() {
		schemaHooksMu.Lock()
		beforeMigrationRenameHook = prev
		schemaHooksMu.Unlock()
	}
}

// GuardDataSchemas checks every file of schemas in dataDir before TRACE
// opens any of them: a file newer than this build knows stops the start
// with a NewerDataError and nothing is written; a file older than this
// build writes is copied to <file>.premigrate-<old> first. It returns the
// backups it made.
func GuardDataSchemas(dataDir string, schemas []SchemaFile) ([]string, error) {
	// First verify no file is newer than supported so nothing is written on NewerDataError.
	for _, sf := range schemas {
		path := filepath.Join(dataDir, sf.Name)
		v, exists, err := ReadSchemaVersion(path, sf.Kind)
		if err != nil {
			return nil, err
		}
		if exists && v > sf.Current {
			return nil, &NewerDataError{File: sf.Name, Found: v, Supported: sf.Current}
		}
	}

	var backups []string
	for _, sf := range schemas {
		path := filepath.Join(dataDir, sf.Name)
		v, exists, valid, err := readSchemaFileState(path, sf.Kind)
		if err != nil {
			return backups, err
		}
		if !exists || !valid {
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
			schemaHooksMu.Lock()
			hook := afterBackupHook
			schemaHooksMu.Unlock()
			if hook != nil {
				if err := hook(sf.Name, backup); err != nil {
					return backups, err
				}
			}
		}
	}
	return backups, nil
}

// BackupBeforeMigration copies path to <path>.premigrate-<fromVersion>,
// with any -wal and -shm sidecar files of a SQLite database beside it, and
// removes the earlier premigrate copies of the same file so that only the
// latest remains.
func BackupBeforeMigration(path string, fromVersion int) (string, error) {
	if info, err := os.Stat(backupTargetCheck(path, fromVersion)); err == nil && info.IsDir() {
		return "", fmt.Errorf("storage: backup destination %s is a directory", backupTargetCheck(path, fromVersion))
	}
	backup := fmt.Sprintf("%s.premigrate-%d", path, fromVersion)
	tmp := TempSiblingPath(backup)
	if err := copyFileSync(path, tmp); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, backup); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		sideSrc := path + suffix
		sideDst := backup + suffix
		sideInfo, statErr := os.Stat(sideSrc)
		if errors.Is(statErr, os.ErrNotExist) {
			_ = os.Remove(sideDst)
			continue
		}
		if statErr != nil {
			return "", statErr
		}
		if sideInfo.IsDir() {
			return "", fmt.Errorf("storage: sidecar %s is a directory", sideSrc)
		}
		sideTmp := TempSiblingPath(sideDst)
		if err := copyFileSync(sideSrc, sideTmp); err != nil {
			_ = os.Remove(sideTmp)
			return "", err
		}
		if err := os.Rename(sideTmp, sideDst); err != nil {
			_ = os.Remove(sideTmp)
			return "", err
		}
	}
	olds, _ := filepath.Glob(path + ".premigrate-*")
	for _, old := range olds {
		if old == backup || old == backup+"-wal" || old == backup+"-shm" {
			continue
		}
		_ = os.Remove(old)
	}
	return backup, nil
}

func backupTargetCheck(path string, fromVersion int) string {
	return fmt.Sprintf("%s.premigrate-%d", path, fromVersion)
}

// WriteMigratedJSONAtomic writes a migrated JSON file to a sibling temp file,
// fsyncs and closes it, runs any registered pre-rename fault hook, and
// atomically renames it over path.
func WriteMigratedJSONAtomic(path string, v any, perm os.FileMode) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("storage: marshal json for %s: %w", path, err)
	}
	data = append(data, '\n')

	schemaHooksMu.Lock()
	hook := beforeMigrationRenameHook
	schemaHooksMu.Unlock()
	return writeFileAtomicWithHook(path, data, perm, hook)
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
// database are done and checkpoints WAL so the main database header
// immediately reflects the new user_version.
func StampSQLiteSchema(db *sql.DB, version int) error {
	if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, version)); err != nil {
		return err
	}
	var busy, logFrames, checkpointed int
	if err := db.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointed); err != nil {
		return fmt.Errorf("storage: checkpoint user_version=%d: %w", version, err)
	}
	if busy != 0 {
		return fmt.Errorf("storage: wal_checkpoint busy=%d after stamping user_version=%d", busy, version)
	}
	return nil
}
