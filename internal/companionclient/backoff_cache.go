package companionclient

import (
	"sync"
	"time"
)

var defaultBackoffSteps = []time.Duration{
	2 * time.Second,
	5 * time.Second,
	10 * time.Second,
	30 * time.Second,
}

// ReconnectSchedule implements the foreground reconnect backoff (2s, 5s, 10s, 30s, ...).
type ReconnectSchedule struct {
	steps []time.Duration
	index int
}

// NewReconnectSchedule returns a schedule using design §7.4 intervals.
func NewReconnectSchedule() *ReconnectSchedule {
	return &ReconnectSchedule{steps: defaultBackoffSteps}
}

// Next returns the next delay and advances the schedule.
func (s *ReconnectSchedule) Next() time.Duration {
	if len(s.steps) == 0 {
		return 30 * time.Second
	}
	if s.index >= len(s.steps) {
		return s.steps[len(s.steps)-1]
	}
	d := s.steps[s.index]
	s.index++
	return d
}

// Reset resets the backoff back to the initial 2s step.
func (s *ReconnectSchedule) Reset() {
	s.index = 0
}

// DefaultImageCacheBytes is the 128 MiB in-memory image cache budget (design §7.2).
const DefaultImageCacheBytes = 128 << 20

type imageEntry struct {
	key  string
	data []byte
	prev *imageEntry
	next *imageEntry
}

// MemoryImageCache is an in-memory LRU byte cache for images fetched from the desktop.
// It never writes to disk and is cleared immediately on disconnect (NFR-MOB-DATA-001).
type MemoryImageCache struct {
	mu       sync.Mutex
	maxBytes int64
	curBytes int64
	entries  map[string]*imageEntry
	head     *imageEntry // most recently used
	tail     *imageEntry // least recently used
}

// NewMemoryImageCache creates an in-memory LRU cache bounded by maxBytes (defaults to 128 MiB).
func NewMemoryImageCache(maxBytes int64) *MemoryImageCache {
	if maxBytes <= 0 {
		maxBytes = DefaultImageCacheBytes
	}
	return &MemoryImageCache{
		maxBytes: maxBytes,
		entries:  make(map[string]*imageEntry),
	}
}

// Get returns a copy of cached bytes for key and promotes it to MRU.
func (c *MemoryImageCache) Get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ent, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	c.moveToFront(ent)
	out := make([]byte, len(ent.data))
	copy(out, ent.data)
	return out, true
}

// Put stores a copy of data under key, evicting least-recently-used entries if needed.
func (c *MemoryImageCache) Put(key string, data []byte) {
	if key == "" || int64(len(data)) > c.maxBytes {
		return
	}
	cp := make([]byte, len(data))
	copy(cp, data)

	c.mu.Lock()
	defer c.mu.Unlock()
	if ent, ok := c.entries[key]; ok {
		c.curBytes -= int64(len(ent.data))
		ent.data = cp
		c.curBytes += int64(len(cp))
		c.moveToFront(ent)
	} else {
		ent := &imageEntry{key: key, data: cp}
		c.entries[key] = ent
		c.curBytes += int64(len(cp))
		c.pushFront(ent)
	}
	for c.curBytes > c.maxBytes && c.tail != nil {
		c.removeEntry(c.tail)
	}
}

// Clear drops all cached images from memory (used on disconnect / background / unpair).
func (c *MemoryImageCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	clear(c.entries)
	c.head = nil
	c.tail = nil
	c.curBytes = 0
}

// Bytes returns the current total size of cached images in bytes.
func (c *MemoryImageCache) Bytes() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.curBytes
}

func (c *MemoryImageCache) pushFront(e *imageEntry) {
	e.prev = nil
	e.next = c.head
	if c.head != nil {
		c.head.prev = e
	}
	c.head = e
	if c.tail == nil {
		c.tail = e
	}
}

func (c *MemoryImageCache) moveToFront(e *imageEntry) {
	if c.head == e {
		return
	}
	c.unlink(e)
	c.pushFront(e)
}

func (c *MemoryImageCache) unlink(e *imageEntry) {
	if e.prev != nil {
		e.prev.next = e.next
	} else {
		c.head = e.next
	}
	if e.next != nil {
		e.next.prev = e.prev
	} else {
		c.tail = e.prev
	}
	e.prev = nil
	e.next = nil
}

func (c *MemoryImageCache) removeEntry(e *imageEntry) {
	c.unlink(e)
	delete(c.entries, e.key)
	c.curBytes -= int64(len(e.data))
}
