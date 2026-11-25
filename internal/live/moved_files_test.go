package live

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/settings"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/storage"
	"github.com/chialecode/trace/internal/subscription"
)

// After the first segment is finished, its file and
// item.json are moved away (the user's rclone move or the uploader). The
// recording goes on, the next segment is finalized normally, and the
// item.json written then lists both finished segments.
func TestRecordingContinuesWhenFinishedSegmentsAreMovedAway(t *testing.T) {
	vM4S, err := download.BuildSyntheticVideoM4S(30, 30)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := mp4.DecodeFile(bytes.NewReader(vM4S), mp4.WithDecodeFlags(mp4.DecStartOnMoof))
	if err != nil {
		t.Fatal(err)
	}
	var initBuf, fragBuf bytes.Buffer
	_ = parsed.Init.Encode(&initBuf)
	_ = parsed.Segments[0].Fragments[0].Encode(&fragBuf)

	var srvURL string
	var hits atomic.Int32
	var live atomic.Bool
	live.Store(true)
	var recordDir atomic.Value
	var moved atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/room/v1/Room/get_status_info_by_uids", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"1001": map[string]any{
			"uid": 1001, "room_id": 8888, "title": "合成直播", "uname": "Streamer1001", "live_status": 1, "live_time": 1794024000}}})
	})
	mux.HandleFunc("/room/v1/Room/room_init", func(w http.ResponseWriter, r *http.Request) {
		st := 0
		if live.Load() {
			st = 1
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"room_id": 8888, "short_id": 88, "uid": 1001, "live_status": st, "live_time": 1794024000}})
	})
	mux.HandleFunc("/xlive/web-room/v2/index/getRoomPlayInfo", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"room_id": 8888, "uid": 1001, "live_status": 1,
			"playurl_info": map[string]any{"playurl": map[string]any{"stream": []map[string]any{{"protocol_name": "http_hls",
				"format": []map[string]any{{"format_name": "fmp4", "codec": []map[string]any{{"codec_name": "avc", "current_qn": 10000, "base_url": "/hls/index.m3u8",
					"url_info": []map[string]any{{"host": srvURL, "extra": "?expires=1999999999"}}}}}}}}}}}})
	})
	mux.HandleFunc("/hls/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
		switch hits.Add(1) {
		case 1:
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:10\n#EXT-X-MAP:URI=\"/hls/init.mp4\"\n#EXTINF:1.0,\n/hls/seg1.m4s\n"))
		case 2:
			w.WriteHeader(http.StatusBadGateway) // the recorder finalizes part-001 and reconnects
		default:
			// part-001 converts asynchronously after the 502 reconnect; wait briefly
			// for part-001.mp4 to finish before moving it away.
			if dir, ok := recordDir.Load().(string); ok && !moved.Load() {
				waitUntil := time.Now().Add(2 * time.Second)
				for time.Now().Before(waitUntil) {
					if _, err := os.Stat(filepath.Join(dir, "part-001.mp4")); err == nil {
						_ = os.Remove(filepath.Join(dir, "part-001.mp4"))
						_ = os.Remove(filepath.Join(dir, "item.json"))
						moved.Store(true)
						break
					}
					time.Sleep(15 * time.Millisecond)
				}
			}
			live.Store(false)
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:11\n#EXT-X-MAP:URI=\"/hls/init.mp4\"\n#EXTINF:1.0,\n/hls/seg2.m4s\n#EXT-X-ENDLIST\n"))
		}
	})
	mux.HandleFunc("/hls/init.mp4", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(initBuf.Bytes()) })
	mux.HandleFunc("/hls/seg1.m4s", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(fragBuf.Bytes()) })
	mux.HandleFunc("/hls/seg2.m4s", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(fragBuf.Bytes()) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	srvURL = srv.URL

	tmp := t.TempDir()
	db, _, _, _ := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	defer db.Close()
	cacheDir := filepath.Join(tmp, "cache")
	lib, _ := library.Open(db, cacheDir)
	lib.SetAllowMovedFiles(true)
	cred, _, _ := credential.Open(filepath.Join(tmp, "cred.dpapi"))
	_, _ = cred.SaveManual("SESSDATA=s; bili_jct=c", "rt")
	client := bilibili.NewClient(bilibili.ClientOptions{CredStore: cred, Endpoints: bilibili.Endpoints{APIBase: srv.URL, LiveAPIBase: srv.URL}, PacingInterval: -1, AllowLocalhost: true})
	subMgr, _ := subscription.NewManager(db, lib, nil, client, settings.Default(cacheDir).Subscription, nil)
	_, _ = subMgr.Upsert(subscription.Subscription{Mid: 1001, Uname: "Streamer1001", Enabled: true, LiveNotify: true})
	mgr := NewManager(lib, subMgr, client, settings.Default(cacheDir).Live, func(Notification) {}, nil)
	mgr.hlsPollInterval = 30 * time.Millisecond
	mgr.reconnectWait = 30 * time.Millisecond
	mgr.freeBytesFunc = func(string) (uint64, error) { return 10 << 30, nil }

	checkNames := watchNames(t, cacheDir)
	ctx := context.Background()
	if _, err := mgr.PollNow(ctx); err != nil {
		t.Fatal(err)
	}
	st, err := mgr.StartRecording(ctx, 8888)
	if err != nil || !st.Recording {
		t.Fatalf("StartRecording = %+v, %v", st, err)
	}
	recordDir.Store(st.RecordItemDir)
	deadline := time.Now().Add(5 * time.Second)
	finished := false
	for time.Now().Before(deadline) {
		if rooms := mgr.ListRooms(); len(rooms) == 1 && !rooms[0].Recording {
			finished = true
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !finished {
		t.Fatalf("timed out waiting for recording to finish: %+v", mgr.ListRooms())
	}
	if !moved.Load() {
		t.Fatal("the test did not move part-001 away during the recording")
	}
	checkNames("part-001.mp4", "item.json")
	if _, err := os.Stat(filepath.Join(st.RecordItemDir, "part-002.mp4")); err != nil {
		t.Fatalf("part-002.mp4: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(st.RecordItemDir, "item.json"))
	if err != nil {
		t.Fatalf("item.json was not written after the next segment: %v", err)
	}
	var meta library.ItemMetadata
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Live == nil || len(meta.Live.Segments) != 2 {
		t.Fatalf("item.json segments = %+v, want both finished segments", meta.Live)
	}
	if meta.Status != "complete" || meta.Live.Segments[1].File != "part-002.mp4" {
		t.Fatalf("item after the move = status %q segments %+v, want complete with part-002.mp4", meta.Status, meta.Live.Segments)
	}
}
