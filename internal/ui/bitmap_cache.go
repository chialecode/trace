package ui

import "github.com/egoist/mygo/ui"

// Limits of the decoded image cache. Thumbnails are decoded at the size they
// are drawn (480 or 960 px), so the budget holds several screens of posts;
// switching the Home site filter back and forth shows them again without
// decoding (DEC-057).
const (
	bitmapCacheMaxEntries = 600
	bitmapCacheMaxBytes   = 320 << 20
)

// bitmapCache is a least-recently-used cache of decoded images. A nil entry
// reserves the key while the image loads. It is guarded by App.mu.
type bitmapCache struct {
	entries map[string]*bitmapEntry
	tick    uint64
	bytes   int64
}

type bitmapEntry struct {
	bmp   *ui.Bitmap
	used  uint64
	bytes int64
}

func bitmapBytes(bmp *ui.Bitmap) int64 {
	if bmp == nil {
		return 0
	}
	w, h := bmp.Size()
	return int64(w) * int64(h) * 4
}

// get returns the cached image (nil while loading) and marks it used.
func (b *bitmapCache) get(key string) (*ui.Bitmap, bool) {
	e, ok := b.entries[key]
	if !ok {
		return nil, false
	}
	b.tick++
	e.used = b.tick
	return e.bmp, true
}

// reserve records key as loading (nil), evicting what was used least.
func (b *bitmapCache) reserve(key string) {
	b.put(key, nil)
}

// put stores bmp under key, evicting what was used least beyond the limits.
func (b *bitmapCache) put(key string, bmp *ui.Bitmap) {
	if b.entries == nil {
		b.entries = make(map[string]*bitmapEntry)
	}
	if old, ok := b.entries[key]; ok {
		b.bytes -= old.bytes
	}
	b.tick++
	e := &bitmapEntry{bmp: bmp, used: b.tick, bytes: bitmapBytes(bmp)}
	b.entries[key] = e
	b.bytes += e.bytes
	b.evict(key)
}

// fill stores a loaded image only if its key is still reserved or cached.
func (b *bitmapCache) fill(key string, bmp *ui.Bitmap) {
	if _, ok := b.entries[key]; ok {
		b.put(key, bmp)
	}
}

func (b *bitmapCache) remove(key string) {
	if e, ok := b.entries[key]; ok {
		b.bytes -= e.bytes
		delete(b.entries, key)
	}
}

func (b *bitmapCache) reset() {
	b.entries = nil
	b.bytes = 0
}

// purgeNil drops cached miss/placeholder entries (bmp == nil) whose keys are
// not currently pending, so newly downloaded covers, avatars, or thumbnails
// can be decoded without evicting valid cached bitmaps.
func (b *bitmapCache) purgeNil(pending map[string]bool) {
	for k, e := range b.entries {
		if e.bmp != nil {
			continue
		}
		if pending["thumb:"+k] || pending["avatar:"+k] || pending["hist-thumb:"+k] || pending[k] {
			continue
		}
		b.remove(k)
	}
}

func (b *bitmapCache) len() int { return len(b.entries) }

func (b *bitmapCache) evict(keep string) {
	for len(b.entries) > bitmapCacheMaxEntries || b.bytes > bitmapCacheMaxBytes {
		oldestKey, oldest := "", uint64(0)
		for k, e := range b.entries {
			if k == keep {
				continue
			}
			if oldestKey == "" || e.used < oldest {
				oldestKey, oldest = k, e.used
			}
		}
		if oldestKey == "" {
			return
		}
		b.remove(oldestKey)
	}
}
