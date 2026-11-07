package ui

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/egoist/mygo/ui"
)

// PostImage represents an image in a PostCard.
type PostImage struct {
	URL     string
	RelPath string
	Width   int
	Height  int
	Live    bool // a live photo (实况): the still of a short motion clip
}

// PostVideo represents a video card embedded in a PostCard.
type PostVideo struct {
	BVID       string
	Title      string
	Desc       string
	CoverURL   string
	RelCover   string
	Duration   string
	ItemKey    string
	Cached     bool
	Status     string
	QualityTag string
}

// PostCardData is the unified view-model for rendering social feed posts across
// /home, /user dynamics, /subscriptions updates, and /subscriptions cached.
type PostCardData struct {
	Key           string
	ItemKey       string
	Site          string
	Kind          string // "video", "dynamic", "live"
	RawID         string
	Mid           int64
	AuthorName    string
	AuthorFace    string
	PubTs         int64
	TypeLabel     string
	Title         string
	Text          string
	URL           string
	Pinned        bool
	Degraded      bool
	Cached        bool
	DotStatus     string
	DotLabel      string
	Images        []PostImage
	Video         *PostVideo
	Origin        *PostCardData
	DynItem       *bilibili.DynamicItem
	LibItem       *library.ItemMetadata
	InHomeFeed    bool
	OpenBtnLabel  string
	CacheBtnLabel string
}

// renderAvatar renders a circular avatar with deterministic color + initial fallback and an optional orange live ring.
func (a *App) renderAvatar(c *ui.Context, name string, mid int64, size float32, liveRing bool, label string, onClick func(), faces ...string) ui.Element {
	return a.renderAvatarWithBitmap(c, name, mid, size, liveRing, label, onClick, nil, faces...)
}

// renderAvatarWithBitmap renders a circular avatar using customBmp when non-nil, falling back to remote face or colored initial.
func (a *App) renderAvatarWithBitmap(c *ui.Context, name string, mid int64, size float32, liveRing bool, label string, onClick func(), customBmp *ui.Bitmap, faces ...string) ui.Element {
	if size <= 0 {
		size = 40
	}
	bg := AvatarColor(name, mid)
	glyph := FirstGlyph(name)
	fontSz := max(12, size*0.42)
	bmp := customBmp
	if bmp == nil {
		face := firstImageURL(faces)
		if face == "" {
			face = a.faceURL(mid)
		}
		bmp = a.lookupRemoteBitmap(face)
	}

	outerSize := size
	if liveRing {
		outerSize = size + 6
	}
	box := ui.ButtonBase(c).
		Size(outerSize, outerSize).
		Radius(outerSize / 2).
		Clip().
		Shrink(0).
		Justify(ui.Center).
		AlignItems(ui.Center)
	if label != "" {
		box.Label(label).Tooltip(label)
	} else if name != "" {
		tip := name
		if mid > 0 {
			tip = fmt.Sprintf("%s (UID %d)", name, mid)
		}
		box.Tooltip(tip)
	}
	box.Draw(func(p *ui.Painter, r ui.Rect) {
		if liveRing {
			p.Stroke(r, ColorLive, r.W/2, 2.0)
			inner := ui.Rect{X: r.X + 3, Y: r.Y + 3, W: r.W - 6, H: r.H - 6}
			p.Fill(inner, bg, inner.W/2)
		} else {
			p.Fill(r, bg, r.W/2)
		}
	})
	box.Children(func() {
		if bmp != nil {
			ui.Image(c, bmp).Size(size, size).Fit(ui.Cover).Radius(size / 2)
		} else {
			ui.Text(c, glyph).FontSize(fontSz).FontWeight(600).TextColor(ColorWhite).SingleLine()
		}
	})
	if onClick != nil && box.Clicked() {
		onClick()
	}
	return box
}

// renderMediaThumb renders a 16:9 or custom-sized rounded cover/thumbnail box with real local bitmap or stylized vector card.
func (a *App) renderMediaThumb(
	c *ui.Context,
	itemKey string,
	relPath string,
	title string,
	durationText string,
	dotStatus string,
	showPlayIcon bool,
	width float32,
	height float32,
	remoteURLs ...string,
) ui.Element {
	return a.renderMediaThumbWithBitmap(c, nil, itemKey, relPath, title, durationText, dotStatus, showPlayIcon, width, height, remoteURLs...)
}

