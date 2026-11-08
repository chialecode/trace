//go:build windows

package appdirs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// Lock represents an exclusive OS-level file lock on app.lock.
type Lock struct {
	path string
	file *os.File
}

// LockDataDir acquires a non-blocking exclusive lock on <dataDir>/app.lock.
func LockDataDir(dataDir string) (*Lock, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("appdirs: create data dir: %w", err)
	}
	lockPath := filepath.Join(dataDir, "app.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("appdirs: open lock file: %w", err)
	}

	var ol windows.Overlapped
	err = windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1,
		0,
		&ol,
	)
	if err != nil {
		_ = f.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
			return nil, ErrAlreadyLocked
		}
		return nil, fmt.Errorf("%w: %v", ErrAlreadyLocked, err)
	}

	return &Lock{
		path: lockPath,
		file: f,
	}, nil
}

// Release unlocks and closes the lock file.
func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	var ol windows.Overlapped
	_ = windows.UnlockFileEx(windows.Handle(l.file.Fd()), 0, 1, 0, &ol)
	err := l.file.Close()
	l.file = nil
	return err
}
