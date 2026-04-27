package observe

import (
	"sort"
	"sync"
)

// PoolStats holds per-workspace connection-pool counters from
// ADR-012. Gauges (size / in-use / idle) are read live from
// pgxpool.Stat at export time and live in the workspace package's
// pool registry; only counters live here.
type PoolStats struct {
	// Open counts how many times a pool has been opened for this
	// workspace ID (cold start + re-build after invalidation).
	Open Counter

	// Saturation counts requests rejected because the bounded
	// queue was full at acquire time.
	Saturation Counter

	mu        sync.Mutex
	closeByCk map[string]*Counter
}

// RecordClose increments the close-by-reason counter. Reasons
// should be a small enumerated set (e.g. "idle", "invalidate",
// "shutdown"); unknown reasons are still recorded but proliferate
// metric series, so callers should prefer fixed labels.
func (p *PoolStats) RecordClose(reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closeByCk == nil {
		p.closeByCk = make(map[string]*Counter)
	}
	c, ok := p.closeByCk[reason]
	if !ok {
		c = &Counter{}
		p.closeByCk[reason] = c
	}
	c.Inc()
}

// closeReasons returns a sorted snapshot of (reason, count) pairs
// for export. Sorting keeps the Prometheus output deterministic.
func (p *PoolStats) closeReasons() []closeReasonCount {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]closeReasonCount, 0, len(p.closeByCk))
	for reason, c := range p.closeByCk {
		out = append(out, closeReasonCount{reason: reason, count: c.Value()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].reason < out[j].reason })
	return out
}

type closeReasonCount struct {
	reason string
	count  int64
}

// poolStatsRegistry holds per-workspace pool counters.
var poolStatsRegistry = struct {
	mu      sync.RWMutex
	entries map[string]*PoolStats
}{
	entries: make(map[string]*PoolStats),
}

// PoolMetrics returns the PoolStats for a workspace ID, creating
// it on first access.
func PoolMetrics(workspaceID string) *PoolStats {
	poolStatsRegistry.mu.RLock()
	if p, ok := poolStatsRegistry.entries[workspaceID]; ok {
		poolStatsRegistry.mu.RUnlock()
		return p
	}
	poolStatsRegistry.mu.RUnlock()

	poolStatsRegistry.mu.Lock()
	defer poolStatsRegistry.mu.Unlock()
	if p, ok := poolStatsRegistry.entries[workspaceID]; ok {
		return p
	}
	p := &PoolStats{}
	poolStatsRegistry.entries[workspaceID] = p
	return p
}

// listPoolStats returns a sorted snapshot of (workspaceID,
// PoolStats) for export.
func listPoolStats() []poolStatsEntry {
	poolStatsRegistry.mu.RLock()
	defer poolStatsRegistry.mu.RUnlock()
	out := make([]poolStatsEntry, 0, len(poolStatsRegistry.entries))
	for id, p := range poolStatsRegistry.entries {
		out = append(out, poolStatsEntry{id: id, stats: p})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

type poolStatsEntry struct {
	id    string
	stats *PoolStats
}

// PoolGaugeSnapshot is a point-in-time read of one workspace's
// connection-pool gauges. The workspace package owns the live
// pgxpool.Pool and registers a provider via SetPoolGaugeProvider so
// observe can render live numbers at export time without importing
// workspace.
type PoolGaugeSnapshot struct {
	WorkspaceID   string
	TotalConns    int32
	AcquiredConns int32
	IdleConns     int32
	MaxConns      int32
}

// PoolGaugeProvider returns live per-workspace pool gauge snapshots.
type PoolGaugeProvider func() []PoolGaugeSnapshot

var (
	poolGaugeProviderMu sync.RWMutex
	poolGaugeProvider   PoolGaugeProvider
)

// SetPoolGaugeProvider registers the function ExportPrometheus calls
// to retrieve live pool gauges. Passing nil clears the provider.
func SetPoolGaugeProvider(p PoolGaugeProvider) {
	poolGaugeProviderMu.Lock()
	defer poolGaugeProviderMu.Unlock()
	poolGaugeProvider = p
}

func currentPoolGauges() []PoolGaugeSnapshot {
	poolGaugeProviderMu.RLock()
	p := poolGaugeProvider
	poolGaugeProviderMu.RUnlock()
	if p == nil {
		return nil
	}
	return p()
}

// ResetPoolMetricsForTest empties the pool stats registry. Tests
// that exercise the export path use this to keep state from
// leaking between cases. Not exported under that suffix because
// nothing in production needs to reset; the leading underscore
// is omitted to keep call sites readable.
func ResetPoolMetricsForTest() {
	poolStatsRegistry.mu.Lock()
	defer poolStatsRegistry.mu.Unlock()
	poolStatsRegistry.entries = make(map[string]*PoolStats)
}
