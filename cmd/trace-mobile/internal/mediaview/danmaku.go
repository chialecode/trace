package mediaview

import (
	"bytes"
	"encoding/xml"
	"image/color"
	"math"
	"sort"
	"strconv"
	"strings"
)

const (
	// DanmakuRollDuration is the seconds a rolling danmaku takes to cross the viewport.
	DanmakuRollDuration = 6.0
	// DanmakuFixedDuration is the seconds a top/bottom pinned danmaku stays visible.
	DanmakuFixedDuration = 4.0
	// DanmakuTrackHeight is the vertical track spacing in dp for mobile overlays.
	DanmakuTrackHeight = 26.0
)

// DanmakuItem represents one parsed Bilibili XML danmaku entry (kept in RAM only).
type DanmakuItem struct {
	ID         string
	TimeSec    float64
	Mode       int // 1,2,3,6 = rolling; 4 = bottom; 5 = top
	FontSize   int
	ColorRGB   uint32
	Text       string
	WidthPx    float32
	secondRank int
}

// PositionedDanmaku is a visible danmaku entry laid out for the current playback frame.
type PositionedDanmaku struct {
	ID       string
	TimeSec  float64
	Text     string
	X        float32
	Y        float32
	Width    float32
	Height   float32
	FontSize float32
	Color    color.RGBA
	Mode     int
}

// DanmakuEngine provides sub-millisecond frame layout over tens of thousands of
// danmaku entries using binary search and deterministic track collision avoidance.
type DanmakuEngine struct {
	items []DanmakuItem
}

func isSupportedDanmakuMode(mode int) bool {
	return mode >= 1 && mode <= 6
}

func clampDanmakuFontSize(fontSize int) float32 {
	if fontSize <= 0 {
		return 14
	}
	if fontSize < 12 {
		return 12
	}
	if fontSize > 18 {
		return 18
	}
	return float32(fontSize)
}

// NewDanmakuEngine creates a sorted DanmakuEngine from parsed items.
func NewDanmakuEngine(items []DanmakuItem) *DanmakuEngine {
	copied := make([]DanmakuItem, 0, len(items))
	for _, it := range items {
		if math.IsNaN(it.TimeSec) || math.IsInf(it.TimeSec, 0) || it.TimeSec < 0 {
			continue
		}
		if it.Mode == 0 {
			it.Mode = 1
		}
		if !isSupportedDanmakuMode(it.Mode) {
			continue
		}
		if it.FontSize <= 0 {
			it.FontSize = 14
		}
		if it.WidthPx <= 0 {
			it.WidthPx = estimateDanmakuWidth(it.Text, it.FontSize)
		}
		copied = append(copied, it)
	}
	sort.SliceStable(copied, func(i, j int) bool {
		if copied[i].TimeSec != copied[j].TimeSec {
			return copied[i].TimeSec < copied[j].TimeSec
		}
		return copied[i].ID < copied[j].ID
	})
	rank := 0
	for i := range copied {
		if i == 0 || math.Floor(copied[i].TimeSec) != math.Floor(copied[i-1].TimeSec) {
			rank = 0
		}
		copied[i].secondRank = rank
		rank++
	}
	return &DanmakuEngine{items: copied}
}

