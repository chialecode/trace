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
	"image/draw"
	"image/png"
	"math"
	"sync"

	"golang.org/x/image/vector"
)

// Design is the size the coordinates below are given in.
const Design = 1024

// downsampleCutoff is the threshold below which icons are downsampled from a
// cached 1024x1024 master via alpha-weighted box averaging.
const downsampleCutoff = 256

var (
	darkGray = color.RGBA{R: 0x3c, G: 0x40, B: 0x43, A: 0xff}
	amber    = color.RGBA{R: 0xf3, G: 0xa1, B: 0x15, A: 0xff}
	white    = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	darkText = color.RGBA{R: 0x20, G: 0x21, B: 0x24, A: 0xff}

	masterOnce [4]sync.Once
	masters    [4]*image.RGBA
)

// Options chooses what the mark carries besides the "T".
type Options struct {
	// Badge adds the amber "DEV" badge in the lower right corner
	// (development and test identities).
	Badge bool
	// Activity adds an optional amber dot in the upper right corner, ringed in
	// the background color (tray icons keep Activity false so status is checked
	// proactively inside the app).
	Activity bool
}

func optionsIndex(o Options) int {
	i := 0
	if o.Badge {
		i += 2
	}
	if o.Activity {
		i++
	}
	return i
}

func masterFor(o Options) *image.RGBA {
	idx := optionsIndex(o)
	masterOnce[idx].Do(func() {
		masters[idx] = drawRaster(Design, o)
	})
	return masters[idx]
}

// Draw draws the mark at size x size pixels. Small target sizes (< 256) are
// downsampled from a cached 1024x1024 master via alpha-weighted box averaging.
func Draw(size int, o Options) *image.RGBA {
	if size <= 0 {
		return image.NewRGBA(image.Rect(0, 0, 0, 0))
	}
	if size < downsampleCutoff {
		src := downsampleAlphaBox(masterFor(o), size)
		dst := image.NewRGBA(image.Rect(0, 0, size, size))
		draw.Draw(dst, dst.Bounds(), src, image.Point{}, draw.Src)
		return dst
	}
	if size == Design {
		src := masterFor(o)
		dst := image.NewRGBA(src.Bounds())
		copy(dst.Pix, src.Pix)
		return dst
	}
	return drawRaster(size, o)
}

// PNG draws the mark at size x size pixels as a PNG.
func PNG(size int, o Options) ([]byte, error) {
	var buf bytes.Buffer
	var img image.Image
	if size > 0 && size < downsampleCutoff {
		img = downsampleAlphaBox(masterFor(o), size)
	} else {
		img = Draw(size, o)
	}
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func drawRaster(size int, o Options) *image.RGBA {
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

// downsampleAlphaBox downsamples a square RGBA master to size x size using
// alpha-weighted box averaging: rgb = round(sum(c*a)/sum(a)),
// alpha = round(sum(a)/count), preventing transparent outer pixels from
// darkening anti-aliased borders.
func downsampleAlphaBox(master *image.RGBA, size int) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, size, size))
	if size <= 0 {
		return dst
	}
	b := master.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return dst
	}
	sx := float64(w) / float64(size)
	sy := float64(h) / float64(size)
	for y := range size {
		y0 := int(float64(y) * sy)
		y1 := max(int(float64(y+1)*sy), y0+1)
		y1 = min(y1, h)
		for x := range size {
			x0 := int(float64(x) * sx)
			x1 := max(int(float64(x+1)*sx), x0+1)
			x1 = min(x1, w)
			var sumR, sumG, sumB, sumA float64
			for yy := y0; yy < y1; yy++ {
				rowOff := (b.Min.Y+yy-master.Rect.Min.Y)*master.Stride + (b.Min.X+x0-master.Rect.Min.X)*4
				for xx := x0; xx < x1; xx++ {
					pa := master.Pix[rowOff+3]
					if pa == 0xff {
						sumR += float64(master.Pix[rowOff]) * 255
						sumG += float64(master.Pix[rowOff+1]) * 255
						sumB += float64(master.Pix[rowOff+2]) * 255
						sumA += 255
					} else if pa != 0 {
						c := color.NRGBAModel.Convert(color.RGBA{
							R: master.Pix[rowOff],
							G: master.Pix[rowOff+1],
							B: master.Pix[rowOff+2],
							A: pa,
						}).(color.NRGBA)
						fa := float64(c.A)
						sumR += float64(c.R) * fa
						sumG += float64(c.G) * fa
						sumB += float64(c.B) * fa
						sumA += fa
					}
					rowOff += 4
				}
			}
			if sumA == 0 {
				continue
			}
			count := float64((y1 - y0) * (x1 - x0))
			dst.SetNRGBA(x, y, color.NRGBA{
				R: uint8(math.Round(sumR / sumA)),
				G: uint8(math.Round(sumG / sumA)),
				B: uint8(math.Round(sumB / sumA)),
				A: uint8(math.Round(sumA / count)),
			})
		}
	}
	return dst
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
	// D: outer bowl with inner counter subtracted in one single-pass path so
	// the counter edge is anti-aliased once without an amber-over-dark seam.
	p.fill(func(r *vector.Rasterizer, f float32) {
		rightRounded(636, 839, 710, 925, 40)(r, f)
		rightRoundedHole(654, 857, 692, 907, 22)(r, f)
	}, darkText)
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

// rightRounded is a clockwise rectangle with only its two right-hand corners rounded.
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

// rightRoundedHole is the counter-clockwise counterpart of rightRounded,
// subtracting an inner counter within the same rasterizer pass.
func rightRoundedHole(x0, y0, x1, y1, rad float32) shape {
	return func(r *vector.Rasterizer, f float32) {
		x0, y0, x1, y1, rad := x0*f, y0*f, x1*f, y1*f, rad*f
		r.MoveTo(x0, y0)
		r.LineTo(x0, y1)
		r.LineTo(x1-rad, y1)
		r.CubeTo(x1-rad+rad*k, y1, x1, y1-rad+rad*k, x1, y1-rad)
		r.LineTo(x1, y0+rad)
		r.CubeTo(x1, y0+rad-rad*k, x1-rad+rad*k, y0, x1-rad, y0)
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
