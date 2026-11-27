package subscriptions

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/chialecode/trace/cmd/trace-mobile/internal/pagekit"
	"github.com/chialecode/trace/cmd/trace-mobile/internal/theme"
	"github.com/chialecode/trace/internal/companionapi"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/mobile"
)

func (p *Page) renderMainView(c *ui.Context, h pagekit.Host, r *ui.Router) {
	ui.Column(c).Fill().Background(theme.ColorBgMain).ClipX().Children(func() {
		if !r.InSystemStack(c) {
			mobile.TopBar(c, mobile.TopBarOptions{Title: p.Title()})
		}

		// Top 3-way segment: 全部订阅 / 全部账号 / 检查计划 (FR-MOB-015, ui.md §3.12).
		ui.Column(c).FillWidth().Padding(8, 14, 10, 14).Gap(8).BorderWidth(0, 0, 1, 0).BorderColor(theme.ColorBorder).Shrink(0).ClipX().Children(func() {
			prev := p.activeSeg
			ui.Segmented(c, &p.activeSeg, "全部订阅", "全部账号", "检查计划").FillWidth().Label("订阅视图切换")
			if p.activeSeg != prev {
				p.actionErr = ""
				p.actionInfo = ""
				switch p.activeSeg {
				case segAllSubs:
					if !p.subsLoaded && !p.subsLoading {
						p.loadSubscriptions(h)
					}
				case segAllAccounts:
					if !p.accountsLoaded && !p.accountsLoading {
						p.loadAccounts(h)
					}
				case segCheckPlan:
					if !p.planLoaded && !p.planLoading {
						p.loadCheckPlan(h)
					}
				}
			}

			p.renderCheckProgressBanner(c, h)
			p.renderFeedbackBanners(c)
		})

		switch p.activeSeg {
		case segAllSubs:
			p.renderAllSubscriptionsSegment(c, h)
		case segAllAccounts:
			p.renderAllAccountsSegment(c, h)
		case segCheckPlan:
			p.renderCheckPlanSegment(c, h)
		}
	})
}

func (p *Page) renderCheckProgressBanner(c *ui.Context, h pagekit.Host) {
	if !p.checking {
		return
	}
	msg := firstNonEmpty(p.checkMessage, "正在检查更新…")
	ui.Row(c).
		Label("检查更新进度条").
		FillWidth().
		Padding(10, 12).
		Radius(12).
		Background(theme.ColorAccentBg).
		Border(1, ui.RGBA(26, 115, 232, 0.25)).
		AlignItems(ui.Center).
		Justify(ui.SpaceBetween).
		Gap(10).
		ClipX().
		Children(func() {
			ui.Text(c, msg).
				FontSize(13).
				FontWeight(600).
				TextColor(theme.ColorAccent).
				MaxLines(2).
				Grow(1).
				Shrink(1)
			btn := ui.Button(c, "").
				Label("取消检查更新").
				Radius(999).
				Padding(6, 14).
				MinHeight(36).
				Background(theme.ColorBgMain).
				Border(1, ui.RGBA(196, 24, 46, 0.35)).
				Shrink(0).
				Center()
			btn.Children(func() {
				ui.Text(c, "取消").FontSize(13).FontWeight(600).TextColor(theme.ColorError).SingleLine()
			})
			if btn.Clicked() {
				p.cancelCheck(h)
			}
		})
}

