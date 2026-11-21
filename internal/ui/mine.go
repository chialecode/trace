package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

// 我的 (docs/ui.md §3.11, DEC-047): the signed-in accounts' own lists, read
// only. Everything lives in memory for this run: the expanded site, the
// chosen function and every list with its cursor and scroll position. Signing
// out of or switching a site clears that site's lists.

// RouteMine is the 我的 page.
const RouteMine = "/mine"

var mineSites = []struct{ ID, Name, Short string }{
	{string(site.Bilibili), "哔哩哔哩", "B 站"},
	{string(site.Douyin), "抖音", "抖音"},
}

// mineNow is the clock for the history date groups (tests pin it).
var mineNow = time.Now

// mineList is one list: a function of a site with one folder or filter.
type mineList struct {
	Items   []service.AccountItem
	Cursor  string
	HasMore bool
	Total   int
	Loaded  bool // the first page arrived
	Err     string
	ErrKind string
	State   ui.ListState
	gen     int
	seen    map[string]bool

	// Cached date-grouped history rows (invalidated on item count or local day change).
	histRows   []mineRow
	histItemN  int
	histDayKey string
}

// MineState is the in-memory state of 我的.
type MineState struct {
	Expanded   string // site whose functions show in the panel ("" = both folded)
	Site, Kind string // chosen function
	Picker     bool   // narrow window: the chooser is shown instead of the list
	Folders    map[string][]service.AccountFolder
	FolderErr  map[string]string
	Folder     map[string]string // chosen folder per site/kind
	Lists      map[string]*mineList
	owner      map[string]string // site -> account the lists belong to
	gen        int               // list identities (pending keys)
	clearGen   int               // bumped when a site is cleared; guards folder results
}

func (m *MineState) ensure() {
	if m.Folders == nil {
		m.Folders = map[string][]service.AccountFolder{}
		m.FolderErr = map[string]string{}
		m.Folder = map[string]string{}
		m.Lists = map[string]*mineList{}
		m.owner = map[string]string{}
	}
}

func (a *App) mineAccountID(siteID string) string {
	st := a.backend.StatusOf(siteID)
	if !st.LoggedIn {
		return ""
	}
	return fmt.Sprintf("%d|%s|%s", st.Mid, st.UID, st.SecUID)
}

// mineOnAccountChanged clears a site's lists after a sign-out, an expiry or
// a different account; a renewal of the same account keeps them. When siteID
// is empty (broadcast account change), every site is checked.
func (a *App) mineOnAccountChanged(siteID string) {
	if a.Mine.Lists == nil {
		return
	}
	if siteID == "" {
		for _, s := range mineSites {
			a.mineOnAccountChanged(s.ID)
		}
		return
	}
	id := a.mineAccountID(siteID)
	if id == "" || (a.Mine.owner[siteID] != "" && a.Mine.owner[siteID] != id) {
		a.mineClearSite(siteID)
	}
}

func mineFnKey(siteID, kind string) string { return siteID + "/" + kind }

func mineHasFolders(siteID, kind string) bool {
	switch mineFnKey(siteID, kind) {
	case "bilibili/" + service.AccountFollowing, "bilibili/" + service.AccountFavorites,
		"bilibili/" + service.AccountToView, "douyin/" + service.AccountCollection:
		return true
	}
	return false
}

func mineIsWorkGrid(siteID, kind string) bool {
	return siteID == string(site.Douyin) && kind != service.AccountFollowing
}

func mineSiteShort(siteID string) string {
	for _, s := range mineSites {
		if s.ID == siteID {
			return s.Short
		}
	}
	return siteID
}

func mineKindLabel(siteID, kind string) string {
	switch kind {
	case service.AccountFollowing:
		return "关注"
	case service.AccountFavorites:
		return "收藏夹"
	case service.AccountToView:
		return "稍后再看"
	case service.AccountHistory:
		return "历史记录"
	case service.AccountLikes:
		return "喜欢"
	case service.AccountCollection:
		return "收藏"
	case service.AccountWatchHistory:
		return "观看历史"
	}
	return kind
}

func mineKindIcon(kind string) string {
	switch kind {
	case service.AccountFollowing:
		return "user"
	case service.AccountFavorites, service.AccountCollection:
		return "star"
	case service.AccountToView:
		return "clock"
	case service.AccountHistory, service.AccountWatchHistory:
		return "history"
	case service.AccountLikes:
		return "heart"
	}
	return "grid"
}

func mineEmptyText(kind string) string {
	switch kind {
	case service.AccountFollowing:
		return "还没有关注"
	case service.AccountFavorites, service.AccountCollection:
		return "收藏夹是空的"
	case service.AccountToView:
		return "稍后再看是空的"
	case service.AccountHistory, service.AccountWatchHistory:
		return "还没有历史记录"
	case service.AccountLikes:
		return "还没有喜欢的作品"
	}
	return "没有内容"
}

