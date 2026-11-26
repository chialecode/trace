package main

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site/bilibili"
)

func TestSeedSyntheticData(t *testing.T) {
	tmpRoot := t.TempDir()
	dataDir := filepath.Join(tmpRoot, "data")
	cacheDir := filepath.Join(tmpRoot, "cache")
	for _, d := range []string{dataDir, cacheDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	loc := appdirs.Locations{
		Channel:         "dev",
		AppName:         "trace-dev",
		DisplayName:     "TRACE Dev",
		Identifier:      "io.github.chialecode.trace-dev",
		DataDir:         dataDir,
		DefaultCacheDir: cacheDir,
		CredentialsDir:  filepath.Join(dataDir, "credentials"),
		LogsDir:         filepath.Join(dataDir, "logs"),
		TempDir:         filepath.Join(tmpRoot, "tmp"),
		LockFile:        filepath.Join(dataDir, "app.lock"),
	}
	backend, err := service.NewBackend(service.Options{
		Locations:        loc,
		DisableBgWorkers: true,
		NodeName:         "DevTestPC",
		BilibiliOpts: bilibili.ClientOptions{
			WrapTransport: func(http.RoundTripper) http.RoundTripper {
				return syntheticAuthRoundTripper{}
			},
		},
	})
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	defer backend.Close()

	if err := seedSyntheticData(backend, dataDir, cacheDir); err != nil {
		t.Fatalf("seedSyntheticData: %v", err)
	}
	subs, err := backend.ListSubscriptions()
	if err != nil || len(subs) != 2 {
		t.Fatalf("ListSubscriptions = %d, err=%v; want 2", len(subs), err)
	}
	items, err := backend.ListLibraryItems(library.FeedFilter{Limit: 20})
	if err != nil || len(items) != 3 {
		t.Fatalf("ListLibraryItems = %d, err=%v; want 3", len(items), err)
	}
	if mp4 := makeSyntheticMP4Bytes(); len(mp4) < 1024 {
		t.Fatalf("synthetic MP4 too small: %d", len(mp4))
	}
}
