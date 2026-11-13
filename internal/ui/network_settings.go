package ui

import (
	"strings"

	"github.com/chialecode/trace/internal/site"
	"github.com/egoist/mygo/ui"
)

// renderNetworkSettings is Settings → 网络 (DEC-046 ⑤): the proxy every site
// request uses — API, cache downloads and live recording. A change applies
// at once; a custom address is saved only when it parses.
func (a *App) renderNetworkSettings(c *ui.Context) {
	ui.Column(c).FillWidth().Gap(12).ClipX().Children(func() {
		mode := a.DraftSettings.Network.ProxyMode
		if mode == "" {
			mode = site.ProxySystem
		}
		if a.proxyEditing {
			mode = site.ProxyCustom
		}
		shown := mode
		if settingChoiceRow(c, "代理", "站点接口、缓存下载和直播录制都走这里的设置，改动立即生效。", "代理方式选择", &shown, []settingChoice{
			{site.ProxySystem, "跟随系统（推荐）"},
			{site.ProxyNone, "不用代理"},
			{site.ProxyCustom, "自定义"},
		}) {
			a.ProxyError = ""
			if shown == site.ProxyCustom {
				// Wait for an address before switching.
				a.proxyEditing = true
				a.ProxyDraftURL = a.DraftSettings.Network.ProxyURL
			} else {
				a.proxyEditing = false
				a.applyProxySetting(shown, a.DraftSettings.Network.ProxyURL)
			}
		}
		switch mode {
		case site.ProxySystem:
			ui.Text(c, "使用 Windows 的代理设置（含自动配置脚本）；设置了 HTTP_PROXY / HTTPS_PROXY 环境变量时以环境变量为准。").
				Label("跟随系统代理说明").FontSize(12).TextColor(ColorTextMuted).MaxLines(3)
		case site.ProxyCustom:
			if !a.proxyEditing && a.ProxyDraftURL == "" {
				a.ProxyDraftURL = a.DraftSettings.Network.ProxyURL
			}
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).ClipX().Children(func() {
				in := ui.TextInput(c, &a.ProxyDraftURL).Label("代理地址").Placeholder("http://127.0.0.1:7890 或 socks5://127.0.0.1:1080").Grow(1)
				submitted := in.Submitted()
				if AccentPillButton(c, "保存", "保存代理地址").Clicked() || submitted {
					a.applyProxySetting(site.ProxyCustom, a.ProxyDraftURL)
				}
			})
			if a.ProxyError != "" {
				ui.Text(c, a.ProxyError).Label("代理设置错误").FontSize(12).TextColor(ColorError).MaxLines(2)
			} else if a.proxyEditing {
				ui.Text(c, "填好地址后点“保存”才会改用自定义代理。").FontSize(12).TextColor(ColorTextMuted).MaxLines(2)
			}
		}
	})
}

func (a *App) applyProxySetting(mode, rawURL string) {
	next := a.DraftSettings
	next.Network.ProxyMode = mode
	if mode == site.ProxyCustom {
		u, err := site.ParseProxyURL(rawURL)
		if err != nil {
			a.ProxyError = err.Error()
			return
		}
		next.Network.ProxyURL = u.String()
	} else {
		next.Network.ProxyURL = strings.TrimSpace(next.Network.ProxyURL)
	}
	updated, err := a.backend.UpdateSettings(next)
	if err != nil {
		a.ProxyError = userErrorText(err)
		return
	}
	a.DraftSettings = updated
	a.ProxyError = ""
	a.proxyEditing = false
	a.ProxyDraftURL = updated.Network.ProxyURL
}
