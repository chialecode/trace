package companion

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/companionapi"
)

type wsClient struct {
	id         uint64
	deviceID   string
	conn       *wsConn
	pingEvery  time.Duration
	pongWait   time.Duration
	coalesce   time.Duration
	now        func() time.Time
	mu         sync.Mutex
	queue      []companionapi.EventFrame
	needsSync  bool
	lastProgAt map[string]time.Time
	pendingPrg map[string]companionapi.EventFrame
	notify     chan struct{}
	closed     bool
	byeMu      sync.Mutex
	done       chan struct{}
}

type eventHub struct {
	mu          sync.Mutex
	nextID      uint64
	clients     map[uint64]*wsClient
	pingEvery   time.Duration
	pongWait    time.Duration
	coalesce    time.Duration
	now         func() time.Time
	onStateDiff func()
}

func newEventHub(now func() time.Time, onStateDiff func()) *eventHub {
	if now == nil {
		now = time.Now
	}
	return &eventHub{
		clients:     make(map[uint64]*wsClient),
		pingEvery:   companionapi.WSPingInterval,
		pongWait:    companionapi.WSPongTimeout,
		coalesce:    time.Second,
		now:         now,
		onStateDiff: onStateDiff,
	}
}

func isProgressEvent(typ string) bool {
	switch typ {
	case companionapi.EventTaskChanged,
		companionapi.EventCacheChanged,
		companionapi.EventCacheQueueUpdated,
		companionapi.EventLiveChanged,
		companionapi.EventTransfersChanged:
		return true
	default:
		return false
	}
}

func (h *eventHub) Add(deviceID string, conn *wsConn) *wsClient {
	h.mu.Lock()
	h.nextID++
	id := h.nextID
	c := &wsClient{
		id:         id,
		deviceID:   deviceID,
		conn:       conn,
		pingEvery:  h.pingEvery,
		pongWait:   h.pongWait,
		coalesce:   h.coalesce,
		now:        h.now,
		lastProgAt: make(map[string]time.Time),
		pendingPrg: make(map[string]companionapi.EventFrame),
		notify:     make(chan struct{}, 1),
		done:       make(chan struct{}),
	}
	h.clients[id] = c
	onDiff := h.onStateDiff
	h.mu.Unlock()

	if onDiff != nil {
		onDiff()
	}
	go h.runClient(c)
	return c
}

func (h *eventHub) remove(c *wsClient) {
	h.mu.Lock()
	_, existed := h.clients[c.id]
	delete(h.clients, c.id)
	onDiff := h.onStateDiff
	h.mu.Unlock()
	if existed && onDiff != nil {
		onDiff()
	}
}

func (h *eventHub) OnlineDeviceIDs() map[string]bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[string]bool, len(h.clients))
	for _, c := range h.clients {
		out[c.deviceID] = true
	}
	return out
}

func (h *eventHub) ClientCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

// Publish broadcasts an event frame to every connected phone without blocking (INV-13).
func (h *eventHub) Publish(frame companionapi.EventFrame) {
	if frame.Timestamp == "" {
		frame.Timestamp = h.now().UTC().Format(time.RFC3339)
	}
	h.mu.Lock()
	list := make([]*wsClient, 0, len(h.clients))
	for _, c := range h.clients {
		list = append(list, c)
	}
	h.mu.Unlock()

	for _, c := range list {
		c.enqueue(frame)
	}
}

// RevokeDevice sends bye{reason: "revoked"} to all active connections for deviceID and closes them.
func (h *eventHub) RevokeDevice(deviceID string, maxWait time.Duration) {
	h.mu.Lock()
	var targets []*wsClient
	for _, c := range h.clients {
		if c.deviceID == deviceID {
			targets = append(targets, c)
		}
	}
	h.mu.Unlock()
	h.sendByeAndClose(targets, companionapi.ByeReasonRevoked, maxWait)
}

// BroadcastBye sends bye{reason} to all connected phones and waits at most maxWait (<= 1s)
// before closing every connection (docs/design/mobile.md §3).
func (h *eventHub) BroadcastBye(reason string, maxWait time.Duration) {
	h.mu.Lock()
	targets := make([]*wsClient, 0, len(h.clients))
	for _, c := range h.clients {
		targets = append(targets, c)
	}
	h.mu.Unlock()
	h.sendByeAndClose(targets, reason, maxWait)
}

