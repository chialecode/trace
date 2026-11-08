//go:build !windows

package appdirs

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
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

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, ErrAlreadyLocked
	}

	return &Lock{
		path: lockPath,
		file: f,
	}, nil
}

// Path returns the locked file path.
func (l *Lock) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Release unlocks and closes the lock file.
func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	err := l.file.Close()
	l.file = nil
	return err
}
