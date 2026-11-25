package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
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
	IsGIF   bool   // animated GIF
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
	Key               string
	ItemKey           string
	Site              string
	Kind              string // "video", "dynamic", "live"
	RawID             string
	Mid               int64
	AuthorUID         string
	AuthorName        string
	AuthorHandle      string
	AuthorFace        string
	PubTs             int64
	TypeLabel         string
	Title             string
	Text              string
	Links             []service.TextLinkDTO
	URL               string
	Pinned            bool
	IsEdited          bool
	Degraded          bool
	Cached            bool
	DotStatus         string
	DotLabel          string
	Images            []PostImage
	NotePages         []douyinNoteRenderPage
	Video             *PostVideo
	Origin            *PostCardData
	OriginUnavailable string
	Unavailable       bool
	UnavailableReason string
	Incomplete        bool
	VisibleLabel      string
	WeiboCard         *service.WeiboRichCardDTO
	WeiboMid          string
	WeiboCacheVia     *library.ItemVia
	XCard             *service.XRichCardDTO
	XPoll             *service.XPollSummaryDTO
	XStatusID         string
	XCacheVia         *library.ItemVia
	HLSOnlyNotice     string
	ThreadCount       int
	OnOpenPost        func()
	DynItem           *bilibili.DynamicItem
	LibItem           *library.ItemMetadata
	InHomeFeed        bool
	Offline           bool
	LocalManage       bool // cached post with a local-only "⋯" (show in folder, delete)
	OpenBtnLabel      string
	CacheBtnLabel     string
}

func parseBilibiliMidUI(siteID, uid string) int64 {
	if normalizeSiteIDUI(siteID) != "bilibili" {
		return 0
	}
	id, err := strconv.ParseInt(strings.TrimSpace(uid), 10, 64)
	if err != nil || id <= 0 {
		return 0
	}
	return id
}

func postAuthorUIDUI(post PostCardData) string {
	if uid := strings.TrimSpace(post.AuthorUID); uid != "" {
		return uid
	}
	if normalizeSiteIDUI(post.Site) == "bilibili" && post.Mid > 0 {
		return strconv.FormatInt(post.Mid, 10)
	}
	return ""
}

func subscriptionAccountName(sub subscription.Subscription, siteID, uid string) string {
	normSite := normalizeSiteIDUI(siteID)
	normUID := strings.TrimSpace(uid)
	if normUID == "" {
		return ""
	}
	for _, acc := range sub.Accounts {
		if normalizeSiteIDUI(acc.Site) == normSite && strings.TrimSpace(acc.UID) == normUID {
			if name := strings.TrimSpace(acc.Name); name != "" {
				return name
			}
		}
	}
	return ""
}

func subscriptionOwnsAccount(sub subscription.Subscription, siteID, uid string, mid int64) bool {
	normSite := normalizeSiteIDUI(siteID)
	normUID := strings.TrimSpace(uid)
	if normSite == "bilibili" && mid > 0 && sub.Mid == mid {
		return true
	}
	for _, acc := range sub.Accounts {
		accSite := normalizeSiteIDUI(acc.Site)
		accUID := strings.TrimSpace(acc.UID)
		if normUID != "" && accSite == normSite && accUID == normUID {
			return true
		}
		if normSite == "bilibili" && mid > 0 && accSite == "bilibili" && parseBilibiliMidUI(accSite, accUID) == mid {
			return true
		}
	}
	return false
}

