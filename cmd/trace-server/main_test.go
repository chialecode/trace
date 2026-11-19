package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestCLIScopedFlagsAndExtraArgs(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := run([]string{"run", "--data", t.TempDir(), "--listen", ":9999"}, &out, &errBuf); code != 2 {
		t.Fatalf("run with init-only --listen = %d, want 2 (stderr=%q)", code, errBuf.String())
	}
	errBuf.Reset()
	if code := run([]string{"run", "--data", t.TempDir(), "extra"}, &out, &errBuf); code != 2 {
		t.Fatalf("run with extra arg = %d, want 2 (stderr=%q)", code, errBuf.String())
	}
	errBuf.Reset()
	if code := run([]string{"version", "extra"}, &out, &errBuf); code != 2 {
		t.Fatalf("version with extra arg = %d, want 2 (stderr=%q)", code, errBuf.String())
	}
	out.Reset()
	errBuf.Reset()
	if code := run([]string{"version"}, &out, &errBuf); code != 0 || !strings.Contains(out.String(), "trace-server ") {
		t.Fatalf("version = %d out=%q err=%q", code, out.String(), errBuf.String())
	}
}
