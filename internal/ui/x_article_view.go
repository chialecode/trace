package ui

import (
	"fmt"
	"strings"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
	"github.com/egoist/mygo/ui"
)

// renderXArticlePreviewCard renders a compact, clickable X Article card inside
// a PostCard (X user "文章" tab, X List timeline, or Home feed). Offline cards
// strictly never request remote bitmaps.
func (a *App) renderXArticlePreviewCard(c *ui.Context, post PostCardData) {
	var (
		title      string
		excerpt    string
		coverURL   string
		coverRel   string
		inlineCnt  int
		isOffline  = post.Offline || post.LocalManage
		hasArticle bool
	)

	if post.XArticle != nil && post.XArticle.HasFullContent() {
		hasArticle = true
		title = strings.TrimSpace(post.XArticle.Title)
		excerpt = firstNonEmptyStrUI(strings.TrimSpace(post.XArticle.PreviewText), strings.TrimSpace(post.XArticle.PlainText))
		if post.XArticle.Cover != nil && !isOffline {
			coverURL = firstNonEmptyStrUI(post.XArticle.Cover.PublicPreviewURL, post.XArticle.Cover.ImageURL)
		}
		for _, blk := range post.XArticle.Blocks {
			if blk.Image != nil {
				inlineCnt++
			}
		}
		if post.LibItem != nil && post.LibItem.Article != nil && post.LibItem.Article.CoverRel != "" {
			coverRel = post.LibItem.Article.CoverRel
		}
	} else if post.LibArticle != nil {
		hasArticle = true
		title = strings.TrimSpace(post.LibArticle.Title)
		excerpt = strings.TrimSpace(post.LibArticle.PlainText)
		coverRel = strings.TrimSpace(post.LibArticle.CoverRel)
		for _, blk := range post.LibArticle.Blocks {
			if blk.MediaRel != "" {
				inlineCnt++
			}
		}
	}
	if !hasArticle {
		return
	}
	if title == "" {
		title = firstNonEmptyStrUI(strings.TrimSpace(post.Title), "X 长文章")
	}

	var coverBmp *ui.Bitmap
	if coverRel != "" && post.ItemKey != "" {
		coverBmp = a.lookupLocalBitmapEdge(post.ItemKey, coverRel, thumbMaxEdge)
	}
	if coverBmp == nil && !isOffline && coverURL != "" {
		coverBmp = a.lookupRemoteBitmap(coverURL)
	}

	card := ui.ButtonBase(c).
		Label("X文章卡片："+firstNonEmptyStrUI(post.RawID, post.Key)).
		FillWidth().
		Radius(12).
		Border(1, ColorBorder).
		Background(ColorBgSecondary).
		Clip()
	if card.Hovered() {
		card.Background(ColorHover)
	}
	card.Children(func() {
		ui.Column(c).FillWidth().Gap(0).ClipX().Children(func() {
			if coverBmp != nil || coverRel != "" || (!isOffline && coverURL != "") {
				ui.Box(c).FillWidth().Height(180).Background(ColorBgPanel).Clip().Children(func() {
					if coverBmp != nil {
						ui.Image(c, coverBmp).Absolute().Left(0).Top(0).Right(0).Bottom(0).Fit(ui.Cover)
					} else {
						ui.Row(c).Fill().Justify(ui.Center).AlignItems(ui.Center).Children(func() {
							DrawIcon(c, "image", 24, ColorTextMuted, 1.6)
						})
					}
				})
			}
			ui.Column(c).FillWidth().Padding(12, 14).Gap(6).ClipX().Children(func() {
				ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
					ui.Box(c).Padding(1, 6).Radius(4).Background(ColorAccentBg).Shrink(0).Children(func() {
						ui.Text(c, "X 文章").FontSize(11).FontWeight(600).TextColor(ColorAccent).SingleLine()
					})
					if inlineCnt > 0 {
						ui.Text(c, fmt.Sprintf("含 %d 张内嵌图", inlineCnt)).FontSize(11).TextColor(ColorTextMuted).SingleLine()
					}
				})
				ui.Text(c, title).FontSize(15).FontWeight(700).LineHeight(LineHeightHeading).TextColor(ColorTextMain).MaxLines(2)
				if excerpt != "" && excerpt != title {
					ui.Text(c, clampRunes(excerpt, 180)).FontSize(13).LineHeight(LineHeightCompact).TextColor(ColorTextMuted).MaxLines(3)
				}
			})
		})
	})
	if card.Clicked() {
		if post.OnOpenPost != nil && !isOffline {
			post.OnOpenPost()
		} else if post.ItemKey != "" && post.LibItem != nil {
			a.OpenLibraryItemDetail(*post.LibItem)
		}
	}
}

