// Package subscriptions implements the 订阅 tab for TRACE mobile
// (FR-MOB-015, FR-MOB-021, FR-MOB-022, FR-MOB-025, docs/ui.md §3.12).
package subscriptions

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/cmd/trace-mobile/internal/pagekit"
	"github.com/chialecode/trace/cmd/trace-mobile/internal/theme"
	"github.com/chialecode/trace/internal/companionapi"
	"github.com/chialecode/trace/internal/companionclient"
	"github.com/egoist/mygo/ui"
)

const (
	segAllSubs = iota
	segAllAccounts
	segCheckPlan
)

const (
	detailTabContent = iota
	detailTabAccounts
)

const (
	defaultRefreshThrottle = time.Second
	maxInMemoryBitmaps     = 160
	bitmapFailCooldown     = 15 * time.Second
	bitmapMaxConcurrency   = 6
)

// Page implements pagekit.TabPage for 订阅.
type Page struct {
	// RefreshThrottle bounds WebSocket-triggered refreshes (<= 1/sec, FR-MOB-016 / NFR-MOB-NET-001).
	// Zero uses defaultRefreshThrottle (1s).
	RefreshThrottle time.Duration

	activeSeg  int
	siteFilter string // "all", "bilibili", "douyin", "weibo", "x"

	initTriggered bool
	sessionGen    uint64

	// Data snapshots (in-memory only, cleared on OnDisconnected).
	subs        []companionapi.SubscriptionDTO
	subsLoading bool
	subsLoaded  bool
	subsErr     string
	subsRefresh ui.RefreshState
	subsListSt  ui.ListState

	accounts        []companionapi.SubscriptionAccountDTO
	accountsLoading bool
	accountsLoaded  bool
	accountsErr     string
	accountsRefresh ui.RefreshState
	accountsListSt  ui.ListState

	plan        []companionapi.CheckPlanRowDTO
	planStatus  string
	planLoading bool
	planLoaded  bool
	planErr     string
	planRefresh ui.RefreshState
	planListSt  ui.ListState

	// Check-update progress & control state.
	checking           bool
	checkMessage       string
	checkingAccountKey string
	actionErr          string
	actionInfo         string
	busyLiveKey        string

	// Detail view state (/sub/{id}).
	detailSubID       string
	detailSub         *companionapi.SubscriptionDTO
	detailLoading     bool
	detailErr         string
	detailTab         int
	detailMenuOpen    bool
	detailFeed        []companionapi.FeedItem
	detailFeedNext    *companionapi.FeedCursor
	detailFeedHasMore bool
	detailFeedLoading bool
	detailFeedMoreing bool
	detailFeedErr     string
	detailFeedRefresh ui.RefreshState
	detailListSt      ui.ListState

	// Event throttling state.
	evMu          sync.Mutex
	lastEventLoad time.Time
	pendingSubs   bool
	pendingPlan   bool
	pendingFeed   bool
	throttleTimer *time.Timer

	// In-memory decoded bitmap cache (never written to disk, cleared on disconnect).
	imgMu        sync.Mutex
	bitmaps      map[string]*ui.Bitmap
	imgInFlight  map[string]bool
	imgFailUntil map[string]time.Time
	imgOrder     []string
	imgSem       chan struct{}

	// Active child windows (image viewers & video players) closed immediately on disconnect (FR-MOB-006).
	childMu            sync.Mutex
	nextChildID        uint64
	activeChildClosers map[uint64]func()
}

// New returns the 订阅 tab page.
func New() *Page {
	return &Page{
		siteFilter:         "all",
		bitmaps:            make(map[string]*ui.Bitmap),
		imgInFlight:        make(map[string]bool),
		imgFailUntil:       make(map[string]time.Time),
		imgSem:             make(chan struct{}, bitmapMaxConcurrency),
		activeChildClosers: make(map[uint64]func()),
	}
}

func (p *Page) ID() pagekit.TabID { return pagekit.TabSubscriptions }
func (p *Page) Title() string     { return "订阅" }

func (p *Page) throttleInterval() time.Duration {
	if p.RefreshThrottle > 0 {
		return p.RefreshThrottle
	}
	return defaultRefreshThrottle
}

// OnConnected resets transient flags and loads initial subscription data.
func (p *Page) OnConnected(h pagekit.Host) {
	p.sessionGen++
	p.initTriggered = true
	p.actionErr = ""
	p.actionInfo = ""
	p.subsErr = ""
	p.accountsErr = ""
	p.planErr = ""
	p.loadAllMain(h)
	if p.isDetailActive(h) {
		p.loadDetail(h, p.detailSubID, true)
	}
}

