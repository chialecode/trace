package download

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site/bilibili"
)

func TestSupplementFailurePreservesMissingState(t *testing.T) {
	manager, lib, _ := setupDownloadEnv(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) }), 2)
	dir := filepath.Join(lib.CacheDir(), "item")
	_ = os.MkdirAll(dir, 0700)
	_ = os.WriteFile(filepath.Join(dir, "video.mp4"), []byte("existing validated video"), 0600)
	item, err := lib.SaveItem(dir, library.ItemMetadata{Site: "bilibili", Kind: "video", ID: "BV1xx411c7mD", Status: "partial", Parts: []library.VideoPartRecord{{CID: 1, Page: 1, Duration: 1, File: "video.mp4"}}, Media: []library.MediaFile{{Role: "video", Path: "video.mp4"}}, Missing: []library.MissingRecord{{Item: "P01.danmaku.xml", Reason: "network"}}})
	if err != nil {
		t.Fatal(err)
	}
	task := Task{TaskID: "supplement", TargetID: item.RawID}
	if err := manager.executeSupplementTask(context.Background(), &task); err != nil {
		t.Fatal(err)
	}
	after, _, err := lib.GetItem(item.ItemKey)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != "partial" || len(after.Missing) == 0 {
		t.Fatalf("failed supplement reported complete: %+v", after)
	}
}

func TestCancelOnlyRemovesTaskOwnedParts(t *testing.T) {
	mgr, _, _ := setupDownloadEnv(t, http.NotFoundHandler(), 1)
	dir := t.TempDir()
	for _, name := range []string{"stream.m4s.part", "unclaimed.mp4.part", "personal-draft.part", "finished.mp4"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	task, err := mgr.enqueueSingleTask(Task{Kind: "video", TargetID: "BV1xx411c7mD", ItemDir: dir, Files: []TaskFileProgress{
		{Path: "stream.m4s", Status: "downloading", Owned: true},
		{Path: "unclaimed.mp4", Status: "waiting"},
		{Path: "finished.mp4", Status: "completed"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Cancel(task.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "stream.m4s.part")); !os.IsNotExist(err) {
		t.Fatalf("unfinished task file was not removed: %v", err)
	}
	for _, name := range []string{"unclaimed.mp4.part", "personal-draft.part", "finished.mp4"} {
		if raw, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(raw) != "keep" {
			t.Fatalf("cancel changed preserved file %s: %q %v", name, raw, err)
		}
	}
}

func TestStoppedTasksRejectLateWorkerProgress(t *testing.T) {
	for _, action := range []string{"pause", "cancel"} {
		t.Run(action, func(t *testing.T) {
			mgr, _, _ := setupDownloadEnv(t, http.NotFoundHandler(), 1)
			task, err := mgr.enqueueSingleTask(Task{Kind: "video", TargetID: "BV1xx411c7mD"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := mgr.db.Exec("UPDATE download_tasks SET status = 'running' WHERE task_id = ?", task.TaskID); err != nil {
				t.Fatal(err)
			}
			stale, _, _ := mgr.GetTask(task.TaskID)
			wantStatus := "paused"
			if action == "pause" {
				_, err = mgr.Pause(task.TaskID)
			} else {
				wantStatus = "canceled"
				_, err = mgr.Cancel(task.TaskID)
			}
			if err != nil {
				t.Fatal(err)
			}
			// A body reader can finish its current read after cancellation and publish
			// the snapshot it took while the task was still running.
			stale.BytesDownloaded = 1024
			mgr.saveTaskProgress(stale)
			after, _, err := mgr.GetTask(task.TaskID)
			if err != nil || after.Status != wantStatus {
				t.Fatalf("late progress undid %s: %+v %v", action, after, err)
			}
		})
	}
}

func TestShutdownPersistsPausedWork(t *testing.T) {
	started := make(chan struct{})
	mgr, _, _ := setupDownloadEnv(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}), 1)
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	task, err := mgr.EnqueueDynamic(context.Background(), DynamicEnqueueRequest{Item: &bilibili.DynamicItem{ID: "100", Author: bilibili.AuthorInfo{Mid: 1}}})
	if err != nil {
		mgr.Stop()
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		mgr.Stop()
		t.Fatal("task did not start")
	}
	mgr.Stop()
	after, _, err := mgr.GetTask(task.TaskID)
	if err != nil || after.Status != "paused" || after.PauseReason != "app_closed" {
		t.Fatalf("shutdown left active work: %+v %v", after, err)
	}
}

func TestMultiPartCachePreservesOtherPartFailures(t *testing.T) {
	video, err := BuildSyntheticVideoM4S(30, 30)
	if err != nil {
		t.Fatal(err)
	}
	var base string
	mgr, lib, server := setupDownloadEnv(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/x/web-interface/wbi/view":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
				"bvid": "BV1xx411c7mD", "title": "multi part", "owner": map[string]any{"mid": 1, "name": "synthetic"},
				"pages": []map[string]any{{"cid": 1, "page": 1, "part": "first", "duration": 1}, {"cid": 2, "page": 2, "part": "second", "duration": 1}},
			}})
		case "/x/player/wbi/playurl":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"dash": map[string]any{
				"video": []map[string]any{{"id": 80, "codecid": 7, "baseUrl": base + "/video", "codecs": "avc1"}},
			}}})
		case "/video":
			w.Header().Set("Content-Length", strconv.Itoa(len(video)))
			_, _ = w.Write(video)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}), 1)
	base = server.URL
	cfg := mgr.dlSettings
	cfg.SaveCover, cfg.SaveDanmaku, cfg.SaveSubtitles = false, false, false
	mgr.UpdateSettings(cfg)
	dir := filepath.Join(lib.CacheDir(), "bilibili", library.CreatorDirName(1, "synthetic"), library.ItemDirName("video", "BV1xx411c7mD"))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "P01.mp4"), video, 0600); err != nil {
		t.Fatal(err)
	}
	_, err = lib.SaveItem(dir, library.ItemMetadata{Site: "bilibili", Kind: "video", ID: "BV1xx411c7mD", Status: "partial",
		Parts: []library.VideoPartRecord{{CID: 1, Page: 1, File: "P01.mp4"}},
		Media: []library.MediaFile{{Role: "video", Path: "P01.mp4"}}, Missing: []library.MissingRecord{{Item: "P01.danmaku.xml", Reason: "network"}}})
	if err != nil {
		t.Fatal(err)
	}
	task := Task{Kind: "video", TargetID: "BV1xx411c7mD", CID: 2, Page: 2}
	if err := mgr.executeVideoTask(context.Background(), &task); err != nil {
		t.Fatal(err)
	}
	after, _, err := lib.GetItem("BV1xx411c7mD")
	if err != nil || len(after.Parts) != 2 || len(after.Missing) != 1 || library.ComputeDotState(after).Dot != "yellow" {
		t.Fatalf("second part erased first part's failure: %+v %v", after, err)
	}
}

