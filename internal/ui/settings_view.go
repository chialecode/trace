package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/egoist/mygo/ui"
)

// Settings categories rendered in the left sidebar while Settings is open (docs/ui.md §3.7).
var settingsCategories = []struct {
	Key   string
	Icon  string
	Label string
}{
	{"account", "user", "账号"},
	{"download", "download", "缓存与下载"},
	{"quality", "play", "画质与音质"},
	{"rules", "grid", "缓存规则"},
	{"subscription", "refresh", "订阅与直播"},
	{"storage", "folder", "存储"},
	{"general", "sliders", "常规"},
	{"network", "link", "网络"},
	{"remote", "upload", "远程"},
	{"logs", "logs", "日志"},
	{"update", "refresh", "更新"},
	{"about", "check-circle", "关于"},
}

var (
	concurrencyChoices          = numberChoices("1", "2", "3", "4")
	multiPartChoices            = []settingChoice{{"all", "缓存全部分 P"}, {"first", "只缓存第 1 P"}}
	videoQualityChoices         = []settingChoice{{"best", "能拿到的最高（推荐）"}, {"2160", "不超过 4K"}, {"1080", "不超过 1080P"}, {"720", "不超过 720P"}}
	audioQualityChoices         = []settingChoice{{"best", "能拿到的最高（推荐）"}, {"192k", "192K"}}
	subscriptionIntervalChoices = []settingChoice{{"30", "30 分钟"}, {"60", "1 小时"}, {"120", "2 小时"}, {"360", "6 小时"}}
	liveIntervalChoices         = []settingChoice{{"1", "1 分钟"}, {"3", "3 分钟"}, {"5", "5 分钟"}, {"10", "10 分钟"}}
	closeActionChoices          = []settingChoice{{"tray", "最小化到托盘（推荐）"}, {"quit", "退出"}}
	codecDescriptions           = map[string]string{
		"avc":  "AVC (H.264)",
		"av1":  "AV1",
		"hevc": "HEVC (H.265) · 部分电脑无法在应用内播放",
	}
)

func settingsCategoryLabel(key string) string {
	for _, cat := range settingsCategories {
		if cat.Key == key {
			return cat.Label
		}
	}
	return "设置"
}

func settingChoiceRowDisabled(c *ui.Context, title, note, label string, value *string, options []settingChoice, disabled bool) bool {
	names := make([]string, len(options))
	shown := *value
	for i, o := range options {
		names[i] = o.name
		if o.value == *value {
			shown = o.name
		}
	}
	changed := false
	titleColor := ColorTextMain
	if disabled {
		titleColor = ColorTextMuted
	}
	ui.Row(c).FillWidth().Padding(12, 0).BorderWidth(0, 0, 1, 0).BorderColor(ColorBorder).AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(12).ClipX().Children(func() {
		ui.Column(c).Grow(1).Gap(2).ClipX().Children(func() {
			ui.Text(c, title).FontSize(14).FontWeight(500).TextColor(titleColor).SingleLine()
			if note != "" {
				ui.Text(c, note).FontSize(12).TextColor(ColorTextMuted).MaxLines(2)
			}
		})
		if ui.Select(c, &shown, names).Label(label).Disabled(disabled).Changed() && !disabled {
			for _, o := range options {
				if o.name == shown && o.value != *value {
					*value, changed = o.value, true
				}
			}
		}
	})
	return changed
}

func settingSwitchRowDisabled(c *ui.Context, title, desc, switchLabel string, value *bool, disabled bool) bool {
	changed := false
	titleColor := ColorTextMain
	if disabled {
		titleColor = ColorTextMuted
	}
	ui.Row(c).
		FillWidth().
		Padding(12, 0).
		BorderWidth(0, 0, 1, 0).
		BorderColor(ColorBorder).
		AlignItems(ui.Center).
		Justify(ui.SpaceBetween).
		Gap(12).
		ClipX().
		Children(func() {
			ui.Column(c).Grow(1).Gap(2).ClipX().Children(func() {
				ui.Text(c, title).FontSize(14).FontWeight(500).TextColor(titleColor).SingleLine()
				if desc != "" {
					ui.Text(c, desc).FontSize(12).TextColor(ColorTextMuted).MaxLines(2)
				}
			})
			changed = ui.Switch(c, value).Label(switchLabel).Disabled(disabled).Changed() && !disabled
		})
	return changed
}

