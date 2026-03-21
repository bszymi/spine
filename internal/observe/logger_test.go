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

func TestLoggerEmptyContext(t *testing.T) {
	observe.SetupLogger("debug", "text")

	ctx := context.Background()
	logger := observe.Logger(ctx)
	if logger == nil {
		t.Fatal("Logger should not be nil for empty context")
	}

	logger.Debug("debug message")
}
