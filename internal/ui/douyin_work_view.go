package ui

import (
	"fmt"
	"strings"

	"github.com/chialecode/trace/internal/library"
	"github.com/egoist/mygo/ui"
)

// ============================================================================
// Route: /douyin/work (Video & Note Detail + BGM Bar, docs/ui.md §3.4)
// ============================================================================

func (a *App) lookupDouyinWorkStateCached(itemKey string) library.DotState {
	if itemKey == "" || a.backend == nil {
		return library.DotState{}
	}
	st := a.fs()
	if st.douyinWorkStateSet && st.douyinWorkStateKey == itemKey && st.douyinWorkStateRev == a.cacheStateRev {
		return st.douyinWorkState
	}
	res := a.backend.States([]string{itemKey})[itemKey]
	st.douyinWorkStateKey = itemKey
	st.douyinWorkStateRev = a.cacheStateRev
	st.douyinWorkState = res
	st.douyinWorkStateSet = true
	return res
}

func (a *App) renderDouyinWorkRoute(c *ui.Context, viewportHeight float32) {
	ui.Scroll(c).Grow(1).FillWidth().ClipX().Children(func() {
		ui.Column(c).FillWidth().Padding(16).Gap(14).ClipX().Children(func() {
			if a.Opening != nil {
				a.renderOpening(c)
				return
			}

			work := a.CurrentDouyinWork
			var cachedItem *library.ItemMetadata
			var relCover string
			var dotStatus string
			if a.CurrentLibraryDetail != nil {
				cp := *a.CurrentLibraryDetail
				cachedItem = &cp
				dotStatus = library.ComputeDotState(cp).Dot
				for _, m := range cp.Media {
					if m.Role == "cover" && relCover == "" {
						relCover = m.Path
					}
				}
			} else if work != nil {
				if work.CacheKey != "" {
					if entry, ok := a.lookupCompactLibEntry(work.CacheKey); ok {
						cachedItem = entry.item
						dotStatus = entry.dotStatus
						relCover = entry.relCover
					}
				}
				if cachedItem == nil && work.CachedItem != nil {
					cp := *work.CachedItem
					cachedItem = &cp
					dotStatus = library.ComputeDotState(cp).Dot
					for _, m := range cp.Media {
						if m.Role == "cover" && relCover == "" {
							relCover = m.Path
						}
					}
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
				canonicalURL = douyinWorkFallbackURL(kind, awemeID)
			}

			offline := a.CurrentRoute == RouteLibraryItem
			if !offline && itemKey != "" {
				if st := a.lookupDouyinWorkStateCached(itemKey); st.Dot != "" && st.Dot != "none" {
					dotStatus = st.Dot
				}
			}
			if offline {
				coverURL = ""
				authorFace = ""
			}

			// 1. Author Row: Avatar, Nickname, 12px 抖音 badge, right-side Primary Cache button + Status Dot
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(10).ClipX().Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(10).Grow(1).ClipX().Children(func() {
					var openAuthor func()
					avatarLabel := ""
					if !offline {
						avatarLabel = "打开抖音作者主页"
						openAuthor = func() {
							if authorSecUID != "" {
								a.OpenDouyinUser(authorSecUID, authorName)
							}
						}
					}
					a.renderAccountAvatar(c, "douyin", firstNonEmptyStrUI(authorName, "抖音作者"), authorSecUID, 0, 42, false, avatarLabel, openAuthor, authorFace)
					ui.Column(c).Grow(1).Gap(2).ClipX().Children(func() {
						ui.Row(c).AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
							displayAuthor := firstNonEmptyStrUI(authorName, "抖音作者")
							if openAuthor != nil {
								nameBtn := ui.ButtonBase(c).Padding(0).Children(func() {
									ui.Text(c, displayAuthor).FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
								})
								if nameBtn.Clicked() {
									openAuthor()
								}
							} else {
								ui.Text(c, displayAuthor).FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
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
					if offline {
						if cachedItem != nil {
							a.renderLibraryItemMoreMenu(c, *cachedItem, 32)
						}
						return
					}
					cacheBtnLabel := "缓存视频"
					if kind == "note" {
						cacheBtnLabel = "缓存图文"
					}
					caching := awemeID != "" && a.isPending("cache-douyin:"+awemeID)
					inFlight := dotStatus == "blue" || dotStatus == "gray" || dotStatus == "queued" || dotStatus == "running"
					btnText := cacheBtnLabel
					if caching || inFlight {
						btnText = "缓存中…"
					}
					disabled := uncacheable || awemeID == "" || caching || inFlight
					if AccentPillButton(c, btnText, cacheBtnLabel).Disabled(disabled).Clicked() && !disabled {
						if forUID, forName := a.douyinWorkCacheFor(awemeID); forUID != "" {
							a.cacheDouyinWorkFor(awemeID, forUID, forName)
						} else {
							a.cacheDouyinWorkNow(awemeID)
						}
					}
					menuOpen := a.PostMenuKey == "douyin-work:"+awemeID
					MoreMenuButton(c, "抖音作品更多", 32, &menuOpen, func(m *ui.Menu) {
						if m.Item("复制链接").Chosen() {
							c.WriteClipboard(canonicalURL)
							c.Toast("已复制链接")
						}
						if m.Item("在浏览器中打开").Chosen() {
							a.showError(a.backend.Desktop().OpenExternalURL(canonicalURL))
						}
						if cachedItem != nil && itemKey != "" {
							if !cachedItem.IsArchivedSnapshot() {
								if m.Item("强制重新下载").Chosen() {
									a.openForceRecacheConfirm(itemKey, firstNonEmptyStrUI(title, desc, awemeID))
								}
							}
							if m.Item("打开文件夹").Chosen() {
								a.showError(a.backend.OpenItemFolder(itemKey))
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
			ui.Text(c, headline).FontSize(20).LineHeight(LineHeightHeading).FontWeight(600).TextColor(ColorTextMain).MaxLines(3)
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
				if offline {
					onlineBGMURL = ""
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