func (h *eventHub) sendByeAndClose(targets []*wsClient, reason string, maxWait time.Duration) {
	if len(targets) == 0 {
		return
	}
	if maxWait <= 0 {
		maxWait = time.Second
	}
	frame := companionapi.EventFrame{
		Type:      companionapi.EventBye,
		Reason:    reason,
		Timestamp: h.now().UTC().Format(time.RFC3339),
	}
	raw, _ := json.Marshal(frame)
	var wg sync.WaitGroup
	for _, c := range targets {
		wg.Add(1)
		go func(cl *wsClient) {
			defer wg.Done()
			cl.byeMu.Lock()
			cl.markClosing()
			_ = cl.conn.WriteText(raw, maxWait)
			_ = cl.conn.WriteClose(1000, reason, 200*time.Millisecond)
			cl.byeMu.Unlock()
			_ = cl.conn.Close()
		}(c)
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(maxWait):
		for _, c := range targets {
			_ = c.conn.Close()
		}
	}
}

func (c *wsClient) markClosing() {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	c.signal()
}

func (c *wsClient) signal() {
	select {
	case c.notify <- struct{}{}:
	default:
	}
}

// enqueue appends frame to the connection's bounded 256-entry queue without blocking.
// Progress events are coalesced to at most once per second; when full, the oldest
// progress event is evicted and needsSync is marked (docs/design/mobile.md §5.4).
func (c *wsClient) enqueue(frame companionapi.EventFrame) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	if isProgressEvent(frame.Type) && c.coalesce > 0 {
		now := c.now()
		if last, ok := c.lastProgAt[frame.Type]; ok && now.Sub(last) < c.coalesce {
			c.pendingPrg[frame.Type] = frame
			c.mu.Unlock()
			return
		}
		c.lastProgAt[frame.Type] = now
	}
	c.pushLocked(frame)
	c.mu.Unlock()
	c.signal()
}

func (c *wsClient) pushLocked(frame companionapi.EventFrame) {
	if len(c.queue) >= companionapi.EventQueueLimit {
		evicted := false
		for i, existing := range c.queue {
			if isProgressEvent(existing.Type) {
				c.queue = append(c.queue[:i], c.queue[i+1:]...)
				evicted = true
				break
			}
		}
		if !evicted && len(c.queue) > 0 {
			c.queue = c.queue[1:]
		}
		c.needsSync = true
	}
	c.queue = append(c.queue, frame)
}

func (c *wsClient) popBatch() ([]companionapi.EventFrame, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, true
	}
	now := c.now()
	for typ, pending := range c.pendingPrg {
		if now.Sub(c.lastProgAt[typ]) >= c.coalesce {
			c.lastProgAt[typ] = now
			delete(c.pendingPrg, typ)
			c.pushLocked(pending)
		}
	}
	if c.needsSync && len(c.queue) == 0 {
		c.needsSync = false
		return []companionapi.EventFrame{{
			Type:      companionapi.EventResync,
			NeedsSync: true,
			Timestamp: now.UTC().Format(time.RFC3339),
		}}, false
	}
	if len(c.queue) == 0 {
		return nil, false
	}
	batch := c.queue
	c.queue = nil
	if c.needsSync {
		c.needsSync = false
		batch = append(batch, companionapi.EventFrame{
			Type:      companionapi.EventResync,
			NeedsSync: true,
			Timestamp: now.UTC().Format(time.RFC3339),
		})
	}
	return batch, false
}

func (h *eventHub) runClient(c *wsClient) {
	defer func() {
		c.markClosing()
		c.byeMu.Lock()
		_ = c.conn.Close()
		c.byeMu.Unlock()
		h.remove(c)
		close(c.done)
	}()

	// Reader goroutine handles Pong / Ping / Close and enforces pongWait read deadline.
	readErr := make(chan error, 1)
	go func() {
		for {
			op, payload, err := c.conn.ReadFrame(time.Now().Add(c.pongWait))
			if err != nil {
				readErr <- err
				return
			}
			switch op {
			case wsOpPing:
				_ = c.conn.WritePong(payload, 2*time.Second)
			case wsOpPong:
				// Read deadline refreshes on next loop iteration.
			case wsOpClose:
				_ = c.conn.WriteClose(1000, "", time.Second)
				readErr <- nil
				return
			}
		}
	}()

	pingTicker := time.NewTicker(c.pingEvery)
	defer pingTicker.Stop()

	flushEvery := c.coalesce
	if flushEvery <= 0 {
		flushEvery = time.Second
	}
	flushTicker := time.NewTicker(flushEvery)
	defer flushTicker.Stop()

	for {
		select {
		case <-readErr:
			return
		case <-c.conn.closed:
			return
		case <-pingTicker.C:
			if err := c.conn.WritePing(3 * time.Second); err != nil {
				return
			}
		case <-flushTicker.C:
			if !c.flushPending() {
				return
			}
		case <-c.notify:
			if !c.flushPending() {
				return
			}
		}
	}
}

func (c *wsClient) flushPending() bool {
	batch, closed := c.popBatch()
	if closed {
		return false
	}
	for _, ev := range batch {
		raw, err := json.Marshal(ev)
		if err != nil {
			continue
		}
		if err := c.conn.WriteText(raw, 5*time.Second); err != nil {
			return false
		}
	}
	return true
}
