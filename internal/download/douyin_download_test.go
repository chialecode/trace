package download

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/settings"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/storage"
)

type fakeDouyinWorkPlanner struct {
	mu       sync.Mutex
	plans    map[string]func(call int) (WorkDownloadPlan, error)
	calls    map[string]int
	authErr  error
	reported []error
}

func newFakeDouyinWorkPlanner() *fakeDouyinWorkPlanner {
	return &fakeDouyinWorkPlanner{
		plans: make(map[string]func(call int) (WorkDownloadPlan, error)),
		calls: make(map[string]int),
	}
}

func (p *fakeDouyinWorkPlanner) SiteID() site.ID { return site.Douyin }

func (p *fakeDouyinWorkPlanner) RequireAuth(_ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.authErr
}

func (p *fakeDouyinWorkPlanner) ReportAutomaticError(_ context.Context, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reported = append(p.reported, err)
}

func (p *fakeDouyinWorkPlanner) setPlan(targetID string, fn func(call int) (WorkDownloadPlan, error)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.plans[targetID] = fn
}

func (p *fakeDouyinWorkPlanner) callCount(targetID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[targetID]
}

func (p *fakeDouyinWorkPlanner) PlanWorkDownload(_ context.Context, targetID string, _ WorkPlanOptions) (WorkDownloadPlan, error) {
	p.mu.Lock()
	p.calls[targetID]++
	call := p.calls[targetID]
	fn := p.plans[targetID]
	p.mu.Unlock()
	if fn == nil {
		return WorkDownloadPlan{}, &site.Error{Site: site.Douyin, Kind: site.ErrKindUnavailable, Message: "作品不存在"}
	}
	return fn(call)
}

func syntheticJPEGMini() []byte {
	return []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0x01, 0x01, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0xFF, 0xD9}
}

func syntheticPNGMini() []byte {
	return []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n', 0x00, 0x00, 0x00, 0x0D, 'I', 'H', 'D', 'R', 0x00, 0x00, 0x00, 0x01}
}

func syntheticWEBPMini() []byte {
	return []byte{'R', 'I', 'F', 'F', 0x14, 0x00, 0x00, 0x00, 'W', 'E', 'B', 'P', 'V', 'P', '8', ' ', 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
}

func syntheticMP3Mini() []byte {
	return append([]byte{'I', 'D', '3', 0x04, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}, bytes.Repeat([]byte{0xFF, 0xFB, 0x90, 0x64}, 16)...)
}

func setupDouyinDownloadTestEnv(t *testing.T, handler http.Handler, maxConc int) (*Manager, *library.Store, *httptest.Server, string, *bytes.Buffer) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "library.db")
	db, _, _, err := storage.OpenSQLite(dbPath)
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
	_, _ = credStore.SaveManual("SESSDATA=bili_sess_secret; bili_jct=bili_csrf_secret; DedeUserID=1001", "rt_secret")

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

	var eventMu sync.Mutex
	var eventBuf bytes.Buffer

	dlCfg := settings.Default(cacheDir).Download
	dlCfg.MaxConcurrentTasks = maxConc
	mgr, err := NewManager(db, libStore, client, dlCfg, func(tk Task) {
		b, _ := json.Marshal(tk)
		eventMu.Lock()
		eventBuf.Write(b)
		eventBuf.WriteByte('\n')
		eventMu.Unlock()
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	mgr.SetBackoffScale(2 * time.Millisecond)
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("mgr.Start: %v", err)
	}
	t.Cleanup(mgr.Stop)
	return mgr, libStore, srv, dbPath, &eventBuf
}

