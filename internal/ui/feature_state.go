package ui

import (
	"hash/fnv"
	"runtime"
	"strings"
	"sync"
	"time"
	"weak"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

// featureState stores UI-thread caches and feature-local view state for an App
// without adding fields to the shared App struct. Entries are keyed by weak
// pointers and removed automatically when the App is garbage collected.
type featureState struct {
	// IME-safe search state.
	librarySearchDirty  bool
	subSearchApplied    string
	importSearchApplied string

	// Virtual list states for views that paginate or list many items.
	checkPlanListState        *ui.ListState
	importFollowingsListState *ui.ListState

	// Live preview mute preference persisted across segment rotations.
	livePreviewMutedSet bool
	livePreviewMuted    bool

	// Cached TaskDetail for the currently expanded download task.
	taskDetailID        string
	taskDetailRev       int
	taskDetailUpdatedAt string
	taskDetailStatus    string
	taskDetailBytes     int64
	taskDetailFileN     int
	taskDetail          download.TaskDetail
	taskDetailOK        bool

	// Cached live sessions + incidents fingerprint.
	liveSessionsHash uint64
	liveSessions     []liveIncidentSession
	liveSessionsSet  bool

	// Cached library tiles.
	libTilesSig libraryTilesSig
	libTiles    []libraryTile

	// Cached /douyin/work cache state lookup.
	douyinWorkStateKey string
	douyinWorkStateRev int
	douyinWorkState    library.DotState
	douyinWorkStateSet bool

	// Cached subscription account rows (Subscriptions › 全部账号).
	subAccRowsSig subAccountRowsSig
	subAccRows    []subAccountRow

	// Cached filtered subscriptions list (default order, used by picker modals).
	filteredSubsSig filteredSubsSig
	filteredSubs    []subscription.Subscription

	// Cached filtered & sorted subscriptions grid items with local latest timestamps.
	subGridSig   subGridItemsSig
	subGridItems []subGridItem
	subGridSubs  []subscription.Subscription
}

type subGridItem struct {
	sub      subscription.Subscription
	latestTs int64
}

type libraryTilesSig struct {
	rev  int
	n    int
	kind string
	hash uint64
}

type subAccountRowsSig struct {
	subsRev int
	subsN   int
	site    string
	hash    uint64
}

type filteredSubsSig struct {
	subsRev int
	subsN   int
	search  string
	hash    uint64
}

type subGridItemsSig struct {
	subsRev      int
	subsN        int
	statsRev     int
	authorsN     int
	authorsFirst *library.ItemAuthor
	authorsHash  uint64
	search       string
	sort         string
	hash         uint64
}

var (
	featureStatesMu sync.Mutex
	featureStates   = map[weak.Pointer[App]]*featureState{}
)

func (a *App) fs() *featureState {
	wp := weak.Make(a)
	featureStatesMu.Lock()
	defer featureStatesMu.Unlock()
	if st, ok := featureStates[wp]; ok {
		return st
	}
	st := &featureState{}
	featureStates[wp] = st
	runtime.AddCleanup(a, func(key weak.Pointer[App]) {
		featureStatesMu.Lock()
		delete(featureStates, key)
		featureStatesMu.Unlock()
	}, wp)
	return st
}

// formatRFC3339Clock formats an RFC3339 timestamp in local time, omitting the
// date when it falls on today.
func formatRFC3339Clock(raw string, withSeconds bool) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return raw
	}
	local := t.Local()
	now := time.Now()
	sameDay := local.Year() == now.Year() && local.YearDay() == now.YearDay()
	if withSeconds {
		if sameDay {
			return local.Format("15:04:05")
		}
		return local.Format("01-02 15:04:05")
	}
	if sameDay {
		return local.Format("15:04")
	}
	return local.Format("01-02 15:04")
}

// segmentedChoice describes one option in a pill segmented control.
type segmentedChoice struct {
	Key   string
	Text  string
	Label string
}

// renderSegmentedPills renders a compact segmented pill bar and calls onSelect
// when a different option is clicked.
func renderSegmentedPills(c *ui.Context, active string, choices []segmentedChoice, onSelect func(key string)) {
	ui.Row(c).Padding(3).Radius(999).Background(ColorBgSecondary).Gap(2).Shrink(0).Children(func() {
		for _, ch := range choices {
			opt := ch
			isActive := active == opt.Key
			btn := ui.ButtonBase(c).Label(opt.Label).Padding(4, 12).Radius(999).Center()
			if isActive {
				btn.Background(ColorBgMain).Border(1, ColorBorder)
			} else if btn.Hovered() {
				btn.Background(ColorHover)
			}
			btn.Children(func() {
				t := ui.Text(c, opt.Text).FontSize(12).SingleLine()
				if isActive {
					t.FontWeight(600).TextColor(ColorAccent)
				} else {
					t.FontWeight(500).TextColor(ColorTextMuted)
				}
			})
			if btn.Clicked() && !isActive && onSelect != nil {
				onSelect(opt.Key)
			}
		}
	})
}

func downloadErrorKindLabel(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "":
		return ""
	case "auth_required", "auth", "unauthorized", "login_required":
		return "需要登录"
	case "rate_limited", "risk_control", "blocked", "challenge":
		return "触发风控或限流"
	case "network", "timeout", "http", "io":
		return "网络错误"
	case "disk_full", "no_space":
		return "磁盘空间不足"
	case "not_found", "deleted", "gone":
		return "内容不可用"
	case "mux_failed", "remux", "convert_failed":
		return "封装失败"
	case "canceled", "cancelled":
		return "已取消"
	default:
		return "下载异常"
	}
}

func taskFileRoleLabel(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "video":
		return "视频"
	case "audio":
		return "音频"
	case "cover":
		return "封面"
	case "image":
		return "图片"
	case "danmaku":
		return "弹幕"
	case "subtitle":
		return "字幕"
	case "live_segment":
		return "直播分段"
	case "live_photo":
		return "实况"
	case "bgm":
		return "背景音乐"
	case "avatar":
		return "头像"
	default:
		return "文件"
	}
}

func taskFileStatusLabel(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "queued", "pending":
		return "排队中"
	case "running", "downloading":
		return "下载中"
	case "completed", "done":
		return "已完成"
	case "failed", "error":
		return "失败"
	case "paused":
		return "已暂停"
	case "canceled", "cancelled":
		return "已取消"
	case "skipped":
		return "已跳过"
	default:
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(status)), "paused_") {
			return "已暂停"
		}
		return taskStatusText(status)
	}
}

func fnvWriteStr(h interface{ Write([]byte) (int, error) }, s string) {
	_, _ = h.Write([]byte(s))
	_, _ = h.Write([]byte{0})
}

func fnvWriteInt64(h interface{ Write([]byte) (int, error) }, v int64) {
	var buf [8]byte
	u := uint64(v)
	buf[0] = byte(u)
	buf[1] = byte(u >> 8)
	buf[2] = byte(u >> 16)
	buf[3] = byte(u >> 24)
	buf[4] = byte(u >> 32)
	buf[5] = byte(u >> 40)
	buf[6] = byte(u >> 48)
	buf[7] = byte(u >> 56)
	_, _ = h.Write(buf[:])
}

func newFNV64a() interface {
	Write([]byte) (int, error)
	Sum64() uint64
} {
	return fnv.New64a()
}

func clampRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	r := []rune(strings.TrimSpace(s))
	if len(r) <= maxRunes {
		return string(r)
	}
	return string(r[:maxRunes])
}
