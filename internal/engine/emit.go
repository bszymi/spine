package engine

import (
	"context"
	"encoding/json"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/event"
)

// emitEvent emits a domain event with fire-and-forget semantics.
// Failures are logged as warnings via event.EmitLogged, not propagated to callers.
func (o *Orchestrator) emitEvent(ctx context.Context, eventType domain.EventType, runID, traceID, eventID string, payload json.RawMessage) {
	event.EmitLogged(ctx, o.events, domain.Event{
		EventID: eventID,
		Type:    eventType,
		RunID:   runID,
		TraceID: traceID,
		Payload: payload,
	})
}