// renderMediaThumbWithBitmap renders a rounded cover/thumbnail box using customBmp if provided, or local/remote bitmap, falling back to stylized placeholder.
func (a *App) renderMediaThumbWithBitmap(
	c *ui.Context,
	customBmp *ui.Bitmap,
	itemKey string,
	relPath string,
	title string,
	durationText string,
	dotStatus string,
	showPlayIcon bool,
	width float32,
	height float32,
	remoteURLs ...string,
) ui.Element {
	bmp := customBmp
	if bmp == nil {
		bmp = a.lookupLocalBitmap(itemKey, relPath)
	}
	if bmp == nil {
		bmp = a.lookupRemoteBitmap(firstImageURL(remoteURLs))
	}
	pal := AvatarColor(title+itemKey, 0)
	fromCol := pal.Mix(ColorTextMain, 0.35)
	toCol := pal.Mix(ColorInfoBlue, 0.45)

	container := ui.Box(c).
		Height(height).
		Shrink(0).
		Radius(12).
		Border(1, ColorBorder).
		Clip()
	if width > 0 {
		container.Width(width).Shrink(0)
	} else {
		container.FillWidth()
	}

	container.Draw(func(p *ui.Painter, r ui.Rect) {
		if bmp == nil {
			p.FillGradient(r, ui.LinearGradient{
				From:  fromCol,
				To:    toCol,
				Angle: 135,
			}, 12)
			// Subtle decorative grid lines and play/video frame icon
			cx, cy := r.X+r.W/2, r.Y+r.H/2
			frameW, frameH := min(r.W*0.32, 56), min(r.H*0.36, 38)
			frameRect := ui.Rect{X: cx - frameW/2, Y: cy - frameH/2, W: frameW, H: frameH}
			p.Stroke(frameRect, ui.RGBA(255, 255, 255, 0.28), 6, 1.5)
		}
	})

	container.Children(func() {
		if bmp != nil {
			ui.Image(c, bmp).Absolute().Left(0).Top(0).Right(0).Bottom(0).Fit(ui.Cover)
		} else if title != "" && (width == 0 || width >= 180) {
			ui.Column(c).Absolute().Left(12).Top(12).Right(40).Children(func() {
				ui.Text(c, title).FontSize(13).FontWeight(600).TextColor(ui.RGBA(255, 255, 255, 0.92)).MaxLines(2)
			})
		}

		if showPlayIcon {
			playSz := float32(42)
			if height < 100 {
				playSz = 28
			}
			ui.Row(c).Absolute().Left(0).Top(0).Right(0).Bottom(0).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
				ui.Box(c).Size(playSz, playSz).Radius(playSz / 2).Background(ui.RGBA(0, 0, 0, 0.58)).Center().Children(func() {
					DrawIcon(c, "play", playSz*0.48, ColorWhite, 2.0)
				})
			})
		}

		if dotStatus != "" && dotStatus != "none" {
			ui.Box(c).Absolute().Top(8).Right(8).Padding(2).Radius(999).Background(ui.RGBA(255, 255, 255, 0.9)).Children(func() {
				DrawStatusDot(c, dotStatus, "")
			})
		}

		if durationText != "" && durationText != "00:00" {
			ui.Box(c).Absolute().Bottom(8).Right(8).Padding(2, 7).Radius(5).Background(ui.RGBA(0, 0, 0, 0.74)).Children(func() {
				ui.Text(c, durationText).FontSize(11).FontWeight(600).TextColor(ColorWhite).SingleLine()
			})
		}
	})
	return container
}

