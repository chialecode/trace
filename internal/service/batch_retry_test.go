package service

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/site"
)

func TestBackendRetryAllTasksAuthAndEvents(t *testing.T) {
	h := newDualSiteTestHarness(t)
	b := h.backend
	ctx := context.Background()

	// 1. Logged out completely -> auth_required error.
	if _, err := b.RetryAllTasks(); !site.IsKind(err, site.ErrKindAuthRequired) {
		t.Fatalf("logged-out RetryAllTasks err = %v, want auth_required", err)
	}

	// Log in Bilibili only (Douyin & Weibo remain logged out).
	if st, err := b.LoginWithCookie(ctx, "bilibili", "SESSDATA=SYNTH_BILI_SESS_BR; DedeUserID=778899; bili_jct=SYNTH_JCT_BR", ""); err != nil || !st.LoggedIn {
		t.Fatalf("LoginWithCookie(bilibili): st=%+v err=%v", st, err)
	}

	db, err := sql.Open("sqlite", filepath.Join(h.loc.DataDir, "library.db"))
	if err != nil {
		t.Fatalf("open library.db: %v", err)
	}
	defer db.Close()

	now := time.Now().UTC().Format(time.RFC3339)
	insertTask := func(id, siteID, kind, targetID, status, pauseReason, updatedAt string) {
		t.Helper()
		_, err := db.Exec(`
			INSERT INTO download_tasks (
				task_id, site, kind, source, target_id, cid, page, title, part_title,
				mid, author_uid, author_name, author_face, cover_url, cache_linked_videos,
				status, pause_reason, error_kind, error_message, quality_hint,
				bytes_downloaded, total_bytes, progress, item_key, item_dir,
				files_json, attempts_json, created_at, updated_at, via_json
			) VALUES (?, ?, ?, 'queue', ?, 0, 1, ?, '',
				0, '', '', '', '', 0,
				?, ?, 'network', 'err', '',
				0, 0, 0, '', '',
				'[]', '[]', ?, ?, '')
		`, id, siteID, kind, targetID, id, status, pauseReason, updatedAt, updatedAt)
		if err != nil {
			t.Fatalf("insertTask %s: %v", id, err)
		}
	}

	insertTask("dl-srv-bili-1", "bilibili", "video", "BV1S1", "failed", "", "2026-11-21T10:00:00Z")
	insertTask("dl-srv-bili-1-dup", "bilibili", "video", "BV1S1", "failed", "", "2026-11-21T09:00:00Z")
	insertTask("dl-srv-bili-2", "bilibili", "video", "BV1S2", "paused", "network", "2026-11-21T10:01:00Z")
	insertTask("dl-srv-bili-user", "bilibili", "video", "BV1S3", "paused", "user", "2026-11-21T10:02:00Z")
	insertTask("dl-srv-bili-can", "bilibili", "video", "BV1S4", "canceled", "", "2026-11-21T10:03:00Z")
	insertTask("dl-srv-dy-1", "douyin", "video", "739911", "paused", "network", "2026-11-21T10:04:00Z")
	insertTask("dl-srv-wb-1", "weibo", "dynamic", "500011", "partial", "", now)

	var retryEvents atomic.Int32
	unsub := b.SubscribeEvents(func(ev Event) {
		if ev.Type == EventTaskUpdated && ev.Message == "retried:all" {
			retryEvents.Add(1)
		}
	})
	defer unsub()

	// 2. Partial auth: Bilibili tasks retried, Douyin & Weibo skipped with no top-level error.
	res, err := b.RetryAllTasks()
	if err != nil {
		t.Fatalf("partial-auth RetryAllTasks err = %v", err)
	}
	if res.Retried != 2 || res.SkippedAuth != 2 || res.SkippedDuplicate != 1 {
		t.Fatalf("partial-auth res = %+v, want Retried=2 SkippedAuth=2 SkippedDuplicate=1", res)
	}
	if want := []string{"douyin", "weibo"}; !reflect.DeepEqual(res.MissingAuthSites, want) {
		t.Fatalf("MissingAuthSites = %v, want %v", res.MissingAuthSites, want)
	}
	if retryEvents.Load() != 1 {
		t.Fatalf("expected 1 EventTaskUpdated(retried:all), got %d", retryEvents.Load())
	}

	// Verify user-paused & canceled remained untouched.
	if d, ok, _ := b.TaskDetail("dl-srv-bili-user"); !ok || d.Task.Status != "paused" || d.Task.PauseReason != "user" {
		t.Fatalf("user-paused task unexpectedly changed: %+v", d.Task)
	}
	if d, ok, _ := b.TaskDetail("dl-srv-bili-can"); !ok || d.Task.Status != "canceled" {
		t.Fatalf("canceled task unexpectedly changed: %+v", d.Task)
	}

	// 3. Re-running now: Bilibili dup is shadowed by active dl-srv-bili-1; only Douyin + Weibo need auth -> 0 retried + multi-site auth error.
	res2, err := b.RetryAllTasks()
	if !site.IsKind(err, site.ErrKindAuthRequired) {
		t.Fatalf("multi-site 0-retried err = %v, want auth_required", err)
	}
	if res2.Retried != 0 || res2.SkippedAuth != 2 || res2.SkippedDuplicate != 1 {
		t.Fatalf("multi-site 0-retried res = %+v, want Retried=0 SkippedAuth=2 SkippedDuplicate=1", res2)
	}
	if !strings.Contains(err.Error(), "抖音") || !strings.Contains(err.Error(), "微博") {
		t.Fatalf("multi-site auth error %q should mention 抖音 and 微博", err.Error())
	}

	// 4. Remove Weibo task so only Douyin needs auth -> single-site auth error carries Site == Douyin.
	if _, err := db.Exec(`DELETE FROM download_tasks WHERE task_id = 'dl-srv-wb-1'`); err != nil {
		t.Fatal(err)
	}
	res3, err := b.RetryAllTasks()
	if !site.IsKind(err, site.ErrKindAuthRequired) || site.SiteOf(err) != site.Douyin {
		t.Fatalf("single-site 0-retried err = %v (site=%q), want douyin auth_required", err, site.SiteOf(err))
	}
	if res3.Retried != 0 || res3.SkippedAuth != 1 || !reflect.DeepEqual(res3.MissingAuthSites, []string{"douyin"}) {
		t.Fatalf("single-site 0-retried res = %+v", res3)
	}
}
