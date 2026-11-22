package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/remote"
	"github.com/chialecode/trace/internal/service"
	"github.com/egoist/mygo/ui"
)

// RemoteUIState is Settings › 远程 (docs/design/remote.md §7).
type RemoteUIState struct {
	CodeInput, URLInput, TokenInput, FingerprintInput string
	Manual                                            bool
	View                                              *service.RemoteView
	Err                                               string
	Info                                              string
	FetchedAt                                         time.Time
	ConfirmRemove                                     bool
}

// remoteRefreshEvery is how often the page re-reads the remote while it is
// shown; nothing is requested while it is not (remote.md §7).
const remoteRefreshEvery = 30 * time.Second

var uploadErrorKindLabels = map[string]string{
	remote.UploadErrNotFound: "找不到 rclone",
	remote.UploadErrQuota:    "网盘配额已满",
	remote.UploadErrConfig:   "rclone 配置有误",
	remote.UploadErrNetwork:  "连不上网盘",
}

func (a *App) renderRemoteSettings(c *ui.Context) {
	info, connected := a.backend.RemoteConnection()
	ui.Column(c).FillWidth().Gap(14).ClipX().Children(func() {
		if !connected {
			a.renderRemoteConnectForm(c)
			return
		}
		now := c.Now()
		pendingStatus := a.isPending("remote-status")
		if !pendingStatus && (a.Remote.FetchedAt.IsZero() || now.Sub(a.Remote.FetchedAt) >= remoteRefreshEvery) {
			a.fetchRemoteStatusAt(now)
			pendingStatus = a.isPending("remote-status")
		}
		if !pendingStatus && !a.Remote.FetchedAt.IsZero() {
			if wait := remoteRefreshEvery - now.Sub(a.Remote.FetchedAt); wait > 0 {
				c.After(wait)
			}
		}
		a.renderRemoteHeader(c, info)
		if a.Remote.Err != "" {
			renderErrorBanner(c, "远程错误", a.Remote.Err, nil)
		}
		if a.Remote.Info != "" {
			ui.Text(c, a.Remote.Info).Label("远程提示").FontSize(12).TextColor(ColorInfoBlue).MaxLines(2)
		}
		if v := a.Remote.View; v != nil {
			a.renderRemoteSync(c, *v)
			a.renderRemoteSites(c, *v, info)
			a.renderRemoteWork(c, v.Status)
			a.renderRemoteUpload(c, v.Status)
			a.renderRemoteErrors(c, v.Status)
		} else if a.Remote.Err == "" {
			ui.Text(c, "正在读取远程状态…").FontSize(13).TextColor(ColorTextMuted).SingleLine()
		}
	})
}

func (a *App) fetchRemoteStatus() {
	a.fetchRemoteStatusAt(time.Time{})
}

func (a *App) fetchRemoteStatusAt(now time.Time) {
	gen := a.remoteGen
	backend := a.backend
	isSync := a.dispatch == nil
	fetchedStamp := func() time.Time {
		if isSync && !now.IsZero() {
			return now
		}
		return time.Now()
	}
	requestGuarded(a, "remote-status", func() bool { return a.remoteGen == gen }, func(ctx context.Context) (service.RemoteView, error) {
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		return backend.RemoteStatus(ctx)
	}, func(v service.RemoteView) {
		a.Remote.View, a.Remote.Err, a.Remote.FetchedAt = &v, "", fetchedStamp()
	}, func(err error) {
		a.Remote.Err, a.Remote.FetchedAt = remoteErrorText(err), fetchedStamp()
	})
}

// remoteErrorText is the message of a failed remote request: each kind its
// own (无法连接、证书变了、令牌错、版本不同、远程拒绝 Cookie).
func remoteErrorText(err error) string {
	if err == nil {
		return ""
	}
	return userErrorText(err)
}

