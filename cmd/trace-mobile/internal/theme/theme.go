// Package theme provides the TRACE mobile visual tokens, Inter font registration,
// and vector icons matching docs/ui.md §3.12 and §5.
package theme

import (
	"bytes"
	_ "embed"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"sync"

	"github.com/egoist/mygo/ui"
	"golang.org/x/image/vector"
)

// Visual tokens per docs/ui.md §5 (light theme only per §3.12).
var (
	ColorBgMain       = ui.Hex("#ffffff")
	ColorBgPanel      = ui.Hex("#f7f7f7")
	ColorBgSecondary  = ui.Hex("#f4f4f4")
	ColorHover        = ui.RGBA(38, 38, 38, 0.06)
	ColorPressed      = ui.RGBA(38, 38, 38, 0.10)
	ColorTextMain     = ui.Hex("#262626")
	ColorTextMuted    = ui.Hex("#6b6b6b")
	ColorTextFaint    = ui.Hex("#a3a3a3")
	ColorBorder       = ui.Hex("#ececec")
	ColorBorderStrong = ui.Hex("#dcdcdc")

	ColorAccent      = ui.Hex("#1a73e8")
	ColorAccentHover = ui.Hex("#1765cc")
	ColorAccentPress = ui.Hex("#1557b0")
	ColorAccentBg    = ui.Hex("#e8f0fe")

	ColorDotGreen  = ui.Hex("#2aae5b")
	ColorDotYellow = ui.Hex("#f3a115")
	ColorDotRed    = ui.Hex("#d91f37")
	ColorLive      = ui.Hex("#c2410c")
	ColorLiveBg    = ui.Hex("#fff1e8")
	ColorError     = ui.Hex("#c4182e")
	ColorErrorBg   = ui.Hex("#fdf0f1")
	ColorWarnBg    = ui.Hex("#fff8ea")
	ColorWarnText  = ui.Hex("#8a5200")
	ColorWhite     = ui.Hex("#ffffff")
)

const (
	UIFont            = "Inter"
	MonoFont          = "JetBrains Mono, Cascadia Mono, Consolas"
	LineHeightBody    = float32(1.6)
	LineHeightCompact = float32(1.45)
	LineHeightHeading = float32(1.35)
)

//go:embed fonts/InterVariable.ttf
var interVariable []byte

var (
	fontOnce       sync.Once
	fontRegistered bool
	themeMu        sync.Mutex
	cachedTheme    *ui.Theme
	cachedAccent   ui.Color
	cachedFont     bool
)

func init() {
	if os.Getenv("TRACE_UI_ACCENT") == "green" {
		ColorAccent = ui.Hex("#0a8754")
		ColorAccentHover = ui.Hex("#08744a")
		ColorAccentPress = ui.Hex("#066340")
		ColorAccentBg = ui.Hex("#e6f4ec")
	}
}

func registerFont() {
	fontOnce.Do(func() {
		if len(interVariable) > 0 {
			fontRegistered = ui.RegisterFont(interVariable, UIFont) == nil
		}
	})
}

// TraceTheme returns the cached TRACE light theme for mobile views.
func TraceTheme() *ui.Theme {
	registerFont()
	themeMu.Lock()
	defer themeMu.Unlock()
	if cachedTheme != nil && cachedAccent == ColorAccent && cachedFont == fontRegistered {
		return cachedTheme
	}
	t := ui.LightTheme()
	t.Background = ColorBgMain
	t.Surface = ColorBgMain
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
	t.Radius = 10
	t.Spacing = 4
	t.FontSize = 14
	if fontRegistered {
		t.Font = UIFont
	}
	cachedTheme = t
	cachedAccent = ColorAccent
	cachedFont = fontRegistered
	return t
}

// PrimaryPillButton renders a 44dp+ touch-friendly primary capsule button (docs/ui.md §3.12).
func PrimaryPillButton(c *ui.Context, text, label string) ui.Element {
	if label == "" {
		label = text
	}
	btn := ui.PrimaryButton(c, "").
		Label(label).
		Radius(999).
		Padding(11, 20).
		MinHeight(44).
		Background(ColorAccent).
		TextColor(ColorWhite).
		Justify(ui.Center).
		AlignItems(ui.Center)
	btn.Children(func() {
		ui.Text(c, text).FontSize(14).FontWeight(600).TextColor(ColorWhite).SingleLine()
	})
	return btn
}

// SecondaryPillButton renders a 44dp+ touch-friendly white-with-hairline capsule button.
func SecondaryPillButton(c *ui.Context, text, label string) ui.Element {
	if label == "" {
		label = text
	}
	btn := ui.Button(c, "").
		Label(label).
		Radius(999).
		Padding(11, 18).
		MinHeight(44).
		Background(ColorBgMain).
		Border(1, ColorBorderStrong).
		TextColor(ColorTextMain).
		Justify(ui.Center).
		AlignItems(ui.Center)
	btn.Children(func() {
		ui.Text(c, text).FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine()
	})
	return btn
}

