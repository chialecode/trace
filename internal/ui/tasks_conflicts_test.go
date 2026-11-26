package ui

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
	"github.com/egoist/mygo/ui"
)

type conflictResolveCall struct {
	id     string
	choice string
}

type conflictsBackendStub struct {
	service.BackendAPI
	raw *service.Backend

	mu              sync.Mutex
	tasks           []download.Task
	conflicts       []service.CacheConflictDTO
	resolveCalls    []conflictResolveCall
	batchCalls      []string
	nextResolveErr  error
	nextBatchRes    service.ConflictBatchResult
	nextBatchErr    error
	blockResolve    chan struct{}
	resolveStarted  chan struct{}
	fetchImageCalls int
	openLinkCalls   int
	metaOnlyCalls   int
	mediaPathCalls  int
	loggedErrors    []error
	subscribers     []func(service.Event)
}

func (b *conflictsBackendStub) HasAnyLogin() bool {
	return true
}

func (b *conflictsBackendStub) SubscribeEvents(fn func(service.Event)) func() {
	b.mu.Lock()
	b.subscribers = append(b.subscribers, fn)
	b.mu.Unlock()
	return func() {}
}

func (b *conflictsBackendStub) emit(ev service.Event) {
	b.mu.Lock()
	subs := make([]func(service.Event), len(b.subscribers))
	copy(subs, b.subscribers)
	b.mu.Unlock()
	for _, fn := range subs {
		if fn != nil {
			fn(ev)
		}
	}
}

func (b *conflictsBackendStub) ListDownloadTasks() ([]download.Task, error) {
	b.mu.Lock()
	out := append([]download.Task(nil), b.tasks...)
	b.mu.Unlock()
	return out, nil
}

func (b *conflictsBackendStub) ListCacheConflicts() ([]service.CacheConflictDTO, error) {
	b.mu.Lock()
	out := append([]service.CacheConflictDTO(nil), b.conflicts...)
	b.mu.Unlock()
	return out, nil
}

func (b *conflictsBackendStub) PendingCacheConflictCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, c := range b.conflicts {
		if c.State != service.ConflictStateResolving {
			n++
		}
	}
	return n
}

func (b *conflictsBackendStub) ResolveCacheConflict(ctx context.Context, id, choice string) error {
	b.mu.Lock()
	b.resolveCalls = append(b.resolveCalls, conflictResolveCall{id: id, choice: choice})
	gate := b.blockResolve
	started := b.resolveStarted
	err := b.nextResolveErr
	b.mu.Unlock()

	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if err != nil {
		for i := range b.conflicts {
			if b.conflicts[i].ID == id {
				b.conflicts[i].State = service.ConflictStateFailed
				b.conflicts[i].LastError = err.Error()
			}
		}
		return err
	}
	filtered := b.conflicts[:0]
	for _, c := range b.conflicts {
		if c.ID != id {
			filtered = append(filtered, c)
		}
	}
	b.conflicts = filtered
	return nil
}

func (b *conflictsBackendStub) ResolveAllCacheConflicts(_ context.Context, choice string) (service.ConflictBatchResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.batchCalls = append(b.batchCalls, choice)
	if b.nextBatchErr != nil {
		return service.ConflictBatchResult{}, b.nextBatchErr
	}
	res := b.nextBatchRes
	if res == (service.ConflictBatchResult{}) {
		res.Applied = len(b.conflicts)
	}
	b.conflicts = nil
	return res, nil
}

func (b *conflictsBackendStub) FetchImage(_ context.Context, _ string) ([]byte, error) {
	b.mu.Lock()
	b.fetchImageCalls++
	b.mu.Unlock()
	return nil, fmt.Errorf("unexpected network FetchImage call")
}

func (b *conflictsBackendStub) OpenLink(_ context.Context, _ string) (*service.OpenLinkResult, error) {
	b.mu.Lock()
	b.openLinkCalls++
	b.mu.Unlock()
	return nil, fmt.Errorf("unexpected network OpenLink call")
}

