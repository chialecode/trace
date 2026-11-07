package live

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
)

func regressionRecorder(t *testing.T, handler http.Handler) (*Manager, *library.Store, string) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	dir := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(dir, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	lib, err := library.OpenWithDataDir(db, filepath.Join(dir, "cache"), dir)
	if err != nil {
		t.Fatal(err)
	}
	cred, _, err := credential.Open(filepath.Join(dir, "credential.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cred.SaveManual("SESSDATA=synthetic; DedeUserID=1", ""); err != nil {
		t.Fatal(err)
	}
	client := bilibili.NewClient(bilibili.ClientOptions{CredStore: cred, AllowLocalhost: true, PacingInterval: -1, Endpoints: bilibili.Endpoints{APIBase: server.URL, LiveAPIBase: server.URL, WWWBase: server.URL}})
	mgr := NewManager(lib, nil, client, settings.Default(lib.CacheDir()).Live, nil, nil)
	mgr.minFreeBytes, mgr.hlsPollInterval = 0, time.Millisecond
	return mgr, lib, server.URL
}

func regressionLiveSamples(t *testing.T) ([]byte, []byte) {
	t.Helper()
	raw, err := download.BuildSyntheticVideoM4S(30, 30)
	if err != nil {
		t.Fatal(err)
	}
	file, err := mp4.DecodeFile(bytes.NewReader(raw), mp4.WithDecodeFlags(mp4.DecStartOnMoof))
	if err != nil {
		t.Fatal(err)
	}
	var init, fragment bytes.Buffer
	if err := file.Init.Encode(&init); err != nil {
		t.Fatal(err)
	}
	if err := file.Segments[0].Fragments[0].Encode(&fragment); err != nil {
		t.Fatal(err)
	}
	return init.Bytes(), fragment.Bytes()
}

func regressionLiveSession(t *testing.T, lib *library.Store) *recordSession {
	t.Helper()
	dir := filepath.Join(lib.CacheDir(), "broadcast")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	meta, err := lib.SaveItem(dir, library.ItemMetadata{Site: "bilibili", Kind: "live", ID: "live:42:100", Status: "recording", Live: &library.LiveRecordingInfo{RoomID: 42, LiveSince: 100}, PendingParts: []string{"part-001.mp4.part"}})
	if err != nil {
		t.Fatal(err)
	}
	return &recordSession{roomID: 42, itemDir: dir, itemMeta: meta, nextPart: 1, startedAt: time.Now(), doneCh: make(chan struct{})}
}

func TestVisibleUnsubscribedCreatorGetsReadOnlyLiveStatus(t *testing.T) {
	var requests atomic.Int32
	mgr, _, _ := regressionRecorder(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/room/v1/Room/get_status_info_by_uids" {
			t.Errorf("unexpected request during visible live status refresh: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		requests.Add(1)
		_, _ = w.Write([]byte(`{"code":0,"data":{"42":{"uid":42,"room_id":9001,"live_status":1,"uname":"fixture creator","title":"fixture live","live_time":1700000000}}}`))
	}))
	for i := 0; i < 2; i++ {
		rooms, err := mgr.RefreshForAccounts(context.Background(), []int64{42})
		if err != nil || len(rooms) != 1 || rooms[0].UID != 42 || rooms[0].RoomID != 9001 || rooms[0].LiveStatus != 1 || rooms[0].Recording {
			t.Fatalf("unsubscribed creator's visible status was omitted or started recording: %+v err=%v", rooms, err)
		}
	}
	if requests.Load() != 1 {
		t.Fatal("visible live refresh did not reuse the 60-second cache")
	}
}

