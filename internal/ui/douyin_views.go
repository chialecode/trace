package ui

import (
	"context"
	"fmt"
	"hash/fnv"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/player"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

// douyinNoteRenderPage normalizes an online or locally cached Douyin note page for grid and viewer rendering.
type douyinNoteRenderPage struct {
	Index         int
	Kind          string // "image", "live_photo", "video", "unknown"
	ImageURL      string
	ImageRel      string
	MotionURL     string
	MotionRel     string
	VideoURL      string
	VideoRel      string
	Width         int
	Height        int
	DurationSec   int
	UnknownReason string
}

func normalizeSiteIDUI(v string) string {
	if strings.EqualFold(strings.TrimSpace(v), "douyin") {
		return "douyin"
	}
	return "bilibili"
}

func subscriptionPrimarySiteAndUID(sub subscription.Subscription) (string, string) {
	if strings.TrimSpace(sub.Avatar.UID) != "" {
		return normalizeSiteIDUI(sub.Avatar.Site), strings.TrimSpace(sub.Avatar.UID)
	}
	if len(sub.Accounts) > 0 && strings.TrimSpace(sub.Accounts[0].UID) != "" {
		return normalizeSiteIDUI(sub.Accounts[0].Site), strings.TrimSpace(sub.Accounts[0].UID)
	}
	if sub.Mid > 0 {
		return "bilibili", fmt.Sprintf("%d", sub.Mid)
	}
	return normalizeSiteIDUI(sub.Site), ""
}

func subscriptionPrimaryUID(sub subscription.Subscription) string {
	_, uid := subscriptionPrimarySiteAndUID(sub)
	return uid
}

// renderSiteSmallBadge renders the secondary-color (#737373) 12px plain-text site mark ("B 站" / "抖音")
// with no icon, no background, and no brand color.
func renderSiteSmallBadge(c *ui.Context, siteID, accessLabel string) ui.Element {
	norm := normalizeSiteIDUI(siteID)
	label := site.DisplayName(norm)
	if label == "" {
		label = "B 站"
	}
	t := ui.Text(c, label).FontSize(12).TextColor(ColorTextMuted).SingleLine().Shrink(0)
	if accessLabel != "" {
		t.Label(accessLabel)
	}
	return t
}

func renderSiteSmallBadgeWithDot(c *ui.Context, siteID, accessLabel string) {
	ui.Text(c, "·").FontSize(12).TextColor(ColorTextMuted).SingleLine().Shrink(0)
	renderSiteSmallBadge(c, siteID, accessLabel)
}

// renderThreeSegmentSiteFilter renders the 3-segment "全部 / B 站 / 抖音" switcher.
func renderThreeSegmentSiteFilter(c *ui.Context, labelPrefix, current string, onSelect func(string)) {
	cur := normalizeSiteFilter(current)
	opts := []struct {
		Key  string
		Text string
	}{
		{"all", "全部"},
		{"bilibili", "B 站"},
		{"douyin", "抖音"},
	}
	ui.Row(c).
		FillWidth().
		Padding(2).
		Radius(8).
		Background(ColorBgMain).
		Border(1, ColorBorder).
		Gap(2).
		Children(func() {
			for _, opt := range opts {
				o := opt
				active := cur == o.Key
				btn := ui.ButtonBase(c).
					Label(labelPrefix+o.Text).
					Grow(1).
					Basis(0).
					Padding(5, 8).
					Radius(6).
					Justify(ui.Center).
					AlignItems(ui.Center)
				if active {
					btn.Background(ColorBgSecondary)
				} else if btn.Hovered() {
					btn.Background(ColorHover)
				}
				btn.Children(func() {
					col := ColorTextMuted
					weight := 400
					if active {
						col = ColorTextMain
						weight = 600
					}
					ui.Text(c, o.Text).FontSize(12).FontWeight(weight).TextColor(col).SingleLine()
				})
				if btn.Clicked() && !active && onSelect != nil {
					onSelect(o.Key)
				}
			}
		})
}

// renderTwoSegmentLoginSiteSwitcher renders the top "哔哩哔哩 / 抖音" switcher on /login.
func renderTwoSegmentLoginSiteSwitcher(c *ui.Context, current string, onSelect func(string)) {
	cur := normalizeSiteIDUI(current)
	opts := []struct {
		Key  string
		Text string
	}{
		{"bilibili", "哔哩哔哩"},
		{"douyin", "抖音"},
	}
	ui.Row(c).
		FillWidth().
		Padding(3).
		Radius(10).
		Background(ColorBgMain).
		Border(1, ColorBorder).
		Gap(4).
		Children(func() {
			for _, opt := range opts {
				o := opt
				active := cur == o.Key
				btn := ui.ButtonBase(c).
					Label("登录站点："+o.Text).
					Grow(1).
					Basis(0).
					Padding(7, 12).
					Radius(8).
					Justify(ui.Center).
					AlignItems(ui.Center)
				if active {
					btn.Background(ColorAccentBg)
				} else if btn.Hovered() {
					btn.Background(ColorHover)
				}
				btn.Children(func() {
					col := ColorTextMuted
					weight := 500
					if active {
						col = ColorAccent
						weight = 600
					}
					ui.Text(c, o.Text).FontSize(14).FontWeight(weight).TextColor(col).SingleLine()
				})
				if btn.Clicked() && !active && onSelect != nil {
					onSelect(o.Key)
				}
			}
		})
}

func accountSeedHash(siteID, uid string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(strings.ToLower(strings.TrimSpace(siteID))))
	_, _ = h.Write([]byte(":"))
	_, _ = h.Write([]byte(strings.TrimSpace(uid)))
	v := int64(h.Sum64() & 0x7fffffffffffffff)
	if v == 0 {
		return 1
	}
	return v
}

func (a *App) renderAccountAvatar(c *ui.Context, siteID, name, uid string, mid int64, size float32, liveRing bool, label string, onClick func(), faces ...string) ui.Element {
	normSite := normalizeSiteIDUI(siteID)
	seed := mid
	if seed <= 0 {
		seed = accountSeedHash(normSite, firstNonEmptyStrUI(uid, name))
	}
	face := firstImageURL(faces)
	if face == "" {
		if normSite == "bilibili" && mid > 0 {
			face = a.faceURL(mid)
		} else {
			face = a.faceURLForAccount(normSite, uid)
		}
	}
	return a.renderAvatarWithBitmap(c, name, seed, size, liveRing, label, onClick, nil, face)
}

func formatDouyinHandle(uniqueID, shortID, uid string) string {
	h := firstNonEmptyStrUI(uniqueID, shortID, uid)
	if h == "" {
		return ""
	}
	return "抖音号：" + h
}

func formatDouyinVideoSpec(info *library.DouyinVideoInfo, onlineQuality, onlineCodec string) string {
	var q, c string
	if info != nil {
		q = firstNonEmptyStrUI(info.QualityName, info.Gear)
		c = strings.ToUpper(strings.TrimSpace(info.Codec))
	}
	if q == "" {
		q = strings.TrimSpace(onlineQuality)
	}
	if c == "" {
		c = strings.ToUpper(strings.TrimSpace(onlineCodec))
	}
	switch c {
	case "H264":
		c = "AVC"
	case "H265", "BYTEVC1":
		c = "HEVC"
	case "BYTEVC2":
		c = "AV1"
	}
	var parts []string
	if q != "" {
		parts = append(parts, q)
	}
	if c != "" {
		parts = append(parts, c)
	}
	return strings.Join(parts, " · ")
}

func formatLiveAccountQualitySpec(st service.LiveAccountState) string {
	if st.AudioOnly {
		return "纯音频"
	}
	return bilibili.FormatLiveQualitySpec(st.QualityName, 0, st.Codec)
}

func splitDouyinHeadlineAndBody(title, desc string, hashtags []string) (string, string) {
	raw := strings.TrimSpace(desc)
	if raw == "" {
		raw = strings.TrimSpace(title)
	}
	var headline, rest string
	if idx := strings.IndexByte(raw, '\n'); idx >= 0 {
		headline = strings.TrimSpace(raw[:idx])
		rest = strings.TrimSpace(raw[idx+1:])
	} else {
		headline = raw
	}
	if headline == "" {
		headline = firstNonEmptyStrUI(title, "无标题作品")
	}
	// Ensure hashtags returned separately are present in the displayed text if missing from raw caption.
	combined := headline + "\n" + rest
	var extraTags []string
	for _, tag := range hashtags {
		clean := strings.TrimPrefix(strings.TrimSpace(tag), "#")
		if clean == "" {
			continue
		}
		token := "#" + clean
		if !strings.Contains(combined, token) {
			extraTags = append(extraTags, token)
		}
	}
	if len(extraTags) > 0 {
		if rest != "" {
			rest += " " + strings.Join(extraTags, " ")
		} else {
			rest = strings.Join(extraTags, " ")
		}
	}
	return headline, rest
}