func (a *App) renderRemoteConnectForm(c *ui.Context) {
	ui.Column(c).Label("连接远程").FillWidth().Gap(10).ClipX().Children(func() {
		ui.Text(c, "连接远程").FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
		ui.Text(c, "远程是放在 VPS 上的 trace-server：按同步过去的订阅检查和缓存新内容、录制直播，再上传到网盘。在 VPS 上运行 trace-server init，把它打印的连接码粘贴到这里。").
			FontSize(12).TextColor(ColorTextMuted).MaxLines(4)
		if !a.Remote.Manual {
			ui.TextInput(c, &a.Remote.CodeInput).Label("远程连接码").Placeholder("trace-remote:1:…").Password().FillWidth()
		} else {
			ui.TextInput(c, &a.Remote.URLInput).Label("远程地址").Placeholder("1.2.3.4 或 https://1.2.3.4:47380").FillWidth()
			ui.TextInput(c, &a.Remote.TokenInput).Label("远程访问令牌").Placeholder("访问令牌").Password().FillWidth()
			ui.TextInput(c, &a.Remote.FingerprintInput).Label("远程证书指纹").Placeholder("证书指纹（SHA-256，64 位十六进制）").FillWidth()
		}
		ui.Row(c).Gap(8).Wrap().AlignItems(ui.Center).Children(func() {
			connecting := a.isPending("remote-connect")
			text := "连接"
			if connecting {
				text = "正在连接…"
			}
			if AccentPillButton(c, text, "连接远程").Disabled(connecting).Clicked() && !connecting {
				a.connectRemote()
			}
			other := "分别填写地址、令牌与指纹"
			if a.Remote.Manual {
				other = "粘贴连接码"
			}
			if OutlinePillButton(c, other, "切换远程连接方式").Disabled(connecting).Clicked() {
				a.Remote.Manual = !a.Remote.Manual
				a.Remote.CodeInput = ""
				a.Remote.TokenInput = ""
				a.Remote.Err = ""
			}
		})
		if a.Remote.Err != "" {
			ui.Text(c, a.Remote.Err).Label("远程连接错误").FontSize(12).TextColor(ColorError).MaxLines(4)
		}
		ui.Text(c, "只连接与连接码里的证书指纹一致的远程；令牌保存在本机的凭证里，不写进设置文件。").
			FontSize(12).TextColor(ColorTextMuted).MaxLines(2)
	})
}

func (a *App) connectRemote() {
	if a.isPending("remote-connect") {
		return
	}
	in := service.RemoteConnectInput{Code: strings.TrimSpace(a.Remote.CodeInput)}
	if a.Remote.Manual {
		in = service.RemoteConnectInput{
			URL:         strings.TrimSpace(a.Remote.URLInput),
			Token:       strings.TrimSpace(a.Remote.TokenInput),
			Fingerprint: strings.TrimSpace(a.Remote.FingerprintInput),
		}
	}
	// Clear sensitive credentials from UI state immediately upon submit.
	a.Remote.CodeInput = ""
	a.Remote.TokenInput = ""
	a.Remote.Err = ""

	gen := a.remoteGen
	backend := a.backend
	requestGuarded(a, "remote-connect", func() bool { return a.remoteGen == gen }, func(ctx context.Context) (service.RemoteView, error) {
		defer func() {
			in.Code = ""
			in.Token = ""
		}()
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		return backend.ConnectRemote(ctx, in)
	}, func(v service.RemoteView) {
		a.remoteGen++
		a.Remote = RemoteUIState{View: &v, FetchedAt: time.Now(), Info: "已连接远程"}
	}, func(err error) {
		a.Remote.Err = remoteErrorText(err)
	})
}

func (a *App) renderRemoteHeader(c *ui.Context, info service.RemoteConnectionInfo) {
	ui.Row(c).Label("远程连接信息").FillWidth().Padding(14).Radius(14).Background(ColorBgSecondary).Gap(12).AlignItems(ui.Center).ClipX().Children(func() {
		DrawIcon(c, "link", 20, ColorTextMuted, 1.6)
		ui.Column(c).Grow(1).Gap(3).ClipX().Children(func() {
			name := firstNonEmptyStrUI(info.Name, "远程")
			ui.Text(c, name).FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
			line := info.URL
			if v := a.Remote.View; v != nil && v.Status.Version != "" {
				line += " · 版本 " + v.Status.Version
				if st, err := time.Parse(time.RFC3339, v.Status.StartedAt); err == nil {
					line += " · 已运行 " + humanDuration(time.Since(st))
				}
			}
			ui.Text(c, line).FontSize(12).TextColor(ColorTextMuted).SingleLine()
			fp := info.Fingerprint
			if len(fp) > 16 {
				fp = fp[:8] + "…" + fp[len(fp)-8:]
			}
			ui.Text(c, "证书指纹 "+fp).FontSize(12).TextColor(ColorTextFaint).SingleLine()
		})
		refreshing := a.isPending("remote-status")
		text := "刷新"
		if refreshing {
			text = "正在刷新…"
		}
		if OutlinePillButton(c, text, "刷新远程状态").Disabled(refreshing).Clicked() && !refreshing {
			a.fetchRemoteStatusAt(c.Now())
		}
		if !a.Remote.ConfirmRemove {
			if DangerOutlinePillButton(c, "断开", "断开远程").Clicked() {
				a.Remote.ConfirmRemove = true
			}
		} else if DangerOutlinePillButton(c, "确认断开", "确认断开远程").Clicked() {
			if err := a.backend.RemoveRemote(); err != nil {
				a.showError(err)
			} else {
				a.remoteGen++
				a.Remote = RemoteUIState{}
			}
		}
	})
	if a.Remote.ConfirmRemove {
		ui.Text(c, "断开只删除本机保存的连接与令牌；远程照常运行，它的 Cookie 要在远程清除。").FontSize(12).TextColor(ColorTextMuted).MaxLines(2)
	}
}

