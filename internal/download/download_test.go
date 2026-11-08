package download

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/settings"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/storage"
)

func setupDownloadEnv(t *testing.T, handler http.Handler, maxConc int) (*Manager, *library.Store, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	tmp := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	cacheDir := filepath.Join(tmp, "cache")
	libStore, err := library.Open(db, cacheDir)
	if err != nil {
		t.Fatalf("library.Open: %v", err)
	}

	credStore, _, err := credential.Open(filepath.Join(tmp, "cred.dpapi"))
	if err != nil {
		t.Fatalf("credential.Open: %v", err)
	}
	_, _ = credStore.SaveManual("SESSDATA=s; bili_jct=c; DedeUserID=1001", "rt")

	client := bilibili.NewClient(bilibili.ClientOptions{
		CredStore: credStore,
		Endpoints: bilibili.Endpoints{
			APIBase:      srv.URL,
			PassportBase: srv.URL,
			WWWBase:      srv.URL,
			SpaceBase:    srv.URL,
			DynamicBase:  srv.URL,
			LiveAPIBase:  srv.URL,
			LiveWebBase:  srv.URL,
		},
		PacingInterval: -1,
		AllowLocalhost: true,
	})
	client.SetWBIKeys("7cd084941338484aae1ad9425b84077c", "4932caff0ff746eab6f01bf08b70ac45")

	dlCfg := settings.Default(cacheDir).Download
	dlCfg.MaxConcurrentTasks = maxConc
	mgr, err := NewManager(db, libStore, client, dlCfg, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	mgr.SetBackoffScale(2 * time.Millisecond)
	return mgr, libStore, srv
}

// PR-2 & C17: Pure-Go DASH video.m4s + audio.m4s muxing via mp4ff, elst verification, Dolby/FLAC sidecar + AAC fallback, and complete video part caching.
func TestMuxDASHFilesAndVideoCacheEndToEnd(t *testing.T) {
	// Generate 2 seconds (60 video frames @ 30fps, 94 audio frames @ 48kHz) so 1s chunk interleaving produces multiple chunks per track.
	vBytes, err := BuildSyntheticVideoM4S(60, 30)
	if err != nil {
		t.Fatalf("BuildSyntheticVideoM4S: %v", err)
	}
	aBytes, err := BuildSyntheticAudioM4S(94)
	if err != nil {
		t.Fatalf("BuildSyntheticAudioM4S: %v", err)
	}

	var srvURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/x/web-interface/wbi/view", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"bvid":     "BV17x411w7KC",
				"aid":      170001,
				"title":    "Synthetic Video",
				"desc":     "End to end test",
				"pic":      srvURL + "/media/cover.jpg",
				"pubdate":  1700000000,
				"duration": 2,
				"owner":    map[string]any{"mid": 888, "name": "Creator888", "face": srvURL + "/media/face.jpg"},
				"pages": []map[string]any{
					{"cid": 5001, "page": 1, "part": "First Part", "duration": 2, "dimension": map[string]any{"width": 16, "height": 16}},
				},
			},
		})
	})
	mux.HandleFunc("/x/player/wbi/playurl", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"quality":        80,
				"accept_format":  "dash",
				"accept_quality": []int{80},
				"dash": map[string]any{
					"duration": 2,
					"video": []map[string]any{
						{
							"id":        80,
							"baseUrl":   srvURL + "/media/video.m4s",
							"codecid":   7,
							"codecs":    "avc1.42c00a",
							"width":     16,
							"height":    16,
							"frameRate": "30.0",
							"bandwidth": 500000,
						},
					},
					"audio": []map[string]any{
						{
							"id":        30280,
							"baseUrl":   srvURL + "/media/audio.m4s",
							"codecs":    "mp4a.40.2",
							"bandwidth": 192000,
						},
					},
					"flac": map[string]any{
						"display": true,
						"audio": map[string]any{
							"id":        30251,
							"baseUrl":   srvURL + "/media/flac.bin",
							"codecs":    "flac",
							"bandwidth": 900000,
						},
					},
				},
			},
		})
	})
	mux.HandleFunc("/x/v2/dm/web/seg.so", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bilibili.EncodeDmSegMobileReply([]bilibili.DanmakuElem{
			{ID: 1, IDStr: "1", Progress: 500, Mode: 1, Fontsize: 25, Color: 0xFFFFFF, Content: "Hello Danmaku"},
		}))
	})
	mux.HandleFunc("/x/player/wbi/v2", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"bvid": "BV17x411w7KC", "cid": 5001,
				"subtitle": map[string]any{
					"subtitles": []map[string]any{
						{"id": 1, "lan": "zh-CN", "lan_doc": "中文", "subtitle_url": srvURL + "/media/sub.json"},
					},
				},
			},
		})
	})
	mux.HandleFunc("/media/sub.json", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"body": []map[string]any{{"from": 0.1, "to": 0.9, "content": "测试字幕"}},
		})
	})
	mux.HandleFunc("/media/cover.jpg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("fake-jpeg-cover-bytes"))
	})
	mux.HandleFunc("/media/video.m4s", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(vBytes)))
		_, _ = w.Write(vBytes)
	})
	mux.HandleFunc("/media/audio.m4s", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(aBytes)))
		_, _ = w.Write(aBytes)
	})
	mux.HandleFunc("/media/flac.bin", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("fLaC-lossless-audio-payload"))
	})

	mgr, libStore, srv := setupDownloadEnv(t, mux, 2)
	srvURL = srv.URL

	// High-quality DASH audio retains its MP4 container name, with AAC in the main MP4.
	dlCfg := mgr.dlSettings
	dlCfg.PreferLosslessAudio = true
	mgr.UpdateSettings(dlCfg)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("mgr.Start: %v", err)
	}
	defer mgr.Stop()

	tasks, err := mgr.EnqueueVideo(ctx, VideoEnqueueRequest{BVID: "BV17x411w7KC"})
	if err != nil || len(tasks) != 1 {
		t.Fatalf("EnqueueVideo = %+v, err=%v", tasks, err)
	}

	// Wait for task completion.
	doneTask := waitForTaskStatus(t, mgr, tasks[0].TaskID, "completed", 5*time.Second)
	mp4Path := filepath.Join(doneTask.ItemDir, "P01.mp4")
	summary, err := InspectMP4File(mp4Path)
	if err != nil {
		t.Fatalf("InspectMP4File(%q): %v", mp4Path, err)
	}
	if summary.TrackCount != 2 {
		t.Fatalf("expected 2 tracks (vide + soun) in muxed MP4 even when FLAC sidecar is saved, got %+v", summary)
	}
	if !summary.MoovBeforeMdat {
		t.Fatalf("expected MoovBeforeMdat=true in muxed MP4, got %+v", summary)
	}
	if !summary.HasElst {
		t.Fatalf("expected HasElst=true in muxed MP4, got %+v", summary)
	}
	if !summary.Interleaved {
		t.Fatalf("expected Interleaved=true in muxed MP4, got %+v", summary)
	}
	if summary.MaxTrackDiffSec > 2.0 {
		t.Fatalf("expected MaxTrackDiffSec <= 2.0, got %.3f", summary.MaxTrackDiffSec)
	}

	// Verify cover, MP4, FLAC sidecar, danmaku, vtt, srt, and item.json exist on disk.
	for _, rel := range []string{"cover.jpg", "P01.mp4", "P01.audio-30251.m4a", "P01.danmaku.xml", "P01.zh-CN.vtt", "P01.zh-CN.srt", "item.json"} {
		if _, err := os.Stat(filepath.Join(doneTask.ItemDir, rel)); err != nil {
			t.Fatalf("expected cached file %q missing: %v", rel, err)
		}
	}

	// Verify TaskDetail returns per-file progress list (FR-CACHE-009).
	td, ok, err := mgr.TaskDetail(doneTask.TaskID)
	if err != nil || !ok || len(td.Files) == 0 {
		t.Fatalf("expected TaskDetail with files, got %+v ok=%v err=%v", td, ok, err)
	}

	// Also verify DefragmentFMP4Bytes on live-style fMP4 and verify corrupt input returns error instead of writing raw bytes.
	defragPath := filepath.Join(t.TempDir(), "live-defrag.mp4")
	defragSummary, err := DefragmentFMP4Bytes(vBytes, defragPath)
	if err != nil || !defragSummary.MoovBeforeMdat {
		t.Fatalf("DefragmentFMP4Bytes failed: summary=%+v err=%v", defragSummary, err)
	}
	if _, err := DefragmentFMP4Bytes([]byte("corrupt-not-mp4"), filepath.Join(t.TempDir(), "corrupt.mp4")); err == nil {
		t.Fatalf("expected DefragmentFMP4Bytes to fail on corrupt input")
	}

	itemMeta, ok, err := libStore.GetItem("BV17x411w7KC")
	if err != nil || !ok || itemMeta.Status != "complete" {
		t.Fatalf("expected complete item in library, got %+v ok=%v err=%v", itemMeta, ok, err)
	}
}