// mineSiteURL is the site's own page of a function, for "在浏览器中打开".
func (a *App) mineSiteURL(siteID, kind string) string {
	if siteID == string(site.Bilibili) {
		mid := a.backend.StatusOf(siteID).Mid
		switch kind {
		case service.AccountFollowing:
			if mid > 0 {
				return fmt.Sprintf("https://space.bilibili.com/%d/fans/follow", mid)
			}
		case service.AccountFavorites:
			if mid > 0 {
				return fmt.Sprintf("https://space.bilibili.com/%d/favlist", mid)
			}
		case service.AccountToView:
			return "https://www.bilibili.com/watchlater/#/list"
		case service.AccountHistory:
			return "https://www.bilibili.com/account/history"
		}
		return "https://www.bilibili.com/"
	}
	switch kind {
	case service.AccountFollowing:
		return "https://www.douyin.com/user/self?showTab=following"
	case service.AccountLikes:
		return "https://www.douyin.com/user/self?showTab=like"
	case service.AccountCollection:
		return "https://www.douyin.com/user/self?showTab=favorite_collection"
	}
	return "https://www.douyin.com/user/self"
}

// ---------------------------------------------------------------------------
// State changes

func (a *App) mineToggleSite(siteID string) {
	if a.Mine.Expanded == siteID {
		a.Mine.Expanded = ""
	} else {
		a.Mine.Expanded = siteID
	}
}

// OpenMine shows 我的 with siteID expanded and, when kind is set, that function chosen.
func (a *App) OpenMine(siteID, kind string) {
	a.Navigate(RouteMine)
	a.Mine.Expanded = siteID
	if kind != "" {
		a.mineSelect(siteID, kind)
	}
}

// mineSelect chooses a function; the first choice of a list requests its
// first page, later choices only show what is in memory.
func (a *App) mineSelect(siteID, kind string) {
	a.Mine.ensure()
	a.Mine.Site, a.Mine.Kind, a.Mine.Picker = siteID, kind, false
	a.PostMenuKey, a.DouyinCardMenuID = "", ""
	fk := mineFnKey(siteID, kind)
	if mineHasFolders(siteID, kind) {
		if _, ok := a.Mine.Folders[fk]; !ok {
			a.mineLoadFolders(siteID, kind)
			return
		}
	}
	a.mineEnsureList()
}

func (a *App) mineFolderID(siteID, kind string) string {
	fk := mineFnKey(siteID, kind)
	if id, ok := a.Mine.Folder[fk]; ok {
		return id
	}
	for _, f := range a.Mine.Folders[fk] {
		if f.IsDefault {
			return f.ID
		}
	}
	if fs := a.Mine.Folders[fk]; len(fs) > 0 {
		return fs[0].ID
	}
	return ""
}

func (a *App) mineListKey() string {
	return mineFnKey(a.Mine.Site, a.Mine.Kind) + "/" + a.mineFolderID(a.Mine.Site, a.Mine.Kind)
}

func (a *App) mineCurrentList() *mineList {
	if a.Mine.Kind == "" || a.Mine.Lists == nil {
		return nil
	}
	return a.Mine.Lists[a.mineListKey()]
}

// mineEnsureList requests the first page of the current list once.
func (a *App) mineEnsureList() {
	a.Mine.ensure()
	key := a.mineListKey()
	if _, ok := a.Mine.Lists[key]; ok {
		return
	}
	if a.Mine.owner[a.Mine.Site] == "" {
		a.Mine.owner[a.Mine.Site] = a.mineAccountID(a.Mine.Site)
	}
	a.Mine.gen++
	l := &mineList{gen: a.Mine.gen, seen: map[string]bool{}}
	a.Mine.Lists[key] = l
	a.mineLoadPage(key, l)
}

// mineRefresh starts the current list again from its first page.
func (a *App) mineRefresh() {
	if a.Mine.Kind == "" {
		return
	}
	a.Mine.ensure()
	fk := mineFnKey(a.Mine.Site, a.Mine.Kind)
	if mineHasFolders(a.Mine.Site, a.Mine.Kind) && a.Mine.FolderErr[fk] != "" {
		delete(a.Mine.Folders, fk)
		a.mineLoadFolders(a.Mine.Site, a.Mine.Kind)
		return
	}
	delete(a.Mine.Lists, a.mineListKey())
	a.mineEnsureList()
}

// mineLoadMore requests the next page unless a request of this list is running.
func (a *App) mineLoadMore() {
	key := a.mineListKey()
	l := a.Mine.Lists[key]
	if l == nil || !l.HasMore || l.Err != "" {
		return
	}
	a.mineLoadPage(key, l)
}

func (a *App) mineRetry() {
	l := a.mineCurrentList()
	if l == nil {
		a.mineRefresh()
		return
	}
	l.Err, l.ErrKind = "", ""
	a.mineLoadPage(a.mineListKey(), l)
}

