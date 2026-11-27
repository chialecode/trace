package library

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chialecode/trace/internal/storage"
)

func TestItemArticleSanitizeMergePortableFilesKeepAndDelete(t *testing.T) {
	dataDir := t.TempDir()
	cacheDir := filepath.Join(dataDir, "cache")
	db, _, _, err := storage.OpenSQLite(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store, err := OpenWithDataDir(db, cacheDir, dataDir)
	if err != nil {
		t.Fatal(err)
	}

	// 1. SanitizeItemMetadata / SanitizeItemArticle scrubs CDN URLs, query strings, traversal, javascript: links, and X secrets.
	dirty := ItemMetadata{
		Version:     1,
		ID:          "1000000000000000501",
		Site:        "x",
		Kind:        "dynamic",
		DynamicType: "article",
		TypeLabel:   "文章",
		Status:      "complete",
		Title:       "文章标题",
		Text:        "纯文本回退正文",
		Author:      ItemAuthor{Site: "x", UID: "1000000000000000001", UniqueID: "synth_author", Name: "合成作者"},
		Article: &ItemArticle{
			Title:     "X Articles 离线清洗测试",
			CoverRel:  "https://pbs.twimg.com/media/ForbiddenRemote.jpg?token=SECRET",
			PlainText: "正文含 auth_token=LEAKED_SECRET_1234567890123456 必须脱敏",
			Blocks: []ItemArticleBlock{
				{
					Type: "header-one",
					Text: "第一章",
				},
				{
					Type: "unstyled",
					Text: "外链过滤测试",
					Links: []ItemLink{
						{Label: "安全文档", URL: "https://example.com/guide"},
						{Label: "恶意脚本", URL: "javascript:alert(1)"},
						{Label: "签名CDN", URL: "https://pbs.twimg.com/media/A.jpg?token=SIG01"},
					},
				},
				{
					Type:     "atomic",
					MediaRel: "../escape/bad.png",
				},
				{
					Type:     "atomic",
					MediaRel: "images\\01.png",
				},
				{
					Type:     "atomic",
					MediaRel: "images/02.png?name=orig",
				},
				{
					Type:    "atomic",
					TweetID: "1791257321273778888",
				},
				{
					Type:    "atomic",
					TweetID: "not-numeric-id",
				},
			},
		},
	}
	SanitizeItemMetadata(&dirty)
	if dirty.Version != 1 {
		t.Fatalf("Version = %d, want 1", dirty.Version)
	}
	if dirty.Article == nil {
		t.Fatal("Article became nil after sanitization")
	}
	if dirty.Article.CoverRel != "" {
		t.Fatalf("remote URL CoverRel must be scrubbed, got %q", dirty.Article.CoverRel)
	}
	if strings.Contains(dirty.Article.PlainText, "LEAKED_SECRET") {
		t.Fatalf("secret marker leaked in PlainText: %q", dirty.Article.PlainText)
	}
	// Links: only safe https://example.com/guide survives.
	if len(dirty.Article.Blocks) < 2 || len(dirty.Article.Blocks[1].Links) != 1 || dirty.Article.Blocks[1].Links[0].URL != "https://example.com/guide" {
		t.Fatalf("unexpected sanitized links: %+v", dirty.Article.Blocks)
	}
	// Traversal, backslash, query-bearing MediaRel and non-numeric TweetID must be dropped.
	for _, b := range dirty.Article.Blocks {
		if strings.Contains(b.MediaRel, "..") || strings.Contains(b.MediaRel, "\\") || strings.Contains(b.MediaRel, "?") || strings.Contains(b.MediaRel, "http") {
			t.Fatalf("unsafe MediaRel survived sanitization: %q", b.MediaRel)
		}
		if b.TweetID == "not-numeric-id" {
			t.Fatalf("non-numeric TweetID survived sanitization")
		}
	}

	// 2. SaveItem, RegisteredFiles, PortableManifest, MergeItemMetadata, KeepListedFiles, and Safe Delete.
	postDir, err := XPostDir(cacheDir, "1000000000000000001", "1000000000000000502", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(postDir, "images"), 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(postDir, "cover.jpg"), []byte("synth-cover"), 0o600)
	_ = os.WriteFile(filepath.Join(postDir, "images", "01.png"), []byte("synth-inline-1"), 0o600)
	_ = os.WriteFile(filepath.Join(postDir, "user_note.txt"), []byte("preserve-me"), 0o600)

	meta := ItemMetadata{
		Version:     1,
		ID:          "1000000000000000502",
		Site:        "x",
		Kind:        "dynamic",
		DynamicType: "article",
		TypeLabel:   "文章",
		Status:      "complete",
		Title:       "完整 X 文章归档测试",
		Text:        "文章纯文本回退，供全文搜索使用。",
		PubTs:       1763800500,
		Author:      ItemAuthor{Site: "x", UID: "1000000000000000001", UniqueID: "synth_author", Name: "合成作者"},
		Media: []MediaFile{
			{Role: "cover", Path: "cover.jpg", Width: 1200, Height: 675},
			{Role: "image", Path: "images/01.png", Width: 800, Height: 600},
		},
		Article: &ItemArticle{
			Title:     "完整 X 文章归档测试",
			CoverRel:  "cover.jpg",
			PlainText: "文章纯文本回退，供全文搜索使用。",
			Blocks: []ItemArticleBlock{
				{Type: "header-one", Text: "概览"},
				{Type: "unstyled", Text: "段落正文", Links: []ItemLink{{Label: "参考", URL: "https://example.com/ref"}}},
				{Type: "atomic", MediaRel: "images/01.png"},
				{Type: "atomic", TweetID: "1791257321273778888"},
			},
		},
	}

	saved, err := store.SaveItem(postDir, meta)
	if err != nil {
		t.Fatalf("SaveItem article failed: %v", err)
	}
	if saved.Version != 1 || saved.Article == nil || saved.Article.CoverRel != "cover.jpg" || len(saved.Article.Blocks) != 4 || saved.TypeLabel != "文章" {
		t.Fatalf("unexpected saved article metadata: %+v", saved)
	}

	// visitManifestFiles must include cover.jpg and images/01.png even if Media were empty.
	articleOnly := ItemMetadata{Article: cloneItemArticle(saved.Article)}
	regFiles := map[string]bool{}
	visitManifestFiles(articleOnly, func(f ManifestFile) { regFiles[f.Path] = true })
	if !regFiles["cover.jpg"] || !regFiles["images/01.png"] {
		t.Fatalf("visitManifestFiles missing article assets: %+v", regFiles)
	}

	// PortableManifest preserves Article cleanly without local absolute paths or CDN URLs.
	rawPort, err := PortableManifest(saved)
	if err != nil {
		t.Fatalf("PortableManifest failed: %v", err)
	}
	var port ItemMetadata
	if err := json.Unmarshal(rawPort, &port); err != nil {
		t.Fatalf("unmarshal PortableManifest: %v", err)
	}
	if port.Article == nil || port.Article.CoverRel != "cover.jpg" || len(port.Article.Blocks) != 4 || port.ItemDir != "" {
		t.Fatalf("PortableManifest article mismatch: %+v", port)
	}

	// MergeItemMetadata preserves Article when incoming lacks Article, and merges when incoming provides Article.
	dstNoArticle := saved
	dstNoArticle.Article = nil
	merged, _ := MergeItemMetadata(dstNoArticle, saved, MergeUnion, nil)
	if merged.Article == nil || merged.Article.CoverRel != "cover.jpg" || len(merged.Article.Blocks) != 4 {
		t.Fatalf("MergeItemMetadata did not adopt incoming Article: %+v", merged.Article)
	}
	incomingEmpty := saved
	incomingEmpty.Article = nil
	mergedKeep, _ := MergeItemMetadata(saved, incomingEmpty, MergeUnion, nil)
	if mergedKeep.Article == nil || mergedKeep.Article.CoverRel != "cover.jpg" {
		t.Fatalf("MergeItemMetadata dropped existing Article: %+v", mergedKeep.Article)
	}

	// KeepListedFiles strips missing CoverRel and Block.MediaRel when excluded from keep set.
	kept, dropped := KeepListedFiles(saved, func(rel string) bool { return rel == "images/01.png" })
	if kept.Article == nil || kept.Article.CoverRel != "" || kept.Article.Blocks[2].MediaRel != "images/01.png" || len(dropped) == 0 {
		t.Fatalf("KeepListedFiles article mismatch: article=%+v dropped=%v", kept.Article, dropped)
	}

	// PlanDelete + Delete removes cover.jpg & images/01.png while preserving unregistered user_note.txt!
	plan, err := store.PlanDelete(saved.ItemKey)
	if err != nil {
		t.Fatalf("PlanDelete failed: %v", err)
	}
	if len(plan.PreservedUserFiles) != 1 {
		t.Fatalf("PreservedUserFiles = %v, want 1 (user_note.txt)", plan.PreservedUserFiles)
	}
	if _, err := store.Delete(saved.ItemKey); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(postDir, "cover.jpg")); !os.IsNotExist(err) {
		t.Fatalf("cover.jpg should be deleted")
	}
	if _, err := os.Stat(filepath.Join(postDir, "images", "01.png")); !os.IsNotExist(err) {
		t.Fatalf("images/01.png should be deleted")
	}
	if _, err := os.Stat(filepath.Join(postDir, "user_note.txt")); err != nil {
		t.Fatalf("user_note.txt must be preserved: %v", err)
	}
}
