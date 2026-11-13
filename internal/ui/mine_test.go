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
	return s.logged["bilibili"] || s.logged["douyin"]
}

func (s *mineStub) AccountSections(siteID string) []service.AccountSection {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.logged[siteID] {
		return []service.AccountSection{{Site: siteID, Kind: service.AccountLogin, Label: "未登录", Reason: "未登录"}}
	}
	if siteID == "bilibili" {
		return []service.AccountSection{
			{Site: siteID, Kind: service.AccountFollowing, Label: "关注", Available: true},
			{Site: siteID, Kind: service.AccountFavorites, Label: "收藏夹", Available: true},
			{Site: siteID, Kind: service.AccountToView, Label: "稍后再看", Available: true},
			{Site: siteID, Kind: service.AccountHistory, Label: "历史记录", Available: true},
		}
	}
	return []service.AccountSection{
		{Site: siteID, Kind: service.AccountFollowing, Label: "关注", Available: true},
		{Site: siteID, Kind: service.AccountLikes, Label: "喜欢", Available: true},
		{Site: siteID, Kind: service.AccountCollection, Label: "收藏", Available: true},
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

func (s *mineStub) CreateSubscription(name, note string, accounts []subscription.AccountRef, rules subscription.SubRules) (subscription.Subscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub := subscription.Subscription{ID: "sub-" + accounts[0].UID, Name: name}
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
