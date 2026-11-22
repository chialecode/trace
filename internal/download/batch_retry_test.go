package download

import (
	"net/http"
	"reflect"
	"sync"
	"testing"
)

func TestIsRetryableTaskMatrix(t *testing.T) {
	cases := []struct {
		name string
		task Task
		want bool
	}{
		{name: "failed", task: Task{Status: "failed"}, want: true},
		{name: "partial", task: Task{Status: "partial"}, want: true},
		{name: "paused_network", task: Task{Status: "paused", PauseReason: "network"}, want: true},
		{name: "paused_app_closed", task: Task{Status: "paused", PauseReason: "app_closed"}, want: true},
		{name: "paused_risk_control", task: Task{Status: "paused", PauseReason: "risk_control"}, want: true},
		{name: "paused_auth_required", task: Task{Status: "paused", PauseReason: "auth_required"}, want: true},
		{name: "paused_quota", task: Task{Status: "paused", PauseReason: "quota"}, want: true},
		{name: "paused_backoff", task: Task{Status: "paused", PauseReason: "backoff"}, want: true},
		{name: "paused_empty_reason", task: Task{Status: "paused", PauseReason: ""}, want: true},
		{name: "paused_prefix_non_user", task: Task{Status: "paused_network", PauseReason: ""}, want: true},
		{name: "paused_user", task: Task{Status: "paused", PauseReason: "user"}, want: false},
		{name: "paused_user_trimmed", task: Task{Status: "paused", PauseReason: " user "}, want: false},
		{name: "paused_prefix_user", task: Task{Status: "paused_manual", PauseReason: "user"}, want: false},
		{name: "queued", task: Task{Status: "queued"}, want: false},
		{name: "running", task: Task{Status: "running"}, want: false},
		{name: "completed", task: Task{Status: "completed"}, want: false},
		{name: "canceled", task: Task{Status: "canceled"}, want: false},
	}
	for _, tc := range cases {
		if got := IsRetryableTask(tc.task); got != tc.want {
			t.Errorf("%s: IsRetryableTask(%+v) = %v, want %v", tc.name, tc.task, got, tc.want)
		}
	}
}

