package pastecache

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
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
	connected   bool
	cli         *companionclient.Client
	selectedTab pagekit.TabID
	onUpdate    func()
}

func (f *fakeHost) ConnState() pagekit.ConnState {
	if f.connected {
		return pagekit.ConnConnected
	}
	return pagekit.ConnError
}
func (f *fakeHost) Desktop() pagekit.DesktopSnapshot                   { return pagekit.DesktopSnapshot{} }
func (f *fakeHost) Client() *companionclient.Client                    { return f.cli }
func (f *fakeHost) MediaProxy() *companionclient.MediaProxy            { return nil }
func (f *fakeHost) ImageCache() *companionclient.MemoryImageCache      { return nil }
func (f *fakeHost) FetchImage(context.Context, string) ([]byte, error) { return nil, nil }
func (f *fakeHost) Window() *mygo.Window                               { return nil }
func (f *fakeHost) Router() *ui.Router                                 { return ui.NewRouter("/") }
func (f *fakeHost) PushRoute(string)                                   {}
func (f *fakeHost) Back()                                              {}
func (f *fakeHost) PushWindow(string, func(*ui.Context)) *mygo.Window  { return nil }
func (f *fakeHost) SetTasksBadge(int)                                  {}
func (f *fakeHost) OpenExternal(string) error                          { return nil }
func (f *fakeHost) RefreshStatus()                                     {}
func (f *fakeHost) Unpair()                                            {}
func (f *fakeHost) SelectTab(tab pagekit.TabID)                        { f.selectedTab = tab }
func (f *fakeHost) Update(fn func()) {
	if fn != nil {
		fn()
	}
	f.mu.Lock()
	cb := f.onUpdate
	f.mu.Unlock()
	if cb != nil {
		cb()
	}
}

func assertNoOverflow(t *testing.T, tt *ui.Tester, width int) {
	t.Helper()
	for _, info := range tt.AccessAll() {
		if info.Bounds.W <= 0 || info.Bounds.H <= 0 || info.Offscreen {
			continue
		}
		if info.Bounds.X < -1 || info.Bounds.X+info.Bounds.W > float32(width)+1 {
			t.Fatalf("horizontal overflow at width=%d on %q: %+v", width, info.Name, info.Bounds)
		}
	}
}

func settleSheet(tt *ui.Tester) {
	tt.Frame()
	for range 16 {
		tt.Advance(100 * time.Millisecond)
	}
	tt.Frame()
}