func TestRangeRejectsUnverifiablePartialContent(t *testing.T) {
	for _, header := range []string{"", "bytes 0-9/10", "bytes 0-3/3"} {
		t.Run(header, func(t *testing.T) {
			mgr, _, server := setupDownloadEnv(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if header != "" {
					w.Header().Set("Content-Range", header)
				}
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write([]byte("bad"))
			}), 1)
			_, err := mgr.fetchSingleSegment(context.Background(), server.URL, filepath.Join(t.TempDir(), "stream.part"), 0, 3, 0, &Task{})
			if err == nil {
				t.Fatal("invalid Content-Range accepted")
			}
		})
	}
}

func TestMediaLengthAndImageFailureBoundaries(t *testing.T) {
	for _, scenario := range []string{"unknown-200", "overlong-206", "image-mime", "image-limit"} {
		t.Run(scenario, func(t *testing.T) {
			mgr, _, server := setupDownloadEnv(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch scenario {
				case "unknown-200":
					w.(http.Flusher).Flush()
				case "overlong-206":
					w.Header().Set("Content-Range", "bytes 0-3/4")
					w.WriteHeader(http.StatusPartialContent)
					w.(http.Flusher).Flush()
				case "image-mime":
					w.Header().Set("Content-Type", "text/html")
				case "image-limit":
					w.Header().Set("Content-Type", "image/jpeg")
					w.Header().Set("Content-Length", strconv.FormatInt(MaxImageFileBytes+1, 10))
				}
				_, _ = w.Write([]byte("too-long"))
			}), 1)
			dir := t.TempDir()
			task := Task{ItemDir: dir, Files: []TaskFileProgress{{Path: "image.jpg", Role: "image"}}}
			if !strings.HasPrefix(scenario, "image-") {
				task.Files[0].Role = "video_stream"
			}
			part := filepath.Join(dir, "image.jpg.part")
			if _, err := mgr.fetchSingleSegment(context.Background(), server.URL, part, 0, 3, 0, &task); err == nil {
				t.Fatal("unverifiable response was accepted")
			}
			if info, err := os.Stat(part); err == nil && info.Size() != 0 {
				t.Fatalf("invalid response left accepted bytes: %d", info.Size())
			}
		})
	}
}