// DangerOutlinePillButton renders a 44dp+ destructive red-outlined capsule button.
func DangerOutlinePillButton(c *ui.Context, text, label string) ui.Element {
	if label == "" {
		label = text
	}
	btn := ui.Button(c, "").
		Label(label).
		Radius(999).
		Padding(11, 18).
		MinHeight(44).
		Background(ColorBgMain).
		Border(1, ui.RGBA(196, 24, 46, 0.35)).
		TextColor(ColorError).
		Justify(ui.Center).
		AlignItems(ui.Center)
	btn.Children(func() {
		ui.Text(c, text).FontSize(14).FontWeight(600).TextColor(ColorError).SingleLine()
	})
	return btn
}

// DrawIcon draws a linear vector icon on a 24x24 grid.
func DrawIcon(c *ui.Context, name string, size float32, col ui.Color, strokeWidth float32) ui.Element {
	if size <= 0 {
		size = 20
	}
	if strokeWidth <= 0 {
		strokeWidth = 1.8
	}
	return ui.Box(c).Size(size, size).Shrink(0).Draw(func(p *ui.Painter, r ui.Rect) {
		PaintIcon(p, r, name, col, strokeWidth)
	})
}

// PaintIcon paints a linear vector icon into r.
func PaintIcon(p *ui.Painter, r ui.Rect, name string, col ui.Color, strokeWidth float32) {
	if strokeWidth <= 0 {
		strokeWidth = 1.8
	}
	s := func(v float32) float32 { return v * r.W / 24.0 }
	x := func(v float32) float32 { return r.X + s(v) }
	y := func(v float32) float32 { return r.Y + s(v) }

	switch name {
	case "home":
		p.StrokePath((&ui.Path{}).MoveTo(x(3), y(10.5)).LineTo(x(12), y(3)).LineTo(x(21), y(10.5)), strokeWidth, col)
		p.StrokePath((&ui.Path{}).MoveTo(x(5), y(9.5)).LineTo(x(5), y(20)).LineTo(x(19), y(20)).LineTo(x(19), y(9.5)), strokeWidth, col)
		p.StrokePath((&ui.Path{}).MoveTo(x(9.5), y(20)).LineTo(x(9.5), y(13.5)).LineTo(x(14.5), y(13.5)).LineTo(x(14.5), y(20)), strokeWidth, col)
	case "library":
		p.StrokePath((&ui.Path{}).MoveTo(x(3.5), y(5)).LineTo(x(20.5), y(5)).LineTo(x(20.5), y(19)).LineTo(x(3.5), y(19)).Close(), strokeWidth, col)
		p.StrokePath((&ui.Path{}).MoveTo(x(10), y(9)).LineTo(x(15.5), y(12)).LineTo(x(10), y(15)).Close(), strokeWidth, col)
	case "subscriptions":
		p.StrokePath((&ui.Path{}).Circle(x(12), y(8), s(4)), strokeWidth, col)
		p.StrokePath((&ui.Path{}).MoveTo(x(4.5), y(20)).QuadTo(x(4.5), y(14.5), x(12), y(14.5)).QuadTo(x(19.5), y(14.5), x(19.5), y(20)), strokeWidth, col)
	case "tasks":
		p.StrokePath((&ui.Path{}).
			MoveTo(x(12), y(3.5)).LineTo(x(12), y(15)).
			MoveTo(x(7.5), y(10.5)).LineTo(x(12), y(15)).LineTo(x(16.5), y(10.5)).
			MoveTo(x(4), y(19.5)).LineTo(x(20), y(19.5)), strokeWidth, col)
	case "computer":
		p.StrokePath((&ui.Path{}).MoveTo(x(3), y(4.5)).LineTo(x(21), y(4.5)).LineTo(x(21), y(16.5)).LineTo(x(3), y(16.5)).Close(), strokeWidth, col)
		p.StrokePath((&ui.Path{}).MoveTo(x(8.5), y(20)).LineTo(x(15.5), y(20)).MoveTo(x(12), y(16.5)).LineTo(x(12), y(20)), strokeWidth, col)
	case "qr":
		p.StrokePath((&ui.Path{}).
			MoveTo(x(4), y(8)).LineTo(x(4), y(4)).LineTo(x(8), y(4)).
			MoveTo(x(16), y(4)).LineTo(x(20), y(4)).LineTo(x(20), y(8)).
			MoveTo(x(20), y(16)).LineTo(x(20), y(20)).LineTo(x(16), y(20)).
			MoveTo(x(8), y(20)).LineTo(x(4), y(20)).LineTo(x(4), y(16)), strokeWidth, col)
		p.StrokePath((&ui.Path{}).MoveTo(x(7), y(12)).LineTo(x(17), y(12)), strokeWidth, col)
	case "alert-circle":
		p.StrokePath((&ui.Path{}).Circle(x(12), y(12), s(9)), strokeWidth, col)
		p.StrokePath((&ui.Path{}).MoveTo(x(12), y(8)).LineTo(x(12), y(12.5)), strokeWidth, col)
		p.Fill(ui.Rect{X: x(11.1), Y: y(15.2), W: s(1.8), H: s(1.8)}, col, s(0.9))
	case "arrow-left":
		p.StrokePath((&ui.Path{}).MoveTo(x(19), y(12)).LineTo(x(5), y(12)).MoveTo(x(11), y(6)).LineTo(x(5), y(12)).LineTo(x(11), y(18)), strokeWidth, col)
	case "shield-check":
		p.StrokePath((&ui.Path{}).
			MoveTo(x(12), y(3)).LineTo(x(19), y(6)).LineTo(x(19), y(12.5)).
			QuadTo(x(19), y(17.5), x(12), y(21)).
			QuadTo(x(5), y(17.5), x(5), y(12.5)).
			LineTo(x(5), y(6)).Close(), strokeWidth, col)
		p.StrokePath((&ui.Path{}).MoveTo(x(8.8), y(12.2)).LineTo(x(11), y(14.5)).LineTo(x(15.5), y(9.8)), strokeWidth, col)
	}
}

