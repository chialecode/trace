package service

import "testing"

// A running task reports progress about once a second; only status changes
// reach the log (2026-11-15: one task wrote 115 identical "-> running" lines).
func TestTaskStatusLogOnlyLogsChanges(t *testing.T) {
	l := newTaskStatusLog()
	var logged []string
	for _, st := range []string{"queued", "running", "running", "running", "paused", "running", "running", "completed"} {
		if l.changed("dl-1", st) {
			logged = append(logged, st)
		}
	}
	want := []string{"queued", "running", "paused", "running", "completed"}
	if len(logged) != len(want) {
		t.Fatalf("logged %v, want %v", logged, want)
	}
	for i := range want {
		if logged[i] != want[i] {
			t.Fatalf("logged %v, want %v", logged, want)
		}
	}
	if len(l.last) != 0 {
		t.Fatalf("finished task still remembered: %v", l.last)
	}
	if !l.changed("dl-2", "running") || l.changed("dl-2", "running") {
		t.Fatal("tasks are tracked independently")
	}
}