func TestStorageFaultCanResumeAfterDatabaseRecovers(t *testing.T) {
	mgr, _, _ := setupDownloadEnv(t, http.NotFoundHandler(), 1)
	task, err := mgr.enqueueSingleTask(Task{Kind: "video", TargetID: "BV1xx411c7mD"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.db.Exec(`UPDATE download_tasks SET status = 'running' WHERE task_id = ?`, task.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.db.Exec(`CREATE TEMP TRIGGER fail_progress BEFORE UPDATE OF bytes_downloaded ON download_tasks BEGIN SELECT RAISE(ABORT, 'synthetic storage failure'); END`); err != nil {
		t.Fatal(err)
	}
	task.Status = "running"
	mgr.persistTaskProgress(task)
	failed, _, err := mgr.GetTask(task.TaskID)
	if err != nil || failed.Status != "failed" || failed.ErrorKind != "storage" {
		t.Fatalf("storage failure was hidden: %+v %v", failed, err)
	}
	if _, err := mgr.db.Exec(`DROP TRIGGER fail_progress`); err != nil {
		t.Fatal(err)
	}
	resumed, err := mgr.Retry(task.TaskID)
	if err != nil || resumed.Status != "queued" || resumed.ErrorKind != "" {
		t.Fatalf("recovered storage fault still masks retry: %+v %v", resumed, err)
	}
}

func TestOwnershipUsesDirectoryAndSupportsValidatedMP4Staging(t *testing.T) {
	mgr, _, _ := setupDownloadEnv(t, http.NotFoundHandler(), 1)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "personal"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "personal", "P01.mp4")
	if err := os.WriteFile(path, []byte("user file"), 0600); err != nil {
		t.Fatal(err)
	}
	task := Task{ItemDir: dir, Files: []TaskFileProgress{{Path: "P01.mp4", Owned: true}}}
	if err := mgr.claimTaskFile(&task, dir, "personal/P01.mp4", path); err == nil {
		t.Fatal("same basename in another directory was treated as task-owned")
	}
	if raw, _ := os.ReadFile(path); string(raw) != "user file" {
		t.Fatal("user file was changed")
	}
	stage := filepath.Join(dir, "P01.mp4.part")
	if err := os.WriteFile(stage, []byte("validated stage"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := mgr.claimTaskFile(&task, dir, "P01.mp4.part", stage); err != nil {
		t.Fatalf("an owned MP4 staging file cannot resume: %v", err)
	}
}

func TestDynamicRiskStopRetainsCompletedImageAndHash(t *testing.T) {
	var base string
	mgr, lib, server := setupDownloadEnv(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/x/polymer/web-dynamic/v1/detail":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"item": map[string]any{
				"id_str": "123456", "type": "DYNAMIC_TYPE_DRAW", "modules": map[string]any{
					"module_author":  map[string]any{"mid": 1, "name": "synthetic", "pub_ts": 1},
					"module_dynamic": map[string]any{"major": map[string]any{"opus": map[string]any{"pics": []map[string]any{{"url": base + "/first.jpg"}, {"url": base + "/risk.jpg"}}}}},
				},
			}}})
		case "/first.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("synthetic completed image"))
		case "/risk.jpg":
			w.WriteHeader(http.StatusPreconditionFailed)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}), 1)
	base = server.URL
	task := Task{Kind: "dynamic", TargetID: "123456"}
	if err := mgr.executeDynamicTask(context.Background(), &task); !bilibili.IsKind(err, bilibili.ErrKindRiskControl) {
		t.Fatalf("expected immediate risk stop, got %v", err)
	}
	item, ok, err := lib.GetItem("dynamic:123456")
	if err != nil || !ok || item.Status != "partial" || len(item.Media) != 1 || item.Media[0].SHA256 == "" || !library.VerifyMediaFile(item.ItemDir, item.Media[0], true) {
		t.Fatalf("risk stop lost completed image metadata: %+v %v", item, err)
	}
}

