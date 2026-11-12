package settings

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/chialecode/trace/internal/storage"
)

// PlayerSettings stores danmaku and subtitle playback preferences.
type PlayerSettings struct {
	DanmakuOn      bool   `json:"danmakuOn"`
	DanmakuOpacity int    `json:"danmakuOpacity"`
	DanmakuDensity string `json:"danmakuDensity"`
	SubtitleLang   string `json:"subtitleLang"`
}

// Settings represents the persistent user configuration stored in settings.json.
type Settings struct {
	Version              int                  `json:"version"`
	CacheRoot            string               `json:"cacheRoot"`
	CacheDir             string               `json:"cacheDir"`
	VideoQuality         string               `json:"videoQuality"`
	CodecOrder           []string             `json:"codecOrder"`
	AudioQuality         string               `json:"audioQuality"`
	MultiPart            string               `json:"multiPart"`
	Concurrency          int                  `json:"concurrency"`
	UpgradeCheckOnOpen   bool                 `json:"upgradeCheckOnOpen"`
	Danmaku              bool                 `json:"danmaku"`
	Subtitles            bool                 `json:"subtitles"`
	AISubtitles          bool                 `json:"aiSubtitles"`
	ScheduledCheck       bool                 `json:"scheduledCheck"`
	CheckIntervalMin     int                  `json:"checkIntervalMin"`
	LiveCheckIntervalMin int                  `json:"liveCheckIntervalMin"`
	MaxRecordings        int                  `json:"maxRecordings"`
	Player               PlayerSettings       `json:"player"`
	CloseAction          string               `json:"closeAction"`
	Download             DownloadSettings     `json:"download"`
	Subscription         SubscriptionSettings `json:"subscription"`
	Live                 LiveSettings         `json:"live"`
	Window               WindowSettings       `json:"window"`
	Diagnostics          DiagnosticsSettings  `json:"diagnostics"`
	Updates              UpdateSettings       `json:"updates"`
	Network              NetworkSettings      `json:"network"`
}

// UpdateSettings are the update preferences of the release channel
// (FR-UPD-002): both on by default, a file without them keeps the defaults.
// NetworkSettings chooses the proxy of every site request (DEC-046 ⑤):
// "system" (default: the system's settings and HTTP(S)_PROXY), "none" or
// "custom" with ProxyURL (http://, https:// or socks5://).
type NetworkSettings struct {
	ProxyMode string `json:"proxyMode"`
	ProxyURL  string `json:"proxyUrl,omitempty"`
}

type UpdateSettings struct {
	AutoCheck    bool `json:"autoCheck"`
	AutoDownload bool `json:"autoDownload"`
}

type DownloadSettings struct {
	VideoQuality        string   `json:"videoQuality,omitempty"`
	CodecOrder          []string `json:"codecOrder,omitempty"`
	MultiPart           string   `json:"multiPart,omitempty"`
	IncludeAI           bool     `json:"includeAI"`
	MaxConcurrentTasks  int      `json:"maxConcurrentTasks"`
	PreferredVideoCodec string   `json:"preferredVideoCodec"`
	PreferLosslessAudio bool     `json:"preferLosslessAudio"`
	PreferDolbyAudio    bool     `json:"preferDolbyAudio"`
	SaveDanmaku         bool     `json:"saveDanmaku"`
	SaveSubtitles       bool     `json:"saveSubtitles"`
	SaveCover           bool     `json:"saveCover"`
}

type SubscriptionSettings struct {
	Enabled         bool   `json:"enabled"`
	Mode            string `json:"mode"`
	IntervalMinutes int    `json:"intervalMinutes"`
	DailyTime       string `json:"dailyTime"`
	WeeklyDays      []int  `json:"weeklyDays"`
	WeeklyTime      string `json:"weeklyTime"`
	CheckOnStartup  bool   `json:"checkOnStartup"`
	AutoCacheScope  string `json:"autoCacheScope"`
}