type richTextSpan struct {
	Text    string
	Hashtag bool
}

func splitHashtagSpans(text string) []richTextSpan {
	var spans []richTextSpan
	var buf strings.Builder
	runes := []rune(text)
	flushPlain := func() {
		if buf.Len() > 0 {
			spans = append(spans, richTextSpan{Text: buf.String()})
			buf.Reset()
		}
	}
	for i := 0; i < len(runes); {
		if runes[i] == '#' {
			j := i + 1
			for j < len(runes) {
				r := runes[j]
				if unicode.IsSpace(r) || r == '#' || r == '，' || r == '。' || r == '！' || r == '？' || r == '；' || r == ',' || r == '.' || r == '!' || r == '?' || r == ';' {
					break
				}
				j++
			}
			if j > i+1 {
				flushPlain()
				tagText := string(runes[i:j])
				if j < len(runes) && runes[j] == '#' {
					tagText += "#"
					j++
				}
				spans = append(spans, richTextSpan{Text: tagText, Hashtag: true})
				i = j
				continue
			}
		}
		buf.WriteRune(runes[i])
		i++
	}
	flushPlain()
	return spans
}

// renderRichHashtagText renders body text with #话题 highlighted in ColorAccent and 6-line / 280-char clamping.
func (a *App) renderRichHashtagText(c *ui.Context, key, text string) {
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
	shown := text
	if needsClamp && !expanded {
		allLines := strings.Split(shown, "\n")
		if len(allLines) > 6 {
			shown = strings.Join(allLines[:6], "\n")
		}
		if utf8.RuneCountInString(shown) > 280 {
			r := []rune(shown)
			shown = string(r[:280]) + "…"
		}
	}

	ui.Column(c).FillWidth().Gap(4).ClipX().Children(func() {
		for _, para := range strings.Split(shown, "\n") {
			line := strings.TrimSpace(para)
			if line == "" {
				continue
			}
			spans := splitHashtagSpans(line)
			hasTag := false
			for _, sp := range spans {
				if sp.Hashtag {
					hasTag = true
					break
				}
			}
			if !hasTag {
				ui.Text(c, line).FontSize(15).TextColor(ColorTextMain)
				continue
			}
			ui.Row(c).FillWidth().Wrap().Gap(2).AlignItems(ui.Center).Children(func() {
				for _, sp := range spans {
					s := sp
					if s.Hashtag {
						ui.Text(c, s.Text).FontSize(15).FontWeight(600).TextColor(ColorAccent)
					} else {
						ui.Text(c, s.Text).FontSize(15).TextColor(ColorTextMain)
					}
				}
			})
		}
		if needsClamp {
			toggleText := "展开"
			if expanded {
				toggleText = "收起"
			}
			btn := ui.ButtonBase(c).Label("正文展开收起："+key).Padding(2, 0).Children(func() {
				ui.Text(c, toggleText).FontSize(13).FontWeight(600).TextColor(ColorInfoBlue).SingleLine()
			})
			if btn.Clicked() {
				a.ExpandedPosts[key] = !expanded
			}
		}
	})
}

// ============================================================================
// Douyin Note BGM Bar & Companion Viewer Inline Playback (docs/ui.md §3.4, §3.5)
// ============================================================================

func playerLoaded(ctrl *player.Controller) bool {
	return ctrl != nil && ctrl.Snapshot(1, 1).Open
}

func playerPlaying(ctrl *player.Controller) bool {
	if ctrl == nil {
		return false
	}
	snap := ctrl.Snapshot(1, 1)
	return snap.Open && snap.Playing
}

func (a *App) pauseBGM() {
	if playerPlaying(a.bgmPlayer) {
		a.bgmPlayer.Pause()
	}
}

func (a *App) stopBGM() {
	a.bgmOpenSeq++
	if a.bgmPlayer != nil {
		_ = a.bgmPlayer.Close()
	}
	a.BGMActiveKey = ""
	a.BGMTitle = ""
	a.BGMAuthor = ""
	a.BGMPath = ""
	a.BGMDurationSec = 0
	a.BGMLoading = false
	a.BGMError = ""
	a.BGMSeekNormalized = 0
}

func (a *App) toggleBGM(key, title, author string, durationSec int, localItemKey, localRelFile, onlinePlayURL string) {
	if key == "" {
		return
	}
	if a.BGMActiveKey == key && playerLoaded(a.bgmPlayer) {
		if !playerPlaying(a.bgmPlayer) {
			if a.ImageViewer.Open && a.ImageViewer.InlineActive {
				a.stopViewerInlinePlayback()
			}
			if a.PlayerOverlay.Open && playerPlaying(a.player) {
				a.player.Pause()
			}
			a.bgmPlayer.Play()
		} else {
			a.bgmPlayer.Pause()
		}
		return
	}

	a.stopBGM()
	if a.ImageViewer.Open && a.ImageViewer.InlineActive {
		a.stopViewerInlinePlayback()
	}
	if a.PlayerOverlay.Open && playerPlaying(a.player) {
		a.player.Pause()
	}

	a.bgmOpenSeq++
	seq := a.bgmOpenSeq
	a.BGMActiveKey = key
	a.BGMTitle = title
	a.BGMAuthor = author
	a.BGMDurationSec = durationSec
	a.BGMLoading = true
	a.BGMError = ""
	a.BGMSeekNormalized = 0

	backend := a.backend
	stale := func() bool {
		return a.bgmOpenSeq != seq || a.BGMActiveKey != key
	}

	type bgmOpened struct {
		path string
		dec  player.MediaDecoder
	}
	requestGuarded(a, fmt.Sprintf("bgm-open-%d", seq), nil, func(ctx context.Context) (bgmOpened, error) {
		var mediaPath string
		if localItemKey != "" && localRelFile != "" {
			p, err := backend.MediaPath(localItemKey, localRelFile)
			if err != nil {
				return bgmOpened{}, fmt.Errorf("配乐文件缺失或不可访问")
			}
			mediaPath = p
		} else if strings.TrimSpace(onlinePlayURL) != "" {
			ext := strings.ToLower(filepath.Ext(strings.Split(onlinePlayURL, "?")[0]))
			if ext == "" || len(ext) > 6 {
				ext = ".mp3"
			}
			p, err := backend.StagePreviewMedia(ctx, "douyin", onlinePlayURL, ext)
			if err != nil {
				return bgmOpened{}, err
			}
			mediaPath = p
		} else {
			return bgmOpened{}, fmt.Errorf("当前作品没有可播放的配乐")
		}
		dec, err := player.OpenMediaFile(mediaPath)
		if err != nil {
			return bgmOpened{}, err
		}
		return bgmOpened{path: mediaPath, dec: dec}, nil
	}, func(res bgmOpened) {
		if stale() {
			_ = res.dec.Close()
			return
		}
		a.BGMLoading = false
		a.BGMPath = res.path
		if err := a.bgmPlayer.LoadDecoder(res.path, res.dec); err != nil {
			a.BGMError = "无法播放配乐：" + userErrorText(err)
			return
		}
		a.bgmPlayer.SetVolume(a.PlayerOverlay.Volume)
		a.bgmPlayer.Play()
	}, func(err error) {
		if stale() {
			return
		}
		a.BGMLoading = false
		a.BGMError = userErrorText(err)
	})
}

func formatMusicLine(title, author string, original bool) string {
	t := strings.TrimSpace(title)
	au := strings.TrimSpace(author)
	if t == "" && original {
		t = "原声"
	}
	if t == "" {
		t = "配乐"
	}
	if au != "" && !strings.Contains(t, au) {
		return fmt.Sprintf("♪ %s · %s", t, au)
	}
	return "♪ " + t
}

