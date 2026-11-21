package live

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/subscription"
)

func TestBilibiliHLSRecordingPreviewStateRolloverAndZeroExtraHTTP(t *testing.T) {
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

	var totalHTTPHits atomic.Int64

	var srvURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/room/v1/Room/get_status_info_by_uids", func(w http.ResponseWriter, r *http.Request) {
		totalHTTPHits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"2001": map[string]any{
					"uid":         2001,
					"room_id":     9001,
					"title":       "HLS Preview Broadcast",
					"uname":       "Streamer2001",
					"live_status": 1,
					"live_time":   1794024000,
				},
			},
		})
	})
	mux.HandleFunc("/room/v1/Room/room_init", func(w http.ResponseWriter, r *http.Request) {
		totalHTTPHits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"room_id":     9001,
				"short_id":    91,
				"uid":         2001,
				"live_status": 1,
				"live_time":   1794024000,
			},
		})
	})
	mux.HandleFunc("/xlive/web-room/v2/index/getRoomPlayInfo", func(w http.ResponseWriter, r *http.Request) {
		totalHTTPHits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"room_id":     9001,
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
		totalHTTPHits.Add(1)
		_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:10\n#EXT-X-MAP:URI=\"/hls/init.mp4\"\n#EXTINF:1.0,\n/hls/seg1.m4s\n"))
	})
	mux.HandleFunc("/hls/init.mp4", func(w http.ResponseWriter, r *http.Request) {
		totalHTTPHits.Add(1)
		_, _ = w.Write(initBytes)
	})
	mux.HandleFunc("/hls/seg1.m4s", func(w http.ResponseWriter, r *http.Request) {
		totalHTTPHits.Add(1)
		_, _ = w.Write(fragBytes)
	})
	mux.HandleFunc("/hls/seg2.m4s", func(w http.ResponseWriter, r *http.Request) {
		totalHTTPHits.Add(1)
		_, _ = w.Write(fragBytes)
	})

	liveMgr, _, subMgr, srv, _ := setupLiveTestHarness(t, mux)
	srvURL = srv.URL
	liveMgr.hlsPollInterval = 40 * time.Millisecond

	_, err = subMgr.Upsert(subscription.Subscription{
		Mid:        2001,
		Uname:      "Streamer2001",
		Enabled:    true,
		LiveNotify: true,
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	ctx := context.Background()
	if _, err := liveMgr.PollNow(ctx); err != nil {
		t.Fatalf("PollNow: %v", err)
	}
	if _, err := liveMgr.StartRecording(ctx, 9001); err != nil {
		t.Fatalf("StartRecording: %v", err)
	}

	// Wait for part-001.mp4.part to become ready for live preview.
	var pv1 RecordingPreview
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		pv, ok := liveMgr.RecordingPreview("9001")
		if ok && pv.Recording && pv.PartReady && pv.PartIndex == 1 && pv.PartBytes > int64(len(initBytes)) {
			pv1 = pv
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if !pv1.PartReady || !pv1.PreviewSupported {
		t.Fatalf("expected ready HLS preview on part 1, got %+v", pv1)
	}
	if pv1.Transport != "hls_fmp4" || pv1.AudioOnly || pv1.PreviewReason != "" {
		t.Fatalf("unexpected preview metadata: %+v", pv1)
	}
	if !strings.HasSuffix(pv1.PartPath, "part-001.mp4.part") {
		t.Fatalf("unexpected PartPath %q", pv1.PartPath)
	}

	// Pause HLS polling interval temporarily to prove RecordingPreview / ListRooms issue 0 HTTP hits.
	liveMgr.mu.Lock()
	liveMgr.hlsPollInterval = 10 * time.Second
	liveMgr.mu.Unlock()
	time.Sleep(60 * time.Millisecond)
	hitsBefore := totalHTTPHits.Load()
	for i := 0; i < 40; i++ {
		pv, ok := liveMgr.RecordingPreview("bilibili:live:9001")
		if !ok || !pv.Recording || !pv.PreviewSupported || pv.PartPath != pv1.PartPath {
			t.Fatalf("RecordingPreview iteration %d mismatch: ok=%v pv=%+v", i, ok, pv)
		}
		rooms := liveMgr.ListRooms()
		if len(rooms) != 1 || !rooms[0].PreviewSupported || !rooms[0].RecordPartReady || rooms[0].RecordPartPath != pv1.PartPath {
			t.Fatalf("ListRooms iteration %d mismatch: %+v", i, rooms)
		}
	}
	if got := totalHTTPHits.Load(); got != hitsBefore {
		t.Fatalf("preview queries triggered %d unexpected HTTP hits", got-hitsBefore)
	}

	// Stop the active recording cleanly and verify part-001.mp4 finalizes and preview resets.
	_, _ = liveMgr.StopRecording(9001)
	waitUntilRoomIdle(t, liveMgr, "bilibili:live:9001", 4*time.Second)

	finalPart1 := strings.TrimSuffix(pv1.PartPath, ".part")
	summary1, err := download.InspectMP4File(finalPart1)
	if err != nil || !summary1.MoovBeforeMdat {
		t.Fatalf("expected finalized moov-before-mdat %s: summary=%+v err=%v", finalPart1, summary1, err)
	}
	if _, err := os.Stat(pv1.PartPath); !os.IsNotExist(err) {
		t.Fatalf("expected .part file removed after finalization, stat err=%v", err)
	}
	pvAfter, okAfter := liveMgr.RecordingPreview("9001")
	if !okAfter || pvAfter.Recording || pvAfter.PartReady || pvAfter.PartPath != "" {
		t.Fatalf("expected inactive RecordingPreview after stop, got ok=%v pv=%+v", okAfter, pvAfter)
	}
}

func TestBilibiliHLSRecordingPreviewSegmentRolloverAdvancesPartIndex(t *testing.T) {
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

	var triggerRollover atomic.Bool
	var servedRolloverErr atomic.Bool
	var srvURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/room/v1/Room/get_status_info_by_uids", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"2002": map[string]any{
					"uid":         2002,
					"room_id":     9002,
					"title":       "Rollover Room",
					"uname":       "Streamer2002",
					"live_status": 1,
					"live_time":   1794024000,
				},
			},
		})
	})
	mux.HandleFunc("/room/v1/Room/room_init", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"room_id": 9002, "uid": 2002, "live_status": 1, "live_time": 1794024000},
		})
	})
	mux.HandleFunc("/xlive/web-room/v2/index/getRoomPlayInfo", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"room_id": 9002, "uid": 2002, "live_status": 1,
				"playurl_info": map[string]any{
					"playurl": map[string]any{
						"stream": []map[string]any{{
							"protocol_name": "http_hls",
							"format": []map[string]any{{
								"format_name": "fmp4",
								"codec": []map[string]any{{
									"codec_name": "avc",
									"current_qn": 10000,
									"base_url":   "/hls/index.m3u8",
									"url_info":   []map[string]any{{"host": srvURL, "extra": "?e=1"}},
								}},
							}},
						}},
					},
				},
			},
		})
	})
	mux.HandleFunc("/hls/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
		if triggerRollover.Load() {
			if servedRolloverErr.CompareAndSwap(false, true) {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:20\n#EXT-X-MAP:URI=\"/hls/init.mp4\"\n#EXTINF:1.0,\n/hls/seg2.m4s\n"))
			return
		}
		_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:10\n#EXT-X-MAP:URI=\"/hls/init.mp4\"\n#EXTINF:1.0,\n/hls/seg1.m4s\n"))
	})
	mux.HandleFunc("/hls/init.mp4", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(initBytes) })
	mux.HandleFunc("/hls/seg1.m4s", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(fragBytes) })
	mux.HandleFunc("/hls/seg2.m4s", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(fragBytes) })

	liveMgr, _, subMgr, srv, _ := setupLiveTestHarness(t, mux)
	srvURL = srv.URL
	_, err = subMgr.Upsert(subscription.Subscription{
		Mid:        2002,
		Uname:      "Streamer2002",
		Enabled:    true,
		LiveNotify: true,
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if _, err := liveMgr.PollNow(context.Background()); err != nil {
		t.Fatalf("PollNow: %v", err)
	}

	if _, err := liveMgr.StartRecording(context.Background(), 9002); err != nil {
		t.Fatalf("StartRecording: %v", err)
	}

	var part1Path string
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if pv, ok := liveMgr.RecordingPreview("9002"); ok && pv.PartReady && pv.PartIndex == 1 {
			part1Path = pv.PartPath
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if part1Path == "" {
		t.Fatal("timed out waiting for part 1 ready")
	}

	triggerRollover.Store(true)
	var pv2 RecordingPreview
	deadline = time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if pv, ok := liveMgr.RecordingPreview("9002"); ok && pv.PartReady && pv.PartIndex == 2 {
			pv2 = pv
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if pv2.PartIndex != 2 || !strings.HasSuffix(pv2.PartPath, "part-002.mp4.part") {
		t.Fatalf("expected rollover to part-002.mp4.part, got %+v", pv2)
	}

	// Part 1 should already be finalized while Part 2 is actively recording!
	finalPart1 := strings.TrimSuffix(part1Path, ".part")
	if sum, err := download.InspectMP4File(finalPart1); err != nil || !sum.MoovBeforeMdat {
		t.Fatalf("expected part-001.mp4 finalized during rollover: sum=%+v err=%v", sum, err)
	}

	_, _ = liveMgr.StopRecording(9002)
	waitUntilRoomIdle(t, liveMgr, "bilibili:live:9002", 4*time.Second)

	finalPart2 := strings.TrimSuffix(pv2.PartPath, ".part")
	if sum, err := download.InspectMP4File(finalPart2); err != nil || !sum.MoovBeforeMdat {
		t.Fatalf("expected part-002.mp4 finalized on stop: sum=%+v err=%v", sum, err)
	}
}

func TestDouyinHTTPFLVRecordingPreviewAndAudioOnlyUnsupported(t *testing.T) {
	const secUID = "MS4wLjABAAAA_preview_dy_creator"
	flvBytes := buildSyntheticFLVBytes(t, "avc", 75) // 2.5s = >2 GOPs

	streamRelease := make(chan struct{})
	var flvHits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/live/preview.flv", func(w http.ResponseWriter, r *http.Request) {
		flvHits.Add(1)
		w.Header().Set("Content-Type", "video/x-flv")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(flvBytes)
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		select {
		case <-streamRelease:
		case <-r.Context().Done():
		}
	})

	liveMgr, _, subMgr, srv, _ := setupLiveTestHarness(t, mux)
	dyProv := newFakeDouyinLiveProvider()
	transport := site.DouyinLiveTransport("Mozilla/5.0 Test", true)

	dyProv.statusFn = func(uid, cachedWebRID string, call int) (site.LiveStatus, error) {
		return site.LiveStatus{
			Site:       site.Douyin,
			AccountUID: secUID,
			WebRID:     "880011",
			RoomID:     "73990011",
			Title:      "Douyin FLV Preview",
			Uname:      "DYHost",
			Status:     site.LiveStateLive,
			LiveSince:  1794024000,
		}, nil
	}
	dyProv.streamFn = func(uid, roomOrWebRID string, codecOrder []string, call int) (site.LiveStreamPlan, error) {
		return site.LiveStreamPlan{
			Site:        site.Douyin,
			AccountUID:  secUID,
			RoomID:      "73990011",
			WebRID:      "880011",
			Status:      site.LiveStateLive,
			Transport:   site.LiveTransportHTTPFLV,
			Format:      "flv",
			Codec:       "avc",
			Quality:     10000,
			QualityName: "原画",
			URLs:        []string{srv.URL + "/live/preview.flv"},
			Media:       transport,
		}, nil
	}
	liveMgr.RegisterSiteProvider(dyProv)

	_, err := subMgr.Upsert(subscription.Subscription{
		Name:    "DYHost",
		Enabled: true,
		Accounts: []subscription.AccountRef{{
			Site: string(site.Douyin),
			UID:  secUID,
			Name: "DYHost",
		}},
	})
	if err != nil {
		t.Fatalf("Upsert douyin: %v", err)
	}

	ctx := context.Background()
	if _, err := liveMgr.PollNow(ctx); err != nil {
		t.Fatalf("PollNow: %v", err)
	}
	if _, err := liveMgr.StartDouyinRecording(ctx, secUID, "880011"); err != nil {
		t.Fatalf("StartDouyinRecording: %v", err)
	}

	var pv RecordingPreview
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		cur, ok := liveMgr.RecordingPreview("douyin:" + secUID)
		if ok && cur.Recording && cur.PartReady && cur.PartBytes > 0 {
			pv = cur
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if !pv.PartReady || !pv.PreviewSupported || pv.Transport != "http_flv" {
		close(streamRelease)
		t.Fatalf("expected ready http_flv preview, got %+v", pv)
	}

	statusBefore := dyProv.totalStatusCalls()
	streamBefore := dyProv.streamCallCount("880011")
	flvBefore := flvHits.Load()

	readerFile, err := os.Open(pv.PartPath)
	if err != nil {
		close(streamRelease)
		t.Fatalf("os.Open active .part: %v", err)
	}
	buf := make([]byte, 64)
	if n, err := readerFile.Read(buf); err != nil || n < 16 {
		_ = readerFile.Close()
		close(streamRelease)
		t.Fatalf("read active .part header: n=%d err=%v", n, err)
	}
	for i := 0; i < 30; i++ {
		got, ok := liveMgr.RecordingPreview("73990011")
		if !ok || got.PartPath != pv.PartPath || !got.PreviewSupported {
			_ = readerFile.Close()
			close(streamRelease)
			t.Fatalf("RecordingPreview by roomID mismatch: ok=%v got=%+v", ok, got)
		}
	}
	_ = readerFile.Close()

	if dyProv.totalStatusCalls() != statusBefore || dyProv.streamCallCount("880011") != streamBefore || flvHits.Load() != flvBefore {
		close(streamRelease)
		t.Fatalf("unexpected network calls during preview inspection: status=%d->%d stream=%d->%d flv=%d->%d",
			statusBefore, dyProv.totalStatusCalls(), streamBefore, dyProv.streamCallCount("880011"), flvBefore, flvHits.Load())
	}

	_, _ = liveMgr.StopDouyinRecording(secUID)
	close(streamRelease)
	waitUntilRoomIdle(t, liveMgr, "douyin:live:73990011", 4*time.Second)

	finalMP4 := filepath.Join(filepath.Dir(pv.PartPath), "part-001.mp4")
	if sum, err := download.InspectMP4File(finalMP4); err != nil || !sum.MoovBeforeMdat {
		t.Fatalf("expected defragmented MP4 with moov-before-mdat: sum=%+v err=%v", sum, err)
	}

	// Verify audio-only broadcast marks PreviewSupported=false with explicit reason.
	dyProv.statusFn = func(uid, cachedWebRID string, call int) (site.LiveStatus, error) {
		return site.LiveStatus{
			Site:       site.Douyin,
			AccountUID: secUID,
			WebRID:     "880011",
			RoomID:     "73990011",
			Title:      "Audio Only Room",
			Uname:      "DYHost",
			Status:     site.LiveStateAudioOnly,
		}, nil
	}
	stAudio, err := liveMgr.CheckAccountLiveStatus(ctx, site.Douyin, secUID, "880011", true)
	if err != nil {
		t.Fatalf("CheckAccountLiveStatus audio-only: %v", err)
	}
	if !stAudio.AudioOnly || stAudio.PreviewSupported || stAudio.PreviewReason != previewReasonUnsupported {
		t.Fatalf("expected audio-only room unsupported preview reason, got %+v", stAudio)
	}
	pvAudio, okAudio := liveMgr.RecordingPreview("douyin:" + secUID)
	if !okAudio || pvAudio.Recording || !pvAudio.AudioOnly || pvAudio.PreviewSupported || pvAudio.PreviewReason != previewReasonUnsupported {
		t.Fatalf("unexpected RecordingPreview for audio-only room: ok=%v pv=%+v", okAudio, pvAudio)
	}
}