func (b *conflictsBackendStub) GetLibraryItemMetaOnly(keyOrID string) (library.ItemMetadata, bool, error) {
	b.mu.Lock()
	b.metaOnlyCalls++
	b.mu.Unlock()
	return b.BackendAPI.GetLibraryItemMetaOnly(keyOrID)
}

func (b *conflictsBackendStub) MediaPath(key, relPath string) (string, error) {
	b.mu.Lock()
	b.mediaPathCalls++
	b.mu.Unlock()
	return b.BackendAPI.MediaPath(key, relPath)
}

func (b *conflictsBackendStub) LogUserError(err error) {
	b.mu.Lock()
	b.loggedErrors = append(b.loggedErrors, err)
	b.mu.Unlock()
}

func seedSyntheticLocalConflictItem(t *testing.T, raw *service.Backend) library.ItemMetadata {
	t.Helper()
	dir := filepath.Join(raw.LibraryStore().CacheDir(), "bilibili", "20001_测试作者", "videos", "BV1confLocal1")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 64, 40))
	for y := 0; y < 40; y++ {
		for x := 0; x < 64; x++ {
			img.SetRGBA(x, y, color.RGBA{R: 48, G: 112, B: 196, A: 255})
		}
	}
	var coverBuf bytes.Buffer
	if err := png.Encode(&coverBuf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cover.png"), coverBuf.Bytes(), 0o600); err != nil {
		t.Fatalf("WriteFile cover.png: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "P01.mp4"), []byte("synthetic-mp4"), 0o600); err != nil {
		t.Fatalf("WriteFile P01.mp4: %v", err)
	}
	saved, err := raw.LibraryStore().SaveItem(dir, library.ItemMetadata{
		Version:  1,
		ID:       "BV1confLocal1",
		RawID:    "BV1confLocal1",
		Site:     "bilibili",
		Kind:     "video",
		Status:   "complete",
		Title:    "本机已缓存 V1 视频条目",
		PubTs:    time.Now().Add(-48 * time.Hour).Unix(),
		CachedAt: time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339),
		Author:   library.ItemAuthor{Mid: 20001, Name: "测试作者"},
		Media: []library.MediaFile{
			{Role: "video", Path: "P01.mp4", Duration: 60},
			{Role: "cover", Path: "cover.png", Width: 64, Height: 40},
		},
		Parts: []library.VideoPartRecord{
			{CID: 101, Page: 1, Title: "第一集", Duration: 60, File: "P01.mp4"},
		},
	})
	if err != nil {
		t.Fatalf("SaveItem: %v", err)
	}
	return saved
}