// ParseDanmakuXML parses standard Bilibili XML (<d p="...">text</d>) in memory.
func ParseDanmakuXML(xmlBytes []byte) (*DanmakuEngine, error) {
	dec := xml.NewDecoder(bytes.NewReader(xmlBytes))
	var items []DanmakuItem

	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "d" {
			continue
		}
		var pAttr string
		for _, a := range se.Attr {
			if a.Name.Local == "p" {
				pAttr = a.Value
				break
			}
		}
		var content string
		if err := dec.DecodeElement(&content, &se); err != nil {
			continue
		}
		content = strings.TrimSpace(content)
		if content == "" || pAttr == "" {
			continue
		}

		parts := strings.Split(pAttr, ",")
		if len(parts) < 4 {
			continue
		}
		tSec, err := strconv.ParseFloat(parts[0], 64)
		if err != nil || math.IsNaN(tSec) || math.IsInf(tSec, 0) || tSec < 0 {
			continue
		}
		mode, _ := strconv.Atoi(parts[1])
		if !isSupportedDanmakuMode(mode) {
			continue
		}
		fontSize, _ := strconv.Atoi(parts[2])
		colVal, _ := strconv.ParseUint(parts[3], 10, 32)
		idStr := ""
		if len(parts) >= 8 {
			idStr = parts[7]
		}
		items = append(items, DanmakuItem{
			ID:       idStr,
			TimeSec:  tSec,
			Mode:     mode,
			FontSize: fontSize,
			ColorRGB: uint32(colVal),
			Text:     content,
			WidthPx:  estimateDanmakuWidth(content, fontSize),
		})
	}
	return NewDanmakuEngine(items), nil
}

// Count returns the total number of loaded danmaku entries.
func (e *DanmakuEngine) Count() int {
	if e == nil {
		return 0
	}
	return len(e.items)
}

