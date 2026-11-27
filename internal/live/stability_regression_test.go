package live

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
)

func buildBiliPlayInfoResp(roomID, uid int64, srvURL, basePath string) map[string]any {
	return map[string]any{
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
											"base_url":   basePath,
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
	}
}

func registerStandardBilibiliLiveHandlers(mux *http.ServeMux, roomID, uid, liveSince int64, stillLive *atomic.Bool) {
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
		ls := 0
		if stillLive.Load() {
			ls = 1
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"room_id": roomID, "uid": uid, "title": "Synthetic Stability Live", "live_status": ls},
		})
	})
	mux.HandleFunc("/room/v1/Room/get_status_info_by_uids", func(w http.ResponseWriter, r *http.Request) {
		ls := 0
		if stillLive.Load() {
			ls = 1
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				fmt.Sprint(uid): map[string]any{
					"uid":         uid,
					"room_id":     roomID,
					"uname":       fmt.Sprintf("Streamer%d", uid),
					"title":       "Synthetic Stability Live",
					"live_status": ls,
					"live_time":   liveSince,
				},
			},
		})
	})
}

// Normal manual stop and rollover conversions must not be killed by the 30s
// app-shutdown finalizeTimeout, while app Stop() must enforce finalizeTimeout
// and preserve .part without recording a spurious convert_failed incident.
func TestNormalFinalizeExceedsShutdownTimeoutAndStopAbortsCleanly(t *testing.T) {
	t.Run("manual stop succeeds past finalizeTimeout", func(t *testing.T) {
		const (
			roomID    int64 = 71001
			uid       int64 = 7101
			liveSince int64 = 1796001000
		)
		initBytes, fragBytes := regressionLiveSamples(t)
		var stillLive atomic.Bool
		stillLive.Store(true)
		var srvURL string

		mux := http.NewServeMux()
		registerStandardBilibiliLiveHandlers(mux, roomID, uid, liveSince, &stillLive)
		mux.HandleFunc("/xlive/web-room/v2/index/getRoomPlayInfo", func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(buildBiliPlayInfoResp(roomID, uid, srvURL, "/hls/index.m3u8"))
		})
		mux.HandleFunc("/hls/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:1\n#EXT-X-MAP:URI=\"/hls/init.mp4\"\n#EXTINF:1.0,\n/hls/seg1.m4s\n"))
		})
		mux.HandleFunc("/hls/init.mp4", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(initBytes) })
		mux.HandleFunc("/hls/seg1.m4s", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(fragBytes) })

		mgr, lib, _, srv, _ := setupLiveTestHarness(t, mux)
		srvURL = srv.URL
		mgr.mu.Lock()
		mgr.hlsPollInterval = 15 * time.Millisecond
		mgr.finalizeTimeout = 60 * time.Millisecond
		mgr.mu.Unlock()
		origConvert := convertPart
		convertPart = func(ctx context.Context, partPath, finalPath string) (download.MuxTrackSummary, error) {
			select {
			case <-time.After(180 * time.Millisecond):
			case <-ctx.Done():
				return download.MuxTrackSummary{}, ctx.Err()
			}
			return origConvert(ctx, partPath, finalPath)
		}
		t.Cleanup(func() { convertPart = origConvert })

		if _, err := mgr.StartRecording(context.Background(), roomID); err != nil {
			t.Fatalf("StartRecording: %v", err)
		}
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if pv, ok := mgr.RecordingPreview(fmt.Sprint(roomID)); ok && pv.PartReady {
				break
			}
			time.Sleep(15 * time.Millisecond)
		}

		if _, err := mgr.StopRecording(roomID); err != nil {
			t.Fatalf("StopRecording returned error despite healthy slow conversion: %v", err)
		}
		meta, ok, err := lib.GetItem(library.CanonicalItemKey("bilibili", "live", fmt.Sprintf("%d:%d", roomID, liveSince)))
		if err != nil || !ok || meta.Live == nil {
			t.Fatalf("GetItem: ok=%v err=%v", ok, err)
		}
		if len(meta.Live.Segments) != 1 || meta.Live.Segments[0].File != "part-001.mp4" {
			t.Fatalf("segments = %+v, want [part-001.mp4]", meta.Live.Segments)
		}
		for _, inc := range meta.Live.Incidents {
			if inc.Kind == library.LiveIncidentConvertFailed {
				t.Fatalf("unexpected convert_failed on healthy slow manual stop: %+v", inc)
			}
		}
	})

	t.Run("parent cancel before Stop still allows finalize within shutdown budget", func(t *testing.T) {
		const (
			roomID    int64 = 71002
			uid       int64 = 7102
			liveSince int64 = 1796002000
		)
		initBytes, fragBytes := regressionLiveSamples(t)
		var stillLive atomic.Bool
		stillLive.Store(true)
		var srvURL string

		mux := http.NewServeMux()
		registerStandardBilibiliLiveHandlers(mux, roomID, uid, liveSince, &stillLive)
		mux.HandleFunc("/xlive/web-room/v2/index/getRoomPlayInfo", func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(buildBiliPlayInfoResp(roomID, uid, srvURL, "/hls/index.m3u8"))
		})
		mux.HandleFunc("/hls/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:1\n#EXT-X-MAP:URI=\"/hls/init.mp4\"\n#EXTINF:1.0,\n/hls/seg1.m4s\n"))
		})
		mux.HandleFunc("/hls/init.mp4", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(initBytes) })
		mux.HandleFunc("/hls/seg1.m4s", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(fragBytes) })

		mgr, lib, _, srv, _ := setupLiveTestHarness(t, mux)
		srvURL = srv.URL
		mgr.mu.Lock()
		mgr.hlsPollInterval = 15 * time.Millisecond
		mgr.finalizeTimeout = 3 * time.Second
		mgr.mu.Unlock()
		origConvert := convertPart
		convertPart = func(ctx context.Context, partPath, finalPath string) (download.MuxTrackSummary, error) {
			select {
			case <-time.After(120 * time.Millisecond):
			case <-ctx.Done():
				return download.MuxTrackSummary{}, ctx.Err()
			}
			return origConvert(ctx, partPath, finalPath)
		}
		t.Cleanup(func() { convertPart = origConvert })

		parentCtx, parentCancel := context.WithCancel(context.Background())
		mgr.Start(parentCtx)
		if _, err := mgr.StartRecording(context.Background(), roomID); err != nil {
			t.Fatalf("StartRecording: %v", err)
		}
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if pv, ok := mgr.RecordingPreview(fmt.Sprint(roomID)); ok && pv.PartReady {
				break
			}
			time.Sleep(15 * time.Millisecond)
		}
		// Simulate Backend.Close(): cancel parentCtx before calling live.Stop().
		parentCancel()
		mgr.Stop()

		meta, ok, err := lib.GetItem(library.CanonicalItemKey("bilibili", "live", fmt.Sprintf("%d:%d", roomID, liveSince)))
		if err != nil || !ok || meta.Live == nil {
			t.Fatalf("GetItem: ok=%v err=%v", ok, err)
		}
		if len(meta.Live.Segments) != 1 || meta.Live.Segments[0].File != "part-001.mp4" {
			t.Fatalf("expected part-001.mp4 converted after parentCancel+Stop, got segments=%+v pending=%v", meta.Live.Segments, meta.PendingParts)
		}
	})

	t.Run("Stop timeout interrupts hung conversion without false convert_failed", func(t *testing.T) {
		const (
			roomID    int64 = 71003
			uid       int64 = 7103
			liveSince int64 = 1796003000
		)
		initBytes, fragBytes := regressionLiveSamples(t)
		var stillLive atomic.Bool
		stillLive.Store(true)
		var srvURL string

		mux := http.NewServeMux()
		registerStandardBilibiliLiveHandlers(mux, roomID, uid, liveSince, &stillLive)
		mux.HandleFunc("/xlive/web-room/v2/index/getRoomPlayInfo", func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(buildBiliPlayInfoResp(roomID, uid, srvURL, "/hls/index.m3u8"))
		})
		mux.HandleFunc("/hls/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:1\n#EXT-X-MAP:URI=\"/hls/init.mp4\"\n#EXTINF:1.0,\n/hls/seg1.m4s\n"))
		})
		mux.HandleFunc("/hls/init.mp4", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(initBytes) })
		mux.HandleFunc("/hls/seg1.m4s", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(fragBytes) })

		mgr, lib, _, srv, _ := setupLiveTestHarness(t, mux)
		srvURL = srv.URL
		mgr.mu.Lock()
		mgr.hlsPollInterval = 15 * time.Millisecond
		mgr.finalizeTimeout = 150 * time.Millisecond
		mgr.mu.Unlock()
		var convertCanceled atomic.Bool
		origConvert := convertPart
		convertPart = func(ctx context.Context, partPath, finalPath string) (download.MuxTrackSummary, error) {
			<-ctx.Done()
			convertCanceled.Store(true)
			return download.MuxTrackSummary{}, ctx.Err()
		}
		t.Cleanup(func() { convertPart = origConvert })

		parentCtx, parentCancel := context.WithCancel(context.Background())
		mgr.Start(parentCtx)
		if _, err := mgr.StartRecording(context.Background(), roomID); err != nil {
			t.Fatalf("StartRecording: %v", err)
		}
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if pv, ok := mgr.RecordingPreview(fmt.Sprint(roomID)); ok && pv.PartReady {
				break
			}
			time.Sleep(15 * time.Millisecond)
		}
		parentCancel()
		stopStart := time.Now()
		mgr.Stop()
		if elapsed := time.Since(stopStart); elapsed > 2*time.Second {
			t.Fatalf("Stop() took %v, expected bounded shutdown near 150ms", elapsed)
		}
		if !convertCanceled.Load() {
			t.Fatal("expected hung convertPart to be cancelled by shutdown timeout")
		}
		meta, ok, err := lib.GetItem(library.CanonicalItemKey("bilibili", "live", fmt.Sprintf("%d:%d", roomID, liveSince)))
		if err != nil || !ok || meta.Live == nil {
			t.Fatalf("GetItem: ok=%v err=%v", ok, err)
		}
		if len(meta.PendingParts) == 0 {
			t.Fatalf("expected .part preserved in PendingParts for next startup recovery, got %+v", meta)
		}
		for _, inc := range meta.Live.Incidents {
			if inc.Kind == library.LiveIncidentConvertFailed {
				t.Fatalf("shutdown interruption must not record bogus convert_failed: %+v", inc)
			}
		}
	})
}