func (a *App) renderBGMBar(c *ui.Context, key, title, author string, original bool, durationSec int, localItemKey, localRelFile, onlinePlayURL, unavailableReason string) {
	musicLabel := formatMusicLine(title, author, original)
	active := a.BGMActiveKey == key
	loaded := active && playerLoaded(a.bgmPlayer)
	playing := loaded && playerPlaying(a.bgmPlayer)
	hasSource := (localItemKey != "" && localRelFile != "") || strings.TrimSpace(onlinePlayURL) != ""

	var posSec, durSec float64
	durSec = float64(max(0, durationSec))
	if loaded {
		snap := a.bgmPlayer.Snapshot(1, 1)
		posSec = snap.PositionSec
		if snap.DurationSec > 0 {
			durSec = snap.DurationSec
		}
		if durSec > 0 {
			a.BGMSeekNormalized = max(0.0, min(1.0, posSec/durSec))
		}
	}

	ui.Column(c).
		Label("配乐栏："+key).
		FillWidth().
		Padding(10, 12).
		Radius(12).
		Background(ColorBgMain).
		Border(1, ColorBorder).
		Gap(6).
		ClipX().
		Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(10).ClipX().Children(func() {
				btnIcon := "play"
				if playing {
					btnIcon = "pause"
				}
				playBtn := ui.ButtonBase(c).
					Label("配乐播放暂停："+key).
					Size(32, 32).
					Radius(16).
					Shrink(0).
					Justify(ui.Center).
					AlignItems(ui.Center).
					Disabled(!hasSource || (active && a.BGMLoading))
				if hasSource {
					playBtn.Background(ColorAccent)
				} else {
					playBtn.Background(ColorBorder)
				}
				playBtn.Children(func() {
					if active && a.BGMLoading {
						ui.Spinner(c).Size(14, 14)
					} else {
						DrawIcon(c, btnIcon, 15, ColorWhite, 2.0)
					}
				})
				if playBtn.Clicked() && hasSource {
					a.toggleBGM(key, title, author, durationSec, localItemKey, localRelFile, onlinePlayURL)
				}

				ui.Column(c).Grow(1).Gap(4).ClipX().Children(func() {
					ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
						ui.Text(c, musicLabel).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine().Grow(1)
						if hasSource {
							timeText := fmt.Sprintf("%s / %s", FormatDuration(int(posSec)), FormatDuration(int(durSec)))
							ui.Text(c, timeText).FontSize(12).TextColor(ColorTextMuted).SingleLine().Shrink(0)
						}
					})
					if hasSource {
						seekVal := a.BGMSeekNormalized
						if !active {
							seekVal = 0
						}
						if ui.Slider(c, &seekVal, 0.0, 1.0).Label("配乐进度条：" + key).FillWidth().Disabled(!loaded || durSec <= 0).Changed() {
							a.BGMSeekNormalized = seekVal
							if loaded && durSec > 0 {
								_ = a.bgmPlayer.Seek(seekVal * durSec)
							}
						}
					} else {
						reason := firstNonEmptyStrUI(unavailableReason, "该作品配乐不可用")
						ui.Text(c, reason).FontSize(12).TextColor(ColorTextMuted).SingleLine()
					}
				})
			})
			if active && a.BGMError != "" {
				ui.Text(c, a.BGMError).FontSize(12).TextColor(ColorError).MaxLines(2)
			}
		})
}

// stopViewerInlinePlayback stops any inline live-photo motion or video page playing inside ImageViewer.
// By design (docs/ui.md §3.4), stopping inline playback does NOT auto-resume the note's BGM bar.
func (a *App) stopViewerInlinePlayback() {
	a.viewerOpenSeq++
	if a.viewerPlayer != nil {
		_ = a.viewerPlayer.Close()
	}
	a.ImageViewer.InlineActive = false
	a.ImageViewer.InlinePageIndex = -1
	a.ImageViewer.InlineLoading = false
	a.ImageViewer.InlineError = ""
	a.ImageViewer.InlineMediaPath = ""
}

// startViewerInlinePage starts playing a live-photo motion clip or mixed-note video page inline inside ImageViewer.
// Starting inline playback immediately pauses any active note BGM bar.
func (a *App) startViewerInlinePage(pageIndex int) {
	if !a.ImageViewer.Open || pageIndex < 0 {
		return
	}
	var localPath, remoteURL string
	if pageIndex < len(a.ImageViewer.InlineMediaPaths) {
		localPath = a.ImageViewer.InlineMediaPaths[pageIndex]
	}
	if pageIndex < len(a.ImageViewer.InlineMediaURLs) {
		remoteURL = a.ImageViewer.InlineMediaURLs[pageIndex]
	}
	if localPath == "" && remoteURL == "" {
		return
	}

	// Pause note BGM immediately; never auto-resume when inline clip finishes or closes.
	a.pauseBGM()
	a.stopViewerInlinePlayback()

	a.viewerOpenSeq++
	seq := a.viewerOpenSeq
	a.ImageViewer.InlineActive = true
	a.ImageViewer.InlinePageIndex = pageIndex
	a.ImageViewer.InlineLoading = true
	a.ImageViewer.InlineError = ""

	backend := a.backend
	inlineSite := a.ImageViewer.InlineSite
	if inlineSite == "" {
		inlineSite = "douyin"
	}
	stale := func() bool {
		return !a.ImageViewer.Open || a.viewerOpenSeq != seq || a.ImageViewer.Index != pageIndex || !a.ImageViewer.InlineActive
	}

	type inlineOpened struct {
		path string
		dec  player.MediaDecoder
	}
	requestGuarded(a, fmt.Sprintf("viewer-inline-%d", seq), nil, func(ctx context.Context) (inlineOpened, error) {
		mediaPath := localPath
		if mediaPath == "" {
			p, err := backend.StagePreviewMedia(ctx, inlineSite, remoteURL, ".mp4")
			if err != nil {
				return inlineOpened{}, err
			}
			mediaPath = p
		}
		dec, err := player.OpenMediaFile(mediaPath)
		if err != nil {
			return inlineOpened{}, err
		}
		return inlineOpened{path: mediaPath, dec: dec}, nil
	}, func(res inlineOpened) {
		if stale() {
			_ = res.dec.Close()
			return
		}
		a.ImageViewer.InlineLoading = false
		a.ImageViewer.InlineMediaPath = res.path
		if err := a.viewerPlayer.LoadDecoder(res.path, res.dec); err != nil {
			a.ImageViewer.InlineError = "无法播放此页媒体：" + userErrorText(err)
			return
		}
		a.viewerPlayer.SetVolume(a.PlayerOverlay.Volume)
		a.viewerPlayer.Play()
	}, func(err error) {
		if stale() {
			return
		}
		a.ImageViewer.InlineLoading = false
		a.ImageViewer.InlineError = userErrorText(err)
	})
}

func (a *App) toggleViewerInlinePlay() {
	if !a.ImageViewer.Open || !a.ImageViewer.InlineActive || !playerLoaded(a.viewerPlayer) {
		return
	}
	if !playerPlaying(a.viewerPlayer) {
		a.pauseBGM()
	}
	a.viewerPlayer.TogglePlay()
}

func (a *App) openDouyinNoteViewer(itemKey string, pages []douyinNoteRenderPage, index int) {
	if len(pages) == 0 {
		return
	}
	urls := make([]string, len(pages))
	localStills := make([]string, len(pages))
	rels := make([]string, len(pages))
	kinds := make([]string, len(pages))
	inlinePaths := make([]string, len(pages))
	inlineURLs := make([]string, len(pages))
	inlineDurs := make([]int, len(pages))

	for i, pg := range pages {
		urls[i] = pg.ImageURL
		rels[i] = pg.ImageRel
		if itemKey != "" && pg.ImageRel != "" && a.backend != nil {
			if p, err := a.backend.MediaPath(itemKey, pg.ImageRel); err == nil {
				localStills[i] = p
			}
		}
		normKind := pg.Kind
		if normKind == "motion" {
			normKind = "live_photo"
		}
		kinds[i] = normKind
		inlineDurs[i] = pg.DurationSec
		switch normKind {
		case "live_photo":
			if itemKey != "" && pg.MotionRel != "" && a.backend != nil {
				if p, err := a.backend.MediaPath(itemKey, pg.MotionRel); err == nil {
					inlinePaths[i] = p
				}
			}
			inlineURLs[i] = pg.MotionURL
		case "video":
			if itemKey != "" && pg.VideoRel != "" && a.backend != nil {
				if p, err := a.backend.MediaPath(itemKey, pg.VideoRel); err == nil {
					inlinePaths[i] = p
				}
			}
			inlineURLs[i] = pg.VideoURL
		}
	}

	a.OpenImageViewer(urls, localStills, index)
	a.ImageViewer.ItemKey = itemKey
	a.ImageViewer.RelPaths = rels
	a.ImageViewer.InlinePageKinds = kinds
	a.ImageViewer.InlineMediaPaths = inlinePaths
	a.ImageViewer.InlineMediaURLs = inlineURLs
	a.ImageViewer.InlineDurations = inlineDurs
	a.ImageViewer.InlineSite = "douyin"

	idx := a.ImageViewer.Index
	if idx >= 0 && idx < len(kinds) && kinds[idx] == "video" {
		a.startViewerInlinePage(idx)
	}
}

