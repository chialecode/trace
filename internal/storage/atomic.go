package storage

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TempPrefix starts the name of every temporary file TRACE writes next to a
// final file (atomic writes, merges, staged images); files still being
// downloaded or recorded end in TempSuffix instead. Anything that copies a
// library or export folder (an upload, an import) skips both, so it never
// takes a half-written file (docs/design/remote.md §6.1).
const (
	TempPrefix = ".trace-"
	TempSuffix = ".part"
)

// IsTempName reports whether a file name is one of TRACE's unfinished files.
func IsTempName(name string) bool {
	return strings.HasPrefix(name, TempPrefix) || strings.HasSuffix(strings.ToLower(name), TempSuffix)
}

// TempSiblingPath is the temporary name used to write path atomically:
// ".trace-<name>.<pid>-<random>.tmp" in the same folder.
func TempSiblingPath(path string) string {
	var rnd [6]byte
	_, _ = rand.Read(rnd[:])
	return filepath.Join(filepath.Dir(path), fmt.Sprintf("%s%s.%d-%s.tmp", TempPrefix, filepath.Base(path), os.Getpid(), hex.EncodeToString(rnd[:])))
}

// WriteFileAtomic writes data to a temporary sibling file, fsyncs it, and renames it over path.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	return writeFileAtomicWithHook(path, data, perm, nil)
}

func writeFileAtomicWithHook(path string, data []byte, perm os.FileMode, beforeRename func(targetPath, tmpPath string) error) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("storage: mkdir %s: %w", dir, err)
	}

	tmpPath := TempSiblingPath(path)

	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("storage: create temp file %s: %w", tmpPath, err)
	}

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("storage: write temp file %s: %w", tmpPath, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("storage: sync temp file %s: %w", tmpPath, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("storage: close temp file %s: %w", tmpPath, err)
	}

	if beforeRename != nil {
		if err := beforeRename(path, tmpPath); err != nil {
			_ = os.Remove(tmpPath)
			return err
		}
	}

	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("storage: rename %s -> %s: %w", tmpPath, path, err)
	}
	return nil
}

// WriteJSONAtomic marshals v with indentation and writes it atomically to path.
func WriteJSONAtomic(path string, v any, perm os.FileMode) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("storage: marshal json for %s: %w", path, err)
	}
	data = append(data, '\n')
	return WriteFileAtomic(path, data, perm)
}

func nextCorruptSuffix(path string) string {
	ts := time.Now().UTC().Format("20060102-150405.000")
	suffix := ".corrupt-" + ts
	for i := 1; i < 100; i++ {
		if _, err := os.Stat(path + suffix); os.IsNotExist(err) {
			return suffix
		}
		suffix = fmt.Sprintf(".corrupt-%s-%d", ts, i)
	}
	return suffix
}

func backupCorruptWithSuffix(path, suffix string) (string, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	backupPath := path + suffix
	if err := os.Rename(path, backupPath); err != nil {
		return "", fmt.Errorf("storage: backup corrupt file %s: %w", path, err)
	}
	return backupPath, nil
}

// BackupCorruptFile renames a corrupted file to <path>.corrupt-<timestamp> and returns the backup path.
func BackupCorruptFile(path string) (string, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return backupCorruptWithSuffix(path, nextCorruptSuffix(path))
}
