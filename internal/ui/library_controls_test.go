package ui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
	"github.com/egoist/mygo/ui"
)

type capturedLocalVideoCacheBackend struct {
	service.BackendAPI
	request *download.VideoEnqueueRequest
}

func (b *capturedLocalVideoCacheBackend) EnqueueVideoCache(_ context.Context, req download.VideoEnqueueRequest) ([]download.Task, error) {
	b.request = &req
	return []download.Task{{TaskID: "synthetic-cache"}}, nil
}

func TestCachedVideoDetailRequestsFreshMetadataAndKeepsKnownMissingPages(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)
	item := seedLibraryControlItem(t, app, library.ItemMetadata{
		Site: "bilibili", Kind: "video", ID: "BV1LocalPartial", Title: "synthetic partial video",
		Media: []library.MediaFile{{Role: "video", Path: "P01.mp4"}},
		Parts: []library.VideoPartRecord{{Page: 1, CID: 101, File: "P01.mp4"}}, ExpectedPages: []int{1, 2},
	})
	app.OpenLibraryItemDetail(item)
	backend := &capturedLocalVideoCacheBackend{BackendAPI: app.backend}
	app.backend = backend
	tt := ui.NewTester(app.View, 1000, 860)
	if _, found := tt.Find("选择分P 2"); !found {
		t.Fatal("cached detail omitted a known missing part")
	}
	// Counts are not kept (DEC-040): a cached video shows none, never zeros.
	if tt.HasText("播放 0") || tt.HasText("评论 0") {
		t.Fatal("cached detail showed absent interaction statistics as real zero counts")
	}
	if err := tt.Click("缓存视频"); err != nil {
		t.Fatal(err)
	}
	if backend.request == nil || backend.request.Detail != nil || backend.request.BVID != item.RawID || len(backend.request.Pages) != 2 {
		t.Fatalf("cached detail supplied incomplete offline metadata or omitted missing pages: %+v", backend.request)
	}
}

