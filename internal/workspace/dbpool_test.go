package workspace_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/workspace"
)

func TestPoolPolicyDefault_MatchesADR012(t *testing.T) {
	d := workspace.PoolPolicyDefault()
	if d.MinConns != 2 {
		t.Errorf("MinConns = %d, want 2", d.MinConns)
	}
	if d.MaxConns != 10 {
		t.Errorf("MaxConns = %d, want 10", d.MaxConns)
	}
	if d.AcquireTimeout != 5*time.Second {
		t.Errorf("AcquireTimeout = %v, want 5s", d.AcquireTimeout)
	}
	if d.HealthCheckPeriod != 30*time.Second {
		t.Errorf("HealthCheckPeriod = %v, want 30s", d.HealthCheckPeriod)
	}
	if d.QueueSize != 50 {
		t.Errorf("QueueSize = %d, want 50", d.QueueSize)
	}
}

func TestBuildPgxpoolConfig_AppliesPolicy(t *testing.T) {
	cfg, err := workspace.BuildPgxpoolConfig(
		"postgres://user:pass@localhost:5432/db?sslmode=disable",
		workspace.PoolPolicy{
			MinConns:          1,
			MaxConns:          7,
			AcquireTimeout:    1 * time.Second, // not on pgxpool.Config; behaviour-only
			HealthCheckPeriod: 11 * time.Second,
			QueueSize:         13, // ditto, behaviour-only
		},
	)
	if err != nil {
		t.Fatalf("BuildPgxpoolConfig: %v", err)
	}
	if cfg.MinConns != 1 {
		t.Errorf("MinConns = %d, want 1", cfg.MinConns)
	}
	if cfg.MaxConns != 7 {
		t.Errorf("MaxConns = %d, want 7", cfg.MaxConns)
	}
	if cfg.HealthCheckPeriod != 11*time.Second {
		t.Errorf("HealthCheckPeriod = %v, want 11s", cfg.HealthCheckPeriod)
	}
}

func TestBuildPgxpoolConfig_PartialPolicyFallsBack(t *testing.T) {
	// Only MaxConns is set; the others must fall back to ADR-012 defaults.
	cfg, err := workspace.BuildPgxpoolConfig(
		"postgres://user:pass@localhost:5432/db?sslmode=disable",
		workspace.PoolPolicy{MaxConns: 4},
	)
	if err != nil {
		t.Fatalf("BuildPgxpoolConfig: %v", err)
	}
	if cfg.MinConns != 2 {
		t.Errorf("MinConns = %d, want 2 (default)", cfg.MinConns)
	}
	if cfg.MaxConns != 4 {
		t.Errorf("MaxConns = %d, want 4 (override)", cfg.MaxConns)
	}
	if cfg.HealthCheckPeriod != 30*time.Second {
		t.Errorf("HealthCheckPeriod = %v, want 30s (default)", cfg.HealthCheckPeriod)
	}
}

func TestBuildPgxpoolConfig_BadURL(t *testing.T) {
	_, err := workspace.BuildPgxpoolConfig("not a url", workspace.PoolPolicy{})
	if err == nil {
		t.Fatalf("expected error for malformed URL")
	}
}

func TestErrPoolSaturated_IsSentinel(t *testing.T) {
	wrapped := workspace.ErrPoolSaturated
	if !errors.Is(wrapped, workspace.ErrPoolSaturated) {
		t.Fatal("ErrPoolSaturated must be its own sentinel")
	}
}

func TestPoolMetrics_RecordCloseAndExport(t *testing.T) {
	observe.ResetPoolMetricsForTest()
	defer observe.ResetPoolMetricsForTest()

	m := observe.PoolMetrics("acme")
	m.Open.Inc()
	m.Open.Inc()
	m.Saturation.Inc()
	m.RecordClose("idle")
	m.RecordClose("idle")
	m.RecordClose("invalidate")

	out := observe.ExportPrometheus()

	mustContain := []string{
		`spine_workspace_pool_open_total{workspace_id="acme"} 2`,
		`spine_workspace_pool_saturation_total{workspace_id="acme"} 1`,
		`spine_workspace_pool_close_total{workspace_id="acme",reason="idle"} 2`,
		`spine_workspace_pool_close_total{workspace_id="acme",reason="invalidate"} 1`,
	}
	for _, s := range mustContain {
		if !strings.Contains(out, s) {
			t.Errorf("/metrics is missing %q\n--- got ---\n%s", s, out)
		}
	}
}

func TestPoolMetrics_NoSeriesWithoutEntries(t *testing.T) {
	// With no pools registered and no counters touched, the
	// per-workspace pool metric series must be absent — empty
	// counter families pollute scrapes.
	observe.ResetPoolMetricsForTest()
	observe.SetPoolGaugeProvider(nil)
	defer observe.SetPoolGaugeProvider(nil)

	out := observe.ExportPrometheus()
	for _, s := range []string{
		"spine_workspace_pool_size",
		"spine_workspace_pool_open_total",
		"spine_workspace_pool_close_total",
		"spine_workspace_pool_saturation_total",
	} {
		if strings.Contains(out, s) {
			t.Errorf("/metrics contains %q without any registered pool: should be silent\n--- got ---\n%s", s, out)
		}
	}
}