// OnDisconnected immediately clears all in-memory data, images, timers, and active child windows (FR-MOB-006 / NFR-MOB-DATA-001).
func (p *Page) OnDisconnected() {
	p.sessionGen++
	p.initTriggered = false
	p.cancelThrottleTimer()
	p.closeAllChildWindows()

	p.subs = nil
	p.subsLoaded = false
	p.subsLoading = false
	p.subsErr = ""
	p.subsRefresh.Refreshing = false

	p.accounts = nil
	p.accountsLoaded = false
	p.accountsLoading = false
	p.accountsErr = ""
	p.accountsRefresh.Refreshing = false

	p.plan = nil
	p.planStatus = ""
	p.planLoaded = false
	p.planLoading = false
	p.planErr = ""
	p.planRefresh.Refreshing = false

	p.checking = false
	p.checkMessage = ""
	p.checkingAccountKey = ""
	p.actionErr = ""
	p.actionInfo = ""
	p.busyLiveKey = ""

	p.detailSub = nil
	p.detailLoading = false
	p.detailErr = ""
	p.detailMenuOpen = false
	p.detailFeed = nil
	p.detailFeedNext = nil
	p.detailFeedHasMore = false
	p.detailFeedLoading = false
	p.detailFeedMoreing = false
	p.detailFeedErr = ""
	p.detailFeedRefresh.Refreshing = false

	p.clearBitmaps()
}

// OnEvent handles live WebSocket events from the desktop companion service.
func (p *Page) OnEvent(h pagekit.Host, ev companionclient.EventFrame) {
	if h == nil || h.ConnState() != pagekit.ConnConnected {
		return
	}
	switch ev.Type {
	case companionapi.EventSubsCheck:
		p.handleSubsCheckEvent(h, ev)
	case companionapi.EventSubsChanged, companionapi.EventLiveChanged:
		p.scheduleThrottledReload(h, true, true, false)
	case companionapi.EventFeedChanged:
		if p.isDetailActive(h) {
			p.scheduleThrottledReload(h, false, false, true)
		}
	case companionapi.EventResync:
		p.scheduleThrottledReload(h, true, true, p.isDetailActive(h))
	}
}

func (p *Page) handleSubsCheckEvent(h pagekit.Host, ev companionclient.EventFrame) {
	state := strings.ToLower(strings.TrimSpace(ev.State))
	msg := strings.TrimSpace(ev.Message)

	switch state {
	case companionapi.CodeAuthRequired:
		p.checking = false
		p.checkingAccountKey = ""
		p.checkMessage = ""
		p.actionErr = formatAuthRequiredMessage(ev.Site, msg)
		p.scheduleThrottledReload(h, false, true, false)
		return
	case companionapi.CodeRiskControl:
		p.checking = false
		p.checkingAccountKey = ""
		p.checkMessage = ""
		p.actionErr = firstNonEmpty(msg, "触发站点风控，检查更新已暂停")
		p.scheduleThrottledReload(h, false, true, false)
		return
	case companionapi.CodeRateLimited:
		p.checking = false
		p.checkingAccountKey = ""
		p.checkMessage = ""
		p.actionErr = firstNonEmpty(msg, "触发站点限流，请稍后再试")
		p.scheduleThrottledReload(h, false, true, false)
		return
	case "canceled", "cancelled":
		p.checking = false
		p.checkingAccountKey = ""
		p.checkMessage = ""
		p.actionInfo = "已取消检查更新"
		p.scheduleThrottledReload(h, false, true, false)
		return
	case "done", "completed", "finished", "idle":
		p.checking = false
		p.checkingAccountKey = ""
		p.checkMessage = ""
		if msg != "" {
			p.actionInfo = msg
		}
		p.scheduleThrottledReload(h, true, true, p.isDetailActive(h))
		return
	case "error", "failed":
		p.checking = false
		p.checkingAccountKey = ""
		p.checkMessage = ""
		p.actionErr = firstNonEmpty(msg, "检查更新失败，请在电脑端查看日志")
		p.scheduleThrottledReload(h, false, true, false)
		return
	}

	// Progress messages (e.g. "progress:1/5 ..." or "正在检查 1 / 5").
	cleaned := strings.TrimSpace(strings.TrimPrefix(msg, "progress:"))
	if strings.HasPrefix(msg, "progress:") || strings.Contains(cleaned, "正在检查") {
		p.checking = true
		p.checkMessage = firstNonEmpty(cleaned, "正在检查更新…")
		p.actionErr = ""
		return
	}
	if strings.Contains(cleaned, "已取消") {
		p.checking = false
		p.checkingAccountKey = ""
		p.checkMessage = ""
		p.actionInfo = cleaned
		p.scheduleThrottledReload(h, false, true, false)
		return
	}
	if strings.Contains(cleaned, "完成") || strings.Contains(cleaned, "结束") {
		p.checking = false
		p.checkingAccountKey = ""
		p.checkMessage = ""
		p.actionInfo = cleaned
		p.scheduleThrottledReload(h, true, true, p.isDetailActive(h))
		return
	}
	if cleaned != "" {
		p.checking = true
		p.checkMessage = cleaned
	}
	p.scheduleThrottledReload(h, true, true, false)
}

