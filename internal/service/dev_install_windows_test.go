//go:build windows

package service

import (
	"fmt"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/windows/registry"
)

func TestReadRegistryDisplayVersionIsolatedKey(t *testing.T) {
	subkey := fmt.Sprintf(`Software\Classes\io.github.chialecode.trace.test-u1-%d-%d`, os.Getpid(), time.Now().UnixNano())
	if got := readRegistryDisplayVersion(registry.CURRENT_USER, subkey); got != "" {
		t.Fatalf("missing key returned %q, want empty", got)
	}

	k, _, err := registry.CreateKey(registry.CURRENT_USER, subkey, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		t.Fatalf("CreateKey isolated test subkey: %v", err)
	}
	defer func() {
		_ = k.Close()
		_ = registry.DeleteKey(registry.CURRENT_USER, subkey)
	}()

	if err := k.SetStringValue("DisplayVersion", "0.1.2-dev.202611151001"); err != nil {
		t.Fatalf("SetStringValue: %v", err)
	}
	if got := readRegistryDisplayVersion(registry.CURRENT_USER, subkey); got != "0.1.2-dev.202611151001" {
		t.Fatalf("readRegistryDisplayVersion = %q, want 0.1.2-dev.202611151001", got)
	}

	if err := k.SetStringValue("DisplayVersion", "https://invalid/version?token=1"); err != nil {
		t.Fatalf("SetStringValue dirty: %v", err)
	}
	if got := readRegistryDisplayVersion(registry.CURRENT_USER, subkey); got != "" {
		t.Fatalf("dirty DisplayVersion = %q, want empty", got)
	}
}
