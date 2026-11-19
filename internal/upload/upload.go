// Package upload is trace-server's built-in upload (docs/design/remote.md
// §6.2, DEC-062 ⑨). It hands each finished item to rclone: the media files
// first, the item's item.json last, so a copy of item.json in the cloud only
// lists files that are already there. After rclone confirms (exit code 0)
// the uploaded files may be deleted locally. Unfinished files (*.part,
// .trace-*) and items still being written are never uploaded or deleted.
package upload

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/remote"
)

// Config is server.json's upload section.
type Config struct {
	Mode              string   `json:"mode"`             // "rclone" or "none"
	Remote            string   `json:"remote,omitempty"` // rclone remote path, e.g. "gdrive:trace"
	Rclone            string   `json:"rclone,omitempty"` // program; found in PATH when empty
	Config            string   `json:"config,omitempty"` // optional rclone config file
	DeleteAfterUpload bool     `json:"deleteAfterUpload"`
	ExtraArgs         []string `json:"extraArgs,omitempty"`
}

// DefaultConfig is the upload section init writes: no upload until the
// user names an rclone remote; files are deleted after upload.
func DefaultConfig() Config {
	return Config{Mode: remote.UploadNone, DeleteAfterUpload: true}
}

// Normalize checks the configuration.
func (c Config) Normalize() (Config, error) {
	c.Mode = strings.ToLower(strings.TrimSpace(c.Mode))
	if c.Mode == "" {
		c.Mode = remote.UploadNone
	}
	c.Remote = strings.TrimSpace(c.Remote)
	switch c.Mode {
	case remote.UploadNone:
	case remote.UploadRclone:
		if c.Remote == "" || !strings.Contains(c.Remote, ":") {
			return c, fmt.Errorf("upload.remote 应是 rclone 远端路径，例如 gdrive:trace")
		}
		c.Remote = strings.TrimRight(c.Remote, "/")
	default:
		return c, fmt.Errorf("upload.mode 只能是 rclone 或 none")
	}
	return c, nil
}

// Backoff is the wait before the n-th retry of a failed item: 1, 5, 15,
// 60 minutes, then every hour.
func Backoff(failures int) time.Duration {
	switch {
	case failures <= 1:
		return time.Minute
	case failures == 2:
		return 5 * time.Minute
	case failures == 3:
		return 15 * time.Minute
	default:
		return time.Hour
	}
}

// Busy reports whether an item is still being written (a cache task is
// queued or running for it, or it is recording).
type Busy func(itemKey string) bool

// Options configure an Uploader.
type Options struct {
	Config  Config
	Library *library.Store
	DB      *sql.DB // library.db; holds upload_files
	DataDir string  // temporary file lists live here, never in the export folder
	Busy    Busy
	Now     func() time.Time
	// Env is the environment of rclone; os.Environ() when nil.
	Env []string
	// Logf receives one line per upload and failure (no rclone output).
	Logf func(format string, args ...any)
}

type failure struct {
	count int
	next  time.Time
	kind  string
	msg   string
}

// Uploader uploads items one at a time with a single rclone process.
type Uploader struct {
	opt Options
	cfg Config

	mu        sync.Mutex
	pending   map[string]bool
	failures  map[string]failure
	gone      map[string]bool
	current   string
	curBytes  int64
	lastErr   failure
	lastErrAt time.Time
	wake      chan struct{}
	stopped   chan struct{}
}