func buildDouyinNoteRenderPages(work *service.DouyinWorkView, cached *library.ItemMetadata) []douyinNoteRenderPage {
	var out []douyinNoteRenderPage
	if cached != nil && len(cached.NotePages) > 0 {
		var onlinePages []service.DouyinMediaPageDTO
		if work != nil {
			onlinePages = work.Pages
		}
		for i, rec := range cached.NotePages {
			kind := rec.Kind
			if kind == "motion" || (kind == "image" && rec.Motion != "") {
				kind = "live_photo"
			}
			pg := douyinNoteRenderPage{
				Index:       rec.Index,
				Kind:        kind,
				ImageRel:    firstNonEmptyStrUI(rec.Image, rec.Cover),
				MotionRel:   rec.Motion,
				VideoRel:    rec.Video,
				Width:       rec.Width,
				Height:      rec.Height,
				DurationSec: rec.Duration,
			}
			if rec.UnknownKind != "" {
				pg.UnknownReason = "暂不支持的页面类型：" + rec.UnknownKind
			}
			if i < len(onlinePages) {
				op := onlinePages[i]
				pg.ImageURL = op.ImageURL
				pg.MotionURL = op.MotionURL
				pg.VideoURL = op.VideoURL
				if pg.Width <= 0 {
					pg.Width = op.Width
				}
				if pg.Height <= 0 {
					pg.Height = op.Height
				}
				if pg.DurationSec <= 0 {
					pg.DurationSec = op.DurationSec
				}
				if pg.UnknownReason == "" {
					pg.UnknownReason = op.UnknownReason
				}
			}
			out = append(out, pg)
		}
		return out
	}

	if work != nil && len(work.Pages) > 0 {
		for _, op := range work.Pages {
			kind := op.Kind
			if kind == "motion" {
				kind = "live_photo"
			}
			out = append(out, douyinNoteRenderPage{
				Index:         op.Index,
				Kind:          kind,
				ImageURL:      op.ImageURL,
				MotionURL:     op.MotionURL,
				VideoURL:      op.VideoURL,
				Width:         op.Width,
				Height:        op.Height,
				DurationSec:   op.DurationSec,
				UnknownReason: op.UnknownReason,
			})
		}
		return out
	}

	if cached != nil {
		idx := 0
		for _, m := range cached.Media {
			if m.Role != "image" {
				continue
			}
			idx++
			kind := "image"
			if m.Motion != "" {
				kind = "live_photo"
			}
			out = append(out, douyinNoteRenderPage{
				Index:     idx,
				Kind:      kind,
				ImageRel:  m.Path,
				MotionRel: m.Motion,
				Width:     m.Width,
				Height:    m.Height,
			})
		}
	}
	return out
}

func (a *App) renderDouyinNotePagesGrid(c *ui.Context, itemKey string, pages []douyinNoteRenderPage) {
	n := len(pages)
	if n == 0 {
		return
	}
	pageBitmap := func(pg douyinNoteRenderPage) *ui.Bitmap {
		if bmp := a.lookupLocalBitmap(itemKey, pg.ImageRel); bmp != nil {
			return bmp
		}
		return a.lookupRemoteBitmap(pg.ImageURL)
	}
	renderCell := func(bmp *ui.Bitmap, pg douyinNoteRenderPage, pageNum int) {
		ui.Box(c).Absolute().Left(0).Top(0).Right(0).Bottom(0).Background(ColorBgSecondary).Children(func() {
			if bmp != nil {
				ui.Image(c, bmp).Absolute().Left(0).Top(0).Right(0).Bottom(0).Fit(ui.Cover)
			}
		})
		switch pg.Kind {
		case "live_photo":
			ui.Box(c).Label(fmt.Sprintf("实况标记 %d", pageNum)).Absolute().Left(6).Top(6).Padding(1, 6).Radius(4).Background(ui.RGBA(0, 0, 0, 0.58)).Children(func() {
				ui.Text(c, "实况").FontSize(11).FontWeight(600).TextColor(ColorWhite).SingleLine()
			})
		case "video":
			ui.Row(c).Label(fmt.Sprintf("视频页标记 %d", pageNum)).Absolute().Left(0).Top(0).Right(0).Bottom(0).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
				ui.Box(c).Size(38, 38).Radius(19).Background(ui.RGBA(0, 0, 0, 0.58)).Center().Children(func() {
					DrawIcon(c, "play", 18, ColorWhite, 2.0)
				})
			})
			if pg.DurationSec > 0 {
				ui.Box(c).Absolute().Bottom(6).Right(6).Padding(2, 6).Radius(4).Background(ui.RGBA(0, 0, 0, 0.72)).Children(func() {
					ui.Text(c, FormatDuration(pg.DurationSec)).FontSize(11).FontWeight(600).TextColor(ColorWhite).SingleLine()
				})
			}
		case "unknown":
			ui.Box(c).Absolute().Bottom(6).Left(6).Right(6).Padding(2, 6).Radius(4).Background(ui.RGBA(0, 0, 0, 0.65)).Children(func() {
				ui.Text(c, firstNonEmptyStrUI(pg.UnknownReason, "暂不支持此页")).FontSize(11).TextColor(ColorWhite).SingleLine()
			})
		}
	}

	if n == 1 {
		pg := pages[0]
		bmp := pageBitmap(pg)
		ratio := imageRatio(PostImage{Width: pg.Width, Height: pg.Height}, bmp)
		cell := ui.ButtonBase(c).Label("查看图文页 1").Radius(8).Clip().Shrink(0)
		if ratio < 1 {
			cell.Size(singleImageHeight*ratio, singleImageHeight)
		} else {
			cell.FillWidth().MaxWidth(singleImageMaxWidth).AspectRatio(ratio)
		}
		cell.Children(func() { renderCell(bmp, pg, 1) })
		if cell.Clicked() {
			a.openDouyinNoteViewer(itemKey, pages, 0)
		}
		return
	}

	cols, maxW := 3, float32(gridMaxWidth3)
	if n == 2 || n == 4 {
		cols, maxW = 2, float32(gridMaxWidth2)
	}
	shown := min(n, 9)
	extra := n - shown
	ui.Grid(c).Columns(cols).Gap(4).FillWidth().MaxWidth(maxW).Children(func() {
		for idx := range shown {
			pageIdx := idx
			pg := pages[pageIdx]
			cell := ui.ButtonBase(c).Label(fmt.Sprintf("查看图文页 %d", pageIdx+1)).AspectRatio(1).Radius(6).Clip()
			cell.Children(func() {
				renderCell(pageBitmap(pg), pg, pageIdx+1)
				if extra > 0 && pageIdx == shown-1 {
					ui.Row(c).Absolute().Left(0).Top(0).Right(0).Bottom(0).Background(ui.RGBA(0, 0, 0, 0.55)).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
						ui.Text(c, fmt.Sprintf("+%d", extra)).FontSize(18).FontWeight(600).TextColor(ColorWhite).SingleLine()
					})
				}
			})
			if cell.Clicked() {
				a.openDouyinNoteViewer(itemKey, pages, pageIdx)
			}
		}
	})
}

func (a *App) cacheDouyinWorkNow(awemeID string) {
	awemeID = strings.TrimSpace(awemeID)
	if awemeID == "" {
		return
	}
	_, err := a.backend.CacheDouyinWork(context.Background(), awemeID)
	a.showError(err)
	if err == nil {
		a.BannerInfo = "已加入缓存队列：" + awemeID
		a.RefreshData()
	}
}

// ============================================================================
// Route: /douyin/user (Neutral Header + Single 3:4 Works Grid, docs/ui.md §3.4)
// ============================================================================

func douyinUserGridColumns(contentWidth float32) int {
	switch {
	case contentWidth >= 820:
		return 5
	case contentWidth >= 600:
		return 4
	default:
		return 3
	}
}

