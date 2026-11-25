package brandicon

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func TestDrawDEVDInnerCounterSinglePass(t *testing.T) {
	img := Draw(Design, Options{Badge: true})
	if got := img.RGBAAt(666, 882); got != amber {
		t.Fatalf("D counter interior (666,882) = %+v, want amber %+v", got, amber)
	}
	if got := img.RGBAAt(645, 882); got != darkText {
		t.Fatalf("D stem (645,882) = %+v, want darkText %+v", got, darkText)
	}
	if got := img.RGBAAt(701, 882); got != darkText {
		t.Fatalf("D right arc (701,882) = %+v, want darkText %+v", got, darkText)
	}

	// Every pixel around the D inner counter must be a single-pass linear blend
	// between amber (#f3a115) and darkText (#202124), with no double-AA seam.
	dr := float64(int(amber.R) - int(darkText.R))
	dg := float64(int(amber.G) - int(darkText.G))
	db := float64(int(amber.B) - int(darkText.B))
	sawEdge := false
	for y := 855; y <= 909; y++ {
		for x := 652; x <= 694; x++ {
			px := img.RGBAAt(x, y)
			if px.A != 0xff {
				t.Fatalf("pixel (%d,%d) alpha = %d, want 255", x, y, px.A)
			}
			if px != amber && px != darkText {
				sawEdge = true
			}
			frac := float64(int(px.R)-int(darkText.R)) / dr
			if frac < -0.02 || frac > 1.02 {
				t.Fatalf("pixel (%d,%d) = %+v outside single-pass [darkText, amber] range (frac=%.3f)", x, y, px, frac)
			}
			wantG := int(math.Round(float64(darkText.G) + frac*dg))
			wantB := int(math.Round(float64(darkText.B) + frac*db))
			if absInt(int(px.G)-wantG) > 2 || absInt(int(px.B)-wantB) > 2 {
				t.Fatalf("pixel (%d,%d) = %+v deviates from single-pass blend (want G=%d B=%d)", x, y, px, wantG, wantB)
			}
		}
	}
	if !sawEdge {
		t.Fatal("expected anti-aliased transition pixels along D counter boundary")
	}
}

func TestDownsampleAlphaBoxPreservesBorderColorAndTraySizes(t *testing.T) {
	// Synthetic 4x4 block: 8 pixels opaque darkGray, 8 pixels fully transparent.
	// Alpha-weighted box average must preserve straight RGB = darkGray and A = 128.
	master := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 2; y++ {
		for x := 0; x < 4; x++ {
			master.SetRGBA(x, y, darkGray)
		}
	}
	down := downsampleAlphaBox(master, 1)
	got := down.NRGBAAt(0, 0)
	want := color.NRGBA{R: darkGray.R, G: darkGray.G, B: darkGray.B, A: 128}
	if got != want {
		t.Fatalf("synthetic half-transparent box average = %+v, want %+v", got, want)
	}

	for _, size := range []int{16, 20, 24, 32, 48, 64, 128} {
		raw, err := PNG(size, Options{Badge: true})
		if err != nil {
			t.Fatalf("PNG(%d): %v", size, err)
		}
		decoded, err := png.Decode(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("decode PNG(%d): %v", size, err)
		}
		if decoded.Bounds().Dx() != size || decoded.Bounds().Dy() != size {
			t.Fatalf("PNG(%d) bounds = %v", size, decoded.Bounds())
		}
		if _, _, _, a := decoded.At(0, 0).RGBA(); a != 0 {
			t.Fatalf("PNG(%d) outer corner (0,0) alpha = %d, want 0", size, a)
		}

		// Check semi-transparent top-left border pixels (upper-left quadrant,
		// far from T, DEV badge, and activity dot) keep straight darkGray RGB.
		checkedSemi := 0
		for y := 0; y < size/3; y++ {
			for x := 0; x < size/3; x++ {
				nrgba := color.NRGBAModel.Convert(decoded.At(x, y)).(color.NRGBA)
				if nrgba.A > 0 && nrgba.A < 255 {
					checkedSemi++
					tol := 1
					if nrgba.A < 16 {
						// Very low-alpha grazing pixels quantize in the 8-bit RGBA master,
						// but still stay near darkGray (~60) rather than darkening toward 0.
						tol = 8
					}
					if absInt(int(nrgba.R)-int(darkGray.R)) > tol ||
						absInt(int(nrgba.G)-int(darkGray.G)) > tol ||
						absInt(int(nrgba.B)-int(darkGray.B)) > tol {
						t.Fatalf("PNG(%d) border pixel (%d,%d) darkened to %+v, want straight RGB near %+v", size, x, y, nrgba, darkGray)
					}
				}
			}
		}
		if checkedSemi == 0 {
			t.Fatalf("PNG(%d): expected semi-transparent anti-aliased border pixels in top-left quadrant", size)
		}
	}

	// Mutating a returned RGBA image must not corrupt cached masters or future draws.
	first := Draw(32, Options{})
	orig := first.RGBAAt(16, 16)
	first.SetRGBA(16, 16, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	second := Draw(32, Options{})
	if second.RGBAAt(16, 16) != orig {
		t.Fatalf("mutating Draw(32) leaked into subsequent call: got %+v, want %+v", second.RGBAAt(16, 16), orig)
	}
}

func TestCommittedAssetIconsMatchMaster(t *testing.T) {
	cases := []struct {
		file  string
		badge bool
	}{
		{file: filepath.Join("..", "..", "assets", "icon.png"), badge: false},
		{file: filepath.Join("..", "..", "assets", "icon-dev.png"), badge: true},
	}
	for _, tc := range cases {
		onDisk, err := os.ReadFile(tc.file)
		if err != nil {
			t.Fatalf("read %s: %v", tc.file, err)
		}
		want, err := PNG(Design, Options{Badge: tc.badge})
		if err != nil {
			t.Fatalf("PNG(Design, badge=%v): %v", tc.badge, err)
		}
		if !bytes.Equal(onDisk, want) {
			t.Fatalf("%s is out of sync with brandicon.PNG(Design, Badge=%v); run go run ./tools/icons", tc.file, tc.badge)
		}
	}
}
