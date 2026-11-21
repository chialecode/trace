package uistate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/storage"
)

// FeedAnchor stores the user's last viewed position and sort order in the home feed.
type FeedAnchor struct {
	Order    string `json:"order"`
	Key      string `json:"key"`
	PubTs    int64  `json:"pubTs"`
	OffsetPx int    `json:"offsetPx"`
}

// HomePosition is where the main timeline of one site filter of Home was
// read: the post at the top of the view and how far its top is above the
// view's top (DEC-053 ①).
type HomePosition struct {
	Key      string `json:"key"`
	PubTs    int64  `json:"pubTs"`
	OffsetPx int    `json:"offsetPx"`
}

// HomePositionSites are the site filters whose positions are kept.
var HomePositionSites = []string{"all", "bilibili", "douyin", "weibo"}

// State represents the persistent UI state stored in ui-state.json.
type State struct {
	Version      int              `json:"version"`
	Window       WindowState      `json:"window"`
	LastRoute    string           `json:"lastRoute"`
	SubDetailTab string           `json:"subDetailTab,omitempty"`
	FeedFilter   FeedFilter       `json:"feedFilter"`
	Anchor       FeedAnchor       `json:"anchor"`
	HomeSeen     map[string]int64 `json:"homeSeen,omitempty"`
	// HomePositions keeps, per site filter ("all", "bilibili", "douyin", "weibo"),
	// where the main timeline was read; optional, schema version 2+.
	HomePositions map[string]HomePosition `json:"homePositions,omitempty"`
	// DismissedLiveSessions are finished recordings removed from Tasks ›
	// 直播录制 "最近结束的录制" (session key → RFC3339 time removed); optional,
	// entries past that list's 7-day window are dropped.
	DismissedLiveSessions map[string]string `json:"dismissedLiveSessions,omitempty"`
	TrayNotified          bool              `json:"trayNotified"`
	Player                PlayerState       `json:"player"`
	// Update records what the updates of TRACE need across launches.
	Update UpdateState `json:"update"`
}

// UpdateState is what the updates of TRACE keep across launches
// (FR-UPD-001, FR-UPD-002).
type UpdateState struct {
	// LastRunVersion is the version that ran last; a launch of another
	// version says once that TRACE was updated.
	LastRunVersion string `json:"lastRunVersion,omitempty"`
	// LastCheck is when the release channel last checked for updates.
	LastCheck string `json:"lastCheck,omitempty"`
	// ReadyVersion and ReadyNotes are the downloaded release, whose notes
	// the first launch of it shows.
	ReadyVersion string `json:"readyVersion,omitempty"`
	ReadyNotes   string `json:"readyNotes,omitempty"`
	// Restart is set when TRACE quit to be updated: by tools/package for
	// the development channel (--quit-for-update) or by "重启以更新".
	Restart *UpdateRestart `json:"restart,omitempty"`
}

// UpdateRestart records a quit for an update.
type UpdateRestart struct {
	Reason      string `json:"reason"` // "dev-install" or "update"
	WindowOpen  bool   `json:"windowOpen"`
	FromVersion string `json:"fromVersion,omitempty"`
	At          string `json:"at"`
}

type WindowState struct {
	Width     int  `json:"width"`
	Height    int  `json:"height"`
	X         int  `json:"x"`
	Y         int  `json:"y"`
	Maximized bool `json:"maximized"`
}

type FeedFilter struct {
	Kind string `json:"kind"`
	Site string `json:"site"`
}

type PlayerState struct {
	Volume         float64 `json:"volume"`
	Muted          bool    `json:"muted"`
	DanmakuVisible bool    `json:"danmakuVisible"`
	DanmakuOpacity float64 `json:"danmakuOpacity"`
	DanmakuArea    float64 `json:"danmakuArea"`
	DanmakuDensity string  `json:"danmakuDensity"`
	SubtitleLang   string  `json:"subtitleLang"`
}

// CurrentSchemaVersion is the schema version written to ui-state.json.
const CurrentSchemaVersion = 3

// NormalizeSubDetailTab normalizes subscription detail tab values (DEC-051 ⑦).
// Legacy "live" maps to "accounts"; empty or unknown values default to "content".
func NormalizeSubDetailTab(tab string) string {
	switch strings.ToLower(strings.TrimSpace(tab)) {
	case "accounts", "live":
		return "accounts"
	default:
		return "content"
	}
}