func TestHighAudioWithoutPlayableFallbackNeverCreatesSilentMainVideo(t *testing.T) {
	video, err := BuildSyntheticVideoM4S(30, 30)
	if err != nil {
		t.Fatal(err)
	}
	for _, fallback := range []string{"missing", "forbidden"} {
		t.Run(fallback, func(t *testing.T) {
			var base string
			mgr, lib, server := setupDownloadEnv(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/x/web-interface/wbi/view":
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"bvid": "BV1xx411c7mD", "title": "synthetic", "owner": map[string]any{"mid": 1}, "pages": []map[string]any{{"cid": 1, "page": 1, "duration": 1}}}})
				case "/x/player/wbi/playurl":
					dash := map[string]any{"video": []map[string]any{{"id": 80, "codecid": 7, "codecs": "avc1", "baseUrl": base + "/video"}}, "flac": map[string]any{"display": true, "audio": map[string]any{"id": 30251, "codecs": "flac", "baseUrl": base + "/lossless"}}}
					if fallback == "forbidden" {
						dash["audio"] = []map[string]any{{"id": 30280, "codecs": "mp4a.40.2", "baseUrl": base + "/aac"}}
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"dash": dash}})
				case "/video":
					w.Header().Set("Content-Length", strconv.Itoa(len(video)))
					_, _ = w.Write(video)
				case "/lossless":
					_, _ = w.Write([]byte("synthetic highest audio"))
				case "/aac":
					w.WriteHeader(http.StatusForbidden)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}), 1)
			base = server.URL
			cfg := mgr.dlSettings
			cfg.SaveCover, cfg.SaveDanmaku, cfg.SaveSubtitles, cfg.PreferLosslessAudio = false, false, false, true
			mgr.UpdateSettings(cfg)
			task := Task{Kind: "video", TargetID: "BV1xx411c7mD", CID: 1, Page: 1}
			if err := mgr.executeVideoTask(context.Background(), &task); err == nil {
				t.Fatal("missing or failed AAC produced a completed video")
			}
			item, ok, err := lib.GetItem(task.TargetID)
			if err != nil || !ok || library.ComputeDotState(item).Dot == "green" {
				t.Fatalf("incomplete audio reported green: %+v %v", item, err)
			}
			if _, err := os.Stat(filepath.Join(item.ItemDir, "P01.mp4")); !os.IsNotExist(err) {
				t.Fatalf("silent main video was created: %v", err)
			}
			var saved bool
			for _, file := range item.Media {
				saved = saved || file.Path == "P01.audio-30251.m4a" && library.VerifyMediaFile(item.ItemDir, file, true)
			}
			if !saved {
				t.Fatal("completed highest audio was not retained")
			}
		})
	}
}

func TestUpgradeValidationFailureRetainsOriginalMP4(t *testing.T) {
	var base string
	mgr, lib, server := setupDownloadEnv(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/x/web-interface/wbi/view":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"bvid": "BV1xx411c7mD", "title": "synthetic", "owner": map[string]any{"mid": 1}, "pages": []map[string]any{{"cid": 1, "page": 1, "duration": 1}}}})
		case "/x/player/wbi/playurl":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"dash": map[string]any{"video": []map[string]any{{"id": 112, "codecid": 7, "codecs": "avc1", "baseUrl": base + "/invalid"}}}}})
		case "/invalid":
			_, _ = w.Write([]byte("synthetic invalid mp4"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}), 1)
	base = server.URL
	cfg := mgr.dlSettings
	cfg.SaveCover, cfg.SaveDanmaku, cfg.SaveSubtitles = false, false, false
	mgr.UpdateSettings(cfg)
	dir := filepath.Join(lib.CacheDir(), "video")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	video, err := BuildSyntheticVideoM4S(30, 30)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "P01.mp4")
	if _, err := DefragmentFMP4Bytes(video, path); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = lib.SaveItem(dir, library.ItemMetadata{Site: "bilibili", Kind: "video", ID: "BV1xx411c7mD", Parts: []library.VideoPartRecord{{CID: 1, Page: 1, File: "P01.mp4", Video: library.PartVideoInfo{Quality: 80}}}, Media: []library.MediaFile{{Role: "video", Path: "P01.mp4", Bytes: int64(len(original))}}})
	if err != nil {
		t.Fatal(err)
	}
	task := Task{Kind: "upgrade", TargetID: "BV1xx411c7mD"}
	if err := mgr.executeUpgradeTask(context.Background(), &task); err == nil {
		t.Fatal("invalid upgraded output was accepted")
	}
	if after, err := os.ReadFile(path); err != nil || string(after) != string(original) {
		t.Fatal("failed upgrade changed the old MP4")
	}
	item, _, err := lib.GetItem(task.TargetID)
	if err != nil || item.Parts[0].Video.Quality != 80 {
		t.Fatalf("failed upgrade changed old quality: %+v %v", item, err)
	}
}

