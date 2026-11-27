package mediaview

import (
	"container/list"
	"context"
	"sync"
	"time"

	"github.com/chialecode/trace/cmd/trace-mobile/internal/pagekit"
	"github.com/egoist/mygo/ui"
)

const (
	defaultMaxDecodedImages = 120
	maxConcurrentImageLoads = 6
)

type decodedEntry struct {
	path string
	src  ui.ImageSource
}

// ImageLoader manages bounded in-memory decoded ui.ImageSource objects fetched
// via Host.FetchImage. It never writes to disk (NFR-MOB-DATA-001) and clears
// immediately on disconnect.
type ImageLoader struct {
	mu       sync.Mutex
	maxItems int
	gen      uint64
	lru      *list.List
	entries  map[string]*list.Element
	inflight map[string]bool
	failed   map[string]time.Time
	sem      chan struct{}
}

// NewImageLoader creates a bounded in-memory decoded image loader.
func NewImageLoader(maxItems int) *ImageLoader {
	if maxItems <= 0 {
		maxItems = defaultMaxDecodedImages
	}
	return &ImageLoader{
		maxItems: maxItems,
		lru:      list.New(),
		entries:  make(map[string]*list.Element, maxItems),
		inflight: make(map[string]bool),
		failed:   make(map[string]time.Time),
		sem:      make(chan struct{}, maxConcurrentImageLoads),
	}
}

// Clear drops all decoded images and invalidates in-flight fetches (called on disconnect).
func (l *ImageLoader) Clear() {
	l.mu.Lock()
	l.gen++
	l.lru.Init()
	clear(l.entries)
	clear(l.inflight)
	clear(l.failed)
	l.mu.Unlock()
}

// Len returns the number of decoded images currently in memory.
func (l *ImageLoader) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lru.Len()
}

// GetOrLoad returns a cached decoded image immediately, or schedules an
// asynchronous bounded fetch via Host.FetchImage and notifies Host.Update on completion.
func (l *ImageLoader) GetOrLoad(h pagekit.Host, path string) (src ui.ImageSource, loading bool, failed bool) {
	if path == "" || h == nil {
		return nil, false, false
	}
	l.mu.Lock()
	if elem, ok := l.entries[path]; ok {
		l.lru.MoveToFront(elem)
		src = elem.Value.(*decodedEntry).src
		l.mu.Unlock()
		return src, false, false
	}
	if ts, ok := l.failed[path]; ok && time.Since(ts) < 15*time.Second {
		l.mu.Unlock()
		return nil, false, true
	}
	if l.inflight[path] {
		l.mu.Unlock()
		return nil, true, false
	}
	if h.Client() == nil {
		l.mu.Unlock()
		return nil, false, false
	}
	l.inflight[path] = true
	gen := l.gen
	l.mu.Unlock()

	go l.fetchAndDecode(h, path, gen)
	return nil, true, false
}

// Retry clears any cached failure for path and triggers a fresh load.
func (l *ImageLoader) Retry(h pagekit.Host, path string) {
	if path == "" {
		return
	}
	l.mu.Lock()
	delete(l.failed, path)
	l.mu.Unlock()
	l.GetOrLoad(h, path)
}

func (l *ImageLoader) fetchAndDecode(h pagekit.Host, path string, gen uint64) {
	l.sem <- struct{}{}
	defer func() { <-l.sem }()

	l.mu.Lock()
	stale := l.gen != gen
	l.mu.Unlock()
	if stale {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	raw, err := h.FetchImage(ctx, path)
	var decoded ui.ImageSource
	if err == nil && len(raw) > 0 {
		decoded, err = ui.DecodeImage(raw)
	}

	l.mu.Lock()
	delete(l.inflight, path)
	if l.gen != gen {
		l.mu.Unlock()
		return
	}
	if err != nil || decoded == nil {
		l.failed[path] = time.Now()
		l.mu.Unlock()
		h.Update(nil)
		return
	}
	delete(l.failed, path)
	if elem, ok := l.entries[path]; ok {
		elem.Value.(*decodedEntry).src = decoded
		l.lru.MoveToFront(elem)
	} else {
		elem := l.lru.PushFront(&decodedEntry{path: path, src: decoded})
		l.entries[path] = elem
		for l.lru.Len() > l.maxItems {
			oldest := l.lru.Back()
			if oldest == nil {
				break
			}
			l.lru.Remove(oldest)
			delete(l.entries, oldest.Value.(*decodedEntry).path)
		}
	}
	l.mu.Unlock()

	h.Update(nil)
}