// renderExpandableText renders post or video body text with 6-line / 280-char truncation and an "展开 / 收起" toggle.
func (a *App) renderExpandableText(c *ui.Context, key string, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if a.ExpandedPosts == nil {
		a.ExpandedPosts = make(map[string]bool)
	}
	lines := strings.Count(text, "\n") + 1
	needsClamp := lines > 6 || utf8.RuneCountInString(text) > 280
	expanded := a.ExpandedPosts[key]

	ui.Column(c).FillWidth().Gap(4).Children(func() {
		t := ui.Text(c, text).FontSize(15).TextColor(ColorTextMain)
		if needsClamp && !expanded {
			t.MaxLines(6)
		}
		if needsClamp {
			toggleText := "展开"
			if expanded {
				toggleText = "收起"
			}
			btn := ui.ButtonBase(c).Padding(2, 0).Children(func() {
				ui.Text(c, toggleText).FontSize(13).FontWeight(600).TextColor(ColorInfoBlue).SingleLine()
			})
			if btn.Clicked() {
				a.ExpandedPosts[key] = !expanded
			}
		}
	})
}

// renderPostImageGrid shows a post's images as the site does (DEC-040): one
// image at its own proportions, so nothing is cut off; two or more as square
// cells in two (2 or 4 images) or three columns, up to nine.
func (a *App) renderPostImageGrid(c *ui.Context, post PostCardData) {
	n := len(post.Images)
	if n == 0 {
		return
	}
	var urls []string
	var rels []string
	for _, img := range post.Images {
		urls = append(urls, img.URL)
		rels = append(rels, img.RelPath)
	}

	openAt := func(idx int) {
		if post.ItemKey != "" {
			a.OpenItemImageViewer(post.ItemKey, urls, rels, idx)
		} else {
			a.OpenImageViewer(urls, nil, idx)
		}
	}

	if n == 1 {
		img := post.Images[0]
		bmp := a.postImageBitmap(post, img)
		ratio := imageRatio(img, bmp)
		cell := ui.ButtonBase(c).Label("查看图片 1").Radius(8).Clip().Shrink(0)
		if ratio < 1 {
			// Portrait: a fixed height, as wide as the image is.
			cell.Size(singleImageHeight*ratio, singleImageHeight)
		} else {
			cell.FillWidth().MaxWidth(singleImageMaxWidth).AspectRatio(ratio)
		}
		cell.Children(func() { a.renderPostImageCell(c, bmp, img) })
		if cell.Clicked() {
			openAt(0)
		}
		return
	}

	cols, maxW := 3, float32(gridMaxWidth3)
	if n == 2 || n == 4 {
		cols, maxW = 2, gridMaxWidth2
	}
	shown := min(n, 9)
	extra := n - shown
	ui.Grid(c).Columns(cols).Gap(4).FillWidth().MaxWidth(maxW).Children(func() {
		for idx := range shown {
			imageIdx := idx
			img := post.Images[imageIdx]
			cell := ui.ButtonBase(c).Label(fmt.Sprintf("查看图片 %d", imageIdx+1)).AspectRatio(1).Radius(6).Clip()
			cell.Children(func() {
				a.renderPostImageCell(c, a.postImageBitmap(post, img), img)
				if extra > 0 && imageIdx == shown-1 {
					ui.Row(c).Absolute().Left(0).Top(0).Right(0).Bottom(0).Background(ui.RGBA(0, 0, 0, 0.55)).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
						ui.Text(c, fmt.Sprintf("+%d", extra)).FontSize(18).FontWeight(600).TextColor(ColorWhite).SingleLine()
					})
				}
			})
			if cell.Clicked() {
				openAt(imageIdx)
			}
		}
	})
}

// Sizes of post images, close to the site's own layout.
const (
	singleImageHeight   = 360 // a portrait image
	singleImageMaxWidth = 480 // a landscape image
	gridMaxWidth2       = 400
	gridMaxWidth3       = 480
)

// postImageBitmap is the cached file when there is one, else the site's image.
func (a *App) postImageBitmap(post PostCardData, img PostImage) *ui.Bitmap {
	if bmp := a.lookupLocalBitmap(post.ItemKey, img.RelPath); bmp != nil {
		return bmp
	}
	return a.lookupRemoteBitmap(img.URL)
}

// imageRatio is width / height from the recorded size or the loaded image,
// kept between a tall phone screenshot and a wide panorama.
func imageRatio(img PostImage, bmp *ui.Bitmap) float32 {
	w, h := float32(img.Width), float32(img.Height)
	if (w <= 0 || h <= 0) && bmp != nil {
		bw, bh := bmp.Size()
		w, h = float32(bw), float32(bh)
	}
	if w <= 0 || h <= 0 {
		return 4.0 / 3
	}
	return min(max(w/h, 0.5), 2.4)
}

