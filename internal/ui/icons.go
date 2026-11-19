package ui

import (
	"math"

	"github.com/egoist/mygo/ui"
)

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

		case "mine", "bookmark":
			// A bookmark: a tall tab with a V notch at the bottom (我的).
			mark := (&ui.Path{}).
				MoveTo(x(6.5), y(4)).
				LineTo(x(17.5), y(4)).
				LineTo(x(17.5), y(20)).
				LineTo(x(12), y(15.5)).
				LineTo(x(6.5), y(20)).
				Close()
			p.StrokePath(mark, strokeWidth, col)
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

		case "upload":
			arrow := (&ui.Path{}).
				MoveTo(x(12), y(15)).
				LineTo(x(12), y(3.5)).
				MoveTo(x(7.5), y(8)).
				LineTo(x(12), y(3.5)).
				LineTo(x(16.5), y(8)).
				MoveTo(x(4), y(19.5)).
				LineTo(x(20), y(19.5))
			p.StrokePath(arrow, strokeWidth, col)

		case "settings":
			// 8-tooth gear polygon with center axle hole; never concentric target circles.
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
			// Horizontal adjustment sliders for General settings.
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

		case "clock":
			circ := (&ui.Path{}).Circle(x(12), y(12), s(8.5))
			p.StrokePath(circ, strokeWidth, col)
			hands := (&ui.Path{}).
				MoveTo(x(12), y(7.5)).
				LineTo(x(12), y(12)).
				LineTo(x(15.2), y(14))
			p.StrokePath(hands, strokeWidth, col)

		case "history":
			arc := buildArcPath(x(12), y(12), s(8.5), math.Pi+0.6, 3*math.Pi-0.15)
			p.StrokePath(arc, strokeWidth, col)
			head := (&ui.Path{}).
				MoveTo(x(3.4), y(4.6)).
				LineTo(x(4.9), y(7.8)).
				LineTo(x(8.2), y(7))
			p.StrokePath(head, strokeWidth, col)
			hands := (&ui.Path{}).
				MoveTo(x(12), y(8)).
				LineTo(x(12), y(12)).
				LineTo(x(14.8), y(13.8))
			p.StrokePath(hands, strokeWidth, col)

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