func serveRangeBytes(w http.ResponseWriter, r *http.Request, payload []byte, contentType string, cutFirstAt int, firstHit *atomic.Bool) {
	w.Header().Set("Content-Type", contentType)
	rh := r.Header.Get("Range")
	start := 0
	end := len(payload) - 1
	hasRange := false
	if strings.HasPrefix(rh, "bytes=") {
		parts := strings.SplitN(strings.TrimPrefix(rh, "bytes="), "-", 2)
		if len(parts) == 2 {
			if s, err := strconv.Atoi(parts[0]); err == nil && s >= 0 && s < len(payload) {
				start = s
				hasRange = true
			}
			if e, err := strconv.Atoi(parts[1]); err == nil && e >= start && e < len(payload) {
				end = e
			}
		}
	}
	slice := payload[start : end+1]
	if firstHit != nil && cutFirstAt > 0 && cutFirstAt < len(slice) && firstHit.CompareAndSwap(true, false) {
		if hasRange {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
			w.Header().Set("Content-Length", strconv.Itoa(len(slice)))
			w.WriteHeader(http.StatusPartialContent)
		} else {
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.WriteHeader(http.StatusOK)
		}
		_, _ = w.Write(slice[:cutFirstAt])
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, hErr := hj.Hijack(); hErr == nil {
				_ = conn.Close()
				return
			}
		}
		return
	}
	if hasRange {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
		w.Header().Set("Content-Length", strconv.Itoa(len(slice)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(slice)
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
}

func TestDouyinVideoCacheRangeRefreshAndSecretScrub(t *testing.T) {
	const (
		secUID     = "MS4wLjABAAAA_test_author_sec_uid_001"
		awemeOK    = "7300000000000000001"
		awemeExp2x = "7300000000000000002"
		awemeHTML  = "7300000000000000003"
		awemeRedir = "7300000000000000004"
		awemeDur   = "7300000000000000005"
		awemeNoVid = "7300000000000000006"
		sigQuery   = "?x-expires=1999999999&a_bogus=SECRET_BOGUS_SENTINEL&token=SECRET_SIG_SENTINEL"
	)

	// Synthetic mdat-before-moov MP4 without elst (~2.0s).
	videoMP4 := buildProgressiveWithoutElst(t, true, true, 0)
	coverWEBP := syntheticWEBPMini()

	aM4S, err := BuildSyntheticAudioM4S(90)
	if err != nil {
		t.Fatalf("BuildSyntheticAudioM4S: %v", err)
	}
	audioOnlyPath := filepath.Join(t.TempDir(), "audio-only.mp4")
	if _, err := DefragmentFMP4Bytes(aM4S, audioOnlyPath); err != nil {
		t.Fatalf("DefragmentFMP4Bytes: %v", err)
	}
	audioOnlyMP4, err := os.ReadFile(audioOnlyPath)
	if err != nil {
		t.Fatalf("ReadFile audioOnly: %v", err)
	}

	var cutOnce atomic.Bool
	cutOnce.Store(true)
	var sawResumeRange atomic.Bool
	var sawCookieLeak atomic.Bool
	var sawBadReferer atomic.Bool

	mux := http.NewServeMux()
	checkCDNHeaders := func(r *http.Request) {
		if r.Header.Get("Cookie") != "" {
			sawCookieLeak.Store(true)
		}
		if r.Header.Get("Referer") != "https://www.douyin.com/" {
			sawBadReferer.Store(true)
		}
	}

	mux.HandleFunc("/cdn/cover.image", func(w http.ResponseWriter, r *http.Request) {
		checkCDNHeaders(r)
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(coverWEBP)
	})
	mux.HandleFunc("/cdn/video-expired.mp4", func(w http.ResponseWriter, r *http.Request) {
		checkCDNHeaders(r)
		w.WriteHeader(http.StatusForbidden)
	})
	mux.HandleFunc("/cdn/video-fresh.mp4", func(w http.ResponseWriter, r *http.Request) {
		checkCDNHeaders(r)
		if strings.HasPrefix(r.Header.Get("Range"), "bytes=") && !strings.HasPrefix(r.Header.Get("Range"), "bytes=0-") {
			sawResumeRange.Store(true)
		}
		serveRangeBytes(w, r, videoMP4, "video/mp4", len(videoMP4)/3, &cutOnce)
	})
	mux.HandleFunc("/cdn/disguised-html.mp4", func(w http.ResponseWriter, r *http.Request) {
		checkCDNHeaders(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<html><body>verify challenge</body></html>"))
	})
	mux.HandleFunc("/cdn/evil-redirect.mp4", func(w http.ResponseWriter, r *http.Request) {
		checkCDNHeaders(r)
		w.Header().Set("Location", "https://evil.example.invalid/steal.mp4")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/cdn/audio-only.mp4", func(w http.ResponseWriter, r *http.Request) {
		checkCDNHeaders(r)
		serveRangeBytes(w, r, audioOnlyMP4, "video/mp4", 0, nil)
	})

	mgr, libStore, srv, dbPath, logBuf := setupDouyinDownloadTestEnv(t, mux, 1)
	planner := newFakeDouyinWorkPlanner()
	mgr.RegisterWorkPlanner(planner)

	transport := site.DouyinVODTransport("Mozilla/5.0 Test", true)

	// 1. Happy path: initial URL is expired (403) -> refreshes once -> disconnects mid-file -> resumes via 206 Range -> relocates moov before mdat!
	planner.setPlan(awemeOK, func(call int) (WorkDownloadPlan, error) {
		vURL := srv.URL + "/cdn/video-expired.mp4" + sigQuery
		if call >= 2 {
			vURL = srv.URL + "/cdn/video-fresh.mp4" + sigQuery
		}
		return WorkDownloadPlan{
			Site:           site.Douyin,
			Kind:           "video",
			TargetID:       awemeOK,
			Title:          "抖音单文件视频测试",
			Desc:           "测试无 elst 单文件 MP4 搬移 moov 与断点续传",
			CanonicalURL:   "https://www.douyin.com/video/" + awemeOK + sigQuery,
			PubTs:          1700001000,
			DurationSec:    2,
			AuthorUID:      secUID,
			AuthorUniqueID: "dy_creator_01",
			AuthorName:     "测试创作者",
			AuthorFace:     srv.URL + "/cdn/cover.image" + sigQuery,
			CoverURLs:      []string{srv.URL + "/cdn/cover.image" + sigQuery},
			VideoURLs:      []string{vURL},
			VideoGear:      "1080p",
			QualityName:    "1080P",
			Codec:          "h264",
			Width:          640,
			Height:         360,
			Transport:      transport,
		}, nil
	})

	taskOK, err := mgr.EnqueueDouyinWork(context.Background(), DouyinWorkEnqueueRequest{AwemeID: awemeOK})
	if err != nil {
		t.Fatalf("EnqueueDouyinWork(awemeOK): %v", err)
	}
	doneOK := waitForTaskStatus(t, mgr, taskOK.TaskID, "completed", 8*time.Second)
	if sawCookieLeak.Load() {
		t.Fatal("CDN media request leaked Cookie header")
	}
	if sawBadReferer.Load() {
		t.Fatal("CDN media request missing expected Douyin Referer header")
	}
	if !sawResumeRange.Load() {
		t.Fatal("expected mid-file disconnect to resume with non-zero Range request")
	}
	// Call 1 was EnqueueDouyinWork, Call 2 was the single 403 detail refresh.
	if got := planner.callCount(awemeOK); got != 2 {
		t.Fatalf("expected exactly 2 PlanWorkDownload calls (enqueue + 1 refresh), got %d", got)
	}

	wantDirSuffix := filepath.Join("douyin", site.DouyinAuthorDir(secUID), "works", awemeOK)
	if !strings.HasSuffix(doneOK.ItemDir, wantDirSuffix) {
		t.Fatalf("unexpected ItemDir %q, want suffix %q", doneOK.ItemDir, wantDirSuffix)
	}
	mp4Summary, err := InspectMP4File(filepath.Join(doneOK.ItemDir, "video.mp4"))
	if err != nil {
		t.Fatalf("InspectMP4File(video.mp4): %v", err)
	}
	if !mp4Summary.MoovBeforeMdat || mp4Summary.HasElst {
		t.Fatalf("expected relocated moov-before-mdat without elst, got %+v", mp4Summary)
	}
	if _, err := os.Stat(filepath.Join(doneOK.ItemDir, "cover.webp")); err != nil {
		t.Fatalf("expected sniffed cover.webp to exist: %v", err)
	}
	itemMeta, ok, err := libStore.GetItem("douyin:video:" + awemeOK)
	if err != nil || !ok || itemMeta.Status != "complete" {
		t.Fatalf("expected complete douyin:video item, got ok=%v err=%v meta=%+v", ok, err, itemMeta)
	}

	// 2. Expired URL returning 403 even after 1 refresh must NOT loop; refreshes at most once then fails.
	planner.setPlan(awemeExp2x, func(_ int) (WorkDownloadPlan, error) {
		return WorkDownloadPlan{
			Site:        site.Douyin,
			Kind:        "video",
			TargetID:    awemeExp2x,
			Title:       "持续 403 测试",
			DurationSec: 2,
			AuthorUID:   secUID,
			AuthorName:  "测试创作者",
			VideoURLs:   []string{srv.URL + "/cdn/video-expired.mp4" + sigQuery},
			Transport:   transport,
		}, nil
	})
	tExp, err := mgr.EnqueueDouyinWork(context.Background(), DouyinWorkEnqueueRequest{AwemeID: awemeExp2x})
	if err != nil {
		t.Fatalf("EnqueueDouyinWork(awemeExp2x): %v", err)
	}
	waitForTaskStatus(t, mgr, tExp.TaskID, "failed", 5*time.Second)
	if got := planner.callCount(awemeExp2x); got != 2 {
		t.Fatalf("expected at most 1 refresh on persistent 403 (total 2 calls), got %d", got)
	}

	// 3. Disguised 200 text/html body rejected, non-allowlist redirect rejected, >2s duration diff & audio-only MP4 rejected.
	for _, tc := range []struct {
		id          string
		urlPath     string
		durationSec int
	}{
		{id: awemeHTML, urlPath: "/cdn/disguised-html.mp4", durationSec: 2},
		{id: awemeRedir, urlPath: "/cdn/evil-redirect.mp4", durationSec: 2},
		{id: awemeDur, urlPath: "/cdn/video-fresh.mp4", durationSec: 9}, // |2.0 - 9.0| > 2.0s
		{id: awemeNoVid, urlPath: "/cdn/audio-only.mp4", durationSec: 2},
	} {
		planner.setPlan(tc.id, func(_ int) (WorkDownloadPlan, error) {
			return WorkDownloadPlan{
				Site:        site.Douyin,
				Kind:        "video",
				TargetID:    tc.id,
				Title:       "异常拒绝测试 " + tc.id,
				DurationSec: tc.durationSec,
				AuthorUID:   secUID,
				AuthorName:  "测试创作者",
				VideoURLs:   []string{srv.URL + tc.urlPath + sigQuery},
				Transport:   transport,
			}, nil
		})
		badTask, err := mgr.EnqueueDouyinWork(context.Background(), DouyinWorkEnqueueRequest{AwemeID: tc.id})
		if err != nil {
			t.Fatalf("EnqueueDouyinWork(%s): %v", tc.id, err)
		}
		waitForTaskStatus(t, mgr, badTask.TaskID, "failed", 5*time.Second)
		badDetail, _, _ := mgr.GetTask(badTask.TaskID)
		if badDetail.ItemDir != "" {
			if _, statErr := os.Stat(filepath.Join(badDetail.ItemDir, "video.mp4")); statErr == nil {
				t.Fatalf("invalid media for %s must never be promoted to video.mp4", tc.id)
			}
		}
	}

	// 4. Zero-secret sentinel scan across item.json, TaskDetail JSON, SQLite DB + WAL bytes, and logs!
	sentinels := []string{
		"SECRET_BOGUS_SENTINEL",
		"SECRET_SIG_SENTINEL",
		"x-expires",
		"bili_sess_secret",
		"bili_csrf_secret",
	}
	rawItemJSON, err := os.ReadFile(filepath.Join(doneOK.ItemDir, "item.json"))
	if err != nil {
		t.Fatalf("ReadFile item.json: %v", err)
	}
	td, _, _ := mgr.TaskDetail(doneOK.TaskID)
	for _, f := range td.Files {
		if strings.HasSuffix(f.Path, ".raw") || strings.HasSuffix(f.Path, ".imgtmp") || strings.HasSuffix(f.Path, ".audtmp") || strings.HasSuffix(f.Path, ".part") {
			t.Fatalf("stale staging entry %q left in TaskDetail.Files: %+v", f.Path, td.Files)
		}
	}
	tdJSON, _ := json.Marshal(td)
	dbBytes, _ := os.ReadFile(dbPath)
	walBytes, _ := os.ReadFile(dbPath + "-wal")

	for _, s := range sentinels {
		for label, blob := range map[string][]byte{
			"item.json":      rawItemJSON,
			"TaskDetail":     tdJSON,
			"library.db":     dbBytes,
			"library.db-wal": walBytes,
			"slog":           logBuf.Bytes(),
		} {
			if bytes.Contains(blob, []byte(s)) {
				t.Fatalf("secret/query sentinel %q leaked into %s", s, label)
			}
		}
	}
}

func TestDouyinPlainAndMixedNotesMusicAndUnknownPage(t *testing.T) {
	const (
		secUID   = "MS4wLjABAAAA_test_note_author_002"
		plainID  = "7310000000000000001"
		mixedID  = "7310000000000000002"
		sigQuery = "?x-expires=1999999999&token=NOTE_SIG_SENTINEL"
	)

	liveClipMP4 := buildProgressiveWithoutElst(t, false, true, 0)  // mdat-before-moov live photo motion
	pageVideoMP4 := buildProgressiveWithoutElst(t, true, false, 0) // progressive video page

	mux := http.NewServeMux()
	mux.HandleFunc("/cdn/img.jpg", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(syntheticJPEGMini())
	})
	mux.HandleFunc("/cdn/img.png", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(syntheticPNGMini())
	})
	mux.HandleFunc("/cdn/img.webp", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/webp")
		_, _ = w.Write(syntheticWEBPMini())
	})
	var failLiveOnce atomic.Bool
	failLiveOnce.Store(true)
	mux.HandleFunc("/cdn/live-motion.mp4", func(w http.ResponseWriter, r *http.Request) {
		if failLiveOnce.CompareAndSwap(true, false) {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		serveRangeBytes(w, r, liveClipMP4, "video/mp4", 0, nil)
	})
	mux.HandleFunc("/cdn/page-video.mp4", func(w http.ResponseWriter, r *http.Request) {
		serveRangeBytes(w, r, pageVideoMP4, "video/mp4", 0, nil)
	})
	mux.HandleFunc("/cdn/bgm.mp3", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write(syntheticMP3Mini())
	})

	mgr, libStore, srv, _, _ := setupDouyinDownloadTestEnv(t, mux, 2)
	planner := newFakeDouyinWorkPlanner()
	mgr.RegisterWorkPlanner(planner)
	transport := site.DouyinVODTransport("Mozilla/5.0 Test", true)

	// 1. Plain 2-image note + available BGM -> complete (green dot).
	planner.setPlan(plainID, func(_ int) (WorkDownloadPlan, error) {
		return WorkDownloadPlan{
			Site:         site.Douyin,
			Kind:         "note",
			TargetID:     plainID,
			Title:        "普通多图图文",
			Desc:         "两张图片加可用配乐",
			CanonicalURL: "https://www.douyin.com/note/" + plainID,
			PubTs:        1700002000,
			AuthorUID:    secUID,
			AuthorName:   "图文作者",
			Pages: []PlannedNotePage{
				{Index: 1, Kind: "image", Width: 1080, Height: 1440, ImageURLs: []string{srv.URL + "/cdn/img.jpg" + sigQuery}},
				{Index: 2, Kind: "image", Width: 1080, Height: 1440, ImageURLs: []string{srv.URL + "/cdn/img.png" + sigQuery}},
			},
			Music: &PlannedMusic{
				ID:          "bgm_01",
				Title:       "测试配乐",
				Author:      "测试歌手",
				DurationSec: 15,
				PlayURLs:    []string{srv.URL + "/cdn/bgm.mp3" + sigQuery},
			},
			Transport: transport,
		}, nil
	})

	tPlain, err := mgr.EnqueueDouyinWork(context.Background(), DouyinWorkEnqueueRequest{AwemeID: plainID})
	if err != nil {
		t.Fatalf("EnqueueDouyinWork(plain): %v", err)
	}
	donePlain := waitForTaskStatus(t, mgr, tPlain.TaskID, "completed", 5*time.Second)
	for _, rel := range []string{"images/01.jpg", "images/02.png", "music.mp3", "item.json"} {
		if _, err := os.Stat(filepath.Join(donePlain.ItemDir, rel)); err != nil {
			t.Fatalf("missing plain note asset %q: %v", rel, err)
		}
	}
	plainStates := libStore.States([]string{"douyin:dynamic:" + plainID})
	if plainStates["douyin:dynamic:"+plainID].Dot != "green" {
		t.Fatalf("expected green dot for complete plain note, got states=%+v", plainStates)
	}

	// 2. Mixed note: static PNG + live photo (JPG + relocated _live.mp4) + video page (WEBP + MP4) + unknown page + copyright-unavailable music -> partial (yellow dot)!
	planner.setPlan(mixedID, func(_ int) (WorkDownloadPlan, error) {
		return WorkDownloadPlan{
			Site:         site.Douyin,
			Kind:         "dynamic",
			TargetID:     mixedID,
			Title:        "实况与多图视频混排图文",
			Desc:         "含静图、实况图、视频页、未知页与无版权音乐",
			CanonicalURL: "https://www.douyin.com/note/" + mixedID,
			PubTs:        1700003000,
			AuthorUID:    secUID,
			AuthorName:   "图文作者",
			Pages: []PlannedNotePage{
				{Index: 1, Kind: "image", Width: 1080, Height: 1440, ImageURLs: []string{srv.URL + "/cdn/img.png" + sigQuery}},
				{
					Index:       2,
					Kind:        "live_photo",
					Width:       1080,
					Height:      1440,
					DurationSec: 2,
					ImageURLs:   []string{srv.URL + "/cdn/img.jpg" + sigQuery},
					VideoURLs:   []string{srv.URL + "/cdn/live-motion.mp4" + sigQuery},
				},
				{
					Index:       3,
					Kind:        "video",
					Width:       640,
					Height:      360,
					DurationSec: 2,
					QualityName: "720P",
					Codec:       "h264",
					ImageURLs:   []string{srv.URL + "/cdn/img.webp" + sigQuery},
					VideoURLs:   []string{srv.URL + "/cdn/page-video.mp4" + sigQuery},
				},
				{
					Index:         4,
					Kind:          "unknown",
					UnknownKind:   "clip_type_99",
					UnknownReason: "暂未识别的图文页类型 (clip_type=99)",
					ImageURLs:     []string{srv.URL + "/cdn/img.jpg" + sigQuery},
				},
			},
			Music: &PlannedMusic{
				ID:          "bgm_unavail_02",
				Title:       "版权受限配乐",
				Author:      "某歌手",
				DurationSec: 30,
				PlayURLs:    nil, // copyright unavailable!
			},
			Transport: transport,
		}, nil
	})

	tMixed, err := mgr.EnqueueDouyinWork(context.Background(), DouyinWorkEnqueueRequest{AwemeID: mixedID})
	if err != nil {
		t.Fatalf("EnqueueDouyinWork(mixed): %v", err)
	}
	doneMixed := waitForTaskStatus(t, mgr, tMixed.TaskID, "partial", 8*time.Second)

	for _, rel := range []string{
		"images/01.png",
		"images/02.jpg",
		"images/02_live.mp4",
		"images/03.webp",
		"images/03.mp4",
		"images/04.jpg",
		"item.json",
	} {
		if _, err := os.Stat(filepath.Join(doneMixed.ItemDir, rel)); err != nil {
			t.Fatalf("missing mixed note asset %q: %v", rel, err)
		}
	}
	liveSummary, err := InspectMP4File(filepath.Join(doneMixed.ItemDir, "images/02_live.mp4"))
	if err != nil || !liveSummary.MoovBeforeMdat {
		t.Fatalf("expected 02_live.mp4 relocated to moov-before-mdat, got summary=%+v err=%v", liveSummary, err)
	}
	pageVidSummary, err := InspectMP4File(filepath.Join(doneMixed.ItemDir, "images/03.mp4"))
	if err != nil || !pageVidSummary.MoovBeforeMdat {
		t.Fatalf("expected 03.mp4 validated with moov-before-mdat, got summary=%+v err=%v", pageVidSummary, err)
	}

	mixedMeta, ok, err := libStore.GetItem("douyin:dynamic:" + mixedID)
	if err != nil || !ok {
		t.Fatalf("GetItem mixed note: ok=%v err=%v", ok, err)
	}
	if mixedMeta.Status != "partial" || len(mixedMeta.NotePages) != 4 {
		t.Fatalf("unexpected mixed note metadata: %+v", mixedMeta)
	}
	if mixedMeta.NotePages[3].Kind != "unknown" || mixedMeta.NotePages[3].UnknownKind != "clip_type_99" {
		t.Fatalf("expected unknown page metadata preserved, got %+v", mixedMeta.NotePages[3])
	}
	hasMusicMissing := false
	for _, m := range mixedMeta.Missing {
		if m.Item == "music" {
			hasMusicMissing = true
		}
	}
	if !hasMusicMissing {
		t.Fatalf("expected missing record for copyright-unavailable music, got %+v", mixedMeta.Missing)
	}

	mixedStates := libStore.States([]string{"douyin:dynamic:" + mixedID})
	if mixedStates["douyin:dynamic:"+mixedID].Dot != "yellow" {
		t.Fatalf("expected yellow dot for partial mixed note, got states=%+v", mixedStates)
	}

	plainDetail, _, _ := mgr.TaskDetail(donePlain.TaskID)
	mixedDetail, _, _ := mgr.TaskDetail(doneMixed.TaskID)
	for _, f := range append(append([]TaskFileProgress{}, plainDetail.Files...), mixedDetail.Files...) {
		if strings.HasSuffix(f.Path, ".raw") || strings.HasSuffix(f.Path, ".imgtmp") || strings.HasSuffix(f.Path, ".audtmp") || strings.HasSuffix(f.Path, ".part") {
			t.Fatalf("stale staging entry %q left in note TaskDetail.Files", f.Path)
		}
	}
	sawSubdirAttempt := false
	for _, att := range mixedDetail.Attempts {
		if att.File == "images/02_live.mp4" {
			sawSubdirAttempt = true
		}
	}
	if !sawSubdirAttempt {
		t.Fatalf("expected attempt log to preserve subdirectory path images/02_live.mp4, got %+v", mixedDetail.Attempts)
	}
}