func sampleConflictFixtures(itemKey string) []service.CacheConflictDTO {
	now := time.Now().UTC()
	return []service.CacheConflictDTO{
		{
			ID:            "c_bili",
			ItemKey:       itemKey,
			Site:          "bilibili",
			Kind:          "video",
			TargetID:      "BV1confLocal1",
			AuthorUID:     "20001",
			AuthorName:    "测试作者",
			Title:         "本机已缓存 V1 视频条目",
			DetectedAt:    now.Add(-15 * time.Minute).Format(time.RFC3339),
			ReasonKind:    service.ConflictReasonParts,
			ReasonLabel:   service.ConflictReasonLabel(service.ConflictReasonParts),
			LocalSummary:  "共 1 P（P1 cid=101 · 4K）",
			RemoteSummary: "共 2 P（P1 cid=202 重制版）",
			State:         service.ConflictStatePending,
		},
		{
			ID:            "c_wb",
			ItemKey:       "weibo:dynamic:5000000000000101",
			Site:          "weibo",
			Kind:          "dynamic",
			TargetID:      "5000000000000101",
			AuthorUID:     "500000001",
			AuthorName:    "测试微博作者",
			Title:         "微博编辑替换配图测试",
			DetectedAt:    now.Add(-40 * time.Minute).Format(time.RFC3339),
			ReasonKind:    service.ConflictReasonMedia,
			ReasonLabel:   service.ConflictReasonLabel(service.ConflictReasonMedia),
			LocalSummary:  "2 张原图（已校验）",
			RemoteSummary: "替换为 1 张裁剪图",
			State:         service.ConflictStatePending,
		},
		{
			ID:            "c_x",
			ItemKey:       "x:dynamic:1000000000000000101",
			Site:          "x",
			Kind:          "dynamic",
			TargetID:      "1000000000000000101",
			AuthorUID:     "1000000000000000001",
			AuthorName:    "测试X作者",
			Title:         "X 帖子正文修订测试",
			DetectedAt:    now.Add(-90 * time.Minute).Format(time.RFC3339),
			ReasonKind:    service.ConflictReasonText,
			ReasonLabel:   service.ConflictReasonLabel(service.ConflictReasonText),
			LocalSummary:  "初版长帖正文与原图",
			RemoteSummary: "编辑后正文（edit_count=2）",
			State:         service.ConflictStateFailed,
			LastError:     "源站内容已失效或受限，已拒绝覆盖（本地原文件未改动）",
		},
	}
}

