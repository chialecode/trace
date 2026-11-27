// Package computer implements the 我的电脑 tab for TRACE mobile (FR-MOB-017, ui.md §3.12).
package computer

import (
	"fmt"
	"strings"

	"github.com/chialecode/trace/cmd/trace-mobile/internal/pagekit"
	"github.com/chialecode/trace/cmd/trace-mobile/internal/theme"
	"github.com/chialecode/trace/internal/companionclient"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/mobile"
)

// Page implements pagekit.TabPage for 我的电脑.
type Page struct {
	confirmUnpair bool
}

// New returns the 我的电脑 tab page.
func New() *Page { return &Page{} }

func (p *Page) ID() pagekit.TabID { return pagekit.TabComputer }
func (p *Page) Title() string     { return "我的电脑" }

func (p *Page) OnConnected(pagekit.Host)                         { p.confirmUnpair = false }
func (p *Page) OnDisconnected()                                  { p.confirmUnpair = false }
func (p *Page) OnEvent(pagekit.Host, companionclient.EventFrame) {}

func (p *Page) View(c *ui.Context, h pagekit.Host) {
	r := h.Router()
	r.SystemStack = true
	snap := h.Desktop()

	ui.Column(c).Fill().Background(theme.ColorBgPanel).Children(func() {
		if !r.InSystemStack(c) {
			mobile.TopBar(c, mobile.TopBarOptions{
				Title: p.Title(),
			})
		}
		r.View(c, func(route *ui.Route) {
			route.Title(p.Title())
			ui.Scroll(c).Fill().Children(func() {
				ui.Column(c).FillWidth().Padding(16, 16, 28, 16).Gap(16).Children(func() {
					// Computer summary card
					renderSectionCard(c, "电脑端信息", func() {
						renderKVRow(c, "电脑名称", firstNonEmpty(snap.ComputerName, "未命名电脑"))
						renderDivider(c)
						verText := firstNonEmpty(snap.Version, "未知")
						if snap.ChannelLabel != "" {
							verText = fmt.Sprintf("%s（%s）", verText, snap.ChannelLabel)
						}
						renderKVRow(c, "TRACE 版本与通道", verText)
						renderDivider(c)
						renderKVRow(c, "运行时长", firstNonEmpty(snap.UptimeText, "—"))
						renderDivider(c)
						renderKVRow(c, "当前连接地址", firstNonEmpty(snap.CurrentAddr, "—"))
						renderDivider(c)
						renderKVRow(c, "缓存位置剩余空间", formatDiskSpace(snap.DiskFree, snap.DiskTotal))
					})

					// Read-only site login statuses (FR-MOB-017 / FR-MOB-025)
					renderSectionCard(c, "站点登录状态（仅电脑端管理）", func() {
						sites := snap.Sites
						if len(sites) == 0 {
							sites = defaultSiteList()
						}
						for i, s := range sites {
							if i > 0 {
								renderDivider(c)
							}
							renderSiteStatusRow(c, s)
						}
					})

					// Unpair action at bottom (ui.md §3.12)
					ui.Column(c).FillWidth().Padding(8, 0, 0, 0).Gap(10).Children(func() {
						if !p.confirmUnpair {
							if theme.DangerOutlinePillButton(c, "解除配对", "解除配对").FillWidth().Clicked() {
								p.confirmUnpair = true
							}
						} else {
							ui.Column(c).
								FillWidth().
								Padding(14, 16).
								Gap(12).
								Radius(12).
								Background(theme.ColorErrorBg).
								Border(1, ui.RGBA(196, 24, 46, 0.25)).
								Children(func() {
									ui.Text(c, "解除后将清除本机保存的配对记录与令牌，之后需要重新扫码连接。").
										FontSize(13).
										LineHeight(theme.LineHeightCompact).
										TextColor(theme.ColorError)
									ui.Row(c).FillWidth().Gap(10).Children(func() {
										if theme.SecondaryPillButton(c, "取消", "取消解除配对").Grow(1).Basis(0).Clicked() {
											p.confirmUnpair = false
										}
										if theme.DangerOutlinePillButton(c, "确认解除", "确认解除配对").Grow(1).Basis(0).Clicked() {
											p.confirmUnpair = false
											h.Unpair()
										}
									})
								})
						}
					})
				})
			})
		}).Grow(1).Basis(0).FillWidth()
	})
}

