//go:build !windows

package credential

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// On Linux and other systems there is no DPAPI. Credentials are sealed with
// AES-256-GCM under a random 32-byte key kept next to them in KeyFileName
// (0600), created the first time one is written. This only stops a
// credential file copied away on its own; whoever can read the key file (the
// same user or root) can read the credentials (docs/design/remote.md §5).

var cipherHeader = []byte("TRACEK1")

var keyMu sync.Mutex

// errNoKey means the key file is gone: what it sealed cannot be read and is
// treated as no credential.
var errNoKey = errors.New("credential: key file missing")

// EnsureKeyFile creates the key file of dir when it is missing.
func EnsureKeyFile(dir string) error {
	key, err := loadKey(dir, true)
	clear(key)
	return err
}

func readExistingKeyFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("credential: key file %s must not be a symlink", KeyFileName)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("credential: key file %s is not a regular file", KeyFileName)
	}
	if info.Mode().Perm() != 0o600 {
		if err := os.Chmod(path, 0o600); err != nil {
			return nil, fmt.Errorf("credential: chmod key file: %w", err)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw) != 32 {
		clear(raw)
		return nil, fmt.Errorf("credential: key file has the wrong size")
	}
	return raw, nil
}

func loadKey(dir string, create bool) ([]byte, error) {
	keyMu.Lock()
	defer keyMu.Unlock()
	path := filepath.Join(dir, KeyFileName)
	raw, err := readExistingKeyFile(path)
	if err == nil {
		return raw, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if !create {
		return nil, errNoKey
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		clear(key)
		return nil, err
	}
	tmp, err := os.CreateTemp(dir, ".trace-key-*.tmp")
	if err != nil {
		clear(key)
		return nil, err
	}
	tmpPath := tmp.Name()
	cleanupTmp := func() { _ = os.Remove(tmpPath) }
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		cleanupTmp()
		clear(key)
		return nil, err
	}
	if _, err := tmp.Write(key); err != nil {
		_ = tmp.Close()
		cleanupTmp()
		clear(key)
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanupTmp()
		clear(key)
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		cleanupTmp()
		clear(key)
		return nil, err
	}
	linkErr := os.Link(tmpPath, path)
	cleanupTmp()
	if linkErr != nil {
		clear(key)
		if errors.Is(linkErr, os.ErrExist) {
			return readExistingKeyFile(path)
		}
		return nil, linkErr
	}
	return key, nil
}

// protectFor seals plain for the file at path; entropy binds it to its site
// (sealed data of one site never opens as another's).
func protectFor(path string, plain, entropy []byte) ([]byte, error) {
	key, err := loadKey(filepath.Dir(path), true)
	if err != nil {
		return nil, err
	}
	defer clear(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	out := append(append([]byte{}, cipherHeader...), nonce...)
	return gcm.Seal(out, nonce, plain, entropy), nil
}

func unprotectFor(path string, data, entropy []byte) ([]byte, error) {
	if !bytes.HasPrefix(data, cipherHeader) {
		return nil, fmt.Errorf("credential: invalid cipher header")
	}
	key, err := loadKey(filepath.Dir(path), false)
	if err != nil {
		return nil, err
	}
	defer clear(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	raw := data[len(cipherHeader):]
	if len(raw) < gcm.NonceSize() {
		return nil, fmt.Errorf("credential: cipher too short")
	}
	return gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], entropy)
}