type articleRenderBlock struct {
	Type     string
	Text     string
	Links    []service.TextLinkDTO
	ImageURL string
	MediaRel string
	Width    int
	Height   int
	TweetID  string
}

type articleRenderDoc struct {
	ItemKey      string
	Offline      bool
	Title        string
	PlainText    string
	CoverURL     string
	CoverRel     string
	CoverWidth   int
	CoverHeight  int
	Blocks       []articleRenderBlock
	ContainerLbl string
}

// renderXOnlineArticle renders the full rich-text body, cover image, inline
// images, safe external links, and embedded tweet citations of an online X Article.
func (a *App) renderXOnlineArticle(c *ui.Context, post *service.XPostDTO, itemKey string, cachedItem *library.ItemMetadata) {
	if post == nil || post.Article == nil || !post.Article.HasFullContent() {
		return
	}
	art := post.Article
	doc := articleRenderDoc{
		ItemKey:      itemKey,
		Offline:      false,
		Title:        strings.TrimSpace(art.Title),
		PlainText:    strings.TrimSpace(art.PlainText),
		ContainerLbl: "X文章全文",
	}
	if art.Cover != nil {
		doc.CoverURL = firstNonEmptyStrUI(art.Cover.PublicPreviewURL, art.Cover.ImageURL)
		doc.CoverWidth = art.Cover.Width
		doc.CoverHeight = art.Cover.Height
	}
	if cachedItem != nil && cachedItem.Article != nil && cachedItem.Article.CoverRel != "" {
		doc.CoverRel = cachedItem.Article.CoverRel
	}
	if len(art.Blocks) > 0 {
		doc.Blocks = make([]articleRenderBlock, 0, len(art.Blocks))
		for i, blk := range art.Blocks {
			rb := articleRenderBlock{
				Type:    blk.Type,
				Text:    blk.Text,
				Links:   blk.Links,
				TweetID: strings.TrimSpace(blk.TweetID),
			}
			if blk.Image != nil {
				rb.ImageURL = firstNonEmptyStrUI(blk.Image.PublicPreviewURL, blk.Image.ImageURL)
				rb.Width = blk.Image.Width
				rb.Height = blk.Image.Height
			}
			if cachedItem != nil && cachedItem.Article != nil && i < len(cachedItem.Article.Blocks) {
				rb.MediaRel = strings.TrimSpace(cachedItem.Article.Blocks[i].MediaRel)
			}
			doc.Blocks = append(doc.Blocks, rb)
		}
	}
	a.renderXArticleDocument(c, doc)
}