type LiveSettings struct {
	PollEnabled             bool     `json:"pollEnabled"`
	PollIntervalSeconds     int      `json:"pollIntervalSeconds"`
	NotifyOnStart           bool     `json:"notifyOnStart"`
	AutoRecordDefault       bool     `json:"autoRecordDefault"`
	MaxConcurrentRecordings int      `json:"maxConcurrentRecordings"`
	CodecOrder              []string `json:"codecOrder"`
}

type WindowSettings struct {
	CloseAction    string `json:"closeAction"`
	StartMinimized bool   `json:"startMinimized"`
}

type DiagnosticsSettings struct {
	DebugLog bool `json:"debugLog"`
}

// Default returns the default Settings populated with defaultCacheDir.
func Default(defaultCacheDir string) Settings {
	cleanCache := filepath.Clean(defaultCacheDir)
	return Settings{
		Version:              1,
		CacheRoot:            "",
		CacheDir:             cleanCache,
		VideoQuality:         "best",
		CodecOrder:           []string{"avc", "av1", "hevc"},
		AudioQuality:         "best",
		MultiPart:            "all",
		Concurrency:          2,
		UpgradeCheckOnOpen:   true,
		Danmaku:              true,
		Subtitles:            true,
		AISubtitles:          true,
		ScheduledCheck:       false,
		CheckIntervalMin:     60,
		LiveCheckIntervalMin: 3,
		MaxRecordings:        2,
		Player: PlayerSettings{
			DanmakuOn:      true,
			DanmakuOpacity: 80,
			DanmakuDensity: "medium",
			SubtitleLang:   "zh-CN",
		},
		CloseAction: "tray",
		Download: DownloadSettings{VideoQuality: "best", MultiPart: "all", IncludeAI: true,
			MaxConcurrentTasks:  2,
			PreferredVideoCodec: "avc",
			PreferLosslessAudio: true,
			PreferDolbyAudio:    true,
			SaveDanmaku:         true,
			SaveSubtitles:       true,
			SaveCover:           true,
		},
		Subscription: SubscriptionSettings{
			Enabled:         false,
			Mode:            "interval",
			IntervalMinutes: 60,
			DailyTime:       "08:00",
			WeeklyDays:      []int{1, 3, 5},
			WeeklyTime:      "08:00",
			CheckOnStartup:  false,
			AutoCacheScope:  "none",
		},
		Live: LiveSettings{
			PollEnabled:             false,
			PollIntervalSeconds:     180,
			NotifyOnStart:           true,
			AutoRecordDefault:       false,
			MaxConcurrentRecordings: 2,
			CodecOrder:              []string{"avc", "av1", "hevc"},
		},
		Window: WindowSettings{
			CloseAction:    "tray",
			StartMinimized: false,
		},
		Diagnostics: DiagnosticsSettings{
			DebugLog: false,
		},
		Updates: UpdateSettings{AutoCheck: true, AutoDownload: true},
		Network: NetworkSettings{ProxyMode: "system"},
	}
}

