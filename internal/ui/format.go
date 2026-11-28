package ui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

// FormatCount formats large numbers using Chinese 万/亿 abbreviations per docs/ui.md §7.
func FormatCount(n int64) string {
	if n < 0 {
		return "0"
	}
	if n >= 100_000_000 {
		v := float64(n) / 100_000_000.0
		return strings.TrimSuffix(fmt.Sprintf("%.1f", v), ".0") + "亿"
	}
	if n >= 10_000 {
		v := float64(n) / 10_000.0
		return strings.TrimSuffix(fmt.Sprintf("%.1f", v), ".0") + "万"
	}
	return fmt.Sprintf("%d", n)
}

// FormatBytes formats a byte count into human-readable B / KB / MB / GB / TB.
func FormatBytes(b int64) string {
	if b <= 0 {
		return "0 B"
	}
	units := [5]string{"B", "KB", "MB", "GB", "TB"}
	val := float64(b)
	idx := 0
	for val >= 1024 && idx < len(units)-1 {
		val /= 1024
		idx++
	}
	if idx == 0 {
		return fmt.Sprintf("%d B", b)
	}
	return strings.TrimSuffix(fmt.Sprintf("%.1f", val), ".0") + " " + units[idx]
}

// FormatDuration formats seconds into MM:SS or H:MM:SS.
func FormatDuration(sec int) string {
	if sec <= 0 {
		return "00:00"
	}
	h := sec / 3600
	m := (sec % 3600) / 60
	s := sec % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}

// FormatTimestamp formats a Unix timestamp (seconds) into a readable local date/time.
func FormatTimestamp(ts int64) string {
	if ts <= 0 {
		return ""
	}
	t := time.Unix(ts, 0).Local()
	return t.Format("2006-01-02 15:04")
}

// FormatRelativeTime formats a Unix timestamp into social-style relative or date text.
func FormatRelativeTime(ts int64) string {
	if ts <= 0 {
		return ""
	}
	t := time.Unix(ts, 0).Local()
	now := time.Now()
	diff := now.Sub(t)
	if diff >= 0 {
		if diff < time.Minute {
			return "刚刚"
		}
		if diff < time.Hour {
			return fmt.Sprintf("%d 分钟前", int(diff.Minutes()))
		}
		if diff < 24*time.Hour {
			return fmt.Sprintf("%d 小时前", int(diff.Hours()))
		}
	}
	if t.Year() == now.Year() {
		return fmt.Sprintf("%d月%d日", int(t.Month()), t.Day())
	}
	return fmt.Sprintf("%d年%d月%d日", t.Year(), int(t.Month()), t.Day())
}

// formatSubGridUpdatedAt formats a subscription's local latest update/cache
// timestamp into a compact secondary caption for the subscriptions avatar grid.
func formatSubGridUpdatedAt(ts int64, now time.Time) string {
	if ts <= 0 {
		return "暂无缓存"
	}
	if now.IsZero() {
		now = time.Now()
	}
	t := time.Unix(ts, 0).Local()
	nowLocal := now.Local()
	diff := nowLocal.Sub(t)
	if diff < 0 && diff > -time.Minute {
		return "刚刚更新"
	}
	if diff >= 0 {
		if diff < time.Minute {
			return "刚刚更新"
		}
		if diff < time.Hour {
			return fmt.Sprintf("%d 分钟前更新", int(diff/time.Minute))
		}
		if diff < 24*time.Hour {
			return fmt.Sprintf("%d 小时前更新", int(diff/time.Hour))
		}
		if diff < 30*24*time.Hour {
			return fmt.Sprintf("%d 天前更新", int(diff/(24*time.Hour)))
		}
	}
	if t.Year() == nowLocal.Year() {
		return fmt.Sprintf("%d月%d日更新", int(t.Month()), t.Day())
	}
	return fmt.Sprintf("%d年%d月%d日更新", t.Year(), int(t.Month()), t.Day())
}

// FirstGlyph returns the first visible rune of a name for avatar fallback badges.
func FirstGlyph(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "T"
	}
	r, _ := utf8.DecodeRuneInString(name)
	if r == utf8.RuneError {
		return "T"
	}
	return strings.ToUpper(string(r))
}

// AvatarColor picks a deterministic palette color for a creator/account.
func AvatarColor(name string, mid int64) ui.Color {
	var h uint64 = uint64(mid) * 2654435761
	for i := 0; i < len(name); i++ {
		h = h*31 + uint64(name[i])
	}
	return AvatarPalette[int(h%uint64(len(AvatarPalette)))]
}
