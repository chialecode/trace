package download

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The user's own `rclone move` takes a finished part and
// item.json away while the next part is still downloading. The task still
// completes, the moved part is not downloaded again, and the final
// item.json lists every part.
func TestCachingContinuesWhenFinishedFilesAreMovedAway(t *testing.T) {
	vBytes, err := BuildSyntheticVideoM4S(30, 30)
	if err != nil {
		t.Fatal(err)
	}
	aBytes, err := BuildSyntheticAudioM4S(47)
	if err != nil {
		t.Fatal(err)
	}
	var srvURL string
	var firstTask atomic.Value // func() string: P1's item folder
	var videoHits [3]atomic.Int32
	var moved atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/x/web-interface/wbi/view", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"bvid": "BV1Mv411w7KC", "aid": 170002, "title": "两P合成视频", "pic": srvURL + "/media/cover.jpg", "pubdate": 1700000000, "duration": 2,
			"owner": map[string]any{"mid": 888, "name": "Creator888"},
			"pages": []map[string]any{
				{"cid": 6001, "page": 1, "part": "P1", "duration": 1, "dimension": map[string]any{"width": 16, "height": 16}},
				{"cid": 6002, "page": 2, "part": "P2", "duration": 1, "dimension": map[string]any{"width": 16, "height": 16}},
			},
		}})
	})
	mux.HandleFunc("/x/player/wbi/playurl", func(w http.ResponseWriter, r *http.Request) {
		cid := r.URL.Query().Get("cid")
		if cid == "6002" && !moved.Load() {
			// P1 is finished: move it and item.json away like rclone move.
			if fn, ok := firstTask.Load().(func() string); ok && fn() != "" {
				dir := fn()
				_ = os.Remove(filepath.Join(dir, "P01.mp4"))
				_ = os.Remove(filepath.Join(dir, "item.json"))
				moved.Store(true)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"quality": 80, "accept_format": "dash", "accept_quality": []int{80},
			"dash": map[string]any{"duration": 1,
				"video": []map[string]any{{"id": 80, "baseUrl": srvURL + "/media/video.m4s?cid=" + cid, "codecid": 7, "codecs": "avc1.42c00a", "width": 16, "height": 16, "frameRate": "30.0", "bandwidth": 500000}},
				"audio": []map[string]any{{"id": 30280, "baseUrl": srvURL + "/media/audio.m4s", "codecs": "mp4a.40.2", "bandwidth": 192000}},
			},
		}})
	})
	mux.HandleFunc("/media/cover.jpg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("fake-jpeg-cover-bytes"))
	})
	mux.HandleFunc("/media/video.m4s", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("cid") {
		case "6001":
			videoHits[1].Add(1)
		case "6002":
			videoHits[2].Add(1)
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(vBytes)))
		_, _ = w.Write(vBytes)
	})
	mux.HandleFunc("/media/audio.m4s", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(aBytes)))
		_, _ = w.Write(aBytes)
	})

	mgr, lib, srv := setupDownloadEnv(t, mux, 1)
	srvURL = srv.URL
	checkNames := watchNames(t, lib.CacheDir())
	cfg := mgr.dlSettings
	cfg.SaveDanmaku, cfg.SaveSubtitles = false, false
	mgr.UpdateSettings(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()
	tasks, err := mgr.EnqueueVideo(ctx, VideoEnqueueRequest{BVID: "BV1Mv411w7KC"})
	if err != nil || len(tasks) != 2 {
		t.Fatalf("EnqueueVideo = %+v, %v", tasks, err)
	}
	firstTask.Store(func() string {
		if tk, ok, _ := mgr.GetTask(tasks[0].TaskID); ok && tk.Status == "completed" {
			return tk.ItemDir
		}
		return ""
	})
	waitForTaskStatus(t, mgr, tasks[0].TaskID, "completed", 10*time.Second)
	done := waitForTaskStatus(t, mgr, tasks[1].TaskID, "completed", 10*time.Second)
	if !moved.Load() {
		t.Fatal("the test did not move P01 away during the download")
	}
	checkNames("P01.mp4", "item.json")
	if n := videoHits[1].Load(); n != 1 {
		t.Fatalf("P01 was downloaded %d times, want 1", n)
	}
	if _, err := os.Stat(filepath.Join(done.ItemDir, "P02.mp4")); err != nil {
		t.Fatalf("P02.mp4: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(done.ItemDir, "item.json"))
	if err != nil {
		t.Fatalf("item.json was not written again after the move: %v", err)
	}
	var meta struct {
		Parts []struct {
			Page int    `json:"page"`
			File string `json:"file"`
		} `json:"parts"`
	}
	_ = json.Unmarshal(raw, &meta)
	if len(meta.Parts) != 2 {
		t.Fatalf("item.json parts = %+v, want both parts listed", meta.Parts)
	}
}

// A DASH stream paused by an older version under its old
// name (P01.video-….m4s.part) resumes from its offset under the temporary
// name, and no old name is left.
func TestLegacyStreamPartResumesUnderTemporaryName(t *testing.T) {
	vBytes, err := BuildSyntheticVideoM4S(60, 30)
	if err != nil {
		t.Fatal(err)
	}
	aBytes, err := BuildSyntheticAudioM4S(94)
	if err != nil {
		t.Fatal(err)
	}
	var srvURL string
	var hold atomic.Bool
	hold.Store(true)
	release := make(chan struct{})
	var resumedFrom atomic.Int64
	resumedFrom.Store(-1)
	mux := http.NewServeMux()
	mux.HandleFunc("/x/web-interface/wbi/view", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"bvid": "BV1Lg411w7KC", "aid": 170003, "title": "旧名续传", "pic": srvURL + "/media/cover.jpg", "pubdate": 1700000000, "duration": 2,
			"owner": map[string]any{"mid": 888, "name": "Creator888"},
			"pages": []map[string]any{{"cid": 7001, "page": 1, "part": "P1", "duration": 2, "dimension": map[string]any{"width": 16, "height": 16}}},
		}})
	})
	mux.HandleFunc("/x/player/wbi/playurl", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"quality": 80, "accept_format": "dash", "accept_quality": []int{80},
			"dash": map[string]any{"duration": 2,
				"video": []map[string]any{{"id": 80, "baseUrl": srvURL + "/media/video.m4s", "codecid": 7, "codecs": "avc1.42c00a", "width": 16, "height": 16, "frameRate": "30.0", "bandwidth": 500000}},
				"audio": []map[string]any{{"id": 30280, "baseUrl": srvURL + "/media/audio.m4s", "codecs": "mp4a.40.2", "bandwidth": 192000}},
			},
		}})
	})
	mux.HandleFunc("/media/cover.jpg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("fake-jpeg-cover-bytes"))
	})
	half := len(vBytes) / 2
	mux.HandleFunc("/media/video.m4s", func(w http.ResponseWriter, r *http.Request) {
		start := 0
		if rh := r.Header.Get("Range"); rh != "" {
			_, _ = fmt.Sscanf(rh, "bytes=%d-", &start)
		}
		if hold.Load() {
			// Serve the first half, then stall until the test pauses.
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(vBytes)-1, len(vBytes)))
			w.Header().Set("Content-Length", strconv.Itoa(len(vBytes)-start))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(vBytes[start:half])
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return
		}
		resumedFrom.CompareAndSwap(-1, int64(start))
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(vBytes)-1, len(vBytes)))
		w.Header().Set("Content-Length", strconv.Itoa(len(vBytes)-start))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(vBytes[start:])
	})
	mux.HandleFunc("/media/audio.m4s", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(aBytes)))
		_, _ = w.Write(aBytes)
	})
	mgr, _, srv := setupDownloadEnv(t, mux, 1)
	srvURL = srv.URL
	cfg := mgr.dlSettings
	cfg.SaveDanmaku, cfg.SaveSubtitles = false, false
	mgr.UpdateSettings(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()
	defer close(release)
	tasks, err := mgr.EnqueueVideo(ctx, VideoEnqueueRequest{BVID: "BV1Lg411w7KC"})
	if err != nil || len(tasks) != 1 {
		t.Fatalf("EnqueueVideo = %+v, %v", tasks, err)
	}
	const newRel = ".trace-P01.video-80-7.m4s"
	const oldRel = "P01.video-80-7.m4s"
	var itemDir string
	deadline := time.Now().Add(5 * time.Second)
	for {
		tk, _, _ := mgr.GetTask(tasks[0].TaskID)
		if tk.ItemDir != "" {
			if st, err := os.Stat(filepath.Join(tk.ItemDir, newRel+".part")); err == nil && st.Size() >= int64(half) {
				itemDir = tk.ItemDir
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the stream never reached half")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := mgr.Pause(tasks[0].TaskID); err != nil {
		t.Fatal(err)
	}
	waitForTaskStatus(t, mgr, tasks[0].TaskID, "paused", 5*time.Second)

	// Turn the paused state into what an older version left behind.
	// (Windows: the paused download may close the file a moment later.)
	for i := 0; ; i++ {
		err := os.Rename(filepath.Join(itemDir, newRel+".part"), filepath.Join(itemDir, oldRel+".part"))
		if err == nil {
			break
		}
		if i > 200 {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	tk, _, _ := mgr.GetTask(tasks[0].TaskID)
	for i := range tk.Files {
		if taskFileIdentity(tk.Files[i].Path) == newRel {
			tk.Files[i].Path = oldRel
		}
	}
	filesJSON, _ := json.Marshal(tk.Files)
	if _, err := mgr.db.Exec(`UPDATE download_tasks SET files_json = ? WHERE task_id = ?`, string(filesJSON), tk.TaskID); err != nil {
		t.Fatal(err)
	}

	hold.Store(false)
	if _, err := mgr.Resume(tasks[0].TaskID); err != nil {
		t.Fatal(err)
	}
	waitForTaskStatus(t, mgr, tasks[0].TaskID, "completed", 10*time.Second)
	if got := resumedFrom.Load(); got < int64(half) {
		t.Fatalf("resumed from byte %d, want at least %d (the old .part was not adopted)", got, half)
	}
	entries, _ := os.ReadDir(itemDir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".m4s") {
			t.Fatalf("a stream file is left after the MP4: %s", e.Name())
		}
	}
}