func (a *App) minePendingKey(key string, l *mineList) string {
	return fmt.Sprintf("mine:%s#%d", key, l.gen)
}

func (a *App) mineLoadPage(key string, l *mineList) {
	siteID, kind := a.Mine.Site, a.Mine.Kind
	folder := a.mineFolderID(siteID, kind)
	cursor := l.Cursor
	if !l.Loaded {
		cursor = ""
	}
	backend := a.backend
	requestGuarded(a, a.minePendingKey(key, l), func() bool {
		return a.Mine.Lists != nil && a.Mine.Lists[key] == l
	}, func(ctx context.Context) (service.AccountPage, error) {
		return backend.AccountPage(ctx, siteID, kind, folder, cursor)
	}, func(p service.AccountPage) {
		for _, it := range p.Items {
			id := it.Kind + ":" + it.ID
			if it.ID != "" && l.seen[id] {
				continue
			}
			if it.ID != "" {
				l.seen[id] = true
			}
			l.Items = append(l.Items, it)
		}
		l.Loaded, l.Cursor, l.HasMore, l.Err, l.ErrKind = true, p.Cursor, p.HasMore, "", ""
		if p.Total > 0 {
			l.Total = p.Total
		}
	}, func(err error) {
		l.Err, l.ErrKind = userErrorText(err), site.KindOf(err)
	})
}

func (a *App) mineLoadFolders(siteID, kind string) {
	a.Mine.ensure()
	fk := mineFnKey(siteID, kind)
	if a.Mine.owner[siteID] == "" {
		a.Mine.owner[siteID] = a.mineAccountID(siteID)
	}
	delete(a.Mine.FolderErr, fk)
	// Only a sign-out or another account invalidates folders; choosing another
	// function meanwhile must not drop them. Including clearGen in the pending
	// key ensures a new account's folder load is never deduplicated behind an
	// old account's still-in-flight request.
	gen := a.Mine.clearGen
	backend := a.backend
	requestGuarded(a, fmt.Sprintf("mine-folders:%s#%d", fk, gen), func() bool {
		return a.Mine.Folders != nil && a.Mine.clearGen == gen
	}, func(ctx context.Context) ([]service.AccountFolder, error) {
		return backend.AccountFolders(ctx, siteID, kind)
	}, func(fs []service.AccountFolder) {
		a.Mine.Folders[fk] = fs
		if a.Mine.Site == siteID && a.Mine.Kind == kind {
			a.mineEnsureList()
		}
	}, func(err error) {
		a.Mine.FolderErr[fk] = userErrorText(err)
	})
}

func (a *App) mineChooseFolder(id string) {
	a.Mine.Folder[mineFnKey(a.Mine.Site, a.Mine.Kind)] = id
	a.mineEnsureList()
}

// mineClearSite forgets every list and folder selection of siteID (sign-out, expiry or a new account).
func (a *App) mineClearSite(siteID string) {
	if a.Mine.Lists == nil || siteID == "" {
		return
	}
	prefix := siteID + "/"
	for k := range a.Mine.Lists {
		if strings.HasPrefix(k, prefix) {
			delete(a.Mine.Lists, k)
		}
	}
	for k := range a.Mine.Folders {
		if strings.HasPrefix(k, prefix) {
			delete(a.Mine.Folders, k)
		}
	}
	for k := range a.Mine.Folder {
		if strings.HasPrefix(k, prefix) {
			delete(a.Mine.Folder, k)
		}
	}
	for k := range a.Mine.FolderErr {
		if strings.HasPrefix(k, prefix) {
			delete(a.Mine.FolderErr, k)
		}
	}
	delete(a.Mine.owner, siteID)
	a.Mine.gen++
	a.Mine.clearGen++
	if a.Mine.Site == siteID {
		a.Mine.Kind = ""
	}
}

// ---------------------------------------------------------------------------
// Context panel and the narrow-window chooser

func (a *App) renderMinePanel(c *ui.Context) {
	ui.Column(c).Fill().ClipX().Children(func() {
		panelTitle(c, "我的")
		ui.Scroll(c).Grow(1).FillWidth().Children(func() {
			ui.Column(c).FillWidth().Padding(0, 8, 12, 8).Gap(2).ClipX().Children(func() {
				a.renderMineChooser(c)
			})
		})
	})
}

