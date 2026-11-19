package download

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/site"
)

// A media redirect leaves the allowed domains only when the transport
// allows public redirects (Douyin music): then any public HTTPS name on
// port 443 is followed, while plain HTTP, IP literals and local names stay
// refused. Other media keep the allow list.
func TestMediaRedirectsFollowPublicHostsOnlyWhenAllowed(t *testing.T) {
	hop := func(spec site.MediaTransportSpec, target string) error {
		t.Helper()
		client := newTransportHTTPClient(spec)
		first, _ := http.NewRequest(http.MethodGet, "https://lf9-music-east.douyinstatic.com/obj/ies-music/synthetic.mp3", nil)
		next, err := http.NewRequest(http.MethodGet, target, nil)
		if err != nil {
			t.Fatal(err)
		}
		return client.CheckRedirect(next, []*http.Request{first})
	}
	vod := site.DouyinVODTransport("", false)
	music := vod
	music.PublicRedirects = true

	if err := hop(vod, "https://edge.example-cdn.net/a.mp3"); site.KindOf(err) != site.ErrKindBlocked {
		t.Fatalf("VOD redirect off the allow list = %v, want blocked", err)
	}
	if err := hop(music, "https://edge.example-cdn.net/a.mp3"); err != nil {
		t.Fatalf("music redirect to a public HTTPS host = %v, want followed", err)
	}
	for _, target := range []string{"http://edge.example-cdn.net/a.mp3", "https://203.0.113.7/a.mp3", "https://cdn.local/a.mp3", "https://edge.example-cdn.net:8443/a.mp3"} {
		if err := hop(music, target); site.KindOf(err) != site.ErrKindBlocked {
			t.Errorf("music redirect to %s = %v, want blocked", target, err)
		}
	}
}

// The missing-music reason names the class and the site error's words.
func TestMusicFailureReasonNamesTheCause(t *testing.T) {
	err := &site.Error{Site: site.Douyin, Kind: site.ErrKindBlocked, Message: "媒体域名不在允许列表中: edge.example-cdn.net"}
	got := musicFailureReason(err)
	if got != "配乐下载失败 (blocked)：媒体域名不在允许列表中: edge.example-cdn.net" {
		t.Fatalf("reason = %q", got)
	}
	if got := musicFailureReason(errors.New("plain")); strings.Contains(got, "：") {
		t.Fatalf("reason for a plain error = %q", got)
	}
}

func TestDouyinBytednsdocMusicHostAllowed(t *testing.T) {
	vod := site.DouyinVODTransport("", false)
	if err := vod.ValidateURL("https://lf3-static.bytednsdoc.com/obj/eden-cn/synthetic/no_sound.mp3"); err != nil {
		t.Fatalf("bytednsdoc.com HTTPS music URL rejected: %v", err)
	}
	for _, bad := range []string{
		"http://lf3-static.bytednsdoc.com/obj/eden-cn/no_sound.mp3",
		"https://lf3-static.bytednsdoc.com:8443/obj/eden-cn/no_sound.mp3",
		"https://203.0.113.10/no_sound.mp3",
		"https://unlisted.example.net/no_sound.mp3",
	} {
		if err := vod.ValidateURL(bad); site.KindOf(err) != site.ErrKindBlocked {
			t.Errorf("ValidateURL(%q) = %v, want blocked", bad, err)
		}
	}
}

type playRedirectingPlanner struct {
	*fakeDouyinWorkPlanner
	resolve func(ctx context.Context, playURL string) (string, error)
}

func (p *playRedirectingPlanner) ResolvePlayRedirect(ctx context.Context, playURL string) (string, error) {
	return p.resolve(ctx, playURL)
}

