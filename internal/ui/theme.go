package ui

import (
	"fmt"
	"math"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

// Visual theme per docs/ui.md §5 (DEC-039, DEC-046 ③): white content, an airy
// neutral light-gray rail and panel, white fields and cards with hairline
// borders rather than nested gray boxes, and one fresh accent. Blue is
// the default; TRACE_UI_ACCENT=green previews the green accent (Q-011).
var (
	ColorBgMain       = ui.Hex("#ffffff") // content
	ColorBgPanel      = ui.Hex("#f7f7f7") // icon rail and context panel
	ColorBgSecondary  = ui.Hex("#f4f4f4") // placeholders, small notices and chips on white
	ColorBgRail       = ColorBgPanel
	ColorHover        = ui.RGBA(38, 38, 38, 0.06)
	ColorPressed      = ui.RGBA(38, 38, 38, 0.10)
	ColorTextMain     = ui.Hex("#262626")
	ColorTextMuted    = ui.Hex("#6b6b6b") // 4.9:1 on the panel gray, 5.3:1 on white
	ColorTextFaint    = ui.Hex("#a3a3a3")
	ColorBorder       = ui.Hex("#ececec")
	ColorBorderStrong = ui.Hex("#dcdcdc")

	ColorAccent      = ui.Hex("#1a73e8")
	ColorAccentHover = ui.Hex("#1765cc")
	ColorAccentPress = ui.Hex("#1557b0")
	ColorAccentBg    = ui.Hex("#e8f0fe")

	ColorInfoBlue      = ui.Hex("#1a73e8")
	ColorInfoBlueHover = ui.Hex("#1765cc")
	ColorInfoBlueBg    = ui.Hex("#e8f0fe")
	ColorDotGreen      = ui.Hex("#2aae5b")
	ColorDotYellow     = ui.Hex("#f3a115")
	ColorDotRed        = ui.Hex("#d91f37")
	ColorLive          = ui.Hex("#c2410c") // "live now" marks and rings
	ColorLiveBg        = ui.Hex("#fff1e8")
	ColorError         = ui.Hex("#c4182e")
	ColorErrorBg       = ui.Hex("#fdf0f1")
	ColorWarnBg        = ui.Hex("#fff8ea")
	ColorWhite         = ui.Hex("#ffffff")
	ColorBlack         = ui.Hex("#000000")
)

// UIFont is the family text uses: Inter, bundled (internal/ui/fonts), with
// the system's Chinese font for what Inter lacks.
const UIFont = "Inter"

func init() { UseAccent(os.Getenv("TRACE_UI_ACCENT")) }

// UseAccent sets the accent: "green", or the default blue for anything
// else. Call it before building a frame.
func UseAccent(name string) {
	if name == "green" {
		ColorAccent, ColorAccentHover, ColorAccentPress, ColorAccentBg = ui.Hex("#0a8754"), ui.Hex("#08744a"), ui.Hex("#066340"), ui.Hex("#e6f4ec")
		return
	}
	ColorAccent, ColorAccentHover, ColorAccentPress, ColorAccentBg = ui.Hex("#1a73e8"), ui.Hex("#1765cc"), ui.Hex("#1557b0"), ui.Hex("#e8f0fe")
}

// AvatarPalette provides deterministic, high-contrast background colors for creator/account avatars.
var AvatarPalette = []ui.Color{
	ui.Hex("#1a73e8"),
	ui.Hex("#0d9488"),
	ui.Hex("#4f46e5"),
	ui.Hex("#d97706"),
	ui.Hex("#15803d"),
	ui.Hex("#0891b2"),
	ui.Hex("#7c3aed"),
	ui.Hex("#475569"),
}

// TraceTheme returns the TRACE light theme (docs/ui.md §5). InactiveAccent
// = Accent keeps the accent in inactive windows and headless testers.
func TraceTheme() *ui.Theme {
	registerUIFont()
	t := ui.LightTheme()
	t.Background = ColorBgMain
	t.Surface = ColorBgMain // fields and controls are white with a hairline (DEC-046 ③)
	t.SurfaceHover = ColorHover
	t.SurfacePressed = ColorPressed
	t.Border = ColorBorderStrong
	t.Text = ColorTextMain
	t.TextMuted = ColorTextMuted
	t.Accent = ColorAccent
	t.AccentHover = ColorAccentHover
	t.AccentPressed = ColorAccentPress
	t.AccentText = ColorWhite
	t.InactiveAccent = ColorAccent
	t.Danger = ColorError
	t.Warning = ColorDotYellow
	t.Success = ColorDotGreen
	t.Selection = ui.RGBA(26, 115, 232, 0.20)
	t.Focus = ui.RGBA(26, 115, 232, 0.50)
	t.FocusRingWidth = 2
	t.FocusRingOffset = 1
	t.Scrollbar = ui.RGBA(38, 38, 38, 0.24)
	t.Radius = 8
	t.Spacing = 4
	t.FontSize = 14
	if fontRegistered {
		t.Font = UIFont
	}
	return t
}

// AccentPillButton renders a primary capsule button: accent fill, white text.
func AccentPillButton(c *ui.Context, text string, label string) ui.Element {
	if label == "" {
		label = text
	}
	btn := ui.PrimaryButton(c, "").
		Label(label).
		Radius(999).
		Padding(7, 16).
		Background(ColorAccent).
		TextColor(ColorWhite)
	btn.Children(func() {
		ui.Text(c, text).FontSize(14).FontWeight(600).TextColor(ColorWhite).SingleLine()
	})
	return btn
}

// MoreMenuButton draws the round "⋯" button whose menu floats beside it as a
// popover, as the menus of the system do (docs/ui.md §1, F-056): its items
// never take room in the page. open, when not nil, lets the app open and
// close the menu, and reports whether it shows.
func MoreMenuButton(c *ui.Context, label string, size float32, open *bool, build func(m *ui.Menu)) ui.Element {
	btn := ui.ButtonBase(c).
		Label(label).
		Tooltip("更多操作").
		Size(size, size).
		Radius(size / 2).
		Shrink(0).
		Center()
	if btn.Hovered() || open != nil && *open {
		btn.Background(ColorHover)
	}
	btn.Children(func() {
		DrawIcon(c, "more", size*0.56, ColorTextMuted, 1.8)
	})
	btn.PopoverMenuButton(build, ui.PopoverMenuButtonOptions{At: ui.AnchorBottomEnd, Self: ui.AnchorTopEnd, Open: open})
	return btn
}

// keyedMenuOpen keeps which one of many "⋯" menus is open in *current.
func keyedMenuOpen(current *string, key string, open bool) {
	if open {
		*current = key
	} else if *current == key {
		*current = ""
	}
}

// OutlinePillButton renders a white-background, gray-outlined secondary capsule button (docs/ui.md §5).
func OutlinePillButton(c *ui.Context, text string, label string) ui.Element {
	if label == "" {
		label = text
	}
	btn := ui.Button(c, "").
		Label(label).
		Radius(999).
		Padding(6, 14).
		Background(ColorBgMain).
		Border(1, ColorBorder).
		TextColor(ColorTextMain)
	btn.Children(func() {
		ui.Text(c, text).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
	})
	return btn
}

// AccentOutlinePillButton renders a capsule button outlined in the accent.
func AccentOutlinePillButton(c *ui.Context, text string, label string) ui.Element {
	if label == "" {
		label = text
	}
	btn := ui.Button(c, "").
		Label(label).
		Radius(999).
		Padding(6, 14).
		Background(ColorBgMain).
		Border(1, ColorAccent).
		TextColor(ColorAccent)
	btn.Children(func() {
		ui.Text(c, text).FontSize(13).FontWeight(600).TextColor(ColorAccent).SingleLine()
	})
	return btn
}

// DangerOutlinePillButton renders a red-outlined capsule button for destructive actions.
func DangerOutlinePillButton(c *ui.Context, text string, label string) ui.Element {
	if label == "" {
		label = text
	}
	btn := ui.Button(c, "").
		Label(label).
		Radius(999).
		Padding(6, 14).
		Background(ColorBgMain).
		Border(1, ui.RGBA(180, 35, 24, 0.35)).
		TextColor(ColorError)
	btn.Children(func() {
		ui.Text(c, text).FontSize(13).FontWeight(600).TextColor(ColorError).SingleLine()
	})
	return btn
}

// FloatingCircleButton renders a semi-transparent circular floating button for dark immersive overlays (R6, docs/ui.md §3.4/§3.5).
func FloatingCircleButton(c *ui.Context, icon string, label string, size float32) ui.Element {
	if size <= 0 {
		size = 40
	}
	btn := ui.Button(c, "").
		Label(label).
		Size(size, size).
		Shrink(0).
		Radius(size/2).
		Padding(0, 0).
		Background(ui.RGBA(255, 255, 255, 0.16)).
		Border(1, ui.RGBA(255, 255, 255, 0.24)).
		Justify(ui.Center).
		AlignItems(ui.Center)
	if btn.Hovered() {
		btn.Background(ui.RGBA(255, 255, 255, 0.28))
	}
	btn.Children(func() {
		if strings.HasPrefix(icon, "text:") {
			ui.Text(c, strings.TrimPrefix(icon, "text:")).FontSize(12).FontWeight(600).TextColor(ColorWhite).SingleLine()
		} else {
			DrawIcon(c, icon, size*0.48, ColorWhite, 2.0)
		}
	})
	return btn
}

// FloatingPillButton renders a semi-transparent capsule floating button for dark immersive overlays (R6).
func FloatingPillButton(c *ui.Context, icon string, text string, label string) ui.Element {
	if label == "" {
		label = text
	}
	btn := ui.Button(c, "").
		Label(label).
		Radius(999).
		Padding(7, 14).
		Background(ui.RGBA(255, 255, 255, 0.16)).
		Border(1, ui.RGBA(255, 255, 255, 0.24)).
		Justify(ui.Center).
		AlignItems(ui.Center)
	if btn.Hovered() {
		btn.Background(ui.RGBA(255, 255, 255, 0.28))
	}
	btn.Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			if icon != "" {
				DrawIcon(c, icon, 15, ColorWhite, 1.85)
			}
			if text != "" {
				ui.Text(c, text).FontSize(12).FontWeight(600).TextColor(ColorWhite).SingleLine()
			}
		})
	})
	return btn
}

