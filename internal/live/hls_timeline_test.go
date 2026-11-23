package live

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/library"
)

// A Bilibili HLS part converts even when the EXTINF sum the recorder kept
// differs from the media timeline (real parts drifted 0.4 s over ten minutes),
// and recovery clears the part's earlier "convert failed" record.
func TestRecoveryAcceptsEXTINFDriftAndClearsConvertFailed(t *testing.T) {
	mgr, lib, _ := regressionRecorder(t, http.NotFoundHandler())
	init, fragment := regressionLiveSamples(t)
	session := regressionLiveSession(t, lib)
	raw := append(append([]byte(nil), init...), fragment...)
	part := filepath.Join(session.itemDir, "part-001.mp4.part")
	if err := os.WriteFile(part, raw, 0600); err != nil {
		t.Fatal(err)
	}
	session.itemMeta.Live.PendingFile, session.itemMeta.Live.PendingBytes, session.itemMeta.Live.PendingDuration = "part-001.mp4.part", int64(len(raw)), 1.6
	session.itemMeta.Live.Incidents = []library.LiveIncident{
		{Kind: library.LiveIncidentConvertFailed, At: "2026-11-15T15:34:32Z", Part: "part-001.mp4.part", Count: 1, Reason: "直播录像转换失败：直播录像轨道或时长校验失败"},
		{Kind: library.LiveIncidentReconnect, At: "2026-11-15T15:40:42Z", Part: "part-002.mp4.part", Count: 1, DurationSec: 10.3},
	}
	if _, err := lib.SaveItem(session.itemDir, session.itemMeta); err != nil {
		t.Fatal(err)
	}
	mgr.RecoverOrphanParts()
	meta, _, err := lib.GetItem(session.itemMeta.ItemKey)
	if err != nil || len(meta.PendingParts) != 0 || len(meta.Live.Segments) != 1 || meta.Status != "complete" {
		t.Fatalf("drifted part did not convert: %+v %v", meta, err)
	}
	if got := meta.Live.Incidents; len(got) != 1 || got[0].Kind != library.LiveIncidentReconnect {
		t.Fatalf("convert-failed record not cleared or other records lost: %+v", got)
	}
}

// A sequence gap longer than the time since the previous segment is a restarted
// stream's new numbering, not lost segments; a plausible gap is still recorded.
func TestHLSSequenceResetIsNotLostSegments(t *testing.T) {
	for _, tc := range []struct {
		name     string
		next     string
		wantLost int
	}{
		{"restarted stream", "100000008", 0},
		{"real gap", "10", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			init, fragment := regressionLiveSamples(t)
			var hits atomic.Int32
			mgr, lib, base := regressionRecorder(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/index":
					if hits.Add(1) == 1 {
						_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:7\n#EXT-X-MAP:URI=\"/init\"\n#EXTINF:1,\n/fragment\n"))
						return
					}
					_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:" + tc.next + "\n#EXT-X-MAP:URI=\"/init\"\n#EXTINF:1,\n/fragment\n#EXT-X-ENDLIST\n"))
				case "/init":
					_, _ = w.Write(init)
				case "/fragment":
					_, _ = w.Write(fragment)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			session := regressionLiveSession(t, lib)
			_, err := mgr.recordHLSSegment(context.Background(), session, []string{base + "/index"}, filepath.Join(session.itemDir, "part-001.mp4.part"), true)
			if errors.Is(err, errSegmentRollover) {
				// A large jump closes the part; the next part starts at the new number.
				session.wroteMedia = false
				_, err = mgr.recordHLSSegment(context.Background(), session, []string{base + "/index"}, filepath.Join(session.itemDir, "part-002.mp4.part"), true)
			}
			if err != nil {
				t.Fatal(err)
			}
			lost := 0
			for _, inc := range session.incidents {
				if inc.Kind == library.LiveIncidentLostSegments {
					lost += inc.LostSegments
				}
			}
			if lost != tc.wantLost {
				t.Fatalf("lost segments = %d, want %d: %+v", lost, tc.wantLost, session.incidents)
			}
		})
	}
}