func TestCrossSiteDownloadCircuitBreakerAndPauseIsolation(t *testing.T) {
	const secUID = "MS4wLjABAAAA_test_isolation_author_003"
	videoMP4 := buildProgressiveWithoutElst(t, true, false, 0)

	biliGate := make(chan struct{})
	biliStarted := make(chan struct{}, 1)
	biliGate2 := make(chan struct{})
	biliStarted2 := make(chan struct{}, 1)
	dyHoldGate := make(chan struct{})
	dyHoldStarted := make(chan struct{}, 1)

	var srvURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/x/polymer/web-dynamic/v1/detail", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		imgPath := "/bili/dyn.jpg"
		if id == "990002" {
			imgPath = "/bili/dyn2.jpg"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"item": map[string]any{
					"id_str": id,
					"type":   "DYNAMIC_TYPE_DRAW",
					"modules": map[string]any{
						"module_author": map[string]any{"mid": 1001, "name": "BiliCreator", "pub_ts": 1700004000},
						"module_dynamic": map[string]any{
							"desc": map[string]any{"text": "B站动态跨站隔离测试 " + id},
							"major": map[string]any{
								"opus": map[string]any{
									"title": "Bili Draw " + id,
									"pics":  []map[string]any{{"url": srvURL + imgPath, "width": 100, "height": 100}},
								},
							},
						},
					},
				},
			},
		})
	})
	mux.HandleFunc("/bili/dyn.jpg", func(w http.ResponseWriter, _ *http.Request) {
		select {
		case biliStarted <- struct{}{}:
		default:
		}
		<-biliGate
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(syntheticJPEGMini())
	})
	mux.HandleFunc("/bili/dyn2.jpg", func(w http.ResponseWriter, _ *http.Request) {
		select {
		case biliStarted2 <- struct{}{}:
		default:
		}
		<-biliGate2
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(syntheticJPEGMini())
	})
	mux.HandleFunc("/cdn/risk412.mp4", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusPreconditionFailed) // 412 -> risk_control
	})
	mux.HandleFunc("/cdn/ok.mp4", func(w http.ResponseWriter, r *http.Request) {
		serveRangeBytes(w, r, videoMP4, "video/mp4", 0, nil)
	})
	mux.HandleFunc("/cdn/hold.mp4", func(w http.ResponseWriter, r *http.Request) {
		select {
		case dyHoldStarted <- struct{}{}:
		default:
		}
		select {
		case <-dyHoldGate:
		case <-r.Context().Done():
			return
		}
		serveRangeBytes(w, r, videoMP4, "video/mp4", 0, nil)
	})

	// MaxConcurrentTasks = 2 so Bilibili task + Douyin task 1 run concurrently while Douyin task 2 sits queued!
	mgr, _, srv, _, _ := setupDouyinDownloadTestEnv(t, mux, 2)
	srvURL = srv.URL
	planner := newFakeDouyinWorkPlanner()
	mgr.RegisterWorkPlanner(planner)
	transport := site.DouyinVODTransport("Mozilla/5.0 Test", true)

	// Enqueue Bilibili dynamic task (occupies slot 1).
	biliTask, err := mgr.EnqueueDynamic(context.Background(), DynamicEnqueueRequest{DynamicID: "990001"})
	if err != nil {
		t.Fatalf("EnqueueDynamic(bili): %v", err)
	}
	<-biliStarted

	// Enqueue Douyin risk task (occupies slot 2) and Douyin queued task (queued behind slots 1&2).
	planner.setPlan("7320000000000000001", func(_ int) (WorkDownloadPlan, error) {
		return WorkDownloadPlan{
			Site: site.Douyin, Kind: "video", TargetID: "7320000000000000001",
			Title: "触发风控作品", DurationSec: 2, AuthorUID: secUID, AuthorName: "DY",
			VideoURLs: []string{srv.URL + "/cdn/risk412.mp4"}, Transport: transport,
		}, nil
	})
	planner.setPlan("7320000000000000002", func(_ int) (WorkDownloadPlan, error) {
		return WorkDownloadPlan{
			Site: site.Douyin, Kind: "video", TargetID: "7320000000000000002",
			Title: "同站排队作品", DurationSec: 2, AuthorUID: secUID, AuthorName: "DY",
			VideoURLs: []string{srv.URL + "/cdn/ok.mp4"}, Transport: transport,
		}, nil
	})

	dyRisk, err := mgr.EnqueueDouyinWork(context.Background(), DouyinWorkEnqueueRequest{AwemeID: "7320000000000000001"})
	if err != nil {
		t.Fatalf("EnqueueDouyinWork(risk): %v", err)
	}
	dyQueued, err := mgr.EnqueueDouyinWork(context.Background(), DouyinWorkEnqueueRequest{AwemeID: "7320000000000000002"})
	if err != nil {
		t.Fatalf("EnqueueDouyinWork(queued): %v", err)
	}

	// Both Douyin tasks must pause with risk_control, while Bilibili task remains running and then completes!
	p1 := waitForTaskStatus(t, mgr, dyRisk.TaskID, "paused", 5*time.Second)
	p2 := waitForTaskStatus(t, mgr, dyQueued.TaskID, "paused", 5*time.Second)
	if p1.PauseReason != "risk_control" || p2.PauseReason != "risk_control" {
		t.Fatalf("expected both Douyin tasks paused with risk_control, got p1=%+v p2=%+v", p1, p2)
	}

	biliMid, _, _ := mgr.GetTask(biliTask.TaskID)
	if biliMid.Status == "paused" {
		t.Fatalf("Douyin circuit breaker must NOT pause active Bilibili task: %+v", biliMid)
	}
	close(biliGate)
	waitForTaskStatus(t, mgr, biliTask.TaskID, "completed", 5*time.Second)

	// Now verify PauseForSite(site.Douyin, "auth_required", ...) pauses running Douyin tasks without touching Bilibili!
	biliTask2, err := mgr.EnqueueDynamic(context.Background(), DynamicEnqueueRequest{DynamicID: "990002"})
	if err != nil {
		t.Fatalf("EnqueueDynamic(bili2): %v", err)
	}
	planner.setPlan("7320000000000000003", func(_ int) (WorkDownloadPlan, error) {
		return WorkDownloadPlan{
			Site: site.Douyin, Kind: "video", TargetID: "7320000000000000003",
			Title: "抖音运行中作品", DurationSec: 2, AuthorUID: secUID, AuthorName: "DY",
			VideoURLs: []string{srv.URL + "/cdn/hold.mp4"}, Transport: transport,
		}, nil
	})
	dyHoldTask, err := mgr.EnqueueDouyinWork(context.Background(), DouyinWorkEnqueueRequest{AwemeID: "7320000000000000003"})
	if err != nil {
		t.Fatalf("EnqueueDouyinWork(hold): %v", err)
	}
	<-biliStarted2
	<-dyHoldStarted

	mgr.PauseForSite(site.Douyin, "auth_required", "抖音登录已退出")
	close(dyHoldGate)

	dyPausedLogout := waitForTaskStatus(t, mgr, dyHoldTask.TaskID, "paused", 5*time.Second)
	if dyPausedLogout.PauseReason != "auth_required" {
		t.Fatalf("expected PauseReason=auth_required after PauseForSite, got %+v", dyPausedLogout)
	}
	close(biliGate2)
	waitForTaskStatus(t, mgr, biliTask2.TaskID, "completed", 5*time.Second)
}