func extractRouteSubDetailTab(rawRoute string) string {
	r := strings.TrimSpace(rawRoute)
	if !strings.HasPrefix(r, "/subscriptions") {
		return ""
	}
	if qIdx := strings.IndexByte(r, '?'); qIdx >= 0 {
		query := r[qIdx+1:]
		for _, part := range strings.Split(query, "&") {
			if k, v, ok := strings.Cut(part, "="); ok && strings.EqualFold(strings.TrimSpace(k), "tab") {
				return strings.TrimSpace(v)
			}
		}
		r = r[:qIdx]
	}
	trimmed := strings.TrimSuffix(strings.TrimPrefix(r, "/subscriptions"), "/")
	if strings.HasPrefix(trimmed, "/") {
		parts := strings.Split(strings.TrimPrefix(trimmed, "/"), "/")
		if len(parts) >= 2 {
			switch strings.ToLower(strings.TrimSpace(parts[len(parts)-1])) {
			case "content", "accounts", "live":
				return parts[len(parts)-1]
			}
		}
	}
	return ""
}

// Default returns the default UI state.
func Default() State {
	return State{
		Version: CurrentSchemaVersion,
		Window: WindowState{
			Width:     1280,
			Height:    820,
			X:         0,
			Y:         0,
			Maximized: false,
		},
		LastRoute:    "/home",
		SubDetailTab: "content",
		FeedFilter: FeedFilter{
			Kind: "all",
			Site: "all",
		},
		Anchor: FeedAnchor{
			Order: "newest",
		},
		Player: PlayerState{
			Volume:         1.0,
			Muted:          false,
			DanmakuVisible: true,
			DanmakuOpacity: 0.85,
			DanmakuArea:    0.5,
			DanmakuDensity: "medium",
			SubtitleLang:   "",
		},
	}
}

// Normalize clamps UI state fields to valid ranges.
func Normalize(st State) State {
	if st.Version < CurrentSchemaVersion {
		st.Version = CurrentSchemaVersion
	}
	if st.Window.Width < 720 {
		st.Window.Width = 1280
	}
	if st.Window.Height < 520 {
		st.Window.Height = 820
	}
	if strings.TrimSpace(st.LastRoute) == "" {
		st.LastRoute = "/home"
	}
	if strings.TrimSpace(st.SubDetailTab) == "" {
		if routeTab := extractRouteSubDetailTab(st.LastRoute); routeTab != "" {
			st.SubDetailTab = routeTab
		}
	}
	st.SubDetailTab = NormalizeSubDetailTab(st.SubDetailTab)
	switch st.FeedFilter.Kind {
	case "all", "video", "dynamic":
	default:
		st.FeedFilter.Kind = "all"
	}
	switch strings.ToLower(strings.TrimSpace(st.FeedFilter.Site)) {
	case "all", "bilibili", "douyin", "weibo":
		st.FeedFilter.Site = strings.ToLower(strings.TrimSpace(st.FeedFilter.Site))
	default:
		st.FeedFilter.Site = "all"
	}
	// DEC-037: Home feed is always newest-first; legacy "oldest" is normalized to "newest" while preserving Anchor Key/PubTs/OffsetPx.
	st.Anchor.Order = "newest"
	if len(st.HomeSeen) > 0 {
		cleanSeen := make(map[string]int64, len(st.HomeSeen))
		for k, v := range st.HomeSeen {
			k = strings.TrimSpace(k)
			if k != "" && v > 0 {
				cleanSeen[k] = v
			}
		}
		if len(cleanSeen) > 0 {
			st.HomeSeen = cleanSeen
		} else {
			st.HomeSeen = nil
		}
	} else {
		st.HomeSeen = nil
	}
	st.HomePositions = normalizeHomePositions(st.HomePositions)
	st.DismissedLiveSessions = normalizeDismissedLiveSessions(st.DismissedLiveSessions, time.Now())
	if st.Player.Volume < 0 {
		st.Player.Volume = 0
	} else if st.Player.Volume > 1 {
		st.Player.Volume = 1
	}
	if st.Player.DanmakuOpacity <= 0 || st.Player.DanmakuOpacity > 1 {
		st.Player.DanmakuOpacity = 0.85
	}
	if st.Player.DanmakuArea <= 0 || st.Player.DanmakuArea > 1 {
		st.Player.DanmakuArea = 0.5
	}
	switch st.Player.DanmakuDensity {
	case "low", "medium", "high", "unlimited":
	default:
		st.Player.DanmakuDensity = "medium"
	}
	return st
}

// DismissedLiveSessionTTL is how long a removed "最近结束的录制" entry is
// remembered: the list itself only shows the last 7 days.
const DismissedLiveSessionTTL = 8 * 24 * time.Hour

