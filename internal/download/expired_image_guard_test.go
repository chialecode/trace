package download

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
)

func TestExpiredImagesRecoverViaPublicCDNCacheWithoutLeakingSecrets(t *testing.T) {
	placeholderGIF := syntheticWeiboPlaceholderGIF480x360(t)
	recoveredPNG := syntheticPNGMini()
	recoveredJPEG := syntheticJPEGMini()

	var (
		fallbackHits    atomic.Int32
		signedLeakHits  atomic.Int32
		headerLeakFound atomic.Bool
	)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := strings.ToLower(r.Host)
		if host == "" {
			host = strings.ToLower(r.URL.Host)
		}

		// Public CDN fallback mock endpoints on fallback.local.test (intercepted in-memory).
		if strings.HasSuffix(host, ".fallback-cdn.example") {
			fallbackHits.Add(1)
			for _, h := range []string{"Cookie", "Authorization", "X-Csrf-Token", "Referer", "Origin"} {
				if r.Header.Get(h) != "" {
					headerLeakFound.Store(true)
				}
			}
			fullURL := r.URL.String()
			for _, secret := range []string{"ssig", "x-signature", "x-expires", "SUB=", "auth_token", "ct0"} {
				if strings.Contains(fullURL, secret) {
					signedLeakHits.Add(1)
				}
			}
			switch {
			case strings.Contains(r.URL.Path, "/web/0id_/https://wx1.sinaimg.cn/large/001SynthDeadWeibo.jpg"):
				w.Header().Set("Content-Type", "image/png")
				w.Header().Set("Content-Length", strconv.Itoa(len(recoveredPNG)))
				_, _ = w.Write(recoveredPNG)
				return
			case r.URL.Path == "/wsrv" && strings.Contains(r.URL.Query().Get("url"), "pbs.twimg.com/media/SynthDeadX01"):
				w.Header().Set("Content-Type", "image/jpeg")
				w.Header().Set("Content-Length", strconv.Itoa(len(recoveredJPEG)))
				_, _ = w.Write(recoveredJPEG)
				return
			default:
				w.WriteHeader(http.StatusNotFound)
				return
			}
		}

		switch {
		case strings.HasSuffix(host, ".sinaimg.cn") && strings.HasSuffix(r.URL.Path, "/001SynthDeadWeibo.jpg"):
			w.Header().Set("Content-Type", "image/gif")
			w.Header().Set("Content-Length", strconv.Itoa(len(placeholderGIF)))
			_, _ = w.Write(placeholderGIF)

		case strings.HasSuffix(host, ".sinaimg.cn") && strings.HasSuffix(r.URL.Path, "/001SynthDeadGifSlot.gif"):
			w.Header().Set("Content-Type", "image/gif")
			w.Header().Set("Content-Length", strconv.Itoa(len(placeholderGIF)))
			_, _ = w.Write(placeholderGIF)

		case strings.HasSuffix(host, ".sinaimg.cn") && strings.HasSuffix(r.URL.Path, "/001SynthRateLimited.jpg"):
			w.WriteHeader(http.StatusTooManyRequests)

		case host == "pbs.twimg.com" && r.URL.Path == "/media/SynthDeadX01":
			w.WriteHeader(http.StatusNotFound)

		case strings.HasSuffix(host, ".douyinpic.com") && strings.HasSuffix(r.URL.Path, "/signed_dead_note.jpeg"):
			w.WriteHeader(http.StatusForbidden)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	mgr, libStore, _, _, _ := setupDouyinDownloadTestEnv(t, http.NotFoundHandler(), 2)
	mgr.SetMediaTransportWrap(func(_ http.RoundTripper) http.RoundTripper {
		return roundTripFunc(func(req *http.Request) (*http.Response, error) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			resp := rec.Result()
			resp.Request = req
			return resp, nil
		})
	})
	mgr.SetImageCDNFallbackProviders([]site.PublicImageCDNProvider{
		{
			Name:           "photon-test",
			AllowedDomains: []string{"fallback-cdn.example"},
			MaxRedirects:   1,
			BuildURL: func(origin *url.URL, _ string) (string, bool) {
				if origin == nil || origin.RawQuery != "" {
					return "", false
				}
				return "https://photon.fallback-cdn.example/" + origin.Hostname() + origin.EscapedPath() + "?ssl=1", true
			},
		},
		{
			Name:           "wsrv-test",
			AllowedDomains: []string{"fallback-cdn.example"},
			MaxRedirects:   1,
			BuildURL: func(_ *url.URL, clean string) (string, bool) {
				return "https://wsrv.fallback-cdn.example/wsrv?url=" + url.QueryEscape(clean) + "&n=-1", true
			},
		},
		{
			Name:           "wayback-test",
			AllowedDomains: []string{"fallback-cdn.example"},
			MaxRedirects:   2,
			BuildURL: func(_ *url.URL, clean string) (string, bool) {
				return "https://wayback.fallback-cdn.example/web/0id_/" + clean, true
			},
		},
	})

	wbPlanner := newFakeWeiboSitePlanner()
	mgr.RegisterSitePlanner(wbPlanner)
	xPlanner := newFakeXSitePlanner()
	mgr.RegisterWorkPlanner(xPlanner)

	// (A) Weibo expired image (all wx1..wx4 + 1x refresh return 480x360 placeholder GIF) recovers via wayback-test!
	const midRecover = "5000000000007101"
	wbPlanner.setPlan(midRecover, func(_ int) (site.DownloadPlan, error) {
		return site.DownloadPlan{
			Site:       site.Weibo,
			ItemKey:    "weibo:dynamic:" + midRecover,
			TargetID:   midRecover,
			Kind:       "image_note",
			AuthorUID:  "1000000001",
			AuthorName: "合成博主",
			Title:      "失效微博图经公共缓存恢复",
			PubTs:      1763900100,
			Images: []site.MediaImageAsset{
				{Index: 1, MediaRole: "image", Width: 1080, Height: 1440, ImageURLs: []string{"https://wx1.sinaimg.cn/large/001SynthDeadWeibo.jpg"}},
			},
			Media: site.WeiboMediaTransport("TRACE-Test-UA", false),
		}, nil
	})
	tWB, err := mgr.EnqueueWeiboWork(context.Background(), WeiboWorkEnqueueRequest{TargetID: midRecover})
	if err != nil {
		t.Fatalf("EnqueueWeiboWork(recover): %v", err)
	}
	doneWB := waitForTaskTerminal(t, mgr, tWB.TaskID, 10*time.Second)
	if doneWB.Status != "completed" {
		t.Fatalf("Weibo CDN fallback status=%s err=%s, want completed", doneWB.Status, doneWB.ErrorMessage)
	}
	metaWB, ok, err := libStore.GetItem("weibo:dynamic:" + midRecover)
	if err != nil || !ok || metaWB.Status != "complete" || len(metaWB.NotePages) != 1 || metaWB.NotePages[0].Image != "images/01.png" {
		t.Fatalf("unexpected metaWB: ok=%v err=%v status=%q pages=%+v", ok, err, metaWB.Status, metaWB.NotePages)
	}
	gotWBBytes, err := os.ReadFile(filepath.Join(metaWB.ItemDir, "images", "01.png"))
	if err != nil || !bytes.Equal(gotWBBytes, recoveredPNG) {
		t.Fatalf("recovered Weibo image mismatch: err=%v len=%d", err, len(gotWBBytes))
	}

	// (B) X expired image (pbs.twimg.com ?format=jpg&name=orig returns 404) skips Photon and recovers via wsrv-test!
	const xStatusID = "1000000000000000801"
	xPlanner.setWorkPlan(xStatusID, func(_ int) (WorkDownloadPlan, error) {
		return WorkDownloadPlan{
			Site:       site.X,
			ItemKey:    "x:dynamic:" + xStatusID,
			Kind:       "dynamic",
			TargetID:   xStatusID,
			AuthorUID:  "9000000001",
			AuthorName: "合成创作者",
			Title:      "失效 X 原图经公共缓存恢复",
			PubTs:      1763900200,
			Pages: []PlannedNotePage{
				{
					Index:     1,
					Kind:      "image",
					Width:     1600,
					Height:    1200,
					ImageURLs: []string{"https://pbs.twimg.com/media/SynthDeadX01?format=jpg&name=orig"},
				},
			},
			Transport: site.XMediaTransport("TRACE-X-Test-UA", false),
		}, nil
	})
	tX, err := mgr.EnqueueXWork(context.Background(), XWorkEnqueueRequest{TargetID: xStatusID})
	if err != nil {
		t.Fatalf("EnqueueXWork(recover): %v", err)
	}
	doneX := waitForTaskTerminal(t, mgr, tX.TaskID, 10*time.Second)
	if doneX.Status != "completed" {
		t.Fatalf("X CDN fallback status=%s err=%s, want completed", doneX.Status, doneX.ErrorMessage)
	}
	metaX, ok, err := libStore.GetItem("x:dynamic:" + xStatusID)
	if err != nil || !ok || metaX.Status != "complete" || len(metaX.NotePages) != 1 || metaX.NotePages[0].Image != "images/01.jpg" {
		t.Fatalf("unexpected metaX: ok=%v err=%v status=%q pages=%+v", ok, err, metaX.Status, metaX.NotePages)
	}

	// (C) Weibo MediaRole="gif" slot returning Sina's 1-frame 480x360 deleted placeholder GIF must be rejected!
	const midGifDead = "5000000000007102"
	wbPlanner.setPlan(midGifDead, func(_ int) (site.DownloadPlan, error) {
		return site.DownloadPlan{
			Site:       site.Weibo,
			ItemKey:    "weibo:dynamic:" + midGifDead,
			TargetID:   midGifDead,
			Kind:       "image_note",
			AuthorUID:  "1000000001",
			AuthorName: "合成博主",
			Title:      "动图位返回新浪删除占位图",
			PubTs:      1763900300,
			Images: []site.MediaImageAsset{
				{Index: 1, MediaRole: "gif", Width: 640, Height: 480, ImageURLs: []string{"https://wx1.sinaimg.cn/large/001SynthDeadGifSlot.gif"}},
			},
			Media: site.WeiboMediaTransport("TRACE-Test-UA", false),
		}, nil
	})
	tGif, err := mgr.EnqueueWeiboWork(context.Background(), WeiboWorkEnqueueRequest{TargetID: midGifDead})
	if err != nil {
		t.Fatalf("EnqueueWeiboWork(gif dead): %v", err)
	}
	doneGif := waitForTaskTerminal(t, mgr, tGif.TaskID, 10*time.Second)
	if doneGif.Status != "partial" {
		t.Fatalf("dead GIF slot status=%s err=%s, want partial", doneGif.Status, doneGif.ErrorMessage)
	}
	metaGif, ok, err := libStore.GetItem("weibo:dynamic:" + midGifDead)
	if err != nil || !ok || metaGif.Status != "partial" || len(metaGif.Missing) != 1 {
		t.Fatalf("expected partial missing record on dead GIF slot, got ok=%v err=%v meta=%+v", ok, err, metaGif)
	}
	if _, statErr := os.Stat(filepath.Join(metaGif.ItemDir, "images", "01.gif")); !os.IsNotExist(statErr) {
		t.Fatalf("Sina 480x360 placeholder GIF must NOT be saved on GIF slot (statErr=%v)", statErr)
	}

	// (D) HTTP 429 rate-limit and signed Douyin URLs must make ZERO requests to fallback providers.
	hitsBefore := fallbackHits.Load()
	const mid429 = "5000000000007103"
	wbPlanner.setPlan(mid429, func(_ int) (site.DownloadPlan, error) {
		return site.DownloadPlan{
			Site:       site.Weibo,
			ItemKey:    "weibo:dynamic:" + mid429,
			TargetID:   mid429,
			Kind:       "image_note",
			AuthorUID:  "1000000001",
			AuthorName: "合成博主",
			Title:      "限流不走第三方回退",
			PubTs:      1763900400,
			Images: []site.MediaImageAsset{
				{Index: 1, MediaRole: "image", Width: 1080, Height: 1080, ImageURLs: []string{"https://wx1.sinaimg.cn/large/001SynthRateLimited.jpg"}},
			},
			Media: site.WeiboMediaTransport("TRACE-Test-UA", false),
		}, nil
	})
	t429, err := mgr.EnqueueWeiboWork(context.Background(), WeiboWorkEnqueueRequest{TargetID: mid429})
	if err != nil {
		t.Fatalf("EnqueueWeiboWork(429): %v", err)
	}
	_ = waitForTaskTerminal(t, mgr, t429.TaskID, 10*time.Second)

	dyPlanner := newFakeDouyinWorkPlanner()
	mgr.RegisterWorkPlanner(dyPlanner)
	const dyID = "7350000000000099101"
	dyPlanner.setPlan(dyID, func(_ int) (WorkDownloadPlan, error) {
		return WorkDownloadPlan{
			Site:       site.Douyin,
			ItemKey:    "douyin:dynamic:" + dyID,
			Kind:       "dynamic",
			TargetID:   dyID,
			AuthorUID:  "MS4wLjABAAAA_synth_dy_guard_01",
			AuthorName: "合成抖音作者",
			Title:      "带签名抖音图不发往第三方",
			PubTs:      1763900500,
			Pages: []PlannedNotePage{
				{
					Index:     1,
					Kind:      "image",
					Width:     1080,
					Height:    1440,
					ImageURLs: []string{"https://p3-pc-sign.douyinpic.com/tos-cn-i-0813/signed_dead_note.jpeg?x-expires=1763099999&x-signature=SynthSecretSig"},
				},
			},
			Transport: site.DouyinVODTransport("TRACE-DY-UA", false),
		}, nil
	})
	tDY, err := mgr.EnqueueDouyinWork(context.Background(), DouyinWorkEnqueueRequest{AwemeID: dyID})
	if err != nil {
		t.Fatalf("EnqueueDouyinWork(signed): %v", err)
	}
	_ = waitForTaskTerminal(t, mgr, tDY.TaskID, 10*time.Second)

	if gotDelta := fallbackHits.Load() - hitsBefore; gotDelta != 0 {
		t.Fatalf("expected 0 fallback hits on 429 + signed Douyin URL, got %d", gotDelta)
	}
	if headerLeakFound.Load() || signedLeakHits.Load() != 0 {
		t.Fatalf("security boundary violated: headerLeak=%v signedLeakHits=%d", headerLeakFound.Load(), signedLeakHits.Load())
	}
}

