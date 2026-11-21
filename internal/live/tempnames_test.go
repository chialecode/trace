package live

import (
	"io/fs"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/storage"
)

// watchNames samples every name under root while a test runs.
// The returned check fails the test for any name that was seen but is
// neither a temporary name (*.part, .trace-*) nor still present at the end
// as a finished file (allowed lists names the test itself removed).
func watchNames(t *testing.T, root string) func(allowed ...string) {
	t.Helper()
	var mu sync.Mutex
	seen := map[string]bool{}
	stop := make(chan struct{})
	done := make(chan struct{})
	walk := func() map[string]bool {
		out := map[string]bool{}
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				out[p] = true
			}
			return nil
		})
		return out
	}
	go func() {
		defer close(done)
		for {
			names := walk()
			mu.Lock()
			for p := range names {
				seen[p] = true
			}
			mu.Unlock()
			select {
			case <-stop:
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()
	return func(allowed ...string) {
		t.Helper()
		close(stop)
		<-done
		final := walk()
		ok := map[string]bool{}
		for _, a := range allowed {
			ok[a] = true
		}
		var bad []string
		mu.Lock()
		for p := range seen {
			name := filepath.Base(p)
			if final[p] || storage.IsTempName(name) || ok[name] {
				continue
			}
			bad = append(bad, p)
		}
		mu.Unlock()
		sort.Strings(bad)
		if len(bad) > 0 {
			t.Fatalf("unfinished files with final-looking names were visible: %v", bad)
		}
	}
}