func (a *App) findDouyinUserLiveState(u *service.DouyinUserView) *service.LiveAccountState {
	if u == nil {
		return nil
	}
	for i := range a.LiveAccounts {
		st := &a.LiveAccounts[i]
		if strings.EqualFold(st.Site, "douyin") && (st.UID == u.SecUID || (u.RoomID != "" && st.RoomID == u.RoomID)) {
			if st.LiveStatus == 1 || st.Recording {
				return st
			}
		}
	}
	if u.LiveRoom != nil && (u.LiveRoom.LiveStatus == 1 || u.LiveRoom.Recording) {
		return u.LiveRoom
	}
	if u.LiveStatus == 1 {
		return &service.LiveAccountState{
			Site:       "douyin",
			AccountKey: "douyin:" + u.SecUID,
			UID:        u.SecUID,
			RoomID:     u.RoomID,
			WebRID:     u.WebRID,
			Uname:      u.Nickname,
			Face:       u.AvatarURL,
			LiveStatus: 1,
			StatusText: "直播中",
		}
	}
	return nil
}

func (a *App) renderDouyinUserRoute(c *ui.Context, centerWidth float32) {
	if a.Opening != nil {
		ui.Scroll(c).Grow(1).FillWidth().ClipX().Children(func() {
			ui.Column(c).FillWidth().Padding(16).Gap(14).ClipX().Children(func() {
				a.renderOpening(c)
			})
		})
		return
	}
	if a.CurrentDouyinUser == nil {
		ui.Column(c).Grow(1).FillWidth().Padding(32).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "未加载抖音作者主页信息").FontSize(15).TextColor(ColorTextMuted).SingleLine()
		})
		return
	}

	u := a.CurrentDouyinUser
	cols := douyinUserGridColumns(a.pageWidth(centerWidth))
	works := a.CurrentDouyinWorks
	if a.UserListFilter == userFilterUncached && len(works) > 0 {
		// 未缓存 filters the loaded pages only (DEC-046 ⑧).
		keys := make([]string, len(works))
		for i, w := range works {
			keys[i] = w.CacheKey
		}
		idx := a.uncachedIndices("douyin:"+u.SecUID, keys)
		kept := make([]service.DouyinWorkCardDTO, 0, len(idx))
		for _, i := range idx {
			kept = append(kept, works[i])
		}
		works = kept
	}
	a.douyinFilteredEmpty = len(works) == 0
	gridRows := 0
	if !u.IsPrivate && len(works) > 0 {
		gridRows = (len(works) + cols - 1) / cols
	}
	// Row 0 = header; Rows 1..gridRows = 3:4 card rows; final row = footer/pagination/auth-expired/retry.
	totalRows := 1 + gridRows + 1

	_, last := a.DouyinUserListState.Visible()
	if !u.IsPrivate && gridRows > 0 && last >= gridRows && a.DouyinWorksHasMore && a.DouyinWorksError == "" && !a.DouyinWorksAuthExpired && !a.pending["douyin-works-"+u.SecUID] {
		a.LoadMoreDouyinWorks()
	}

	var keys []string
	for _, w := range works {
		if w.CacheKey != "" {
			keys = append(keys, w.CacheKey)
		}
	}
	var states map[string]library.DotState
	if len(keys) > 0 {
		states = a.backend.States(keys)
	}

	a.DouyinUserListState.Key = func(i int) any {
		if i == 0 {
			return "douyin-header"
		}
		if i <= gridRows {
			idx := (i - 1) * cols
			if idx < len(works) {
				return "douyin-row-" + works[idx].ID
			}
		}
		return "douyin-footer"
	}

	ui.List(c, &a.DouyinUserListState, totalRows, func(i int) {
		if i == 0 {
			a.renderDouyinUserHeader(c, u)
			return
		}
		if i <= gridRows {
			start := (i - 1) * cols
			end := min(len(works), start+cols)
			rowCards := works[start:end]
			ui.Row(c).FillWidth().Padding(6, 16).Gap(10).AlignItems(ui.Start).ClipX().Children(func() {
				for slot := 0; slot < cols; slot++ {
					if slot < len(rowCards) {
						card := rowCards[slot]
						var dot string
						if states != nil {
							dot = states[card.CacheKey].Dot
						}
						a.renderDouyinWorkGridCard(c, u, card, dot)
					} else {
						ui.Box(c).Grow(1).Basis(0)
					}
				}
			})
			return
		}
		a.renderDouyinUserFooter(c, u)
	}).Label("抖音作品虚拟列表").Grow(1).FillWidth()
}

func (a *App) renderDouyinUserHeader(c *ui.Context, u *service.DouyinUserView) {
	liveState := a.findDouyinUserLiveState(u)
	ui.Column(c).FillWidth().BorderWidth(0, 0, 1, 0).BorderColor(ColorBorder).ClipX().Children(func() {
		// Neutral background header block, never Bilibili's gradient banner (docs/ui.md §3.4).
		ui.Column(c).
			FillWidth().
			Padding(16).
			Background(ColorBgSecondary).
			Gap(10).
			ClipX().
			Children(func() {
				ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(12).ClipX().Children(func() {
					a.renderAccountAvatar(c, "douyin", u.Nickname, u.SecUID, 0, 56, liveState != nil, "抖音作者头像", nil, u.AvatarURL)
					ui.Column(c).Grow(1).Gap(4).ClipX().Children(func() {
						ui.Row(c).AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
							ui.Text(c, firstNonEmptyStrUI(u.Nickname, "抖音作者")).FontSize(18).FontWeight(600).TextColor(ColorTextMain).SingleLine()
							renderSiteSmallBadgeWithDot(c, "douyin", "用户页站点标记")
						})
						if handle := formatDouyinHandle(u.UniqueID, u.ShortID, u.UID); handle != "" {
							ui.Text(c, handle).FontSize(12).TextColor(ColorTextMuted).SingleLine()
						}
					})
					ui.Row(c).Shrink(0).Gap(6).AlignItems(ui.Center).Children(func() {
						ownerSub, owned := a.backend.OwnerOfAccount("douyin", u.SecUID)
						if owned {
							if OutlinePillButton(c, "已订阅", "进入所属订阅").Clicked() {
								a.OpenSubscriptionDetail(ownerSub)
							}
						} else if AccentPillButton(c, "订阅", "订阅该抖音作者").Clicked() {
							acct := subscription.AccountRef{
								Site:     "douyin",
								UID:      u.SecUID,
								UniqueID: firstNonEmptyStrUI(u.UniqueID, u.ShortID),
								WebRID:   u.WebRID,
								Name:     firstNonEmptyStrUI(u.Nickname, u.SecUID),
								Face:     u.AvatarURL,
							}
							_, err := a.backend.CreateSubscription(acct.Name, "", []subscription.AccountRef{acct}, subscription.SubRules{
								ScheduledCheck: true,
							})
							a.showError(err)
							a.RefreshData()
						}
						targetURL := firstNonEmptyStrUI(u.CanonicalURL, "https://www.douyin.com/user/"+u.SecUID)
						MoreMenuButton(c, "抖音作者更多", 32, &a.DouyinUserSubMenuOpen, func(m *ui.Menu) {
							if !u.IsPrivate {
								a.cacheOlderMenu(m, "douyin")
							}
							if m.Item("在浏览器中打开").Chosen() {
								_ = a.backend.Desktop().OpenExternalURL(targetURL)
							}
							if liveState != nil {
								roomOrWebRID := firstNonEmptyStrUI(liveState.WebRID, u.WebRID, liveState.RoomID, u.RoomID)
								if roomOrWebRID != "" && m.Item("在浏览器中观看直播").Chosen() {
									_ = a.backend.OpenLiveInBrowser("douyin", roomOrWebRID)
								}
							}
						})
					})
				})

				ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(12).Wrap().ClipX().Children(func() {
					ui.Text(c, fmt.Sprintf("关注 %s · 粉丝 %s · 获赞 %s · 作品 %s",
						FormatCount(u.FollowingCount),
						FormatCount(u.FollowerCount),
						FormatCount(u.TotalFavorited),
						FormatCount(int64(u.AwemeCount)))).
						FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
				})

				if strings.TrimSpace(u.Signature) != "" {
					ui.Text(c, strings.TrimSpace(u.Signature)).FontSize(13).TextColor(ColorTextMuted).MaxLines(3)
				}

				if liveState != nil {
					st := *liveState
					qualSpec := formatLiveAccountQualitySpec(st)
					ui.Row(c).
						Label("抖音主页直播横幅").
						FillWidth().
						Padding(8, 12).
						Radius(10).
						Background(ColorLiveBg).
						Border(1, ColorLive).
						AlignItems(ui.Center).
						Justify(ui.SpaceBetween).
						Gap(8).
						ClipX().
						Children(func() {
							ui.Row(c).AlignItems(ui.Center).Gap(8).Grow(1).ClipX().Children(func() {
								ui.Box(c).Shrink(0).Padding(2, 8).Radius(999).Background(ColorLive).Children(func() {
									ui.Text(c, "直播中").FontSize(11).FontWeight(600).TextColor(ColorWhite).SingleLine()
								})
								title := firstNonEmptyStrUI(st.Title, u.Nickname+"的直播间")
								ui.Text(c, title).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
								if qualSpec != "" {
									ui.Text(c, "· "+qualSpec).Shrink(0).FontSize(12).FontWeight(600).TextColor(ColorLive).SingleLine()
								}
							})
							if st.Recording {
								recKey := firstNonEmptyStrUI(st.RecordingKey, "douyin:"+u.SecUID)
								if DangerOutlinePillButton(c, "停止录制", "抖音主页停止录制").Clicked() {
									a.stopAccountLiveRecording(recKey)
								}
							} else if AccentPillButton(c, "录制", "抖音主页开始录制").Clicked() {
								a.startAccountLiveRecording("douyin", u.SecUID, firstNonEmptyStrUI(st.WebRID, u.WebRID, st.RoomID, u.RoomID))
							}
						})
				}
			})

		// Single "作品" section heading (no tabs).
		ui.Row(c).FillWidth().Padding(10, 16, 6, 16).AlignItems(ui.Center).Justify(ui.SpaceBetween).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
				ui.Text(c, "作品").FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine()
				if u.AwemeCount > 0 {
					ui.Text(c, fmt.Sprintf("共 %d 个", u.AwemeCount)).FontSize(12).TextColor(ColorTextMuted).SingleLine()
				}
			})
			if !u.IsPrivate {
				a.renderUserListFilter(c)
			}
		})
	})
}

