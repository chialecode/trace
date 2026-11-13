// Command icons draws the application icons of TRACE: assets/icon.png for
// the release channel (a white "T" on dark gray) and assets/icon-dev.png for
// the development channel (the same mark with an amber "DEV" badge). The
// background, letter height (250-780), stroke (110), corner radius (28) and
// badge are fixed values (the person's choice of 2026-11-14), and the badge
// letters are drawn as shapes, so the result never depends on a font. Both
// are 1024x1024 PNGs that MyGo turns into the icons of the executable, the
// installer and the uninstall entry.
//
//	go run ./tools/icons
package main

import (
	"bytes"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"

	"golang.org/x/image/vector"
)

const size = 1024

var (
	darkGray = color.RGBA{R: 0x3c, G: 0x40, B: 0x43, A: 0xff}
	amber    = color.RGBA{R: 0xf3, G: 0xa1, B: 0x15, A: 0xff}
	white    = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	darkText = color.RGBA{R: 0x20, G: 0x21, B: 0x24, A: 0xff}
)

func main() {
	out := flag.String("out", "assets", "directory of icon.png and icon-dev.png")
	flag.Parse()
	if err := run(*out); err != nil {
		fmt.Fprintln(os.Stderr, "icons:", err)
		os.Exit(1)
	}
}

func run(out string) error {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	for name, dev := range map[string]bool{"icon.png": false, "icon-dev.png": true} {
		data, err := Render(dev)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(out, name), data, 0o644); err != nil {
			return err
		}
		fmt.Printf("wrote %s (%d bytes)\n", filepath.Join(out, name), len(data))
	}
	return nil
}

// Render draws one icon as PNG.
func Render(dev bool) ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	// Rounded square on dark gray, as the tray icon draws it; the channels
	// differ only by the badge.
	fill(img, roundedRect(48, 48, size-48, size-48, 200), darkGray)
	// The "T" mark: a bar and a stem, centered (DEC-046 ②, no dot).
	fill(img, roundedRect(272, 250, 752, 360, 28), white)
	fill(img, roundedRect(457, 250, 567, 780, 28), white)
	if dev {
		fill(img, roundedRect(560, 800, 960, 960, 52), amber)
		drawDEV(img)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// drawDEV draws the badge's "DEV" (cap height 839-925, stroke 18, centered
// on x 760) as shapes.
func drawDEV(dst *image.RGBA) {
	// D: a bowl with rounded right corners and its counter cut out again.
	fill(dst, rightRounded(636, 839, 710, 925, 40), darkText)
	fill(dst, rightRounded(654, 857, 692, 907, 22), amber)
	fill(dst, polygon(
		726, 839, 782, 839, 782, 857, 744, 857, 744, 873, 776, 873,
		776, 891, 744, 891, 744, 907, 782, 907, 782, 925, 726, 925,
	), darkText)
	fill(dst, polygon(796, 839, 815, 839, 840, 903, 865, 839, 884, 839, 850, 925, 830, 925), darkText)
}

type shape func(r *vector.Rasterizer)

func fill(dst *image.RGBA, s shape, c color.RGBA) {
	r := vector.NewRasterizer(size, size)
	s(r)
	r.Draw(dst, dst.Bounds(), image.NewUniform(c), image.Point{})
}

const k = 0.5523 // cubic approximation of a quarter circle

func roundedRect(x0, y0, x1, y1, rad float32) shape {
	return func(r *vector.Rasterizer) {
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
	return func(r *vector.Rasterizer) {
		r.MoveTo(x0, y0)
		r.LineTo(x1-rad, y0)
		r.CubeTo(x1-rad+rad*k, y0, x1, y0+rad-rad*k, x1, y0+rad)
		r.LineTo(x1, y1-rad)
		r.CubeTo(x1, y1-rad+rad*k, x1-rad+rad*k, y1, x1-rad, y1)
		r.LineTo(x0, y1)
		r.ClosePath()
	}
}

// polygon joins x, y pairs with straight lines.
func polygon(xy ...float32) shape {
	return func(r *vector.Rasterizer) {
		r.MoveTo(xy[0], xy[1])
		for i := 2; i+1 < len(xy); i += 2 {
			r.LineTo(xy[i], xy[i+1])
		}
		r.ClosePath()
	}
}
