package ui

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/service"
	"github.com/egoist/mygo/ui"
)

type dec089StubBackend struct {
	service.BackendAPI
	mu            sync.Mutex
	schedulers    map[string]service.SiteSchedulerStatus
	queues        []service.CacheQueueTask
	probedSites   []string
	resumedQueues []string
	probeErr      error
}

func (s *dec089StubBackend) SchedulerStatuses() map[string]service.SiteSchedulerStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]service.SiteSchedulerStatus, len(s.schedulers))
	for k, v := range s.schedulers {
		out[k] = v
	}
	return out
}

func (s *dec089StubBackend) ProbeSiteBackoff(_ context.Context, siteID string) (service.SiteSchedulerStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.probedSites = append(s.probedSites, siteID)
	if s.probeErr != nil {
		return s.schedulers[siteID], s.probeErr
	}
	st := s.schedulers[siteID]
	st.BackingOff = false
	st.BackoffSummary = ""
	st.BackoffUntil = ""
	s.schedulers[siteID] = st
	return st, nil
}

func (s *dec089StubBackend) ListCacheQueue() []service.CacheQueueTask {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]service.CacheQueueTask(nil), s.queues...)
}

func (s *dec089StubBackend) ResumeCacheQueue(id string) (service.CacheQueueTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resumedQueues = append(s.resumedQueues, id)
	for i := range s.queues {
		if s.queues[i].ID == id {
			s.queues[i].Status = service.CacheQueueRunning
			s.queues[i].StopReason = ""
			s.queues[i].NextRunAt = ""
			return s.queues[i], nil
		}
	}
	return service.CacheQueueTask{}, nil
}

func (s *dec089StubBackend) StatusOf(siteID string) credential.Status {
	if siteID == "weibo" {
		return credential.Status{Site: "weibo", LoggedIn: true, State: "logged_in", UID: "500000100", Uname: "微博测试号"}
	}
	return s.BackendAPI.StatusOf(siteID)
}

func (s *dec089StubBackend) HasAnyLogin() bool { return true }

func TestDEC089SettingsProbeQueueRetryAndWeiboMutedBanner(t *testing.T) {
	baseApp, rawStub, cleanup := newWeiboUITestHarness(t)
	defer cleanup()

	if _, err := rawStub.raw.ImportCookie(context.Background(), "SESSDATA=synth_bili; bili_jct=csrf; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}

	stub := &dec089StubBackend{
		BackendAPI: baseApp.backend,
		schedulers: map[string]service.SiteSchedulerStatus{
			"weibo": {
				Site:            "weibo",
				DisplayName:     "微博",
				BackingOff:      true,
				CurrentInterval: "15 分钟",
				BackoffUntil:    time.Now().Add(15 * time.Minute).UTC().Format(time.RFC3339),
				BackoffSummary:  "微博：因风控放慢到 15 分钟，12:15 恢复",
			},
		},
		queues: []service.CacheQueueTask{
			{
				ID:          "cq_wb_backoff",
				Site:        "weibo",
				User:        "500000001",
				UserName:    "退避队列测试博主",
				List:        "posts",
				ListLabel:   "微博",
				Status:      service.CacheQueueWaitingBackoff,
				StatusText:  "遇到站点风控，等待退避结束",
				StopReason:  "遇到站点风控，等待退避结束",
				NextRunAt:   time.Now().Add(15 * time.Minute).UTC().Format(time.RFC3339),
				CachedCount: 3,
			},
		},
	}

	app := NewApp(stub, nil)
	posted := make(chan func(), 32)
	app.dispatch = func(fn func()) { posted <- fn }
	app.CompleteLogin()
	waitUIIdle(t, app, posted)

	tt := ui.NewTester(app.View, 1280, 860)

	// 1. Settings › 订阅与直播 shows backoff status + "重试恢复" button, clicking triggers ProbeSiteBackoff("weibo").
	app.SettingsCategory = "subscription"
	app.Navigate(RouteSettings)
	tt.Frame()
	if _, ok := tt.Find("退避状态：weibo"); !ok {
		t.Fatalf("settings subscription view missing 退避状态：weibo")
	}
	if _, ok := tt.Find("探测恢复退避：weibo"); !ok {
		t.Fatalf("settings subscription view missing 探测恢复退避：weibo")
	}
	if err := tt.Click("探测恢复退避：weibo"); err != nil {
		t.Fatalf("failed to click 探测恢复退避：weibo: %v", err)
	}
	nextPosted(t, posted)()
	tt.Frame()
	stub.mu.Lock()
	probed := append([]string(nil), stub.probedSites...)
	stub.mu.Unlock()
	if len(probed) != 1 || probed[0] != "weibo" {
		t.Fatalf("probedSites = %v, want [weibo]", probed)
	}
	if _, ok := tt.Find("探测恢复退避：weibo"); ok {
		t.Fatalf("probe button should disappear after backoff cleared")
	}

	// 2. Tasks › 缓存队列 shows "立即重试" on waiting_backoff card and calls ResumeCacheQueue.
	app.OpenCacheQueueTask("cq_wb_backoff")
	tt.Frame()
	if _, ok := tt.Find("立即重试缓存队列：cq_wb_backoff"); !ok {
		t.Fatalf("tasks queue waiting_backoff card missing 立即重试 button")
	}
	if err := tt.Click("立即重试缓存队列：cq_wb_backoff"); err != nil {
		t.Fatalf("failed to click 立即重试缓存队列：cq_wb_backoff: %v", err)
	}
	tt.Frame()
	stub.mu.Lock()
	resumed := append([]string(nil), stub.resumedQueues...)
	stub.mu.Unlock()
	if len(resumed) != 1 || resumed[0] != "cq_wb_backoff" {
		t.Fatalf("resumedQueues = %v, want [cq_wb_backoff]", resumed)
	}

	// 3. Weibo user page with IsMuteUser=true shows warm muted banner & calm empty hint without empty-shell/retry error.
	app.Opening = nil
	app.CurrentWeiboUser = &service.WeiboUserView{
		UID:           "500000010",
		ScreenName:    "禁言测试博主",
		StatusesCount: 200,
		IsMuteUser:    true,
		CanonicalURL:  "https://weibo.com/u/500000010",
	}
	app.CurrentWeiboPosts = nil
	app.WeiboPostsHasMore = false
	app.WeiboPostsAuthExpired = false
	app.WeiboPostsUncertainEmpty = false
	app.WeiboPostsError = ""
	app.WeiboUserListState = &ui.ListState{}
	app.CurrentRoute = RouteWeiboUser
	tt.Frame()
	if _, ok := tt.Find("微博禁言提示"); !ok {
		t.Fatalf("muted Weibo user page missing 微博禁言提示")
	}
	if _, ok := tt.Find("微博禁言空态提示"); !ok {
		t.Fatalf("muted Weibo user page missing 微博禁言空态提示")
	}
	if _, ok := tt.Find("微博空壳提示"); ok {
		t.Fatalf("muted Weibo user page must not render empty-shell error")
	}
	if _, ok := tt.Find("重试加载微博"); ok {
		t.Fatalf("muted Weibo user page must not render retry button")
	}
}