// renderMineChooser draws the two folding site groups and their functions.
func (a *App) renderMineChooser(c *ui.Context) {
	for _, s := range mineSites {
		siteInfo := s
		expanded := a.Mine.Expanded == siteInfo.ID
		st := a.backend.StatusOf(siteInfo.ID)
		header := ui.ButtonBase(c).Label("我的站点："+siteInfo.Name).FillWidth().Radius(8).Padding(8, 10).Gap(8).AlignItems(ui.Center).Justify(ui.Start).ClipX()
		if header.Hovered() {
			header.Background(ColorHover)
		}
		header.Children(func() {
			chev := "chevron-right"
			if expanded {
				chev = "chevron-down"
			}
			DrawIcon(c, chev, 14, ColorTextMuted, 1.8)
			ui.Text(c, siteInfo.Name).FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine().Grow(1)
			if st.LoggedIn {
				name := firstNonEmptyStrUI(st.Uname, st.UniqueID, siteInfo.Name)
				a.renderAccountAvatar(c, siteInfo.ID, name, firstNonEmptyStrUI(st.SecUID, st.UID), st.Mid, 24, false, "", nil, st.Face)
			} else {
				ui.Text(c, "未登录").FontSize(12).TextColor(ColorTextFaint).SingleLine()
			}
		})
		if header.Clicked() {
			a.mineToggleSite(siteInfo.ID)
		}
		if !expanded {
			continue
		}
		for _, sec := range a.backend.AccountSections(siteInfo.ID) {
			section := sec
			if !section.Available {
				action := "登录"
				if section.Reason == "登录已失效" {
					action = "重新登录"
				}
				ui.Row(c).FillWidth().Padding(6, 10, 6, 32).Gap(6).AlignItems(ui.Center).ClipX().Children(func() {
					ui.Text(c, section.Reason+" ·").FontSize(13).TextColor(ColorTextMuted).SingleLine()
					link := ui.ButtonBase(c).Label("我的登录："+siteInfo.Name).Padding(2, 4).Radius(6)
					if link.Hovered() {
						link.Background(ColorHover)
					}
					link.Children(func() {
						ui.Text(c, action).FontSize(13).FontWeight(500).TextColor(ColorAccent).SingleLine()
					})
					if link.Clicked() {
						a.OpenLoginForSite(siteInfo.ID)
					}
				})
				continue
			}
			active := a.Mine.Site == section.Site && a.Mine.Kind == section.Kind
			ui.Box(c).FillWidth().Padding(0, 0, 0, 14).Children(func() {
				if panelIconItem(c, mineKindIcon(section.Kind), section.Label, "我的功能："+siteInfo.Name+" "+section.Label, active) {
					a.mineSelect(section.Site, section.Kind)
				}
			})
		}
	}
}

// ---------------------------------------------------------------------------
// Page

func (a *App) mineTitle() string {
	if a.Mine.Kind == "" || (!a.panelShown && a.Mine.Picker) {
		return "我的"
	}
	return mineKindLabel(a.Mine.Site, a.Mine.Kind) + " · " + mineSiteShort(a.Mine.Site)
}

func (a *App) mineShowsList() bool {
	return a.Mine.Kind != "" && (a.panelShown || !a.Mine.Picker)
}

func (a *App) renderMineHeaderActions(c *ui.Context) {
	if !a.mineShowsList() {
		return
	}
	open := a.PostMenuKey == "mine:header"
	MoreMenuButton(c, "我的更多菜单", 32, &open, func(m *ui.Menu) {
		if m.Item("刷新").Chosen() {
			a.mineRefresh()
		}
		if m.Item("在浏览器中打开").Chosen() {
			a.showError(a.backend.Desktop().OpenExternalURL(a.mineSiteURL(a.Mine.Site, a.Mine.Kind)))
		}
	})
	keyedMenuOpen(&a.PostMenuKey, "mine:header", open)
}

func (a *App) renderMineRoute(c *ui.Context, width float32) {
	if !a.mineShowsList() {
		if a.panelShown {
			ui.Column(c).Grow(1).FillWidth().Padding(32).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
				ui.Text(c, "在左侧选择一个站点和功能").Label("我的选择提示").FontSize(14).TextColor(ColorTextMuted).SingleLine()
			})
			return
		}
		ui.Scroll(c).Grow(1).FillWidth().Children(func() {
			ui.Column(c).Label("我的选择列表").FillWidth().Padding(12, 12).Gap(2).ClipX().Children(func() {
				a.renderMineChooser(c)
			})
		})
		return
	}
	siteID, kind := a.Mine.Site, a.Mine.Kind
	for _, sec := range a.backend.AccountSections(siteID) {
		if !sec.Available {
			a.renderMineCenterNote(c, sec.Reason+"，登录"+mineSiteShort(siteID)+"后才能查看", "去登录", func() { a.OpenLoginForSite(siteID) })
			return
		}
	}
	a.Mine.ensure()
	fk := mineFnKey(siteID, kind)
	if mineHasFolders(siteID, kind) {
		if msg := a.Mine.FolderErr[fk]; msg != "" {
			a.renderMineCenterNote(c, msg, "重试", func() { a.mineLoadFolders(siteID, kind) })
			return
		}
		if _, ok := a.Mine.Folders[fk]; !ok {
			a.mineLoadFolders(siteID, kind)
			a.renderMinePlaceholders(c, siteID, kind)
			return
		}
	}
	a.mineEnsureList()
	l := a.mineCurrentList()
	a.renderMineToolbar(c, siteID, kind, l)
	switch {
	case l == nil || (!l.Loaded && l.Err == ""):
		a.renderMinePlaceholders(c, siteID, kind)
	case !l.Loaded && l.Err != "":
		a.renderMineCenterNote(c, l.Err, "重试", a.mineRetry)
	case len(l.Items) == 0 && !l.HasMore:
		ui.Column(c).Grow(1).FillWidth().Padding(32).AlignItems(ui.Center).Children(func() {
			ui.Text(c, mineEmptyText(kind)).Label("我的列表为空").FontSize(14).TextColor(ColorTextMuted).SingleLine()
		})
	case mineIsWorkGrid(siteID, kind):
		a.renderMineWorkGrid(c, width, l)
	default:
		a.renderMineRows(c, siteID, kind, l)
	}
}

