package ui

import (
	"testing"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
	"github.com/egoist/mygo/ui"
)

type refreshReportBackend struct {
	service.BackendAPI
	report library.RebuildReport
	calls  int
}

func (b *refreshReportBackend) RefreshLibrary() (library.RebuildReport, error) {
	b.calls++
	return b.report, nil
}

// The media library's "刷新" rescans the folders and lists what was deleted
// outside TRACE: removed items with their folder, missing files by name.
func TestLibraryRefreshListsDeletedFiles(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 1)
	backend := &refreshReportBackend{BackendAPI: app.backend, report: library.RebuildReport{
		Scanned:      3,
		Removed:      []library.ChangedItem{{ItemKey: "bilibili:dynamic:200", Title: "整个删掉的动态", ItemDir: `C:\cache\bilibili\1_UP\200`}},
		NewlyMissing: []library.ChangedItem{{ItemKey: "bilibili:dynamic:300", Title: "删了一张图的动态", Files: []string{"images/d.jpg"}}},
	}}
	app.backend = backend
	app.Navigate(RouteLibrary)
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	if err := tt.Click("刷新媒体库"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if backend.calls != 1 {
		t.Fatalf("refresh calls = %d", backend.calls)
	}
	for _, want := range []string{"媒体库已刷新", "文件夹已删除，已从媒体库移除（1）", "整个删掉的动态", `C:\cache\bilibili\1_UP\200`, "缺少文件（1）", "删了一张图的动态", "d.jpg"} {
		if !tt.HasText(want) {
			t.Fatalf("dialog lacks %q", want)
		}
	}
	if err := tt.Click("关闭媒体库刷新结果"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if app.LibraryRefresh.Open || app.LibraryRefresh.Running {
		t.Fatalf("dialog state after closing: %+v", app.LibraryRefresh)
	}

	backend.report = library.RebuildReport{Scanned: 3}
	if err := tt.Click("刷新媒体库"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if !tt.HasText("检查了 3 个条目，没有发现被删除的文件。") {
		t.Fatal("an empty result does not say nothing was deleted")
	}
}