// TabOption describes a tab item in UnderlineTabs.
type TabOption struct {
	Key         string
	Text        string
	AccessLabel string
}

// UnderlineTabs renders a social-network style tab bar where the selected tab has bold text
// and a 4px rounded blue (#1565c0) bottom indicator bar (docs/ui.md §3, §5).
func UnderlineTabs(c *ui.Context, activeKey string, items []TabOption, onSelect func(key string)) {
	ui.Row(c).FillWidth().BorderWidth(0, 0, 1, 0).BorderColor(ColorBorder).Background(ColorBgMain).ClipX().Children(func() {
		for _, item := range items {
			opt := item
			isActive := opt.Key == activeKey
			acc := opt.AccessLabel
			if acc == "" {
				acc = opt.Text
			}
			bg := ColorBgMain
			btn := ui.ButtonBase(c).
				Label(acc).
				Grow(1).
				Padding(11, 8).
				Background(bg).
				Justify(ui.Center).
				AlignItems(ui.Center)
			if btn.Hovered() {
				btn.Background(ColorHover)
			}
			btn.DrawOver(func(p *ui.Painter, r ui.Rect) {
				if isActive {
					barW := min(max(r.W*0.48, 36), 88)
					p.Fill(ui.Rect{
						X: r.X + (r.W-barW)/2,
						Y: r.Y + r.H - 4,
						W: barW,
						H: 4,
					}, ColorInfoBlue, 2)
				}
			})
			btn.Children(func() {
				txt := ui.Text(c, opt.Text).FontSize(14).SingleLine()
				if isActive {
					txt.FontWeight(600).TextColor(ColorTextMain)
				} else {
					txt.TextColor(ColorTextMuted)
				}
			})
			if btn.Clicked() && onSelect != nil {
				onSelect(opt.Key)
			}
		}
	})
}