func (p *Page) renderFeedbackBanners(c *ui.Context) {
	if p.actionErr != "" {
		ui.Row(c).
			Label("操作错误提示").
			FillWidth().
			Padding(10, 12).
			Radius(10).
			Background(theme.ColorErrorBg).
			Border(1, ui.RGBA(196, 24, 46, 0.25)).
			AlignItems(ui.Center).
			Justify(ui.SpaceBetween).
			Gap(8).
			ClipX().
			Children(func() {
				theme.DrawIcon(c, "alert-circle", 16, theme.ColorError, 1.8)
				ui.Text(c, p.actionErr).
					FontSize(13).
					LineHeight(theme.LineHeightCompact).
					TextColor(theme.ColorError).
					MaxLines(3).
					Grow(1).
					Shrink(1)
				closeBtn := ui.ButtonBase(c).Label("关闭错误提示").Padding(4, 8).MinHeight(32).Shrink(0).Center()
				closeBtn.Children(func() {
					ui.Text(c, "关闭").FontSize(12).TextColor(theme.ColorError).SingleLine()
				})
				if closeBtn.Clicked() {
					p.actionErr = ""
				}
			})
	}
	if p.actionInfo != "" {
		ui.Row(c).
			Label("操作状态提示").
			FillWidth().
			Padding(8, 12).
			Radius(10).
			Background(theme.ColorBgSecondary).
			Border(1, theme.ColorBorder).
			AlignItems(ui.Center).
			Justify(ui.SpaceBetween).
			Gap(8).
			ClipX().
			Children(func() {
				ui.Text(c, p.actionInfo).
					FontSize(12).
					TextColor(theme.ColorTextMuted).
					MaxLines(2).
					Grow(1).
					Shrink(1)
				closeBtn := ui.ButtonBase(c).Label("关闭提示").Padding(4, 8).MinHeight(32).Shrink(0).Center()
				closeBtn.Children(func() {
					ui.Text(c, "关闭").FontSize(12).TextColor(theme.ColorTextMuted).SingleLine()
				})
				if closeBtn.Clicked() {
					p.actionInfo = ""
				}
			})
	}
}

func (p *Page) renderAllSubscriptionsSegment(c *ui.Context, h pagekit.Host) {
	if p.subsLoading && !p.subsLoaded {
		p.renderLoadingSkeleton(c)
		return
	}
	if p.subsErr != "" && len(p.subs) == 0 {
		p.renderLoadError(c, p.subsErr, func() { p.loadSubscriptions(h) })
		return
	}
	if len(p.subs) == 0 {
		sc := ui.Scroll(c).Grow(1).Basis(0).FillWidth().ClipX()
		if sc.PullToRefresh(&p.subsRefresh) {
			p.loadSubscriptions(h)
		}
		sc.Children(func() {
			ui.Column(c).FillWidth().Padding(48, 24).Gap(10).AlignItems(ui.Center).Children(func() {
				theme.DrawIcon(c, "subscriptions", 32, theme.ColorTextFaint, 1.6)
				ui.Text(c, "还没有订阅").FontSize(16).FontWeight(600).TextColor(theme.ColorTextMain)
				ui.Text(c, "在电脑端添加订阅后，这里会显示全部订阅、账号与检查计划。").
					FontSize(13).LineHeight(theme.LineHeightCompact).TextColor(theme.ColorTextMuted).TextAlign(ui.Center)
			})
		})
		return
	}

	cols := 3
	vw, _ := c.Size()
	switch {
	case vw >= 900:
		cols = 5
	case vw >= 600:
		cols = 4
	}
	rowCount := (len(p.subs) + cols - 1) / cols

	p.subsListSt.Key = func(i int) any {
		return fmt.Sprintf("sub-row-%d-%d", cols, i)
	}
	list := ui.List(c, &p.subsListSt, rowCount, func(rowIdx int) {
		start := rowIdx * cols
		ui.Row(c).FillWidth().Padding(10, 14, 6, 14).Gap(10).AlignItems(ui.Start).ClipX().Children(func() {
			for col := 0; col < cols; col++ {
				idx := start + col
				if idx < len(p.subs) {
					sub := p.subs[idx]
					p.renderSubscriptionGridCell(c, h, sub)
				} else {
					ui.Box(c).Grow(1).Basis(0)
				}
			}
		})
	}).Label("全部订阅网格").Grow(1).Basis(0).FillWidth()
	if list.PullToRefresh(&p.subsRefresh) {
		p.loadSubscriptions(h)
	}
}

