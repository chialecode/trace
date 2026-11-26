// Package app implements the TRACE mobile companion shell: pairing, foreground
// connection management, reconnect backoff, WebSocket heartbeat, loopback media
// proxy lifecycle, five bottom tabs, and the full-screen connection gate.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/cmd/trace-mobile/internal/pagekit"
	"github.com/chialecode/trace/cmd/trace-mobile/internal/pages/computer"
	"github.com/chialecode/trace/cmd/trace-mobile/internal/pages/home"
	"github.com/chialecode/trace/cmd/trace-mobile/internal/pages/library"
	"github.com/chialecode/trace/cmd/trace-mobile/internal/pages/subscriptions"
	"github.com/chialecode/trace/cmd/trace-mobile/internal/pages/tasks"
	"github.com/chialecode/trace/cmd/trace-mobile/internal/qrscanner"
	"github.com/chialecode/trace/cmd/trace-mobile/internal/theme"
	"github.com/chialecode/trace/internal/companionapi"
	"github.com/chialecode/trace/internal/companionclient"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/mobile"
)

// SavedUIState is persisted via mygo.App.OnSaveState across OS background kills.
type SavedUIState struct {
	Tab    int      `json:"tab"`
	Routes []string `json:"routes"`
}

// Options configures the mobile App instance.
type Options struct {
	Store       Store
	Channel     string
	AppVersion  string
	DeviceName  string
	DeviceModel string
	OnlineFn    func() bool
	OpenURLFn   func(string) error
}

// App coordinates connection state, lifecycle, and the five bottom-tab pages.
type App struct {
	store       Store
	channel     string
	appVersion  string
	deviceName  string
	deviceModel string
	onlineFn    func() bool
	openURLFn   func(string) error

	probeFn func(ctx context.Context, addr string) (string, string, error)
	pairFn  func(ctx context.Context, addrs []string, fp string, req companionapi.PairRequest, onWaiting func(string)) (string, string, companionapi.PairPollResponse, error)

	mu         sync.Mutex
	foreground bool
	connState  pagekit.ConnState
	tryingAddr string
	lastErr    *companionclient.Error
	desktop    pagekit.DesktopSnapshot
	client     *companionclient.Client
	proxy      *companionclient.MediaProxy
	imgCache   *companionclient.MemoryImageCache
	backoff    *companionclient.ReconnectSchedule
	connCancel context.CancelFunc
	retryTimer *time.Timer
	tasksBadge int

	// Navigation & UI
	activeTab  int
	pages      [5]pagekit.TabPage
	routers    [5]*ui.Router
	tabWindows [5]*mygo.Window
	tabs       *mygo.Tabs
	gateWindow *mygo.Window
	qrScanner  qrscanner.Scanner
	invalidate func() // optional test hook for ui.Tester

	pair pairingState
}

// New creates an App ready to run in MyGo or ui.Tester.
func New(opts Options) *App {
	if opts.Store == nil {
		opts.Store = MyGoStore{}
	}
	ch := companionclient.NormalizeChannel(opts.Channel)
	if ch == "" {
		if mygo.IsDev() {
			ch = "dev"
		} else {
			ch = "stable"
		}
	}
	ver := strings.TrimSpace(opts.AppVersion)
	if ver == "" {
		ver = "0.1.0-dev"
	}
	if opts.OnlineFn == nil {
		opts.OnlineFn = func() bool { return mygo.Network.Status().Online }
	}
	if opts.OpenURLFn == nil {
		opts.OpenURLFn = mygo.Shell.OpenExternal
	}
	devName := strings.TrimSpace(opts.DeviceName)
	if devName == "" {
		devName = "Android Phone"
	}
	devModel := strings.TrimSpace(opts.DeviceModel)
	if devModel == "" {
		devModel = "Android"
	}

	a := &App{
		store:       opts.Store,
		channel:     ch,
		appVersion:  ver,
		deviceName:  devName,
		deviceModel: devModel,
		onlineFn:    opts.OnlineFn,
		openURLFn:   opts.OpenURLFn,
		probeFn:     companionclient.ProbeIdentity,
		pairFn:      companionclient.PairWithAddrs,
		foreground:  true,
		imgCache:    companionclient.NewMemoryImageCache(companionclient.DefaultImageCacheBytes),
		backoff:     companionclient.NewReconnectSchedule(),
	}
	a.pages = [5]pagekit.TabPage{
		home.New(),
		library.New(),
		subscriptions.New(),
		tasks.New(),
		computer.New(),
	}
	defaultRoutes := [5]string{"/", "/", "/", "/", "/"}
	for i := range a.routers {
		a.routers[i] = ui.NewRouter(defaultRoutes[i])
		a.routers[i].SystemStack = true
	}
	if _, ok := a.store.LoadPairing(); !ok {
		a.connState = pagekit.ConnUnpaired
	} else {
		a.connState = pagekit.ConnConnecting
	}
	return a
}

