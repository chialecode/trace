package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

// mineStub answers 我的 from memory and counts every request.
type mineStub struct {
	service.BackendAPI
	mu       sync.Mutex
	logged   map[string]bool
	mid      int64
	pages    map[string]service.AccountPage // site/kind/folder/cursor
	errs     map[string]error
	folders  map[string][]service.AccountFolder
	calls    map[string]int
	folderN  map[string]int
	hold     chan struct{} // when set, AccountPage waits for a token
	opened   []string
	owners   map[string]subscription.Subscription
	browsers []string
}

func newMineStub(base service.BackendAPI) *mineStub {
	return &mineStub{
		BackendAPI: base,
		logged:     map[string]bool{"bilibili": true, "douyin": false},
		mid:        12345,
		pages:      map[string]service.AccountPage{},
		errs:       map[string]error{},
		folders:    map[string][]service.AccountFolder{},
		calls:      map[string]int{},
		folderN:    map[string]int{},
		owners:     map[string]subscription.Subscription{},
	}
}

func (s *mineStub) StatusOf(siteID string) credential.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.logged[siteID] {
		return credential.Status{Site: siteID, State: "logged_out"}
	}
	st := credential.Status{Site: siteID, LoggedIn: true, State: "logged_in", Uname: "我"}
	if siteID == "bilibili" {
		st.Mid = s.mid
	} else {
		st.SecUID, st.UID = "MS4wLjABAAAA_self_account_00000001", "1"
	}
	return st
}

func (s *mineStub) HasAnyLogin() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.logged["bilibili"] || s.logged["douyin"] || s.logged["weibo"] || s.logged["x"]
}

func (s *mineStub) AccountSections(siteID string) []service.AccountSection {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.logged[siteID] {
		return []service.AccountSection{{Site: siteID, Kind: service.AccountLogin, Label: "未登录", Reason: "未登录"}}
	}
	switch siteID {
	case "bilibili":
		return []service.AccountSection{
			{Site: siteID, Kind: service.AccountFollowing, Label: "关注", Available: true},
			{Site: siteID, Kind: service.AccountFavorites, Label: "收藏夹", Available: true},
			{Site: siteID, Kind: service.AccountToView, Label: "稍后再看", Available: true},
			{Site: siteID, Kind: service.AccountHistory, Label: "历史记录", Available: true},
		}
	case "weibo":
		return []service.AccountSection{
			{Site: siteID, Kind: service.AccountFollowing, Label: "关注", Available: true},
			{Site: siteID, Kind: service.AccountFavorites, Label: "收藏", Available: true},
		}
	case "x":
		return []service.AccountSection{
			{Site: siteID, Kind: service.AccountFollowing, Label: "关注", Available: true},
			{Site: siteID, Kind: service.AccountBookmarks, Label: "书签", Available: true},
			{Site: siteID, Kind: service.AccountLikes, Label: "喜欢", Available: true},
		}
	default:
		return []service.AccountSection{
			{Site: siteID, Kind: service.AccountFollowing, Label: "关注", Available: true},
			{Site: siteID, Kind: service.AccountLikes, Label: "喜欢", Available: true},
			{Site: siteID, Kind: service.AccountCollection, Label: "收藏", Available: true},
			{Site: siteID, Kind: service.AccountWatchHistory, Label: "观看历史", Available: true},
		}
	}
}

func (s *mineStub) AccountFolders(ctx context.Context, siteID, kind string) ([]service.AccountFolder, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.folderN[siteID+"/"+kind]++
	return s.folders[siteID+"/"+kind], nil
}

func (s *mineStub) AccountPage(ctx context.Context, siteID, kind, folder, cursor string) (service.AccountPage, error) {
	s.mu.Lock()
	hold := s.hold
	s.mu.Unlock()
	if hold != nil {
		<-hold
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.logged[siteID] {
		return service.AccountPage{}, &site.Error{Kind: site.ErrKindAuthRequired, Message: "请先登录"}
	}
	key := strings.Join([]string{siteID, kind, folder, cursor}, "/")
	s.calls[key]++
	if err := s.errs[key]; err != nil {
		return service.AccountPage{}, err
	}
	return s.pages[key], nil
}

func (s *mineStub) OwnerOfAccount(siteID, uid string) (subscription.Subscription, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.owners[siteID+":"+uid]
	return o, ok
}

func (s *mineStub) ListSubscriptions() ([]subscription.Subscription, error) {
	base, err := s.BackendAPI.ListSubscriptions()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]subscription.Subscription(nil), base...)
	seen := make(map[string]bool, len(out))
	for _, sub := range out {
		seen[sub.ID] = true
	}
	for _, sub := range s.owners {
		if !seen[sub.ID] {
			seen[sub.ID] = true
			out = append(out, sub)
		}
	}
	return out, nil
}

func (s *mineStub) CreateSubscription(name, note string, accounts []subscription.AccountRef, rules subscription.SubRules) (subscription.Subscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub := subscription.Subscription{
		ID:       "sub-" + accounts[0].UID,
		Name:     name,
		Accounts: append([]subscription.AccountRef(nil), accounts...),
	}
	s.owners[accounts[0].Site+":"+accounts[0].UID] = sub
	return sub, nil
}

