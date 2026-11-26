package ui

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

func clickMust(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	if err := tt.Click(label); err != nil {
		t.Fatalf("Click(%q) failed: %v", label, err)
	}
}

func TestFileDialogPickersAllSixInputsSelectionCancelAndError(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)

	var mu sync.Mutex
	var recorded []FileDialogOptions
	var nextPath string
	var nextErr error

	app.BindFileDialog(func(opts FileDialogOptions) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		recorded = append(recorded, opts)
		return nextPath, nextErr
	})

	lastOpts := func() FileDialogOptions {
		mu.Lock()
		defer mu.Unlock()
		if len(recorded) == 0 {
			t.Fatal("expected FileDialogHandler to be called")
		}
		return recorded[len(recorded)-1]
	}
	callCount := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(recorded)
	}

	// 1. Settings > Storage: CacheDirInput (folder picker)
	app.Navigate(RouteSettings)
	app.SettingsCategory = "storage"
	app.CacheDirInput = `C:\existing\cache`
	nextPath, nextErr = `D:\new\trace-cache`, nil
	tt := ui.NewTester(app.View, 1280, 860)
	clickMust(t, tt, "选择缓存保存位置")
	opts := lastOpts()
	if opts.Kind != FileDialogPickDirectory || opts.DefaultPath != `C:\existing\cache` {
		t.Fatalf("unexpected cache dir picker opts: %+v", opts)
	}
	if app.CacheDirInput != `D:\new\trace-cache` {
		t.Fatalf("CacheDirInput = %q, want %q", app.CacheDirInput, `D:\new\trace-cache`)
	}
	// Cancel preserves existing input without error.
	nextPath, nextErr = "", nil
	app.BannerError = ""
	clickMust(t, tt, "选择缓存保存位置")
	if app.CacheDirInput != `D:\new\trace-cache` || app.BannerError != "" {
		t.Fatalf("cancel altered state: CacheDirInput=%q BannerError=%q", app.CacheDirInput, app.BannerError)
	}
	// Error surfaces calmly in BannerError without clobbering input.
	nextPath, nextErr = "", errors.New("dialog boom")
	clickMust(t, tt, "选择缓存保存位置")
	if app.CacheDirInput != `D:\new\trace-cache` || !strings.Contains(app.BannerError, "dialog boom") {
		t.Fatalf("error handling mismatch: CacheDirInput=%q BannerError=%q", app.CacheDirInput, app.BannerError)
	}
	app.BannerError = ""
	tt.Frame()

	// 2. Settings > Storage: AddLibraryRootInput (folder picker)
	app.AddLibraryRootInput = `C:\old\root`
	nextPath, nextErr = `E:\portable\trace-data`, nil
	clickMust(t, tt, "选择已有缓存文件夹")
	opts = lastOpts()
	if opts.Kind != FileDialogPickDirectory || opts.DefaultPath != `C:\old\root` {
		t.Fatalf("unexpected add library root picker opts: %+v", opts)
	}
	if app.AddLibraryRootInput != `E:\portable\trace-data` {
		t.Fatalf("AddLibraryRootInput = %q, want %q", app.AddLibraryRootInput, `E:\portable\trace-data`)
	}

	// 3. Library > Import page: Transfer.ImportPath (folder picker)
	app.openImportPage()
	app.Transfer.ImportPath = `D:\old-import`
	app.Transfer.ImportErr = "旧错误提示"
	nextPath, nextErr = `D:\Downloads\trace-import`, nil
	tt.Frame()
	clickMust(t, tt, "选择导入文件夹")
	opts = lastOpts()
	if opts.Kind != FileDialogPickDirectory || opts.DefaultPath != `D:\old-import` {
		t.Fatalf("unexpected import dir picker opts: %+v", opts)
	}
	if app.Transfer.ImportPath != `D:\Downloads\trace-import` || app.Transfer.ImportErr != "" {
		t.Fatalf("import dir selection mismatch: path=%q err=%q", app.Transfer.ImportPath, app.Transfer.ImportErr)
	}
	// Error shows inline in Transfer.ImportErr and keeps path.
	nextPath, nextErr = "", errors.New("import picker failed")
	clickMust(t, tt, "选择导入文件夹")
	if app.Transfer.ImportPath != `D:\Downloads\trace-import` || !strings.Contains(app.Transfer.ImportErr, "import picker failed") {
		t.Fatalf("import picker error mismatch: path=%q err=%q", app.Transfer.ImportPath, app.Transfer.ImportErr)
	}

	// 4. Library > Export modal: Transfer.ExportPath (folder picker)
	app.Transfer.ImportPage = false
	app.openExportDialog()
	app.Transfer.ExportPath = `E:\old-export`
	app.Transfer.ExportErr = "旧导出错误"
	nextPath, nextErr = `F:\trace-export`, nil
	tt.Frame()
	clickMust(t, tt, "选择导出文件夹")
	opts = lastOpts()
	if opts.Kind != FileDialogPickDirectory || opts.DefaultPath != `E:\old-export` {
		t.Fatalf("unexpected export dir picker opts: %+v", opts)
	}
	if app.Transfer.ExportPath != `F:\trace-export` || app.Transfer.ExportErr != "" {
		t.Fatalf("export dir selection mismatch: path=%q err=%q", app.Transfer.ExportPath, app.Transfer.ExportErr)
	}
	// Cancel preserves path.
	nextPath, nextErr = "   ", nil
	clickMust(t, tt, "选择导出文件夹")
	if app.Transfer.ExportPath != `F:\trace-export` || app.Transfer.ExportErr != "" {
		t.Fatalf("export cancel altered state: path=%q err=%q", app.Transfer.ExportPath, app.Transfer.ExportErr)
	}
	app.closeExportDialog()

	// 5. Subscriptions > Export modal: SubFile.ExportPath (save file *.json, default trace-subscriptions.json)
	app.Navigate(RouteSubscriptions)
	app.openSubExport()
	app.SubFile.ExportPath = `D:\backup`
	app.SubFile.Err = "旧订阅导出错误"
	nextPath, nextErr = `D:\backup\trace-subscriptions.json`, nil
	tt.Frame()
	clickMust(t, tt, "选择订阅导出位置")
	opts = lastOpts()
	if opts.Kind != FileDialogSaveFile || opts.DefaultPath != `D:\backup` || opts.DefaultName != "trace-subscriptions.json" {
		t.Fatalf("unexpected sub export picker opts: %+v", opts)
	}
	if len(opts.Filters) == 0 || len(opts.Filters[0].Extensions) == 0 || opts.Filters[0].Extensions[0] != "json" {
		t.Fatalf("expected *.json filter on sub export picker, got %+v", opts.Filters)
	}
	if app.SubFile.ExportPath != `D:\backup\trace-subscriptions.json` || app.SubFile.Err != "" {
		t.Fatalf("sub export selection mismatch: path=%q err=%q", app.SubFile.ExportPath, app.SubFile.Err)
	}
	app.closeSubExport()

	// 6. Subscriptions > Import modal: SubFile.ImportPath (open file *.json; clears stale Preview & bumps subImportGen)
	app.openSubImport()
	app.SubFile.ImportPath = `D:\backup\old.json`
	app.SubFile.Preview = &subscription.ImportPreview{Unchanged: 3}
	app.SubFile.Err = "旧导入错误"
	genBeforeCancel := app.subImportGen
	tt.Frame()

	// Cancel must NOT clear Preview or bump subImportGen.
	nextPath, nextErr = "", nil
	clickMust(t, tt, "选择订阅文件")
	if app.SubFile.ImportPath != `D:\backup\old.json` || app.SubFile.Preview == nil || app.subImportGen != genBeforeCancel {
		t.Fatalf("cancel altered sub import state: path=%q preview=%v gen=%d (want %d)",
			app.SubFile.ImportPath, app.SubFile.Preview, app.subImportGen, genBeforeCancel)
	}

	// Selecting a file updates ImportPath, clears Preview & Err, and bumps subImportGen.
	nextPath, nextErr = `D:\backup\new-subs.json`, nil
	clickMust(t, tt, "选择订阅文件")
	opts = lastOpts()
	if opts.Kind != FileDialogOpenFile || opts.DefaultPath != `D:\backup\old.json` {
		t.Fatalf("unexpected sub import picker opts: %+v", opts)
	}
	if len(opts.Filters) == 0 || len(opts.Filters[0].Extensions) == 0 || opts.Filters[0].Extensions[0] != "json" {
		t.Fatalf("expected *.json filter on sub import picker, got %+v", opts.Filters)
	}
	if app.SubFile.ImportPath != `D:\backup\new-subs.json` || app.SubFile.Preview != nil || app.SubFile.Err != "" {
		t.Fatalf("sub import selection mismatch: path=%q preview=%v err=%q", app.SubFile.ImportPath, app.SubFile.Preview, app.SubFile.Err)
	}
	if app.subImportGen != genBeforeCancel+1 {
		t.Fatalf("expected subImportGen to bump from %d to %d, got %d", genBeforeCancel, genBeforeCancel+1, app.subImportGen)
	}

	// Error surfaces in SubFile.Err without overwriting ImportPath.
	nextPath, nextErr = "", errors.New("cannot open picker")
	clickMust(t, tt, "选择订阅文件")
	if app.SubFile.ImportPath != `D:\backup\new-subs.json` || !strings.Contains(app.SubFile.Err, "cannot open picker") {
		t.Fatalf("sub import error mismatch: path=%q err=%q", app.SubFile.ImportPath, app.SubFile.Err)
	}

	if got := callCount(); got != 12 {
		t.Fatalf("expected 12 mock picker calls, got %d", got)
	}
}

