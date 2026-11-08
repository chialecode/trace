package player

import (
	"sort"
	"strconv"
	"strings"
)

// SubtitleCue represents a single timed subtitle interval.
type SubtitleCue struct {
	StartSec float64 `json:"startSec"`
	EndSec   float64 `json:"endSec"`
	Text     string  `json:"text"`
}

// SubtitleTrack holds parsed cues for a single language track.
type SubtitleTrack struct {
	Lang  string        `json:"lang"`
	Label string        `json:"label"`
	Cues  []SubtitleCue `json:"cues"`
}

// ParseWebVTT parses a WebVTT (.vtt) or SRT (.srt) subtitle payload into sorted SubtitleCues (C22).
func ParseWebVTT(raw []byte) []SubtitleCue {
	content := strings.ReplaceAll(string(raw), "\r\n", "\n")
	blocks := strings.Split(content, "\n\n")
	var cues []SubtitleCue

	for _, block := range blocks {
		lines := strings.Split(strings.TrimSpace(block), "\n")
		if len(lines) == 0 {
			continue
		}
		if strings.HasPrefix(lines[0], "WEBVTT") || strings.HasPrefix(lines[0], "NOTE") {
			continue
		}

		timeLineIdx := -1
		for i, line := range lines {
			if strings.Contains(line, "-->") {
				timeLineIdx = i
				break
			}
		}
		if timeLineIdx < 0 || timeLineIdx+1 >= len(lines) {
			continue
		}

		parts := strings.Split(lines[timeLineIdx], "-->")
		if len(parts) != 2 {
			continue
		}
		startSec, ok1 := parseCueTime(strings.TrimSpace(parts[0]))
		// Strip optional WebVTT positioning settings after timestamp
		endFields := strings.Fields(strings.TrimSpace(parts[1]))
		if len(endFields) == 0 {
			continue
		}
		endSec, ok2 := parseCueTime(endFields[0])
		if !ok1 || !ok2 || endSec <= startSec {
			continue
		}

		text := unescapeWebVTTText(strings.TrimSpace(strings.Join(lines[timeLineIdx+1:], "\n")))
		if text == "" {
			continue
		}
		cues = append(cues, SubtitleCue{
			StartSec: startSec,
			EndSec:   endSec,
			Text:     text,
		})
	}

	sort.SliceStable(cues, func(i, j int) bool {
		if cues[i].StartSec != cues[j].StartSec {
			return cues[i].StartSec < cues[j].StartSec
		}
		return cues[i].EndSec < cues[j].EndSec
	})
	return cues
}

// ActiveCueAt returns the active subtitle text at playback timestamp tSec.
func ActiveCueAt(cues []SubtitleCue, tSec float64) string {
	if len(cues) == 0 || tSec < 0 {
		return ""
	}
	idx := sort.Search(len(cues), func(i int) bool {
		return cues[i].EndSec >= tSec
	})
	var active []string
	for i := idx; i < len(cues); i++ {
		if cues[i].StartSec > tSec {
			break
		}
		if tSec >= cues[i].StartSec && tSec <= cues[i].EndSec {
			active = append(active, cues[i].Text)
		}
	}
	return strings.Join(active, "\n")
}

func parseCueTime(s string) (float64, bool) {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", "."))
	parts := strings.Split(s, ":")
	if len(parts) == 3 {
		h, err1 := strconv.ParseFloat(parts[0], 64)
		m, err2 := strconv.ParseFloat(parts[1], 64)
		sec, err3 := strconv.ParseFloat(parts[2], 64)
		if err1 != nil || err2 != nil || err3 != nil {
			return 0, false
		}
		return h*3600 + m*60 + sec, true
	}
	if len(parts) == 2 {
		m, err1 := strconv.ParseFloat(parts[0], 64)
		sec, err2 := strconv.ParseFloat(parts[1], 64)
		if err1 != nil || err2 != nil {
			return 0, false
		}
		return m*60 + sec, true
	}
	return 0, false
}

var webvttUnescaper = strings.NewReplacer(
	"&lt;", "<",
	"&gt;", ">",
	"&quot;", `"`,
	"&#39;", "'",
	"&apos;", "'",
	"&nbsp;", " ",
	"&amp;", "&",
)

func unescapeWebVTTText(s string) string {
	return webvttUnescaper.Replace(s)
}
