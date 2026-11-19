package ui

import (
	"fmt"
	"math"
	"os"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
)

// Visual theme per docs/ui.md §5: white content, an airy neutral light-gray
// rail and panel, white fields and cards with hairline borders rather than
// nested gray boxes, and one fresh accent. Blue is the default;
// TRACE_UI_ACCENT=green previews the green accent.
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

var (
	themeMu           sync.Mutex
	cachedTheme       *ui.Theme
	cachedThemeAccent ui.Color
	cachedThemeFont   bool
)

// UIFont is the family text uses: Inter, bundled (internal/ui/fonts), with
// the system's Chinese font for what Inter lacks.
const UIFont = "Inter"

func init() { UseAccent(os.Getenv("TRACE_UI_ACCENT")) }

// UseAccent sets the accent: "green", or the default blue for anything
// else. Call it before building a frame.
func UseAccent(name string) {
	themeMu.Lock()
	defer themeMu.Unlock()
	if name == "green" {
		ColorAccent, ColorAccentHover, ColorAccentPress, ColorAccentBg = ui.Hex("#0a8754"), ui.Hex("#08744a"), ui.Hex("#066340"), ui.Hex("#e6f4ec")
	} else {
		ColorAccent, ColorAccentHover, ColorAccentPress, ColorAccentBg = ui.Hex("#1a73e8"), ui.Hex("#1765cc"), ui.Hex("#1557b0"), ui.Hex("#e8f0fe")
	}
	cachedTheme = nil
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

// TraceTheme returns the cached TRACE light theme (docs/ui.md §5). InactiveAccent
// = Accent keeps the accent in inactive windows and headless testers.
func TraceTheme() *ui.Theme {
	registerUIFont()
	themeMu.Lock()
	defer themeMu.Unlock()
	if cachedTheme != nil && cachedThemeAccent == ColorAccent && cachedThemeFont == fontRegistered {
		return cachedTheme
	}
	t := ui.LightTheme()
	t.Background = ColorBgMain
	t.Surface = ColorBgMain // fields and controls are white with a hairline
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
	t.Selection = ColorAccent.Alpha(0.20)
	t.Focus = ColorAccent.Alpha(0.50)
	t.FocusRingWidth = 2
	t.FocusRingOffset = 1
	t.Scrollbar = ui.RGBA(38, 38, 38, 0.24)
	t.Radius = 8
	t.Spacing = 4
	t.FontSize = 14
	if fontRegistered {
		t.Font = UIFont
	}
	cachedTheme = t
	cachedThemeAccent = ColorAccent
	cachedThemeFont = fontRegistered
	return t
}

// renderErrorBanner renders a full-width inline error banner with an alert icon,
// message text, and optional trailing actions.
func renderErrorBanner(c *ui.Context, label, text string, actions func()) {
	row := ui.Row(c).
		FillWidth().
		Padding(10, 14).
		Radius(10).
		Background(ColorErrorBg).
		Border(1, ui.RGBA(180, 35, 24, 0.22)).
		AlignItems(ui.Center).
		Justify(ui.SpaceBetween).
		Gap(10)
	if label != "" {
		row.Label(label)
	}
	row.Children(func() {
		ui.Row(c).Grow(1).Shrink(1).AlignItems(ui.Center).Gap(8).Children(func() {
			DrawIcon(c, "alert-circle", 16, ColorError, 1.8)
			ui.Text(c, text).FontSize(13).TextColor(ColorError).MaxLines(3).Grow(1).Shrink(1)
		})
		if actions != nil {
			ui.Row(c).Shrink(0).AlignItems(ui.Center).Gap(6).Children(actions)
		}
	})
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
// popover, as the menus of the system do (docs/ui.md §1): its items
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

// FloatingCircleButton renders a semi-transparent circular floating button for dark immersive overlays (docs/ui.md §3.4/§3.5).
func FloatingCircleButton(c *ui.Context, icon string, label string, size float32) ui.Element {
	if size <= 0 {
		size = 40
	}
	btn := ui.Button(c, "").
		Label(label).
		Tooltip(label).
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

// FloatingPillButton renders a semi-transparent capsule floating button for dark immersive overlays.
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
// and a 4px rounded accent bottom indicator bar (docs/ui.md §3, §5).
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
					}, ColorAccent, 2)
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

// DrawStatusDot renders a status mark per docs/ui.md §3.3 & §5.
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
	isPaused := paused || status == "paused" || strings.HasPrefix(status, "paused_")
	if label == "" {
		switch {
		case statusInProgress(status):
			label = fmt.Sprintf("正在缓存 %.0f%%", math.Round(progress*100))
		case status == "green" || status == "completed" || status == "complete":
			label = "已缓存，完整"
		case isPaused:
			label = "已暂停"
		case status == "upgrade":
			label = "可升级画质"
		case status == "yellow" || status == "partial":
			label = "部分缓存"
		case status == "red" || status == "failed" || status == "missing" || status == "missing_files" || status == "corrupt":
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
		switch {
		case statusInProgress(status):
			track := (&ui.Path{}).Circle(cx, cy, 5.2)
			p.StrokePath(track, 2.0, ColorBorder)
			frac := float32(max(0.12, min(0.95, progress)))
			startAng := float32(-math.Pi / 2)
			endAng := startAng + frac*float32(2*math.Pi)
			arc := buildArcPath(cx, cy, 5.2, startAng, endAng)
			p.StrokePath(arc, 2.0, ColorInfoBlue)

		case status == "green" || status == "completed" || status == "complete":
			p.Fill(dotRect(r), ColorDotGreen, 4)

		case isPaused || status == "yellow" || status == "partial" || status == "upgrade":
			p.Fill(dotRect(r), ColorDotYellow, 4)

		case status == "red" || status == "failed" || status == "missing" || status == "missing_files" || status == "corrupt":
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

// DrawNavIcon draws a 22x22 linear vector icon for the left navigation rail (docs/ui.md §2).
func DrawNavIcon(c *ui.Context, icon string, active bool) ui.Element {
	col := ColorTextMuted
	sw := float32(1.6)
	if active {
		col = ColorAccent
		sw = 2
	}
	return DrawIcon(c, icon, 22, col, sw)
}