func TestFileDialogUnavailableSurfacesCalmError(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)

	// No FileDialogHandler bound: clicking must not panic or open OS dialogs.
	app.Navigate(RouteSubscriptions)
	app.openSubExport()
	app.SubFile.ExportPath = `D:\manual\subs.json`
	tt := ui.NewTester(app.View, 1280, 860)
	clickMust(t, tt, "选择订阅导出位置")
	if app.SubFile.ExportPath != `D:\manual\subs.json` {
		t.Fatalf("ExportPath overwritten when handler nil: %q", app.SubFile.ExportPath)
	}
	if app.SubFile.Err == "" {
		t.Fatal("expected calm error when no FileDialogHandler is bound")
	}
}

func TestFileDialogRunsOffUIThreadAndDropsStaleResults(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)

	posted := make(chan func(), 8)
	app.dispatch = func(fn func()) { posted <- fn }

	gate := make(chan string, 1)
	started := make(chan struct{}, 4)
	var calls atomic.Int32
	app.BindFileDialog(func(opts FileDialogOptions) (string, error) {
		calls.Add(1)
		started <- struct{}{}
		return <-gate, nil
	})

	waitStarted := func() {
		t.Helper()
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for async FileDialogHandler to start")
		}
	}

	// 1. Export modal: starts off-UI-thread, does not trigger top-bar loading
	// banner, blocks re-entrant clicks, and drops stale result when modal closes.
	app.Navigate(RouteLibrary)
	app.openExportDialog()
	app.Transfer.ExportPath = `E:\original`
	tt := ui.NewTester(app.View, 1280, 860)
	clickMust(t, tt, "选择导出文件夹")
	waitStarted()
	if app.hasForegroundPending() {
		t.Fatal("file-dialog pending key must not trigger foreground loading banner")
	}
	// Clicking again while busy must not invoke a second concurrent dialog.
	_ = tt.Click("选择导出文件夹")
	if calls.Load() != 1 {
		t.Fatalf("expected exactly 1 dialog call while busy, got %d", calls.Load())
	}
	app.closeExportDialog()
	gate <- `E:\stale-export`
	nextPosted(t, posted)()
	if app.Transfer.ExportPath != `E:\original` {
		t.Fatalf("stale export picker wrote to closed modal: %q", app.Transfer.ExportPath)
	}

	// 2. SubFile Import modal closed & reopened while picker in-flight: stale result dropped.
	app.Navigate(RouteSubscriptions)
	app.openSubImport()
	app.SubFile.ImportPath = `D:\original-subs.json`
	app.SubFile.Preview = &subscription.ImportPreview{Unchanged: 2}
	app.pickSubImportFile()
	waitStarted()
	app.closeSubImport()
	app.openSubImport()
	app.SubFile.ImportPath = `D:\reopened-subs.json`
	gate <- `D:\stale-subs.json`
	nextPosted(t, posted)()
	if app.SubFile.ImportPath != `D:\reopened-subs.json` {
		t.Fatalf("stale sub-import picker overwrote reopened modal: %q", app.SubFile.ImportPath)
	}

	// 3. Settings > Storage: switching Settings category while picker is in-flight drops stale result.
	app.closeSubImport()
	app.Navigate(RouteSettings)
	app.SettingsCategory = "storage"
	app.CacheDirInput = `C:\original-cache`
	app.pickCacheDir()
	waitStarted()
	app.SettingsCategory = "account"
	gate <- `Z:\stale-cache`
	nextPosted(t, posted)()
	if app.CacheDirInput != `C:\original-cache` {
		t.Fatalf("stale cache-dir picker wrote after switching settings category: %q", app.CacheDirInput)
	}

	// 4. Transfer Import page: leaving import page drops stale result.
	app.openImportPage()
	app.Transfer.ImportPath = `D:\keep-import`
	app.pickTransferImportDir()
	waitStarted()
	app.Transfer.ImportPage = false
	gate <- `Z:\stale-import`
	nextPosted(t, posted)()
	if app.Transfer.ImportPath != `D:\keep-import` {
		t.Fatalf("stale import-dir picker wrote after leaving import page: %q", app.Transfer.ImportPath)
	}

	// 5. SubFile Export modal: closing drops stale result.
	app.Navigate(RouteSubscriptions)
	app.openSubExport()
	app.SubFile.ExportPath = `D:\keep-export.json`
	app.pickSubExportFile()
	waitStarted()
	app.closeSubExport()
	gate <- `Z:\stale-export.json`
	nextPosted(t, posted)()
	if app.SubFile.ExportPath != `D:\keep-export.json` {
		t.Fatalf("stale sub-export picker wrote after closing modal: %q", app.SubFile.ExportPath)
	}
}

