package shell

import (
	"testing"

	"github.com/chialecode/trace/internal/appdirs"
)

type fakeUninstallEntries map[string][2]string // identifier -> location, version

func (f fakeUninstallEntries) Read(id string) (string, string, bool, error) {
	e, ok := f[id]
	return e[0], e[1], ok, nil
}

func (f fakeUninstallEntries) SetVersion(id, version string) error {
	e := f[id]
	e[1] = version
	f[id] = e
	return nil
}

// After an update the uninstall entry of this installation names the new
// version; other installations and source builds leave it alone.
func TestSyncUninstallVersion(t *testing.T) {
	const id = "io.github.chialecode.trace"
	entries := fakeUninstallEntries{id: {`"C:\Users\u\AppData\Local\Programs\trace"`, "0.1.1"}}
	if wrote, err := syncUninstallVersion(entries, id, `C:\Users\u\AppData\Local\Programs\trace`, "0.1.2"); err != nil || !wrote || entries[id][1] != "0.1.2" {
		t.Fatalf("update not recorded: %v %v %v", wrote, err, entries[id])
	}
	if wrote, _ := syncUninstallVersion(entries, id, `C:\Users\u\AppData\Local\Programs\trace`, "0.1.2"); wrote {
		t.Fatal("rewrote the same version")
	}
	if wrote, _ := syncUninstallVersion(entries, id, `D:\portable\trace`, "0.1.3"); wrote || entries[id][1] != "0.1.2" {
		t.Fatal("a copy elsewhere changed the installation's entry")
	}
	if wrote, _ := syncUninstallVersion(entries, id, `C:\Users\u\AppData\Local\Programs\trace`, appdirs.UnversionedBuild); wrote {
		t.Fatal("a source build changed the entry")
	}
	if wrote, err := syncUninstallVersion(entries, "io.github.chialecode.trace-dev", `C:\x`, "0.1.2"); wrote || err != nil {
		t.Fatal("a missing entry was written")
	}
}