func TestDouyinCancelAndRemoveCleanStagingAndPartFiles(t *testing.T) {
	const (
		secUID     = "MS4wLjABAAAA_test_cancel_author_004"
		vidCancel  = "7330000000000000001"
		noteCancel = "7330000000000000002"
	)

	videoMP4 := buildProgressiveWithoutElst(t, true, false, 0)
	imgBytes := syntheticPNGMini()

	vidFlushed := make(chan struct{}, 1)
	vidHold := make(chan struct{})
	noteFlushed := make(chan struct{}, 1)
	noteHold := make(chan struct{})

	mux := http.NewServeMux()
	mux.HandleFunc("/cdn/slow-video.mp4", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Length", strconv.Itoa(len(videoMP4)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(videoMP4[:len(videoMP4)/2])
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case vidFlushed <- struct{}{}:
		default:
		}
		select {
		case <-vidHold:
		case <-r.Context().Done():
		}
	})
	mux.HandleFunc("/cdn/slow-img.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Length", strconv.Itoa(len(imgBytes)*4))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(imgBytes)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case noteFlushed <- struct{}{}:
		default:
		}
		select {
		case <-noteHold:
		case <-r.Context().Done():
		}
	})

	mgr, _, srv, _, _ := setupDouyinDownloadTestEnv(t, mux, 2)
	planner := newFakeDouyinWorkPlanner()
	mgr.RegisterWorkPlanner(planner)
	transport := site.DouyinVODTransport("Mozilla/5.0 Test", true)

	planner.setPlan(vidCancel, func(_ int) (WorkDownloadPlan, error) {
		return WorkDownloadPlan{
			Site: site.Douyin, Kind: "video", TargetID: vidCancel,
			Title: "取消清理测试视频", DurationSec: 2, AuthorUID: secUID, AuthorName: "DY",
			VideoURLs: []string{srv.URL + "/cdn/slow-video.mp4"}, Transport: transport,
		}, nil
	})
	planner.setPlan(noteCancel, func(_ int) (WorkDownloadPlan, error) {
		return WorkDownloadPlan{
			Site: site.Douyin, Kind: "note", TargetID: noteCancel,
			Title: "取消清理测试图文", AuthorUID: secUID, AuthorName: "DY",
			Pages:     []PlannedNotePage{{Index: 1, Kind: "image", ImageURLs: []string{srv.URL + "/cdn/slow-img.png"}}},
			Transport: transport,
		}, nil
	})

	tVid, err := mgr.EnqueueDouyinWork(context.Background(), DouyinWorkEnqueueRequest{AwemeID: vidCancel})
	if err != nil {
		t.Fatalf("EnqueueDouyinWork(vidCancel): %v", err)
	}
	tNote, err := mgr.EnqueueDouyinWork(context.Background(), DouyinWorkEnqueueRequest{AwemeID: noteCancel})
	if err != nil {
		t.Fatalf("EnqueueDouyinWork(noteCancel): %v", err)
	}

	<-vidFlushed
	<-noteFlushed
	// Wait briefly for worker goroutine to write the flushed chunk to .part on disk.
	time.Sleep(40 * time.Millisecond)

	if _, err := mgr.Cancel(tVid.TaskID); err != nil {
		t.Fatalf("Cancel(tVid): %v", err)
	}
	if _, err := mgr.Cancel(tNote.TaskID); err != nil {
		t.Fatalf("Cancel(tNote): %v", err)
	}
	close(vidHold)
	close(noteHold)

	// Wait until workers finish their deferred cleanTaskPartFiles cleanup so Remove succeeds.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		err1 := mgr.Remove(tVid.TaskID)
		err2 := mgr.Remove(tNote.TaskID)
		if err1 == nil && err2 == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	for _, dir := range []string{tVid.ItemDir, tNote.ItemDir} {
		if dir == "" {
			continue
		}
		_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, walkErr error) error {
			if walkErr != nil || d.IsDir() {
				return nil
			}
			name := d.Name()
			if strings.HasSuffix(name, ".part") || strings.HasSuffix(name, ".raw") || strings.HasSuffix(name, ".imgtmp") || strings.HasSuffix(name, ".audtmp") {
				t.Fatalf("orphan temp/part file left after Cancel+Remove: %s", p)
			}
			return nil
		})
	}
}