func (p *Page) scheduleThrottledReload(h pagekit.Host, wantSubs, wantPlan, wantFeed bool) {
	p.evMu.Lock()
	p.pendingSubs = p.pendingSubs || wantSubs
	p.pendingPlan = p.pendingPlan || wantPlan
	p.pendingFeed = p.pendingFeed || wantFeed

	interval := p.throttleInterval()
	now := time.Now()
	elapsed := now.Sub(p.lastEventLoad)
	if elapsed >= interval {
		s, pl, fd := p.pendingSubs, p.pendingPlan, p.pendingFeed
		p.pendingSubs, p.pendingPlan, p.pendingFeed = false, false, false
		p.lastEventLoad = now
		p.evMu.Unlock()
		p.flushEventReload(h, s, pl, fd)
		return
	}
	if p.throttleTimer == nil {
		wait := interval - elapsed
		gen := p.sessionGen
		p.throttleTimer = time.AfterFunc(wait, func() {
			p.evMu.Lock()
			p.throttleTimer = nil
			s, pl, fd := p.pendingSubs, p.pendingPlan, p.pendingFeed
			p.pendingSubs, p.pendingPlan, p.pendingFeed = false, false, false
			p.lastEventLoad = time.Now()
			p.evMu.Unlock()
			if !s && !pl && !fd {
				return
			}
			h.Update(func() {
				if p.sessionGen != gen || h.ConnState() != pagekit.ConnConnected {
					return
				}
				p.flushEventReload(h, s, pl, fd)
			})
		})
	}
	p.evMu.Unlock()
}

func (p *Page) isDetailActive(h pagekit.Host) bool {
	if p.detailSubID == "" || h == nil || h.Router() == nil {
		return false
	}
	return strings.HasPrefix(h.Router().Path(), "/sub/")
}

func (p *Page) flushEventReload(h pagekit.Host, wantSubs, wantPlan, wantFeed bool) {
	detailActive := p.isDetailActive(h)
	if wantSubs {
		p.loadSubscriptions(h)
		if p.activeSeg == segAllAccounts || p.accountsLoaded {
			p.loadAccounts(h)
		}
		if detailActive {
			p.loadDetailSub(h, p.detailSubID)
		}
	}
	if wantPlan {
		p.loadCheckPlan(h)
	}
	if wantFeed && detailActive {
		p.loadDetailFeed(h, p.detailSubID, nil, false)
	}
}

func (p *Page) cancelThrottleTimer() {
	p.evMu.Lock()
	if p.throttleTimer != nil {
		p.throttleTimer.Stop()
		p.throttleTimer = nil
	}
	p.pendingSubs = false
	p.pendingPlan = false
	p.pendingFeed = false
	p.evMu.Unlock()
}

// View renders the 订阅 tab with SystemStack routing.
func (p *Page) View(c *ui.Context, h pagekit.Host) {
	if !p.initTriggered && h.ConnState() == pagekit.ConnConnected && h.Client() != nil {
		p.initTriggered = true
		p.loadAllMain(h)
	}

	r := h.Router()
	r.SystemStack = true
	detailRendered := false
	ui.Column(c).Fill().Background(theme.ColorBgMain).Children(func() {
		r.View(c, func(route *ui.Route) {
			switch {
			case route.Match("/sub/{id}"):
				detailRendered = true
				rawID := route.Param("id")
				subID, err := url.PathUnescape(rawID)
				if err != nil || subID == "" {
					subID = rawID
				}
				if p.detailSubID != subID {
					p.enterDetail(h, subID)
				}
				title := "订阅详情"
				if p.detailSub != nil && p.detailSub.Name != "" {
					title = p.detailSub.Name
				}
				route.Title(title)
				p.renderDetailView(c, h, r, title)
			default:
				route.Title(p.Title())
				p.renderMainView(c, h, r)
			}
		}).Grow(1).Basis(0).FillWidth()
	})
	// Once back on the root route and any outgoing detail transition has settled,
	// release detail state so subsequent WS events never poll stale details.
	if !detailRendered && !strings.HasPrefix(r.Path(), "/sub/") && p.detailSubID != "" {
		p.clearDetailState()
	}
}

func (p *Page) clearDetailState() {
	p.detailSubID = ""
	p.detailSub = nil
	p.detailLoading = false
	p.detailErr = ""
	p.detailMenuOpen = false
	p.detailFeed = nil
	p.detailFeedNext = nil
	p.detailFeedHasMore = false
	p.detailFeedLoading = false
	p.detailFeedMoreing = false
	p.detailFeedErr = ""
	p.detailFeedRefresh.Refreshing = false
}

func (p *Page) loadAllMain(h pagekit.Host) {
	p.loadSubscriptions(h)
	p.loadAccounts(h)
	p.loadCheckPlan(h)
}

