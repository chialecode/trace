package ui

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/service"
	"github.com/egoist/mygo/ui"
)

type conflictDiffStub struct {
	*conflictsBackendStub
	diffMu       sync.Mutex
	diffToReturn service.CacheConflictDiffDTO
	diffErr      error
	diffCalls    int
}

func (s *conflictDiffStub) InspectCacheConflictDiff(_ context.Context, _ string) (service.CacheConflictDiffDTO, error) {
	s.diffMu.Lock()
	s.diffCalls++
	dto := s.diffToReturn
	err := s.diffErr
	s.diffMu.Unlock()
	if err != nil {
		return service.CacheConflictDiffDTO{}, err
	}
	return dto, nil
}

func TestTasksConflictsDiffModalHighlightsTextAndAlignsMediaSideBySide(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()

	rawBackend := app.backend.(*service.Backend)
	_, _ = rawBackend.ImportCookie(context.Background(), "SESSDATA=synth; bili_jct=csrf; DedeUserID=12345", "")
	saved := seedSyntheticLocalConflictItem(t, rawBackend)

	conflict := service.CacheConflictDTO{
		ID:            "conflict_diff_1",
		ItemKey:       saved.ItemKey,
		Site:          "bilibili",
		Kind:          "video",
		TargetID:      saved.RawID,
		AuthorName:    "测试作者",
		Title:         "视频标题 V1",
		ReasonKind:    service.ConflictReasonParts,
		ReasonLabel:   "分P变更",
		LocalSummary:  "共 1 P（CID 101）",
		RemoteSummary: "共 2 P（CID 209、210）",
		State:         service.ConflictStatePending,
	}
	base := &conflictsBackendStub{
		BackendAPI: rawBackend,
		raw:        rawBackend,
		conflicts:  []service.CacheConflictDTO{conflict},
	}
	_, locTitle, remTitle := service.DiffTextSideBySide("测试视频 V1 初版", "测试视频 V2 重制版")
	_, locText, remText := service.DiffTextSideBySide("旧正文：1080P 单分P", "新正文：4K 双分P")
	stub := &conflictDiffStub{
		conflictsBackendStub: base,
		diffToReturn: service.CacheConflictDiffDTO{
			Conflict: conflict,
			Local: service.ConflictDiffSideDTO{
				Heading: "本地已缓存（旧版 V1）",
				Summary: "共 1 P（CID 101 · 1080P）",
				Title:   "测试视频 V1 初版",
				Text:    "旧正文：1080P 单分P",
			},
			Remote: service.ConflictDiffSideDTO{
				Heading: "源站当前（新版 V2）",
				Summary: "共 2 P（CID 209 重制版 + P2 勘误）",
				Title:   "测试视频 V2 重制版",
				Text:    "新正文：4K 双分P",
			},
			TitleChanged: true,
			LocalTitle:   locTitle,
			RemoteTitle:  remTitle,
			TextChanged:  true,
			LocalText:    locText,
			RemoteText:   remText,
			MediaPairs: []service.ConflictMediaPairDTO{
				{
					Index:      1,
					Kind:       "video",
					Status:     service.ConflictDiffStatusChanged,
					StatusText: "已变更",
					Local:      &service.ConflictMediaSideDTO{Kind: "video", Label: "P01 初版", Sublabel: "1080P · CID 101", LocalItemKey: saved.ItemKey, LocalRelPath: "cover.png", PlayRelPath: "P01.mp4", CID: 101},
					Remote:     &service.ConflictMediaSideDTO{Kind: "video", Label: "P01 重制", Sublabel: "4K · CID 209", CID: 209},
				},
				{
					Index:      2,
					Kind:       "image",
					Status:     service.ConflictDiffStatusRemoteOnly,
					StatusText: "源站新增",
					Remote:     &service.ConflictMediaSideDTO{Kind: "image", Label: "图 2", Sublabel: "1920×1080"},
				},
			},
		},
	}

	app.backend = stub
	app.BindEventLoop(func(fn func()) { fn() })
	app.OpenTasksConflicts()

	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()

	if _, ok := tt.Find("对比差异：conflict_diff_1"); !ok {
		t.Fatal("missing 对比差异 button on conflict card")
	}
	tt.Click("对比差异：conflict_diff_1")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tt.Frame()
		if app.ConflictDiff.Open && !app.ConflictDiff.Loading && app.ConflictDiff.Diff != nil && len(app.ConflictDiff.Diff.MediaPairs) == 2 {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	tt.Frame()

	if !app.ConflictDiff.Open {
		t.Fatal("expected ConflictDiff modal to open")
	}
	stub.diffMu.Lock()
	calls := stub.diffCalls
	stub.diffMu.Unlock()
	if calls == 0 {
		t.Fatal("expected InspectCacheConflictDiff to be called")
	}
	for _, wantLabel := range []string{
		"内容冲突对比弹窗",
		"内容冲突并列栏头",
		"左侧列头：本地已缓存",
		"右侧列头：源站当前",
		"文字差异对比区",
		"标题文字对比（本地）",
		"标题文字对比（源站）",
		"正文文字对比（本地）",
		"正文文字对比（源站）",
		"图片与视频并行视图区",
		"媒体并列项 1（本地）",
		"媒体并列项 1（源站）",
		"媒体并列项 2（本地）",
		"媒体并列项 2（源站）",
		"对比弹窗保留两版",
	} {
		if _, ok := tt.Find(wantLabel); !ok {
			t.Errorf("missing expected label %q in conflict diff modal", wantLabel)
		}
	}

	// Verify 720px narrow window renders modal cleanly without horizontal overflow.
	ttNarrow := ui.NewTester(app.View, 720, 860)
	ttNarrow.Frame()
	if rect, ok := ttNarrow.Find("内容冲突对比弹窗"); !ok {
		t.Fatal("expected diff modal to render at 720px width")
	} else if rect.X < -0.5 || rect.X+rect.W > 720.5 {
		t.Fatalf("720px modal overflows horizontally: %+v", rect)
	}

	// Inline confirm for destructive replacement inside modal.
	tt.Click("对比弹窗用新版替换")
	tt.Frame()
	if _, ok := tt.Find("对比弹窗确认替换栏"); !ok {
		t.Fatal("expected inline confirm bar inside diff modal")
	}
	tt.Click("对比弹窗取消替换")
	tt.Frame()
	if _, ok := tt.Find("对比弹窗确认替换栏"); ok {
		t.Fatal("expected inline confirm bar to close on cancel")
	}

	// Archive-first resolution from inside modal dispatches ConflictKeepBoth and closes modal.
	tt.Click("对比弹窗保留两版")
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tt.Frame()
		base.mu.Lock()
		n := len(base.resolveCalls)
		base.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if app.ConflictDiff.Open {
		t.Fatal("expected diff modal to close after clicking 保留两版")
	}
	base.mu.Lock()
	rCalls := append([]conflictResolveCall(nil), base.resolveCalls...)
	base.mu.Unlock()
	if len(rCalls) != 1 || rCalls[0].id != "conflict_diff_1" || rCalls[0].choice != service.ConflictKeepBoth {
		t.Fatalf("resolveCalls = %+v, want keep_both on conflict_diff_1", rCalls)
	}

	longText := strings.Repeat("中文差异测试", 10)
	chunks := chunkDiffDisplayText(longText, 12)
	if strings.Join(chunks, "") != longText {
		t.Fatalf("chunkDiffDisplayText lost characters: got %q", strings.Join(chunks, ""))
	}
	if !isBackgroundPendingKey("conflict-diff:conflict_diff_1") {
		t.Fatal("expected conflict-diff: key to be treated as background pending key")
	}
}

func TestTasksConflictsDiffModalEscapeClosesTopMediaOverlayFirst(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()

	rawBackend := app.backend.(*service.Backend)
	saved := seedSyntheticLocalConflictItem(t, rawBackend)

	conflict := service.CacheConflictDTO{
		ID:          "conflict_esc_1",
		ItemKey:     saved.ItemKey,
		Site:        "weibo",
		Kind:        "dynamic",
		TargetID:    "500001",
		Title:       "图文冲突",
		ReasonKind:  service.ConflictReasonMedia,
		ReasonLabel: "媒体变更",
		State:       service.ConflictStatePending,
	}
	app.OpenTasksConflicts()
	app.OpenConflictDiffModalWithData(service.CacheConflictDiffDTO{
		Conflict: conflict,
		Local:    service.ConflictDiffSideDTO{Heading: "本地已缓存（旧版 V1）", Text: "旧正文"},
		Remote:   service.ConflictDiffSideDTO{Heading: "源站当前（新版 V2）", Text: "新正文"},
		MediaPairs: []service.ConflictMediaPairDTO{
			{
				Index:      1,
				Kind:       "image",
				Status:     service.ConflictDiffStatusChanged,
				StatusText: "已变更",
				Local: &service.ConflictMediaSideDTO{
					Kind:         "image",
					Label:        "图 1",
					Sublabel:     "1080×1080",
					LocalItemKey: saved.ItemKey,
					LocalRelPath: "cover.png",
				},
				Remote: &service.ConflictMediaSideDTO{
					Kind:     "image",
					Label:    "图 1",
					Sublabel: "2160×2160",
				},
			},
		},
	})

	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	if !app.ConflictDiff.Open {
		t.Fatal("expected ConflictDiff modal to be open")
	}

	// Open ImageViewer from the left media card inside ConflictDiffModal.
	tt.Click("媒体并列项 1（本地）查看大图")
	tt.Frame()
	if !app.ImageViewer.Open {
		t.Fatal("expected ImageViewer to open when clicking local image in diff modal")
	}
	if !app.ConflictDiff.Open {
		t.Fatal("expected ConflictDiff modal to stay open behind ImageViewer")
	}

	// Verify ImageViewer shortcuts work while stacked above ConflictDiffModal.
	prevZoom := app.ImageViewer.Zoom
	tt.Key(0, ui.KeyEqual)
	tt.Frame()
	if app.ImageViewer.Zoom <= prevZoom {
		t.Fatalf("expected KeyEqual to zoom ImageViewer above diff modal: before=%v after=%v", prevZoom, app.ImageViewer.Zoom)
	}

	// Pressing Escape must close ImageViewer first while keeping ConflictDiffModal open.
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if app.ImageViewer.Open {
		t.Fatal("expected Escape to close ImageViewer")
	}
	if !app.ConflictDiff.Open {
		t.Fatal("expected ConflictDiff modal to remain open after closing ImageViewer with Escape")
	}

	// Opening PlayerOverlay above ConflictDiffModal: Escape closes PlayerOverlay first.
	app.PlayerOverlay.Open = true
	app.PlayerOverlay.Title = "测试视频"
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if app.PlayerOverlay.Open {
		t.Fatal("expected Escape to close PlayerOverlay")
	}
	if !app.ConflictDiff.Open {
		t.Fatal("expected ConflictDiff modal to remain open after closing PlayerOverlay with Escape")
	}

	// Pressing Escape again with no top media overlay closes ConflictDiffModal.
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if app.ConflictDiff.Open {
		t.Fatal("expected second Escape to close ConflictDiff modal")
	}
}

func TestTasksConflictsDiffModalRendersClickableLinksAndAsymmetry(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()

	rawBackend := app.backend.(*service.Backend)
	desk := rawBackend.Desktop().(*service.MockDesktop)
	const cid = "100808c9a1f5c18abbfc31cecd32f45a14ddc1"

	conflict := service.CacheConflictDTO{
		ID:          "conflict_links_1",
		ItemKey:     "weibo:dynamic:5090000000000401",
		Site:        "weibo",
		Kind:        "dynamic",
		TargetID:    "5090000000000401",
		Title:       "超话链接对比",
		ReasonKind:  service.ConflictReasonText,
		ReasonLabel: "正文编辑",
		State:       service.ConflictStatePending,
	}
	_, locSegs, remSegs := service.DiffTextSideBySide("摄影超话 旧正文内容", "摄影超话 新正文内容与 勘误说明")
	app.OpenTasksConflicts()
	app.OpenConflictDiffModalWithData(service.CacheConflictDiffDTO{
		Conflict:    conflict,
		Local:       service.ConflictDiffSideDTO{Heading: "本地已缓存（旧版 V1）", Text: "摄影超话 旧正文内容"},
		Remote:      service.ConflictDiffSideDTO{Heading: "源站当前（新版 V2）", Text: "摄影超话 新正文内容与 勘误说明"},
		TextChanged: true,
		LocalText:   locSegs,
		RemoteText:  remSegs,
		LocalLinks: []service.TextLinkDTO{
			{Label: "摄影超话", URL: "https://weibo.com/p/" + cid},
			{Label: "恶意脚本", URL: "javascript:alert(1)"},
		},
		RemoteLinks: []service.TextLinkDTO{
			{Label: "摄影超话", URL: "https://weibo.com/p/" + cid},
			{Label: "勘误说明", URL: "https://example.com/errata"},
		},
	})

	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()

	for _, want := range []string{"正文超链接（本地）", "正文超链接（源站）", "本地对比超链接：摄影超话", "源站对比超链接：摄影超话", "源站对比超链接：勘误说明"} {
		if _, ok := tt.Find(want); !ok {
			t.Fatalf("missing expected conflict diff link element %q", want)
		}
	}
	if _, ok := tt.Find("本地对比超链接：恶意脚本"); ok {
		t.Fatal("unsafe javascript: link must NEVER render as clickable pill in conflict diff modal")
	}

	if err := tt.Click("本地对比超链接：摄影超话"); err != nil {
		t.Fatalf("Click local link pill failed: %v", err)
	}
	if err := tt.Click("源站对比超链接：勘误说明"); err != nil {
		t.Fatalf("Click remote link pill failed: %v", err)
	}
	actions := desk.Actions()
	if !slices.Contains(actions, service.RecordedAction{Kind: "url", Target: "https://weibo.com/p/" + cid}) ||
		!slices.Contains(actions, service.RecordedAction{Kind: "url", Target: "https://example.com/errata"}) {
		t.Fatalf("expected conflict diff link clicks recorded in MockDesktop, got %+v", actions)
	}

	// Narrow 720px window must render link pills without horizontal overflow.
	ttNarrow := ui.NewTester(app.View, 720, 860)
	ttNarrow.Frame()
	if rect, ok := ttNarrow.Find("内容冲突对比弹窗"); !ok {
		t.Fatal("expected diff modal to render at 720px")
	} else if rect.X < -0.5 || rect.X+rect.W > 720.5 {
		t.Fatalf("720px diff modal with links overflows horizontally: %+v", rect)
	}

	// Asymmetric links: local has 0 links, remote has 1 link -> local explicitly shows （无超链接）.
	app.OpenConflictDiffModalWithData(service.CacheConflictDiffDTO{
		Conflict:    conflict,
		Local:       service.ConflictDiffSideDTO{Heading: "本地已缓存（旧版 V1）", Text: "纯文字旧版"},
		Remote:      service.ConflictDiffSideDTO{Heading: "源站当前（新版 V2）", Text: "新版补充 摄影超话"},
		TextChanged: true,
		LocalText:   []service.ConflictTextSegment{{Text: "纯文字旧版", Kind: "removed"}},
		RemoteText:  []service.ConflictTextSegment{{Text: "新版补充 摄影超话", Kind: "added"}},
		RemoteLinks: []service.TextLinkDTO{{Label: "摄影超话", URL: "https://weibo.com/p/" + cid}},
	})
	tt.Frame()
	if !tt.HasText("（无超链接）") {
		t.Fatal("expected explicit （无超链接） indicator on side without hyperlinks")
	}
	if _, ok := tt.Find("源站对比超链接：摄影超话"); !ok {
		t.Fatal("expected remote hyperlink pill to remain visible on asymmetric diff")
	}
}