func TestFileDialogPickersResponsiveLayoutNoOverflow(t *testing.T) {
	widths := []int{1280, 1000, 720}
	const height = 860
	longPath := `C:\Users\demo\Documents\very\long\nested\archive\directory\trace-subscriptions.json`

	assertControlsFit := func(t *testing.T, tt *ui.Tester, w int, labels ...string) {
		t.Helper()
		for _, lbl := range labels {
			r, ok := tt.Find(lbl)
			if !ok {
				t.Fatalf("width=%d: missing control %q", w, lbl)
			}
			if r.W <= 0 || r.H <= 0 {
				t.Fatalf("width=%d: control %q has zero size: %+v", w, lbl, r)
			}
			if r.X < -0.5 || r.X+r.W > float32(w)+0.5 {
				t.Fatalf("width=%d: control %q overflows viewport horizontally: %+v", w, lbl, r)
			}
		}
	}

	assertNoHorizontalOverlap := func(t *testing.T, tt *ui.Tester, w int, leftLabel, rightLabel string) {
		t.Helper()
		l, okL := tt.Find(leftLabel)
		r, okR := tt.Find(rightLabel)
		if !okL || !okR {
			t.Fatalf("width=%d: missing controls %q/%q", w, leftLabel, rightLabel)
		}
		if l.X+l.W > r.X+0.5 {
			t.Fatalf("width=%d: %q (right=%.1f) overlaps %q (left=%.1f)", w, leftLabel, l.X+l.W, rightLabel, r.X)
		}
	}

	for _, w := range widths {
		app, _, cleanup := newTestUIApp(t)
		seedUIFeed(t, app, 0)
		app.BindFileDialog(func(FileDialogOptions) (string, error) { return "", nil })

		// 1 & 2. Settings > Storage
		app.Navigate(RouteSettings)
		app.SettingsCategory = "storage"
		app.CacheDirInput = longPath
		app.AddLibraryRootInput = longPath
		tt := ui.NewTester(app.View, w, height)
		tt.Frame()
		assertControlsFit(t, tt, w,
			"缓存目录输入框", "选择缓存保存位置", "应用缓存目录",
			"添加已有文件夹输入框", "选择已有缓存文件夹", "从已有文件夹添加",
		)
		assertNoHorizontalOverlap(t, tt, w, "缓存目录输入框", "选择缓存保存位置")
		assertNoHorizontalOverlap(t, tt, w, "选择缓存保存位置", "应用缓存目录")
		assertNoHorizontalOverlap(t, tt, w, "添加已有文件夹输入框", "选择已有缓存文件夹")
		assertNoHorizontalOverlap(t, tt, w, "选择已有缓存文件夹", "从已有文件夹添加")

		// 3. Library > Import page
		app.openImportPage()
		app.Transfer.ImportPath = longPath
		tt.Frame()
		assertControlsFit(t, tt, w, "导入文件夹路径", "选择导入文件夹", "扫描导入文件夹")
		assertNoHorizontalOverlap(t, tt, w, "导入文件夹路径", "选择导入文件夹")
		assertNoHorizontalOverlap(t, tt, w, "选择导入文件夹", "扫描导入文件夹")

		// 4. Library > Export dialog
		app.Transfer.ImportPage = false
		app.openExportDialog()
		app.Transfer.ExportPath = longPath
		tt.Frame()
		assertControlsFit(t, tt, w, "导出媒体库对话框", "导出到的文件夹", "选择导出文件夹", "开始导出")
		assertNoHorizontalOverlap(t, tt, w, "导出到的文件夹", "选择导出文件夹")
		app.closeExportDialog()

		// 5. Subscriptions > Export dialog
		app.Navigate(RouteSubscriptions)
		app.openSubExport()
		app.SubFile.ExportPath = longPath
		tt.Frame()
		assertControlsFit(t, tt, w, "导出订阅对话框", "订阅导出位置", "选择订阅导出位置", "确认导出订阅")
		assertNoHorizontalOverlap(t, tt, w, "订阅导出位置", "选择订阅导出位置")
		app.closeSubExport()

		// 6. Subscriptions > Import dialog
		app.openSubImport()
		app.SubFile.ImportPath = longPath
		tt.Frame()
		assertControlsFit(t, tt, w, "导入订阅对话框", "订阅文件路径", "选择订阅文件", "预览导入订阅")
		assertNoHorizontalOverlap(t, tt, w, "订阅文件路径", "选择订阅文件")
		assertNoHorizontalOverlap(t, tt, w, "选择订阅文件", "预览导入订阅")
		app.closeSubImport()

		cleanup()
	}
}