func (p *Page) loadSubscriptions(h pagekit.Host) {
	cli := h.Client()
	if cli == nil {
		return
	}
	gen := p.sessionGen
	p.subsLoading = true
	p.subsErr = ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var resp companionapi.SubscriptionsResponse
		err := cli.DoJSON(ctx, "GET", companionapi.PathSubscriptions, nil, &resp)
		h.Update(func() {
			if p.sessionGen != gen {
				return
			}
			p.subsLoading = false
			p.subsRefresh.Refreshing = false
			if err != nil {
				p.subsErr = formatActionError(err, "")
				return
			}
			p.subs = resp.Items
			p.subsLoaded = true
			p.subsErr = ""
			if p.detailSubID != "" {
				for i := range p.subs {
					if p.subs[i].ID == p.detailSubID {
						cp := p.subs[i]
						p.detailSub = &cp
						break
					}
				}
			}
		})
	}()
}

func (p *Page) loadAccounts(h pagekit.Host) {
	cli := h.Client()
	if cli == nil {
		return
	}
	gen := p.sessionGen
	p.accountsLoading = true
	p.accountsErr = ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var resp companionapi.AllAccountsResponse
		err := cli.DoJSON(ctx, "GET", companionapi.PathSubscriptionsAccounts, nil, &resp)
		h.Update(func() {
			if p.sessionGen != gen {
				return
			}
			p.accountsLoading = false
			p.accountsRefresh.Refreshing = false
			if err != nil {
				p.accountsErr = formatActionError(err, "")
				return
			}
			p.accounts = sortAccountsBySiteOrder(resp.Items)
			p.accountsLoaded = true
			p.accountsErr = ""
		})
	}()
}

func (p *Page) loadCheckPlan(h pagekit.Host) {
	cli := h.Client()
	if cli == nil {
		return
	}
	gen := p.sessionGen
	p.planLoading = true
	p.planErr = ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var resp companionapi.CheckPlanResponse
		err := cli.DoJSON(ctx, "GET", companionapi.PathChecksPlan, nil, &resp)
		h.Update(func() {
			if p.sessionGen != gen {
				return
			}
			p.planLoading = false
			p.planRefresh.Refreshing = false
			if err != nil {
				p.planErr = formatActionError(err, "")
				return
			}
			p.plan = resp.Items
			p.planStatus = strings.TrimSpace(resp.Status)
			p.planLoaded = true
			p.planErr = ""
			if resp.Checking {
				p.checking = true
				p.checkMessage = firstNonEmpty(strings.TrimSpace(resp.Status), p.checkMessage, "正在检查更新…")
			} else if !resp.Checking && p.checking && p.checkingAccountKey == "" {
				p.checking = false
				p.checkMessage = ""
			}
		})
	}()
}

func (p *Page) enterDetail(h pagekit.Host, subID string) {
	p.detailSubID = subID
	p.detailTab = detailTabContent
	p.detailMenuOpen = false
	p.detailSub = nil
	p.detailErr = ""
	p.detailFeed = nil
	p.detailFeedNext = nil
	p.detailFeedHasMore = false
	p.detailFeedErr = ""
	p.actionErr = ""
	p.actionInfo = ""

	for i := range p.subs {
		if p.subs[i].ID == subID {
			cp := p.subs[i]
			p.detailSub = &cp
			break
		}
	}
	p.loadDetail(h, subID, true)
}

func (p *Page) loadDetail(h pagekit.Host, subID string, reloadFeed bool) {
	p.loadDetailSub(h, subID)
	if reloadFeed {
		p.loadDetailFeed(h, subID, nil, false)
	}
}

func (p *Page) loadDetailSub(h pagekit.Host, subID string) {
	cli := h.Client()
	if cli == nil || subID == "" {
		return
	}
	gen := p.sessionGen
	p.detailLoading = true
	p.detailErr = ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var resp companionapi.SubscriptionDetailResponse
		err := cli.DoJSON(ctx, "GET", companionapi.SubscriptionPath(subID), nil, &resp)
		h.Update(func() {
			if p.sessionGen != gen || p.detailSubID != subID {
				return
			}
			p.detailLoading = false
			if err != nil {
				p.detailErr = formatActionError(err, "")
				return
			}
			sub := resp.Subscription
			sub.Accounts = sortAccountsBySitePreserveOrder(sub.Accounts)
			p.detailSub = &sub
			p.detailErr = ""
		})
	}()
}