// renderPostImageCell fills its parent with the image (a plain tile while it
// loads) and marks live photos.
func (a *App) renderPostImageCell(c *ui.Context, bmp *ui.Bitmap, img PostImage) {
	ui.Box(c).Absolute().Left(0).Top(0).Right(0).Bottom(0).Background(ColorBgSecondary).Children(func() {
		if bmp != nil {
			ui.Image(c, bmp).Absolute().Left(0).Top(0).Right(0).Bottom(0).Fit(ui.Cover)
		}
	})
	if img.Live {
		ui.Box(c).Absolute().Left(6).Top(6).Padding(1, 6).Radius(4).Background(ui.RGBA(0, 0, 0, 0.55)).Children(func() {
			ui.Text(c, "实况").FontSize(11).FontWeight(600).TextColor(ColorWhite).SingleLine()
		})
	}
}

// renderPostCard renders a unified social-network PostCard (docs/ui.md §3, §3.1, §3.3).
func (a *App) renderPostCard(c *ui.Context, post PostCardData) {
	cardRow := ui.Row(c).
		FillWidth().
		Padding(14, 16).
		Gap(12).
		AlignItems(ui.Start).
		BorderWidth(0, 0, 1, 0).
		BorderColor(ColorBorder).
		Background(ColorBgMain).
		ClipX()

	cardRow.Children(func() {
		// Left: 40px circular creator avatar
		a.renderAvatar(c, post.AuthorName, post.Mid, 40, false, "", func() {
			a.OpenUser(post.Mid, post.AuthorName)
		})

		// Right: Main post column
		ui.Column(c).Grow(1).Gap(6).ClipX().Children(func() {
			// 1. Header line: Author · Relative Time · Type · Pinned/Unread · StatusDot
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(6).ClipX().Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(6).Grow(1).ClipX().Children(func() {
					authorName := post.AuthorName
					if authorName == "" {
						authorName = "未知 UP 主"
					}
					authorBtn := ui.ButtonBase(c).Padding(0).Children(func() {
						ui.Text(c, authorName).FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
					})
					if authorBtn.Clicked() {
						a.OpenUser(post.Mid, post.AuthorName)
					}

					if post.PubTs > 0 {
						rel := FormatRelativeTime(post.PubTs)
						exact := FormatTimestamp(post.PubTs)
						ui.Text(c, "· "+rel).FontSize(13).TextColor(ColorTextMuted).Tooltip(exact).SingleLine()
					}
					if post.TypeLabel != "" {
						ui.Text(c, "· "+post.TypeLabel).FontSize(13).TextColor(ColorTextMuted).SingleLine()
					}
					if post.Pinned {
						ui.Box(c).Padding(1, 6).Radius(4).Background(ColorAccentBg).Children(func() {
							ui.Text(c, "置顶").FontSize(11).FontWeight(600).TextColor(ColorAccent).SingleLine()
						})
					}
				})

				ui.Row(c).AlignItems(ui.Center).Gap(6).Shrink(0).Children(func() {
					// One dot for the whole post: its own state and its
					// video's, the worse of the two (images and the video
					// card carry none).
					if dot := postDotStatus(post); dot != "" && dot != "none" {
						label := post.DotLabel
						if dot != post.DotStatus {
							label = ""
						}
						DrawStatusDot(c, dot, label)
					}
					if !post.InHomeFeed && post.Key != "" {
						open := a.PostMenuKey == post.Key
						MoreMenuButton(c, "帖子更多："+post.Key, 28, &open, func(m *ui.Menu) {
							a.postMenuItems(c, m, post)
						})
						keyedMenuOpen(&a.PostMenuKey, post.Key, open)
					}
				})
			})

			// 2. Title & Body Text
			if post.Title != "" && (post.Video == nil || post.Title != post.Video.Title) {
				ui.Text(c, post.Title).FontSize(15).FontWeight(600).TextColor(ColorTextMain).MaxLines(2)
			}
			if post.Text != "" && post.Text != post.Title {
				a.renderExpandableText(c, post.Key, post.Text)
			}

			if post.Degraded {
				ui.Row(c).FillWidth().Padding(8, 12).Radius(8).Background(ColorBgSecondary).AlignItems(ui.Center).Gap(6).Children(func() {
					DrawIcon(c, "alert-circle", 16, ColorTextMuted, 1.75)
					ui.Text(c, fmt.Sprintf("TRACE 暂不支持完整展示「%s」类型，可在浏览器中打开查看", post.TypeLabel)).
						FontSize(13).TextColor(ColorTextMuted).MaxLines(2)
				})
			}

			// 3. Attached Image 9-Grid
			if len(post.Images) > 0 {
				a.renderPostImageGrid(c, post)
			}

			// 4. Attached Video 16:9 Card
			if post.Video != nil {
				v := post.Video
				// Cover on the left, title and description beside it, as
				// the site shows a video in a post (DEC-040). No counts:
				// they change and are not kept.
				vCard := ui.ButtonBase(c).
					Label("播放视频卡片："+firstNonEmptyStrUI(v.ItemKey, v.BVID, post.Key)).
					FillWidth().
					Radius(10).
					Border(1, ColorBorder).
					Background(ColorBgSecondary).
					Clip()
				if vCard.Hovered() {
					vCard.Background(ColorHover)
				}
				vCard.Children(func() {
					ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Start).ClipX().Children(func() {
						// No title on the placeholder: it is beside the cover.
						a.renderMediaThumb(c, v.ItemKey, v.RelCover, "", v.Duration, "", v.Cached, videoCoverWidth, videoCoverWidth*9/16, v.CoverURL)
						ui.Column(c).Grow(1).Basis(0).Padding(10, 12, 10, 0).Gap(4).ClipX().Children(func() {
							ui.Text(c, v.Title).FontSize(14).FontWeight(600).TextColor(ColorTextMain).MaxLines(2)
							if desc := strings.TrimSpace(v.Desc); desc != "" && desc != "-" {
								ui.Text(c, desc).FontSize(13).TextColor(ColorTextMuted).MaxLines(2)
							}
							if v.QualityTag != "" {
								ui.Text(c, v.QualityTag).FontSize(12).TextColor(ColorTextMuted).SingleLine()
							}
						})
					})
				})
				if vCard.Clicked() {
					if v.Cached && v.ItemKey != "" {
						if it, ok, err := a.backend.GetLibraryItem(v.ItemKey); err == nil && ok {
							a.OpenPlayerOverlay(it)
						}
					} else {
						a.OpenVideo(v.BVID, v.Title, post.AuthorName, post.Mid, v.CoverURL, v.Duration)
					}
				}
			}

			// 5. Forwarded Origin Quote Box
			if post.Origin != nil {
				orig := post.Origin
				ui.Column(c).
					FillWidth().
					Padding(10, 12).
					Radius(12).
					Background(ColorBgSecondary).
					Border(1, ColorBorder).
					Gap(4).
					ClipX().
					Children(func() {
						ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
							a.renderAvatar(c, orig.AuthorName, orig.Mid, 20, false, "", nil)
							ui.Text(c, "@"+orig.AuthorName).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
							if orig.TypeLabel != "" {
								ui.Text(c, "· "+orig.TypeLabel).FontSize(12).TextColor(ColorTextMuted).SingleLine()
							}
						})
						if orig.Title != "" {
							ui.Text(c, orig.Title).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
						}
						if orig.Text != "" {
							ui.Text(c, orig.Text).FontSize(13).TextColor(ColorTextMuted).MaxLines(3)
						}
					})
			}

		})
	})
}

