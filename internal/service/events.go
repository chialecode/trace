package service

import (
	"sync"
	"time"
)

// EventType identifies backend domain events emitted to the UI shell.
type EventType string

const (
	EventAccountChanged      EventType = "account.changed"
	EventSettingsChanged     EventType = "settings.changed"
	EventTaskUpdated         EventType = "download.updated"
	EventLibraryChanged      EventType = "library.changed"
	EventSubscriptionChecked EventType = "subscription.checked"
	EventLiveUpdated         EventType = "live.updated"
)

// Event carries a backend state change notification.
type Event struct {
	Type      EventType `json:"type"`
	Message   string    `json:"message,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

type eventBus struct {
	mu          sync.RWMutex
	nextID      int
	subscribers map[int]func(Event)
	recent      []Event
}

func newEventBus() *eventBus {
	return &eventBus{
		subscribers: make(map[int]func(Event)),
	}
}

func (b *eventBus) Subscribe(fn func(Event)) func() {
	b.mu.Lock()
	id := b.nextID
	b.nextID++
	b.subscribers[id] = fn
	b.mu.Unlock()

	return func() {
		b.mu.Lock()
		delete(b.subscribers, id)
		b.mu.Unlock()
	}
}

func (b *eventBus) Publish(evtType EventType, msg string) {
	evt := Event{
		Type:      evtType,
		Message:   msg,
		Timestamp: time.Now().UTC(),
	}
	b.mu.Lock()
	b.recent = append(b.recent, evt)
	if len(b.recent) > 100 {
		b.recent = b.recent[len(b.recent)-100:]
	}
	subs := make([]func(Event), 0, len(b.subscribers))
	for _, fn := range b.subscribers {
		subs = append(subs, fn)
	}
	b.mu.Unlock()

	for _, fn := range subs {
		if fn != nil {
			fn(evt)
		}
	}
}

func (b *eventBus) Recent() []Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Event, len(b.recent))
	copy(out, b.recent)
	return out
}