// Recoverable disconnects must finalize the previous part asynchronously so
// reconnect starts immediately rather than blocking on multi-GB conversion.
func TestDisconnectLaunchesAsyncFinalizeAndPreservesAccurateDisconnectTime(t *testing.T) {
	const (
		roomID    int64 = 71004
		uid       int64 = 7104
		liveSince int64 = 1796004000
	)
	initBytes, fragBytes := regressionLiveSamples(t)
	var stillLive atomic.Bool
	stillLive.Store(true)
	var playCalls atomic.Int32
	var m3u8Hits atomic.Int32
	var seg2Fetched atomic.Bool
	var srvURL string

	convertEntered := make(chan struct{}, 1)
	releaseConvert := make(chan struct{})

	mux := http.NewServeMux()
	registerStandardBilibiliLiveHandlers(mux, roomID, uid, liveSince, &stillLive)
	mux.HandleFunc("/xlive/web-room/v2/index/getRoomPlayInfo", func(w http.ResponseWriter, r *http.Request) {
		playCalls.Add(1)
		_ = json.NewEncoder(w).Encode(buildBiliPlayInfoResp(roomID, uid, srvURL, "/hls/index.m3u8"))
	})
	mux.HandleFunc("/hls/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
		switch m3u8Hits.Add(1) {
		case 1:
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:10\n#EXT-X-MAP:URI=\"/hls/init.mp4\"\n#EXTINF:1.0,\n/hls/seg1.m4s\n"))
		case 2:
			w.WriteHeader(http.StatusBadGateway)
		default:
			stillLive.Store(false)
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:11\n#EXT-X-MAP:URI=\"/hls/init.mp4\"\n#EXTINF:1.0,\n/hls/seg2.m4s\n#EXT-X-ENDLIST\n"))
		}
	})
	mux.HandleFunc("/hls/init.mp4", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(initBytes) })
	mux.HandleFunc("/hls/seg1.m4s", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(fragBytes) })
	mux.HandleFunc("/hls/seg2.m4s", func(w http.ResponseWriter, r *http.Request) {
		seg2Fetched.Store(true)
		_, _ = w.Write(fragBytes)
	})

	mgr, lib, _, srv, _ := setupLiveTestHarness(t, mux)
	srvURL = srv.URL
	mgr.mu.Lock()
	mgr.hlsPollInterval = 15 * time.Millisecond
	mgr.reconnectWait = 20 * time.Millisecond
	mgr.mu.Unlock()
	origConvert := convertPart
	convertPart = func(ctx context.Context, partPath, finalPath string) (download.MuxTrackSummary, error) {
		if strings.HasSuffix(partPath, "part-001.mp4.part") {
			select {
			case convertEntered <- struct{}{}:
			default:
			}
			<-releaseConvert
		}
		return origConvert(ctx, partPath, finalPath)
	}
	t.Cleanup(func() { convertPart = origConvert })

	if _, err := mgr.StartRecording(context.Background(), roomID); err != nil {
		t.Fatalf("StartRecording: %v", err)
	}
	select {
	case <-convertEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for async part-001 conversion after disconnect")
	}

	// Reconnect and part-002 fetch MUST happen while part-001 conversion is still blocked!
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !seg2Fetched.Load() {
		time.Sleep(15 * time.Millisecond)
	}
	if !seg2Fetched.Load() || playCalls.Load() < 2 {
		close(releaseConvert)
		t.Fatalf("reconnect blocked on part-001 conversion: seg2Fetched=%v playCalls=%d", seg2Fetched.Load(), playCalls.Load())
	}
	close(releaseConvert)
	waitLiveRecordingDone(t, mgr, roomID)

	meta, ok, err := lib.GetItem(library.CanonicalItemKey("bilibili", "live", fmt.Sprintf("%d:%d", roomID, liveSince)))
	if err != nil || !ok || meta.Live == nil {
		t.Fatalf("GetItem: ok=%v err=%v", ok, err)
	}
	if len(meta.Live.Segments) != 2 || meta.Live.Segments[0].File != "part-001.mp4" || meta.Live.Segments[1].File != "part-002.mp4" {
		t.Fatalf("segments = %+v, want [part-001.mp4 part-002.mp4]", meta.Live.Segments)
	}
	sawReconnect := false
	for _, inc := range meta.Live.Incidents {
		if inc.Kind == library.LiveIncidentReconnect {
			sawReconnect = true
		}
	}
	if !sawReconnect {
		t.Fatalf("expected reconnect incident, got %+v", meta.Live.Incidents)
	}
}

// Manager.Start() must run orphan part recovery asynchronously without blocking
// startup, and mergeLiveMedia/mergeLiveSegment must keep parts sorted numerically.
func TestAsyncStartupRecoveryNonBlockingAndNumericSegmentSort(t *testing.T) {
	initBytes, fragBytes := regressionLiveSamples(t)
	mgr, lib, _ := regressionRecorder(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))

	dir := filepath.Join(lib.CacheDir(), "bilibili", "7105", "lives", "71005-1796005000")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	rawPart := append(append([]byte(nil), initBytes...), fragBytes...)
	part1Path := filepath.Join(dir, "part-001.mp4.part")
	if err := os.WriteFile(part1Path, rawPart, 0o600); err != nil {
		t.Fatal(err)
	}
	// Seed part-002.mp4 already finalized so recovery of part-001 tests out-of-order numeric sorting.
	part2Path := filepath.Join(dir, "part-002.mp4")
	part2Tmp := filepath.Join(dir, "part-002.mp4.part")
	if err := os.WriteFile(part2Tmp, rawPart, 0o600); err != nil {
		t.Fatal(err)
	}
	part2Sum, err := finalizeLivePart(context.Background(), part2Tmp, part2Path)
	if err != nil {
		t.Fatalf("finalize seed part-002: %v", err)
	}
	_ = os.Remove(part2Tmp)

	_, err = lib.SaveLiveItem(dir, library.ItemMetadata{
		Site:         "bilibili",
		Kind:         "live",
		ID:           "live:71005:1796005000",
		Title:        "Async Recovery Test",
		Status:       "recording",
		PendingParts: []string{"part-001.mp4.part"},
		Media: []library.MediaFile{{
			Role:   "live_segment",
			Path:   "part-002.mp4",
			Bytes:  part2Sum.FileBytes,
			SHA256: part2Sum.SHA256,
		}},
		Live: &library.LiveRecordingInfo{
			RoomID:    71005,
			LiveSince: 1796005000,
			Segments: []library.LiveSegmentInfo{{
				File:        "part-002.mp4",
				Bytes:       part2Sum.FileBytes,
				SHA256:      part2Sum.SHA256,
				DurationSec: part2Sum.DurationSec,
			}},
		},
	})
	if err != nil {
		t.Fatalf("SaveLiveItem seed: %v", err)
	}

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	origConvert := convertPart
	convertPart = func(ctx context.Context, partPath, finalPath string) (download.MuxTrackSummary, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		return origConvert(ctx, partPath, finalPath)
	}
	t.Cleanup(func() { convertPart = origConvert })

	startBegin := time.Now()
	mgr.Start(context.Background())
	if elapsed := time.Since(startBegin); elapsed > 200*time.Millisecond {
		close(release)
		t.Fatalf("Start() blocked for %v waiting on orphan recovery", elapsed)
	}

	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("background orphan recovery never started")
	}
	close(release)
	mgr.Stop()

	meta, ok, err := lib.GetItem(library.CanonicalItemKey("bilibili", "live", "71005:1796005000"))
	if err != nil || !ok || meta.Live == nil {
		t.Fatalf("GetItem after recovery: ok=%v err=%v", ok, err)
	}
	if len(meta.PendingParts) != 0 {
		t.Fatalf("PendingParts not cleared: %v", meta.PendingParts)
	}
	if len(meta.Live.Segments) != 2 || meta.Live.Segments[0].File != "part-001.mp4" || meta.Live.Segments[1].File != "part-002.mp4" {
		t.Fatalf("Live.Segments not sorted numerically: %+v", meta.Live.Segments)
	}
	var mediaParts []string
	for _, mf := range meta.Media {
		if mf.Role == "live_segment" {
			mediaParts = append(mediaParts, mf.Path)
		}
	}
	if len(mediaParts) != 2 || mediaParts[0] != "part-001.mp4" || mediaParts[1] != "part-002.mp4" {
		t.Fatalf("Media live_segment entries not sorted numerically: %v", mediaParts)
	}
}