// Run starts the MyGo mobile application lifecycle and windows.
func (a *App) Run() error {
	mygo.App.OnSaveState(func() []byte {
		routes := make([]string, len(a.routers))
		for i, r := range a.routers {
			routes[i] = r.Location()
		}
		b, _ := json.Marshal(SavedUIState{
			Tab:    a.activeTab,
			Routes: routes,
		})
		return b
	})

	mygo.Power.OnSuspend(func() { a.SetForeground(false) })
	mygo.Power.OnResume(func() { a.SetForeground(true) })
	mygo.App.OnActivate(func(bool) { a.SetForeground(true) })
	mygo.Network.OnChanged(func(st mygo.NetworkStatus) { a.OnNetworkChanged(st.Online) })

	mygo.App.WhenReady(func() {
		// Restore saved navigation route state (only routes, zero content — NFR-MOB-DATA-001).
		if raw := mygo.App.RestoredState(); len(raw) > 0 {
			var saved SavedUIState
			if json.Unmarshal(raw, &saved) == nil {
				if saved.Tab >= 0 && saved.Tab < len(a.routers) {
					a.activeTab = saved.Tab
				}
				for i := 0; i < len(a.routers) && i < len(saved.Routes); i++ {
					if strings.HasPrefix(saved.Routes[i], "/") {
						a.routers[i] = ui.NewRouter(saved.Routes[i])
						a.routers[i].SystemStack = true
					}
				}
			}
		}
		a.setupWindows()
		a.StartForegroundConnect()
	})
	return mygo.App.Run()
}

func (a *App) setupWindows() {
	iconNames := [5]string{"home", "library", "subscriptions", "tasks", "computer"}
	symbols := [5]string{"house", "photo.on.rectangle", "person.2", "arrow.down.circle", "desktopcomputer"}
	tabItems := make([]mygo.TabItem, 5)
	for i := range 5 {
		idx := i
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:  a.pages[idx].Title(),
			Hidden: true,
			Content: ui.View(func(c *ui.Context) {
				c.SetTheme(theme.TraceTheme())
				a.pages[idx].View(c, a.tabHost(idx))
			}),
		})
		a.tabWindows[idx] = win
		tabItems[idx] = mygo.TabItem{
			Title:  a.pages[idx].Title(),
			Symbol: symbols[idx],
			Icon:   theme.TabIconPNG(iconNames[idx]),
			Window: win,
		}
	}
	tabs, err := mygo.NewTabs(tabItems...)
	if err == nil {
		a.tabs = tabs
		if a.activeTab > 0 && a.activeTab < 5 {
			tabs.Select(a.activeTab)
		}
		tabs.OnSelect(func(i int) {
			a.activeTab = i
		})
	}

	a.gateWindow = mygo.NewWindow(mygo.WindowOptions{
		Title:  "TRACE",
		Hidden: true,
		Content: ui.View(func(c *ui.Context) {
			a.renderGate(c)
		}),
	})
	if a.connState != pagekit.ConnConnected {
		a.gateWindow.Show()
	}
}

// View renders the testable root view (used by ui.Tester): either the full-screen
// pairing/connection gate, or the connected tab view + Mobile BottomBar.
func (a *App) View(c *ui.Context) {
	c.SetTheme(theme.TraceTheme())
	if a.connState != pagekit.ConnConnected || a.pair.rescanning {
		a.renderGate(c)
		return
	}
	ui.Column(c).Fill().Background(theme.ColorBgMain).Children(func() {
		idx := a.activeTab
		if idx < 0 || idx >= len(a.pages) {
			idx = 0
		}
		ui.Box(c.Key(idx)).Grow(1).Basis(0).FillWidth().Children(func() {
			a.pages[idx].View(c, a.tabHost(idx))
		})
		badgeStr := ""
		if a.tasksBadge > 0 {
			badgeStr = fmt.Sprint(a.tasksBadge)
		}
		buttons := make([]mobile.BarButton, 5)
		iconNames := [5]string{"home", "library", "subscriptions", "tasks", "computer"}
		for i, p := range a.pages {
			name := iconNames[i]
			b := mobile.BarButton{
				Label: p.Title(),
				Icon: func(painter *ui.Painter, r ui.Rect, col ui.Color) {
					theme.PaintIcon(painter, r, name, col, 1.8)
				},
			}
			if i == int(pagekit.TabTasks) {
				b.Badge = badgeStr
			}
			buttons[i] = b
		}
		mobile.BottomBar(c, &a.activeTab, buttons...)
	})
}