func (a *App) renderRemoteSync(c *ui.Context, v service.RemoteView) {
	remoteSection(c, "订阅", func() {
		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).ClipX().Children(func() {
			subs := v.Status.Subscriptions
			state, col := "有改动未同步", ColorDotYellow
			if v.InSync {
				state, col = "已同步", ColorDotGreen
			}
			ui.Box(c).Size(8, 8).Radius(4).Background(col).Shrink(0)
			ui.Text(c, state).Label("远程订阅同步状态").FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
			detail := fmt.Sprintf("远程 %d 个订阅、%d 个账号", subs.Subscriptions, subs.Accounts)
			if subs.SyncedAt != "" {
				detail += " · 上次同步 " + shortTime(subs.SyncedAt)
			}
			ui.Text(c, detail).FontSize(12).TextColor(ColorTextMuted).Grow(1).SingleLine()
			syncing := a.isPending("remote-sync")
			text := "同步订阅"
			if syncing {
				text = "正在同步…"
			}
			if AccentPillButton(c, text, "同步订阅到远程").Disabled(syncing).Clicked() && !syncing {
				a.syncRemoteSubscriptions()
			}
		})
		ui.Text(c, "同步用本机的订阅、缓存规则与画质设置替换远程的；远程只缓存同步之后的新内容，规则里的“全部缓存”不在远程执行。").
			FontSize(12).TextColor(ColorTextMuted).MaxLines(3)
		for _, n := range v.Status.Notes {
			ui.Text(c, n).FontSize(12).TextColor(ColorTextMuted).MaxLines(2)
		}
	})
}

func (a *App) syncRemoteSubscriptions() {
	gen := a.remoteGen
	backend := a.backend
	requestGuarded(a, "remote-sync", func() bool { return a.remoteGen == gen }, func(ctx context.Context) (remote.SubscriptionsInfo, error) {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		return backend.SyncSubscriptionsToRemote(ctx)
	}, func(info remote.SubscriptionsInfo) {
		a.Remote.Info = fmt.Sprintf("已同步 %d 个订阅、%d 个账号", info.Subscriptions, info.Accounts)
		a.Remote.Err = ""
		a.fetchRemoteStatus()
	}, func(err error) { a.Remote.Err = remoteErrorText(err) })
}

func (a *App) renderRemoteSites(c *ui.Context, v service.RemoteView, info service.RemoteConnectionInfo) {
	remoteSection(c, "登录", func() {
		pushing := a.isPending("remote-push")
		clearing := a.isPending("remote-clear")
		for _, s := range v.Status.Sites {
			row := s
			pending := false
			for _, p := range info.PendingCookies {
				pending = pending || p == row.Site
			}
			ui.Row(c).Label("远程站点：" + row.Site).FillWidth().Gap(10).AlignItems(ui.Center).ClipX().Children(func() {
				ui.Text(c, siteDisplayName(row.Site)).FontSize(13).FontWeight(600).TextColor(ColorTextMain).Width(64).SingleLine()
				text, col := remoteSiteStateText(row)
				if pending {
					text, col = "Cookie 待同步（远程暂时连不上，稍后自动重试）", ColorDotYellow
				}
				ui.Box(c).Size(8, 8).Radius(4).Background(col).Shrink(0)
				line := text
				if row.Uname != "" && row.State == remote.SiteValid {
					line += " · " + row.Uname
				}
				if row.CookieSyncedAt != "" {
					line += " · Cookie 同步于 " + shortTime(row.CookieSyncedAt)
				}
				ui.Text(c, line).FontSize(12).TextColor(ColorTextMuted).Grow(1).MaxLines(2)
				local := a.backend.StatusOf(row.Site)
				if local.LoggedIn && OutlinePillButton(c, "同步 Cookie", "同步Cookie到远程："+row.Site).Disabled(pushing).Clicked() && !pushing {
					a.pushRemoteCookie(row.Site)
				}
				if row.State != remote.SiteNone && DangerOutlinePillButton(c, "清除远程登录", "清除远程登录："+row.Site).Disabled(clearing).Clicked() && !clearing {
					a.clearRemoteCookie(row.Site)
				}
			})
		}
		ui.Text(c, "本机登录、扫码或续期成功后会自动把 Cookie 推送到远程；远程不续期，Cookie 失效时这里显示“需要重新同步”。").
			FontSize(12).TextColor(ColorTextMuted).MaxLines(2)
	})
}