// renderSettingsRoute shows the chosen category; the categories themselves
// are the left sidebar while Settings is open (docs/ui.md §3.7).
func (a *App) renderSettingsRoute(c *ui.Context) {
	ui.Scroll(c).Grow(1).FillWidth().ClipX().Children(func() {
		ui.Column(c).FillWidth().Padding(18, 20).Gap(14).ClipX().Children(func() {
			a.renderSettingsCategoryContent(c)
		})
	})
}

// renderSettingsSidebar replaces the main navigation while Settings is open:
// a way back, then one entry per category, each an icon and a name only, as
// the main navigation's entries.
func (a *App) renderSettingsSidebar(c *ui.Context, compact bool) {
	ui.Column(c).Fill().Padding(max(10, a.titleBar.Height+2), 8, 10, 8).Gap(2).ClipX().Children(func() {
		back := ui.ButtonBase(c).Label("返回").Tooltip("返回").FillWidth().Radius(8).Padding(8, 10).Gap(10).AlignItems(ui.Center)
		if compact {
			back.Justify(ui.Center)
		} else {
			back.Justify(ui.Start)
		}
		if back.Hovered() {
			back.Background(ColorHover)
		}
		back.Children(func() {
			DrawIcon(c, "arrow-left", 18, ColorTextMain, 1.8)
			if !compact {
				ui.Text(c, "设置").FontSize(16).FontWeight(600).TextColor(ColorTextMain).SingleLine()
			}
		})
		if back.Clicked() {
			a.NavigateBack()
		}
		ui.Box(c).Height(8)
		for _, cat := range settingsCategories {
			ct := cat
			active := a.SettingsCategory == ct.Key
			var clicked bool
			if compact {
				clicked = panelItem(c, "设置分类："+ct.Label, active, func() {
					col := ColorTextMuted
					if active {
						col = ColorAccent
					}
					ui.Row(c).Grow(1).Justify(ui.Center).Children(func() { DrawIcon(c, ct.Icon, 18, col, 1.6) })
				})
			} else {
				clicked = panelIconItem(c, ct.Icon, ct.Label, "设置分类："+ct.Label, active)
			}
			if clicked {
				if a.SettingsCategory == "remote" && ct.Key != "remote" {
					a.Remote.CodeInput = ""
					a.Remote.TokenInput = ""
				}
				a.SettingsCategory = ct.Key
			}
		}
	})
}

func (a *App) renderSettingsCategoryContent(c *ui.Context) {
	switch a.SettingsCategory {
	case "rules":
		a.renderCacheRulesSettings(c)
	case "account":
		a.renderAccountSettings(c)
	case "download":
		a.renderDownloadSettings(c)
	case "quality":
		a.renderQualitySettings(c)
	case "subscription":
		a.renderSubscriptionSettings(c)
	case "storage":
		a.renderStorageSettings(c)
	case "general":
		ui.Column(c).FillWidth().Gap(12).ClipX().Children(func() {
			if settingChoiceRow(c, "关闭窗口时", "最小化到托盘时，缓存、定时检查和录制在后台继续，界面占用的内存会释放。", "关闭窗口动作选择", &a.DraftSettings.Window.CloseAction, closeActionChoices) {
				a.DraftSettings.CloseAction = a.DraftSettings.Window.CloseAction
				a.saveSettings()
			}
		})
	case "network":
		a.renderNetworkSettings(c)
	case "remote":
		a.renderRemoteSettings(c)
	case "logs":
		a.renderLogsSettings(c)
	case "update":
		a.renderUpdateSettings(c)
	case "about":
		a.renderAboutSettings(c)
	}
}