func (a *App) renderGate(c *ui.Context) {
	c.SetTheme(theme.TraceTheme())
	if a.connState == pagekit.ConnUnpaired || a.pair.rescanning {
		a.renderPairingGate(c)
		return
	}
	a.renderConnectionGate(c)
}

// SetForeground handles foreground/background transitions (design §7.4).
func (a *App) SetForeground(fg bool) {
	a.mu.Lock()
	if a.foreground == fg {
		a.mu.Unlock()
		return
	}
	a.foreground = fg
	a.mu.Unlock()

	if !fg {
		a.stopConnectionLocked(nil)
		a.Update(func() {
			a.stopQRScanner()
		})
		return
	}
	a.Update(func() {
		if a.pair.step == pairStepQRScan && (a.connState == pagekit.ConnUnpaired || a.pair.rescanning) {
			a.ensureQRScanner()
		}
	})
	a.backoff.Reset()
	a.StartForegroundConnect()
}

// OnNetworkChanged responds to OS connectivity changes (design §7.4 & §8).
func (a *App) OnNetworkChanged(online bool) {
	if !online {
		a.stopConnectionLocked(&companionclient.Error{Kind: companionclient.KindPhoneOffline})
		return
	}
	a.mu.Lock()
	fg := a.foreground
	st := a.connState
	a.mu.Unlock()
	if fg && st != pagekit.ConnConnected {
		a.backoff.Reset()
		a.StartForegroundConnect()
	}
}

