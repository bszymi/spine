package engine_test

import (
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/scenariotest/engine"
)

func TestScenarioResult_Summary(t *testing.T) {
	result := &engine.ScenarioResult{
		Name:     "test-scenario",
		Duration: 150 * time.Millisecond,
		Steps: []engine.StepResult{
			{Name: "step-1", Status: engine.StepPassed, Duration: 50 * time.Millisecond},
			{Name: "step-2", Status: engine.StepPassed, Duration: 100 * time.Millisecond},
		},
	}

	summary := result.Summary()
	if !strings.Contains(summary, "[PASS]") {
		t.Errorf("expected PASS in summary, got %q", summary)
	}
	if !strings.Contains(summary, "2/2 steps passed") {
		t.Errorf("expected '2/2 steps passed' in summary, got %q", summary)
	}
}

func TestScenarioResult_Summary_Failed(t *testing.T) {
	result := &engine.ScenarioResult{
		Name:     "failing-scenario",
		Duration: 200 * time.Millisecond,
		Steps: []engine.StepResult{
			{Name: "step-1", Status: engine.StepPassed, Duration: 50 * time.Millisecond},
			{Name: "step-2", Status: engine.StepFailed, Duration: 100 * time.Millisecond, Error: "assertion failed"},
			{Name: "step-3", Status: engine.StepSkipped},
		},
	}

	summary := result.Summary()
	if !strings.Contains(summary, "[FAIL]") {
		t.Errorf("expected FAIL in summary, got %q", summary)
	}
	if !strings.Contains(summary, "1/3 steps passed") {
		t.Errorf("expected '1/3 steps passed' in summary, got %q", summary)
	}
}

func TestScenarioResult_Counts(t *testing.T) {
	result := &engine.ScenarioResult{
		Steps: []engine.StepResult{
			{Status: engine.StepPassed},
			{Status: engine.StepPassed},
			{Status: engine.StepFailed},
			{Status: engine.StepSkipped},
		},
	}

	counts := result.Counts()
	if counts.Total != 4 {
		t.Errorf("total: got %d, want 4", counts.Total)
	}
	if counts.Passed != 2 {
		t.Errorf("passed: got %d, want 2", counts.Passed)
	}
	if counts.Failed != 1 {
		t.Errorf("failed: got %d, want 1", counts.Failed)
	}
	if counts.Skipped != 1 {
		t.Errorf("skipped: got %d, want 1", counts.Skipped)
	}
}

func TestScenarioResult_FailedSteps(t *testing.T) {
	result := &engine.ScenarioResult{
		Steps: []engine.StepResult{
			{Name: "a", Status: engine.StepPassed},
			{Name: "b", Status: engine.StepFailed, Error: "boom"},
			{Name: "c", Status: engine.StepSkipped},
		},
	}

	failed := result.FailedSteps()
	if len(failed) != 1 {
		t.Fatalf("expected 1 failed step, got %d", len(failed))
	}
	if failed[0].Name != "b" {
		t.Errorf("expected failed step 'b', got %q", failed[0].Name)
	}
}

func TestScenarioResult_Report(t *testing.T) {
	result := &engine.ScenarioResult{
		Name:     "report-test",
		Duration: 300 * time.Millisecond,
		Steps: []engine.StepResult{
			{Name: "create", Status: engine.StepPassed, Duration: 100 * time.Millisecond},
			{Name: "verify", Status: engine.StepFailed, Duration: 150 * time.Millisecond, Error: "not found"},
			{Name: "cleanup", Status: engine.StepSkipped},
		},
	}

	report := result.Report()
	if !strings.Contains(report, "create") {
		t.Error("report should contain step name 'create'")
	}
	if !strings.Contains(report, "not found") {
		t.Error("report should contain error 'not found'")
	}
}