func (a *App) renderAccountSettings(c *ui.Context) {
	biliAcct := a.backend.StatusOf("bilibili")
	biliRenew := a.backend.Renewal("bilibili")
	dyAcct := a.backend.StatusOf("douyin")
	wbAcct := a.backend.StatusOf("weibo")

	ui.Column(c).FillWidth().Gap(0).ClipX().Children(func() {
		// Group 1: 哔哩哔哩 (docs/ui.md §3.7)
		ui.Column(c).
			Label("账号分组：哔哩哔哩").
			FillWidth().
			Padding(0, 0, 18, 0).
			BorderWidth(0, 0, 1, 0).
			BorderColor(ColorBorder).
			Gap(12).
			ClipX().
			Children(func() {
				ui.Text(c, "哔哩哔哩").FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()

				if biliAcct.LoggedIn || biliAcct.State == "expired" {
					ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(12).ClipX().Children(func() {
						a.renderAccountAvatar(c, "bilibili", firstNonEmptyStrUI(biliAcct.Uname, "未登录"), biliAcct.UID, biliAcct.Mid, 48, false, "", nil, biliAcct.Face)
						ui.Column(c).Grow(1).Gap(3).ClipX().Children(func() {
							name := firstNonEmptyStrUI(biliAcct.Uname, fmt.Sprintf("UID %d", biliAcct.Mid))
							ui.Text(c, name).FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
							sourceText := "手动凭证（粘贴 Cookie）"
							if biliAcct.Source == "qr" {
								sourceText = "扫码登录"
							}
							ui.Text(c, fmt.Sprintf("UID %d · 登录方式：%s", biliAcct.Mid, sourceText)).
								FontSize(13).TextColor(ColorTextMuted).SingleLine()
						})
					})

					if biliAcct.LoggedIn && biliAcct.Source == "qr" {
						lastChk := firstNonEmptyStrUI(biliRenew.LastRenewedAt, biliAcct.LastCheckedAt, "刚刚")
						renewLine := "自动续期：可用 · 上次续期 " + lastChk
						if biliRenew.LastError != "" {
							renewLine += "（失败：" + biliRenew.LastError + "）"
						}
						ui.Text(c, renewLine).FontSize(12).TextColor(ColorTextMuted).MaxLines(2)
					} else {
						ui.Text(c, "自动续期：不可用：粘贴的 Cookie 与浏览器共用会话，续期会让浏览器那边掉线。过期后请重新粘贴，或改用扫码登录。").
							FontSize(12).TextColor(ColorTextMuted).MaxLines(3)
					}

					ui.Row(c).Gap(8).Wrap().Children(func() {
						if OutlinePillButton(c, "重新登录", "进入登录/更换账号").Clicked() {
							a.OpenLoginForSite("bilibili")
						}
						if biliAcct.LoggedIn && biliAcct.Source == "qr" {
							if OutlinePillButton(c, "检查并续期 Cookie", "检查并续期 Cookie").Clicked() {
								a.refreshAccount()
							}
						}
						if DangerOutlinePillButton(c, "退出登录", "退出登录").Clicked() {
							a.ConfirmLogoutSite = "bilibili"
							a.ConfirmLogoutOpen = true
						}
					})
				} else {
					ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(12).Children(func() {
						ui.Text(c, "未登录哔哩哔哩账号").FontSize(13).TextColor(ColorTextMuted).SingleLine()
						if AccentPillButton(c, "登录哔哩哔哩", "登录哔哩哔哩").Clicked() {
							a.OpenLoginForSite("bilibili")
						}
					})
				}
			})

		// Group 2: 抖音 (docs/ui.md §3.7)
		ui.Column(c).
			Label("账号分组：抖音").
			FillWidth().
			Padding(18, 0, 18, 0).
			BorderWidth(0, 0, 1, 0).
			BorderColor(ColorBorder).
			Gap(12).
			ClipX().
			Children(func() {
				ui.Text(c, "抖音").FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()

				if dyAcct.LoggedIn || dyAcct.State == "expired" {
					handleLine := firstNonEmptyStrUI(formatDouyinHandle(dyAcct.UniqueID, "", dyAcct.UID), "抖音号：—")
					dyUID := firstNonEmptyStrUI(dyAcct.SecUID, dyAcct.UID)
					ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(12).ClipX().Children(func() {
						a.renderAccountAvatar(c, "douyin", firstNonEmptyStrUI(dyAcct.Uname, "抖音用户"), dyUID, 0, 48, false, "", nil, dyAcct.Face)
						ui.Column(c).Grow(1).Gap(3).ClipX().Children(func() {
							ui.Text(c, firstNonEmptyStrUI(dyAcct.Uname, "抖音用户")).FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
							ui.Text(c, handleLine+" · 登录方式：粘贴 Cookie").
								FontSize(13).TextColor(ColorTextMuted).SingleLine()
						})
					})

					ui.Text(c, "自动续期：不可用，粘贴的 Cookie 与浏览器共用会话；过期后请重新粘贴").
						FontSize(12).TextColor(ColorTextMuted).MaxLines(2)

					ui.Row(c).Gap(8).Wrap().Children(func() {
						if OutlinePillButton(c, "重新粘贴", "重新粘贴抖音Cookie").Clicked() {
							a.OpenLoginForSite("douyin")
						}
						if DangerOutlinePillButton(c, "退出登录", "退出抖音登录").Clicked() {
							a.ConfirmLogoutSite = "douyin"
							a.ConfirmLogoutOpen = true
						}
					})
				} else {
					ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(12).Children(func() {
						ui.Text(c, "未登录抖音账号").FontSize(13).TextColor(ColorTextMuted).SingleLine()
						if AccentPillButton(c, "登录抖音", "登录抖音").Clicked() {
							a.OpenLoginForSite("douyin")
						}
					})
				}
			})

		// Group 3: 微博 (docs/ui.md §3.7)
		ui.Column(c).
			Label("账号分组：微博").
			FillWidth().
			Padding(18, 0, 0, 0).
			Gap(12).
			ClipX().
			Children(func() {
				ui.Text(c, "微博").FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()

				if wbAcct.LoggedIn || wbAcct.State == "expired" {
					wbUID := strings.TrimSpace(wbAcct.UID)
					uidLine := "UID：—"
					if wbUID != "" {
						uidLine = "UID " + wbUID
					}
					ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(12).ClipX().Children(func() {
						a.renderAccountAvatar(c, "weibo", firstNonEmptyStrUI(wbAcct.Uname, "微博用户"), wbUID, 0, 48, false, "", nil, wbAcct.Face)
						ui.Column(c).Grow(1).Gap(3).ClipX().Children(func() {
							ui.Text(c, firstNonEmptyStrUI(wbAcct.Uname, "微博用户")).FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
							ui.Text(c, uidLine+" · 登录方式：粘贴 Cookie").
								FontSize(13).TextColor(ColorTextMuted).SingleLine()
						})
					})

					ui.Text(c, "自动续期：不可用，粘贴的 Cookie 不续期；过期后请重新粘贴").
						FontSize(12).TextColor(ColorTextMuted).MaxLines(2)

					ui.Row(c).Gap(8).Wrap().Children(func() {
						if OutlinePillButton(c, "重新粘贴", "重新粘贴微博Cookie").Clicked() {
							a.OpenLoginForSite("weibo")
						}
						if DangerOutlinePillButton(c, "退出登录", "退出微博登录").Clicked() {
							a.ConfirmLogoutSite = "weibo"
							a.ConfirmLogoutOpen = true
						}
					})
				} else {
					ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(12).Children(func() {
						ui.Text(c, "未登录微博账号").FontSize(13).TextColor(ColorTextMuted).SingleLine()
						if AccentPillButton(c, "登录微博", "登录微博").Clicked() {
							a.OpenLoginForSite("weibo")
						}
					})
				}
			})
	})
}

