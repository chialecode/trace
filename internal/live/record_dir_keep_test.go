package live

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/chialecode/trace/internal/library"
)

// After a recording ends, a status refresh rebuilds the room's state; the
// state keeps the last session's folder with its incidents, so Tasks ›
// 录制异常 does not show them as a second, folderless session.
func TestStatusRefreshKeepsLastSessionFolder(t *testing.T) {
	const roomID, uid = int64(9601), int64(19601)
	mux := http.NewServeMux()
	mux.HandleFunc("/room/v1/Room/get_status_info_by_uids", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"19601": map[string]any{"uid": uid, "room_id": roomID, "uname": "synthetic", "live_status": 0},
		}})
	})
	mux.HandleFunc("/room/v1/Room/room_init", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"room_id": roomID, "uid": uid, "live_status": 0}})
	})
	mgr, _, _, _, _ := setupLiveTestHarness(t, mux)
	incs := []library.LiveIncident{{Kind: library.LiveIncidentLostSegments, At: "2026-11-18T10:20:34Z", LostSegments: 1, Count: 1}}
	mgr.mu.Lock()
	mgr.states[roomID] = RoomState{UID: uid, RoomID: roomID, StopReason: "ended", RecordItemDir: `C:\synthetic\1966814385-1794991191`, Incidents: incs}
	mgr.mu.Unlock()

	if _, err := mgr.RefreshForAccounts(context.Background(), []int64{uid}); err != nil {
		t.Fatal(err)
	}
	st, err := mgr.OpenRoom(context.Background(), roomID)
	if err != nil {
		t.Fatal(err)
	}
	for _, got := range append(mgr.ListRooms(), st) {
		if got.RoomID != roomID {
			continue
		}
		if got.Recording || got.RecordItemDir != `C:\synthetic\1966814385-1794991191` || len(got.Incidents) != 1 {
			t.Fatalf("refresh dropped the last session's folder or incidents: %+v", got)
		}
	}
}
