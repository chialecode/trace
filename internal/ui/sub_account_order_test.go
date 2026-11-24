package ui

import (
	"context"
	"testing"

	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

// DEC-071: Subscription Detail ("账号" tab and "更换头像") and "订阅 › 全部账号"
// render linked accounts in canonical site order (bilibili -> douyin -> weibo -> x)
// while keeping stable relative order for multiple accounts on the same site.
func TestSubscriptionAccountsRenderedInCanonicalSiteOrder(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	rawBackend := app.backend.(*service.Backend)
	if _, err := rawBackend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()

	const (
		dy1 = "MS4wLjABAAAA_ui_order_dy_first"
		dy2 = "MS4wLjABAAAA_ui_order_dy_second"
		wb1 = "100000801"
		x1  = "180000000000000801"
	)

	// Link accounts sequentially in reverse site order to verify UI order does not follow add order.
	sub, err := rawBackend.CreateSubscription("跨站多号作者", "", []subscription.AccountRef{
		{Site: "x", UID: x1, UniqueID: "ui_x_one", Name: "X账号"},
	}, subscription.DefaultSubRules())
	if err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}
	for _, acc := range []subscription.AccountRef{
		{Site: "weibo", UID: wb1, Name: "微博账号"},
		{Site: "douyin", UID: dy1, UniqueID: "dy_first", Name: "抖音B号"},
		{Site: "bilibili", UID: "8802", Name: "B站B号"},
		{Site: "douyin", UID: dy2, UniqueID: "dy_second", Name: "抖音A号"},
		{Site: "bilibili", UID: "6601", Name: "B站A号"},
	} {
		sub, err = rawBackend.LinkSubscriptionAccountRef(sub.ID, acc)
		if err != nil {
			t.Fatalf("LinkSubscriptionAccountRef(%s:%s): %v", acc.Site, acc.UID, err)
		}
	}

	app.RefreshData()
	tt := ui.NewTester(app.View, 1280, 960)
	app.OpenSubscriptionDetail(sub)
	tt.Frame()
	if err := tt.Click("订阅标签：账号"); err != nil {
		t.Fatalf("Click 订阅标签：账号: %v", err)
	}
	tt.Frame()

	rowLabels := []string{
		"订阅账号主页：bilibili:8802", // added before 6601 -> stays first within bilibili
		"订阅账号主页：bilibili:6601",
		"订阅账号主页：douyin:" + dy1, // added before dy2 -> stays first within douyin
		"订阅账号主页：douyin:" + dy2,
		"订阅账号主页：weibo:" + wb1,
		"订阅账号主页：x:" + x1,
	}
	var prevY float32 = -1
	for i, lbl := range rowLabels {
		rect, ok := tt.Find(lbl)
		if !ok {
			t.Fatalf("missing account row %q in Subscription Detail › 账号", lbl)
		}
		if i > 0 && !(rect.Y > prevY) {
			t.Fatalf("account row %q (Y=%.1f) not rendered below %q (Y=%.1f)", lbl, rect.Y, rowLabels[i-1], prevY)
		}
		prevY = rect.Y
	}

	// Open "更换头像" from header "⋯" menu and verify avatar choices follow canonical site order.
	chooseMenuItem(t, tt, "订阅管理菜单", "更换头像")
	tt.Frame()
	avatarLabels := []string{
		"订阅头像：bilibili:8802",
		"订阅头像：bilibili:6601",
		"订阅头像：douyin:" + dy1,
		"订阅头像：douyin:" + dy2,
		"订阅头像：weibo:" + wb1,
		"订阅头像：x:" + x1,
	}
	var prevAvatarPos [2]float32
	for i, lbl := range avatarLabels {
		rect, ok := tt.Find(lbl)
		if !ok {
			t.Fatalf("missing avatar button %q", lbl)
		}
		if i > 0 {
			after := rect.Y > prevAvatarPos[1]+4 || (rect.Y >= prevAvatarPos[1]-4 && rect.X > prevAvatarPos[0])
			if !after {
				t.Fatalf("avatar choice %q (%.1f, %.1f) not ordered after %q (%.1f, %.1f)",
					lbl, rect.X, rect.Y, avatarLabels[i-1], prevAvatarPos[0], prevAvatarPos[1])
			}
		}
		prevAvatarPos = [2]float32{rect.X, rect.Y}
	}

	// Open "订阅 › 全部账号" and verify all four sites sort bilibili -> douyin -> weibo -> x
	// (and within each site by Name, so "B站A号" < "B站B号" and "抖音A号" < "抖音B号").
	if err := tt.Click("订阅侧栏：全部账号"); err != nil {
		t.Fatalf("Click 订阅侧栏：全部账号: %v", err)
	}
	tt.Frame()
	allAcctLabels := []string{
		"订阅账号：bilibili:6601",
		"订阅账号：bilibili:8802",
		"订阅账号：douyin:" + dy2,
		"订阅账号：douyin:" + dy1,
		"订阅账号：weibo:" + wb1,
		"订阅账号：x:" + x1,
	}
	prevY = -1
	for i, lbl := range allAcctLabels {
		rect, ok := tt.Find(lbl)
		if !ok {
			t.Fatalf("missing row %q in 订阅 › 全部账号", lbl)
		}
		if i > 0 && !(rect.Y > prevY) {
			t.Fatalf("全部账号 row %q (Y=%.1f) not below %q (Y=%.1f)", lbl, rect.Y, allAcctLabels[i-1], prevY)
		}
		prevY = rect.Y
	}
}
