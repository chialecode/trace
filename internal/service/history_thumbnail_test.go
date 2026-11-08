package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/site/bilibili"
)

func historyThumbnailPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 90, A: 255})
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	// A valid PNG text chunk must disappear when converted to a thumbnail.
	text := []byte("Comment\x00synthetic-metadata")
	chunk := make([]byte, 12+len(text))
	binary.BigEndian.PutUint32(chunk[:4], uint32(len(text)))
	copy(chunk[4:8], "tEXt")
	copy(chunk[8:], text)
	binary.BigEndian.PutUint32(chunk[len(chunk)-4:], crc32.ChecksumIEEE(chunk[4:len(chunk)-4]))
	raw := out.Bytes()
	result := append([]byte(nil), raw[:33]...)
	result = append(result, chunk...)
	return append(result, raw[33:]...)
}

func TestHistoryThumbnailsAreBoundedDecodedAndReencoded(t *testing.T) {
	wide, small := historyThumbnailPNG(t, 1600, 800), historyThumbnailPNG(t, 32, 16)
	oversized := append([]byte(nil), small...)
	binary.BigEndian.PutUint32(oversized[16:20], 5000)
	binary.BigEndian.PutUint32(oversized[20:24], 5000)
	binary.BigEndian.PutUint32(oversized[29:33], crc32.ChecksumIEEE(oversized[12:29]))
	images := map[string][]byte{"/wide.png": wide, "/small.png": small, "/truncated.png": wide[:len(wide)/2], "/oversized.png": oversized}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/x/web-interface/nav" {
			_, _ = fmt.Fprint(w, `{"code":0,"data":{"isLogin":true,"mid":10001,"uname":"synthetic account"}}`)
			return
		}
		if r.Header.Get("Cookie") != "" || r.Header.Get("Referer") != "" {
			t.Error("history thumbnail request leaked private headers")
		}
		if raw, ok := images[r.URL.Path]; ok {
			_, _ = w.Write(raw)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	root := t.TempDir()
	loc, err := appdirs.ResolveWithOptions(appdirs.Options{Channel: "dev", DataDir: filepath.Join(root, "data"), CacheDir: filepath.Join(root, "cache")})
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewBackend(Options{
		Locations: loc, Desktop: NewMockDesktop(), DisableBgWorkers: true,
		BilibiliOpts: bilibili.ClientOptions{AllowLocalhost: true, PacingInterval: -1, Endpoints: bilibili.Endpoints{APIBase: srv.URL}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if _, err := b.ImportCookie(context.Background(), "SESSDATA=synthetic; DedeUserID=10001", ""); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, path, role string
		width, height    int
	}{
		{"avatar", "/wide.png", "avatar", 128, 64},
		{"cover", "/wide.png", "cover", 640, 320},
		{"small image", "/small.png", "cover", 32, 16},
		{"truncated image", "/truncated.png", "cover", 0, 0},
		{"pixel limit", "/oversized.png", "cover", 0, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			thumb := b.fetchHistoryThumb(context.Background(), test.role, srv.URL+test.path)
			if test.width == 0 {
				if len(thumb.Data) != 0 {
					t.Fatal("invalid source was accepted as a thumbnail")
				}
				return
			}
			img, err := png.Decode(bytes.NewReader(thumb.Data))
			if err != nil || img.Bounds().Dx() != test.width || img.Bounds().Dy() != test.height || thumb.Ext != ".png" || thumb.Role != test.role {
				t.Fatalf("thumbnail was not decoded and resized as expected: err=%v", err)
			}
			if bytes.Contains(thumb.Data, []byte("synthetic-metadata")) {
				t.Fatal("thumbnail retained source metadata")
			}
		})
	}
}
