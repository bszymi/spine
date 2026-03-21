package observe_test

import (
	"context"
	"testing"

	"github.com/bszymi/spine/internal/observe"
)

func TestGenerateTraceID(t *testing.T) {
	id1, err := observe.GenerateTraceID()
	if err != nil {
		t.Fatalf("GenerateTraceID: %v", err)
	}
	id2, err := observe.GenerateTraceID()
	if err != nil {
		t.Fatalf("GenerateTraceID: %v", err)
	}

	if id1 == "" {
		t.Fatal("trace ID should not be empty")
	}
	if id1 == id2 {
		t.Error("two generated trace IDs should be different")
	}
	// UUID-like format: 8-4-4-4-12 hex chars
	if len(id1) < 32 {
		t.Errorf("trace ID too short: %s", id1)
	}
}

func TestContextPropagation(t *testing.T) {
	ctx := context.Background()

	// Empty context returns empty strings
	if observe.TraceID(ctx) != "" {
		t.Error("expected empty trace_id from empty context")
	}
	if observe.RunID(ctx) != "" {
		t.Error("expected empty run_id from empty context")
	}
	if observe.StepID(ctx) != "" {
		t.Error("expected empty step_id from empty context")
	}
	if observe.ActorID(ctx) != "" {
		t.Error("expected empty actor_id from empty context")
	}
	if observe.ArtifactPath(ctx) != "" {
		t.Error("expected empty artifact_path from empty context")
	}
	if observe.Component(ctx) != "" {
		t.Error("expected empty component from empty context")
	}

	// Set all values
	ctx = observe.WithTraceID(ctx, "trace-abc")
	ctx = observe.WithRunID(ctx, "run-123")
	ctx = observe.WithStepID(ctx, "step-assign")
	ctx = observe.WithActorID(ctx, "actor-456")
	ctx = observe.WithArtifactPath(ctx, "governance/test.md")
	ctx = observe.WithComponent(ctx, "workflow_engine")

	// Verify all values
	if observe.TraceID(ctx) != "trace-abc" {
		t.Errorf("expected trace-abc, got %s", observe.TraceID(ctx))
	}
	if observe.RunID(ctx) != "run-123" {
		t.Errorf("expected run-123, got %s", observe.RunID(ctx))
	}
	if observe.StepID(ctx) != "step-assign" {
		t.Errorf("expected step-assign, got %s", observe.StepID(ctx))
	}
	if observe.ActorID(ctx) != "actor-456" {
		t.Errorf("expected actor-456, got %s", observe.ActorID(ctx))
	}
	if observe.ArtifactPath(ctx) != "governance/test.md" {
		t.Errorf("expected governance/test.md, got %s", observe.ArtifactPath(ctx))
	}
	if observe.Component(ctx) != "workflow_engine" {
		t.Errorf("expected workflow_engine, got %s", observe.Component(ctx))
	}
}

func TestTrailersFromContext(t *testing.T) {
	ctx := context.Background()
	ctx = observe.WithTraceID(ctx, "trace-t1")
	ctx = observe.WithActorID(ctx, "actor-a1")
	ctx = observe.WithRunID(ctx, "run-r1")

	trailers := observe.TrailersFromContext(ctx, "artifact.create")

	if trailers["Trace-ID"] != "trace-t1" {
		t.Errorf("expected Trace-ID=trace-t1, got %s", trailers["Trace-ID"])
	}
	if trailers["Actor-ID"] != "actor-a1" {
		t.Errorf("expected Actor-ID=actor-a1, got %s", trailers["Actor-ID"])
	}
	if trailers["Run-ID"] != "run-r1" {
		t.Errorf("expected Run-ID=run-r1, got %s", trailers["Run-ID"])
	}
	if trailers["Operation"] != "artifact.create" {
		t.Errorf("expected Operation=artifact.create, got %s", trailers["Operation"])
	}
}

func TestTrailersFromContextNoRun(t *testing.T) {
	ctx := context.Background()
	ctx = observe.WithTraceID(ctx, "trace-t2")
	ctx = observe.WithActorID(ctx, "actor-a2")
	// No run ID set

	trailers := observe.TrailersFromContext(ctx, "artifact.update")

	if trailers["Run-ID"] != "none" {
		t.Errorf("expected Run-ID=none when no run in context, got %s", trailers["Run-ID"])
	}
}

func TestTrailersFromEmptyContext(t *testing.T) {
	ctx := context.Background()
	trailers := observe.TrailersFromContext(ctx, "system.health")

	if trailers["Operation"] != "system.health" {
		t.Errorf("expected Operation=system.health, got %s", trailers["Operation"])
	}
	if trailers["Run-ID"] != "none" {
		t.Errorf("expected Run-ID=none, got %s", trailers["Run-ID"])
	}
	if _, ok := trailers["Trace-ID"]; ok {
		t.Error("Trace-ID should not be set for empty context")
	}
}