// Normalize fills missing defaults, synchronizes top-level and nested fields, and validates fields.
func Normalize(s Settings, defaultCacheDir string) (Settings, error) {
	def := Default(defaultCacheDir)
	if s.Version <= 0 {
		s.Version = 1
	}

	// Synchronize CacheRoot and CacheDir.
	if strings.TrimSpace(s.CacheRoot) != "" {
		s.CacheRoot = filepath.Clean(strings.TrimSpace(s.CacheRoot))
		s.CacheDir = s.CacheRoot
	} else if strings.TrimSpace(s.CacheDir) != "" && filepath.Clean(s.CacheDir) != def.CacheDir {
		s.CacheDir = filepath.Clean(s.CacheDir)
		s.CacheRoot = s.CacheDir
	} else {
		s.CacheRoot = ""
		s.CacheDir = def.CacheDir
	}

	// VideoQuality ("best", "2160", "1080", "720")
	vq := strings.ToLower(strings.TrimSpace(s.VideoQuality))
	switch vq {
	case "best", "2160", "1080", "720":
		s.VideoQuality = vq
	case "":
		s.VideoQuality = "best"
	default:
		return s, fmt.Errorf("settings: invalid videoQuality %q", s.VideoQuality)
	}

	// AudioQuality ("best", "192k")
	aq := strings.ToLower(strings.TrimSpace(s.AudioQuality))
	switch aq {
	case "best", "192k":
		s.AudioQuality = aq
	case "":
		s.AudioQuality = "best"
	default:
		return s, fmt.Errorf("settings: invalid audioQuality %q", s.AudioQuality)
	}
	s.Download.PreferLosslessAudio = s.AudioQuality == "best"
	s.Download.PreferDolbyAudio = s.AudioQuality == "best"

	// MultiPart ("all", "first")
	mp := strings.ToLower(strings.TrimSpace(s.MultiPart))
	switch mp {
	case "all", "first":
		s.MultiPart = mp
	case "":
		s.MultiPart = "all"
	default:
		return s, fmt.Errorf("settings: invalid multiPart %q", s.MultiPart)
	}

	// Concurrency (1..4) and Download.MaxConcurrentTasks
	if s.Concurrency == 0 && s.Download.MaxConcurrentTasks != 0 {
		s.Concurrency = s.Download.MaxConcurrentTasks
	}
	if s.Download.MaxConcurrentTasks != 0 && s.Download.MaxConcurrentTasks != 2 && s.Concurrency == 2 {
		s.Concurrency = s.Download.MaxConcurrentTasks
	}
	if s.Concurrency < 1 || s.Concurrency > 4 {
		return s, fmt.Errorf("settings: concurrency must be between 1 and 4, got %d", s.Concurrency)
	}
	s.Download.MaxConcurrentTasks = s.Concurrency

	// CodecOrder & Download.PreferredVideoCodec
	codec := strings.ToLower(strings.TrimSpace(s.Download.PreferredVideoCodec))
	switch codec {
	case "avc", "hevc", "av1":
		s.Download.PreferredVideoCodec = codec
	case "":
		s.Download.PreferredVideoCodec = "avc"
	default:
		return s, fmt.Errorf("settings: unsupported preferredVideoCodec %q", s.Download.PreferredVideoCodec)
	}
	if len(s.CodecOrder) == 0 {
		if s.Download.PreferredVideoCodec != "avc" {
			s.CodecOrder = uniqueCodecs([]string{s.Download.PreferredVideoCodec, "avc", "av1", "hevc"})
		} else {
			s.CodecOrder = []string{"avc", "av1", "hevc"}
		}
	} else {
		cleanOrder := make([]string, 0, len(s.CodecOrder))
		seen := make(map[string]bool)
		for _, c := range s.CodecOrder {
			cl := strings.ToLower(strings.TrimSpace(c))
			if cl != "avc" && cl != "av1" && cl != "hevc" {
				return s, fmt.Errorf("settings: invalid codec in codecOrder %q", c)
			}
			if !seen[cl] {
				seen[cl] = true
				cleanOrder = append(cleanOrder, cl)
			}
		}
		s.CodecOrder = cleanOrder
		s.Download.PreferredVideoCodec = cleanOrder[0]
	}

	// Danmaku & Subtitles sync
	if !s.Danmaku && s.Download.SaveDanmaku {
		s.Download.SaveDanmaku = s.Danmaku
	} else if !s.Download.SaveDanmaku {
		s.Danmaku = false
	}
	if !s.Subtitles && s.Download.SaveSubtitles {
		s.Download.SaveSubtitles = s.Subtitles
	} else if !s.Download.SaveSubtitles {
		s.Subtitles = false
	}

	// ScheduledCheck & CheckIntervalMin sync with Subscription
	if s.ScheduledCheck != s.Subscription.Enabled {
		if s.ScheduledCheck {
			s.Subscription.Enabled = true
		} else if s.Subscription.Enabled {
			s.ScheduledCheck = true
		}
	}
	if s.CheckIntervalMin == 0 && s.Subscription.IntervalMinutes != 0 {
		s.CheckIntervalMin = s.Subscription.IntervalMinutes
	} else if s.Subscription.IntervalMinutes != 60 && s.Subscription.IntervalMinutes != 0 && s.CheckIntervalMin == 60 {
		s.CheckIntervalMin = s.Subscription.IntervalMinutes
	}
	switch s.CheckIntervalMin {
	case 30, 60, 120, 360:
	default:
		return s, fmt.Errorf("settings: checkIntervalMin must be 30, 60, 120 or 360, got %d", s.CheckIntervalMin)
	}
	s.Subscription.IntervalMinutes = s.CheckIntervalMin

	mode := strings.ToLower(strings.TrimSpace(s.Subscription.Mode))
	switch mode {
	case "interval", "daily", "weekly", "manual":
		s.Subscription.Mode = mode
	case "":
		s.Subscription.Mode = "interval"
	default:
		return s, fmt.Errorf("settings: unsupported subscription mode %q", s.Subscription.Mode)
	}
	if s.Subscription.DailyTime == "" {
		s.Subscription.DailyTime = "08:00"
	}
	if !isValidHHMM(s.Subscription.DailyTime) {
		return s, fmt.Errorf("settings: invalid dailyTime %q (expected HH:MM)", s.Subscription.DailyTime)
	}
	if s.Subscription.WeeklyTime == "" {
		s.Subscription.WeeklyTime = "08:00"
	}
	if !isValidHHMM(s.Subscription.WeeklyTime) {
		return s, fmt.Errorf("settings: invalid weeklyTime %q (expected HH:MM)", s.Subscription.WeeklyTime)
	}
	if len(s.Subscription.WeeklyDays) == 0 {
		s.Subscription.WeeklyDays = []int{1, 3, 5}
	} else {
		seen := make(map[int]bool)
		cleanDays := make([]int, 0, len(s.Subscription.WeeklyDays))
		for _, d := range s.Subscription.WeeklyDays {
			if d < 1 || d > 7 {
				return s, fmt.Errorf("settings: invalid weeklyDay %d (expected 1..7)", d)
			}
			if !seen[d] {
				seen[d] = true
				cleanDays = append(cleanDays, d)
			}
		}
		s.Subscription.WeeklyDays = cleanDays
	}

	scope := strings.ToLower(strings.TrimSpace(s.Subscription.AutoCacheScope))
	switch scope {
	case "none", "video", "dynamic", "both":
		s.Subscription.AutoCacheScope = scope
	case "":
		s.Subscription.AutoCacheScope = "none"
	default:
		return s, fmt.Errorf("settings: invalid autoCacheScope %q", s.Subscription.AutoCacheScope)
	}

	// LiveCheckIntervalMin (1..30) & MaxRecordings (1..4)
	if s.LiveCheckIntervalMin == 0 {
		if s.Live.PollIntervalSeconds >= 60 {
			s.LiveCheckIntervalMin = s.Live.PollIntervalSeconds / 60
		} else {
			s.LiveCheckIntervalMin = 3
		}
	}
	if s.LiveCheckIntervalMin < 1 || s.LiveCheckIntervalMin > 30 {
		return s, fmt.Errorf("settings: liveCheckIntervalMin must be between 1 and 30, got %d", s.LiveCheckIntervalMin)
	}
	if s.Live.PollIntervalSeconds < 30 {
		return s, fmt.Errorf("settings: live pollIntervalSeconds must be >= 30, got %d", s.Live.PollIntervalSeconds)
	}
	s.Live.PollIntervalSeconds = s.LiveCheckIntervalMin * 60
	if s.MaxRecordings == 0 {
		if s.Live.MaxConcurrentRecordings != 0 {
			s.MaxRecordings = s.Live.MaxConcurrentRecordings
		} else {
			s.MaxRecordings = 2
		}
	} else if s.Live.MaxConcurrentRecordings != 0 && s.Live.MaxConcurrentRecordings != 2 && s.MaxRecordings == 2 {
		s.MaxRecordings = s.Live.MaxConcurrentRecordings
	}
	if s.MaxRecordings < 1 || s.MaxRecordings > 4 {
		return s, fmt.Errorf("settings: maxRecordings must be between 1 and 4, got %d", s.MaxRecordings)
	}
	s.Live.MaxConcurrentRecordings = s.MaxRecordings

	if len(s.Live.CodecOrder) == 0 {
		s.Live.CodecOrder = append([]string(nil), s.CodecOrder...)
	} else {
		seen := make(map[string]bool)
		order := make([]string, 0, len(s.Live.CodecOrder))
		for _, c := range s.Live.CodecOrder {
			cl := strings.ToLower(strings.TrimSpace(c))
			if cl != "avc" && cl != "hevc" && cl != "av1" {
				return s, fmt.Errorf("settings: invalid live codec %q", c)
			}
			if !seen[cl] {
				seen[cl] = true
				order = append(order, cl)
			}
		}
		s.Live.CodecOrder = order
	}

	// Player settings validation
	if s.Player.DanmakuOpacity == 0 {
		s.Player.DanmakuOpacity = 80
	}
	if s.Player.DanmakuOpacity < 20 || s.Player.DanmakuOpacity > 100 {
		return s, fmt.Errorf("settings: player.danmakuOpacity must be between 20 and 100, got %d", s.Player.DanmakuOpacity)
	}
	density := strings.ToLower(strings.TrimSpace(s.Player.DanmakuDensity))
	switch density {
	case "low", "medium", "high", "unlimited":
		s.Player.DanmakuDensity = density
	case "":
		s.Player.DanmakuDensity = "medium"
	default:
		return s, fmt.Errorf("settings: invalid player.danmakuDensity %q", s.Player.DanmakuDensity)
	}
	if strings.TrimSpace(s.Player.SubtitleLang) == "" {
		s.Player.SubtitleLang = "zh-CN"
	}

	// CloseAction sync
	closeAct := strings.ToLower(strings.TrimSpace(s.CloseAction))
	winCloseAct := strings.ToLower(strings.TrimSpace(s.Window.CloseAction))
	if closeAct == "" {
		closeAct = winCloseAct
	} else if winCloseAct != "" && winCloseAct != "tray" && closeAct == "tray" {
		closeAct = winCloseAct
	}
	switch closeAct {
	case "tray", "quit":
		s.CloseAction = closeAct
		s.Window.CloseAction = closeAct
	case "":
		s.CloseAction = "tray"
		s.Window.CloseAction = "tray"
	default:
		return s, fmt.Errorf("settings: invalid closeAction %q", closeAct)
	}

	s.Network.ProxyURL = strings.TrimSpace(s.Network.ProxyURL)
	switch pm := strings.ToLower(strings.TrimSpace(s.Network.ProxyMode)); pm {
	case "", "system":
		s.Network.ProxyMode = "system"
	case "none":
		s.Network.ProxyMode = pm
	case "custom":
		if s.Network.ProxyURL == "" {
			return s, fmt.Errorf("settings: custom proxy needs an address")
		}
		s.Network.ProxyMode = pm
	default:
		return s, fmt.Errorf("settings: invalid proxyMode %q", s.Network.ProxyMode)
	}

	s.Download.VideoQuality = s.VideoQuality
	s.Download.CodecOrder = append([]string(nil), s.CodecOrder...)
	s.Download.MultiPart = s.MultiPart
	s.Download.IncludeAI = s.AISubtitles

	return s, nil
}