func TestDouyinNeedSetTokenPlayRedirectFallbackAndPartialReason(t *testing.T) {
	mp4Bytes := buildProgressiveWithoutElst(t, true, true, 0)
	coverJPEG := syntheticJPEGMini()
	musicMP3 := syntheticMP3Mini()

	var direct403Hits atomic.Int32
	var resolverHits atomic.Int32
	var cdnCookieLeaks atomic.Int32
	var edgeRedirectHits atomic.Int32

	mux := http.NewServeMux()
	checkNoCookie := func(r *http.Request) {
		if r.Header.Get("Cookie") != "" {
			cdnCookieLeaks.Add(1)
		}
	}
	mux.HandleFunc("/cdn/cover.jpg", func(w http.ResponseWriter, r *http.Request) {
		checkNoCookie(r)
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(coverJPEG)
	})
	mux.HandleFunc("/cdn/music.mp3", func(w http.ResponseWriter, r *http.Request) {
		checkNoCookie(r)
		w.Header().Set("Content-Type", "audio/mpeg")
		serveRangeBytes(w, r, musicMP3, "audio/mpeg", 0, nil)
	})
	mux.HandleFunc("/cdn/music-missing.mp3", func(w http.ResponseWriter, r *http.Request) {
		checkNoCookie(r)
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/cdn/direct-403.mp4", func(w http.ResponseWriter, r *http.Request) {
		checkNoCookie(r)
		direct403Hits.Add(1)
		w.WriteHeader(http.StatusForbidden)
	})
	mux.HandleFunc("/cdn/resolved-first.mp4", func(w http.ResponseWriter, r *http.Request) {
		checkNoCookie(r)
		edgeRedirectHits.Add(1)
		w.Header().Set("Location", "/cdn/edge-final.mp4")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/cdn/edge-final.mp4", func(w http.ResponseWriter, r *http.Request) {
		checkNoCookie(r)
		serveRangeBytes(w, r, mp4Bytes, "video/mp4", 0, nil)
	})

	mgr, libStore, srv, _, _ := setupDouyinDownloadTestEnv(t, mux, 2)
	planner := &playRedirectingPlanner{
		fakeDouyinWorkPlanner: newFakeDouyinWorkPlanner(),
		resolve: func(_ context.Context, playURL string) (string, error) {
			resolverHits.Add(1)
			u, err := url.Parse(playURL)
			if err != nil || strings.TrimSuffix(u.Path, "/") != "/aweme/v1/play" {
				return "", &site.Error{Site: site.Douyin, Kind: site.ErrKindBlocked, Message: "unexpected play URL"}
			}
			return srv.URL + "/cdn/resolved-first.mp4", nil
		},
	}
	mgr.RegisterWorkPlanner(planner)

	transport := site.DouyinVODTransport("", true)
	const secUID = "MS4wLjABAAAA_need_set_token_author"

	// 1. Mixed note with need_set_token=true on both a video page and a live-photo page:
	// must go straight to PlayURLResolver (0 direct 403 hits), follow CDN edge redirect with 0 Cookie, and complete.
	planner.setPlan("7600000000000000101", func(_ int) (WorkDownloadPlan, error) {
		return WorkDownloadPlan{
			Site:       site.Douyin,
			Kind:       "dynamic",
			TargetID:   "7600000000000000101",
			Title:      "需要跳转的混合图文",
			AuthorUID:  secUID,
			AuthorName: "测试作者",
			CoverURLs:  []string{srv.URL + "/cdn/cover.jpg"},
			Music: &PlannedMusic{
				Title:    "背景音乐",
				PlayURLs: []string{srv.URL + "/cdn/music.mp3"},
			},
			Transport: transport,
			Pages: []PlannedNotePage{
				{
					Index:        1,
					Kind:         "video",
					ImageURLs:    []string{srv.URL + "/cdn/cover.jpg"},
					VideoURLs:    []string{srv.URL + "/cdn/direct-403.mp4", srv.URL + "/aweme/v1/play/?video_id=p1&line=0"},
					NeedSetToken: true,
					DurationSec:  2,
				},
				{
					Index:        2,
					Kind:         "live_photo",
					ImageURLs:    []string{srv.URL + "/cdn/cover.jpg"},
					VideoURLs:    []string{srv.URL + "/cdn/direct-403.mp4", srv.URL + "/aweme/v1/play/?video_id=p2&line=0"},
					NeedSetToken: true,
					DurationSec:  2,
				},
			},
		}, nil
	})

	tk1, err := mgr.EnqueueDouyinWork(context.Background(), DouyinWorkEnqueueRequest{
		AwemeID: "7600000000000000101",
		Source:  "manual",
	})
	if err != nil {
		t.Fatalf("EnqueueDouyinWork note: %v", err)
	}
	done1 := waitForTaskStatus(t, mgr, tk1.TaskID, "completed", 10*time.Second)
	if done1.Status != "completed" || done1.ErrorMessage != "" {
		t.Fatalf("need_set_token note task = %+v, want completed with empty error", done1)
	}
	if direct403Hits.Load() != 0 {
		t.Fatalf("need_set_token=true hit direct 403 URL %d times, want 0", direct403Hits.Load())
	}
	if resolverHits.Load() != 2 || edgeRedirectHits.Load() != 2 {
		t.Fatalf("resolverHits=%d edgeRedirectHits=%d, want 2/2", resolverHits.Load(), edgeRedirectHits.Load())
	}
	item1, ok, err := libStore.GetItem(done1.ItemKey)
	if err != nil || !ok || item1.Status != "complete" || len(item1.Missing) != 0 || item1.Music == nil || item1.Music.File == "" {
		t.Fatalf("saved note item1 = %+v ok=%v err=%v", item1, ok, err)
	}

	// 2. Normal video (!need_set_token) whose direct CDN returns persistent 403 falls back to PlayURLResolver and completes.
	planner.setPlan("7600000000000000102", func(_ int) (WorkDownloadPlan, error) {
		return WorkDownloadPlan{
			Site:        site.Douyin,
			Kind:        "video",
			TargetID:    "7600000000000000102",
			Title:       "直链403回退视频",
			AuthorUID:   secUID,
			AuthorName:  "测试作者",
			CoverURLs:   []string{srv.URL + "/cdn/cover.jpg"},
			VideoURLs:   []string{srv.URL + "/cdn/direct-403.mp4", srv.URL + "/aweme/v1/play/?video_id=v2&line=0"},
			DurationSec: 2,
			Transport:   transport,
		}, nil
	})
	tk2, err := mgr.EnqueueDouyinWork(context.Background(), DouyinWorkEnqueueRequest{
		AwemeID: "7600000000000000102",
		Source:  "manual",
	})
	if err != nil {
		t.Fatalf("EnqueueDouyinWork video: %v", err)
	}
	done2 := waitForTaskStatus(t, mgr, tk2.TaskID, "completed", 10*time.Second)
	if done2.Status != "completed" {
		t.Fatalf("fallback video task = %+v, want completed", done2)
	}
	if direct403Hits.Load() == 0 {
		t.Fatalf("expected normal video to try direct CDN before falling back")
	}
	if cdnCookieLeaks.Load() != 0 {
		t.Fatalf("detected %d Cookie leaks to CDN", cdnCookieLeaks.Load())
	}

	// 3. Partial note preserves concise missing reason on Task.ErrorMessage, and ListTasks backfills legacy empty partial reasons.
	planner.setPlan("7600000000000000103", func(_ int) (WorkDownloadPlan, error) {
		return WorkDownloadPlan{
			Site:       site.Douyin,
			Kind:       "dynamic",
			TargetID:   "7600000000000000103",
			Title:      "缺配乐的部分完成图文",
			AuthorUID:  secUID,
			AuthorName: "测试作者",
			CoverURLs:  []string{srv.URL + "/cdn/cover.jpg"},
			Music: &PlannedMusic{
				Title:    "缺失配乐",
				PlayURLs: []string{srv.URL + "/cdn/music-missing.mp3"},
			},
			Transport: transport,
			Pages: []PlannedNotePage{
				{
					Index:     1,
					Kind:      "image",
					ImageURLs: []string{srv.URL + "/cdn/cover.jpg"},
				},
			},
		}, nil
	})
	tk3, err := mgr.EnqueueDouyinWork(context.Background(), DouyinWorkEnqueueRequest{
		AwemeID: "7600000000000000103",
		Source:  "manual",
	})
	if err != nil {
		t.Fatalf("EnqueueDouyinWork partial note: %v", err)
	}
	done3 := waitForTaskStatus(t, mgr, tk3.TaskID, "partial", 10*time.Second)
	if done3.ErrorMessage == "" || !strings.Contains(done3.ErrorMessage, "配乐下载失败") {
		t.Fatalf("partial task ErrorMessage = %q, want missing music reason preserved", done3.ErrorMessage)
	}
	// Simulate a legacy DB row whose error_message was cleared; ListTasks and GetTask must backfill it from item.Missing.
	if _, err := mgr.db.Exec(`UPDATE download_tasks SET error_message = '' WHERE task_id = ?`, done3.TaskID); err != nil {
		t.Fatalf("clear error_message: %v", err)
	}
	got3, ok, err := mgr.GetTask(done3.TaskID)
	if err != nil || !ok || !strings.Contains(got3.ErrorMessage, "配乐下载失败") {
		t.Fatalf("GetTask backfilled ErrorMessage = %q (ok=%v err=%v), want missing music reason", got3.ErrorMessage, ok, err)
	}
	listed, err := mgr.ListTasks()
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	foundBackfilled := false
	for _, tk := range listed {
		if tk.TaskID == done3.TaskID && strings.Contains(tk.ErrorMessage, "配乐下载失败") {
			foundBackfilled = true
		}
	}
	if !foundBackfilled {
		t.Fatalf("ListTasks did not backfill missing reason for partial task %s", done3.TaskID)
	}
}
