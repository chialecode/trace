package subscription

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/settings"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/storage"
)

// C32 (scaled): accounts in one round are spaced by the configured interval
// (2s in production); the first account is not delayed.
func TestCheckRoundSpacesAccountsByInterval(t *testing.T) {
	const gap = 150 * time.Millisecond
	var mu sync.Mutex
	var hits []time.Time
	mux := http.NewServeMux()
	mux.HandleFunc("/x/polymer/web-dynamic/v1/feed/space", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, time.Now())
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"has_more": false, "offset": "", "items": []any{}}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	tmp := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	lib, _ := library.Open(db, filepath.Join(tmp, "cache"))
	cred, _, _ := credential.Open(filepath.Join(tmp, "cred.dpapi"))
	_, _ = cred.SaveManual("SESSDATA=s; bili_jct=c", "")
	client := bilibili.NewClient(bilibili.ClientOptions{
		CredStore:      cred,
		Endpoints:      bilibili.Endpoints{APIBase: srv.URL},
		PacingInterval: -1,
		AllowLocalhost: true,
	})
	client.SetWBIKeys("7cd084941338484aae1ad9425b84077c", "4932caff0ff746eab6f01bf08b70ac45")
	mgr, err := NewManager(db, lib, nil, client, settings.Default(filepath.Join(tmp, "cache")).Subscription, nil)
	if err != nil {
		t.Fatal(err)
	}
	mgr.SetAccountInterval(gap)
	for _, mid := range []int64{9101, 9102, 9103} {
		if _, err := mgr.Upsert(Subscription{Mid: mid, Uname: "UP", Enabled: true, Scope: "dynamic"}); err != nil {
			t.Fatal(err)
		}
	}
	started := time.Now()
	sum, err := mgr.CheckNow(context.Background(), 0)
	if err != nil || sum.CheckedCreators != 3 {
		t.Fatalf("round: %+v %v", sum, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(hits) != 3 {
		t.Fatalf("expected one request per account, got %d", len(hits))
	}
	if first := hits[0].Sub(started); first >= gap {
		t.Fatalf("first account was delayed by %v", first)
	}
	for i := 1; i < len(hits); i++ {
		if d := hits[i].Sub(hits[i-1]); d < gap-10*time.Millisecond {
			t.Fatalf("accounts %d and %d only %v apart, want >= %v", i-1, i, d, gap)
		}
	}
}