func normalizeDismissedLiveSessions(in map[string]string, now time.Time) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		k = strings.TrimSpace(k)
		t, err := time.Parse(time.RFC3339, strings.TrimSpace(v))
		if k == "" || err != nil || now.Sub(t) > DismissedLiveSessionTTL {
			continue
		}
		out[k] = t.UTC().Format(time.RFC3339)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeHomePositions(in map[string]HomePosition) map[string]HomePosition {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]HomePosition, len(in))
	for k, v := range in {
		k = strings.ToLower(strings.TrimSpace(k))
		v.Key = strings.TrimSpace(v.Key)
		valid := false
		for _, site := range HomePositionSites {
			valid = valid || k == site
		}
		if !valid || (v.Key == "" && v.PubTs <= 0) {
			continue
		}
		if v.PubTs < 0 {
			v.PubTs = 0
		}
		if v.OffsetPx < 0 {
			v.OffsetPx = 0
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Store manages reading and writing ui-state.json safely.
type Store struct {
	mu      sync.RWMutex
	path    string
	current State
}

// Open loads ui-state.json from path. Corrupt files are backed up and reset to Default().
func Open(path string) (*Store, bool, string, error) {
	def := Default()
	s := &Store{
		path:    path,
		current: def,
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			if wErr := storage.WriteJSONAtomic(path, def, 0o600); wErr != nil {
				return nil, false, "", wErr
			}
			return s, false, "", nil
		}
		return nil, false, "", err
	}

	loaded := Default()
	loaded.Version = 0
	loaded.SubDetailTab = ""
	loaded.FeedFilter.Site = ""
	if err := json.Unmarshal(raw, &loaded); err != nil {
		backup, bErr := storage.BackupCorruptFile(path)
		if bErr != nil {
			return nil, false, "", bErr
		}
		if wErr := storage.WriteJSONAtomic(path, def, 0o600); wErr != nil {
			return nil, true, backup, wErr
		}
		return s, true, backup, nil
	}
	if loaded.Version > CurrentSchemaVersion {
		return nil, false, "", &storage.NewerDataError{
			File:      "ui-state.json",
			Found:     loaded.Version,
			Supported: CurrentSchemaVersion,
		}
	}
	if loaded.Version < CurrentSchemaVersion {
		oldVersion := loaded.Version
		if oldVersion <= 0 {
			oldVersion = 1
		}
		if loaded.Version < 2 {
			site := strings.ToLower(strings.TrimSpace(loaded.FeedFilter.Site))
			if site == "" || site == "bilibili" {
				loaded.FeedFilter.Site = "all"
			}
		}
		loaded.Version = CurrentSchemaVersion
		norm := Normalize(loaded)
		backupPath := fmt.Sprintf("%s.premigrate-%d", path, oldVersion)
		if info, statErr := os.Stat(backupPath); errors.Is(statErr, os.ErrNotExist) || statErr != nil || info.IsDir() {
			if _, bErr := storage.BackupBeforeMigration(path, oldVersion); bErr != nil {
				return nil, false, "", bErr
			}
		}
		if err := storage.WriteMigratedJSONAtomic(path, norm, 0o600); err != nil {
			return nil, false, "", err
		}
		s.current = norm
		return s, false, "", nil
	}

	s.current = Normalize(loaded)
	return s, false, "", nil
}

// Get returns the current UI state.
func (s *Store) Get() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := s.current
	return cloneState(out)
}

func cloneState(st State) State {
	out := st
	if len(st.HomeSeen) > 0 {
		out.HomeSeen = make(map[string]int64, len(st.HomeSeen))
		for k, v := range st.HomeSeen {
			out.HomeSeen[k] = v
		}
	}
	if len(st.HomePositions) > 0 {
		out.HomePositions = make(map[string]HomePosition, len(st.HomePositions))
		for k, v := range st.HomePositions {
			out.HomePositions[k] = v
		}
	}
	if len(st.DismissedLiveSessions) > 0 {
		out.DismissedLiveSessions = make(map[string]string, len(st.DismissedLiveSessions))
		for k, v := range st.DismissedLiveSessions {
			out.DismissedLiveSessions[k] = v
		}
	}
	if st.Update.Restart != nil {
		r := *st.Update.Restart
		out.Update.Restart = &r
	}
	return out
}

// Mutate applies fn to the current state and saves the result atomically,
// so that writers of different fields do not overwrite each other.
func (s *Store) Mutate(fn func(*State)) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := cloneState(s.current)
	fn(&next)
	norm := cloneState(Normalize(next))
	if err := storage.WriteJSONAtomic(s.path, norm, 0o600); err != nil {
		return State{}, err
	}
	s.current = norm
	return cloneState(s.current), nil
}

// Update saves the updated UI state atomically.
func (s *Store) Update(next State) (State, error) {
	norm := cloneState(Normalize(next))
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := storage.WriteJSONAtomic(s.path, norm, 0o600); err != nil {
		return State{}, err
	}
	s.current = norm
	return cloneState(s.current), nil
}
