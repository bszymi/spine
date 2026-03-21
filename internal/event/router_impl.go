package event

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/queue"
)

// QueueRouter implements EventRouter backed by a Queue.
// Events are published as queue entries with type "event_delivery".
type QueueRouter struct {
	queue    queue.Queue
	handlers map[domain.EventType][]EventHandler
}

// NewQueueRouter creates a new EventRouter backed by the given queue.
func NewQueueRouter(q queue.Queue) *QueueRouter {
	return &QueueRouter{
		queue:    q,
		handlers: make(map[domain.EventType][]EventHandler),
	}
}

// Emit publishes an event to the queue for async delivery.
func (r *QueueRouter) Emit(ctx context.Context, event domain.Event) error {
	if event.EventID == "" {
		return fmt.Errorf("event_id is required")
	}
	if event.Type == "" {
		return fmt.Errorf("event type is required")
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}

	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	return r.queue.Publish(ctx, queue.Entry{
		EntryID:        event.EventID,
		EntryType:      "event_delivery",
		Payload:        payload,
		IdempotencyKey: event.EventID, // Events are idempotent by event ID
		CreatedAt:      event.Timestamp,
	})
}

// Subscribe registers a handler for a specific event type.
// The handler is also registered with the underlying queue to receive deliveries.
func (r *QueueRouter) Subscribe(ctx context.Context, eventType domain.EventType, handler EventHandler) error {
	r.handlers[eventType] = append(r.handlers[eventType], handler)

	// Register a queue handler that deserializes and dispatches
	return r.queue.Subscribe(ctx, "event_delivery", func(ctx context.Context, entry queue.Entry) error {
		var event domain.Event
		if err := json.Unmarshal(entry.Payload, &event); err != nil {
			return fmt.Errorf("unmarshal event: %w", err)
		}

		// Only dispatch to handlers that match this event type
		if event.Type != eventType {
			return nil
		}

		return handler(ctx, event)
	})
}
