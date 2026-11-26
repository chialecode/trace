package ui

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/egoist/mygo/ui"
)

type backfillAndForceStub struct {
	service.BackendAPI
	mu           sync.Mutex
	summary      service.BackfillSummaryDTO
	queue        []service.CacheQueueTask
	startedCalls int
	forcedKeys   []string
	cachedByKey  map[string]library.ItemMetadata
}

func (s *backfillAndForceStub) BackfillSummary() (service.BackfillSummaryDTO, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.summary, nil
}

func (s *backfillAndForceStub) ListCacheQueue() []service.CacheQueueTask {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]service.CacheQueueTask, len(s.queue))
	copy(out, s.queue)
	return out
}

func (s *backfillAndForceStub) StartWeiboBackfill(_ context.Context) (service.CacheQueueTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.startedCalls++
	task := service.CacheQueueTask{
		ID:            "cq_bf_1",
		Kind:          service.CacheQueueKindWeiboBackfill,
		Site:          "weibo",
		User:          service.CacheQueueBackfillUser,
		UserName:      "历史缓存补全",
		List:          service.CacheQueueListWeiboBackfill,
		ListLabel:     "历史缓存补全",
		Status:        service.CacheQueueRunning,
		StatusText:    "正在补全微博无水印伴侣图",
		BackfillPhase: "fast_cdn",
		BackfillTotal: s.summary.TotalItems,
		CachedCount:   1,
	}
	s.queue = []service.CacheQueueTask{task}
	return task, nil
}

func (s *backfillAndForceStub) ForceRecacheItem(_ context.Context, itemKey string) (download.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.forcedKeys = append(s.forcedKeys, itemKey)
	return download.Task{TaskID: "force_" + itemKey, Status: "queued"}, nil
}

func (s *backfillAndForceStub) GetLibraryItem(itemKey string) (library.ItemMetadata, bool, error) {
	s.mu.Lock()
	it, ok := s.cachedByKey[itemKey]
	s.mu.Unlock()
	if ok {
		return it, true, nil
	}
	return s.BackendAPI.GetLibraryItem(itemKey)
}

