package observe

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Histogram tracks value distributions using predefined buckets.
type Histogram struct {
	mu      sync.Mutex
	buckets []histBucket
	sum     float64
	count   int64
}

type histBucket struct {
	le    float64 // upper bound
	count int64
}

// NewHistogram creates a histogram with the given bucket boundaries.
func NewHistogram(boundaries ...float64) *Histogram {
	buckets := make([]histBucket, len(boundaries)+1)
	for i, b := range boundaries {
		buckets[i] = histBucket{le: b}
	}
	buckets[len(boundaries)] = histBucket{le: float64(1<<63 - 1)} // +Inf
	return &Histogram{buckets: buckets}
}

// Observe records a value.
func (h *Histogram) Observe(v float64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sum += v
	h.count++
	for i := range h.buckets {
		if v <= h.buckets[i].le {
			h.buckets[i].count++
		}
	}
}

// ObserveDuration records a duration in seconds.
func (h *Histogram) ObserveDuration(d time.Duration) {
	h.Observe(d.Seconds())
}

// Gauge holds a single numeric value that can go up and down.
type Gauge struct {
	value atomic.Int64
}

// Set sets the gauge to the given value.
func (g *Gauge) Set(v int64) { g.value.Store(v) }

// Inc increments the gauge by 1.
func (g *Gauge) Inc() { g.value.Add(1) }

// Dec decrements the gauge by 1.
func (g *Gauge) Dec() { g.value.Add(-1) }

// Value returns the current gauge value.
func (g *Gauge) Value() int64 { return g.value.Load() }

// ExportPrometheus renders all global metrics in Prometheus text format.
func ExportPrometheus() string {
	var b strings.Builder
	m := GlobalMetrics

	// Counters
	writeCounter(&b, "spine_runs_started_total", m.RunsStarted.Value())
	writeCounter(&b, "spine_runs_completed_total", m.RunsCompleted.Value())
	writeCounter(&b, "spine_runs_failed_total", m.RunsFailed.Value())
	writeCounter(&b, "spine_steps_completed_total", m.StepsCompleted.Value())
	writeCounter(&b, "spine_steps_failed_total", m.StepsFailed.Value())
	writeCounter(&b, "spine_steps_retried_total", m.StepsRetried.Value())
	writeCounter(&b, "spine_artifacts_created_total", m.ArtifactsCreated.Value())
	writeCounter(&b, "spine_artifacts_updated_total", m.ArtifactsUpdated.Value())
	writeCounter(&b, "spine_git_commits_total", m.GitCommits.Value())
	writeCounter(&b, "spine_git_commit_retries_total", m.GitCommitRetries.Value())
	writeCounter(&b, "spine_events_emitted_total", m.EventsEmitted.Value())
	writeCounter(&b, "spine_projection_syncs_total", m.ProjectionSyncs.Value())
	writeCounter(&b, "spine_scheduler_scans_total", m.SchedulerScans.Value())
	writeCounter(&b, "spine_timeouts_detected_total", m.TimeoutsDetected.Value())
	writeCounter(&b, "spine_orphans_detected_total", m.OrphansDetected.Value())
	writeCounter(&b, "spine_recoveries_executed_total", m.RecoveriesExecuted.Value())

	// Gauges
	writeGauge(&b, "spine_active_runs", m.ActiveRuns.Value())
	writeGauge(&b, "spine_active_steps", m.ActiveSteps.Value())

	// Histograms
	writeHistogram(&b, "spine_run_duration_seconds", m.RunDuration)
	writeHistogram(&b, "spine_step_duration_seconds", m.StepDuration)

	return b.String()
}

func writeCounter(b *strings.Builder, name string, value int64) {
	fmt.Fprintf(b, "# TYPE %s counter\n%s %d\n", name, name, value)
}

func writeGauge(b *strings.Builder, name string, value int64) {
	fmt.Fprintf(b, "# TYPE %s gauge\n%s %d\n", name, name, value)
}

func writeHistogram(b *strings.Builder, name string, h *Histogram) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	fmt.Fprintf(b, "# TYPE %s histogram\n", name)
	for _, bucket := range h.buckets {
		if bucket.le >= float64(1<<62) {
			fmt.Fprintf(b, "%s_bucket{le=\"+Inf\"} %d\n", name, bucket.count)
		} else {
			fmt.Fprintf(b, "%s_bucket{le=\"%.3f\"} %d\n", name, bucket.le, bucket.count)
		}
	}
	fmt.Fprintf(b, "%s_sum %f\n", name, h.sum)
	fmt.Fprintf(b, "%s_count %d\n", name, h.count)
}
