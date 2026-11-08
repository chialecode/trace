package player

import (
	"image/color"
	"testing"
)

// A 1080x1920 portrait frame arrived as 8,355,840 bytes (4352-byte rows) while
// MF_MT_DEFAULT_STRIDE said 4320; using 4320 sheared every row (garbled video).
func TestPaddedRowsAreNotSheared(t *testing.T) {
	if got := contiguousStride("rgb32", 1080, 1920, 4320, 8355840); got != 4352 {
		t.Fatalf("stride = %d, want 4352", got)
	}
	if got := contiguousStride("rgb32", 1920, 1080, 7680, 7680*1080); got != 7680 {
		t.Fatalf("unpadded stride changed to %d", got)
	}
	if got := contiguousStride("rgb32", 1920, 1080, 7680, 7680*1088); got != 7680 {
		t.Fatalf("ambiguous length must keep the type stride, got %d", got)
	}

	const w, h, pitch = 6, 4, 8 * 4 // two padding pixels per row
	buf := make([]byte, pitch*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*pitch + x*4
			buf[i], buf[i+1], buf[i+2] = byte(10*x), byte(50*y), 200 // B, G, R
		}
	}
	img := BGRA32ToRGBA(buf, w, h, contiguousStride("rgb32", w, h, w*4, len(buf)), nil)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if got, want := img.RGBAAt(x, y), (color.RGBA{200, byte(50 * y), byte(10 * x), 255}); got != want {
				t.Fatalf("pixel (%d,%d) = %v, want %v", x, y, got, want)
			}
		}
	}
}
