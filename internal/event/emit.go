package event

import (
	"context"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/observe"
)

// Emitter is the minimal surface EmitLogged needs. Keeping it narrower than
// EventRouter lets callers (e.g. the engine) pass their own
// single-method emit interface without adopting the full router contract.
type Emitter interface {
	Emit(ctx context.Context, event domain.Event) error
}

// EmitLogged emits a domain event with fire-and-forget semantics: any error
// from the router is logged via observe.Logger(ctx) at Warn level and not
// propagated to the caller. Missing Timestamp and TraceID fields are
// populated from time.Now() and observe.TraceID(ctx) respectively.
//
// This consolidates the 10+ copies of the same "fill fields, emit, log
// warning on error" block across engine, artifact, gateway, scheduler,
// divergence, projection, and actor packages. Callers that need to
// propagate the error should call Emit directly.
func EmitLogged(ctx context.Context, router Emitter, ev domain.Event) {
	if router == nil {
		return
	}
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now()
	}
	if ev.TraceID == "" {
		ev.TraceID = observe.TraceID(ctx)
	}
	if err := router.Emit(ctx, ev); err != nil {
		observe.Logger(ctx).Warn("failed to emit event", "event_type", ev.Type, "error", err)
	}
}
