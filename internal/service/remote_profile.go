package service

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/settings"
	"github.com/chialecode/trace/internal/site"
)

// Profile selects what a Backend runs: the desktop app or the headless
// remote trace-server (docs/design/remote.md §2).
type Profile string

const (
	// ProfileDesktop is the Windows desktop app (the default).
	ProfileDesktop Profile = ""
	// ProfileRemote is trace-server: it checks the synced subscriptions,
	// caches their new content by rule and records lives. It never renews
	// Cookies, never runs 全部缓存 and has no manual caching (remote.md §4).
	ProfileRemote Profile = "remote"
)

// CredentialSourceRemotePush marks a credential the desktop pushed to the
// remote; the remote never renews it (DEC-062 ③).
const CredentialSourceRemotePush = "remote_push"

// ErrRemoteUnsupported is returned by the operations the remote does not
// offer: manual caching, 全部缓存 and Cookie renewal.
var ErrRemoteUnsupported = &site.Error{Kind: site.ErrKindBadRequest, Message: "远程版只缓存订阅的新内容与录制，不提供这个操作"}

const maxRecentErrors = 20

// RecentError is one of the last errors written to the run log, already
// sanitized like the log line (remote.md §7).
type RecentError struct {
	At      string `json:"at"`
	Message string `json:"message"`
}

// IsRemote reports whether the backend runs as trace-server.
func (b *Backend) IsRemote() bool { return b.profile == ProfileRemote }

// StartedAt is when the backend started.
func (b *Backend) StartedAt() time.Time { return b.startedAt }

// RecentErrors returns the last errors of the run log, oldest first.
func (b *Backend) RecentErrors() []RecentError {
	b.logMu.Lock()
	defer b.logMu.Unlock()
	return append([]RecentError(nil), b.recentErrors...)
}

// SetCacheGate installs the check the remote runs before automatic caching
// starts a task (the export folder's free space, remote.md §6.2).
func (b *Backend) SetCacheGate(fn func() error) {
	b.settingsMu.Lock()
	b.cacheGate = fn
	b.settingsMu.Unlock()
}

func (b *Backend) remoteCacheGate() error {
	b.settingsMu.Lock()
	gate := b.cacheGate
	b.settingsMu.Unlock()
	if gate == nil {
		return nil
	}
	return gate()
}

// requireDesktop rejects the operations the remote does not offer.
func (b *Backend) requireDesktop() error {
	if b.profile == ProfileRemote {
		return ErrRemoteUnsupported
	}
	return nil
}

// remoteSettings forces what the remote keeps fixed: the save location is
// the export folder, and scheduled checking is always on (each
// subscription's rule decides whether it is checked, remote.md §4).
func remoteSettings(s settings.Settings, exportDir string) (settings.Settings, bool) {
	exportDir = filepath.Clean(exportDir)
	changed := false
	if filepath.Clean(s.CacheDir) != exportDir || filepath.Clean(s.CacheRoot) != exportDir {
		s.CacheDir, s.CacheRoot = exportDir, exportDir
		changed = true
	}
	if !s.Subscription.Enabled {
		s.Subscription.Enabled = true
		changed = true
	}
	if !s.ScheduledCheck {
		s.ScheduledCheck = true
		changed = true
	}
	return s, changed
}

func nodeName(name string) string {
	if n := strings.TrimSpace(name); n != "" {
		return n
	}
	if h, err := os.Hostname(); err == nil {
		return strings.TrimSpace(h)
	}
	return ""
}

// LibraryDB is library.db; trace-server keeps its upload records there.
func (b *Backend) LibraryDB() *sql.DB { return b.db }

// ItemBusy reports whether an item is still being written: a cache task for
// it is queued or running, or a recording writes into its folder. The
// uploader leaves such items alone (remote.md §6.2).
func (b *Backend) ItemBusy(itemKey string) bool {
	if b.dl != nil {
		if tasks, err := b.dl.ListTasks(); err == nil {
			for _, t := range tasks {
				if t.ItemKey == itemKey && (t.Status == "running" || t.Status == "queued") {
					return true
				}
			}
		}
	}
	if b.live != nil {
		meta, ok, err := b.lib.GetItem(itemKey)
		if err == nil && ok && meta.Kind == "live" {
			for _, r := range b.live.ListRooms() {
				if r.Recording && r.RecordItemDir != "" && strings.EqualFold(filepath.Clean(r.RecordItemDir), filepath.Clean(meta.ItemDir)) {
					return true
				}
			}
		}
	}
	return false
}

// RemoteLocations are the directories of trace-server: everything under
// the data directory given on the command line, the export folder as the
// save location (remote.md §2).
func RemoteLocations(dataDir, exportDir string) appdirs.Locations {
	return appdirs.Locations{
		Mode:            "remote",
		Channel:         appdirs.EffectiveChannel(""),
		AppName:         "trace-server",
		DisplayName:     "trace-server",
		Identifier:      "io.github.chialecode.trace-server",
		InstallDir:      dataDir,
		DataDir:         dataDir,
		DefaultCacheDir: exportDir,
		CredentialsDir:  filepath.Join(dataDir, "credentials"),
		LogsDir:         filepath.Join(dataDir, "logs"),
		TempDir:         filepath.Join(dataDir, "tmp"),
		LockFile:        filepath.Join(dataDir, "app.lock"),
	}
}