// Same-URI EXT-X-MAP must be fetched once per part, not re-fetched on every poll.
func TestHLSFetchesInitMapOncePerPartAcrossPolls(t *testing.T) {
	initBytes, fragBytes := regressionLiveSamples(t)
	var pollHits atomic.Int32
	var initHits atomic.Int32
	mgr, lib, base := regressionRecorder(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/hls/index.m3u8":
			n := pollHits.Add(1)
			end := ""
			if n >= 4 {
				end = "#EXT-X-ENDLIST\n"
			}
			_, _ = fmt.Fprintf(w, "#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:%d\n#EXT-X-MAP:URI=\"/hls/init.mp4\"\n#EXTINF:1.0,\n/hls/seg%d.m4s\n%s", 10+n-1, n, end)
		case "/hls/init.mp4":
			initHits.Add(1)
			_, _ = w.Write(initBytes)
		default:
			_, _ = w.Write(fragBytes)
		}
	}))
	sess := regressionLiveSession(t, lib)
	partPath := filepath.Join(sess.itemDir, "part-001.mp4.part")
	if _, err := mgr.recordHLSSegment(context.Background(), sess, []string{base + "/hls/index.m3u8"}, partPath, true); err != nil {
		t.Fatalf("recordHLSSegment: %v", err)
	}
	if initHits.Load() != 1 {
		t.Fatalf("EXT-X-MAP fetched %d times across %d polls, want 1", initHits.Load(), pollHits.Load())
	}
}

// Default HLS poll sleep must subtract loop elapsed time from clamp(targetDuration/2, 500ms, 2s).
func TestHLSPollSleepAccountsForLoopElapsedTime(t *testing.T) {
	initBytes, fragBytes := regressionLiveSamples(t)
	var mu sync.Mutex
	var pollTimes []time.Time
	var pollHits atomic.Int32
	mgr, lib, base := regressionRecorder(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/hls/index.m3u8":
			mu.Lock()
			pollTimes = append(pollTimes, time.Now())
			mu.Unlock()
			n := pollHits.Add(1)
			end := ""
			if n >= 3 {
				end = "#EXT-X-ENDLIST\n"
			}
			_, _ = fmt.Fprintf(w, "#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:%d\n#EXT-X-MAP:URI=\"/hls/init.mp4\"\n#EXTINF:1.0,\n/hls/seg%d.m4s\n%s", 10+n-1, n, end)
		case "/hls/init.mp4":
			_, _ = w.Write(initBytes)
		default:
			time.Sleep(250 * time.Millisecond)
			_, _ = w.Write(fragBytes)
		}
	}))
	// Use production default poll cadence (hlsPollInterval == 0).
	mgr.hlsPollInterval = 0
	sess := regressionLiveSession(t, lib)
	partPath := filepath.Join(sess.itemDir, "part-001.mp4.part")
	if _, err := mgr.recordHLSSegment(context.Background(), sess, []string{base + "/hls/index.m3u8"}, partPath, true); err != nil {
		t.Fatalf("recordHLSSegment: %v", err)
	}
	mu.Lock()
	times := append([]time.Time(nil), pollTimes...)
	mu.Unlock()
	if len(times) < 3 {
		t.Fatalf("expected 3 polls, got %d", len(times))
	}
	// With TARGETDURATION=1 -> target=500ms and 250ms segment fetch, inter-poll gap
	// should stay near ~500ms rather than 250ms + 1000ms = 1250ms.
	gap := times[2].Sub(times[1])
	if gap > 900*time.Millisecond {
		t.Fatalf("poll gap = %v, expected elapsed-aware cadence near 500ms (not >1s additive sleep)", gap)
	}
}

// Single aged-out .m4s HTTP 404 skips cleanly without reconnect; 3 consecutive 404s escalate.
func TestHLSSingleSegment404SkipsAndThree404sEscalate(t *testing.T) {
	t.Run("single 404 skips without reconnect", func(t *testing.T) {
		const (
			roomID    int64 = 71006
			uid       int64 = 7106
			liveSince int64 = 1796006000
		)
		initBytes, fragBytes := regressionLiveSamples(t)
		var stillLive atomic.Bool
		stillLive.Store(true)
		var playCalls atomic.Int32
		var pollHits atomic.Int32
		var srvURL string

		mux := http.NewServeMux()
		registerStandardBilibiliLiveHandlers(mux, roomID, uid, liveSince, &stillLive)
		mux.HandleFunc("/xlive/web-room/v2/index/getRoomPlayInfo", func(w http.ResponseWriter, r *http.Request) {
			playCalls.Add(1)
			_ = json.NewEncoder(w).Encode(buildBiliPlayInfoResp(roomID, uid, srvURL, "/hls/index.m3u8"))
		})
		mux.HandleFunc("/hls/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
			switch pollHits.Add(1) {
			case 1:
				_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:10\n#EXT-X-MAP:URI=\"/hls/init.mp4\"\n#EXTINF:1.0,\n/hls/seg10.m4s\n"))
			default:
				stillLive.Store(false)
				_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:11\n#EXT-X-MAP:URI=\"/hls/init.mp4\"\n#EXTINF:1.0,\n/hls/seg11.m4s\n#EXTINF:1.0,\n/hls/seg12.m4s\n#EXT-X-ENDLIST\n"))
			}
		})
		mux.HandleFunc("/hls/init.mp4", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(initBytes) })
		mux.HandleFunc("/hls/seg10.m4s", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(fragBytes) })
		mux.HandleFunc("/hls/seg11.m4s", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) })
		mux.HandleFunc("/hls/seg12.m4s", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(fragBytes) })

		mgr, lib, _, srv, _ := setupLiveTestHarness(t, mux)
		srvURL = srv.URL
		mgr.mu.Lock()
		mgr.hlsPollInterval = 15 * time.Millisecond
		mgr.reconnectWait = 15 * time.Millisecond
		mgr.mu.Unlock()

		if _, err := mgr.StartRecording(context.Background(), roomID); err != nil {
			t.Fatalf("StartRecording: %v", err)
		}
		waitLiveRecordingDone(t, mgr, roomID)

		if playCalls.Load() != 1 {
			t.Fatalf("playInfoCalls = %d, want 1 (single 404 must not trigger reconnect)", playCalls.Load())
		}
		meta, ok, err := lib.GetItem(library.CanonicalItemKey("bilibili", "live", fmt.Sprintf("%d:%d", roomID, liveSince)))
		if err != nil || !ok || meta.Live == nil {
			t.Fatalf("GetItem: ok=%v err=%v", ok, err)
		}
		if len(meta.Live.Segments) != 1 {
			t.Fatalf("segments = %v, want single continuous part", meta.Live.Segments)
		}
		lost := 0
		for _, inc := range meta.Live.Incidents {
			if inc.Kind == library.LiveIncidentLostSegments {
				lost += inc.LostSegments
			}
		}
		if lost != 1 {
			t.Fatalf("lost_segments = %d, want 1", lost)
		}
	})

	t.Run("three consecutive 404s escalate to reconnect", func(t *testing.T) {
		const (
			roomID    int64 = 71007
			uid       int64 = 7107
			liveSince int64 = 1796007000
		)
		initBytes, fragBytes := regressionLiveSamples(t)
		var stillLive atomic.Bool
		stillLive.Store(true)
		var playCalls atomic.Int32
		var aHits atomic.Int32
		var srvURL string

		mux := http.NewServeMux()
		registerStandardBilibiliLiveHandlers(mux, roomID, uid, liveSince, &stillLive)
		mux.HandleFunc("/xlive/web-room/v2/index/getRoomPlayInfo", func(w http.ResponseWriter, r *http.Request) {
			path := "/hls/a.m3u8"
			if playCalls.Add(1) >= 2 {
				path = "/hls/b.m3u8"
			}
			_ = json.NewEncoder(w).Encode(buildBiliPlayInfoResp(roomID, uid, srvURL, path))
		})
		mux.HandleFunc("/hls/a.m3u8", func(w http.ResponseWriter, r *http.Request) {
			if aHits.Add(1) == 1 {
				_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:10\n#EXT-X-MAP:URI=\"/hls/init.mp4\"\n#EXTINF:1.0,\n/hls/good.m4s\n"))
				return
			}
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:11\n#EXT-X-MAP:URI=\"/hls/init.mp4\"\n#EXTINF:1.0,\n/hls/missing1.m4s\n#EXTINF:1.0,\n/hls/missing2.m4s\n#EXTINF:1.0,\n/hls/missing3.m4s\n"))
		})
		mux.HandleFunc("/hls/b.m3u8", func(w http.ResponseWriter, r *http.Request) {
			stillLive.Store(false)
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:14\n#EXT-X-MAP:URI=\"/hls/init.mp4\"\n#EXTINF:1.0,\n/hls/good.m4s\n#EXT-X-ENDLIST\n"))
		})
		mux.HandleFunc("/hls/init.mp4", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(initBytes) })
		mux.HandleFunc("/hls/good.m4s", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(fragBytes) })

		mgr, lib, _, srv, _ := setupLiveTestHarness(t, mux)
		srvURL = srv.URL
		mgr.mu.Lock()
		mgr.hlsPollInterval = 15 * time.Millisecond
		mgr.reconnectWait = 15 * time.Millisecond
		mgr.mu.Unlock()

		if _, err := mgr.StartRecording(context.Background(), roomID); err != nil {
			t.Fatalf("StartRecording: %v", err)
		}
		waitLiveRecordingDone(t, mgr, roomID)

		if playCalls.Load() != 2 {
			t.Fatalf("playInfoCalls = %d, want 2 (3 consecutive 404s must escalate to reconnect)", playCalls.Load())
		}
		meta, ok, err := lib.GetItem(library.CanonicalItemKey("bilibili", "live", fmt.Sprintf("%d:%d", roomID, liveSince)))
		if err != nil || !ok || meta.Live == nil || len(meta.Live.Segments) != 2 {
			t.Fatalf("expected 2 segments after reconnect, got ok=%v err=%v live=%+v", ok, err, meta.Live)
		}
	})
}

