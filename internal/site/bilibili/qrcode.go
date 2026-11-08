package bilibili

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/png"

	qrcode "github.com/yeqown/go-qrcode/v2"
)

// RenderQRPNG encodes the whole URL. Oversized input fails instead of silently
// producing a QR code for a truncated login URL.
func RenderQRPNG(text string, scale int) ([]byte, error) {
	rgba, err := renderQR(text, scale)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, rgba); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func RenderQRRGBA(text string, scale int) *image.RGBA {
	rgba, _ := renderQR(text, scale)
	return rgba
}

func renderQR(text string, scale int) (*image.RGBA, error) {
	if scale < 2 || scale > 16 {
		scale = 6
	}
	code, err := qrcode.NewWith(text, qrcode.WithEncodingMode(qrcode.EncModeByte))
	if err != nil {
		return nil, errors.New("登录二维码内容无法编码")
	}
	writer := &qrBitmapWriter{scale: scale}
	if err := code.Save(writer); err != nil {
		return nil, err
	}
	return writer.image, nil
}

type qrBitmapWriter struct {
	scale int
	image *image.RGBA
}

func (w *qrBitmapWriter) Close() error { return nil }

func (w *qrBitmapWriter) Write(matrix qrcode.Matrix) error {
	const border = 4
	w.image = image.NewRGBA(image.Rect(0, 0, (matrix.Width()+2*border)*w.scale, (matrix.Height()+2*border)*w.scale))
	draw.Draw(w.image, w.image.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	matrix.Iterate(qrcode.IterDirection_ROW, func(x, y int, value qrcode.QRValue) {
		if value.IsSet() {
			r := image.Rect((x+border)*w.scale, (y+border)*w.scale, (x+border+1)*w.scale, (y+border+1)*w.scale)
			draw.Draw(w.image, r, image.NewUniform(color.Black), image.Point{}, draw.Src)
		}
	})
	return nil
}
