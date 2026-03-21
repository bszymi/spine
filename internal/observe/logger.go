package observe

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

// SetupLogger configures the global slog logger based on environment settings.
// Per Observability §5.1 and Docker Runtime §14.
func SetupLogger(level, format string) {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "info":
		lvl = slog.LevelInfo
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: lvl}

	var handler slog.Handler
	switch strings.ToLower(format) {
	case "text":
		handler = slog.NewTextHandler(os.Stdout, opts)
	default:
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}

	slog.SetDefault(slog.New(handler))
}

// Logger returns a logger enriched with context values (trace_id, run_id, etc.).
// Per Observability §5.1 standard fields.
func Logger(ctx context.Context) *slog.Logger {
	logger := slog.Default()

	if v := Component(ctx); v != "" {
		logger = logger.With("component", v)
	}
	if v := TraceID(ctx); v != "" {
		logger = logger.With("trace_id", v)
	}
	if v := RunID(ctx); v != "" {
		logger = logger.With("run_id", v)
	}
	if v := StepID(ctx); v != "" {
		logger = logger.With("step_id", v)
	}
	if v := ActorID(ctx); v != "" {
		logger = logger.With("actor_id", v)
	}
	if v := ArtifactPath(ctx); v != "" {
		logger = logger.With("artifact_path", v)
	}

	return logger
}
