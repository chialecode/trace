package live

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
)

type fakeAdvanceClock struct {
	mu  sync.Mutex
	cur time.Time
}

func newFakeAdvanceClock(start time.Time) *fakeAdvanceClock {
	return &fakeAdvanceClock{cur: start}
}

func (c *fakeAdvanceClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cur
}

func (c *fakeAdvanceClock) Advance(d time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cur = c.cur.Add(d)
	return c.cur
}

func splitSyntheticHLSInitAndFrag(t *testing.T) ([]byte, []byte) {
	t.Helper()
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
	return initBuf.Bytes(), fragBuf.Bytes()
}

// M4-K33: Even when legacy LiveSettings carries MaxConcurrentRecordings=1,
// starting 5 live recordings simultaneously (3 Bilibili HLS + 2 Douyin HTTP-FLV) succeeds for all 5
// with zero "已达到同时录制上限" errors, and all 5 finalize valid MP4 segments.
func TestConcurrentRecordingsUnlimited_M4K33(t *testing.T) {
	initBytes, fragBytes := splitSyntheticHLSInitAndFrag(t)
	flvBytes := buildSyntheticFLVBytes(t, "avc", 45)

	gate := make(chan struct{})
	var activeServed atomic.Int32
	var roomsLive atomic.Bool
	roomsLive.Store(true)

	var srvURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/room/v1/Room/room_init", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		liveSt := 0
		if roomsLive.Load() {
			liveSt = 1
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"room_id":     id,
				"short_id":    id,
				"uid":         id + 10000,
				"live_status": liveSt,
				"live_time":   1794024000,
			},
		})
	})
	mux.HandleFunc("/room/v1/Room/get_status_info_by_uids", func(w http.ResponseWriter, r *http.Request) {
		data := make(map[string]any)
		for _, roomID := range []int64{9501, 9502, 9503} {
			uid := roomID + 10000
			data[strconv.FormatInt(uid, 10)] = map[string]any{
				"uid":         uid,
				"room_id":     roomID,
				"title":       fmt.Sprintf("并发直播 %d", roomID),
				"uname":       fmt.Sprintf("主播_%d", roomID),
				"live_status": 1,
				"live_time":   1794024000,
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
	})
	mux.HandleFunc("/xlive/web-room/v2/index/getRoomPlayInfo", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.URL.Query().Get("room_id"), 10, 64)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"room_id":     id,
				"uid":         id + 10000,
				"live_status": 1,
				"playurl_info": map[string]any{
					"playurl": map[string]any{
						"stream": []map[string]any{{
							"protocol_name": "http_hls",
							"format": []map[string]any{{
								"format_name": "fmp4",
								"codec": []map[string]any{{
									"codec_name": "avc",
									"current_qn": 10000,
									"base_url":   fmt.Sprintf("/hls/%d/index.m3u8", id),
									"url_info":   []map[string]any{{"host": srvURL, "extra": ""}},
								}},
							}},
						}},
					},
				},
			},
		})
	})
	var roomPolls sync.Map
	mux.HandleFunc("/hls/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/init.mp4"):
			_, _ = w.Write(initBytes)
		case strings.HasSuffix(r.URL.Path, "/seg1.m4s"):
			activeServed.Add(1)
			<-gate
			_, _ = w.Write(fragBytes)
		case strings.HasSuffix(r.URL.Path, "/index.m3u8"):
			cntAny, _ := roomPolls.LoadOrStore(r.URL.Path, new(atomic.Int32))
			hit := cntAny.(*atomic.Int32).Add(1)
			base := strings.TrimSuffix(r.URL.Path, "/index.m3u8")
			if hit == 1 {
				_, _ = fmt.Fprintf(w, "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:1\n#EXT-X-MAP:URI=\"%s/init.mp4\"\n#EXTINF:1.0,\n%s/seg1.m4s\n", base, base)
			} else {
				_, _ = fmt.Fprintf(w, "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:1\n#EXT-X-MAP:URI=\"%s/init.mp4\"\n#EXTINF:1.0,\n%s/seg1.m4s\n#EXT-X-ENDLIST\n", base, base)
			}
		}
	})
	mux.HandleFunc("/douyin/live/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/x-flv")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(flvBytes[:len(flvBytes)/2])
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		activeServed.Add(1)
		<-gate
		_, _ = w.Write(flvBytes[len(flvBytes)/2:])
	})

	mgr, lib, _, srv, _ := setupLiveTestHarness(t, mux)
	srvURL = srv.URL

	// Explicitly set legacy MaxConcurrentRecordings = 1; Manager must ignore it and allow all 5 concurrent sessions!
	liveCfg := mgr.liveSettings
	liveCfg.MaxConcurrentRecordings = 1
	mgr.UpdateSettings(liveCfg)

	dyProv := newFakeDouyinLiveProvider()
	transport := site.DouyinLiveTransport("Mozilla/5.0 Test", true)
	dyProv.statusFn = func(uid, cachedWebRID string, call int) (site.LiveStatus, error) {
		stVal := site.LiveStateLive
		if call >= 2 {
			stVal = site.LiveStateOffline
		}
		return site.LiveStatus{
			Site:       site.Douyin,
			AccountUID: uid,
			RoomID:     "740000" + cachedWebRID,
			WebRID:     cachedWebRID,
			Uname:      "抖音主播_" + cachedWebRID,
			Title:      "抖音并发直播 " + cachedWebRID,
			Status:     stVal,
			LiveSince:  1794024000,
		}, nil
	}
	dyProv.streamFn = func(uid, roomOrWebRID string, codecOrder []string, call int) (site.LiveStreamPlan, error) {
		return site.LiveStreamPlan{
			Site:        site.Douyin,
			AccountUID:  uid,
			RoomID:      "740000" + roomOrWebRID,
			WebRID:      roomOrWebRID,
			Status:      site.LiveStateLive,
			Quality:     10000,
			QualityName: "原画",
			Codec:       "avc",
			Protocol:    "http_stream",
			Format:      "flv",
			Transport:   site.LiveTransportHTTPFLV,
			URLs:        []string{srv.URL + "/douyin/live/" + roomOrWebRID + ".flv"},
			Media:       transport,
		}, nil
	}
	mgr.RegisterSiteProvider(dyProv)

	ctx := context.Background()
	for _, roomID := range []int64{9501, 9502, 9503} {
		if _, err := mgr.StartRecording(ctx, roomID); err != nil {
			t.Fatalf("StartRecording(%d) failed unexpectedly: %v", roomID, err)
		}
	}
	for _, dyID := range []string{"7701", "7702"} {
		if _, err := mgr.StartDouyinRecording(ctx, "sec_"+dyID, dyID); err != nil {
			t.Fatalf("StartDouyinRecording(%s) failed unexpectedly: %v", dyID, err)
		}
	}

	// Wait until all 5 streams are simultaneously in flight!
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && activeServed.Load() < 5 {
		time.Sleep(10 * time.Millisecond)
	}
	if got := activeServed.Load(); got != 5 {
		t.Fatalf("expected all 5 recordings in flight simultaneously, got %d", got)
	}

	activeCount := 0
	for _, r := range mgr.ListRooms() {
		if strings.Contains(r.LastError, "上限") {
			t.Fatalf("unexpected limit error on room %s: %s", r.RoomKey, r.LastError)
		}
		if r.Recording {
			activeCount++
		}
	}
	if activeCount != 5 {
		t.Fatalf("expected 5 concurrent active recordings in ListRooms(), got %d", activeCount)
	}

	roomsLive.Store(false)
	close(gate)

	for _, key := range []string{"9501", "9502", "9503", "douyin:live:7400007701", "douyin:live:7400007702"} {
		waitUntilRoomIdle(t, mgr, key, 8*time.Second)
	}

	items, err := lib.ListItems(library.FeedFilter{Kind: "live", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 5 {
		t.Fatalf("expected 5 saved live library items, got %d", len(items))
	}
	for _, it := range items {
		if it.Live == nil || len(it.Live.Segments) == 0 {
			t.Fatalf("expected finalized MP4 segment for %s, got status=%s live=%+v", it.ItemKey, it.Status, it.Live)
		}
	}
}

// M4-K34: Collects all 5 incident types (lost_segments, reconnect, slow, disk, convert_failed)
// strictly from recording data with 0 extra site/CDN requests, injectable clock (no 30s sleep),
// real-time visibility while recording, item.json persistence without URL/Cookie leaks,
// 1-minute per-kind coalescing, and 200-entry cap.
func TestLiveRecordingIncidentsAllFiveKindsCoalesceAndCap_M4K34(t *testing.T) {
	// --- Part A: Coalescing (<= 60s merges, > 60s separates) & 200-entry cap ---
	var coalesced []library.LiveIncident
	baseT := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	coalesced, _, _ = appendCoalescedIncident(coalesced, library.LiveIncident{
		Kind:         library.LiveIncidentLostSegments,
		At:           baseT.Format(time.RFC3339),
		EndedAt:      baseT.Format(time.RFC3339),
		DurationSec:  6,
		LostSegments: 3,
		Count:        1,
	})
	// +45s (<= 60s): merges into entry 0!
	coalesced, _, _ = appendCoalescedIncident(coalesced, library.LiveIncident{
		Kind:         library.LiveIncidentLostSegments,
		At:           baseT.Add(45 * time.Second).Format(time.RFC3339),
		EndedAt:      baseT.Add(45 * time.Second).Format(time.RFC3339),
		DurationSec:  4,
		LostSegments: 2,
		Count:        1,
	})
	// +120s (> 60s after previous EndedAt +45s): creates separate entry 1!
	coalesced, _, _ = appendCoalescedIncident(coalesced, library.LiveIncident{
		Kind:         library.LiveIncidentLostSegments,
		At:           baseT.Add(120 * time.Second).Format(time.RFC3339),
		EndedAt:      baseT.Add(120 * time.Second).Format(time.RFC3339),
		DurationSec:  2,
		LostSegments: 1,
		Count:        1,
	})
	if len(coalesced) != 2 {
		t.Fatalf("expected 2 entries after 45s coalesce + 75s gap, got %d: %+v", len(coalesced), coalesced)
	}
	if coalesced[0].Count != 2 || coalesced[0].LostSegments != 5 || coalesced[0].DurationSec != 10 {
		t.Fatalf("expected merged entry Count=2 LostSegments=5 DurationSec=10, got %+v", coalesced[0])
	}
	if coalesced[1].Count != 1 || coalesced[1].LostSegments != 1 || coalesced[1].DurationSec != 2 {
		t.Fatalf("expected separate second entry Count=1 LostSegments=1 DurationSec=2, got %+v", coalesced[1])
	}

	var capped []library.LiveIncident
	for i := 0; i < 250; i++ {
		ts := baseT.Add(time.Duration(i*2) * time.Minute).Format(time.RFC3339)
		capped, _, _ = appendCoalescedIncident(capped, library.LiveIncident{
			Kind:        library.LiveIncidentReconnect,
			At:          ts,
			EndedAt:     ts,
			DurationSec: 3,
			Count:       1,
		})
	}
	if len(capped) != library.MaxLiveIncidentsPerSession {
		t.Fatalf("expected cap of %d incidents, got %d", library.MaxLiveIncidentsPerSession, len(capped))
	}

	// --- Part B & C: End-to-end synthetic HLS (lost_segments 3 segs + 30s slow + real-time preview + disk stop)
	// and Douyin HTTP-FLV (disconnect 20s -> reconnect + FLV lost_segments 20s) ---
	initBytes, fragBytes := splitSyntheticHLSInitAndFrag(t)
	flvBytes := buildSyntheticFLVBytes(t, "avc", 45)
	clock := newFakeAdvanceClock(time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC))

	var apiHits atomic.Int32
	var m3u8Hits atomic.Int32
	var flvConnHits atomic.Int32
	var diskFreeBytes atomic.Uint64
	diskFreeBytes.Store(8 << 30)

	holdBeforeEnd := make(chan struct{})
	reachedHold := make(chan struct{})
	var holdOnce sync.Once

	var srvURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/room/v1/Room/room_init", func(w http.ResponseWriter, r *http.Request) {
		apiHits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"room_id":     9601,
				"short_id":    9601,
				"uid":         29601,
				"live_status": 1,
				"live_time":   1794030000,
			},
		})
	})
	mux.HandleFunc("/room/v1/Room/get_status_info_by_uids", func(w http.ResponseWriter, r *http.Request) {
		apiHits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"29601": map[string]any{
					"uid":         29601,
					"room_id":     9601,
					"title":       "异常测试直播间",
					"uname":       "测试主播A",
					"live_status": 1,
					"live_time":   1794030000,
				},
			},
		})
	})
	mux.HandleFunc("/xlive/web-room/v2/index/getRoomPlayInfo", func(w http.ResponseWriter, r *http.Request) {
		apiHits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"room_id":     9601,
				"uid":         29601,
				"live_status": 1,
				"playurl_info": map[string]any{
					"playurl": map[string]any{
						"stream": []map[string]any{{
							"protocol_name": "http_hls",
							"format": []map[string]any{{
								"format_name": "fmp4",
								"codec": []map[string]any{{
									"codec_name": "avc",
									"current_qn": 10000,
									"base_url":   "/hls/master.m3u8",
									"url_info":   []map[string]any{{"host": srvURL, "extra": "?upsig=secret_sig_9601&expires=1999999999"}},
								}},
							}},
						}},
					},
				},
			},
		})
	})
	mux.HandleFunc("/hls/master.m3u8", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=8000000\n%s/hls/media.m3u8?upsig=secret_child\n", srvURL)
	})
	mux.HandleFunc("/hls/media.m3u8", func(w http.ResponseWriter, r *http.Request) {
		hit := m3u8Hits.Add(1)
		switch hit {
		case 1:
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:10\n#EXT-X-MAP:URI=\"/hls/init.mp4?upsig=init_sec\"\n#EXTINF:2.0,\n/hls/seg10.m4s?upsig=s10\n"))
		case 2:
			// Sequence jumps from 10 -> 14 (dropped 11, 12, 13 = 3 segments, estimated 3 * 2s = 6s!)
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:14\n#EXT-X-MAP:URI=\"/hls/init.mp4?upsig=init_sec\"\n#EXTINF:2.0,\n/hls/seg14.m4s?upsig=s14\n"))
		case 3:
			// Sequence 15 fetched slowly as well -> cumulative slow >= 35s!
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:15\n#EXT-X-MAP:URI=\"/hls/init.mp4?upsig=init_sec\"\n#EXTINF:2.0,\n/hls/seg15.m4s?upsig=s15\n"))
		default:
			holdOnce.Do(func() { close(reachedHold) })
			<-holdBeforeEnd
			diskFreeBytes.Store(100 << 20) // < 1 GiB -> triggers disk incident!
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:15\n#EXT-X-MAP:URI=\"/hls/init.mp4?upsig=init_sec\"\n#EXTINF:2.0,\n/hls/seg15.m4s?upsig=s15\n"))
		}
	})
	mux.HandleFunc("/hls/init.mp4", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(initBytes)
	})
	mux.HandleFunc("/hls/seg10.m4s", func(w http.ResponseWriter, r *http.Request) {
		clock.Advance(12 * time.Second)
		_, _ = w.Write(fragBytes)
	})
	mux.HandleFunc("/hls/seg14.m4s", func(w http.ResponseWriter, r *http.Request) {
		clock.Advance(12 * time.Second)
		_, _ = w.Write(fragBytes)
	})
	mux.HandleFunc("/hls/seg15.m4s", func(w http.ResponseWriter, r *http.Request) {
		clock.Advance(11 * time.Second)
		_, _ = w.Write(fragBytes)
	})
	mux.HandleFunc("/live/dy7801.flv", func(w http.ResponseWriter, r *http.Request) {
		hit := flvConnHits.Add(1)
		if hit == 2 {
			// Stream was disconnected at T; advance injected clock by 20s right as reconnect stream arrives!
			clock.Advance(20 * time.Second)
		}
		w.Header().Set("Content-Type", "video/x-flv")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(flvBytes)
	})

	mgr, lib, _, srv, cacheDir := setupLiveTestHarness(t, mux)
	srvURL = srv.URL
	mgr.SetNowFunc(clock.Now)
	mgr.freeBytesFunc = func(string) (uint64, error) { return diskFreeBytes.Load(), nil }

	if _, err := mgr.StartRecording(context.Background(), 9601); err != nil {
		t.Fatalf("StartRecording(9601): %v", err)
	}

	select {
	case <-reachedHold:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for HLS recorder to reach hold point")
	}

	// Verify real-time in-memory visibility BEFORE recording finishes!
	preview, ok := mgr.RecordingPreview("bilibili:live:9601")
	if !ok || !preview.Recording {
		t.Fatalf("expected active preview for 9601, got ok=%v preview=%+v", ok, preview)
	}
	var sawRealtimeLost, sawRealtimeSlow bool
	for _, inc := range preview.Incidents {
		if inc.Kind == library.LiveIncidentLostSegments && inc.LostSegments == 3 && inc.DurationSec == 6 {
			sawRealtimeLost = true
		}
		if inc.Kind == library.LiveIncidentSlow && inc.DurationSec >= 35 {
			sawRealtimeSlow = true
		}
	}
	if !sawRealtimeLost || !sawRealtimeSlow {
		t.Fatalf("expected real-time lost_segments(3,6s) and slow(>=35s) in preview, got %+v", preview.Incidents)
	}
	apiHitsBeforeStop := apiHits.Load()

	// Release hold -> next loop sees low disk space -> records 'disk' incident and stops!
	close(holdBeforeEnd)
	waitUntilRoomIdle(t, mgr, "9601", 6*time.Second)

	// Verify zero extra site API requests were made to compute incidents!
	if got := apiHits.Load(); got != apiHitsBeforeStop {
		t.Fatalf("expected 0 extra site API requests on incident recording/stop, before=%d after=%d", apiHitsBeforeStop, got)
	}

	// --- Part C: FLV disconnect 20s -> reconnect (produces reconnect 20s + FLV lost_segments 20s) ---
	dyProv := newFakeDouyinLiveProvider()
	transport := site.DouyinLiveTransport("Mozilla/5.0 Test", true)
	dyProv.statusFn = func(uid, cachedWebRID string, call int) (site.LiveStatus, error) {
		stVal := site.LiveStateLive
		if flvConnHits.Load() >= 2 {
			stVal = site.LiveStateOffline
		}
		return site.LiveStatus{
			Site:       site.Douyin,
			AccountUID: uid,
			RoomID:     "7400000000000007801",
			WebRID:     "7801",
			Uname:      "抖音断流主播",
			Title:      "FLV断流重连测试",
			Status:     stVal,
			LiveSince:  1794031000,
		}, nil
	}
	dyProv.streamFn = func(uid, roomOrWebRID string, codecOrder []string, call int) (site.LiveStreamPlan, error) {
		return site.LiveStreamPlan{
			Site:        site.Douyin,
			AccountUID:  uid,
			RoomID:      "7400000000000007801",
			WebRID:      "7801",
			Status:      site.LiveStateLive,
			Quality:     10000,
			QualityName: "原画",
			Codec:       "avc",
			BitrateBps:  4_000_000,
			Protocol:    "http_stream",
			Format:      "flv",
			Transport:   site.LiveTransportHTTPFLV,
			URLs:        []string{srv.URL + "/live/dy7801.flv?sessionid=secret_dy_tok&upsig=flv_sig"},
			Media:       transport,
		}, nil
	}
	diskFreeBytes.Store(8 << 30)
	mgr.RegisterSiteProvider(dyProv)
	if _, err := mgr.StartDouyinRecording(context.Background(), "sec_dy_7801", "7801"); err != nil {
		t.Fatalf("StartDouyinRecording: %v", err)
	}
	waitUntilRoomIdle(t, mgr, "douyin:live:7400000000000007801", 6*time.Second)

	// --- Part D: Orphan corrupt .part recovery -> records 'convert_failed' with F-004 reason ---
	orphanDir := filepath.Join(cacheDir, "bilibili", "live", "9602-corrupt")
	if err := os.MkdirAll(orphanDir, 0o755); err != nil {
		t.Fatal(err)
	}
	corruptPart := filepath.Join(orphanDir, "part-001.mp4.part")
	if err := os.WriteFile(corruptPart, []byte("not-a-valid-fmp4-stream"), 0o600); err != nil {
		t.Fatal(err)
	}
	orphanMeta := library.ItemMetadata{
		Version:      1,
		ID:           "9602-corrupt",
		Site:         "bilibili",
		Kind:         "live",
		RawID:        "9602-corrupt",
		Title:        "损坏分段恢复测试",
		Author:       library.ItemAuthor{Mid: 29602, Name: "损坏测试主播"},
		PubTs:        1794032000,
		URL:          "https://live.bilibili.com/9602",
		CachedAt:     clock.Now().UTC().Format(time.RFC3339),
		Status:       "recording",
		PendingParts: []string{"part-001.mp4.part"},
		Live: &library.LiveRecordingInfo{
			RoomID:    9602,
			StartedAt: clock.Now().UTC().Format(time.RFC3339),
			Source:    "manual",
		},
	}
	if _, err := lib.SaveItem(orphanDir, orphanMeta); err != nil {
		t.Fatal(err)
	}
	mgr.RecoverOrphanParts()

	// --- Verify persisted item.json manifests for all 5 incident types & zero secret/URL leaks ---
	liveItems, err := lib.ListItems(library.FeedFilter{Kind: "live", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	var sawHLSLost3, sawFLVReconnect20, sawFLVLostGap20, sawSlow35, sawDisk, sawConvertFailed bool
	for _, it := range liveItems {
		rawManifest, err := os.ReadFile(filepath.Join(it.ItemDir, "item.json"))
		if err != nil {
			t.Fatalf("read item.json in %s: %v", it.ItemDir, err)
		}
		manifestStr := string(rawManifest)
		for _, secret := range []string{"upsig=", "secret_sig", "secret_child", "secret_dy_tok", "SESSDATA", "sessionid", "127.0.0.1"} {
			if strings.Contains(manifestStr, secret) {
				t.Fatalf("item.json leaked forbidden token %q: %s", secret, manifestStr)
			}
		}
		if it.Live == nil {
			continue
		}
		for _, inc := range it.Live.Incidents {
			switch inc.Kind {
			case library.LiveIncidentLostSegments:
				if inc.LostSegments == 3 && inc.DurationSec == 6 {
					sawHLSLost3 = true
				}
				if inc.LostSegments == 0 && inc.DurationSec >= 19.9 {
					sawFLVLostGap20 = true
				}
			case library.LiveIncidentReconnect:
				if inc.DurationSec >= 19.9 {
					sawFLVReconnect20 = true
				}
			case library.LiveIncidentSlow:
				if inc.DurationSec >= 35 {
					sawSlow35 = true
				}
			case library.LiveIncidentDisk:
				sawDisk = true
			case library.LiveIncidentConvertFailed:
				if strings.Contains(inc.Reason, "失败") {
					sawConvertFailed = true
				}
			}
		}
	}

	if !sawHLSLost3 || !sawFLVReconnect20 || !sawFLVLostGap20 || !sawSlow35 || !sawDisk || !sawConvertFailed {
		t.Fatalf("missing persisted incident variants: hlsLost3=%v flvReconnect20=%v flvLostGap20=%v slow35=%v disk=%v convertFailed=%v",
			sawHLSLost3, sawFLVReconnect20, sawFLVLostGap20, sawSlow35, sawDisk, sawConvertFailed)
	}
}
