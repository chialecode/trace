package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/update"
	"github.com/egoist/mygo/ui"
)

// UpdaterAPI is what the interface reads and asks of the updates of TRACE
// (internal/update.Controller in the app, a fake in tests).
type UpdaterAPI interface {
	State() update.State
	CheckNow(ctx context.Context)
	Download(ctx context.Context)
	DismissBanner()
	DismissUpdatedNotice()
	RestartPrompt() string
	Restart()
	PrefsChanged()
}

// BindUpdater connects the interface to the updates of the running build.
func (a *App) BindUpdater(u UpdaterAPI) {
	a.updater = u
}

func (a *App) updateState() (update.State, bool) {
	if a.updater == nil {
		return update.State{}, false
	}
	return a.updater.State(), true
}

// versionLine is "版本 0.1.1（正式版）".
func versionLine(build appdirs.Build, channel string) string {
	return fmt.Sprintf("版本 %s（%s）", build.Version, appdirs.ChannelLabel(channel))
}

// buildLine is "提交 1a2b3c4 · 构建于 2026-11-09 15:30 UTC".
func buildLine(build appdirs.Build) string {
	commit := build.Commit
	if commit == "" {
		commit = "未知"
	}
	built := "未知"
	if t, err := time.Parse(time.RFC3339, build.BuildTime); err == nil {
		built = t.UTC().Format("2006-01-02 15:04") + " UTC"
	}
	return fmt.Sprintf("提交 %s · 构建于 %s", commit, built)
}

// renderUpdateBanner is the one line below the top bar that tells about an
// update without interrupting: a new version that is ready (or available,
// when automatic download is off), or, once after an update, the version
// that now runs.
func (a *App) renderUpdateBanner(c *ui.Context) {
	st, ok := a.updateState()
	if !ok {
		return
	}
	var text, notes string
	var primary, primaryLabel string
	dismiss := func() {}
	switch {
	case st.Phase == update.PhaseReady && st.Release != nil && !st.BannerDismissed:
		text = fmt.Sprintf("新版本 %s 已就绪", st.Release.Version)
		notes = st.Release.Notes
		primary, primaryLabel = "重启以更新", "重启以更新到新版本"
		dismiss = a.updater.DismissBanner
	case st.Phase == update.PhaseAvailable && st.Release != nil && !st.BannerDismissed && !st.Prefs.AutoDownload:
		text = fmt.Sprintf("有新版本 %s", st.Release.Version)
		notes = st.Release.Notes
		primary, primaryLabel = "下载并安装", "下载并安装新版本"
		dismiss = a.updater.DismissBanner
	case st.UpdatedTo != "":
		text = fmt.Sprintf("已更新到 %s", st.UpdatedTo)
		notes = st.UpdatedNotes
		dismiss = a.updater.DismissUpdatedNotice
	default:
		a.UpdateNotesOpen = false
		return
	}
	ui.Column(c).Label("更新提示").FillWidth().Padding(8, 12).Radius(10).Background(ColorInfoBlueBg).Gap(6).ClipX().Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(10).ClipX().Children(func() {
			DrawIcon(c, "check-circle", 16, ColorInfoBlue, 1.8)
			ui.Text(c, text).FontSize(13).FontWeight(600).TextColor(ColorInfoBlue).SingleLine()
			if strings.TrimSpace(notes) != "" {
				label := "更新内容"
				if a.UpdateNotesOpen {
					label = "收起更新内容"
				}
				link := ui.ButtonBase(c).Label(label).Padding(2, 4)
				link.Children(func() {
					ui.Text(c, label).FontSize(13).TextColor(ColorAccent).SingleLine()
				})
				if link.Clicked() {
					a.UpdateNotesOpen = !a.UpdateNotesOpen
				}
			}
			ui.Box(c).Grow(1)
			if primary != "" && AccentOutlinePillButton(c, primary, primaryLabel).Clicked() {
				a.onUpdatePrimary(st)
			}
			closeBtn := ui.ButtonBase(c).Label("关闭更新提示").Tooltip("关闭").Size(26, 26).Radius(999).Center()
			if closeBtn.Hovered() {
				closeBtn.Background(ColorHover)
			}
			closeBtn.Children(func() { DrawIcon(c, "close", 14, ColorTextMuted, 1.8) })
			if closeBtn.Clicked() {
				a.UpdateNotesOpen = false
				dismiss()
			}
		})
		if a.UpdateNotesOpen && strings.TrimSpace(notes) != "" {
			ui.Column(c).Label("更新内容").FillWidth().Padding(4, 26).Gap(2).ClipX().Children(func() {
				for _, line := range releaseNoteLines(notes) {
					ui.Text(c, line).FontSize(13).TextColor(ColorTextMain).MaxLines(3)
				}
			})
		}
	})
}

