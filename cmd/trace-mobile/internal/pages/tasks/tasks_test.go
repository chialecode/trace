package tasks

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"image/png"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/cmd/trace-mobile/internal/pagekit"
	"github.com/chialecode/trace/cmd/trace-mobile/internal/theme"
	"github.com/chialecode/trace/internal/companionapi"
	"github.com/chialecode/trace/internal/companionclient"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type fakeHost struct {
	mu          sync.Mutex
	connState   pagekit.ConnState
	desktop     pagekit.DesktopSnapshot
	client      *companionclient.Client
	imgCache    *companionclient.MemoryImageCache
	router      *ui.Router
	invalidate  func()
	pending     []func()
	badgeCounts []int
}

func newFakeHost(cli *companionclient.Client) *fakeHost {
	r := ui.NewRouter("/")
	r.SystemStack = true
	return &fakeHost{
		connState: pagekit.ConnConnected,
		desktop: pagekit.DesktopSnapshot{
			ComputerName: "Studio-PC",
			Version:      "0.1.0-dev",
			Channel:      "dev",
			ChannelLabel: "开发版",
			LoggedIn:     true,
		},
		client:   cli,
		imgCache: companionclient.NewMemoryImageCache(8 << 20),
		router:   r,
	}
}

func (h *fakeHost) ConnState() pagekit.ConnState                      { return h.connState }
func (h *fakeHost) Desktop() pagekit.DesktopSnapshot                  { return h.desktop }
func (h *fakeHost) Client() *companionclient.Client                   { return h.client }
func (h *fakeHost) MediaProxy() *companionclient.MediaProxy           { return nil }
func (h *fakeHost) ImageCache() *companionclient.MemoryImageCache     { return h.imgCache }
func (h *fakeHost) Window() *mygo.Window                              { return nil }
func (h *fakeHost) Router() *ui.Router                                { return h.router }
func (h *fakeHost) PushRoute(path string)                             { h.router.Push(path) }
func (h *fakeHost) Back()                                             { h.router.Back() }
func (h *fakeHost) PushWindow(string, func(*ui.Context)) *mygo.Window { return nil }
func (h *fakeHost) SelectTab(pagekit.TabID)                           {}
func (h *fakeHost) OpenExternal(string) error                         { return nil }
func (h *fakeHost) RefreshStatus()                                    {}
func (h *fakeHost) Unpair()                                           {}

func (h *fakeHost) FetchImage(ctx context.Context, path string) ([]byte, error) {
	if h.client == nil {
		return nil, &companionclient.Error{Kind: companionclient.KindUnreachable}
	}
	return h.client.FetchBytes(ctx, path)
}

func (h *fakeHost) Update(fn func()) {
	if fn != nil {
		h.mu.Lock()
		h.pending = append(h.pending, fn)
		h.mu.Unlock()
	}
	if h.invalidate != nil {
		h.invalidate()
	}
}

func (h *fakeHost) drainUpdates() {
	for {
		h.mu.Lock()
		if len(h.pending) == 0 {
			h.mu.Unlock()
			return
		}
		batch := h.pending
		h.pending = nil
		h.mu.Unlock()
		for _, fn := range batch {
			fn()
		}
	}
}

func (h *fakeHost) SetTasksBadge(count int) {
	h.mu.Lock()
	h.badgeCounts = append(h.badgeCounts, count)
	h.mu.Unlock()
}

func (h *fakeHost) lastBadge() (int, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.badgeCounts) == 0 {
		return 0, false
	}
	return h.badgeCounts[len(h.badgeCounts)-1], true
}

func startFakeCompanionServer(t *testing.T, handler http.HandlerFunc) (*companionclient.Client, func()) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	fp := companionclient.Fingerprint(der)

	srv := httptest.NewUnstartedServer(handler)
	srv.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
	}
	srv.StartTLS()

	addr := strings.TrimPrefix(srv.URL, "https://")
	rec := companionclient.PairingRecord{
		Node:     "node-1",
		Name:     "Studio-PC",
		Channel:  "dev",
		FP:       fp,
		Addrs:    []string{addr},
		LastAddr: addr,
	}
	cli, err := companionclient.NewClient(rec, "tok-test-tasks-12345678901234", "0.1.0-dev", "dev")
	if err != nil {
		srv.Close()
		t.Fatal(err)
	}
	return cli, func() {
		cli.Close()
		srv.Close()
	}
}

func assertNoHorizontalOverflow(t *testing.T, tt *ui.Tester, width int) {
	t.Helper()
	for _, info := range tt.AccessAll() {
		if info.Bounds.W <= 0 || info.Bounds.H <= 0 || info.Offscreen {
			continue
		}
		if info.Bounds.X < -1 || info.Bounds.X+info.Bounds.W > float32(width)+1 {
			t.Fatalf("horizontal overflow at width=%d on %q (%s): rect=%+v", width, info.Name, info.Value, info.Bounds)
		}
	}
}

func assertNoForbiddenEntries(t *testing.T, tt *ui.Tester) {
	t.Helper()
	forbidden := []string{"退出登录", "粘贴 Cookie", "删除媒体", "删除记录", "退出电脑", "修改设置", "开始导入", "开始导出", "批量应用"}
	for _, txt := range tt.Texts() {
		for _, bad := range forbidden {
			if strings.Contains(txt, bad) {
				t.Fatalf("FR-MOB-025 violation: forbidden entry %q found in text %q", bad, txt)
			}
		}
	}
}

func saveShotIfRequested(t *testing.T, tt *ui.Tester, name string) {
	t.Helper()
	dir := strings.TrimSpace(os.Getenv("TRACE_OPS_SHOTS_DIR"))
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, name+".png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, tt.Image()); err != nil {
		t.Fatal(err)
	}
}