func TestValidCachedImagesNeverOverwrittenByExpiredOrDeletedReCache(t *testing.T) {
	placeholderGIF := syntheticWeiboPlaceholderGIF480x360(t)
	origJPG := append([]byte(nil), syntheticJPEGMini()...)
	origPNG := append([]byte(nil), syntheticPNGMini()...)

	var serveExpired atomic.Bool

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := strings.ToLower(r.Host)
		if host == "" {
			host = strings.ToLower(r.URL.Host)
		}
		if serveExpired.Load() {
			if strings.HasSuffix(host, ".sinaimg.cn") {
				w.Header().Set("Content-Type", "image/gif")
				w.Header().Set("Content-Length", strconv.Itoa(len(placeholderGIF)))
				_, _ = w.Write(placeholderGIF)
				return
			}
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch {
		case strings.HasSuffix(host, ".sinaimg.cn") && strings.HasSuffix(r.URL.Path, "/keep_01.jpg"):
			w.Header().Set("Content-Type", "image/jpeg")
			w.Header().Set("Content-Length", strconv.Itoa(len(origJPG)))
			_, _ = w.Write(origJPG)
		case strings.HasSuffix(host, ".sinaimg.cn") && strings.HasSuffix(r.URL.Path, "/keep_02.png"):
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Content-Length", strconv.Itoa(len(origPNG)))
			_, _ = w.Write(origPNG)
		case host == "pbs.twimg.com" && r.URL.Path == "/media/OriginKeep01":
			w.Header().Set("Content-Type", "image/jpeg")
			w.Header().Set("Content-Length", strconv.Itoa(len(origJPG)))
			_, _ = w.Write(origJPG)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	mgr, libStore, _, _, _ := setupDouyinDownloadTestEnv(t, http.NotFoundHandler(), 2)
	mgr.SetMediaTransportWrap(func(_ http.RoundTripper) http.RoundTripper {
		return roundTripFunc(func(req *http.Request) (*http.Response, error) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			resp := rec.Result()
			resp.Request = req
			return resp, nil
		})
	})

	wbPlanner := newFakeWeiboSitePlanner()
	mgr.RegisterSitePlanner(wbPlanner)
	xPlanner := newFakeXSitePlanner()
	mgr.RegisterWorkPlanner(xPlanner)

	// 1. Seed a complete 2-image Weibo post, then switch origin to Sina 480x360 placeholder GIF and re-cache.
	const wbMid = "5000000000007201"
	wbPlanner.setPlan(wbMid, func(_ int) (site.DownloadPlan, error) {
		return site.DownloadPlan{
			Site:       site.Weibo,
			ItemKey:    "weibo:dynamic:" + wbMid,
			TargetID:   wbMid,
			Kind:       "image_note",
			AuthorUID:  "1000000001",
			AuthorName: "合成博主",
			Title:      "重缓存不覆盖已有有效原图",
			PubTs:      1763910100,
			Images: []site.MediaImageAsset{
				{Index: 1, MediaRole: "image", Width: 1440, Height: 1080, ImageURLs: []string{"https://wx1.sinaimg.cn/large/keep_01.jpg"}},
				{Index: 2, MediaRole: "image", Width: 1440, Height: 1080, ImageURLs: []string{"https://wx1.sinaimg.cn/large/keep_02.png"}},
			},
			Media: site.WeiboMediaTransport("TRACE-Test-UA", false),
		}, nil
	})
	t1, err := mgr.EnqueueWeiboWork(context.Background(), WeiboWorkEnqueueRequest{TargetID: wbMid})
	if err != nil {
		t.Fatalf("initial EnqueueWeiboWork: %v", err)
	}
	if done1 := waitForTaskTerminal(t, mgr, t1.TaskID, 10*time.Second); done1.Status != "completed" {
		t.Fatalf("initial Weibo cache status=%s err=%s", done1.Status, done1.ErrorMessage)
	}

	// Also seed an X forward with an origin image before switching origin to deleted/unavailable.
	const xFwdID = "1000000000000000901"
	xPlanner.setWorkPlan(xFwdID, func(_ int) (WorkDownloadPlan, error) {
		if serveExpired.Load() {
			return WorkDownloadPlan{
				Site:                    site.X,
				ItemKey:                 "x:dynamic:" + xFwdID,
				Kind:                    "dynamic",
				TargetID:                xFwdID,
				AuthorUID:               "9000000001",
				AuthorName:              "转发者",
				Desc:                    "转发评论保留",
				PubTs:                   1763910200,
				IsForward:               true,
				OriginID:                "1000000000000000900",
				OriginUnavailable:       true,
				OriginUnavailableReason: "原帖已删除",
				Pages:                   nil,
				Transport:               site.XMediaTransport("TRACE-X-Test-UA", false),
			}, nil
		}
		return WorkDownloadPlan{
			Site:       site.X,
			ItemKey:    "x:dynamic:" + xFwdID,
			Kind:       "dynamic",
			TargetID:   xFwdID,
			AuthorUID:  "9000000001",
			AuthorName: "转发者",
			Desc:       "转发评论保留",
			PubTs:      1763910200,
			IsForward:  true,
			OriginID:   "1000000000000000900",
			OriginText: "原帖正文",
			Pages: []PlannedNotePage{
				{Index: 1, Kind: "image", FromOrigin: true, Width: 1200, Height: 900, ImageURLs: []string{"https://pbs.twimg.com/media/OriginKeep01?format=jpg&name=orig"}},
			},
			Transport: site.XMediaTransport("TRACE-X-Test-UA", false),
		}, nil
	})
	tXFwd1, err := mgr.EnqueueXWork(context.Background(), XWorkEnqueueRequest{TargetID: xFwdID})
	if err != nil {
		t.Fatalf("initial EnqueueXWork(forward): %v", err)
	}
	if doneXF1 := waitForTaskTerminal(t, mgr, tXFwd1.TaskID, 10*time.Second); doneXF1.Status != "completed" {
		t.Fatalf("initial X forward status=%s err=%s", doneXF1.Status, doneXF1.ErrorMessage)
	}

	// Now flip origin to return ONLY expired placeholder GIFs / 404s / deleted origin!
	serveExpired.Store(true)

	t2, err := mgr.EnqueueWeiboWork(context.Background(), WeiboWorkEnqueueRequest{TargetID: wbMid})
	if err != nil {
		t.Fatalf("re-cache EnqueueWeiboWork: %v", err)
	}
	done2 := waitForTaskTerminal(t, mgr, t2.TaskID, 10*time.Second)
	if done2.Status != "completed" {
		t.Fatalf("re-cache Weibo status=%s err=%s, want completed (valid cached images preserved)", done2.Status, done2.ErrorMessage)
	}
	metaWB, ok, err := libStore.GetItem("weibo:dynamic:" + wbMid)
	if err != nil || !ok || metaWB.Status != "complete" || len(metaWB.NotePages) != 2 {
		t.Fatalf("re-cached metaWB degraded: ok=%v err=%v status=%q pages=%+v", ok, err, metaWB.Status, metaWB.NotePages)
	}
	disk01, err := os.ReadFile(filepath.Join(metaWB.ItemDir, "images", "01.jpg"))
	if err != nil || !bytes.Equal(disk01, origJPG) {
		t.Fatalf("images/01.jpg was overwritten or corrupted: err=%v len=%d", err, len(disk01))
	}
	disk02, err := os.ReadFile(filepath.Join(metaWB.ItemDir, "images", "02.png"))
	if err != nil || !bytes.Equal(disk02, origPNG) {
		t.Fatalf("images/02.png was overwritten or corrupted: err=%v len=%d", err, len(disk02))
	}

	// 2. Re-caching the X forward after the origin post is deleted must preserve cached images/01.jpg & NotePages!
	tXFwd2, err := mgr.EnqueueXWork(context.Background(), XWorkEnqueueRequest{TargetID: xFwdID})
	if err != nil {
		t.Fatalf("re-cache EnqueueXWork(forward): %v", err)
	}
	doneXF2 := waitForTaskTerminal(t, mgr, tXFwd2.TaskID, 10*time.Second)
	if doneXF2.Status != "completed" {
		t.Fatalf("re-cache X forward status=%s err=%s, want completed", doneXF2.Status, doneXF2.ErrorMessage)
	}
	metaXF, ok, err := libStore.GetItem("x:dynamic:" + xFwdID)
	if err != nil || !ok || metaXF.Status != "complete" || len(metaXF.NotePages) != 1 || metaXF.NotePages[0].Image != "images/01.jpg" {
		t.Fatalf("X forward lost previously cached origin image: ok=%v err=%v status=%q pages=%+v", ok, err, metaXF.Status, metaXF.NotePages)
	}
	diskXF, err := os.ReadFile(filepath.Join(metaXF.ItemDir, "images", "01.jpg"))
	if err != nil || !bytes.Equal(diskXF, origJPG) {
		t.Fatalf("X forward images/01.jpg overwritten: err=%v len=%d", err, len(diskXF))
	}

	// 3. Even if library.db has no row yet, an existing TRACE-owned item.json + valid images/01.jpg on disk is preserved!
	const wbUnindexed = "5000000000007202"
	unindexedDir := filepath.Join(libStore.CacheDir(), "weibo", "1000000001", "posts", wbUnindexed)
	if err := os.MkdirAll(filepath.Join(unindexedDir, "images"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unindexedDir, "images", "01.jpg"), origJPG, 0o600); err != nil {
		t.Fatal(err)
	}
	sumJPG := sha256.Sum256(origJPG)
	rawItemJSON, err := json.Marshal(library.ItemMetadata{
		Version: 1,
		Site:    "weibo",
		Kind:    "dynamic",
		ID:      "dynamic:" + wbUnindexed,
		Status:  "complete",
		Author:  library.ItemAuthor{Site: "weibo", UID: "1000000001", Name: "合成博主"},
		Media: []library.MediaFile{{
			Role:   "image",
			Path:   "images/01.jpg",
			Bytes:  int64(len(origJPG)),
			SHA256: hex.EncodeToString(sumJPG[:]),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unindexedDir, "item.json"), rawItemJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	wbPlanner.setPlan(wbUnindexed, func(_ int) (site.DownloadPlan, error) {
		return site.DownloadPlan{
			Site:       site.Weibo,
			ItemKey:    "weibo:dynamic:" + wbUnindexed,
			TargetID:   wbUnindexed,
			Kind:       "image_note",
			AuthorUID:  "1000000001",
			AuthorName: "合成博主",
			Title:      "仅磁盘存在清单仍保护已有图片",
			PubTs:      1763910300,
			Images: []site.MediaImageAsset{
				{Index: 1, MediaRole: "image", Width: 1440, Height: 1080, ImageURLs: []string{"https://wx1.sinaimg.cn/large/keep_01.jpg"}},
			},
			Media: site.WeiboMediaTransport("TRACE-Test-UA", false),
		}, nil
	})
	tUnidx, err := mgr.EnqueueWeiboWork(context.Background(), WeiboWorkEnqueueRequest{TargetID: wbUnindexed})
	if err != nil {
		t.Fatalf("EnqueueWeiboWork(unindexed): %v", err)
	}
	if doneUnidx := waitForTaskTerminal(t, mgr, tUnidx.TaskID, 10*time.Second); doneUnidx.Status != "completed" {
		t.Fatalf("unindexed owned item status=%s err=%s, want completed", doneUnidx.Status, doneUnidx.ErrorMessage)
	}
	diskUnidx, err := os.ReadFile(filepath.Join(unindexedDir, "images", "01.jpg"))
	if err != nil || !bytes.Equal(diskUnidx, origJPG) {
		t.Fatalf("unindexed owned images/01.jpg corrupted: err=%v", err)
	}

	// 4. INV-09 safety: an UNOWNED user file (no library.db row and no item.json) is NEVER silently adopted or overwritten.
	serveExpired.Store(false)
	const wbForeign = "5000000000007203"
	foreignDir := filepath.Join(libStore.CacheDir(), "weibo", "1000000001", "posts", wbForeign, "images")
	if err := os.MkdirAll(foreignDir, 0o700); err != nil {
		t.Fatal(err)
	}
	userBytes := append([]byte(nil), origPNG...)
	if err := os.WriteFile(filepath.Join(foreignDir, "01.jpg"), userBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	wbPlanner.setPlan(wbForeign, func(_ int) (site.DownloadPlan, error) {
		return site.DownloadPlan{
			Site:       site.Weibo,
			ItemKey:    "weibo:dynamic:" + wbForeign,
			TargetID:   wbForeign,
			Kind:       "image_note",
			AuthorUID:  "1000000001",
			AuthorName: "合成博主",
			Title:      "未登记用户文件冲突保护",
			PubTs:      1763910400,
			Images: []site.MediaImageAsset{
				{Index: 1, MediaRole: "image", Width: 1440, Height: 1080, ImageURLs: []string{"https://wx1.sinaimg.cn/large/keep_01.jpg"}},
			},
			Media: site.WeiboMediaTransport("TRACE-Test-UA", false),
		}, nil
	})
	tForeign, err := mgr.EnqueueWeiboWork(context.Background(), WeiboWorkEnqueueRequest{TargetID: wbForeign})
	if err != nil {
		t.Fatalf("EnqueueWeiboWork(foreign): %v", err)
	}
	doneForeign := waitForTaskTerminal(t, mgr, tForeign.TaskID, 10*time.Second)
	if doneForeign.Status != "failed" || doneForeign.ErrorKind != site.ErrKindStorage {
		t.Fatalf("expected failed/storage on unowned foreign file, got status=%s kind=%s msg=%s",
			doneForeign.Status, doneForeign.ErrorKind, doneForeign.ErrorMessage)
	}
	stillUserBytes, err := os.ReadFile(filepath.Join(foreignDir, "01.jpg"))
	if err != nil || !bytes.Equal(stillUserBytes, userBytes) {
		t.Fatalf("unowned foreign file was modified: err=%v", err)
	}
}