// releaseNoteLines turns the Markdown of a CHANGELOG section into plain
// lines.
func releaseNoteLines(notes string) []string {
	var out []string
	for _, line := range strings.Split(strings.ReplaceAll(notes, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		line = strings.TrimLeft(line, "#")
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") {
			line = "· " + strings.TrimSpace(line[2:])
		}
		out = append(out, line)
		if len(out) >= 40 {
			break
		}
	}
	return out
}

func (a *App) onUpdatePrimary(st update.State) {
	switch st.Phase {
	case update.PhaseReady:
		a.requestRestartToUpdate()
	case update.PhaseAvailable:
		u := a.updater
		go u.Download(context.Background())
	}
}

// requestRestartToUpdate restarts at once when nothing would be paused,
// and otherwise says first what the restart pauses.
func (a *App) requestRestartToUpdate() {
	if a.updater == nil {
		return
	}
	if prompt := a.updater.RestartPrompt(); prompt != "" {
		a.RestartConfirmText = prompt
		a.RestartConfirmOpen = true
		return
	}
	a.updater.Restart()
}

func (a *App) renderRestartConfirmOverlay(c *ui.Context, w, h float32) {
	if c.Shortcut(0, ui.KeyEscape) {
		a.RestartConfirmOpen = false
		return
	}
	ui.Overlay(c, func() {
		ui.Column(c).Absolute().Top(0).Left(0).Size(w, h).Background(ui.RGBA(15, 20, 25, 0.55)).Center().Children(func() {
			ui.Column(c).Label("重启以更新确认对话框").Width(min(float32(460), w-40)).Padding(22).Radius(16).Background(ColorBgMain).Gap(12).Children(func() {
				ui.Text(c, "重启以更新？").FontSize(18).FontWeight(600).TextColor(ColorTextMain)
				ui.Text(c, a.RestartConfirmText).FontSize(13).TextColor(ColorTextMuted).MaxLines(3)
				ui.Row(c).FillWidth().Justify(ui.End).Gap(8).Children(func() {
					if OutlinePillButton(c, "取消", "取消重启以更新").Clicked() {
						a.RestartConfirmOpen = false
					}
					if AccentOutlinePillButton(c, "重启以更新", "确认重启以更新").Clicked() {
						a.RestartConfirmOpen = false
						a.updater.Restart()
					}
				})
			})
		})
	})
}

const (
	releaseUpdateMethodLine = "打开 TRACE 时每 24 小时自动检查一次（可在下面关闭），也可以立即检查"
	devUpdateMethodLine     = "开发版不会自己联网检查；Agent 打包后会自动安装，正在运行的 TRACE Dev 会先退出，装完在托盘打开"
)

func (a *App) currentBuildPresentation() (appdirs.Build, string, string, update.State, bool) {
	diag := a.backend.DiagnosticsSnapshot()
	build := appdirs.BuildInfo()
	if diag.Version != "" {
		build.Version, build.Commit, build.BuildTime = diag.Version, diag.Commit, diag.BuildTime
	}
	st, hasUpdater := a.updateState()
	if hasUpdater && st.CurrentVersion != "" {
		build.Version = st.CurrentVersion
	}
	channel := diag.Channel
	if hasUpdater && st.Channel != "" {
		channel = st.Channel
	}
	name := diag.DisplayName
	if hasUpdater && st.Channel != "" {
		name = appdirs.DisplayNameForChannel(st.Channel)
	}
	if strings.TrimSpace(name) == "" {
		name = appdirs.DisplayNameForChannel(channel)
	}
	return build, channel, name, st, hasUpdater
}

