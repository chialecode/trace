package build

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func writeTestBundle(t *testing.T, dir, version string) (map[string][]byte, []string) {
	t.Helper()
	files := map[string][]byte{
		"trace.exe":   []byte("synthetic executable " + version),
		"a-setup.exe": []byte("synthetic installer " + version),
		"VERSION.txt": []byte("synthetic version " + version),
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var names []string
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var sums strings.Builder
	for _, name := range names {
		fmt.Fprintf(&sums, "%x  %s\n", sha256.Sum256(files[name]), name)
	}
	files["SHA256SUMS.txt"] = []byte(sums.String())
	names = append(names, "SHA256SUMS.txt")
	for name, raw := range files {
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return files, names
}

func assertTestBundle(t *testing.T, dir string, want map[string][]byte) {
	t.Helper()
	for name, expected := range want {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(raw) != string(expected) {
			t.Fatalf("bundle file %s changed or disappeared: err=%v", name, err)
		}
	}
}

func TestBundleReplacementKeepsUserFilesAndRemovesObsoleteRegisteredFiles(t *testing.T) {
	root := t.TempDir()
	dest, src := filepath.Join(root, "dest"), filepath.Join(root, "src")
	_, _ = writeTestBundle(t, dest, "old")
	want, names := writeTestBundle(t, src, "new")
	if err := os.WriteFile(filepath.Join(dest, "my-note.txt"), []byte("user file"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldSums, err := os.ReadFile(filepath.Join(dest, "SHA256SUMS.txt"))
	if err != nil {
		t.Fatal(err)
	}
	obsolete := []byte("old registered installer")
	if err := os.WriteFile(filepath.Join(dest, "old-setup.exe"), obsolete, 0o600); err != nil {
		t.Fatal(err)
	}
	oldSums = append(oldSums, []byte(fmt.Sprintf("%x  old-setup.exe\n", sha256.Sum256(obsolete)))...)
	if err := os.WriteFile(filepath.Join(dest, "SHA256SUMS.txt"), oldSums, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PublishBundle(src, dest, names, true); err != nil {
		t.Fatal(err)
	}
	want["my-note.txt"] = []byte("user file")
	assertTestBundle(t, dest, want)
	if _, err := os.Stat(filepath.Join(dest, "old-setup.exe")); !os.IsNotExist(err) {
		t.Fatalf("obsolete registered artifact was retained: %v", err)
	}
}

func TestBundleInvalidInputAndManifestLeaveOldPackageIntact(t *testing.T) {
	for _, fault := range []string{"missing source", "unsafe manifest", "unregistered collision"} {
		t.Run(fault, func(t *testing.T) {
			root := t.TempDir()
			dest, src := filepath.Join(root, "dest"), filepath.Join(root, "src")
			want, _ := writeTestBundle(t, dest, "old")
			_, names := writeTestBundle(t, src, "new")
			switch fault {
			case "missing source":
				if err := os.Remove(filepath.Join(src, "trace.exe")); err != nil {
					t.Fatal(err)
				}
			case "unsafe manifest":
				want["SHA256SUMS.txt"] = []byte(fmt.Sprintf("%x  ../outside.txt\n", sha256.Sum256(nil)))
				if err := os.WriteFile(filepath.Join(dest, "SHA256SUMS.txt"), want["SHA256SUMS.txt"], 0o600); err != nil {
					t.Fatal(err)
				}
			case "unregistered collision":
				want["user.exe"] = []byte("user file")
				for _, dir := range []string{src, dest} {
					if err := os.WriteFile(filepath.Join(dir, "user.exe"), want["user.exe"], 0o600); err != nil {
						t.Fatal(err)
					}
				}
				names = append(names, "user.exe")
			}
			if err := PublishBundle(src, dest, names, true); err == nil {
				t.Fatal("invalid input unexpectedly replaced the package")
			}
			assertTestBundle(t, dest, want)
		})
	}
}

func TestBundleMetadataRefreshPreservesRegisteredBinaries(t *testing.T) {
	root := t.TempDir()
	dest, src := filepath.Join(root, "dest"), filepath.Join(root, "src")
	want, _ := writeTestBundle(t, dest, "old")
	_, _ = writeTestBundle(t, src, "new")
	want["VERSION.txt"] = []byte("synthetic version new")
	var sums strings.Builder
	for _, name := range []string{"VERSION.txt", "a-setup.exe", "trace.exe"} {
		fmt.Fprintf(&sums, "%x  %s\n", sha256.Sum256(want[name]), name)
	}
	want["SHA256SUMS.txt"] = []byte(sums.String())
	if err := os.WriteFile(filepath.Join(src, "SHA256SUMS.txt"), want["SHA256SUMS.txt"], 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PublishBundle(src, dest, []string{"VERSION.txt", "SHA256SUMS.txt"}, false); err != nil {
		t.Fatal(err)
	}
	assertTestBundle(t, dest, want)
}