func buildArcPath(cx, cy, radius, startAngle, endAngle float32) *ui.Path {
	path := &ui.Path{}
	steps := 20
	for i := 0; i <= steps; i++ {
		ang := float64(startAngle) + (float64(endAngle-startAngle)*float64(i))/float64(steps)
		px := cx + radius*float32(math.Cos(ang))
		py := cy + radius*float32(math.Sin(ang))
		if i == 0 {
			path.MoveTo(px, py)
		} else {
			path.LineTo(px, py)
		}
	}
	return path
}

// DrawStatusDot renders a 14x14 status dot per docs/ui.md §3.3 & §5.
func DrawStatusDot(c *ui.Context, status string, label string) ui.Element {
	paused := status == "paused" || strings.HasPrefix(status, "paused_")
	prog := 0.35
	if status == "running" || status == "downloading" || status == "recording" {
		prog = 0.6
	}
	return DrawStatusDotFull(c, status, prog, paused, label)
}

// DrawStatusDotFull renders a status mark: a progress ring while caching,
// otherwise a plain 8px dot, green when complete, yellow when partial, paused
// or upgradable, red when failed (docs/ui.md §3.3). The meaning is in the
// label and tooltip; the dot carries no symbol.
func DrawStatusDotFull(c *ui.Context, status string, progress float64, paused bool, label string) ui.Element {
	if status == "" || status == "none" {
		return ui.Box(c).Size(0, 0)
	}
	if label == "" {
		switch status {
		case "ring", "progress", "running", "queued", "downloading", "muxing", "recording":
			label = fmt.Sprintf("正在缓存 %.0f%%", math.Round(progress*100))
		case "green", "completed", "complete":
			label = "已缓存，完整"
		case "yellow", "partial", "paused", "upgrade":
			if paused || status == "paused" {
				label = "已暂停"
			} else if status == "upgrade" {
				label = "可升级画质"
			} else {
				label = "部分缓存"
			}
		case "red", "failed", "missing", "missing_files", "corrupt":
			label = "失败或文件缺失"
		default:
			label = "状态：" + status
		}
	}
	size := float32(14)
	if !statusInProgress(status) {
		size = 10
	}
	return ui.Box(c).Size(size, size).Shrink(0).Label(label).Tooltip(label).Draw(func(p *ui.Painter, r ui.Rect) {
		cx, cy := r.X+r.W/2, r.Y+r.H/2
		switch status {
		case "ring", "progress", "running", "queued", "downloading", "muxing", "recording":
			track := (&ui.Path{}).Circle(cx, cy, 5.2)
			p.StrokePath(track, 2.0, ColorBorder)
			frac := float32(max(0.12, min(0.95, progress)))
			startAng := float32(-math.Pi / 2)
			endAng := startAng + frac*float32(2*math.Pi)
			arc := buildArcPath(cx, cy, 5.2, startAng, endAng)
			p.StrokePath(arc, 2.0, ColorInfoBlue)

		case "green", "completed", "complete":
			p.Fill(dotRect(r), ColorDotGreen, 4)

		case "yellow", "partial", "paused", "upgrade":
			p.Fill(dotRect(r), ColorDotYellow, 4)

		case "red", "failed", "missing", "missing_files", "corrupt":
			p.Fill(dotRect(r), ColorDotRed, 4)

		default:
			p.Fill(dotRect(r), ColorTextMuted, 4)
		}
	})
}