func waitLiveRecordingDone(t *testing.T, mgr *Manager, roomID int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mgr.mu.Lock()
		_, active := mgr.sessions[roomID]
		mgr.mu.Unlock()
		if !active {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("recording session for room %d did not finish within timeout", roomID)
}

func buildSyntheticAVCFLVBytes(t *testing.T) []byte {
	t.Helper()
	sps := mustHex(t, testAVCSPSHex)
	pps := mustHex(t, testAVCPPSHex)
	idr := mustHex(t, testAVCSliceHex)
	pSlice := mustHex(t, testAVCPSlice)

	fb := newFLVBuilder(0x05)
	fb.writeClassicVideoConfig(0, flvVideoCodecAVC, buildAVCDecConfBytes(t, sps, pps, 4))
	fb.writeAACConfig(0, testAACConfig48kStereo)

	audioTS := uint32(0)
	for i := 0; i < 60; i++ {
		vTS := uint32(i * 100 / 3)
		for audioTS <= vTS {
			fb.writeAACFrame(audioTS, testAACSilentFrame)
			audioTS += 21
		}
		isKey := i%30 == 0
		slice := pSlice
		if isKey {
			slice = idr
		}
		fb.writeClassicVideoFrame(vTS, flvVideoCodecAVC, isKey, 0, packLengthPrefixedNALUs(4, slice))
	}
	return fb.buf.Bytes()
}

// Resuming an existing Bilibili live item.json from disk must anchor lastSequenceAt
// to the previous session's EndedAt/StartedAt so a +100,000,201 CDN sequence rebase
// never records bogus lost_segments, while a genuine small gap within wall time does.
func TestResumedHLSStartRecordingSequenceRebaseNotLostSegments(t *testing.T) {
	const (
		roomID    int64 = 1966814385
		uid       int64 = 1001
		liveSince int64 = 1795353122
	)
	now := time.Date(2026, 11, 22, 14, 13, 50, 0, time.UTC)

	for _, tc := range []struct {
		name      string
		prevSeq   int64
		lastSeqAt time.Duration
		endedAgo  time.Duration
		nextSeq   int64
		wantLost  int
	}{
		{
			name:     "resumed +100000201 sequence rebase",
			prevSeq:  1795353122,
			endedAgo: 3*time.Minute + 19*time.Second,
			nextSeq:  1795353122 + 100000201,
			wantLost: 0,
		},
		{
			name:     "resumed genuine 2-segment gap",
			prevSeq:  100,
			endedAgo: 5 * time.Second,
			nextSeq:  103,
			wantLost: 2,
		},
		{
			name:      "prefers LastSequenceAt ahead of EndedAt",
			prevSeq:   100,
			lastSeqAt: 5 * time.Second,
			endedAgo:  50 * time.Minute,
			nextSeq:   161,
			wantLost:  0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			initBytes, fragBytes := regressionLiveSamples(t)
			var stillLive atomic.Bool
			stillLive.Store(true)
			var srvURL string

			mux := http.NewServeMux()
			mux.HandleFunc("/room/v1/Room/room_init", func(w http.ResponseWriter, r *http.Request) {
				ls := 0
				if stillLive.Load() {
					ls = 1
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code": 0,
					"data": map[string]any{
						"room_id":     roomID,
						"uid":         uid,
						"live_status": ls,
						"live_time":   liveSince,
					},
				})
			})
			mux.HandleFunc("/room/v1/Room/get_info", func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code": 0,
					"data": map[string]any{
						"room_id":     roomID,
						"uid":         uid,
						"title":       "Resumed Broadcast",
						"live_status": 1,
						"live_time":   "2026-11-22 21:12:02",
					},
				})
			})
			mux.HandleFunc("/room/v1/Room/get_status_info_by_uids", func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code": 0,
					"data": map[string]any{
						"1001": map[string]any{
							"uid":         uid,
							"room_id":     roomID,
							"uname":       "Streamer1001",
							"title":       "Resumed Broadcast",
							"live_status": 1,
							"live_time":   liveSince,
						},
					},
				})
			})
			mux.HandleFunc("/xlive/web-room/v2/index/getRoomPlayInfo", func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code": 0,
					"data": map[string]any{
						"room_id":     roomID,
						"uid":         uid,
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
														"url_info":   []map[string]any{{"host": srvURL, "extra": ""}},
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
				stillLive.Store(false)
				_, _ = fmt.Fprintf(w, "#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:%d\n#EXT-X-MAP:URI=\"/hls/init.m4s\"\n#EXTINF:1,\n/hls/seg.m4s\n#EXT-X-ENDLIST\n", tc.nextSeq)
			})
			mux.HandleFunc("/hls/init.m4s", func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write(initBytes)
			})
			mux.HandleFunc("/hls/seg.m4s", func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write(fragBytes)
			})

			mgr, lib, _, srv, _ := setupLiveTestHarness(t, mux)
			srvURL = srv.URL
			mgr.SetNowFunc(func() time.Time { return now })
			mgr.mu.Lock()
			mgr.hlsPollInterval = 10 * time.Millisecond
			mgr.reconnectWait = 10 * time.Millisecond
			mgr.mu.Unlock()

			// Seed an existing finalized Session #1 item.json on disk for the same broadcast.
			sessionID := fmt.Sprintf("%d:%d", roomID, liveSince)
			itemDir := filepath.Join(lib.CacheDir(), "bilibili", "1001", "lives", fmt.Sprintf("%d-%d", roomID, liveSince))
			if err := os.MkdirAll(itemDir, 0o700); err != nil {
				t.Fatal(err)
			}
			seedLastSeqAt := ""
			if tc.lastSeqAt > 0 {
				seedLastSeqAt = now.Add(-tc.lastSeqAt).Format(time.RFC3339Nano)
			}
			if _, err := lib.SaveItem(itemDir, library.ItemMetadata{
				Version: 1,
				ID:      "live:" + sessionID,
				Site:    "bilibili",
				Kind:    "live",
				Status:  "complete",
				Title:   "Resumed Broadcast",
				PubTs:   liveSince,
				Author:  library.ItemAuthor{Mid: uid, Name: "Streamer1001"},
				Live: &library.LiveRecordingInfo{
					RoomID:         roomID,
					LiveSince:      liveSince,
					StartedAt:      now.Add(-55 * time.Minute).Format(time.RFC3339),
					EndedAt:        now.Add(-tc.endedAgo).Format(time.RFC3339),
					StopReason:     "ended",
					LastSequence:   tc.prevSeq,
					LastSequenceAt: seedLastSeqAt,
					HasSequence:    true,
				},
			}); err != nil {
				t.Fatalf("seed prior session item.json: %v", err)
			}

			if _, err := mgr.StartRecording(context.Background(), roomID); err != nil {
				t.Fatalf("StartRecording resume: %v", err)
			}
			waitLiveRecordingDone(t, mgr, roomID)

			meta, ok, err := lib.GetItem(library.CanonicalItemKey("bilibili", "live", sessionID))
			if err != nil || !ok || meta.Live == nil {
				t.Fatalf("reload resumed item.json: ok=%v err=%v", ok, err)
			}
			if meta.Live.LastSequence != tc.nextSeq {
				t.Fatalf("LastSequence = %d, want %d", meta.Live.LastSequence, tc.nextSeq)
			}
			if ts, parseErr := time.Parse(time.RFC3339Nano, meta.Live.LastSequenceAt); parseErr != nil || ts.IsZero() {
				t.Fatalf("LastSequenceAt = %q, parseErr=%v, want persisted RFC3339Nano timestamp", meta.Live.LastSequenceAt, parseErr)
			}
			lost := 0
			for _, inc := range meta.Live.Incidents {
				if inc.Kind == library.LiveIncidentLostSegments {
					lost += inc.LostSegments
				}
			}
			if lost != tc.wantLost {
				t.Fatalf("resumed lost_segments = %d, want %d (incidents=%+v)", lost, tc.wantLost, meta.Live.Incidents)
			}
		})
	}
}

