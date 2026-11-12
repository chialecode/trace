package ui

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/chialecode/trace/internal/library"
	"github.com/egoist/mygo/ui"
)

// livePhotoStub records which site's CDN the viewer asks to stage a clip from.
type livePhotoStub struct {
	*douyinStubBackend
	clip      string
	stagedFor []string
}

func (s *livePhotoStub) StagePreviewMedia(_ context.Context, siteID, rawURL, _ string) (string, error) {
	s.stagedFor = append(s.stagedFor, siteID+" "+rawURL)
	return s.clip, nil
}

// DEC-046 ①: a Bilibili live photo plays its motion clip inside the viewer,
// online before caching (staged from the Bilibili CDN) and from the cached
// clip afterwards, without leaving the viewer.
func TestBilibiliLivePhotoPlaysInTheViewer(t *testing.T) {
	app, stub, _, cleanup := newDouyinUITestHarness(t)
	defer cleanup()
	posted := make(chan func(), 32)
	app.dispatch = func(fn func()) { posted <- fn }
	_, _ = stub.raw.ImportCookie(context.Background(), "SESSDATA=fixture; bili_jct=csrf; DedeUserID=12345", "")

	rootDir := stub.raw.GetSettings().CacheDir
	if err := os.MkdirAll(rootDir, 0o700); err != nil {
		t.Fatal(err)
	}
	mp4Bytes := buildTestMP4Bytes(t, rootDir)
	staged := filepath.Join(rootDir, "staged_clip.mp4")
	if err := os.WriteFile(staged, mp4Bytes, 0o600); err != nil {
		t.Fatal(err)
	}
	ls := &livePhotoStub{douyinStubBackend: stub, clip: staged}
	app.backend = ls

	post := PostCardData{Key: "bili-live", Site: "bilibili", Images: []PostImage{
		{URL: "https://i0.hdslb.com/bfs/synthetic/still.jpg", Width: 1080, Height: 1440, Live: true, LiveURL: "https://i0.hdslb.com/bfs/synthetic/clip.mp4"},
		{URL: "https://i0.hdslb.com/bfs/synthetic/plain.jpg", Width: 1080, Height: 1440},
	}}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Size(1280, 860).Children(func() {
			ui.Column(c).Width(720).Children(func() { app.renderPostCard(c, post) })
			if app.ImageViewer.Open {
				app.renderImageViewerOverlay(c, 1280, 860)
			}
		})
	}, 1280, 860)
	if err := tt.Click("查看图片 1"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !app.ImageViewer.Open || app.ImageViewer.InlineSite != "bilibili" {
		t.Fatalf("viewer not open for a Bilibili live photo: %+v", app.ImageViewer.InlineSite)
	}
	if err := tt.Click("看图播放实况"); err != nil {
		t.Fatalf("an uncached live photo has no play button in the viewer: %v", err)
	}
	nextPosted(t, posted)()
	waitUIIdle(t, app, posted)
	if len(ls.stagedFor) != 1 || ls.stagedFor[0] != "bilibili https://i0.hdslb.com/bfs/synthetic/clip.mp4" {
		t.Fatalf("clip staged as %v, want from the Bilibili CDN", ls.stagedFor)
	}
	if runtime.GOOS != "windows" {
		t.Skip("the clip was staged from the right CDN; playing it needs Media Foundation")
	}
	if !playerPlaying(app.viewerPlayer) || !app.ImageViewer.Open {
		t.Fatalf("motion clip does not play inside the viewer: err=%q", app.ImageViewer.InlineError)
	}
	app.closeImageViewer()

	// Cached: the still's motion clip plays from disk, no staging.
	dir := filepath.Join(rootDir, "bilibili", "1", "dynamics", "778899")
	_ = os.MkdirAll(filepath.Join(dir, "images"), 0o700)
	_ = os.WriteFile(filepath.Join(dir, "images", "01.png"), buildTestPNGBytes(), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "images", "01_live.mp4"), mp4Bytes, 0o600)
	saved, err := stub.raw.LibraryStore().SaveItem(dir, library.ItemMetadata{
		Version: 1, ID: "778899", RawID: "778899", Site: "bilibili", Kind: "dynamic", Status: "complete",
		Author: library.ItemAuthor{Site: "bilibili", UID: "1", Name: "synthetic"},
		Media: []library.MediaFile{
			{Role: "image", Path: "images/01.png", Motion: "images/01_live.mp4", Width: 1080, Height: 1440},
			{Role: "motion", Path: "images/01_live.mp4", Duration: 3},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	app.OpenItemImageViewer(saved.ItemKey, nil, []string{"images/01.png"}, 0)
	tt.Frame()
	if err := tt.Click("看图播放实况"); err != nil {
		t.Fatalf("a cached live photo has no play button in the viewer: %v", err)
	}
	nextPosted(t, posted)()
	waitUIIdle(t, app, posted)
	if len(ls.stagedFor) != 1 {
		t.Fatalf("a cached clip must not be fetched again: %v", ls.stagedFor)
	}
	if !playerPlaying(app.viewerPlayer) || !app.ImageViewer.Open || app.PlayerOverlay.Open {
		t.Fatalf("cached motion clip must play inside the viewer: err=%q player=%v", app.ImageViewer.InlineError, app.PlayerOverlay.Open)
	}
	app.closeImageViewer()
}
