package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/settings"
	"github.com/chialecode/trace/internal/site/weibo"
	"github.com/chialecode/trace/internal/subscription"
)

func TestWeiboRemoveWatermarkDynamicToggleCacheClearAndSnapshotSync(t *testing.T) {
	ctx := context.Background()
	var showHits atomic.Int32

	const mid = "5000000000000701"
	mblogID, err := weibo.MidToMblogID(mid)
	if err != nil {
		t.Fatalf("MidToMblogID: %v", err)
	}

	wbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/ajax/setting/getBasicInfo":
			_, _ = w.Write([]byte(`{"ok":1,"data":{"screen_name":"合成测试号"}}`))
		case "/ajax/profile/info":
			_, _ = w.Write([]byte(`{"ok":1,"data":{"user":{"idstr":"1000000001","screen_name":"合成测试号"}}}`))
		case "/ajax/statuses/show":
			showHits.Add(1)
			_, _ = w.Write([]byte(fmt.Sprintf(`{
				"ok": 1,
				"idstr": %q,
				"mblogid": %q,
				"created_at": "Sat Nov 21 15:00:00 +0800 2026",
				"text_raw": "动态去水印测试微博",
				"user": {"idstr": "1000000001", "screen_name": "合成测试号"},
				"pic_ids": ["wm_static_1", "wm_gif_2", "wm_live_3"],
				"pic_infos": {
					"wm_static_1": {
						"pic_id": "wm_static_1",
						"type": "pic",
						"largest": {"url": "https://wx1.sinaimg.cn/mw2000/wm_static_1.jpg", "width": 2000, "height": 1500}
					},
					"wm_gif_2": {
						"pic_id": "wm_gif_2",
						"type": "gif",
						"largest": {"url": "https://wx1.sinaimg.cn/large/wm_gif_2.gif", "width": 480, "height": 360}
					},
					"wm_live_3": {
						"pic_id": "wm_live_3",
						"type": "livephoto",
						"largest": {"url": "https://wx2.sinaimg.cn/large/wm_live_3.jpg", "width": 1080, "height": 1440},
						"video": "https://video.weibo.com/media/play?livephoto=syn701"
					}
				}
			}`, mid, mblogID)))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer wbSrv.Close()

	base := t.TempDir()
	dataDir := filepath.Join(base, "data")
	cacheDir := filepath.Join(base, "cache")
	loc := appdirs.Locations{
		Mode:            "test",
		Channel:         "dev",
		AppName:         "trace-test",
		DataDir:         dataDir,
		DefaultCacheDir: cacheDir,
		CredentialsDir:  filepath.Join(dataDir, "credentials"),
		LogsDir:         filepath.Join(dataDir, "logs"),
		TempDir:         filepath.Join(dataDir, "tmp"),
		LockFile:        filepath.Join(dataDir, "app.lock"),
	}
	b, err := NewBackend(Options{
		Locations: loc,
		Desktop:   &MockDesktop{},
		WeiboOpts: weibo.ClientOptions{
			Endpoints: weibo.Endpoints{
				PCBasicInfo:  wbSrv.URL + "/ajax/setting/getBasicInfo",
				ProfileInfo:  wbSrv.URL + "/ajax/profile/info",
				StatusesShow: wbSrv.URL + "/ajax/statuses/show",
			},
			MinInterval:    -1,
			JitterMax:      -1,
			AllowLocalhost: true,
		},
	})
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	defer func() { _ = b.Close() }()

	if !b.GetSettings().WeiboRemoveWatermark {
		t.Fatal("default WeiboRemoveWatermark = false, want true")
	}

	if _, err := b.LoginWithCookie(ctx, "weibo", "SUB=SYNTHETIC_SUB_WM_TEST; SUBP=SYNTH_SUBP", ""); err != nil {
		t.Fatalf("LoginWithCookie: %v", err)
	}

	// 1. Default enabled -> static image & LivePhoto still rewrite to /oslarge/ with original in FallbackURLs[0]; GIF untouched.
	pvOn, err := b.WeiboPost(ctx, mid)
	if err != nil {
		t.Fatalf("WeiboPost(on): %v", err)
	}
	if len(pvOn.Images) != 3 {
		t.Fatalf("len(Images) = %d, want 3", len(pvOn.Images))
	}
	if pvOn.Images[0].ImageURL != "https://wx1.sinaimg.cn/oslarge/wm_static_1.jpg" ||
		len(pvOn.Images[0].FallbackURLs) == 0 ||
		pvOn.Images[0].FallbackURLs[0] != "https://wx1.sinaimg.cn/mw2000/wm_static_1.jpg" {
		t.Fatalf("enabled static pic = (%q, fb=%v)", pvOn.Images[0].ImageURL, pvOn.Images[0].FallbackURLs)
	}
	if pvOn.Images[1].ImageURL != "https://wx1.sinaimg.cn/large/wm_gif_2.gif" {
		t.Fatalf("enabled gif pic = %q, want original .gif", pvOn.Images[1].ImageURL)
	}
	if pvOn.Images[2].ImageURL != "https://wx2.sinaimg.cn/oslarge/wm_live_3.jpg" ||
		len(pvOn.Images[2].FallbackURLs) == 0 ||
		pvOn.Images[2].FallbackURLs[0] != "https://wx2.sinaimg.cn/large/wm_live_3.jpg" {
		t.Fatalf("enabled livephoto still = (%q, fb=%v)", pvOn.Images[2].ImageURL, pvOn.Images[2].FallbackURLs)
	}
	if _, ok := b.weiboPostCache.get(mid, b.preferredWeiboCodec(), b.currentWeiboMaxResolution(), true); !ok {
		t.Fatal("expected post to be cached in weiboPostCache after WeiboPost")
	}

	// 2. Disable via UpdateSettings -> clears weiboPostCache immediately and takes effect without restart!
	cfg := b.GetSettings()
	cfg.WeiboRemoveWatermark = false
	updated, err := b.UpdateSettings(cfg)
	if err != nil || updated.WeiboRemoveWatermark {
		t.Fatalf("UpdateSettings(false) = (%+v, %v)", updated, err)
	}
	if _, ok := b.weiboPostCache.get(mid, b.preferredWeiboCodec(), b.currentWeiboMaxResolution(), true); ok {
		t.Fatal("UpdateSettings toggling WeiboRemoveWatermark did not clear weiboPostCache")
	}
	if _, ok := b.weiboPostCache.get(mid, b.preferredWeiboCodec(), b.currentWeiboMaxResolution(), false); ok {
		t.Fatal("weiboPostCache unexpectedly returned entry after clear")
	}

	pvOff, err := b.WeiboPost(ctx, mid)
	if err != nil {
		t.Fatalf("WeiboPost(off): %v", err)
	}
	if pvOff.Images[0].ImageURL != "https://wx1.sinaimg.cn/mw2000/wm_static_1.jpg" ||
		strings.Contains(pvOff.Images[0].ImageURL, "/oslarge/") {
		t.Fatalf("disabled static pic = %q, want original /mw2000/", pvOff.Images[0].ImageURL)
	}
	if pvOff.Images[2].ImageURL != "https://wx2.sinaimg.cn/large/wm_live_3.jpg" {
		t.Fatalf("disabled livephoto still = %q, want original /large/", pvOff.Images[2].ImageURL)
	}
	if showHits.Load() < 2 {
		t.Fatalf("expected fresh fetch after toggle, showHits=%d", showHits.Load())
	}

	// 3. Remote subscription snapshot propagates WeiboRemoveWatermark=false and legacy nil preserves default true.
	snap, err := b.SubscriptionSnapshot()
	if err != nil || snap.Settings == nil || snap.Settings.WeiboRemoveWatermark == nil || *snap.Settings.WeiboRemoveWatermark {
		t.Fatalf("SubscriptionSnapshot settings = %+v, err=%v", snap.Settings, err)
	}
	remoteSet := applySnapshotSettings(settings.Default(filepath.Join(base, "remote")), *snap.Settings)
	if remoteSet.WeiboRemoveWatermark {
		t.Fatal("applySnapshotSettings did not apply WeiboRemoveWatermark=false")
	}
	legacySet := applySnapshotSettings(settings.Default(filepath.Join(base, "legacy")), subscription.SnapshotSettings{VideoQuality: "best"})
	if !legacySet.WeiboRemoveWatermark {
		t.Fatal("applySnapshotSettings with nil WeiboRemoveWatermark overwrote default true")
	}
}