func (a *App) renderDownloadSettings(c *ui.Context) {
	ui.Column(c).FillWidth().Gap(12).ClipX().Children(func() {
		concStr := strconv.Itoa(a.DraftSettings.Download.MaxConcurrentTasks)
		if concStr == "0" {
			concStr = "2"
		}
		if settingChoiceRow(c, "同时下载数", "", "选择并发任务数", &concStr, concurrencyChoices) {
			if v, err := strconv.Atoi(concStr); err == nil {
				a.DraftSettings.Download.MaxConcurrentTasks = v
				a.DraftSettings.Concurrency = v
				a.saveSettings()
			}
		}

		if a.DraftSettings.MultiPart == "" {
			a.DraftSettings.MultiPart = "all"
		}
		if settingChoiceRow(c, "多 P 视频", "", "选择多P策略", &a.DraftSettings.MultiPart, multiPartChoices) {
			a.saveSettings()
		}

		if settingSwitchRow(c, "缓存弹幕", "", "开关缓存弹幕", &a.DraftSettings.Download.SaveDanmaku) {
			a.DraftSettings.Danmaku = a.DraftSettings.Download.SaveDanmaku
			a.saveSettings()
		}
		if settingSwitchRow(c, "缓存字幕", "", "开关缓存字幕", &a.DraftSettings.Download.SaveSubtitles) {
			a.DraftSettings.Subtitles = a.DraftSettings.Download.SaveSubtitles
			a.saveSettings()
		}
		if settingSwitchRowDisabled(c, "包括 AI 字幕", "", "开关AI字幕", &a.DraftSettings.AISubtitles, !a.DraftSettings.Download.SaveSubtitles) {
			a.saveSettings()
		}
		if settingSwitchRow(c, "自动保存视频封面", "", "开关保存封面", &a.DraftSettings.Download.SaveCover) {
			a.saveSettings()
		}
	})
}