// LayoutFrame computes all visible danmaku positions at playback time tSec.
// Uses binary search over [tSec - DanmakuRollDuration, tSec] and track collision
// avoidance so 10,000+ entries lay out in well under 1 ms per frame.
func (e *DanmakuEngine) LayoutFrame(
	tSec float64,
	viewportW, viewportH float32,
	areaRatio float64,
	opacity float64,
	density ...string,
) []PositionedDanmaku {
	if e == nil || len(e.items) == 0 || viewportW <= 0 || viewportH <= 0 || math.IsNaN(tSec) || math.IsInf(tSec, 0) || tSec < 0 {
		return nil
	}
	if areaRatio <= 0 || areaRatio > 1.0 {
		areaRatio = 0.5
	}
	if opacity <= 0 || opacity > 1.0 {
		opacity = 0.85
	}

	usableH := viewportH * float32(areaRatio)
	maxTracks := int(usableH / DanmakuTrackHeight)
	if maxTracks < 1 {
		maxTracks = 1
	}
	if maxTracks > 28 {
		maxTracks = 28
	}

	windowStart := tSec - DanmakuRollDuration
	startIdx := sort.Search(len(e.items), func(i int) bool {
		return e.items[i].TimeSec >= windowStart
	})

	var rollRightEdge [28]float32
	var rollOccupied [28]bool
	var topOccupied [28]bool
	var bottomOccupied [28]bool

	alpha := uint8(opacity*255.0 + 0.5)
	initCap := maxTracks * 2
	if rem := len(e.items) - startIdx; rem < initCap {
		initCap = rem
	}
	var out []PositionedDanmaku
	if initCap > 0 {
		out = make([]PositionedDanmaku, 0, initCap)
	}
	perSecond := 0
	if len(density) > 0 {
		perSecond = danmakuPerSecond(density[0])
	}

	for i := startIdx; i < len(e.items); i++ {
		it := e.items[i]
		if it.TimeSec > tSec {
			break
		}
		if perSecond > 0 && it.secondRank >= perSecond {
			continue
		}
		elapsed := tSec - it.TimeSec
		if elapsed < 0 {
			continue
		}

		rgb := it.ColorRGB
		if rgb == 0 {
			rgb = 0xFFFFFF
		}
		col := color.RGBA{
			R: uint8((rgb >> 16) & 0xFF),
			G: uint8((rgb >> 8) & 0xFF),
			B: uint8(rgb & 0xFF),
			A: alpha,
		}
		fontSize := clampDanmakuFontSize(it.FontSize)

		switch it.Mode {
		case 4: // Bottom pinned
			if elapsed > DanmakuFixedDuration {
				continue
			}
			track := -1
			for tr := 0; tr < maxTracks; tr++ {
				if !bottomOccupied[tr] {
					track = tr
					bottomOccupied[tr] = true
					break
				}
			}
			if track < 0 {
				continue
			}
			x := (viewportW - it.WidthPx) * 0.5
			if x < 8 {
				x = 8
			}
			y := viewportH - float32(track+1)*DanmakuTrackHeight - 8
			if y < 6 {
				y = 6
			}
			out = append(out, PositionedDanmaku{
				ID:       it.ID,
				TimeSec:  it.TimeSec,
				Text:     it.Text,
				X:        x,
				Y:        y,
				Width:    it.WidthPx,
				Height:   DanmakuTrackHeight,
				FontSize: fontSize,
				Color:    col,
				Mode:     it.Mode,
			})

		case 5: // Top pinned
			if elapsed > DanmakuFixedDuration {
				continue
			}
			track := -1
			for tr := 0; tr < maxTracks; tr++ {
				if !topOccupied[tr] {
					track = tr
					topOccupied[tr] = true
					break
				}
			}
			if track < 0 {
				continue
			}
			x := (viewportW - it.WidthPx) * 0.5
			if x < 8 {
				x = 8
			}
			y := 6 + float32(track)*DanmakuTrackHeight
			out = append(out, PositionedDanmaku{
				ID:       it.ID,
				TimeSec:  it.TimeSec,
				Text:     it.Text,
				X:        x,
				Y:        y,
				Width:    it.WidthPx,
				Height:   DanmakuTrackHeight,
				FontSize: fontSize,
				Color:    col,
				Mode:     it.Mode,
			})

		case 1, 2, 3, 6: // Rolling
			if elapsed > DanmakuRollDuration {
				continue
			}
			totalDist := viewportW + it.WidthPx
			progress := float32(elapsed / DanmakuRollDuration)
			x := viewportW - totalDist*progress
			if x+it.WidthPx < 0 || x > viewportW {
				continue
			}
			track := -1
			for tr := 0; tr < maxTracks; tr++ {
				if !rollOccupied[tr] || rollRightEdge[tr]+14 <= x {
					track = tr
					break
				}
			}
			if track < 0 {
				continue
			}
			rollOccupied[track] = true
			if right := x + it.WidthPx; right > rollRightEdge[track] {
				rollRightEdge[track] = right
			}
			y := 6 + float32(track)*DanmakuTrackHeight
			out = append(out, PositionedDanmaku{
				ID:       it.ID,
				TimeSec:  it.TimeSec,
				Text:     it.Text,
				X:        x,
				Y:        y,
				Width:    it.WidthPx,
				Height:   DanmakuTrackHeight,
				FontSize: fontSize,
				Color:    col,
				Mode:     1,
			})
		}
	}
	return out
}

// NormalizeDanmakuDensity normalizes density to "low", "medium", "high", or "unlimited".
func NormalizeDanmakuDensity(density string) string {
	switch strings.ToLower(strings.TrimSpace(density)) {
	case "low", "medium", "high", "unlimited":
		return strings.ToLower(strings.TrimSpace(density))
	default:
		return "medium"
	}
}

// DanmakuDensityLabel returns the Chinese UI label for a normalized density.
func DanmakuDensityLabel(density string) string {
	switch NormalizeDanmakuDensity(density) {
	case "low":
		return "低"
	case "high":
		return "高"
	case "unlimited":
		return "不限"
	default:
		return "中"
	}
}

func danmakuPerSecond(density string) int {
	switch NormalizeDanmakuDensity(density) {
	case "low":
		return 2
	case "high":
		return 10
	case "unlimited":
		return 0
	default:
		return 5
	}
}

func estimateDanmakuWidth(s string, fontSize int) float32 {
	scale := clampDanmakuFontSize(fontSize)
	var width float32
	for _, r := range s {
		if r <= 0x7F {
			width += scale * 0.56
		} else {
			width += scale
		}
	}
	if width < 24 {
		width = 24
	}
	return width
}