// The untranscoded source (原画) is tried first through the play redirector and
// recorded as such; when it fails, the best transcoded stream is cached instead.
func TestDouyinVideoPrefersOriginalThenFallsBack(t *testing.T) {
	const (
		secUID      = "MS4wLjABAAAA_test_author_sec_uid_002"
		awemeOrig   = "7300000000000000021"
		awemeNoOrig = "7300000000000000022"
	)
	videoMP4 := buildProgressiveWithoutElst(t, true, false, 0)
	bigMP4 := buildProgressiveWithoutElst(t, true, false, 64<<10)
	var transcodedHits atomic.Int64

	mux := http.NewServeMux()
	mux.HandleFunc("/aweme/v1/play/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" {
			t.Error("play redirector received a Cookie")
		}
		if r.URL.Query().Get("ratio") != "default" || r.URL.Query().Get("watermark") != "0" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.URL.Query().Get("video_id") == "v0200missing" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		http.Redirect(w, r, "/cdn/original.mp4", http.StatusFound)
	})
	mux.HandleFunc("/cdn/original.mp4", func(w http.ResponseWriter, r *http.Request) {
		serveRangeBytes(w, r, bigMP4, "video/mp4", 0, nil)
	})
	mux.HandleFunc("/cdn/transcoded.mp4", func(w http.ResponseWriter, r *http.Request) {
		transcodedHits.Add(1)
		serveRangeBytes(w, r, videoMP4, "video/mp4", 0, nil)
	})

	mgr, libStore, srv, _, _ := setupDouyinDownloadTestEnv(t, mux, 1)
	planner := newFakeDouyinWorkPlanner()
	mgr.RegisterWorkPlanner(planner)
	transport := site.DouyinVODTransport("Mozilla/5.0 Test", true)
	plan := func(id, videoID string) func(int) (WorkDownloadPlan, error) {
		return func(int) (WorkDownloadPlan, error) {
			return WorkDownloadPlan{
				Site: site.Douyin, Kind: "video", TargetID: id, Title: "原画测试", DurationSec: 2,
				AuthorUID: secUID, AuthorName: "测试创作者",
				OriginalURLs: []string{srv.URL + "/aweme/v1/play/?video_id=" + videoID + "&ratio=default&watermark=0"},
				VideoURLs:    []string{srv.URL + "/cdn/transcoded.mp4"},
				VideoGear:    "adapt_lowest_4_1", QualityName: "4K", Codec: "hevc", Width: 640, Height: 360,
				AvailableGears: []string{"4K", "1080P"},
				Transport:      transport,
			}, nil
		}
	}
	planner.setPlan(awemeOrig, plan(awemeOrig, "v0200okokok"))
	planner.setPlan(awemeNoOrig, plan(awemeNoOrig, "v0200missing"))

	for _, tc := range []struct {
		id       string
		wantGear string
		wantHits int64
	}{
		{awemeOrig, douyinOriginalGear, 0},
		{awemeNoOrig, "adapt_lowest_4_1", 1},
	} {
		task, err := mgr.EnqueueDouyinWork(context.Background(), DouyinWorkEnqueueRequest{AwemeID: tc.id})
		if err != nil {
			t.Fatalf("EnqueueDouyinWork(%s): %v", tc.id, err)
		}
		done := waitForTaskStatus(t, mgr, task.TaskID, "completed", 8*time.Second)
		item, ok, err := libStore.GetItem("douyin:video:" + tc.id)
		if err != nil || !ok || item.DouyinVideo == nil {
			t.Fatalf("%s: item not indexed: ok=%v err=%v", tc.id, ok, err)
		}
		if item.DouyinVideo.Gear != tc.wantGear {
			t.Fatalf("%s: gear = %q, want %q", tc.id, item.DouyinVideo.Gear, tc.wantGear)
		}
		if tc.wantGear == douyinOriginalGear && (item.DouyinVideo.QualityName != douyinOriginalQuality || item.DouyinVideo.AvailableGears[0] != douyinOriginalQuality) {
			t.Fatalf("%s: original not labelled: %+v", tc.id, item.DouyinVideo)
		}
		if got := transcodedHits.Load(); got != tc.wantHits {
			t.Fatalf("%s: transcoded requests = %d, want %d", tc.id, got, tc.wantHits)
		}
		entries, _ := os.ReadDir(done.ItemDir)
		for _, e := range entries {
			if strings.Contains(e.Name(), ".raw") || strings.HasSuffix(e.Name(), ".part") {
				t.Fatalf("%s: staging file left behind: %s", tc.id, e.Name())
			}
		}
		info, err := os.Stat(filepath.Join(done.ItemDir, "video.mp4"))
		if err != nil {
			t.Fatal(err)
		}
		if tc.wantGear == douyinOriginalGear && info.Size() <= int64(len(videoMP4)) {
			t.Fatalf("%s: cached %d bytes, want the larger original", tc.id, info.Size())
		}
	}
}
