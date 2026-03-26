//go:build scenario

package engine_test

import (
	"testing"

	"github.com/bszymi/spine/internal/scenariotest/engine"
	"github.com/bszymi/spine/internal/scenariotest/harness"
)

func TestRunScenario_ReturnsResult(t *testing.T) {
	result := engine.RunScenario(t, engine.Scenario{
		Name:    "result-test",
		EnvOpts: harness.Seeded(),
		Steps: []engine.Step{
			{
				Name:   "step-one",
				Action: func(sc *engine.ScenarioContext) error { return nil },
			},
			{
				Name:   "step-two",
				Action: func(sc *engine.ScenarioContext) error { return nil },
			},
		},
	})

	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Name != "result-test" {
		t.Errorf("expected name 'result-test', got %q", result.Name)
	}
	if len(result.Steps) != 2 {
		t.Fatalf("expected 2 step results, got %d", len(result.Steps))
	}
	if result.Steps[0].Status != engine.StepPassed {
		t.Errorf("expected step-one passed, got %s", result.Steps[0].Status)
	}
	if result.Steps[1].Status != engine.StepPassed {
		t.Errorf("expected step-two passed, got %s", result.Steps[1].Status)
	}
	if !result.Passed() {
		t.Error("expected scenario to pass")
	}
	if result.Duration <= 0 {
		t.Error("expected positive duration")
	}
	if result.Steps[0].Duration <= 0 {
		t.Error("expected positive step duration")
	}
}

func TestRunScenario_ContextPropagation(t *testing.T) {
	result := engine.RunScenario(t, engine.Scenario{
		Name: "context-propagation",
		Steps: []engine.Step{
			{
				Name: "set-value",
				Action: func(sc *engine.ScenarioContext) error {
					sc.Set("key", "value-from-step-1")
					return nil
				},
			},
			{
				Name: "read-value",
				Action: func(sc *engine.ScenarioContext) error {
					v := sc.MustGet("key").(string)
					if v != "value-from-step-1" {
						sc.T.Errorf("expected 'value-from-step-1', got %q", v)
					}
					return nil
				},
			},
		},
	})

	if !result.Passed() {
		t.Error("expected context propagation scenario to pass")
	}
}

func TestScenarioResult_Passed(t *testing.T) {
	allPassed := &engine.ScenarioResult{
		Steps: []engine.StepResult{
			{Name: "a", Status: engine.StepPassed},
			{Name: "b", Status: engine.StepPassed},
		},
	}
	if !allPassed.Passed() {
		t.Error("expected Passed() = true when all steps passed")
	}

	withFailure := &engine.ScenarioResult{
		Steps: []engine.StepResult{
			{Name: "a", Status: engine.StepPassed},
			{Name: "b", Status: engine.StepFailed, Error: "fail"},
		},
	}
	if withFailure.Passed() {
		t.Error("expected Passed() = false when a step failed")
	}

	empty := &engine.ScenarioResult{}
	if empty.Passed() {
		t.Error("expected Passed() = false for empty result")
	}
}