func (a *App) renderMineCenterNote(c *ui.Context, msg, action string, do func()) {
	ui.Column(c).Grow(1).FillWidth().Padding(32, 24).AlignItems(ui.Center).Gap(12).ClipX().Children(func() {
		ui.Text(c, msg).Label("我的提示：" + msg).FontSize(14).TextColor(ColorTextMuted).MaxLines(3)
		if action != "" && OutlinePillButton(c, action, "我的"+action).Clicked() {
			do()
		}
	})
}

func (a *App) renderMinePlaceholders(c *ui.Context, siteID, kind string) {
	ui.Column(c).Label("我的首次加载占位").FillWidth().Padding(8, 16).Gap(12).ClipX().Children(func() {
		for i := 0; i < 6; i++ {
			ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Center).Children(func() {
				if kind == service.AccountFollowing {
					ui.Box(c).Size(40, 40).Radius(20).Background(ColorBgSecondary)
				} else {
					ui.Box(c).Size(144, 82).Radius(12).Background(ColorBgSecondary)
				}
				ui.Column(c).Grow(1).Gap(8).Children(func() {
					ui.Box(c).FillWidth().Height(12).Radius(6).Background(ColorBgSecondary)
					ui.Box(c).Width(120).Height(10).Radius(5).Background(ColorBgSecondary)
				})
			})
		}
	})
}

func mineFolderDisplayNames(folders []service.AccountFolder) []string {
	names := make([]string, len(folders))
	counts := make(map[string]int, len(folders))
	for i, f := range folders {
		name := f.Title
		if f.Count > 0 || f.ID != service.AccountFolderAll {
			name = fmt.Sprintf("%s · %d", f.Title, f.Count)
		}
		if f.Private {
			name += " · 私密"
		}
		names[i] = name
		counts[name]++
	}
	// Disambiguate colliding display names so ui.Select can address every folder distinctly.
	seen := make(map[string]int, len(folders))
	for i, name := range names {
		if counts[name] > 1 {
			seen[name]++
			names[i] = fmt.Sprintf("%s (%d)", name, seen[name])
		}
	}
	return names
}

// renderMineToolbar draws the folder dropdown, the 全部/未看完 switch or the count.
func (a *App) renderMineToolbar(c *ui.Context, siteID, kind string, l *mineList) {
	fk := mineFnKey(siteID, kind)
	folders := a.Mine.Folders[fk]
	if !mineHasFolders(siteID, kind) || len(folders) == 0 {
		return
	}
	current := a.mineFolderID(siteID, kind)
	ui.Row(c).Label("我的列表工具栏").FillWidth().Padding(8, 16).Gap(10).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(ColorBorder).Shrink(0).ClipX().Children(func() {
		if kind == service.AccountToView {
			ui.Row(c).Width(180).Padding(2).Radius(8).Background(ColorBgMain).Border(1, ColorBorder).Gap(2).Children(func() {
				for _, f := range folders {
					opt := f
					active := opt.ID == current
					btn := ui.ButtonBase(c).Label("稍后再看筛选："+opt.Title).Grow(1).Basis(0).Padding(5, 8).Radius(6).Justify(ui.Center).AlignItems(ui.Center)
					if active {
						btn.Background(ColorBgSecondary)
					} else if btn.Hovered() {
						btn.Background(ColorHover)
					}
					btn.Children(func() {
						col := ColorTextMuted
						if active {
							col = ColorTextMain
						}
						ui.Text(c, opt.Title).FontSize(13).FontWeight(500).TextColor(col).SingleLine()
					})
					if btn.Clicked() && !active {
						a.mineChooseFolder(opt.ID)
					}
				}
			})
			ui.Box(c).Grow(1)
			if l != nil && l.Loaded {
				ui.Text(c, fmt.Sprintf("共 %d 个", l.Total)).Label("稍后再看总数").FontSize(12).TextColor(ColorTextMuted).SingleLine()
			}
			return
		}
		names := mineFolderDisplayNames(folders)
		shown := ""
		for i, f := range folders {
			if f.ID == current {
				shown = names[i]
				break
			}
		}
		label := "收藏夹选择"
		if kind == service.AccountFollowing {
			label = "关注分组选择"
		}
		ui.Box(c).Width(260).Shrink(1).Children(func() {
			if ui.Select(c, &shown, names).Label(label).Changed() {
				for i, n := range names {
					if n == shown && folders[i].ID != current {
						a.mineChooseFolder(folders[i].ID)
						break
					}
				}
			}
		})
		ui.Box(c).Grow(1)
	})
}

