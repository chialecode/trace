package companion

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/storage"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const (
	thumbMaxCacheBytes int64 = 256 << 20 // 256 MiB LRU cache (docs/design/mobile.md §3)
	thumbMaxSourceByte int64 = 64 << 20  // refuse decoding absurdly large source files
	thumbMaxPixels     int64 = 100_000_000
	thumbJPEGQuality         = 82
)

// thumbCache generates and LRU-caches resized JPEG thumbnails under
// <DataDir>/cache/companion-thumbs/ (docs/design/mobile.md §3, §6).
type thumbCache struct {
	mu       sync.Mutex
	dir      string
	maxBytes int64
}

func newThumbCache(dataDir string) *thumbCache {
	return &thumbCache{
		dir:      filepath.Join(dataDir, filepath.FromSlash(ThumbsCacheRelDir)),
		maxBytes: thumbMaxCacheBytes,
	}
}

// isResizableImageExt reports whether relPath is a static raster image suitable
// for ?w= JPEG resizing. GIFs, videos, and audio are always served raw (design §6).
func isResizableImageExt(relPath string) bool {
	switch strings.ToLower(filepath.Ext(relPath)) {
	case ".jpg", ".jpeg", ".png", ".webp":
		return true
	default:
		return false
	}
}

// GetOrBuild returns resized JPEG bytes for srcPath scaled so its longest edge is <= maxEdge.
// For w <= 480 (list thumbnails), results are cached on disk under cache/companion-thumbs/
// keyed by itemKey prefix + content hash/mtime so deleting an item invalidates its thumbnails cleanly.
func (tc *thumbCache) GetOrBuild(itemKey, relPath, srcPath string, maxEdge int) ([]byte, error) {
	if maxEdge <= 0 {
		return nil, errors.New("invalid maxEdge")
	}
	info, err := os.Stat(srcPath)
	if err != nil {
		return nil, err
	}
	if info.Size() <= 0 || info.Size() > thumbMaxSourceByte {
		return nil, fmt.Errorf("image size out of bounds: %d", info.Size())
	}

	cacheOnDisk := tc.dir != "" && maxEdge <= 480
	var cachePath string
	if cacheOnDisk {
		cachePath = filepath.Join(tc.dir, thumbFileName(itemKey, relPath, maxEdge, info.Size(), info.ModTime().UnixNano()))
		if raw, err := os.ReadFile(cachePath); err == nil && len(raw) > 0 {
			now := time.Now()
			_ = os.Chtimes(cachePath, now, now)
			return raw, nil
		}
	}

	jpegBytes, err := resizeImageToJPEG(srcPath, maxEdge)
	if err != nil {
		return nil, err
	}

	if cacheOnDisk {
		tc.mu.Lock()
		_ = os.MkdirAll(tc.dir, 0o700)
		_ = storage.WriteFileAtomic(cachePath, jpegBytes, 0o600)
		tc.evictLRULocked()
		tc.mu.Unlock()
	}
	return jpegBytes, nil
}

// InvalidateItem removes all cached thumbnails belonging to itemKey (design §3).
func (tc *thumbCache) InvalidateItem(itemKey string) {
	if tc == nil || tc.dir == "" || strings.TrimSpace(itemKey) == "" {
		return
	}
	prefix := thumbKeyPrefix(itemKey)
	tc.mu.Lock()
	defer tc.mu.Unlock()
	entries, err := os.ReadDir(tc.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), prefix) {
			_ = os.Remove(filepath.Join(tc.dir, e.Name()))
		}
	}
}

// ClearAll removes all cached thumbnails (used when a batch delete occurs without per-item keys).
func (tc *thumbCache) ClearAll() {
	if tc == nil || tc.dir == "" {
		return
	}
	tc.mu.Lock()
	defer tc.mu.Unlock()
	entries, err := os.ReadDir(tc.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			_ = os.Remove(filepath.Join(tc.dir, e.Name()))
		}
	}
}

func (tc *thumbCache) evictLRULocked() {
	entries, err := os.ReadDir(tc.dir)
	if err != nil {
		return
	}
	type fileEntry struct {
		path    string
		size    int64
		modTime time.Time
	}
	var list []fileEntry
	var total int64
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jpg") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		total += info.Size()
		list = append(list, fileEntry{
			path:    filepath.Join(tc.dir, e.Name()),
			size:    info.Size(),
			modTime: info.ModTime(),
		})
	}
	if total <= tc.maxBytes {
		return
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].modTime.Before(list[j].modTime)
	})
	for _, f := range list {
		if total <= tc.maxBytes {
			break
		}
		if os.Remove(f.path) == nil {
			total -= f.size
		}
	}
}

func thumbKeyPrefix(itemKey string) string {
	sum := sha256.Sum256([]byte(itemKey))
	return hex.EncodeToString(sum[:8]) + "_"
}

func thumbFileName(itemKey, relPath string, maxEdge int, size, modNano int64) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s|%s|%d|%d|%d", itemKey, relPath, maxEdge, size, modNano)
	return thumbKeyPrefix(itemKey) + hex.EncodeToString(h.Sum(nil)[:12]) + ".jpg"
}

func resizeImageToJPEG(srcPath string, maxEdge int) ([]byte, error) {
	f, err := os.Open(srcPath)
	if err != nil {
		return nil, err
	}
	cfg, _, err := image.DecodeConfig(f)
	_ = f.Close()
	if err != nil {
		return nil, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > thumbMaxPixels {
		return nil, errors.New("image dimensions out of bounds")
	}

	f, err = os.Open(srcPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}

	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	dstW, dstH := w, h
	if w > maxEdge || h > maxEdge {
		if w >= h {
			dstW = maxEdge
			dstH = max(1, int(int64(h)*int64(maxEdge)/int64(w)))
		} else {
			dstH = maxEdge
			dstW = max(1, int(int64(w)*int64(maxEdge)/int64(h)))
		}
	}

	var outImg image.Image = img
	if dstW != w || dstH != h {
		dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
		draw.CatmullRom.Scale(dst, dst.Bounds(), img, bounds, draw.Over, nil)
		outImg = dst
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, outImg, &jpeg.Options{Quality: thumbJPEGQuality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
