package ui

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

// PostImage represents an image in a PostCard.
type PostImage struct {
	URL     string
	RelPath string
	Width   int
	Height  int
	Live    bool   // a live photo (实况): the still of a short motion clip
	LiveURL string // online motion clip of a live photo not cached yet
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
	AuthorUID     string
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
	NotePages     []douyinNoteRenderPage
	Video         *PostVideo
	Origin        *PostCardData
	DynItem       *bilibili.DynamicItem
	LibItem       *library.ItemMetadata
	InHomeFeed    bool
	Offline       bool
	LocalManage   bool // cached post with a local-only "⋯" (show in folder, delete)
	OpenBtnLabel  string
	CacheBtnLabel string
}

// homePostSubscription is the subscription a Home post belongs to: its
// author's, or for a co-creation cached for a subscribed account (via
// collab) that account's.
func (a *App) homePostSubscription(post PostCardData) (subscription.Subscription, bool) {
	if !post.InHomeFeed {
		return subscription.Subscription{}, false
	}
	if sub, ok := a.subscriptionForAuthor(post.Site, post.AuthorUID, post.Mid); ok {
		return sub, true
	}
	if post.LibItem != nil && post.LibItem.Via != nil && post.LibItem.Via.Reason == library.ViaCollab {
		return a.subscriptionForAuthor(post.Site, post.LibItem.Via.UID, 0)
	}
	return subscription.Subscription{}, false
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
	skipRemote := a.pendingLocalAvatar
	a.pendingLocalAvatar = false
	bmp := customBmp
	if bmp == nil && !skipRemote {
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

// renderPostImageGrid shows a post's images as the site does: one image at its
// own proportions so nothing is cut off; two or more as square cells in two
// (2 or 4 images) or three columns, up to nine.
func (a *App) renderPostImageGrid(c *ui.Context, post PostCardData) {
	n := len(post.Images)
	if n == 0 {
		return
	}
	openAt := func(idx int) {
		urls := make([]string, n)
		rels := make([]string, n)
		liveURLs := make([]string, n)
		for i, img := range post.Images {
			if !post.Offline {
				urls[i] = img.URL
				liveURLs[i] = img.LiveURL
			}
			rels[i] = img.RelPath
		}
		a.openPostImageViewer(post.ItemKey, post.Site, urls, rels, liveURLs, idx)
	}

	if n == 1 {
		img := post.Images[0]
		bmp := a.postImageBitmap(post, img, thumbMaxEdge)
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
				a.renderPostImageCell(c, a.postImageBitmap(post, img, gridThumbEdge), img)
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
func (a *App) postImageBitmap(post PostCardData, img PostImage, maxEdge int) *ui.Bitmap {
	if bmp := a.lookupLocalBitmapEdge(post.ItemKey, img.RelPath, maxEdge); bmp != nil {
		return bmp
	}
	if post.Offline {
		return nil
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
		var openCreator func()
		authorFace := post.AuthorFace
		if post.Offline {
			authorFace = ""
		}
		// On Home the avatar opens the author's subscription; the user page
		// (a parse) only opens for an author without one.
		homeSub, homeSubOK := a.homePostSubscription(post)
		if homeSubOK {
			sub := homeSub
			openCreator = func() { a.OpenSubscriptionDetail(sub) }
		} else if !post.Offline {
			openCreator = func() {
				if normalizeSiteIDUI(post.Site) == "douyin" {
					if post.AuthorUID != "" {
						a.OpenDouyinUser(post.AuthorUID, post.AuthorName)
					}
					return
				}
				a.OpenUser(post.Mid, post.AuthorName)
			}
		}
		// Left: 40px circular creator avatar
		ui.Box(c).Label("作者头像：" + post.Key).Shrink(0).Children(func() {
			a.renderAccountAvatar(c, post.Site, post.AuthorName, post.AuthorUID, post.Mid, 40, false, "", openCreator, authorFace)
		})

		// Right: Main post column
		ui.Column(c).Grow(1).Gap(6).ClipX().Children(func() {
			// 1. Header line: Author · Site (on Home) · Relative Time · Type · Pinned · StatusDot
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(6).ClipX().Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(6).Grow(1).ClipX().Children(func() {
					authorName := post.AuthorName
					if authorName == "" {
						if normalizeSiteIDUI(post.Site) == "douyin" {
							authorName = "未知作者"
						} else {
							authorName = "未知 UP 主"
						}
					}
					if openCreator != nil {
						authorBtn := ui.ButtonBase(c).Padding(0).Children(func() {
							ui.Text(c, authorName).FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
						})
						if authorBtn.Clicked() {
							openCreator()
						}
					} else {
						ui.Text(c, authorName).FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
					}
					// On Home the subscription's name follows the account in
					// smaller accent text, unless it only repeats the account.
					if homeSubOK && strings.TrimSpace(homeSub.Name) != "" && strings.TrimSpace(homeSub.Name) != strings.TrimSpace(authorName) {
						subName := strings.TrimSpace(homeSub.Name)
						subBtn := ui.ButtonBase(c).Label("主页订阅名：" + post.Key).Padding(0).Shrink(1).Children(func() {
							ui.Text(c, subName).FontSize(13).FontWeight(500).TextColor(ColorAccent).SingleLine()
						})
						if subBtn.Clicked() {
							openCreator()
						}
					}
					if post.InHomeFeed {
						renderSiteSmallBadgeWithDot(c, post.Site, "主页作者站点："+post.Key)
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
					// card carry none). Home lists cached posts only and
					// draws no dot.
					if dot := postDotStatus(post); !post.InHomeFeed && dot != "" && dot != "none" {
						label := post.DotLabel
						if dot != post.DotStatus {
							label = ""
						}
						DrawStatusDot(c, dot, label)
					}
					if post.LocalManage && post.LibItem != nil {
						a.renderCachedPostMenu(c, post.Key, *post.LibItem)
					} else if post.Offline {
						if post.LibItem != nil {
							a.renderLibraryItemMoreMenu(c, *post.LibItem, 28)
						}
					} else if !post.InHomeFeed && post.Key != "" {
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
				if normalizeSiteIDUI(post.Site) == "douyin" {
					a.renderRichHashtagText(c, post.Key, post.Text)
				} else {
					a.renderExpandableText(c, post.Key, post.Text)
				}
			}

			if post.Degraded {
				ui.Row(c).FillWidth().Padding(8, 12).Radius(8).Background(ColorBgSecondary).AlignItems(ui.Center).Gap(6).Children(func() {
					DrawIcon(c, "alert-circle", 16, ColorTextMuted, 1.75)
					ui.Text(c, fmt.Sprintf("TRACE 暂不支持完整展示「%s」类型，可在浏览器中打开查看", post.TypeLabel)).
						FontSize(13).TextColor(ColorTextMuted).MaxLines(2)
				})
			}

			// 3. Attached Pages / Image 9-Grid + Douyin Note BGM Bar
			if len(post.NotePages) > 0 {
				a.renderDouyinNotePagesGrid(c, post.ItemKey, post.NotePages)
				if post.LibItem != nil && post.LibItem.Music != nil {
					m := post.LibItem.Music
					hint := ""
					if m.File == "" {
						hint = "本机配乐文件缺失"
					}
					a.renderBGMBar(c, "feed-bgm:"+post.Key, m.Title, m.Author, m.Original, m.Duration, post.ItemKey, m.File, "", hint)
				}
			} else if len(post.Images) > 0 {
				a.renderPostImageGrid(c, post)
			}

			// 4. Attached Video Card (16:9 horizontal for Bilibili/live, 3:4 vertical cover on left for Douyin video)
			if post.Video != nil {
				v := post.Video
				isDouyinVideo := normalizeSiteIDUI(post.Site) == "douyin" && post.Kind == "video"
				coverW := float32(videoCoverWidth)
				coverH := float32(videoCoverWidth * 9 / 16)
				if isDouyinVideo {
					coverW = 108
					coverH = 144
				}
				vCard := ui.ButtonBase(c).
					Label("播放视频卡片："+firstNonEmptyStrUI(v.ItemKey, v.BVID, post.Key)).
					FillWidth().
					Radius(10).
					Border(1, ColorBorder).
					Background(ColorBgMain).
					Clip()
				if vCard.Hovered() {
					vCard.Background(ColorHover)
				}
				coverURL := v.CoverURL
				if post.Offline {
					coverURL = ""
				}
				vCard.Children(func() {
					ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Start).ClipX().Children(func() {
						// No title on the placeholder: it is beside the cover.
						a.renderMediaThumb(c, v.ItemKey, v.RelCover, "", v.Duration, "", v.Cached, coverW, coverH, coverURL)
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
						it, ok, err := a.backend.GetLibraryItem(v.ItemKey)
						if err != nil {
							a.showError(err)
						} else if ok {
							a.OpenPlayerOverlay(it)
						}
					} else if !post.Offline {
						if isDouyinVideo {
							a.OpenDouyinWork(v.BVID, "video", v.Title, post.AuthorName, post.AuthorUID, v.CoverURL, v.Duration)
						} else if a.CurrentRoute == RouteUser && a.CurrentUser != nil {
							a.openVideoFor(v.BVID, v.Title, post.AuthorName, post.Mid, v.CoverURL, v.Duration, a.CurrentUser.Mid)
						} else {
							a.OpenVideo(v.BVID, v.Title, post.AuthorName, post.Mid, v.CoverURL, v.Duration)
						}
					}
				}
			}

			// 5. Forwarded Origin Quote Box
			if post.Origin != nil {
				orig := post.Origin
				origFace := orig.AuthorFace
				if post.Offline {
					origFace = ""
				}
				ui.Column(c).
					FillWidth().
					Padding(10, 12).
					Radius(12).
					Background(ColorBgMain).
					Border(1, ColorBorder).
					Gap(4).
					ClipX().
					Children(func() {
						ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
							a.renderAvatar(c, orig.AuthorName, orig.Mid, 20, false, "", nil, origFace)
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

// videoCoverWidth is the width of a video's cover in a post.
const videoCoverWidth = 200

// postDotStatus is the single status shown for a post: the worse of the
// post's own state and its video's (failed, then caching, then partial, then
// complete). A post whose video is only partly cached is yellow.
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
// browser and the link. Posts carry no text buttons of their own.
func (a *App) postMenuItems(c *ui.Context, m *ui.Menu, post PostCardData) {
	if !post.Cached && (post.DotStatus == "" || post.DotStatus == "none") && post.CacheBtnLabel != "" {
		if m.Item("缓存").Chosen() {
			a.cachePost(post)
		}
	}
	targetURL := post.URL
	if targetURL == "" && post.RawID != "" {
		if normalizeSiteIDUI(post.Site) == "douyin" {
			switch post.Kind {
			case "dynamic":
				targetURL = "https://www.douyin.com/note/" + post.RawID
			case "live":
				if post.LibItem != nil && post.LibItem.Live != nil && post.LibItem.Live.WebRID != "" {
					targetURL = "https://live.douyin.com/" + post.LibItem.Live.WebRID
				}
			default:
				targetURL = "https://www.douyin.com/video/" + post.RawID
			}
		} else {
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
	}
	if targetURL == "" {
		return
	}
	if m.Item("在浏览器中打开").Chosen() {
		if err := a.backend.Desktop().OpenExternalURL(targetURL); err != nil {
			a.showError(err)
		}
	}
	if m.Item("复制链接").Chosen() {
		c.WriteClipboard(targetURL)
		c.Toast("已复制链接")
		a.BannerInfo = "已复制链接"
	}
}

// cachePost queues an uncached post off the UI thread: a Douyin work,
// a Bilibili dynamic with its linked videos, or a Bilibili video.
func (a *App) cachePost(post PostCardData) {
	if normalizeSiteIDUI(post.Site) == "douyin" && post.RawID != "" {
		a.cacheDouyinWorkNow(post.RawID)
		return
	}
	backend := a.backend
	if post.DynItem != nil {
		dynCopy := *post.DynItem
		requestGuarded(a, "cache-dyn:"+dynCopy.ID, nil, func(ctx context.Context) (struct{}, error) {
			_, err := backend.EnqueueDynamicCache(ctx, download.DynamicEnqueueRequest{
				DynamicID:         dynCopy.ID,
				Item:              &dynCopy,
				CacheLinkedVideos: true,
			})
			return struct{}{}, err
		}, func(struct{}) {
			a.BannerInfo = "已加入缓存队列：" + dynCopy.ID
			a.RefreshData()
		}, func(err error) {
			a.showError(err)
			a.RefreshData()
		})
	} else if post.Kind == "video" && post.RawID != "" {
		bvid := post.RawID
		requestGuarded(a, "cache-vid:"+bvid, nil, func(ctx context.Context) (struct{}, error) {
			_, err := backend.EnqueueVideoCache(ctx, download.VideoEnqueueRequest{BVID: bvid})
			return struct{}{}, err
		}, func(struct{}) {
			a.BannerInfo = "已加入缓存队列：" + bvid
			a.RefreshData()
		}, func(err error) {
			a.showError(err)
			a.RefreshData()
		})
	}
}