func TestAllowedActionsStrictlyPerState(t *testing.T) {
	dlCases := []struct {
		status string
		want   []string
	}{
		{"queued", []string{"pause", "cancel"}},
		{"running", []string{"pause", "cancel"}},
		{"paused", []string{"resume", "cancel"}},
		{"paused_network", []string{"resume", "cancel"}},
		{"paused_risk", []string{"resume", "cancel"}},
		{"failed", []string{"retry", "remove"}},
		{"partial", []string{"retry", "remove"}},
		{"completed", []string{"remove"}},
		{"canceled", []string{"remove"}},
	}
	for _, tc := range dlCases {
		got := AllowedDownloadActions(tc.status)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Fatalf("AllowedDownloadActions(%q) = %v, want %v", tc.status, got, tc.want)
		}
	}

	qCases := []struct {
		status string
		want   []string
	}{
		{"running", []string{"pause", "cancel"}},
		{"queued", []string{"pause", "cancel"}},
		{"waiting", []string{"pause", "cancel"}},
		{"waiting_backoff", []string{"pause", "cancel"}},
		{"paused", []string{"resume", "cancel"}},
		{"completed", []string{"remove"}},
		{"stopped", []string{"remove"}},
		{"canceled", []string{"remove"}},
	}
	for _, tc := range qCases {
		got := AllowedQueueActions(tc.status)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Fatalf("AllowedQueueActions(%q) = %v, want %v", tc.status, got, tc.want)
		}
	}
}

func settleAnim(tt *ui.Tester) {
	for i := 0; i < 12; i++ {
		tt.Advance(100 * time.Millisecond)
	}
}

func clickLastByName(t *testing.T, tt *ui.Tester, name string) {
	t.Helper()
	var found *ui.AccessInfo
	for _, info := range tt.AccessAll() {
		if info.Name == name && info.Bounds.W > 0 && info.Bounds.H > 0 {
			cp := info
			found = &cp
		}
	}
	if found == nil {
		t.Fatalf("element %q not found; access=%v", name, tt.Texts())
	}
	tt.ClickAt(found.Bounds.X+found.Bounds.W/2, found.Bounds.Y+found.Bounds.H/2)
}

func TestActiveDownloadBadgeCountOnlyQueuedAndRunning(t *testing.T) {
	list := []companionapi.DownloadTaskDTO{
		{TaskID: "1", Status: "queued"},
		{TaskID: "2", Status: "running"},
		{TaskID: "3", Status: "paused"},
		{TaskID: "4", Status: "paused_network"},
		{TaskID: "5", Status: "failed"},
		{TaskID: "6", Status: "partial"},
		{TaskID: "7", Status: "completed"},
		{TaskID: "8", Status: "canceled"},
	}
	if got := ActiveDownloadBadgeCount(list); got != 2 {
		t.Fatalf("ActiveDownloadBadgeCount = %d, want 2 (only queued+running)", got)
	}
}