func TestTaskListKeepsActiveAndRecentlyChangedTasksFirst(t *testing.T) {
	mgr, _, _ := setupDownloadEnv(t, http.NotFoundHandler(), 1)
	for i, task := range []struct{ status, updated string }{
		{"completed", "2026-11-08T12:00:00Z"},
		{"paused", "2026-11-08T08:00:00Z"},
		{"running", "2026-11-08T09:00:00Z"},
		{"failed", "2026-11-08T11:00:00Z"},
		{"queued", "2026-11-08T10:00:00Z"},
	} {
		created, err := mgr.enqueueSingleTask(Task{Kind: "video", TargetID: "fixture-" + strconv.Itoa(i)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := mgr.db.Exec("UPDATE download_tasks SET status=?, updated_at=? WHERE task_id=?", task.status, task.updated, created.TaskID); err != nil {
			t.Fatal(err)
		}
	}
	tasks, err := mgr.ListTasks()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"queued", "running", "failed", "paused", "completed"}
	if len(tasks) != len(want) {
		t.Fatalf("listed %d tasks, want %d", len(tasks), len(want))
	}
	for i, task := range tasks {
		if task.Status != want[i] {
			t.Fatalf("task %d status = %q, want %q", i, task.Status, want[i])
		}
	}
}

func TestMigrationWaitsForPausedWorkerToExit(t *testing.T) {
	mgr, _, _ := setupDownloadEnv(t, http.NotFoundHandler(), 1)
	task, err := mgr.enqueueSingleTask(Task{Kind: "video", TargetID: "fixture-migration"})
	if err != nil {
		t.Fatal(err)
	}
	ran := false
	migrate := func() error { ran = true; return nil }
	if err := mgr.WithIdleTasks(migrate); err == nil || ran {
		t.Fatal("migration started with queued work")
	}
	if _, err := mgr.Pause(task.TaskID); err != nil {
		t.Fatal(err)
	}
	// Pause has persisted, but the canceled worker still owns its slot until
	// its final disk write and defer have returned.
	mgr.mu.Lock()
	mgr.activeRun[task.TaskID] = func() {}
	mgr.mu.Unlock()
	if err := mgr.WithIdleTasks(migrate); err == nil || ran {
		t.Fatal("migration overlapped a paused worker that had not exited")
	}
	mgr.mu.Lock()
	delete(mgr.activeRun, task.TaskID)
	mgr.mu.Unlock()
	if err := mgr.WithIdleTasks(migrate); err != nil || !ran {
		t.Fatalf("idle migration did not run: %v", err)
	}
}

// A third-party node outside the media allowlist (seen once from playurl as
// the primary URL) is skipped without a failed attempt; an allowlist-only
// failure is reported as unavailable so playurl is refreshed once.
func TestUnsupportedMediaNodeIsSkippedNotCountedAsFailure(t *testing.T) {
	payload := []byte(strings.Repeat("trace-node", 100))
	manager, _, srv := setupDownloadEnv(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}), 1)
	dir := t.TempDir()
	task := Task{TaskID: "node"}
	n, _, err := manager.downloadFileWithRange(context.Background(), []string{
		"https://upos-lcdn-example.solseed.cn/media/x.m4s",
		srv.URL + "/media/x.m4s",
	}, filepath.Join(dir, "x.m4s"), &task)
	if err != nil || n != int64(len(payload)) {
		t.Fatalf("download with skipped node: n=%d err=%v", n, err)
	}
	if len(task.Attempts) != 0 {
		t.Fatalf("skipped node was recorded as a failed attempt: %+v", task.Attempts)
	}
	_, _, err = manager.downloadFileWithRange(context.Background(), []string{"https://upos-lcdn-example.solseed.cn/media/y.m4s"}, filepath.Join(dir, "y.m4s"), &Task{TaskID: "node2"})
	if !bilibili.IsKind(err, bilibili.ErrKindUnavailable) {
		t.Fatalf("only unsupported nodes: want unavailable, got %v", err)
	}
}

