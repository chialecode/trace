package download

import (
	"context"
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

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
)

type fakeXSitePlanner struct {
	mu       sync.Mutex
	plans    map[string]func(call int) (site.DownloadPlan, error)
	workPlan map[string]func(call int) (WorkDownloadPlan, error)
	calls    map[string]int
	authErr  error
	reported []error
}

func newFakeXSitePlanner() *fakeXSitePlanner {
	return &fakeXSitePlanner{
		plans:    make(map[string]func(call int) (site.DownloadPlan, error)),
		workPlan: make(map[string]func(call int) (WorkDownloadPlan, error)),
		calls:    make(map[string]int),
	}
}

func (p *fakeXSitePlanner) SiteID() site.ID { return site.X }

func (p *fakeXSitePlanner) RequireAuth(_ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.authErr
}

func (p *fakeXSitePlanner) ReportAutomaticError(_ context.Context, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reported = append(p.reported, err)
}

func (p *fakeXSitePlanner) setSitePlan(targetID string, fn func(call int) (site.DownloadPlan, error)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.plans[targetID] = fn
}

func (p *fakeXSitePlanner) setWorkPlan(targetID string, fn func(call int) (WorkDownloadPlan, error)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.workPlan[targetID] = fn
}

func (p *fakeXSitePlanner) callCount(targetID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[targetID]
}

func (p *fakeXSitePlanner) reportedCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.reported)
}

func (p *fakeXSitePlanner) PlanWorkDownload(_ context.Context, targetID string, _ WorkPlanOptions) (WorkDownloadPlan, error) {
	p.mu.Lock()
	p.calls[targetID]++
	call := p.calls[targetID]
	wfn := p.workPlan[targetID]
	sfn := p.plans[targetID]
	p.mu.Unlock()
	if wfn != nil {
		return wfn(call)
	}
	if sfn != nil {
		sp, err := sfn(call)
		if err != nil {
			return WorkDownloadPlan{}, err
		}
		return convertSiteDownloadPlan(sp), nil
	}
	return WorkDownloadPlan{}, &site.Error{Site: site.X, Kind: site.ErrKindUnavailable, Message: "X 帖子不存在"}
}