// Transient stale lower-sequence polls from an edge CDN must be ignored;
// a genuine large sequence reset (>10) with EXT-X-MEDIA-SEQUENCE still rolls over.
func TestHLSStaleLowerSequenceToleratesEdgeLagAndRolloversOnRealReset(t *testing.T) {
	t.Run("two stale polls ignored without rollover", func(t *testing.T) {
		initBytes, fragBytes := regressionLiveSamples(t)
		var hits atomic.Int32
		mgr, lib, base := regressionRecorder(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/hls/index.m3u8":
				switch hits.Add(1) {
				case 1:
					_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:20\n#EXT-X-MAP:URI=\"/hls/init.mp4\"\n#EXTINF:1.0,\n/hls/seg.m4s\n"))
				case 2, 3:
					_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:19\n#EXT-X-MAP:URI=\"/hls/init.mp4\"\n#EXTINF:1.0,\n/hls/seg.m4s\n"))
				default:
					_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:21\n#EXT-X-MAP:URI=\"/hls/init.mp4\"\n#EXTINF:1.0,\n/hls/seg.m4s\n#EXT-X-ENDLIST\n"))
				}
			case "/hls/init.mp4":
				_, _ = w.Write(initBytes)
			default:
				_, _ = w.Write(fragBytes)
			}
		}))
		sess := regressionLiveSession(t, lib)
		partPath := filepath.Join(sess.itemDir, "part-001.mp4.part")
		if _, err := mgr.recordHLSSegment(context.Background(), sess, []string{base + "/hls/index.m3u8"}, partPath, true); err != nil {
			t.Fatalf("stale lower sequence triggered unexpected error/rollover: %v", err)
		}
		if sess.lastSequence != 21 {
			t.Fatalf("lastSequence = %d, want 21", sess.lastSequence)
		}
	})

	t.Run("large real sequence reset rolls over immediately", func(t *testing.T) {
		initBytes, fragBytes := regressionLiveSamples(t)
		var hits atomic.Int32
		mgr, lib, base := regressionRecorder(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/hls/index.m3u8":
				if hits.Add(1) == 1 {
					_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:100\n#EXT-X-MAP:URI=\"/hls/init.mp4\"\n#EXTINF:1.0,\n/hls/seg.m4s\n"))
					return
				}
				_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:1\n#EXT-X-MAP:URI=\"/hls/init.mp4\"\n#EXTINF:1.0,\n/hls/seg.m4s\n"))
			case "/hls/init.mp4":
				_, _ = w.Write(initBytes)
			default:
				_, _ = w.Write(fragBytes)
			}
		}))
		sess := regressionLiveSession(t, lib)
		partPath := filepath.Join(sess.itemDir, "part-001.mp4.part")
		_, err := mgr.recordHLSSegment(context.Background(), sess, []string{base + "/hls/index.m3u8"}, partPath, true)
		if !errors.Is(err, errSegmentRollover) {
			t.Fatalf("err = %v, want errSegmentRollover", err)
		}
	})
}

// Reconnect backoff starts fast (500ms -> 2s -> 4s -> cap) and respects smaller caps.
func TestHLSReconnectBackoffStartsFastAndCapsAtReconnectWait(t *testing.T) {
	want := []time.Duration{
		500 * time.Millisecond,
		2 * time.Second,
		4 * time.Second,
		6 * time.Second,
		6 * time.Second,
	}
	for i, w := range want {
		if got := reconnectBackoffDelay(i, 6*time.Second); got != w {
			t.Fatalf("attempt %d: backoff = %v, want %v", i, got, w)
		}
	}
	for i := 0; i < 4; i++ {
		if got := reconnectBackoffDelay(i, 100*time.Millisecond); got != 100*time.Millisecond {
			t.Fatalf("small cap attempt %d: backoff = %v, want 100ms", i, got)
		}
	}
}