func TestVisibleLiveCacheExpiresFromOriginalFetch(t *testing.T) {
	for _, readRoom := range []bool{false, true} {
		t.Run(fmt.Sprintf("read-room-%t", readRoom), func(t *testing.T) {
			var requests atomic.Int32
			mgr, _, _ := regressionRecorder(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/room/v1/Room/get_status_info_by_uids":
					liveStatus := 1
					if requests.Add(1) > 1 {
						liveStatus = 0
					}
					_, _ = fmt.Fprintf(w, `{"code":0,"data":{"42":{"uid":42,"room_id":9001,"live_status":%d,"uname":"fixture creator"}}}`, liveStatus)
				case "/room/v1/Room/room_init":
					_, _ = w.Write([]byte(`{"code":0,"data":{"uid":42,"room_id":9001,"live_status":1}}`))
				default:
					t.Errorf("unexpected request: %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			now := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
			mgr.nowFunc = func() time.Time { return now }
			if _, err := mgr.RefreshForAccounts(context.Background(), []int64{42}); err != nil {
				t.Fatal(err)
			}
			now = now.Add(45 * time.Second)
			if readRoom {
				if _, err := mgr.OpenRoom(context.Background(), 9001); err != nil {
					t.Fatal(err)
				}
			} else if _, err := mgr.RefreshForAccounts(context.Background(), []int64{42}); err != nil {
				t.Fatal(err)
			}
			if requests.Load() != 1 {
				t.Fatal("a cached lookup made an early status request")
			}
			now = now.Add(16 * time.Second)
			rooms, err := mgr.RefreshForAccounts(context.Background(), []int64{42})
			if err != nil || requests.Load() != 2 || len(rooms) != 1 || rooms[0].LiveStatus != 0 {
				t.Fatalf("cached reads extended the original 60-second deadline: requests=%d rooms=%+v err=%v", requests.Load(), rooms, err)
			}
		})
	}
}

func TestRecorderPersistsCompleteFragmentBoundary(t *testing.T) {
	init, fragment := regressionLiveSamples(t)
	mgr, lib, base := regressionRecorder(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index":
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:7\n#EXT-X-MAP:URI=\"/init\"\n#EXTINF:1,\n/fragment\n#EXT-X-ENDLIST\n"))
		case "/init":
			_, _ = w.Write(init)
		case "/fragment":
			_, _ = w.Write(fragment)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	session := regressionLiveSession(t, lib)
	part := filepath.Join(session.itemDir, "part-001.mp4.part")
	if _, err := mgr.recordHLSSegment(context.Background(), session, []string{base + "/index"}, part, true); err != nil {
		t.Fatal(err)
	}
	meta, _, err := lib.GetItem(session.itemMeta.ItemKey)
	if err != nil || meta.Live == nil || !meta.Live.HasSequence || meta.Live.LastSequence != 7 || meta.Live.PendingBytes != int64(len(init)+len(fragment)) || meta.Live.PendingDuration != 1 {
		t.Fatalf("complete fragment has no durable checkpoint: %+v %v", meta.Live, err)
	}
}

func TestInvalidLiveInitNeverRequestsMediaFragment(t *testing.T) {
	var mediaHits atomic.Int32
	mgr, lib, base := regressionRecorder(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index":
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MAP:URI=\"/init\"\n#EXTINF:1,\n/fragment\n#EXT-X-ENDLIST\n"))
		case "/init":
			_, _ = w.Write([]byte("invalid init"))
		case "/fragment":
			mediaHits.Add(1)
			_, _ = w.Write([]byte("must not request"))
		}
	}))
	session := regressionLiveSession(t, lib)
	_, err := mgr.recordHLSSegment(context.Background(), session, []string{base + "/index"}, filepath.Join(session.itemDir, "part-001.mp4.part"), true)
	if !bilibili.IsKind(err, bilibili.ErrKindSchemaChanged) || mediaHits.Load() != 0 {
		t.Fatalf("invalid init accepted or media requested: %v, %d", err, mediaHits.Load())
	}
}

func TestRecoveryUsesRegisteredPartAndTruncatesAfterCheckpoint(t *testing.T) {
	mgr, lib, _ := regressionRecorder(t, http.NotFoundHandler())
	init, fragment := regressionLiveSamples(t)
	session := regressionLiveSession(t, lib)
	raw := append(append([]byte(nil), init...), fragment...)
	part := filepath.Join(session.itemDir, "part-001.mp4.part")
	if err := os.WriteFile(part, append(raw, []byte("unfinished tail")...), 0600); err != nil {
		t.Fatal(err)
	}
	personal := filepath.Join(session.itemDir, "part-999.mp4.part")
	if err := os.WriteFile(personal, []byte("personal file"), 0600); err != nil {
		t.Fatal(err)
	}
	session.itemMeta.Live.PendingFile, session.itemMeta.Live.PendingBytes, session.itemMeta.Live.PendingDuration = "part-001.mp4.part", int64(len(raw)), 1
	if _, err := lib.SaveItem(session.itemDir, session.itemMeta); err != nil {
		t.Fatal(err)
	}
	mgr.RecoverOrphanParts()
	meta, _, err := lib.GetItem(session.itemMeta.ItemKey)
	if err != nil || len(meta.PendingParts) != 0 || len(meta.Live.Segments) != 1 || meta.Status != "complete" {
		t.Fatalf("registered part did not recover: %+v %v", meta, err)
	}
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Fatalf("recovered original still present: %v", err)
	}
	if saved, err := os.ReadFile(personal); err != nil || string(saved) != "personal file" {
		t.Fatal("recovery changed unregistered part")
	}
	if !library.VerifyMediaFile(meta.ItemDir, meta.Media[0], true) {
		t.Fatal("recovered MP4 hash is invalid")
	}
}

