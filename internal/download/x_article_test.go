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

	"github.com/chialecode/trace/internal/site"
)

func TestXArticleDownloadCoverInlineImagesDEC075AndZeroRemoteURLsInManifest(t *testing.T) {
	jpegPayload := syntheticJPEGMini()
	const (
		authorRestID = "1000000000000000001"
		statusID     = "1000000000000000601"
		secretTok    = "XArticleSecretTokenSentinel99"
	)

	var (
		mu             sync.Mutex
		seenCookies    []string
		seenReferers   []string
		coverHits      atomic.Int32
		inline1Hits    atomic.Int32
		inline2Hits    atomic.Int32
		degradeOnRetry atomic.Bool
	)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if c := r.Header.Get("Cookie"); c != "" {
			seenCookies = append(seenCookies, c)
		}
		if ref := r.Header.Get("Referer"); ref != "" {
			seenReferers = append(seenReferers, ref)
		}
		mu.Unlock()

		host := strings.ToLower(r.Host)
		if host == "" {
			host = strings.ToLower(r.URL.Host)
		}
		if host != "pbs.twimg.com" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch r.URL.Path {
		case "/media/SynthArtCover.jpg", "/media/SynthArtCover":
			coverHits.Add(1)
			if degradeOnRetry.Load() {
				// Simulate expired/broken HTML on re-cache to verify local valid cover is preserved (DEC-075).
				w.Header().Set("Content-Type", "text/html")
				_, _ = w.Write([]byte("<html>deleted</html>"))
				return
			}
			w.Header().Set("Content-Type", "image/jpeg")
			w.Header().Set("Content-Length", strconv.Itoa(len(jpegPayload)))
			_, _ = w.Write(jpegPayload)
		case "/media/SynthArtInline1_bad.jpg", "/media/SynthArtInline1_bad":
			// First candidate returns disguised HTML (fails DEC-075 magic check), must fall back to second candidate!
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("<html>placeholder error page</html>"))
		case "/media/SynthArtInline1_ok.jpg", "/media/SynthArtInline1_ok":
			inline1Hits.Add(1)
			w.Header().Set("Content-Type", "image/jpeg")
			w.Header().Set("Content-Length", strconv.Itoa(len(jpegPayload)))
			_, _ = w.Write(jpegPayload)
		case "/media/SynthArtInline2.jpg", "/media/SynthArtInline2":
			inline2Hits.Add(1)
			w.Header().Set("Content-Type", "image/jpeg")
			w.Header().Set("Content-Length", strconv.Itoa(len(jpegPayload)))
			_, _ = w.Write(jpegPayload)
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

	planner := newFakeXSitePlanner()
	mgr.RegisterWorkPlanner(planner)

	buildArticlePlan := func() WorkDownloadPlan {
		return WorkDownloadPlan{
			Site:           site.X,
			ItemKey:        "x:dynamic:" + statusID,
			TargetID:       statusID,
			Kind:           "text_only",
			CanonicalURL:   "https://x.com/synth_writer/status/" + statusID,
			AuthorUID:      authorRestID,
			AuthorUniqueID: "synth_writer",
			AuthorName:     "合成专栏作者",
			Title:          "X Articles 完整离线下载测试",
			Desc:           "文章纯文本回退正文：覆盖封面图、内嵌图与推文引用。",
			PubTs:          1763800600,
			Transport:      site.XMediaTransport("TRACE-X-Test-UA", false),
			Article: &PlannedArticle{
				Title:       "X Articles 完整离线下载测试",
				PlainText:   "文章纯文本回退正文：覆盖封面图、内嵌图与推文引用。",
				CoverURLs:   []string{"https://pbs.twimg.com/media/SynthArtCover.jpg?format=jpg&name=orig&token=" + secretTok},
				CoverWidth:  1600,
				CoverHeight: 900,
				Blocks: []PlannedArticleBlock{
					{Type: "header-one", Text: "一、离线归档目标"},
					{Type: "unstyled", Text: "参考规格文档。", Links: []site.TextLink{{Label: "规格文档", URL: "https://example.com/spec"}}},
					{
						Type:      "atomic",
						MediaID:   "art_img_1",
						ImageURLs: []string{"https://pbs.twimg.com/media/SynthArtInline1_bad.jpg", "https://pbs.twimg.com/media/SynthArtInline1_ok.jpg?format=jpg&name=orig"},
						Width:     1200,
						Height:    800,
					},
					{
						Type:      "atomic",
						MediaID:   "art_img_2",
						ImageURLs: []string{"https://pbs.twimg.com/media/SynthArtInline2.jpg?format=jpg&name=orig"},
						Width:     1080,
						Height:    720,
					},
					{Type: "atomic", TweetID: "1791257321273778888"},
				},
			},
		}
	}

	planner.setWorkPlan(statusID, func(_ int) (WorkDownloadPlan, error) {
		return buildArticlePlan(), nil
	})

	task, err := mgr.EnqueueXWork(context.Background(), XWorkEnqueueRequest{TargetID: statusID})
	if err != nil {
		t.Fatalf("EnqueueXWork article failed: %v", err)
	}
	done := waitForTaskTerminal(t, mgr, task.TaskID, 10*time.Second)
	if done.Status != "completed" {
		t.Fatalf("article download status=%s err=%s", done.Status, done.ErrorMessage)
	}

	// Verify 0 cookies & 0 referers leaked on CDN image fetches.
	mu.Lock()
	if len(seenCookies) > 0 || len(seenReferers) > 0 {
		t.Fatalf("CDN requests leaked headers: cookies=%v referers=%v", seenCookies, seenReferers)
	}
	mu.Unlock()
	if coverHits.Load() < 1 || inline1Hits.Load() < 1 || inline2Hits.Load() < 1 {
		t.Fatalf("expected cover and both inline images fetched: cover=%d img1=%d img2=%d", coverHits.Load(), inline1Hits.Load(), inline2Hits.Load())
	}

	// Verify persisted item.json metadata.
	meta, ok, err := libStore.GetItem("x:dynamic:" + statusID)
	if err != nil || !ok {
		t.Fatalf("GetItem article failed: ok=%v err=%v", ok, err)
	}
	if meta.Version != 1 {
		t.Fatalf("item.json Version = %d, want 1", meta.Version)
	}
	if meta.TypeLabel != "文章" || meta.DynamicType != "article" {
		t.Fatalf("TypeLabel=%q DynamicType=%q, want 文章/article", meta.TypeLabel, meta.DynamicType)
	}
	if !strings.Contains(meta.Text, "文章纯文本回退正文") {
		t.Fatalf("meta.Text plain-text fallback missing: %q", meta.Text)
	}
	if meta.Article == nil {
		t.Fatal("meta.Article is nil")
	}
	if meta.Article.CoverRel != "cover.jpg" {
		t.Fatalf("Article.CoverRel = %q, want cover.jpg", meta.Article.CoverRel)
	}
	if len(meta.Article.Blocks) != 5 {
		t.Fatalf("Article.Blocks len = %d, want 5", len(meta.Article.Blocks))
	}
	if meta.Article.Blocks[2].MediaRel != "images/01.jpg" || meta.Article.Blocks[3].MediaRel != "images/02.jpg" {
		t.Fatalf("inline MediaRel = (%q, %q), want (images/01.jpg, images/02.jpg)", meta.Article.Blocks[2].MediaRel, meta.Article.Blocks[3].MediaRel)
	}
	if meta.Article.Blocks[4].TweetID != "1791257321273778888" {
		t.Fatalf("embedded TweetID = %q", meta.Article.Blocks[4].TweetID)
	}

	// Verify cover + both inline images are ALL registered in meta.Media and exist on disk.
	mediaPaths := map[string]string{}
	for _, mf := range meta.Media {
		mediaPaths[mf.Path] = mf.Role
		if _, err := os.Stat(filepath.Join(meta.ItemDir, filepath.FromSlash(mf.Path))); err != nil {
			t.Fatalf("registered media file missing on disk (%s): %v", mf.Path, err)
		}
	}
	if mediaPaths["cover.jpg"] != "cover" || mediaPaths["images/01.jpg"] != "image" || mediaPaths["images/02.jpg"] != "image" {
		t.Fatalf("meta.Media missing article assets: %+v", mediaPaths)
	}

	// Verify ZERO remote CDN URLs or secret tokens persisted in raw item.json!
	rawManifest, err := os.ReadFile(filepath.Join(meta.ItemDir, "item.json"))
	if err != nil {
		t.Fatal(err)
	}
	rawStr := string(rawManifest)
	for _, forbidden := range []string{"pbs.twimg.com", secretTok, "SynthArtCover", "SynthArtInline", "?format="} {
		if strings.Contains(rawStr, forbidden) {
			t.Fatalf("item.json leaked forbidden remote URL/token %q:\n%s", forbidden, rawStr)
		}
	}

	// Verify non-forced re-cache reuses valid local cover/inline assets without clobbering them even if remote degrades!
	degradeOnRetry.Store(true)
	hitsBefore := coverHits.Load()
	task2, err := mgr.EnqueueXWork(context.Background(), XWorkEnqueueRequest{TargetID: statusID})
	if err != nil {
		t.Fatalf("re-cache EnqueueXWork failed: %v", err)
	}
	done2 := waitForTaskTerminal(t, mgr, task2.TaskID, 10*time.Second)
	if done2.Status != "completed" {
		t.Fatalf("re-cache status=%s err=%s", done2.Status, done2.ErrorMessage)
	}
	if coverHits.Load() != hitsBefore {
		t.Fatalf("re-cache should reuse valid local cover.jpg without refetching: before=%d after=%d", hitsBefore, coverHits.Load())
	}
}
