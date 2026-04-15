package delivery

import (
	"sync"
	"sync/atomic"

	"github.com/bszymi/spine/internal/domain"
)

// EventBroadcaster fans out events to registered SSE listeners.
// Listeners can be added and removed without leaking.
type EventBroadcaster struct {
	mu        sync.RWMutex
	listeners map[int64]chan<- domain.Event
	nextID    atomic.Int64
}

// NewEventBroadcaster creates a new broadcaster.
func NewEventBroadcaster() *EventBroadcaster {
	return &EventBroadcaster{
		listeners: make(map[int64]chan<- domain.Event),
	}
}

// Subscribe registers a channel to receive events. Returns an ID
// that must be passed to Unsubscribe when the listener is done.
func (b *EventBroadcaster) Subscribe(ch chan<- domain.Event) int64 {
	id := b.nextID.Add(1)
	b.mu.Lock()
	b.listeners[id] = ch
	b.mu.Unlock()
	return id
}

// Unsubscribe removes a listener by ID.
func (b *EventBroadcaster) Unsubscribe(id int64) {
	b.mu.Lock()
	delete(b.listeners, id)
	b.mu.Unlock()
}

// Broadcast sends an event to all registered listeners.
// Non-blocking: if a listener's channel is full, the event is dropped.
func (b *EventBroadcaster) Broadcast(evt domain.Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	for _, ch := range b.listeners {
		select {
		case ch <- evt:
		default:
			// Drop if channel full — consumer too slow
		}
	}
}