func TestXSingleVideoMoovReorderResumeExpiryRefreshCDNSecurityHLSOnlyAndZeroLeak(t *testing.T) {
	mp4Payload := buildProgressiveWithoutElst(t, true, true, 12*1024) // tail-moov progressive MP4 (>12 KiB)
	jpegPayload := syntheticJPEGMini()

	// Build a valid audio-only MP4 (no video track) for no-video-track rejection testing.
	audioM4S, err := BuildSyntheticAudioM4S(90)
	if err != nil {
		t.Fatalf("BuildSyntheticAudioM4S: %v", err)
	}
	audioOnlyPath := filepath.Join(t.TempDir(), "x-audio-only.mp4")
	if _, err := DefragmentFMP4Bytes(audioM4S, audioOnlyPath); err != nil {
		t.Fatalf("DefragmentFMP4Bytes: %v", err)
	}
	audioOnlyMP4, err := os.ReadFile(audioOnlyPath)
	if err != nil {
		t.Fatalf("read audio-only mp4: %v", err)
	}

	const (
		expiredTok   = "XExpiredTokenSentinel001"
		freshTok     = "XFreshTokenSentinel002"
		authTok      = "XAuthCookieSentinel003"
		ct0Tok       = "XCt0CookieSentinel004"
		kdtTok       = "XKdtCookieSentinel005"
		authorRestID = "1000000000000000001"
	)

	var (
		mu              sync.Mutex
		seenCookies     []string
		seenReferers    []string
		seenOrigins     []string
		resumeRangeHdrs []string
		fresh403Ranges  []string
		fresh404Ranges  []string
		fresh410Ranges  []string
		expired403Hits  atomic.Int32
		expired404Hits  atomic.Int32
		expired410Hits  atomic.Int32
		disconnectHits  atomic.Int32
		hlsHits         atomic.Int32
	)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if c := r.Header.Get("Cookie"); c != "" {
			seenCookies = append(seenCookies, c)
		}
		if ref := r.Header.Get("Referer"); ref != "" {
			seenReferers = append(seenReferers, ref)
		}
		if ori := r.Header.Get("Origin"); ori != "" {
			seenOrigins = append(seenOrigins, ori)
		}
		mu.Unlock()

		host := strings.ToLower(r.Host)
		if host == "" {
			host = strings.ToLower(r.URL.Host)
		}
		switch {
		case host == "pbs.twimg.com" && r.URL.Path == "/ext_tw_video_thumb/1000000000000000009/pu/img/cover01.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			w.Header().Set("Content-Length", strconv.Itoa(len(jpegPayload)))
			_, _ = w.Write(jpegPayload)

		case host == "video.twimg.com" && r.URL.Path == "/ext_tw_video/1/pu/vid/disconnect_resume.mp4":
			hit := disconnectHits.Add(1)
			if hit == 1 {
				// Simulate a mid-segment disconnect after writing 2048 bytes of a 4096-byte segment.
				w.Header().Set("Content-Type", "video/mp4")
				w.Header().Set("Content-Range", "bytes 0-4095/"+strconv.Itoa(len(mp4Payload)))
				w.Header().Set("Content-Length", "4096")
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write(mp4Payload[:2048])
				return
			}
			mu.Lock()
			resumeRangeHdrs = append(resumeRangeHdrs, r.Header.Get("Range"))
			mu.Unlock()
			serveRangeBytes(w, r, mp4Payload, "video/mp4", 0, nil)

		case host == "video.twimg.com" && r.URL.Path == "/ext_tw_video/1/pu/vid/expired_403.mp4":
			if expired403Hits.Add(1) == 1 {
				serveRangeBytes(w, r, mp4Payload, "video/mp4", 0, nil)
				return
			}
			w.WriteHeader(http.StatusForbidden)

		case host == "video.twimg.com" && r.URL.Path == "/ext_tw_video/1/pu/vid/fresh_403.mp4":
			mu.Lock()
			fresh403Ranges = append(fresh403Ranges, r.Header.Get("Range"))
			mu.Unlock()
			serveRangeBytes(w, r, mp4Payload, "video/mp4", 0, nil)

		case host == "video.twimg.com" && r.URL.Path == "/ext_tw_video/1/pu/vid/expired_404.mp4":
			if expired404Hits.Add(1) == 1 {
				serveRangeBytes(w, r, mp4Payload, "video/mp4", 0, nil)
				return
			}
			w.WriteHeader(http.StatusNotFound)

		case host == "video.twimg.com" && r.URL.Path == "/ext_tw_video/1/pu/vid/fresh_404.mp4":
			mu.Lock()
			fresh404Ranges = append(fresh404Ranges, r.Header.Get("Range"))
			mu.Unlock()
			serveRangeBytes(w, r, mp4Payload, "video/mp4", 0, nil)

		case host == "video.twimg.com" && r.URL.Path == "/ext_tw_video/1/pu/vid/expired_410.mp4":
			if expired410Hits.Add(1) == 1 {
				serveRangeBytes(w, r, mp4Payload, "video/mp4", 0, nil)
				return
			}
			w.WriteHeader(http.StatusGone)

		case host == "video.twimg.com" && r.URL.Path == "/ext_tw_video/1/pu/vid/fresh_410.mp4":
			mu.Lock()
			fresh410Ranges = append(fresh410Ranges, r.Header.Get("Range"))
			mu.Unlock()
			serveRangeBytes(w, r, mp4Payload, "video/mp4", 0, nil)

		case host == "video.twimg.com" && r.URL.Path == "/ext_tw_video/1/pu/vid/always_403.mp4":
			w.WriteHeader(http.StatusForbidden)

		case host == "video.twimg.com" && r.URL.Path == "/ext_tw_video/1/pu/vid/ok_video.mp4":
			serveRangeBytes(w, r, mp4Payload, "video/mp4", 0, nil)

		case host == "video.twimg.com" && r.URL.Path == "/ext_tw_video/1/pu/vid/audio_only.mp4":
			serveRangeBytes(w, r, audioOnlyMP4, "video/mp4", 0, nil)

		case host == "video.twimg.com" && r.URL.Path == "/ext_tw_video/1/pu/vid/disguised_html.mp4":
			body := []byte("<!DOCTYPE html><html><body>Access Denied</body></html>")
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			_, _ = w.Write(body)

		case host == "video.twimg.com" && r.URL.Path == "/ext_tw_video/1/pu/vid/disguised_json.mp4":
			body := []byte(`{"errors":[{"message":"Media token expired"}]}`)
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			_, _ = w.Write(body)

		case host == "video.twimg.com" && r.URL.Path == "/ext_tw_video/1/pu/vid/redirect_evil.mp4":
			http.Redirect(w, r, "https://evil.example/hijack.mp4", http.StatusFound)

		case host == "video.twimg.com" && strings.HasSuffix(r.URL.Path, ".m3u8"):
			hlsHits.Add(1)
			w.Header().Set("Content-Type", "application/x-mpegURL")
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-VERSION:3\n"))

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	mgr, libStore, _, dbPath, eventBuf := setupDouyinDownloadTestEnv(t, http.NotFoundHandler(), 2)
	mgr.SetChunkSize(4096)
	mgr.SetMediaTransportWrap(func(_ http.RoundTripper) http.RoundTripper {
		return roundTripFunc(func(req *http.Request) (*http.Response, error) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			resp := rec.Result()
			resp.Request = req
			return resp, nil
		})
	})

	planner := newFakeXSitePlanner()
	mgr.RegisterWorkPlanner(planner)

	// Intentionally pass non-empty Referer/Origin and PublicRedirects=true to verify normalizeXMediaTransport clears all three!
	dirtyTransport := site.XMediaTransport("TRACE-X-Test-UA", false)
	dirtyTransport.Referer = "https://x.com/"
	dirtyTransport.Origin = "https://x.com"
	dirtyTransport.PublicRedirects = true

	// 1. Disconnect mid-segment -> Range resume from byte 2048 -> tail-moov relocated to front!
	const idResume = "1000000000000000101"
	planner.setSitePlan(idResume, func(_ int) (site.DownloadPlan, error) {
		return site.DownloadPlan{
			Site:         site.X,
			ItemKey:      "x:dynamic:" + idResume,
			TargetID:     idResume,
			Kind:         "single_mp4",
			CanonicalURL: "https://x.com/synthetic_creator/status/" + idResume + "?auth_token=" + authTok,
			AuthorUID:    "00" + authorRestID,
			AuthorName:   "合成 X 创作者",
			AuthorFace:   "https://pbs.twimg.com/profile_images/1/avatar.jpg?ct0=" + ct0Tok + "&kdt=" + kdtTok,
			Title:        "断点续传与 moov 前置测试",
			Desc:         "单视频完整正文",
			PubTs:        1763800100,
			DurationSec:  2,
			VideoCodec:   "avc",
			QualityLabel: "1080p",
			Width:        1280,
			Height:       720,
			VideoURLs:    []string{"https://video.twimg.com/ext_tw_video/1/pu/vid/disconnect_resume.mp4?tag=12&token=" + expiredTok},
			CoverURLs:    []string{"https://pbs.twimg.com/ext_tw_video_thumb/1000000000000000009/pu/img/cover01.jpg?token=" + expiredTok},
			Media:        dirtyTransport,
		}, nil
	})
	tResume, err := mgr.EnqueueXWork(context.Background(), XWorkEnqueueRequest{TargetID: idResume})
	if err != nil {
		t.Fatalf("EnqueueXWork disconnect resume: %v", err)
	}
	doneResume := waitForTaskTerminal(t, mgr, tResume.TaskID, 10*time.Second)
	if doneResume.Status != "completed" {
		t.Fatalf("disconnect resume status=%s err=%s", doneResume.Status, doneResume.ErrorMessage)
	}
	mu.Lock()
	if len(resumeRangeHdrs) == 0 || !strings.HasPrefix(resumeRangeHdrs[0], "bytes=2048-") {
		t.Fatalf("expected Range resume from byte 2048 after disconnect, got %v", resumeRangeHdrs)
	}
	mu.Unlock()
	metaResume, ok, err := libStore.GetItem("x:dynamic:" + idResume)
	if err != nil || !ok {
		t.Fatalf("GetItem(x:dynamic:%s): ok=%v err=%v", idResume, ok, err)
	}
	wantDir := filepath.Join(libStore.CacheDir(), "x", authorRestID, "posts", idResume)
	if filepath.Clean(metaResume.ItemDir) != filepath.Clean(wantDir) {
		t.Fatalf("ItemDir = %q, want %q", metaResume.ItemDir, wantDir)
	}
	if metaResume.Kind != "dynamic" || metaResume.Status != "complete" || metaResume.Author.UID != authorRestID {
		t.Fatalf("unexpected metaResume: kind=%q status=%q uid=%q", metaResume.Kind, metaResume.Status, metaResume.Author.UID)
	}
	summaryResume, err := InspectMP4File(filepath.Join(metaResume.ItemDir, "video.mp4"))
	if err != nil || !summaryResume.MoovBeforeMdat {
		t.Fatalf("expected tail-moov relocated to front in video.mp4, err=%v summary=%+v", err, summaryResume)
	}
	if _, err := os.Stat(filepath.Join(metaResume.ItemDir, "cover.jpg")); err != nil {
		t.Fatalf("expected cover.jpg: %v", err)
	}

	// 2. Expired media URL (403, 404, 410) mid-file -> refresh AT MOST ONCE via TweetDetail & Range resume from byte 4096.
	expiryCases := []struct {
		code       int
		statusID   string
		expiredURL string
		freshURL   string
		ranges     *[]string
	}{
		{403, "1000000000000000102", "https://video.twimg.com/ext_tw_video/1/pu/vid/expired_403.mp4?token=" + expiredTok, "https://video.twimg.com/ext_tw_video/1/pu/vid/fresh_403.mp4?token=" + freshTok, &fresh403Ranges},
		{404, "1000000000000000103", "https://video.twimg.com/ext_tw_video/1/pu/vid/expired_404.mp4?token=" + expiredTok, "https://video.twimg.com/ext_tw_video/1/pu/vid/fresh_404.mp4?token=" + freshTok, &fresh404Ranges},
		{410, "1000000000000000104", "https://video.twimg.com/ext_tw_video/1/pu/vid/expired_410.mp4?token=" + expiredTok, "https://video.twimg.com/ext_tw_video/1/pu/vid/fresh_410.mp4?token=" + freshTok, &fresh410Ranges},
	}
	for _, tc := range expiryCases {
		tc := tc
		planner.setSitePlan(tc.statusID, func(call int) (site.DownloadPlan, error) {
			u := tc.expiredURL
			if call >= 2 {
				u = tc.freshURL
			}
			return site.DownloadPlan{
				Site:         site.X,
				ItemKey:      "x:dynamic:" + tc.statusID,
				TargetID:     tc.statusID,
				Kind:         "single_mp4",
				AuthorUID:    authorRestID,
				AuthorName:   "合成 X 创作者",
				Title:        "过期刷新测试",
				DurationSec:  2,
				VideoCodec:   "avc",
				QualityLabel: "720p",
				VideoURLs:    []string{u},
				Media:        site.XMediaTransport("TRACE-X-Test-UA", false),
			}, nil
		})
		tk, err := mgr.EnqueueXWork(context.Background(), XWorkEnqueueRequest{TargetID: tc.statusID})
		if err != nil {
			t.Fatalf("EnqueueXWork HTTP %d expiry: %v", tc.code, err)
		}
		done := waitForTaskTerminal(t, mgr, tk.TaskID, 10*time.Second)
		if done.Status != "completed" {
			t.Fatalf("HTTP %d expiry refresh status=%s err=%s", tc.code, done.Status, done.ErrorMessage)
		}
		if planner.callCount(tc.statusID) != 2 {
			t.Fatalf("HTTP %d expiry planner calls = %d, want 2 (1 initial + 1 refresh)", tc.code, planner.callCount(tc.statusID))
		}
		mu.Lock()
		gotRanges := append([]string(nil), (*tc.ranges)...)
		mu.Unlock()
		if len(gotRanges) == 0 || !strings.HasPrefix(gotRanges[0], "bytes=4096-") {
			t.Fatalf("HTTP %d expected Range resume from byte 4096 after refresh, got %v", tc.code, gotRanges)
		}
	}

	// 3. Persistent 403 refreshes at most ONCE (total 2 calls) and fails cleanly.
	const idPersist403 = "1000000000000000105"
	planner.setSitePlan(idPersist403, func(_ int) (site.DownloadPlan, error) {
		return site.DownloadPlan{
			Site:        site.X,
			TargetID:    idPersist403,
			Kind:        "single_mp4",
			AuthorUID:   authorRestID,
			AuthorName:  "合成 X 创作者",
			Title:       "持续 403 仅刷新一次",
			DurationSec: 2,
			VideoURLs:   []string{"https://video.twimg.com/ext_tw_video/1/pu/vid/always_403.mp4?token=" + expiredTok},
			Media:       site.XMediaTransport("TRACE-X-Test-UA", false),
		}, nil
	})
	tP403, err := mgr.EnqueueXWork(context.Background(), XWorkEnqueueRequest{TargetID: idPersist403})
	if err != nil {
		t.Fatalf("EnqueueXWork persistent 403: %v", err)
	}
	doneP403 := waitForTaskTerminal(t, mgr, tP403.TaskID, 10*time.Second)
	if doneP403.Status != "failed" {
		t.Fatalf("persistent 403 status=%s, want failed", doneP403.Status)
	}
	if planner.callCount(idPersist403) != 2 {
		t.Fatalf("persistent 403 planner calls = %d, want 2", planner.callCount(idPersist403))
	}

	// 4. Reject bad duration, no video track (audio-only), disguised HTML, disguised X JSON,
	// non-allowlisted host, and non-allowlisted redirect.
	rejectCases := []struct {
		name        string
		statusID    string
		videoURL    string
		durationSec int
	}{
		{"duration mismatch", "1000000000000000106", "https://video.twimg.com/ext_tw_video/1/pu/vid/ok_video.mp4", 120},
		{"no video track", "1000000000000000107", "https://video.twimg.com/ext_tw_video/1/pu/vid/audio_only.mp4", 2},
		{"disguised HTML", "1000000000000000108", "https://video.twimg.com/ext_tw_video/1/pu/vid/disguised_html.mp4", 2},
		{"disguised X JSON", "1000000000000000109", "https://video.twimg.com/ext_tw_video/1/pu/vid/disguised_json.mp4", 2},
		{"non-allowlisted host", "1000000000000000110", "https://evil.example/video.mp4", 2},
		{"non-allowlisted redirect", "1000000000000000111", "https://video.twimg.com/ext_tw_video/1/pu/vid/redirect_evil.mp4", 2},
	}
	for _, tc := range rejectCases {
		tc := tc
		planner.setSitePlan(tc.statusID, func(_ int) (site.DownloadPlan, error) {
			return site.DownloadPlan{
				Site:        site.X,
				TargetID:    tc.statusID,
				Kind:        "single_mp4",
				AuthorUID:   authorRestID,
				AuthorName:  "合成 X 创作者",
				Title:       tc.name,
				DurationSec: tc.durationSec,
				VideoURLs:   []string{tc.videoURL},
				Media:       dirtyTransport,
			}, nil
		})
		tk, err := mgr.EnqueueXWork(context.Background(), XWorkEnqueueRequest{TargetID: tc.statusID})
		if err != nil {
			t.Fatalf("enqueue %s: %v", tc.name, err)
		}
		done := waitForTaskTerminal(t, mgr, tk.TaskID, 10*time.Second)
		if done.Status != "failed" {
			t.Fatalf("%s: status=%s, want failed", tc.name, done.Status)
		}
	}

	// 5. HLS-only video (.m3u8 only, no progressive video/mp4): 0 HLS requests, saved partial with clear missing notice!
	const idHLS = "1000000000000000112"
	planner.setSitePlan(idHLS, func(_ int) (site.DownloadPlan, error) {
		return site.DownloadPlan{
			Site:        site.X,
			TargetID:    idHLS,
			Kind:        "single_mp4",
			AuthorUID:   authorRestID,
			AuthorName:  "合成 X 创作者",
			Title:       "仅分段流视频帖",
			Desc:        "这是一条只有 HLS 分段流的视频帖正文",
			PubTs:       1763800500,
			DurationSec: 30,
			VideoURLs:   []string{"https://video.twimg.com/ext_tw_video/1/pl/master.m3u8?tag=14&token=" + expiredTok},
			CoverURLs:   []string{"https://pbs.twimg.com/ext_tw_video_thumb/1000000000000000009/pu/img/cover01.jpg"},
			Media:       site.XMediaTransport("TRACE-X-Test-UA", false),
		}, nil
	})
	tHLS, err := mgr.EnqueueXWork(context.Background(), XWorkEnqueueRequest{TargetID: idHLS})
	if err != nil {
		t.Fatalf("EnqueueXWork HLS-only: %v", err)
	}
	doneHLS := waitForTaskTerminal(t, mgr, tHLS.TaskID, 10*time.Second)
	if doneHLS.Status != "partial" {
		t.Fatalf("HLS-only task status=%s err=%s, want partial", doneHLS.Status, doneHLS.ErrorMessage)
	}
	if hlsHits.Load() != 0 {
		t.Fatalf("HLS-only video must make 0 segment/playlist requests, got %d", hlsHits.Load())
	}
	metaHLS, ok, err := libStore.GetItem("x:dynamic:" + idHLS)
	if err != nil || !ok {
		t.Fatalf("GetItem(HLS-only): ok=%v err=%v", ok, err)
	}
	if metaHLS.Status != "partial" || len(metaHLS.Missing) != 1 || metaHLS.Missing[0].Reason != XHLSOnlyMissingReason {
		t.Fatalf("HLS-only manifest status/missing mismatch: status=%q missing=%+v", metaHLS.Status, metaHLS.Missing)
	}
	if dot := library.ComputeDotState(metaHLS); dot.Dot != "yellow" {
		t.Fatalf("HLS-only dot = %+v, want yellow", dot)
	}
	if _, err := os.Stat(filepath.Join(metaHLS.ItemDir, "cover.jpg")); err != nil {
		t.Fatalf("expected cover.jpg to still be saved for HLS-only video: %v", err)
	}

	// 6. Verify ZERO Cookie, ZERO Referer, ZERO Origin sent on any CDN request.
	mu.Lock()
	if len(seenCookies) > 0 {
		t.Fatalf("X CDN requests must never send Cookie, saw: %v", seenCookies)
	}
	if len(seenReferers) > 0 {
		t.Fatalf("X CDN requests must never send Referer, saw: %v", seenReferers)
	}
	if len(seenOrigins) > 0 {
		t.Fatalf("X CDN requests must never send Origin, saw: %v", seenOrigins)
	}
	mu.Unlock()

	// 7. Sentinel scan of item.json, library.db (+WAL), and task event logs: ZERO signed URLs or secrets.
	assertNoSentinelsInTreeAndDB(
		t,
		libStore.CacheDir(),
		dbPath,
		eventBuf.String(),
		[]string{expiredTok, freshTok, authTok, ct0Tok, kdtTok, "auth_token=", "ct0=", "kdt=", "disconnect_resume.mp4", "expired_403.mp4", "fresh_403.mp4", "master.m3u8"},
		[]string{"video.twimg.com"},
	)
}