func TestManagerRetryActionableMixedStatusesAuthAndDedup(t *testing.T) {
	mgr, _, _ := setupDownloadEnv(t, http.NewServeMux(), 1)

	var mu sync.Mutex
	var emitted []string
	mgr.onTaskUpdate = func(tk Task) {
		mu.Lock()
		emitted = append(emitted, tk.TaskID+":"+tk.Status)
		mu.Unlock()
	}

	insert := func(id, siteID, kind, targetID string, cid int64, status, pauseReason, errKind, errMsg, updatedAt string) {
		t.Helper()
		_, err := mgr.db.Exec(`
			INSERT INTO download_tasks (
				task_id, site, kind, source, target_id, cid, page, title, part_title,
				mid, author_uid, author_name, author_face, cover_url, cache_linked_videos,
				status, pause_reason, error_kind, error_message, quality_hint,
				bytes_downloaded, total_bytes, progress, item_key, item_dir,
				files_json, attempts_json, created_at, updated_at, via_json
			) VALUES (?, ?, ?, 'manual', ?, ?, 1, ?, '',
				0, '', '', '', '', 0,
				?, ?, ?, ?, '',
				0, 0, 0, '', '',
				'[]', '[]', ?, ?, '')
		`, id, siteID, kind, targetID, cid, id, status, pauseReason, errKind, errMsg, updatedAt, updatedAt)
		if err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}

	// Eligible Bilibili tasks (allowed).
	insert("dl-bili-failed", "bilibili", "video", "BV1A", 101, "failed", "", "network", "boom", "2026-11-21T10:00:00Z")
	insert("dl-bili-partial", "bilibili", "dynamic", "dyn1", 0, "partial", "", "network", "missing img", "2026-11-21T10:01:00Z")
	insert("dl-bili-paused-net", "bilibili", "video", "BV1B", 102, "paused", "network", "network", "已暂停（网络）：分段重试已达上限", "2026-11-21T10:02:00Z")

	// Duplicate failed pair for same (site, kind, target_id, cid): newer should win, older skipped.
	insert("dl-bili-dup-old", "bilibili", "video", "BV1DUP", 201, "failed", "", "network", "old", "2026-11-21T09:00:00Z")
	insert("dl-bili-dup-new", "bilibili", "video", "BV1DUP", 201, "failed", "", "network", "new", "2026-11-21T11:00:00Z")

	// Failed task whose (site, kind, target_id, cid) is already queued: must be skipped as duplicate.
	insert("dl-bili-active", "bilibili", "video", "BV1ACT", 301, "queued", "", "", "", "2026-11-21T10:05:00Z")
	insert("dl-bili-dup-active", "bilibili", "video", "BV1ACT", 301, "failed", "", "network", "stale", "2026-11-21T10:04:00Z")

	// Non-retryable tasks: must never be touched.
	insert("dl-bili-paused-user", "bilibili", "video", "BV1USR", 401, "paused", "user", "", "已暂停", "2026-11-21T10:06:00Z")
	insert("dl-bili-completed", "bilibili", "video", "BV1OK", 402, "completed", "", "", "", "2026-11-21T10:07:00Z")
	insert("dl-bili-canceled", "bilibili", "video", "BV1CAN", 403, "canceled", "", "", "", "2026-11-21T10:08:00Z")

	// Logged-out sites (douyin, weibo): must be skipped with MissingAuthSites sorted deterministically.
	insert("dl-dy-paused-net", "douyin", "video", "739901", 0, "paused", "network", "network", "已暂停（网络）：分段重试已达上限", "2026-11-21T10:09:00Z")
	insert("dl-wb-failed", "weibo", "dynamic", "500001", 0, "failed", "", "network", "err", "2026-11-21T10:10:00Z")

	// Seed a stale initial plan on a retried task to confirm RetryActionable clears it.
	mgr.mu.Lock()
	mgr.initialPlans["dl-bili-failed"] = WorkDownloadPlan{TargetID: "BV1A"}
	mgr.mu.Unlock()

	res, err := mgr.RetryActionable(map[string]bool{
		"bilibili": true,
		"douyin":   false,
		"weibo":    false,
	})
	if err != nil {
		t.Fatalf("RetryActionable: %v", err)
	}
	if res.Retried != 4 {
		t.Fatalf("res.Retried = %d, want 4 (%+v)", res.Retried, res)
	}
	if res.SkippedAuth != 2 {
		t.Fatalf("res.SkippedAuth = %d, want 2 (%+v)", res.SkippedAuth, res)
	}
	if res.SkippedDuplicate != 2 {
		t.Fatalf("res.SkippedDuplicate = %d, want 2 (%+v)", res.SkippedDuplicate, res)
	}
	if wantSites := []string{"douyin", "weibo"}; !reflect.DeepEqual(res.MissingAuthSites, wantSites) {
		t.Fatalf("res.MissingAuthSites = %v, want %v", res.MissingAuthSites, wantSites)
	}

	assertStatus := func(id, wantStatus, wantPause, wantErrKind, wantErrMsg string) {
		t.Helper()
		tk, ok, err := mgr.GetTask(id)
		if err != nil || !ok {
			t.Fatalf("GetTask(%s): ok=%v err=%v", id, ok, err)
		}
		if tk.Status != wantStatus || tk.PauseReason != wantPause || tk.ErrorKind != wantErrKind || tk.ErrorMessage != wantErrMsg {
			t.Fatalf("task %s = (status=%q pause=%q kind=%q msg=%q), want (%q %q %q %q)",
				id, tk.Status, tk.PauseReason, tk.ErrorKind, tk.ErrorMessage,
				wantStatus, wantPause, wantErrKind, wantErrMsg)
		}
	}

	// Retried tasks are queued with cleared pause/error fields.
	assertStatus("dl-bili-failed", "queued", "", "", "")
	assertStatus("dl-bili-partial", "queued", "", "", "")
	assertStatus("dl-bili-paused-net", "queued", "", "", "")
	assertStatus("dl-bili-dup-new", "queued", "", "", "")

	// Deduplicated older/shadowed failed tasks remain failed.
	assertStatus("dl-bili-dup-old", "failed", "", "network", "old")
	assertStatus("dl-bili-dup-active", "failed", "", "network", "stale")

	// Non-retryable and unauthorized tasks remain untouched.
	assertStatus("dl-bili-paused-user", "paused", "user", "", "已暂停")
	assertStatus("dl-bili-completed", "completed", "", "", "")
	assertStatus("dl-bili-canceled", "canceled", "", "", "")
	assertStatus("dl-dy-paused-net", "paused", "network", "network", "已暂停（网络）：分段重试已达上限")
	assertStatus("dl-wb-failed", "failed", "", "network", "err")

	mgr.mu.Lock()
	_, stillHasPlan := mgr.initialPlans["dl-bili-failed"]
	mgr.mu.Unlock()
	if stillHasPlan {
		t.Fatal("RetryActionable did not clear stale initialPlans entry")
	}

	mu.Lock()
	gotEmitted := len(emitted)
	mu.Unlock()
	if gotEmitted != 4 {
		t.Fatalf("expected 4 emitted task updates, got %d (%v)", gotEmitted, emitted)
	}
}
