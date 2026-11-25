package live

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/site"
)

var slowLiveIOThreshold = 500 * time.Millisecond

// SetLogFunc installs an optional sanitized diagnostic logger for live recording events.
// Callers wire this to the runtime log writer; the callback is always invoked outside
// Manager and session locks.
func (m *Manager) SetLogFunc(fn func(level, msg string)) {
	m.mu.Lock()
	m.logFunc = fn
	m.mu.Unlock()
}

func (m *Manager) logEvent(level, format string, args ...any) {
	m.mu.Lock()
	fn := m.logFunc
	m.mu.Unlock()
	if fn == nil {
		return
	}
	fn(level, fmt.Sprintf(format, args...))
}

func liveLogHost(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "-"
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "-"
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	if host == "" {
		return "-"
	}
	return host
}

func liveLogSite(sess *recordSession) string {
	if sess == nil {
		return string(site.Bilibili)
	}
	return firstNonEmpty(sess.siteID, string(site.Bilibili))
}

func liveLogRoom(sess *recordSession) string {
	if sess == nil {
		return "-"
	}
	if strings.TrimSpace(sess.roomIDStr) != "" {
		return strings.TrimSpace(sess.roomIDStr)
	}
	if sess.roomID > 0 {
		return strconv.FormatInt(sess.roomID, 10)
	}
	return "-"
}
