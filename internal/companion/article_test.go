package companion

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/companionapi"
	"github.com/chialecode/trace/internal/library"
)

func TestLibraryItemDetailIncludesSanitizedXArticleAndFallbackCover(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir, "node-x-article")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := store.AddDevice("Phone", "Pixel", "android", companionapi.ChannelDev, "192.168.1.50", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	backend := newTestCompanionBackend(t, true)
	defer backend.Close()

	postDir, err := library.XPostDir(backend.LibraryStore().CacheDir(), "1000000000000000001", "1000000000000000701", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(postDir, "images"), 0o700); err != nil {
		t.Fatal(err)
	}
	imgBytes := synthJPEG(320, 200)
	_ = os.WriteFile(filepath.Join(postDir, "cover.jpg"), imgBytes, 0o600)
	_ = os.WriteFile(filepath.Join(postDir, "images", "01.jpg"), imgBytes, 0o600)

	saved, err := backend.LibraryStore().SaveItem(postDir, library.ItemMetadata{
		Version:      1,
		ID:           "1000000000000000701",
		Site:         "x",
		Kind:         "dynamic",
		DynamicType:  "article",
		TypeLabel:    "文章",
		Status:       "complete",
		DirectCached: true,
		Title:        "移动端离线文章 DTO 测试",
		Text:         "纯文本回退内容",
		PubTs:        1732700000,
		Author:       library.ItemAuthor{Site: "x", UID: "1000000000000000001", UniqueID: "synth_x_writer", Name: "合成作者"},
		Media: []library.MediaFile{
			{Role: "cover", Path: "cover.jpg", Bytes: int64(len(imgBytes)), Width: 320, Height: 200},
			{Role: "image", Path: "images/01.jpg", Bytes: int64(len(imgBytes)), Width: 320, Height: 200},
		},
		Article: &library.ItemArticle{
			Title:     "移动端离线文章 DTO 测试",
			CoverRel:  "cover.jpg",
			PlainText: "纯文本回退内容",
			Blocks: []library.ItemArticleBlock{
				{Type: "header-one", Text: "第一节"},
				{Type: "unstyled", Text: "正文段落", Links: []library.ItemLink{{Label: "文档", URL: "https://example.com/doc"}}},
				{Type: "atomic", MediaRel: "images/01.jpg"},
				{Type: "atomic", TweetID: "1791257321273778888"},
			},
		},
	})
	if err != nil {
		t.Fatalf("SaveItem: %v", err)
	}

	srv := NewServer(ServerOptions{
		Backend: backend,
		Store:   store,
		Pairing: newPairingManager(nil, nil),
		Hub:     newEventHub(nil, nil),
		Channel: companionapi.ChannelDev,
		Version: "0.1.0-dev",
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/library/items/"+url.PathEscape(saved.ItemKey), nil)
	req.RemoteAddr = "192.168.1.50:45123"
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(companionapi.HeaderProtocol, companionapi.ProtocolVersion)
	req.Header.Set(companionapi.HeaderChannel, companionapi.ChannelDev)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET library item status = %d, body = %s", rec.Code, rec.Body.String())
	}
	rawBody := rec.Body.String()
	if strings.Contains(rawBody, "pbs.twimg.com") || strings.Contains(rawBody, postDir) {
		t.Fatalf("companion response leaked remote CDN URL or local path: %s", rawBody)
	}
	var detail companionapi.LibraryItemDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.Article == nil || detail.Article.Title != "移动端离线文章 DTO 测试" || detail.Article.CoverRel != "cover.jpg" {
		t.Fatalf("unexpected detail.Article: %+v", detail.Article)
	}
	if len(detail.Article.Blocks) != 4 || detail.Article.Blocks[2].MediaRel != "images/01.jpg" || detail.Article.Blocks[3].TweetID != "1791257321273778888" {
		t.Fatalf("unexpected detail.Article.Blocks: %+v", detail.Article.Blocks)
	}
	if gotCover := findCoverRelPath(library.ItemMetadata{Article: &library.ItemArticle{CoverRel: "cover.jpg"}}); gotCover != "cover.jpg" {
		t.Fatalf("findCoverRelPath fallback to Article.CoverRel = %q, want cover.jpg", gotCover)
	}

	// Verify GET /api/v1/feed also populates FeedItem.Article with sanitized local paths and zero remote URLs.
	feedReq := httptest.NewRequest(http.MethodGet, companionapi.PathFeed+"?site=x", nil)
	feedReq.RemoteAddr = "192.168.1.50:45124"
	feedReq.Header.Set("Authorization", "Bearer "+token)
	feedReq.Header.Set(companionapi.HeaderProtocol, companionapi.ProtocolVersion)
	feedReq.Header.Set(companionapi.HeaderChannel, companionapi.ChannelDev)
	feedRec := httptest.NewRecorder()
	srv.ServeHTTP(feedRec, feedReq)
	if feedRec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/feed status = %d, body = %s", feedRec.Code, feedRec.Body.String())
	}
	feedRaw := feedRec.Body.String()
	if strings.Contains(feedRaw, "pbs.twimg.com") || strings.Contains(feedRaw, postDir) {
		t.Fatalf("companion feed response leaked remote CDN URL or local path: %s", feedRaw)
	}
	var feedResp companionapi.FeedPageResponse
	if err := json.Unmarshal(feedRec.Body.Bytes(), &feedResp); err != nil {
		t.Fatalf("decode feed response: %v", err)
	}
	var feedArt *companionapi.FeedItem
	for i := range feedResp.Items {
		if feedResp.Items[i].ItemKey == saved.ItemKey {
			feedArt = &feedResp.Items[i]
			break
		}
	}
	if feedArt == nil {
		t.Fatalf("feed response missing saved article item %s: %+v", saved.ItemKey, feedResp.Items)
	}
	if feedArt.Article == nil || feedArt.Article.Title != "移动端离线文章 DTO 测试" || feedArt.Article.CoverRel != "cover.jpg" || len(feedArt.Article.Blocks) != 4 {
		t.Fatalf("unexpected FeedItem.Article: %+v", feedArt.Article)
	}
}
