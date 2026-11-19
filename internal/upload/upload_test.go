package upload

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/remote"
	"github.com/chialecode/trace/internal/storage"
	"github.com/chialecode/trace/internal/upload/fakerclone"
)

func TestMain(m *testing.M) {
	if fakerclone.Enabled() {
		os.Exit(fakerclone.Main(os.Args[1:]))
	}
	os.Exit(m.Run())
}

type env struct {
	t       *testing.T
	dir     string
	export  string
	cloud   string
	logPath string
	db      *sql.DB
	lib     *library.Store
	busy    atomic.Bool
	mode    string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	e := &env{t: t, dir: dir, export: filepath.Join(dir, "export"), cloud: filepath.Join(dir, "cloud"), logPath: filepath.Join(dir, "rclone.log")}
	db, _, _, err := storage.OpenSQLite(filepath.Join(dir, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	lib, err := library.OpenWithDataDir(db, e.export, dir)
	if err != nil {
		t.Fatal(err)
	}
	e.db, e.lib = db, lib
	return e
}

func (e *env) uploader(deleteAfter bool, rclone string) *Uploader {
	e.t.Helper()
	if rclone == "" {
		rclone = os.Args[0]
	}
	u, err := New(Options{
		Config:  Config{Mode: remote.UploadRclone, Remote: "fake:trace", Rclone: rclone, DeleteAfterUpload: deleteAfter},
		Library: e.lib, DB: e.db, DataDir: e.dir,
		Busy: func(string) bool { return e.busy.Load() },
		Env:  append(os.Environ(), "TRACE_FAKE_RCLONE=1", "TRACE_FAKE_RCLONE_ROOT="+e.cloud, "TRACE_FAKE_RCLONE_LOG="+e.logPath, "TRACE_FAKE_RCLONE_MODE="+e.mode),
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return u
}

func sum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func (e *env) video(id string) library.ItemMetadata {
	e.t.Helper()
	itemDir := filepath.Join(e.export, "bilibili", "1001_UP", "videos", "video_"+id)
	if err := os.MkdirAll(itemDir, 0o700); err != nil {
		e.t.Fatal(err)
	}
	video := []byte("video-bytes-" + id)
	cover := []byte("cover-bytes-" + id)
	_ = os.WriteFile(filepath.Join(itemDir, "P01.mp4"), video, 0o600)
	_ = os.WriteFile(filepath.Join(itemDir, "cover.jpg"), cover, 0o600)
	// An unfinished download next to it must never be uploaded.
	_ = os.WriteFile(filepath.Join(itemDir, "P02.mp4.part"), []byte("half"), 0o600)
	meta, err := e.lib.SaveItem(itemDir, library.ItemMetadata{ID: id, Site: "bilibili", Kind: "video", Title: "t", Author: library.ItemAuthor{Mid: 1001, Name: "UP"},
		Media: []library.MediaFile{{Role: "video", Path: "P01.mp4", Bytes: int64(len(video)), SHA256: sum(video)}, {Role: "cover", Path: "cover.jpg", Bytes: int64(len(cover)), SHA256: sum(cover)}}})
	if err != nil {
		e.t.Fatal(err)
	}
	return meta
}

func (e *env) calls() []string {
	raw, _ := os.ReadFile(e.logPath)
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// Media before item.json; delete after; .part never listed.
func TestUploadOrderDeleteAndNoPartFiles(t *testing.T) {
	e := newEnv(t)
	meta := e.video("BV1aa")
	u := e.uploader(true, "")
	if again := u.process(context.Background(), meta.ItemKey); again {
		t.Fatal("finished item should be done in one pass")
	}
	calls := e.calls()
	if len(calls) != 2 {
		t.Fatalf("calls = %v", calls)
	}
	if !strings.Contains(calls[0], "P01.mp4") || !strings.Contains(calls[0], "cover.jpg") || strings.Contains(calls[0], "item.json") {
		t.Fatalf("first call should carry only the media: %s", calls[0])
	}
	if !strings.HasSuffix(calls[1], " item.json") {
		t.Fatalf("item.json must be uploaded last: %s", calls[1])
	}
	for _, c := range calls {
		if strings.Contains(c, ".part") {
			t.Fatalf("unfinished file listed: %s", c)
		}
	}
	rel := filepath.Join("bilibili", "1001_UP", "videos", "video_BV1aa")
	for _, n := range []string{"P01.mp4", "cover.jpg", "item.json"} {
		if !exists(filepath.Join(e.cloud, "trace", rel, n)) {
			t.Fatalf("%s not in the cloud", n)
		}
		if exists(filepath.Join(meta.ItemDir, n)) {
			t.Fatalf("%s should be deleted after upload", n)
		}
	}
	// The user's .part file stays, so the folder stays too.
	if !exists(filepath.Join(meta.ItemDir, "P02.mp4.part")) {
		t.Fatal(".part file must never be deleted")
	}
	// The index keeps the item (the remote decides "cached" from it).
	if _, ok, _ := e.lib.GetItem(meta.ItemKey); !ok {
		t.Fatal("index entry removed")
	}
	// Nothing is uploaded twice.
	u.process(context.Background(), meta.ItemKey)
	if n := len(e.calls()); n != 2 {
		t.Fatalf("re-upload after done: %v", e.calls())
	}
}

// A recording only uploads finished segments, deletes them, and
// keeps item.json and the cover until the session is final.
func TestUploadRecordingSegmentsThenFinal(t *testing.T) {
	e := newEnv(t)
	itemDir := filepath.Join(e.export, "bilibili", "1001_UP", "lives", "live_1")
	_ = os.MkdirAll(itemDir, 0o700)
	seg1 := []byte("segment-one")
	cover := []byte("cover")
	_ = os.WriteFile(filepath.Join(itemDir, "part-001.mp4"), seg1, 0o600)
	_ = os.WriteFile(filepath.Join(itemDir, "cover.jpg"), cover, 0o600)
	_ = os.WriteFile(filepath.Join(itemDir, "part-002.flv.part"), []byte("recording"), 0o600)
	live := &library.LiveRecordingInfo{RoomID: 7, Segments: []library.LiveSegmentInfo{{File: "part-001.mp4", Bytes: int64(len(seg1)), SHA256: sum(seg1)}}}
	meta, err := e.lib.SaveItem(itemDir, library.ItemMetadata{ID: "1", Site: "bilibili", Kind: "live", Status: "recording", Title: "live", Author: library.ItemAuthor{Mid: 1001, Name: "UP"},
		Media: []library.MediaFile{{Role: "live_segment", Path: "part-001.mp4", Bytes: int64(len(seg1)), SHA256: sum(seg1)}, {Role: "cover", Path: "cover.jpg", Bytes: int64(len(cover))}}, Live: live})
	if err != nil {
		t.Fatal(err)
	}
	e.busy.Store(true)
	u := e.uploader(true, "")
	u.process(context.Background(), meta.ItemKey)
	if exists(filepath.Join(itemDir, "part-001.mp4")) {
		t.Fatal("uploaded segment should be deleted")
	}
	if !exists(filepath.Join(itemDir, "item.json")) || !exists(filepath.Join(itemDir, "cover.jpg")) {
		t.Fatal("item.json and cover stay while recording")
	}
	calls := e.calls()
	if len(calls) != 2 || !strings.Contains(calls[0], "part-001.mp4") || strings.Contains(calls[0], "cover.jpg") || !strings.HasSuffix(calls[1], "item.json") {
		t.Fatalf("calls = %v", calls)
	}
	// Second segment finishes; the manifest lists both (the first is gone locally).
	seg2 := []byte("segment-two")
	_ = os.Remove(filepath.Join(itemDir, "part-002.flv.part"))
	_ = os.WriteFile(filepath.Join(itemDir, "part-002.mp4"), seg2, 0o600)
	meta.Live.Segments = append(meta.Live.Segments, library.LiveSegmentInfo{File: "part-002.mp4", Bytes: int64(len(seg2)), SHA256: sum(seg2)})
	meta.Media = append(meta.Media, library.MediaFile{Role: "live_segment", Path: "part-002.mp4", Bytes: int64(len(seg2)), SHA256: sum(seg2)})
	meta.Status = "complete"
	if _, err := e.lib.SaveItem(itemDir, meta); err != nil {
		t.Fatal(err)
	}
	e.busy.Store(false)
	u.process(context.Background(), meta.ItemKey)
	cloudDir := filepath.Join(e.cloud, "trace", "bilibili", "1001_UP", "lives", "live_1")
	raw, err := os.ReadFile(filepath.Join(cloudDir, "item.json"))
	if err != nil {
		t.Fatal(err)
	}
	up, _ := library.ParseItemMetadata(raw)
	if len(up.Live.Segments) != 2 {
		t.Fatalf("cloud item.json should list both segments: %+v", up.Live.Segments)
	}
	for _, n := range []string{"part-001.mp4", "part-002.mp4", "cover.jpg"} {
		if !exists(filepath.Join(cloudDir, n)) {
			t.Fatalf("%s missing in the cloud", n)
		}
	}
	if exists(itemDir) {
		t.Fatal("the emptied item folder should be removed")
	}
}

// A cache task still writing an item keeps it untouched.
func TestUploadWaitsForBusyDownload(t *testing.T) {
	e := newEnv(t)
	meta := e.video("BV1bb")
	e.busy.Store(true)
	u := e.uploader(true, "")
	if again := u.process(context.Background(), meta.ItemKey); !again {
		t.Fatal("busy item should be retried later")
	}
	if len(e.calls()) != 0 || !exists(filepath.Join(meta.ItemDir, "P01.mp4")) {
		t.Fatal("busy item must not be uploaded or deleted")
	}
}

// Classification, backoff, gone files, deleteAfterUpload off.
func TestUploadFailuresAndKeepFiles(t *testing.T) {
	e := newEnv(t)
	meta := e.video("BV1cc")
	now := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	missing := e.uploader(true, filepath.Join(e.dir, "no-such-rclone"))
	missing.opt.Now = func() time.Time { return now }
	missing.process(context.Background(), meta.ItemKey)
	if st := missing.Status(); st.LastErrorKind != remote.UploadErrNotFound || st.NextRetryAt != now.Add(time.Minute).Format(time.RFC3339) {
		t.Fatalf("missing rclone: %+v", st)
	}
	if !exists(filepath.Join(meta.ItemDir, "P01.mp4")) {
		t.Fatal("nothing may be deleted after a failure")
	}
	for _, c := range []struct{ mode, kind string }{{"quota", remote.UploadErrQuota}, {"config", remote.UploadErrConfig}, {"fail", remote.UploadErrOther}} {
		e.mode = c.mode
		u := e.uploader(true, "")
		u.process(context.Background(), meta.ItemKey)
		st := u.Status()
		if st.LastErrorKind != c.kind {
			t.Fatalf("mode %s: kind %q, want %q (%s)", c.mode, st.LastErrorKind, c.kind, st.LastError)
		}
		if strings.Contains(st.LastError, "googleapi") && strings.Contains(st.LastError, "userRateLimitExceeded") {
			// kept as one sanitized line, fine
		}
		if !exists(filepath.Join(meta.ItemDir, "P01.mp4")) {
			t.Fatal("nothing may be deleted after a failure")
		}
	}
	if Backoff(1) != time.Minute || Backoff(2) != 5*time.Minute || Backoff(3) != 15*time.Minute || Backoff(4) != time.Hour || Backoff(9) != time.Hour {
		t.Fatal("backoff schedule")
	}

	// deleteAfterUpload off: uploaded, nothing deleted.
	e.mode = ""
	keep := e.uploader(false, "")
	keep.process(context.Background(), meta.ItemKey)
	for _, n := range []string{"P01.mp4", "cover.jpg", "item.json"} {
		if !exists(filepath.Join(meta.ItemDir, n)) {
			t.Fatalf("%s deleted with deleteAfterUpload off", n)
		}
	}

	// A file removed before upload is reported once and not retried.
	other := e.video("BV1dd")
	_ = os.Remove(filepath.Join(other.ItemDir, "P01.mp4"))
	g := e.uploader(true, "")
	g.process(context.Background(), other.ItemKey)
	g.process(context.Background(), other.ItemKey)
	if st := g.Status(); st.GoneFiles != 1 {
		t.Fatalf("gone files = %d", st.GoneFiles)
	}
}

// After a restart only what was not uploaded goes up.
func TestUploadResumesAfterRestart(t *testing.T) {
	e := newEnv(t)
	a := e.video("BV1ee")
	b := e.video("BV1ff")
	u := e.uploader(false, "")
	u.process(context.Background(), a.ItemKey)
	before := len(e.calls())
	// "Restart": a new uploader on the same database scans every item.
	u2 := e.uploader(false, "")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { u2.Run(ctx); close(done) }()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if len(e.calls()) >= before+2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	calls := e.calls()[before:]
	if len(calls) != 2 {
		t.Fatalf("calls after restart = %v", calls)
	}
	for _, c := range calls {
		if strings.Contains(c, "BV1ee") {
			t.Fatalf("already uploaded item uploaded again: %s", c)
		}
		if !strings.Contains(c, "BV1ff") {
			t.Fatalf("unexpected call %s", c)
		}
	}
	_ = b
}

func TestSanitizeKeepsFirstErrorLineWithoutSecrets(t *testing.T) {
	in := "2026/11/19 NOTICE: x\n2026/11/19 ERROR : f: Failed: Post \"https://alice:s3cr3tPass@www.googleapis.com/upload/drive/v3/files?alt=json&uploadType=resumable&upload_id=ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789\": token expired ya29.a0AfH6SMBxxxxxxxxxxxxxxxxxxxxxxxxxxxx\nnext"
	out := Sanitize(in)
	if strings.Contains(out, "alice") || strings.Contains(out, "s3cr3tPass") || strings.Contains(out, "upload_id") || strings.Contains(out, "ya29") || strings.Contains(out, "next") || !strings.Contains(out, "ERROR") {
		t.Fatalf("sanitize = %q", out)
	}
}

func TestRemoteJoinHandlesBareColonRemote(t *testing.T) {
	if got := remoteJoin("gdrive:", "bilibili/1001/videos/v1"); got != "gdrive:bilibili/1001/videos/v1" {
		t.Fatalf("remoteJoin bare colon = %q", got)
	}
	if got := remoteJoin("gdrive:trace", "bilibili/1001/videos/v1"); got != "gdrive:trace/bilibili/1001/videos/v1" {
		t.Fatalf("remoteJoin path = %q", got)
	}
}

func TestWriteFilesFromFailsOnUnwritableDir(t *testing.T) {
	// Passing a file path as temp dir makes os.CreateTemp fail cleanly before rclone runs.
	f := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := writeFilesFrom(f, []string{"P01.mp4"}); err == nil {
		t.Fatal("expected writeFilesFrom error on non-directory")
	}
}

func TestDrainDoesNotSpinOnBusyOrCorruptItem(t *testing.T) {
	e := newEnv(t)
	busyItem := e.video("BV1busy")
	corruptItem := e.video("BV1corrupt")
	readyItem := e.video("BV1ready")
	if err := os.WriteFile(filepath.Join(corruptItem.ItemDir, "item.json"), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	var busyChecks atomic.Int32
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	u, err := New(Options{
		Config:  Config{Mode: remote.UploadRclone, Remote: "fake:trace", Rclone: os.Args[0], DeleteAfterUpload: true},
		Library: e.lib, DB: e.db, DataDir: e.dir,
		Now: func() time.Time { return now },
		Busy: func(k string) bool {
			if k == busyItem.ItemKey {
				busyChecks.Add(1)
				return true
			}
			return false
		},
		Env: append(os.Environ(), "TRACE_FAKE_RCLONE=1", "TRACE_FAKE_RCLONE_ROOT="+e.cloud, "TRACE_FAKE_RCLONE_LOG="+e.logPath),
	})
	if err != nil {
		t.Fatal(err)
	}
	u.Notify(busyItem)
	u.Notify(corruptItem)
	u.Notify(readyItem)
	// drain must finish promptly without spinning on busyItem or corruptItem,
	// and must still process readyItem!
	u.drain(context.Background())
	if got := busyChecks.Load(); got != 1 {
		t.Fatalf("busy item checked %d times in one drain pass, want 1", got)
	}
	if exists(filepath.Join(readyItem.ItemDir, "P01.mp4")) {
		t.Fatal("readyItem was starved by busyItem or corruptItem")
	}
	// corruptItem must keep its failure backoff instead of clearing it.
	u.mu.Lock()
	f, hasFail := u.failures[corruptItem.ItemKey]
	u.mu.Unlock()
	if !hasFail || !f.next.After(now) {
		t.Fatalf("corrupt item failure = (%+v, %v), want backoff after %v", f, hasFail, now)
	}
}

func TestStatusReportsQueuedBytesAndHandlesNilDB(t *testing.T) {
	uNil, err := New(Options{Config: Config{Mode: remote.UploadNone}})
	if err != nil {
		t.Fatal(err)
	}
	_ = uNil.Status() // must not panic with nil DB

	e := newEnv(t)
	meta := e.video("BV1bytes")
	u := e.uploader(true, "")
	u.Notify(meta)
	st := u.Status()
	if st.QueuedItems != 1 || st.QueuedBytes <= 0 {
		t.Fatalf("queued status = %+v, want QueuedItems=1 and QueuedBytes>0", st)
	}
	u.process(context.Background(), meta.ItemKey)
	u.queueAll()
	st2 := u.Status()
	if st2.QueuedItems != 0 || st2.QueuedBytes != 0 {
		t.Fatalf("after upload + queueAll status = %+v, want 0 queued", st2)
	}
}

func TestUpdatedMediaUploadedBeforeNewItemJSON(t *testing.T) {
	e := newEnv(t)
	meta := e.video("BV1upg")
	u := e.uploader(false, "")
	u.process(context.Background(), meta.ItemKey)
	before := len(e.calls())
	// Upgrade P01.mp4 with new content & bytes and re-save item.json.
	newVideo := []byte("upgraded-higher-quality-video-bytes")
	if err := os.WriteFile(filepath.Join(meta.ItemDir, "P01.mp4"), newVideo, 0o600); err != nil {
		t.Fatal(err)
	}
	meta.Media[0].Bytes = int64(len(newVideo))
	meta.Media[0].SHA256 = sum(newVideo)
	if _, err := e.lib.SaveItem(meta.ItemDir, meta); err != nil {
		t.Fatal(err)
	}
	u.process(context.Background(), meta.ItemKey)
	calls := e.calls()[before:]
	if len(calls) != 2 || !strings.Contains(calls[0], "P01.mp4") || !strings.HasSuffix(calls[1], "item.json") {
		t.Fatalf("upgraded calls = %v, want P01.mp4 then item.json", calls)
	}
}
