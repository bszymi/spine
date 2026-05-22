package observe

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestHistogram_Observe(t *testing.T) {
	h := NewHistogram(1, 5, 10)

	h.Observe(0.5)
	h.Observe(3)
	h.Observe(7)
	h.Observe(15)

	if h.count != 4 {
		t.Errorf("expected count 4, got %d", h.count)
	}
	if h.sum != 25.5 {
		t.Errorf("expected sum 25.5, got %f", h.sum)
	}

	// bucket le=1: only 0.5
	if h.buckets[0].count != 1 {
		t.Errorf("bucket le=1: expected 1, got %d", h.buckets[0].count)
	}
	// bucket le=5: 0.5 + 3
	if h.buckets[1].count != 2 {
		t.Errorf("bucket le=5: expected 2, got %d", h.buckets[1].count)
	}
	// bucket le=10: 0.5 + 3 + 7
	if h.buckets[2].count != 3 {
		t.Errorf("bucket le=10: expected 3, got %d", h.buckets[2].count)
	}
	// +Inf: all 4
	if h.buckets[3].count != 4 {
		t.Errorf("bucket +Inf: expected 4, got %d", h.buckets[3].count)
	}
}

func TestHistogram_ObserveDuration(t *testing.T) {
	h := NewHistogram(1, 5)
	h.ObserveDuration(2 * time.Second)

	if h.count != 1 {
		t.Errorf("expected count 1, got %d", h.count)
	}
	if h.sum < 1.9 || h.sum > 2.1 {
		t.Errorf("expected sum ~2.0, got %f", h.sum)
	}
}

func TestGauge(t *testing.T) {
	var g Gauge
	g.Set(10)
	if g.Value() != 10 {
		t.Errorf("expected 10, got %d", g.Value())
	}
	g.Inc()
	if g.Value() != 11 {
		t.Errorf("expected 11, got %d", g.Value())
	}
	g.Dec()
	if g.Value() != 10 {
		t.Errorf("expected 10, got %d", g.Value())
	}
}

func TestExportPrometheus(t *testing.T) {
	// Reset metrics for clean test.
	GlobalMetrics.RunsStarted = Counter{}
	GlobalMetrics.RunsStarted.Inc()
	GlobalMetrics.RunsStarted.Inc()

	output := ExportPrometheus()

	if !strings.Contains(output, "spine_runs_started_total 2") {
		t.Errorf("expected runs_started_total 2 in output:\n%s", output)
	}
	if !strings.Contains(output, "# TYPE spine_runs_started_total counter") {
		t.Error("expected counter TYPE annotation")
	}
	if !strings.Contains(output, "# TYPE spine_active_runs gauge") {
		t.Error("expected gauge TYPE annotation")
	}
	if !strings.Contains(output, "# TYPE spine_run_duration_seconds histogram") {
		t.Error("expected histogram TYPE annotation")
	}
	if !strings.Contains(output, "_bucket{le=\"+Inf\"}") {
		t.Error("expected +Inf bucket")
	}
}

func TestAuditLog(t *testing.T) {
	// Just verify it doesn't panic with nil context values.
	ctx := context.Background()
	AuditLog(ctx, "test_operation", "key", "value")
}
