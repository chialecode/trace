package ui

import (
	"github.com/egoist/mygo/ui"
)

// homeStripStride is the width of one entry of the narrow window's avatar
// strip on Home.
const homeStripStride = 76

// renderHomeUpdatedAvatarsRow is the narrow window's form of the Home
// panel's subscriptions: a strip of round avatars with the name below,
// "全部动态" first, live rings and unread dots as in the panel.
// Only the entries scrolled into view are built.
func (a *App) renderHomeUpdatedAvatarsRow(c *ui.Context) {
	rows := a.homeSubscriptionRows()
	if len(rows) == 0 {
		return
	}
	total := len(rows) + 1
	lo := max(0, int(a.homeStripScroll.X/homeStripStride)-2)
	hi := min(total, lo+40)
	ui.Column(c).
		Label("主页订阅头像条").
		FillWidth().
		Padding(10, 8).
		BorderWidth(0, 0, 1, 0).
		BorderColor(ColorBorder).
		Background(ColorBgMain).
		Shrink(0).
		ClipX().
		Children(func() {
			ui.ScrollHorizontal(c).TrackScroll(&a.homeStripScroll).FillWidth().Height(84).Shrink(0).Children(func() {
				ui.Row(c).AlignItems(ui.Start).Children(func() {
					if lo > 0 {
						ui.Box(c).Width(float32(lo * homeStripStride)).Height(1).Shrink(0)
					}
					for i := lo; i < hi; i++ {
						if i == 0 {
							a.renderHomeStripAll(c)
							continue
						}
						actor := rows[i-1]
						a.renderHomeStripEntry(c, actor.Name, a.HomeActorFilter != nil && a.HomeActorFilter.Key == actor.Key, func() {
							a.renderHomeSubAvatar(c, actor, 52, ColorBgMain, func() { a.openHomeActorFeed(actor) })
						}, func() { a.openHomeActorFeed(actor) })
					}
					if hi < total {
						ui.Box(c).Width(float32((total - hi) * homeStripStride)).Height(1).Shrink(0)
					}
				})
			})
		})
}

func (a *App) renderHomeStripAll(c *ui.Context) {
	active := a.HomeActorFilter == nil
	a.renderHomeStripEntry(c, "全部动态", active, func() {
		btn := ui.ButtonBase(c).Label("主页订阅头像：全部动态").Size(52, 52).Radius(26).Shrink(0).Justify(ui.Center).AlignItems(ui.Center)
		if active {
			btn.Background(ColorAccentBg)
		} else {
			btn.Background(ColorBgSecondary)
		}
		btn.Children(func() {
			col := ColorTextMuted
			if active {
				col = ColorAccent
			}
			DrawIcon(c, "home", 22, col, 1.6)
		})
		if btn.Clicked() {
			a.closeHomeActorFeed()
		}
	}, a.closeHomeActorFeed)
}

func (a *App) renderHomeStripEntry(c *ui.Context, name string, active bool, avatar func(), open func()) {
	ui.Column(c).Width(homeStripStride).Shrink(0).AlignItems(ui.Center).Gap(4).ClipX().Children(func() {
		ui.Box(c).Height(58).Width(58).Justify(ui.Center).AlignItems(ui.Center).Shrink(0).Children(avatar)
		nameBtn := ui.ButtonBase(c).Width(homeStripStride - 8).Padding(0).Justify(ui.Center).AlignItems(ui.Center).ClipX()
		nameBtn.Children(func() {
			t := ui.Text(c, name).FontSize(12).SingleLine()
			if active {
				t.TextColor(ColorAccent).FontWeight(600)
			} else {
				t.TextColor(ColorTextMain)
			}
		})
		if nameBtn.Clicked() {
			open()
		}
	})
}

func (a *App) renderHomeRoute(c *ui.Context) {
	if a.FeedError != "" {
		ui.Row(c).FillWidth().Padding(8, 16).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, a.FeedError).FontSize(13).TextColor(ColorError).MaxLines(2).Grow(1)
			if OutlinePillButton(c, "重试", "重试主页分页").Clicked() {
				if len(a.FeedItems) == 0 {
					a.loadInitialFeed()
				} else {
					a.loadFeedEdge(a.feedFailedBefore)
				}
			}
		})
	}
	first, last := a.FeedListState.Visible()
	restoring := a.HomeActorFilter == nil && a.stepHomeRestore(c, first, last)
	if !restoring {
		a.markHomeSeen(first, last)
	}
	// Posts cached while the timeline is open, or newer than the position
	// it opened at, are fetched when it reaches the top, the rows in view
	// staying where they are; nothing is counted or announced as unread.
	if !restoring && a.HomeActorFilter == nil && first >= 0 && first <= 2 && a.feedMoreBefore && a.FeedError == "" {
		a.loadFeedEdge(true)
	}
	if !restoring && last >= 0 && last >= len(a.FeedItems)-3 && a.feedMoreAfter && a.FeedError == "" {
		a.loadFeedEdge(false)
	}
	// Where the main timeline is read: the top row's place against the
	// list's, both as the last frame laid them out. The rows are built as
	// the list lays out, so the top row notes it.
	topKey := ""
	if !restoring && a.HomeActorFilter == nil && first >= 0 && first < len(a.FeedItems) && last >= first {
		topKey = a.FeedItems[first].FeedKey
	}

	if len(a.FeedItems) == 0 {
		ui.Column(c).Grow(1).FillWidth().Padding(48, 24).AlignItems(ui.Center).Gap(12).ClipX().Children(func() {
			if a.HomeActorFilter != nil {
				ui.Text(c, "该订阅下还没有本机已缓存的内容").FontSize(17).FontWeight(600).TextColor(ColorTextMain).SingleLine()
				if OutlinePillButton(c, "返回主时间线", "空状态返回主页时间线").Clicked() {
					a.closeHomeActorFeed()
				}
				return
			}
			ui.Text(c, "还没有缓存的内容").FontSize(17).FontWeight(600).TextColor(ColorTextMain).SingleLine()
			ui.Text(c, "到「解析」里打开一位 UP 主、抖音作者、微博博主或 X 用户，在动态、作品、微博或帖子上点「缓存」").
				FontSize(14).TextColor(ColorTextMuted).MaxLines(2)
			if AccentPillButton(c, "去解析", "去解析链接").Clicked() {
				a.Navigate(RouteParse)
			}
		})
		return
	}

	list := ui.List(c, a.FeedListState, len(a.FeedItems), func(i int) {
		if i < 0 || i >= len(a.FeedItems) {
			return
		}
		entry := a.FeedItems[i]
		post := a.feedEntryToPostCard(entry)
		row := ui.Column(c).Label("信息流行 " + entry.FeedKey).FillWidth().Children(func() { a.renderPostCard(c, post) })
		if topKey != "" && entry.FeedKey == topKey {
			rb, lb := row.Bounds(), a.homeListBounds
			a.noteHomePosition(c, i, rb.Y, lb.Y, rb.H > 0 && lb.H > 0)
		}
		if i == len(a.FeedItems)-1 && !a.feedMoreAfter {
			ui.Row(c).FillWidth().Padding(18, 16).Justify(ui.Center).Children(func() {
				ui.Text(c, "已经到最早的缓存了").FontSize(13).TextColor(ColorTextMuted).SingleLine()
			})
		}
	})
	list.Label("主页信息流列表").TrackScroll(&a.FeedScroll).Grow(1).FillWidth()
	// As the last frame laid the list out; the rows, built as this frame
	// lays out, read it.
	a.homeListBounds = list.Bounds()
}
