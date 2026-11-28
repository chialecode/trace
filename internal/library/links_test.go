package library

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chialecode/trace/internal/storage"
)

func TestItemMetadataLinksBackwardCompatAndSanitization(t *testing.T) {
	tmp := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer db.Close()

	store, err := OpenWithDataDir(db, filepath.Join(tmp, "cache"), tmp)
	if err != nil {
		t.Fatalf("OpenWithDataDir: %v", err)
	}

	itemDir := filepath.Join(tmp, "cache", "weibo", "500000001_测试博主", "posts", "5000000000000001")
	saved, err := store.SaveItem(itemDir, ItemMetadata{
		Version: 1,
		ID:      "dynamic:5000000000000001",
		Site:    "weibo",
		Kind:    "dynamic",
		Status:  "complete",
		Title:   "含链接的微博归档",
		Text:    "详见 网页链接 与 #系统工程#",
		PubTs:   1710004500,
		Author:  ItemAuthor{Site: "weibo", UID: "500000001", Name: "测试博主"},
		Links: []ItemLink{
			{Label: "网页链接", URL: "https://example.com/docs/spec#sec-1"},
			{Label: "#系统工程#", URL: "https://s.weibo.com/weibo?q=%23%E7%B3%BB%E7%BB%9F%E5%B7%A5%E7%A8%8B%23"},
			{Label: "危险脚本", URL: "javascript:alert(1)"},
			{Label: "本机文件", URL: "file:///C:/Windows/win.ini"},
			{Label: "签名流", URL: "https://f.video.weibocdn.com/a.mp4?Expires=1800000000&ssig=syn_secret&KID=unistore"},
			{Label: "图床直链", URL: "https://wx1.sinaimg.cn/large/pic.jpg"},
		},
		Origin: &ItemOrigin{
			Site:   "weibo",
			ID:     "5000000000000002",
			Author: ItemAuthor{Site: "weibo", UID: "500000002", Name: "原博主"},
			Text:   "原博参考 规范文档",
			Links: []ItemLink{
				{Label: "规范文档", URL: "https://example.org/rfc"},
				{Label: "带凭证", URL: "https://user:pass@example.org/private"},
			},
		},
		Thread: []ItemThreadPart{
			{
				ID:   "5000000000000003",
				Text: "续帖链接 补充说明",
				Links: []ItemLink{
					{Label: "补充说明", URL: "https://example.net/notes"},
					{Label: "X视频CDN", URL: "https://video.twimg.com/ext_tw_video/1/pu/vid/a.mp4"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("SaveItem: %v", err)
	}

	if saved.Version != 1 {
		t.Fatalf("saved.Version = %d, want 1 (schema version must stay backward-compatible)", saved.Version)
	}
	if len(saved.Links) != 2 || saved.Links[0].Label != "网页链接" || saved.Links[0].URL != "https://example.com/docs/spec" {
		t.Fatalf("unexpected sanitized saved.Links: %+v", saved.Links)
	}
	if saved.Origin == nil || len(saved.Origin.Links) != 1 || saved.Origin.Links[0].URL != "https://example.org/rfc" {
		t.Fatalf("unexpected sanitized saved.Origin.Links: %+v", saved.Origin)
	}
	if len(saved.Thread) != 1 || len(saved.Thread[0].Links) != 1 || saved.Thread[0].Links[0].URL != "https://example.net/notes" {
		t.Fatalf("unexpected sanitized saved.Thread[0].Links: %+v", saved.Thread)
	}

	loaded, ok, err := store.GetItem(saved.ItemKey)
	if err != nil || !ok {
		t.Fatalf("GetItem(%q) = (%v, %v)", saved.ItemKey, ok, err)
	}
	if len(loaded.Links) != 2 || loaded.Origin == nil || len(loaded.Origin.Links) != 1 || len(loaded.Thread[0].Links) != 1 {
		t.Fatalf("round-trip lost links: outer=%+v origin=%+v thread=%+v", loaded.Links, loaded.Origin, loaded.Thread)
	}

	manifestBytes, err := PortableManifest(loaded)
	if err != nil {
		t.Fatalf("PortableManifest: %v", err)
	}
	rawJSON := string(manifestBytes)
	for _, bad := range []string{"javascript:", "file:", "ssig=", "weibocdn.com", "sinaimg.cn", "video.twimg.com", "user:pass"} {
		if strings.Contains(rawJSON, bad) {
			t.Fatalf("manifest leaked forbidden URL/token %q:\n%s", bad, rawJSON)
		}
	}

	// Verify MergeItemMetadata supplements missing links onto an older local manifest without links.
	var olderLocal ItemMetadata
	if err := json.Unmarshal(manifestBytes, &olderLocal); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	olderLocal.Links = nil
	if olderLocal.Origin != nil {
		olderLocal.Origin.Links = nil
	}
	merged, _ := MergeItemMetadata(olderLocal, loaded, MergeUnion, nil)
	if len(merged.Links) != 2 || merged.Origin == nil || len(merged.Origin.Links) != 1 {
		t.Fatalf("MergeItemMetadata failed to supplement links: outer=%+v origin=%+v", merged.Links, merged.Origin)
	}
}

func TestXItemMetadataLinksAndForbiddenHostsSanitization(t *testing.T) {
	tmp := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer db.Close()

	store, err := OpenWithDataDir(db, filepath.Join(tmp, "cache"), tmp)
	if err != nil {
		t.Fatalf("OpenWithDataDir: %v", err)
	}

	itemDir := filepath.Join(tmp, "cache", "x", "1000000000000000001", "posts", "1900000000000099101")
	saved, err := store.SaveItem(itemDir, ItemMetadata{
		Version: 1,
		ID:      "dynamic:1900000000000099101",
		Site:    "x",
		Kind:    "dynamic",
		Status:  "complete",
		Title:   "X 多层级链接清洗测试",
		Text:    "主帖链接 https://github.com/org/repo/releases/tag/v2.0.0#notes",
		PubTs:   1710010000,
		Author:  ItemAuthor{Site: "x", UID: "1000000000000000001", UniqueID: "synth_x", Name: "合成X号"},
		Links: []ItemLink{
			{Label: "github.com/org/repo/rel…", URL: "https://github.com/org/repo/releases/tag/v2.0.0#notes"},
			{Label: "私密媒体", URL: "https://ton.x.com/i/ton/data/1"},
			{Label: "图片直链", URL: "https://pbs.twimg.com/media/synth.jpg"},
			{Label: "直播流", URL: "https://prod-fastly.pscp.tv/stream.m3u8"},
			{Label: "含凭证", URL: "https://example.com/p?auth_token=SECRET&ct0=CSRF"},
		},
		Origin: &ItemOrigin{
			Site:   "x",
			ID:     "1900000000000099100",
			Author: ItemAuthor{Site: "x", UID: "1000000000000000002", UniqueID: "orig_x", Name: "原帖作者"},
			Text:   "外层引用 https://example.org/outer",
			Links:  []ItemLink{{Label: "example.org/outer", URL: "https://example.org/outer"}},
			Origin: &ItemOrigin{
				Site:   "x",
				ID:     "1900000000000099099",
				Author: ItemAuthor{Site: "x", UID: "1000000000000000003", UniqueID: "inner_x", Name: "内层作者"},
				Text:   "内层引用 https://example.net/inner",
				Links: []ItemLink{
					{Label: "example.net/inner", URL: "https://example.net/inner"},
					{Label: "脚本", URL: "javascript:alert(1)"},
				},
			},
		},
		Article: &ItemArticle{
			Title: "文章标题",
			Blocks: []ItemArticleBlock{{
				Type: "header-one",
				Text: "第一章 规格说明",
				Links: []ItemLink{
					{Label: "规格说明", URL: "https://docs.example.dev/spec"},
					{Label: "非法视频", URL: "https://video.twimg.com/ext_tw_video/1/a.mp4"},
				},
			}},
		},
	})
	if err != nil {
		t.Fatalf("SaveItem: %v", err)
	}
	if saved.Version != 1 {
		t.Fatalf("saved.Version = %d, want 1", saved.Version)
	}
	if len(saved.Links) != 1 || saved.Links[0].URL != "https://github.com/org/repo/releases/tag/v2.0.0" {
		t.Fatalf("unexpected saved.Links: %+v", saved.Links)
	}
	if saved.Origin == nil || saved.Origin.Origin == nil || len(saved.Origin.Origin.Links) != 1 || saved.Origin.Origin.Links[0].URL != "https://example.net/inner" {
		t.Fatalf("unexpected nested Origin.Origin.Links: %+v", saved.Origin)
	}
	if saved.Article == nil || len(saved.Article.Blocks) != 1 || len(saved.Article.Blocks[0].Links) != 1 || saved.Article.Blocks[0].Links[0].URL != "https://docs.example.dev/spec" {
		t.Fatalf("unexpected Article.Blocks[0].Links: %+v", saved.Article)
	}
}