// postDotStatus is the single status shown for a post: the worse of the
// post's own state and its video's (failed, then caching, then partial, then
// complete). A post whose video is only partly cached is yellow.
// videoCoverWidth is the width of a video's cover in a post.
const videoCoverWidth = 200

func postDotStatus(post PostCardData) string {
	dot := post.DotStatus
	if post.Video != nil && dotRank(post.Video.Status) > dotRank(dot) {
		dot = post.Video.Status
	}
	return dot
}

func dotRank(status string) int {
	switch {
	case status == "" || status == "none":
		return 0
	case status == "green" || status == "completed" || status == "complete":
		return 1
	case status == "yellow" || status == "partial" || status == "upgrade" || strings.HasPrefix(status, "paused"):
		return 2
	case statusInProgress(status):
		return 3
	default:
		return 4
	}
}

// postMenuItems fills a post's "⋯" menu: caching an uncached post, then the
// browser and the link (R8, F-056). Posts carry no text buttons of their own.
func (a *App) postMenuItems(c *ui.Context, m *ui.Menu, post PostCardData) {
	if !post.Cached && (post.DotStatus == "" || post.DotStatus == "none") && post.CacheBtnLabel != "" {
		if m.Item("缓存").Chosen() {
			a.cachePost(post)
		}
	}
	targetURL := post.URL
	if targetURL == "" && post.RawID != "" {
		switch post.Kind {
		case "video":
			targetURL = "https://www.bilibili.com/video/" + post.RawID
		case "dynamic":
			targetURL = "https://t.bilibili.com/" + post.RawID
		case "live":
			if post.LibItem != nil && post.LibItem.Live != nil && post.LibItem.Live.RoomID > 0 {
				targetURL = fmt.Sprintf("https://live.bilibili.com/%d", post.LibItem.Live.RoomID)
			}
		}
	}
	if targetURL == "" {
		return
	}
	if m.Item("在浏览器中打开").Chosen() {
		_ = a.backend.Desktop().OpenExternalURL(targetURL)
	}
	if m.Item("复制链接").Chosen() {
		c.WriteClipboard(targetURL)
		c.Toast("已复制链接")
		a.BannerInfo = "已复制链接"
	}
}