func (p *Page) renderSubscriptionGridCell(c *ui.Context, h pagekit.Host, sub companionapi.SubscriptionDTO) {
	cell := ui.ButtonBase(c.Key(sub.ID)).
		Label("打开订阅："+sub.Name).
		Grow(1).
		Basis(0).
		Padding(6, 4).
		Radius(12).
		MinHeight(44).
		ClipX()
	cell.Children(func() {
		ui.Column(c).FillWidth().AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
			p.renderCircleAvatar(c, h, sub.AvatarPath, sub.Name, sub.ID, 64, sub.Live || sub.Recording)

			if sub.Recording {
				ui.Row(c).Padding(2, 8).Radius(999).Background(theme.ColorError).AlignItems(ui.Center).Gap(4).Shrink(0).Children(func() {
					ui.Box(c).Size(6, 6).Radius(3).Background(theme.ColorWhite)
					ui.Text(c, "录制中").FontSize(11).FontWeight(600).TextColor(theme.ColorWhite).SingleLine()
				})
			} else if sub.Live {
				ui.Box(c).Padding(2, 8).Radius(999).Background(theme.ColorLive).Shrink(0).Children(func() {
					ui.Text(c, "直播").FontSize(11).FontWeight(600).TextColor(theme.ColorWhite).SingleLine()
				})
			}

			ui.Text(c, sub.Name).
				FontSize(13).
				FontWeight(600).
				TextColor(theme.ColorTextMain).
				TextAlign(ui.Center).
				MaxLines(2)
		})
	})
	if cell.Clicked() {
		p.enterDetail(h, sub.ID)
		h.PushRoute("/sub/" + url.PathEscape(sub.ID))
	}
}