// Switching between HTTP-FLV and HLS-fMP4 must keep sequence namespaces isolated so
// FLV GOP chunk counters never pollute HLS MEDIA-SEQUENCE or trigger false lost_segments.
func TestFLVToHLSTransportSwitchIsolatesSequenceNamespaces(t *testing.T) {
	const (
		roomID    int64 = 1966814386
		uid       int64 = 1001
		liveSince int64 = 1795354000
	)
	initBytes, fragBytes := regressionLiveSamples(t)
	flvBytes := buildSyntheticAVCFLVBytes(t)

	var playInfoCalls atomic.Int32
	var stillLive atomic.Bool
	stillLive.Store(true)
	var srvURL string

	mux := http.NewServeMux()
	mux.HandleFunc("/room/v1/Room/room_init", func(w http.ResponseWriter, r *http.Request) {
		ls := 0
		if stillLive.Load() {
			ls = 1
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"room_id": roomID, "uid": uid, "live_status": ls, "live_time": liveSince},
		})
	})
	mux.HandleFunc("/room/v1/Room/get_info", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"room_id": roomID, "uid": uid, "title": "FLV to HLS Switch", "live_status": 1},
		})
	})
	mux.HandleFunc("/room/v1/Room/get_status_info_by_uids", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"1001": map[string]any{"uid": uid, "room_id": roomID, "uname": "Streamer1001", "title": "FLV to HLS Switch", "live_status": 1, "live_time": liveSince},
			},
		})
	})
	mux.HandleFunc("/xlive/web-room/v2/index/getRoomPlayInfo", func(w http.ResponseWriter, r *http.Request) {
		call := playInfoCalls.Add(1)
		var stream map[string]any
		if call == 1 {
			stream = map[string]any{
				"protocol_name": "http_stream",
				"format": []map[string]any{
					{
						"format_name": "flv",
						"codec": []map[string]any{
							{"codec_name": "avc", "current_qn": 10000, "base_url": "/live.flv", "url_info": []map[string]any{{"host": srvURL, "extra": ""}}},
						},
					},
				},
			}
		} else {
			stream = map[string]any{
				"protocol_name": "http_hls",
				"format": []map[string]any{
					{
						"format_name": "fmp4",
						"codec": []map[string]any{
							{"codec_name": "avc", "current_qn": 10000, "base_url": "/hls/index.m3u8", "url_info": []map[string]any{{"host": srvURL, "extra": ""}}},
						},
					},
				},
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"room_id":     roomID,
				"uid":         uid,
				"live_status": 1,
				"playurl_info": map[string]any{
					"playurl": map[string]any{"stream": []map[string]any{stream}},
				},
			},
		})
	})
	mux.HandleFunc("/live.flv", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/x-flv")
		_, _ = w.Write(flvBytes)
	})
	mux.HandleFunc("/hls/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
		stillLive.Store(false)
		_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:500000\n#EXT-X-MAP:URI=\"/hls/init.m4s\"\n#EXTINF:1,\n/hls/seg.m4s\n#EXT-X-ENDLIST\n"))
	})
	mux.HandleFunc("/hls/init.m4s", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(initBytes) })
	mux.HandleFunc("/hls/seg.m4s", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(fragBytes) })

	mgr, lib, _, srv, _ := setupLiveTestHarness(t, mux)
	srvURL = srv.URL
	mgr.mu.Lock()
	mgr.hlsPollInterval = 10 * time.Millisecond
	mgr.reconnectWait = 10 * time.Millisecond
	mgr.mu.Unlock()

	if _, err := mgr.StartRecording(context.Background(), roomID); err != nil {
		t.Fatalf("StartRecording FLV->HLS: %v", err)
	}
	waitLiveRecordingDone(t, mgr, roomID)

	sessionID := fmt.Sprintf("%d:%d", roomID, liveSince)
	meta, ok, err := lib.GetItem(library.CanonicalItemKey("bilibili", "live", sessionID))
	if err != nil || !ok || meta.Live == nil {
		t.Fatalf("reload FLV->HLS item.json: ok=%v err=%v", ok, err)
	}
	if playInfoCalls.Load() < 2 {
		t.Fatalf("expected reconnect from FLV to HLS, playInfoCalls=%d", playInfoCalls.Load())
	}
	if meta.Live.LastSequence != 500000 || !meta.Live.HasSequence {
		t.Fatalf("expected HLS sequence 500000 after switch, got (%d, %v)", meta.Live.LastSequence, meta.Live.HasSequence)
	}
	for _, inc := range meta.Live.Incidents {
		if inc.Kind == library.LiveIncidentLostSegments {
			t.Fatalf("FLV->HLS switch must not emit bogus lost_segments: %+v", meta.Live.Incidents)
		}
	}
}