// cachePost queues an uncached post: a dynamic with its linked videos, or a video.
func (a *App) cachePost(post PostCardData) {
	if post.DynItem != nil {
		dynCopy := *post.DynItem
		_, err := a.backend.EnqueueDynamicCache(context.Background(), download.DynamicEnqueueRequest{
			DynamicID:         dynCopy.ID,
			Item:              &dynCopy,
			CacheLinkedVideos: true,
		})
		a.showError(err)
		if err == nil {
			a.BannerInfo = "已加入缓存队列：" + dynCopy.ID
		}
		a.RefreshData()
	} else if post.Kind == "video" && post.RawID != "" {
		_, err := a.backend.EnqueueVideoCache(context.Background(), download.VideoEnqueueRequest{BVID: post.RawID})
		a.showError(err)
		if err == nil {
			a.BannerInfo = "已加入缓存队列：" + post.RawID
		}
		a.RefreshData()
	}
}

// formatLiveRecordingSpec formats the live stream quality and codec (e.g. "原画 · AVC")
// from LiveRecordingInfo, falling back to Segments[i] for pre-Rev3 recorded items (R4).
func formatLiveRecordingSpec(liveInfo *library.LiveRecordingInfo, segPath string) string {
	if liveInfo == nil {
		return ""
	}
	qName, qn, codec := liveInfo.QualityName, liveInfo.Quality, liveInfo.Codec
	if segPath != "" {
		for _, seg := range liveInfo.Segments {
			if seg.File == segPath {
				if seg.QualityName != "" || seg.Quality > 0 || seg.Codec != "" {
					qName, qn, codec = seg.QualityName, seg.Quality, seg.Codec
				}
				break
			}
		}
	}
	if qName == "" && qn == 0 && codec == "" && len(liveInfo.Segments) > 0 {
		s0 := liveInfo.Segments[0]
		qName, qn, codec = s0.QualityName, s0.Quality, s0.Codec
	}
	return bilibili.FormatLiveQualitySpec(qName, qn, codec)
}

// itemKindLabel names a cached item's kind for people, never the internal value.
func itemKindLabel(kind string) string {
	switch kind {
	case "dynamic":
		return "动态"
	case "live":
		return "直播录像"
	}
	return "视频"
}

// feedEntryToPostCard converts a library.FeedEntry into a PostCardData for /home.
// feedEntryToPostCard returns the card for a feed entry, built once until
// tasks or the library change (see App.postCards).
func (a *App) feedEntryToPostCard(entry library.FeedEntry) PostCardData {
	return a.cachedPostCard("feed:"+entry.FeedKey, func() PostCardData { return a.buildFeedPostCard(entry) })
}

