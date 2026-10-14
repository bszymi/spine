package observe

import (
	"strings"
	"testing"
)

// TestExportPrometheusWorkspace pins the tenant-scoped export (INIT-023
// TASK-015, audit P3-29): only the named workspace's
// spine_workspace_pool_* series, gauges and counters, and no
// process-global series; ExportPrometheus still renders everything.
func TestExportPrometheusWorkspace(t *testing.T) {
	ResetPoolMetricsForTest()
	t.Cleanup(ResetPoolMetricsForTest)
	SetPoolGaugeProvider(func() []PoolGaugeSnapshot {
		return []PoolGaugeSnapshot{
			{WorkspaceID: "ws-b", TotalConns: 7, MaxConns: 9},
			{WorkspaceID: "ws-a", TotalConns: 3, AcquiredConns: 1, IdleConns: 2, MaxConns: 4},
		}
	})
	t.Cleanup(func() { SetPoolGaugeProvider(nil) })
	PoolMetrics("ws-a").Open.Inc()
	PoolMetrics("ws-a").RecordClose("idle")
	PoolMetrics("ws-b").Open.Inc()
	PoolMetrics("ws-b").Saturation.Inc()
	GlobalMetrics.RunsStarted.Inc()

	got := ExportPrometheusWorkspace("ws-a")
	for _, want := range []string{
		`spine_workspace_pool_size{workspace_id="ws-a"} 3`,
		`spine_workspace_pool_in_use{workspace_id="ws-a"} 1`,
		`spine_workspace_pool_idle{workspace_id="ws-a"} 2`,
		`spine_workspace_pool_max{workspace_id="ws-a"} 4`,
		`spine_workspace_pool_open_total{workspace_id="ws-a"} 1`,
		`spine_workspace_pool_close_total{workspace_id="ws-a",reason="idle"} 1`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("tenant export lacks %q:\n%s", want, got)
		}
	}
	for _, leak := range []string{"ws-b", "spine_runs_started_total", "spine_active_runs", "spine_run_duration_seconds"} {
		if strings.Contains(got, leak) {
			t.Errorf("tenant export leaks %q:\n%s", leak, got)
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(got), "\n") {
		if !strings.HasPrefix(line, "# TYPE spine_workspace_pool_") && !strings.HasPrefix(line, "spine_workspace_pool_") {
			t.Errorf("unexpected line in tenant export: %q", line)
		}
	}

	full := ExportPrometheus()
	for _, want := range []string{`workspace_id="ws-a"`, `workspace_id="ws-b"`, "spine_runs_started_total", "spine_workspace_pool_saturation_total"} {
		if !strings.Contains(full, want) {
			t.Errorf("full export lacks %q", want)
		}
	}

	if got := ExportPrometheusWorkspace("ws-unknown"); got != "" {
		t.Errorf("unknown workspace export = %q, want empty", got)
	}
	if got := ExportPrometheusWorkspace(""); got != "" {
		t.Errorf("empty workspace ID export = %q, want empty", got)
	}
}
