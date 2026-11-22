package download

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image/color"
	"image/gif"
	"io"
	"mime"
	"net/url"
	"os"
	"strings"

	"github.com/chialecode/trace/internal/site"
)

func rejectDisguisedMediaContentType(siteID site.ID, rawCT, role string, statusCode int) error {
	mediaType, _, err := mime.ParseMediaType(rawCT)
	if err != nil {
		mediaType = strings.ToLower(strings.TrimSpace(strings.SplitN(rawCT, ";", 2)[0]))
	}
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	switch mediaType {
	case "text/html", "application/xhtml+xml", "application/json", "text/json", "text/plain", "text/xml", "application/xml", "application/javascript", "text/javascript":
		return &site.Error{
			Site:       siteID,
			Kind:       site.ErrKindSchemaChanged,
			HTTPStatus: statusCode,
			Message:    fmt.Sprintf("媒体请求返回了非媒体类型 (%s)", mediaType),
		}
	}
	if (role == "image" || role == "cover") && mediaType != "" &&
		!strings.HasPrefix(mediaType, "image/") && mediaType != "application/octet-stream" {
		return &site.Error{
			Site:       siteID,
			Kind:       site.ErrKindSchemaChanged,
			HTTPStatus: statusCode,
			Message:    fmt.Sprintf("图片响应不是有效的图片类型 (%s)", mediaType),
		}
	}
	return nil
}

func looksLikeDisguisedTextBody(head []byte) bool {
	trimmed := bytes.TrimLeft(head, " \t\r\n")
	if len(trimmed) == 0 {
		return false
	}
	lower := bytes.ToLower(trimmed[:min(len(trimmed), 256)])
	if bytes.HasPrefix(lower, []byte("<!doctype html")) ||
		bytes.HasPrefix(lower, []byte("<html")) ||
		bytes.HasPrefix(lower, []byte("<head")) ||
		bytes.HasPrefix(lower, []byte("<body")) ||
		bytes.HasPrefix(lower, []byte("<?xml")) {
		return true
	}
	if trimmed[0] == '{' && (bytes.Contains(lower, []byte(`"status_code"`)) ||
		bytes.Contains(lower, []byte(`"code"`)) ||
		bytes.Contains(lower, []byte(`"error"`)) ||
		bytes.Contains(lower, []byte(`"errors"`)) ||
		bytes.Contains(lower, []byte(`"message"`)) ||
		bytes.Contains(lower, []byte(`"ok"`)) ||
		bytes.Contains(lower, []byte(`"errno"`)) ||
		bytes.Contains(lower, []byte(`"msg"`))) {
		return true
	}
	return false
}

// looksLikeWeiboPlaceholderGIF reports whether filePath (downloaded for a non-GIF Weibo image slot)
// matches Sina CDN's HTTP-200 deleted/unavailable placeholder GIF (a small single-frame static
// GIF returned instead of a 404 for purged .jpg/.png/.webp assets, typically 480x360 grayscale).
// Genuine multi-frame animated GIFs and non-GIF files always return false.
func looksLikeWeiboPlaceholderGIF(filePath, requestedURL string, expectedW, expectedH int) bool {
	info, err := os.Stat(filePath)
	if err != nil || info.IsDir() || info.Size() <= 0 || info.Size() > 128*1024 {
		return false
	}
	f, err := os.Open(filePath)
	if err != nil {
		return false
	}
	defer f.Close()
	var hdr [10]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil {
		return false
	}
	if !bytes.Equal(hdr[:6], []byte("GIF87a")) && !bytes.Equal(hdr[:6], []byte("GIF89a")) {
		return false
	}
	gifW := int(binary.LittleEndian.Uint16(hdr[6:8]))
	gifH := int(binary.LittleEndian.Uint16(hdr[8:10]))
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return false
	}
	decoded, err := gif.DecodeAll(f)
	if err != nil || decoded == nil {
		return false
	}
	// Multi-frame animated GIFs are genuine animations, not static deleted-image placeholders.
	if len(decoded.Image) > 1 {
		return false
	}
	// Signal 1: Sina CDN returned a static 1-frame GIF for a URL explicitly naming a non-GIF image format.
	if u, perr := url.Parse(strings.TrimSpace(requestedURL)); perr == nil {
		host := strings.ToLower(u.Hostname())
		if host == "sinaimg.cn" || strings.HasSuffix(host, ".sinaimg.cn") {
			lp := strings.ToLower(u.Path)
			if strings.HasSuffix(lp, ".jpg") || strings.HasSuffix(lp, ".jpeg") || strings.HasSuffix(lp, ".png") || strings.HasSuffix(lp, ".webp") {
				return true
			}
		}
	}
	// Signal 2: Declared image dimensions disagree with the tiny static GIF returned for a non-GIF slot.
	if expectedW > 0 && expectedH > 0 && (gifW != expectedW || gifH != expectedH) && info.Size() <= 32*1024 {
		return true
	}
	// Signal 3: Canonical 480x360 small static grayscale Sina placeholder GIF (observed ~8-9 KiB).
	if gifW == 480 && gifH == 360 && info.Size() <= 32*1024 && isGrayscaleGIF(decoded) {
		return true
	}
	return false
}

