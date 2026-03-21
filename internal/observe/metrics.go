package observe

import "sync/atomic"

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

// Metrics holds all Spine runtime metrics counters.
// These are in-process only (not exported to external systems in v0.x).
type Metrics struct {
	RunsStarted      Counter
	RunsCompleted    Counter
	RunsFailed       Counter
	StepsCompleted   Counter
	StepsFailed      Counter
	StepsRetried     Counter
	ArtifactsCreated Counter
	ArtifactsUpdated Counter
	GitCommits       Counter
	GitCommitRetries Counter
	EventsEmitted    Counter
	ProjectionSyncs  Counter
}

// GlobalMetrics is the singleton metrics instance.
var GlobalMetrics = &Metrics{}