func (p *Page) loadDetailFeed(h pagekit.Host, subID string, cursor *companionapi.FeedCursor, appendPage bool) {
	cli := h.Client()
	if cli == nil || subID == "" {
		return
	}
	gen := p.sessionGen
	if appendPage {
		if p.detailFeedMoreing || !p.detailFeedHasMore || cursor == nil {
			return
		}
		p.detailFeedMoreing = true
	} else {
		p.detailFeedLoading = true
		p.detailFeedErr = ""
	}
	q := url.Values{}
	q.Set("subId", subID)
	q.Set("limit", "30")
	q.Set("direction", "older")
	if cursor != nil && cursor.Key != "" {
		q.Set("cursorPubTs", fmt.Sprint(cursor.PubTs))
		q.Set("cursorKey", cursor.Key)
	}
	path := companionapi.PathFeed + "?" + q.Encode()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var resp companionapi.FeedPageResponse
		err := cli.DoJSON(ctx, "GET", path, nil, &resp)
		h.Update(func() {
			if p.sessionGen != gen || p.detailSubID != subID {
				return
			}
			p.detailFeedLoading = false
			p.detailFeedMoreing = false
			p.detailFeedRefresh.Refreshing = false
			if err != nil {
				p.detailFeedErr = formatActionError(err, "")
				return
			}
			if appendPage {
				seen := make(map[string]bool, len(p.detailFeed))
				for _, it := range p.detailFeed {
					seen[it.FeedKey] = true
				}
				for _, it := range resp.Items {
					if it.FeedKey == "" || !seen[it.FeedKey] {
						p.detailFeed = append(p.detailFeed, it)
						if it.FeedKey != "" {
							seen[it.FeedKey] = true
						}
					}
				}
			} else {
				p.detailFeed = resp.Items
			}
			p.detailFeedNext = resp.NextCursor
			p.detailFeedHasMore = resp.HasMore
			p.detailFeedErr = ""
		})
	}()
}

// Control commands (FR-MOB-021, FR-MOB-022).

func (p *Page) startCheckAll(h pagekit.Host) {
	cli := h.Client()
	if cli == nil || p.checking {
		return
	}
	gen := p.sessionGen
	p.checking = true
	p.checkingAccountKey = ""
	p.checkMessage = "正在检查全部订阅…"
	p.actionErr = ""
	p.actionInfo = ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var resp companionapi.ActionResponse
		err := cli.DoJSON(ctx, "POST", companionapi.PathChecks, companionapi.CheckRequest{}, &resp)
		h.Update(func() {
			if p.sessionGen != gen {
				return
			}
			if err != nil {
				p.checking = false
				p.checkMessage = ""
				p.actionErr = formatActionError(err, "")
				return
			}
			if resp.Message != "" {
				p.checkMessage = resp.Message
			}
		})
	}()
}

func (p *Page) startCheckSubscription(h pagekit.Host, sub companionapi.SubscriptionDTO) {
	cli := h.Client()
	if cli == nil || p.checking || sub.ID == "" {
		return
	}
	fallbackSite := ""
	if len(sub.Accounts) > 0 {
		fallbackSite = sub.Accounts[0].Site
	}
	gen := p.sessionGen
	p.checking = true
	p.checkingAccountKey = ""
	p.checkMessage = "正在检查「" + sub.Name + "」…"
	p.actionErr = ""
	p.actionInfo = ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var resp companionapi.ActionResponse
		err := cli.DoJSON(ctx, "POST", companionapi.PathChecks, companionapi.CheckRequest{SubIDs: []string{sub.ID}}, &resp)
		h.Update(func() {
			if p.sessionGen != gen {
				return
			}
			if err != nil {
				p.checking = false
				p.checkMessage = ""
				p.actionErr = formatActionError(err, fallbackSite)
				return
			}
			if resp.Message != "" {
				p.checkMessage = resp.Message
			}
		})
	}()
}

func (p *Page) startCheckAccount(h pagekit.Host, row companionapi.CheckPlanRowDTO) {
	cli := h.Client()
	if cli == nil || p.checking {
		return
	}
	acctKey := normalizeSite(row.Site) + ":" + strings.TrimSpace(row.UID)
	gen := p.sessionGen
	p.checking = true
	p.checkingAccountKey = acctKey
	p.checkMessage = fmt.Sprintf("正在检查 %s（%s）…", firstNonEmpty(row.Name, row.UID), siteShortLabel(row.Site))
	p.actionErr = ""
	p.actionInfo = ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var resp companionapi.ActionResponse
		req := companionapi.CheckAccountRequest{
			SubID: row.SubID,
			Site:  normalizeSite(row.Site),
			UID:   strings.TrimSpace(row.UID),
		}
		err := cli.DoJSON(ctx, "POST", companionapi.PathChecksAccount, req, &resp)
		h.Update(func() {
			if p.sessionGen != gen {
				return
			}
			if err != nil {
				p.checking = false
				p.checkingAccountKey = ""
				p.checkMessage = ""
				p.actionErr = formatActionError(err, row.Site)
				return
			}
			if resp.Message != "" {
				p.checkMessage = resp.Message
			}
		})
	}()
}