func (p *Page) renderAllAccountsSegment(c *ui.Context, h pagekit.Host) {
	if p.accountsLoading && !p.accountsLoaded {
		p.renderLoadingSkeleton(c)
		return
	}
	if p.accountsErr != "" && len(p.accounts) == 0 {
		p.renderLoadError(c, p.accountsErr, func() { p.loadAccounts(h) })
		return
	}

	// Build filtered accounts grouped in fixed site order: B站 → 抖音 → 微博 → X.
	filtered := make([]companionapi.SubscriptionAccountDTO, 0, len(p.accounts))
	for _, acc := range p.accounts {
		if p.siteFilter != "all" && normalizeSite(acc.Site) != p.siteFilter {
			continue
		}
		filtered = append(filtered, acc)
	}
	filtered = sortAccountsBySiteOrder(filtered)

	type groupedRow struct {
		isHeader bool
		header   string
		acc      companionapi.SubscriptionAccountDTO
	}
	var rows []groupedRow
	lastSite := ""
	siteCounts := make(map[string]int)
	for _, acc := range filtered {
		siteCounts[normalizeSite(acc.Site)]++
	}
	for _, acc := range filtered {
		s := normalizeSite(acc.Site)
		if s != lastSite {
			lastSite = s
			rows = append(rows, groupedRow{
				isHeader: true,
				header:   fmt.Sprintf("%s（%d）", siteShortLabel(s), siteCounts[s]),
			})
		}
		rows = append(rows, groupedRow{acc: acc})
	}

	ui.Column(c).Grow(1).Basis(0).FillWidth().ClipX().Children(func() {
		// Site filter bar.
		ui.Row(c).FillWidth().Padding(10, 14, 8, 14).Gap(6).AlignItems(ui.Center).Wrap().Shrink(0).ClipX().Children(func() {
			filters := []struct {
				id, label string
			}{
				{"all", "全部"},
				{"bilibili", "B 站"},
				{"douyin", "抖音"},
				{"weibo", "微博"},
				{"x", "X"},
			}
			for _, f := range filters {
				active := p.siteFilter == f.id
				bg := theme.ColorBgSecondary
				fg := theme.ColorTextMain
				border := theme.ColorBorder
				if active {
					bg = theme.ColorAccentBg
					fg = theme.ColorAccent
					border = theme.ColorAccent
				}
				pill := ui.Button(c.Key("acct-filter-"+f.id), "").
					Label("筛选站点："+f.label).
					Radius(999).
					Padding(6, 12).
					MinHeight(34).
					Background(bg).
					Border(1, border).
					Center()
				pill.Children(func() {
					ui.Text(c, f.label).FontSize(12).FontWeight(600).TextColor(fg).SingleLine()
				})
				if pill.Clicked() {
					p.siteFilter = f.id
				}
			}
			ui.Text(c, fmt.Sprintf("%d 个账号", len(filtered))).
				FontSize(12).TextColor(theme.ColorTextMuted).SingleLine().Padding(0, 4)
		})

		if len(rows) == 0 {
			sc := ui.Scroll(c).Grow(1).Basis(0).FillWidth().ClipX()
			if sc.PullToRefresh(&p.accountsRefresh) {
				p.loadAccounts(h)
			}
			sc.Children(func() {
				ui.Column(c).FillWidth().Padding(48, 24).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "没有符合筛选的订阅账号").FontSize(15).FontWeight(600).TextColor(theme.ColorTextMain)
				})
			})
			return
		}

		p.accountsListSt.Key = func(i int) any {
			if i >= 0 && i < len(rows) {
				if rows[i].isHeader {
					return "hdr:" + rows[i].header
				}
				ak := firstNonEmpty(rows[i].acc.AccountKey, normalizeSite(rows[i].acc.Site)+":"+strings.TrimSpace(rows[i].acc.UID))
				return ak + ":" + rows[i].acc.SubID
			}
			return i
		}
		list := ui.List(c, &p.accountsListSt, len(rows), func(i int) {
			if i < 0 || i >= len(rows) {
				return
			}
			row := rows[i]
			if row.isHeader {
				ui.Row(c).FillWidth().Padding(10, 14, 4, 14).Background(theme.ColorBgPanel).ClipX().Children(func() {
					ui.Text(c, row.header).FontSize(12).FontWeight(600).TextColor(theme.ColorTextMuted).SingleLine()
				})
				return
			}
			p.renderAccountListRow(c, h, row.acc)
		}).Label("全部账号列表").Grow(1).Basis(0).FillWidth()
		if list.PullToRefresh(&p.accountsRefresh) {
			p.loadAccounts(h)
		}
	})
}

func (p *Page) renderAccountListRow(c *ui.Context, h pagekit.Host, acc companionapi.SubscriptionAccountDTO) {
	btn := ui.ButtonBase(c).
		Label("打开账号所属订阅："+firstNonEmpty(acc.Name, acc.UID)).
		FillWidth().
		Padding(10, 14).
		MinHeight(56).
		Background(theme.ColorBgMain).
		BorderWidth(0, 0, 1, 0).
		BorderColor(theme.ColorBorder).
		ClipX()
	btn.Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(10).ClipX().Children(func() {
			liveRing := isLiveRecordableSite(acc.Site) && acc.LiveSupported && (acc.LiveStatus == 1 || acc.Recording)
			p.renderCircleAvatar(c, h, acc.AvatarPath, firstNonEmpty(acc.Name, acc.UID), acc.AccountKey, 40, liveRing)

			ui.Column(c).Grow(1).Shrink(1).Gap(3).ClipX().Children(func() {
				ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
					ui.Text(c, firstNonEmpty(acc.Name, acc.UID)).
						FontSize(14).FontWeight(600).TextColor(theme.ColorTextMain).SingleLine().Shrink(1)
					renderSiteTag(c, acc.Site)
				})
				meta := make([]string, 0, 2)
				if handle := formatAccountHandle(acc); handle != "" {
					meta = append(meta, handle)
				}
				meta = append(meta, formatWorksText(acc))
				ui.Text(c, strings.Join(meta, " · ")).
					FontSize(12).TextColor(theme.ColorTextMuted).SingleLine()
			})

			if acc.SubName != "" {
				ui.Box(c).Padding(4, 8).Radius(999).Background(theme.ColorBgSecondary).Border(1, theme.ColorBorder).Shrink(0).Children(func() {
					ui.Text(c, acc.SubName).FontSize(11).FontWeight(500).TextColor(theme.ColorTextMuted).SingleLine()
				})
			}
		})
	})
	if btn.Clicked() && acc.SubID != "" {
		p.enterDetail(h, acc.SubID)
		h.PushRoute("/sub/" + url.PathEscape(acc.SubID))
	}
}