// lost_segments duration uses observed #EXTINF rolling average instead of
// inflating 2x when EXT-X-TARGETDURATION is 2s for 1s segments.
func TestHLSLostSegmentsUsesObservedExtinfInsteadOfTargetDuration(t *testing.T) {
	initBytes, fragBytes := regressionLiveSamples(t)
	var hits atomic.Int32
	mgr, lib, base := regressionRecorder(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/hls/index.m3u8":
			if hits.Add(1) == 1 {
				_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:10\n#EXT-X-MAP:URI=\"/hls/init.mp4\"\n#EXTINF:1.0,\n/hls/seg10.m4s\n"))
				return
			}
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:14\n#EXT-X-MAP:URI=\"/hls/init.mp4\"\n#EXTINF:1.0,\n/hls/seg14.m4s\n#EXT-X-ENDLIST\n"))
		case "/hls/init.mp4":
			_, _ = w.Write(initBytes)
		default:
			_, _ = w.Write(fragBytes)
		}
	}))
	sess := regressionLiveSession(t, lib)
	partPath := filepath.Join(sess.itemDir, "part-001.mp4.part")
	if _, err := mgr.recordHLSSegment(context.Background(), sess, []string{base + "/hls/index.m3u8"}, partPath, true); err != nil {
		t.Fatalf("recordHLSSegment: %v", err)
	}
	var got *library.LiveIncident
	for i := range sess.incidents {
		if sess.incidents[i].Kind == library.LiveIncidentLostSegments {
			got = &sess.incidents[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("expected lost_segments incident, got %+v", sess.incidents)
	}
	if got.LostSegments != 3 || got.DurationSec != 3.0 {
		t.Fatalf("lost_segments = %+v, want LostSegments=3 DurationSec=3.0 (not 6.0 from TARGETDURATION:2)", got)
	}
}

// Session-level failed host tracking deprioritizes recently failed hosts on
// subsequent reconnects and expires automatically after failedHostTTL.
func TestSessionFailedHostDeprioritizedAcrossReconnects(t *testing.T) {
	now := time.Date(2026, 11, 24, 18, 0, 0, 0, time.UTC)
	mgr := &Manager{nowFunc: func() time.Time { return now }}
	sess := &recordSession{}
	urls := []string{
		"https://edge-a.bilivideo.com/live/stream.m3u8?e=1",
		"https://edge-b.bilivideo.com/live/stream.m3u8?e=1",
		"https://edge-c.bilivideo.com/live/stream.m3u8?e=1",
	}

	mgr.noteSessionFailedHost(sess, urls[0])
	ordered := mgr.orderSessionURLs(sess, urls)
	if ordered[0] != urls[1] || ordered[1] != urls[2] || ordered[2] != urls[0] {
		t.Fatalf("ordered = %v, want edge-b, edge-c, edge-a", ordered)
	}

	// After failedHostTTL expires, original order is restored.
	now = now.Add(failedHostTTL + time.Second)
	restored := mgr.orderSessionURLs(sess, urls)
	if restored[0] != urls[0] || restored[1] != urls[1] || restored[2] != urls[2] {
		t.Fatalf("after TTL restored = %v, want original order", restored)
	}
}

// Local checkpoint persistence latency in HTTP-FLV recording must not pollute
// prevCheckAt or trigger false LiveIncidentSlow warnings.
func TestFLVSlowDetectorExcludesLocalCheckpointWait(t *testing.T) {
	flvBytes := buildSyntheticFLVBytes(t, "avc", 90) // 3s of video -> multiple GOP checkpoints
	mgr, lib, base := regressionRecorder(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/live.flv" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "video/x-flv")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(flvBytes)
	}))

	clock := newFakeAdvanceClock(time.Date(2026, 11, 24, 20, 0, 0, 0, time.UTC))
	mgr.SetNowFunc(clock.Now)
	mgr.checkpointInterval = time.Nanosecond // exercise checkpoint callback on every GOP
	// Simulate 40s of local disk/manifest stall inside SaveLiveItem.
	lib.OnSaved(func(library.ItemMetadata) {
		clock.Advance(40 * time.Second)
	})

	sess := regressionLiveSession(t, lib)
	spec := site.BilibiliMediaTransport(true)
	var conn *activeFLVConn
	defer func() {
		if conn != nil {
			_ = conn.Close()
		}
	}()
	partPath := filepath.Join(sess.itemDir, "part-001.mp4.part")
	written, err := mgr.recordFLVPartWithTransport(context.Background(), sess, spec, []string{base + "/live.flv"}, &conn, partPath)
	if err != nil {
		t.Fatalf("recordFLVPartWithTransport: written=%d err=%v", written, err)
	}
	if written <= 0 {
		t.Fatal("expected FLV bytes written")
	}
	for _, inc := range sess.incidents {
		if inc.Kind == library.LiveIncidentSlow {
			t.Fatalf("local checkpoint stall caused false slow incident: %+v", inc)
		}
	}
}

// Async startup recovery's commitRecoveredPart and concurrent StartRecording for
// the same itemDir must serialize their GetItem -> SaveLiveItem -> register
// critical sections so neither overwrites the other with a stale manifest snapshot.
func TestAsyncRecoveryCommitSerializesWithConcurrentStartRecording(t *testing.T) {
	setupScenario := func(t *testing.T, roomID, uid, liveSince int64) (*Manager, *library.Store, string, *atomic.Bool) {
		t.Helper()
		initBytes, fragBytes := regressionLiveSamples(t)
		var stillLive atomic.Bool
		stillLive.Store(true)
		var srvURL string

		mux := http.NewServeMux()
		registerStandardBilibiliLiveHandlers(mux, roomID, uid, liveSince, &stillLive)
		mux.HandleFunc("/xlive/web-room/v2/index/getRoomPlayInfo", func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(buildBiliPlayInfoResp(roomID, uid, srvURL, "/hls/index.m3u8"))
		})
		mux.HandleFunc("/hls/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
			stillLive.Store(false)
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:20\n#EXT-X-MAP:URI=\"/hls/init.mp4\"\n#EXTINF:1.0,\n/hls/seg20.m4s\n#EXT-X-ENDLIST\n"))
		})
		mux.HandleFunc("/hls/init.mp4", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(initBytes) })
		mux.HandleFunc("/hls/seg20.m4s", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(fragBytes) })

		mgr, lib, _, srv, _ := setupLiveTestHarness(t, mux)
		srvURL = srv.URL
		mgr.mu.Lock()
		mgr.hlsPollInterval = 15 * time.Millisecond
		mgr.reconnectWait = 15 * time.Millisecond
		mgr.mu.Unlock()

		dir := filepath.Join(lib.CacheDir(), "bilibili", fmt.Sprint(uid), "lives", fmt.Sprintf("%d-%d", roomID, liveSince))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		rawPart := append(append([]byte(nil), initBytes...), fragBytes...)
		if err := os.WriteFile(filepath.Join(dir, "part-001.mp4.part"), rawPart, 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := lib.SaveLiveItem(dir, library.ItemMetadata{
			Site:         "bilibili",
			Kind:         "live",
			ID:           fmt.Sprintf("live:%d:%d", roomID, liveSince),
			Title:        "Concurrent Recovery Race Test",
			Status:       "recording",
			PendingParts: []string{"part-001.mp4.part"},
			Live: &library.LiveRecordingInfo{
				RoomID:    roomID,
				LiveSince: liveSince,
			},
		})
		if err != nil {
			t.Fatalf("seed orphan manifest: %v", err)
		}
		return mgr, lib, dir, &stillLive
	}

	t.Run("start enters commit slot before recovery commits", func(t *testing.T) {
		const (
			roomID    int64 = 71010
			uid       int64 = 7110
			liveSince int64 = 1796010000
		)
		mgr, lib, dir, _ := setupScenario(t, roomID, uid, liveSince)

		convertFinished := make(chan struct{})
		allowRecoveryCommit := make(chan struct{})
		origConvert := convertPart
		convertPart = func(ctx context.Context, partPath, finalPath string) (download.MuxTrackSummary, error) {
			sum, err := origConvert(ctx, partPath, finalPath)
			if strings.HasSuffix(partPath, "part-001.mp4.part") {
				close(convertFinished)
				<-allowRecoveryCommit
			}
			return sum, err
		}
		t.Cleanup(func() { convertPart = origConvert })

		var startSaveHookOnce sync.Once
		mgr.testBeforeStartSave = func(itemDir string) {
			if cleanLiveDirKey(itemDir) != cleanLiveDirKey(dir) {
				return
			}
			startSaveHookOnce.Do(func() {
				// Release recovery right while StartRecording is between GetItem and SaveLiveItem.
				// Without per-itemDir commit serialization, recovery would commit and remove
				// part-001.mp4.part here, and StartRecording would overwrite item.json with its
				// stale snapshot!
				close(allowRecoveryCommit)
				time.Sleep(80 * time.Millisecond)
				if _, err := os.Stat(filepath.Join(dir, "part-001.mp4.part")); err != nil {
					t.Errorf("part-001.mp4.part removed before StartRecording released commit slot: %v", err)
				}
			})
		}

		mgr.Start(context.Background())
		select {
		case <-convertFinished:
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for recovery convertPart")
		}

		if _, err := mgr.StartRecording(context.Background(), roomID); err != nil {
			t.Fatalf("StartRecording: %v", err)
		}
		waitLiveRecordingDone(t, mgr, roomID)
		mgr.Stop()

		meta, ok, err := lib.GetItem(library.CanonicalItemKey("bilibili", "live", fmt.Sprintf("%d:%d", roomID, liveSince)))
		if err != nil || !ok || meta.Live == nil {
			t.Fatalf("GetItem: ok=%v err=%v", ok, err)
		}
		if meta.Status != "complete" {
			t.Fatalf("Status = %q, want complete (pending=%v missing=%+v)", meta.Status, meta.PendingParts, meta.Missing)
		}
		if len(meta.PendingParts) != 0 {
			t.Fatalf("PendingParts = %v, want empty", meta.PendingParts)
		}
		if len(meta.Live.Segments) != 2 || meta.Live.Segments[0].File != "part-001.mp4" || meta.Live.Segments[1].File != "part-002.mp4" {
			t.Fatalf("Live.Segments = %+v, want [part-001.mp4 part-002.mp4]", meta.Live.Segments)
		}
		if _, err := os.Stat(filepath.Join(dir, "part-001.mp4.part")); !os.IsNotExist(err) {
			t.Fatalf("expected part-001.mp4.part deleted after commit, stat err=%v", err)
		}
	})

	t.Run("recovery enters commit slot before start loads manifest", func(t *testing.T) {
		const (
			roomID    int64 = 71011
			uid       int64 = 7111
			liveSince int64 = 1796011000
		)
		mgr, lib, dir, _ := setupScenario(t, roomID, uid, liveSince)

		recoveryInCommit := make(chan struct{})
		var recoveryHookOnce sync.Once
		mgr.testInRecoveryCommit = func(itemDir string) {
			if cleanLiveDirKey(itemDir) != cleanLiveDirKey(dir) {
				return
			}
			recoveryHookOnce.Do(func() {
				close(recoveryInCommit)
				time.Sleep(80 * time.Millisecond)
			})
		}

		mgr.Start(context.Background())
		select {
		case <-recoveryInCommit:
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for recovery commit slot")
		}

		if _, err := mgr.StartRecording(context.Background(), roomID); err != nil {
			t.Fatalf("StartRecording: %v", err)
		}
		waitLiveRecordingDone(t, mgr, roomID)
		mgr.Stop()

		meta, ok, err := lib.GetItem(library.CanonicalItemKey("bilibili", "live", fmt.Sprintf("%d:%d", roomID, liveSince)))
		if err != nil || !ok || meta.Live == nil {
			t.Fatalf("GetItem: ok=%v err=%v", ok, err)
		}
		if meta.Status != "complete" {
			t.Fatalf("Status = %q, want complete (pending=%v missing=%+v)", meta.Status, meta.PendingParts, meta.Missing)
		}
		if len(meta.PendingParts) != 0 {
			t.Fatalf("PendingParts = %v, want empty", meta.PendingParts)
		}
		if len(meta.Live.Segments) != 2 || meta.Live.Segments[0].File != "part-001.mp4" || meta.Live.Segments[1].File != "part-002.mp4" {
			t.Fatalf("Live.Segments = %+v, want [part-001.mp4 part-002.mp4]", meta.Live.Segments)
		}
	})
}