func (a *App) cachedPostCard(key string, build func() PostCardData) PostCardData {
	if card, ok := a.postCards[key]; ok {
		return card
	}
	card := build()
	if a.postCards == nil {
		a.postCards = make(map[string]PostCardData)
	}
	if len(a.postCards) > 600 {
		clear(a.postCards)
	}
	a.postCards[key] = card
	return card
}

func (a *App) buildFeedPostCard(entry library.FeedEntry) PostCardData {
	typeLabel := itemKindLabel(entry.Kind)
	dot := entry.CacheStatus
	if entry.Cached && dot == "" {
		dot = "green"
	}

	var libPtr *library.ItemMetadata
	var relCover string
	var qualityTag string
	var durationStr string
	var images []PostImage

	if entry.Cached && entry.ItemKey != "" {
		if item, ok, err := a.backend.GetLibraryItem(entry.ItemKey); err == nil && ok {
			libPtr = &item
			if item.TypeLabel != "" {
				typeLabel = item.TypeLabel
			}
			for _, m := range item.Media {
				if m.Role == "cover" && relCover == "" {
					relCover = m.Path
				} else if m.Role == "image" {
					images = append(images, PostImage{
						RelPath: m.Path,
						Width:   m.Width,
						Height:  m.Height,
						Live:    m.Motion != "",
					})
				}
			}
			if len(item.Parts) > 0 {
				p0 := item.Parts[0]
				if p0.Duration > 0 {
					durationStr = FormatDuration(p0.Duration)
				}
				var qParts []string
				if p0.Video.QualityName != "" {
					qParts = append(qParts, p0.Video.QualityName)
				}
				if p0.Audio.Name != "" {
					qParts = append(qParts, p0.Audio.Name)
				}
				qualityTag = strings.Join(qParts, " · ")
			} else if item.Live != nil {
				qualityTag = formatLiveRecordingSpec(item.Live, "")
			}
		}
	}

	var videoCard *PostVideo
	text := entry.Summary
	if entry.Kind == "video" || entry.Kind == "live" {
		// A video's description belongs beside its cover, not above it.
		text = ""
		if durationStr == "" && libPtr != nil && libPtr.Duration > 0 {
			durationStr = FormatDuration(libPtr.Duration)
		}
		videoCard = &PostVideo{
			BVID:       entry.RawID,
			Title:      entry.Title,
			Desc:       entry.Summary,
			CoverURL:   entry.CoverURL,
			RelCover:   relCover,
			Duration:   durationStr,
			ItemKey:    entry.ItemKey,
			Cached:     entry.Cached,
			Status:     dot,
			QualityTag: qualityTag,
		}
	} else if libPtr != nil && len(libPtr.Videos) > 0 {
		refKey := library.CanonicalItemKey(entry.Site, "video", libPtr.Videos[0])
		if refItem, ok, err := a.backend.GetLibraryItem(refKey); err == nil && ok {
			var refCover, refDur, refQTag string
			for _, m := range refItem.Media {
				if m.Role == "cover" && refCover == "" {
					refCover = m.Path
				}
			}
			if len(refItem.Parts) > 0 {
				p0 := refItem.Parts[0]
				if p0.Duration > 0 {
					refDur = FormatDuration(p0.Duration)
				}
				var qParts []string
				if p0.Video.QualityName != "" {
					qParts = append(qParts, p0.Video.QualityName)
				}
				if p0.Audio.Name != "" {
					qParts = append(qParts, p0.Audio.Name)
				}
				refQTag = strings.Join(qParts, " · ")
			}
			if refDur == "" && refItem.Duration > 0 {
				refDur = FormatDuration(refItem.Duration)
			}
			refDot := library.ComputeDotState(refItem).Dot
			videoCard = &PostVideo{
				BVID:       refItem.RawID,
				Title:      firstNonEmptyStrUI(refItem.Title, entry.Title),
				Desc:       refItem.Desc,
				RelCover:   refCover,
				Duration:   refDur,
				ItemKey:    refItem.ItemKey,
				Cached:     true,
				Status:     refDot,
				QualityTag: refQTag,
			}
		}
	}

	return PostCardData{
		Key:        entry.FeedKey,
		ItemKey:    entry.ItemKey,
		Site:       entry.Site,
		Kind:       entry.Kind,
		RawID:      entry.RawID,
		Mid:        entry.Mid,
		AuthorName: entry.AuthorName,
		AuthorFace: entry.AuthorFace,
		PubTs:      entry.PubTs,
		TypeLabel:  typeLabel,
		Title:      entry.Title,
		Text:       text,
		URL:        entry.CanonicalURL,
		Cached:     entry.Cached,
		DotStatus:  dot,
		DotLabel:   "已缓存：" + entry.RawID,
		Images:     images,
		Video:      videoCard,
		LibItem:    libPtr,
		InHomeFeed: true,
	}
}