func uniqueCodecs(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool)
	for _, c := range in {
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

func isValidHHMM(v string) bool {
	parts := strings.Split(v, ":")
	if len(parts) != 2 || len(parts[0]) != 2 || len(parts[1]) != 2 {
		return false
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return false
	}
	return h >= 0 && h <= 23 && m >= 0 && m <= 59
}

// Store manages reading and writing settings.json safely.
type Store struct {
	mu              sync.RWMutex
	path            string
	defaultCacheDir string
	current         Settings
}

// Open loads settings.json from path. If missing, writes defaults.
// If corrupted or invalid, backs up to <path>.corrupt-<timestamp> and resets to defaults.
func Open(path, defaultCacheDir string) (*Store, bool, string, error) {
	def := Default(defaultCacheDir)
	s := &Store{
		path:            path,
		defaultCacheDir: defaultCacheDir,
		current:         def,
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

	// Start with defaults so omitted fields inherit default values.
	loaded := Default(defaultCacheDir)
	if err := json.Unmarshal(raw, &loaded); err != nil {
		backup, bErr := storage.BackupCorruptFile(path)
		if bErr != nil {
			return nil, false, "", bErr
		}
		_ = storage.WriteJSONAtomic(path, def, 0o600)
		return s, true, backup, nil
	}

	norm, err := Normalize(loaded, defaultCacheDir)
	if err != nil {
		backup, bErr := storage.BackupCorruptFile(path)
		if bErr != nil {
			return nil, false, "", bErr
		}
		_ = storage.WriteJSONAtomic(path, def, 0o600)
		return s, true, backup, nil
	}

	s.current = norm
	return s, false, "", nil
}

// Get returns a deep copy of the current settings.
func (s *Store) Get() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneSettings(s.current)
}

// Update validates and saves new settings atomically.
func (s *Store) Update(next Settings) (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	norm, err := normalizeUpdate(next, s.current, s.defaultCacheDir)
	if err != nil {
		return Settings{}, err
	}
	if err := storage.WriteJSONAtomic(s.path, norm, 0o600); err != nil {
		return Settings{}, err
	}
	s.current = norm
	return cloneSettings(s.current), nil
}

// PreviewUpdate validates the same update as Update without changing memory or disk.
func (s *Store) PreviewUpdate(next Settings) (Settings, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return normalizeUpdate(next, s.current, s.defaultCacheDir)
}

func syncChangedAlias[T comparable](canonical, legacy *T, beforeCanonical, beforeLegacy T) {
	if *canonical == beforeCanonical && *legacy != beforeLegacy {
		*canonical = *legacy
	}
	*legacy = *canonical
}

func normalizeUpdate(next, before Settings, defaultCacheDir string) (Settings, error) {
	next = cloneSettings(next)
	if next.CacheRoot != before.CacheRoot {
		next.CacheDir = next.CacheRoot
	} else if next.CacheDir != before.CacheDir {
		next.CacheRoot = next.CacheDir
	}
	syncChangedAlias(&next.Concurrency, &next.Download.MaxConcurrentTasks, before.Concurrency, before.Download.MaxConcurrentTasks)
	syncChangedAlias(&next.Danmaku, &next.Download.SaveDanmaku, before.Danmaku, before.Download.SaveDanmaku)
	syncChangedAlias(&next.Subtitles, &next.Download.SaveSubtitles, before.Subtitles, before.Download.SaveSubtitles)
	syncChangedAlias(&next.ScheduledCheck, &next.Subscription.Enabled, before.ScheduledCheck, before.Subscription.Enabled)
	syncChangedAlias(&next.CheckIntervalMin, &next.Subscription.IntervalMinutes, before.CheckIntervalMin, before.Subscription.IntervalMinutes)
	syncChangedAlias(&next.MaxRecordings, &next.Live.MaxConcurrentRecordings, before.MaxRecordings, before.Live.MaxConcurrentRecordings)
	syncChangedAlias(&next.CloseAction, &next.Window.CloseAction, before.CloseAction, before.Window.CloseAction)
	if next.LiveCheckIntervalMin == before.LiveCheckIntervalMin && next.Live.PollIntervalSeconds != before.Live.PollIntervalSeconds {
		next.LiveCheckIntervalMin = next.Live.PollIntervalSeconds / 60
	}
	if slices.Equal(next.CodecOrder, before.CodecOrder) && next.Download.PreferredVideoCodec != before.Download.PreferredVideoCodec {
		next.CodecOrder = uniqueCodecs([]string{next.Download.PreferredVideoCodec, "avc", "av1", "hevc"})
	}
	next.Live.CodecOrder = append([]string(nil), next.CodecOrder...)
	return Normalize(next, defaultCacheDir)
}

func cloneSettings(in Settings) Settings {
	out := in
	if in.CodecOrder != nil {
		out.CodecOrder = append([]string(nil), in.CodecOrder...)
	}
	if in.Download.CodecOrder != nil {
		out.Download.CodecOrder = append([]string(nil), in.Download.CodecOrder...)
	}
	if in.Subscription.WeeklyDays != nil {
		out.Subscription.WeeklyDays = append([]int(nil), in.Subscription.WeeklyDays...)
	}
	if in.Live.CodecOrder != nil {
		out.Live.CodecOrder = append([]string(nil), in.Live.CodecOrder...)
	}
	return out
}