func (p *Page) renderCheckPlanSegment(c *ui.Context, h pagekit.Host) {
	if p.planLoading && !p.planLoaded {
		p.renderLoadingSkeleton(c)
		return
	}
	if p.planErr != "" && len(p.plan) == 0 {
		p.renderLoadError(c, p.planErr, func() { p.loadCheckPlan(h) })
		return
	}

	ui.Column(c).Grow(1).Basis(0).FillWidth().Background(theme.ColorBgPanel).ClipX().Children(func() {
		// Top bar inside 检查计划: summary + "检查全部" button.
		ui.Row(c).
			FillWidth().
			Padding(12, 14).
			Background(theme.ColorBgMain).
			BorderWidth(0, 0, 1, 0).
			BorderColor(theme.ColorBorder).
			AlignItems(ui.Center).
			Justify(ui.SpaceBetween).
			Gap(10).
			Shrink(0).
			ClipX().
			Children(func() {
				ui.Column(c).Grow(1).Shrink(1).Gap(2).ClipX().Children(func() {
					summary := p.planStatus
					if summary == "" {
						summary = fmt.Sprintf("共 %d 个订阅账号排程", len(p.plan))
					}
					ui.Text(c, summary).FontSize(13).FontWeight(600).TextColor(theme.ColorTextMain).MaxLines(2)
					ui.Text(c, "仅查看电脑端检查排程；手动触发按电脑端规则与额度执行").
						FontSize(11).TextColor(theme.ColorTextMuted).MaxLines(2)
				})
				btnText := "检查全部"
				if p.checking {
					btnText = "检查中…"
				}
				checkAllBtn := theme.PrimaryPillButton(c, btnText, "检查全部订阅").
					Disabled(p.checking || len(p.plan) == 0).
					Shrink(0)
				if checkAllBtn.Clicked() && !p.checking {
					p.startCheckAll(h)
				}
			})

		if len(p.plan) == 0 {
			sc := ui.Scroll(c).Grow(1).Basis(0).FillWidth().ClipX()
			if sc.PullToRefresh(&p.planRefresh) {
				p.loadCheckPlan(h)
			}
			sc.Children(func() {
				ui.Column(c).FillWidth().Padding(48, 24).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "当前没有检查计划").FontSize(15).FontWeight(600).TextColor(theme.ColorTextMain)
					ui.Text(c, "添加订阅账号后，这里会显示各账号的上次检查与下次计划时间。").
						FontSize(13).TextColor(theme.ColorTextMuted).TextAlign(ui.Center)
				})
			})
			return
		}

		p.planListSt.Key = func(i int) any {
			if i >= 0 && i < len(p.plan) {
				return p.plan[i].SubID + ":" + p.plan[i].Site + ":" + p.plan[i].UID
			}
			return i
		}
		list := ui.List(c, &p.planListSt, len(p.plan), func(i int) {
			if i < 0 || i >= len(p.plan) {
				return
			}
			ui.Column(c).FillWidth().Padding(6, 14).ClipX().Children(func() {
				p.renderCheckPlanCard(c, h, p.plan[i])
			})
		}).Label("检查计划列表").Grow(1).Basis(0).FillWidth()
		if list.PullToRefresh(&p.planRefresh) {
			p.loadCheckPlan(h)
		}
	})
}

