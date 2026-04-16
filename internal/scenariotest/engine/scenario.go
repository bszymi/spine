package engine

import (
	"context"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/scenariotest/harness"
)

// Scenario defines a named sequence of steps that operate on a test environment.
type Scenario struct {
	Name        string
	Description string
	EnvOpts     []harness.EnvOption // environment configuration; defaults to empty (bare repo)
	Steps       []Step
}

// Step is a single action within a scenario.
type Step struct {
	Name   string
	Action func(ctx *ScenarioContext) error
}

// StepStatus represents the outcome of a single step execution.
type StepStatus string

const (
	StepPassed  StepStatus = "passed"
	StepFailed  StepStatus = "failed"
	StepSkipped StepStatus = "skipped"
)

// StepResult records the outcome of a single step execution.
type StepResult struct {
	Name     string
	Status   StepStatus
	Duration time.Duration
	Error    string // non-empty when Status is StepFailed
}

// ScenarioResult records the outcome of a full scenario execution.
type ScenarioResult struct {
	Name     string
	Steps    []StepResult
	Duration time.Duration
}

// Passed returns true if no steps failed. Skipped steps are not considered failures.
func (r *ScenarioResult) Passed() bool {
	for _, s := range r.Steps {
		if s.Status == StepFailed {
			return false
		}
	}
	return len(r.Steps) > 0
}

// ScenarioContext carries the test environment and accumulated state between steps.
type ScenarioContext struct {
	// T is the current step's subtest. It is rebound on every step, so
	// cleanup registered via T.Cleanup fires at the end of that step.
	T *testing.T
	// ParentT is the scenario-level test. Cleanup registered here persists
	// for the entire scenario — use it for resources (servers, listeners)
	// that must outlive the setup step.
	ParentT *testing.T
	Repo    *harness.TestRepo
	DB      *harness.TestDB
	Runtime *harness.TestRuntime
	Ctx     context.Context
	State   map[string]any
}

// Get returns a value from step-to-step state, or nil if not set.
func (sc *ScenarioContext) Get(key string) any {
	return sc.State[key]
}

// Set stores a value in step-to-step state.
func (sc *ScenarioContext) Set(key string, value any) {
	sc.State[key] = value
}

// MustGet returns a value from state, failing the test if the key is missing.
func (sc *ScenarioContext) MustGet(key string) any {
	sc.T.Helper()
	v, ok := sc.State[key]
	if !ok {
		sc.T.Fatalf("scenario state: key %q not set", key)
	}
	return v
}

// RunScenario creates an isolated test environment and executes the scenario's
// steps sequentially. Execution stops on the first step failure (fail-fast).
// Environment configuration is taken from scenario.EnvOpts.
// Returns a ScenarioResult with per-step outcomes.
func RunScenario(t *testing.T, scenario Scenario) *ScenarioResult {
	t.Helper()

	scenarioStart := time.Now()
	env := harness.NewTestEnvironment(t, scenario.EnvOpts...)

	ctx := context.Background()
	ctx = observe.WithTraceID(ctx, "scenario-"+scenario.Name)
	ctx = observe.WithActorID(ctx, "scenario-actor")

	sc := &ScenarioContext{
		T:       t,
		ParentT: t,
		Repo:    env.Repo,
		DB:      env.DB,
		Runtime: env.Runtime,
		Ctx:     ctx,
		State:   make(map[string]any),
	}

	result := &ScenarioResult{
		Name: scenario.Name,
	}

	for i, step := range scenario.Steps {
		stepStart := time.Now()
		sr := StepResult{Name: step.Name}

		var skipped bool
		passed := t.Run(step.Name, func(st *testing.T) {
			sc.T = st
			if err := step.Action(sc); err != nil {
				st.Fatalf("step %q failed: %v", step.Name, err)
			}
			skipped = st.Skipped()
		})

		sr.Duration = time.Since(stepStart)
		switch {
		case !passed:
			sr.Status = StepFailed
			sr.Error = "step failed (see subtest output)"
		case skipped:
			sr.Status = StepSkipped
		default:
			sr.Status = StepPassed
		}
		result.Steps = append(result.Steps, sr)

		if !passed {
			// Mark remaining steps as skipped.
			for _, remaining := range scenario.Steps[i+1:] {
				result.Steps = append(result.Steps, StepResult{
					Name:   remaining.Name,
					Status: StepSkipped,
				})
			}
			result.Duration = time.Since(scenarioStart)
			t.Errorf("scenario aborted: step %q failed", step.Name)
			return result
		}
	}

	result.Duration = time.Since(scenarioStart)
	return result
}