// renderAboutSettings is "设置 › 关于": version, diagnostics summary, license
// notice, and a direct link to "设置 › 更新".
func (a *App) renderAboutSettings(c *ui.Context) {
	diag := a.backend.DiagnosticsSnapshot()
	build, channel, name, _, _ := a.currentBuildPresentation()
	ui.Column(c).FillWidth().Gap(4).ClipX().Children(func() {
		ui.Column(c).Label("版本信息").FillWidth().Padding(14, 0).BorderWidth(0, 0, 1, 0).BorderColor(ColorBorder).Gap(6).ClipX().Children(func() {
			ui.Text(c, name).FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
			ui.Row(c).Label("当前版本").FillWidth().ClipX().Children(func() {
				ui.Text(c, versionLine(build, channel)).FontSize(13).TextColor(ColorTextMain).SingleLine()
			})
			ui.Row(c).Label("提交与构建时间").FillWidth().ClipX().Children(func() {
				ui.Text(c, buildLine(build)).FontSize(13).TextColor(ColorTextMuted).SingleLine()
			})
		})

		ui.Row(c).FillWidth().Padding(12, 0).BorderWidth(0, 0, 1, 0).BorderColor(ColorBorder).AlignItems(ui.Center).ClipX().Children(func() {
			link := ui.ButtonBase(c).Label("版本与更新见‘更新’").Padding(4, 6).Radius(6)
			if link.Hovered() {
				link.Background(ColorHover)
			}
			link.Children(func() {
				ui.Text(c, "版本与更新见‘更新’").FontSize(13).TextColor(ColorAccent).SingleLine()
			})
			if link.Clicked() {
				a.SettingsCategory = "update"
			}
		})

		ui.Column(c).FillWidth().Padding(14, 0).Gap(6).ClipX().Children(func() {
			ui.Text(c, fmt.Sprintf("本地媒体库：%d 项 · 订阅：%d 个 · 缓存任务：%d 个", diag.LibraryCount, diag.SubscriptionCnt, diag.TaskCount)).
				FontSize(13).TextColor(ColorTextMuted).SingleLine()
			ui.Text(c, "第三方开源许可声明随安装包附于 THIRD-PARTY-NOTICES.txt。").
				FontSize(12).TextColor(ColorTextMuted).SingleLine()
		})
	})
}

// renderUpdateSettings is "设置 › 更新" (DEC-051 ①, Q-018 #1): flat 1px-divider
// layout showing current version, how this channel updates, channel-specific
// controls or local dev installation state, latest 20 check records, and
// latest 50 update records.
func (a *App) renderUpdateSettings(c *ui.Context) {
	build, channel, name, st, hasUpdater := a.currentBuildPresentation()
	effChannel := appdirs.EffectiveChannel(channel)
	isRelease := hasUpdater && st.Enabled
	isDev := !isRelease && effChannel == "dev"
	hist := a.backend.GetUpdateHistory()

	ui.Column(c).FillWidth().Gap(4).ClipX().Children(func() {
		// 1. Current version block
		ui.Column(c).Label("版本信息").FillWidth().Padding(14, 0).BorderWidth(0, 0, 1, 0).BorderColor(ColorBorder).Gap(6).ClipX().Children(func() {
			ui.Text(c, name).FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
			ui.Row(c).Label("当前版本").FillWidth().ClipX().Children(func() {
				ui.Text(c, versionLine(build, channel)).FontSize(13).TextColor(ColorTextMain).SingleLine()
			})
			ui.Row(c).Label("提交与构建时间").FillWidth().ClipX().Children(func() {
				ui.Text(c, buildLine(build)).FontSize(13).TextColor(ColorTextMuted).SingleLine()
			})
		})

		// 2. One-sentence update mechanism description
		methodSentence := "这个构建没有联网更新"
		switch {
		case isRelease:
			methodSentence = releaseUpdateMethodLine
		case isDev:
			methodSentence = devUpdateMethodLine
		}
		ui.Row(c).Label("更新方式").FillWidth().Padding(12, 0).BorderWidth(0, 0, 1, 0).BorderColor(ColorBorder).ClipX().Children(func() {
			ui.Text(c, methodSentence).FontSize(13).TextColor(ColorTextMuted).MaxLines(3)
		})

		// 3. Channel-specific section
		if isRelease {
			a.renderReleaseUpdateSection(c, st, hist.Checks)
		} else if isDev {
			a.renderDevUpdateSection(c, build)
		}

		// 4. Check history (latest 20) & update history (latest 50)
		a.renderUpdateCheckHistorySection(c, isDev, hist.Checks)
		a.renderAppUpdateHistorySection(c, hist.Updates)
	})
}

