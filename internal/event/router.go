package event

import (
	"context"

	"github.com/bszymi/spine/internal/domain"
)

// EventRouter defines the interface for emitting and subscribing to domain events.
// Per Implementation Guide §3.3.
type EventRouter interface {
	Emit(ctx context.Context, event domain.Event) error
	Subscribe(ctx context.Context, eventType domain.EventType, handler EventHandler) error
}

// EventHandler processes a domain event.
type EventHandler func(ctx context.Context, event domain.Event) error
