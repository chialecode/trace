package download

import (
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
)

func TestDownloadTaskIndexSummaryNonTerminalAndBatchCoalescing(t *testing.T) {
	mgr, libStore, _ := setupDownloadEnv(t, http.NewServeMux(), 1)

	// 1. Verify idx_download_tasks_status_updated exists on download_tasks.
	var idxName string
	if err := mgr.db.QueryRow(`
		SELECT name FROM sqlite_master
		WHERE type = 'index' AND tbl_name = 'download_tasks' AND name = 'idx_download_tasks_status_updated'
	`).Scan(&idxName); err != nil || idxName != "idx_download_tasks_status_updated" {
		t.Fatalf("expected index idx_download_tasks_status_updated, got name=%q err=%v", idxName, err)
	}

	// Seed a partial library item so fillPartialReason backfills via GetItemMetaOnly.
	partialDir := filepath.Join(libStore.CacheDir(), "x", "9001", "posts", "1800000000000000900")
	if err := os.MkdirAll(partialDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(partialDir, "01.jpg"), []byte("img"), 0o600); err != nil {
		t.Fatal(err)
	}
	partialMeta, err := libStore.SaveItem(partialDir, library.ItemMetadata{
		Version: 1,
		Site:    "x",
		Kind:    "dynamic",
		ID:      "1800000000000000900",
		Title:   "Partial X Post",
		Status:  "partial",
		PubTs:   1700000500,
		Author:  library.ItemAuthor{Site: "x", UID: "9001", Name: "XAuthor"},
		Media:   []library.MediaFile{{Role: "image", Path: "01.jpg"}},
		Missing: []library.MissingRecord{{Item: "images/02.mp4", Reason: XHLSOnlyMissingReason}},
	})
	if err != nil {
		t.Fatalf("SaveItem partial: %v", err)
	}

	filesBlob := `[{"role":"image","path":"01.jpg","status":"completed","downloadedBytes":10,"totalBytes":10,"owned":true}]`
	attemptsBlob := `[{"time":"2026-11-22T10:00:00Z","file":"01.jpg","offset":0,"host":"pbs.twimg.com","attempt":1,"httpStatus":200}]`

	insert := func(id, siteID, kind, targetID, status, pauseReason, errMsg, itemKey, updatedAt string) {
		t.Helper()
		_, err := mgr.db.Exec(`
			INSERT INTO download_tasks (
				task_id, site, kind, source, target_id, cid, page, title, part_title,
				mid, author_uid, author_name, author_face, cover_url, cache_linked_videos,
				status, pause_reason, error_kind, error_message, quality_hint,
				bytes_downloaded, total_bytes, progress, item_key, item_dir,
				files_json, attempts_json, created_at, updated_at, via_json
			) VALUES (?, ?, ?, 'manual', ?, 0, 1, ?, '',
				0, '9001', 'Author', '', '', 0,
				?, ?, '', ?, '',
				10, 20, 0.5, ?, '',
				?, ?, ?, ?, '')
		`, id, siteID, kind, targetID, id, status, pauseReason, errMsg, itemKey, filesBlob, attemptsBlob, updatedAt, updatedAt)
		if err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}

	insert("dl-run", "bilibili", "video", "BV1run", "running", "", "", "", "2026-11-22T10:05:00Z")
	insert("dl-queue-x", "x", "dynamic", "1801", "queued", "", "", "", "2026-11-22T10:04:00Z")
	insert("dl-queue-b", "bilibili", "video", "BV1q", "queued", "", "", "", "2026-11-22T10:03:00Z")
	insert("dl-partial", "x", "dynamic", "1800000000000000900", "partial", "", "", partialMeta.ItemKey, "2026-11-22T10:02:00Z")
	insert("dl-failed-1", "x", "dynamic", "1802", "failed", "", "net err", "", "2026-11-22T10:01:00Z")
	insert("dl-paused-net", "x", "dynamic", "1803", "paused", "network", "paused net", "", "2026-11-22T10:00:00Z")
	insert("dl-completed", "x", "dynamic", "1804", "completed", "", "", "", "2026-11-22T10:06:00Z")
	insert("dl-canceled", "bilibili", "video", "BV1can", "canceled", "", "", "", "2026-11-22T10:07:00Z")

	// 2. Compare ListTasksSummary vs ListTasks: exact same IDs & order, backfilled partial reason,
	// but Files and Attempts omitted in summary path while intact in ListTasks / TaskDetail.
	fullList, err := mgr.ListTasks()
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	summaryList, err := mgr.ListTasksSummary()
	if err != nil {
		t.Fatalf("ListTasksSummary: %v", err)
	}
	if len(summaryList) != len(fullList) || len(summaryList) != 8 {
		t.Fatalf("ListTasksSummary len=%d, ListTasks len=%d, want 8", len(summaryList), len(fullList))
	}
	var gotSummaryIDs, gotFullIDs []string
	var sawPartialSummary Task
	for i := range summaryList {
		gotSummaryIDs = append(gotSummaryIDs, summaryList[i].TaskID)
		gotFullIDs = append(gotFullIDs, fullList[i].TaskID)
		if len(summaryList[i].Files) != 0 || len(summaryList[i].Attempts) != 0 {
			t.Fatalf("ListTasksSummary(%s) should omit Files/Attempts, got files=%d attempts=%d",
				summaryList[i].TaskID, len(summaryList[i].Files), len(summaryList[i].Attempts))
		}
		if len(fullList[i].Files) != 1 || len(fullList[i].Attempts) != 1 {
			t.Fatalf("ListTasks(%s) lost Files/Attempts: files=%d attempts=%d",
				fullList[i].TaskID, len(fullList[i].Files), len(fullList[i].Attempts))
		}
		if summaryList[i].TaskID == "dl-partial" {
			sawPartialSummary = summaryList[i]
		}
	}
	if !reflect.DeepEqual(gotSummaryIDs, gotFullIDs) {
		t.Fatalf("ListTasksSummary order = %v, want %v", gotSummaryIDs, gotFullIDs)
	}
	if sawPartialSummary.ErrorMessage == "" {
		t.Fatalf("ListTasksSummary did not backfill legacy empty partial ErrorMessage")
	}

	detail, ok, err := mgr.TaskDetail("dl-partial")
	if err != nil || !ok || len(detail.Files) != 1 || len(detail.Attempts) != 1 {
		t.Fatalf("TaskDetail(dl-partial) = ok=%v files=%d attempts=%d err=%v", ok, len(detail.Files), len(detail.Attempts), err)
	}

	// 3. ListNonTerminalTasks excludes completed and canceled rows, omits Files/Attempts, preserves order.
	nonTerm, err := mgr.ListNonTerminalTasks()
	if err != nil {
		t.Fatalf("ListNonTerminalTasks: %v", err)
	}
	wantNonTerm := []string{"dl-run", "dl-queue-x", "dl-queue-b", "dl-partial", "dl-failed-1", "dl-paused-net"}
	var gotNonTerm []string
	for _, tk := range nonTerm {
		gotNonTerm = append(gotNonTerm, tk.TaskID)
		if len(tk.Files) != 0 || len(tk.Attempts) != 0 {
			t.Fatalf("ListNonTerminalTasks(%s) should omit Files/Attempts", tk.TaskID)
		}
	}
	if !reflect.DeepEqual(gotNonTerm, wantNonTerm) {
		t.Fatalf("ListNonTerminalTasks = %v, want %v", gotNonTerm, wantNonTerm)
	}

	// 4. Coalesced batch notifications via SetOnTaskBatchUpdate:
	// RetryActionable, PauseForSite, and PauseAll each emit 1 batch callback instead of M single callbacks.
	type batchEvent struct {
		action string
		ids    []string
	}
	var (
		mu          sync.Mutex
		singleCalls int
		batchEvents []batchEvent
	)
	mgr.onTaskUpdate = func(Task) {
		mu.Lock()
		singleCalls++
		mu.Unlock()
	}
	mgr.SetOnTaskBatchUpdate(func(action string, tasks []Task) {
		ids := make([]string, 0, len(tasks))
		for _, tk := range tasks {
			ids = append(ids, tk.TaskID)
		}
		mu.Lock()
		batchEvents = append(batchEvents, batchEvent{action: action, ids: ids})
		mu.Unlock()
	})

	retryRes, err := mgr.RetryActionable(map[string]bool{"x": true, "bilibili": true})
	if err != nil {
		t.Fatalf("RetryActionable: %v", err)
	}
	if retryRes.Retried != 3 {
		t.Fatalf("RetryActionable Retried=%d, want 3", retryRes.Retried)
	}

	mgr.PauseForSite(site.X, "rate_limited", "X 限流")
	mgr.PauseAll("user", "全部暂停")

	mu.Lock()
	defer mu.Unlock()
	if singleCalls != 0 {
		t.Fatalf("expected 0 per-task onTaskUpdate calls when batch handler is set, got %d", singleCalls)
	}
	if len(batchEvents) != 3 {
		t.Fatalf("expected 3 coalesced batch callbacks, got %d: %+v", len(batchEvents), batchEvents)
	}
	if batchEvents[0].action != "retry_batch" || len(batchEvents[0].ids) != 3 {
		t.Fatalf("event[0] = %+v, want retry_batch with 3 tasks", batchEvents[0])
	}
	if batchEvents[1].action != "pause_site" || len(batchEvents[1].ids) != 4 {
		t.Fatalf("event[1] = %+v, want pause_site with 4 X tasks", batchEvents[1])
	}
	if batchEvents[2].action != "pause_all" || len(batchEvents[2].ids) != 2 {
		t.Fatalf("event[2] = %+v, want pause_all with 2 remaining bilibili tasks", batchEvents[2])
	}
}