// commitSessionMeta must release sess.saveMu before invoking m.logEvent on both
// success and error slow-IO paths so logging callbacks never run under session locks.
func TestCommitSessionMetaUnlocksSaveMuBeforeLogging(t *testing.T) {
	origThreshold := slowLiveIOThreshold
	slowLiveIOThreshold = 0
	t.Cleanup(func() { slowLiveIOThreshold = origThreshold })

	mgr, lib, _ := regressionRecorder(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	sess := regressionLiveSession(t, lib)

	var logCalls atomic.Int32
	mgr.SetLogFunc(func(level, msg string) {
		if !strings.Contains(msg, "op=save_session") {
			return
		}
		logCalls.Add(1)
		if !sess.saveMu.TryLock() {
			t.Errorf("sess.saveMu was still held during commitSessionMeta logEvent (%s: %s)", level, msg)
			return
		}
		sess.saveMu.Unlock()
	})

	// 1. Success branch
	if err := mgr.commitSessionMeta(sess, false, nil, nil); err != nil {
		t.Fatalf("commitSessionMeta success branch failed: %v", err)
	}
	if logCalls.Load() != 1 {
		t.Fatalf("expected 1 slow_io log on success branch, got %d", logCalls.Load())
	}

	// 2. Error branch (SaveLiveItem rejects non-live Kind)
	err := mgr.commitSessionMeta(sess, false, func(meta *library.ItemMetadata) {
		meta.Kind = "video"
	}, nil)
	if err == nil {
		t.Fatal("expected SaveLiveItem error for non-live Kind")
	}
	if logCalls.Load() != 2 {
		t.Fatalf("expected 2 slow_io logs after error branch, got %d", logCalls.Load())
	}
}

// Candidate URL failover inside recordHLSSegment and OpenHTTPFLVStream must recover
// seamlessly when an initial node returns a blocked variant URI, bad init segment,
// or blocked FLV redirect, and HLS 302 redirects must resolve relative URIs against
// the final redirected playlist URL.
func TestLiveCandidateHostFallbackAndPlaylistRedirectRelativeURI(t *testing.T) {
	t.Run("HLS falls back from blocked variant host to backup candidate", func(t *testing.T) {
		initBytes, fragBytes := regressionLiveSamples(t)
		var fallbackLogged atomic.Bool
		mgr, lib, base := regressionRecorder(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/bad-variant/index.m3u8":
				// Non-loopback http:// variant is rejected even when AllowLocalhost is true.
				_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-STREAM-INF:BANDWIDTH=6000000\nhttp://blocked-edge.example.net/live/index.m3u8\n"))
			case "/good/index.m3u8":
				_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:1\n#EXT-X-MAP:URI=\"/good/init.mp4\"\n#EXTINF:1.0,\n/good/seg1.m4s\n#EXT-X-ENDLIST\n"))
			case "/good/init.mp4":
				_, _ = w.Write(initBytes)
			case "/good/seg1.m4s":
				_, _ = w.Write(fragBytes)
			default:
				http.NotFound(w, r)
			}
		}))
		mgr.SetLogFunc(func(level, msg string) {
			if strings.Contains(msg, "live hls host_fallback:") && strings.Contains(msg, "reason=blocked_variant") {
				fallbackLogged.Store(true)
			}
		})
		sess := regressionLiveSession(t, lib)
		partPath := filepath.Join(sess.itemDir, "part-001.mp4.part")
		n, err := mgr.recordHLSSegment(context.Background(), sess, []string{base + "/bad-variant/index.m3u8", base + "/good/index.m3u8"}, partPath, true)
		if err != nil {
			t.Fatalf("expected fallback to good candidate, got err=%v", err)
		}
		if n != int64(len(initBytes)+len(fragBytes)) || !sess.wroteMedia {
			t.Fatalf("written=%d wroteMedia=%v, want %d/true", n, sess.wroteMedia, len(initBytes)+len(fragBytes))
		}
		if !fallbackLogged.Load() {
			t.Fatal("expected live hls host_fallback log for blocked_variant")
		}
	})

	t.Run("HLS falls back from bad init host to backup candidate", func(t *testing.T) {
		initBytes, fragBytes := regressionLiveSamples(t)
		mgr, lib, base := regressionRecorder(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/bad-init/index.m3u8":
				_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:1\n#EXT-X-MAP:URI=\"/bad-init/init.mp4\"\n#EXTINF:1.0,\n/bad-init/seg1.m4s\n"))
			case "/bad-init/init.mp4":
				_, _ = w.Write([]byte("not-a-valid-moov"))
			case "/good/index.m3u8":
				_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:1\n#EXT-X-MAP:URI=\"/good/init.mp4\"\n#EXTINF:1.0,\n/good/seg1.m4s\n#EXT-X-ENDLIST\n"))
			case "/good/init.mp4":
				_, _ = w.Write(initBytes)
			case "/good/seg1.m4s":
				_, _ = w.Write(fragBytes)
			default:
				http.NotFound(w, r)
			}
		}))
		sess := regressionLiveSession(t, lib)
		partPath := filepath.Join(sess.itemDir, "part-001.mp4.part")
		n, err := mgr.recordHLSSegment(context.Background(), sess, []string{base + "/bad-init/index.m3u8", base + "/good/index.m3u8"}, partPath, true)
		if err != nil || n != int64(len(initBytes)+len(fragBytes)) {
			t.Fatalf("bad-init fallback failed: n=%d err=%v", n, err)
		}
	})

	t.Run("HLS 302 redirect resolves relative URIs against final redirected URL", func(t *testing.T) {
		initBytes, fragBytes := regressionLiveSamples(t)
		mgr, lib, base := regressionRecorder(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/dispatch/index.m3u8":
				http.Redirect(w, r, "/edge/live/index.m3u8", http.StatusFound)
			case "/edge/live/index.m3u8":
				_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:5\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:1.0,\nseg5.m4s\n#EXT-X-ENDLIST\n"))
			case "/edge/live/init.mp4":
				_, _ = w.Write(initBytes)
			case "/edge/live/seg5.m4s":
				_, _ = w.Write(fragBytes)
			default:
				http.NotFound(w, r)
			}
		}))
		sess := regressionLiveSession(t, lib)
		partPath := filepath.Join(sess.itemDir, "part-001.mp4.part")
		n, err := mgr.recordHLSSegment(context.Background(), sess, []string{base + "/dispatch/index.m3u8"}, partPath, true)
		if err != nil || n != int64(len(initBytes)+len(fragBytes)) {
			t.Fatalf("302 relative URI resolution failed: n=%d err=%v", n, err)
		}
	})

	t.Run("HTTP-FLV falls back past single-host blocked redirect to backup URL", func(t *testing.T) {
		flvBytes := buildSyntheticFLVBytes(t, "avc", 35)
		_, _, base := regressionRecorder(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/blocked.flv":
				http.Redirect(w, r, "http://blocked-edge.example.net/stream.flv", http.StatusFound)
			case "/good.flv":
				w.Header().Set("Content-Type", "video/x-flv")
				_, _ = w.Write(flvBytes)
			default:
				http.NotFound(w, r)
			}
		}))
		var failedURLs []string
		rc, openedURL, err := openHTTPFLVStreamWithCallback(
			context.Background(),
			site.BilibiliLiveTransport(true),
			[]string{base + "/blocked.flv", base + "/good.flv"},
			time.Second,
			func(u string) { failedURLs = append(failedURLs, u) },
		)
		if err != nil {
			t.Fatalf("expected FLV fallback to good URL, got err=%v", err)
		}
		_ = rc.Close()
		if openedURL != base+"/good.flv" || len(failedURLs) != 1 || failedURLs[0] != base+"/blocked.flv" {
			t.Fatalf("openedURL=%q failedURLs=%v", openedURL, failedURLs)
		}
	})
}