func (s *mineStub) OpenLink(ctx context.Context, raw string) (*service.OpenLinkResult, error) {
	s.mu.Lock()
	s.opened = append(s.opened, raw)
	s.mu.Unlock()
	if strings.Contains(raw, "douyin.com") {
		return nil, errors.New("stub: douyin not opened in this test")
	}
	if strings.Contains(raw, "weibo.com/") {
		parts := strings.Split(strings.TrimRight(raw, "/"), "/")
		mid := parts[len(parts)-1]
		return &service.OpenLinkResult{
			Site:       string(site.Weibo),
			SiteTarget: &site.ResolveTarget{Site: site.Weibo, Kind: "dynamic", ID: mid, CanonicalURL: raw},
			WeiboPost:  &service.WeiboPostView{Mid: mid, Text: "打开的收藏微博详情", Author: service.WeiboAuthorDTO{UID: "500001", ScreenName: "收藏详情博主"}},
		}, nil
	}
	return s.BackendAPI.OpenLink(ctx, raw)
}

func (s *mineStub) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, v := range s.calls {
		n += v
	}
	return n
}

func mineVideos(prefix string, n int, start int64) []service.AccountItem {
	out := make([]service.AccountItem, n)
	for i := range out {
		id := fmt.Sprintf("%s%02d", prefix, i)
		out[i] = service.AccountItem{Kind: service.AccountItemVideo, Site: "bilibili", ID: id, Title: "视频 " + id, Author: "UP", AuthorID: "8888",
			Duration: 120, OpenURL: "https://www.bilibili.com/video/" + id, BrowserURL: "https://www.bilibili.com/video/" + id, FavAt: start - int64(i)*3600}
	}
	return out
}

func newMineApp(t *testing.T) (*App, *mineStub, func()) {
	t.Helper()
	app, _, cleanup := newTestUIApp(t)
	if _, err := app.backend.LoginWithCookie(context.Background(), "bilibili", "SESSDATA=mine_sess; DedeUserID=12345; bili_jct=mine_jct", ""); err != nil {
		t.Fatalf("login: %v", err)
	}
	stub := newMineStub(app.backend)
	app.backend = stub
	stub.folders["bilibili/favorites"] = []service.AccountFolder{{ID: "11", Title: "默认收藏夹", Count: 40, IsDefault: true}, {ID: "12", Title: "私密夹", Count: 1, Private: true}}
	stub.folders["bilibili/following"] = []service.AccountFolder{{ID: "all", Title: "全部关注", IsDefault: true}, {ID: "-10", Title: "特别关注", Count: 1}}
	stub.folders["bilibili/toview"] = []service.AccountFolder{{ID: "all", Title: "全部", IsDefault: true}, {ID: "unfinished", Title: "未看完"}}
	page1 := mineVideos("BV1fav", 20, 1763000000)
	page1[0].ID, page1[0].OpenURL = "BV1xx411c7mD", "https://www.bilibili.com/video/BV1xx411c7mD"
	page1[1] = service.AccountItem{Kind: service.AccountItemUnsupported, Site: "bilibili", ID: "gone", Title: "已失效", Invalid: true}
	stub.pages["bilibili/favorites/11/"] = service.AccountPage{Items: page1, HasMore: true, Cursor: "2|", Total: 40}
	page2 := mineVideos("BV1fbv", 20, 1762000000)
	page2[0] = page1[2] // a repeat across pages is dropped
	stub.pages["bilibili/favorites/11/2|"] = service.AccountPage{Items: page2, HasMore: false, Total: 40}
	stub.pages["bilibili/favorites/12/"] = service.AccountPage{Items: []service.AccountItem{}}
	stub.pages["bilibili/following/all/"] = service.AccountPage{Items: []service.AccountItem{
		{Kind: service.AccountItemUser, Site: "bilibili", ID: "2001", Title: "关注甲", Signature: "签名", OpenURL: "https://space.bilibili.com/2001", BrowserURL: "https://space.bilibili.com/2001"},
	}}
	stub.pages["bilibili/toview/all/"] = service.AccountPage{Items: mineVideos("BV1tov", 3, 0), Total: 3}
	return app, stub, cleanup
}