func (a *App) renderReleaseUpdateSection(c *ui.Context, st update.State, checks []update.CheckRecord) {
	ui.Row(c).Label("检查更新").FillWidth().Padding(12, 0).BorderWidth(0, 0, 1, 0).BorderColor(ColorBorder).AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(12).ClipX().Children(func() {
		ui.Column(c).Grow(1).Gap(4).ClipX().Children(func() {
			lastLine, lastIsErr := formatLastCheckLine(st, checks)
			lastColor := ColorTextMain
			if lastIsErr {
				lastColor = ColorError
			}
			ui.Row(c).Label("上次检查").FillWidth().ClipX().Children(func() {
				ui.Text(c, lastLine).FontSize(13).TextColor(lastColor).MaxLines(2)
			})

			nextLine := formatNextCheckLine(st, a.DraftSettings.Updates.AutoCheck)
			ui.Row(c).Label("下次检查时间").FillWidth().ClipX().Children(func() {
				ui.Text(c, nextLine).FontSize(12).TextColor(ColorTextMuted).SingleLine()
			})

			if extra, extraErr := formatActiveUpdateProgressLine(st, lastLine); extra != "" {
				col := ColorTextMuted
				if extraErr {
					col = ColorError
				}
				ui.Row(c).Label("更新状态").FillWidth().ClipX().Children(func() {
					ui.Text(c, extra).FontSize(12).TextColor(col).MaxLines(2)
				})
			}
		})

		busy := st.Phase == update.PhaseChecking || st.Phase == update.PhaseDownloading
		switch {
		case st.Phase == update.PhaseReady:
			if AccentOutlinePillButton(c, "重启以更新", "更新页重启以更新").Clicked() {
				a.requestRestartToUpdate()
			}
		case st.Phase == update.PhaseAvailable:
			if AccentOutlinePillButton(c, "下载并安装", "更新页下载并安装").Clicked() {
				u := a.updater
				go u.Download(context.Background())
			}
		default:
			label := "立即检查"
			if busy {
				label = "正在检查…"
			}
			btn := OutlinePillButton(c, label, "立即检查按钮")
			if btn.Clicked() && !busy {
				u := a.updater
				go u.CheckNow(context.Background())
			}
		}
	})

	draft := a.DraftSettings.Updates
	if settingSwitchRow(c, "自动检查更新", "", "自动检查更新开关", &draft.AutoCheck) {
		a.DraftSettings.Updates.AutoCheck = draft.AutoCheck
		a.saveSettings()
		a.updater.PrefsChanged()
	}
	if settingSwitchRow(c, "自动下载更新", "", "自动下载更新开关", &draft.AutoDownload) {
		a.DraftSettings.Updates.AutoDownload = draft.AutoDownload
		a.saveSettings()
		a.updater.PrefsChanged()
	}
}