// Normal 1-hour CDN token expiry (HTTP 403 after media was served) must NOT demote
// the healthy primary host into failedHosts, whereas an immediate 403 on a fresh
// selection still demotes the broken host.
func TestHLS403AfterMediaDoesNotDemoteHealthyHost(t *testing.T) {
	initBytes, fragBytes := regressionLiveSamples(t)

	t.Run("403 after media served keeps host healthy", func(t *testing.T) {
		var polls atomic.Int32
		mgr, lib, base := regressionRecorder(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/hls/index.m3u8":
				if polls.Add(1) == 1 {
					_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:10\n#EXT-X-MAP:URI=\"/hls/init.mp4\"\n#EXTINF:1.0,\n/hls/seg10.m4s\n"))
					return
				}
				w.WriteHeader(http.StatusForbidden)
			case "/hls/init.mp4":
				_, _ = w.Write(initBytes)
			case "/hls/seg10.m4s":
				_, _ = w.Write(fragBytes)
			default:
				http.NotFound(w, r)
			}
		}))
		sess := regressionLiveSession(t, lib)
		partPath := filepath.Join(sess.itemDir, "part-001.mp4.part")
		n, err := mgr.recordHLSSegment(context.Background(), sess, []string{base + "/hls/index.m3u8"}, partPath, true)
		if httpStatusCodeOf(err) != http.StatusForbidden || n <= 0 {
			t.Fatalf("got n=%d err=%v, want >0 bytes and HTTP 403", n, err)
		}
		mgr.mu.Lock()
		failedCount := len(sess.failedHosts)
		mgr.mu.Unlock()
		if failedCount != 0 {
			t.Fatalf("healthy host was demoted after post-media 403 expiry: failedHosts=%+v", sess.failedHosts)
		}
	})

	t.Run("immediate 403 on fresh selection demotes host", func(t *testing.T) {
		mgr, lib, base := regressionRecorder(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		sess := regressionLiveSession(t, lib)
		partPath := filepath.Join(sess.itemDir, "part-001.mp4.part")
		n, err := mgr.recordHLSSegment(context.Background(), sess, []string{base + "/hls/index.m3u8"}, partPath, true)
		if httpStatusCodeOf(err) != http.StatusForbidden || n != 0 {
			t.Fatalf("got n=%d err=%v, want 0 bytes and HTTP 403", n, err)
		}
		mgr.mu.Lock()
		failedCount := len(sess.failedHosts)
		mgr.mu.Unlock()
		if failedCount != 1 {
			t.Fatalf("expected fresh 403 host to be demoted, failedHosts=%+v", sess.failedHosts)
		}
	})
}

// Deferred init-header writing prevents empty init-only .part files and bogus
// convert_failed incidents when the first media segment of a new part fails,
// and an aged-out front segment (404) on a new part skips to the next segment
// in the same playlist without reconnecting.
func TestHLSDeferredInitWritePreventsEmptyPartAndSkipsPostDiscontinuity404(t *testing.T) {
	t.Run("first segment truncated after discontinuity never creates init-only convert_failed part", func(t *testing.T) {
		const (
			roomID    int64 = 71020
			uid       int64 = 7120
			liveSince int64 = 1796020000
		)
		initBytes, fragBytes := regressionLiveSamples(t)
		var stillLive atomic.Bool
		stillLive.Store(true)
		var playCalls atomic.Int32
		var aHits atomic.Int32
		var srvURL string

		mux := http.NewServeMux()
		registerStandardBilibiliLiveHandlers(mux, roomID, uid, liveSince, &stillLive)
		mux.HandleFunc("/xlive/web-room/v2/index/getRoomPlayInfo", func(w http.ResponseWriter, r *http.Request) {
			path := "/hls/a.m3u8"
			if playCalls.Add(1) >= 2 {
				path = "/hls/b.m3u8"
			}
			_ = json.NewEncoder(w).Encode(buildBiliPlayInfoResp(roomID, uid, srvURL, path))
		})
		mux.HandleFunc("/hls/a.m3u8", func(w http.ResponseWriter, r *http.Request) {
			if aHits.Add(1) == 1 {
				_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:10\n#EXT-X-MAP:URI=\"/hls/init.mp4\"\n#EXTINF:1.0,\n/hls/good.m4s\n"))
				return
			}
			// Discontinuity triggers rollover to part-002; first segment of part-002 is truncated.
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:11\n#EXT-X-MAP:URI=\"/hls/init.mp4\"\n#EXT-X-DISCONTINUITY\n#EXTINF:1.0,\n/hls/bad.m4s\n"))
		})
		mux.HandleFunc("/hls/b.m3u8", func(w http.ResponseWriter, r *http.Request) {
			stillLive.Store(false)
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:12\n#EXT-X-MAP:URI=\"/hls/init.mp4\"\n#EXTINF:1.0,\n/hls/good.m4s\n#EXT-X-ENDLIST\n"))
		})
		mux.HandleFunc("/hls/init.mp4", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(initBytes) })
		mux.HandleFunc("/hls/good.m4s", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(fragBytes) })
		mux.HandleFunc("/hls/bad.m4s", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("truncated")) })

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

		meta, ok, err := lib.GetItem(library.CanonicalItemKey("bilibili", "live", fmt.Sprintf("%d:%d", roomID, liveSince)))
		if err != nil || !ok || meta.Live == nil {
			t.Fatalf("GetItem: ok=%v err=%v", ok, err)
		}
		if meta.Status != "complete" || len(meta.PendingParts) != 0 || len(meta.Missing) != 0 {
			t.Fatalf("expected complete item with no pending/missing parts, got status=%q pending=%v missing=%+v", meta.Status, meta.PendingParts, meta.Missing)
		}
		// Failed empty part-002 attempt must NOT burn part number 2; reconnected part must be part-002.mp4.
		if len(meta.Live.Segments) != 2 || meta.Live.Segments[0].File != "part-001.mp4" || meta.Live.Segments[1].File != "part-002.mp4" {
			t.Fatalf("segments = %+v, want [part-001.mp4 part-002.mp4]", meta.Live.Segments)
		}
		for _, inc := range meta.Live.Incidents {
			if inc.Kind == library.LiveIncidentConvertFailed {
				t.Fatalf("unexpected convert_failed incident: %+v", inc)
			}
		}
		if _, err := os.Stat(filepath.Join(meta.ItemDir, "part-002.mp4.part")); !os.IsNotExist(err) {
			t.Fatalf("orphan init-only part-002.mp4.part should not exist: %v", err)
		}
	})

	t.Run("post-discontinuity front segment 404 skips cleanly without reconnect", func(t *testing.T) {
		const (
			roomID    int64 = 71021
			uid       int64 = 7121
			liveSince int64 = 1796021000
		)
		initBytes, fragBytes := regressionLiveSamples(t)
		var stillLive atomic.Bool
		stillLive.Store(true)
		var playCalls atomic.Int32
		var m3u8Hits atomic.Int32
		var srvURL string

		mux := http.NewServeMux()
		registerStandardBilibiliLiveHandlers(mux, roomID, uid, liveSince, &stillLive)
		mux.HandleFunc("/xlive/web-room/v2/index/getRoomPlayInfo", func(w http.ResponseWriter, r *http.Request) {
			playCalls.Add(1)
			_ = json.NewEncoder(w).Encode(buildBiliPlayInfoResp(roomID, uid, srvURL, "/hls/index.m3u8"))
		})
		mux.HandleFunc("/hls/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
			switch m3u8Hits.Add(1) {
			case 1:
				_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:20\n#EXT-X-MAP:URI=\"/hls/init.mp4\"\n#EXTINF:1.0,\n/hls/good.m4s\n"))
			default:
				stillLive.Store(false)
				// Discontinuity rolls over to part-002; seq 21 returns 404, seq 22 is valid.
				_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:21\n#EXT-X-MAP:URI=\"/hls/init.mp4\"\n#EXT-X-DISCONTINUITY\n#EXTINF:1.0,\n/hls/expired.m4s\n#EXTINF:1.0,\n/hls/good.m4s\n#EXT-X-ENDLIST\n"))
			}
		})
		mux.HandleFunc("/hls/init.mp4", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(initBytes) })
		mux.HandleFunc("/hls/expired.m4s", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) })
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

		if playCalls.Load() != 1 {
			t.Fatalf("playCalls = %d, want 1 (post-discontinuity front 404 must skip without reconnect)", playCalls.Load())
		}
		meta, ok, err := lib.GetItem(library.CanonicalItemKey("bilibili", "live", fmt.Sprintf("%d:%d", roomID, liveSince)))
		if err != nil || !ok || meta.Live == nil {
			t.Fatalf("GetItem: ok=%v err=%v", ok, err)
		}
		if meta.Status != "complete" || len(meta.Live.Segments) != 2 {
			t.Fatalf("expected 2 complete parts, got status=%q segments=%+v", meta.Status, meta.Live.Segments)
		}
		for _, inc := range meta.Live.Incidents {
			if inc.Kind == library.LiveIncidentConvertFailed {
				t.Fatalf("unexpected convert_failed incident: %+v", inc)
			}
		}
	})
}