func (p *Page) cancelCheck(h pagekit.Host) {
	cli := h.Client()
	if cli == nil {
		return
	}
	gen := p.sessionGen
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var resp companionapi.ActionResponse
		err := cli.DoJSON(ctx, "POST", companionapi.PathChecksCancel, nil, &resp)
		h.Update(func() {
			if p.sessionGen != gen {
				return
			}
			if err != nil {
				p.actionErr = formatActionError(err, "")
				return
			}
			p.checking = false
			p.checkingAccountKey = ""
			p.checkMessage = ""
			p.actionInfo = firstNonEmpty(resp.Message, "已取消检查更新")
			p.loadCheckPlan(h)
		})
	}()
}

func (p *Page) startLiveRecording(h pagekit.Host, acc companionapi.SubscriptionAccountDTO) {
	if !isLiveRecordableSite(acc.Site) || !acc.LiveSupported {
		return
	}
	cli := h.Client()
	if cli == nil || p.busyLiveKey != "" {
		return
	}
	acctKey := firstNonEmpty(acc.AccountKey, normalizeSite(acc.Site)+":"+strings.TrimSpace(acc.UID))
	gen := p.sessionGen
	p.busyLiveKey = acctKey
	p.actionErr = ""
	p.actionInfo = ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		var rec companionapi.LiveRecordingDTO
		err := cli.DoJSON(ctx, "POST", companionapi.PathLiveStart, companionapi.StartLiveRequest{AccountKey: acctKey}, &rec)
		h.Update(func() {
			if p.sessionGen != gen {
				return
			}
			p.busyLiveKey = ""
			if err != nil {
				p.actionErr = formatActionError(err, acc.Site)
				return
			}
			p.actionInfo = "已开始录制：" + firstNonEmpty(acc.Name, acc.UID)
			p.applyLiveRecordingUpdate(acctKey, true, rec.RecordingKey)
			if p.detailSubID != "" {
				p.loadDetailSub(h, p.detailSubID)
			}
			p.loadSubscriptions(h)
		})
	}()
}

func (p *Page) stopLiveRecording(h pagekit.Host, acc companionapi.SubscriptionAccountDTO) {
	if !isLiveRecordableSite(acc.Site) || !acc.LiveSupported {
		return
	}
	cli := h.Client()
	if cli == nil || p.busyLiveKey != "" {
		return
	}
	acctKey := firstNonEmpty(acc.AccountKey, normalizeSite(acc.Site)+":"+strings.TrimSpace(acc.UID))
	recKey := firstNonEmpty(acc.RecordingKey, acctKey)
	gen := p.sessionGen
	p.busyLiveKey = acctKey
	p.actionErr = ""
	p.actionInfo = ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		var rec companionapi.LiveRecordingDTO
		err := cli.DoJSON(ctx, "POST", companionapi.LiveRecordingStopPath(recKey), nil, &rec)
		h.Update(func() {
			if p.sessionGen != gen {
				return
			}
			p.busyLiveKey = ""
			if err != nil {
				p.actionErr = formatActionError(err, acc.Site)
				return
			}
			p.actionInfo = "已停止录制：" + firstNonEmpty(acc.Name, acc.UID)
			p.applyLiveRecordingUpdate(acctKey, false, "")
			if p.detailSubID != "" {
				p.loadDetailSub(h, p.detailSubID)
			}
			p.loadSubscriptions(h)
		})
	}()
}

func (p *Page) applyLiveRecordingUpdate(acctKey string, recording bool, recordingKey string) {
	if p.detailSub != nil {
		anyRec := false
		for i := range p.detailSub.Accounts {
			a := &p.detailSub.Accounts[i]
			k := firstNonEmpty(a.AccountKey, normalizeSite(a.Site)+":"+strings.TrimSpace(a.UID))
			if k == acctKey {
				a.Recording = recording
				if recording && recordingKey != "" {
					a.RecordingKey = recordingKey
				} else if !recording {
					a.RecordingKey = ""
				}
			}
			if a.Recording {
				anyRec = true
			}
		}
		p.detailSub.Recording = anyRec
	}
}

// In-memory bitmap decoding helper (zero disk writes, bounded concurrency, failure cooldown, cleared on disconnect).