// TabIconPNG renders a crisp 72x72 template PNG for mygo.TabItem.Icon on Android.
func TabIconPNG(name string) []byte {
	const size = 72
	r := vector.NewRasterizer(size, size)
	u := float32(size) / 24.0
	stroke := func(pts ...float32) {
		for i := 0; i+3 < len(pts); i += 2 {
			drawThickSegment(r, pts[i]*u, pts[i+1]*u, pts[i+2]*u, pts[i+3]*u, 1.9*u)
		}
	}
	switch name {
	case "home":
		stroke(3, 10.5, 12, 3, 12, 3, 21, 10.5)
		stroke(5, 9.5, 5, 20, 5, 20, 19, 20, 19, 20, 19, 9.5)
		stroke(9.5, 20, 9.5, 14, 9.5, 14, 14.5, 14, 14.5, 14, 14.5, 20)
	case "library":
		stroke(3.5, 5, 20.5, 5, 20.5, 5, 20.5, 19, 20.5, 19, 3.5, 19, 3.5, 19, 3.5, 5)
		r.MoveTo(10*u, 8.5*u)
		r.LineTo(16*u, 12*u)
		r.LineTo(10*u, 15.5*u)
		r.ClosePath()
	case "subscriptions":
		drawRing(r, u, 12, 8, 4.7, 2.9)
		stroke(4.5, 20, 6.5, 16, 6.5, 16, 12, 14.5, 12, 14.5, 17.5, 16, 17.5, 16, 19.5, 20)
	case "tasks":
		stroke(12, 3.5, 12, 15)
		stroke(7.5, 10.5, 12, 15, 12, 15, 16.5, 10.5)
		stroke(4, 19.5, 20, 19.5)
	case "computer":
		stroke(3, 4.5, 21, 4.5, 21, 4.5, 21, 16.5, 21, 16.5, 3, 16.5, 3, 16.5, 3, 4.5)
		stroke(8.5, 20, 15.5, 20)
		stroke(12, 16.5, 12, 20)
	}
	alpha := image.NewAlpha(image.Rect(0, 0, size, size))
	r.Draw(alpha, alpha.Bounds(), image.NewUniform(color.Alpha{A: 255}), image.Point{})
	out := image.NewNRGBA(alpha.Bounds())
	draw.Draw(out, out.Bounds(), &image.Uniform{color.Black}, image.Point{}, draw.Src)
	for i := range alpha.Pix {
		out.Pix[4*i+3] = alpha.Pix[i]
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, out)
	return buf.Bytes()
}

func drawThickSegment(r *vector.Rasterizer, x0, y0, x1, y1, width float32) {
	dx, dy := x1-x0, y1-y0
	length := float32(math.Hypot(float64(dx), float64(dy)))
	if length <= 0 {
		return
	}
	nx, ny := -dy/length*(width/2), dx/length*(width/2)
	r.MoveTo(x0+nx, y0+ny)
	r.LineTo(x1+nx, y1+ny)
	r.LineTo(x1-nx, y1-ny)
	r.LineTo(x0-nx, y0-ny)
	r.ClosePath()
}

func drawRing(r *vector.Rasterizer, u, cx, cy, outer, inner float32) {
	circlePath(r, u, cx, cy, outer, false)
	circlePath(r, u, cx, cy, inner, true)
}

func circlePath(r *vector.Rasterizer, u, cx, cy, rad float32, reverse bool) {
	const n = 36
	for i := 0; i <= n; i++ {
		a := 2 * math.Pi * float64(i) / n
		if reverse {
			a = -a
		}
		px := (cx + rad*float32(math.Cos(a))) * u
		py := (cy + rad*float32(math.Sin(a))) * u
		if i == 0 {
			r.MoveTo(px, py)
		} else {
			r.LineTo(px, py)
		}
	}
	r.ClosePath()
}