// Transient malformed M3U8 or truncated fMP4 after media has already been recorded
// should recover via stream reconnect; persistent or first-part schema mismatch
// must still terminate cleanly with stopReason="schema_changed".
func TestTransientMalformedM3U8AndTruncatedFragmentRecoverViaReconnect(t *testing.T) {
	for _, tc := range []struct {
		name           string
		mode           string
		wantStopReason string
		wantStatus     string
		wantPlayCalls  int32
		wantParts      int
	}{
		{
			name:           "transient malformed m3u8 recovers",
			mode:           "transient_m3u8",
			wantStopReason: "ended",
			wantStatus:     "complete",
			wantPlayCalls:  2,
			wantParts:      2,
		},
		{
			name:           "transient truncated fragment recovers",
			mode:           "transient_frag",
			wantStopReason: "ended",
			wantStatus:     "complete",
			wantPlayCalls:  2,
			wantParts:      2,
		},
		{
			name:           "persistent malformed m3u8 escalates after bounded retries",
			mode:           "persistent_m3u8",
			wantStopReason: "schema_changed",
			wantStatus:     "partial",
			wantPlayCalls:  3, // initial + 2 consecutive retries
			wantParts:      1,
		},
		{
			name:           "initial malformed m3u8 with no recorded media stops immediately",
			mode:           "initial_m3u8",
			wantStopReason: "schema_changed",
			wantStatus:     "partial",
			wantPlayCalls:  1, // 0 retries when no media recorded yet
			wantParts:      0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const (
				roomID    int64 = 1966814390
				uid       int64 = 1001
				liveSince int64 = 1795355000
			)
			initBytes, fragBytes := regressionLiveSamples(t)
			var playCalls atomic.Int32
			var m3u8ACalls atomic.Int32
			var stillLive atomic.Bool
			stillLive.Store(true)
			var srvURL string

			mux := http.NewServeMux()
			mux.HandleFunc("/room/v1/Room/room_init", func(w http.ResponseWriter, r *http.Request) {
				ls := 0
				if stillLive.Load() {
					ls = 1
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code": 0,
					"data": map[string]any{"room_id": roomID, "uid": uid, "live_status": ls, "live_time": liveSince},
				})
			})
			mux.HandleFunc("/room/v1/Room/get_info", func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code": 0,
					"data": map[string]any{"room_id": roomID, "uid": uid, "title": "Transient Schema Test", "live_status": 1},
				})
			})
			mux.HandleFunc("/room/v1/Room/get_status_info_by_uids", func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code": 0,
					"data": map[string]any{
						"1001": map[string]any{"uid": uid, "room_id": roomID, "uname": "Streamer1001", "title": "Transient Schema Test", "live_status": 1, "live_time": liveSince},
					},
				})
			})
			mux.HandleFunc("/xlive/web-room/v2/index/getRoomPlayInfo", func(w http.ResponseWriter, r *http.Request) {
				c := playCalls.Add(1)
				basePath := "/hls/a.m3u8"
				if c >= 2 {
					basePath = "/hls/b.m3u8"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code": 0,
					"data": map[string]any{
						"room_id":     roomID,
						"uid":         uid,
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
													{"codec_name": "avc", "current_qn": 10000, "base_url": basePath, "url_info": []map[string]any{{"host": srvURL, "extra": ""}}},
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
			mux.HandleFunc("/hls/a.m3u8", func(w http.ResponseWriter, r *http.Request) {
				hit := m3u8ACalls.Add(1)
				switch tc.mode {
				case "initial_m3u8":
					_, _ = w.Write([]byte("<html>502 bad gateway</html>"))
				case "transient_m3u8", "persistent_m3u8":
					if hit == 1 {
						_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:10\n#EXT-X-MAP:URI=\"/hls/init.m4s\"\n#EXTINF:1,\n/hls/good.m4s\n"))
						return
					}
					_, _ = w.Write([]byte("<html>502 bad gateway</html>"))
				case "transient_frag":
					if hit == 1 {
						_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:10\n#EXT-X-MAP:URI=\"/hls/init.m4s\"\n#EXTINF:1,\n/hls/good.m4s\n"))
						return
					}
					_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:11\n#EXT-X-MAP:URI=\"/hls/init.m4s\"\n#EXTINF:1,\n/hls/bad.m4s\n"))
				}
			})
			mux.HandleFunc("/hls/b.m3u8", func(w http.ResponseWriter, r *http.Request) {
				if tc.mode == "persistent_m3u8" {
					_, _ = w.Write([]byte("<html>502 still broken</html>"))
					return
				}
				stillLive.Store(false)
				_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:12\n#EXT-X-MAP:URI=\"/hls/init.m4s\"\n#EXTINF:1,\n/hls/good.m4s\n#EXT-X-ENDLIST\n"))
			})
			mux.HandleFunc("/hls/init.m4s", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(initBytes) })
			mux.HandleFunc("/hls/good.m4s", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(fragBytes) })
			mux.HandleFunc("/hls/bad.m4s", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("truncated-corrupt-fragment")) })

			mgr, lib, _, srv, _ := setupLiveTestHarness(t, mux)
			srvURL = srv.URL
			mgr.mu.Lock()
			mgr.hlsPollInterval = 10 * time.Millisecond
			mgr.reconnectWait = 10 * time.Millisecond
			mgr.mu.Unlock()

			if _, err := mgr.StartRecording(context.Background(), roomID); err != nil {
				t.Fatalf("StartRecording: %v", err)
			}
			waitLiveRecordingDone(t, mgr, roomID)

			sessionID := fmt.Sprintf("%d:%d", roomID, liveSince)
			meta, ok, err := lib.GetItem(library.CanonicalItemKey("bilibili", "live", sessionID))
			if err != nil || !ok || meta.Live == nil {
				t.Fatalf("reload item.json: ok=%v err=%v", ok, err)
			}
			if playCalls.Load() != tc.wantPlayCalls {
				t.Fatalf("LiveRoomPlayInfo calls = %d, want %d", playCalls.Load(), tc.wantPlayCalls)
			}
			if meta.Live.StopReason != tc.wantStopReason {
				t.Fatalf("StopReason = %q, want %q", meta.Live.StopReason, tc.wantStopReason)
			}
			if meta.Status != tc.wantStatus {
				t.Fatalf("Status = %q, want %q (missing=%+v)", meta.Status, tc.wantStatus, meta.Missing)
			}
			if len(meta.Live.Segments) != tc.wantParts {
				t.Fatalf("recorded segments = %d, want %d", len(meta.Live.Segments), tc.wantParts)
			}
		})
	}
}