func (a *App) renderQualitySettings(c *ui.Context) {
	ui.Column(c).FillWidth().Gap(12).ClipX().Children(func() {
		if a.DraftSettings.VideoQuality == "" {
			a.DraftSettings.VideoQuality = "best"
		}
		if settingChoiceRow(c, "视频清晰度", "", "选择首选画质", &a.DraftSettings.VideoQuality, videoQualityChoices) {
			a.saveSettings()
		}

		// Codec Preference Order List (AVC / AV1 / HEVC, docs/ui.md §3.7)
		if len(a.DraftSettings.CodecOrder) == 0 {
			a.DraftSettings.CodecOrder = []string{"avc", "av1", "hevc"}
		}
		ui.Column(c).FillWidth().Padding(14, 0).BorderWidth(0, 0, 1, 0).BorderColor(ColorBorder).Gap(8).Children(func() {
			ui.Text(c, "编码偏好顺序").FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine()
			for idx, cCode := range a.DraftSettings.CodecOrder {
				i := idx
				code := cCode
				label := codecDescriptions[code]
				if label == "" {
					label = strings.ToUpper(code)
				}
				ui.Row(c).FillWidth().Padding(6, 0).AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
					ui.Text(c, fmt.Sprintf("%d. %s", i+1, label)).FontSize(13).TextColor(ColorTextMain).SingleLine().Grow(1)
					ui.Row(c).Gap(4).Children(func() {
						if OutlinePillButton(c, "↑ 上移", fmt.Sprintf("编码上移 %s", code)).Disabled(i == 0).Clicked() {
							a.DraftSettings.CodecOrder[i-1], a.DraftSettings.CodecOrder[i] = a.DraftSettings.CodecOrder[i], a.DraftSettings.CodecOrder[i-1]
							a.DraftSettings.Download.PreferredVideoCodec = a.DraftSettings.CodecOrder[0]
							a.saveSettings()
						}
						if OutlinePillButton(c, "↓ 下移", fmt.Sprintf("编码下移 %s", code)).Disabled(i+1 >= len(a.DraftSettings.CodecOrder)).Clicked() {
							a.DraftSettings.CodecOrder[i+1], a.DraftSettings.CodecOrder[i] = a.DraftSettings.CodecOrder[i], a.DraftSettings.CodecOrder[i+1]
							a.DraftSettings.Download.PreferredVideoCodec = a.DraftSettings.CodecOrder[0]
							a.saveSettings()
						}
					})
				})
			}
		})

		if a.DraftSettings.AudioQuality == "" {
			a.DraftSettings.AudioQuality = "best"
		}
		if settingChoiceRow(c, "音质", "", "选择首选音质", &a.DraftSettings.AudioQuality, audioQualityChoices) {
			a.saveSettings()
		}

		if settingSwitchRow(c, "优先 Hi-Res 无损音轨", "", "开关无损音轨", &a.DraftSettings.Download.PreferLosslessAudio) {
			a.saveSettings()
		}
		if settingSwitchRow(c, "优先杜比全景声", "", "开关杜比全景声", &a.DraftSettings.Download.PreferDolbyAudio) {
			a.saveSettings()
		}
		if settingSwitchRow(c, "打开已缓存视频时检查画质更新", "", "开关打开时检查升级", &a.DraftSettings.UpgradeCheckOnOpen) {
			a.saveSettings()
		}
	})
}

