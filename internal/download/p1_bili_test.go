package download

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
)

var syntheticJPEG = []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0x01, 0x01, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0xff, 0xd9}

func TestVideoCacheSavesViewPointsChaptersWithZeroExtraRequests(t *testing.T) {
	vBytes, err := BuildSyntheticVideoM4S(60, 30)
	if err != nil {
		t.Fatalf("BuildSyntheticVideoM4S: %v", err)
	}
	aBytes, err := BuildSyntheticAudioM4S(94)
	if err != nil {
		t.Fatalf("BuildSyntheticAudioM4S: %v", err)
	}

	var srvURL string
	var playerV2Calls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/x/web-interface/wbi/view", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"bvid":     "BV1ch411c7m1",
				"aid":      170101,
				"title":    "带看点章节的测试视频",
				"desc":     "chapters e2e",
				"pic":      srvURL + "/media/cover.jpg",
				"pubdate":  1700000000,
				"duration": 2,
				"owner":    map[string]any{"mid": 888, "name": "Creator888", "face": srvURL + "/media/face.jpg"},
				"pages": []map[string]any{
					{"cid": 5011, "page": 1, "part": "第一集", "duration": 2, "dimension": map[string]any{"width": 16, "height": 16}},
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
						{"id": 80, "baseUrl": srvURL + "/media/video.m4s", "codecid": 7, "codecs": "avc1.42c00a", "width": 16, "height": 16, "frameRate": "30.0", "bandwidth": 500000},
					},
					"audio": []map[string]any{
						{"id": 30280, "baseUrl": srvURL + "/media/audio.m4s", "codecs": "mp4a.40.2", "bandwidth": 192000},
					},
				},
			},
		})
	})
	dmReply := bilibili.EncodeDmSegMobileReply([]bilibili.DanmakuElem{
		{ID: 1, IDStr: "1", Progress: 500, Mode: 1, Fontsize: 25, Color: 0xFFFFFF, Content: "hello"},
	})
	mux.HandleFunc("/x/v2/dm/web/seg.so", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(dmReply)
	})
	mux.HandleFunc("/x/v2/dm/wbi/web/seg.so", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(dmReply)
	})
	mux.HandleFunc("/x/player/wbi/v2", func(w http.ResponseWriter, r *http.Request) {
		playerV2Calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"bvid":     "BV1ch411c7m1",
				"cid":      5011,
				"subtitle": map[string]any{"subtitles": []any{}},
				"view_points": []map[string]any{
					{"type": 2, "from": 1, "to": 2, "content": "结尾总结"},
					{"type": 2, "from": 0, "to": 1, "content": "开篇引入"},
				},
			},
		})
	})
	mux.HandleFunc("/media/video.m4s", func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "video.m4s", time.Unix(0, 0), bytes.NewReader(vBytes))
	})
	mux.HandleFunc("/media/audio.m4s", func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "audio.m4s", time.Unix(0, 0), bytes.NewReader(aBytes))
	})
	mux.HandleFunc("/media/cover.jpg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		http.ServeContent(w, r, "cover.jpg", time.Unix(0, 0), bytes.NewReader(syntheticJPEG))
	})
	mux.HandleFunc("/media/face.jpg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		http.ServeContent(w, r, "face.jpg", time.Unix(0, 0), bytes.NewReader(syntheticJPEG))
	})

	mgr, libStore, srv := setupDownloadEnv(t, mux, 1)
	srvURL = srv.URL

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr.Start(ctx)
	defer mgr.Stop()

	tasks, err := mgr.EnqueueVideo(ctx, VideoEnqueueRequest{BVID: "BV1ch411c7m1"})
	if err != nil {
		t.Fatalf("EnqueueVideo: %v", err)
	}
	waitForTaskStatus(t, mgr, tasks[0].TaskID, "completed", 10*time.Second)

	if playerV2Calls.Load() != 1 {
		t.Fatalf("expected exactly 1 /x/player/wbi/v2 request, got %d", playerV2Calls.Load())
	}
	got, ok, err := libStore.GetItem(library.CanonicalItemKey("bilibili", "video", "BV1ch411c7m1"))
	if err != nil || !ok {
		t.Fatalf("GetItem: ok=%v err=%v", ok, err)
	}
	if len(got.Parts) != 1 || len(got.Parts[0].Chapters) != 2 {
		t.Fatalf("expected 2 chapters saved in item.json, got %+v", got.Parts)
	}
	if got.Parts[0].Chapters[0] != (library.VideoChapter{FromSec: 0, ToSec: 1, Title: "开篇引入"}) ||
		got.Parts[0].Chapters[1] != (library.VideoChapter{FromSec: 1, ToSec: 2, Title: "结尾总结"}) {
		t.Fatalf("unexpected saved chapters: %+v", got.Parts[0].Chapters)
	}
}