// New prepares an uploader and its table.
func New(opt Options) (*Uploader, error) {
	cfg, err := opt.Config.Normalize()
	if err != nil {
		return nil, err
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Logf == nil {
		opt.Logf = func(string, ...any) {}
	}
	if opt.Busy == nil {
		opt.Busy = func(string) bool { return false }
	}
	if opt.DB == nil {
		// Only Test works without the database (trace-server upload-test).
		return &Uploader{opt: opt, cfg: cfg, pending: map[string]bool{}, failures: map[string]failure{}, gone: map[string]bool{}, wake: make(chan struct{}, 1), stopped: make(chan struct{})}, nil
	}
	if _, err := opt.DB.Exec(`CREATE TABLE IF NOT EXISTS upload_files (
		item_key TEXT NOT NULL,
		rel_path TEXT NOT NULL,
		bytes INTEGER NOT NULL DEFAULT 0,
		sha256 TEXT NOT NULL DEFAULT '',
		uploaded_at TEXT NOT NULL,
		PRIMARY KEY (item_key, rel_path)
	)`); err != nil {
		return nil, fmt.Errorf("upload: create upload_files: %w", err)
	}
	return &Uploader{opt: opt, cfg: cfg, pending: map[string]bool{}, failures: map[string]failure{}, gone: map[string]bool{}, wake: make(chan struct{}, 1), stopped: make(chan struct{})}, nil
}

// Config returns the active configuration.
func (u *Uploader) Config() Config { return u.cfg }

// Notify queues an item after its manifest was saved. It never blocks and
// never calls back into the library (it runs under the library's lock).
func (u *Uploader) Notify(meta library.ItemMetadata) {
	if u.cfg.Mode != remote.UploadRclone || meta.ItemKey == "" {
		return
	}
	u.mu.Lock()
	u.pending[meta.ItemKey] = true
	u.mu.Unlock()
	u.poke()
}

func (u *Uploader) poke() {
	select {
	case u.wake <- struct{}{}:
	default:
	}
}

// RetryNow clears the waits of failed items.
func (u *Uploader) RetryNow() {
	u.mu.Lock()
	for k, f := range u.failures {
		f.next = time.Time{}
		u.failures[k] = f
		u.pending[k] = true
	}
	u.mu.Unlock()
	u.poke()
}

// Run uploads until ctx ends: first every indexed item (catching up after a
// restart), then what Notify queues, re-checking waiting items each minute.
func (u *Uploader) Run(ctx context.Context) {
	defer close(u.stopped)
	if u.cfg.Mode != remote.UploadRclone {
		<-ctx.Done()
		return
	}
	u.queueAll()
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	lastScan := u.opt.Now()
	for {
		u.drain(ctx)
		select {
		case <-ctx.Done():
			return
		case <-u.wake:
		case <-tick.C:
			if u.opt.Now().Sub(lastScan) >= 15*time.Minute {
				u.queueAll()
				lastScan = u.opt.Now()
			}
		}
	}
}

// Wait returns after Run has returned.
func (u *Uploader) Wait() { <-u.stopped }

func (u *Uploader) queueAll() {
	if u.opt.Library == nil {
		return
	}
	keys, err := u.opt.Library.AllItemKeys()
	if err != nil {
		u.opt.Logf("upload: list items: %v", err)
		return
	}
	known := make(map[string]bool, len(keys))
	var toQueue []string
	for _, k := range keys {
		known[k] = true
		if pending, _ := u.inspectItemPending(k); pending {
			toQueue = append(toQueue, k)
		}
	}
	u.mu.Lock()
	for _, k := range toQueue {
		u.pending[k] = true
	}
	for id := range u.gone {
		if k, _, ok := strings.Cut(id, "\x00"); ok && !known[k] {
			delete(u.gone, id)
		}
	}
	u.mu.Unlock()
}

// inspectItemPending reports whether key currently has local files or manifest
// changes that still need upload (or post-upload deletion), plus the estimated
// bytes remaining to upload.
func (u *Uploader) inspectItemPending(key string) (bool, int64) {
	if u.opt.Library == nil || u.opt.DB == nil {
		return false, 0
	}
	meta, ok, err := u.opt.Library.GetItem(key)
	if err != nil || !ok || meta.ItemDir == "" {
		return false, 0
	}
	if _, ok := u.opt.Library.RelativeItemDir(meta.ItemDir); !ok {
		return false, 0
	}
	manifest, raw, err := readManifest(meta.ItemDir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, 0
		}
		// Unreadable/corrupt item.json: surface so failure tracking runs.
		return true, 0
	}
	recs, err := u.records(key)
	if err != nil {
		return true, 0
	}
	var pendingBytes int64
	hasLocalRegistered := false
	for _, f := range library.RegisteredFiles(manifest) {
		full, err := library.ResolveItemPath(meta.ItemDir, f.Path)
		if err != nil {
			continue
		}
		info, err := os.Lstat(full)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		hasLocalRegistered = true
		f.Bytes = info.Size()
		if !uploaded(recs, f) {
			pendingBytes += f.Bytes
		}
	}
	sum := sha256.Sum256(raw)
	jsonRec := library.ManifestFile{Path: "item.json", Bytes: int64(len(raw)), SHA256: hex.EncodeToString(sum[:])}
	jsonPending := !uploaded(recs, jsonRec)
	if jsonPending {
		pendingBytes += jsonRec.Bytes
	}
	if pendingBytes > 0 {
		return true, pendingBytes
	}
	// All files + item.json uploaded; still need a pass if DeleteAfterUpload is
	// enabled and local copies remain for a non-recording item.
	recording := meta.Kind == "live" && meta.Status == "recording"
	if u.cfg.DeleteAfterUpload && !recording && (hasLocalRegistered || len(raw) > 0) {
		return true, 0
	}
	return false, 0
}