func (a *App) renderSubscriptionSettings(c *ui.Context) {
	schedStatuses := a.backend.SchedulerStatuses()
	ui.Column(c).FillWidth().Gap(12).ClipX().Children(func() {
		if settingSwitchRow(c, "定时检查更新", "只在 TRACE 打开时检查；遇到风控会自动放慢", "开关定时订阅", &a.DraftSettings.Subscription.Enabled) {
			a.DraftSettings.ScheduledCheck = a.DraftSettings.Subscription.Enabled
			a.saveSettings()
		}

		chkMinStr := strconv.Itoa(a.DraftSettings.Subscription.IntervalMinutes)
		if chkMinStr == "0" {
			chkMinStr = "60"
		}
		if settingChoiceRowDisabled(c, "检查间隔", "", "选择订阅检查间隔", &chkMinStr, subscriptionIntervalChoices, !a.DraftSettings.Subscription.Enabled) {
			if v, err := strconv.Atoi(chkMinStr); err == nil {
				a.DraftSettings.Subscription.IntervalMinutes = v
				a.DraftSettings.CheckIntervalMin = v
				a.saveSettings()
			}
		}

		// Per-site scheduler schedule & backoff lines (docs/ui.md §3.7)
		if len(schedStatuses) > 0 {
			var backoffLines []struct {
				SiteID string
				Text   string
			}
			var timeLines []string
			for _, siteID := range site.All() {
				st, ok := schedStatuses[string(siteID)]
				if !ok {
					continue
				}
				if strings.TrimSpace(st.BackoffSummary) != "" {
					backoffLines = append(backoffLines, struct {
						SiteID string
						Text   string
					}{SiteID: string(siteID), Text: strings.TrimSpace(st.BackoffSummary)})
				}
				if st.LastCheckAt != "" || st.NextCheckAt != "" {
					siteLabel := site.DisplayName(string(siteID))
					lastTxt := firstNonEmptyStrUI(st.LastCheckAt, "尚未检查")
					nextTxt := firstNonEmptyStrUI(st.NextCheckAt, "未排程")
					timeLines = append(timeLines, fmt.Sprintf("%s：上次检查 %s · 下次检查 %s", siteLabel, lastTxt, nextTxt))
				}
			}
			if len(timeLines) > 0 || len(backoffLines) > 0 {
				ui.Column(c).FillWidth().Padding(10, 0).BorderWidth(0, 0, 1, 0).BorderColor(ColorBorder).Gap(6).ClipX().Children(func() {
					for _, tl := range timeLines {
						line := tl
						ui.Text(c, line).FontSize(12).TextColor(ColorTextMuted).SingleLine()
					}
					for _, bl := range backoffLines {
						item := bl
						ui.Text(c, item.Text).
							Label("退避状态：" + item.SiteID).
							FontSize(12).
							FontWeight(600).
							TextColor(ColorDotYellow).
							MaxLines(2)
					}
				})
			}
		}

		if settingSwitchRow(c, "开播状态检查", "只在有订阅开启开播自动录制时运行", "开关直播轮询", &a.DraftSettings.Live.PollEnabled) {
			a.saveSettings()
		}

		liveMinStr := strconv.Itoa(a.DraftSettings.LiveCheckIntervalMin)
		if liveMinStr == "0" {
			liveMinStr = "3"
		}
		if settingChoiceRow(c, "开播检查间隔", liveIntervalNote(a.checkPlanRows()), "选择直播轮询间隔", &liveMinStr, liveIntervalChoices) {
			if v, err := strconv.Atoi(liveMinStr); err == nil {
				a.DraftSettings.LiveCheckIntervalMin = v
				a.DraftSettings.Live.PollIntervalSeconds = v * 60
				a.saveSettings()
			}
		}
		renderLiveCheckRules(c, a.DraftSettings.Live.PollIntervalSeconds)
	})
}