func TestXMultiImageOrigFallbackGIFMixedThreadPartialForwardsAndNoMove(t *testing.T) {
	jpegBytes := syntheticJPEGMini()
	pngBytes := syntheticPNGMini()
	webpBytes := syntheticWEBPMini()
	gifMP4Bytes := buildProgressiveWithoutElst(t, false, false, 0) // muted GIF MP4 (no audio track, video-only)
	mixedMP4Bytes := buildProgressiveWithoutElst(t, true, true, 0)

	const (
		authorRestID    = "1000000000000000011"
		forwarderRestID = "1000000000000000012"
		originRestID    = "1000000000000000013"
		gifTok          = "XGIFSignedToken001"
		mixTok          = "XMixSignedToken002"
		secretTok       = "XSecretCookieSentinel009"
	)

	var (
		mu          sync.Mutex
		img01Query  string
		img02Reqs   []string
		missingReqs []string
		hlsMixedHit atomic.Int32
	)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := strings.ToLower(r.Host)
		if host == "" {
			host = strings.ToLower(r.URL.Host)
		}
		switch {
		case host == "pbs.twimg.com" && r.URL.Path == "/media/SynthImg01":
			mu.Lock()
			img01Query = r.URL.RawQuery
			mu.Unlock()
			w.Header().Set("Content-Type", "image/jpeg")
			w.Header().Set("Content-Length", strconv.Itoa(len(jpegBytes)))
			_, _ = w.Write(jpegBytes)

		case host == "pbs.twimg.com" && r.URL.Path == "/media/SynthImg02":
			mu.Lock()
			img02Reqs = append(img02Reqs, r.URL.RawQuery)
			mu.Unlock()
			// Serve PNG magic bytes under a ?format=jpg URL to verify magic-byte extension sniffing.
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Length", strconv.Itoa(len(pngBytes)))
			_, _ = w.Write(pngBytes)

		case host == "pbs.twimg.com" && r.URL.Path == "/media/SynthImgMissing":
			mu.Lock()
			missingReqs = append(missingReqs, r.URL.RawQuery)
			mu.Unlock()
			w.WriteHeader(http.StatusNotFound)

		case host == "pbs.twimg.com" && r.URL.Path == "/tweet_video_thumb/SynthGif03.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			w.Header().Set("Content-Length", strconv.Itoa(len(jpegBytes)))
			_, _ = w.Write(jpegBytes)

		case host == "pbs.twimg.com" && r.URL.Path == "/ext_tw_video_thumb/4/pu/img/cover04":
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Length", strconv.Itoa(len(webpBytes)))
			_, _ = w.Write(webpBytes)

		case host == "video.twimg.com" && r.URL.Path == "/tweet_video/SynthGif03.mp4":
			serveRangeBytes(w, r, gifMP4Bytes, "video/mp4", 0, nil)

		case host == "video.twimg.com" && r.URL.Path == "/ext_tw_video/4/pu/vid/mixed04.mp4":
			serveRangeBytes(w, r, mixedMP4Bytes, "video/mp4", 0, nil)

		case host == "video.twimg.com" && strings.HasSuffix(r.URL.Path, ".m3u8"):
			hlsMixedHit.Add(1)
			w.WriteHeader(http.StatusOK)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	mgr, libStore, _, dbPath, eventBuf := setupDouyinDownloadTestEnv(t, http.NotFoundHandler(), 2)
	mgr.SetMediaTransportWrap(func(_ http.RoundTripper) http.RoundTripper {
		return roundTripFunc(func(req *http.Request) (*http.Response, error) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			resp := rec.Result()
			resp.Request = req
			return resp, nil
		})
	})

	planner := newFakeXSitePlanner()
	mgr.RegisterWorkPlanner(planner)
	strictTransport := site.XMediaTransport("TRACE-X-Test-UA", false)

	// Subtest A: 4-item mixed post (photo orig, photo orig with PNG magic, GIF MP4 + gif:true, mixed video MP4)
	// + long note_tweet text + author self-reply thread in the SAME item!
	const idMixed = "1000000000000000201"
	longNoteText := strings.Repeat("这是一段超过 280 字的 X 长帖（note_tweet）完整正文，包含多图、动图与混排视频。", 12)
	planner.setWorkPlan(idMixed, func(_ int) (WorkDownloadPlan, error) {
		return WorkDownloadPlan{
			Site:           site.X,
			ItemKey:        "x:dynamic:" + idMixed,
			Kind:           "dynamic",
			TargetID:       idMixed,
			CanonicalURL:   "https://x.com/synthetic_creator/status/" + idMixed,
			AuthorUID:      authorRestID,
			AuthorUniqueID: "synthetic_creator",
			AuthorName:     "合成创作者",
			Title:          "四项混排长帖与线程",
			Desc:           longNoteText,
			PubTs:          1763810100,
			Thread: []PlannedThreadPart{
				{ID: "1000000000000000202", PubTs: 1763810110, Text: "作者自回复线程第二段内容"},
				{ID: "1000000000000000203", PubTs: 1763810120, Text: "作者自回复线程第三段收尾"},
			},
			Pages: []PlannedNotePage{
				{Index: 1, Kind: "image", Width: 1600, Height: 1200, ImageURLs: []string{"https://pbs.twimg.com/media/SynthImg01.jpg:large"}},
				{Index: 2, Kind: "image", Width: 1200, Height: 900, ImageURLs: []string{"https://pbs.twimg.com/media/SynthImg02.jpg"}},
				{Index: 3, Kind: "gif", Width: 480, Height: 360, ImageURLs: []string{"https://pbs.twimg.com/tweet_video_thumb/SynthGif03.jpg"}, VideoURLs: []string{"https://video.twimg.com/tweet_video/SynthGif03.mp4?token=" + gifTok}},
				{Index: 4, Kind: "video", Width: 1280, Height: 720, DurationSec: 2, Codec: "avc", QualityName: "720p", ImageURLs: []string{"https://pbs.twimg.com/ext_tw_video_thumb/4/pu/img/cover04"}, VideoURLs: []string{"https://video.twimg.com/ext_tw_video/4/pu/vid/mixed04.mp4?token=" + mixTok}},
			},
			Transport: strictTransport,
		}, nil
	})
	tMixed, err := mgr.EnqueueXWork(context.Background(), XWorkEnqueueRequest{TargetID: idMixed})
	if err != nil {
		t.Fatalf("EnqueueXWork mixed post: %v", err)
	}
	doneMixed := waitForTaskTerminal(t, mgr, tMixed.TaskID, 15*time.Second)
	if doneMixed.Status != "completed" {
		t.Fatalf("mixed post status=%s err=%s", doneMixed.Status, doneMixed.ErrorMessage)
	}

	mu.Lock()
	if img01Query != "format=jpg&name=orig" {
		t.Fatalf("page 1 image query = %q, want format=jpg&name=orig", img01Query)
	}
	// Originals only (2026-11-24): never request the recompressed name=large copy.
	if len(img02Reqs) != 1 || img02Reqs[0] != "format=jpg&name=orig" {
		t.Fatalf("page 2 expected a single name=orig request, got %v", img02Reqs)
	}
	mu.Unlock()

	metaMixed, ok, err := libStore.GetItem("x:dynamic:" + idMixed)
	if err != nil || !ok {
		t.Fatalf("GetItem(mixed): ok=%v err=%v", ok, err)
	}
	if metaMixed.Status != "complete" || metaMixed.Author.UniqueID != "synthetic_creator" {
		t.Fatalf("unexpected metaMixed status=%q uniqueId=%q", metaMixed.Status, metaMixed.Author.UniqueID)
	}
	if !strings.Contains(metaMixed.Text, longNoteText) ||
		!strings.Contains(metaMixed.Text, "作者自回复线程第二段内容") ||
		!strings.Contains(metaMixed.Text, "作者自回复线程第三段收尾") {
		t.Fatalf("long note_tweet or thread missing from metaMixed.Text: %q", metaMixed.Text)
	}
	if len(metaMixed.Thread) != 2 || metaMixed.Thread[0].ID != "1000000000000000202" || metaMixed.Thread[1].ID != "1000000000000000203" {
		t.Fatalf("unexpected persisted Thread: %+v", metaMixed.Thread)
	}
	if len(metaMixed.NotePages) != 4 {
		t.Fatalf("expected 4 ordered NotePages, got %d", len(metaMixed.NotePages))
	}
	if metaMixed.NotePages[0].Image != "images/01.jpg" ||
		metaMixed.NotePages[1].Image != "images/02.png" ||
		metaMixed.NotePages[2].Kind != "video" || !metaMixed.NotePages[2].GIF || metaMixed.NotePages[2].Video != "images/03.mp4" || metaMixed.NotePages[2].Cover != "images/03.jpg" ||
		metaMixed.NotePages[3].Kind != "video" || metaMixed.NotePages[3].GIF || metaMixed.NotePages[3].Video != "images/04.mp4" || metaMixed.NotePages[3].Cover != "images/04.webp" {
		t.Fatalf("unexpected mixed NotePages order/GIF/extensions: %+v", metaMixed.NotePages)
	}
	for _, rel := range []string{"images/01.jpg", "images/02.png", "images/03.jpg", "images/03.mp4", "images/04.webp", "images/04.mp4"} {
		if _, err := os.Stat(filepath.Join(metaMixed.ItemDir, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("expected cached file %s: %v", rel, err)
		}
	}
	// Verify mixed video MP4 had tail-moov reordered to front.
	if sum4, err := InspectMP4File(filepath.Join(metaMixed.ItemDir, "images", "04.mp4")); err != nil || !sum4.MoovBeforeMdat {
		t.Fatalf("expected mixed images/04.mp4 moov-before-mdat, err=%v sum=%+v", err, sum4)
	}

	// Subtest B: Pure GIF post (1 GIF MP4, gif=true) -> dynamicType="draw", appears in "image" NOT "video".
	const idPureGIF = "1000000000000000204"
	planner.setSitePlan(idPureGIF, func(_ int) (site.DownloadPlan, error) {
		return site.DownloadPlan{
			Site:       site.X,
			TargetID:   idPureGIF,
			Kind:       "image_note",
			AuthorUID:  authorRestID,
			AuthorName: "合成创作者",
			Title:      "纯 GIF 帖",
			PubTs:      1763810150,
			Images: []site.MediaImageAsset{
				{Index: 1, MediaRole: "animated_gif", Width: 480, Height: 360, ImageURLs: []string{"https://pbs.twimg.com/tweet_video_thumb/SynthGif03.jpg"}, VideoURLs: []string{"https://video.twimg.com/tweet_video/SynthGif03.mp4"}},
			},
			Media: strictTransport,
		}, nil
	})
	tGIF, err := mgr.EnqueueXWork(context.Background(), XWorkEnqueueRequest{TargetID: idPureGIF})
	if err != nil {
		t.Fatalf("EnqueueXWork pure GIF: %v", err)
	}
	if doneGIF := waitForTaskTerminal(t, mgr, tGIF.TaskID, 10*time.Second); doneGIF.Status != "completed" {
		t.Fatalf("pure GIF status=%s err=%s", doneGIF.Status, doneGIF.ErrorMessage)
	}
	metaGIF, ok, err := libStore.GetItem("x:dynamic:" + idPureGIF)
	if err != nil || !ok || !metaGIF.NotePages[0].GIF || metaGIF.DynamicType != "draw" {
		t.Fatalf("unexpected pure GIF meta: ok=%v err=%v dt=%q pages=%+v", ok, err, metaGIF.DynamicType, metaGIF.NotePages)
	}

	// Subtest C: Single-file missing + mixed-media HLS-only item marks yellow partial dot without failing remaining images!
	const idPartial = "1000000000000000205"
	planner.setSitePlan(idPartial, func(_ int) (site.DownloadPlan, error) {
		return site.DownloadPlan{
			Site:       site.X,
			TargetID:   idPartial,
			Kind:       "image_note",
			AuthorUID:  authorRestID,
			AuthorName: "合成创作者",
			Title:      "单文件缺失与混排 HLS 黄点测试",
			PubTs:      1763810200,
			Images: []site.MediaImageAsset{
				{Index: 1, MediaRole: "image", ImageURLs: []string{"https://pbs.twimg.com/media/SynthImg01.jpg"}},
				{Index: 2, MediaRole: "image", ImageURLs: []string{"https://pbs.twimg.com/media/SynthImgMissing.jpg"}},
				{Index: 3, MediaRole: "video", ImageURLs: []string{"https://pbs.twimg.com/ext_tw_video_thumb/4/pu/img/cover04"}, VideoURLs: []string{"https://video.twimg.com/ext_tw_video/9/pl/mixed_only.m3u8"}},
			},
			Media: strictTransport,
		}, nil
	})
	tPartial, err := mgr.EnqueueXWork(context.Background(), XWorkEnqueueRequest{TargetID: idPartial})
	if err != nil {
		t.Fatalf("EnqueueXWork partial: %v", err)
	}
	donePartial := waitForTaskTerminal(t, mgr, tPartial.TaskID, 10*time.Second)
	if donePartial.Status != "partial" {
		t.Fatalf("partial task status=%s err=%s, want partial", donePartial.Status, donePartial.ErrorMessage)
	}
	mu.Lock()
	for _, q := range missingReqs {
		if strings.Contains(q, "name=large") || !strings.Contains(q, "name=orig") {
			mu.Unlock()
			t.Fatalf("missing original must not fall back to a compressed copy, requests=%v", missingReqs)
		}
	}
	if len(missingReqs) == 0 {
		mu.Unlock()
		t.Fatalf("missing original was never requested")
	}
	mu.Unlock()
	if hlsMixedHit.Load() != 0 {
		t.Fatalf("mixed HLS-only page must make 0 playlist requests, got %d", hlsMixedHit.Load())
	}
	metaPartial, ok, err := libStore.GetItem("x:dynamic:" + idPartial)
	if err != nil || !ok || metaPartial.Status != "partial" || len(metaPartial.Missing) != 2 {
		t.Fatalf("unexpected partial meta: ok=%v err=%v status=%q missing=%+v", ok, err, metaPartial.Status, metaPartial.Missing)
	}
	if dot := library.ComputeDotState(metaPartial); dot.Dot != "yellow" {
		t.Fatalf("expected yellow dot for partial item, got %+v", dot)
	}
	if _, err := os.Stat(filepath.Join(metaPartial.ItemDir, "images", "01.jpg")); err != nil {
		t.Fatalf("expected non-missing images/01.jpg to still be saved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(metaPartial.ItemDir, "images", "03.webp")); err != nil {
		t.Fatalf("expected HLS video cover images/03.webp to still be saved: %v", err)
	}
	foundHLSNotice := false
	for _, mr := range metaPartial.Missing {
		if mr.Reason == XHLSOnlyMissingReason {
			foundHLSNotice = true
		}
	}
	if !foundHLSNotice {
		t.Fatalf("expected HLS missing reason %q in %+v", XHLSOnlyMissingReason, metaPartial.Missing)
	}

	// Subtest D: Retweet stored under forwarder's forwards/ with via + Origin, zero folder under original author,
	// and re-caching never moves existing directory.
	const idRetweet = "1000000000000000206"
	planner.setSitePlan(idRetweet, func(_ int) (site.DownloadPlan, error) {
		return site.DownloadPlan{
			Site:             site.X,
			TargetID:         idRetweet,
			Kind:             "image_note",
			AuthorUID:        forwarderRestID,
			AuthorName:       "转帖用户",
			Title:            "转帖外层",
			Desc:             "转帖外层",
			PubTs:            1763810300,
			IsForward:        true,
			OriginID:         "1000000000000000901",
			OriginAuthorUID:  originRestID,
			OriginAuthorName: "原推作者",
			OriginText:       "原推完整正文 auth_token=" + secretTok,
			Images: []site.MediaImageAsset{
				{Index: 1, MediaRole: "image", FromOrigin: true, ImageURLs: []string{"https://pbs.twimg.com/media/SynthImg01.jpg"}},
			},
			Media: strictTransport,
		}, nil
	})
	tRT, err := mgr.EnqueueXWork(context.Background(), XWorkEnqueueRequest{TargetID: idRetweet})
	if err != nil {
		t.Fatalf("EnqueueXWork retweet: %v", err)
	}
	if doneRT := waitForTaskTerminal(t, mgr, tRT.TaskID, 10*time.Second); doneRT.Status != "completed" {
		t.Fatalf("retweet status=%s err=%s", doneRT.Status, doneRT.ErrorMessage)
	}
	metaRT, ok, err := libStore.GetItem("x:dynamic:" + idRetweet)
	if err != nil || !ok {
		t.Fatalf("GetItem(retweet): ok=%v err=%v", ok, err)
	}
	wantRTDir := filepath.Join(libStore.CacheDir(), "x", forwarderRestID, "forwards", idRetweet)
	if filepath.Clean(metaRT.ItemDir) != filepath.Clean(wantRTDir) {
		t.Fatalf("retweet ItemDir=%q, want %q", metaRT.ItemDir, wantRTDir)
	}
	if metaRT.Via == nil || metaRT.Via.Reason != library.ViaForward || metaRT.Via.Site != "x" || metaRT.Via.UID != forwarderRestID {
		t.Fatalf("unexpected retweet Via: %+v", metaRT.Via)
	}
	if _, err := os.Stat(filepath.Join(libStore.CacheDir(), "x", originRestID)); !os.IsNotExist(err) {
		t.Fatalf("original author folder must not be created for X retweet: err=%v", err)
	}

	// Subtest E: Quote post with unavailable/deleted origin still saves outer post cleanly under forwarder's forwards/.
	const idQuoteDeleted = "1000000000000000207"
	planner.setSitePlan(idQuoteDeleted, func(_ int) (site.DownloadPlan, error) {
		return site.DownloadPlan{
			Site:                    site.X,
			TargetID:                idQuoteDeleted,
			Kind:                    "dynamic",
			AuthorUID:               forwarderRestID,
			AuthorName:              "转帖用户",
			Title:                   "引用已删原帖",
			Desc:                    "外层引用评论完整保留",
			PubTs:                   1763810400,
			IsForward:               true,
			OriginID:                "1000000000000000902",
			OriginUnavailable:       true,
			OriginUnavailableReason: "原帖已被作者删除",
			Media:                   strictTransport,
		}, nil
	})
	tQD, err := mgr.EnqueueXWork(context.Background(), XWorkEnqueueRequest{TargetID: idQuoteDeleted})
	if err != nil {
		t.Fatalf("EnqueueXWork quote deleted origin: %v", err)
	}
	if doneQD := waitForTaskTerminal(t, mgr, tQD.TaskID, 10*time.Second); doneQD.Status != "completed" {
		t.Fatalf("quote deleted origin status=%s err=%s", doneQD.Status, doneQD.ErrorMessage)
	}
	metaQD, ok, err := libStore.GetItem("x:dynamic:" + idQuoteDeleted)
	if err != nil || !ok || metaQD.Origin == nil || !metaQD.Origin.Unavailable || metaQD.Origin.UnavailableReason != "原帖已被作者删除" {
		t.Fatalf("unexpected quote deleted origin meta: ok=%v err=%v origin=%+v", ok, err, metaQD.Origin)
	}
	wantQDDir := filepath.Join(libStore.CacheDir(), "x", forwarderRestID, "forwards", idQuoteDeleted)
	if filepath.Clean(metaQD.ItemDir) != filepath.Clean(wantQDDir) {
		t.Fatalf("quote ItemDir=%q, want %q", metaQD.ItemDir, wantQDDir)
	}

	// Re-caching an already-cached retweet without IsForward must NOT move its directory or strip Via.
	planner.setSitePlan(idRetweet, func(_ int) (site.DownloadPlan, error) {
		return site.DownloadPlan{
			Site:       site.X,
			TargetID:   idRetweet,
			Kind:       "image_note",
			AuthorUID:  originRestID,
			AuthorName: "原推作者",
			Title:      "重缓存不移动",
			PubTs:      1763810300,
			IsForward:  false,
			Images:     []site.MediaImageAsset{{Index: 1, MediaRole: "image", ImageURLs: []string{"https://pbs.twimg.com/media/SynthImg01.jpg"}}},
			Media:      strictTransport,
		}, nil
	})
	tRecache, err := mgr.EnqueueXWork(context.Background(), XWorkEnqueueRequest{TargetID: idRetweet})
	if err != nil {
		t.Fatalf("re-enqueue retweet: %v", err)
	}
	if doneRecache := waitForTaskTerminal(t, mgr, tRecache.TaskID, 10*time.Second); doneRecache.Status != "completed" {
		t.Fatalf("recache status=%s err=%s", doneRecache.Status, doneRecache.ErrorMessage)
	}
	metaRecache, _, err := libStore.GetItem("x:dynamic:" + idRetweet)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(metaRecache.ItemDir) != filepath.Clean(wantRTDir) || metaRecache.Via == nil || metaRecache.Via.UID != forwarderRestID {
		t.Fatalf("re-caching moved directory or altered via: dir=%q via=%+v", metaRecache.ItemDir, metaRecache.Via)
	}

	// RebuildIndex + media library filters ("image", "video", "forward") + CountCachedWorks.
	if rep, err := libStore.RebuildIndex(); err != nil || rep.Indexed != 5 {
		t.Fatalf("RebuildIndex = %+v err=%v, want Indexed=5", rep, err)
	}
	imgItems, err := libStore.ListItems(library.FeedFilter{Site: "x", Kind: "image", ExcludeForward: true, Limit: 20})
	if err != nil || len(imgItems) != 3 {
		t.Fatalf("x image filter = %d (%+v) err=%v, want 3 (mixed + pure GIF + partial)", len(imgItems), imgItems, err)
	}
	vidItems, err := libStore.ListItems(library.FeedFilter{Site: "x", Kind: "video", ExcludeForward: true, Limit: 20})
	if err != nil || len(vidItems) != 2 {
		t.Fatalf("x video filter = %d (%+v) err=%v, want 2 (mixed + partial video page; pure GIF excluded)", len(vidItems), vidItems, err)
	}
	fwdItems, err := libStore.ListItems(library.FeedFilter{Site: "x", Kind: "forward", Limit: 20})
	if err != nil || len(fwdItems) != 2 {
		t.Fatalf("x forward filter = %d (%+v) err=%v, want 2 (retweet + quote)", len(fwdItems), fwdItems, err)
	}
	counts, err := libStore.CountCachedWorks([]library.AuthorRef{
		{Site: "x", UID: authorRestID},
		{Site: "x", UID: forwarderRestID},
		{Site: "x", UID: originRestID},
	})
	if err != nil {
		t.Fatalf("CountCachedWorks: %v", err)
	}
	if counts["x:"+authorRestID] != 3 || counts["x:"+forwarderRestID] != 2 || counts["x:"+originRestID] != 0 {
		t.Fatalf("CountCachedWorks = %+v, want author=3 forwarder=2 origin=0", counts)
	}

	// Zero signed-URL or cookie sentinels persisted anywhere.
	assertNoSentinelsInTreeAndDB(
		t,
		libStore.CacheDir(),
		dbPath,
		eventBuf.String(),
		[]string{gifTok, mixTok, secretTok, "auth_token=", "SynthGif03.mp4", "mixed04.mp4", "mixed_only.m3u8"},
		[]string{"video.twimg.com"},
	)
}

func TestXCircuitBreakerIsolatesXOnly(t *testing.T) {
	mp4Bytes := buildProgressiveWithoutElst(t, true, false, 0)
	douyinStarted := make(chan struct{}, 1)
	douyinRelease := make(chan struct{})

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/x/rate_limited.mp4":
			w.WriteHeader(http.StatusTooManyRequests) // 429 -> ErrKindRateLimited
		case "/douyin/ok.mp4":
			select {
			case douyinStarted <- struct{}{}:
			default:
			}
			<-douyinRelease
			serveRangeBytes(w, r, mp4Bytes, "video/mp4", 0, nil)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	mgr, _, srv, _, _ := setupDouyinDownloadTestEnv(t, handler, 2)
	xPlanner := newFakeXSitePlanner()
	dyPlanner := newFakeDouyinWorkPlanner()
	mgr.RegisterWorkPlanner(xPlanner)
	mgr.RegisterWorkPlanner(dyPlanner)

	xLocal := site.XMediaTransport("TRACE-X-Test-UA", true)
	dyLocal := site.DouyinVODTransport("TRACE-Test-UA", true)

	const dyID = "7400000000000000201"
	const dyUID = "MS4wLjABAAAA_test_douyin_x_iso_001"
	dyPlanner.setPlan(dyID, func(_ int) (WorkDownloadPlan, error) {
		return WorkDownloadPlan{
			Site:        site.Douyin,
			Kind:        "video",
			TargetID:    dyID,
			AuthorUID:   dyUID,
			AuthorName:  "抖音并发作者",
			Title:       "X 熔断不影响抖音",
			DurationSec: 2,
			VideoURLs:   []string{srv.URL + "/douyin/ok.mp4"},
			Transport:   dyLocal,
		}, nil
	})
	tDouyin, err := mgr.EnqueueDouyinWork(context.Background(), DouyinWorkEnqueueRequest{AwemeID: dyID})
	if err != nil {
		t.Fatalf("EnqueueDouyinWork: %v", err)
	}
	select {
	case <-douyinStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Douyin task to start")
	}

	const xID1 = "1000000000000000301"
	const xID2 = "1000000000000000302"
	for _, id := range []string{xID1, xID2} {
		id := id
		xPlanner.setSitePlan(id, func(_ int) (site.DownloadPlan, error) {
			return site.DownloadPlan{
				Site:        site.X,
				TargetID:    id,
				Kind:        "single_mp4",
				AuthorUID:   "1000000000000000001",
				AuthorName:  "合成 X 创作者",
				Title:       "限流触发 " + id,
				DurationSec: 2,
				VideoURLs:   []string{srv.URL + "/x/rate_limited.mp4"},
				Media:       xLocal,
			}, nil
		})
	}
	tX1, err := mgr.EnqueueXWork(context.Background(), XWorkEnqueueRequest{TargetID: xID1})
	if err != nil {
		t.Fatalf("EnqueueXWork x1: %v", err)
	}
	tX2, err := mgr.EnqueueXWork(context.Background(), XWorkEnqueueRequest{TargetID: xID2})
	if err != nil {
		t.Fatalf("EnqueueXWork x2: %v", err)
	}

	doneX1 := waitForTaskTerminal(t, mgr, tX1.TaskID, 10*time.Second)
	doneX2 := waitForTaskTerminal(t, mgr, tX2.TaskID, 10*time.Second)
	if doneX1.Status != "paused" || doneX1.PauseReason != site.ErrKindRiskControl || doneX1.ErrorKind != site.ErrKindRateLimited {
		t.Fatalf("x1 status=%q pauseReason=%q errKind=%q, want paused/risk_control/rate_limited", doneX1.Status, doneX1.PauseReason, doneX1.ErrorKind)
	}
	if doneX2.Status != "paused" || doneX2.PauseReason != site.ErrKindRiskControl {
		t.Fatalf("x2 status=%q pauseReason=%q, want paused/risk_control", doneX2.Status, doneX2.PauseReason)
	}
	if xPlanner.reportedCount() == 0 {
		t.Fatal("expected X rate_limited error to be reported to X planner")
	}

	// Release Douyin task and confirm it completes unaffected by X's circuit breaker.
	close(douyinRelease)
	doneDY := waitForTaskTerminal(t, mgr, tDouyin.TaskID, 10*time.Second)
	if doneDY.Status != "completed" {
		t.Fatalf("Douyin task affected by X circuit breaker: status=%q pauseReason=%q err=%q", doneDY.Status, doneDY.PauseReason, doneDY.ErrorMessage)
	}
}
