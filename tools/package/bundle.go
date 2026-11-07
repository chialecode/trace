package main

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func validBundleName(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name &&
		!strings.ContainsAny(name, "/\\: \t\r\n")
}

// registeredBundleFiles only trusts plain filenames in the previous manifest.
// A missing manifest represents an empty bundle; collisions remain user files.
func registeredBundleFiles(destDir string) (map[string]bool, error) {
	raw, err := os.ReadFile(filepath.Join(destDir, "SHA256SUMS.txt"))
	if os.IsNotExist(err) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, err
	}
	registered := map[string]bool{"SHA256SUMS.txt": true}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) != 64 || !validBundleName(fields[1]) || registered[fields[1]] {
			return nil, fmt.Errorf("invalid previous SHA256SUMS.txt entry")
		}
		if _, err := hex.DecodeString(fields[0]); err != nil {
			return nil, fmt.Errorf("invalid previous SHA256SUMS.txt hash")
		}
		registered[fields[1]] = true
	}
	if len(registered) == 1 {
		return nil, fmt.Errorf("empty previous SHA256SUMS.txt")
	}
	return registered, nil
}

// publishBundle stages every incoming file, then backs up registered originals
// before replacing them. A failed rename restores all originals, including the
// checksum manifest. Recovery backups live beside the bundle, outside build
// staging, so a failed rollback cannot discard the only copy of an old file.
func publishBundle(srcDir, destDir string, names []string, pruneOld bool) error {
	destDir, err := filepath.Abs(destDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}
	if info, err := os.Lstat(destDir); err != nil || !info.IsDir() || info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return fmt.Errorf("invalid package destination")
	}
	registered, err := registeredBundleFiles(destDir)
	if err != nil {
		return err
	}
	replacing := make(map[string]bool, len(names))
	for _, name := range names {
		if !validBundleName(name) || replacing[name] {
			return fmt.Errorf("invalid or repeated package filename %q", name)
		}
		replacing[name] = true
		if info, err := os.Lstat(filepath.Join(srcDir, name)); err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("missing or non-regular package input %s", name)
		}
		if _, err := os.Lstat(filepath.Join(destDir, name)); err == nil && !registered[name] {
			return fmt.Errorf("refusing to replace unregistered package file %s", name)
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	var oldNames []string
	for name := range registered {
		if !pruneOld && !replacing[name] {
			continue
		}
		info, err := os.Lstat(filepath.Join(destDir, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("unavailable or non-regular registered package file %s", name)
		}
		oldNames = append(oldNames, name)
	}
	sort.Strings(oldNames)
	names = append([]string(nil), names...)
	sort.Strings(names)
	parent := filepath.Dir(destDir)
	stagedDir, err := os.MkdirTemp(parent, ".publish-"+filepath.Base(destDir)+"-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stagedDir)
	for _, name := range names {
		if err := copyFile(filepath.Join(srcDir, name), filepath.Join(stagedDir, name)); err != nil {
			return fmt.Errorf("stage %s: %w", name, err)
		}
	}
	backupDir, err := os.MkdirTemp(parent, ".backup-"+filepath.Base(destDir)+"-")
	if err != nil {
		return err
	}
	keepBackup := false
	defer func() {
		if !keepBackup {
			_ = os.RemoveAll(backupDir)
		}
	}()
	var backedUp, published []string
	rollback := func(cause error) error {
		var recoveryErr error
		for i := len(published) - 1; i >= 0; i-- {
			if err := os.Remove(filepath.Join(destDir, published[i])); err != nil && !os.IsNotExist(err) {
				recoveryErr = errors.Join(recoveryErr, err)
			}
		}
		for i := len(backedUp) - 1; i >= 0; i-- {
			name := backedUp[i]
			if _, err := os.Lstat(filepath.Join(destDir, name)); err == nil {
				recoveryErr = errors.Join(recoveryErr, fmt.Errorf("cannot restore occupied file %s", name))
				continue
			} else if !os.IsNotExist(err) {
				recoveryErr = errors.Join(recoveryErr, err)
				continue
			}
			if err := os.Rename(filepath.Join(backupDir, name), filepath.Join(destDir, name)); err != nil {
				recoveryErr = errors.Join(recoveryErr, err)
			}
		}
		if recoveryErr != nil {
			keepBackup = true
			return errors.Join(cause, fmt.Errorf("rollback incomplete; originals retained in %s: %w", backupDir, recoveryErr))
		}
		return fmt.Errorf("previous package restored: %w", cause)
	}
	for _, name := range oldNames {
		if err := os.Rename(filepath.Join(destDir, name), filepath.Join(backupDir, name)); err != nil {
			return rollback(fmt.Errorf("back up %s: %w", name, err))
		}
		backedUp = append(backedUp, name)
	}
	for _, name := range names {
		if err := os.Rename(filepath.Join(stagedDir, name), filepath.Join(destDir, name)); err != nil {
			return rollback(fmt.Errorf("publish %s: %w", name, err))
		}
		published = append(published, name)
	}
	if err := os.RemoveAll(backupDir); err != nil {
		keepBackup = true
		fmt.Fprintf(os.Stderr, "package published; previous files retained in %s: %v\n", backupDir, err)
	}
	return nil
}