func (a *App) renderStorageSettings(c *ui.Context) {
	diag := a.diagnosticsSnapshotCached()
	loc := a.backend.Locations()
	stats := a.libraryStatistics
	editionLabel := "正式版 (stable)"
	if diag.Channel == "dev" {
		editionLabel = "开发版 (trace-dev)"
	}

	rebuilding := a.isPending("storage-rebuild")
	migrating := a.isPending("storage-migrate")
	addingRoot := a.isPending("storage-add-root")

	ui.Column(c).FillWidth().Gap(12).ClipX().Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Children(func() {
			ui.Box(c)
			ui.Box(c).Padding(2, 10).Radius(999).Background(ColorAccentBg).Children(func() {
				ui.Text(c, editionLabel).FontSize(12).FontWeight(600).TextColor(ColorAccent).SingleLine()
			})
		})

		// Directory overview rows (InstallDir, DataDir, CacheDir, Library usage)
		ui.Column(c).FillWidth().Padding(14, 0).BorderWidth(0, 0, 1, 0).BorderColor(ColorBorder).Gap(8).ClipX().Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
				ui.Column(c).Grow(1).Gap(2).ClipX().Children(func() {
					ui.Text(c, "安装位置").FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
					ui.Text(c, firstNonEmptyStrUI(loc.InstallDir, ".")).FontSize(12).TextColor(ColorTextMuted).SingleLine()
				})
				if OutlinePillButton(c, "打开文件夹", "打开安装目录").Clicked() {
					if err := a.backend.Desktop().OpenPath(firstNonEmptyStrUI(loc.InstallDir, ".")); err != nil {
						a.showError(err)
					}
				}
			})
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
				ui.Column(c).Grow(1).Gap(2).ClipX().Children(func() {
					ui.Text(c, "数据目录").FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
					ui.Text(c, diag.DataDir).FontSize(12).TextColor(ColorTextMuted).SingleLine()
				})
				if OutlinePillButton(c, "打开数据文件夹", "打开数据文件夹").Clicked() {
					if err := a.backend.OpenDataFolder("data"); err != nil {
						a.showError(err)
					}
				}
			})
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
				ui.Column(c).Grow(1).Gap(2).ClipX().Children(func() {
					ui.Text(c, fmt.Sprintf("缓存位置（已缓存 %d 项 · %s）", stats.Items, FormatBytes(stats.Bytes))).
						FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
					ui.Text(c, diag.CacheDir).FontSize(12).TextColor(ColorTextMuted).SingleLine()
				})
				ui.Row(c).Gap(6).Wrap().Children(func() {
					if OutlinePillButton(c, "打开缓存文件夹", "打开缓存文件夹").Clicked() {
						if err := a.backend.OpenDataFolder("cache"); err != nil {
							a.showError(err)
						}
					}
					if OutlinePillButton(c, "打开日志文件夹", "打开日志文件夹").Clicked() {
						if err := a.backend.OpenDataFolder("logs"); err != nil {
							a.showError(err)
						}
					}
					rebuildText := "重建索引"
					if rebuilding {
						rebuildText = "正在重建…"
					}
					if OutlinePillButton(c, rebuildText, "重建索引").Disabled(rebuilding).Clicked() && !rebuilding {
						backend := a.backend
						requestGuarded(a, "storage-rebuild", nil, func(context.Context) (library.RebuildReport, error) {
							return backend.RebuildLibraryIndex()
						}, func(rep library.RebuildReport) {
							a.RefreshData()
							a.BannerInfo = fmt.Sprintf("索引重建完成：共 %d 条内容", rep.Indexed)
						}, func(err error) {
							a.showError(err)
						})
					}
				})
			})
		})

		// Change Cache Directory
		ui.Column(c).FillWidth().Padding(14, 0).BorderWidth(0, 0, 1, 0).BorderColor(ColorBorder).Gap(8).ClipX().Children(func() {
			ui.Text(c, "缓存保存位置").FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine()
			ui.Text(c, "只影响之后的缓存。").FontSize(12).TextColor(ColorTextMuted).SingleLine()
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).ClipX().Children(func() {
				ui.TextInput(c, &a.CacheDirInput).Placeholder("新缓存目录路径").Label("缓存目录输入框").Grow(1)
				migrateText := "应用缓存目录"
				if migrating {
					migrateText = "正在迁移…"
				}
				if AccentPillButton(c, migrateText, "应用缓存目录").Disabled(migrating).Clicked() && !migrating {
					targetDir := a.CacheDirInput
					moveFiles := a.MoveExistingFiles
					backend := a.backend
					requestGuarded(a, "storage-migrate", nil, func(context.Context) (library.RebuildReport, error) {
						return backend.MigrateCacheDir(targetDir, moveFiles)
					}, func(rep library.RebuildReport) {
						a.RefreshData()
						a.BannerInfo = fmt.Sprintf("已切换缓存目录，索引 %d 项", rep.Indexed)
					}, func(err error) {
						a.showError(err)
					})
				}
			})
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Children(func() {
				ui.Text(c, "同时迁移已有缓存文件（关闭则将旧缓存留在原处继续访问）").FontSize(12).TextColor(ColorTextMuted).SingleLine()
				ui.Switch(c, &a.MoveExistingFiles).Label("迁移已有文件开关")
			})
		})

		// Add Existing Cache Folder
		ui.Column(c).FillWidth().Padding(14, 0).BorderWidth(0, 0, 1, 0).BorderColor(ColorBorder).Gap(8).ClipX().Children(func() {
			ui.Text(c, "添加已有的缓存文件夹").FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine()
			ui.Text(c, "扫描后接入媒体库与主页，文件留在原处。").FontSize(12).TextColor(ColorTextMuted).MaxLines(2)
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).ClipX().Children(func() {
				ui.TextInput(c, &a.AddLibraryRootInput).
					Placeholder("输入已有缓存根目录或 trace-data 文件夹路径").
					Label("添加已有文件夹输入框").
					Grow(1)
				addText := "从已有文件夹添加"
				if addingRoot {
					addText = "正在扫描…"
				}
				if AccentOutlinePillButton(c, addText, "从已有文件夹添加").Disabled(addingRoot).Clicked() && !addingRoot {
					rootPath := a.AddLibraryRootInput
					backend := a.backend
					requestGuarded(a, "storage-add-root", nil, func(context.Context) (library.AddRootReport, error) {
						return backend.AddLibraryRoot(rootPath)
					}, func(rep library.AddRootReport) {
						a.AddLibraryRootInput = ""
						a.RefreshData()
						a.BannerInfo = fmt.Sprintf("接入了 %d 条，跳过 %d 条（已存在）", rep.Added, rep.Skipped)
					}, func(err error) {
						a.showError(err)
					})
				}
			})
		})
	})
}

