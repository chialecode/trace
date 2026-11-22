package ui

import (
	"context"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

func normalizeSiteIDUI(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "douyin":
		return "douyin"
	case "weibo":
		return "weibo"
	case "x":
		return "x"
	default:
		return "bilibili"
	}
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

// renderThreeSegmentSiteFilter renders the 5-way "全部 / B 站 / 抖音 / 微博 / X" switcher
// with compact spacing so it fits cleanly inside narrow panels and 720px viewports.
func renderThreeSegmentSiteFilter(c *ui.Context, labelPrefix, current string, onSelect func(string)) {
	cur := normalizeSiteFilter(current)
	opts := []struct {
		Key  string
		Text string
	}{
		{"all", "全部"},
		{"bilibili", "B 站"},
		{"douyin", "抖音"},
		{"weibo", "微博"},
		{"x", "X"},
	}
	ui.Row(c).
		FillWidth().
		Padding(2).
		Radius(8).
		Background(ColorBgMain).
		Border(1, ColorBorder).
		Gap(2).
		ClipX().
		Children(func() {
			for _, opt := range opts {
				o := opt
				active := cur == o.Key
				btn := ui.ButtonBase(c).
					Label(labelPrefix+o.Text).
					Grow(1).
					Basis(0).
					Padding(5, 2).
					Radius(6).
					Justify(ui.Center).
					AlignItems(ui.Center).
					ClipX()
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

// renderTwoSegmentLoginSiteSwitcher renders the top "哔哩哔哩 / 抖音 / 微博 / X" switcher on /login.
func renderTwoSegmentLoginSiteSwitcher(c *ui.Context, current string, onSelect func(string)) {
	cur := normalizeSiteIDUI(current)
	opts := []struct {
		Key  string
		Text string
	}{
		{"bilibili", "哔哩哔哩"},
		{"douyin", "抖音"},
		{"weibo", "微博"},
		{"x", "X"},
	}
	ui.Row(c).
		FillWidth().
		Padding(3).
		Radius(10).
		Background(ColorBgMain).
		Border(1, ColorBorder).
		Gap(4).
		ClipX().
		Children(func() {
			for _, opt := range opts {
				o := opt
				active := cur == o.Key
				btn := ui.ButtonBase(c).
					Label("登录站点："+o.Text).
					Grow(1).
					Basis(0).
					Padding(7, 8).
					Radius(8).
					Justify(ui.Center).
					AlignItems(ui.Center).
					ClipX()
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
	// A subscribed account has a local copy of its avatar (both sites), which
	// works offline and does not depend on a stored URL.
	lookupUID := uid
	if lookupUID == "" && normSite == "bilibili" && mid > 0 {
		lookupUID = strconv.FormatInt(mid, 10)
	}
	if bmp := a.lookupAccountAvatarBitmap(normSite, lookupUID); bmp != nil {
		return a.renderAvatarWithBitmap(c, name, seed, size, liveRing, label, onClick, bmp)
	}
	var face string
	if !a.pendingLocalAvatar {
		face = firstImageURL(faces)
		if face == "" {
			if normSite == "bilibili" && mid > 0 {
				face = a.faceURL(mid)
			} else {
				face = a.faceURLForAccount(normSite, uid)
			}
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

	ui.Column(c).FillWidth().Gap(6).ClipX().Children(func() {
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
				ui.Text(c, line).FontSize(15).LineHeight(LineHeightBody).TextColor(ColorTextMain)
				continue
			}
			ui.Row(c).FillWidth().Wrap().Gap(2).AlignItems(ui.Center).Children(func() {
				for _, sp := range spans {
					s := sp
					if s.Hashtag {
						ui.Text(c, s.Text).FontSize(15).LineHeight(LineHeightBody).FontWeight(600).TextColor(ColorAccent)
					} else {
						ui.Text(c, s.Text).FontSize(15).LineHeight(LineHeightBody).TextColor(ColorTextMain)
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

// cacheDouyinWorkFor caches a work from the user page of account forUID: a
// co-creation by someone else goes to forUID's folder (DEC-063).
func (a *App) cacheDouyinWorkFor(awemeID, forUID, forName string) {
	awemeID = strings.TrimSpace(awemeID)
	if awemeID == "" || a.backend == nil {
		return
	}
	reqKey := "cache-douyin:" + awemeID
	if a.isPending(reqKey) {
		return
	}
	backend := a.backend
	requestGuarded(a, reqKey, nil, func(ctx context.Context) (download.Task, error) {
		return backend.CacheDouyinWorkFor(ctx, awemeID, forUID, forName)
	}, func(download.Task) {
		a.BannerInfo = "已加入缓存队列：" + awemeID
		a.RefreshData()
	}, func(err error) {
		a.showError(err)
	})
}

func (a *App) cacheDouyinWorkNow(awemeID string) {
	awemeID = strings.TrimSpace(awemeID)
	if awemeID == "" || a.backend == nil {
		return
	}
	reqKey := "cache-douyin:" + awemeID
	if a.isPending(reqKey) {
		return
	}
	backend := a.backend
	requestGuarded(a, reqKey, nil, func(ctx context.Context) (download.Task, error) {
		return backend.CacheDouyinWork(ctx, awemeID)
	}, func(download.Task) {
		a.BannerInfo = "已加入缓存队列：" + awemeID
		a.RefreshData()
	}, func(err error) {
		a.showError(err)
	})
}