func (a *App) renderDouyinWorkGridCard(c *ui.Context, u *service.DouyinUserView, card service.DouyinWorkCardDTO, dotStatus string) {
	var relCover string
	if card.CacheKey != "" {
		if it, ok, err := a.backend.GetLibraryItem(card.CacheKey); err == nil && ok {
			if dotStatus == "" {
				dotStatus = library.ComputeDotState(it).Dot
			}
			for _, m := range it.Media {
				if (m.Role == "cover" || m.Role == "image") && relCover == "" {
					relCover = m.Path
				}
			}
			if relCover == "" && len(it.NotePages) > 0 {
				relCover = firstNonEmptyStrUI(it.NotePages[0].Image, it.NotePages[0].Cover)
			}
		}
	}
	bmp := a.lookupLocalBitmap(card.CacheKey, relCover)
	if bmp == nil {
		bmp = a.lookupRemoteBitmap(card.CoverURL)
	}
	pal := AvatarColor(card.Title+card.ID, accountSeedHash("douyin", card.ID))

	ui.Column(c).Grow(1).Basis(0).Gap(5).ClipX().Children(func() {
		coverBox := ui.Box(c).
			FillWidth().
			AspectRatio(0.75). // 3:4 vertical card
			Radius(10).
			Border(1, ColorBorder).
			Background(ColorBgSecondary).
			Clip()
		coverBox.Draw(func(p *ui.Painter, r ui.Rect) {
			if bmp == nil {
				p.FillGradient(r, ui.LinearGradient{
					From:  pal.Mix(ColorTextMain, 0.35),
					To:    pal.Mix(ColorInfoBlue, 0.45),
					Angle: 135,
				}, 10)
			}
		})
		coverBox.Children(func() {
			openBtn := ui.ButtonBase(c).
				Label("打开作品：" + card.ID).
				Absolute().
				Left(0).
				Top(0).
				Right(0).
				Bottom(0).
				Padding(0)
			openBtn.Children(func() {
				if bmp != nil {
					ui.Image(c, bmp).Absolute().Left(0).Top(0).Right(0).Bottom(0).Fit(ui.Cover)
				}
			})
			if openBtn.Clicked() {
				dur := ""
				if card.DurationSec > 0 {
					dur = FormatDuration(card.DurationSec)
				}
				a.OpenDouyinWork(card.ID, card.Kind, card.Title, u.Nickname, u.SecUID, card.CoverURL, dur)
			}

			// Top-left badges: 置顶 & 图文
			if card.IsTop || card.Kind == "note" {
				ui.Row(c).Absolute().Top(6).Left(6).Gap(4).AlignItems(ui.Center).Children(func() {
					if card.IsTop {
						ui.Box(c).Label("抖音置顶标记："+card.ID).Padding(2, 6).Radius(4).Background(ColorAccent).Children(func() {
							ui.Text(c, "置顶").FontSize(11).FontWeight(600).TextColor(ColorWhite).SingleLine()
						})
					}
					if card.Kind == "note" {
						ui.Box(c).Label("抖音图文标记："+card.ID).Padding(2, 6).Radius(4).Background(ui.RGBA(0, 0, 0, 0.65)).Children(func() {
							ui.Text(c, "图文").FontSize(11).FontWeight(600).TextColor(ColorWhite).SingleLine()
						})
					}
				})
			}

			// Top-right 8px status dot
			if dotStatus != "" && dotStatus != "none" {
				ui.Box(c).Absolute().Top(6).Right(6).Padding(2).Radius(999).Background(ui.RGBA(255, 255, 255, 0.92)).Children(func() {
					DrawStatusDot(c, dotStatus, "作品状态："+card.ID)
				})
			}

			// Bottom-left video duration (keeps bottom-right clear for floating ⋯ button)
			if card.Kind == "video" && card.DurationSec > 0 {
				ui.Box(c).Absolute().Bottom(6).Left(6).Padding(2, 6).Radius(4).Background(ui.RGBA(0, 0, 0, 0.72)).Children(func() {
					ui.Text(c, FormatDuration(card.DurationSec)).FontSize(11).FontWeight(600).TextColor(ColorWhite).SingleLine()
				})
			}

			// Bottom-right floating ⋯ menu ("缓存" / "复制链接" / "在浏览器中打开")
			menuOpen := a.DouyinCardMenuID == card.ID
			ui.Box(c).Absolute().Bottom(6).Right(6).Radius(14).Background(ui.RGBA(255, 255, 255, 0.88)).Children(func() {
				MoreMenuButton(c, "作品更多："+card.ID, 26, &menuOpen, func(m *ui.Menu) {
					if !card.Uncacheable && dotStatus != "green" && m.Item("缓存").Chosen() {
						a.cacheDouyinWorkNow(card.ID)
					}
					canonURL := firstNonEmptyStrUI(card.CanonicalURL, "https://www.douyin.com/video/"+card.ID)
					if m.Item("复制链接").Chosen() {
						c.WriteClipboard(canonURL)
						c.Toast("已复制链接")
					}
					if m.Item("在浏览器中打开").Chosen() {
						_ = a.backend.Desktop().OpenExternalURL(canonURL)
					}
				})
			})
			keyedMenuOpen(&a.DouyinCardMenuID, card.ID, menuOpen)
		})

		caption := firstNonEmptyStrUI(card.Title, card.Desc, card.ID)
		ui.Text(c, caption).FontSize(13).FontWeight(500).TextColor(ColorTextMain).MaxLines(2)
	})
}

func (a *App) renderDouyinUserFooter(c *ui.Context, u *service.DouyinUserView) {
	ui.Column(c).FillWidth().Padding(14, 16, 24, 16).AlignItems(ui.Center).Gap(8).ClipX().Children(func() {
		if u.IsPrivate {
			ui.Text(c, "这个账号是私密账号，不能浏览作品").
				Label("私密账号提示").
				FontSize(14).
				TextColor(ColorTextMuted).
				SingleLine()
			return
		}
		if a.UserListFilter == userFilterUncached && len(a.CurrentDouyinWorks) > 0 && a.douyinFilteredEmpty {
			ui.Text(c, "已加载的作品都缓存了").Label("未缓存筛选为空").FontSize(13).TextColor(ColorTextMuted).SingleLine()
		}
		if a.DouyinWorksAuthExpired {
			ui.Row(c).
				FillWidth().
				Padding(10, 14).
				Radius(10).
				Background(ColorWarnBg).
				Border(1, ColorDotYellow).
				AlignItems(ui.Center).
				Justify(ui.SpaceBetween).
				Gap(8).
				Children(func() {
					ui.Text(c, "登录已失效").FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine().Grow(1)
					if AccentPillButton(c, "去重新登录", "抖音失效去登录").Clicked() {
						a.OpenLoginForSite("douyin")
					}
				})
			return
		}
		if a.DouyinWorksError != "" {
			a.renderUserListError(c, a.DouyinWorksError, "重试加载抖音作品", a.LoadMoreDouyinWorks)
			return
		}
		if a.pending["douyin-works-"+u.SecUID] {
			ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
				ui.Spinner(c).Size(16, 16)
				ui.Text(c, "正在加载作品…").FontSize(13).TextColor(ColorTextMuted).SingleLine()
			})
			return
		}
		if a.DouyinWorksHasMore {
			if OutlinePillButton(c, "加载更多作品", "加载更多抖音作品").Clicked() {
				a.LoadMoreDouyinWorks()
			}
			return
		}
		if len(a.CurrentDouyinWorks) == 0 {
			ui.Text(c, "还没有发布公开作品").FontSize(13).TextColor(ColorTextMuted).SingleLine()
		} else {
			ui.Text(c, "已经到底了").FontSize(13).TextColor(ColorTextMuted).SingleLine()
		}
	})
}

