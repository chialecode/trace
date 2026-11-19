package live

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
)

// A rollover converts the finished part in the background: the next part
// keeps fetching segments while the previous one converts, so a long part's
// conversion no longer lets the live playlist move past unfetched segments
// (dev 2026-11-18: 12 segments lost after a 3.3 GB part). Both parts end up
// registered, in order, with nothing pending and no lost segments.
func TestRolloverConvertsPreviousPartWhileRecording(t *testing.T) {
	initBytes, fragBytes := splitSyntheticHLSInitAndFrag(t)
	const roomID = int64(9701)
	var srvURL string
	var polls, fetchedSeq atomic.Int32
	var ended atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/room/v1/Room/room_init", func(w http.ResponseWriter, r *http.Request) {
		live := 1
		if ended.Load() {
			live = 0
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"room_id": roomID, "uid": roomID + 10000, "live_status": live, "live_time": 1794024000}})
	})
	mux.HandleFunc("/room/v1/Room/get_status_info_by_uids", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{}})
	})
	mux.HandleFunc("/xlive/web-room/v2/index/getRoomPlayInfo", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"room_id": roomID, "uid": roomID + 10000, "live_status": 1,
			"playurl_info": map[string]any{"playurl": map[string]any{"stream": []map[string]any{{
				"protocol_name": "http_hls",
				"format": []map[string]any{{"format_name": "fmp4", "codec": []map[string]any{{
					"codec_name": "avc", "current_qn": 10000, "base_url": "/hls/index.m3u8",
					"url_info": []map[string]any{{"host": srvURL, "extra": ""}},
				}}}},
			}}}},
		}})
	})
	mux.HandleFunc("/hls/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ".mp4"):
			_, _ = w.Write(initBytes)
		case strings.HasSuffix(r.URL.Path, ".m4s"):
			var seq int32
			_, _ = fmt.Sscanf(filepath.Base(r.URL.Path), "seg%d.m4s", &seq)
			if seq > fetchedSeq.Load() {
				fetchedSeq.Store(seq)
			}
			_, _ = w.Write(fragBytes)
		case strings.HasSuffix(r.URL.Path, "index.m3u8"):
			// The first poll has segment 1 under one init; later polls move
			// to another init (a rollover) and one segment further each time.
			p := polls.Add(1)
			if p == 1 {
				_, _ = fmt.Fprint(w, "#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:1\n#EXT-X-MAP:URI=\"/hls/initA.mp4\"\n#EXTINF:1.0,\n/hls/seg1.m4s\n")
				return
			}
			seq := min(p, 6)
			end := ""
			if seq == 6 {
				end = "#EXT-X-ENDLIST\n"
				ended.Store(true)
			}
			// A live window of the last three segments.
			first := max(2, seq-2)
			var b strings.Builder
			fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:%d\n#EXT-X-MAP:URI=\"/hls/initB.mp4\"\n", first)
			for i := first; i <= seq; i++ {
				fmt.Fprintf(&b, "#EXTINF:1.0,\n/hls/seg%d.m4s\n", i)
			}
			_, _ = fmt.Fprint(w, b.String()+end)
		}
	})
	mgr, _, _, srv, _ := setupLiveTestHarness(t, mux)
	srvURL = srv.URL
	mgr.hlsPollInterval = 10 * time.Millisecond
	mgr.reconnectWait = 10 * time.Millisecond

	// Converting part-001 waits until the next part has fetched segment 4:
	// a conversion inside the recording loop would never see it.
	var progressed atomic.Bool
	orig := convertPart
	convertPart = func(ctx context.Context, partPath, finalPath string) (download.MuxTrackSummary, error) {
		if filepath.Base(finalPath) == "part-001.mp4" {
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) && fetchedSeq.Load() < 4 {
				time.Sleep(5 * time.Millisecond)
			}
			progressed.Store(fetchedSeq.Load() >= 4)
		}
		return orig(ctx, partPath, finalPath)
	}
	t.Cleanup(func() { convertPart = orig })

	rec, err := mgr.StartRecording(context.Background(), roomID)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if st, ok := roomState(mgr, roomID); ok && !st.Recording && ended.Load() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !progressed.Load() {
		t.Fatal("the next part fetched nothing while the previous part converted")
	}
	raw, err := os.ReadFile(filepath.Join(rec.RecordItemDir, "item.json"))
	if err != nil {
		t.Fatal(err)
	}
	meta, err := library.ParseItemMetadata(raw)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Live == nil || len(meta.Live.Segments) != 2 || meta.Live.Segments[0].File != "part-001.mp4" || meta.Live.Segments[1].File != "part-002.mp4" {
		t.Fatalf("parts not registered in order: %+v", meta.Live)
	}
	if len(meta.PendingParts) != 0 || meta.Status != "complete" {
		t.Fatalf("status %q, pending %v", meta.Status, meta.PendingParts)
	}
	for _, inc := range meta.Live.Incidents {
		if inc.Kind == library.LiveIncidentLostSegments {
			t.Fatalf("segments lost across the rollover: %+v", inc)
		}
	}
	for _, f := range []string{"part-001.mp4.part", "part-002.mp4.part"} {
		if _, err := os.Stat(filepath.Join(rec.RecordItemDir, f)); !os.IsNotExist(err) {
			t.Fatalf("%s left behind (%v)", f, err)
		}
	}
}

func roomState(m *Manager, roomID int64) (RoomState, bool) {
	for _, st := range m.ListRooms() {
		if st.RoomID == roomID {
			return st, true
		}
	}
	return RoomState{}, false
}