// Isolated transient M3U8 glitches separated by healthy media progress must reset
// consecutiveTransientSchema on each successful part so a long multi-hour recording
// does not accumulate lifetime glitches and abort on the 3rd isolated blip.
func TestIsolatedTransientM3U8GlitchesAfterMediaProgressResetCounter(t *testing.T) {
	const (
		roomID    int64 = 1966814391
		uid       int64 = 1001
		liveSince int64 = 1795356000
	)
	initBytes, fragBytes := regressionLiveSamples(t)
	var playCalls atomic.Int32
	var m3u8Hits [4]atomic.Int32
	var stillLive atomic.Bool
	stillLive.Store(true)
	var srvURL string

	mux := http.NewServeMux()
	mux.HandleFunc("/room/v1/Room/room_init", func(w http.ResponseWriter, r *http.Request) {
		ls := 0
		if stillLive.Load() {
			ls = 1
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"room_id": roomID, "uid": uid, "live_status": ls, "live_time": liveSince},
		})
	})
	mux.HandleFunc("/room/v1/Room/get_info", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"room_id": roomID, "uid": uid, "title": "Isolated Glitch Reset Test", "live_status": 1},
		})
	})
	mux.HandleFunc("/room/v1/Room/get_status_info_by_uids", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"1001": map[string]any{"uid": uid, "room_id": roomID, "uname": "Streamer1001", "title": "Isolated Glitch Reset Test", "live_status": 1, "live_time": liveSince},
			},
		})
	})
	mux.HandleFunc("/xlive/web-room/v2/index/getRoomPlayInfo", func(w http.ResponseWriter, r *http.Request) {
		c := playCalls.Add(1)
		if c > 4 {
			c = 4
		}
		basePath := fmt.Sprintf("/hls/p%d.m3u8", c)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"room_id":     roomID,
				"uid":         uid,
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
											{"codec_name": "avc", "current_qn": 10000, "base_url": basePath, "url_info": []map[string]any{{"host": srvURL, "extra": ""}}},
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
	// Parts 1, 2, 3 each record 1 valid media fragment before hitting an isolated malformed M3U8.
	// Part 4 records 1 valid media fragment and ends cleanly with #EXT-X-ENDLIST.
	for i := 1; i <= 4; i++ {
		idx := i
		mux.HandleFunc(fmt.Sprintf("/hls/p%d.m3u8", idx), func(w http.ResponseWriter, r *http.Request) {
			hit := m3u8Hits[idx-1].Add(1)
			seq := 100 + idx
			if idx == 4 {
				stillLive.Store(false)
				_, _ = fmt.Fprintf(w, "#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:%d\n#EXT-X-MAP:URI=\"/hls/init.m4s\"\n#EXTINF:1,\n/hls/good.m4s\n#EXT-X-ENDLIST\n", seq)
				return
			}
			if hit == 1 {
				_, _ = fmt.Fprintf(w, "#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:%d\n#EXT-X-MAP:URI=\"/hls/init.m4s\"\n#EXTINF:1,\n/hls/good.m4s\n", seq)
				return
			}
			_, _ = w.Write([]byte("<html>502 isolated CDN glitch</html>"))
		})
	}
	mux.HandleFunc("/hls/init.m4s", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(initBytes) })
	mux.HandleFunc("/hls/good.m4s", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(fragBytes) })

	mgr, lib, _, srv, _ := setupLiveTestHarness(t, mux)
	srvURL = srv.URL
	mgr.mu.Lock()
	mgr.hlsPollInterval = 10 * time.Millisecond
	mgr.reconnectWait = 10 * time.Millisecond
	mgr.mu.Unlock()

	if _, err := mgr.StartRecording(context.Background(), roomID); err != nil {
		t.Fatalf("StartRecording: %v", err)
	}
	waitLiveRecordingDone(t, mgr, roomID)

	sessionID := fmt.Sprintf("%d:%d", roomID, liveSince)
	meta, ok, err := lib.GetItem(library.CanonicalItemKey("bilibili", "live", sessionID))
	if err != nil || !ok || meta.Live == nil {
		t.Fatalf("reload item.json: ok=%v err=%v", ok, err)
	}
	if playCalls.Load() != 4 {
		t.Fatalf("LiveRoomPlayInfo calls = %d, want 4 (3 isolated glitches recovered + clean end)", playCalls.Load())
	}
	if meta.Live.StopReason != "ended" {
		t.Fatalf("StopReason = %q, want ended", meta.Live.StopReason)
	}
	if meta.Status != "complete" {
		t.Fatalf("Status = %q, want complete (missing=%+v)", meta.Status, meta.Missing)
	}
	if len(meta.Live.Segments) != 4 {
		t.Fatalf("recorded segments = %d, want 4", len(meta.Live.Segments))
	}
}
