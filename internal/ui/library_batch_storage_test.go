package ui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

type libraryOfflineGuardBackend struct {
	service.BackendAPI
	netCalls atomic.Int32
}

func (b *libraryOfflineGuardBackend) FetchImage(context.Context, string) ([]byte, error) {
	b.netCalls.Add(1)
	return nil, errors.New("unexpected remote image request on offline library")
}

func seedLibraryBytesItem(t *testing.T, lib *library.Store, dir string, meta library.ItemMetadata, files map[string][]byte) library.ItemMetadata {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for rel, data := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for i := range meta.Media {
		if data, ok := files[meta.Media[i].Path]; ok {
			meta.Media[i].Bytes = int64(len(data))
			sum := sha256.Sum256(data)
			meta.Media[i].SHA256 = hex.EncodeToString(sum[:])
		}
	}
	saved, err := lib.SaveItem(dir, meta)
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

func TestLibrarySidebarStorageUsageSortingFilteringAndOfflineZeroNetwork(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)
	realBackend := app.backend.(*service.Backend)
	lib := realBackend.LibraryStore()

	// Subscription SubA combines a Bilibili account (10 KiB) + a Douyin account (20 KiB) => 2 items, 30 KiB.
	subA, err := realBackend.CreateSubscription("多站创作者A", "", []subscription.AccountRef{
		{Site: "bilibili", UID: "3001", Name: "B站号A", Face: "https://image.example.invalid/face_a.png"},
		{Site: "douyin", UID: "sec_dy_a", Name: "抖音号A", Face: "https://image.example.invalid/face_dy_a.png"},
	}, subscription.SubRules{})
	if err != nil {
		t.Fatal(err)
	}
	seedLibraryBytesItem(t, lib, filepath.Join(lib.CacheDir(), "bilibili", "3001", "videos", "BV1subA01"), library.ItemMetadata{
		Version: 1, Site: "bilibili", Kind: "video", ID: "BV1subA01", Title: "订阅A B站视频", PubTs: 1700000100,
		Author: library.ItemAuthor{Site: "bilibili", Mid: 3001, UID: "3001", Name: "B站号A", Face: "https://image.example.invalid/face_a.png"},
		Media:  []library.MediaFile{{Role: "video", Path: "P01.mp4"}},
	}, map[string][]byte{"P01.mp4": make([]byte, 10*1024)})

	seedLibraryBytesItem(t, lib, filepath.Join(lib.CacheDir(), "douyin", "sec_dy_a", "works", "70001"), library.ItemMetadata{
		Version: 1, Site: "douyin", Kind: "video", ID: "70001", Title: "订阅A 抖音作品", PubTs: 1700000110,
		Author: library.ItemAuthor{Site: "douyin", UID: "sec_dy_a", Name: "抖音号A"},
		Media:  []library.MediaFile{{Role: "video", Path: "video.mp4"}},
	}, map[string][]byte{"video.mp4": make([]byte, 20*1024)})

	// Unsubscribed author HeavyB has 1 large Bilibili video (200 KiB) — much larger than SubA!
	seedLibraryBytesItem(t, lib, filepath.Join(lib.CacheDir(), "bilibili", "4002", "videos", "BV1heavy01"), library.ItemMetadata{
		Version: 1, Site: "bilibili", Kind: "video", ID: "BV1heavy01", Title: "大体积独立UP视频", PubTs: 1700000120,
		Author: library.ItemAuthor{Site: "bilibili", Mid: 4002, UID: "4002", Name: "大体积UP", Face: "https://image.example.invalid/heavy.png"},
		Media:  []library.MediaFile{{Role: "video", Path: "P01.mp4"}},
	}, map[string][]byte{"P01.mp4": make([]byte, 200*1024)})

	guard := &libraryOfflineGuardBackend{BackendAPI: realBackend}
	app.backend = guard
	app.RefreshData()
	app.Navigate(RouteLibrary)

	// Default ordering: subscriptions first (SubA), then unsubscribed authors (HeavyB).
	rows := app.libraryPanelRows()
	if len(rows) < 2 || !rows[0].isSub || rows[0].sub.ID != subA.ID {
		t.Fatalf("default order should put subscription first, got %+v", rows)
	}
	if rows[0].itemCount != 2 || rows[0].totalBytes != 30*1024 {
		t.Fatalf("SubA aggregated usage = (%d items, %d bytes), want (2, 30720)", rows[0].itemCount, rows[0].totalBytes)
	}
	if rows[1].isSub || rows[1].author.Mid != 4002 || rows[1].itemCount != 1 || rows[1].totalBytes != 200*1024 {
		t.Fatalf("HeavyB usage = %+v, want mid=4002 (1, 204800)", rows[1])
	}

	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	if !tt.HasText("2 项 · 30 KB") || !tt.HasText("1 项 · 200 KB") {
		t.Fatalf("sidebar missing formatted item count / storage size readouts (want 2 项 · 30 KB and 1 项 · 200 KB)")
	}

	// Click "按占用空间" toggle: HeavyB (200 KiB) must move above SubA (30 KiB).
	if err := tt.Click("媒体库来源按占用空间排序"); err != nil {
		t.Fatalf("click sort by size: %v", err)
	}
	tt.Frame()
	if !app.LibrarySourceSortBySize {
		t.Fatal("LibrarySourceSortBySize did not toggle on")
	}
	sortedRows := app.libraryPanelRows()
	if len(sortedRows) < 2 || sortedRows[0].isSub || sortedRows[0].author.Mid != 4002 {
		t.Fatalf("expected HeavyB first when sorted by storage usage, got %+v", sortedRows)
	}

	// Clicking HeavyB filters the library grid to that author.
	if err := tt.Click("按UP主筛选 4002"); err != nil {
		t.Fatalf("click author row: %v", err)
	}
	tt.Frame()
	if app.LibraryCreatorMid != 4002 || len(app.LibraryItems) != 1 || app.LibraryItems[0].RawID != "BV1heavy01" {
		t.Fatalf("filtering to HeavyB yielded %+v", app.LibraryItems)
	}

	// Switching site filter to "douyin" aggregates SubA's Douyin items only (1 item, 20 KiB) and omits Bilibili-only HeavyB.
	if err := tt.Click("媒体库站点筛选：抖音"); err != nil {
		t.Fatalf("click douyin filter: %v", err)
	}
	tt.Frame()
	dyRows := app.libraryPanelRows()
	if len(dyRows) != 1 || !dyRows[0].isSub || dyRows[0].itemCount != 1 || dyRows[0].totalBytes != 20*1024 {
		t.Fatalf("douyin site filter panel rows = %+v, want only SubA (1, 20480)", dyRows)
	}

	// Narrow window (720px) exposes author filter + storage usage + sort toggle without overflow.
	app.LibrarySiteFilter = "all"
	app.selectLibraryFilter()
	narrow := ui.NewTester(app.View, 720, 860)
	narrow.Frame()
	if err := narrow.Click("媒体库作者筛选"); err != nil {
		t.Fatalf("click narrow author filter: %v", err)
	}
	narrow.Frame()
	if _, ok := narrow.Find("媒体库来源按占用空间排序"); !ok {
		t.Fatal("narrow author filter missing 按占用空间 toggle")
	}
	if !narrow.HasText("1 项 · 200 KB") {
		t.Fatal("narrow author filter missing storage size readout")
	}
	centerRect, okC := narrow.Find("中栏内容")
	if !okC || centerRect.X+centerRect.W > 720.5 {
		t.Fatalf("720px narrow library overflowed horizontally: %+v", centerRect)
	}

	// Strictly zero remote network requests across all offline Library interactions.
	if guard.netCalls.Load() != 0 {
		t.Fatalf("offline library triggered %d remote image requests (want 0)", guard.netCalls.Load())
	}
}

