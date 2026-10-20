package engine

import (
	"context"
	"encoding/json"

	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/event"
)

// emitEvent emits a domain event with fire-and-forget semantics.
// Failures are logged as warnings via event.EmitLogged, not propagated to callers.
func (o *Orchestrator) emitEvent(ctx context.Context, eventType domain.EventType, runID, traceID, eventID string, payload json.RawMessage) {
	o.emitEventAs(ctx, eventType, runID, traceID, "", eventID, payload)
}

// emitEventAs is emitEvent with the envelope's ActorID set.
func (o *Orchestrator) emitEventAs(ctx context.Context, eventType domain.EventType, runID, traceID, actorID, eventID string, payload json.RawMessage) {
	event.EmitLogged(ctx, o.events, domain.Event{
		EventID: eventID,
		Type:    eventType,
		RunID:   runID,
		TraceID: traceID,
		ActorID: actorID,
		Payload: payload,
	})
}