func TestTasksConflictsRailBadgeExclusionAndResponsiveNav(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()

	rawBackend := app.backend.(*service.Backend)
	_, _ = rawBackend.ImportCookie(context.Background(), "SESSDATA=synth; bili_jct=csrf; DedeUserID=12345", "")
	saved := seedSyntheticLocalConflictItem(t, rawBackend)

	stub := &conflictsBackendStub{
		BackendAPI: rawBackend,
		raw:        rawBackend,
		tasks: []download.Task{
			{TaskID: "dl_done", Site: "bilibili", Kind: "video", TargetID: "BV1done", Title: "已完成任务", Status: "completed"},
		},
		conflicts: sampleConflictFixtures(saved.ItemKey),
	}
	app.backend = stub
	app.BindEventLoop(nil)
	app.Tasks = append([]download.Task(nil), stub.tasks...)
	app.OpenTasksConflicts()

	// 1. With 0 running/queued download tasks and 3 conflicts, rail 任务 button MUST NOT draw a badge pill.
	UseAccent("blue")
	ttWide := ui.NewTester(app.View, 1280, 860)
	ttWide.Frame()
	if app.activeWorkCount() != 0 {
		t.Fatalf("activeWorkCount = %d, want 0", app.activeWorkCount())
	}
	railTasksRect, okRail := ttWide.Find("任务")
	if !okRail {
		t.Fatal("missing left-rail 任务 button")
	}
	wideImg0 := ttWide.Image()
	badgeX := int(railTasksRect.X + railTasksRect.W - 5)
	badgeY := int(railTasksRect.Y + 6)
	cr0, cg0, cb0, _ := wideImg0.At(badgeX, badgeY).RGBA()
	if int(cb0>>8) > 180 && int(cr0>>8) < 80 {
		t.Fatalf("left-rail 任务 button rendered an accent badge with 0 active downloads and 3 conflicts (RGB=%d,%d,%d)", cr0>>8, cg0>>8, cb0>>8)
	}
	if !ttWide.HasText("待处理（3）") || !ttWide.HasText("内容冲突（3）") {
		t.Fatal("Tasks sidebar must display 待处理（3） and attention view must display 内容冲突（3）")
	}
	if _, ok := ttWide.Find("任务侧栏：待处理"); !ok {
		t.Fatal("missing sidebar item 任务侧栏：待处理 at 1280px")
	}

	// With 1 running download task + 3 conflicts, rail 任务 badge renders for the 1 download task (never 1+3=4).
	stub.mu.Lock()
	stub.tasks = append(stub.tasks, download.Task{
		TaskID: "dl_run", Site: "bilibili", Kind: "video", TargetID: "BV1run", Title: "进行中任务", Status: "running",
	})
	stub.mu.Unlock()
	app.Tasks = append([]download.Task(nil), stub.tasks...)
	ttWide.Frame()
	wideImg1 := ttWide.Image()
	cr1, cg1, cb1, _ := wideImg1.At(badgeX, badgeY).RGBA()
	if !(int(cb1>>8) > 180 && int(cr1>>8) < 80) {
		t.Fatalf("expected left-rail 任务 button to render accent badge when 1 download is running (RGB=%d,%d,%d)", cr1>>8, cg1>>8, cb1>>8)
	}
	if app.activeWorkCount() != 1 {
		t.Fatalf("activeWorkCount = %d, want 1 (excluding 3 conflicts)", app.activeWorkCount())
	}
	if !ttWide.HasText("进行中（1）") || !ttWide.HasText("待处理（3）") {
		t.Fatal("expected 进行中（1） and 待处理（3） in Tasks sidebar")
	}

	// 2. Check 1000px medium layout and 720px narrow layout for zero horizontal overflow.
	ttMed := ui.NewTester(app.View, 1000, 860)
	ttMed.Frame()
	if r, ok := ttMed.Find("任务侧栏：待处理"); !ok || r.X < -0.5 || r.X+r.W > 1000.5 {
		t.Fatalf("1000px sidebar item missing or overflowed: ok=%v rect=%+v", ok, r)
	}

	ttNarrow := ui.NewTester(app.View, 720, 860)
	ttNarrow.Frame()
	if _, ok := ttNarrow.Find("上下文侧栏"); ok {
		t.Fatal("context panel must collapse below 1000px")
	}
	if !ttNarrow.HasText("待处理（3）") {
		t.Fatal("720px narrow selector must display 待处理（3）")
	}
	for _, segLabel := range []string{
		"任务分段：进行中",
		"任务分段：直播",
		"任务分段：待处理",
		"任务分段：记录",
		"内容冲突顶部栏",
		"全部待处理方式",
		"批量应用冲突处理",
		"内容冲突卡片：c_bili",
		"内容冲突卡片：c_wb",
		"内容冲突卡片：c_x",
	} {
		r, ok := ttNarrow.Find(segLabel)
		if !ok {
			t.Fatalf("720px missing %q", segLabel)
		}
		if r.X < -0.5 || r.X+r.W > 720.5 || r.W <= 0 || r.H <= 0 {
			t.Fatalf("720px %q overflowed viewport: rect=%+v", segLabel, r)
		}
	}

	// 3. EventCacheConflictsChanged updates conflicts dynamically; 0 attention items renders "待处理" and empty state.
	stub.mu.Lock()
	stub.conflicts = nil
	stub.mu.Unlock()
	stub.emit(service.Event{Type: service.EventCacheConflictsChanged})

	ttWide.Frame()
	if ttWide.HasText("待处理（") {
		t.Fatal("sidebar must display plain 待处理 without (0) when count is 0")
	}
	if !ttWide.HasText("待处理") {
		t.Fatal("sidebar missing 待处理 label when count is 0")
	}
	if _, ok := ttWide.Find("待处理空状态"); !ok || !ttWide.HasText("目前没有需要处理的事项") {
		t.Fatal("expected clean empty state 目前没有需要处理的事项 when all attention categories are empty")
	}
}

