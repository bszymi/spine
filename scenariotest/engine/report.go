package engine

import (
	"fmt"
	"strings"
	"testing"
)

// Summary returns a one-line summary of the scenario result.
func (r *ScenarioResult) Summary() string {
	counts := r.Counts()
	status := "PASS"
	if !r.Passed() {
		status = "FAIL"
	}
	return fmt.Sprintf("[%s] %s: %d/%d steps passed (%s)",
		status, r.Name, counts.Passed, counts.Total, r.Duration.Round(1e6))
}

// StepCounts holds aggregate counts for step outcomes.
type StepCounts struct {
	Total   int
	Passed  int
	Failed  int
	Skipped int
}

// Counts returns aggregate step outcome counts.
func (r *ScenarioResult) Counts() StepCounts {
	c := StepCounts{Total: len(r.Steps)}
	for _, s := range r.Steps {
		switch s.Status {
		case StepPassed:
			c.Passed++
		case StepFailed:
			c.Failed++
		case StepSkipped:
			c.Skipped++
		}
	}
	return c
}

// FailedSteps returns only the steps that failed.
func (r *ScenarioResult) FailedSteps() []StepResult {
	var failed []StepResult
	for _, s := range r.Steps {
		if s.Status == StepFailed {
			failed = append(failed, s)
		}
	}
	return failed
}

// Report returns a multi-line human-readable report.
func (r *ScenarioResult) Report() string {
	var b strings.Builder
	b.WriteString(r.Summary())
	b.WriteByte('\n')

	for _, s := range r.Steps {
		icon := "  ✓"
		switch s.Status {
		case StepFailed:
			icon = "  ✗"
		case StepSkipped:
			icon = "  ○"
		}
		fmt.Fprintf(&b, "%s %s (%s)", icon, s.Name, s.Duration.Round(1e6))
		if s.Error != "" {
			fmt.Fprintf(&b, " — %s", s.Error)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// LogResult logs the scenario result to the test output using t.Log.
// This integrates with `go test -v` for human-readable output.
func LogResult(t *testing.T, result *ScenarioResult) {
	t.Helper()
	if result == nil {
		return
	}
	t.Log("\n" + result.Report())
}