func TestLibraryMultiSelectTileSyncBatchDeleteAndBatchExport(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)
	realBackend := app.backend.(*service.Backend)
	lib := realBackend.LibraryStore()

	// 1. Multi-tile dynamic D1 (2 images => 2 grid tiles) referencing exclusive video V_excl and shared video V_shared.
	vExclDir := filepath.Join(lib.CacheDir(), "bilibili", "5001", "videos", "BV1uiexcl1")
	vExcl := seedLibraryBytesItem(t, lib, vExclDir, library.ItemMetadata{
		Version: 1, Site: "bilibili", Kind: "video", ID: "BV1uiexcl1", Title: "独占引用视频", PubTs: 1700000100,
		DirectCached: false, Author: library.ItemAuthor{Site: "bilibili", Mid: 5001, UID: "5001", Name: "作者甲"},
		Media: []library.MediaFile{{Role: "video", Path: "P01.mp4"}},
		Parts: []library.VideoPartRecord{{CID: 11, Page: 1, File: "P01.mp4"}},
	}, map[string][]byte{
		"P01.mp4":      make([]byte, 4096),
		"my-notes.txt": []byte("preserve-me"),
	})

	vShared := seedLibraryBytesItem(t, lib, filepath.Join(lib.CacheDir(), "bilibili", "5001", "videos", "BV1uishr02"), library.ItemMetadata{
		Version: 1, Site: "bilibili", Kind: "video", ID: "BV1uishr02", Title: "共用保留视频", PubTs: 1700000105,
		DirectCached: false, Author: library.ItemAuthor{Site: "bilibili", Mid: 5001, UID: "5001", Name: "作者甲"},
		Media: []library.MediaFile{{Role: "video", Path: "P01.mp4"}},
		Parts: []library.VideoPartRecord{{CID: 12, Page: 1, File: "P01.mp4"}},
	}, map[string][]byte{"P01.mp4": make([]byte, 2048)})

	d1 := seedLibraryBytesItem(t, lib, filepath.Join(lib.CacheDir(), "bilibili", "5001", "dynamics", "dyn_multi_1"), library.ItemMetadata{
		Version: 1, Site: "bilibili", Kind: "dynamic", ID: "dyn_multi_1", Title: "多图动态一", PubTs: 1700000300,
		Author: library.ItemAuthor{Site: "bilibili", Mid: 5001, UID: "5001", Name: "作者甲"},
		Videos: []string{"BV1uiexcl1", "BV1uishr02"},
		Media: []library.MediaFile{
			{Role: "image", Path: "images/01.png"},
			{Role: "image", Path: "images/02.png"},
		},
	}, map[string][]byte{
		"images/01.png": []byte("fake-png-1"),
		"images/02.png": []byte("fake-png-2"),
	})

	d2 := seedLibraryBytesItem(t, lib, filepath.Join(lib.CacheDir(), "bilibili", "5001", "dynamics", "dyn_multi_2"), library.ItemMetadata{
		Version: 1, Site: "bilibili", Kind: "dynamic", ID: "dyn_multi_2", Title: "单图动态二", PubTs: 1700000290,
		Author: library.ItemAuthor{Site: "bilibili", Mid: 5001, UID: "5001", Name: "作者甲"},
		Videos: []string{"BV1uiexcl1"},
		Media:  []library.MediaFile{{Role: "image", Path: "images/01.png"}},
	}, map[string][]byte{"images/01.png": []byte("fake-png-3")})

	// Unselected dynamic D3 keeping V_shared alive.
	d3 := seedLibraryBytesItem(t, lib, filepath.Join(lib.CacheDir(), "bilibili", "5001", "dynamics", "dyn_keep_3"), library.ItemMetadata{
		Version: 1, Site: "bilibili", Kind: "dynamic", ID: "dyn_keep_3", Title: "保留动态三", PubTs: 1700000280,
		Author: library.ItemAuthor{Site: "bilibili", Mid: 5001, UID: "5001", Name: "作者甲"},
		Videos: []string{"BV1uishr02"},
		Media:  []library.MediaFile{{Role: "image", Path: "images/01.png"}},
	}, map[string][]byte{"images/01.png": []byte("fake-png-4")})

	guard := &libraryOfflineGuardBackend{BackendAPI: realBackend}
	app.backend = guard
	app.RefreshData()
	app.Navigate(RouteLibrary)

	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()

	// Enter multi-select via header toggle button.
	if err := tt.Click("媒体库多选"); err != nil {
		t.Fatalf("click multi-select toggle: %v", err)
	}
	tt.Frame()
	if !app.LibrarySelectMode {
		t.Fatal("LibrarySelectMode not enabled")
	}
	if _, ok := tt.Find("媒体库批量操作栏"); !ok {
		t.Fatal("batch action bar missing in multi-select mode")
	}
	// Card "⋯" menus must be hidden in multi-select mode.
	if _, found := tt.Find("媒体更多：" + d1.ItemKey); found {
		t.Fatal("card ⋯ menu should be hidden in multi-select mode")
	}

	// Clicking tile 1 of multi-tile dynamic d1 selects d1.ItemKey (without opening viewer)
	// and visually syncs BOTH tile 1 and tile 2!
	if err := tt.Click("媒体缩略图 " + d1.ItemKey); err != nil {
		t.Fatalf("click tile 1 of d1: %v", err)
	}
	tt.Frame()
	if app.ImageViewer.Open || app.PlayerOverlay.Open {
		t.Fatal("clicking a card in multi-select mode must not open viewer/player")
	}
	if app.librarySelectedCount() != 1 || !app.LibrarySelectedKeys[d1.ItemKey] {
		t.Fatalf("expected 1 selected item (%s), got %v", d1.ItemKey, app.LibrarySelectedKeys)
	}
	if _, ok := tt.Find("已选媒体：" + d1.ItemKey); !ok {
		t.Fatal("tile 1 missing selected badge")
	}
	if _, ok := tt.Find("已选媒体：" + d1.ItemKey + ":images/02.png"); !ok {
		t.Fatal("tile 2 of same multi-tile item did not stay visually synced")
	}

	// Clicking tile 2 toggles d1 OFF and unchecks both tiles.
	if err := tt.Click("媒体缩略图 " + d1.ItemKey + ":images/02.png"); err != nil {
		t.Fatalf("click tile 2 of d1: %v", err)
	}
	tt.Frame()
	if app.librarySelectedCount() != 0 {
		t.Fatalf("expected 0 selected items after toggling tile 2, got %d", app.librarySelectedCount())
	}

	// Select all loaded / clear all.
	if err := tt.Click("媒体库全选已加载"); err != nil {
		t.Fatalf("click select all loaded: %v", err)
	}
	tt.Frame()
	if app.librarySelectedCount() != 5 {
		t.Fatalf("expected all 5 loaded items selected, got %d", app.librarySelectedCount())
	}
	if _, ok := tt.Find("媒体库取消全选"); !ok {
		t.Fatal("expected 取消全选 button after selecting all loaded items")
	}
	if err := tt.Click("媒体库取消全选"); err != nil {
		t.Fatalf("click clear all: %v", err)
	}
	tt.Frame()
	if app.librarySelectedCount() != 0 {
		t.Fatalf("expected 0 selected items after clear all, got %d", app.librarySelectedCount())
	}

	// Verify responsive widths 720, 1000, 1280 have zero horizontal overflow with batch bar active.
	app.toggleLibraryItemSelection(d1.ItemKey)
	app.toggleLibraryItemSelection(d2.ItemKey)
	for _, w := range []int{720, 1000, 1280} {
		rt := ui.NewTester(app.View, w, 860)
		rt.Frame()
		centerRect, okC := rt.Find("中栏内容")
		barRect, okB := rt.Find("媒体库批量操作栏")
		if !okC || !okB {
			t.Fatalf("width %d missing center or batch bar: okC=%v okB=%v", w, okC, okB)
		}
		if centerRect.X+centerRect.W > float32(w)+0.5 || barRect.X+barRect.W > float32(w)+0.5 {
			t.Fatalf("width %d horizontal overflow: center=%+v bar=%+v", w, centerRect, barRect)
		}
		for _, lbl := range []string{"媒体库批量导出", "媒体库批量删除", "退出多选"} {
			r, ok := rt.Find(lbl)
			if !ok || r.X+r.W > float32(w)+0.5 {
				t.Fatalf("width %d batch button %q clipped or missing: ok=%v rect=%+v", w, lbl, ok, r)
			}
		}
	}

	// Batch Export: opens export modal pre-selected to "selected" scope ("已选 2 项"), exports ONLY d1 & d2.
	tt.Frame()
	if err := tt.Click("媒体库批量导出"); err != nil {
		t.Fatalf("click batch export: %v", err)
	}
	tt.Frame()
	if !app.Transfer.ExportOpen || app.Transfer.ExportScope != "selected" {
		t.Fatalf("expected ExportOpen=true ExportScope=selected, got open=%v scope=%q", app.Transfer.ExportOpen, app.Transfer.ExportScope)
	}
	if !tt.HasText("已选 2 项") {
		t.Fatal("export dialog missing 已选 2 项 scope text")
	}
	// Esc closes export dialog WITHOUT exiting multi-select!
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if app.Transfer.ExportOpen {
		t.Fatal("Esc did not close export dialog")
	}
	if !app.LibrarySelectMode || app.librarySelectedCount() != 2 {
		t.Fatalf("Esc while modal was open must keep multi-select active: mode=%v count=%d", app.LibrarySelectMode, app.librarySelectedCount())
	}
	// Esc closes ForceRecacheConfirm WITHOUT exiting multi-select either (DEC-079/DEC-083 merge guard).
	app.ForceRecacheConfirm = ForceRecacheConfirmState{Open: true, ItemKey: d1.ItemKey, Title: d1.Title}
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if app.ForceRecacheConfirm.Open {
		t.Fatal("Esc did not close ForceRecacheConfirm dialog")
	}
	if !app.LibrarySelectMode || app.librarySelectedCount() != 2 {
		t.Fatalf("Esc while ForceRecacheConfirm was open must keep multi-select active: mode=%v count=%d", app.LibrarySelectMode, app.librarySelectedCount())
	}

	// Re-open batch export and run real export.
	posted := make(chan func(), 64)
	app.BindEventLoop(func(fn func()) { posted <- fn })
	if err := tt.Click("媒体库批量导出"); err != nil {
		t.Fatal(err)
	}
	drainPosted(posted)
	tt.Frame()
	exportDir := filepath.Join(t.TempDir(), "ui-batch-export")
	app.Transfer.ExportPath = exportDir
	if err := tt.Click("开始导出"); err != nil {
		t.Fatalf("click start export: %v", err)
	}
	drainPosted(posted)
	tt.Frame()
	if app.CurrentRoute != RouteTasks || app.effectiveTasksSection() != TasksSectionRecords {
		t.Fatalf("expected navigation to Tasks records, got route=%s section=%s", app.CurrentRoute, app.TasksSection)
	}
	if app.LibrarySelectMode || app.librarySelectedCount() != 0 {
		t.Fatal("starting batch export must exit and clear multi-select")
	}
	deadline := time.Now().Add(10 * time.Second)
	var finishedJob *service.TransferJob
	for time.Now().Before(deadline) {
		drainPosted(posted)
		for _, j := range realBackend.TransferJobs() {
			if j.Kind == "export" && !j.Active() {
				cp := j
				finishedJob = &cp
				break
			}
		}
		if finishedJob != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	drainPosted(posted)
	if finishedJob == nil || finishedJob.Status != service.TransferDone || finishedJob.Export == nil || finishedJob.Export.Items != 2 {
		t.Fatalf("batch export job did not export exactly 2 items: %+v", finishedJob)
	}

	// Navigate back to Library, test Esc exiting multi-select when no modal is open, and nav-away clearing.
	app.Navigate(RouteLibrary)
	app.enterLibrarySelectMode()
	app.toggleLibraryItemSelection(d1.ItemKey)
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if app.LibrarySelectMode || app.librarySelectedCount() != 0 {
		t.Fatal("Esc with no modal open must exit multi-select and clear selection")
	}

	app.enterLibrarySelectMode()
	app.toggleLibraryItemSelection(d1.ItemKey)
	app.Navigate(RouteHome)
	if app.LibrarySelectMode || app.librarySelectedCount() != 0 {
		t.Fatal("navigating away from Library must exit multi-select and clear selection")
	}

	// Batch Delete Confirmation & Execution: select d1 + d2 (exclusively holding vExcl, sharing vShared with unselected d3).
	app.Navigate(RouteLibrary)
	app.enterLibrarySelectMode()
	app.toggleLibraryItemSelection(d1.ItemKey)
	app.toggleLibraryItemSelection(d2.ItemKey)
	tt.Frame()
	if err := tt.Click("媒体库批量删除"); err != nil {
		t.Fatalf("click batch delete: %v", err)
	}
	tt.Frame()
	if !app.DeleteConfirm.Open || !app.DeleteConfirm.Batch {
		t.Fatalf("expected batch delete confirmation open, got %+v", app.DeleteConfirm)
	}
	for _, wantText := range []string{
		"批量删除缓存确认",
		"已选择 2 个媒体库条目",
		"同时删除所选动态独占引用的 1 个视频",
		"所选动态引用的 1 个视频单独缓存过或还被未选动态引用，将保留",
		"安全保留：目录内 1 个不是 TRACE 创建的文件将原样保留",
	} {
		if !tt.HasText(wantText) {
			t.Fatalf("batch delete modal missing text %q", wantText)
		}
	}

	if err := tt.Click("确认删除"); err != nil {
		t.Fatalf("click confirm batch delete: %v", err)
	}
	tt.Frame()
	if app.DeleteConfirm.Open || app.LibrarySelectMode || app.librarySelectedCount() != 0 {
		t.Fatalf("batch delete did not cleanly close modal and exit multi-select: open=%v mode=%v count=%d",
			app.DeleteConfirm.Open, app.LibrarySelectMode, app.librarySelectedCount())
	}
	if !strings.Contains(app.BannerInfo, "保留了 1 个不是 TRACE 创建的文件") {
		t.Fatalf("expected BannerInfo to note preserved non-TRACE file, got %q", app.BannerInfo)
	}
	if _, ok, _ := lib.GetItem(d1.ItemKey); ok {
		t.Fatal("d1 should be deleted")
	}
	if _, ok, _ := lib.GetItem(d2.ItemKey); ok {
		t.Fatal("d2 should be deleted")
	}
	if _, ok, _ := lib.GetItem(vExcl.ItemKey); ok {
		t.Fatal("exclusively referenced vExcl should be deleted")
	}
	if _, ok, _ := lib.GetItem(vShared.ItemKey); !ok {
		t.Fatal("vShared referenced by unselected d3 should be preserved")
	}
	if _, ok, _ := lib.GetItem(d3.ItemKey); !ok {
		t.Fatal("unselected d3 should be preserved")
	}
	if data, err := os.ReadFile(filepath.Join(vExclDir, "my-notes.txt")); err != nil || string(data) != "preserve-me" {
		t.Fatalf("user file my-notes.txt was not preserved: err=%v data=%q", err, string(data))
	}
	if guard.netCalls.Load() != 0 {
		t.Fatalf("offline library batch operations triggered %d network calls (want 0)", guard.netCalls.Load())
	}
}