func TestTasksConflictsBatchDefaultAndDestructiveConfirm(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()

	rawBackend := app.backend.(*service.Backend)
	_, _ = rawBackend.ImportCookie(context.Background(), "SESSDATA=synth; bili_jct=csrf; DedeUserID=12345", "")
	saved := seedSyntheticLocalConflictItem(t, rawBackend)

	stub := &conflictsBackendStub{
		BackendAPI: rawBackend,
		raw:        rawBackend,
		conflicts:  sampleConflictFixtures(saved.ItemKey),
	}
	app.backend = stub
	app.OpenTasksConflicts()

	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()

	// Default batch choice MUST be archive-first keep_both ("保留两版（推荐）").
	if got := app.effectiveConflictBatchChoice(); got != service.ConflictKeepBoth {
		t.Fatalf("default batch choice = %q, want %q", got, service.ConflictKeepBoth)
	}
	if !tt.HasText("保留两版（推荐）") {
		t.Fatal("expected batch ui.Select to display archive-first default 保留两版（推荐）")
	}

	// Clicking 批量应用 on keep_both applies immediately without destructive confirmation.
	if err := tt.Click("批量应用冲突处理"); err != nil {
		t.Fatalf("click batch apply: %v", err)
	}
	tt.Frame()

	stub.mu.Lock()
	calls := append([]string(nil), stub.batchCalls...)
	stub.conflicts = sampleConflictFixtures(saved.ItemKey)
	stub.mu.Unlock()
	if len(calls) != 1 || calls[0] != service.ConflictKeepBoth {
		t.Fatalf("batchCalls = %v, want [%q]", calls, service.ConflictKeepBoth)
	}

	// Reseed and select "用新版替换" in ui.Select: must require inline batch confirmation first!
	app.refreshCacheConflicts()
	tt.Frame()
	if err := tt.Click("全部待处理方式"); err != nil {
		t.Fatalf("open batch ui.Select: %v", err)
	}
	tt.Frame()
	if err := tt.Click("用新版替换"); err != nil {
		t.Fatalf("select 用新版替换: %v", err)
	}
	tt.Frame()
	if got := app.effectiveConflictBatchChoice(); got != service.ConflictUseRemote {
		t.Fatalf("batch choice after select = %q, want %q", got, service.ConflictUseRemote)
	}

	if err := tt.Click("批量应用冲突处理"); err != nil {
		t.Fatalf("click batch apply on use_remote: %v", err)
	}
	tt.Frame()

	stub.mu.Lock()
	callsAfterFirstClick := len(stub.batchCalls)
	stub.mu.Unlock()
	if callsAfterFirstClick != 1 {
		t.Fatalf("expected inline confirmation before ResolveAllCacheConflicts(use_remote), got %d calls", callsAfterFirstClick)
	}
	if _, ok := tt.Find("确认批量替换冲突栏"); !ok {
		t.Fatal("expected inline batch confirmation bar 确认批量替换冲突栏")
	}

	// Cancel dismisses inline batch confirmation without calling backend.
	if err := tt.Click("取消批量替换冲突"); err != nil {
		t.Fatalf("click cancel batch confirm: %v", err)
	}
	tt.Frame()
	if _, ok := tt.Find("确认批量替换冲突栏"); ok {
		t.Fatal("batch confirmation bar should close after cancel")
	}

	// Re-open and confirm batch replacement -> dispatches ResolveAllCacheConflicts(ctx, use_remote).
	if err := tt.Click("批量应用冲突处理"); err != nil {
		t.Fatalf("re-open batch confirm: %v", err)
	}
	tt.Frame()
	if err := tt.Click("确认批量替换冲突"); err != nil {
		t.Fatalf("confirm batch replace: %v", err)
	}
	tt.Frame()

	stub.mu.Lock()
	finalCalls := append([]string(nil), stub.batchCalls...)
	stub.mu.Unlock()
	if len(finalCalls) != 2 || finalCalls[1] != service.ConflictUseRemote {
		t.Fatalf("batchCalls = %v, want second call %q", finalCalls, service.ConflictUseRemote)
	}
}