// drain processes every due pending item once per pass.
func (u *Uploader) drain(ctx context.Context) {
	deferred := map[string]bool{}
	rechecks := map[string]int{}
	for ctx.Err() == nil {
		key, ok := u.nextDue(deferred)
		if !ok {
			return
		}
		again, deferPass := u.processKey(ctx, key)
		u.mu.Lock()
		u.current, u.curBytes = "", 0
		if again {
			u.pending[key] = true
			if deferPass {
				deferred[key] = true
			} else {
				rechecks[key]++
				if rechecks[key] >= 2 {
					deferred[key] = true
				}
			}
		}
		u.mu.Unlock()
	}
}

func (u *Uploader) nextDue(skip map[string]bool) (string, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	now := u.opt.Now()
	keys := make([]string, 0, len(u.pending))
	for k := range u.pending {
		if skip != nil && skip[k] {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if f, ok := u.failures[k]; ok && now.Before(f.next) {
			continue
		}
		delete(u.pending, k)
		return k, true
	}
	return "", false
}

type record struct {
	bytes int64
	sha   string
}

func (u *Uploader) records(itemKey string) (map[string]record, error) {
	if u.opt.DB == nil {
		return nil, errors.New("upload: database not configured")
	}
	rows, err := u.opt.DB.Query(`SELECT rel_path, bytes, sha256 FROM upload_files WHERE item_key = ?`, itemKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]record{}
	for rows.Next() {
		var rel string
		var r record
		if err := rows.Scan(&rel, &r.bytes, &r.sha); err != nil {
			return nil, err
		}
		out[rel] = r
	}
	return out, rows.Err()
}

func (u *Uploader) remember(itemKey string, files []library.ManifestFile) error {
	if u.opt.DB == nil {
		return errors.New("upload: database not configured")
	}
	tx, err := u.opt.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	at := u.opt.Now().UTC().Format(time.RFC3339)
	for _, f := range files {
		if _, err := tx.Exec(`INSERT INTO upload_files (item_key, rel_path, bytes, sha256, uploaded_at) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(item_key, rel_path) DO UPDATE SET bytes = excluded.bytes, sha256 = excluded.sha256, uploaded_at = excluded.uploaded_at`,
			itemKey, f.Path, f.Bytes, f.SHA256, at); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func uploaded(recs map[string]record, f library.ManifestFile) bool {
	r, ok := recs[f.Path]
	if !ok || r.bytes != f.Bytes {
		return false
	}
	return f.SHA256 == "" || r.sha == "" || strings.EqualFold(r.sha, f.SHA256)
}

// process handles one item; it reports whether the item should be looked at
// again soon (still being written, or changed while uploading).
func (u *Uploader) process(ctx context.Context, key string) bool {
	again, _ := u.processKey(ctx, key)
	return again
}

func (u *Uploader) processKey(ctx context.Context, key string) (again bool, deferPass bool) {
	if u.opt.Library == nil {
		return false, false
	}
	meta, ok, err := u.opt.Library.GetItem(key)
	if err != nil || !ok || meta.ItemDir == "" {
		return false, false
	}
	relDir, ok := u.opt.Library.RelativeItemDir(meta.ItemDir)
	if !ok {
		return false, false
	}
	busy := u.opt.Busy(key)
	recording := meta.Kind == "live" && meta.Status == "recording"
	if busy && !recording {
		// A cache task is still writing this item: defer until Notify or next
		// tick so we don't busy-spin while waiting.
		return true, true
	}
	final := !busy && !recording

	recs, err := u.records(key)
	if err != nil {
		u.fail(key, remote.UploadErrOther, err.Error())
		return false, false
	}
	var todo []library.ManifestFile
	for _, f := range library.RegisteredFiles(meta) {
		if !final && f.Role != "live_segment" {
			continue // only finished segments go up while recording
		}
		full, err := library.ResolveItemPath(meta.ItemDir, f.Path)
		if err != nil {
			continue
		}
		info, err := os.Lstat(full)
		if err != nil || !info.Mode().IsRegular() {
			if _, done := recs[f.Path]; !done {
				u.markGone(key, f.Path)
			}
			continue
		}
		u.clearGone(key, f.Path)
		f.Bytes = info.Size()
		if uploaded(recs, f) {
			continue
		}
		todo = append(todo, f)
	}

	// 1. The media files.
	if len(todo) > 0 {
		var size int64
		for _, f := range todo {
			size += f.Bytes
		}
		u.mu.Lock()
		u.current, u.curBytes = key, size
		u.mu.Unlock()
		names := make([]string, len(todo))
		for i, f := range todo {
			names[i] = f.Path
		}
		if err := u.copy(ctx, meta.ItemDir, relDir, names); err != nil {
			if ctx.Err() != nil {
				return true, true
			}
			u.failErr(key, err)
			return false, false
		}
		if err := u.remember(key, todo); err != nil {
			u.fail(key, remote.UploadErrOther, err.Error())
			return false, false
		}
		u.opt.Logf("upload: item=%s files=%d bytes=%d", key, len(todo), size)
		if u.cfg.DeleteAfterUpload && !final {
			for _, f := range todo {
				u.removeIfUnchanged(key, meta.ItemDir, f)
			}
		}
	}

	// 2. item.json, frozen as read, only when every file it lists is up.
	manifest, raw, err := readManifest(meta.ItemDir)
	if err != nil {
		if os.IsNotExist(err) {
			u.clearFailure(key)
			return false, false
		}
		u.fail(key, remote.UploadErrOther, "读取 item.json 失败")
		return false, false
	}
	if !final && manifest.Status != "recording" {
		// Recording finished while step 1 ran; re-run so cover and remaining
		// files upload before the final item.json.
		return true, false
	}
	recs, err = u.records(key)
	if err != nil {
		u.fail(key, remote.UploadErrOther, err.Error())
		return false, false
	}
	for _, f := range library.RegisteredFiles(manifest) {
		if !final && f.Role != "live_segment" {
			continue
		}
		full, err := library.ResolveItemPath(meta.ItemDir, f.Path)
		if err != nil {
			continue
		}
		info, err := os.Lstat(full)
		if err != nil || !info.Mode().IsRegular() {
			if _, ok := recs[f.Path]; ok || u.isGone(key, f.Path) {
				continue
			}
			continue
		}
		f.Bytes = info.Size()
		if !uploaded(recs, f) {
			return true, false // listed or updated on disk but not uploaded yet
		}
	}
	sum := sha256.Sum256(raw)
	jsonRec := library.ManifestFile{Path: "item.json", Bytes: int64(len(raw)), SHA256: hex.EncodeToString(sum[:])}
	if !uploaded(recs, jsonRec) {
		stage, err := os.MkdirTemp(u.tempDir(), "item-")
		if err != nil {
			u.fail(key, remote.UploadErrOther, err.Error())
			return false, false
		}
		defer os.RemoveAll(stage)
		// The cloud copy leaves out this machine's paths (the record keeps
		// the local version's hash, which decides the deletion below).
		portable, err := library.PortableManifest(manifest)
		if err != nil {
			u.fail(key, remote.UploadErrOther, err.Error())
			return false, false
		}
		if err := os.WriteFile(filepath.Join(stage, "item.json"), portable, 0o600); err != nil {
			u.fail(key, remote.UploadErrOther, err.Error())
			return false, false
		}
		if err := u.copy(ctx, stage, relDir, []string{"item.json"}); err != nil {
			if ctx.Err() != nil {
				return true, true
			}
			u.failErr(key, err)
			return false, false
		}
		if err := u.remember(key, []library.ManifestFile{jsonRec}); err != nil {
			u.fail(key, remote.UploadErrOther, err.Error())
			return false, false
		}
	}
	u.clearFailure(key)

	// 3. A finished item whose last item.json is up loses its local files.
	if final && u.cfg.DeleteAfterUpload {
		recs, err = u.records(key)
		if err != nil {
			u.fail(key, remote.UploadErrOther, err.Error())
			return false, false
		}
		if !uploaded(recs, jsonRec) {
			return false, false
		}
		for _, f := range library.RegisteredFiles(manifest) {
			full, err := library.ResolveItemPath(meta.ItemDir, f.Path)
			if err != nil {
				continue
			}
			info, err := os.Lstat(full)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			f.Bytes = info.Size()
			if uploaded(recs, f) {
				u.removeIfUnchanged(key, meta.ItemDir, f)
			}
		}
		// item.json goes last, and only the version that was uploaded.
		if cur, err := os.ReadFile(filepath.Join(meta.ItemDir, "item.json")); err == nil {
			if again := sha256.Sum256(cur); hex.EncodeToString(again[:]) == jsonRec.SHA256 {
				if err := u.opt.Library.RemoveItemFile(key, "item.json"); err != nil {
					u.opt.Logf("upload: remove item.json of %s: %v", key, err)
				}
			}
		}
		u.opt.Library.RemoveEmptyItemFolders(key)
	}
	return false, false
}

func readManifest(itemDir string) (library.ItemMetadata, []byte, error) {
	raw, err := os.ReadFile(filepath.Join(itemDir, "item.json"))
	if err != nil {
		return library.ItemMetadata{}, nil, err
	}
	meta, err := library.ParseItemMetadata(raw)
	if err != nil {
		return library.ItemMetadata{}, nil, err
	}
	return meta, raw, nil
}

// removeIfUnchanged deletes an uploaded file through the library when its
// size is still what was uploaded.
func (u *Uploader) removeIfUnchanged(key, itemDir string, f library.ManifestFile) {
	full, err := library.ResolveItemPath(itemDir, f.Path)
	if err != nil {
		return
	}
	info, err := os.Lstat(full)
	if err != nil || !info.Mode().IsRegular() || info.Size() != f.Bytes {
		return
	}
	if err := u.opt.Library.RemoveItemFile(key, f.Path); err != nil {
		u.opt.Logf("upload: remove %s of %s: %v", f.Path, key, err)
	}
}

func (u *Uploader) failErr(key string, err error) {
	var re *rcloneError
	if errors.As(err, &re) {
		u.fail(key, re.kind, re.msg)
		return
	}
	u.fail(key, remote.UploadErrOther, err.Error())
}

func (u *Uploader) fail(key, kind, msg string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	f := u.failures[key]
	f.count++
	f.kind, f.msg = kind, msg
	f.next = u.opt.Now().Add(Backoff(f.count))
	u.failures[key] = f
	u.pending[key] = true
	u.lastErr, u.lastErrAt = f, u.opt.Now()
	u.opt.Logf("upload failed: item=%s kind=%s attempt=%d %s", key, kind, f.count, msg)
}

func (u *Uploader) clearFailure(key string) {
	u.mu.Lock()
	delete(u.failures, key)
	u.mu.Unlock()
}

func (u *Uploader) markGone(key, rel string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	id := key + "\x00" + rel
	if !u.gone[id] {
		u.gone[id] = true
		u.opt.Logf("upload: %s of %s is gone before upload; not retried", rel, key)
	}
}

func (u *Uploader) clearGone(key, rel string) {
	u.mu.Lock()
	delete(u.gone, key+"\x00"+rel)
	u.mu.Unlock()
}

func (u *Uploader) isGone(key, rel string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.gone[key+"\x00"+rel]
}

// Status is the upload part of the remote's status.
func (u *Uploader) Status() remote.UploadStatus {
	st := remote.UploadStatus{Mode: u.cfg.Mode, Target: u.cfg.Remote, DeleteAfter: u.cfg.DeleteAfterUpload}
	u.mu.Lock()
	pendingKeys := make([]string, 0, len(u.pending))
	for k := range u.pending {
		if k != u.current {
			pendingKeys = append(pendingKeys, k)
		}
	}
	st.Uploading = u.current
	st.UploadingSize = u.curBytes
	st.GoneFiles = len(u.gone)
	if u.lastErr.msg != "" {
		st.LastError, st.LastErrorKind = u.lastErr.msg, u.lastErr.kind
		st.LastErrorAt = u.lastErrAt.UTC().Format(time.RFC3339)
	}
	var next time.Time
	for _, f := range u.failures {
		if next.IsZero() || f.next.Before(next) {
			next = f.next
		}
	}
	if !next.IsZero() {
		st.NextRetryAt = next.UTC().Format(time.RFC3339)
	}
	u.mu.Unlock()

	if u.opt.Library != nil && u.opt.DB != nil {
		for _, k := range pendingKeys {
			if ok, b := u.inspectItemPending(k); ok {
				st.QueuedItems++
				st.QueuedBytes += b
			}
		}
	} else {
		st.QueuedItems = len(pendingKeys)
	}

	if u.opt.DB != nil {
		now := u.opt.Now()
		midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).UTC().Format(time.RFC3339)
		row := u.opt.DB.QueryRow(`SELECT COALESCE(SUM(bytes), 0), COUNT(*) FROM upload_files WHERE uploaded_at >= ? AND rel_path != 'item.json'`, midnight)
		_ = row.Scan(&st.TodayBytes, &st.TodayFiles)
	}
	return st
}

// Test uploads a small file to the configured remote and deletes it again
// (trace-server upload-test).
func (u *Uploader) Test(ctx context.Context) error {
	if u.cfg.Mode != remote.UploadRclone {
		return fmt.Errorf("upload.mode 不是 rclone，没有可测试的上传")
	}
	dir, err := os.MkdirTemp(u.tempDir(), "test-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	name := fmt.Sprintf("trace-upload-test-%d.txt", u.opt.Now().UnixNano())
	if err := os.WriteFile(filepath.Join(dir, name), []byte("TRACE upload test\n"), 0o600); err != nil {
		return err
	}
	if err := u.run(ctx, append([]string{"copy", dir, u.cfg.Remote}, u.commonArgs()...)); err != nil {
		return err
	}
	return u.run(ctx, append([]string{"deletefile", remoteJoin(u.cfg.Remote, name)}, u.commonArgs()...))
}