func (a *App) renderDevUpdateSection(c *ui.Context, runningBuild appdirs.Build) {
	installedVer := strings.TrimSpace(a.backend.InstalledDevVersion())
	ui.Column(c).Label("本机安装的开发版区域").FillWidth().Padding(12, 0).BorderWidth(0, 0, 1, 0).BorderColor(ColorBorder).Gap(4).ClipX().Children(func() {
		line := "本机安装的开发版：未在本机安装"
		if installedVer != "" {
			line = "本机安装的开发版：" + installedVer
		}
		ui.Row(c).Label("本机安装的开发版").FillWidth().ClipX().Children(func() {
			ui.Text(c, line).FontSize(13).TextColor(ColorTextMain).SingleLine()
		})
		runningVer := strings.TrimSpace(runningBuild.Version)
		if installedVer != "" && runningVer != "" && runningVer != appdirs.UnversionedBuild && update.Compare(installedVer, runningVer) > 0 {
			ui.Row(c).Label("开发版重启生效提示").FillWidth().ClipX().Children(func() {
				ui.Text(c, "已安装更新的版本，重启 TRACE Dev 后生效").FontSize(12).TextColor(ColorInfoBlue).MaxLines(2)
			})
		}
	})
}

func (a *App) renderUpdateCheckHistorySection(c *ui.Context, isDev bool, checks []update.CheckRecord) {
	ui.Column(c).Label("检查记录区域").FillWidth().Padding(14, 0).BorderWidth(0, 0, 1, 0).BorderColor(ColorBorder).Gap(8).ClipX().Children(func() {
		ui.Text(c, "检查记录").FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine()
		if isDev && len(checks) == 0 {
			ui.Row(c).Label("检查记录空提示").FillWidth().ClipX().Children(func() {
				ui.Text(c, "开发版不会自己联网检查更新，无检查记录").FontSize(12).TextColor(ColorTextMuted).SingleLine()
			})
			return
		}
		if len(checks) == 0 {
			ui.Row(c).Label("检查记录空提示").FillWidth().ClipX().Children(func() {
				ui.Text(c, "暂无检查记录").FontSize(12).TextColor(ColorTextMuted).SingleLine()
			})
			return
		}
		limit := min(len(checks), update.MaxCheckRecords)
		for i := 0; i < limit; i++ {
			chk := checks[i]
			resCol := ColorTextMain
			if strings.HasPrefix(chk.Result, "检查失败") || strings.HasPrefix(chk.Result, "检查更新失败") {
				resCol = ColorError
			}
			meta := fmt.Sprintf("%s · %s", update.TriggerLabel(chk.Trigger), formatUpdateHistoryTime(chk.At))
			ui.Row(c).FillWidth().Padding(4, 0).AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(12).ClipX().Children(func() {
				ui.Text(c, chk.Result).FontSize(13).TextColor(resCol).MaxLines(2).Grow(1)
				ui.Text(c, meta).FontSize(12).TextColor(ColorTextMuted).SingleLine().Shrink(0)
			})
		}
	})
}

func (a *App) renderAppUpdateHistorySection(c *ui.Context, updates []update.UpdateRecord) {
	ui.Column(c).Label("更新记录区域").FillWidth().Padding(14, 0).Gap(8).ClipX().Children(func() {
		ui.Text(c, "更新记录").FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine()
		if len(updates) == 0 {
			ui.Row(c).Label("更新记录空提示").FillWidth().ClipX().Children(func() {
				ui.Text(c, "暂无更新记录").FontSize(12).TextColor(ColorTextMuted).SingleLine()
			})
			return
		}
		limit := min(len(updates), update.MaxUpdateRecords)
		for i := 0; i < limit; i++ {
			upd := updates[i]
			versionSpan := fmt.Sprintf("从 %s 到 %s", upd.FromVersion, upd.ToVersion)
			meta := fmt.Sprintf("%s · %s", update.MethodLabel(upd.Method), formatUpdateHistoryTime(upd.At))
			ui.Row(c).FillWidth().Padding(4, 0).AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(12).ClipX().Children(func() {
				ui.Text(c, versionSpan).FontSize(13).TextColor(ColorTextMain).SingleLine().Grow(1)
				ui.Text(c, meta).FontSize(12).TextColor(ColorTextMuted).SingleLine().Shrink(0)
			})
		}
	})
}