func (a *App) renderLogsSettings(c *ui.Context) {
	now := c.Now()
	a.refreshRuntimeLogs(now)
	if wait := a.runtimeLogsNextRead.Sub(now); wait > 0 {
		c.After(wait)
	}
	diag := a.runtimeDiagnostics
	logLines := a.runtimeLogLines

	ui.Column(c).FillWidth().Gap(14).ClipX().Children(func() {
		// Top: Background Status
		ui.Column(c).FillWidth().Padding(14, 0).BorderWidth(0, 0, 1, 0).BorderColor(ColorBorder).Gap(8).ClipX().Children(func() {
			ui.Text(c, "后台状态").FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()

			subStatus := firstNonEmptyStrUI(diag.SubscriptionCheckStatus, "空闲")
			if a.isPending("check-updates") {
				subStatus = fmt.Sprintf("正在检查更新 %d / %d", a.CheckDone, a.CheckTotal)
			}
			statusRows := [4]struct {
				Title string
				Value string
			}{
				{"索引重建", firstNonEmptyStrUI(diag.IndexStatus, "正常")},
				{"检查更新", subStatus},
				{"自动缓存", firstNonEmptyStrUI(diag.AutoCacheStatus, "空闲")},
				{"直播录制", firstNonEmptyStrUI(diag.LiveRecordStatus, "空闲")},
			}
			for _, sr := range statusRows {
				row := sr
				ui.Row(c).FillWidth().Padding(6, 0).AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
					ui.Text(c, row.Title).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
					ui.Text(c, row.Value).FontSize(13).TextColor(ColorTextMuted).SingleLine()
				})
			}
		})

		// Bottom: Recent Runtime Logs (up to 500 sanitized lines, virtualized monospace list)
		ui.Column(c).FillWidth().Padding(14, 0).BorderWidth(0, 0, 1, 0).BorderColor(ColorBorder).Gap(10).ClipX().Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Wrap().Children(func() {
				ui.Text(c, fmt.Sprintf("最近运行日志（已脱敏 · 共 %d 行）", len(logLines))).FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					if OutlinePillButton(c, "打开日志文件夹", "日志页打开日志文件夹").Clicked() {
						if err := a.backend.OpenDataFolder("logs"); err != nil {
							a.showError(err)
						}
					}
					if OutlinePillButton(c, "复制", "复制运行日志").Clicked() {
						c.WriteClipboard(strings.Join(logLines, "\n"))
						c.Toast("已复制运行日志")
						a.BannerInfo = "已复制运行日志"
					}
				})
			})

			if a.runtimeLogError != "" {
				ui.Text(c, a.runtimeLogError).Label("运行日志读取错误").FontSize(13).TextColor(ColorError).MaxLines(3)
			}
			ui.Box(c).FillWidth().Height(260).Padding(10, 12).Radius(10).Background(ColorBgMain).Border(1, ColorBorder).Clip().Children(func() {
				if len(logLines) == 0 {
					if a.runtimeLogError == "" {
						ui.Text(c, "暂无运行日志记录").FontSize(12).Font("monospace").TextColor(ColorTextMuted).SingleLine()
					}
					return
				}
				ui.List(c, &a.RuntimeLogsListState, len(logLines), func(i int) {
					if i < 0 || i >= len(logLines) {
						return
					}
					ui.Box(c).FillWidth().Padding(0, 0, 4, 0).ClipX().Children(func() {
						ui.Text(c, logLines[i]).FontSize(12).Font("monospace").TextColor(ColorTextMain).MaxLines(2)
					})
				}).Label("运行日志列表").Fill()
			})
		})
	})
}