func isGrayscaleGIF(decoded *gif.GIF) bool {
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

func sniffImageExtension(filePath, contentType string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var head [32]byte
	n, _ := io.ReadFull(f, head[:])
	b := head[:n]

	switch {
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return ".jpg", nil
	case len(b) >= 8 && bytes.Equal(b[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n'}):
		return ".png", nil
	case len(b) >= 12 && bytes.Equal(b[:4], []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WEBP")):
		return ".webp", nil
	case len(b) >= 6 && (bytes.Equal(b[:6], []byte("GIF87a")) || bytes.Equal(b[:6], []byte("GIF89a"))):
		return ".gif", nil
	case len(b) >= 12 && bytes.Equal(b[4:8], []byte("ftyp")):
		brand := string(b[8:12])
		switch brand {
		case "avif", "avis":
			return ".avif", nil
		case "heic", "heix", "hevc", "hevx", "mif1", "msf1":
			return ".heic", nil
		}
	}

	mt, _, _ := mime.ParseMediaType(contentType)
	switch strings.ToLower(strings.TrimSpace(mt)) {
	case "image/jpeg", "image/jpg":
		if len(b) >= 4 && !looksLikeDisguisedTextBody(b) {
			return ".jpg", nil
		}
	case "image/png":
		if len(b) >= 4 && !looksLikeDisguisedTextBody(b) {
			return ".png", nil
		}
	case "image/webp":
		if len(b) >= 4 && !looksLikeDisguisedTextBody(b) {
			return ".webp", nil
		}
	case "image/gif":
		if len(b) >= 4 && !looksLikeDisguisedTextBody(b) {
			return ".gif", nil
		}
	case "image/avif":
		if len(b) >= 4 && !looksLikeDisguisedTextBody(b) {
			return ".avif", nil
		}
	case "image/heic", "image/heif":
		if len(b) >= 4 && !looksLikeDisguisedTextBody(b) {
			return ".heic", nil
		}
	}
	return "", errors.New("文件头不是受支持的图片格式")
}

func sniffAudioExtension(filePath, contentType string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var head [32]byte
	n, _ := io.ReadFull(f, head[:])
	b := head[:n]
	if len(b) == 0 || looksLikeDisguisedTextBody(b) {
		return "", errors.New("配乐文件不是有效的音频格式")
	}

	switch {
	case len(b) >= 3 && bytes.Equal(b[:3], []byte("ID3")):
		return ".mp3", nil
	case len(b) >= 2 && b[0] == 0xFF && (b[1]&0xF6) == 0xF0:
		return ".aac", nil
	case len(b) >= 2 && b[0] == 0xFF && (b[1]&0xE0) == 0xE0 && ((b[1]>>1)&0x03) != 0:
		return ".mp3", nil
	case len(b) >= 12 && bytes.Equal(b[4:8], []byte("ftyp")):
		return ".m4a", nil
	case len(b) >= 4 && bytes.Equal(b[:4], []byte("OggS")):
		return ".ogg", nil
	case len(b) >= 4 && bytes.Equal(b[:4], []byte("fLaC")):
		return ".flac", nil
	case len(b) >= 12 && bytes.Equal(b[:4], []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WAVE")):
		return ".wav", nil
	case len(b) >= 4 && bytes.Equal(b[:4], []byte{0x1A, 0x45, 0xDF, 0xA3}):
		return ".webm", nil
	}

	mt, _, _ := mime.ParseMediaType(contentType)
	switch strings.ToLower(strings.TrimSpace(mt)) {
	case "audio/mpeg", "audio/mp3":
		return ".mp3", nil
	case "audio/mp4", "audio/x-m4a", "audio/m4a", "video/mp4":
		return ".m4a", nil
	case "audio/aac", "audio/aacp":
		return ".aac", nil
	case "audio/ogg", "application/ogg":
		return ".ogg", nil
	case "audio/flac":
		return ".flac", nil
	case "audio/wav", "audio/x-wav", "audio/wave":
		return ".wav", nil
	case "audio/webm":
		return ".webm", nil
	}
	return "", errors.New("配乐文件不是受支持的音频格式")
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func sanitizeStoredURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func summaryPrimaryVideoCodec(s MuxTrackSummary) string {
	for i, h := range s.Handlers {
		if h == "vide" && i < len(s.Codecs) {
			switch s.Codecs[i] {
			case "avc1", "avc3":
				return "avc"
			case "hvc1", "hev1":
				return "hevc"
			default:
				return s.Codecs[i]
			}
		}
	}
	return ""
}
