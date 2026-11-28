package library

import (
	"testing"
	"time"

	"github.com/chialecode/trace/internal/companionapi"
	"github.com/egoist/mygo/ui"
)

func TestMobileLibraryDetail_WeiboEditHistoryOfflineZeroExternal(t *testing.T) {
	srv, host, cleanup := newSyntheticLibServer(t, 2)
	defer cleanup()

	key := "weibo:dynamic:5009001002003099"
	srv.mu.Lock()
	srv.details[key] = companionapi.LibraryItemDetail{
		ItemKey:    key,
		ID:         "dynamic:5009001002003099",
		Site:       "weibo",
		Kind:       "dynamic",
		Status:     "complete",
		Title:      "移动端已编辑微博示例",
		Text:       "最终版本正文",
		PubTs:      1764203600,
		AuthorUID:  "500000001",
		AuthorName: "合成微博博主",
		EditCount:  2,
		Media: []companionapi.MediaFileDTO{
			{Role: "image", Path: "images/01.png", Width: 640, Height: 480},
			{Role: "edit_image", Path: "images/edit_v01_01.png", Width: 640, Height: 480},
		},
		EditHistory: []companionapi.ItemEditRevisionDTO{
			{
				Version:    3,
				EditTs:     1764203600,
				Text:       "最终版本正文",
				Links:      []companionapi.TextLink{{Label: "参考链接", URL: "https://example.org/final"}},
				MediaPaths: []string{"images/01.png"},
			},
			{
				Version:    1,
				EditTs:     1764200000,
				Text:       "原始首发正文，附历史旧图",
				MediaPaths: []string{"images/edit_v01_01.png"},
			},
		},
	}
	srv.mu.Unlock()

	page := New()
	page.nowFn = func() time.Time { return time.Unix(1764210000, 0) }
	page.mu.Lock()
	det := srv.details[key]
	page.detailKey = key
	page.detail = &det
	page.mu.Unlock()

	tt := ui.NewTester(func(c *ui.Context) {
		page.renderDetailBody(c, host, det)
	}, 360, 1200)
	tt.Frame()

	sec, ok := tt.Find("移动端离线编辑历史")
	if !ok {
		t.Fatal("expected 移动端离线编辑历史 section rendered")
	}
	if sec.X+sec.W > 361 {
		t.Fatalf("mobile edit history overflowed 360dp width: %+v", sec)
	}
	assertNoHorizontalOverflow(t, tt, 360)
	if _, ok := tt.Find("编辑历史版本 3"); !ok {
		t.Fatal("expected 编辑历史版本 3 rendered in mobile detail")
	}
	if _, ok := tt.Find("编辑历史版本 1"); !ok {
		t.Fatal("expected 编辑历史版本 1 rendered in mobile detail")
	}
	if _, ok := tt.Find("打开链接 参考链接"); !ok {
		t.Fatal("expected revision hyperlink rendered in mobile detail")
	}

	// Collapse and verify cards hide cleanly.
	if err := tt.Click("切换编辑历史"); err != nil {
		t.Fatalf("click 切换编辑历史: %v", err)
	}
	tt.Frame()
	if _, ok := tt.Find("编辑历史版本 1"); ok {
		t.Fatal("expected revision cards collapsed after toggle tap")
	}
}