func TestTasksConflictsPerCardActionsAsyncResolvingAndOfflineLocalDetail(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()

	rawBackend := app.backend.(*service.Backend)
	_, _ = rawBackend.ImportCookie(context.Background(), "SESSDATA=synth; bili_jct=csrf; DedeUserID=12345", "")
	saved := seedSyntheticLocalConflictItem(t, rawBackend)

	stub := &conflictsBackendStub{
		BackendAPI: rawBackend,
		raw:        rawBackend,
		conflicts:  sampleConflictFixtures(saved.ItemKey),
	}
	app.backend = stub

	posted := make(chan func(), 32)
	app.BindEventLoop(func(fn func()) {
		posted <- fn
	})
	drainPosted := func() {
		for {
			select {
			case fn := <-posted:
				if fn != nil {
					fn()
				}
			case <-time.After(25 * time.Millisecond):
				return
			}
		}
	}

	app.OpenTasksConflicts()
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	drainPosted()
	tt.Frame()

	// 1. Offline local thumbnail must use GetLibraryItemMetaOnly + MediaPath and ZERO network calls.
	stub.mu.Lock()
	metaCalls := stub.metaOnlyCalls
	mediaCalls := stub.mediaPathCalls
	fetchCalls := stub.fetchImageCalls
	openCalls := stub.openLinkCalls
	stub.mu.Unlock()
	if metaCalls == 0 || mediaCalls == 0 {
		t.Fatalf("expected offline local thumbnail lookup via GetLibraryItemMetaOnly (%d) + MediaPath (%d)", metaCalls, mediaCalls)
	}
	if fetchCalls != 0 || openCalls != 0 {
		t.Fatalf("expected 0 network calls on conflict view render, got FetchImage=%d OpenLink=%d", fetchCalls, openCalls)
	}

	// Failure banner on c_x must display LastError preserving "（本地原文件未改动）".
	if _, ok := tt.Find("冲突失败提示：c_x"); !ok || !tt.HasText("（本地原文件未改动）") {
		t.Fatal("expected ColorErrorBg failure banner on c_x preserving （本地原文件未改动）")
	}

	// 2. Async "保留两版" shows "处理中…" and disables card buttons while in-flight, then resolves keep_both.
	gate := make(chan struct{})
	started := make(chan struct{}, 1)
	stub.mu.Lock()
	stub.blockResolve = gate
	stub.resolveStarted = started
	stub.mu.Unlock()

	if err := tt.Click("保留两版：c_bili"); err != nil {
		t.Fatalf("click 保留两版：c_bili: %v", err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("async ResolveCacheConflict did not start")
	}
	tt.Frame()
	if !tt.HasText("处理中…") {
		t.Fatal("expected resolving card to show 处理中… while ResolveCacheConflict is in-flight")
	}
	// Clicking while disabled must not trigger a second call.
	_ = tt.Click("保留两版：c_bili")
	_ = tt.Click("保留本地：c_bili")

	close(gate)
	drainPosted()
	stub.mu.Lock()
	stub.blockResolve = nil
	stub.resolveStarted = nil
	gotCalls := append([]conflictResolveCall(nil), stub.resolveCalls...)
	stub.mu.Unlock()
	tt.Frame()

	if len(gotCalls) != 1 || gotCalls[0] != (conflictResolveCall{id: "c_bili", choice: service.ConflictKeepBoth}) {
		t.Fatalf("resolveCalls after keep_both = %+v, want [{c_bili keep_both}]", gotCalls)
	}

	// 3. Per-card "保留本地" dispatches keep_local.
	if err := tt.Click("保留本地：c_wb"); err != nil {
		t.Fatalf("click 保留本地：c_wb: %v", err)
	}
	drainPosted()
	tt.Frame()

	stub.mu.Lock()
	gotCalls = append([]conflictResolveCall(nil), stub.resolveCalls...)
	stub.mu.Unlock()
	if len(gotCalls) != 2 || gotCalls[1] != (conflictResolveCall{id: "c_wb", choice: service.ConflictKeepLocal}) {
		t.Fatalf("resolveCalls after keep_local = %+v, want second {c_wb keep_local}", gotCalls)
	}

	// 4. Per-card "用新版替换" opens inline confirm first; cancel does nothing; confirm dispatches use_remote
	// and preserves "（本地原文件未改动）" when ErrRemoteDegradedRefused is returned.
	stub.mu.Lock()
	stub.conflicts = []service.CacheConflictDTO{sampleConflictFixtures(saved.ItemKey)[0]}
	stub.nextResolveErr = fmt.Errorf("%w：源站视频已失效（本地原文件未改动）", service.ErrRemoteDegradedRefused)
	stub.mu.Unlock()
	app.refreshCacheConflicts()
	tt.Frame()

	if err := tt.Click("用新版替换：c_bili"); err != nil {
		t.Fatalf("click 用新版替换：c_bili: %v", err)
	}
	tt.Frame()
	if _, ok := tt.Find("确认替换冲突栏：c_bili"); !ok || !tt.HasText("确认用源站新版替换本地已缓存内容？") {
		t.Fatal("expected per-card inline confirmation row before use_remote")
	}
	stub.mu.Lock()
	beforeConfirmN := len(stub.resolveCalls)
	stub.mu.Unlock()
	if beforeConfirmN != 2 {
		t.Fatalf("ResolveCacheConflict must not fire before clicking 确认替换, got %d", beforeConfirmN)
	}

	if err := tt.Click("取消替换冲突：c_bili"); err != nil {
		t.Fatalf("click 取消替换冲突：c_bili: %v", err)
	}
	tt.Frame()
	if _, ok := tt.Find("确认替换冲突栏：c_bili"); ok {
		t.Fatal("inline confirmation row should close on 取消")
	}

	if err := tt.Click("用新版替换：c_bili"); err != nil {
		t.Fatalf("re-open 用新版替换：c_bili: %v", err)
	}
	tt.Frame()
	if err := tt.Click("确认替换冲突：c_bili"); err != nil {
		t.Fatalf("click 确认替换冲突：c_bili: %v", err)
	}
	drainPosted()
	tt.Frame()

	stub.mu.Lock()
	gotCalls = append([]conflictResolveCall(nil), stub.resolveCalls...)
	stub.nextResolveErr = nil
	stub.mu.Unlock()
	if len(gotCalls) != 3 || gotCalls[2] != (conflictResolveCall{id: "c_bili", choice: service.ConflictUseRemote}) {
		t.Fatalf("resolveCalls after confirmed use_remote = %+v", gotCalls)
	}
	if _, ok := tt.Find("冲突失败提示：c_bili"); !ok || !tt.HasText("（本地原文件未改动）") {
		t.Fatal("expected failed use_remote to display ColorErrorBg banner preserving （本地原文件未改动）")
	}

	// 5. Per-card "查看本地" opens offline /library/item with 0 network calls, and Back returns to Tasks › 内容冲突.
	if err := tt.Click("查看本地冲突条目：c_bili"); err != nil {
		t.Fatalf("click 查看本地冲突条目：c_bili: %v", err)
	}
	drainPosted()
	tt.Frame()

	if app.CurrentRoute != RouteLibraryItem || app.CurrentLibraryDetail == nil || app.CurrentLibraryDetail.ItemKey != saved.ItemKey {
		t.Fatalf("expected offline RouteLibraryItem for %q, got route=%q detail=%+v", saved.ItemKey, app.CurrentRoute, app.CurrentLibraryDetail)
	}
	stub.mu.Lock()
	fetchAfterOpen := stub.fetchImageCalls
	openAfterOpen := stub.openLinkCalls
	stub.mu.Unlock()
	if fetchAfterOpen != 0 || openAfterOpen != 0 {
		t.Fatalf("expected 0 network calls when opening local conflict item, got FetchImage=%d OpenLink=%d", fetchAfterOpen, openAfterOpen)
	}

	if err := tt.Click("返回"); err != nil {
		t.Fatalf("click 返回 from /library/item: %v", err)
	}
	tt.Frame()
	if app.CurrentRoute != RouteTasks || app.effectiveTasksSection() != TasksSectionAttention {
		t.Fatalf("expected 返回 to restore RouteTasks attention section, got route=%q section=%q", app.CurrentRoute, app.effectiveTasksSection())
	}
}

func isAccentFilledButton(img image.Image, r ui.Rect) bool {
	if img == nil || r.W < 8 || r.H < 8 {
		return false
	}
	b := img.Bounds()
	// Sample upper-left inner corner of the pill background (away from centered text glyphs).
	px := min(max(int(r.X)+6, b.Min.X), b.Max.X-1)
	py := min(max(int(r.Y)+int(r.H/2), b.Min.Y), b.Max.Y-1)
	cr, cg, cb, _ := img.At(px, py).RGBA()
	R, G, B := int(cr>>8), int(cg>>8), int(cb>>8)
	// ColorAccent (#1a73e8 = R:26, G:115, B:232) has strong blue dominance vs white (#ffffff) or warn/error tints.
	return B > 180 && R < 80 && G > 70 && G < 170
}

func TestTasksConflictsMaxOneAccentButtonPerRegion(t *testing.T) {
	UseAccent("blue")
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()

	rawBackend := app.backend.(*service.Backend)
	_, _ = rawBackend.ImportCookie(context.Background(), "SESSDATA=synth; bili_jct=csrf; DedeUserID=12345", "")
	saved := seedSyntheticLocalConflictItem(t, rawBackend)

	stub := &conflictsBackendStub{
		BackendAPI: rawBackend,
		raw:        rawBackend,
		conflicts:  sampleConflictFixtures(saved.ItemKey),
	}
	app.backend = stub
	app.OpenTasksConflicts()
	// Open BOTH batch inline confirmation and per-card inline confirmation simultaneously
	// to verify neither confirmation bar introduces a second AccentPillButton in any region.
	app.ConflictBatchChoice = service.ConflictUseRemote
	app.ConflictBatchConfirmOpen = true
	app.ConflictReplaceConfirmID = "c_wb"

	tt := ui.NewTester(app.View, 1280, 960)
	tt.Frame()
	img := tt.Image()

	// Region 1: Batch top bar (including open batch confirmation row).
	topBarButtons := []string{
		"批量应用冲突处理",
		"取消批量替换冲突",
		"确认批量替换冲突",
	}
	accentInTopBar := 0
	for _, lbl := range topBarButtons {
		r, ok := tt.Find(lbl)
		if !ok {
			t.Fatalf("missing button %q in batch top bar", lbl)
		}
		if isAccentFilledButton(img, r) {
			accentInTopBar++
		}
	}
	if accentInTopBar != 1 {
		t.Fatalf("batch top bar accent button count = %d, want strictly 1 (docs/ui.md §3)", accentInTopBar)
	}

	// Region 2..N: Each conflict card (including c_wb with open inline replace confirmation).
	cards := map[string][]string{
		"c_bili": {
			"保留两版：c_bili",
			"保留本地：c_bili",
			"用新版替换：c_bili",
			"查看本地冲突条目：c_bili",
		},
		"c_wb": {
			"保留两版：c_wb",
			"保留本地：c_wb",
			"用新版替换：c_wb",
			"查看本地冲突条目：c_wb",
			"取消替换冲突：c_wb",
			"确认替换冲突：c_wb",
		},
		"c_x": {
			"保留两版：c_x",
			"保留本地：c_x",
			"用新版替换：c_x",
			"查看本地冲突条目：c_x",
		},
	}
	for cardID, btns := range cards {
		accentCount := 0
		for _, lbl := range btns {
			r, ok := tt.Find(lbl)
			if !ok {
				t.Fatalf("card %s missing button %q", cardID, lbl)
			}
			if isAccentFilledButton(img, r) {
				accentCount++
			}
		}
		if accentCount != 1 {
			t.Fatalf("card %s accent button count = %d, want strictly 1 (docs/ui.md §3)", cardID, accentCount)
		}
	}
}
