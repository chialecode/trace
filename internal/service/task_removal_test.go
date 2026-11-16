package service

import (
	"path/filepath"
	"testing"

	"github.com/chialecode/trace/internal/library"
)

// DEC-057: 录制异常 "删除记录" deletes a finished session's incidents and
// keeps the recording; a session still recording is refused.
func TestClearLiveIncidentsKeepsTheRecording(t *testing.T) {
	b, err := NewBackend(Options{Locations: testLocations(t), Desktop: NewMockDesktop(), DisableBgWorkers: true})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	lib := b.LibraryStore()
	save := func(id, status string) library.ItemMetadata {
		dir := filepath.Join(lib.CacheDir(), "bilibili", "42", "lives", id)
		item, err := lib.SaveItem(dir, library.ItemMetadata{Site: "bilibili", Kind: "live", ID: "live:" + id, Status: status, Title: "直播",
			Author: library.ItemAuthor{Mid: 42, Name: "主播"},
			Live: &library.LiveRecordingInfo{RoomID: 9, Incidents: []library.LiveIncident{
				{Kind: library.LiveIncidentReconnect, At: "2026-11-16T10:00:00Z", DurationSec: 3, Count: 1},
			}}})
		if err != nil {
			t.Fatal(err)
		}
		return item
	}
	done := save("9-1700000000", "complete")
	if err := b.ClearLiveIncidents(done.ItemKey, "bilibili", "9", "42"); err != nil {
		t.Fatal(err)
	}
	got, ok, err := lib.GetItem(done.ItemKey)
	if err != nil || !ok || got.Live == nil || len(got.Live.Incidents) != 0 || got.Live.RoomID != 9 {
		t.Fatalf("after clearing: %+v ok=%v err=%v", got.Live, ok, err)
	}

	live := save("9-1700009999", "recording")
	if err := b.ClearLiveIncidents(live.ItemKey, "bilibili", "9", "42"); err == nil {
		t.Fatal("clearing a session still recording succeeded")
	}
	if got, _, _ := lib.GetItem(live.ItemKey); got.Live == nil || len(got.Live.Incidents) != 1 {
		t.Fatal("a recording session lost its incidents")
	}
}
