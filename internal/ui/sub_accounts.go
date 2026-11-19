package ui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

// subAccountRow is one subscribed account and the subscription it belongs to
// (订阅 › 全部账号, DEC-057).
type subAccountRow struct {
	Site, UID, UniqueID, Name, Face string
	Sub                             subscription.Subscription
}

// subscriptionAccountRows lists every account of every subscription under
// the site filter, by site and then name.
func (a *App) subscriptionAccountRows(siteFilter string) []subAccountRow {
	siteFilter = normalizeSiteFilter(siteFilter)
	var rows []subAccountRow
	for _, sub := range a.Subscriptions {
		for _, acc := range sub.Accounts {
			siteID := normalizeSiteIDUI(acc.Site)
			if siteFilter != "all" && siteID != siteFilter {
				continue
			}
			rows = append(rows, subAccountRow{
				Site: siteID, UID: strings.TrimSpace(acc.UID), UniqueID: strings.TrimSpace(acc.UniqueID),
				Name: firstNonEmptyStrUI(acc.Name, acc.UniqueID, acc.UID), Face: acc.Face, Sub: sub,
			})
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Site != rows[j].Site {
			return rows[i].Site < rows[j].Site
		}
		if rows[i].Name != rows[j].Name {
			return rows[i].Name < rows[j].Name
		}
		return rows[i].UID < rows[j].UID
	})
	return rows
}

// accountWorksText is the works line of a subscribed account: the total
// from the last parse of the account and how many are cached (DEC-061).
// Showing it sends no request; "" when nothing is known yet.
func (a *App) accountWorksText(siteID, uid string) string {
	w, ok := a.accountWorks[normalizeSiteIDUI(siteID)+":"+strings.TrimSpace(uid)]
	if !ok {
		return ""
	}
	if w.Total < 0 {
		return fmt.Sprintf("已缓存 %d · 作品总数在打开主页后统计", w.Cached)
	}
	text := fmt.Sprintf("作品 %d · 已缓存 %d", w.Total, w.Cached)
	if at := formatPlanTime(w.TotalAt, time.Now()); at != "" {
		text += " · " + at + " 统计"
	}
	return text
}

func (a *App) openSubscriptionAccounts() {
	a.refreshAccountWorks()
	a.SubDetailOpen = false
	a.SubAccountsOpen = true
	a.SubManageMenuOpen = false
	a.SubManageModalMode = ""
}

// renderSubscriptionAccountsView: every subscribed account under a site
// filter, each with the subscription it belongs to; clicking opens that
// subscription.
func (a *App) renderSubscriptionAccountsView(c *ui.Context) {
	rows := a.subscriptionAccountRows(a.SubAccountsSite)
	ui.Column(c).Grow(1).FillWidth().Padding(16, 16, 0, 16).Gap(10).ClipX().Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
			ui.Row(c).Width(260).Shrink(0).Children(func() {
				renderThreeSegmentSiteFilter(c, "订阅账号站点筛选：", normalizeSiteFilter(a.SubAccountsSite), func(next string) {
					a.SubAccountsSite = normalizeSiteFilter(next)
					a.SubAccountsListState.ScrollTo(0, ui.Start)
				})
			})
			ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
				ui.Text(c, fmt.Sprintf("%d 个账号", len(rows))).FontSize(12).TextColor(ColorTextMuted).SingleLine()
				if !a.panelShown && OutlinePillButton(c, "全部订阅", "订阅账号返回全部订阅").Clicked() {
					a.SubAccountsOpen = false
				}
			})
		})
		if len(rows) == 0 {
			ui.Column(c).FillWidth().Padding(40, 16).AlignItems(ui.Center).Gap(8).Children(func() {
				ui.Text(c, "这个站点还没有订阅的账号").FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
			})
			return
		}
		a.SubAccountsListState.Key = func(i int) any { return rows[i].Site + ":" + rows[i].UID + ":" + rows[i].Sub.ID }
		ui.List(c, &a.SubAccountsListState, len(rows), func(i int) {
			a.renderSubscriptionAccountRow(c, rows[i])
		}).Label("订阅账号列表").Grow(1).FillWidth()
	})
}

func (a *App) renderSubscriptionAccountRow(c *ui.Context, r subAccountRow) {
	label := "订阅账号：" + r.Site + ":" + r.UID
	row := ui.ButtonBase(c).
		Label(label).
		FillWidth().
		Padding(10, 12).
		Radius(12).
		Gap(12).
		AlignItems(ui.Center).
		Justify(ui.Start).
		ClipX()
	if row.Hovered() {
		row.Background(ColorHover)
	}
	row.Children(func() {
		var mid int64
		if r.Site == "bilibili" {
			mid, _ = strconv.ParseInt(r.UID, 10, 64)
		}
		a.renderAccountAvatar(c, r.Site, r.Name, r.UID, mid, 36, false, "", nil, r.Face)
		ui.Column(c).Grow(1).Gap(3).ClipX().Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
				ui.Text(c, r.Name).FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine()
				renderSiteSmallBadgeWithDot(c, r.Site, "订阅账号站点："+r.Site+":"+r.UID)
			})
			id := "UID " + r.UID
			if r.Site == "douyin" {
				id = "抖音号 " + firstNonEmptyStrUI(r.UniqueID, "未知")
			}
			if works := a.accountWorksText(r.Site, r.UID); works != "" {
				id += " · " + works
			}
			ui.Text(c, id).FontSize(12).TextColor(ColorTextMuted).SingleLine()
		})
		ui.Row(c).Shrink(0).AlignItems(ui.Center).Gap(6).Padding(4, 10).Radius(999).Background(ColorBgSecondary).Children(func() {
			ui.Text(c, "订阅").FontSize(11).TextColor(ColorTextMuted).SingleLine()
			ui.Text(c, r.Sub.Name).Label("所属订阅：" + r.Site + ":" + r.UID).FontSize(12).FontWeight(600).TextColor(ColorTextMain).SingleLine()
		})
	})
	if row.Clicked() {
		a.OpenSubscriptionDetail(r.Sub)
	}
}