func formatUpdateHistoryTime(raw string) string {
	if t, err := time.Parse(time.RFC3339, strings.TrimSpace(raw)); err == nil {
		return t.Local().Format("2006-01-02 15:04")
	}
	return strings.TrimSpace(raw)
}

func formatLastCheckLine(st update.State, checks []update.CheckRecord) (string, bool) {
	var checkTime, result string
	if !st.LastCheck.IsZero() {
		checkTime = st.LastCheck.Local().Format("2006-01-02 15:04")
	} else if len(checks) > 0 {
		checkTime = formatUpdateHistoryTime(checks[0].At)
	}
	switch {
	case strings.TrimSpace(st.LastCheckResult) != "":
		result = strings.TrimSpace(st.LastCheckResult)
	case len(checks) > 0 && strings.TrimSpace(checks[0].Result) != "":
		result = strings.TrimSpace(checks[0].Result)
	case strings.TrimSpace(st.Message) != "":
		result = strings.TrimSpace(st.Message)
	}
	isErr := strings.HasPrefix(result, "检查失败") || strings.HasPrefix(result, "检查更新失败") || (st.MessageError && result == strings.TrimSpace(st.Message) && result != "")
	switch {
	case checkTime == "" && result == "":
		return "上次检查：还没有检查过", false
	case checkTime != "" && result != "":
		return fmt.Sprintf("上次检查：%s · %s", checkTime, result), isErr
	case checkTime != "":
		return "上次检查：" + checkTime, false
	default:
		return "上次检查：" + result, isErr
	}
}

func formatNextCheckLine(st update.State, draftAutoCheck bool) string {
	if st.Phase == update.PhaseReady && st.Release != nil {
		return fmt.Sprintf("下次检查：新版本 %s 已就绪，重启后生效", st.Release.Version)
	}
	if !draftAutoCheck || !st.Prefs.AutoCheck {
		return "下次检查：已关闭自动检查"
	}
	if !st.NextCheck.IsZero() {
		return "下次检查：" + st.NextCheck.Local().Format("2006-01-02 15:04")
	}
	if !st.LastCheck.IsZero() {
		return "下次检查：" + st.LastCheck.Add(update.CheckInterval).Local().Format("2006-01-02 15:04")
	}
	return "下次检查：打开后每 24 小时自动检查"
}

func formatActiveUpdateProgressLine(st update.State, lastLine string) (string, bool) {
	switch st.Phase {
	case update.PhaseChecking, update.PhaseDownloading, update.PhaseReady:
		return updateStatusText(st), false
	case update.PhaseAvailable:
		if st.MessageError && st.Message != "" {
			return st.Message, true
		}
		return updateStatusText(st), false
	}
	if st.MessageError && st.Message != "" && !strings.Contains(lastLine, st.Message) {
		return st.Message, true
	}
	return "", false
}

// updateStatusText says where the update stands, beside the button.
func updateStatusText(st update.State) string {
	switch st.Phase {
	case update.PhaseChecking:
		return "正在检查更新…"
	case update.PhaseDownloading:
		v := ""
		if st.Release != nil {
			v = " " + st.Release.Version
		}
		if st.Total > 0 {
			return fmt.Sprintf("正在下载新版本%s（%d%%）", v, st.Downloaded*100/st.Total)
		}
		return "正在下载新版本" + v
	case update.PhaseReady:
		if st.Release != nil {
			return fmt.Sprintf("新版本 %s 已就绪，重启以更新；不重启时下次启动就是新版本", st.Release.Version)
		}
	case update.PhaseAvailable:
		if st.Message != "" && st.MessageError {
			return st.Message
		}
		if st.Release != nil {
			return fmt.Sprintf("有新版本 %s", st.Release.Version)
		}
	}
	if st.Message != "" {
		return st.Message
	}
	return "版本 " + st.CurrentVersion
}
