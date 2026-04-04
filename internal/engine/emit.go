package engine

import (
	"context"
	"encoding/json"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/observe"
)

// emitEvent emits a domain event with fire-and-forget semantics.
// Failures are logged as warnings, not propagated to callers.
func (o *Orchestrator) emitEvent(ctx context.Context, eventType domain.EventType, runID, traceID, eventID string, payload json.RawMessage) {
	if err := o.events.Emit(ctx, domain.Event{
		EventID:   eventID,
		Type:      eventType,
		Timestamp: time.Now(),
		RunID:     runID,
		TraceID:   traceID,
		Payload:   payload,
	}); err != nil {
		observe.Logger(ctx).Warn("failed to emit event", "event_type", eventType, "error", err)
	}
}