// ============================================================================
// Route: /douyin/work (Video & Note Detail + BGM Bar, docs/ui.md §3.4)
// ============================================================================

func (a *App) renderDouyinWorkRoute(c *ui.Context, viewportHeight float32) {
	ui.Scroll(c).Grow(1).FillWidth().ClipX().Children(func() {
		ui.Column(c).FillWidth().Padding(16).Gap(14).ClipX().Children(func() {
			if a.Opening != nil {
				a.renderOpening(c)
				return
			}
			if a.CurrentLibraryDetail != nil && a.CurrentLibraryDetail.Kind == "live" {
				a.renderRecordingDetail(c, *a.CurrentLibraryDetail)
				return
			}

			work := a.CurrentDouyinWork
			var cachedItem *library.ItemMetadata
			if a.CurrentLibraryDetail != nil {
				cp := *a.CurrentLibraryDetail
				cachedItem = &cp
			} else if work != nil {
				if work.CacheKey != "" {
					if it, ok, err := a.backend.GetLibraryItem(work.CacheKey); err == nil && ok {
						cachedItem = &it
					}
				}
				if cachedItem == nil && work.CachedItem != nil {
					cp := *work.CachedItem
					cachedItem = &cp
				}
			}

			if work == nil && cachedItem == nil {
				ui.Text(c, "未选择抖音作品，请先通过解析页打开内容。").FontSize(14).TextColor(ColorTextMuted).SingleLine()
				return
			}

			// Derive unified metadata from online DouyinWorkView and/or offline cached ItemMetadata.
			var (
				awemeID, kind, title, desc, canonicalURL, coverURL string
				authorSecUID, authorName, authorFace, authorHandle string
				pubTs                                              int64
				durationSec, coverW, coverH                        int
				hashtags                                           []string
				uncacheable                                        bool
				uncacheableReason                                  string
				itemKey                                            string
			)
			if work != nil {
				awemeID = work.ID
				kind = work.Kind
				title = work.Title
				desc = work.Desc
				hashtags = work.Hashtags
				pubTs = work.CreateTime
				durationSec = work.DurationSec
				coverURL = work.CoverURL
				coverW = work.Width
				coverH = work.Height
				canonicalURL = work.CanonicalURL
				uncacheable = work.Uncacheable
				uncacheableReason = work.UncacheableReason
				itemKey = work.CacheKey
				authorSecUID = work.Author.SecUID
				authorName = work.Author.Nickname
				authorFace = work.Author.AvatarURL
				authorHandle = formatDouyinHandle(work.Author.UniqueID, work.Author.ShortID, work.Author.UID)
			}
			if cachedItem != nil {
				if awemeID == "" {
					awemeID = firstNonEmptyStrUI(cachedItem.RawID, cachedItem.ID)
				}
				if kind == "" {
					if cachedItem.Kind == "dynamic" || len(cachedItem.NotePages) > 0 {
						kind = "note"
					} else {
						kind = "video"
					}
				}
				if title == "" {
					title = cachedItem.Title
				}
				if desc == "" {
					desc = firstNonEmptyStrUI(cachedItem.Desc, cachedItem.Text)
				}
				if pubTs <= 0 {
					pubTs = cachedItem.PubTs
				}
				if durationSec <= 0 {
					durationSec = cachedItem.Duration
				}
				if canonicalURL == "" {
					canonicalURL = cachedItem.URL
				}
				if itemKey == "" {
					itemKey = cachedItem.ItemKey
				}
				if authorSecUID == "" {
					authorSecUID = cachedItem.Author.UID
				}
				if authorName == "" {
					authorName = cachedItem.Author.Name
				}
				if authorFace == "" {
					authorFace = cachedItem.Author.Face
				}
				if authorHandle == "" {
					authorHandle = formatDouyinHandle(cachedItem.Author.UniqueID, "", "")
				}
				if cachedItem.DouyinVideo != nil {
					if coverW <= 0 {
						coverW = cachedItem.DouyinVideo.Width
					}
					if coverH <= 0 {
						coverH = cachedItem.DouyinVideo.Height
					}
				}
			}
			if canonicalURL == "" && awemeID != "" {
				if kind == "note" {
					canonicalURL = "https://www.douyin.com/note/" + awemeID
				} else {
					canonicalURL = "https://www.douyin.com/video/" + awemeID
				}
			}

			var dotStatus string
			var relCover string
			if cachedItem != nil {
				dotStatus = library.ComputeDotState(*cachedItem).Dot
				for _, m := range cachedItem.Media {
					if m.Role == "cover" && relCover == "" {
						relCover = m.Path
					}
				}
			}

			// 1. Author Row: Avatar, Nickname, 12px 抖音 badge, right-side Primary Cache button + Status Dot
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(10).ClipX().Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(10).Grow(1).ClipX().Children(func() {
					openAuthor := func() {
						if authorSecUID != "" {
							a.OpenDouyinUser(authorSecUID, authorName)
						}
					}
					a.renderAccountAvatar(c, "douyin", firstNonEmptyStrUI(authorName, "抖音作者"), authorSecUID, 0, 42, false, "打开抖音作者主页", openAuthor, authorFace)
					ui.Column(c).Grow(1).Gap(2).ClipX().Children(func() {
						ui.Row(c).AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
							nameBtn := ui.ButtonBase(c).Padding(0).Children(func() {
								ui.Text(c, firstNonEmptyStrUI(authorName, "抖音作者")).FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
							})
							if nameBtn.Clicked() {
								openAuthor()
							}
							renderSiteSmallBadgeWithDot(c, "douyin", "抖音作品作者站点标记")
						})
						if authorHandle != "" {
							ui.Text(c, authorHandle).FontSize(12).TextColor(ColorTextMuted).SingleLine()
						}
					})
				})

				ui.Row(c).Shrink(0).AlignItems(ui.Center).Gap(8).Children(func() {
					if dotStatus != "" && dotStatus != "none" {
						DrawStatusDot(c, dotStatus, "抖音作品缓存状态")
					}
					cacheBtnLabel := "缓存视频"
					if kind == "note" {
						cacheBtnLabel = "缓存图文"
					}
					if AccentPillButton(c, cacheBtnLabel, cacheBtnLabel).Disabled(uncacheable || awemeID == "").Clicked() {
						a.cacheDouyinWorkNow(awemeID)
					}
					menuOpen := a.PostMenuKey == "douyin-work:"+awemeID
					MoreMenuButton(c, "抖音作品更多", 32, &menuOpen, func(m *ui.Menu) {
						if m.Item("复制链接").Chosen() {
							c.WriteClipboard(canonicalURL)
							c.Toast("已复制链接")
						}
						if m.Item("在浏览器中打开").Chosen() {
							_ = a.backend.Desktop().OpenExternalURL(canonicalURL)
						}
						if cachedItem != nil && itemKey != "" {
							if m.Item("打开文件夹").Chosen() {
								_ = a.backend.OpenItemFolder(itemKey)
							}
						}
					})
					keyedMenuOpen(&a.PostMenuKey, "douyin-work:"+awemeID, menuOpen)
				})
			})

			if uncacheable || kind == "unsupported" {
				reason := firstNonEmptyStrUI(uncacheableReason, "当前抖音内容暂不支持缓存，可在浏览器中打开查看。")
				ui.Row(c).FillWidth().Padding(10, 14).Radius(10).Background(ColorWarnBg).Border(1, ColorDotYellow).AlignItems(ui.Center).Gap(8).Children(func() {
					DrawIcon(c, "alert-circle", 16, ColorDotYellow, 1.8)
					ui.Text(c, reason).FontSize(13).TextColor(ColorTextMain).MaxLines(2).Grow(1)
				})
			}

			// 2. Headline (first line of caption) + Expandable Body with Accent #话题
			headline, restBody := splitDouyinHeadlineAndBody(title, desc, hashtags)
			ui.Text(c, headline).FontSize(20).FontWeight(600).TextColor(ColorTextMain).MaxLines(3)
			if restBody != "" {
				a.renderRichHashtagText(c, "douyin-work-body:"+awemeID, restBody)
			}

			// 3. Media Body: Note Ordered Pages + BGM Bar OR Vertical Video Cover
			if kind == "note" {
				pages := buildDouyinNoteRenderPages(work, cachedItem)
				a.renderDouyinNotePagesGrid(c, itemKey, pages)

				// BGM Bar right below note pages (docs/ui.md §3.4)
				var bgmTitle, bgmAuthor, localBGMRel, onlineBGMURL, unavailableHint string
				var bgmDur int
				var bgmOriginal bool
				hasMusicMeta := false
				if cachedItem != nil && cachedItem.Music != nil {
					hasMusicMeta = true
					bgmTitle = cachedItem.Music.Title
					bgmAuthor = cachedItem.Music.Author
					bgmDur = cachedItem.Music.Duration
					bgmOriginal = cachedItem.Music.Original
					localBGMRel = cachedItem.Music.File
					if localBGMRel == "" {
						unavailableHint = "本机配乐文件缺失"
					}
				}
				if work != nil && work.Music != nil {
					hasMusicMeta = true
					if bgmTitle == "" {
						bgmTitle = work.Music.Title
					}
					if bgmAuthor == "" {
						bgmAuthor = work.Music.Author
					}
					if bgmDur <= 0 {
						bgmDur = work.Music.DurationSec
					}
					bgmOriginal = bgmOriginal || work.Music.IsOriginalSound
					if work.Music.Playable && work.Music.PlayURL != "" {
						onlineBGMURL = work.Music.PlayURL
					} else if localBGMRel == "" {
						unavailableHint = firstNonEmptyStrUI(work.Music.UnavailableHint, unavailableHint, "该作品没有可缓存配乐")
					}
				}
				if !hasMusicMeta && cachedItem != nil {
					for _, miss := range cachedItem.Missing {
						if strings.Contains(miss.Item, "music") || strings.Contains(miss.Item, "audio") {
							hasMusicMeta = true
							unavailableHint = firstNonEmptyStrUI(miss.Reason, "配乐文件缺失")
						}
					}
				}
				if hasMusicMeta {
					a.renderBGMBar(c, "douyin-note:"+awemeID, bgmTitle, bgmAuthor, bgmOriginal, bgmDur, itemKey, localBGMRel, onlineBGMURL, unavailableHint)
				}
			} else {
				// Video cover preserving original aspect ratio, capped at <= 70% of visible viewport height (docs/ui.md §3.4).
				maxCoverH := float32(460)
				if viewportHeight > 240 {
					maxCoverH = min(max(viewportHeight*0.66, 220), 540)
				}
				ratio := float32(0.75) // default 3:4 portrait
				if coverW > 0 && coverH > 0 {
					ratio = min(max(float32(coverW)/float32(coverH), 0.5), 1.8)
				}
				dispH := maxCoverH
				dispW := dispH * ratio
				if dispW > 600 {
					dispW = 600
					dispH = dispW / ratio
				}
				durText := ""
				if durationSec > 0 {
					durText = FormatDuration(durationSec)
				}
				ui.Row(c).FillWidth().Justify(ui.Center).Children(func() {
					coverBtn := ui.ButtonBase(c).
						Label("抖音视频封面播放").
						Padding(0).
						Radius(12).
						Clip().
						Children(func() {
							a.renderMediaThumb(c, itemKey, relCover, headline, durText, "", cachedItem != nil, dispW, dispH, coverURL)
						})
					if coverBtn.Clicked() {
						if cachedItem != nil {
							a.OpenPlayerOverlay(*cachedItem)
						} else if coverURL != "" {
							a.OpenImageViewer([]string{coverURL}, nil, 0)
						}
					}
				})
			}

			// 4. Metadata Line: Publish Time & Duration + Music Line (for video works)
			var metaParts []string
			if pubTs > 0 {
				metaParts = append(metaParts, FormatTimestamp(pubTs))
			}
			if kind != "note" && durationSec > 0 {
				metaParts = append(metaParts, "时长 "+FormatDuration(durationSec))
			}
			if kind == "note" && cachedItem != nil && len(cachedItem.NotePages) > 0 {
				metaParts = append(metaParts, fmt.Sprintf("共 %d 页", len(cachedItem.NotePages)))
			} else if kind == "note" && work != nil && len(work.Pages) > 0 {
				metaParts = append(metaParts, fmt.Sprintf("共 %d 页", len(work.Pages)))
			}
			if len(metaParts) > 0 {
				ui.Text(c, strings.Join(metaParts, " · ")).FontSize(13).TextColor(ColorTextMuted).SingleLine()
			}

			if kind != "note" {
				var mTitle, mAuthor string
				var mOrig bool
				if work != nil && work.Music != nil && (work.Music.Title != "" || work.Music.Author != "" || work.Music.IsOriginalSound) {
					mTitle, mAuthor, mOrig = work.Music.Title, work.Music.Author, work.Music.IsOriginalSound
				} else if cachedItem != nil && cachedItem.Music != nil {
					mTitle, mAuthor, mOrig = cachedItem.Music.Title, cachedItem.Music.Author, cachedItem.Music.Original
				}
				if mTitle != "" || mAuthor != "" || mOrig {
					ui.Text(c, formatMusicLine(mTitle, mAuthor, mOrig)).FontSize(13).TextColor(ColorTextMuted).SingleLine()
				}
			}

			// 5. Interaction Statistics Row (Online ONLY; NO play count — Douyin Web returns placeholder 0, docs/ui.md §3.4)
			if a.CurrentLibraryDetail == nil && work != nil {
				stats := []struct {
					Icon  string
					Label string
					Count int64
				}{
					{"heart", "点赞", work.Stats.DiggCount},
					{"message", "评论", work.Stats.CommentCount},
					{"star", "收藏", work.Stats.CollectCount},
					{"share", "分享", work.Stats.ShareCount},
				}
				ui.Row(c).
					Label("抖音作品统计行").
					FillWidth().
					Padding(10, 4).
					BorderWidth(1, 0, 1, 0).
					BorderColor(ColorBorder).
					AlignItems(ui.Center).
					Justify(ui.SpaceBetween).
					Wrap().
					Gap(12).
					Children(func() {
						for _, st := range stats {
							s := st
							ui.Row(c).AlignItems(ui.Center).Gap(5).Tooltip(fmt.Sprintf("%s %d", s.Label, s.Count)).Children(func() {
								DrawIcon(c, s.Icon, 16, ColorTextMuted, 1.75)
								ui.Text(c, fmt.Sprintf("%s %s", s.Label, FormatCount(s.Count))).
									FontSize(13).TextColor(ColorTextMuted).SingleLine()
							})
						}
					})
			}

			// 6. Local Cached Stream Spec Summary (NO quality upgrade banner, NO danmaku/subtitle supplement button — docs/ui.md §3.4)
			if cachedItem != nil {
				var specText string
				if kind != "note" {
					var onlineQ, onlineC string
					if work != nil {
						onlineQ, onlineC = work.QualityLabel, work.VideoCodec
					}
					specText = formatDouyinVideoSpec(cachedItem.DouyinVideo, onlineQ, onlineC)
				} else if len(cachedItem.NotePages) > 0 {
					specText = fmt.Sprintf("图文 %d 页", len(cachedItem.NotePages))
				}
				ui.Row(c).
					FillWidth().
					Padding(10, 14).
					Radius(12).
					Background(ColorBgMain).
					Border(1, ColorBorder).
					AlignItems(ui.Center).
					Justify(ui.SpaceBetween).
					Gap(10).
					Children(func() {
						label := "本机已缓存"
						if specText != "" {
							label = "本机已缓存：" + specText
						}
						ui.Text(c, label).Label("抖音本机缓存规格").FontSize(13).FontWeight(600).TextColor(ColorDotGreen).SingleLine().Grow(1)
						if kind != "note" {
							if OutlinePillButton(c, "播放本机缓存", "播放抖音缓存视频").Clicked() {
								a.OpenPlayerOverlay(*cachedItem)
							}
						}
					})
			}
		})
	})
}
