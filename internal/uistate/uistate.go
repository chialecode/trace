package uistate

import (
	"encoding/json"
	"os"
	"strings"
	"sync"

	"github.com/chialecode/trace/internal/storage"
)

// FeedAnchor stores the user's last viewed position and sort order in the home feed.
type FeedAnchor struct {
	Order    string `json:"order"`
	Key      string `json:"key"`
	PubTs    int64  `json:"pubTs"`
	OffsetPx int    `json:"offsetPx"`
}

// State represents the persistent UI state stored in ui-state.json.
type State struct {
	Version      int              `json:"version"`
	Window       WindowState      `json:"window"`
	LastRoute    string           `json:"lastRoute"`
	FeedFilter   FeedFilter       `json:"feedFilter"`
	Anchor       FeedAnchor       `json:"anchor"`
	HomeSeen     map[string]int64 `json:"homeSeen,omitempty"`
	TrayNotified bool             `json:"trayNotified"`
	Player       PlayerState      `json:"player"`
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

// Default returns the default UI state.
func Default() State {
	return State{
		Version: 1,
		Window: WindowState{
			Width:     1280,
			Height:    820,
			X:         0,
			Y:         0,
			Maximized: false,
		},
		LastRoute: "/home",
		FeedFilter: FeedFilter{
			Kind: "all",
			Site: "bilibili",
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
	if st.Version <= 0 {
		st.Version = 1
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
	switch st.FeedFilter.Kind {
	case "all", "video", "dynamic":
	default:
		st.FeedFilter.Kind = "all"
	}
	if strings.TrimSpace(st.FeedFilter.Site) == "" {
		st.FeedFilter.Site = "bilibili"
	}
	// DEC-037 / R10: Home feed is always newest-first; legacy "oldest" is normalized to "newest" while preserving Anchor Key/PubTs/OffsetPx.
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
	if err := json.Unmarshal(raw, &loaded); err != nil {
		backup, bErr := storage.BackupCorruptFile(path)
		if bErr != nil {
			return nil, false, "", bErr
		}
		_ = storage.WriteJSONAtomic(path, def, 0o600)
		return s, true, backup, nil
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
	return out
}

// Update saves the updated UI state atomically.
func (s *Store) Update(next State) (State, error) {
	norm := Normalize(next)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := storage.WriteJSONAtomic(s.path, norm, 0o600); err != nil {
		return State{}, err
	}
	s.current = norm
	return cloneState(s.current), nil
}