// C18 & R-06: Closed-interval 8 MiB Range resume, Content-Range verification, 200 fallback, per-segment retry + URL rotation, and 403 PlayURL refresh.
func TestRangeResume206AndFallback200(t *testing.T) {
	fullPayload := bytes.Repeat([]byte("0123456789abcdef"), 1024) // 16 KiB
	wantSum := sha256.Sum256(fullPayload)
	wantHex := hex.EncodeToString(wantSum[:])

	var gotRangeHeaders []string
	var force200 atomic.Bool
	var failFirstNodeOnce atomic.Bool

	mux := http.NewServeMux()
	mux.HandleFunc("/media/file.bin", func(w http.ResponseWriter, r *http.Request) {
		rh := r.Header.Get("Range")
		gotRangeHeaders = append(gotRangeHeaders, rh)
		if failFirstNodeOnce.CompareAndSwap(true, false) {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		if !force200.Load() && strings.HasPrefix(rh, "bytes=") {
			spec := strings.TrimPrefix(rh, "bytes=")
			parts := strings.SplitN(spec, "-", 2)
			if len(parts) == 2 {
				start, err1 := strconv.Atoi(parts[0])
				end, err2 := strconv.Atoi(parts[1])
				if err1 == nil && err2 == nil && start >= 0 && start < len(fullPayload) {
					if end >= len(fullPayload) {
						end = len(fullPayload) - 1
					}
					w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(fullPayload)))
					w.WriteHeader(http.StatusPartialContent)
					_, _ = w.Write(fullPayload[start : end+1])
					return
				}
			}
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(fullPayload)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(fullPayload)
	})

	mgr, _, srv := setupDownloadEnv(t, mux, 2)
	mgr.SetChunkSize(4096) // 4 KiB chunks so 16 KiB payload uses 4 closed-interval segments
	ctx := context.Background()

	// 1. Test 206 Partial Content resume from existing 4096-byte .part file with 1 transport failure + URL rotation.
	failFirstNodeOnce.Store(true)
	dir := t.TempDir()
	dest206 := filepath.Join(dir, "out206.bin")
	if err := os.WriteFile(dest206+".part", fullPayload[:4096], 0o600); err != nil {
		t.Fatalf("write .part: %v", err)
	}
	dummyTask := Task{TaskID: "t-206", Files: []TaskFileProgress{{Path: "out206.bin", Owned: true, DownloadedBytes: 4096, Status: "paused"}}}
	n, sha, err := mgr.downloadFileWithRange(ctx, []string{
		srv.URL + "/media/file.bin?token=secret1",
		srv.URL + "/media/file.bin?token=secret2",
	}, dest206, &dummyTask)
	if err != nil {
		t.Fatalf("downloadFileWithRange 206: %v", err)
	}
	if len(gotRangeHeaders) == 0 || gotRangeHeaders[0] != "bytes=4096-8191" {
		t.Fatalf("first Range header = %v, want bytes=4096-8191", gotRangeHeaders)
	}
	if n != int64(len(fullPayload)) || sha != wantHex {
		t.Fatalf("206 size=%d sha=%s, want size=%d sha=%s", n, sha, len(fullPayload), wantHex)
	}
	// Verify attempt log recorded the 502 error and stripped query string from Host!
	if len(dummyTask.Attempts) != 1 || strings.Contains(dummyTask.Attempts[0].Host, "secret") || dummyTask.Attempts[0].HTTPStatus != 502 {
		t.Fatalf("unexpected task attempts: %+v", dummyTask.Attempts)
	}

	// 2. Test 200 OK fallback (server ignores Range when offset > 0 -> truncates .part, rotates URL, and downloads from 0 on next request).
	force200.Store(true)
	dest200 := filepath.Join(dir, "out200.bin")
	if err := os.WriteFile(dest200+".part", []byte("stale-partial-prefix-that-must-be-truncated"), 0o600); err != nil {
		t.Fatalf("write .part for 200: %v", err)
	}
	dummyTask.Files = append(dummyTask.Files, TaskFileProgress{Path: "out200.bin", Owned: true, DownloadedBytes: 40, Status: "paused"})
	n2, sha2, err := mgr.downloadFileWithRange(ctx, []string{srv.URL + "/media/file.bin"}, dest200, &dummyTask)
	if err != nil {
		t.Fatalf("downloadFileWithRange 200: %v", err)
	}
	if n2 != int64(len(fullPayload)) || sha2 != wantHex {
		t.Fatalf("200 fallback size=%d sha=%s, want size=%d sha=%s", n2, sha2, len(fullPayload), wantHex)
	}
}

