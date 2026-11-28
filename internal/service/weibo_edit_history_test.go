package service

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/weibo"
)

func synthServiceTestPNG(c color.RGBA) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func TestServiceWeiboEditHistory_ZeroCallWhenUneditedCacheReuseAndDegrade(t *testing.T) {
	pngA := synthServiceTestPNG(color.RGBA{R: 50, G: 100, B: 200, A: 255})
	pngB := synthServiceTestPNG(color.RGBA{R: 200, G: 90, B: 40, A: 255})

	var editHistoryHits atomic.Int32
	var failEditHistory atomic.Bool

	const (
		midUnedited = "5099000000000001"
		midEdited   = "5099000000000002"
		midDegraded = "5099000000000003"
	)

	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/cdn/") {
			w.Header().Set("Content-Type", "image/png")
			if strings.Contains(r.URL.Path, "old") {
				w.Header().Set("Content-Length", fmt.Sprintf("%d", len(pngB)))
				_, _ = w.Write(pngB)
			} else {
				w.Header().Set("Content-Length", fmt.Sprintf("%d", len(pngA)))
				_, _ = w.Write(pngA)
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		cdn := srvURL + "/cdn"
		switch r.URL.Path {
		case "/ajax/setting/getBasicInfo":
			_, _ = fmt.Fprintf(w, `{"ok":1,"data":{"screen_name":%q,"domain":"synthself"}}`, synthWeiboSelfName)
		case "/ajax/profile/info":
			_, _ = fmt.Fprintf(w, `{"ok":1,"data":{"user":{"id":5100000001,"idstr":%q,"screen_name":%q,"avatar_hd":%q,"statuses_count":5}}}`, synthWeiboSelfUID, synthWeiboSelfName, cdn+"/avatar.png")
		case "/ajax/statuses/show":
			id := r.URL.Query().Get("id")
			if decoded, err := weibo.MblogIDToMid(id); err == nil {
				id = decoded
			}
			switch id {
			case midUnedited:
				_, _ = fmt.Fprintf(w, `{"ok":1,"idstr":%q,"mid":%q,"created_at":"Thu Nov 28 10:00:00 +0800 2026","text_raw":"未编辑的普通微博","edit_count":0,"user":{"idstr":%q,"screen_name":%q}}`,
					midUnedited, midUnedited, synthWeiboUID, synthWeiboName)
			case midEdited:
				_, _ = fmt.Fprintf(w, `{"ok":1,"idstr":%q,"mid":%q,"created_at":"Thu Nov 28 12:30:00 +0800 2026","text_raw":"编辑后的最终版本","edit_count":1,"user":{"idstr":%q,"screen_name":%q},"pic_ids":["picCur"],"pic_infos":{"picCur":{"pic_id":"picCur","type":"pic","largest":{"url":%q,"width":64,"height":64}}}}`,
					midEdited, midEdited, synthWeiboUID, synthWeiboName, cdn+"/cur.png")
			case midDegraded:
				_, _ = fmt.Fprintf(w, `{"ok":1,"idstr":%q,"mid":%q,"created_at":"Thu Nov 28 13:00:00 +0800 2026","text_raw":"编辑历史接口受限时仍可缓存的主帖","edit_count":2,"user":{"idstr":%q,"screen_name":%q}}`,
					midDegraded, midDegraded, synthWeiboUID, synthWeiboName)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		case "/ajax/statuses/editHistory":
			editHistoryHits.Add(1)
			if failEditHistory.Load() {
				w.WriteHeader(418)
				_, _ = w.Write([]byte(`{"ok":-100,"msg":"blocked"}`))
				return
			}
			mid := r.URL.Query().Get("mid")
			_, _ = fmt.Fprintf(w, `{
				"ok": 1,
				"total": 2,
				"statuses": [
					{
						"idstr": %q,
						"created_at": "Thu Nov 28 12:30:00 +0800 2026",
						"text_raw": "编辑后的最终版本",
						"pic_ids": ["picCur"],
						"pic_infos": {"picCur": {"pic_id": "picCur", "type": "pic", "largest": {"url": %q, "width": 64, "height": 64}}}
					},
					{
						"idstr": %q,
						"created_at": "Thu Nov 28 12:00:00 +0800 2026",
						"text_raw": "原始首发版本",
						"pic_ids": ["picOld"],
						"pic_infos": {"picOld": {"pic_id": "picOld", "type": "pic", "largest": {"url": %q, "width": 64, "height": 64}}}
					}
				]
			}`, mid, cdn+"/cur.png", mid, cdn+"/old.png")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	srvURL = srv.URL

	b, err := NewBackend(Options{
		Locations:        testLocations(t),
		Desktop:          NewMockDesktop(),
		DisableBgWorkers: true,
		WeiboOpts: weibo.ClientOptions{
			Endpoints: weibo.Endpoints{
				PCBasicInfo:         srv.URL + "/ajax/setting/getBasicInfo",
				ProfileInfo:         srv.URL + "/ajax/profile/info",
				StatusesShow:        srv.URL + "/ajax/statuses/show",
				StatusesEditHistory: srv.URL + "/ajax/statuses/editHistory",
			},
			MinInterval:    -1,
			JitterMax:      -1,
			AllowLocalhost: true,
		},
	})
	if err != nil {
		t.Fatalf("New backend: %v", err)
	}
	defer b.Close()

	runCtx, cancelRun := context.WithCancel(context.Background())
	if err := b.dl.Start(runCtx); err != nil {
		cancelRun()
		t.Fatalf("dl.Start: %v", err)
	}
	defer cancelRun()

	ctx := context.Background()
	if _, err := b.LoginWithCookie(ctx, string(site.Weibo), synthWeiboCookie(), ""); err != nil {
		t.Fatalf("LoginWithCookie weibo: %v", err)
	}

	// 1. edit_count == 0 -> 0 requests to /ajax/statuses/editHistory.
	t1, err := b.CacheWeiboPost(ctx, midUnedited, nil)
	if err != nil {
		t.Fatalf("CacheWeiboPost unedited: %v", err)
	}
	if done := waitDownloadTerminal(t, b, t1.TaskID); done.Status != "completed" {
		t.Fatalf("unedited task status=%s err=%s", done.Status, done.ErrorMessage)
	}
	if editHistoryHits.Load() != 0 {
		t.Fatalf("expected 0 editHistory calls for edit_count==0, got %d", editHistoryHits.Load())
	}

	// 2. Online WeiboPostEditHistory populates memory cache -> subsequent CacheWeiboPost costs 0 extra editHistory calls!
	revs, err := b.WeiboPostEditHistory(ctx, midEdited)
	if err != nil {
		t.Fatalf("WeiboPostEditHistory: %v", err)
	}
	if len(revs) != 2 || revs[0].Version != 2 || revs[1].Version != 1 {
		t.Fatalf("unexpected online editHistory DTOs: %+v", revs)
	}
	if editHistoryHits.Load() != 1 {
		t.Fatalf("expected 1 editHistory call, got %d", editHistoryHits.Load())
	}

	t2, err := b.CacheWeiboPost(ctx, midEdited, nil)
	if err != nil {
		t.Fatalf("CacheWeiboPost edited: %v", err)
	}
	if done := waitDownloadTerminal(t, b, t2.TaskID); done.Status != "completed" {
		t.Fatalf("edited task status=%s err=%s", done.Status, done.ErrorMessage)
	}
	if editHistoryHits.Load() != 1 {
		t.Fatalf("expected in-memory editHistory cache reuse (still 1 hit), got %d", editHistoryHits.Load())
	}
	itemEdited, ok, err := b.GetLibraryItem("weibo:dynamic:" + midEdited)
	if err != nil || !ok {
		t.Fatalf("GetLibraryItem edited: ok=%v err=%v", ok, err)
	}
	if len(itemEdited.EditHistory) != 2 || len(itemEdited.EditHistory[1].MediaPaths) != 1 {
		t.Fatalf("expected 2 persisted revisions with archived old image, got %+v", itemEdited.EditHistory)
	}

	// 3. When editHistory returns HTTP 418, CacheWeiboPost degrades gracefully and main post still succeeds!
	failEditHistory.Store(true)
	t3, err := b.CacheWeiboPost(ctx, midDegraded, nil)
	if err != nil {
		t.Fatalf("CacheWeiboPost degraded: %v", err)
	}
	if done := waitDownloadTerminal(t, b, t3.TaskID); done.Status != "completed" {
		t.Fatalf("expected main post to complete despite editHistory 418, got status=%s err=%s", done.Status, done.ErrorMessage)
	}
	itemDegraded, ok, err := b.GetLibraryItem("weibo:dynamic:" + midDegraded)
	if err != nil || !ok || itemDegraded.Status != "complete" {
		t.Fatalf("expected complete degraded item, got ok=%v status=%q err=%v", ok, itemDegraded.Status, err)
	}
}