func (s *backfillAndForceStub) States(keys []string) map[string]library.DotState {
	out := s.BackendAPI.States(keys)
	if out == nil {
		out = make(map[string]library.DotState)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range keys {
		if _, ok := s.cachedByKey[k]; ok {
			out[k] = library.DotState{Dot: "green", Label: "已完整缓存"}
		}
	}
	return out
}

func TestSettingsStorageAndTasksQueueBackfillUI(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	posted := make(chan func(), 32)
	app.dispatch = func(fn func()) { posted <- fn }
	drain := func() {
		for {
			select {
			case fn := <-posted:
				fn()
			default:
				return
			}
		}
	}

	seedUIFeed(t, app, 0)
	stub := &backfillAndForceStub{
		BackendAPI: app.backend,
		summary:    service.BackfillSummaryDTO{TotalItems: 0},
	}
	app.backend = stub
	app.BackfillSummary = stub.summary
	app.CacheQueue = nil

	// 1. Settings › 存储 with TotalItems == 0 shows up-to-date copy and no action button.
	app.Navigate(RouteSettings)
	app.SettingsCategory = "storage"
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	if !tt.HasText("历史缓存已全部符合最新标准") {
		t.Fatal("Settings › 存储 should show up-to-date message when TotalItems == 0")
	}
	if _, ok := tt.Find("补全历史缓存"); ok {
		t.Fatal("Settings › 存储 must not show 补全历史缓存 button when TotalItems == 0")
	}

	// 2. Settings › 存储 with TotalItems == 3 shows count + 补全历史缓存 button; clicking starts backfill & opens Tasks queue!
	stub.mu.Lock()
	stub.summary = service.BackfillSummaryDTO{
		WeiboMissingNoWM:       2,
		WeiboLegacyLinks:       1,
		WeiboFastCDNCandidates: 2,
		TotalItems:             3,
	}
	stub.mu.Unlock()
	app.BackfillSummary = stub.summary
	tt.Frame()
	if !tt.HasText("发现 3 条历史微博可补全") {
		t.Fatal("Settings › 存储 should show pending backfill count")
	}
	if err := tt.Click("补全历史缓存"); err != nil {
		t.Fatalf("failed to click 补全历史缓存 button: %v", err)
	}
	// Wait for async requestGuarded goroutine and drain UI dispatch.
	for i := 0; i < 50; i++ {
		drain()
		if app.CurrentRoute == RouteTasks && len(app.CacheQueue) == 1 {
			break
		}
		tt.Frame()
	}
	stub.mu.Lock()
	started := stub.startedCalls
	stub.mu.Unlock()
	if started != 1 || app.CurrentRoute != RouteTasks || app.TasksSection != "queue" {
		t.Fatalf("expected StartWeiboBackfill + navigation to /tasks queue; started=%d route=%s sec=%s", started, app.CurrentRoute, app.TasksSection)
	}

	// 3. Tasks › 缓存队列 renders card titled "历史缓存补全 · 微博" and hides the top prompt bar while active.
	tt.Frame()
	if !tt.HasText("历史缓存补全 · 微博") {
		t.Fatal("Tasks › 缓存队列 must render backfill queue card titled 历史缓存补全 · 微博")
	}
	if _, ok := tt.Find("队列详情计数：cq_bf_1"); !ok {
		t.Fatal("Tasks › 缓存队列 must render backfill progress count element 队列详情计数：cq_bf_1")
	}
	if _, ok := tt.Find("历史缓存补全提示条"); ok {
		t.Fatal("top backfill action bar must be hidden while backfill queue is active")
	}

	// 4. Settings › 存储 switches to "查看补全队列" while active.
	app.Navigate(RouteSettings)
	app.SettingsCategory = "storage"
	tt.Frame()
	if _, ok := tt.Find("查看历史缓存补全队列"); !ok {
		t.Fatal("Settings › 存储 should show 查看补全队列 button when backfill is active")
	}

	// 5. Tasks › 缓存队列 with empty CacheQueue & TotalItems == 3 renders top bar "发现 3 条历史微博可补全" + "一键补全".
	stub.mu.Lock()
	stub.queue = nil
	stub.mu.Unlock()
	app.CacheQueue = nil
	app.Navigate(RouteTasks)
	app.TasksSection = "queue"
	tt.Frame()
	if !tt.HasText("发现 3 条历史微博可补全") {
		t.Fatal("Tasks › 缓存队列 should show 发现 3 条历史微博可补全 top bar even when queue list is empty")
	}
	if _, ok := tt.Find("一键补全历史微博缓存"); !ok {
		t.Fatal("Tasks › 缓存队列 missing 一键补全 button")
	}
}

func TestDetailMenusForceRecacheConfirmModalAllFourSites(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	posted := make(chan func(), 32)
	app.dispatch = func(fn func()) { posted <- fn }
	drain := func() {
		for {
			select {
			case fn := <-posted:
				fn()
			default:
				return
			}
		}
	}

	seedUIFeed(t, app, 0)
	cacheDir := app.backend.GetSettings().CacheDir
	dummyDir := filepath.Join(cacheDir, "dummy")
	if err := os.MkdirAll(dummyDir, 0o700); err != nil {
		t.Fatal(err)
	}

	items := map[string]library.ItemMetadata{
		"weibo:dynamic:5098000000000001": {
			ItemKey: "weibo:dynamic:5098000000000001", Site: "weibo", Kind: "dynamic", ID: "5098000000000001", RawID: "5098000000000001",
			Title: "微博详情测试", Text: "微博详情测试", Status: "complete", ItemDir: dummyDir,
		},
		"weibo:video:1034:5098000000000002": {
			ItemKey: "weibo:video:1034:5098000000000002", Site: "weibo", Kind: "video", ID: "1034:5098000000000002", RawID: "1034:5098000000000002",
			Title: "微博视频测试", Status: "complete", ItemDir: dummyDir,
		},
		"x:dynamic:1898000000000000001": {
			ItemKey: "x:dynamic:1898000000000000001", Site: "x", Kind: "dynamic", ID: "1898000000000000001", RawID: "1898000000000000001",
			Title: "X详情测试", Text: "X详情测试", Status: "complete", ItemDir: dummyDir,
		},
		"douyin:video:7398000000000000001": {
			ItemKey: "douyin:video:7398000000000000001", Site: "douyin", Kind: "video", ID: "7398000000000000001", RawID: "7398000000000000001",
			Title: "抖音详情测试", Status: "complete", ItemDir: dummyDir,
		},
		"bilibili:video:BV1force00001": {
			ItemKey: "bilibili:video:BV1force00001", Site: "bilibili", Kind: "video", ID: "BV1force00001", RawID: "BV1force00001",
			Title: "B站视频测试", Status: "complete", ItemDir: dummyDir,
		},
	}

	stub := &backfillAndForceStub{
		BackendAPI:  app.backend,
		cachedByKey: items,
	}
	app.backend = stub

	tt := ui.NewTester(app.View, 1280, 860)

	cases := []struct {
		name      string
		setup     func()
		menuLabel string
		wantKey   string
	}{
		{
			name: "weibo_post",
			setup: func() {
				it := items["weibo:dynamic:5098000000000001"]
				app.CurrentRoute = RouteWeiboPost
				app.CurrentWeiboVideo = nil
				app.CurrentWeiboPost = &service.WeiboPostView{
					Mid: "5098000000000001", Text: "微博详情测试", CacheKey: it.ItemKey,
					Author: service.WeiboAuthorDTO{UID: "100001", ScreenName: "微博作者"}, CachedItem: &it,
				}
			},
			menuLabel: "微博详情更多",
			wantKey:   "weibo:dynamic:5098000000000001",
		},
		{
			name: "weibo_video",
			setup: func() {
				it := items["weibo:video:1034:5098000000000002"]
				app.CurrentRoute = RouteWeiboPost
				app.CurrentWeiboPost = nil
				app.CurrentWeiboVideo = &service.WeiboVideoView{
					ObjectID: "1034:5098000000000002", Title: "微博视频测试", AuthorUID: "100001", AuthorName: "微博作者",
					CacheKey: it.ItemKey, CachedItem: &it,
				}
			},
			menuLabel: "微博视频详情更多",
			wantKey:   "weibo:video:1034:5098000000000002",
		},
		{
			name: "x_post",
			setup: func() {
				it := items["x:dynamic:1898000000000000001"]
				app.CurrentRoute = RouteXPost
				app.CurrentXPost = &service.XPostDTO{
					ID: "1898000000000000001", Text: "X详情测试", CacheKey: it.ItemKey,
					Author: service.XAuthorDTO{RestID: "190001", Name: "X作者", ScreenName: "xauthor"}, CachedItem: &it,
				}
			},
			menuLabel: "X详情更多",
			wantKey:   "x:dynamic:1898000000000000001",
		},
		{
			name: "douyin_work",
			setup: func() {
				it := items["douyin:video:7398000000000000001"]
				app.CurrentRoute = RouteDouyinWork
				app.CurrentDouyinWork = &service.DouyinWorkView{
					ID: "7398000000000000001", Kind: "video", Title: "抖音详情测试", CacheKey: it.ItemKey,
					Author: service.DouyinAuthorDTO{SecUID: "MS4w_1", Nickname: "抖音作者"},
				}
				app.CurrentLibraryDetail = nil
				_ = it
			},
			menuLabel: "抖音作品更多",
			wantKey:   "douyin:video:7398000000000000001",
		},
		{
			name: "bilibili_video",
			setup: func() {
				app.CurrentRoute = RouteVideo
				app.CurrentLibraryDetail = nil
				app.CurrentVideoItemKey = "bilibili:video:BV1force00001"
				app.CurrentVideo = &bilibili.VideoDetail{
					BVID: "BV1force00001", Title: "B站视频测试",
					Author: bilibili.AuthorInfo{Mid: 20001, Name: "UP主"},
				}
			},
			menuLabel: "视频详情更多",
			wantKey:   "bilibili:video:BV1force00001",
		},
	}

	const expectedModalCopy = "将从源站重新完整下载并覆盖本机已有文件；若源站已删除或失效，仍会保留本机原文件不覆盖。"

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app.PostMenuKey = ""
			app.ForceRecacheConfirm = ForceRecacheConfirmState{}
			app.cacheStateRev++
			tc.setup()
			tt.Frame()

			if err := tt.Click(tc.menuLabel); err != nil {
				t.Fatalf("failed to open menu %q: %v", tc.menuLabel, err)
			}
			tt.Frame()
			if err := tt.Click("强制重新下载"); err != nil {
				t.Fatalf("menu %q missing 强制重新下载 item: %v", tc.menuLabel, err)
			}
			tt.Frame()

			if !app.ForceRecacheConfirm.Open || app.ForceRecacheConfirm.ItemKey != tc.wantKey {
				t.Fatalf("confirm dialog not opened for %q: %+v", tc.wantKey, app.ForceRecacheConfirm)
			}
			if !tt.HasText(expectedModalCopy) {
				t.Fatalf("confirm modal missing expected warning copy %q", expectedModalCopy)
			}
			if err := tt.Click("确认强制重新下载"); err != nil {
				t.Fatalf("failed to click confirm button 确认强制重新下载: %v", err)
			}
			for i := 0; i < 40; i++ {
				drain()
				stub.mu.Lock()
				n := len(stub.forcedKeys)
				stub.mu.Unlock()
				if n > 0 && stub.forcedKeys[n-1] == tc.wantKey {
					break
				}
				tt.Frame()
			}
			stub.mu.Lock()
			found := len(stub.forcedKeys) > 0 && stub.forcedKeys[len(stub.forcedKeys)-1] == tc.wantKey
			stub.mu.Unlock()
			if !found {
				t.Fatalf("ForceRecacheItem not called with %q; got %v", tc.wantKey, stub.forcedKeys)
			}
		})
	}

	// Verify TriggerAuthExpired closes ForceRecacheConfirm.Open.
	app.ForceRecacheConfirm = ForceRecacheConfirmState{Open: true, ItemKey: "weibo:dynamic:5098000000000001"}
	app.TriggerAuthExpired("expired")
	if app.ForceRecacheConfirm.Open {
		t.Fatal("TriggerAuthExpired must close ForceRecacheConfirm modal")
	}
}