func remoteSiteStateText(s remote.SiteStatus) (string, ui.Color) {
	switch s.State {
	case remote.SiteValid:
		return "有效", ColorDotGreen
	case remote.SiteResync:
		return "需要重新同步", ColorDotRed
	case remote.SiteBackoff:
		return "暂停到 " + shortTime(s.BackoffUntil) + "（站点限制）", ColorDotYellow
	case remote.SiteUnknown:
		return firstNonEmptyStrUI(s.Message, "暂时无法确认"), ColorDotYellow
	default:
		return "未登录", ColorTextFaint
	}
}

func (a *App) pushRemoteCookie(siteID string) {
	gen := a.remoteGen
	backend := a.backend
	requestGuarded(a, "remote-push", func() bool { return a.remoteGen == gen }, func(ctx context.Context) (remote.SiteStatus, error) {
		ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
		defer cancel()
		return backend.PushCredentialToRemote(ctx, siteID)
	}, func(remote.SiteStatus) {
		a.Remote.Info, a.Remote.Err = siteDisplayName(siteID)+" 的 Cookie 已同步到远程", ""
		a.fetchRemoteStatus()
	}, func(err error) { a.Remote.Err = remoteErrorText(err) })
}

func (a *App) clearRemoteCookie(siteID string) {
	gen := a.remoteGen
	backend := a.backend
	requestGuarded(a, "remote-clear", func() bool { return a.remoteGen == gen }, func(ctx context.Context) (bool, error) {
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		return true, backend.ClearRemoteCredential(ctx, siteID)
	}, func(bool) {
		a.Remote.Info = "已清除远程的" + siteDisplayName(siteID) + "登录"
		a.fetchRemoteStatus()
	}, func(err error) { a.Remote.Err = remoteErrorText(err) })
}

func (a *App) renderRemoteWork(c *ui.Context, st remote.Status) {
	remoteSection(c, "检查与缓存", func() {
		scheduled, next := 0, ""
		for _, r := range st.Checks {
			if r.Scheduled {
				scheduled++
				if r.NextAt != "" && (next == "" || r.NextAt < next) {
					next = r.NextAt
				}
			}
		}
		line := fmt.Sprintf("定时检查 %d 个账号", scheduled)
		if next != "" {
			line += " · 下次 " + shortTime(next)
		}
		line += fmt.Sprintf(" · 缓存中 %d、排队 %d", st.Cache.Running, st.Cache.Queued)
		if len(st.Recordings) > 0 {
			line += fmt.Sprintf(" · 录制中 %d", len(st.Recordings))
		}
		ui.Text(c, line).Label("远程检查与缓存概况").FontSize(13).TextColor(ColorTextMain).MaxLines(2)
		for _, r := range st.Recordings {
			rec := r
			ui.Text(c, fmt.Sprintf("● %s · %s · %s · %d 段", firstNonEmptyStrUI(rec.Name, siteDisplayName(rec.Site)), firstNonEmptyStrUI(rec.Title, "直播"), humanDuration(time.Duration(rec.DurationSec)*time.Second), rec.Segments)).
				FontSize(12).TextColor(ColorLive).SingleLine()
		}
		for i, t := range st.Cache.Recent {
			if i >= 5 {
				break
			}
			task := t
			status := taskStatusText(task.Status)
			if task.Error != "" {
				status += "：" + task.Error
			}
			ui.Text(c, fmt.Sprintf("%s · %s · %s", shortTime(task.At), firstNonEmptyStrUI(task.Title, "（无标题）"), status)).
				FontSize(12).TextColor(ColorTextMuted).SingleLine()
		}
	})
}