// cachedViaAccount resolves the cached account (site, uid, mid, name) for a
// post cached via collaboration (ViaCollab) or, when includeForward is true,
// cross-account forward (ViaForward whose UID differs from the post's author,
// e.g. Weibo fast-retweets).
func cachedViaAccount(post PostCardData, includeForward bool) (viaSite, viaUID string, viaMid int64, viaName string, ok bool) {
	if post.LibItem == nil || post.LibItem.Via == nil {
		return "", "", 0, "", false
	}
	via := post.LibItem.Via
	viaUID = strings.TrimSpace(via.UID)
	if viaUID == "" {
		return "", "", 0, "", false
	}
	switch via.Reason {
	case library.ViaCollab:
	case library.ViaForward:
		if !includeForward || viaUID == postAuthorUIDUI(post) {
			return "", "", 0, "", false
		}
	default:
		return "", "", 0, "", false
	}
	viaSite = firstNonEmptyStrUI(via.Site, post.Site)
	viaMid = parseBilibiliMidUI(viaSite, viaUID)
	viaName = strings.TrimSpace(via.Name)
	return viaSite, viaUID, viaMid, viaName, true
}

// hasLocalAccountAvatar reports whether (siteID, uid) has a local avatar copy,
// reusing App.images (and its negative-miss cache) so steady frames never
// repeat AccountAvatarPath disk stats.
func (a *App) hasLocalAccountAvatar(siteID, uid string) bool {
	uid = strings.TrimSpace(uid)
	if a.backend == nil || uid == "" {
		return false
	}
	if a.imageQueue == nil {
		return a.backend.AccountAvatarPath(normalizeSiteIDUI(siteID), uid) != ""
	}
	bmp := a.lookupAccountAvatarBitmap(siteID, uid)
	pending := a.pendingLocalAvatar
	a.pendingLocalAvatar = false
	return bmp != nil || pending
}

// postCardAvatarSpec holds the resolved identity for the 40px left avatar of a
// PostCard.
type postCardAvatarSpec struct {
	site     string
	uid      string
	mid      int64
	name     string
	face     string
	isCollab bool
	viaSite  string
	viaUID   string
	viaName  string
}

// resolvePostCardAvatar resolves the 40px left avatar for a PostCard. For
// collaboration posts (ViaCollab) or cross-account forwards (ViaForward whose
// UID differs from the author, e.g. Weibo fast-retweets) shown on Home or
// Subscription Detail › 内容 (post.InHomeFeed), it displays the cached (via)
// account's avatar instead of the co-creator's/original author's, falling back
// to the subscription's primary account avatar when the via account lacks a local copy.
func (a *App) resolvePostCardAvatar(post PostCardData, homeSub subscription.Subscription, homeSubOK bool) postCardAvatarSpec {
	face := post.AuthorFace
	if post.Offline {
		face = ""
	}
	if !post.InHomeFeed {
		return postCardAvatarSpec{
			site: post.Site,
			uid:  post.AuthorUID,
			mid:  post.Mid,
			name: post.AuthorName,
			face: face,
		}
	}
	viaSite, viaUID, viaMid, rawViaName, ok := cachedViaAccount(post, true)
	if !ok {
		return postCardAvatarSpec{
			site: post.Site,
			uid:  post.AuthorUID,
			mid:  post.Mid,
			name: post.AuthorName,
			face: face,
		}
	}

	viaSub, viaSubOK := a.subscriptionForAuthor(viaSite, viaUID, viaMid)
	if !viaSubOK && homeSubOK && subscriptionOwnsAccount(homeSub, viaSite, viaUID, viaMid) {
		viaSub, viaSubOK = homeSub, true
	}
	matchedAccName := ""
	subName := ""
	if viaSubOK {
		matchedAccName = subscriptionAccountName(viaSub, viaSite, viaUID)
		subName = strings.TrimSpace(viaSub.Name)
	}
	viaName := firstNonEmptyStrUI(rawViaName, matchedAccName, subName, viaUID)

	avatarSite, avatarUID, avatarMid := viaSite, viaUID, viaMid
	if viaSubOK && !a.hasLocalAccountAvatar(avatarSite, avatarUID) {
		pSite, pUID := subscriptionPrimarySiteAndUID(viaSub)
		if pUID != "" && (normalizeSiteIDUI(pSite) != normalizeSiteIDUI(avatarSite) || pUID != avatarUID) && a.hasLocalAccountAvatar(pSite, pUID) {
			avatarSite = pSite
			avatarUID = pUID
			avatarMid = parseBilibiliMidUI(pSite, pUID)
		}
	}

	return postCardAvatarSpec{
		site:     avatarSite,
		uid:      avatarUID,
		mid:      avatarMid,
		name:     viaName,
		face:     "",
		isCollab: true,
		viaSite:  viaSite,
		viaUID:   viaUID,
		viaName:  viaName,
	}
}