// StartForegroundConnect initiates connection if paired and in foreground.
func (a *App) StartForegroundConnect() {
	rec, ok := a.store.LoadPairing()
	if !ok {
		a.Update(func() {
			a.connState = pagekit.ConnUnpaired
			a.syncGateVisibility()
		})
		return
	}
	tok, err := a.store.LoadToken()
	if err != nil || tok == "" {
		a.Update(func() {
			a.connState = pagekit.ConnError
			a.lastErr = &companionclient.Error{Kind: companionclient.KindUnauthorized}
			a.syncGateVisibility()
		})
		return
	}
	if !a.onlineFn() {
		a.stopConnectionLocked(&companionclient.Error{Kind: companionclient.KindPhoneOffline})
		return
	}

	a.mu.Lock()
	if !a.foreground {
		a.mu.Unlock()
		return
	}
	if a.connCancel != nil {
		a.connCancel()
	}
	if a.retryTimer != nil {
		a.retryTimer.Stop()
		a.retryTimer = nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.connCancel = cancel
	a.mu.Unlock()

	a.Update(func() {
		a.connState = pagekit.ConnConnecting
		a.tryingAddr = ""
		if len(rec.OrderedAddrs()) > 0 {
			a.tryingAddr = rec.OrderedAddrs()[0]
		}
		a.lastErr = nil
		a.syncGateVisibility()
	})

	go a.runConnectionSession(ctx, rec, tok)
}

// RetryNow cancels any backoff sleep and reconnects immediately.
func (a *App) RetryNow() {
	a.backoff.Reset()
	a.StartForegroundConnect()
}

// StartRescan opens the pairing QR screen to re-pair or update computer addresses.
func (a *App) StartRescan() {
	a.Update(func() {
		a.pair.rescanning = true
		a.pair.errMsg = ""
		a.enterQRScan()
		a.syncGateVisibility()
	})
}

func (a *App) cancelConnect() {
	a.mu.Lock()
	if a.connCancel != nil {
		a.connCancel()
		a.connCancel = nil
	}
	if a.retryTimer != nil {
		a.retryTimer.Stop()
		a.retryTimer = nil
	}
	a.mu.Unlock()
	a.Update(func() {
		a.connState = pagekit.ConnError
		a.lastErr = &companionclient.Error{Kind: companionclient.KindCanceled}
		a.syncGateVisibility()
	})
}

func (a *App) runConnectionSession(ctx context.Context, rec companionclient.PairingRecord, tok string) {
	cli, err := companionclient.NewClient(rec, tok, a.appVersion, a.channel)
	if err != nil {
		a.handleDisconnect(ctx, &companionclient.Error{Kind: companionclient.KindCertChanged, Cause: err})
		return
	}
	st, activeAddr, err := cli.ConnectStatus(ctx)
	if err != nil {
		cli.Close()
		a.handleDisconnect(ctx, asCompanionError(err))
		return
	}
	if activeAddr != "" && activeAddr != rec.LastAddr {
		rec.LastAddr = activeAddr
		_ = a.store.SavePairing(rec)
	}

	proxy, err := companionclient.NewMediaProxy(cli.HTTPClient(), func() (string, string, string, string, bool) {
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.client == nil || a.connState != pagekit.ConnConnected {
			return "", "", "", "", false
		}
		return a.client.ActiveBaseURL(), a.client.Token(), a.client.AppVersion(), a.client.Channel(), true
	})
	if err != nil {
		cli.Close()
		a.handleDisconnect(ctx, asCompanionError(err))
		return
	}

	ec, err := cli.DialEvents(ctx, companionclient.DefaultHeartbeatTimeout)
	if err != nil {
		_ = proxy.Close()
		cli.Close()
		a.handleDisconnect(ctx, asCompanionError(err))
		return
	}

	a.backoff.Reset()
	a.mu.Lock()
	if ctx.Err() != nil {
		a.mu.Unlock()
		_ = ec.Close()
		_ = proxy.Close()
		cli.Close()
		return
	}
	a.client = cli
	a.proxy = proxy
	a.desktop = snapshotFromStatus(rec, activeAddr, st)
	a.tasksBadge = st.DownloadBadge
	a.mu.Unlock()

	a.Update(func() {
		a.connState = pagekit.ConnConnected
		a.lastErr = nil
		if a.tabs != nil {
			badge := ""
			if st.DownloadBadge > 0 {
				badge = fmt.Sprint(st.DownloadBadge)
			}
			a.tabs.SetBadge(int(pagekit.TabTasks), badge)
		}
		a.syncGateVisibility()
		for i, pg := range a.pages {
			pg.OnConnected(a.tabHost(i))
		}
	})

	// Read WebSocket events until disconnect, bye, heartbeat timeout, or suspend.
	for {
		ev, err := ec.ReadEvent(ctx)
		if err != nil {
			_ = ec.Close()
			a.handleDisconnect(ctx, asCompanionError(err))
			return
		}
		if ev.Type == "bye" {
			_ = ec.Close()
			a.handleDisconnect(ctx, companionclient.ByeError(ev.Reason))
			return
		}
		a.dispatchEvent(ev)
	}
}

func (a *App) dispatchEvent(ev companionclient.EventFrame) {
	if ev.Type == "auth:changed" {
		a.RefreshStatus()
	}
	a.Update(func() {
		for i, pg := range a.pages {
			pg.OnEvent(a.tabHost(i), ev)
		}
	})
}

func (a *App) handleDisconnect(sessionCtx context.Context, ce *companionclient.Error) {
	if sessionCtx.Err() != nil {
		return
	}
	a.stopConnectionLocked(ce)

	a.mu.Lock()
	fg := a.foreground
	a.mu.Unlock()
	if !fg || !shouldAutoReconnect(ce) {
		return
	}
	delay := a.backoff.Next()
	a.mu.Lock()
	if a.retryTimer != nil {
		a.retryTimer.Stop()
	}
	a.retryTimer = time.AfterFunc(delay, func() {
		a.StartForegroundConnect()
	})
	a.mu.Unlock()
}

func (a *App) stopConnectionLocked(ce *companionclient.Error) {
	a.mu.Lock()
	if a.connCancel != nil {
		a.connCancel()
		a.connCancel = nil
	}
	if a.retryTimer != nil {
		a.retryTimer.Stop()
		a.retryTimer = nil
	}
	cli := a.client
	proxy := a.proxy
	a.client = nil
	a.proxy = nil
	hasPairing := false
	if _, ok := a.store.LoadPairing(); ok {
		hasPairing = true
	}
	a.mu.Unlock()

	if proxy != nil {
		_ = proxy.Close()
	}
	if cli != nil {
		cli.Close()
	}
	a.imgCache.Clear()

	a.Update(func() {
		for _, pg := range a.pages {
			pg.OnDisconnected()
		}
		if !hasPairing {
			a.connState = pagekit.ConnUnpaired
			a.lastErr = nil
		} else {
			a.connState = pagekit.ConnError
			a.lastErr = ce
		}
		a.syncGateVisibility()
	})
}

func (a *App) syncGateVisibility() {
	if a.gateWindow == nil {
		return
	}
	if a.connState == pagekit.ConnConnected && !a.pair.rescanning {
		a.gateWindow.Hide()
	} else {
		a.gateWindow.Show()
	}
}

func shouldAutoReconnect(ce *companionclient.Error) bool {
	if ce == nil {
		return true
	}
	switch ce.Kind {
	case companionclient.KindPhoneOffline,
		companionclient.KindCertChanged,
		companionclient.KindUnauthorized,
		companionclient.KindByeRevoked,
		companionclient.KindRateLimited,
		companionclient.KindProtocolMismatch,
		companionclient.KindForbiddenSource,
		companionclient.KindCanceled:
		return false
	default:
		return true
	}
}

func asCompanionError(err error) *companionclient.Error {
	if ce, ok := err.(*companionclient.Error); ok {
		return ce
	}
	return companionclient.ClassifyTransportError(err)
}

func snapshotFromStatus(rec companionclient.PairingRecord, addr string, st companionapi.StatusResponse) pagekit.DesktopSnapshot {
	name := st.Name
	if name == "" {
		name = rec.Name
	}
	ch := companionclient.NormalizeChannel(st.Channel)
	if ch == "" {
		ch = rec.Channel
	}
	chLabel := "正式版"
	if ch == "dev" {
		chLabel = "开发版"
	}
	sites := make([]pagekit.SiteAuthStatus, 0, len(st.Sites))
	for _, s := range st.Sites {
		sites = append(sites, pagekit.SiteAuthStatus{
			Site:     s.Site,
			Label:    s.DisplayName,
			State:    s.State,
			Nickname: s.Uname,
		})
	}
	return pagekit.DesktopSnapshot{
		NodeID:       st.Node,
		ComputerName: name,
		Version:      st.Version,
		Channel:      ch,
		ChannelLabel: chLabel,
		StartedAt:    st.StartedAt,
		UptimeText:   formatUptime(st.UptimeSec, st.StartedAt),
		CurrentAddr:  addr,
		DiskFree:     st.Disk.FreeBytes,
		DiskTotal:    st.Disk.TotalBytes,
		LoggedIn:     st.LoggedIn,
		Sites:        sites,
	}
}

func formatUptime(uptimeSec int64, startedAt string) string {
	if uptimeSec <= 0 && startedAt != "" {
		if t, err := time.Parse(time.RFC3339, startedAt); err == nil {
			uptimeSec = int64(time.Since(t).Seconds())
		}
	}
	if uptimeSec < 0 {
		uptimeSec = 0
	}
	d := time.Duration(uptimeSec) * time.Second
	days := int(d / (24 * time.Hour))
	hours := int((d % (24 * time.Hour)) / time.Hour)
	mins := int((d % time.Hour) / time.Minute)
	switch {
	case days > 0:
		return fmt.Sprintf("%d 天 %d 小时", days, hours)
	case hours > 0:
		return fmt.Sprintf("%d 小时 %d 分钟", hours, mins)
	case mins > 0:
		return fmt.Sprintf("%d 分钟", mins)
	default:
		return "刚刚启动"
	}
}

// Update schedules fn on the UI main thread and triggers re-render.
func (a *App) Update(fn func()) {
	if a.invalidate != nil {
		if fn != nil {
			fn()
		}
		a.invalidate()
		return
	}
	win := a.gateWindow
	if a.connState == pagekit.ConnConnected && !a.pair.rescanning {
		idx := a.activeTab
		if idx >= 0 && idx < len(a.tabWindows) && a.tabWindows[idx] != nil {
			win = a.tabWindows[idx]
		}
	}
	if win != nil {
		win.Update(func() {
			if fn != nil {
				fn()
			}
		})
		return
	}
	if fn != nil {
		fn()
	}
}

// OpenExternal opens an http(s) URL in the phone's system browser.
func (a *App) OpenExternal(rawURL string) error {
	if !strings.HasPrefix(rawURL, "https://") && !strings.HasPrefix(rawURL, "http://") {
		return fmt.Errorf("unsupported external URL scheme")
	}
	return a.openURLFn(rawURL)
}

// RefreshStatus reloads GET /api/v1/status on the active client.
func (a *App) RefreshStatus() {
	a.mu.Lock()
	cli := a.client
	a.mu.Unlock()
	if cli == nil {
		return
	}
	rec, _ := a.store.LoadPairing()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		st, addr, err := cli.ConnectStatus(ctx)
		if err != nil {
			a.handleDisconnect(ctx, asCompanionError(err))
			return
		}
		a.Update(func() {
			a.desktop = snapshotFromStatus(rec, addr, st)
			a.tasksBadge = st.DownloadBadge
			if a.tabs != nil {
				badge := ""
				if st.DownloadBadge > 0 {
					badge = fmt.Sprint(st.DownloadBadge)
				}
				a.tabs.SetBadge(int(pagekit.TabTasks), badge)
			}
		})
	}()
}

