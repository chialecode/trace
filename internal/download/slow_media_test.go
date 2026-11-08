package download

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/settings"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/storage"
)

// C17 (scaled): media reads have only an idle timeout, never a total timeout.
// A segment that trickles in far longer than the idle timeout must complete,
// and a stall longer than the idle timeout must resume from the breakpoint.
func TestSlowMediaUsesIdleTimeoutNotTotalTimeout(t *testing.T) {
	const idle = 200 * time.Millisecond
	payload := bytes.Repeat([]byte("trace-slow-media"), 4096) // 64 KiB
	sum := sha256.Sum256(payload)
	want := hex.EncodeToString(sum[:])

	var stallOnce atomic.Bool
	var mu sync.Mutex
	var ranges []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := 0
		if spec := strings.TrimPrefix(r.Header.Get("Range"), "bytes="); spec != "" {
			start, _ = strconv.Atoi(strings.SplitN(spec, "-", 2)[0])
		}
		mu.Lock()
		ranges = append(ranges, r.Header.Get("Range"))
		mu.Unlock()
		body := payload[start:]
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(payload)-1, len(payload)))
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusPartialContent)
		flusher := w.(http.Flusher)
		for off := 0; off < len(body); off += 2048 {
			end := min(off+2048, len(body))
			if _, err := w.Write(body[off:end]); err != nil {
				return
			}
			flusher.Flush()
			if off >= 16*1024 && stallOnce.CompareAndSwap(true, false) {
				// Stall until the client gives up; only its idle timeout can end this quickly.
				select {
				case <-r.Context().Done():
				case <-time.After(10 * time.Second):
				}
				return
			}
			time.Sleep(idle / 4) // trickle: each read stays under the idle timeout
		}
	}))
	t.Cleanup(srv.Close)

	tmp := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cacheDir := filepath.Join(tmp, "cache")
	lib, err := library.Open(db, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	cred, _, err := credential.Open(filepath.Join(tmp, "cred.dpapi"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = cred.SaveManual("SESSDATA=s; bili_jct=c; DedeUserID=1001", "")
	client := bilibili.NewClient(bilibili.ClientOptions{
		CredStore:        cred,
		Endpoints:        bilibili.Endpoints{APIBase: srv.URL},
		PacingInterval:   -1,
		AllowLocalhost:   true,
		MediaIdleTimeout: idle,
	})
	mgr, err := NewManager(db, lib, client, settings.Default(cacheDir).Download, nil)
	if err != nil {
		t.Fatal(err)
	}
	mgr.SetBackoffScale(time.Millisecond)
	mgr.SetChunkSize(int64(len(payload))) // one segment for the whole file

	for _, stall := range []bool{false, true} {
		stallOnce.Store(stall)
		mu.Lock()
		ranges = nil
		mu.Unlock()
		dest := filepath.Join(tmp, fmt.Sprintf("slow-%v.bin", stall))
		task := Task{TaskID: "slow"}
		started := time.Now()
		n, got, err := mgr.downloadFileWithRange(context.Background(), []string{srv.URL + "/media/slow.m4s"}, dest, &task)
		if err != nil {
			t.Fatalf("stall=%v: %v", stall, err)
		}
		if n != int64(len(payload)) || got != want {
			t.Fatalf("stall=%v: size=%d sha=%s", stall, n, got)
		}
		if elapsed := time.Since(started); elapsed < 5*idle || elapsed > 5*time.Second {
			t.Fatalf("stall=%v: elapsed %v outside the trickle/idle-timeout window", stall, elapsed)
		}
		mu.Lock()
		seen := append([]string(nil), ranges...)
		mu.Unlock()
		if !stall && len(seen) != 1 {
			t.Fatalf("trickling segment was retried: %v", seen)
		}
		if stall && (len(seen) < 2 || seen[1] == seen[0] || strings.HasPrefix(seen[1], "bytes=0-")) {
			t.Fatalf("stall did not resume from the breakpoint: %v", seen)
		}
	}
}