func (p *Page) renderCheckPlanCard(c *ui.Context, h pagekit.Host, row companionapi.CheckPlanRowDTO) {
	acctKey := normalizeSite(row.Site) + ":" + strings.TrimSpace(row.UID)
	thisChecking := p.checking && p.checkingAccountKey == acctKey

	ui.Column(c).
		Label("检查计划卡片："+acctKey).
		FillWidth().
		Padding(12, 14).
		Radius(12).
		Background(theme.ColorBgMain).
		Border(1, theme.ColorBorder).
		Gap(8).
		ClipX().
		Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
				ui.Column(c).Grow(1).Shrink(1).Gap(3).ClipX().Children(func() {
					ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
						ui.Text(c, firstNonEmpty(row.Name, row.UID)).
							FontSize(14).FontWeight(600).TextColor(theme.ColorTextMain).SingleLine().Shrink(1)
						renderSiteTag(c, row.Site)
					})
					if row.SubName != "" {
						ui.Text(c, "所属订阅："+row.SubName).FontSize(12).TextColor(theme.ColorTextMuted).SingleLine()
					}
				})

				btnLabel := "检查"
				if thisChecking {
					btnLabel = "检查中…"
				}
				btn := ui.Button(c, "").
					Label("检查账号："+firstNonEmpty(row.Name, row.UID)).
					Radius(999).
					Padding(8, 16).
					MinHeight(40).
					Background(theme.ColorBgMain).
					Border(1, theme.ColorBorderStrong).
					Disabled(p.checking).
					Shrink(0).
					Center()
				btn.Children(func() {
					ui.Text(c, btnLabel).FontSize(13).FontWeight(600).TextColor(theme.ColorTextMain).SingleLine()
				})
				if btn.Clicked() && !p.checking {
					p.startCheckAccount(h, row)
				}
			})

			// Last check status.
			lastText := "更新检查：还没有检查过"
			lastCol := theme.ColorTextMuted
			if row.LastCheckedAt != "" {
				lastText = "更新检查：上次 " + formatTimeShort(row.LastCheckedAt)
				if row.LastKind != "" {
					lastText += " · " + formatCheckKindLabel(row.LastKind)
				}
			}
			if row.Note != "" {
				lastText += " · " + row.Note
				if strings.Contains(row.LastKind, "fail") || strings.Contains(row.LastKind, "error") ||
					strings.Contains(row.LastKind, "risk") || strings.Contains(row.LastKind, "rate") ||
					strings.Contains(row.LastKind, "auth") {
					lastCol = theme.ColorError
				}
			}
			ui.Text(c, lastText).FontSize(12).LineHeight(theme.LineHeightCompact).TextColor(lastCol).MaxLines(2)

			// Next schedule.
			nextText := formatPlanScheduleText(row)
			if nextText != "" {
				nextCol := theme.ColorTextMuted
				if row.AfterBackoff {
					nextCol = theme.ColorWarnText
				}
				ui.Text(c, nextText).FontSize(12).TextColor(nextCol).SingleLine()
			}

			// Live check info (for B站/抖音 auto-record or Weibo/X unsupported note).
			if row.LiveNote != "" {
				ui.Text(c, row.LiveNote).FontSize(12).TextColor(theme.ColorTextMuted).MaxLines(2)
			} else if row.AutoRecord && isLiveRecordableSite(row.Site) {
				liveLine := "开播检查：已开启开播录制"
				if row.LiveCheckedAt != "" {
					liveLine = "开播检查：上次 " + formatTimeShort(row.LiveCheckedAt)
				}
				if row.LiveEverySec > 0 {
					mins := row.LiveEverySec / 60
					if mins > 0 {
						liveLine += fmt.Sprintf(" · 约每 %d 分钟", mins)
					} else {
						liveLine += fmt.Sprintf(" · 约每 %d 秒", row.LiveEverySec)
					}
				}
				ui.Text(c, liveLine).FontSize(12).TextColor(theme.ColorTextMuted).SingleLine()
			}
		})
}