// mineRow is a row of a list: an item or a date group heading.
type mineRow struct {
	heading string
	item    int
}

func mineHistoryRows(items []service.AccountItem, now time.Time) []mineRow {
	rows := make([]mineRow, 0, len(items)+4)
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	yesterday := today.AddDate(0, 0, -1)
	lastGroup, olderShown := "", false
	for i, it := range items {
		t := time.Unix(it.ViewedAt, 0).In(now.Location())
		group := ""
		switch {
		case it.ViewedAt <= 0:
			group = "更早"
		case !t.Before(today):
			group = "今天"
		case !t.Before(yesterday):
			group = "昨天"
		default:
			group = fmt.Sprintf("%d月%d日", int(t.Month()), t.Day())
			if t.Year() != now.Year() {
				group = fmt.Sprintf("%d年%d月%d日", t.Year(), int(t.Month()), t.Day())
			}
		}
		if group != lastGroup {
			if group != "今天" && group != "昨天" && !olderShown {
				rows = append(rows, mineRow{heading: "更早", item: -1})
				olderShown = true
			}
			if group != "更早" {
				rows = append(rows, mineRow{heading: group, item: -1})
			}
			lastGroup = group
		}
		rows = append(rows, mineRow{item: i})
	}
	return rows
}

func (l *mineList) cachedHistoryRows(now time.Time) []mineRow {
	dayKey := now.Local().Format("2006-01-02")
	if l.histRows != nil && l.histItemN == len(l.Items) && l.histDayKey == dayKey {
		return l.histRows
	}
	l.histRows = mineHistoryRows(l.Items, now)
	l.histItemN = len(l.Items)
	l.histDayKey = dayKey
	return l.histRows
}

func (a *App) renderMineRows(c *ui.Context, siteID, kind string, l *mineList) {
	var rows []mineRow
	isHistory := kind == service.AccountHistory
	if isHistory {
		rows = l.cachedHistoryRows(mineNow())
	}
	rowCount := len(l.Items)
	if isHistory {
		rowCount = len(rows)
	}
	total := rowCount + 1 // + footer
	_, last := l.State.Visible()
	if last >= 0 && last >= total-2 && l.HasMore && l.Err == "" && !a.isPending(a.minePendingKey(a.mineListKey(), l)) {
		a.mineLoadMore()
	}
	l.State.Key = func(i int) any {
		if i < 0 || i >= rowCount {
			return "mine-footer"
		}
		if isHistory {
			if rows[i].item < 0 {
				return fmt.Sprintf("h:%d:%s", i, rows[i].heading)
			}
			idx := rows[i].item
			if idx >= len(l.Items) {
				return i
			}
			it := l.Items[idx]
			return it.Kind + ":" + it.ID + ":" + strconv.Itoa(idx)
		}
		it := l.Items[i]
		return it.Kind + ":" + it.ID + ":" + strconv.Itoa(i)
	}
	ui.List(c, &l.State, total, func(i int) {
		if i < 0 || i >= rowCount {
			a.renderMineFooter(c, l)
			return
		}
		itemIdx := i
		if isHistory {
			r := rows[i]
			if r.item < 0 {
				ui.Row(c).FillWidth().Padding(12, 16, 4, 16).Children(func() {
					ui.Text(c, r.heading).Label("历史分组：" + r.heading).FontSize(12).FontWeight(500).TextColor(ColorTextMuted).SingleLine()
				})
				return
			}
			itemIdx = r.item
		}
		if itemIdx < 0 || itemIdx >= len(l.Items) {
			return
		}
		it := l.Items[itemIdx]
		if it.Kind == service.AccountItemUser {
			a.renderMineUserRow(c, it)
			return
		}
		a.renderMineVideoRow(c, kind, it)
	}).Label("我的虚拟列表").Grow(1).FillWidth()
}

func (a *App) renderMineFooter(c *ui.Context, l *mineList) {
	ui.Column(c).FillWidth().Padding(14, 16, 20, 16).AlignItems(ui.Center).Gap(8).ClipX().Children(func() {
		switch {
		case l.Err != "":
			a.renderUserListError(c, l.Err, "我的列表重试", a.mineRetry)
		case l.HasMore:
			ui.Text(c, "正在加载…").Label("我的正在加载").FontSize(13).TextColor(ColorTextMuted).SingleLine()
		default:
			ui.Text(c, "没有更多了").Label("我的没有更多").FontSize(13).TextColor(ColorTextMuted).SingleLine()
		}
	})
}

