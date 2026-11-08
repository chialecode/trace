package live

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
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

// A recording that is stopped while the broadcast goes on, as quitting
// TRACE for an update does, is finalized into a playable file.
func TestStopRecordingWhileLiveFinalizesPlayableFile(t *testing.T) {
	vM4S, err := download.BuildSyntheticVideoM4S(30, 30)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := mp4.DecodeFile(bytes.NewReader(vM4S), mp4.WithDecodeFlags(mp4.DecStartOnMoof))
	if err != nil {
		t.Fatal(err)
	}
	var initBuf bytes.Buffer
	_ = parsed.Init.Encode(&initBuf)
	initBytes := initBuf.Bytes()
	frag := parsed.Segments[0].Fragments[0]
	var fragDur uint64
	for _, traf := range frag.Moof.Trafs {
		for _, trun := range traf.Truns {
			fragDur = max(fragDur, trun.Duration(traf.Tfhd.DefaultSampleDuration))
		}
	}
	// Each segment of the broadcast continues the timeline of the last.
	fragmentAt := func(n uint32) []byte {
		frag.Moof.Mfhd.SequenceNumber = n
		for _, traf := range frag.Moof.Trafs {
			traf.Tfdt.SetBaseMediaDecodeTime(uint64(n) * fragDur)
		}
		var b bytes.Buffer
		_ = frag.Encode(&b)
		return b.Bytes()
	}

	var seq atomic.Int32
	var fragments atomic.Int32
	seq.Store(100)
	var srvURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/room/v1/Room/get_status_info_by_uids", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"2101": map[string]any{"uid": 2101, "room_id": 9101, "title": "R9101", "uname": "U2101", "live_status": 1, "live_time": 1794024000},
		}})
	})
	mux.HandleFunc("/room/v1/Room/room_init", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"room_id": 9101, "uid": 2101, "live_status": 1, "live_time": 1794024000}})
	})
	mux.HandleFunc("/xlive/web-room/v2/index/getRoomPlayInfo", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"room_id": 9101, "uid": 2101, "live_status": 1,
			"playurl_info": map[string]any{"playurl": map[string]any{"stream": []map[string]any{{
				"protocol_name": "http_hls",
				"format": []map[string]any{{"format_name": "fmp4", "codec": []map[string]any{{
					"codec_name": "avc", "current_qn": 10000, "base_url": "/hls/live.m3u8",
					"url_info": []map[string]any{{"host": srvURL, "extra": ""}},
				}}}},
			}}}},
		}})
	})
	// A broadcast that never ends: one new segment per playlist read.
	mux.HandleFunc("/hls/live.m3u8", func(w http.ResponseWriter, r *http.Request) {
		n := seq.Add(1)
		fmt.Fprintf(w, "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:%d\n#EXT-X-MAP:URI=\"/hls/init.mp4\"\n#EXTINF:1.0,\n/hls/seg%d.m4s\n", n, n)
	})
	mux.HandleFunc("/hls/init.mp4", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(initBytes) })
	var fragMu sync.Mutex
	mux.HandleFunc("/hls/", func(w http.ResponseWriter, r *http.Request) {
		var n uint32
		if _, err := fmt.Sscanf(r.URL.Path, "/hls/seg%d.m4s", &n); err != nil {
			http.NotFound(w, r)
			return
		}
		fragments.Add(1)
		fragMu.Lock()
		data := fragmentAt(n)
		fragMu.Unlock()
		_, _ = w.Write(data)
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pattern := mux.Handler(r); pattern == "" {
			t.Logf("unhandled %s", r.URL.Path)
		}
		mux.ServeHTTP(w, r)
	}))
	defer srv.Close()
	srvURL = srv.URL

	tmp := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cacheDir := filepath.Join(tmp, "cache")
	lib, _ := library.Open(db, cacheDir)
	cred, _, _ := credential.Open(filepath.Join(tmp, "cred.dpapi"))
	_, _ = cred.SaveManual("SESSDATA=s; bili_jct=c", "rt")
	client := bilibili.NewClient(bilibili.ClientOptions{
		CredStore:      cred,
		Endpoints:      bilibili.Endpoints{APIBase: srv.URL, LiveAPIBase: srv.URL},
		PacingInterval: -1,
		AllowLocalhost: true,
	})
	subMgr, _ := subscription.NewManager(db, lib, nil, client, settings.Default(cacheDir).Subscription, nil)
	liveMgr := NewManager(lib, subMgr, client, settings.Default(cacheDir).Live, nil, nil)
	liveMgr.hlsPollInterval = 20 * time.Millisecond
	liveMgr.freeBytesFunc = func(string) (uint64, error) { return 10 << 30, nil }
	defer liveMgr.Stop()

	rec, err := liveMgr.StartRecording(context.Background(), 9101)
	if err != nil {
		t.Fatalf("StartRecording: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for fragments.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if fragments.Load() < 3 {
		t.Fatal("recording did not receive fragments")
	}

	if _, err := liveMgr.StopRecording(9101); err != nil {
		t.Fatalf("StopRecording: %v", err)
	}
	for _, r := range liveMgr.ListRooms() {
		if r.RoomID == 9101 && r.Recording {
			t.Fatal("room still recording after stop")
		}
	}
	final := filepath.Join(rec.RecordItemDir, "part-001.mp4")
	if entries, err := os.ReadDir(rec.RecordItemDir); err == nil {
		for _, e := range entries {
			info, _ := e.Info()
			t.Logf("recording dir: %s %d", e.Name(), info.Size())
		}
	}
	if _, err := os.Stat(final + ".part"); !os.IsNotExist(err) {
		t.Fatalf("temporary part left behind: %v", err)
	}
	f, err := os.Open(final)
	if err != nil {
		t.Fatalf("finalized recording missing: %v", err)
	}
	defer f.Close()
	file, err := mp4.DecodeFile(f)
	if err != nil {
		t.Fatalf("finalized recording does not decode: %v", err)
	}
	if file.Moov == nil || len(file.Moov.Traks) == 0 {
		t.Fatal("finalized recording has no moov/track: not playable")
	}
	raw, err := os.ReadFile(filepath.Join(rec.RecordItemDir, "item.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta library.ItemMetadata
	if err := json.Unmarshal(raw, &meta); err != nil || meta.Status == "recording" {
		t.Fatalf("item still marked recording: %+v %v", meta.Status, err)
	}
}