// Startup recovery must automatically clean up stranded init-only 0-sample .part
// files (clearing PendingParts, Missing, and convert_failed incidents) while
// preserving genuinely corrupt non-MP4 .part files for inspection.
func TestRecoveryDiscardsStrandedInitOnlyPartAndPreservesCorruptPart(t *testing.T) {
	initBytes, fragBytes := regressionLiveSamples(t)
	mgr, lib, _ := regressionRecorder(t, http.NotFoundHandler())
	var discardedLogged atomic.Bool
	mgr.SetLogFunc(func(level, msg string) {
		if strings.Contains(msg, "live recover discarded_empty:") && strings.Contains(msg, "part=part-002.mp4.part") {
			discardedLogged.Store(true)
		}
	})

	dir := filepath.Join(lib.CacheDir(), "bilibili", "7122", "lives", "71022-1796022000")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	rawPart := append(append([]byte(nil), initBytes...), fragBytes...)
	part1Tmp := filepath.Join(dir, "part-001.mp4.part")
	part1Path := filepath.Join(dir, "part-001.mp4")
	if err := os.WriteFile(part1Tmp, rawPart, 0o600); err != nil {
		t.Fatal(err)
	}
	sum1, err := finalizeLivePart(context.Background(), part1Tmp, part1Path)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(part1Tmp)

	// Seed stranded init-only 2-track part-002.mp4.part (0 samples).
	emptyPartPath := filepath.Join(dir, "part-002.mp4.part")
	if err := os.WriteFile(emptyPartPath, buildSyntheticTwoTrackInitBytes(t), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = lib.SaveLiveItem(dir, library.ItemMetadata{
		Site:         "bilibili",
		Kind:         "live",
		ID:           "live:71022:1796022000",
		Title:        "Stranded Init Only Recovery",
		Status:       "partial",
		PendingParts: []string{"part-002.mp4.part"},
		Missing:      []library.MissingRecord{{Item: "part-002.mp4.part", Reason: "转换失败，临时片段已保留"}},
		Media: []library.MediaFile{{
			Role:     "live_segment",
			Path:     "part-001.mp4",
			Bytes:    sum1.FileBytes,
			SHA256:   sum1.SHA256,
			Duration: 1,
		}},
		Live: &library.LiveRecordingInfo{
			RoomID:     71022,
			LiveSince:  1796022000,
			StopReason: "ended",
			Segments: []library.LiveSegmentInfo{{
				File:        "part-001.mp4",
				Bytes:       sum1.FileBytes,
				SHA256:      sum1.SHA256,
				DurationSec: sum1.DurationSec,
			}},
			Incidents: []library.LiveIncident{
				{Kind: library.LiveIncidentConvertFailed, At: "2026-11-27T00:00:00Z", Part: "part-002.mp4.part", Count: 1, Reason: "直播录像转换失败：track 2 has no samples"},
				{Kind: library.LiveIncidentReconnect, At: "2026-11-27T01:00:00Z", Part: "part-001.mp4.part", Count: 1, DurationSec: 1.2},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Also seed a second item with a genuinely corrupt non-MP4 .part that must NOT be discarded.
	corruptDir := filepath.Join(lib.CacheDir(), "bilibili", "7123", "lives", "71023-1796023000")
	if err := os.MkdirAll(corruptDir, 0o700); err != nil {
		t.Fatal(err)
	}
	corruptPartPath := filepath.Join(corruptDir, "part-001.mp4.part")
	if err := os.WriteFile(corruptPartPath, []byte("not-mp4-corrupt-payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = lib.SaveLiveItem(corruptDir, library.ItemMetadata{
		Site:         "bilibili",
		Kind:         "live",
		ID:           "live:71023:1796023000",
		Title:        "Corrupt Non-MP4 Preserved",
		Status:       "partial",
		PendingParts: []string{"part-001.mp4.part"},
		Live: &library.LiveRecordingInfo{
			RoomID:     71023,
			LiveSince:  1796023000,
			StopReason: "ended",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	mgr.RecoverOrphanParts()

	meta, ok, err := lib.GetItem(library.CanonicalItemKey("bilibili", "live", "71022:1796022000"))
	if err != nil || !ok || meta.Live == nil {
		t.Fatalf("GetItem init-only item: ok=%v err=%v", ok, err)
	}
	if meta.Status != "complete" || len(meta.PendingParts) != 0 || len(meta.Missing) != 0 {
		t.Fatalf("stranded init-only part not cleaned up: status=%q pending=%v missing=%+v", meta.Status, meta.PendingParts, meta.Missing)
	}
	if len(meta.Live.Incidents) != 1 || meta.Live.Incidents[0].Kind != library.LiveIncidentReconnect {
		t.Fatalf("expected convert_failed cleared and reconnect preserved, got %+v", meta.Live.Incidents)
	}
	if _, statErr := os.Stat(emptyPartPath); !os.IsNotExist(statErr) {
		t.Fatalf("stranded init-only .part file still present on disk: %v", statErr)
	}
	if !discardedLogged.Load() {
		t.Fatal("expected live recover discarded_empty diagnostic log")
	}

	corruptMeta, ok, err := lib.GetItem(library.CanonicalItemKey("bilibili", "live", "71023:1796023000"))
	if err != nil || !ok || corruptMeta.Status != "partial" || len(corruptMeta.PendingParts) != 1 {
		t.Fatalf("corrupt non-MP4 .part must stay pending/partial: ok=%v err=%v meta=%+v", ok, err, corruptMeta)
	}
	if _, statErr := os.Stat(corruptPartPath); statErr != nil {
		t.Fatalf("corrupt non-MP4 .part must be preserved on disk: %v", statErr)
	}
}

// Abnormal live record stop must log a sanitized err= field without leaking signed URLs.
func TestAbnormalLiveRecordStopLogsSanitizedError(t *testing.T) {
	const (
		roomID    int64 = 71024
		uid       int64 = 7124
		liveSince int64 = 1796024000
	)
	var stillLive atomic.Bool
	stillLive.Store(true)
	var srvURL string
	mux := http.NewServeMux()
	registerStandardBilibiliLiveHandlers(mux, roomID, uid, liveSince, &stillLive)
	mux.HandleFunc("/xlive/web-room/v2/index/getRoomPlayInfo", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(buildBiliPlayInfoResp(roomID, uid, srvURL, "/hls/broken.m3u8"))
	})
	mux.HandleFunc("/hls/broken.m3u8", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>broken upstream</html>"))
	})

	mgr, _, _, srv, _ := setupLiveTestHarness(t, mux)
	srvURL = srv.URL
	var stopLine atomic.Value
	mgr.SetLogFunc(func(level, msg string) {
		if strings.HasPrefix(msg, "live record stop:") {
			stopLine.Store(msg)
		}
	})
	if _, err := mgr.StartRecording(context.Background(), roomID); err != nil {
		t.Fatalf("StartRecording: %v", err)
	}
	waitLiveRecordingDone(t, mgr, roomID)

	got, _ := stopLine.Load().(string)
	if !strings.Contains(got, "reason=schema_changed") || !strings.Contains(got, "err=") {
		t.Fatalf("stop log missing reason or err: %q", got)
	}
	if strings.Contains(got, "http://") || strings.Contains(got, "https://") {
		t.Fatalf("stop log leaked URL: %q", got)
	}
}
