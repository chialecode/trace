package ui

import (
	"context"
	"testing"

	"github.com/chialecode/trace/internal/site"
	"github.com/egoist/mygo/ui"
)

// DEC-046 ⑤: Settings → 网络 offers 跟随系统 (default) / 不用代理 / 自定义; a
// custom address is checked before it is used and applies at once.
func TestNetworkSettingsProxyModes(t *testing.T) {
	t.Cleanup(func() { _ = site.SetProxy(site.ProxySystem, "") })
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()
	app.SettingsCategory = "network"
	app.Navigate(RouteSettings)
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	if !tt.HasText("跟随系统（推荐）") {
		t.Fatal("the proxy defaults to following the system")
	}
	if _, ok := tt.Find("跟随系统代理说明"); !ok {
		t.Fatal("following the system lacks its note")
	}
	if err := tt.Click("代理方式选择"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("自定义"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if site.ProxyMode() != site.ProxySystem {
		t.Fatal("choosing 自定义 switched before an address was saved")
	}
	app.ProxyDraftURL = "ftp://127.0.0.1:21"
	if err := tt.Click("保存代理地址"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, ok := tt.Find("代理设置错误"); !ok || site.ProxyMode() != site.ProxySystem {
		t.Fatal("a bad address was not refused in place")
	}
	app.ProxyDraftURL = "socks5://127.0.0.1:1080"
	if err := tt.Click("保存代理地址"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if site.ProxyMode() != site.ProxyCustom || app.backend.GetSettings().Network.ProxyURL != "socks5://127.0.0.1:1080" {
		t.Fatalf("custom proxy not applied: %q %+v", site.ProxyMode(), app.backend.GetSettings().Network)
	}
	if err := tt.Click("代理方式选择"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("不用代理"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if site.ProxyMode() != site.ProxyNone {
		t.Fatalf("不用代理 not applied: %q", site.ProxyMode())
	}
}
