package main

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteTarGzLayoutModesAndLineEndings(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.WriteFile(bin, []byte("ELF"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "p.tar.gz")
	files := []tarFile{
		{name: "trace-server", path: bin, mode: 0o755},
		{name: "README.md", data: []byte("a\r\nb\r\n"), mode: 0o644},
	}
	if err := writeTarGz(out, "trace-server-1.0.0-linux-amd64", files, time.Unix(1700000000, 0)); err != nil {
		t.Fatal(err)
	}
	if err := writeTarGz(out, "x", files, time.Now()); err == nil {
		t.Fatal("an existing archive should not be overwritten")
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	got := map[string]*tar.Header{}
	body := map[string]string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		got[h.Name], body[h.Name] = h, string(b)
	}
	if h := got["trace-server-1.0.0-linux-amd64/"]; h == nil || h.Typeflag != tar.TypeDir {
		t.Fatalf("top folder missing: %v", got)
	}
	if h := got["trace-server-1.0.0-linux-amd64/trace-server"]; h == nil || h.Mode != 0o755 || body[h.Name] != "ELF" {
		t.Fatalf("program entry = %+v", h)
	}
	if b := body["trace-server-1.0.0-linux-amd64/README.md"]; b != "a\nb\n" {
		t.Fatalf("README line endings = %q", b)
	}
}
