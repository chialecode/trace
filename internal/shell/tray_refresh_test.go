package shell

import (
	"sync"
	"testing"
	"time"
)

// A backend event published while its sender holds a lock must not wait for
// the tray: the tray waits for the main thread, which may be waiting for that
// lock (the cache queue and the author page's "⋯" menu deadlocked this way).
func TestTrayRefreshDoesNotBlockThePublisher(t *testing.T) {
	var queueMu sync.Mutex // stands for the cache queue's lock
	mainThread := make(chan struct{})
	refreshed := make(chan struct{}, 4)
	s := &Shell{}
	s.trayRefresh = func() {
		<-mainThread // the tray call waits for the main thread
		refreshed <- struct{}{}
	}

	published := make(chan struct{})
	go func() {
		queueMu.Lock()
		s.scheduleTrayRefresh() // notifyStateLocked → event → shell
		queueMu.Unlock()
		close(published)
	}()
	select {
	case <-published:
	case <-time.After(2 * time.Second):
		t.Fatal("publishing an event waited for the tray refresh")
	}
	// The main thread can take the lock again, then serves the tray.
	queueMu.Lock()
	queueMu.Unlock()
	close(mainThread)
	select {
	case <-refreshed:
	case <-time.After(2 * time.Second):
		t.Fatal("tray was not refreshed")
	}
}

func TestTrayRefreshCoalescesBursts(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 8)
	s := &Shell{}
	s.trayRefresh = func() {
		started <- struct{}{}
		<-release
	}
	s.scheduleTrayRefresh()
	<-started
	// While one refresh runs, a burst of events schedules at most one more.
	for range 5 {
		s.scheduleTrayRefresh()
	}
	close(release)
	deadline := time.After(2 * time.Second)
	count := 1
	for count < 2 {
		select {
		case <-started:
			count++
		case <-deadline:
			t.Fatalf("expected a follow-up refresh, got %d", count)
		}
	}
	select {
	case <-started:
		t.Fatal("a burst of events refreshed the tray more than once")
	case <-time.After(100 * time.Millisecond):
	}
}
