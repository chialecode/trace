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

// C32: Live state transitions (0/2 -> 1 notifies, 2=carousel does NOT notify/record),
// HLS fMP4 recording, and disconnect rollover into registered part files.
func TestLiveTransitionsCarouselFilterAndRecordingRollover(t *testing.T) {
	vM4S, err := download.BuildSyntheticVideoM4S(30, 30)
	if err != nil {
		t.Fatalf("BuildSyntheticVideoM4S: %v", err)
	}
	// Split vM4S into init (ftyp+moov) and fragment (moof+mdat) for HLS EXT-X-MAP + segment serving!
	parsed, err := mp4.DecodeFile(bytes.NewReader(vM4S), mp4.WithDecodeFlags(mp4.DecStartOnMoof))
	if err != nil {
		t.Fatalf("DecodeFile vM4S: %v", err)
	}
	var initBuf, fragBuf bytes.Buffer
	_ = parsed.Init.Encode(&initBuf)
	_ = parsed.Segments[0].Fragments[0].Encode(&fragBuf)
	initBytes := initBuf.Bytes()
	fragBytes := fragBuf.Bytes()

	var pollWave atomic.Int32
	var m3u8Hits atomic.Int32
	var roomStillLive atomic.Bool
	roomStillLive.Store(true)

	var srvURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/room/v1/Room/get_status_info_by_uids", func(w http.ResponseWriter, r *http.Request) {
		wave := pollWave.Add(1)
		// UID 1001: wave 1 -> 2 (carousel), wave 2 -> 1 (live!), wave 3 -> 1 (still live, no duplicate notification)
		st1001 := 2
		if wave >= 2 {
			st1001 = 1
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"1001": map[string]any{
					"uid":         1001,
					"room_id":     8888,
					"title":       "Room 8888 Broadcast",
					"uname":       "Streamer1001",
					"live_status": st1001,
					"live_time":   1794024000,
				},
			},
		})
	})

	mux.HandleFunc("/room/v1/Room/room_init", func(w http.ResponseWriter, r *http.Request) {
		liveSt := 0
		if roomStillLive.Load() {
			liveSt = 1
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"room_id":     8888,
				"short_id":    88,
				"uid":         1001,
				"live_status": liveSt,
				"live_time":   1794024000,
			},
		})
	})

	mux.HandleFunc("/xlive/web-room/v2/index/getRoomPlayInfo", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"room_id":     8888,
				"uid":         1001,
				"live_status": 1,
				"playurl_info": map[string]any{
					"playurl": map[string]any{
						"stream": []map[string]any{
							{
								"protocol_name": "http_hls",
								"format": []map[string]any{
									{
										"format_name": "fmp4",
										"codec": []map[string]any{
											{
												"codec_name": "avc",
												"current_qn": 10000,
												"base_url":   "/hls/index.m3u8",
												"url_info": []map[string]any{
													{"host": srvURL, "extra": "?expires=1999999999"},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		})
	})

	mux.HandleFunc("/hls/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
		hit := m3u8Hits.Add(1)
		switch hit {
		case 1:
			// First playlist poll serves init.mp4 + seg1.m4s
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:10\n#EXT-X-MAP:URI=\"/hls/init.mp4\"\n#EXTINF:1.0,\n/hls/seg1.m4s\n"))
		case 2:
			// Simulate mid-stream disconnect on second poll so recorder rolls over to segment_002.mp4!
			w.WriteHeader(http.StatusBadGateway)
		default:
			// Reconnected second segment serves init.mp4 + seg2.m4s + ENDLIST
			roomStillLive.Store(false)
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:11\n#EXT-X-MAP:URI=\"/hls/init.mp4\"\n#EXTINF:1.0,\n/hls/seg2.m4s\n#EXT-X-ENDLIST\n"))
		}
	})
	mux.HandleFunc("/hls/init.mp4", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(initBytes)
	})
	mux.HandleFunc("/hls/seg1.m4s", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(fragBytes)
	})
	mux.HandleFunc("/hls/seg2.m4s", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(fragBytes)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()
	srvURL = srv.URL

	tmp := t.TempDir()
	db, _, _, _ := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
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
	_, _ = subMgr.Upsert(subscription.Subscription{
		Mid:            1001,
		Uname:          "Streamer1001",
		Enabled:        true,
		LiveNotify:     true,
		LiveAutoRecord: false,
	})

	var notifications []Notification
	liveMgr := NewManager(lib, subMgr, client, settings.Default(cacheDir).Live, func(n Notification) {
		notifications = append(notifications, n)
	}, nil)
	liveMgr.hlsPollInterval = 30 * time.Millisecond
	liveMgr.reconnectWait = 30 * time.Millisecond
	liveMgr.freeBytesFunc = func(string) (uint64, error) { return 10 << 30, nil }

	ctx := context.Background()

	// Wave 1: live_status=2 (轮播中) -> MUST NOT trigger notification!
	rooms1, err := liveMgr.PollNow(ctx)
	if err != nil || len(rooms1) != 1 || rooms1[0].StatusText != "轮播中" {
		t.Fatalf("PollNow wave 1 = %+v, err=%v", rooms1, err)
	}
	if len(notifications) != 0 {
		t.Fatalf("expected 0 notifications for carousel status=2, got %+v", notifications)
	}

	// Wave 2: live_status transitions 2 -> 1 (直播中) -> MUST trigger 1 notification!
	rooms2, err := liveMgr.PollNow(ctx)
	if err != nil || rooms2[0].StatusText != "直播中" {
		t.Fatalf("PollNow wave 2 = %+v, err=%v", rooms2, err)
	}
	if len(notifications) != 1 || notifications[0].RoomID != 8888 {
		t.Fatalf("expected 1 notification on 2->1 transition, got %+v", notifications)
	}

	// Wave 3: live_status stays 1 -> MUST NOT emit duplicate notification!
	_, _ = liveMgr.PollNow(ctx)
	if len(notifications) != 1 {
		t.Fatalf("expected no duplicate notification when staying in status=1, got %d", len(notifications))
	}

	// A disconnect finalizes the first part and reconnects into the next part.
	stRec, err := liveMgr.StartRecording(ctx, 8888)
	if err != nil || !stRec.Recording {
		t.Fatalf("StartRecording = %+v, err=%v", stRec, err)
	}
	if stRec.Quality != 10000 || stRec.QualityName != "原画" || stRec.Codec != "avc" {
		t.Fatalf("expected Quality=10000 QualityName=原画 Codec=avc on RoomState, got %+v", stRec)
	}
	if spec := bilibili.FormatLiveQualitySpec(stRec.QualityName, stRec.Quality, stRec.Codec); spec != "原画 · AVC" {
		t.Fatalf("expected FormatLiveQualitySpec = 原画 · AVC, got %q", spec)
	}

	// Wait for recording session to finish after ENDLIST.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rooms := liveMgr.ListRooms()
		if len(rooms) == 1 && !rooms[0].Recording {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	for _, name := range []string{"part-001.mp4", "part-002.mp4", "item.json"} {
		p := filepath.Join(stRec.RecordItemDir, name)
		info, err := os.Stat(p)
		if err != nil || info.Size() == 0 {
			t.Fatalf("expected recorded file %s to exist and be non-empty: err=%v", name, err)
		}
	}

	items, err := lib.ListItems(library.FeedFilter{Kind: "live"})
	if err != nil || len(items) != 1 || items[0].Status != "complete" || len(items[0].Media) != 2 {
		t.Fatalf("unexpected indexed live item: %+v err=%v", items, err)
	}
	if items[0].Live == nil || len(items[0].Live.Segments) != 2 || items[0].PubTs != 1794024000 || len(items[0].PendingParts) != 0 {
		t.Fatalf("missing broadcast/segment metadata: %+v", items[0])
	}
	if items[0].Live.Quality != 10000 || items[0].Live.QualityName != "原画" || items[0].Live.Codec != "avc" {
		t.Fatalf("missing live quality/codec metadata on recorded item: %+v", items[0].Live)
	}
	// Deletion must remove every TRACE-owned recording file, without extra aliases.
	if _, err := lib.Delete(items[0].ItemKey); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stRec.RecordItemDir); !os.IsNotExist(err) {
		t.Fatalf("recording directory still contains unregistered copies: %v", err)
	}
}

// C32 & R-14: 60s status batch cache, HLS sequence/discontinuity/MAP rollover,
// conversion failure keeping .part as partial, disk space < 1 GiB stop, maxRecordings limit,
// and manual stop suppressing auto-restart during the same broadcast.
func TestLiveStatusBatchCacheAndHLSRecorder(t *testing.T) {
	vM4S, err := download.BuildSyntheticVideoM4S(30, 30)
	if err != nil {
		t.Fatalf("BuildSyntheticVideoM4S: %v", err)
	}
	parsed, err := mp4.DecodeFile(bytes.NewReader(vM4S), mp4.WithDecodeFlags(mp4.DecStartOnMoof))
	if err != nil {
		t.Fatalf("DecodeFile vM4S: %v", err)
	}
	var initBuf, fragBuf bytes.Buffer
	_ = parsed.Init.Encode(&initBuf)
	_ = parsed.Segments[0].Fragments[0].Encode(&fragBuf)
	initBytes := initBuf.Bytes()
	fragBytes := fragBuf.Bytes()

	var statusHits atomic.Int32
	var m3u8Step atomic.Int32
	var corruptMode atomic.Bool
	var room9001Live atomic.Bool
	var room9002Live atomic.Bool
	room9001Live.Store(true)
	room9002Live.Store(true)

	var srvURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/room/v1/Room/get_status_info_by_uids", func(w http.ResponseWriter, r *http.Request) {
		statusHits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"2001": map[string]any{"uid": 2001, "room_id": 9001, "title": "R9001", "uname": "U2001", "live_status": 1, "live_time": 1794024000},
				"2002": map[string]any{"uid": 2002, "room_id": 9002, "title": "R9002", "uname": "U2002", "live_status": 1, "live_time": 1794024000},
			},
		})
	})
	mux.HandleFunc("/room/v1/Room/room_init", func(w http.ResponseWriter, r *http.Request) {
		roomID := 9001
		liveSt := 0
		if r.URL.Query().Get("id") == "9002" {
			roomID = 9002
			if room9002Live.Load() {
				liveSt = 1
			}
		} else if room9001Live.Load() {
			liveSt = 1
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"room_id":     roomID,
				"uid":         2001 + (roomID - 9001),
				"live_status": liveSt,
				"live_time":   1794024000,
			},
		})
	})
	mux.HandleFunc("/xlive/web-room/v2/index/getRoomPlayInfo", func(w http.ResponseWriter, r *http.Request) {
		roomID := 9001
		if r.URL.Query().Get("room_id") == "9002" {
			roomID = 9002
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"room_id":     roomID,
				"uid":         2001,
				"live_status": 1,
				"playurl_info": map[string]any{
					"playurl": map[string]any{
						"stream": []map[string]any{
							{
								"protocol_name": "http_hls",
								"format": []map[string]any{
									{
										"format_name": "fmp4",
										"codec": []map[string]any{
											{
												"codec_name": "avc",
												"current_qn": 10000,
												"base_url":   "/hls/stream.m3u8",
												"url_info": []map[string]any{
													{"host": srvURL, "extra": ""},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		})
	})
	mux.HandleFunc("/hls/stream.m3u8", func(w http.ResponseWriter, r *http.Request) {
		if corruptMode.Load() {
			room9002Live.Store(false)
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:1\n#EXT-X-MAP:URI=\"/hls/corrupt.mp4\"\n#EXTINF:1.0,\n/hls/corrupt.m4s\n#EXT-X-ENDLIST\n"))
			return
		}
		step := m3u8Step.Add(1)
		switch step {
		case 1:
			// Part 1: seq 100
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:100\n#EXT-X-MAP:URI=\"/hls/init1.mp4\"\n#EXTINF:1.0,\n/hls/seg100.m4s\n"))
		case 2:
			// Sequence gap > 3 (100 -> 105) + DISCONTINUITY -> triggers rollover to part-002!
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:105\n#EXT-X-MAP:URI=\"/hls/init1.mp4\"\n#EXT-X-DISCONTINUITY\n#EXTINF:1.0,\n/hls/seg105.m4s\n"))
		default:
			// Part 2 finishes on ENDLIST
			room9001Live.Store(false)
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:105\n#EXT-X-MAP:URI=\"/hls/init1.mp4\"\n#EXTINF:1.0,\n/hls/seg105.m4s\n#EXT-X-ENDLIST\n"))
		}
	})
	mux.HandleFunc("/hls/init1.mp4", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(initBytes) })
	mux.HandleFunc("/hls/seg100.m4s", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(fragBytes) })
	mux.HandleFunc("/hls/seg105.m4s", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(fragBytes) })
	mux.HandleFunc("/hls/corrupt.mp4", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(initBytes) })
	mux.HandleFunc("/hls/corrupt.m4s", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("not-an-mp4-frag")) })

	srv := httptest.NewServer(mux)
	defer srv.Close()
	srvURL = srv.URL

	tmp := t.TempDir()
	db, _, _, _ := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
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
	liveCfg := settings.Default(cacheDir).Live
	liveCfg.MaxConcurrentRecordings = 1
	liveMgr := NewManager(lib, subMgr, client, liveCfg, nil, nil)
	liveMgr.hlsPollInterval = 20 * time.Millisecond
	liveMgr.reconnectWait = 20 * time.Millisecond

	now := time.Date(2026, 11, 7, 15, 0, 0, 0, time.UTC)
	liveMgr.nowFunc = func() time.Time { return now }
	var freeDisk atomic.Uint64
	freeDisk.Store(10 << 30)
	liveMgr.freeBytesFunc = func(string) (uint64, error) { return freeDisk.Load(), nil }

	ctx := context.Background()

	// 1. 60s Status cache verification
	st1, err := liveMgr.Status(ctx, []int64{2001, 2002})
	if err != nil || len(st1) != 2 || statusHits.Load() != 1 {
		t.Fatalf("Status call 1: len=%d hits=%d err=%v", len(st1), statusHits.Load(), err)
	}
	now = now.Add(30 * time.Second)
	_, err = liveMgr.Status(ctx, []int64{2001, 2002})
	if err != nil || statusHits.Load() != 1 {
		t.Fatalf("Status call within 60s should use cache, hits=%d err=%v", statusHits.Load(), err)
	}
	now = now.Add(31 * time.Second)
	_, err = liveMgr.Status(ctx, []int64{2001})
	if err != nil || statusHits.Load() != 2 {
		t.Fatalf("Status call after 60s should re-fetch, hits=%d err=%v", statusHits.Load(), err)
	}

	// 2. Sequence gap > 3 + DISCONTINUITY rollover into part-001.mp4 and part-002.mp4
	rec1, err := liveMgr.StartRecording(ctx, 9001)
	if err != nil {
		t.Fatalf("StartRecording 9001: %v", err)
	}
	// While 9001 is recording and MaxConcurrentRecordings == 1, starting 9002 must fail!
	if _, err := liveMgr.StartRecording(ctx, 9002); err == nil {
		t.Fatalf("expected StartRecording 9002 to fail when MaxConcurrentRecordings=1 is reached")
	}
	// Wait for 9001 to finish
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rooms := liveMgr.ListRooms()
		active := false
		for _, r := range rooms {
			if r.RoomID == 9001 && r.Recording {
				active = true
			}
		}
		if !active {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, f := range []string{"part-001.mp4", "part-002.mp4"} {
		if _, err := os.Stat(filepath.Join(rec1.RecordItemDir, f)); err != nil {
			t.Fatalf("expected %s after discontinuity/gap rollover: %v", f, err)
		}
	}

	// 3. Conversion failure preserves .part on disk and marks item "partial" (R-14)
	now = now.Add(10 * time.Second)
	corruptMode.Store(true)
	recCorrupt, err := liveMgr.StartRecording(ctx, 9002)
	if err != nil {
		t.Fatalf("StartRecording 9002 corrupt: %v", err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rooms := liveMgr.ListRooms()
		active := false
		for _, r := range rooms {
			if r.RoomID == 9002 && r.Recording {
				active = true
			}
		}
		if !active {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	partPath := filepath.Join(recCorrupt.RecordItemDir, "part-001.mp4.part")
	if _, err := os.Stat(partPath); err != nil {
		t.Fatalf("expected corrupt .part file to be preserved on disk: %v", err)
	}
	rawMeta, err := os.ReadFile(filepath.Join(recCorrupt.RecordItemDir, "item.json"))
	if err != nil {
		t.Fatalf("ReadFile item.json: %v", err)
	}
	var metaCorrupt library.ItemMetadata
	if err := json.Unmarshal(rawMeta, &metaCorrupt); err != nil || metaCorrupt.Status != "partial" || len(metaCorrupt.Missing) == 0 {
		t.Fatalf("expected partial status with Missing record on conversion failure, got %+v err=%v", metaCorrupt, err)
	}

	// 4. Low disk space (< 1 GiB) rejects recording
	room9001Live.Store(true)
	freeDisk.Store(512 << 20) // 512 MiB < 1 GiB
	if _, err := liveMgr.StartRecording(ctx, 9001); err == nil {
		t.Fatalf("expected StartRecording to fail when free disk space < 1 GiB")
	}
	liveMgr.Stop()
}