func TestUpowerLockedInterceptedBeforePlayURLAndUnlockedCachesNormally(t *testing.T) {
	vBytes, err := BuildSyntheticVideoM4S(60, 30)
	if err != nil {
		t.Fatalf("BuildSyntheticVideoM4S: %v", err)
	}
	aBytes, err := BuildSyntheticAudioM4S(94)
	if err != nil {
		t.Fatalf("BuildSyntheticAudioM4S: %v", err)
	}

	var srvURL string
	var lockedPlayURLCalls atomic.Int32
	var unlockedPlayURLCalls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/x/web-interface/wbi/view", func(w http.ResponseWriter, r *http.Request) {
		bvid := r.URL.Query().Get("bvid")
		isPlay := bvid == "BV1upUnlock02"
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"bvid":                bvid,
				"aid":                 170201,
				"title":               "充电专属测试视频 " + bvid,
				"pic":                 srvURL + "/media/cover.jpg",
				"pubdate":             1700000000,
				"duration":            2,
				"is_upower_exclusive": true,
				"is_upower_play":      isPlay,
				"is_upower_preview":   !isPlay,
				"owner":               map[string]any{"mid": 888, "name": "Creator888", "face": srvURL + "/media/face.jpg"},
				"pages": []map[string]any{
					{"cid": 5021, "page": 1, "part": "P1", "duration": 2, "dimension": map[string]any{"width": 16, "height": 16}},
				},
			},
		})
	})
	mux.HandleFunc("/x/player/wbi/playurl", func(w http.ResponseWriter, r *http.Request) {
		bvid := r.URL.Query().Get("bvid")
		if bvid == "BV1upLocked01" {
			lockedPlayURLCalls.Add(1)
			t.Errorf("unexpected playurl request for locked upower video %s", bvid)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		unlockedPlayURLCalls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"quality":        80,
				"accept_format":  "dash",
				"accept_quality": []int{80},
				"dash": map[string]any{
					"duration": 2,
					"video": []map[string]any{
						{"id": 80, "baseUrl": srvURL + "/media/video.m4s", "codecid": 7, "codecs": "avc1.42c00a", "width": 16, "height": 16, "frameRate": "30.0", "bandwidth": 500000},
					},
					"audio": []map[string]any{
						{"id": 30280, "baseUrl": srvURL + "/media/audio.m4s", "codecs": "mp4a.40.2", "bandwidth": 192000},
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
	mux.HandleFunc("/x/polymer/web-dynamic/v1/detail", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"item": map[string]any{
					"id_str": "777666555444",
					"type":   "DYNAMIC_TYPE_DRAW",
					"basic":  map[string]any{"is_only_fans": true},
					"modules": map[string]any{
						"module_author": map[string]any{"mid": 888, "name": "Creator888", "pub_ts": 1700000000},
						"module_dynamic": map[string]any{
							"desc": map[string]any{"text": "专属图文"},
							"major": map[string]any{
								"type":    "MAJOR_TYPE_BLOCKED",
								"blocked": map[string]any{"hint_message": "成为充电会员查看"},
							},
						},
					},
				},
			},
		})
	})
	dmReply := bilibili.EncodeDmSegMobileReply([]bilibili.DanmakuElem{
		{ID: 1, IDStr: "1", Progress: 500, Mode: 1, Fontsize: 25, Color: 0xFFFFFF, Content: "hello"},
	})
	mux.HandleFunc("/x/v2/dm/web/seg.so", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(dmReply) })
	mux.HandleFunc("/x/v2/dm/wbi/web/seg.so", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(dmReply) })
	mux.HandleFunc("/x/player/wbi/v2", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"bvid": r.URL.Query().Get("bvid"), "cid": 5021, "subtitle": map[string]any{"subtitles": []any{}}}})
	})
	mux.HandleFunc("/media/video.m4s", func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "video.m4s", time.Unix(0, 0), bytes.NewReader(vBytes))
	})
	mux.HandleFunc("/media/audio.m4s", func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "audio.m4s", time.Unix(0, 0), bytes.NewReader(aBytes))
	})
	mux.HandleFunc("/media/flac.bin", func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "flac.bin", time.Unix(0, 0), bytes.NewReader([]byte("fLaC-synthetic")))
	})
	mux.HandleFunc("/media/cover.jpg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		http.ServeContent(w, r, "cover.jpg", time.Unix(0, 0), bytes.NewReader(syntheticJPEG))
	})
	mux.HandleFunc("/media/face.jpg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		http.ServeContent(w, r, "face.jpg", time.Unix(0, 0), bytes.NewReader(syntheticJPEG))
	})

	mgr, libStore, srv := setupDownloadEnv(t, mux, 1)
	srvURL = srv.URL

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr.Start(ctx)
	defer mgr.Stop()

	// 1. Locked upower video rejected at EnqueueVideo with ErrKindForbidden and 0 playurl calls.
	if _, err := mgr.EnqueueVideo(ctx, VideoEnqueueRequest{BVID: "BV1upLocked01"}); err == nil || site.KindOf(err) != site.ErrKindForbidden {
		t.Fatalf("expected ErrKindForbidden for locked upower video, got %v", err)
	}
	if lockedPlayURLCalls.Load() != 0 {
		t.Fatalf("expected 0 playurl calls for locked upower video, got %d", lockedPlayURLCalls.Load())
	}

	// 2. Direct execution path (cacheVideoPart / executeVideoTask) also intercepts before playurl.
	directTask := Task{TaskID: "direct-upower-locked", Kind: "video", Site: "bilibili", TargetID: "BV1upLocked01", ItemKey: library.CanonicalItemKey("bilibili", "video", "BV1upLocked01")}
	if err := mgr.executeVideoTask(ctx, &directTask); err == nil || site.KindOf(err) != site.ErrKindForbidden {
		t.Fatalf("expected ErrKindForbidden in executeVideoTask, got %v", err)
	}
	if lockedPlayURLCalls.Load() != 0 {
		t.Fatalf("expected 0 playurl calls after executeVideoTask, got %d", lockedPlayURLCalls.Load())
	}

	// 3. Blocked onlyfans dynamic rejected at EnqueueDynamic with ErrKindForbidden.
	if _, err := mgr.EnqueueDynamic(ctx, DynamicEnqueueRequest{DynamicID: "777666555444"}); err == nil || site.KindOf(err) != site.ErrKindForbidden {
		t.Fatalf("expected ErrKindForbidden for blocked onlyfans dynamic, got %v", err)
	}

	// 4. Unlocked upower video caches normally at highest quality + lossless audio.
	tasks, err := mgr.EnqueueVideo(ctx, VideoEnqueueRequest{BVID: "BV1upUnlock02"})
	if err != nil {
		t.Fatalf("EnqueueVideo unlocked: %v", err)
	}
	waitForTaskStatus(t, mgr, tasks[0].TaskID, "completed", 10*time.Second)
	if unlockedPlayURLCalls.Load() != 1 {
		t.Fatalf("expected 1 playurl call for unlocked upower video, got %d", unlockedPlayURLCalls.Load())
	}
	got, ok, err := libStore.GetItem(library.CanonicalItemKey("bilibili", "video", "BV1upUnlock02"))
	if err != nil || !ok || len(got.Parts) != 1 || got.Parts[0].Audio.Quality != 30251 || got.Parts[0].Audio.SavedAs == "" {
		t.Fatalf("expected unlocked upower video cached with lossless audio, got ok=%v err=%v item=%+v", ok, err, got)
	}
}
