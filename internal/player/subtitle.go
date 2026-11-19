package player

import (
	"math"
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
	Lang   string        `json:"lang"`
	Label  string        `json:"label"`
	Cues   []SubtitleCue `json:"cues"`
	maxEnd []float64
}

func (t *SubtitleTrack) buildIndex() {
	if len(t.Cues) == 0 {
		t.maxEnd = nil
		return
	}
	t.maxEnd = make([]float64, len(t.Cues))
	best := math.Inf(-1)
	for i, c := range t.Cues {
		if c.EndSec > best {
			best = c.EndSec
		}
		t.maxEnd[i] = best
	}
}

func (t *SubtitleTrack) activeCueAt(tSec float64) string {
	if len(t.Cues) == 0 || math.IsNaN(tSec) || math.IsInf(tSec, 0) || tSec < 0 {
		return ""
	}
	if len(t.maxEnd) != len(t.Cues) {
		return ActiveCueAt(t.Cues, tSec)
	}
	idx := sort.Search(len(t.maxEnd), func(i int) bool {
		return t.maxEnd[i] >= tSec
	})
	var active []string
	for i := idx; i < len(t.Cues); i++ {
		if t.Cues[i].StartSec > tSec {
			break
		}
		if tSec >= t.Cues[i].StartSec && tSec <= t.Cues[i].EndSec {
			active = append(active, t.Cues[i].Text)
		}
	}
	return strings.Join(active, "\n")
}

// ParseWebVTT parses a WebVTT (.vtt) or SRT (.srt) subtitle payload into sorted SubtitleCues.
func ParseWebVTT(raw []byte) []SubtitleCue {
	content := strings.TrimPrefix(string(raw), "\uFEFF")
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	blocks := splitSubtitleBlocks(content)
	var cues []SubtitleCue

	for _, lines := range blocks {
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

func splitSubtitleBlocks(content string) [][]string {
	var blocks [][]string
	var cur []string
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == "" {
			if len(cur) > 0 {
				blocks = append(blocks, cur)
				cur = nil
			}
			continue
		}
		cur = append(cur, line)
	}
	if len(cur) > 0 {
		blocks = append(blocks, cur)
	}
	return blocks
}

// ActiveCueAt returns the active subtitle text at playback timestamp tSec.
// Cues must be sorted by StartSec (as returned by ParseWebVTT); overlapping
// cues with non-monotonic EndSec are handled without missing earlier long cues.
func ActiveCueAt(cues []SubtitleCue, tSec float64) string {
	if len(cues) == 0 || math.IsNaN(tSec) || math.IsInf(tSec, 0) || tSec < 0 {
		return ""
	}
	endIdx := sort.Search(len(cues), func(i int) bool {
		return cues[i].StartSec > tSec
	})
	var active []string
	for i := 0; i < endIdx; i++ {
		if tSec >= cues[i].StartSec && tSec <= cues[i].EndSec {
			active = append(active, cues[i].Text)
		}
	}
	return strings.Join(active, "\n")
}

func parseCueTime(s string) (float64, bool) {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", "."))
	if s == "" || strings.ContainsAny(s, "+-") {
		return 0, false
	}
	parts := strings.Split(s, ":")
	if len(parts) == 3 {
		h, err1 := strconv.ParseFloat(parts[0], 64)
		m, err2 := strconv.ParseFloat(parts[1], 64)
		sec, err3 := strconv.ParseFloat(parts[2], 64)
		if err1 != nil || err2 != nil || err3 != nil ||
			!isValidNonNegFloat(h) || !isValidNonNegFloat(m) || !isValidNonNegFloat(sec) {
			return 0, false
		}
		total := h*3600 + m*60 + sec
		if !isValidNonNegFloat(total) {
			return 0, false
		}
		return total, true
	}
	if len(parts) == 2 {
		m, err1 := strconv.ParseFloat(parts[0], 64)
		sec, err2 := strconv.ParseFloat(parts[1], 64)
		if err1 != nil || err2 != nil ||
			!isValidNonNegFloat(m) || !isValidNonNegFloat(sec) {
			return 0, false
		}
		total := m*60 + sec
		if !isValidNonNegFloat(total) {
			return 0, false
		}
		return total, true
	}
	return 0, false
}

func isValidNonNegFloat(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0
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
