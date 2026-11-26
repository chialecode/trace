// Package pagekit defines the small documented contract between the mobile app
// shell (cmd/trace-mobile/internal/app) and the five bottom-tab page packages
// under cmd/trace-mobile/internal/pages/.
//
// Later workers implementing 主页, 媒体库, 订阅, and 任务 pages only edit their
// own package under cmd/trace-mobile/internal/pages/<tab> and consume Host.
package pagekit

import (
	"context"

	"github.com/chialecode/trace/internal/companionclient"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// TabID identifies one of the five bottom tabs (FR-MOB-010).
type TabID int

const (
	TabHome TabID = iota
	TabLibrary
	TabSubscriptions
	TabTasks
	TabComputer
)

// ConnState describes the current foreground connection state.
type ConnState int

const (
	ConnUnpaired ConnState = iota
	ConnConnecting
	ConnError
	ConnConnected
)

// SiteAuthStatus summarizes one site's login state on the paired desktop.
type SiteAuthStatus struct {
	Site     string
	Label    string // "B 站", "抖音", "微博", "X"
	State    string // "logged_in", "logged_out", "expired", "unknown"
	Nickname string
}

// DesktopSnapshot holds the desktop metadata shown by 我的电脑 and tab headers.
type DesktopSnapshot struct {
	NodeID       string
	ComputerName string
	Version      string
	Channel      string // "stable" or "dev"
	ChannelLabel string // "正式版" or "开发版"
	StartedAt    string
	UptimeText   string
	CurrentAddr  string
	DiskFree     uint64
	DiskTotal    uint64
	LoggedIn     bool
	Sites        []SiteAuthStatus
}

// Host is the documented interface exposed by the mobile app shell to every
// tab page package. Page implementations must not import app or desktop packages.
type Host interface {
	// ConnState returns the current connection state (ConnConnected when ready).
	ConnState() ConnState

	// Desktop returns the latest paired computer status snapshot.
	Desktop() DesktopSnapshot

	// Client returns the active pinned-TLS companion client, or nil if not connected.
	Client() *companionclient.Client

	// MediaProxy returns the loopback media proxy for video playback, or nil when disconnected.
	MediaProxy() *companionclient.MediaProxy

	// ImageCache returns the 128 MiB in-memory LRU byte cache (cleared on disconnect).
	ImageCache() *companionclient.MemoryImageCache

	// FetchImage fetches an authenticated image path from the desktop (e.g.
	// "/api/v1/media/{key}/{rel}?w=480") via the in-memory LRU cache.
	FetchImage(ctx context.Context, path string) ([]byte, error)

	// Window returns the host mygo.Window for this tab (nil inside ui.Tester).
	Window() *mygo.Window

	// Router returns this tab's ui.Router (configured with SystemStack = true).
	Router() *ui.Router

	// PushRoute navigates inside the tab's router stack.
	PushRoute(path string)

	// Back navigates back inside the tab's router stack.
	Back()

	// PushWindow pushes a full-screen native child window onto this tab's stack
	// (e.g. full-screen image viewer or video player).
	PushWindow(title string, view func(c *ui.Context)) *mygo.Window

	// Update schedules fn on the UI main thread and triggers a re-render.
	Update(fn func())

	// SetTasksBadge updates the badge count on the 任务 bottom tab.
	SetTasksBadge(count int)

	// SelectTab switches the active bottom tab.
	SelectTab(tab TabID)

	// OpenExternal opens an http(s) URL in the phone's system browser.
	OpenExternal(rawURL string) error

	// RefreshStatus triggers a status refresh from the desktop.
	RefreshStatus()

	// Unpair calls DELETE /api/v1/devices/self (best-effort), clears local
	// pairing record and secret token, and returns to the pairing guide.
	Unpair()
}

// TabPage is implemented by each of the five bottom-tab packages.
type TabPage interface {
	ID() TabID
	Title() string
	View(c *ui.Context, h Host)
	OnConnected(h Host)
	OnDisconnected()
	OnEvent(h Host, ev companionclient.EventFrame)
}
