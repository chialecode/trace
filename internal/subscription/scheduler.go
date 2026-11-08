package subscription

import (
	"strconv"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/settings"
)

// NextRunTime computes the next scheduled subscription check timestamp from now (C32).
// Returns (zeroTime, false) when scheduling is disabled or in manual mode.
func NextRunTime(cfg settings.SubscriptionSettings, now time.Time) (time.Time, bool) {
	if !cfg.Enabled {
		return time.Time{}, false
	}
	mode := strings.ToLower(strings.TrimSpace(cfg.Mode))
	switch mode {
	case "manual", "":
		return time.Time{}, false

	case "interval":
		mins := cfg.IntervalMinutes
		if mins < 15 {
			mins = 15
		}
		return now.Add(time.Duration(mins) * time.Minute), true

	case "daily":
		hour, min := parseClockHHMM(cfg.DailyTime, 8, 0)
		loc := now.Location()
		candidate := time.Date(now.Year(), now.Month(), now.Day(), hour, min, 0, 0, loc)
		if !candidate.After(now) {
			candidate = candidate.AddDate(0, 0, 1)
		}
		return candidate, true

	case "weekly":
		hour, min := parseClockHHMM(cfg.WeeklyTime, 8, 0)
		daySet := make(map[int]bool, len(cfg.WeeklyDays))
		for _, d := range cfg.WeeklyDays {
			if d >= 1 && d <= 7 {
				daySet[d] = true
			}
		}
		if len(daySet) == 0 {
			daySet[1] = true
			daySet[3] = true
			daySet[5] = true
		}

		loc := now.Location()
		for offset := 0; offset <= 7; offset++ {
			dayTime := time.Date(now.Year(), now.Month(), now.Day(), hour, min, 0, 0, loc).AddDate(0, 0, offset)
			isoDay := isoWeekday(dayTime.Weekday())
			if daySet[isoDay] && dayTime.After(now) {
				return dayTime, true
			}
		}
		return time.Time{}, false

	default:
		return time.Time{}, false
	}
}

func parseClockHHMM(s string, defH, defM int) (int, int) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 2 {
		return defH, defM
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return defH, defM
	}
	return h, m
}

// isoWeekday converts time.Weekday (Sunday=0..Saturday=6) to ISO 1..7 (Monday=1..Sunday=7).
func isoWeekday(w time.Weekday) int {
	if w == time.Sunday {
		return 7
	}
	return int(w)
}
