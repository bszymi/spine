package observe

import (
	"sync"
	"sync/atomic"
)

// Counter is a simple thread-safe counter for metrics scaffolding.
// Per Observability §7 — full metrics implementation is deferred,
// but the interface is established for future instrumentation.
type Counter struct {
	value atomic.Int64
}

// Inc increments the counter by 1.
func (c *Counter) Inc() {
	c.value.Add(1)
}

// Add increments the counter by the given delta.
func (c *Counter) Add(delta int64) {
	c.value.Add(delta)
}

// Value returns the current counter value.
func (c *Counter) Value() int64 {
	return c.value.Load()
}

// Metrics holds all Spine runtime metrics.
type Metrics struct {
	// Counters
	RunsStarted        Counter
	RunsCompleted      Counter
	RunsFailed         Counter
	StepsCompleted     Counter
	StepsFailed        Counter
	StepsRetried       Counter
	ArtifactsCreated   Counter
	ArtifactsUpdated   Counter
	GitCommits         Counter
	GitCommitRetries   Counter
	EventsEmitted      Counter
	ProjectionSyncs    Counter
	SchedulerScans     Counter
	TimeoutsDetected   Counter
	OrphansDetected    Counter
	RecoveriesExecuted Counter

	// Gauges
	ActiveRuns  Gauge
	ActiveSteps Gauge

	// Histograms (duration tracking)
	RunDuration  *Histogram
	StepDuration *Histogram
}

// GlobalMetrics is the singleton metrics instance.
// Aggregates across all workspaces.
var GlobalMetrics = &Metrics{
	RunDuration:  NewHistogram(1, 5, 10, 30, 60, 300, 600, 1800, 3600),
	StepDuration: NewHistogram(0.1, 0.5, 1, 5, 10, 30, 60, 300, 600),
}

// workspaceMetrics holds per-workspace metrics instances.
var workspaceMetrics = struct {
	mu      sync.RWMutex
	entries map[string]*Metrics
}{
	entries: make(map[string]*Metrics),
}

// WorkspaceMetrics returns the metrics instance for a given workspace ID.
// Creates one on first access. These allow per-workspace filtering/grouping
// in monitoring tools alongside the aggregated GlobalMetrics.
func WorkspaceMetrics(workspaceID string) *Metrics {
	workspaceMetrics.mu.RLock()
	if m, ok := workspaceMetrics.entries[workspaceID]; ok {
		workspaceMetrics.mu.RUnlock()
		return m
	}
	workspaceMetrics.mu.RUnlock()

	workspaceMetrics.mu.Lock()
	defer workspaceMetrics.mu.Unlock()

	// Double-check after acquiring write lock.
	if m, ok := workspaceMetrics.entries[workspaceID]; ok {
		return m
	}

	m := &Metrics{
		RunDuration:  NewHistogram(1, 5, 10, 30, 60, 300, 600, 1800, 3600),
		StepDuration: NewHistogram(0.1, 0.5, 1, 5, 10, 30, 60, 300, 600),
	}
	workspaceMetrics.entries[workspaceID] = m
	return m
}

// ListWorkspaceMetrics returns all workspace IDs that have metrics.
func ListWorkspaceMetrics() []string {
	workspaceMetrics.mu.RLock()
	defer workspaceMetrics.mu.RUnlock()

	ids := make([]string, 0, len(workspaceMetrics.entries))
	for id := range workspaceMetrics.entries {
		ids = append(ids, id)
	}
	return ids
}