// statusInProgress reports the statuses drawn as a progress ring.
func statusInProgress(status string) bool {
	switch status {
	case "ring", "progress", "running", "queued", "downloading", "muxing", "recording":
		return true
	}
	return false
}

// dotRect is the 8px dot centred in r.
func dotRect(r ui.Rect) ui.Rect {
	return ui.Rect{X: r.X + r.W/2 - 4, Y: r.Y + r.H/2 - 4, W: 8, H: 8}
}

// DrawNavIcon draws a 22x22 linear vector icon for the left navigation rail (DEC-014, docs/ui.md §2).
func DrawNavIcon(c *ui.Context, icon string, active bool) ui.Element {
	col := ColorTextMuted
	sw := float32(1.6)
	if active {
		col = ColorAccent
		sw = 2
	}
	return DrawIcon(c, icon, 22, col, sw)
}

// DrawIcon draws a unified linear vector icon scaled to size x size DIPs.
func DrawIcon(c *ui.Context, name string, size float32, col ui.Color, strokeWidth float32) ui.Element {
	if size <= 0 {
		size = 18
	}
	if strokeWidth <= 0 {
		strokeWidth = 1.75
	}
	return ui.Box(c).Size(size, size).Shrink(0).Draw(func(p *ui.Painter, r ui.Rect) {
		s := func(v float32) float32 { return v * r.W / 24.0 }
		x := func(v float32) float32 { return r.X + s(v) }
		y := func(v float32) float32 { return r.Y + s(v) }

		switch name {
		case "home":
			roof := (&ui.Path{}).
				MoveTo(x(3), y(10.5)).
				LineTo(x(12), y(3)).
				LineTo(x(21), y(10.5))
			p.StrokePath(roof, strokeWidth, col)
			walls := (&ui.Path{}).
				MoveTo(x(5), y(9.5)).
				LineTo(x(5), y(20)).
				LineTo(x(19), y(20)).
				LineTo(x(19), y(9.5))
			p.StrokePath(walls, strokeWidth, col)
			door := (&ui.Path{}).
				MoveTo(x(9.5), y(20)).
				LineTo(x(9.5), y(13.5)).
				LineTo(x(14.5), y(13.5)).
				LineTo(x(14.5), y(20))
			p.StrokePath(door, strokeWidth, col)

		case "subscriptions", "user":
			head := (&ui.Path{}).Circle(x(12), y(8), s(4))
			p.StrokePath(head, strokeWidth, col)
			body := (&ui.Path{}).
				MoveTo(x(4.5), y(20)).
				QuadTo(x(4.5), y(14.5), x(12), y(14.5)).
				QuadTo(x(19.5), y(14.5), x(19.5), y(20))
			p.StrokePath(body, strokeWidth, col)

		case "parse", "search":
			lens := (&ui.Path{}).Circle(x(11), y(11), s(6.8))
			p.StrokePath(lens, strokeWidth, col)
			handle := (&ui.Path{}).
				MoveTo(x(16), y(16)).
				LineTo(x(21), y(21))
			p.StrokePath(handle, strokeWidth, col)

		case "library":
			rect := (&ui.Path{}).
				MoveTo(x(3.5), y(5)).
				LineTo(x(20.5), y(5)).
				LineTo(x(20.5), y(19)).
				LineTo(x(3.5), y(19)).
				Close()
			p.StrokePath(rect, strokeWidth, col)
			tri := (&ui.Path{}).
				MoveTo(x(10), y(9)).
				LineTo(x(15.5), y(12)).
				LineTo(x(10), y(15)).
				Close()
			p.StrokePath(tri, strokeWidth, col)

		case "tasks", "download":
			arrow := (&ui.Path{}).
				MoveTo(x(12), y(3.5)).
				LineTo(x(12), y(15)).
				MoveTo(x(7.5), y(10.5)).
				LineTo(x(12), y(15)).
				LineTo(x(16.5), y(10.5)).
				MoveTo(x(4), y(19.5)).
				LineTo(x(20), y(19.5))
			p.StrokePath(arrow, strokeWidth, col)

		case "settings":
			// 8-tooth gear polygon with center axle hole (R3: never draw concentric target circles).
			gear := &ui.Path{}
			const teeth = 8
			outerR := s(9.2)
			innerR := s(6.8)
			cx, cy := x(12), y(12)
			for i := 0; i < teeth; i++ {
				base := float64(i) * (2 * math.Pi / float64(teeth))
				a0 := base - 0.19
				a1 := base - 0.11
				a2 := base + 0.11
				a3 := base + 0.19
				p0x, p0y := cx+innerR*float32(math.Cos(a0)), cy+innerR*float32(math.Sin(a0))
				p1x, p1y := cx+outerR*float32(math.Cos(a1)), cy+outerR*float32(math.Sin(a1))
				p2x, p2y := cx+outerR*float32(math.Cos(a2)), cy+outerR*float32(math.Sin(a2))
				p3x, p3y := cx+innerR*float32(math.Cos(a3)), cy+innerR*float32(math.Sin(a3))
				if i == 0 {
					gear.MoveTo(p0x, p0y)
				} else {
					gear.LineTo(p0x, p0y)
				}
				gear.LineTo(p1x, p1y).LineTo(p2x, p2y).LineTo(p3x, p3y)
			}
			gear.Close()
			axle := (&ui.Path{}).Circle(cx, cy, s(2.8))
			p.StrokePath(gear, strokeWidth*0.95, col)
			p.StrokePath(axle, strokeWidth, col)

		case "sliders", "general":
			// Horizontal adjustment sliders for General settings (R3).
			tracks := (&ui.Path{}).
				MoveTo(x(4), y(6.5)).LineTo(x(20), y(6.5)).
				MoveTo(x(4), y(12)).LineTo(x(20), y(12)).
				MoveTo(x(4), y(17.5)).LineTo(x(20), y(17.5)).
				MoveTo(x(9), y(4.2)).LineTo(x(9), y(8.8)).
				MoveTo(x(15), y(9.7)).LineTo(x(15), y(14.3)).
				MoveTo(x(11), y(15.2)).LineTo(x(11), y(19.8))
			p.StrokePath(tracks, strokeWidth, col)

		case "logs", "terminal":
			frame := (&ui.Path{}).
				MoveTo(x(4), y(4.5)).
				LineTo(x(20), y(4.5)).
				LineTo(x(20), y(19.5)).
				LineTo(x(4), y(19.5)).
				Close()
			prompt := (&ui.Path{}).
				MoveTo(x(7.5), y(9)).
				LineTo(x(10.5), y(11.5)).
				LineTo(x(7.5), y(14)).
				MoveTo(x(12), y(14)).
				LineTo(x(16.5), y(14))
			p.StrokePath(frame, strokeWidth, col)
			p.StrokePath(prompt, strokeWidth, col)

		case "arrow-left":
			path := (&ui.Path{}).
				MoveTo(x(19), y(12)).
				LineTo(x(5), y(12)).
				MoveTo(x(11.5), y(5.5)).
				LineTo(x(5), y(12)).
				LineTo(x(11.5), y(18.5))
			p.StrokePath(path, strokeWidth, col)

		case "arrow-up":
			path := (&ui.Path{}).
				MoveTo(x(12), y(19)).
				LineTo(x(12), y(5)).
				MoveTo(x(6), y(11)).
				LineTo(x(12), y(5)).
				LineTo(x(18), y(11))
			p.StrokePath(path, strokeWidth, col)

		case "link":
			path := (&ui.Path{}).
				MoveTo(x(9.5), y(16.5)).
				LineTo(x(7.5), y(16.5)).
				QuadTo(x(3), y(16.5), x(3), y(12)).
				QuadTo(x(3), y(7.5), x(7.5), y(7.5)).
				LineTo(x(9.5), y(7.5)).
				MoveTo(x(14.5), y(7.5)).
				LineTo(x(16.5), y(7.5)).
				QuadTo(x(21), y(7.5), x(21), y(12)).
				QuadTo(x(21), y(16.5), x(16.5), y(16.5)).
				LineTo(x(14.5), y(16.5)).
				MoveTo(x(8.5), y(12)).
				LineTo(x(15.5), y(12))
			p.StrokePath(path, strokeWidth, col)

		case "check-circle":
			circ := (&ui.Path{}).Circle(x(12), y(12), s(8.5))
			p.StrokePath(circ, strokeWidth, col)
			chk := (&ui.Path{}).
				MoveTo(x(8), y(12.2)).
				LineTo(x(10.8), y(15)).
				LineTo(x(16.2), y(9.2))
			p.StrokePath(chk, strokeWidth, col)

		case "play":
			tri := (&ui.Path{}).
				MoveTo(x(7), y(5)).
				LineTo(x(19), y(12)).
				LineTo(x(7), y(19)).
				Close()
			p.StrokePath(tri, strokeWidth, col)

		case "pause":
			bars := (&ui.Path{}).
				MoveTo(x(8.5), y(5)).LineTo(x(8.5), y(19)).
				MoveTo(x(15.5), y(5)).LineTo(x(15.5), y(19))
			p.StrokePath(bars, strokeWidth*1.2, col)

		case "refresh":
			arc := buildArcPath(x(12), y(12), s(7.5), -2.4, 2.2)
			p.StrokePath(arc, strokeWidth, col)
			head := (&ui.Path{}).
				MoveTo(x(16), y(4.5)).
				LineTo(x(19.5), y(8)).
				LineTo(x(15), y(9))
			p.StrokePath(head, strokeWidth, col)

		case "image":
			p.Stroke(ui.Rect{X: x(3), Y: y(4), W: s(18), H: s(16)}, col, s(3), strokeWidth)
			hill := (&ui.Path{}).
				MoveTo(x(3.5), y(17.5)).
				LineTo(x(9), y(12)).
				LineTo(x(13.5), y(16.5)).
				LineTo(x(16), y(14)).
				LineTo(x(20.5), y(18.5))
			p.StrokePath(hill, strokeWidth, col)
			p.Stroke(ui.Rect{X: x(14), Y: y(7.5), W: s(3), H: s(3)}, col, s(1.5), strokeWidth)

		case "video":
			p.Stroke(ui.Rect{X: x(3), Y: y(5), W: s(18), H: s(14)}, col, s(3), strokeWidth)
			tri := (&ui.Path{}).
				MoveTo(x(10), y(9)).
				LineTo(x(15), y(12)).
				LineTo(x(10), y(15)).
				Close()
			p.StrokePath(tri, strokeWidth, col)

		case "live":
			p.Stroke(ui.Rect{X: x(10), Y: y(10), W: s(4), H: s(4)}, col, s(2), strokeWidth)
			for _, r := range []float32{5.5, 9} {
				p.StrokePath(buildArcPath(x(12), y(12), s(r), -0.75, 0.75), strokeWidth, col)
				p.StrokePath(buildArcPath(x(12), y(12), s(r), math.Pi-0.75, math.Pi+0.75), strokeWidth, col)
			}

		case "grid":
			for _, o := range [][2]float32{{4, 4}, {13, 4}, {4, 13}, {13, 13}} {
				p.Stroke(ui.Rect{X: x(o[0]), Y: y(o[1]), W: s(7), H: s(7)}, col, s(1.8), strokeWidth)
			}

		case "folder":
			path := (&ui.Path{}).
				MoveTo(x(3), y(6)).
				LineTo(x(9.5), y(6)).
				LineTo(x(11.5), y(8.5)).
				LineTo(x(21), y(8.5)).
				LineTo(x(21), y(19)).
				LineTo(x(3), y(19)).
				Close()
			p.StrokePath(path, strokeWidth, col)

		case "external-link":
			path := (&ui.Path{}).
				MoveTo(x(14), y(4)).
				LineTo(x(20), y(4)).
				LineTo(x(20), y(10)).
				MoveTo(x(10), y(14)).
				LineTo(x(20), y(4)).
				MoveTo(x(18), y(13.5)).
				LineTo(x(18), y(19.5)).
				LineTo(x(4.5), y(19.5)).
				LineTo(x(4.5), y(6)).
				LineTo(x(10.5), y(6))
			p.StrokePath(path, strokeWidth, col)

		case "copy":
			path := (&ui.Path{}).
				MoveTo(x(9), y(9)).
				LineTo(x(20), y(9)).
				LineTo(x(20), y(20)).
				LineTo(x(9), y(20)).
				Close().
				MoveTo(x(15), y(9)).
				LineTo(x(15), y(4.5)).
				LineTo(x(4.5), y(4.5)).
				LineTo(x(4.5), y(15)).
				LineTo(x(9), y(15))
			p.StrokePath(path, strokeWidth, col)

		case "trash":
			path := (&ui.Path{}).
				MoveTo(x(4), y(6.5)).LineTo(x(20), y(6.5)).
				MoveTo(x(9), y(6.5)).LineTo(x(9), y(4)).LineTo(x(15), y(4)).LineTo(x(15), y(6.5)).
				MoveTo(x(6.5), y(6.5)).LineTo(x(6.5), y(20)).LineTo(x(17.5), y(20)).LineTo(x(17.5), y(6.5)).
				MoveTo(x(10), y(10.5)).LineTo(x(10), y(16.5)).
				MoveTo(x(14), y(10.5)).LineTo(x(14), y(16.5))
			p.StrokePath(path, strokeWidth, col)

		case "eye":
			eye := (&ui.Path{}).
				MoveTo(x(2.5), y(12)).
				QuadTo(x(12), y(4.5), x(21.5), y(12)).
				QuadTo(x(12), y(19.5), x(2.5), y(12))
			pupil := (&ui.Path{}).Circle(x(12), y(12), s(2.8))
			p.StrokePath(eye, strokeWidth, col)
			p.StrokePath(pupil, strokeWidth, col)

		case "heart":
			path := (&ui.Path{}).
				MoveTo(x(12), y(20)).
				LineTo(x(4.5), y(12.5)).
				QuadTo(x(2), y(8), x(6.5), y(5.5)).
				QuadTo(x(10), y(4), x(12), y(7.5)).
				QuadTo(x(14), y(4), x(17.5), y(5.5)).
				QuadTo(x(22), y(8), x(19.5), y(12.5)).
				Close()
			p.StrokePath(path, strokeWidth, col)

		case "coin":
			c1 := (&ui.Path{}).Circle(x(12), y(12), s(8.2))
			c2 := (&ui.Path{}).
				MoveTo(x(9.2), y(7.8)).LineTo(x(12), y(11.2)).LineTo(x(14.8), y(7.8)).
				MoveTo(x(9.2), y(11.8)).LineTo(x(14.8), y(11.8)).
				MoveTo(x(9.2), y(14.4)).LineTo(x(14.8), y(14.4)).
				MoveTo(x(12), y(11.2)).LineTo(x(12), y(17))
			p.StrokePath(c1, strokeWidth, col)
			p.StrokePath(c2, strokeWidth*0.95, col)

		case "star":
			path := (&ui.Path{}).
				MoveTo(x(12), y(3.5)).
				LineTo(x(14.6), y(9)).
				LineTo(x(20.5), y(9.8)).
				LineTo(x(16.2), y(14)).
				LineTo(x(17.3), y(20)).
				LineTo(x(12), y(17)).
				LineTo(x(6.7), y(20)).
				LineTo(x(7.8), y(14)).
				LineTo(x(3.5), y(9.8)).
				LineTo(x(9.4), y(9)).
				Close()
			p.StrokePath(path, strokeWidth, col)

		case "message":
			path := (&ui.Path{}).
				MoveTo(x(4), y(5)).
				LineTo(x(20), y(5)).
				LineTo(x(20), y(16.5)).
				LineTo(x(9), y(16.5)).
				LineTo(x(4), y(20.5)).
				Close()
			p.StrokePath(path, strokeWidth, col)

		case "share":
			c1 := (&ui.Path{}).Circle(x(18), y(5.5), s(2.5))
			c2 := (&ui.Path{}).Circle(x(6), y(12), s(2.5))
			c3 := (&ui.Path{}).Circle(x(18), y(18.5), s(2.5))
			lines := (&ui.Path{}).
				MoveTo(x(8.3), y(10.8)).LineTo(x(15.7), y(6.7)).
				MoveTo(x(8.3), y(13.2)).LineTo(x(15.7), y(17.3))
			p.StrokePath(c1, strokeWidth, col)
			p.StrokePath(c2, strokeWidth, col)
			p.StrokePath(c3, strokeWidth, col)
			p.StrokePath(lines, strokeWidth, col)

		case "alert-circle":
			circ := (&ui.Path{}).Circle(x(12), y(12), s(8.5))
			line := (&ui.Path{}).MoveTo(x(12), y(7.5)).LineTo(x(12), y(13))
			p.StrokePath(circ, strokeWidth, col)
			p.StrokePath(line, strokeWidth, col)
			p.Fill(ui.Rect{X: x(11), Y: y(15.5), W: s(2), H: s(2)}, col, s(1))

		case "chevron-down":
			path := (&ui.Path{}).
				MoveTo(x(6), y(9)).
				LineTo(x(12), y(15)).
				LineTo(x(18), y(9))
			p.StrokePath(path, strokeWidth, col)

		case "chevron-up":
			path := (&ui.Path{}).
				MoveTo(x(6), y(15)).
				LineTo(x(12), y(9)).
				LineTo(x(18), y(15))
			p.StrokePath(path, strokeWidth, col)

		case "chevron-left":
			path := (&ui.Path{}).
				MoveTo(x(15), y(6)).
				LineTo(x(9), y(12)).
				LineTo(x(15), y(18))
			p.StrokePath(path, strokeWidth, col)

		case "chevron-right":
			path := (&ui.Path{}).
				MoveTo(x(9), y(6)).
				LineTo(x(15), y(12)).
				LineTo(x(9), y(18))
			p.StrokePath(path, strokeWidth, col)

		case "close":
			path := (&ui.Path{}).
				MoveTo(x(6), y(6)).LineTo(x(18), y(18)).
				MoveTo(x(18), y(6)).LineTo(x(6), y(18))
			p.StrokePath(path, strokeWidth, col)

		case "maximize", "fullscreen":
			path := (&ui.Path{}).
				MoveTo(x(4), y(9)).LineTo(x(4), y(4)).LineTo(x(9), y(4)).
				MoveTo(x(15), y(4)).LineTo(x(20), y(4)).LineTo(x(20), y(9)).
				MoveTo(x(20), y(15)).LineTo(x(20), y(20)).LineTo(x(15), y(20)).
				MoveTo(x(9), y(20)).LineTo(x(4), y(20)).LineTo(x(4), y(15))
			p.StrokePath(path, strokeWidth, col)

		case "volume":
			cone := (&ui.Path{}).
				MoveTo(x(4.5), y(9.5)).
				LineTo(x(8.5), y(9.5)).
				LineTo(x(13.5), y(5.5)).
				LineTo(x(13.5), y(18.5)).
				LineTo(x(8.5), y(14.5)).
				LineTo(x(4.5), y(14.5)).
				Close()
			wave := buildArcPath(x(13.5), y(12), s(4.2), -0.85, 0.85)
			p.StrokePath(cone, strokeWidth, col)
			p.StrokePath(wave, strokeWidth, col)

		case "volume-mute":
			cone := (&ui.Path{}).
				MoveTo(x(4.5), y(9.5)).
				LineTo(x(8.5), y(9.5)).
				LineTo(x(13.5), y(5.5)).
				LineTo(x(13.5), y(18.5)).
				LineTo(x(8.5), y(14.5)).
				LineTo(x(4.5), y(14.5)).
				Close()
			cross := (&ui.Path{}).
				MoveTo(x(16), y(9.5)).LineTo(x(20.5), y(14.5)).
				MoveTo(x(20.5), y(9.5)).LineTo(x(16), y(14.5))
			p.StrokePath(cone, strokeWidth, col)
			p.StrokePath(cross, strokeWidth, col)

		case "more":
			p.Fill(ui.Rect{X: x(5), Y: y(10.6), W: s(2.8), H: s(2.8)}, col, s(1.4))
			p.Fill(ui.Rect{X: x(10.6), Y: y(10.6), W: s(2.8), H: s(2.8)}, col, s(1.4))
			p.Fill(ui.Rect{X: x(16.2), Y: y(10.6), W: s(2.8), H: s(2.8)}, col, s(1.4))

		default:
			circ := (&ui.Path{}).Circle(x(12), y(12), s(6))
			p.StrokePath(circ, strokeWidth, col)
		}
	})
}

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

// FormatBytes formats a byte count into human-readable B / KB / MB / GB.
func FormatBytes(b int64) string {
	if b <= 0 {
		return "0 B"
	}
	units := []string{"B", "KB", "MB", "GB", "TB"}
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

// FormatTimestamp formats a Unix timestamp (or RFC3339 string) into a readable date/time.
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