// Unpair calls DELETE /api/v1/devices/self (best-effort), clears local pairing
// record and secret token, and returns to the pairing guide (design §4.6).
func (a *App) Unpair() {
	a.mu.Lock()
	cli := a.client
	a.mu.Unlock()
	if cli != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = cli.Unpair(ctx)
		cancel()
	}
	_ = a.store.ClearAll()
	a.pair = pairingState{step: pairStepGuide}
	a.stopConnectionLocked(nil)
}

func (a *App) tabHost(tabIdx int) pagekit.Host {
	return &tabHost{app: a, idx: tabIdx}
}

type tabHost struct {
	app *App
	idx int
}

func (h *tabHost) ConnState() pagekit.ConnState {
	h.app.mu.Lock()
	defer h.app.mu.Unlock()
	return h.app.connState
}

func (h *tabHost) Desktop() pagekit.DesktopSnapshot {
	h.app.mu.Lock()
	defer h.app.mu.Unlock()
	return h.app.desktop
}

func (h *tabHost) Client() *companionclient.Client {
	h.app.mu.Lock()
	defer h.app.mu.Unlock()
	return h.app.client
}

func (h *tabHost) MediaProxy() *companionclient.MediaProxy {
	h.app.mu.Lock()
	defer h.app.mu.Unlock()
	return h.app.proxy
}