func (p *Page) lookupBitmap(h pagekit.Host, path string, maxSide int) *ui.Bitmap {
	path = strings.TrimSpace(path)
	if path == "" || h == nil {
		return nil
	}
	cacheKey := fmt.Sprintf("%s#%d", path, maxSide)
	now := time.Now()
	p.imgMu.Lock()
	if bmp, ok := p.bitmaps[cacheKey]; ok {
		p.imgMu.Unlock()
		return bmp
	}
	if p.imgInFlight[cacheKey] {
		p.imgMu.Unlock()
		return nil
	}
	if until, ok := p.imgFailUntil[cacheKey]; ok && now.Before(until) {
		p.imgMu.Unlock()
		return nil
	}
	if p.imgSem == nil {
		p.imgSem = make(chan struct{}, bitmapMaxConcurrency)
	}
	sem := p.imgSem
	p.imgInFlight[cacheKey] = true
	p.imgMu.Unlock()

	gen := p.sessionGen
	go func() {
		sem <- struct{}{}
		defer func() { <-sem }()

		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		raw, err := h.FetchImage(ctx, path)
		var bmp *ui.Bitmap
		if err == nil && len(raw) > 0 {
			if maxSide > 0 {
				bmp, _ = ui.DecodeBitmapScaled(raw, maxSide)
			} else {
				bmp, _ = ui.DecodeBitmap(raw)
			}
		}

		if bmp == nil {
			p.imgMu.Lock()
			delete(p.imgInFlight, cacheKey)
			if p.imgFailUntil == nil {
				p.imgFailUntil = make(map[string]time.Time)
			}
			p.imgFailUntil[cacheKey] = time.Now().Add(bitmapFailCooldown)
			p.imgMu.Unlock()
			return
		}

		h.Update(func() {
			p.imgMu.Lock()
			delete(p.imgInFlight, cacheKey)
			delete(p.imgFailUntil, cacheKey)
			if p.sessionGen == gen {
				if p.bitmaps == nil {
					p.bitmaps = make(map[string]*ui.Bitmap)
				}
				if _, exists := p.bitmaps[cacheKey]; !exists {
					p.imgOrder = append(p.imgOrder, cacheKey)
					if len(p.imgOrder) > maxInMemoryBitmaps {
						evict := p.imgOrder[0]
						p.imgOrder = p.imgOrder[1:]
						delete(p.bitmaps, evict)
					}
				}
				p.bitmaps[cacheKey] = bmp
			}
			p.imgMu.Unlock()
		})
	}()
	return nil
}

func (p *Page) clearBitmaps() {
	p.imgMu.Lock()
	clear(p.bitmaps)
	clear(p.imgInFlight)
	clear(p.imgFailUntil)
	p.imgOrder = nil
	p.imgMu.Unlock()
}

func (p *Page) registerChildWindow(cleanup func()) uint64 {
	if cleanup == nil {
		return 0
	}
	p.childMu.Lock()
	p.nextChildID++
	id := p.nextChildID
	if p.activeChildClosers == nil {
		p.activeChildClosers = make(map[uint64]func())
	}
	p.activeChildClosers[id] = cleanup
	p.childMu.Unlock()
	return id
}

func (p *Page) unregisterChildWindow(id uint64) {
	if id == 0 {
		return
	}
	p.childMu.Lock()
	delete(p.activeChildClosers, id)
	p.childMu.Unlock()
}

func (p *Page) closeAllChildWindows() {
	p.childMu.Lock()
	closers := make([]func(), 0, len(p.activeChildClosers))
	for _, fn := range p.activeChildClosers {
		closers = append(closers, fn)
	}
	clear(p.activeChildClosers)
	p.childMu.Unlock()
	for _, fn := range closers {
		fn()
	}
}

// Formatting and domain helpers (pure presentation, zero desktop/site package deps).

func normalizeSite(site string) string {
	return strings.ToLower(strings.TrimSpace(site))
}

func siteOrderRank(site string) int {
	switch normalizeSite(site) {
	case "bilibili":
		return 0
	case "douyin":
		return 1
	case "weibo":
		return 2
	case "x":
		return 3
	default:
		return 4
	}
}

func siteShortLabel(site string) string {
	switch normalizeSite(site) {
	case "bilibili":
		return "B 站"
	case "douyin":
		return "抖音"
	case "weibo":
		return "微博"
	case "x":
		return "X"
	default:
		return site
	}
}

func siteFullLabel(site string) string {
	switch normalizeSite(site) {
	case "bilibili":
		return "哔哩哔哩"
	case "douyin":
		return "抖音"
	case "weibo":
		return "微博"
	case "x":
		return "X"
	default:
		return site
	}
}

func isLiveRecordableSite(site string) bool {
	s := normalizeSite(site)
	return s == "bilibili" || s == "douyin"
}

// sortAccountsBySitePreserveOrder groups accounts by fixed site order (B站→抖音→微博→X)
// while preserving each site's original relative order (DEC-071, subscription detail 账号 tab).
func sortAccountsBySitePreserveOrder(in []companionapi.SubscriptionAccountDTO) []companionapi.SubscriptionAccountDTO {
	out := make([]companionapi.SubscriptionAccountDTO, len(in))
	copy(out, in)
	sort.SliceStable(out, func(i, j int) bool {
		return siteOrderRank(out[i].Site) < siteOrderRank(out[j].Site)
	})
	return out
}

func sortAccountsBySiteOrder(in []companionapi.SubscriptionAccountDTO) []companionapi.SubscriptionAccountDTO {
	out := make([]companionapi.SubscriptionAccountDTO, len(in))
	copy(out, in)
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := siteOrderRank(out[i].Site), siteOrderRank(out[j].Site)
		if ri != rj {
			return ri < rj
		}
		ni := firstNonEmpty(out[i].Name, out[i].UniqueID, out[i].UID)
		nj := firstNonEmpty(out[j].Name, out[j].UniqueID, out[j].UID)
		if ni != nj {
			return ni < nj
		}
		return out[i].UID < out[j].UID
	})
	return out
}