func renderSectionCard(c *ui.Context, title string, content func()) {
	ui.Column(c).FillWidth().Gap(8).Children(func() {
		ui.Text(c, title).FontSize(13).FontWeight(600).TextColor(theme.ColorTextMuted).PaddingX(4)
		ui.Column(c).
			FillWidth().
			Background(theme.ColorBgMain).
			Radius(12).
			Border(1, theme.ColorBorder).
			Children(content)
	})
}

func renderKVRow(c *ui.Context, key, val string) {
	ui.Row(c).
		FillWidth().
		Padding(12, 16).
		Gap(12).
		AlignItems(ui.Center).
		Justify(ui.SpaceBetween).
		Children(func() {
			ui.Text(c, key).FontSize(14).TextColor(theme.ColorTextMuted).Shrink(0)
			ui.Text(c, val).FontSize(14).FontWeight(500).TextColor(theme.ColorTextMain).TextAlign(ui.End).Grow(1).Shrink(1)
		})
}

func renderSiteStatusRow(c *ui.Context, s pagekit.SiteAuthStatus) {
	label := s.Label
	if label == "" {
		label = siteDisplayLabel(s.Site)
	}
	statusText, dotColor := formatSiteState(s.State, s.Nickname)
	ui.Row(c).
		FillWidth().
		Padding(12, 16).
		Gap(10).
		AlignItems(ui.Center).
		Justify(ui.SpaceBetween).
		Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(8).Shrink(0).Children(func() {
				ui.Box(c).Size(8, 8).Radius(4).Background(dotColor)
				ui.Text(c, label).FontSize(14).FontWeight(500).TextColor(theme.ColorTextMain)
			})
			ui.Text(c, statusText).FontSize(13).TextColor(theme.ColorTextMuted).TextAlign(ui.End).Grow(1).Shrink(1)
		})
}

func renderDivider(c *ui.Context) {
	ui.Box(c).FillWidth().Height(1).Background(theme.ColorBorder)
}

func formatSiteState(state, nickname string) (string, ui.Color) {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "logged_in", "valid":
		if nickname != "" {
			return "已登录 · " + nickname, theme.ColorDotGreen
		}
		return "已登录", theme.ColorDotGreen
	case "expired", "resync":
		if nickname != "" {
			return "已失效 · " + nickname, theme.ColorDotYellow
		}
		return "已失效", theme.ColorDotYellow
	case "unknown":
		return "无法确认", theme.ColorTextFaint
	default:
		return "未登录", theme.ColorTextFaint
	}
}

func siteDisplayLabel(site string) string {
	switch strings.ToLower(strings.TrimSpace(site)) {
	case "bilibili":
		return "B 站"
	case "douyin":
		return "抖音"
	case "weibo":
		return "微博"
	case "x":
		return "X"
	default:
		return site
	}
}

func defaultSiteList() []pagekit.SiteAuthStatus {
	return []pagekit.SiteAuthStatus{
		{Site: "bilibili", Label: "B 站", State: "logged_out"},
		{Site: "douyin", Label: "抖音", State: "logged_out"},
		{Site: "weibo", Label: "微博", State: "logged_out"},
		{Site: "x", Label: "X", State: "logged_out"},
	}
}

func formatDiskSpace(free, total uint64) string {
	if free == 0 && total == 0 {
		return "—"
	}
	if total == 0 {
		return "可用 " + formatBytes(free)
	}
	return fmt.Sprintf("%s / %s 可用", formatBytes(free), formatBytes(total))
}

func formatBytes(n uint64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
		tb = 1024 * gb
	)
	switch {
	case n >= tb:
		return fmt.Sprintf("%.1f TB", float64(n)/float64(tb))
	case n >= gb:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(gb))
	case n >= mb:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(mb))
	case n >= kb:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(kb))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