func seedLibraryControlItem(t *testing.T, app *App, item library.ItemMetadata) library.ItemMetadata {
	t.Helper()
	lib := app.backend.(*service.Backend).LibraryStore()
	dir := filepath.Join(lib.CacheDir(), item.Kind, item.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, media := range item.Media {
		path := filepath.Join(dir, filepath.FromSlash(media.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("synthetic media"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	saved, err := lib.SaveItem(dir, item)
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

func TestLibraryTitleOpensMediaAndFileChoiceDoesNotDependOnPartOrder(t *testing.T) {
	for _, target := range []string{"title", "thumbnail"} {
		t.Run(target, func(t *testing.T) {
			app, _, cleanup := newTestUIApp(t)
			defer cleanup()
			seedUIFeed(t, app, 0)
			item := seedLibraryControlItem(t, app, library.ItemMetadata{
				Site: "bilibili", Kind: "video", ID: "BV1FileOrder", Title: "synthetic multipart video",
				Media: []library.MediaFile{{Role: "video", Path: "P01.mp4"}, {Role: "video", Path: "P02.mp4"}},
				Parts: []library.VideoPartRecord{{Page: 2, File: "P02.mp4"}, {Page: 1, File: "P01.mp4"}},
			})
			app.RefreshData()
			app.Navigate(RouteLibrary)
			tt := ui.NewTester(app.View, 1000, 860)
			label := "媒体缩略图 " + item.ItemKey
			if target == "title" {
				label = "打开媒体：" + item.ItemKey
			}
			if _, found := tt.Find(label); !found {
				t.Fatalf("library %s is not an accessible media action", target)
			}
			tt.Click(label)
			if app.PlayerOverlay.MediaPath != filepath.Join(item.ItemDir, "P01.mp4") {
				t.Fatalf("clicking a tile chose a different file according to Parts order: %q", app.PlayerOverlay.MediaPath)
			}
		})
	}
}

func TestCachedDynamicDetailKeepsReferencedVideo(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)
	video := seedLibraryControlItem(t, app, library.ItemMetadata{
		Site: "bilibili", Kind: "video", ID: "BV1Referenced", Title: "synthetic referenced video",
		Media: []library.MediaFile{{Role: "video", Path: "P01.mp4"}},
		Parts: []library.VideoPartRecord{{Page: 1, File: "P01.mp4"}},
	})
	dynamic := seedLibraryControlItem(t, app, library.ItemMetadata{
		Site: "bilibili", Kind: "dynamic", ID: "123", Title: "synthetic dynamic", Videos: []string{video.RawID},
	})
	app.OpenLibraryItemDetail(dynamic)
	tt := ui.NewTester(app.View, 1000, 860)
	if _, found := tt.Find("播放视频卡片：" + video.ItemKey); !found {
		t.Fatal("opening a cached dynamic's detail lost its referenced video")
	}
}

func TestLiveLibraryDetailOnlyOffersRecordingActions(t *testing.T) {
	app, desk, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)
	item := seedLibraryControlItem(t, app, library.ItemMetadata{
		Site: "bilibili", Kind: "live", ID: "44-100", Title: "synthetic recording", PubTs: 100,
		Media: []library.MediaFile{{Role: "live_segment", Path: "part-001.mp4", Duration: 5}},
		Live:  &library.LiveRecordingInfo{RoomID: 44, Quality: 10000, Codec: "avc", Segments: []library.LiveSegmentInfo{{File: "part-001.mp4", DurationSec: 5}}},
	})
	app.OpenLibraryItemDetail(item)
	tt := ui.NewTester(app.View, 1000, 860)
	for _, invalidAction := range []string{"缓存视频", "选择分P 1", "补全：" + item.ItemKey} {
		if _, found := tt.Find(invalidAction); found {
			t.Fatalf("a recording was treated as a BVID with action %q", invalidAction)
		}
	}
	if _, found := tt.Find("直播录像详情"); !found {
		t.Fatal("recording detail does not identify its actual media type")
	}
	if _, found := tt.Find("· 原画 · AVC"); found {
		t.Fatal("recording quality starts with a stray separator")
	}
	chooseMenuItem(t, tt, "帖子更多："+item.ItemKey, "在浏览器中打开")
	if actions := desk.Actions(); len(actions) != 1 || actions[0].Kind != "url" || actions[0].Target != "https://live.bilibili.com/44" {
		t.Fatalf("recording detail used a video/dynamic URL for its room: %v", actions)
	}
}

type offlineHomeBackend struct {
	service.BackendAPI
	imageCalls atomic.Int32
}

func (b *offlineHomeBackend) FetchImage(context.Context, string) ([]byte, error) {
	b.imageCalls.Add(1)
	return nil, errors.New("synthetic remote image request")
}

func TestHomeRenderingAndLocalMediaNeverFetchRemoteImages(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 3)
	backend := &offlineHomeBackend{BackendAPI: app.backend}
	app.backend = backend
	posted := make(chan func(), 16)
	app.dispatch = func(fn func()) { posted <- fn }
	app.FeedItems[0].AuthorFace = "https://image.example.invalid/face.png"
	app.FeedItems[0].CoverURL = "https://image.example.invalid/cover.png"
	tt := ui.NewTester(app.View, 1000, 860)
	tt.Frame()
	app.mu.Lock()
	imagePending := false
	for key := range app.pending {
		if strings.HasPrefix(key, "public-image:") {
			imagePending = true
		}
	}
	app.mu.Unlock()
	if imagePending || backend.imageCalls.Load() != 0 {
		t.Fatal("rendering the local home feed scheduled remote thumbnail requests")
	}
	post := app.feedEntryToPostCard(app.FeedItems[0])
	for _, img := range post.Images {
		if img.URL != "" {
			t.Fatal("missing local media on Home fell back to a remote image URL")
		}
	}
	item, found, err := backend.GetLibraryItem(app.FeedItems[0].ItemKey)
	if err != nil || !found {
		t.Fatalf("cached item fixture is unavailable: %v", err)
	}
	item.Author.Face = "https://image.example.invalid/detail-face.png"
	app.OpenLibraryItemDetail(item)
	tt.Frame()
	app.mu.Lock()
	imagePending = false
	for key := range app.pending {
		if strings.HasPrefix(key, "public-image:") {
			imagePending = true
		}
	}
	app.mu.Unlock()
	if imagePending || backend.imageCalls.Load() != 0 {
		t.Fatal("opening a local cached detail fetched a remote avatar")
	}
}

type historyClearFailureBackend struct{ service.BackendAPI }

func (b historyClearFailureBackend) ClearHistory() error {
	return errors.New("synthetic history clear write failure")
}

func TestHistoryClearFailureKeepsConfirmationAndReportsError(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)
	if _, err := app.backend.OpenLink(context.Background(), "BV1xx411c7mD"); err != nil {
		t.Fatal(err)
	}
	app.RefreshData()
	app.Navigate(RouteParse)
	app.backend = historyClearFailureBackend{app.backend}
	app.HistoryClearConfirmOpen = true
	tt := ui.NewTester(app.View, 1000, 860)
	if err := tt.Click("确认清空历史"); err != nil {
		t.Fatal(err)
	}
	if app.BannerError == "" || !app.HistoryClearConfirmOpen || len(app.HistoryItems) == 0 {
		t.Fatal("failed history clear hid the error, dismissed confirmation or lost entries")
	}
}