func formatClock(sec int) string {
	if sec <= 0 {
		return "0:00"
	}
	return FormatDuration(sec)
}

func (a *App) renderMineVideoRow(c *ui.Context, kind string, it service.AccountItem) {
	parts := []string{}
	if it.Author != "" {
		parts = append(parts, it.Author)
	}
	switch kind {
	case service.AccountFavorites:
		if it.FavAt > 0 {
			parts = append(parts, "收藏于 "+time.Unix(it.FavAt, 0).Format("2006-01-02"))
		}
	case service.AccountToView, service.AccountHistory:
		if it.Kind == service.AccountItemVideo {
			if it.Finished {
				parts = append(parts, "已看完")
			} else if it.Progress > 0 {
				parts = append(parts, "看到 "+formatClock(it.Progress))
			}
		}
		if kind == service.AccountHistory && it.ViewedAt > 0 {
			parts = append(parts, time.Unix(it.ViewedAt, 0).Format("15:04"))
		}
	}
	row := compactVideoRow{
		Label:      "我的条目：" + firstNonEmptyStrUI(it.Title, it.ID),
		MenuKey:    "mine:" + it.Kind + ":" + it.ID,
		MenuName:   "我的条目更多：" + it.ID,
		Title:      it.Title,
		Cover:      it.Cover,
		Meta:       strings.Join(parts, " · "),
		TypeLabel:  it.TypeLabel,
		Invalid:    it.Invalid,
		BrowserURL: it.BrowserURL,
	}
	if it.Duration > 0 {
		row.Duration = FormatDuration(it.Duration)
	}
	if it.TypeLabel == "直播" {
		row.Badge, row.BadgeLive, row.TypeLabel = "直播", it.Live, ""
	}
	if it.Kind == service.AccountItemVideo && !it.Finished && it.Progress > 0 && it.Duration > 0 {
		row.Progress = float32(it.Progress) / float32(it.Duration)
	}
	switch it.Kind {
	case service.AccountItemVideo:
		row.BVID, row.CopyURL = it.ID, it.OpenURL
		mid, _ := strconv.ParseInt(it.AuthorID, 10, 64)
		dur := row.Duration
		row.Open = func() { a.OpenVideo(it.ID, it.Title, it.Author, mid, it.Cover, dur) }
	case service.AccountItemUser:
		mid, _ := strconv.ParseInt(firstNonEmptyStrUI(it.AuthorID, it.ID), 10, 64)
		row.Open = func() { a.OpenUser(mid, it.Author) }
	}
	a.renderCompactVideoRow(c, row)
}

func (a *App) renderMineUserRow(c *ui.Context, it service.AccountItem) {
	if it.TypeLabel == "直播" {
		a.renderMineVideoRow(c, service.AccountHistory, it)
		return
	}
	mid, _ := strconv.ParseInt(it.ID, 10, 64)
	owner, owned := a.ownerOfAccountInMemory(it.Site, it.ID, mid)
	ui.Row(c).FillWidth().Padding(10, 16).Gap(8).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(ColorBorder).ClipX().Children(func() {
		body := ui.ButtonBase(c).Label("我的用户：" + firstNonEmptyStrUI(it.Title, it.ID)).Grow(1).Padding(0).Gap(12).AlignItems(ui.Center).Justify(ui.Start).ClipX()
		body.Children(func() {
			a.renderAccountAvatar(c, it.Site, it.Title, it.ID, mid, 40, false, "", nil, it.Cover)
			ui.Column(c).Grow(1).Gap(3).ClipX().Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).ClipX().Children(func() {
					ui.Text(c, firstNonEmptyStrUI(it.Title, it.ID)).FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine().Shrink(1)
					if owned {
						ui.Text(c, "订阅："+owner.Name).Label("所属订阅：" + owner.Name).FontSize(12).TextColor(ColorAccent).SingleLine().Shrink(1)
					}
				})
				if it.Site == string(site.Douyin) && it.UniqueID != "" {
					ui.Text(c, "抖音号 "+it.UniqueID).FontSize(12).TextColor(ColorTextMuted).SingleLine()
				}
				if it.Signature != "" {
					ui.Text(c, it.Signature).FontSize(12).TextColor(ColorTextMuted).SingleLine()
				}
			})
		})
		if body.Clicked() {
			if it.Site == string(site.Douyin) {
				a.OpenDouyinUser(it.ID, it.Title)
			} else {
				a.OpenUser(mid, it.Title)
			}
		}
		key := "mine-user:" + it.Site + ":" + it.ID
		open := a.PostMenuKey == key
		MoreMenuButton(c, "我的用户更多："+it.ID, 28, &open, func(m *ui.Menu) {
			if owned {
				if m.Item("打开订阅“" + owner.Name + "”").Chosen() {
					a.OpenSubscriptionDetail(owner)
				}
			} else if m.Item("加入订阅").Chosen() {
				a.mineSubscribe(it)
			}
			if it.BrowserURL != "" && m.Item("在浏览器中打开").Chosen() {
				a.showError(a.backend.Desktop().OpenExternalURL(it.BrowserURL))
			}
		})
		keyedMenuOpen(&a.PostMenuKey, key, open)
	})
}