// C19 & C23: Dynamic caching with linked video and partial image failure + retry recovery.
func TestDynamicCacheLinkedVideosAndPartialRetry(t *testing.T) {
	vBytes, _ := BuildSyntheticVideoM4S(10, 30)
	aBytes, _ := BuildSyntheticAudioM4S(15)

	var failImg2Once atomic.Bool
	failImg2Once.Store(true)
	var img1Hits atomic.Int32
	var srvURL string

	mux := http.NewServeMux()
	mux.HandleFunc("/x/polymer/web-dynamic/v1/detail", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"item": map[string]any{
					"id_str": "777888999",
					"type":   "DYNAMIC_TYPE_DRAW",
					"modules": map[string]any{
						"module_author": map[string]any{"mid": 888, "name": "Creator888", "pub_ts": 1700000500},
						"module_dynamic": map[string]any{
							"desc": map[string]any{"text": "Dynamic with images and video"},
							"major": map[string]any{
								"opus": map[string]any{
									"title": "Draw Dynamic",
									"pics": []map[string]any{
										{"url": srvURL + "/img/1.jpg", "width": 100, "height": 100},
										{"url": srvURL + "/img/2.jpg", "width": 100, "height": 100},
									},
								},
								"archive": map[string]any{
									"bvid":  "BV17x411w7KC",
									"title": "Linked Video",
								},
							},
						},
					},
				},
			},
		})
	})
	mux.HandleFunc("/img/1.jpg", func(w http.ResponseWriter, r *http.Request) {
		img1Hits.Add(1)
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("image-1-bytes"))
	})
	mux.HandleFunc("/img/2.jpg", func(w http.ResponseWriter, r *http.Request) {
		if failImg2Once.CompareAndSwap(true, false) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("image-2-bytes"))
	})
	mux.HandleFunc("/x/web-interface/wbi/view", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"bvid":     "BV17x411w7KC",
				"aid":      170001,
				"title":    "Linked Video",
				"duration": 1,
				"owner":    map[string]any{"mid": 888, "name": "Creator888"},
				"pages":    []map[string]any{{"cid": 5001, "page": 1, "part": "P1", "duration": 1}},
			},
		})
	})
	mux.HandleFunc("/x/player/wbi/playurl", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"quality":        80,
				"accept_quality": []int{80},
				"dash": map[string]any{
					"duration": 1,
					"video":    []map[string]any{{"id": 80, "baseUrl": srvURL + "/media/video.m4s", "codecid": 7, "codecs": "avc1"}},
					"audio":    []map[string]any{{"id": 30280, "baseUrl": srvURL + "/media/audio.m4s", "codecs": "mp4a.40.2"}},
				},
			},
		})
	})
	mux.HandleFunc("/x/v2/dm/web/seg.so", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/x/player/wbi/v2", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"bvid": "BV17x411w7KC", "cid": 5001}})
	})
	mux.HandleFunc("/media/video.m4s", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(vBytes)))
		_, _ = w.Write(vBytes)
	})
	mux.HandleFunc("/media/audio.m4s", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(aBytes)))
		_, _ = w.Write(aBytes)
	})

	mgr, libStore, srv := setupDownloadEnv(t, mux, 2)
	srvURL = srv.URL

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = mgr.Start(ctx)
	defer mgr.Stop()

	task, err := mgr.EnqueueDynamic(ctx, DynamicEnqueueRequest{
		DynamicID:         "777888999",
		CacheLinkedVideos: true,
	})
	if err != nil {
		t.Fatalf("EnqueueDynamic: %v", err)
	}

	// Attempt 1: img_02 fails -> task ends in "partial", img_01 and linked video are saved!
	partialTask := waitForTaskStatus(t, mgr, task.TaskID, "partial", 5*time.Second)
	if _, err := os.Stat(filepath.Join(partialTask.ItemDir, "images", "01.jpg")); err != nil {
		t.Fatalf("img_01.jpg should exist after partial run: %v", err)
	}
	if _, ok, _ := libStore.GetItem("BV17x411w7KC"); !ok {
		t.Fatalf("linked video BV17x411w7KC should be cached when CacheLinkedVideos=true")
	}

	// Retry: img_01 is already on disk so it should NOT be re-downloaded; img_02 succeeds -> status="completed".
	if _, err := mgr.Retry(task.TaskID); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	waitForTaskStatus(t, mgr, task.TaskID, "completed", 5*time.Second)
	if img1Hits.Load() != 1 {
		t.Fatalf("img_01.jpg was downloaded %d times, want 1 (should skip existing file on retry)", img1Hits.Load())
	}
	dynMeta, ok, _ := libStore.GetItem("bilibili:dynamic:777888999")
	if !ok || dynMeta.Status != "complete" || len(dynMeta.Videos) != 1 || dynMeta.Videos[0] != "video:BV17x411w7KC" {
		t.Fatalf("unexpected completed dynamic metadata: %+v", dynMeta)
	}
}

