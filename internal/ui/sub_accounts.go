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
	Acct                            subscription.AccountRef
	Sub                             subscription.Subscription
}

func (a *App) subscriptionsAccountsFingerprint() uint64 {
	h := newFNV64a()
	for i := range a.Subscriptions {
		sub := &a.Subscriptions[i]
		fnvWriteStr(h, sub.ID)
		fnvWriteStr(h, sub.Name)
		fnvWriteStr(h, sub.Uname)
		fnvWriteStr(h, sub.Note)
		fnvWriteStr(h, sub.Face)
		fnvWriteStr(h, sub.Avatar.Site)
		fnvWriteStr(h, sub.Avatar.UID)
		fnvWriteInt64(h, sub.Mid)
		fnvWriteInt64(h, int64(len(sub.Accounts)))
		for j := range sub.Accounts {
			acc := &sub.Accounts[j]
			fnvWriteStr(h, acc.Site)
			fnvWriteStr(h, acc.UID)
			fnvWriteStr(h, acc.UniqueID)
			fnvWriteStr(h, acc.Name)
			fnvWriteStr(h, acc.Face)
		}
		fnvWriteInt64(h, int64(len(sub.Links)))
		for j := range sub.Links {
			lnk := &sub.Links[j]
			fnvWriteStr(h, lnk.Label)
			fnvWriteStr(h, lnk.URL)
		}
	}
	return h.Sum64()
}

// subscriptionAccountRows lists every account of every subscription under
// the site filter, by site and then name (cached per subscription snapshot + site filter).
func (a *App) subscriptionAccountRows(siteFilter string) []subAccountRow {
	siteFilter = normalizeSiteFilter(siteFilter)
	sig := subAccountRowsSig{
		subsRev: a.subsRev,
		subsN:   len(a.Subscriptions),
		site:    siteFilter,
		hash:    a.subscriptionsAccountsFingerprint(),
	}
	st := a.fs()
	if st.subAccRows != nil && st.subAccRowsSig == sig {
		return st.subAccRows
	}

	rows := make([]subAccountRow, 0, len(a.Subscriptions))
	for _, sub := range a.Subscriptions {
		for _, acc := range sub.Accounts {
			siteID := normalizeSiteIDUI(acc.Site)
			if siteFilter != "all" && siteID != siteFilter {
				continue
			}
			rows = append(rows, subAccountRow{
				Site: siteID, UID: strings.TrimSpace(acc.UID), UniqueID: strings.TrimSpace(acc.UniqueID),
				Name: firstNonEmptyStrUI(acc.Name, acc.UniqueID, acc.UID), Face: acc.Face, Acct: acc, Sub: sub,
			})
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		ri, rj := subscription.AccountSiteOrderRank(rows[i].Site), subscription.AccountSiteOrderRank(rows[j].Site)
		if ri != rj {
			return ri < rj
		}
		if rows[i].Name != rows[j].Name {
			return rows[i].Name < rows[j].Name
		}
		return rows[i].UID < rows[j].UID
	})
	st.subAccRowsSig = sig
	st.subAccRows = rows
	return rows
}

// accountWorksText is the works line of a subscribed account: the total
// from the last parse of the account and how many are cached (DEC-061).
// Showing it sends no request; "" when nothing is known yet.
func (a *App) accountWorksText(siteID, uid string) string {
	normSite := normalizeSiteIDUI(siteID)
	w, ok := a.accountWorks[normSite+":"+strings.TrimSpace(uid)]
	if !ok {
		return ""
	}
	noun := "作品"
	switch normSite {
	case "weibo":
		noun = "微博"
	case "x":
		noun = "帖子"
	}
	if w.Total < 0 {
		return fmt.Sprintf("已缓存 %d · %s总数在打开主页后统计", w.Cached, noun)
	}
	text := fmt.Sprintf("%s %d · 已缓存 %d", noun, w.Total, w.Cached)
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
	a.SubAccountMenuKey = ""
	a.SubManageModalMode = ""
}

// renderSubscriptionAccountsView: every subscribed account under a site
// filter, each with the subscription it belongs to; clicking opens that
// subscription.
func (a *App) renderSubscriptionAccountsView(c *ui.Context) {
	rows := a.subscriptionAccountRows(a.SubAccountsSite)
	ui.Column(c).Grow(1).FillWidth().Padding(16, 16, 0, 16).Gap(10).ClipX().Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
			ui.Row(c).Width(330).Shrink(1).Children(func() {
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
		a.SubAccountsListState.Key = func(i int) any {
			if i < 0 || i >= len(rows) {
				return i
			}
			return rows[i].Site + ":" + rows[i].UID + ":" + rows[i].Sub.ID
		}
		ui.List(c, &a.SubAccountsListState, len(rows), func(i int) {
			if i < 0 || i >= len(rows) {
				return
			}
			a.renderSubscriptionAccountRow(c, rows[i])
		}).Label("订阅账号列表").Grow(1).FillWidth()
	})
}

func (a *App) renderSubscriptionAccountRow(c *ui.Context, r subAccountRow) {
	label := "订阅账号：" + r.Site + ":" + r.UID
	menuKey := "allacct:" + r.Site + ":" + r.UID
	ui.Row(c).
		FillWidth().
		Padding(2, 4).
		Gap(6).
		AlignItems(ui.Center).
		ClipX().
		Children(func() {
			row := ui.ButtonBase(c).
				Label(label).
				Grow(1).
				Padding(8, 8).
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
						ui.Text(c, r.Name).FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine().Shrink(1)
						renderSiteSmallBadgeWithDot(c, r.Site, "订阅账号站点："+r.Site+":"+r.UID)
					})
					id := "UID " + r.UID
					if r.Site == "douyin" {
						id = "抖音号 " + firstNonEmptyStrUI(r.UniqueID, "未知")
					} else if r.Site == "x" {
						if h := strings.TrimPrefix(strings.TrimSpace(r.UniqueID), "@"); h != "" {
							id = "@" + h
						} else {
							id = "ID " + r.UID
						}
					}
					if works := a.accountWorksText(r.Site, r.UID); works != "" {
						id += " · " + works
					}
					ui.Text(c, id).FontSize(12).TextColor(ColorTextMuted).SingleLine()
				})
				ui.Row(c).Shrink(0).MaxWidth(160).AlignItems(ui.Center).Gap(6).Padding(4, 10).Radius(999).Background(ColorBgSecondary).ClipX().Children(func() {
					ui.Text(c, "订阅").FontSize(11).TextColor(ColorTextMuted).SingleLine().Shrink(0)
					ui.Text(c, r.Sub.Name).Label("所属订阅：" + r.Site + ":" + r.UID).FontSize(12).FontWeight(600).TextColor(ColorTextMain).SingleLine().Shrink(1)
				})
			})
			if row.Clicked() {
				a.OpenSubscriptionDetail(r.Sub)
			}

			open := a.SubAccountMenuKey == menuKey
			MoreMenuButton(c, "全部账号更多："+r.Site+":"+r.UID, 30, &open, func(m *ui.Menu) {
				if m.Item("更改关联订阅").Chosen() {
					a.OpenMoveAccountPicker(r.Sub, r.Acct)
				}
				if m.Item("打开订阅").Chosen() {
					a.OpenSubscriptionDetail(r.Sub)
				}
				if m.Item("打开主页").Chosen() {
					a.openAccountUserPage(r.Site, r.UID, r.Name)
				}
			})
			keyedMenuOpen(&a.SubAccountMenuKey, menuKey, open)
		})
}