func TestTasksAllSegmentsActionsReadOnlyAndErrors(t *testing.T) {
	now := time.Now().UTC()
	downloads := []companionapi.DownloadTaskDTO{
		{
			TaskID: "dl-run", Site: "bilibili", Kind: "video", Source: "auto", TargetID: "BV1RUN",
			Title: "正在下载的4K视频", Status: "running", QualityHint: "4K · AVC · 192K",
			BytesDownloaded: 45 << 20, TotalBytes: 100 << 20, SpeedBps: 3 << 20, Progress: 0.45,
		},
		{
			TaskID: "dl-queue", Site: "douyin", Kind: "video", TargetID: "DY2Q",
			Title: "排队中的抖音作品", Status: "queued",
			BytesDownloaded: 0, TotalBytes: 20 << 20, Progress: 0,
		},
		{
			TaskID: "dl-paused", Site: "bilibili", Kind: "video", TargetID: "BV3PAUSED",
			Title: "已暂停的长视频", Status: "paused", PauseReason: "user",
			BytesDownloaded: 30 << 20, TotalBytes: 60 << 20, Progress: 0.5,
		},
		{
			TaskID: "dl-fail", Site: "weibo", Kind: "dynamic", TargetID: "WB4FAIL",
			Title: "失败的微博图文", Status: "failed", ErrorKind: "risk_control", ErrorMessage: "触发微博风控，已停止",
			BytesDownloaded: 2 << 20, TotalBytes: 8 << 20, Progress: 0.25,
		},
		{
			TaskID: "dl-done", Site: "x", Kind: "dynamic", TargetID: "X5DONE",
			Title: "已完成的帖子", Status: "completed",
			BytesDownloaded: 5 << 20, TotalBytes: 5 << 20, Progress: 1.0,
		},
	}

	dlDetail := companionapi.DownloadTaskDetailResponse{
		Task: downloads[0],
		Files: []companionapi.DownloadTaskFileDTO{
			{Role: "video", Path: "P01.video.m4s", Status: "running", DownloadedBytes: 40 << 20, TotalBytes: 90 << 20, SpeedBps: 3 << 20},
			{Role: "audio", Path: "P01.audio.m4s", Status: "completed", DownloadedBytes: 5 << 20, TotalBytes: 5 << 20},
			{Role: "danmaku", Path: "P01.danmaku.xml", Status: "completed", DownloadedBytes: 24 << 10, TotalBytes: 24 << 10},
		},
		Attempts: []companionapi.DownloadTaskAttemptDTO{
			{Time: now.Add(-1 * time.Minute).Format(time.RFC3339), File: "P01.video.m4s", Offset: 12 << 20, Host: "upos-sz-mirror.example.com", Attempt: 1, ErrorKind: "timeout", HTTPStatus: 504},
		},
	}

	queues := []companionapi.CacheQueueDTO{
		{
			ID: "q-run", Site: "bilibili", User: "1001", UserName: "科技评测B站", List: "videos", ListLabel: "投稿",
			Status: "running", CachedCount: 12, SkippedCount: 3, ActiveTitle: "第 13 期测试视频",
			RecentItems: []companionapi.CacheQueueRecentDTO{
				{ItemKey: "bilibili:video:BV1A", Title: "第 12 期实测", CachedAt: now.Add(-5 * time.Minute).Format(time.RFC3339)},
			},
		},
		{
			ID: "q-paused", Site: "douyin", User: "dy-5", UserName: "科技评测抖音", List: "works", ListLabel: "作品",
			Status: "paused", PauseReason: "rate_limited", CachedCount: 4, SkippedCount: 1,
		},
		{
			ID: "q-done", Site: "weibo", User: "wb-7", UserName: "科技评测微博", List: "posts", ListLabel: "微博",
			Status: "completed", CachedCount: 25, SkippedCount: 5,
		},
	}

	recordings := []companionapi.LiveRecordingDTO{
		{
			RecordingKey: "rec-active-1", AccountKey: "bilibili:1001", Site: "bilibili", UID: "1001",
			Uname: "科技评测B站", Title: "晚间直播间", QualityName: "原画", Codec: "AVC",
			Recording: true, DurationSec: 1845, SpeedBps: 1500 * 1024, Bytes: 420 << 20, Segments: 2,
			Incidents: []companionapi.LiveIncident{
				{Kind: "reconnect", At: now.Add(-10 * time.Minute).Format(time.RFC3339), DurationSec: 4, Reason: "上游波动已自动重连"},
			},
		},
		{
			RecordingKey: "rec-done-1", AccountKey: "douyin:dy-5", Site: "douyin", UID: "dy-5",
			Uname: "科技评测抖音", Title: "午间发布会回放", QualityName: "蓝光", Codec: "HEVC",
			Recording: false, DurationSec: 3600, Bytes: 950 << 20, Segments: 3, StopReason: "主播已下播",
		},
	}

	conflicts := []companionapi.ConflictDTO{
		{
			ID: "cf-1", ItemKey: "bilibili:video:BV1CF", Site: "bilibili", Kind: "video",
			AuthorName: "科技评测B站", Title: "年度横评视频", DetectedAt: now.Add(-30 * time.Minute).Format(time.RFC3339),
			ReasonKind: "parts_changed", ReasonLabel: "分P变更",
			LocalSummary: "2 个分P（1080P AVC）", RemoteSummary: "3 个分P（新增补充测试）",
		},
	}

	incidents := []companionapi.LiveIncidentSessionDTO{
		{
			RecordingKey: "rec-active-1", Site: "bilibili", UID: "1001", Uname: "科技评测B站",
			Title: "晚间直播间", Recording: true, StartedAt: now.Add(-35 * time.Minute).Format(time.RFC3339),
			Incidents: []companionapi.LiveIncident{
				{Kind: "segment_lost", At: now.Add(-15 * time.Minute).Format(time.RFC3339), LostSegments: 2, Reason: "HLS 分片过期"},
			},
		},
	}

	transfers := []companionapi.TransferJobDTO{
		{
			ID: "tr-import-1", Kind: "import", Status: "completed",
			DoneItems: 18, TotalItems: 18, DoneBytes: 640 << 20, TotalBytes: 640 << 20,
			StartedAt: now.Add(-1 * time.Hour).Format(time.RFC3339), FinishedAt: now.Add(-55 * time.Minute).Format(time.RFC3339),
		},
		{
			ID: "tr-export-2", Kind: "export", Status: "exporting",
			DoneItems: 5, TotalItems: 20, DoneBytes: 120 << 20, TotalBytes: 500 << 20, SpeedBps: 40 << 20,
			StartedAt: now.Add(-2 * time.Minute).Format(time.RFC3339),
		},
	}

	var (
		mu             sync.Mutex
		dlActions      []string
		clearFinHits   int
		queueActions   []string
		liveStopHits   []string
		nextActionErr  *companionapi.ErrorDetail
		nextActionCode int
	)

	cli, cleanup := startFakeCompanionServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == companionapi.PathTasksDownloads:
			_ = json.NewEncoder(w).Encode(companionapi.DownloadTasksResponse{Items: downloads})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, companionapi.PathTasksDownloadsPrefix):
			_ = json.NewEncoder(w).Encode(dlDetail)
		case r.Method == http.MethodPost && r.URL.Path == companionapi.PathTasksDownloadsClearFinished:
			mu.Lock()
			clearFinHits++
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(companionapi.ActionResponse{OK: true, Count: 1})
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, companionapi.PathTasksDownloadsPrefix):
			rest := strings.TrimPrefix(r.URL.Path, companionapi.PathTasksDownloadsPrefix)
			mu.Lock()
			if nextActionErr != nil {
				st := nextActionCode
				ed := *nextActionErr
				nextActionErr = nil
				mu.Unlock()
				w.WriteHeader(st)
				_ = json.NewEncoder(w).Encode(companionapi.ErrorBody{Error: ed})
				return
			}
			dlActions = append(dlActions, rest)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(companionapi.ActionResponse{OK: true})
		case r.Method == http.MethodGet && r.URL.Path == companionapi.PathTasksQueues:
			_ = json.NewEncoder(w).Encode(companionapi.CacheQueuesResponse{Items: queues})
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, companionapi.PathTasksQueuesPrefix):
			rest := strings.TrimPrefix(r.URL.Path, companionapi.PathTasksQueuesPrefix)
			mu.Lock()
			queueActions = append(queueActions, rest)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(companionapi.ActionResponse{OK: true})
		case r.Method == http.MethodGet && r.URL.Path == companionapi.PathLiveRecordings:
			_ = json.NewEncoder(w).Encode(companionapi.LiveRecordingsResponse{Items: recordings})
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, companionapi.PathLiveRecordingsPrefix) && strings.HasSuffix(r.URL.Path, "/stop"):
			key := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, companionapi.PathLiveRecordingsPrefix), "/stop")
			mu.Lock()
			liveStopHits = append(liveStopHits, key)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(companionapi.LiveRecordingDTO{RecordingKey: key, Recording: false})
		case r.Method == http.MethodGet && r.URL.Path == companionapi.PathConflicts:
			_ = json.NewEncoder(w).Encode(companionapi.ConflictsResponse{Items: conflicts})
		case r.Method == http.MethodGet && r.URL.Path == companionapi.PathLiveIncidents:
			_ = json.NewEncoder(w).Encode(companionapi.LiveIncidentsResponse{Items: incidents})
		case r.Method == http.MethodGet && r.URL.Path == companionapi.PathTransfers:
			_ = json.NewEncoder(w).Encode(companionapi.TransfersResponse{Items: transfers})
		default:
			http.NotFound(w, r)
		}
	})
	defer cleanup()

	host := newFakeHost(cli)
	pg := New()
	pg.RefreshThrottle = 20 * time.Millisecond

	notify := make(chan struct{}, 64)
	host.invalidate = func() {
		select {
		case notify <- struct{}{}:
		default:
		}
	}

	viewFn := func(c *ui.Context) {
		c.SetTheme(theme.TraceTheme())
		pg.View(c, host)
	}
	tt := ui.NewTester(viewFn, 412, 915)
	pumpUntil := func(cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			host.drainUpdates()
			tt.Frame()
			if cond() {
				host.drainUpdates()
				tt.Frame()
				return
			}
			select {
			case <-notify:
			case <-time.After(10 * time.Millisecond):
			}
		}
		t.Fatalf("condition timed out; texts=%v", tt.Texts())
	}

	pg.OnConnected(host)
	pumpUntil(func() bool {
		return pg.downloadsLoaded && pg.queuesLoaded && pg.recordingsLoaded && pg.conflictsLoaded && pg.incidentsLoaded && pg.transfersLoaded
	})

	// 1. Verify Host.SetTasksBadge received ONLY queued + running downloads (2), NOT recordings/queues/conflicts/failed!
	badge, ok := host.lastBadge()
	if !ok || badge != 2 {
		t.Fatalf("expected SetTasksBadge(2), got (%d, %v)", badge, ok)
	}

	// 2. 进行中 segment: downloads + queues + per-state actions + clear finished.
	for _, want := range []string{"进行中", "直播", "待处理", "记录", "正在下载的4K视频", "触发微博风控", "科技评测B站 · 投稿", "第 13 期测试视频", "清除已结束（1）"} {
		if !tt.HasText(want) {
			t.Fatalf("进行中 missing %q; got %v", want, tt.Texts())
		}
	}
	assertNoHorizontalOverflow(t, tt, 412)
	assertNoForbiddenEntries(t, tt)
	saveShotIfRequested(t, tt, "tasks-active-412x915")

	// Expand cache queue recent items.
	if err := tt.Click("展开或收起缓存队列：q-run"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("第 12 期实测") {
		t.Fatalf("expected expanded recent cached item, got %v", tt.Texts())
	}

	// Test every download action strictly per state via ActionSheet (rendered last/on top):
	// - running -> pause
	if err := tt.Click("下载任务更多操作：dl-run"); err != nil {
		t.Fatal(err)
	}
	settleAnim(tt)
	clickLastByName(t, tt, "暂停")
	settleAnim(tt)
	pumpUntil(func() bool {
		mu.Lock()
		defer mu.Unlock()
		return containsStr(dlActions, "dl-run/pause") && pg.busyActionKey == ""
	})

	// - queued -> cancel
	if err := tt.Click("下载任务更多操作：dl-queue"); err != nil {
		t.Fatal(err)
	}
	settleAnim(tt)
	clickLastByName(t, tt, "取消")
	settleAnim(tt)
	pumpUntil(func() bool {
		mu.Lock()
		defer mu.Unlock()
		return containsStr(dlActions, "dl-queue/cancel") && pg.busyActionKey == ""
	})

	// - paused -> resume
	if err := tt.Click("下载任务更多操作：dl-paused"); err != nil {
		t.Fatal(err)
	}
	settleAnim(tt)
	clickLastByName(t, tt, "继续")
	settleAnim(tt)
	pumpUntil(func() bool {
		mu.Lock()
		defer mu.Unlock()
		return containsStr(dlActions, "dl-paused/resume") && pg.busyActionKey == ""
	})

	// - failed -> retry + honest auth_required error ("请先在电脑上登录微博")
	mu.Lock()
	nextActionCode = http.StatusPreconditionFailed
	nextActionErr = &companionapi.ErrorDetail{Code: companionapi.CodeAuthRequired, Site: "weibo", Message: "请先在电脑上登录微博"}
	mu.Unlock()
	if err := tt.Click("下载任务更多操作：dl-fail"); err != nil {
		t.Fatal(err)
	}
	settleAnim(tt)
	clickLastByName(t, tt, "重试")
	settleAnim(tt)
	pumpUntil(func() bool {
		return strings.Contains(pg.actionErr, "请先在电脑上登录微博") && pg.busyActionKey == ""
	})
	if !tt.HasText("请先在电脑上登录微博") {
		t.Fatalf("expected auth_required error banner, got %v", tt.Texts())
	}

	// - failed -> retry (success)
	if err := tt.Click("下载任务更多操作：dl-fail"); err != nil {
		t.Fatal(err)
	}
	settleAnim(tt)
	clickLastByName(t, tt, "重试")
	settleAnim(tt)
	pumpUntil(func() bool {
		mu.Lock()
		defer mu.Unlock()
		return containsStr(dlActions, "dl-fail/retry") && pg.busyActionKey == ""
	})

	// - failed -> remove (inline confirm)
	if err := tt.Click("下载任务更多操作：dl-fail"); err != nil {
		t.Fatal(err)
	}
	settleAnim(tt)
	clickLastByName(t, tt, "移除")
	settleAnim(tt)
	if !tt.HasText("确认移除该任务") {
		t.Fatalf("expected inline remove confirmation for failed download")
	}
	if err := tt.Click("确认移除任务：dl-fail"); err != nil {
		t.Fatal(err)
	}
	pumpUntil(func() bool {
		mu.Lock()
		defer mu.Unlock()
		return containsStr(dlActions, "dl-fail/remove") && pg.busyActionKey == ""
	})

	// - clear finished
	if err := tt.Click("清除已结束的下载任务"); err != nil {
		t.Fatal(err)
	}
	pumpUntil(func() bool {
		mu.Lock()
		defer mu.Unlock()
		return clearFinHits == 1 && pg.busyActionKey == ""
	})

	// Test cache queue commands: pause, resume, cancel, remove.
	// Scroll down so all cache queue cards are within the 915dp viewport.
	tt.Scroll(200, 450, 0, 600)
	settleAnim(tt)
	for _, tc := range []struct {
		qid    string
		action string
		want   string
	}{
		{"q-run", "暂停", "q-run/pause"},
		{"q-paused", "继续", "q-paused/resume"},
		{"q-paused", "取消", "q-paused/cancel"},
		{"q-done", "移除", "q-done/remove"},
	} {
		if err := tt.Click("缓存队列更多操作：" + tc.qid); err != nil {
			t.Fatalf("open queue sheet %s: %v", tc.qid, err)
		}
		settleAnim(tt)
		clickLastByName(t, tt, tc.action)
		settleAnim(tt)
		pumpUntil(func() bool {
			mu.Lock()
			defer mu.Unlock()
			return containsStr(queueActions, tc.want) && pg.busyActionKey == ""
		})
	}

	// Scroll back to top and tap download card -> opens per-file detail /download/dl-run.
	tt.Scroll(200, 450, 0, -600)
	settleAnim(tt)
	if err := tt.Click("查看下载详情：正在下载的4K视频"); err != nil {
		t.Fatal(err)
	}
	pumpUntil(func() bool { return pg.detailResp != nil })
	settleAnim(tt)
	for _, want := range []string{"逐文件进度（3 个文件）", "P01.video.m4s", "P01.audio.m4s", "P01.danmaku.xml", "upos-sz-mirror.example.com"} {
		if !tt.HasText(want) {
			t.Fatalf("download detail missing %q; got %v", want, tt.Texts())
		}
	}
	assertNoHorizontalOverflow(t, tt, 412)
	saveShotIfRequested(t, tt, "tasks-download-detail-412x915")
	host.Back()
	settleAnim(tt)

	// 3. 直播 segment: active & recent recordings, inline expandable incidents, "停止录制" ONLY on active!
	pg.activeSeg = segLive
	tt.Frame()
	for _, want := range []string{"正在录制（1）", "最近结束的录制（1）", "晚间直播间", "原画 · AVC", "⚠ 1 处异常"} {
		if !tt.HasText(want) {
			t.Fatalf("直播 segment missing %q; got %v", want, tt.Texts())
		}
	}
	if _, ok := tt.Find("停止直播录制：科技评测B站"); !ok {
		t.Fatal("expected 停止录制 button on active recording")
	}
	if _, ok := tt.Find("停止直播录制：科技评测抖音"); ok {
		t.Fatal("finished recording must NOT have 停止录制 button")
	}
	// Expand inline incidents.
	if err := tt.Click("展开或收起录制异常：rec-active-1"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("上游波动已自动重连") {
		t.Fatalf("expected inline expanded live incident text, got %v", tt.Texts())
	}
	assertNoHorizontalOverflow(t, tt, 412)
	assertNoForbiddenEntries(t, tt)
	saveShotIfRequested(t, tt, "tasks-live-412x915")

	// Stop active recording.
	if err := tt.Click("停止直播录制：科技评测B站"); err != nil {
		t.Fatal(err)
	}
	pumpUntil(func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(liveStopHits) == 1 && liveStopHits[0] == "rec-active-1" && pg.busyActionKey == ""
	})

	// 4. 待处理 segment: actionable content conflicts (keep_both, keep_local, inline-confirmed use_remote), failed downloads, live incidents.
	pg.activeSeg = segPending
	tt.Frame()
	for _, want := range []string{"内容冲突（1）", "不会自动覆盖", "保留两版（推荐）", "保留本地", "用新版替换", "分P变更", "年度横评视频", "失败 / 未完成的下载任务（1）", "近期录制异常（1 场）", "HLS 分片过期"} {
		if !tt.HasText(want) {
			t.Fatalf("待处理 missing %q; got %v", want, tt.Texts())
		}
	}
	assertNoHorizontalOverflow(t, tt, 412)
	assertNoForbiddenEntries(t, tt)
	saveShotIfRequested(t, tt, "tasks-pending-412x915")

	// Verify removing a failed download in 待处理 requires inline confirmation first (P2 parity).
	mu.Lock()
	removeBeforePending := countStr(dlActions, "dl-fail/remove")
	mu.Unlock()
	if err := tt.Click("待处理移除任务：dl-fail"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("确认移除该任务") {
		t.Fatal("expected inline remove confirmation in 待处理 before deleting failed download")
	}
	mu.Lock()
	removeMidPending := countStr(dlActions, "dl-fail/remove")
	mu.Unlock()
	if removeMidPending != removeBeforePending {
		t.Fatal("failed download in 待处理 must NOT be removed before inline confirmation")
	}
	if err := tt.Click("待处理确认移除任务：dl-fail"); err != nil {
		t.Fatal(err)
	}
	pumpUntil(func() bool {
		mu.Lock()
		defer mu.Unlock()
		return countStr(dlActions, "dl-fail/remove") == removeBeforePending+1 && pg.busyActionKey == ""
	})

	// 5. 记录 segment: read-only import/export transfers (zero actions).
	pg.activeSeg = segHistory
	tt.Frame()
	for _, want := range []string{"导入任务 · tr-import-1", "导出任务 · tr-export-2", "18 / 18 条", "5 / 20 条", "只读查看记录"} {
		if !tt.HasText(want) {
			t.Fatalf("记录 missing %q; got %v", want, tt.Texts())
		}
	}
	assertNoHorizontalOverflow(t, tt, 412)
	assertNoForbiddenEntries(t, tt)
	saveShotIfRequested(t, tt, "tasks-history-412x915")

	// 6. Responsive widths 360x800, 412x915, 800x1280 with zero overflow.
	for _, sz := range [][2]int{{360, 800}, {412, 915}, {800, 1280}} {
		host.router = ui.NewRouter("/")
		host.router.SystemStack = true
		for _, seg := range []int{segActive, segLive, segPending, segHistory} {
			pg.activeSeg = seg
			rTT := ui.NewTester(viewFn, sz[0], sz[1])
			settleAnim(rTT)
			assertNoHorizontalOverflow(t, rTT, sz[0])
			assertNoForbiddenEntries(t, rTT)
			if seg == segActive && sz[0] == 360 {
				saveShotIfRequested(t, rTT, "tasks-active-360x800")
			}
			if seg == segActive && sz[0] == 800 {
				saveShotIfRequested(t, rTT, "tasks-active-800x1280")
			}
		}
	}

	// 7. OnDisconnected clears all in-memory data.
	pg.OnDisconnected()
	if len(pg.downloads) != 0 || len(pg.queues) != 0 || len(pg.recordings) != 0 || len(pg.conflicts) != 0 || len(pg.incidents) != 0 || len(pg.transfers) != 0 {
		t.Fatal("expected OnDisconnected to clear all in-memory task lists")
	}
}

func TestTasksEventThrottlingAtMostOncePerInterval(t *testing.T) {
	var dlHits atomic.Int32
	cli, cleanup := startFakeCompanionServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case companionapi.PathTasksDownloads:
			dlHits.Add(1)
			_ = json.NewEncoder(w).Encode(companionapi.DownloadTasksResponse{Items: []companionapi.DownloadTaskDTO{
				{TaskID: "t1", Status: "running", Progress: 0.5},
			}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
		}
	})
	defer cleanup()

	host := newFakeHost(cli)
	pg := New()
	pg.RefreshThrottle = 200 * time.Millisecond
	pg.initTriggered = true

	// Burst 10 WS events within 10ms -> should trigger at most 1 immediate fetch + 1 trailing fetch.
	for i := 0; i < 10; i++ {
		pg.OnEvent(host, companionclient.EventFrame{Type: companionapi.EventTaskChanged})
		pg.OnEvent(host, companionclient.EventFrame{Type: companionapi.EventCacheChanged})
		pg.OnEvent(host, companionclient.EventFrame{Type: companionapi.EventSubsCheck})
	}
	drainFor := func(d time.Duration) {
		end := time.Now().Add(d)
		for time.Now().Before(end) {
			host.drainUpdates()
			time.Sleep(10 * time.Millisecond)
		}
		host.drainUpdates()
	}
	drainFor(80 * time.Millisecond)
	mid := dlHits.Load()
	if mid > 1 {
		t.Fatalf("expected at most 1 immediate fetch during throttle window, got %d", mid)
	}
	drainFor(260 * time.Millisecond)
	final := dlHits.Load()
	if final < 1 || final > 2 {
		t.Fatalf("expected 1..2 coalesced fetches total, got %d", final)
	}
	pg.OnDisconnected()
}