func formatCheckKindLabel(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "ok", "success":
		return "正常"
	case "auth_required":
		return "未登录"
	case "risk_control":
		return "风控"
	case "rate_limited":
		return "限流"
	case "quota_deferred":
		return "额度顺延"
	case "error", "failed":
		return "失败"
	default:
		return kind
	}
}

func formatPlanScheduleText(row companionapi.CheckPlanRowDTO) string {
	if row.NextAt != "" {
		t := formatTimeShort(row.NextAt)
		if row.AfterBackoff {
			return "下次检查：退避后 " + t
		}
		return "下次检查：" + t
	}
	if row.Plan != "" {
		return "排程：" + row.Plan
	}
	return ""
}

func (p *Page) renderCircleAvatar(c *ui.Context, h pagekit.Host, avatarPath, name, seed string, size float32, liveRing bool) {
	bmp := p.lookupBitmap(h, avatarPath, int(size*2))
	ringPad := float32(0)
	if liveRing {
		ringPad = 3
	}
	outer := size + ringPad*2
	box := ui.Box(c).Size(outer, outer).Radius(outer / 2).Shrink(0).Center()
	if liveRing {
		box.Border(2.5, theme.ColorLive)
	}
	box.Children(func() {
		col := avatarColor(firstNonEmpty(seed, name))
		inner := ui.Box(c).Size(size, size).Radius(size / 2).Background(col).Clip().Center()
		inner.Children(func() {
			if bmp != nil {
				ui.Image(c, bmp).Size(size, size).Fit(ui.Cover)
				return
			}
			ui.Text(c, firstGlyph(name)).FontSize(size * 0.38).FontWeight(600).TextColor(theme.ColorWhite).SingleLine()
		})
	})
}

func renderSiteTag(c *ui.Context, site string) {
	label := siteShortLabel(site)
	if label == "" {
		return
	}
	ui.Text(c, label).FontSize(12).TextColor(theme.ColorTextMuted).SingleLine().Shrink(0)
}

func (p *Page) renderLoadingSkeleton(c *ui.Context) {
	ui.Column(c).Grow(1).Basis(0).FillWidth().Padding(16, 14).Gap(12).ClipX().Children(func() {
		for i := 0; i < 4; i++ {
			ui.Row(c.Key(i)).FillWidth().Padding(12).Radius(12).Background(theme.ColorBgSecondary).Gap(12).AlignItems(ui.Center).Children(func() {
				ui.Box(c).Size(44, 44).Radius(22).Background(theme.ColorBorder).Shrink(0)
				ui.Column(c).Grow(1).Gap(6).Children(func() {
					ui.Box(c).FillWidth().Height(14).Radius(4).Background(theme.ColorBorder)
					ui.Box(c).Width(140).Height(11).Radius(4).Background(theme.ColorBorder)
				})
			})
		}
	})
}

func (p *Page) renderLoadError(c *ui.Context, msg string, onRetry func()) {
	ui.Column(c).Grow(1).Basis(0).FillWidth().Padding(40, 24).Gap(12).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
		theme.DrawIcon(c, "alert-circle", 32, theme.ColorError, 1.8)
		ui.Text(c, msg).FontSize(14).LineHeight(theme.LineHeightCompact).TextColor(theme.ColorTextMain).TextAlign(ui.Center)
		if theme.PrimaryPillButton(c, "重试", "重试加载订阅").Clicked() && onRetry != nil {
			onRetry()
		}
	})
}
