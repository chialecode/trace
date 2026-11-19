package live

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/subscription"
)

// Starting a Douyin recording for an account that records automatically,
// with no fresh status in the cache, reads the status without starting a
// second recording from inside or blocking subsequent status checks and Stop.
func TestDouyinStartWithoutCachedStatusDoesNotLockItself(t *testing.T) {
	const (
		secUID = "MS4wLjABAAAA_start_reentry_auto"
		roomID = "7400000000000009401"
	)
	flv := buildSyntheticFLVBytes(t, "avc", 45)
	mux := http.NewServeMux()
	mux.HandleFunc("/live/hold.flv", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/x-flv")
		_, _ = w.Write(flv)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	})
	mgr, _, subMgr, srv, _ := setupLiveTestHarness(t, mux)
	provider := newFakeDouyinLiveProvider()
	mgr.RegisterSiteProvider(provider)
	provider.statusFn = func(uid, _ string, _ int) (site.LiveStatus, error) {
		return site.LiveStatus{Site: site.Douyin, AccountUID: uid, WebRID: "880021", RoomID: roomID, Status: site.LiveStateLive, Title: "自动录制", LiveSince: 1794024000}, nil
	}
	provider.streamFn = func(uid, _ string, _ []string, _ int) (site.LiveStreamPlan, error) {
		return site.LiveStreamPlan{
			Site: site.Douyin, AccountUID: uid, WebRID: "880021", RoomID: roomID,
			Status: site.LiveStateLive, Transport: site.LiveTransportHTTPFLV, Format: "flv", Codec: "avc", QualityName: "原画",
			URLs: []string{srv.URL + "/live/hold.flv"}, Media: site.DouyinLiveTransport("Mozilla/5.0 Test", true),
		}, nil
	}
	if _, err := subMgr.Create("录直播", "", []subscription.AccountRef{{Site: "douyin", UID: secUID, Name: "抖音主播"}}, subscription.SubRules{AutoRecord: true}); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := mgr.StartDouyinRecording(context.Background(), secUID, "880021")
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("StartDouyinRecording: %v", err)
		}
	case <-time.After(5 * time.Second):
		hung("StartDouyinRecording hung: it locked startMu twice")
	}
	if st, ok := mgr.CachedAccountState(site.Douyin, secUID); !ok || !st.Recording {
		t.Fatalf("account state after the start = %+v %v, want recording", st, ok)
	}
	provider.mu.Lock()
	streams := 0
	for _, n := range provider.streamCalls {
		streams += n
	}
	provider.mu.Unlock()
	if streams != 1 {
		t.Fatalf("stream plan requested %d times, want one recording", streams)
	}

	// A later check of the same account and quitting both go through.
	checked := make(chan struct{})
	go func() {
		_, _ = mgr.CheckAccountLiveStatus(context.Background(), site.Douyin, secUID, "", true)
		mgr.Stop()
		close(checked)
	}()
	select {
	case <-checked:
	case <-time.After(10 * time.Second):
		hung("a status check or Stop after the start hung")
	}
}

// hung ends the test binary at once. t.Fatal (or a panic in the test)
// would run the cleanup, which stops the manager and hangs on the same lock;
// a panic in another goroutine skips it.
func hung(msg string) {
	go panic(msg)
	select {}
}