// homePostSubscription is the subscription a Home post belongs to: for a
// co-creation cached for a subscribed account (via collab) or a cross-account
// forward (e.g. a Weibo fast-retweet), that cached account's subscription is
// resolved first before falling back to the post's author.
func (a *App) homePostSubscription(post PostCardData) (subscription.Subscription, bool) {
	if !post.InHomeFeed || a.CurrentRoute != RouteHome {
		return subscription.Subscription{}, false
	}
	if viaSite, viaUID, viaMid, _, ok := cachedViaAccount(post, true); ok {
		if sub, subOK := a.subscriptionForAuthor(viaSite, viaUID, viaMid); subOK {
			return sub, true
		}
	}
	if sub, ok := a.subscriptionForAuthor(post.Site, post.AuthorUID, post.Mid); ok {
		return sub, true
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

// renderMediaThumbEdge renders a cover/thumbnail decoded to fit maxEdge pixels
// (e.g. avatarThumbEdge = 256 for small 96x58 task cards) instead of full 960px.
func (a *App) renderMediaThumbEdge(
	c *ui.Context,
	itemKey string,
	relPath string,
	maxEdge int,
	title string,
	durationText string,
	dotStatus string,
	showPlayIcon bool,
	width float32,
	height float32,
	remoteURLs ...string,
) ui.Element {
	bmp := a.lookupLocalBitmapEdge(itemKey, relPath, maxEdge)
	return a.renderMediaThumbWithBitmap(c, bmp, itemKey, "", title, durationText, dotStatus, showPlayIcon, width, height, remoteURLs...)
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

// renderExpandableText renders post or video body text with clickable hyperlinks,
// 6-line / 280-char truncation, and an "展开 / 收起" toggle.
func (a *App) renderExpandableText(c *ui.Context, key string, text string) {
	a.renderExpandableTextWithLinks(c, key, text, nil, 15, ColorTextMain, false)
}

func (a *App) renderExpandableTextStyled(c *ui.Context, key string, text string, fontSize float32, textColor ui.Color) {
	a.renderExpandableTextWithLinks(c, key, text, nil, fontSize, textColor, false)
}

func (a *App) renderExpandableTextWithLinks(
	c *ui.Context,
	key string,
	text string,
	links []service.TextLinkDTO,
	fontSize float32,
	textColor ui.Color,
	highlightHashtags bool,
) {
	a.renderRichText(c, text, links, richTextOptions{
		ClampKey:          key,
		FontSize:          fontSize,
		LineHeight:        LineHeightBody,
		TextColor:         textColor,
		HighlightHashtags: highlightHashtags,
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
	} else if img.IsGIF {
		ui.Box(c).Absolute().Left(6).Top(6).Padding(1, 6).Radius(4).Background(ui.RGBA(0, 0, 0, 0.55)).Children(func() {
			ui.Text(c, "GIF").FontSize(11).FontWeight(600).TextColor(ColorWhite).SingleLine()
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
		// On Home the avatar opens the author's subscription (or the cached
		// via account's subscription for collab/cross-account posts); the user
		// page (a parse) only opens for an author without one.
		homeSub, homeSubOK := a.homePostSubscription(post)
		avatar := a.resolvePostCardAvatar(post, homeSub, homeSubOK)
		if homeSubOK {
			sub := homeSub
			openCreator = func() { a.OpenSubscriptionDetail(sub) }
		} else if !post.Offline {
			openCreator = func() {
				switch normalizeSiteIDUI(post.Site) {
				case "douyin":
					if post.AuthorUID != "" {
						a.OpenDouyinUser(post.AuthorUID, post.AuthorName)
					}
					return
				case "weibo":
					if post.AuthorUID != "" {
						a.OpenWeiboUser(post.AuthorUID, post.AuthorName)
					}
					return
				case "x":
					target := firstNonEmptyStrUI(post.AuthorUID, post.AuthorHandle)
					if target != "" {
						a.OpenXUser(target, post.AuthorName)
					}
					return
				}
				a.OpenUser(post.Mid, post.AuthorName)
			}
		}
		openAvatar := openCreator
		if avatar.isCollab {
			if homeSubOK {
				sub := homeSub
				openAvatar = func() { a.OpenSubscriptionDetail(sub) }
			} else if !post.Offline && a.CurrentRoute != RouteSubscriptions {
				viaSite, viaUID, viaName := avatar.viaSite, avatar.viaUID, avatar.viaName
				openAvatar = func() { a.openAccountUserPage(viaSite, viaUID, viaName) }
			} else {
				openAvatar = nil
			}
		}
		// Left: 40px circular creator avatar (shows the cached via account's
		// avatar for collab posts on Home / Subscription Detail › 内容).
		ui.Box(c).Label("作者头像：" + post.Key).Shrink(0).Children(func() {
			a.renderAccountAvatar(c, avatar.site, avatar.name, avatar.uid, avatar.mid, 40, false, "", openAvatar, avatar.face)
		})

		// Right: Main post column
		ui.Column(c).Grow(1).Gap(6).ClipX().Children(func() {
			// 1. Header line: Author · Site (on Home) · Relative Time · Type · Pinned · StatusDot
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(6).ClipX().Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(6).Grow(1).ClipX().Children(func() {
					authorName := post.AuthorName
					if authorName == "" {
						switch normalizeSiteIDUI(post.Site) {
						case "douyin":
							authorName = "未知作者"
						case "weibo":
							authorName = "未知博主"
						case "x":
							authorName = "未知用户"
						default:
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
					if normalizeSiteIDUI(post.Site) == "x" {
						if h := strings.TrimPrefix(strings.TrimSpace(post.AuthorHandle), "@"); h != "" {
							ui.Text(c, "@"+h).FontSize(13).TextColor(ColorTextMuted).SingleLine().Shrink(1)
						}
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
						if post.OnOpenPost != nil {
							openBtn := ui.ButtonBase(c).Label(firstNonEmptyStrUI(post.OpenBtnLabel, "打开帖子："+post.Key)).Padding(0).Tooltip(exact).Children(func() {
								ui.Text(c, "· "+rel).FontSize(13).TextColor(ColorTextMuted).SingleLine()
							})
							if openBtn.Clicked() {
								post.OnOpenPost()
							}
						} else {
							ui.Text(c, "· "+rel).FontSize(13).TextColor(ColorTextMuted).Tooltip(exact).SingleLine()
						}
					} else if post.OnOpenPost != nil {
						openBtn := ui.ButtonBase(c).Label(firstNonEmptyStrUI(post.OpenBtnLabel, "打开帖子："+post.Key)).Padding(0).Children(func() {
							ui.Text(c, "· 详情").FontSize(13).TextColor(ColorInfoBlue).SingleLine()
						})
						if openBtn.Clicked() {
							post.OnOpenPost()
						}
					}
					if post.TypeLabel != "" {
						ui.Text(c, "· "+post.TypeLabel).FontSize(13).TextColor(ColorTextMuted).SingleLine()
					}
					if post.VisibleLabel != "" {
						ui.Box(c).Padding(1, 6).Radius(4).Background(ColorBgSecondary).Children(func() {
							ui.Text(c, post.VisibleLabel).FontSize(11).TextColor(ColorTextMuted).SingleLine()
						})
					}
					if post.IsEdited {
						ui.Box(c).Padding(1, 6).Radius(4).Background(ColorBgSecondary).Children(func() {
							ui.Text(c, "已编辑").FontSize(11).TextColor(ColorTextMuted).SingleLine()
						})
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
			isDouyinSite := normalizeSiteIDUI(post.Site) == "douyin"
			titleText := post.Title
			bodyText := post.Text
			if titleText != "" && titleText == bodyText && post.Kind == "dynamic" {
				if _, titleHasRich := parseRichTextParagraphs(titleText, post.Links, isDouyinSite); titleHasRich {
					titleText = ""
				}
			}
			if titleText != "" && (post.Video == nil || titleText != post.Video.Title) {
				if bodyText == "" {
					if _, titleHasRich := parseRichTextParagraphs(titleText, post.Links, isDouyinSite); titleHasRich {
						a.renderRichText(c, titleText, post.Links, richTextOptions{
							MaxLines:          2,
							FontSize:          15,
							LineHeight:        LineHeightCompact,
							TextColor:         ColorTextMain,
							HighlightHashtags: isDouyinSite,
						})
					} else {
						ui.Text(c, titleText).FontSize(15).LineHeight(LineHeightCompact).FontWeight(600).TextColor(ColorTextMain).MaxLines(2)
					}
				} else {
					ui.Text(c, titleText).FontSize(15).LineHeight(LineHeightCompact).FontWeight(600).TextColor(ColorTextMain).MaxLines(2)
				}
			}
			if bodyText != "" && bodyText != titleText {
				a.renderExpandableTextWithLinks(c, post.Key, bodyText, post.Links, 15, ColorTextMain, isDouyinSite)
			}

			if post.Degraded {
				ui.Row(c).FillWidth().Padding(8, 12).Radius(8).Background(ColorBgSecondary).AlignItems(ui.Center).Gap(6).Children(func() {
					DrawIcon(c, "alert-circle", 16, ColorTextMuted, 1.75)
					ui.Text(c, fmt.Sprintf("TRACE 暂不支持完整展示「%s」类型，可在浏览器中打开查看", post.TypeLabel)).
						FontSize(13).LineHeight(LineHeightCompact).TextColor(ColorTextMuted).MaxLines(2)
				})
			}

			// 3. Attached Pages / Image 9-Grid + Douyin Note BGM Bar
			if len(post.NotePages) > 0 {
				a.renderNotePagesGrid(c, post.ItemKey, normalizeSiteIDUI(post.Site), post.NotePages, false)
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

			// 3b. Degraded Weibo Rich Card (article/vote/music/webpage/live — summary + open in browser, NO video cache)
			if post.WeiboCard != nil {
				a.renderWeiboRichCard(c, post.WeiboCard, post.Offline || post.LocalManage)
			}
			// 3c. X Poll / Rich Card / HLS-only Notice / Thread Hint
			if post.XPoll != nil {
				a.renderXPollCard(c, post.XPoll, post.URL, post.Offline || post.LocalManage)
			}
			if post.XCard != nil {
				a.renderXRichCard(c, post.XCard, post.URL, post.Offline || post.LocalManage)
			}
			if post.HLSOnlyNotice != "" {
				a.renderXHLSOnlyNotice(c, post.HLSOnlyNotice, post.URL, post.Offline || post.LocalManage)
			}
			if post.ThreadCount > 0 {
				threadHint := fmt.Sprintf("含 %d 条作者自回复串", post.ThreadCount)
				if post.OnOpenPost != nil && !post.Offline {
					tBtn := ui.ButtonBase(c).Padding(2, 0).Justify(ui.Start).Children(func() {
						ui.Text(c, threadHint+" · 查看完整串").FontSize(12).TextColor(ColorInfoBlue).SingleLine()
					})
					if tBtn.Clicked() {
						post.OnOpenPost()
					}
				} else {
					ui.Text(c, threadHint).FontSize(12).TextColor(ColorTextMuted).SingleLine()
				}
			}

			// 4. Attached Video Card (16:9 horizontal for Bilibili/live/Weibo, 3:4 vertical cover on left for Douyin video)
			if post.Video != nil {
				a.renderPostVideoCard(c, post, post.Video, false)
			}

			// 5. Forwarded Origin Quote Box
			if post.Origin != nil && !post.Origin.Unavailable {
				orig := post.Origin
				origSite := normalizeSiteIDUI(firstNonEmptyStrUI(orig.Site, post.Site))
				origOffline := post.Offline || orig.Offline || post.LocalManage
				origFace := orig.AuthorFace
				if origOffline {
					origFace = ""
				}
				origAuthorName := firstNonEmptyStrUI(orig.AuthorName, "原博主")
				if origSite == "x" {
					origAuthorName = firstNonEmptyStrUI(orig.AuthorName, orig.AuthorHandle, "原帖作者")
				}
				origItemKey := firstNonEmptyStrUI(orig.ItemKey, post.ItemKey)
				var openOrigAuthor func()
				if !origOffline && origSite == "weibo" && strings.TrimSpace(orig.AuthorUID) != "" {
					origUID := strings.TrimSpace(orig.AuthorUID)
					openOrigAuthor = func() { a.OpenWeiboUser(origUID, origAuthorName) }
				} else if !origOffline && origSite == "x" && (strings.TrimSpace(orig.AuthorUID) != "" || strings.TrimSpace(orig.AuthorHandle) != "") {
					origTarget := firstNonEmptyStrUI(orig.AuthorUID, orig.AuthorHandle)
					openOrigAuthor = func() { a.OpenXUser(origTarget, origAuthorName) }
				}

				box := ui.Column(c).
					FillWidth().
					Padding(10, 12).
					Radius(12).
					Background(ColorBgMain).
					Border(1, ColorBorder).
					Gap(6).
					ClipX()
				if post.Key != "" {
					box.Label("转发原内容：" + post.Key)
				}
				box.Children(func() {
					ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
						a.renderAccountAvatar(c, origSite, origAuthorName, orig.AuthorUID, orig.Mid, 20, false, "", openOrigAuthor, origFace)
						if openOrigAuthor != nil {
							btn := ui.ButtonBase(c).Padding(0).Children(func() {
								display := "@" + origAuthorName
								if origSite == "x" {
									display = origAuthorName
								}
								ui.Text(c, display).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
							})
							if btn.Clicked() {
								openOrigAuthor()
							}
						} else {
							display := "@" + origAuthorName
							if origSite == "x" {
								display = origAuthorName
							}
							ui.Text(c, display).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
						}
						if origSite == "x" {
							if h := strings.TrimPrefix(strings.TrimSpace(orig.AuthorHandle), "@"); h != "" {
								ui.Text(c, "@"+h).FontSize(12).TextColor(ColorTextMuted).SingleLine().Shrink(1)
							}
						}
						if orig.TypeLabel != "" {
							ui.Text(c, "· "+orig.TypeLabel).FontSize(12).TextColor(ColorTextMuted).SingleLine()
						}
						if orig.PubTs > 0 {
							ui.Text(c, "· "+FormatRelativeTime(orig.PubTs)).
								FontSize(12).
								TextColor(ColorTextMuted).
								Tooltip(FormatTimestamp(orig.PubTs)).
								SingleLine()
						}
					})
					if orig.Title != "" && (orig.Video == nil || orig.Title != orig.Video.Title) {
						ui.Text(c, orig.Title).FontSize(13).LineHeight(LineHeightCompact).FontWeight(600).TextColor(ColorTextMain).MaxLines(2)
					}
					if orig.Text != "" && orig.Text != orig.Title {
						a.renderExpandableTextWithLinks(c, post.Key+":orig", orig.Text, orig.Links, 14, ColorTextMain, origSite == "douyin")
					}
					if len(orig.NotePages) > 0 {
						a.renderNotePagesGrid(c, origItemKey, origSite, orig.NotePages, false)
					} else if len(orig.Images) > 0 {
						a.renderPostImageGrid(c, PostCardData{
							ItemKey: origItemKey,
							Site:    origSite,
							Offline: origOffline,
							Images:  orig.Images,
						})
					}
					if orig.Video != nil {
						a.renderPostVideoCard(c, post, orig.Video, true)
					}
					if orig.WeiboCard != nil {
						a.renderWeiboRichCard(c, orig.WeiboCard, origOffline)
					}
					if orig.XPoll != nil {
						a.renderXPollCard(c, orig.XPoll, orig.URL, origOffline)
					}
					if orig.XCard != nil {
						a.renderXRichCard(c, orig.XCard, orig.URL, origOffline)
					}
					if orig.HLSOnlyNotice != "" {
						a.renderXHLSOnlyNotice(c, orig.HLSOnlyNotice, orig.URL, origOffline)
					}
					if orig.Incomplete && !post.Offline {
						hintText := "原微博含更多文字或媒体，点开查看完整内容"
						if post.OnOpenPost != nil {
							hintBtn := ui.ButtonBase(c).Label("原微博未完整提示："+post.Key).Padding(2, 0).Justify(ui.Start).AlignItems(ui.Start).Children(func() {
								ui.Text(c, hintText).FontSize(12).LineHeight(LineHeightCompact).TextColor(ColorInfoBlue).MaxLines(2)
							})
							if hintBtn.Clicked() {
								post.OnOpenPost()
							}
						} else {
							ui.Box(c).Label("原微博未完整提示：" + post.Key).FillWidth().Children(func() {
								ui.Text(c, hintText).FontSize(12).LineHeight(LineHeightCompact).TextColor(ColorTextMuted).MaxLines(2)
							})
						}
					}
				})
			} else if unavailableText := func() string {
				if post.Origin != nil && post.Origin.Unavailable {
					fallback := "原微博已删除或不可查看"
					if normalizeSiteIDUI(post.Site) == "x" {
						fallback = "原帖已删除或不可查看"
					}
					return firstNonEmptyStrUI(post.Origin.UnavailableReason, post.OriginUnavailable, fallback)
				}
				return strings.TrimSpace(post.OriginUnavailable)
			}(); unavailableText != "" {
				notice := ui.Row(c).
					FillWidth().
					Padding(10, 12).
					Radius(12).
					Background(ColorBgSecondary).
					Border(1, ColorBorder).
					AlignItems(ui.Center).
					Gap(6).
					ClipX()
				if post.Key != "" {
					notice.Label("转发原内容不可用：" + post.Key)
				}
				notice.Children(func() {
					DrawIcon(c, "alert-circle", 15, ColorTextMuted, 1.75)
					ui.Text(c, unavailableText).FontSize(13).LineHeight(LineHeightCompact).TextColor(ColorTextMuted).MaxLines(2)
				})
			}

		})
	})
}

func (a *App) renderPostVideoCard(c *ui.Context, post PostCardData, v *PostVideo, isOrigin bool) {
	if v == nil {
		return
	}
	isDouyinVideo := !isOrigin && normalizeSiteIDUI(post.Site) == "douyin" && post.Kind == "video"
	coverW := float32(videoCoverWidth)
	coverH := float32(videoCoverWidth * 9 / 16)
	if isDouyinVideo {
		coverW = 108
		coverH = 144
	}
	labelPrefix := "播放视频卡片："
	if isOrigin {
		labelPrefix = "播放原视频卡片："
	}
	vCard := ui.ButtonBase(c).
		Label(labelPrefix+firstNonEmptyStrUI(v.ItemKey, v.BVID, post.Key)).
		FillWidth().
		Radius(10).
		Border(1, ColorBorder).
		Background(ColorBgMain).
		Clip()
	if vCard.Hovered() {
		vCard.Background(ColorHover)
	}
	coverURL := v.CoverURL
	if post.Offline || post.LocalManage {
		coverURL = ""
	}
	vCard.Children(func() {
		ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Start).ClipX().Children(func() {
			// No title on the placeholder: it is beside the cover.
			a.renderMediaThumb(c, v.ItemKey, v.RelCover, "", v.Duration, "", v.Cached, coverW, coverH, coverURL)
			ui.Column(c).Grow(1).Basis(0).Padding(10, 12, 10, 0).Gap(4).ClipX().Children(func() {
				ui.Text(c, v.Title).FontSize(14).LineHeight(LineHeightCompact).FontWeight(600).TextColor(ColorTextMain).MaxLines(2)
				if desc := strings.TrimSpace(v.Desc); desc != "" && desc != "-" {
					ui.Text(c, desc).FontSize(13).LineHeight(LineHeightCompact).TextColor(ColorTextMuted).MaxLines(2)
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
			} else if normalizeSiteIDUI(post.Site) == "weibo" {
				if post.OnOpenPost != nil {
					post.OnOpenPost()
				} else if post.WeiboMid != "" {
					a.OpenWeiboPost(post.WeiboMid, v.Title, post.AuthorName, post.AuthorUID, v.CoverURL, post.URL)
				} else if isOrigin && post.Origin != nil && post.Origin.WeiboMid != "" {
					a.OpenWeiboPost(post.Origin.WeiboMid, v.Title, post.Origin.AuthorName, post.Origin.AuthorUID, v.CoverURL, post.Origin.URL)
				} else if v.BVID != "" {
					a.OpenWeiboVideo(v.BVID, v.Title, post.AuthorName, post.AuthorUID, v.CoverURL, v.Duration)
				}
			} else if normalizeSiteIDUI(post.Site) == "x" {
				if post.OnOpenPost != nil {
					post.OnOpenPost()
				} else if post.XStatusID != "" {
					a.OpenXPost(post.XStatusID, v.Title, post.AuthorName, post.AuthorUID, v.CoverURL, post.URL)
				} else if isOrigin && post.Origin != nil && post.Origin.XStatusID != "" {
					a.OpenXPost(post.Origin.XStatusID, v.Title, post.Origin.AuthorName, post.Origin.AuthorUID, v.CoverURL, post.Origin.URL)
				}
			} else if a.CurrentRoute == RouteUser && a.CurrentUser != nil {
				a.openVideoFor(v.BVID, v.Title, post.AuthorName, post.Mid, v.CoverURL, v.Duration, a.CurrentUser.Mid)
			} else {
				a.OpenVideo(v.BVID, v.Title, post.AuthorName, post.Mid, v.CoverURL, v.Duration)
			}
		}
	}
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
		switch normalizeSiteIDUI(post.Site) {
		case "douyin":
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
		case "weibo":
			if post.Kind == "video" {
				targetURL = "https://weibo.com/tv/show/" + post.RawID
			} else {
				targetURL = "https://weibo.com/detail/" + post.RawID
			}
		case "x":
			targetURL = "https://x.com/i/web/status/" + post.RawID
		default:
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
// a Weibo post/video, a Bilibili dynamic with its linked videos, or a Bilibili video.
func (a *App) cachePost(post PostCardData) {
	switch normalizeSiteIDUI(post.Site) {
	case "douyin":
		if post.RawID != "" {
			a.cacheDouyinWorkNow(post.RawID)
		}
		return
	case "weibo":
		if post.RawID != "" {
			if post.Kind == "video" {
				a.cacheWeiboVideoNow(post.RawID)
			} else {
				a.cacheWeiboPostNow(post.RawID, post.WeiboCacheVia)
			}
		}
		return
	case "x":
		if post.RawID != "" {
			a.cacheXPostNow(post.RawID, post.XCacheVia)
		}
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