func TestPasteCacheSheetSubmitSuccessAlreadyCachedAndErrors(t *testing.T) {
	notify := make(chan struct{}, 16)
	h := &fakeHost{
		connected:   true,
		cli:         &companionclient.Client{},
		selectedTab: pagekit.TabHome,
		onUpdate: func() {
			select {
			case notify <- struct{}{}:
			default:
			}
		},
	}

	var gotInputs []string
	var nextRes Result
	var nextErr error
	queuedCallbacks := 0

	var sheet Sheet
	sheet.SetOnQueued(func() { queuedCallbacks++ })
	sheet.SubmitFn = func(_ context.Context, _ *companionclient.Client, input string) (Result, error) {
		gotInputs = append(gotInputs, input)
		return nextRes, nextErr
	}

	tt := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(theme.TraceTheme())
		ui.Box(c).Fill().Children(func() {
			sheet.View(c, h)
		})
	}, 360, 800)

	// 1. Open and submit a Bilibili share text -> queued task -> jump to Tasks tab.
	nextRes = Result{
		OK:         true,
		TaskID:     "dl_42",
		Site:       "bilibili",
		Title:      "【演示】手机分享让电脑缓存",
		AuthorName: "测试UP主",
	}
	sheet.Open("【演示】https://www.bilibili.com/video/BV1xx411c7mD")
	settleSheet(tt)

	// Ensure zero forbidden entries (FR-MOB-025) and zero overflow at 360/412/800.
	for _, sz := range [][2]int{{360, 800}, {412, 915}, {800, 1280}} {
		tt.SetSize(sz[0], sz[1])
		settleSheet(tt)
		assertNoOverflow(t, tt, sz[0])
	}
	tt.SetSize(360, 800)
	settleSheet(tt)
	if tt.HasText("登录") || tt.HasText("Cookie") || tt.HasText("预览视频") {
		t.Fatalf("forbidden login/preview text must not appear in paste sheet: %v", tt.Texts())
	}

	if err := tt.Click("发送到电脑缓存"); err != nil {
		t.Fatalf("click submit: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(gotInputs) == 0 && time.Now().Before(deadline) {
		select {
		case <-notify:
		case <-time.After(10 * time.Millisecond):
		}
		tt.Frame()
	}
	tt.Frame()
	if len(gotInputs) != 1 || !strings.Contains(gotInputs[0], "BV1xx411c7mD") {
		t.Fatalf("unexpected submitted input: %+v", gotInputs)
	}
	if queuedCallbacks != 1 {
		t.Fatalf("expected onQueued callback once, got %d", queuedCallbacks)
	}
	if !tt.HasText("已加入电脑缓存队列") || !tt.HasText("【演示】手机分享让电脑缓存") {
		t.Fatalf("expected queued result feedback, texts=%v", tt.Texts())
	}

	// Click "查看任务" -> switches to Tasks tab and closes sheet.
	if err := tt.Click("前往任务页查看缓存进度"); err != nil {
		t.Fatalf("click jump to tasks: %v", err)
	}
	settleSheet(tt)
	if h.selectedTab != pagekit.TabTasks {
		t.Fatalf("expected TabTasks selected, got %v", h.selectedTab)
	}
	if sheet.IsOpen() {
		t.Fatal("expected sheet closed after jumping to tasks")
	}

	// 2. Android Share Intent auto-submit -> already cached -> jump to Library tab.
	nextRes = Result{
		OK:            true,
		Site:          "douyin",
		Title:         "已缓存抖音作品",
		AlreadyCached: true,
	}
	sheet.SubmitShared(h, "https://v.douyin.com/example/")
	deadline = time.Now().Add(2 * time.Second)
	for len(gotInputs) < 2 && time.Now().Before(deadline) {
		select {
		case <-notify:
		case <-time.After(10 * time.Millisecond):
		}
		tt.Frame()
	}
	settleSheet(tt)
	if !tt.HasText("电脑媒体库中已缓存该内容") {
		t.Fatalf("expected already-cached feedback, texts=%v", tt.Texts())
	}
	if err := tt.Click("前往媒体库查看已缓存内容"); err != nil {
		t.Fatalf("click jump to library: %v", err)
	}
	settleSheet(tt)
	if h.selectedTab != pagekit.TabLibrary {
		t.Fatalf("expected TabLibrary selected, got %v", h.selectedTab)
	}

	// 3. Error feedback (auth_required / risk_control / unsupported).
	nextRes = Result{}
	nextErr = &companionclient.Error{
		Status:  http.StatusUnauthorized,
		Code:    companionapi.CodeAuthRequired,
		Message: "请先在电脑上登录哔哩哔哩",
	}
	sheet.SubmitShared(h, "https://www.bilibili.com/video/BV1needlogin")
	deadline = time.Now().Add(2 * time.Second)
	for len(gotInputs) < 3 && time.Now().Before(deadline) {
		select {
		case <-notify:
		case <-time.After(10 * time.Millisecond):
		}
		tt.Frame()
	}
	settleSheet(tt)
	if !tt.HasText("请先在电脑上登录哔哩哔哩") {
		t.Fatalf("expected auth_required message, texts=%v", tt.Texts())
	}

	// 4. Reset clears all in-memory state.
	sheet.Reset()
	if sheet.IsOpen() {
		t.Fatal("expected sheet closed after Reset")
	}

	// Sanity check JSON tag compatibility with frozen camelCase companionapi contract.
	raw, err := json.Marshal(Result{OK: true, TaskID: "t1", AlreadyCached: true, AuthorName: "a"})
	if err != nil || !strings.Contains(string(raw), `"taskId"`) || !strings.Contains(string(raw), `"alreadyCached"`) {
		t.Fatalf("unexpected JSON serialization: %s err=%v", string(raw), err)
	}
}

func TestSubmitSharedSupersedesInFlightSubmission(t *testing.T) {
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	notify := make(chan struct{}, 16)

	var sheet Sheet
	sheet.SubmitFn = func(_ context.Context, _ *companionclient.Client, input string) (Result, error) {
		if strings.Contains(input, "BV1FIRST") {
			close(firstStarted)
			<-releaseFirst
			return Result{OK: true, Site: "bilibili", Title: "旧分享结果"}, nil
		}
		return Result{OK: true, Site: "bilibili", Title: "新分享结果"}, nil
	}

	h := &fakeHost{
		connected: true,
		cli:       &companionclient.Client{},
		onUpdate: func() {
			select {
			case notify <- struct{}{}:
			default:
			}
		},
	}
	sheet.SubmitShared(h, "https://www.bilibili.com/video/BV1FIRST")
	<-firstStarted

	// Share a second link while the first submission is still in flight.
	sheet.SubmitShared(h, "https://www.bilibili.com/video/BV1SECOND")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		sheet.mu.Lock()
		done := !sheet.submitting && sheet.result != nil
		sheet.mu.Unlock()
		if done {
			break
		}
		select {
		case <-notify:
		case <-time.After(10 * time.Millisecond):
		}
	}
	// Release the superseded first request and verify it does NOT overwrite the newer result.
	close(releaseFirst)
	time.Sleep(30 * time.Millisecond)

	sheet.mu.Lock()
	got := sheet.result
	submitting := sheet.submitting
	sheet.mu.Unlock()
	if submitting {
		t.Fatal("expected submitting=false after second share completed")
	}
	if got == nil || got.Title != "新分享结果" {
		t.Fatalf("expected newer share result to win, got %+v", got)
	}
}