// mineSubscribe subscribes to a followed account the same way the user
// pages' 订阅 button does: into an existing subscription or a new one.
func (a *App) mineSubscribe(it service.AccountItem) {
	acct := subscription.AccountRef{Site: it.Site, UID: it.ID, Name: firstNonEmptyStrUI(it.Title, it.ID), Face: it.Cover}
	if it.Site == string(site.Douyin) {
		acct.UniqueID = it.UniqueID
	}
	a.OpenSubscribePicker(acct)
}

func (a *App) renderMineWorkGrid(c *ui.Context, width float32, l *mineList) {
	cols := douyinUserGridColumns(width)
	gridRows := (len(l.Items) + cols - 1) / cols
	total := gridRows + 1
	_, last := l.State.Visible()
	if last >= 0 && last >= gridRows-1 && l.HasMore && l.Err == "" && !a.isPending(a.minePendingKey(a.mineListKey(), l)) {
		a.mineLoadMore()
	}
	l.State.Key = func(i int) any {
		if i >= 0 && i < gridRows {
			idx := i * cols
			if idx < len(l.Items) {
				return "mine-row-" + l.Items[idx].ID
			}
		}
		return "mine-footer"
	}
	ui.List(c, &l.State, total, func(i int) {
		if i < 0 || i >= gridRows {
			a.renderMineFooter(c, l)
			return
		}
		start := i * cols
		end := min(len(l.Items), start+cols)
		rowKeys := make([]string, 0, end-start)
		for _, it := range l.Items[start:end] {
			rowKeys = append(rowKeys, it.CacheKey)
		}
		locals := a.workCardLocals(rowKeys)
		ui.Row(c).FillWidth().Padding(6, 16).Gap(10).AlignItems(ui.Start).ClipX().Children(func() {
			for slot := 0; slot < cols; slot++ {
				if start+slot >= end {
					ui.Box(c).Grow(1).Basis(0)
					continue
				}
				it := l.Items[start+slot]
				if it.Kind == service.AccountItemUnsupported {
					a.renderMineUnsupportedWorkCard(c, it)
					continue
				}
				if it.Work == nil {
					ui.Box(c).Grow(1).Basis(0)
					continue
				}
				a.renderDouyinWorkGridCard(c, it.Author, it.AuthorID, *it.Work, locals[it.CacheKey])
			}
		})
	}).Label("我的作品虚拟列表").Grow(1).FillWidth()
}

// renderMineUnsupportedWorkCard is a work TRACE cannot open (a live-room card
// or another type): a grey 3:4 block with its type, not clickable, and only
// "在浏览器中打开" in its "⋯" (FR-ACCT-003).
func (a *App) renderMineUnsupportedWorkCard(c *ui.Context, it service.AccountItem) {
	ui.Column(c).Grow(1).Basis(0).Gap(5).ClipX().Label("我的不支持作品：" + it.ID).Children(func() {
		ui.Box(c).FillWidth().AspectRatio(0.75).Radius(10).Border(1, ColorBorder).Background(ColorBgSecondary).Clip().Children(func() {
			ui.Column(c).Absolute().Left(0).Top(0).Right(0).Bottom(0).AlignItems(ui.Center).Justify(ui.Center).Padding(8).Children(func() {
				ui.Text(c, firstNonEmptyStrUI(it.TypeLabel, "暂不支持的作品")).FontSize(12).TextColor(ColorTextMuted).SingleLine()
			})
			if it.BrowserURL == "" {
				return
			}
			key := "mine-unsupported:" + it.ID
			open := a.DouyinCardMenuID == key
			ui.Box(c).Absolute().Bottom(6).Right(6).Radius(14).Background(ui.RGBA(255, 255, 255, 0.88)).Children(func() {
				MoreMenuButton(c, "作品更多："+it.ID, 26, &open, func(m *ui.Menu) {
					if m.Item("在浏览器中打开").Chosen() {
						a.showError(a.backend.Desktop().OpenExternalURL(it.BrowserURL))
					}
				})
			})
			keyedMenuOpen(&a.DouyinCardMenuID, key, open)
		})
		ui.Text(c, firstNonEmptyStrUI(it.Title, it.ID)).FontSize(13).FontWeight(500).TextColor(ColorTextMuted).MaxLines(2)
	})
}