// renderXOfflineArticle renders a cached X Article on /library/item with
// strictly zero network requests (only local bitmaps registered in the item).
func (a *App) renderXOfflineArticle(c *ui.Context, item library.ItemMetadata) {
	if item.Article == nil {
		return
	}
	art := item.Article
	mediaDims := make(map[string][2]int, len(item.Media))
	for _, mf := range item.Media {
		if mf.Path != "" && mf.Width > 0 && mf.Height > 0 {
			mediaDims[mf.Path] = [2]int{mf.Width, mf.Height}
		}
	}
	doc := articleRenderDoc{
		ItemKey:      item.ItemKey,
		Offline:      true,
		Title:        firstNonEmptyStrUI(strings.TrimSpace(art.Title), strings.TrimSpace(item.Title)),
		PlainText:    firstNonEmptyStrUI(strings.TrimSpace(art.PlainText), strings.TrimSpace(item.Text)),
		CoverRel:     strings.TrimSpace(art.CoverRel),
		ContainerLbl: "X离线文章全文",
	}
	if d, ok := mediaDims[doc.CoverRel]; ok {
		doc.CoverWidth, doc.CoverHeight = d[0], d[1]
	}
	if len(art.Blocks) > 0 {
		doc.Blocks = make([]articleRenderBlock, 0, len(art.Blocks))
		for _, blk := range art.Blocks {
			rb := articleRenderBlock{
				Type:     blk.Type,
				Text:     blk.Text,
				Links:    service.MapItemLinksToDTO(blk.Links),
				MediaRel: strings.TrimSpace(blk.MediaRel),
				TweetID:  strings.TrimSpace(blk.TweetID),
			}
			if d, ok := mediaDims[rb.MediaRel]; ok {
				rb.Width, rb.Height = d[0], d[1]
			}
			doc.Blocks = append(doc.Blocks, rb)
		}
	}
	a.renderXArticleDocument(c, doc)
}