func TestDownloadDetailRemoveConfirmAndBackClearsStalePolling(t *testing.T) {
	var detailHits, removeHits atomic.Int32
	failedTask := companionapi.DownloadTaskDTO{
		TaskID: "dl-fail-detail", Site: "bilibili", Kind: "video", TargetID: "BV1FAIL",
		Title: "失败任务详情测试", Status: "failed", ErrorMessage: "分段校验失败",
		BytesDownloaded: 10 << 20, TotalBytes: 20 << 20, Progress: 0.5,
	}
	cli, cleanup := startFakeCompanionServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == companionapi.PathTasksDownloads:
			_ = json.NewEncoder(w).Encode(companionapi.DownloadTasksResponse{Items: []companionapi.DownloadTaskDTO{failedTask}})
		case r.Method == http.MethodGet && r.URL.Path == companionapi.DownloadTaskPath("dl-fail-detail"):
			detailHits.Add(1)
			_ = json.NewEncoder(w).Encode(companionapi.DownloadTaskDetailResponse{Task: failedTask})
		case r.Method == http.MethodPost && r.URL.Path == companionapi.DownloadTaskActionPath("dl-fail-detail", "remove"):
			removeHits.Add(1)
			_ = json.NewEncoder(w).Encode(companionapi.ActionResponse{OK: true})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
		}
	})
	defer cleanup()

	host := newFakeHost(cli)
	pg := New()
	pg.RefreshThrottle = 10 * time.Millisecond
	viewFn := func(c *ui.Context) {
		c.SetTheme(theme.TraceTheme())
		pg.View(c, host)
	}
	tt := ui.NewTester(viewFn, 412, 915)
	pg.OnConnected(host)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		host.drainUpdates()
		tt.Frame()
		if pg.downloadsLoaded {
			host.drainUpdates()
			tt.Frame()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := tt.Click("查看下载详情：失败任务详情测试"); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		host.drainUpdates()
		tt.Frame()
		if pg.detailResp != nil {
			host.drainUpdates()
			tt.Frame()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	settleAnim(tt)

	// Clicking 移除 in /download/{id} must show inline confirmation first without immediately removing.
	if err := tt.Click("详情移除任务"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("确认移除该任务") || removeHits.Load() != 0 {
		t.Fatalf("expected inline confirmation with 0 remove hits, hits=%d", removeHits.Load())
	}

	// Cancel confirmation, then navigate back to root -> detailTaskID cleared & WS task:changed stops polling old detail.
	if err := tt.Click("详情取消移除任务"); err != nil {
		t.Fatal(err)
	}
	host.Back()
	settleAnim(tt)
	host.drainUpdates()
	tt.Frame()
	if pg.detailTaskID != "" || pg.detailResp != nil {
		t.Fatalf("expected detailTaskID cleared after Back, got id=%q resp=%v", pg.detailTaskID, pg.detailResp)
	}
	before := detailHits.Load()
	pg.OnEvent(host, companionclient.EventFrame{Type: companionapi.EventTaskChanged})
	time.Sleep(60 * time.Millisecond)
	host.drainUpdates()
	if detailHits.Load() != before {
		t.Fatalf("stale download detail polling after Back: %d -> %d", before, detailHits.Load())
	}

	// Re-open detail and confirm inline removal executes POST .../remove.
	if err := tt.Click("查看下载详情：失败任务详情测试"); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		host.drainUpdates()
		tt.Frame()
		if pg.detailResp != nil {
			host.drainUpdates()
			tt.Frame()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	settleAnim(tt)
	if err := tt.Click("详情移除任务"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("详情确认移除任务：dl-fail-detail"); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		host.drainUpdates()
		tt.Frame()
		if removeHits.Load() == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if removeHits.Load() != 1 {
		t.Fatalf("expected 1 remove call after confirmation, got %d", removeHits.Load())
	}
	pg.OnDisconnected()
}

func countStr(list []string, target string) int {
	n := 0
	for _, s := range list {
		if s == target {
			n++
		}
	}
	return n
}

func containsStr(list []string, target string) bool {
	for _, s := range list {
		if s == target {
			return true
		}
	}
	return false
}

func TestTasksV2ConflictsResolutionPasteLinkAndLivePreview(t *testing.T) {
	now := time.Now().UTC()
	var (
		mu        sync.Mutex
		conflicts = []companionapi.ConflictDTO{
			{
				ID: "cf-both", ItemKey: "bilibili:video:BV1A", Site: "bilibili", Kind: "video",
				AuthorName: "UP主A", Title: "冲突视频一（保留两版）", DetectedAt: now.Format(time.RFC3339),
				ReasonKind: "parts_changed", ReasonLabel: "分P变更",
				LocalSummary: "2 P", RemoteSummary: "3 P",
			},
			{
				ID: "cf-local", ItemKey: "douyin:video:DY2", Site: "douyin", Kind: "video",
				AuthorName: "作者B", Title: "冲突视频二（保留本地）", DetectedAt: now.Format(time.RFC3339),
				ReasonKind: "text_changed", ReasonLabel: "文案变更",
				LocalSummary: "旧文案", RemoteSummary: "新文案",
			},
			{
				ID: "cf-remote", ItemKey: "weibo:dynamic:WB3", Site: "weibo", Kind: "dynamic",
				AuthorName: "博主C", Title: "冲突微博三（用新版替换）", DetectedAt: now.Format(time.RFC3339),
				ReasonKind: "media_changed", ReasonLabel: "媒体变更",
				LocalSummary: "2 图", RemoteSummary: "4 图",
			},
		}
		recordings = []companionapi.LiveRecordingDTO{
			{
				RecordingKey: "rec-live-1", AccountKey: "bilibili:1001", Site: "bilibili", UID: "1001",
				Uname: "直播测试主", Title: "正在录制可实时预览", QualityName: "原画", Codec: "AVC",
				Recording: true, PreviewAvailable: true, DurationSec: 120, Bytes: 32 << 20,
			},
			{
				RecordingKey: "rec-live-noprev", AccountKey: "bilibili:1002", Site: "bilibili", UID: "1002",
				Uname: "刚开播主播", Title: "等待首段写入", QualityName: "原画", Codec: "AVC",
				Recording: true, PreviewAvailable: false, PreviewReason: "正在等待首个分段数据写入",
			},
			{
				RecordingKey: "rec-ended-2", AccountKey: "douyin:2002", Site: "douyin", UID: "2002",
				Uname: "已结束主播", Title: "已结束不可预览", Recording: false,
			},
		}
		resolvedCalls []string
		parseInputs   []string
		previewOpened []string
	)

	cli, cleanup := startFakeCompanionServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == companionapi.PathTasksDownloads:
			_ = json.NewEncoder(w).Encode(companionapi.DownloadTasksResponse{})
		case r.Method == http.MethodGet && r.URL.Path == companionapi.PathTasksQueues:
			_ = json.NewEncoder(w).Encode(companionapi.CacheQueuesResponse{})
		case r.Method == http.MethodGet && r.URL.Path == companionapi.PathLiveRecordings:
			_ = json.NewEncoder(w).Encode(companionapi.LiveRecordingsResponse{Items: recordings})
		case r.Method == http.MethodGet && r.URL.Path == companionapi.PathLiveIncidents:
			_ = json.NewEncoder(w).Encode(companionapi.LiveIncidentsResponse{})
		case r.Method == http.MethodGet && r.URL.Path == companionapi.PathTransfers:
			_ = json.NewEncoder(w).Encode(companionapi.TransfersResponse{})
		case r.Method == http.MethodGet && r.URL.Path == companionapi.PathConflicts:
			mu.Lock()
			items := append([]companionapi.ConflictDTO(nil), conflicts...)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(companionapi.ConflictsResponse{Items: items})
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, companionapi.PathConflictsPrefix) && strings.HasSuffix(r.URL.Path, "/resolve"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, companionapi.PathConflictsPrefix), "/resolve")
			var req companionapi.ResolveConflictRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			mu.Lock()
			resolvedCalls = append(resolvedCalls, id+":"+req.Choice)
			next := conflicts[:0]
			for _, c := range conflicts {
				if c.ID != id {
					next = append(next, c)
				}
			}
			conflicts = next
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(companionapi.ResolveConflictResponse{OK: true, ID: id, Choice: req.Choice})
		case r.Method == http.MethodPost && r.URL.Path == companionapi.PathParseAndCache:
			var body companionapi.ParseAndCacheRequest
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			parseInputs = append(parseInputs, body.Input)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(companionapi.ParseAndCacheResponse{
				OK: true, TaskID: "dl-from-paste", Site: "bilibili", Title: "手机粘贴缓存测试视频", AuthorName: "测试UP",
			})
		default:
			http.NotFound(w, r)
		}
	})
	defer cleanup()

	host := newFakeHost(cli)
	pg := New()
	pg.openLivePreviewFn = func(_ pagekit.Host, rec companionapi.LiveRecordingDTO) {
		mu.Lock()
		previewOpened = append(previewOpened, rec.RecordingKey)
		mu.Unlock()
	}

	notify := make(chan struct{}, 64)
	host.invalidate = func() {
		select {
		case notify <- struct{}{}:
		default:
		}
	}
	viewFn := func(c *ui.Context) {
		c.SetTheme(theme.TraceTheme())
		pg.View(c, host)
	}
	tt := ui.NewTester(viewFn, 412, 915)
	pumpUntil := func(cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			host.drainUpdates()
			tt.Frame()
			if cond() {
				host.drainUpdates()
				tt.Frame()
				return
			}
			select {
			case <-notify:
			case <-time.After(10 * time.Millisecond):
			}
		}
		t.Fatalf("condition timed out; texts=%v", tt.Texts())
	}

	pg.OnConnected(host)
	pumpUntil(func() bool { return pg.conflictsLoaded && pg.recordingsLoaded })

	// 1. Live recording real-time preview button appears ONLY on previewable active recording; unsupported shows clear reason.
	pg.activeSeg = segLive
	tt.Frame()
	if _, ok := tt.Find("实时预览直播录制：直播测试主"); !ok {
		t.Fatalf("expected 实时预览 button on previewable active recording; texts=%v", tt.Texts())
	}
	if _, ok := tt.Find("实时预览直播录制：刚开播主播"); ok {
		t.Fatal("non-previewable active recording must NOT show 实时预览 button")
	}
	if !tt.HasText("正在等待首个分段数据写入") {
		t.Fatalf("expected PreviewReason shown for non-previewable active recording; texts=%v", tt.Texts())
	}
	if _, ok := tt.Find("实时预览直播录制：已结束主播"); ok {
		t.Fatal("ended recording must NOT show 实时预览 button")
	}
	if err := tt.Click("实时预览直播录制：直播测试主"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	gotPrev := append([]string(nil), previewOpened...)
	mu.Unlock()
	if len(gotPrev) != 1 || gotPrev[0] != "rec-live-1" {
		t.Fatalf("expected live preview opened for rec-live-1, got %v", gotPrev)
	}

	// 2. Content conflicts resolution: keep_both, keep_local, inline-confirmed use_remote.
	pg.activeSeg = segPending
	tt.Frame()
	saveShotIfRequested(t, tt, "tasks-pending-conflicts-v2-412x915")

	// 2a. keep_both
	if err := tt.Click("解决冲突保留两版：cf-both"); err != nil {
		t.Fatal(err)
	}
	pumpUntil(func() bool {
		mu.Lock()
		defer mu.Unlock()
		return containsStr(resolvedCalls, "cf-both:keep_both") && len(pg.conflicts) == 2 && pg.busyActionKey == ""
	})
	if !tt.HasText("已保留本地旧版并缓存源站新版") {
		t.Fatalf("expected keep_both success banner, texts=%v", tt.Texts())
	}

	// 2b. keep_local
	if err := tt.Click("解决冲突保留本地：cf-local"); err != nil {
		t.Fatal(err)
	}
	pumpUntil(func() bool {
		mu.Lock()
		defer mu.Unlock()
		return containsStr(resolvedCalls, "cf-local:keep_local") && len(pg.conflicts) == 1 && pg.busyActionKey == ""
	})

	// 2c. use_remote MUST show inline confirmation first and NOT execute before confirming!
	if err := tt.Click("解决冲突用新版替换：cf-remote"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("确认用源站新版替换本地已缓存内容") {
		t.Fatalf("expected inline confirmation before use_remote, texts=%v", tt.Texts())
	}
	mu.Lock()
	preConfirm := containsStr(resolvedCalls, "cf-remote:use_remote")
	mu.Unlock()
	if preConfirm {
		t.Fatal("use_remote must NOT execute before inline confirmation")
	}
	if err := tt.Click("确认用新版替换：cf-remote"); err != nil {
		t.Fatal(err)
	}
	pumpUntil(func() bool {
		mu.Lock()
		defer mu.Unlock()
		return containsStr(resolvedCalls, "cf-remote:use_remote") && len(pg.conflicts) == 0 && pg.busyActionKey == ""
	})

	// 3. In-app "粘贴链接让电脑缓存" sheet + IME composition guard + submit.
	if err := tt.Click("粘贴链接让电脑缓存"); err != nil {
		t.Fatal(err)
	}
	settleAnim(tt)
	_ = tt.Click("粘贴链接或分享文案输入框")
	tt.Compose("bilibili", 8)
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	mu.Lock()
	afterIME := len(parseInputs)
	mu.Unlock()
	if afterIME != 0 {
		t.Fatalf("Enter during IME composition must not submit paste-and-cache, got %d", afterIME)
	}
	tt.Compose("", 0)
	pg.pasteSheet.Open("https://www.bilibili.com/video/BV1PASTE")
	settleAnim(tt)
	if err := tt.Click("发送到电脑缓存"); err != nil {
		t.Fatal(err)
	}
	pumpUntil(func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(parseInputs) == 1
	})
	if !tt.HasText("已加入电脑缓存队列") || !tt.HasText("手机粘贴缓存测试视频") {
		t.Fatalf("expected parse-and-cache queued feedback, texts=%v", tt.Texts())
	}
	assertNoHorizontalOverflow(t, tt, 412)
	assertNoForbiddenEntries(t, tt)
	pg.OnDisconnected()
}