// H-001 feedback: canceling a completed task rewrote it to "canceled", and
// finished tasks could not be removed. Removal clears only the record.
func TestFinishedTasksCanBeRemovedButNotCanceled(t *testing.T) {
	mgr, _, _ := setupDownloadEnv(t, http.NotFoundHandler(), 1)
	dir := t.TempDir()
	kept := filepath.Join(dir, "P01.mp4")
	if err := os.WriteFile(kept, []byte("cached"), 0600); err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, status := range []string{"completed", "failed", "queued"} {
		task, err := mgr.enqueueSingleTask(Task{Kind: "video", TargetID: "BV1" + status, ItemDir: dir})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := mgr.db.Exec("UPDATE download_tasks SET status = ? WHERE task_id = ?", status, task.TaskID); err != nil {
			t.Fatal(err)
		}
		ids[status] = task.TaskID
	}
	if _, err := mgr.Cancel(ids["completed"]); err == nil {
		t.Fatal("canceling a completed task must be refused")
	}
	if got, _, _ := mgr.GetTask(ids["completed"]); got.Status != "completed" {
		t.Fatalf("cancel rewrote a completed task to %q", got.Status)
	}
	if err := mgr.Remove(ids["queued"]); err == nil {
		t.Fatal("an unfinished task must not be removable")
	}
	if err := mgr.Remove(ids["failed"]); err != nil {
		t.Fatal(err)
	}
	if n, err := mgr.RemoveFinished(); err != nil || n != 1 {
		t.Fatalf("RemoveFinished = %d, %v", n, err)
	}
	tasks, _ := mgr.ListTasks()
	if len(tasks) != 1 || tasks[0].TaskID != ids["queued"] {
		t.Fatalf("remaining tasks = %+v", tasks)
	}
	if raw, err := os.ReadFile(kept); err != nil || string(raw) != "cached" {
		t.Fatalf("removing task records touched cached files: %q %v", raw, err)
	}
}

// DEC-040: a live photo (实况) keeps its motion clip next to the still; a
// second run reuses both files.
func TestDynamicLivePhotoKeepsItsMotionClip(t *testing.T) {
	var base string
	clipHits := 0
	mgr, lib, server := setupDownloadEnv(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/x/polymer/web-dynamic/v1/detail":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"item": map[string]any{
				"id_str": "778899", "type": "DYNAMIC_TYPE_DRAW", "modules": map[string]any{
					"module_author": map[string]any{"mid": 1, "name": "synthetic", "pub_ts": 1},
					"module_dynamic": map[string]any{"major": map[string]any{"opus": map[string]any{"pics": []map[string]any{
						{"url": base + "/still.png", "width": 1080, "height": 1440, "live_url": base + "/clip.mp4"},
						{"url": base + "/plain.jpg"},
					}}}},
				},
			}}})
		case "/still.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("synthetic still"))
		case "/plain.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("synthetic plain"))
		case "/clip.mp4":
			clipHits++
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write([]byte("synthetic motion clip"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}), 1)
	base = server.URL
	for run := 0; run < 2; run++ {
		task := Task{Kind: "dynamic", TargetID: "778899"}
		if err := mgr.executeDynamicTask(context.Background(), &task); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
	}
	item, ok, err := lib.GetItem("dynamic:778899")
	if err != nil || !ok || item.Status != "complete" {
		t.Fatalf("live photo dynamic not complete: %+v %v", item, err)
	}
	var still, plain, clip *library.MediaFile
	for i := range item.Media {
		switch f := &item.Media[i]; {
		case f.Role == "image" && f.Source == base+"/still.png":
			still = f
		case f.Role == "image":
			plain = f
		case f.Role == "motion":
			clip = f
		}
	}
	if still == nil || plain == nil || clip == nil || still.Motion != clip.Path || plain.Motion != "" || clip.Source != base+"/clip.mp4" {
		t.Fatalf("live photo files: %+v", item.Media)
	}
	if !library.VerifyMediaFile(item.ItemDir, *clip, true) {
		t.Fatal("motion clip not saved with its hash")
	}
	if clipHits != 1 {
		t.Fatalf("motion clip fetched %d times, want once", clipHits)
	}
}