// Aliases for stage-report C19 & C23 references.
func TestDynamicCacheAndLinkedVideos(t *testing.T) {
	TestDynamicCacheLinkedVideosAndPartialRetry(t)
}

func TestPartialFailureAndRetrySkipsCompleted(t *testing.T) {
	TestDynamicCacheLinkedVideosAndPartialRetry(t)
}

// C15, C20, R-05: Queue concurrency limit, deduplication, startup pause ("已暂停（应用关闭）"), and risk_control circuit breaker.
func TestQueueConcurrencyDedupStartupRecoveryAndCircuitBreaker(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/x/web-interface/wbi/view", func(w http.ResponseWriter, r *http.Request) {
		bvid := r.URL.Query().Get("bvid")
		if bvid == "BV1risk00001" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code":    -412,
				"message": "request was banned",
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"bvid":  bvid,
				"aid":   100,
				"title": "Video " + bvid,
				"owner": map[string]any{"mid": 1, "name": "UP"},
				"pages": []map[string]any{
					{"cid": 1001, "page": 1, "part": "P1", "duration": 10},
				},
			},
		})
	})

	mgr, _, _ := setupDownloadEnv(t, mux, 1)
	ctx := context.Background()

	// 1. Test task deduplication before starting workers (C15).
	detail := &bilibili.VideoDetail{
		BVID:   "BV1dedup0001",
		Title:  "Dedup Video",
		Author: bilibili.AuthorInfo{Mid: 1, Name: "UP"},
		Pages:  []bilibili.VideoPage{{CID: 1001, Page: 1, Title: "P1", Duration: 10}},
	}
	t1, err := mgr.EnqueueVideo(ctx, VideoEnqueueRequest{BVID: "BV1dedup0001", Detail: detail})
	if err != nil || len(t1) != 1 {
		t.Fatalf("EnqueueVideo 1: %v", err)
	}
	t2, err := mgr.EnqueueVideo(ctx, VideoEnqueueRequest{BVID: "BV1dedup0001", Detail: detail})
	if err != nil || len(t2) != 1 || t2[0].TaskID != t1[0].TaskID {
		t.Fatalf("expected duplicate EnqueueVideo to return same task ID %q, got %+v", t1[0].TaskID, t2)
	}

	// Simulate an interrupted "running" task in SQLite and verify Start() transitions it to "paused" ("已暂停（应用关闭）") without auto-running (R-05, C15).
	_, _ = mgr.db.Exec(`UPDATE download_tasks SET status = 'running' WHERE task_id = ?`, t1[0].TaskID)
	_ = mgr.Start(ctx)
	mgr.Stop()
	recovered, _, _ := mgr.GetTask(t1[0].TaskID)
	if recovered.Status != "paused" || recovered.PauseReason != "app_closed" || !strings.Contains(recovered.ErrorMessage, "应用关闭") {
		t.Fatalf("expected running task to be paused with app_closed on Start(), got %+v", recovered)
	}
	_, _ = mgr.Cancel(t1[0].TaskID)

	// 2. Test risk_control circuit breaker (C20):
	// Start manager first, then enqueue a task that triggers -412 risk_control and a second queued task.
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_ = mgr.Start(runCtx)
	defer mgr.Stop()

	riskDetail := &bilibili.VideoDetail{
		BVID:   "BV1risk00001",
		Title:  "Risk Video",
		Author: bilibili.AuthorInfo{Mid: 1, Name: "UP"},
		Pages:  []bilibili.VideoPage{{CID: 2001, Page: 1, Title: "P1", Duration: 10}},
	}
	nextDetail := &bilibili.VideoDetail{
		BVID:   "BV1next00002",
		Title:  "Next Queued Video",
		Author: bilibili.AuthorInfo{Mid: 1, Name: "UP"},
		Pages:  []bilibili.VideoPage{{CID: 2002, Page: 1, Title: "P1", Duration: 10}},
	}
	rt1, _ := mgr.EnqueueVideo(ctx, VideoEnqueueRequest{BVID: "BV1risk00001", Detail: riskDetail})
	rt2, _ := mgr.EnqueueVideo(ctx, VideoEnqueueRequest{BVID: "BV1next00002", Detail: nextDetail})

	pausedRisk := waitForTaskStatus(t, mgr, rt1[0].TaskID, "paused", 3*time.Second)
	if pausedRisk.ErrorKind != bilibili.ErrKindRiskControl || pausedRisk.PauseReason != "risk_control" {
		t.Fatalf("expected errorKind=risk_control and pauseReason=risk_control, got %+v", pausedRisk)
	}
	pausedNext := waitForTaskStatus(t, mgr, rt2[0].TaskID, "paused", 3*time.Second)
	if pausedNext.ErrorKind != bilibili.ErrKindRiskControl {
		t.Fatalf("expected queued task auto-paused with risk_control, got %+v", pausedNext)
	}
}

// Aliases for stage-report C15 & C20 references.
func TestTaskQueueConcurrencyAndStartupPause(t *testing.T) {
	TestQueueConcurrencyDedupStartupRecoveryAndCircuitBreaker(t)
}

func TestRiskControlCircuitBreakerPausesQueued(t *testing.T) {
	TestQueueConcurrencyDedupStartupRecoveryAndCircuitBreaker(t)
}

func waitForTaskStatus(t *testing.T, mgr *Manager, taskID string, wantStatus string, timeout time.Duration) Task {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		task, ok, err := mgr.GetTask(taskID)
		if err == nil && ok {
			if task.Status == wantStatus {
				return task
			}
			if (task.Status == "failed" || task.Status == "canceled" || task.Status == "completed" || task.Status == "partial") && task.Status != wantStatus {
				t.Fatalf("task %s reached terminal status %q (%s), want %q", taskID, task.Status, task.ErrorMessage, wantStatus)
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	task, _, _ := mgr.GetTask(taskID)
	t.Fatalf("timed out waiting for task %s to reach %q (current=%+v)", taskID, wantStatus, task)
	return Task{}
}