func TestMineFoldedPanelAndFirstChoiceRequests(t *testing.T) {
	app, stub, cleanup := newMineApp(t)
	defer cleanup()
	app.Navigate(RouteMine)
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	if _, ok := tt.Find("我的选择提示"); !ok {
		t.Fatal("entering 我的 should ask to choose a function")
	}
	for _, l := range []string{"我的功能：哔哩哔哩 收藏夹", "我的功能：抖音 喜欢"} {
		if _, ok := tt.Find(l); ok {
			t.Fatalf("%s visible while folded", l)
		}
	}
	if stub.callCount() != 0 {
		t.Fatal("entering 我的 sent a request")
	}
	if err := tt.Click("我的站点：哔哩哔哩"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for _, l := range []string{"关注", "收藏夹", "稍后再看", "历史记录"} {
		if _, ok := tt.Find("我的功能：哔哩哔哩 " + l); !ok {
			t.Fatalf("expanded 哔哩哔哩 lacks %s", l)
		}
	}
	// Accordion: opening 抖音 folds 哔哩哔哩; 抖音 is signed out and only offers login.
	_ = tt.Click("我的站点：抖音")
	tt.Frame()
	if _, ok := tt.Find("我的功能：哔哩哔哩 收藏夹"); ok {
		t.Fatal("哔哩哔哩 stayed open beside 抖音")
	}
	if _, ok := tt.Find("我的登录：抖音"); !ok {
		t.Fatal("signed-out 抖音 should offer only login")
	}
	if _, ok := tt.Find("我的功能：抖音 喜欢"); ok {
		t.Fatal("signed-out 抖音 lists functions")
	}
	_ = tt.Click("我的登录：抖音")
	tt.Frame()
	if app.CurrentRoute != RouteLogin || app.LoginSite != "douyin" || app.SavedRoute != RouteMine {
		t.Fatalf("login link: route=%s site=%s saved=%s", app.CurrentRoute, app.LoginSite, app.SavedRoute)
	}
	app.CurrentRoute = RouteMine
	_ = tt.Click("我的站点：抖音") // fold
	tt.Frame()
	_ = tt.Click("我的站点：哔哩哔哩")
	tt.Frame()
	_ = tt.Click("我的功能：哔哩哔哩 收藏夹")
	tt.Frame()
	tt.Frame()
	if stub.calls["bilibili/favorites/11/"] != 1 || stub.folderN["bilibili/favorites"] != 1 {
		t.Fatalf("first choice: calls=%v folders=%v", stub.calls, stub.folderN)
	}
	if got := app.routeTitle(); got != "收藏夹 · B 站" {
		t.Fatalf("title = %q", got)
	}
	if _, ok := tt.Find("收藏夹选择"); !ok {
		t.Fatal("folder dropdown missing")
	}
	// Switching away and back does not request again.
	_ = tt.Click("我的功能：哔哩哔哩 稍后再看")
	tt.Frame()
	_ = tt.Click("我的功能：哔哩哔哩 收藏夹")
	tt.Frame()
	if stub.calls["bilibili/favorites/11/"] != 1 || stub.folderN["bilibili/favorites"] != 1 {
		t.Fatalf("switching back re-requested: %v %v", stub.calls, stub.folderN)
	}
	if _, ok := tt.Find("稍后再看总数"); ok {
		t.Fatal("toview count leaked into favorites")
	}
}

func TestMineScrollLoadsNextPageOnceAndKeepsPosition(t *testing.T) {
	app, stub, cleanup := newMineApp(t)
	defer cleanup()
	app.Navigate(RouteMine)
	app.mineSelect("bilibili", service.AccountFavorites)
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	l := app.mineCurrentList()
	if l == nil || len(l.Items) != 20 || !l.HasMore {
		t.Fatalf("first page = %+v", l)
	}
	// Hold the next page so a second scroll cannot start another request.
	stub.mu.Lock()
	stub.hold = make(chan struct{}, 4)
	stub.mu.Unlock()
	posted := make(chan func(), 16)
	app.dispatch = func(fn func()) { posted <- fn }
	list, _ := tt.Find("我的虚拟列表")
	for i := 0; i < 8; i++ {
		tt.Scroll(list.X+list.W/2, list.Y+list.H/2, 0, 2000)
		tt.Frame()
	}
	if !app.pending[app.minePendingKey(app.mineListKey(), l)] {
		t.Fatal("scrolling to the end did not request the next page")
	}
	stub.hold <- struct{}{}
	select {
	case fn := <-posted:
		fn()
	case <-time.After(3 * time.Second):
		t.Fatal("next page never arrived")
	}
	stub.mu.Lock()
	stub.hold = nil
	stub.mu.Unlock()
	tt.Frame()
	if stub.calls["bilibili/favorites/11/2|"] != 1 {
		t.Fatalf("second page requested %d times", stub.calls["bilibili/favorites/11/2|"])
	}
	if len(l.Items) != 39 || l.HasMore {
		t.Fatalf("after page 2: %d items (repeat must be dropped), more=%v", len(l.Items), l.HasMore)
	}
	if _, ok := tt.Find("我的没有更多"); !ok {
		for i := 0; i < 4; i++ {
			tt.Scroll(list.X+list.W/2, list.Y+list.H/2, 0, 4000)
			tt.Frame()
		}
		if _, ok := tt.Find("我的没有更多"); !ok {
			t.Fatal("the end of the list is not said")
		}
	}
	app.dispatch = nil
	first, _ := l.State.Visible()
	if first == 0 {
		t.Fatal("list did not scroll")
	}
	// Open a video and come back: same list, same place, no new request.
	before := stub.callCount()
	app.OpenVideo("BV1xx411c7mD", "视频", "UP", 8888, "", "")
	if app.CurrentRoute != RouteVideo || app.PreviousRoute != RouteMine {
		t.Fatalf("open: route=%s prev=%s", app.CurrentRoute, app.PreviousRoute)
	}
	tt.Frame()
	app.NavigateBack()
	tt.Frame()
	if app.CurrentRoute != RouteMine || app.mineCurrentList() != l || stub.callCount() != before {
		t.Fatalf("back: route=%s same=%v calls %d->%d", app.CurrentRoute, app.mineCurrentList() == l, before, stub.callCount())
	}
	if again, _ := l.State.Visible(); again != first {
		t.Fatalf("scroll position lost: %d -> %d", first, again)
	}
	// 刷新 starts from the first page.
	chooseMenuItem(t, tt, "我的更多菜单", "刷新")
	tt.Frame()
	if stub.calls["bilibili/favorites/11/"] != 2 || len(app.mineCurrentList().Items) != 20 {
		t.Fatalf("refresh: calls=%v", stub.calls)
	}
}

func TestMineItemsOpenThroughOpenLink(t *testing.T) {
	app, stub, cleanup := newMineApp(t)
	defer cleanup()
	app.Navigate(RouteMine)
	app.mineSelect("bilibili", service.AccountFavorites)
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	before := len(app.backend.ListHistory())
	// The invalid row is not clickable and has no "⋯".
	if err := tt.Click("我的条目：已失效"); err == nil {
		tt.Frame()
		if app.CurrentRoute != RouteMine {
			t.Fatal("clicking an invalid item opened something")
		}
	}
	if _, ok := tt.Find("我的条目更多：gone"); ok {
		t.Fatal("an invalid item has a menu")
	}
	if got := menuItems(t, tt, "我的条目更多：BV1fav02"); strings.Join(got, ",") != "缓存,复制链接,在浏览器中打开" {
		t.Fatalf("video menu = %v", got)
	}
	if err := tt.Click("我的条目：视频 BV1fav00"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if app.CurrentRoute != RouteVideo || len(stub.opened) != 1 || stub.opened[0] != "BV1xx411c7mD" {
		t.Fatalf("video open: route=%s opened=%v", app.CurrentRoute, stub.opened)
	}
	if n := len(app.backend.ListHistory()); n != before+1 {
		t.Fatalf("opening recorded %d recent parses (err=%q opening=%+v)", n-before, app.LastParseError, app.Opening)
	}
	app.NavigateBack()
	tt.Frame()

	// A followed user opens the user page; "加入订阅" then shows the owner.
	app.mineSelect("bilibili", service.AccountFollowing)
	tt.Frame()
	if got := menuItems(t, tt, "我的用户更多：2001"); strings.Join(got, ",") != "加入订阅,在浏览器中打开" {
		t.Fatalf("user menu = %v", got)
	}
	chooseMenuItem(t, tt, "我的用户更多：2001", "加入订阅")
	tt.Frame()
	// It asks whether to join an existing subscription or start one (DEC-058).
	if _, ok := tt.Find("加入订阅对话框"); !ok {
		t.Fatal("加入订阅 did not ask which subscription to join")
	}
	if err := tt.Click("新建订阅"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, ok := tt.Find("所属订阅：关注甲"); !ok {
		t.Fatal("owner not shown after 加入订阅")
	}
	if got := menuItems(t, tt, "我的用户更多：2001"); got[0] != "打开订阅“关注甲”" {
		t.Fatalf("owned user menu = %v", got)
	}
	_ = tt.Click("我的用户：关注甲")
	tt.Frame()
	if app.CurrentRoute != RouteUser || stub.opened[len(stub.opened)-1] != "UID:2001" {
		t.Fatalf("user open: route=%s opened=%v", app.CurrentRoute, stub.opened)
	}
}

func TestMineErrorsEmptyAndSignOut(t *testing.T) {
	app, stub, cleanup := newMineApp(t)
	defer cleanup()
	stub.errs["bilibili/toview/all/"] = &site.Error{Site: site.Bilibili, Kind: site.ErrKindRiskControl, Message: "请求触发风控拦截，已停止自动请求"}
	app.Navigate(RouteMine)
	app.mineSelect("bilibili", service.AccountToView)
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	if _, ok := tt.Find("我的列表为空"); ok {
		t.Fatal("a risk-control error shown as empty")
	}
	if _, ok := tt.Find("我的重试"); !ok {
		t.Fatal("first-page error lacks 重试")
	}
	if stub.calls["bilibili/toview/all/"] != 1 {
		t.Fatal("the error was retried automatically")
	}
	delete(stub.errs, "bilibili/toview/all/")
	_ = tt.Click("我的重试")
	tt.Frame()
	if _, ok := tt.Find("稍后再看总数"); !ok || stub.calls["bilibili/toview/all/"] != 2 {
		t.Fatal("retry did not load the list")
	}
	// 未看完 is empty: a real empty list says so.
	stub.pages["bilibili/toview/unfinished/"] = service.AccountPage{Items: []service.AccountItem{}}
	_ = tt.Click("稍后再看筛选：未看完")
	tt.Frame()
	if r, ok := tt.Find("我的列表为空"); !ok || r.W <= 0 {
		t.Fatal("empty 未看完 not said")
	}
	// Sign out of 哔哩哔哩: its lists go away at once.
	stub.mu.Lock()
	stub.logged["bilibili"], stub.logged["douyin"] = false, true
	stub.mu.Unlock()
	app.handleBackendEvent(service.Event{Type: service.EventAccountChanged, Site: "bilibili", Message: "logged_out"})
	for k := range app.Mine.Lists {
		if strings.HasPrefix(k, "bilibili/") {
			t.Fatalf("list %s kept after sign-out", k)
		}
	}
	if app.Mine.Kind != "" {
		t.Fatal("the chosen 哔哩哔哩 function stayed chosen")
	}
	// A renewal of the same account keeps the lists.
	app.mineSelect("douyin", service.AccountLikes)
	n := len(app.Mine.Lists)
	app.handleBackendEvent(service.Event{Type: service.EventAccountChanged, Site: "douyin", Message: "logged_in"})
	if len(app.Mine.Lists) != n {
		t.Fatal("a renewal cleared the lists")
	}
}

func TestMineNarrowWindowChooserAndBack(t *testing.T) {
	app, _, cleanup := newMineApp(t)
	defer cleanup()
	app.Navigate(RouteMine)
	tt := ui.NewTester(app.View, 720, 760)
	tt.Frame()
	if _, ok := tt.Find("上下文侧栏"); ok {
		t.Fatal("720 wide has no panel")
	}
	if _, ok := tt.Find("我的选择列表"); !ok {
		t.Fatal("narrow 我的 should list the sites in the page")
	}
	_ = tt.Click("我的站点：哔哩哔哩")
	tt.Frame()
	_ = tt.Click("我的功能：哔哩哔哩 收藏夹")
	tt.Frame()
	if _, ok := tt.Find("我的虚拟列表"); !ok {
		t.Fatal("choosing a function did not show the list")
	}
	if err := tt.Click("返回我的功能列表"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, ok := tt.Find("我的选择列表"); !ok || app.CurrentRoute != RouteMine {
		t.Fatal("back did not return to the chooser")
	}
	for _, w := range []int{1280, 1000, 720} {
		app.Mine.Picker = false
		tt := ui.NewTester(app.View, w, 760)
		tt.Frame()
		page, _ := tt.Find("中栏内容")
		if page.X+page.W > float32(w)+0.5 {
			t.Fatalf("width %d: page overflows: %+v", w, page)
		}
	}
}

func TestMineHistoryDateGroups(t *testing.T) {
	now := time.Date(2026, 11, 14, 15, 0, 0, 0, time.Local)
	at := func(d time.Duration) int64 { return now.Add(-d).Unix() }
	items := []service.AccountItem{{ViewedAt: at(time.Hour)}, {ViewedAt: at(20 * time.Hour)}, {ViewedAt: at(4 * 24 * time.Hour)}, {ViewedAt: at(5 * 24 * time.Hour)}}
	var heads []string
	for _, r := range mineHistoryRows(items, now) {
		if r.item < 0 {
			heads = append(heads, r.heading)
		}
	}
	if strings.Join(heads, ",") != "今天,昨天,更早,11月10日,11月9日" {
		t.Fatalf("groups = %v", heads)
	}
}

// A function chosen while another function's folders are on the way must not
// drop those folders: coming back shows them without asking the site again.
func TestMineFoldersSurviveChoosingAnotherFunction(t *testing.T) {
	app, stub, cleanup := newMineApp(t)
	defer cleanup()
	app.Navigate(RouteMine)
	posted := make(chan func(), 16)
	app.dispatch = func(fn func()) { posted <- fn }
	app.mineSelect("bilibili", service.AccountFollowing) // folders requested
	app.mineSelect("bilibili", service.AccountHistory)   // a new list meanwhile
	for i := 0; i < 2; i++ {
		select {
		case fn := <-posted:
			fn()
		case <-time.After(3 * time.Second):
			t.Fatal("request never finished")
		}
	}
	app.dispatch = nil
	if _, ok := app.Mine.Folders["bilibili/following"]; !ok {
		t.Fatal("following groups were dropped when 历史记录 was chosen")
	}
	app.mineSelect("bilibili", service.AccountFollowing)
	if n := stub.folderN["bilibili/following"]; n != 1 {
		t.Fatalf("following groups requested %d times, want 1", n)
	}
	// A sign-out still discards folders that arrive afterwards.
	app.dispatch = func(fn func()) { posted <- fn }
	app.mineSelect("bilibili", service.AccountFavorites)
	app.mineClearSite("bilibili")
	select {
	case fn := <-posted:
		fn()
	case <-time.After(3 * time.Second):
		t.Fatal("folders never finished")
	}
	app.dispatch = nil
	if _, ok := app.Mine.Folders["bilibili/favorites"]; ok {
		t.Fatal("folders of a cleared site were applied")
	}
}

// A Douyin work TRACE cannot open is a grey card: no open button, no 缓存.
func TestMineUnsupportedDouyinWorkIsNotClickable(t *testing.T) {
	app, stub, cleanup := newMineApp(t)
	defer cleanup()
	stub.logged["douyin"] = true
	ok := service.DouyinWorkCardDTO{ID: "7300000000000000001", Kind: "video", Title: "能打开的作品", DurationSec: 15, CacheKey: "douyin:video:7300000000000000001"}
	bad := service.DouyinWorkCardDTO{ID: "7300000000000000002", Kind: "unsupported", Title: "直播卡片"}
	stub.pages["douyin/likes//"] = service.AccountPage{Items: []service.AccountItem{
		{Kind: service.AccountItemWork, Site: "douyin", ID: ok.ID, Title: ok.Title, Work: &ok, CacheKey: ok.CacheKey, OpenURL: "https://www.douyin.com/video/" + ok.ID},
		{Kind: service.AccountItemUnsupported, Site: "douyin", ID: bad.ID, Title: bad.Title, Work: &bad, TypeLabel: "暂不支持的作品", BrowserURL: "https://www.douyin.com/video/" + bad.ID},
	}}
	app.Navigate(RouteMine)
	app.mineSelect("douyin", service.AccountLikes)
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	tt.Frame()
	if _, found := tt.Find("打开作品：" + ok.ID); !found {
		t.Fatal("the supported work has no open button")
	}
	if _, found := tt.Find("打开作品：" + bad.ID); found {
		t.Fatal("the unsupported work can be opened")
	}
	if _, found := tt.Find("我的不支持作品：" + bad.ID); !found {
		t.Fatal("the unsupported work is not shown as such")
	}
	if _, found := tt.Find("作品更多：" + bad.ID); !found {
		t.Fatal("the unsupported work lacks 在浏览器中打开")
	}
}

func TestMineWeiboFavoritesPostAndRetweetMediaViewerAndNavigation(t *testing.T) {
	app, stub, cleanup := newMineApp(t)
	defer cleanup()
	stub.logged["weibo"] = true
	stub.logged["x"] = true

	const (
		imgMid = "5000000000009101"
		rtMid  = "5000000000009102"
		vidMid = "5000000000009103"
	)
	imgPost := service.WeiboPostView{
		Mid:          imgMid,
		Author:       service.WeiboAuthorDTO{UID: "500001", ScreenName: "收藏配图博主"},
		CreatedAt:    1731000000,
		Text:         "原帖带普通图片与实况照片",
		CanonicalURL: "https://weibo.com/500001/" + imgMid,
		CacheKey:     "weibo:dynamic:" + imgMid,
		Images: []service.WeiboMediaItemDTO{
			{Index: 1, Kind: "image", ImageURL: "https://wx1.sinaimg.cn/large/syn_m1.jpg", Width: 800, Height: 600},
			{Index: 2, Kind: "live_photo", ImageURL: "https://wx1.sinaimg.cn/large/syn_m2.jpg", MotionURL: "https://video.weibo.com/media/play?livephoto=syn2", Width: 600, Height: 800},
		},
	}
	rtPost := service.WeiboPostView{
		Mid:              rtMid,
		Author:           service.WeiboAuthorDTO{UID: "500002", ScreenName: "收藏转发博主"},
		CreatedAt:        1730990000,
		Text:             "转发配图与视频并茂的原微博",
		IsRetweet:        true,
		OriginIncomplete: true,
		CanonicalURL:     "https://weibo.com/500002/" + rtMid,
		CacheKey:         "weibo:dynamic:" + rtMid,
		Retweeted: &service.WeiboPostView{
			Mid:       "5000000000008888",
			Author:    service.WeiboAuthorDTO{UID: "500009", ScreenName: "被转发原作者"},
			CreatedAt: 1730980000,
			Text:      "被转发原博正文内容展示",
			Images: []service.WeiboMediaItemDTO{
				{Index: 1, Kind: "gif", FromOrigin: true, ImageURL: "https://wx2.sinaimg.cn/large/syn_rt_gif.gif", Width: 640, Height: 480},
			},
			Video: &service.WeiboVideoDTO{
				ObjectID:    "1034:5000000000008888",
				Title:       "被转发原博视频标题",
				CoverURL:    "https://wx2.sinaimg.cn/large/syn_rt_vid.jpg",
				DurationSec: 75,
			},
		},
	}
	vidPost := service.WeiboPostView{
		Mid:          vidMid,
		Author:       service.WeiboAuthorDTO{UID: "500003", ScreenName: "收藏视频博主"},
		CreatedAt:    1730970000,
		Text:         "原创收藏视频微博",
		CanonicalURL: "https://weibo.com/500003/" + vidMid,
		CacheKey:     "weibo:dynamic:" + vidMid,
		Video: &service.WeiboVideoDTO{
			ObjectID:    "1034:" + vidMid,
			Title:       "原创收藏视频封面标题",
			CoverURL:    "https://wx3.sinaimg.cn/large/syn_outer_vid.jpg",
			DurationSec: 40,
		},
	}

	stub.pages["weibo/favorites//"] = service.AccountPage{
		Total: 3,
		Items: []service.AccountItem{
			{Kind: service.AccountItemPost, Site: "weibo", ID: imgMid, Title: imgPost.Text, Author: imgPost.Author.ScreenName, AuthorID: imgPost.Author.UID, OpenURL: imgPost.CanonicalURL, BrowserURL: imgPost.CanonicalURL, CacheKey: imgPost.CacheKey, Post: &imgPost},
			{Kind: service.AccountItemPost, Site: "weibo", ID: rtMid, Title: rtPost.Text, Author: rtPost.Author.ScreenName, AuthorID: rtPost.Author.UID, OpenURL: rtPost.CanonicalURL, BrowserURL: rtPost.CanonicalURL, CacheKey: rtPost.CacheKey, Post: &rtPost},
			{Kind: service.AccountItemPost, Site: "weibo", ID: vidMid, Title: vidPost.Text, Author: vidPost.Author.ScreenName, AuthorID: vidPost.Author.UID, OpenURL: vidPost.CanonicalURL, BrowserURL: vidPost.CanonicalURL, CacheKey: vidPost.CacheKey, Post: &vidPost},
		},
	}

	app.OpenMine("weibo", service.AccountFavorites)
	tt := ui.NewTester(app.View, 1280, 1100)
	tt.Frame()
	tt.Frame()

	// 1. Outer images (including LivePhoto badge), retweet quote box, retweet GIF badge, retweet video card, and outer video card all render!
	for _, label := range []string{
		"我的微博行：" + imgMid,
		"查看图文页 1",
		"查看图文页 2",
		"实况标记 2",
		"我的微博转发内容：" + rtMid,
		"我的微博转发正文：" + rtMid,
		"动图标记 1",
		"播放原视频卡片：" + rtPost.CacheKey,
		"原微博未完整提示：mine-wb:" + rtMid,
		"播放视频卡片：" + vidPost.CacheKey,
	} {
		if _, ok := tt.Find(label); !ok {
			t.Fatalf("missing expected label %q in Mine › Weibo Favorites", label)
		}
	}
	for _, txt := range []string{"@被转发原作者", "被转发原博正文内容展示", "被转发原博视频标题", "原创收藏视频封面标题"} {
		if !tt.HasText(txt) {
			t.Fatalf("missing expected text %q in Mine › Weibo Favorites", txt)
		}
	}

	// 2. Clicking an image opens ImageViewer inline on RouteMine without navigating away.
	if err := tt.Click("查看图文页 2"); err != nil {
		t.Fatalf("click image cell: %v", err)
	}
	tt.Frame()
	if !app.ImageViewer.Open || app.ImageViewer.InlineSite != "weibo" || app.ImageViewer.Index != 1 || app.CurrentRoute != RouteMine {
		t.Fatalf("unexpected ImageViewer state: open=%v site=%q idx=%d route=%s", app.ImageViewer.Open, app.ImageViewer.InlineSite, app.ImageViewer.Index, app.CurrentRoute)
	}
	app.closeImageViewer()
	tt.Frame()

	// 3. Clicking uncached retweeted video card opens Weibo post detail and NavigateBack returns to Mine.
	if err := tt.Click("播放原视频卡片：" + rtPost.CacheKey); err != nil {
		t.Fatalf("click retweet video card: %v", err)
	}
	tt.Frame()
	if app.CurrentRoute != RouteWeiboPost || app.CurrentWeiboPost == nil || app.CurrentWeiboPost.Mid != rtMid {
		t.Fatalf("expected RouteWeiboPost for %s, got route=%s post=%+v", rtMid, app.CurrentRoute, app.CurrentWeiboPost)
	}
	app.NavigateBack()
	tt.Frame()
	if app.CurrentRoute != RouteMine {
		t.Fatalf("expected NavigateBack to return to RouteMine, got %s", app.CurrentRoute)
	}

	// 4. Narrow 720px layout has zero horizontal overflow.
	ttNarrow := ui.NewTester(app.View, 720, 960)
	ttNarrow.Frame()
	centerRect, ok := ttNarrow.Find("中栏内容")
	rowRect, okRow := ttNarrow.Find("我的微博行：" + imgMid)
	if !ok || !okRow || centerRect.X+centerRect.W > 720.5 || rowRect.X+rowRect.W > 720.5 {
		t.Fatalf("narrow 720px overflow: center=%+v row=%+v", centerRect, rowRect)
	}
}

func mineDouyinWorks(prefix string, n int) []service.AccountItem {
	out := make([]service.AccountItem, n)
	for i := range out {
		id := fmt.Sprintf("%s%02d", prefix, i)
		kind := "video"
		if i%3 == 1 {
			kind = "note"
		}
		card := service.DouyinWorkCardDTO{
			ID:           id,
			Kind:         kind,
			Title:        "观看历史作品 " + id,
			DurationSec:  20 + i*5,
			CanonicalURL: "https://www.douyin.com/video/" + id,
			CacheKey:     "douyin:video:" + id,
		}
		out[i] = service.AccountItem{
			Kind:       service.AccountItemWork,
			Site:       "douyin",
			ID:         id,
			Title:      card.Title,
			Author:     "抖音作者甲",
			AuthorID:   "MS4wLjABAAAA_dy_author_000001",
			OpenURL:    card.CanonicalURL,
			BrowserURL: card.CanonicalURL,
			CacheKey:   card.CacheKey,
			Work:       &card,
		}
	}
	return out
}

func TestMineDouyinWatchHistoryGridPaginationDetailBackAndSignOut(t *testing.T) {
	app, stub, cleanup := newMineApp(t)
	defer cleanup()
	stub.logged["douyin"] = true

	page1 := mineDouyinWorks("7550000000000001", 25)
	page2 := mineDouyinWorks("7550000000000002", 6)
	page2[0] = page1[1] // duplicate across pages must be dropped
	stub.pages["douyin/watch_history//"] = service.AccountPage{Items: page1, HasMore: true, Cursor: "2|1732400100"}
	stub.pages["douyin/watch_history//2|1732400100"] = service.AccountPage{Items: page2, HasMore: false}

	app.Navigate(RouteMine)
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	if err := tt.Click("我的站点：抖音"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for _, label := range []string{"关注", "喜欢", "收藏", "观看历史"} {
		if _, ok := tt.Find("我的功能：抖音 " + label); !ok {
			t.Fatalf("expanded 抖音 lacks %s", label)
		}
	}
	if err := tt.Click("我的功能：抖音 观看历史"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if got := app.routeTitle(); got != "观看历史 · 抖音" {
		t.Fatalf("title = %q, want 观看历史 · 抖音", got)
	}
	if got := app.mineSiteURL("douyin", service.AccountWatchHistory); got != "https://www.douyin.com/user/self?showTab=record" {
		t.Fatalf("mineSiteURL = %q", got)
	}
	if _, ok := tt.Find("收藏夹选择"); ok {
		t.Fatal("watch history should not show folder dropdown")
	}
	if _, ok := tt.Find("我的作品虚拟列表"); !ok {
		t.Fatal("watch history work grid missing")
	}
	if got := menuItems(t, tt, "作品更多："+page1[0].ID); strings.Join(got, ",") != "缓存,复制链接,在浏览器中打开" {
		t.Fatalf("watch history work menu = %v", got)
	}

	// Scroll to load page 2 once and deduplicate across pages.
	l := app.mineCurrentList()
	if l == nil || len(l.Items) != 25 || !l.HasMore {
		t.Fatalf("page 1 list = %+v", l)
	}
	listRect, _ := tt.Find("我的作品虚拟列表")
	for i := 0; i < 10; i++ {
		tt.Scroll(listRect.X+listRect.W/2, listRect.Y+listRect.H/2, 0, 1500)
		tt.Frame()
	}
	if stub.calls["douyin/watch_history//2|1732400100"] != 1 {
		t.Fatalf("page 2 calls = %d, want 1", stub.calls["douyin/watch_history//2|1732400100"])
	}
	if len(l.Items) != 30 || l.HasMore {
		t.Fatalf("after page 2: items=%d more=%v", len(l.Items), l.HasMore)
	}
	if _, ok := tt.Find("我的没有更多"); !ok {
		t.Fatal("end of watch history not shown")
	}

	// Opening detail and navigating back preserves the list and scroll position without re-requesting.
	firstVisible, _ := l.State.Visible()
	if firstVisible == 0 {
		t.Fatal("watch history grid did not scroll")
	}
	callsBefore := stub.callCount()
	app.OpenDouyinWork(page1[0].ID, "video", page1[0].Title, page1[0].Author, page1[0].AuthorID, "", "0:20")
	if app.CurrentRoute != RouteDouyinWork || app.PreviousRoute != RouteMine {
		t.Fatalf("open work: route=%s prev=%s", app.CurrentRoute, app.PreviousRoute)
	}
	tt.Frame()
	app.NavigateBack()
	tt.Frame()
	if app.CurrentRoute != RouteMine || app.mineCurrentList() != l || stub.callCount() != callsBefore {
		t.Fatalf("back: route=%s same=%v calls %d->%d", app.CurrentRoute, app.mineCurrentList() == l, callsBefore, stub.callCount())
	}
	if afterVisible, _ := l.State.Visible(); afterVisible != firstVisible {
		t.Fatalf("scroll position lost: %d -> %d", firstVisible, afterVisible)
	}

	// Narrow 720px chooser exposes 观看历史 and opens grid with zero horizontal overflow.
	app.Mine.Picker = true
	ttNarrow := ui.NewTester(app.View, 720, 800)
	ttNarrow.Frame()
	if _, ok := ttNarrow.Find("我的功能：抖音 观看历史"); !ok {
		t.Fatal("narrow chooser lacks 观看历史")
	}
	if err := ttNarrow.Click("我的功能：抖音 观看历史"); err != nil {
		t.Fatal(err)
	}
	ttNarrow.Frame()
	centerRect, ok := ttNarrow.Find("中栏内容")
	if !ok || centerRect.X+centerRect.W > 720.5 {
		t.Fatalf("narrow watch history overflow: %+v", centerRect)
	}
	if err := ttNarrow.Click("返回我的功能列表"); err != nil {
		t.Fatal(err)
	}
	ttNarrow.Frame()
	if _, ok := ttNarrow.Find("我的选择列表"); !ok {
		t.Fatal("back from narrow watch history did not return to chooser")
	}

	// Error and genuine empty state on first page.
	app.mineClearSite("douyin")
	stub.errs["douyin/watch_history//"] = &site.Error{Site: site.Douyin, Kind: site.ErrKindRiskControl, Message: "抖音返回了空壳，登录已失效或被拦截"}
	app.mineSelect("douyin", service.AccountWatchHistory)
	ttErr := ui.NewTester(app.View, 1280, 860)
	ttErr.Frame()
	if _, ok := ttErr.Find("我的列表为空"); ok {
		t.Fatal("watch history error shown as empty")
	}
	if _, ok := ttErr.Find("我的重试"); !ok {
		t.Fatal("watch history error lacks retry")
	}
	delete(stub.errs, "douyin/watch_history//")
	stub.pages["douyin/watch_history//"] = service.AccountPage{Items: []service.AccountItem{}, HasMore: false}
	if err := ttErr.Click("我的重试"); err != nil {
		t.Fatal(err)
	}
	ttErr.Frame()
	if _, ok := ttErr.Find("我的列表为空"); !ok {
		t.Fatal("genuine empty watch history not shown")
	}

	// Sign-out clears Douyin watch history from memory immediately.
	stub.mu.Lock()
	stub.logged["douyin"] = false
	stub.mu.Unlock()
	app.handleBackendEvent(service.Event{Type: service.EventAccountChanged, Site: "douyin", Message: "logged_out"})
	for k := range app.Mine.Lists {
		if strings.HasPrefix(k, "douyin/") {
			t.Fatalf("douyin list %s kept after sign-out", k)
		}
	}
	if app.Mine.Kind != "" {
		t.Fatalf("douyin watch history stayed selected after sign-out: %q", app.Mine.Kind)
	}
}
