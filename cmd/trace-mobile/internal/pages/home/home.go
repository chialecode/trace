// Package home implements the 主页 tab for TRACE mobile.
package home

import (
	"github.com/chialecode/trace/cmd/trace-mobile/internal/pagekit"
	"github.com/chialecode/trace/cmd/trace-mobile/internal/theme"
	"github.com/chialecode/trace/internal/companionclient"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/mobile"
)

// Page implements pagekit.TabPage for 主页.
type Page struct{}

// New returns the 主页 tab page.
func New() *Page { return &Page{} }

func (p *Page) ID() pagekit.TabID { return pagekit.TabHome }
func (p *Page) Title() string     { return "主页" }

func (p *Page) OnConnected(pagekit.Host)                         {}
func (p *Page) OnDisconnected()                                  {}
func (p *Page) OnEvent(pagekit.Host, companionclient.EventFrame) {}

func (p *Page) View(c *ui.Context, h pagekit.Host) {
	r := h.Router()
	r.SystemStack = true
	ui.Column(c).Fill().Background(theme.ColorBgMain).Children(func() {
		if !r.InSystemStack(c) {
			mobile.TopBar(c, mobile.TopBarOptions{Title: p.Title()})
		}
		r.View(c, func(route *ui.Route) {
			route.Title(p.Title())
			renderPlaceholder(c, h.Desktop().ComputerName, "主页信息流", "已连接电脑端，主页信息流将在后续模块接入。")
		}).Grow(1).Basis(0).FillWidth()
	})
}

func renderPlaceholder(c *ui.Context, computerName, heading, subtitle string) {
	ui.Scroll(c).Fill().Children(func() {
		ui.Column(c).FillWidth().Padding(32, 20).Gap(10).AlignItems(ui.Center).Children(func() {
			theme.DrawIcon(c, "home", 32, theme.ColorTextMuted, 1.7)
			ui.Text(c, heading).FontSize(17).FontWeight(600).TextColor(theme.ColorTextMain)
			if computerName != "" {
				ui.Text(c, "已连接："+computerName).FontSize(13).TextColor(theme.ColorAccent)
			}
			ui.Text(c, subtitle).FontSize(13).LineHeight(theme.LineHeightCompact).TextColor(theme.ColorTextMuted).TextAlign(ui.Center)
		})
	})
}
