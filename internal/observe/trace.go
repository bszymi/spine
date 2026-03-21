package observe

import (
	"context"
	"crypto/rand"
	"fmt"
)

type contextKey string

const (
	traceIDKey      contextKey = "trace_id"
	runIDKey        contextKey = "run_id"
	stepIDKey       contextKey = "step_id"
	actorIDKey      contextKey = "actor_id"
	artifactPathKey contextKey = "artifact_path"
	componentKey    contextKey = "component"
)

// GenerateTraceID creates a new random trace ID.
// Returns an error if the OS random number generator fails.
func GenerateTraceID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate trace ID: %w", err)
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

// WithTraceID adds a trace ID to the context.
func WithTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, traceIDKey, traceID)
}

// TraceID extracts the trace ID from context. Returns empty string if not set.
func TraceID(ctx context.Context) string {
	if v, ok := ctx.Value(traceIDKey).(string); ok {
		return v
	}
	return ""
}

// WithRunID adds a run ID to the context.
func WithRunID(ctx context.Context, runID string) context.Context {
	return context.WithValue(ctx, runIDKey, runID)
}

// RunID extracts the run ID from context.
func RunID(ctx context.Context) string {
	if v, ok := ctx.Value(runIDKey).(string); ok {
		return v
	}
	return ""
}

// WithStepID adds a step ID to the context.
func WithStepID(ctx context.Context, stepID string) context.Context {
	return context.WithValue(ctx, stepIDKey, stepID)
}

// StepID extracts the step ID from context.
func StepID(ctx context.Context) string {
	if v, ok := ctx.Value(stepIDKey).(string); ok {
		return v
	}
	return ""
}

// WithActorID adds an actor ID to the context.
func WithActorID(ctx context.Context, actorID string) context.Context {
	return context.WithValue(ctx, actorIDKey, actorID)
}

// ActorID extracts the actor ID from context.
func ActorID(ctx context.Context) string {
	if v, ok := ctx.Value(actorIDKey).(string); ok {
		return v
	}
	return ""
}

// WithArtifactPath adds an artifact path to the context.
func WithArtifactPath(ctx context.Context, path string) context.Context {
	return context.WithValue(ctx, artifactPathKey, path)
}

// ArtifactPath extracts the artifact path from context.
func ArtifactPath(ctx context.Context) string {
	if v, ok := ctx.Value(artifactPathKey).(string); ok {
		return v
	}
	return ""
}

// WithComponent adds a component name to the context.
func WithComponent(ctx context.Context, component string) context.Context {
	return context.WithValue(ctx, componentKey, component)
}

// Component extracts the component name from context.
func Component(ctx context.Context) string {
	if v, ok := ctx.Value(componentKey).(string); ok {
		return v
	}
	return ""
}

// TrailersFromContext builds Git commit trailers from context values.
// Per Git Integration §5.1.
func TrailersFromContext(ctx context.Context, operation string) map[string]string {
	trailers := map[string]string{
		"Operation": operation,
	}

	if v := TraceID(ctx); v != "" {
		trailers["Trace-ID"] = v
	}
	if v := ActorID(ctx); v != "" {
		trailers["Actor-ID"] = v
	}
	if v := RunID(ctx); v != "" {
		trailers["Run-ID"] = v
	} else {
		trailers["Run-ID"] = "none"
	}

	return trailers
}