func TestRecordingPreservesUserOwnedTargetName(t *testing.T) {
	mgr, lib, _ := regressionRecorder(t, http.NotFoundHandler())
	session := regressionLiveSession(t, lib)
	session.itemMeta.PendingParts = nil
	if _, err := lib.SaveItem(session.itemDir, session.itemMeta); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(session.itemDir, "part-001.mp4")
	if err := os.WriteFile(path, []byte("personal recording"), 0600); err != nil {
		t.Fatal(err)
	}
	mgr.sessions[42] = session
	mgr.states[42] = RoomState{RoomID: 42}
	mgr.wg.Add(1)
	mgr.runRecordingSession(context.Background(), session, bilibili.LivePlaySelection{}, nil)
	if raw, err := os.ReadFile(path); err != nil || string(raw) != "personal recording" {
		t.Fatal("recording overwrote a user target")
	}
	meta, _, err := lib.GetItem(session.itemMeta.ItemKey)
	if err != nil || len(meta.PendingParts) != 0 {
		t.Fatalf("unclaimed target was entered in deletion ledger: %+v %v", meta, err)
	}
}

// Some live nodes answer the playurl address with a multivariant playlist; the
// recorder follows the best variant once and records from the media playlist,
// whose entries are relative and carry Bilibili-style EXTINF titles and the
// site's own #EXT-BILI-AUX tag (seen in ACT-007).
func TestRecorderFollowsMultivariantPlaylistOnce(t *testing.T) {
	init, fragment := regressionLiveSamples(t)
	var masterHits atomic.Int32
	mgr, lib, base := regressionRecorder(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/live/index.m3u8":
			masterHits.Add(1)
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-STREAM-INF:PROGRAM-ID=1,BANDWIDTH=800000\nlow/index.m3u8?token=a\n#EXT-X-STREAM-INF:BANDWIDTH=6000000,CODECS=\"avc1.640032,mp4a.40.2\"\nhigh/index.m3u8?token=b\n"))
		case "/live/high/index.m3u8":
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-START:TIME-OFFSET=-3\n#EXT-X-MEDIA-SEQUENCE:43358734\n#EXT-X-TARGETDURATION:1\n#EXT-X-MAP:URI=\"h1699999999.m4s?token=b\"\n#EXTINF:1.00,4a3e8|e2f5b0e1\n#EXT-BILI-AUX:a1b2c3|K|1d2e|ffff\n43358734.m4s?token=b\n#EXT-X-ENDLIST\n"))
		case "/live/high/h1699999999.m4s":
			_, _ = w.Write(init)
		case "/live/high/43358734.m4s":
			_, _ = w.Write(fragment)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	session := regressionLiveSession(t, lib)
	part := filepath.Join(session.itemDir, "part-001.mp4.part")
	n, err := mgr.recordHLSSegment(context.Background(), session, []string{base + "/live/index.m3u8"}, part, true)
	if err != nil || n != int64(len(init)+len(fragment)) || masterHits.Load() != 1 || session.lastSequence != 43358734 {
		t.Fatalf("multivariant playlist not followed: n=%d err=%v masterHits=%d seq=%d", n, err, masterHits.Load(), session.lastSequence)
	}
}

func TestNestedMultivariantPlaylistStopsWithReason(t *testing.T) {
	mgr, lib, base := regressionRecorder(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nnext.m3u8\n"))
	}))
	session := regressionLiveSession(t, lib)
	_, err := mgr.recordHLSSegment(context.Background(), session, []string{base + "/index.m3u8"}, filepath.Join(session.itemDir, "part-001.mp4.part"), true)
	if !bilibili.IsKind(err, bilibili.ErrKindSchemaChanged) || !strings.Contains(err.Error(), "多级索引") {
		t.Fatalf("nested variants should stop with a clear reason: %v", err)
	}
	p := parseM3U8PlaylistDetails("#EXTM3U\n#EXTINF:1,\nseg.m4s\n", base)
	if p.valid || !strings.Contains(p.invalidReason, "EXT-X-MAP") {
		t.Fatalf("missing map should name the reason: %+v", p)
	}
}
