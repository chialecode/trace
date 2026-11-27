package download

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/storage"
)

const taskSelectColumns = `
	task_id, site, kind, source, target_id, cid, page, title, part_title,
	mid, author_uid, author_name, author_face, cover_url, cache_linked_videos,
	status, pause_reason, error_kind, error_message, quality_hint,
	bytes_downloaded, total_bytes, progress, item_key, item_dir,
	files_json, attempts_json, created_at, updated_at, via_json`

const taskSummarySelectColumns = `
	task_id, site, kind, source, target_id, cid, page, title, part_title,
	mid, author_uid, author_name, author_face, cover_url, cache_linked_videos,
	status, pause_reason, error_kind, error_message, quality_hint,
	bytes_downloaded, total_bytes, progress, item_key, item_dir,
	created_at, updated_at, via_json`

const taskListOrderSQL = `
	ORDER BY CASE WHEN status IN ('running', 'queued') THEN 0 WHEN status IN ('paused', 'failed', 'partial') THEN 1 ELSE 2 END,
		julianday(updated_at) DESC, rowid DESC`

func (m *Manager) initSchema() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS download_tasks (
			task_id TEXT PRIMARY KEY,
			site TEXT NOT NULL,
			kind TEXT NOT NULL,
			source TEXT NOT NULL DEFAULT 'manual',
			target_id TEXT NOT NULL,
			cid INTEGER NOT NULL DEFAULT 0,
			page INTEGER NOT NULL DEFAULT 1,
			title TEXT NOT NULL DEFAULT '',
			part_title TEXT NOT NULL DEFAULT '',
			mid INTEGER NOT NULL DEFAULT 0,
			author_uid TEXT NOT NULL DEFAULT '',
			author_name TEXT NOT NULL DEFAULT '',
			author_face TEXT NOT NULL DEFAULT '',
			cover_url TEXT NOT NULL DEFAULT '',
			cache_linked_videos INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'queued',
			pause_reason TEXT NOT NULL DEFAULT '',
			error_kind TEXT NOT NULL DEFAULT '',
			error_message TEXT NOT NULL DEFAULT '',
			quality_hint TEXT NOT NULL DEFAULT '',
			bytes_downloaded INTEGER NOT NULL DEFAULT 0,
			total_bytes INTEGER NOT NULL DEFAULT 0,
			progress REAL NOT NULL DEFAULT 0,
			item_key TEXT NOT NULL DEFAULT '',
			item_dir TEXT NOT NULL DEFAULT '',
			files_json TEXT NOT NULL DEFAULT '[]',
			attempts_json TEXT NOT NULL DEFAULT '[]',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS task_attempts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			task_id TEXT NOT NULL,
			attempt_time TEXT NOT NULL,
			file_path TEXT NOT NULL,
			offset_bytes INTEGER NOT NULL DEFAULT 0,
			host TEXT NOT NULL DEFAULT '',
			attempt_num INTEGER NOT NULL DEFAULT 1,
			error_kind TEXT NOT NULL DEFAULT '',
			http_status INTEGER NOT NULL DEFAULT 0
		);`,
	}
	for _, q := range stmts {
		if _, err := m.db.Exec(q); err != nil {
			return err
		}
	}
	// Ensure columns exist on databases created before schema additions.
	for _, alter := range []string{
		`ALTER TABLE download_tasks ADD COLUMN source TEXT NOT NULL DEFAULT 'manual'`,
		`ALTER TABLE download_tasks ADD COLUMN author_uid TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE download_tasks ADD COLUMN pause_reason TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE download_tasks ADD COLUMN files_json TEXT NOT NULL DEFAULT '[]'`,
		`ALTER TABLE download_tasks ADD COLUMN attempts_json TEXT NOT NULL DEFAULT '[]'`,
		`ALTER TABLE download_tasks ADD COLUMN via_json TEXT NOT NULL DEFAULT ''`,
	} {
		_, _ = m.db.Exec(alter)
	}
	if _, err := m.db.Exec(`CREATE INDEX IF NOT EXISTS idx_download_tasks_status_updated ON download_tasks(status, updated_at)`); err != nil {
		return err
	}
	return nil
}

func (m *Manager) enqueueSingleTask(in Task) (Task, error) {
	return m.enqueueSingleTaskWithPlan(in, nil)
}

func (m *Manager) enqueueSingleTaskWithPlan(in Task, initialPlan *WorkDownloadPlan) (Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	in.Site = strings.ToLower(strings.TrimSpace(in.Site))
	if in.Site == "" {
		in.Site = string(site.Bilibili)
	} else if !site.ID(in.Site).Valid() {
		return Task{}, &site.Error{
			Site:    site.ID(in.Site),
			Kind:    site.ErrKindBadRequest,
			Message: "不支持的下载站点: " + in.Site,
		}
	}
	if in.AuthorUID == "" && in.Mid > 0 && in.Site == string(site.Bilibili) {
		in.AuthorUID = strconv.FormatInt(in.Mid, 10)
	}

	// Deduplicate if an identical task for the same site is already queued or running.
	existing, ok, err := m.findActiveOrQueuedLocked(in.Site, in.Kind, in.TargetID, in.CID)
	if err != nil {
		return Task{}, err
	}
	if ok {
		if initialPlan != nil && existing.Status == "queued" {
			if _, has := m.initialPlans[existing.TaskID]; !has {
				m.initialPlans[existing.TaskID] = *initialPlan
			}
		}
		return existing, nil
	}

	now := time.Now().UTC().Format(time.RFC3339)
	var rnd [6]byte
	_, _ = rand.Read(rnd[:])
	in.TaskID = "dl-" + hex.EncodeToString(rnd[:])
	in.Status = "queued"
	if in.Source == "" {
		in.Source = "manual"
	}
	in.CreatedAt = now
	in.UpdatedAt = now

	linkedInt := 0
	if in.CacheLinkedVideos {
		linkedInt = 1
	}

	filesJSON, _ := json.Marshal(in.Files)
	attemptsJSON, _ := json.Marshal(in.Attempts)

	_, err = m.db.Exec(`
		INSERT INTO download_tasks (
			task_id, site, kind, source, target_id, cid, page, title, part_title,
			mid, author_uid, author_name, author_face, cover_url, cache_linked_videos,
			status, pause_reason, error_kind, error_message, quality_hint,
			bytes_downloaded, total_bytes, progress, item_key, item_dir,
			files_json, attempts_json, created_at, updated_at, via_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', '', '', '', 0, 0, 0, ?, ?, ?, ?, ?, ?, ?)
	`,
		in.TaskID, in.Site, in.Kind, in.Source, in.TargetID, in.CID, in.Page, in.Title, in.PartTitle,
		in.Mid, in.AuthorUID, in.AuthorName, in.AuthorFace, in.CoverURL, linkedInt,
		in.Status, in.ItemKey, in.ItemDir, string(filesJSON), string(attemptsJSON), in.CreatedAt, in.UpdatedAt, viaJSON(in.Via),
	)
	if err != nil {
		return Task{}, err
	}
	if initialPlan != nil {
		m.initialPlans[in.TaskID] = *initialPlan
	}
	if in.Source == "manual" && m.siteBackoffFn != nil {
		sid := normalizeDownloadSite(in.Site)
		if inBackoff, until := m.siteBackoffFn(sid); inBackoff && until.After(time.Now().UTC()) {
			if !m.siteHasActiveOrQueuedProbeLocked(sid) {
				if m.manualProbeTasks == nil {
					m.manualProbeTasks = make(map[string]bool)
				}
				m.manualProbeTasks[in.TaskID] = true
			}
		}
	}
	return in, nil
}

// markManualProbeIfBackedOff atomically marks taskID as the site's single-shot
// manual probe before notifyWake when ctx carries site.WithManualProbe(ctx) and
// the site is currently in backoff with no other active/queued probe (DEC-089).
func (m *Manager) markManualProbeIfBackedOff(ctx context.Context, taskID, siteStr string) {
	if !site.IsManualProbe(ctx) || taskID == "" {
		return
	}
	sid := normalizeDownloadSite(siteStr)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.siteBackoffFn == nil {
		return
	}
	if inBackoff, until := m.siteBackoffFn(sid); inBackoff && until.After(time.Now().UTC()) {
		if !m.siteHasActiveOrQueuedProbeLocked(sid) {
			if m.manualProbeTasks == nil {
				m.manualProbeTasks = make(map[string]bool)
			}
			m.manualProbeTasks[taskID] = true
		}
	}
}

// siteHasActiveOrQueuedProbeLocked reports whether sid currently has an in-flight
// manual probe or a queued task already earmarked in manualProbeTasks.
// Caller must hold m.mu.
func (m *Manager) siteHasActiveOrQueuedProbeLocked(sid site.ID) bool {
	if m.activeProbeSite[sid] != "" {
		return true
	}
	if len(m.manualProbeTasks) == 0 || m.db == nil {
		return false
	}
	rows, err := m.db.Query(`SELECT task_id, site FROM download_tasks WHERE status = 'queued'`)
	if err != nil {
		return false
	}
	defer rows.Close()
	queuedSites := make(map[string]site.ID)
	for rows.Next() {
		var id, s string
		if scanErr := rows.Scan(&id, &s); scanErr == nil {
			queuedSites[id] = normalizeDownloadSite(s)
		}
	}
	found := false
	for id := range m.manualProbeTasks {
		qSite, ok := queuedSites[id]
		if !ok {
			delete(m.manualProbeTasks, id)
			continue
		}
		if qSite == sid {
			found = true
		}
	}
	return found
}

func (m *Manager) findActiveOrQueuedLocked(siteID, kind, targetID string, cid int64) (Task, bool, error) {
	row := m.db.QueryRow(`SELECT`+taskSelectColumns+`
		FROM download_tasks
		WHERE site = ? AND kind = ? AND target_id = ? AND cid = ? AND status IN ('queued', 'running')
		LIMIT 1
	`, siteID, kind, targetID, cid)
	t, err := scanTask(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return Task{}, false, nil
		}
		return Task{}, false, err
	}
	return t, true, nil
}

// ListTasks returns active tasks first, then tasks needing action, then finished
// tasks, with the most recently updated task first within each group.
func (m *Manager) ListTasks() ([]Task, error) {
	rows, err := m.db.Query(`SELECT` + taskSelectColumns + `
		FROM download_tasks` + taskListOrderSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m.withTaskFault(t))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	for i := range out {
		m.fillPartialReason(&out[i])
	}
	m.applyQueuedOrder(out)
	return out, nil
}

// ListTasksSummary returns all tasks in the same order as ListTasks without
// reading or deserializing files_json and attempts_json. Callers that need
// per-file progress or attempt logs should call TaskDetail(taskID).
func (m *Manager) ListTasksSummary() ([]Task, error) {
	return m.queryTaskSummaries(`SELECT` + taskSummarySelectColumns + `
		FROM download_tasks` + taskListOrderSQL)
}

// ListNonTerminalTasks returns only non-terminal tasks (excluding completed and
// canceled rows) without deserializing files_json or attempts_json.
func (m *Manager) ListNonTerminalTasks() ([]Task, error) {
	return m.queryTaskSummaries(`SELECT` + taskSummarySelectColumns + `
		FROM download_tasks
		WHERE status NOT IN ('completed', 'canceled')` + taskListOrderSQL)
}

func (m *Manager) queryTaskSummaries(query string, args ...any) ([]Task, error) {
	rows, err := m.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Task
	for rows.Next() {
		t, err := scanTaskSummary(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m.withTaskFault(t))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	for i := range out {
		m.fillPartialReason(&out[i])
	}
	m.applyQueuedOrder(out)
	return out, nil
}

func (m *Manager) listTasksSummaryByIDs(ids []string) ([]Task, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	const chunkSize = 400
	byID := make(map[string]Task, len(ids))
	for start := 0; start < len(ids); start += chunkSize {
		end := min(start+chunkSize, len(ids))
		chunk := ids[start:end]
		ph := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		batch, err := m.queryTaskSummaries(`SELECT`+taskSummarySelectColumns+`
			FROM download_tasks
			WHERE task_id IN (`+ph+`)`, args...)
		if err != nil {
			return nil, err
		}
		for _, t := range batch {
			byID[t.TaskID] = t
		}
	}
	out := make([]Task, 0, len(byID))
	for _, id := range ids {
		if t, ok := byID[id]; ok {
			out = append(out, t)
		}
	}
	return out, nil
}

// GetTask retrieves a single task by taskID.
func (m *Manager) GetTask(taskID string) (Task, bool, error) {
	row := m.db.QueryRow(`SELECT`+taskSelectColumns+`
		FROM download_tasks
		WHERE task_id = ?
	`, taskID)
	t, err := scanTask(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return Task{}, false, nil
		}
		return Task{}, false, err
	}
	t = m.withTaskFault(t)
	m.fillPartialReason(&t)
	return t, true, nil
}

// TaskDetail returns the task along with its per-file progress manifest and desensitized attempt log.
func (m *Manager) TaskDetail(taskID string) (TaskDetail, bool, error) {
	t, ok, err := m.GetTask(taskID)
	if err != nil || !ok {
		return TaskDetail{}, ok, err
	}
	return TaskDetail{
		Task:     t,
		Files:    t.Files,
		Attempts: t.Attempts,
	}, true, nil
}

func normalizeDownloadSite(raw string) site.ID {
	if sid, ok := site.ParseID(strings.TrimSpace(raw)); ok {
		return sid
	}
	return site.Bilibili
}

func (m *Manager) listQueuedIDsLocked() ([]string, error) {
	rows, err := m.db.Query(`SELECT task_id FROM download_tasks WHERE status = 'queued' ORDER BY rowid ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (m *Manager) effectiveQueuedOrderLocked(dbQueuedIDs []string) []string {
	if len(dbQueuedIDs) == 0 {
		m.queueOrder = nil
		return nil
	}
	queuedSet := make(map[string]bool, len(dbQueuedIDs))
	for _, id := range dbQueuedIDs {
		queuedSet[id] = true
	}
	out := make([]string, 0, len(dbQueuedIDs))
	seen := make(map[string]bool, len(dbQueuedIDs))
	for _, id := range m.queueOrder {
		if queuedSet[id] && !seen[id] {
			out = append(out, id)
			seen[id] = true
		}
	}
	for _, id := range dbQueuedIDs {
		if !seen[id] {
			out = append(out, id)
			seen[id] = true
		}
	}
	if len(m.queueOrder) > 0 {
		m.queueOrder = append([]string(nil), out...)
	}
	return out
}

func (m *Manager) removeQueueOrderLocked(taskID string) {
	if len(m.queueOrder) == 0 {
		return
	}
	dst := m.queueOrder[:0]
	for _, id := range m.queueOrder {
		if id != taskID {
			dst = append(dst, id)
		}
	}
	if len(dst) == 0 {
		m.queueOrder = nil
	} else {
		m.queueOrder = dst
	}
}

func (m *Manager) applyQueuedOrder(tasks []Task) {
	var queuedPos []int
	for i := range tasks {
		if tasks[i].Status == "queued" {
			queuedPos = append(queuedPos, i)
		}
	}
	if len(queuedPos) < 2 {
		return
	}
	m.mu.Lock()
	dbIDs, err := m.listQueuedIDsLocked()
	var order []string
	if err == nil && len(dbIDs) > 0 {
		order = m.effectiveQueuedOrderLocked(dbIDs)
	}
	m.mu.Unlock()
	if len(order) == 0 {
		return
	}
	rank := make(map[string]int, len(order))
	for i, id := range order {
		rank[id] = i
	}
	queued := make([]Task, len(queuedPos))
	for i, idx := range queuedPos {
		queued[i] = tasks[idx]
	}
	sort.SliceStable(queued, func(i, j int) bool {
		ri, oki := rank[queued[i].TaskID]
		rj, okj := rank[queued[j].TaskID]
		switch {
		case oki && okj:
			return ri < rj
		case oki:
			return true
		case okj:
			return false
		default:
			return false
		}
	})
	for i, idx := range queuedPos {
		tasks[idx] = queued[i]
	}
}

func (m *Manager) claimNextQueuedLocked() (Task, bool, bool, time.Time) {
	query := `SELECT` + taskSelectColumns + `
		FROM download_tasks
		WHERE status = 'queued'`
	var args []any
	if len(m.activeRun) > 0 {
		query += " AND task_id NOT IN (" + strings.TrimSuffix(strings.Repeat("?,", len(m.activeRun)), ",") + ")"
		for id := range m.activeRun {
			args = append(args, id)
		}
	}
	query += " ORDER BY rowid ASC"
	rows, err := m.db.Query(query, args...)
	if err != nil {
		return Task{}, false, false, time.Time{}
	}
	var candidates []Task
	for rows.Next() {
		t, sErr := scanTask(rows)
		if sErr != nil {
			_ = rows.Close()
			return Task{}, false, false, time.Time{}
		}
		candidates = append(candidates, t)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil || len(candidates) == 0 {
		if len(candidates) == 0 {
			m.queueOrder = nil
		}
		return Task{}, false, false, time.Time{}
	}

	// Reorder candidates by manual queue priority (pruning stale IDs).
	if len(m.queueOrder) > 0 {
		queuedIDs := make([]string, len(candidates))
		for i := range candidates {
			queuedIDs[i] = candidates[i].TaskID
		}
		order := m.effectiveQueuedOrderLocked(queuedIDs)
		rank := make(map[string]int, len(order))
		for i, id := range order {
			rank[id] = i
		}
		sort.SliceStable(candidates, func(i, j int) bool {
			return rank[candidates[i].TaskID] < rank[candidates[j].TaskID]
		})
	}

	// Skip sites currently in backoff for all non-probe tasks (including extra manual
	// tasks) so one site's risk-control backoff never blocks other healthy sites (DEC-088)
	// and never bursts concurrent probes (DEC-089). Explicit single-shot manual-probe
	// tasks bypass siteBackoffFn with at most 1 probe in flight per site.
	var (
		now             = time.Now().UTC()
		earliestSkipped time.Time
		eligible        = make([]Task, 0, len(candidates))
		backoffCache    map[site.ID]struct {
			blocked bool
			until   time.Time
		}
	)
	if m.siteBackoffFn != nil {
		backoffCache = make(map[site.ID]struct {
			blocked bool
			until   time.Time
		}, 4)
	}
	for _, c := range candidates {
		sid := normalizeDownloadSite(c.Site)
		if m.manualProbeTasks[c.TaskID] {
			if m.activeProbeSite[sid] != "" {
				continue
			}
			eligible = append(eligible, c)
			continue
		}
		if m.siteBackoffFn != nil {
			res, cached := backoffCache[sid]
			if !cached {
				inBackoff, until := m.siteBackoffFn(sid)
				res.blocked = inBackoff && until.After(now)
				res.until = until.UTC()
				backoffCache[sid] = res
			}
			if res.blocked {
				if earliestSkipped.IsZero() || res.until.Before(earliestSkipped) {
					earliestSkipped = res.until
				}
				continue
			}
		}
		eligible = append(eligible, c)
	}
	if len(eligible) == 0 {
		return Task{}, false, false, earliestSkipped
	}

	// Cross-site anti-starvation: prefer a site with the fewest currently active
	// download slots so one site's backlog never fills all concurrency slots and
	// starves other sites; tie-break by effective queue order (manual priority, then FIFO).
	activeBySite := make(map[site.ID]int, len(m.activeSite))
	for _, s := range m.activeSite {
		activeBySite[normalizeDownloadSite(s)]++
	}
	bestIdx := 0
	bestActive := activeBySite[normalizeDownloadSite(eligible[0].Site)]
	for i := 1; i < len(eligible); i++ {
		cnt := activeBySite[normalizeDownloadSite(eligible[i].Site)]
		if cnt < bestActive {
			bestActive = cnt
			bestIdx = i
			if bestActive == 0 {
				break
			}
		}
	}

	t := eligible[bestIdx]
	nowStr := now.Format(time.RFC3339)
	res, err := m.db.Exec(`
		UPDATE download_tasks
		SET status = 'running', pause_reason = '', error_kind = '', error_message = '', updated_at = ?
		WHERE task_id = ? AND status = 'queued'
	`, nowStr, t.TaskID)
	if err != nil {
		return Task{}, false, false, earliestSkipped
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Task{}, false, false, earliestSkipped
	}
	isManualProbe := m.manualProbeTasks[t.TaskID]
	if isManualProbe {
		delete(m.manualProbeTasks, t.TaskID)
		if m.activeProbeSite == nil {
			m.activeProbeSite = make(map[site.ID]string)
		}
		m.activeProbeSite[normalizeDownloadSite(t.Site)] = t.TaskID
	}
	m.removeQueueOrderLocked(t.TaskID)
	t.Status = "running"
	t.PauseReason = ""
	t.ErrorKind = ""
	t.ErrorMessage = ""
	t.UpdatedAt = nowStr
	return t, isManualProbe, true, earliestSkipped
}

func (m *Manager) resetFileProgress(t *Task, relPath string) {
	relID := taskFileIdentity(relPath)
	for i := range t.Files {
		if taskFileIdentity(t.Files[i].Path) == relID {
			t.Files[i].DownloadedBytes = 0
			t.Files[i].TotalBytes = 0
			t.Files[i].SpeedBps = 0
		}
	}
}

func (m *Manager) setFileProgress(t *Task, role, relPath, status string, downloaded, total, speed int64) {
	found := false
	for i := range t.Files {
		if t.Files[i].Path == relPath {
			t.Files[i].Role = role
			t.Files[i].Status = status
			t.Files[i].DownloadedBytes = downloaded
			if total > 0 {
				t.Files[i].TotalBytes = total
			}
			t.Files[i].SpeedBps = speed
			found = true
			break
		}
	}
	if !found {
		t.Files = append(t.Files, TaskFileProgress{
			Role:            role,
			Path:            relPath,
			Status:          status,
			DownloadedBytes: downloaded,
			TotalBytes:      total,
			SpeedBps:        speed,
		})
	}
}

func (m *Manager) recordAttempt(t *Task, relFile string, offset int64, rawURL string, attempt int, errKind string, httpStatus int) {
	host := ""
	if u, err := url.Parse(rawURL); err == nil {
		host = u.Hostname()
	}
	entry := TaskAttempt{
		Time:       time.Now().UTC().Format(time.RFC3339),
		File:       relFile,
		Offset:     offset,
		Host:       host,
		Attempt:    attempt,
		ErrorKind:  errKind,
		HTTPStatus: httpStatus,
	}
	t.Attempts = append(t.Attempts, entry)
	if len(t.Attempts) > 200 {
		t.Attempts = t.Attempts[len(t.Attempts)-200:]
	}
	ownerID := t.TaskID
	if ownerID == "" && t.ProgressOwner != nil {
		ownerID = t.ProgressOwner.TaskID
		t.ProgressOwner.Attempts = t.Attempts
	}
	if ownerID != "" && m.db != nil {
		_, _ = m.db.Exec(`
			INSERT INTO task_attempts (task_id, attempt_time, file_path, offset_bytes, host, attempt_num, error_kind, http_status)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, ownerID, entry.Time, entry.File, entry.Offset, entry.Host, entry.Attempt, entry.ErrorKind, entry.HTTPStatus)
	}
}

func (m *Manager) saveTaskProgress(t Task) error {
	if t.ProgressOwner != nil {
		owner := t.ProgressOwner
		for _, file := range t.Files {
			file.Path = t.ProgressPrefix + file.Path
			file.ItemDir = t.ItemDir
			found := false
			for i := range owner.Files {
				if owner.Files[i].Path == file.Path {
					owner.Files[i], found = file, true
					break
				}
			}
			if !found {
				owner.Files = append(owner.Files, file)
			}
		}
		if t.BytesDownloaded > 0 || t.TotalBytes > 0 {
			owner.BytesDownloaded = t.BytesDownloaded
			owner.TotalBytes = t.TotalBytes
			owner.Progress = t.Progress
		}
		owner.Attempts = t.Attempts
		return m.saveTaskProgress(*owner)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	t.UpdatedAt = now
	if t.ErrorMessage == "" && t.LastError != "" {
		t.ErrorMessage = t.LastError
	}
	t.LastError = t.ErrorMessage
	filesJSON, _ := json.Marshal(t.Files)
	attemptsJSON, _ := json.Marshal(t.Attempts)
	res, err := m.db.Exec(`
		UPDATE download_tasks
		SET
			cid = ?, page = ?, title = ?, part_title = ?,
			mid = ?, author_uid = ?, author_name = ?, author_face = ?, cover_url = ?,
			status = ?, pause_reason = ?, error_kind = ?, error_message = ?, quality_hint = ?,
			bytes_downloaded = ?, total_bytes = ?, progress = ?,
			item_key = ?, item_dir = ?, files_json = ?, attempts_json = ?, updated_at = ?
		WHERE task_id = ? AND status = 'running'
	`,
		t.CID, t.Page, t.Title, t.PartTitle,
		t.Mid, t.AuthorUID, t.AuthorName, t.AuthorFace, t.CoverURL,
		t.Status, t.PauseReason, t.ErrorKind, t.ErrorMessage, t.QualityHint,
		t.BytesDownloaded, t.TotalBytes, t.Progress,
		t.ItemKey, t.ItemDir, string(filesJSON), string(attemptsJSON), t.UpdatedAt,
		t.TaskID,
	)
	if err != nil {
		return err
	}
	if changed, _ := res.RowsAffected(); changed == 0 {
		return nil
	}
	m.taskFaults.Delete(t.TaskID)
	m.emitUpdate(t)
	return nil
}

func (m *Manager) emitUpdate(t Task) {
	if t.ErrorMessage == "" && t.LastError != "" {
		t.ErrorMessage = t.LastError
	}
	t.LastError = t.ErrorMessage
	if m.onTaskUpdate != nil && t.TaskID != "" {
		m.notifyMu.Lock()
		if m.lastNotify == nil {
			m.lastNotify = make(map[string]taskNotification)
		}
		previous := m.lastNotify[t.TaskID]
		if previous.status == t.Status && t.Status == "running" && time.Since(previous.at) < time.Second {
			m.notifyMu.Unlock()
			return
		}
		switch t.Status {
		case "completed", "failed", "canceled", "partial":
			delete(m.lastNotify, t.TaskID)
		default:
			m.lastNotify[t.TaskID] = taskNotification{status: t.Status, at: time.Now()}
		}
		m.notifyMu.Unlock()
		m.onTaskUpdate(t)
	}
}

type taskNotification struct {
	status string
	at     time.Time
}

func (m *Manager) persistTaskProgress(task Task) {
	if err := m.saveTaskProgress(task); err != nil {
		task.Status, task.ErrorKind, task.ErrorMessage = "failed", "storage", "任务状态写入失败，请检查存储后重试"
		m.taskFaults.Store(task.TaskID, task)
		m.emitUpdate(task)
	}
}

func (m *Manager) withTaskFault(task Task) Task {
	if fault, exists := m.taskFaults.Load(task.TaskID); exists {
		return fault.(Task)
	}
	return task
}

// fillPartialReason backfills the concise missing-item summary on partial tasks
// whose persisted error_message was left empty (for example, items recorded
// before partial reasons were preserved on download_tasks). Must only be called
// when no active rows cursor holds the single SQLite connection.
func (m *Manager) fillPartialReason(task *Task) {
	if task == nil || task.Status != "partial" || m.lib == nil {
		return
	}
	if strings.TrimSpace(task.ErrorMessage) != "" || strings.TrimSpace(task.LastError) != "" || strings.TrimSpace(task.ItemKey) == "" {
		return
	}
	if meta, ok, err := m.lib.GetItemMetaOnly(task.ItemKey); err == nil && ok && len(meta.Missing) > 0 {
		task.ErrorMessage = summarizeMissingReasons(meta.Missing)
		task.LastError = task.ErrorMessage
	}
}

// Register task ownership before creating any file. A pre-existing name with
// no task checkpoint or manifest is a user file, and must never be replaced.
func (m *Manager) claimTaskFile(t *Task, root, rel, finalPath string) error {
	owned := false
	for _, file := range t.Files {
		if taskFileIdentity(file.Path) == taskFileIdentity(rel) && taskFileOwned(file) {
			owned = true
		}
	}
	if t.ProgressOwner != nil {
		for _, file := range t.ProgressOwner.Files {
			if taskFileIdentity(file.Path) == taskFileIdentity(t.ProgressPrefix+rel) && strings.EqualFold(file.ItemDir, root) && taskFileOwned(file) {
				owned = true
			}
		}
	}
	if m.lib != nil {
		if meta, ok, err := m.lib.GetItem(t.ItemKey); err != nil {
			return err
		} else if ok && strings.EqualFold(meta.ItemDir, root) {
			for _, file := range meta.Media {
				if taskFileIdentity(file.Path) == taskFileIdentity(rel) {
					owned = true
				}
			}
		}
	}
	if !owned {
		for _, p := range []string{finalPath, finalPath + ".part"} {
			if _, err := os.Lstat(p); err == nil {
				return &bilibili.SiteError{Kind: "storage", Message: "目标文件已存在且未由本任务登记，已保留原文件"}
			} else if !os.IsNotExist(err) {
				return err
			}
		}
	}
	found := false
	for i := range t.Files {
		if taskFileIdentity(t.Files[i].Path) == taskFileIdentity(rel) {
			t.Files[i].Owned = true
			found = true
		}
	}
	if !found {
		t.Files = append(t.Files, TaskFileProgress{Path: rel, Status: "waiting", Owned: true})
	}
	return m.saveTaskProgress(*t)
}

func taskFileIdentity(rel string) string {
	return strings.TrimSuffix(filepath.ToSlash(rel), ".part")
}

func taskFileOwned(file TaskFileProgress) bool {
	// Pre-ownership task records can prove a written file through a durable
	// length checkpoint or hash. A waiting filename alone grants no ownership.
	return file.Owned || file.SHA256 != "" || file.DownloadedBytes > 0 && file.TotalBytes >= file.DownloadedBytes
}

func taskRelativePath(task *Task, filePath string) string {
	if task.ItemDir != "" {
		if rel, err := filepath.Rel(task.ItemDir, filePath); err == nil {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.Base(filePath)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func viaJSON(v *library.ItemVia) string {
	if v == nil {
		return ""
	}
	raw, _ := json.Marshal(v)
	return string(raw)
}

func scanTask(r rowScanner) (Task, error) {
	var t Task
	var linkedInt int
	var filesRaw, attemptsRaw, viaRaw string
	err := r.Scan(
		&t.TaskID, &t.Site, &t.Kind, &t.Source, &t.TargetID, &t.CID, &t.Page, &t.Title, &t.PartTitle,
		&t.Mid, &t.AuthorUID, &t.AuthorName, &t.AuthorFace, &t.CoverURL, &linkedInt,
		&t.Status, &t.PauseReason, &t.ErrorKind, &t.ErrorMessage, &t.QualityHint,
		&t.BytesDownloaded, &t.TotalBytes, &t.Progress, &t.ItemKey, &t.ItemDir,
		&filesRaw, &attemptsRaw, &t.CreatedAt, &t.UpdatedAt, &viaRaw,
	)
	if viaRaw != "" {
		var via library.ItemVia
		if json.Unmarshal([]byte(viaRaw), &via) == nil && via.UID != "" {
			t.Via = &via
		}
	}
	t.LastError = t.ErrorMessage
	t.CacheLinkedVideos = linkedInt == 1
	if filesRaw != "" {
		_ = json.Unmarshal([]byte(filesRaw), &t.Files)
	}
	if attemptsRaw != "" {
		_ = json.Unmarshal([]byte(attemptsRaw), &t.Attempts)
	}
	return t, err
}

func scanTaskSummary(r rowScanner) (Task, error) {
	var t Task
	var linkedInt int
	var viaRaw string
	err := r.Scan(
		&t.TaskID, &t.Site, &t.Kind, &t.Source, &t.TargetID, &t.CID, &t.Page, &t.Title, &t.PartTitle,
		&t.Mid, &t.AuthorUID, &t.AuthorName, &t.AuthorFace, &t.CoverURL, &linkedInt,
		&t.Status, &t.PauseReason, &t.ErrorKind, &t.ErrorMessage, &t.QualityHint,
		&t.BytesDownloaded, &t.TotalBytes, &t.Progress, &t.ItemKey, &t.ItemDir,
		&t.CreatedAt, &t.UpdatedAt, &viaRaw,
	)
	if viaRaw != "" {
		var via library.ItemVia
		if json.Unmarshal([]byte(viaRaw), &via) == nil && via.UID != "" {
			t.Via = &via
		}
	}
	t.LastError = t.ErrorMessage
	t.CacheLinkedVideos = linkedInt == 1
	return t, err
}

func hashFileSHA256(filePath string) (string, int64, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func isTempStagingPath(rel string) bool {
	if strings.HasPrefix(path.Base(filepath.ToSlash(rel)), storage.TempPrefix) {
		return true
	}
	for _, marker := range stagingMarkers {
		if strings.HasSuffix(rel, marker) || strings.HasSuffix(rel, marker+".part") {
			return true
		}
	}
	return false
}

func cleanTaskPartFiles(t Task) {
	if t.ItemDir == "" {
		return
	}
	for _, file := range t.Files {
		root, rel := t.ItemDir, file.Path
		if file.ItemDir != "" {
			root, rel = file.ItemDir, filepath.Base(file.Path)
		}
		rel = taskFileIdentity(rel)
		isTemp := isTempStagingPath(rel)
		// Always clean owned temporary staging files (e.g. .trace-*.m4s or *.stage.part)
		// even if they reached "completed" before the task was canceled or failed.
		if !taskFileOwned(file) || file.Status == "skipped" || (file.Status == "completed" && !isTemp) {
			continue
		}
		candidates := []string{rel + ".part", rel + ".part.part", rel + ".raw", rel + ".raw.part", rel + stageSuffix, rel + stageSuffix + ".part"}
		if isTemp {
			candidates = append(candidates, rel)
		}
		for _, pending := range candidates {
			partPath, err := library.ResolveItemPath(root, pending)
			if err == nil {
				_ = os.Remove(partPath)
			}
		}
	}
}

func mergePartRecords(existing []library.VideoPartRecord, added library.VideoPartRecord) []library.VideoPartRecord {
	out := make([]library.VideoPartRecord, 0, len(existing)+1)
	replaced := false
	for _, p := range existing {
		if p.Page == added.Page || (p.CID > 0 && p.CID == added.CID) {
			out = append(out, added)
			replaced = true
		} else {
			out = append(out, p)
		}
	}
	if !replaced {
		out = append(out, added)
	}
	return out
}

func mergeMediaFiles(existing []library.MediaFile, added []library.MediaFile) []library.MediaFile {
	byPath := make(map[string]library.MediaFile, len(existing)+len(added))
	var order []string
	for _, m := range existing {
		byPath[m.Path] = m
		order = append(order, m.Path)
	}
	for _, m := range added {
		if _, seen := byPath[m.Path]; !seen {
			order = append(order, m.Path)
		}
		byPath[m.Path] = m
	}
	out := make([]library.MediaFile, 0, len(order))
	for _, p := range order {
		out = append(out, byPath[p])
	}
	return out
}

func audioRank(id int) int {
	switch id {
	case 30251:
		return 5
	case 30250:
		return 4
	case 30280:
		return 3
	case 30232:
		return 2
	case 30216:
		return 1
	}
	return 0
}

func isStopError(err error) bool {
	return bilibili.IsKind(err, bilibili.ErrKindRiskControl) || bilibili.IsKind(err, bilibili.ErrKindRateLimited) || bilibili.IsKind(err, bilibili.ErrKindAuthRequired)
}

func isStorageError(err error) bool {
	if err == nil {
		return false
	}
	var pathErr *os.PathError
	var linkErr *os.LinkError
	return errors.As(err, &pathErr) ||
		errors.As(err, &linkErr) ||
		site.IsKind(err, site.ErrKindStorage) ||
		bilibili.IsKind(err, "storage")
}

func targetQuality(quality string) int {
	switch quality {
	case "2160":
		return 120
	case "1080":
		return 112
	case "720":
		return 74
	}
	return 127
}
