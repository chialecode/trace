//go:build windows

package library

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

// A save location written with 8.3 short names (as %TEMP% is on GitHub's
// Windows runners, C:\Users\RUNNER~1\...) is not a link; a junction still
// is.
func TestShortPathNamesAreNotLinksButJunctionsAre(t *testing.T) {
	tmp := t.TempDir()
	long := filepath.Join(tmp, "a long directory name for the short form")
	if err := os.MkdirAll(long, 0o700); err != nil {
		t.Fatal(err)
	}
	in, _ := windows.UTF16PtrFromString(long)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetShortPathName(in, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 {
		t.Skipf("no short path: %v", err)
	}
	short := windows.UTF16ToString(buf[:n])
	if short == long {
		t.Skip("8.3 names are disabled on this volume")
	}
	// No link on the way: the long form is what EvalSymlinks gives, with
	// every short name expanded (t.TempDir may hold some too).
	want, err := filepath.EvalSymlinks(long)
	if err != nil {
		t.Fatal(err)
	}
	if got := longPathName(short); !samePath(got, want) {
		t.Fatalf("longPathName(%q) = %q, want %q", short, got, want)
	}
	if err := rejectDirectoryLinks(short); err != nil {
		t.Fatalf("short path taken for a link: %v", err)
	}
	if !safeRegisteredPath(short, "part-001.mp4") {
		t.Fatal("a registered file under a short path was refused")
	}

	junction := filepath.Join(tmp, "junction")
	cmd := exec.Command("cmd", "/c", "mklink", "/J", junction, long)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot create junction: %v %s", err, out)
	}
	if err := rejectDirectoryLinks(junction); err == nil {
		t.Fatal("a junction was not rejected")
	}
}

func samePath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}
