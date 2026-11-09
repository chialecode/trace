// Command icons draws the application icons of TRACE: assets/icon.png for
// the release channel (a white "T." on the accent blue) and
// assets/icon-dev.png for the development channel (the same mark on dark
// gray with an amber "DEV" badge). Both are 1024x1024 PNGs that MyGo turns
// into the icons of the executable, the installer and the uninstall entry.
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

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

const size = 1024

var (
	accent   = color.RGBA{R: 0x1a, G: 0x73, B: 0xe8, A: 0xff}
	devGray  = color.RGBA{R: 0x3c, G: 0x40, B: 0x43, A: 0xff}
	amber    = color.RGBA{R: 0xf3, G: 0xa1, B: 0x15, A: 0xff}
	white    = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	darkText = color.RGBA{R: 0x20, G: 0x21, B: 0x24, A: 0xff}
)

func main() {
	out := flag.String("out", "assets", "directory of icon.png and icon-dev.png")
	fontPath := flag.String("font", filepath.Join("internal", "ui", "fonts", "InterVariable.ttf"), "font of the DEV badge")
	flag.Parse()
	if err := run(*out, *fontPath); err != nil {
		fmt.Fprintln(os.Stderr, "icons:", err)
		os.Exit(1)
	}
}

func run(out, fontPath string) error {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	release, err := Render(false, nil)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(fontPath)
	if err != nil {
		return err
	}
	dev, err := Render(true, raw)
	if err != nil {
		return err
	}
	for name, data := range map[string][]byte{"icon.png": release, "icon-dev.png": dev} {
		if err := os.WriteFile(filepath.Join(out, name), data, 0o644); err != nil {
			return err
		}
		fmt.Printf("wrote %s (%d bytes)\n", filepath.Join(out, name), len(data))
	}
	return nil
}

// Render draws one icon as PNG; the development icon needs the font of
// its badge.
func Render(dev bool, fontData []byte) ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	bg := accent
	if dev {
		bg = devGray
	}
	// Rounded square, as the tray icon and the interface draw it.
	fill(img, roundedRect(48, 48, size-48, size-48, 200), bg)
	// The "T." mark: a bar, a stem and a dot.
	fill(img, roundedRect(232, 250, 712, 360, 28), white)
	fill(img, roundedRect(417, 250, 527, 780, 28), white)
	fill(img, circle(650, 722, 62), white)
	if dev {
		fill(img, roundedRect(560, 800, 960, 960, 52), amber)
		if err := drawText(img, fontData, "DEV", 760, 925, 118, darkText); err != nil {
			return nil, err
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

type shape func(r *vector.Rasterizer)

func fill(dst *image.RGBA, s shape, c color.RGBA) {
	r := vector.NewRasterizer(size, size)
	s(r)
	r.Draw(dst, dst.Bounds(), image.NewUniform(c), image.Point{})
}

func roundedRect(x0, y0, x1, y1, rad float32) shape {
	const k = 0.5523 // cubic approximation of a quarter circle
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

func circle(cx, cy, rad float32) shape {
	return roundedRect(cx-rad, cy-rad, cx+rad, cy+rad, rad)
}

// drawText draws text centered on cx with its baseline at y, made bolder
// by drawing it with small offsets.
func drawText(dst *image.RGBA, fontData []byte, text string, cx, y int, px float64, c color.RGBA) error {
	f, err := opentype.Parse(fontData)
	if err != nil {
		return err
	}
	face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: px, DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		return err
	}
	defer face.Close()
	d := &font.Drawer{Dst: dst, Src: image.NewUniform(c), Face: face}
	width := d.MeasureString(text).Round()
	for _, off := range [][2]int{{0, 0}, {3, 0}, {6, 0}, {0, 2}, {3, 2}, {6, 2}} {
		d.Dot = fixed.P(cx-width/2-3+off[0], y+off[1])
		d.DrawString(text)
	}
	return nil
}
