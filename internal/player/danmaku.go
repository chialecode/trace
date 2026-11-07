package player

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
	DanmakuRollDuration  = 6.0 // seconds for rolling danmaku to cross the viewport
	DanmakuFixedDuration = 4.0 // seconds for top/bottom pinned danmaku
	DanmakuTrackHeight   = 28.0
)

// DanmakuItem represents a parsed danmaku entry ready for timeline layout.
type DanmakuItem struct {
	ID         string
	TimeSec    float64
	Mode       int // 1,2,3 = rolling; 4 = bottom; 5 = top
	FontSize   int
	ColorRGB   uint32
	Text       string
	WidthPx    float32
	secondRank int
}

// PositionedDanmaku is a visible danmaku item with computed screen coordinates for the current frame.
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

// DanmakuEngine provides sub-millisecond frame layout over tens of thousands of danmaku items (PR-3, C22).
type DanmakuEngine struct {
	items []DanmakuItem
}

// NewDanmakuEngine creates a sorted DanmakuEngine from items.
func NewDanmakuEngine(items []DanmakuItem) *DanmakuEngine {
	copied := make([]DanmakuItem, len(items))
	copy(copied, items)
	for i := range copied {
		if copied[i].FontSize <= 0 {
			copied[i].FontSize = 20
		}
		if copied[i].WidthPx <= 0 {
			copied[i].WidthPx = estimateTextWidth(copied[i].Text, copied[i].FontSize)
		}
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

// ParseDanmakuXML parses standard Bilibili XML (<d p="...">text</d>) into a DanmakuEngine.
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
		if err != nil {
			continue
		}
		mode, _ := strconv.Atoi(parts[1])
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
			WidthPx:  estimateTextWidth(content, fontSize),
		})
	}
	return NewDanmakuEngine(items), nil
}

// Count returns the total number of loaded danmaku items.
func (e *DanmakuEngine) Count() int {
	if e == nil {
		return 0
	}
	return len(e.items)
}

// LayoutFrame computes all visible danmaku positions at playback time tSec.
// Uses binary search over [tSec - DanmakuRollDuration, tSec] and track collision avoidance
// so 10,000+ items take < 1ms per frame (PR-3, C22).
// Optional density limits how many original entries per second are shown.
// Selection is stable across frames and seeks; omitted density is unlimited.
func (e *DanmakuEngine) LayoutFrame(
	tSec float64,
	viewportW, viewportH float32,
	areaRatio float64,
	opacity float64,
	density ...string,
) []PositionedDanmaku {
	if e == nil || len(e.items) == 0 || viewportW <= 0 || viewportH <= 0 {
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
	if maxTracks > 36 {
		maxTracks = 36
	}

	windowStart := tSec - DanmakuRollDuration
	startIdx := sort.Search(len(e.items), func(i int) bool {
		return e.items[i].TimeSec >= windowStart
	})

	// Track occupancy: rightEdge tracks the rightmost occupied X pixel in each rolling track at tSec.
	var rollRightEdge [36]float32
	var rollOccupied [36]bool
	var topOccupied [36]bool
	var bottomOccupied [36]bool

	alpha := uint8(opacity*255.0 + 0.5)
	var out []PositionedDanmaku
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

		col := color.RGBA{
			R: uint8((it.ColorRGB >> 16) & 0xFF),
			G: uint8((it.ColorRGB >> 8) & 0xFF),
			B: uint8(it.ColorRGB & 0xFF),
			A: alpha,
		}

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
			out = append(out, PositionedDanmaku{
				ID:       it.ID,
				TimeSec:  it.TimeSec,
				Text:     it.Text,
				X:        x,
				Y:        y,
				Width:    it.WidthPx,
				Height:   DanmakuTrackHeight,
				FontSize: 15,
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
			y := 8 + float32(track)*DanmakuTrackHeight
			out = append(out, PositionedDanmaku{
				ID:       it.ID,
				TimeSec:  it.TimeSec,
				Text:     it.Text,
				X:        x,
				Y:        y,
				Width:    it.WidthPx,
				Height:   DanmakuTrackHeight,
				FontSize: 15,
				Color:    col,
				Mode:     it.Mode,
			})

		default: // Rolling (modes 1, 2, 3)
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
				if !rollOccupied[tr] || rollRightEdge[tr]+16 <= x {
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
			y := 8 + float32(track)*DanmakuTrackHeight
			out = append(out, PositionedDanmaku{
				ID:       it.ID,
				TimeSec:  it.TimeSec,
				Text:     it.Text,
				X:        x,
				Y:        y,
				Width:    it.WidthPx,
				Height:   DanmakuTrackHeight,
				FontSize: 15,
				Color:    col,
				Mode:     1,
			})
		}
	}
	return out
}

func NormalizeDanmakuDensity(density string) string {
	switch density {
	case "low", "medium", "high", "unlimited":
		return density
	default:
		return "medium"
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

func estimateTextWidth(s string, fontSize int) float32 {
	if fontSize <= 0 {
		fontSize = 16
	}
	scale := float32(15.0)
	var width float32
	for _, r := range s {
		if r <= 0x7F {
			width += scale * 0.55
		} else {
			width += scale
		}
	}
	if width < 24 {
		width = 24
	}
	return width
}
