// Package brandicon draws the TRACE mark: a white "T" on a dark gray rounded
// square, with an amber "DEV" badge for the development channels. The
// application icons (tools/icons, 1024x1024) and the tray icon (internal/shell,
// 32x32) are drawn from the same shapes, so every place shows the same channel.
// The background, letter height (250-780 of 1024), stroke (110), corner radius
// and badge are fixed values (the person's choice of 2026-11-14), and the
// badge letters are drawn as shapes, so the result never depends on a font.
package brandicon

import (
	"bytes"
	"image"
	"image/color"
	"image/png"

	"golang.org/x/image/vector"
)

// Design is the size the coordinates below are given in.
const Design = 1024

var (
	darkGray = color.RGBA{R: 0x3c, G: 0x40, B: 0x43, A: 0xff}
	amber    = color.RGBA{R: 0xf3, G: 0xa1, B: 0x15, A: 0xff}
	white    = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	darkText = color.RGBA{R: 0x20, G: 0x21, B: 0x24, A: 0xff}
)

// Options chooses what the mark carries besides the "T".
type Options struct {
	// Badge adds the amber "DEV" badge in the lower right corner
	// (development and test identities).
	Badge bool
	// Activity adds an amber dot in the upper right corner, ringed in the
	// background color (the tray icon while caching or recording runs).
	Activity bool
}

// Draw draws the mark at size x size pixels.
func Draw(size int, o Options) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	p := painter{dst: img, size: size, f: float32(size) / Design}
	p.fill(roundedRect(48, 48, Design-48, Design-48, 200), darkGray)
	// The "T" mark: a bar and a stem, centered (DEC-046 ②, no dot).
	p.fill(roundedRect(272, 250, 752, 360, 28), white)
	p.fill(roundedRect(457, 250, 567, 780, 28), white)
	if o.Badge {
		p.fill(roundedRect(560, 800, 960, 960, 52), amber)
		p.drawDEV()
	}
	if o.Activity {
		p.fill(circle(808, 216, 168), darkGray)
		p.fill(circle(808, 216, 124), amber)
	}
	return img
}

// PNG draws the mark at size x size pixels as a PNG.
func PNG(size int, o Options) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, Draw(size, o)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

type painter struct {
	dst  *image.RGBA
	size int
	f    float32 // pixels per design unit
}

// shape adds a path to r, scaling design coordinates by f.
type shape func(r *vector.Rasterizer, f float32)

func (p painter) fill(s shape, c color.RGBA) {
	r := vector.NewRasterizer(p.size, p.size)
	s(r, p.f)
	r.Draw(p.dst, p.dst.Bounds(), image.NewUniform(c), image.Point{})
}

// drawDEV draws the badge's "DEV" (cap height 839-925, stroke 18, centered
// on x 760) as shapes.
func (p painter) drawDEV() {
	// D: a bowl with rounded right corners and its counter cut out again.
	p.fill(rightRounded(636, 839, 710, 925, 40), darkText)
	p.fill(rightRounded(654, 857, 692, 907, 22), amber)
	p.fill(polygon(
		726, 839, 782, 839, 782, 857, 744, 857, 744, 873, 776, 873,
		776, 891, 744, 891, 744, 907, 782, 907, 782, 925, 726, 925,
	), darkText)
	p.fill(polygon(796, 839, 815, 839, 840, 903, 865, 839, 884, 839, 850, 925, 830, 925), darkText)
}

const k = 0.5523 // cubic approximation of a quarter circle

func roundedRect(x0, y0, x1, y1, rad float32) shape {
	return func(r *vector.Rasterizer, f float32) {
		x0, y0, x1, y1, rad := x0*f, y0*f, x1*f, y1*f, rad*f
		r.MoveTo(x0+rad, y0)
		r.LineTo(x1-rad, y0)
		r.CubeTo(x1-rad+rad*k, y0, x1, y0+rad-rad*k, x1, y0+rad)
		r.LineTo(x1, y1-rad)
		r.CubeTo(x1, y1-rad+rad*k, x1-rad+rad*k, y1, x1-rad, y1)
		r.LineTo(x0+rad, y1)
		r.CubeTo(x0+rad-rad*k, y1, x0, y1-rad+rad*k, x0, y1-rad)
		r.LineTo(x0, y0+rad)
		r.CubeTo(x0, y0+rad-rad*k, x0+rad-rad*k, y0, x0+rad, y0)
		r.ClosePath()
	}
}

// rightRounded is a rectangle with only its two right-hand corners rounded.
func rightRounded(x0, y0, x1, y1, rad float32) shape {
	return func(r *vector.Rasterizer, f float32) {
		x0, y0, x1, y1, rad := x0*f, y0*f, x1*f, y1*f, rad*f
		r.MoveTo(x0, y0)
		r.LineTo(x1-rad, y0)
		r.CubeTo(x1-rad+rad*k, y0, x1, y0+rad-rad*k, x1, y0+rad)
		r.LineTo(x1, y1-rad)
		r.CubeTo(x1, y1-rad+rad*k, x1-rad+rad*k, y1, x1-rad, y1)
		r.LineTo(x0, y1)
		r.ClosePath()
	}
}

// circle is a circle around (cx, cy).
func circle(cx, cy, rad float32) shape {
	return roundedRect(cx-rad, cy-rad, cx+rad, cy+rad, rad)
}

// polygon joins x, y pairs with straight lines.
func polygon(xy ...float32) shape {
	return func(r *vector.Rasterizer, f float32) {
		r.MoveTo(xy[0]*f, xy[1]*f)
		for i := 2; i+1 < len(xy); i += 2 {
			r.LineTo(xy[i]*f, xy[i+1]*f)
		}
		r.ClosePath()
	}
}
