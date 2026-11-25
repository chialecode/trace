package ui

import (
	"context"
	"testing"

	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

// DEC-074: Subscription Detail renders custom external links as clickable chips
// opening via OpenExternalLink (MockDesktop in tests), provides add/edit/remove
// dialog with strict URL validation and IME composition safety, and wraps
// cleanly within narrow 720px viewports.
func TestSubscriptionCustomLinksAddEditRemoveClickIMEAndNarrowLayout(t *testing.T) {
	app, desk, cleanup := newTestUIApp(t)
	defer cleanup()
	rawBackend := app.backend.(*service.Backend)
	if _, err := rawBackend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()

	sub, err := rawBackend.CreateSubscription("跨平台创作者", "个人合集备注", []subscription.AccountRef{
		{Site: "bilibili", UID: "5001", Name: "创作者B站"},
	}, subscription.DefaultSubRules())
	if err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}

	app.RefreshData()
	tt := ui.NewTester(app.View, 1280, 860)
	app.OpenSubscriptionDetail(sub)
	tt.Frame()

	// 1. Empty subscription omits custom link row clutter in header.
	if _, ok := tt.Find("订阅自定义链接栏"); ok {
		t.Fatal("empty subscription must not render 订阅自定义链接栏")
	}

	// 2. Open custom links dialog from "订阅管理菜单" -> "自定义链接".
	chooseMenuItem(t, tt, "订阅管理菜单", "自定义链接")
	tt.Frame()
	if _, ok := tt.Find("订阅自定义链接对话框"); !ok || !tt.HasText("还没有添加自定义链接") {
		t.Fatal("expected 订阅自定义链接对话框 with empty state")
	}

	// 3. Reject unsafe schemes (javascript:, file:, missing scheme) while preserving draft input.
	for _, bad := range []string{"javascript:alert(1)", "file:///C:/secret.txt", "link3.cc/no-scheme"} {
		app.SubLinks.NewURL = bad
		tt.Frame()
		if err := tt.Click("确认添加订阅链接"); err != nil {
			t.Fatalf("Click 确认添加订阅链接: %v", err)
		}
		tt.Frame()
		if _, ok := tt.Find("订阅自定义链接错误"); !ok {
			t.Fatalf("expected error banner for invalid URL %q", bad)
		}
		if app.SubLinks.NewURL != bad {
			t.Fatalf("failed validation wiped user draft input: got %q, want %q", app.SubLinks.NewURL, bad)
		}
		if len(app.SubLinks.Links) != 0 {
			t.Fatalf("invalid URL %q was added: %+v", bad, app.SubLinks.Links)
		}
	}

	// 4. IME composition guard: pressing Enter while composing in label/URL input must NOT submit.
	app.SubLinks.NewLabel = ""
	app.SubLinks.NewURL = "https://www.linktr.ee/creator_one/"
	tt.Frame()
	if err := tt.Click("新订阅链接名称"); err != nil {
		t.Fatalf("Click 新订阅链接名称: %v", err)
	}
	tt.Compose("geren", 5)
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if len(app.SubLinks.Links) != 0 {
		t.Fatalf("Enter during IME composition prematurely submitted custom link: %+v", app.SubLinks.Links)
	}
	tt.Compose("", 0)
	app.SubLinks.NewLabel = ""
	tt.Frame()

	// 5. Add unlabeled link -> derives clean "linktr.ee/creator_one" chip.
	if err := tt.Click("确认添加订阅链接"); err != nil {
		t.Fatalf("Click 确认添加订阅链接: %v", err)
	}
	tt.Frame()
	if app.SubLinks.Error != "" || len(app.SubLinks.Links) != 1 {
		t.Fatalf("expected 1 added link without error, got links=%+v err=%q", app.SubLinks.Links, app.SubLinks.Error)
	}
	if app.SubLinks.NewURL != "" {
		t.Fatalf("expected NewURL cleared after successful add, got %q", app.SubLinks.NewURL)
	}

	// 6. Add labeled link ("Link3 聚合") + extra links for narrow-width wrap check.
	for _, item := range []subscription.CustomLink{
		{Label: "Link3 聚合", URL: "https://link3.cc/creator_one"},
		{Label: "作品归档站", URL: "https://archive.example.org/creator_one"},
		{URL: "https://blog.example.org/notes/2026"},
	} {
		app.SubLinks.NewLabel = item.Label
		app.SubLinks.NewURL = item.URL
		tt.Frame()
		if err := tt.Click("确认添加订阅链接"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
	}
	if len(app.SubLinks.Links) != 4 {
		t.Fatalf("expected 4 custom links, got %+v", app.SubLinks.Links)
	}

	// Close modal and verify chips in Subscription Detail header + click records URL in MockDesktop.
	if err := tt.Click("关闭自定义链接对话框"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if app.SubLinks.Open {
		t.Fatal("expected custom links modal closed")
	}

	for _, chip := range []string{
		"打开订阅链接：linktr.ee/creator_one",
		"打开订阅链接：Link3 聚合",
		"打开订阅链接：作品归档站",
		"打开订阅链接：blog.example.org/notes/2026",
		"管理订阅自定义链接",
	} {
		if _, ok := tt.Find(chip); !ok {
			t.Fatalf("missing header chip %q", chip)
		}
	}

	if err := tt.Click("打开订阅链接：linktr.ee/creator_one"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("打开订阅链接：Link3 聚合"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()

	acts := desk.Actions()
	if len(acts) != 2 ||
		acts[0].Kind != "url" || acts[0].Target != "https://www.linktr.ee/creator_one/" ||
		acts[1].Kind != "url" || acts[1].Target != "https://link3.cc/creator_one" {
		t.Fatalf("unexpected MockDesktop URL actions: %+v", acts)
	}

	// 7. Narrow 720px viewport wraps all custom link chips cleanly within [0, 720] without overflow.
	narrow := ui.NewTester(app.View, 720, 860)
	narrow.Frame()
	for _, chip := range []string{
		"打开订阅链接：linktr.ee/creator_one",
		"打开订阅链接：Link3 聚合",
		"打开订阅链接：作品归档站",
		"打开订阅链接：blog.example.org/notes/2026",
		"管理订阅自定义链接",
	} {
		r, ok := narrow.Find(chip)
		if !ok {
			t.Fatalf("narrow 720px missing %q", chip)
		}
		if r.X < 0 || r.X+r.W > 720.5 {
			t.Fatalf("narrow 720px chip %q overflowed horizontally: %+v", chip, r)
		}
	}

	// 8. Reopen via "管理订阅自定义链接" chip, edit link 1, delete remaining links, verify persistence.
	if err := tt.Click("管理订阅自定义链接"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("编辑订阅链接 1"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	app.SubLinks.EditLabel = "Linktree 主页"
	app.SubLinks.EditURL = "https://linktr.ee/creator_updated"
	tt.Frame()
	if err := tt.Click("保存编辑订阅链接"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if app.SubLinks.EditIndex != -1 || app.SubLinks.Links[0].Label != "Linktree 主页" || app.SubLinks.Links[0].URL != "https://linktr.ee/creator_updated" {
		t.Fatalf("edit did not persist properly: %+v", app.SubLinks)
	}

	// Remove all 4 links one by one; header custom links bar disappears when empty.
	for len(app.SubLinks.Links) > 0 {
		if err := tt.Click("删除订阅链接 1"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
	}
	if err := tt.Click("关闭自定义链接对话框"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, ok := tt.Find("订阅自定义链接栏"); ok {
		t.Fatal("expected 订阅自定义链接栏 hidden after removing all custom links")
	}
}