// dynamicItemToPostCard converts a bilibili.DynamicItem into a PostCardData for /user and /subscriptions.
func (a *App) dynamicItemToPostCard(d bilibili.DynamicItem, openLabel, cacheLabel string) PostCardData {
	return a.cachedPostCard("dyn:"+d.ID+"|"+openLabel+"|"+cacheLabel, func() PostCardData {
		return a.buildDynamicPostCard(d, openLabel, cacheLabel)
	})
}

func (a *App) buildDynamicPostCard(d bilibili.DynamicItem, openLabel, cacheLabel string) PostCardData {
	typeLabel := d.TypeLabel
	if typeLabel == "" {
		typeLabel = d.Type
	}
	var imgs []PostImage
	for _, im := range d.Images {
		imgs = append(imgs, PostImage{
			URL:    im.URL,
			Width:  im.Width,
			Height: im.Height,
			Live:   im.LiveURL != "",
		})
	}
	var vid *PostVideo
	if len(d.Videos) > 0 {
		v0 := d.Videos[0]
		dur := v0.DurationText
		if dur == "" && v0.DurationSec > 0 {
			dur = FormatDuration(v0.DurationSec)
		}
		vid = &PostVideo{
			BVID:     v0.BVID,
			Title:    v0.Title,
			Desc:     v0.Desc,
			CoverURL: v0.Cover,
			Duration: dur,
		}
		if v0.BVID != "" {
			vKey := library.CanonicalItemKey("bilibili", "video", v0.BVID)
			if vItem, ok, err := a.backend.GetLibraryItem(vKey); err == nil && ok {
				vid.ItemKey = vItem.ItemKey
				vid.Cached = true
				vid.Status = library.ComputeDotState(vItem).Dot
				for _, m := range vItem.Media {
					if m.Role == "cover" && vid.RelCover == "" {
						vid.RelCover = m.Path
					}
				}
			}
		}
	}
	var orig *PostCardData
	if d.Origin != nil {
		o := a.buildDynamicPostCard(*d.Origin, "", "")
		orig = &o
	}

	dotStatus := ""
	itemKey := library.CanonicalItemKey("bilibili", "dynamic", d.ID)
	var hasItem bool
	if it, ok, err := a.backend.GetLibraryItem(itemKey); err == nil && ok {
		hasItem = true
		dotStatus = library.ComputeDotState(it).Dot
	}
	if st, ok := a.backend.States([]string{itemKey})[itemKey]; ok && st.Dot != "" && st.Dot != "none" {
		dotStatus = st.Dot
	}

	dCopy := d
	return PostCardData{
		Key:           "dyn:" + d.ID,
		ItemKey:       itemKey,
		Site:          "bilibili",
		Kind:          "dynamic",
		RawID:         d.ID,
		Mid:           d.Author.Mid,
		AuthorName:    d.Author.Name,
		AuthorFace:    d.Author.Face,
		PubTs:         d.PubTs,
		TypeLabel:     typeLabel,
		Title:         d.Title,
		Text:          d.Text,
		URL:           d.CanonicalURL,
		Pinned:        d.Pinned,
		Degraded:      d.Degraded,
		Cached:        hasItem || (dotStatus != "" && dotStatus != "none"),
		DotStatus:     dotStatus,
		Images:        imgs,
		Video:         vid,
		Origin:        orig,
		DynItem:       &dCopy,
		OpenBtnLabel:  openLabel,
		CacheBtnLabel: cacheLabel,
	}
}