func (a *App) renderXArticleDocument(c *ui.Context, doc articleRenderDoc) {
	var galleryURLs []string
	var galleryRels []string

	hasCover := doc.CoverRel != "" || (!doc.Offline && doc.CoverURL != "")
	coverGalleryIdx := -1
	if hasCover {
		coverGalleryIdx = len(galleryRels)
		if doc.Offline {
			galleryURLs = append(galleryURLs, "")
		} else {
			galleryURLs = append(galleryURLs, doc.CoverURL)
		}
		galleryRels = append(galleryRels, doc.CoverRel)
	}

	blockGalleryIdx := make(map[int]int, len(doc.Blocks))
	for i, blk := range doc.Blocks {
		if blk.MediaRel != "" || (!doc.Offline && blk.ImageURL != "") {
			blockGalleryIdx[i] = len(galleryRels)
			if doc.Offline {
				galleryURLs = append(galleryURLs, "")
			} else {
				galleryURLs = append(galleryURLs, blk.ImageURL)
			}
			galleryRels = append(galleryRels, blk.MediaRel)
		}
	}

	lookupArticleBmp := func(rel, rawURL string) *ui.Bitmap {
		if rel != "" && doc.ItemKey != "" {
			if bmp := a.lookupLocalBitmapEdge(doc.ItemKey, rel, thumbMaxEdge); bmp != nil {
				return bmp
			}
		}
		if doc.Offline || rawURL == "" {
			return nil
		}
		return a.lookupRemoteBitmap(rawURL)
	}

	articleImageAspect := func(w, h int, bmp *ui.Bitmap) float32 {
		fw, fh := float32(w), float32(h)
		if (fw <= 0 || fh <= 0) && bmp != nil {
			bw, bh := bmp.Size()
			fw, fh = float32(bw), float32(bh)
		}
		if fw <= 0 || fh <= 0 {
			return 16.0 / 9.0
		}
		return min(max(fw/fh, 0.65), 2.4)
	}

	ui.Column(c).Label(doc.ContainerLbl).FillWidth().Gap(12).ClipX().Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
			ui.Box(c).Padding(2, 8).Radius(4).Background(ColorAccentBg).Children(func() {
				ui.Text(c, "X 文章").FontSize(11).FontWeight(600).TextColor(ColorAccent).SingleLine()
			})
		})

		if doc.Title != "" {
			ui.Box(c).Label("X文章标题").FillWidth().ClipX().Children(func() {
				ui.Text(c, doc.Title).
					FontSize(20).
					FontWeight(700).
					LineHeight(LineHeightHeading).
					TextColor(ColorTextMain)
			})
		}

		if hasCover {
			coverBmp := lookupArticleBmp(doc.CoverRel, doc.CoverURL)
			ratio := articleImageAspect(doc.CoverWidth, doc.CoverHeight, coverBmp)
			coverH := min(float32(180), max(float32(132), 300/ratio))
			coverBtn := ui.ButtonBase(c).
				Label("查看文章封面").
				FillWidth().
				Height(coverH).
				Radius(12).
				Border(1, ColorBorder).
				Background(ColorBgSecondary).
				Clip()
			coverBtn.Children(func() {
				if coverBmp != nil {
					ui.Image(c, coverBmp).Absolute().Left(0).Top(0).Right(0).Bottom(0).Fit(ui.Cover)
				} else {
					ui.Row(c).Fill().Justify(ui.Center).AlignItems(ui.Center).Children(func() {
						DrawIcon(c, "image", 24, ColorTextMuted, 1.6)
					})
				}
			})
			if coverBtn.Clicked() && coverGalleryIdx >= 0 {
				a.openPostImageViewer(doc.ItemKey, "x", galleryURLs, galleryRels, nil, coverGalleryIdx)
			}
		}

		if len(doc.Blocks) == 0 {
			if doc.PlainText != "" && doc.PlainText != doc.Title {
				a.renderRichText(c, doc.PlainText, nil, richTextOptions{
					Label:      "X文章正文",
					FontSize:   15,
					LineHeight: LineHeightBody,
					TextColor:  ColorTextMain,
				})
			}
			return
		}

		orderedIdx := 0
		inlineImgSeq := 0
		for bi, blk := range doc.Blocks {
			if blk.Type != "ordered-list-item" {
				orderedIdx = 0
			}
			switch blk.Type {
			case "header-one":
				if txt := strings.TrimSpace(blk.Text); txt != "" {
					ui.Box(c).FillWidth().Padding(6, 0, 0, 0).ClipX().Children(func() {
						if _, hasRich := parseRichTextParagraphs(txt, blk.Links, false); hasRich {
							a.renderRichText(c, txt, blk.Links, richTextOptions{
								FontSize:   18,
								LineHeight: LineHeightHeading,
								TextColor:  ColorTextMain,
							})
						} else {
							ui.Text(c, txt).FontSize(18).FontWeight(700).LineHeight(LineHeightHeading).TextColor(ColorTextMain)
						}
					})
				}

			case "header-two":
				if txt := strings.TrimSpace(blk.Text); txt != "" {
					ui.Box(c).FillWidth().Padding(4, 0, 0, 0).ClipX().Children(func() {
						if _, hasRich := parseRichTextParagraphs(txt, blk.Links, false); hasRich {
							a.renderRichText(c, txt, blk.Links, richTextOptions{
								FontSize:   16,
								LineHeight: LineHeightHeading,
								TextColor:  ColorTextMain,
							})
						} else {
							ui.Text(c, txt).FontSize(16).FontWeight(600).LineHeight(LineHeightHeading).TextColor(ColorTextMain)
						}
					})
				}

			case "blockquote":
				if txt := strings.TrimSpace(blk.Text); txt != "" {
					ui.Row(c).
						FillWidth().
						Padding(8, 12).
						Radius(6).
						Background(ColorBgSecondary).
						BorderWidth(0, 0, 0, 3).
						BorderColor(ColorAccent).
						ClipX().
						Children(func() {
							ui.Column(c).Grow(1).Basis(0).ClipX().Children(func() {
								a.renderRichText(c, txt, blk.Links, richTextOptions{
									FontSize:   14,
									LineHeight: LineHeightBody,
									TextColor:  ColorTextMuted,
								})
							})
						})
				}

			case "ordered-list-item":
				orderedIdx++
				if txt := strings.TrimSpace(blk.Text); txt != "" {
					prefix := fmt.Sprintf("%d.", orderedIdx)
					ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Start).Padding(0, 0, 0, 6).ClipX().Children(func() {
						ui.Text(c, prefix).FontSize(15).FontWeight(600).LineHeight(LineHeightBody).TextColor(ColorTextMuted).SingleLine().Shrink(0)
						ui.Column(c).Grow(1).Basis(0).ClipX().Children(func() {
							a.renderRichText(c, txt, blk.Links, richTextOptions{
								FontSize:   15,
								LineHeight: LineHeightBody,
								TextColor:  ColorTextMain,
							})
						})
					})
				}

			case "unordered-list-item":
				if txt := strings.TrimSpace(blk.Text); txt != "" {
					ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Start).Padding(0, 0, 0, 6).ClipX().Children(func() {
						ui.Text(c, "•").FontSize(15).FontWeight(700).LineHeight(LineHeightBody).TextColor(ColorTextMuted).SingleLine().Shrink(0)
						ui.Column(c).Grow(1).Basis(0).ClipX().Children(func() {
							a.renderRichText(c, txt, blk.Links, richTextOptions{
								FontSize:   15,
								LineHeight: LineHeightBody,
								TextColor:  ColorTextMain,
							})
						})
					})
				}

			case "atomic":
				if gIdx, hasImg := blockGalleryIdx[bi]; hasImg {
					inlineImgSeq++
					imgBmp := lookupArticleBmp(blk.MediaRel, blk.ImageURL)
					ratio := articleImageAspect(blk.Width, blk.Height, imgBmp)
					imgH := min(float32(144), max(float32(112), 220/ratio))
					idxCopy := gIdx
					imgBtn := ui.ButtonBase(c).
						Label(fmt.Sprintf("查看文章图片 %d", inlineImgSeq)).
						FillWidth().
						Height(imgH).
						Radius(10).
						Border(1, ColorBorder).
						Background(ColorBgSecondary).
						Clip()
					imgBtn.Children(func() {
						if imgBmp != nil {
							ui.Image(c, imgBmp).Absolute().Left(0).Top(0).Right(0).Bottom(0).Fit(ui.Cover)
						} else {
							ui.Row(c).Fill().Justify(ui.Center).AlignItems(ui.Center).Children(func() {
								DrawIcon(c, "image", 22, ColorTextMuted, 1.6)
							})
						}
					})
					if imgBtn.Clicked() {
						a.openPostImageViewer(doc.ItemKey, "x", galleryURLs, galleryRels, nil, idxCopy)
					}
				}
				if blk.TweetID != "" {
					tid := blk.TweetID
					ui.Row(c).
						Label("X文章引用帖子："+tid).
						FillWidth().
						Padding(10, 12).
						Radius(10).
						Background(ColorBgSecondary).
						Border(1, ColorBorder).
						AlignItems(ui.Center).
						Justify(ui.SpaceBetween).
						Gap(8).
						ClipX().
						Children(func() {
							ui.Row(c).AlignItems(ui.Center).Gap(8).Grow(1).Basis(0).ClipX().Children(func() {
								ui.Box(c).Padding(1, 6).Radius(4).Background(ColorBgMain).Border(1, ColorBorder).Shrink(0).Children(func() {
									ui.Text(c, "引用帖子").FontSize(11).FontWeight(600).TextColor(ColorTextMuted).SingleLine()
								})
								ui.Text(c, "X 帖子 ID "+tid).FontSize(13).TextColor(ColorTextMain).SingleLine().Shrink(1)
							})
							if !doc.Offline {
								if OutlinePillButton(c, "查看帖子", "打开文章引用帖子："+tid).Clicked() {
									a.OpenXPost(tid, "", "", "", "", "https://x.com/i/web/status/"+tid)
								}
							}
						})
				}

			default:
				if txt := strings.TrimSpace(blk.Text); txt != "" {
					a.renderRichText(c, txt, blk.Links, richTextOptions{
						FontSize:   15,
						LineHeight: LineHeightBody,
						TextColor:  ColorTextMain,
					})
				}
			}
		}
	})
}
