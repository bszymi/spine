package observe_test

import (
	"context"
	"testing"

	"github.com/bszymi/spine/internal/observe"
)

func TestSetupLogger(t *testing.T) {
	// Should not panic for any level/format combination
	levels := []string{"debug", "info", "warn", "error", "unknown", ""}
	formats := []string{"json", "text", "unknown", ""}

	for _, level := range levels {
		for _, format := range formats {
			observe.SetupLogger(level, format)
		}
	}
}

func TestLoggerWithContext(t *testing.T) {
	observe.SetupLogger("info", "json")

	ctx := context.Background()
	ctx = observe.WithTraceID(ctx, "trace-log")
	ctx = observe.WithRunID(ctx, "run-log")
	ctx = observe.WithComponent(ctx, "test_component")

	logger := observe.Logger(ctx)
	if logger == nil {
		t.Fatal("Logger should not be nil")
	}

	// Should not panic when logging with enriched context
	logger.Info("test message", "extra_key", "extra_value")
}

func TestLoggerAllContextFields(t *testing.T) {
	observe.SetupLogger("debug", "json")

	ctx := context.Background()
	ctx = observe.WithTraceID(ctx, "trace-all")
	ctx = observe.WithRunID(ctx, "run-all")
	ctx = observe.WithStepID(ctx, "step-all")
	ctx = observe.WithActorID(ctx, "actor-all")
	ctx = observe.WithArtifactPath(ctx, "governance/test.md")
	ctx = observe.WithComponent(ctx, "test_all")

	logger := observe.Logger(ctx)
	if logger == nil {
		t.Fatal("Logger should not be nil")
	}
	// All 6 context fields should be enriched — just verify no panic
	logger.Info("all fields test")
}

func TestLoggerPartialContext(t *testing.T) {
	observe.SetupLogger("info", "json")

	// Only some fields set
	ctx := context.Background()
	ctx = observe.WithTraceID(ctx, "trace-partial")
	ctx = observe.WithComponent(ctx, "partial")

	logger := observe.Logger(ctx)
	logger.Warn("partial context test")
}

func TestLoggerEmptyContext(t *testing.T) {
	observe.SetupLogger("debug", "text")

	ctx := context.Background()
	logger := observe.Logger(ctx)
	if logger == nil {
		t.Fatal("Logger should not be nil for empty context")
	}

	logger.Debug("debug message")
}
