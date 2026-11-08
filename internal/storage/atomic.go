package storage

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// WriteFileAtomic writes data to a temporary sibling file, fsyncs it, and renames it over path.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("storage: mkdir %s: %w", dir, err)
	}

	var rnd [6]byte
	_, _ = rand.Read(rnd[:])
	tmpPath := fmt.Sprintf("%s.tmp-%d-%s", path, os.Getpid(), hex.EncodeToString(rnd[:]))

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

// BackupCorruptFile renames a corrupted file to <path>.corrupt-<timestamp> and returns the backup path.
func BackupCorruptFile(path string) (string, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	ts := time.Now().UTC().Format("20060102-150405.000")
	backupPath := fmt.Sprintf("%s.corrupt-%s", path, ts)
	for i := 1; i < 100; i++ {
		if _, err := os.Stat(backupPath); os.IsNotExist(err) {
			break
		}
		backupPath = fmt.Sprintf("%s.corrupt-%s-%d", path, ts, i)
	}
	if err := os.Rename(path, backupPath); err != nil {
		return "", fmt.Errorf("storage: backup corrupt file %s: %w", path, err)
	}
	return backupPath, nil
}