func (a *App) renderRemoteUpload(c *ui.Context, st remote.Status) {
	remoteSection(c, "上传与空间", func() {
		up := st.Upload
		if up.Mode != remote.UploadRclone {
			ui.Text(c, "远程没有开启内置上传（用自己的定时 rclone 时，请排除 *.part 与 .trace-*）。").FontSize(13).TextColor(ColorTextMain).MaxLines(2)
		} else {
			line := fmt.Sprintf("上传到 %s · 待上传 %d 个条目（%s）· 今天已传 %d 个文件（%s）", up.Target, up.QueuedItems, FormatBytes(up.QueuedBytes), up.TodayFiles, FormatBytes(up.TodayBytes))
			ui.Text(c, line).Label("远程上传概况").FontSize(13).TextColor(ColorTextMain).MaxLines(2)
			if up.Uploading != "" {
				ui.Text(c, "正在上传 "+up.Uploading).FontSize(12).TextColor(ColorInfoBlue).SingleLine()
			}
			if up.LastError != "" {
				retrying := a.isPending("remote-retry")
				ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).ClipX().Children(func() {
					msg := uploadErrorText(up)
					ui.Text(c, msg).Label("远程上传错误").FontSize(12).TextColor(ColorError).Grow(1).MaxLines(3)
					if OutlinePillButton(c, "立即重试", "立即重试上传").Disabled(retrying).Clicked() && !retrying {
						a.retryRemoteUpload()
					}
				})
			}
		}
		if st.Disk.Total > 0 {
			col := ColorTextMuted
			line := fmt.Sprintf("导出目录剩余 %s / %s", FormatBytes(int64(st.Disk.Free)), FormatBytes(int64(st.Disk.Total)))
			if st.Disk.Paused {
				col, line = ColorError, line+"，不足 1 GiB，新的缓存任务已暂停"
			} else if st.Disk.Warning {
				col, line = ColorDotYellow, line+"，不足 5 GiB"
			}
			ui.Text(c, line).Label("远程磁盘空间").FontSize(12).TextColor(col).SingleLine()
		}
	})
}

func uploadErrorText(up remote.UploadStatus) string {
	kind := uploadErrorKindLabels[up.LastErrorKind]
	msg := "上传失败"
	if kind != "" {
		msg += "（" + kind + "）"
	}
	msg += "：" + up.LastError
	if up.NextRetryAt != "" {
		msg += " · " + shortTime(up.NextRetryAt) + " 重试"
	}
	return msg
}

func (a *App) retryRemoteUpload() {
	gen := a.remoteGen
	backend := a.backend
	requestGuarded(a, "remote-retry", func() bool { return a.remoteGen == gen }, func(ctx context.Context) (bool, error) {
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		return true, backend.RetryRemoteUpload(ctx)
	}, func(bool) { a.Remote.Info = "已让远程立即重试上传"; a.fetchRemoteStatus() }, func(err error) { a.Remote.Err = remoteErrorText(err) })
}

func (a *App) renderRemoteErrors(c *ui.Context, st remote.Status) {
	if len(st.Errors) == 0 {
		return
	}
	remoteSection(c, "最近的错误", func() {
		for i := len(st.Errors) - 1; i >= 0 && i >= len(st.Errors)-8; i-- {
			e := st.Errors[i]
			ui.Text(c, shortTime(e.At)+" · "+e.Message).FontSize(12).TextColor(ColorTextMuted).MaxLines(2)
		}
	})
}

func remoteSection(c *ui.Context, title string, body func()) {
	ui.Column(c).Label("远程分组："+title).FillWidth().Padding(0, 0, 14, 0).BorderWidth(0, 0, 1, 0).BorderColor(ColorBorder).Gap(8).ClipX().Children(func() {
		ui.Text(c, title).FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine()
		body()
	})
}

func siteDisplayName(id string) string {
	switch strings.ToLower(id) {
	case "bilibili":
		return "哔哩哔哩"
	case "douyin":
		return "抖音"
	case "weibo":
		return "微博"
	}
	return id
}

// shortTime shows an RFC 3339 time as local "01-02 15:04".
func shortTime(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	t = t.Local()
	if y, m, d := time.Now().Date(); t.Year() == y && t.Month() == m && t.Day() == d {
		return t.Format("15:04")
	}
	return t.Format("01-02 15:04")
}

func humanDuration(d time.Duration) string {
	if d < time.Minute {
		return "不到 1 分钟"
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if h >= 24 {
		return fmt.Sprintf("%d 天 %d 小时", h/24, h%24)
	}
	if h > 0 {
		return fmt.Sprintf("%d 小时 %d 分", h, m)
	}
	return fmt.Sprintf("%d 分钟", m)
}