func (h *tabHost) ImageCache() *companionclient.MemoryImageCache {
	return h.app.imgCache
}

func (h *tabHost) FetchImage(ctx context.Context, path string) ([]byte, error) {
	if cached, ok := h.app.imgCache.Get(path); ok {
		return cached, nil
	}
	cli := h.Client()
	if cli == nil {
		return nil, &companionclient.Error{Kind: companionclient.KindUnreachable}
	}
	raw, err := cli.FetchBytes(ctx, path)
	if err != nil {
		return nil, err
	}
	h.app.imgCache.Put(path, raw)
	return raw, nil
}

func (h *tabHost) Window() *mygo.Window {
	return h.app.tabWindows[h.idx]
}

func (h *tabHost) Router() *ui.Router {
	return h.app.routers[h.idx]
}

func (h *tabHost) PushRoute(path string) {
	h.app.routers[h.idx].Push(path)
}

func (h *tabHost) Back() {
	h.app.routers[h.idx].Back()
}

func (h *tabHost) PushWindow(title string, view func(c *ui.Context)) *mygo.Window {
	parent := h.app.tabWindows[h.idx]
	child := mygo.NewWindow(mygo.WindowOptions{
		Title:   title,
		Hidden:  true,
		Content: ui.View(view),
	})
	if parent != nil {
		parent.Push(child)
	}
	return child
}

func (h *tabHost) Update(fn func()) {
	h.app.Update(fn)
}

func (h *tabHost) SetTasksBadge(count int) {
	h.app.Update(func() {
		h.app.tasksBadge = count
		if h.app.tabs != nil {
			badge := ""
			if count > 0 {
				badge = fmt.Sprint(count)
			}
			h.app.tabs.SetBadge(int(pagekit.TabTasks), badge)
		}
	})
}

func (h *tabHost) SelectTab(tab pagekit.TabID) {
	idx := int(tab)
	if idx < 0 || idx >= 5 {
		return
	}
	h.app.Update(func() {
		h.app.activeTab = idx
		if h.app.tabs != nil {
			h.app.tabs.Select(idx)
		}
	})
}

func (h *tabHost) OpenExternal(rawURL string) error {
	return h.app.OpenExternal(rawURL)
}

func (h *tabHost) RefreshStatus() {
	h.app.RefreshStatus()
}

func (h *tabHost) Unpair() {
	h.app.Unpair()
}