// TestWorkspaceDBPool_GateSaturates verifies the saturation gate
// itself: once MaxConns + QueueSize slots are taken, the next
// caller fails fast with ErrPoolSaturated and the saturation
// counter increments. The test pokes the unexported gate via the
// exported Exec / Query / Begin paths through a helper that uses
// reflection-free internals: we construct a pool with
// MaxConns=1, QueueSize=1, then prime the gate by directly
// pushing into the underlying inFlight channel through
// SaturateForTest so we don't need a real pgxpool.
func TestWorkspaceDBPool_GateSaturates(t *testing.T) {
	observe.ResetPoolMetricsForTest()
	defer observe.ResetPoolMetricsForTest()

	wp := workspace.NewWorkspaceDBPoolForTest("acme", 1, 1)
	defer wp.CloseForTest()

	// Fill MaxConns + QueueSize = 2 slots.
	rel1, err := wp.EnterGateForTest()
	if err != nil {
		t.Fatalf("first slot: %v", err)
	}
	rel2, err := wp.EnterGateForTest()
	if err != nil {
		t.Fatalf("second slot: %v", err)
	}

	// 3rd attempt must fail fast.
	if _, err := wp.EnterGateForTest(); !errors.Is(err, workspace.ErrPoolSaturated) {
		t.Fatalf("third slot: expected ErrPoolSaturated, got %v", err)
	}

	// Saturation counter incremented once.
	if got := observe.PoolMetrics("acme").Saturation.Value(); got != 1 {
		t.Fatalf("saturation counter = %d, want 1", got)
	}

	// Releasing a slot lets a new caller through.
	rel1()
	rel3, err := wp.EnterGateForTest()
	if err != nil {
		t.Fatalf("after release: expected success, got %v", err)
	}
	rel2()
	rel3()
}

// TestWorkspaceDBPool_CloseRecordsReason verifies the close-reason
// metric tags are written verbatim and that double-close is a no-op.
func TestWorkspaceDBPool_CloseRecordsReason(t *testing.T) {
	observe.ResetPoolMetricsForTest()
	defer observe.ResetPoolMetricsForTest()

	wp := workspace.NewWorkspaceDBPoolForTest("acme", 1, 1)
	wp.CloseForTest() // test helper that doesn't record a reason

	// Drive the public Close path that Evict / EvictIdle use: a
	// closer that takes the reason and forwards it to wp.Close.
	wp2 := workspace.NewWorkspaceDBPoolForTest("globex", 1, 1)
	closer := func(reason string) { wp2.Close(reason) }
	closer("invalidate")
	closer("invalidate") // double-close should be a no-op

	out := observe.ExportPrometheus()
	if !strings.Contains(out, `spine_workspace_pool_close_total{workspace_id="globex",reason="invalidate"} 1`) {
		t.Fatalf("close-reason metric missing or wrong:\n%s", out)
	}
}

func TestPoolSaturated_MapsToUnavailableDomainError(t *testing.T) {
	observe.ResetPoolMetricsForTest()
	defer observe.ResetPoolMetricsForTest()

	wp := workspace.NewWorkspaceDBPoolForTest("acme", 1, 0)
	defer wp.CloseForTest()

	rel, err := wp.EnterGateForTest()
	if err != nil {
		t.Fatalf("first slot: %v", err)
	}
	defer rel()

	_, err = wp.EnterGateForTest()
	if err == nil {
		t.Fatalf("expected saturation error, got nil")
	}
	// Sentinel still matches via Unwrap chain.
	if !errors.Is(err, workspace.ErrPoolSaturated) {
		t.Fatalf("errors.Is(err, ErrPoolSaturated) = false; err=%v", err)
	}
	// Gateway side: the error is a *domain.SpineError with code
	// service_unavailable so WriteError maps it to HTTP 503.
	var sErr *domain.SpineError
	if !errors.As(err, &sErr) {
		t.Fatalf("expected *domain.SpineError, got %T (%v)", err, err)
	}
	if sErr.Code != domain.ErrUnavailable {
		t.Fatalf("Code = %q, want %q", sErr.Code, domain.ErrUnavailable)
	}
}

func TestPoolGaugeProvider_LiveSnapshot(t *testing.T) {
	// Stand in a fake gauge provider so we can exercise the
	// gauge-export path without spinning up a real pgxpool.
	defer observe.SetPoolGaugeProvider(nil)
	observe.SetPoolGaugeProvider(func() []observe.PoolGaugeSnapshot {
		return []observe.PoolGaugeSnapshot{
			{WorkspaceID: "acme", TotalConns: 3, AcquiredConns: 1, IdleConns: 2, MaxConns: 10},
			{WorkspaceID: "globex", TotalConns: 0, AcquiredConns: 0, IdleConns: 0, MaxConns: 10},
		}
	})

	out := observe.ExportPrometheus()
	for _, want := range []string{
		`spine_workspace_pool_size{workspace_id="acme"} 3`,
		`spine_workspace_pool_in_use{workspace_id="acme"} 1`,
		`spine_workspace_pool_idle{workspace_id="acme"} 2`,
		`spine_workspace_pool_max{workspace_id="acme"} 10`,
		`spine_workspace_pool_size{workspace_id="globex"} 0`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
