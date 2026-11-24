package site

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"image/color"
	"image/gif"
	"io"
	"mime"
	"net/url"
	"os"
	"path"
	"strings"
)

// ErrUnsupportedImageHeader indicates that a file or payload does not start with
// a supported image format header.
var ErrUnsupportedImageHeader = errors.New("文件头不是受支持的图片格式")

// SniffImageMagicBytes inspects raw file header bytes strictly by binary magic
// signatures (ignoring HTTP Content-Type headers) and returns the canonical
// lowercase extension (".jpg", ".png", ".webp", ".gif", ".avif", ".heic").
func SniffImageMagicBytes(b []byte) (string, bool) {
	if len(b) < 3 || LooksLikeDisguisedImageText(b) {
		return "", false
	}
	switch {
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return ".jpg", true
	case len(b) >= 8 && bytes.Equal(b[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n'}):
		return ".png", true
	case len(b) >= 12 && bytes.Equal(b[:4], []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WEBP")):
		return ".webp", true
	case len(b) >= 6 && (bytes.Equal(b[:6], []byte("GIF87a")) || bytes.Equal(b[:6], []byte("GIF89a"))):
		return ".gif", true
	case len(b) >= 12 && bytes.Equal(b[4:8], []byte("ftyp")):
		brand := string(b[8:12])
		switch brand {
		case "avif", "avis":
			return ".avif", true
		case "heic", "heix", "hevc", "hevx", "mif1", "msf1":
			return ".heic", true
		}
	}
	return "", false
}

// SniffImageExtensionBytes determines an image extension from binary magic bytes
// first, falling back to a verified image/* Content-Type only when the payload
// is non-empty and does not resemble disguised HTML/JSON/XML/error text.
func SniffImageExtensionBytes(b []byte, contentType string) (string, error) {
	if ext, ok := SniffImageMagicBytes(b); ok {
		return ext, nil
	}
	if len(b) < 4 || LooksLikeDisguisedImageText(b) {
		return "", ErrUnsupportedImageHeader
	}
	mt, _, _ := mime.ParseMediaType(contentType)
	switch strings.ToLower(strings.TrimSpace(mt)) {
	case "image/jpeg", "image/jpg":
		return ".jpg", nil
	case "image/png":
		return ".png", nil
	case "image/webp":
		return ".webp", nil
	case "image/gif":
		return ".gif", nil
	case "image/avif":
		return ".avif", nil
	case "image/heic", "image/heif":
		return ".heic", nil
	}
	return "", ErrUnsupportedImageHeader
}

// SniffImageExtensionFile opens filePath and sniffs its canonical image extension.
func SniffImageExtensionFile(filePath, contentType string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var head [64]byte
	n, _ := io.ReadFull(f, head[:])
	return SniffImageExtensionBytes(head[:n], contentType)
}

// LooksLikeDisguisedImageText reports whether head resembles an HTML page, XML
// error document, JSON error envelope, or plain-text proxy error body returned
// instead of binary image bytes.
func LooksLikeDisguisedImageText(head []byte) bool {
	trimmed := bytes.TrimLeft(head, " \t\r\n")
	if len(trimmed) == 0 {
		return true
	}
	lower := bytes.ToLower(trimmed[:min(len(trimmed), 256)])
	if bytes.HasPrefix(lower, []byte("<!doctype")) ||
		bytes.HasPrefix(lower, []byte("<html")) ||
		bytes.HasPrefix(lower, []byte("<head")) ||
		bytes.HasPrefix(lower, []byte("<body")) ||
		bytes.HasPrefix(lower, []byte("<script")) ||
		bytes.HasPrefix(lower, []byte("<?xml")) ||
		bytes.HasPrefix(lower, []byte("<svg")) ||
		bytes.HasPrefix(lower, []byte("<error")) {
		return true
	}
	if (trimmed[0] == '{' || trimmed[0] == '[') &&
		(bytes.Contains(lower, []byte(`"status"`)) ||
			bytes.Contains(lower, []byte(`"status_code"`)) ||
			bytes.Contains(lower, []byte(`"code"`)) ||
			bytes.Contains(lower, []byte(`"error"`)) ||
			bytes.Contains(lower, []byte(`"errors"`)) ||
			bytes.Contains(lower, []byte(`"message"`)) ||
			bytes.Contains(lower, []byte(`"ok"`)) ||
			bytes.Contains(lower, []byte(`"errno"`)) ||
			bytes.Contains(lower, []byte(`"msg"`)) ||
			bytes.Contains(lower, []byte(`"data"`))) {
		return true
	}
	if bytes.HasPrefix(lower, []byte("sorry, the parameters")) ||
		bytes.HasPrefix(lower, []byte("we cannot complete this request")) ||
		bytes.HasPrefix(lower, []byte("error 404")) ||
		bytes.HasPrefix(lower, []byte("404 not found")) ||
		bytes.HasPrefix(lower, []byte("403 forbidden")) {
		return true
	}
	return false
}

// LooksLikeExpiredPlaceholderImageBytes reports whether raw matches a known site
// CDN "image deleted / expired" static placeholder response (such as Sina CDN's
// HTTP-200 single-frame 480x360 grayscale placeholder GIF, or a tiny single-frame
// static GIF served for a non-GIF image URL). Genuine multi-frame animated GIFs
// always return false.
func LooksLikeExpiredPlaceholderImageBytes(raw []byte, requestedURL string, expectedW, expectedH int, isGIFSlot bool) bool {
	if len(raw) == 0 || len(raw) > 128*1024 {
		return false
	}
	if len(raw) < 10 {
		return false
	}
	if !bytes.Equal(raw[:6], []byte("GIF87a")) && !bytes.Equal(raw[:6], []byte("GIF89a")) {
		return false
	}
	gifW := int(binary.LittleEndian.Uint16(raw[6:8]))
	gifH := int(binary.LittleEndian.Uint16(raw[8:10]))

	decoded, err := gif.DecodeAll(bytes.NewReader(raw))
	if err != nil || decoded == nil {
		return false
	}
	// Multi-frame animated GIFs are genuine animations, never static placeholders.
	if len(decoded.Image) > 1 {
		return false
	}

	// Signal 1: A single-frame static GIF returned for a URL explicitly requesting
	// a non-GIF format (.jpg, .jpeg, .png, .webp, .avif, .heic, or format=jpg/png/webp).
	if !isGIFSlot && urlExplicitlyNamesNonGIFImage(requestedURL) && len(raw) <= 64*1024 {
		return true
	}

	// Signal 2: Declared image dimensions disagree with a tiny single-frame static GIF
	// returned for a non-GIF slot.
	if !isGIFSlot && expectedW > 0 && expectedH > 0 && (gifW != expectedW || gifH != expectedH) && len(raw) <= 32*1024 {
		return true
	}

	// Signal 3: Canonical 480x360 small static grayscale Sina deleted-image placeholder GIF
	// (applies even when expected dimensions are unknown or the slot was a deleted Weibo GIF).
	if gifW == 480 && gifH == 360 && len(raw) <= 32*1024 && isGrayscaleGIFPalette(decoded) {
		return true
	}

	return false
}

// LooksLikeExpiredPlaceholderImageFile reports whether filePath on disk matches
// a known site CDN expired/deleted static placeholder image.
func LooksLikeExpiredPlaceholderImageFile(filePath, requestedURL string, expectedW, expectedH int, isGIFSlot bool) bool {
	info, err := os.Stat(filePath)
	if err != nil || info.IsDir() || info.Size() <= 0 || info.Size() > 128*1024 {
		return false
	}
	raw, err := os.ReadFile(filePath)
	if err != nil {
		return false
	}
	return LooksLikeExpiredPlaceholderImageBytes(raw, requestedURL, expectedW, expectedH, isGIFSlot)
}

func urlExplicitlyNamesNonGIFImage(rawURL string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	ext := strings.ToLower(path.Ext(u.Path))
	if idx := strings.IndexByte(ext, ':'); idx >= 0 {
		ext = ext[:idx]
	}
	switch ext {
	case ".jpg", ".jpeg", ".png", ".webp", ".avif", ".heic":
		return true
	}
	if fmtParam := strings.ToLower(strings.TrimSpace(u.Query().Get("format"))); fmtParam != "" {
		switch fmtParam {
		case "jpg", "jpeg", "png", "webp", "avif", "heic":
			return true
		}
	}
	return false
}

func isGrayscaleGIFPalette(decoded *gif.GIF) bool {
	if decoded == nil || len(decoded.Image) == 0 {
		return false
	}
	checkPalette := func(p color.Palette) bool {
		if len(p) == 0 {
			return false
		}
		for _, c := range p {
			r, g, b, _ := c.RGBA()
			r8, g8, b8 := int(r>>8), int(g>>8), int(b>>8)
			drg := r8 - g8
			if drg < 0 {
				drg = -drg
			}
			dgb := g8 - b8
			if dgb < 0 {
				dgb = -dgb
			}
			drb := r8 - b8
			if drb < 0 {
				drb = -drb
			}
			if drg > 4 || dgb > 4 || drb > 4 {
				return false
			}
		}
		return true
	}
	if len(decoded.Image[0].Palette) > 0 {
		return checkPalette(decoded.Image[0].Palette)
	}
	if gp, ok := decoded.Config.ColorModel.(color.Palette); ok {
		return checkPalette(gp)
	}
	return false
}

// ValidateExistingImageFile verifies that filePath is a regular, non-symlink file
// within DefaultMaxImageBytes whose binary header matches a supported image magic
// signature and is NOT a known expired/deleted placeholder image. When valid, it
// returns the sniffed extension, byte length, hex SHA-256, and true.
func ValidateExistingImageFile(filePath, requestedURL string, expectedW, expectedH int, isGIFSlot bool) (ext string, size int64, shaHex string, ok bool) {
	info, err := os.Lstat(filePath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return "", 0, "", false
	}
	if info.Size() <= 0 || info.Size() > DefaultMaxImageBytes {
		return "", 0, "", false
	}
	f, err := os.Open(filePath)
	if err != nil {
		return "", 0, "", false
	}
	defer f.Close()

	var head [64]byte
	nHead, _ := io.ReadFull(f, head[:])
	if nHead <= 0 {
		return "", 0, "", false
	}
	sniffedExt, magicOK := SniffImageMagicBytes(head[:nHead])
	if !magicOK {
		return "", 0, "", false
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", 0, "", false
	}

	h := sha256.New()
	var smallBuf bytes.Buffer
	const placeholderCheckMax = 128 * 1024
	var writer io.Writer = h
	if info.Size() <= placeholderCheckMax {
		smallBuf.Grow(int(info.Size()))
		writer = io.MultiWriter(h, &smallBuf)
	}
	copied, err := io.Copy(writer, io.LimitReader(f, DefaultMaxImageBytes+1))
	if err != nil || copied != info.Size() {
		return "", 0, "", false
	}
	if info.Size() <= placeholderCheckMax {
		if LooksLikeExpiredPlaceholderImageBytes(smallBuf.Bytes(), requestedURL, expectedW, expectedH, isGIFSlot) {
			return "", 0, "", false
		}
	}
	return sniffedExt, copied, hex.EncodeToString(h.Sum(nil)), true
}