func formatAuthRequiredMessage(site, serverMsg string) string {
	if s := strings.TrimSpace(serverMsg); s != "" && strings.Contains(s, "请先在电脑上登录") {
		return s
	}
	if full := siteFullLabel(site); full != "" {
		return "请先在电脑上登录" + full
	}
	return "请先在电脑上登录对应站点"
}

func formatActionError(err error, fallbackSite string) string {
	if err == nil {
		return ""
	}
	var ce *companionclient.Error
	if errors.As(err, &ce) && ce != nil {
		code := strings.ToLower(strings.TrimSpace(ce.Code))
		switch code {
		case companionapi.CodeAuthRequired:
			return formatAuthRequiredMessage(fallbackSite, ce.Message)
		case companionapi.CodeRiskControl:
			return firstNonEmpty(ce.Message, "触发站点风控，请稍后再试")
		case companionapi.CodeRateLimited:
			return firstNonEmpty(ce.Message, "触发站点限流，请稍后再试")
		case companionapi.CodeUnsupported:
			return firstNonEmpty(ce.Message, "该操作暂不支持")
		case companionapi.CodeConflict:
			return firstNonEmpty(ce.Message, "当前状态冲突，请刷新后重试")
		case companionapi.CodeUnavailable, companionapi.CodeNotFound:
			return firstNonEmpty(ce.Message, "目标内容不存在或不可用")
		}
		if ce.Kind == companionclient.KindRateLimited {
			return firstNonEmpty(ce.Message, "触发站点限流，请稍后再试")
		}
		if ce.Message != "" {
			return ce.Message
		}
		return ce.UserMessage()
	}
	return err.Error()
}

func formatWorksText(acc companionapi.SubscriptionAccountDTO) string {
	noun := "作品"
	switch normalizeSite(acc.Site) {
	case "weibo":
		noun = "微博"
	case "x":
		noun = "帖子"
	}
	if acc.TotalWorks > 0 {
		return fmt.Sprintf("%s %d · 已缓存 %d", noun, acc.TotalWorks, acc.CachedWorks)
	}
	return fmt.Sprintf("已缓存 %d", acc.CachedWorks)
}

func formatAccountHandle(acc companionapi.SubscriptionAccountDTO) string {
	switch normalizeSite(acc.Site) {
	case "bilibili":
		if acc.UID != "" {
			return "UID " + acc.UID
		}
	case "douyin":
		if acc.UniqueID != "" {
			return "抖音号 " + acc.UniqueID
		}
		if acc.UID != "" {
			return "UID " + acc.UID
		}
	case "weibo":
		if acc.UID != "" {
			return "UID " + acc.UID
		}
	case "x":
		if acc.UniqueID != "" {
			if strings.HasPrefix(acc.UniqueID, "@") {
				return acc.UniqueID
			}
			return "@" + acc.UniqueID
		}
		if acc.UID != "" {
			return "ID " + acc.UID
		}
	}
	return acc.UID
}

func formatTimeShort(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return raw
	}
	lt := t.Local()
	now := time.Now()
	if lt.Year() == now.Year() && lt.YearDay() == now.YearDay() {
		return "今天 " + lt.Format("15:04")
	}
	return lt.Format("01-02 15:04")
}

func formatPubTs(ts int64) string {
	if ts <= 0 {
		return ""
	}
	lt := time.Unix(ts, 0).Local()
	now := time.Now()
	if lt.Year() == now.Year() && lt.YearDay() == now.YearDay() {
		return "今天 " + lt.Format("15:04")
	}
	if lt.Year() == now.Year() {
		return lt.Format("01-02 15:04")
	}
	return lt.Format("2006-01-02")
}

func formatDurationSec(sec int) string {
	if sec <= 0 {
		return ""
	}
	h := sec / 3600
	m := (sec % 3600) / 60
	s := sec % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}

var avatarPalette = []ui.Color{
	ui.Hex("#2563eb"), // blue
	ui.Hex("#0d9488"), // teal
	ui.Hex("#4f46e5"), // indigo
	ui.Hex("#d97706"), // amber
	ui.Hex("#16a34a"), // green
	ui.Hex("#0891b2"), // cyan
	ui.Hex("#7c3aed"), // purple
	ui.Hex("#475569"), // slate
}

func avatarColor(seed string) ui.Color {
	var h uint32 = 2166136261
	for i := 0; i < len(seed); i++ {
		h ^= uint32(seed[i])
		h *= 16777619
	}
	return avatarPalette[int(h)%len(avatarPalette)]
}

func firstGlyph(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "?"
	}
	for _, r := range s {
		return strings.ToUpper(string(r))
	}
	return "?"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
