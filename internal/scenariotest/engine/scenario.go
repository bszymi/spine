package engine

import (
	"context"
	"testing"

	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/scenariotest/harness"
)

// Scenario defines a named sequence of steps that operate on a test environment.
type Scenario struct {
	Name        string
	Description string
	Steps       []Step
}

// Step is a single action within a scenario.
type Step struct {
	Name   string
	Action func(ctx *ScenarioContext) error
}

// ScenarioContext carries the test environment and accumulated state between steps.
type ScenarioContext struct {
	T       *testing.T
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
// steps sequentially. Execution stops on the first step failure.
func RunScenario(t *testing.T, scenario Scenario) {
	t.Helper()

	repo := harness.NewTestRepo(t)
	db := harness.NewTestDB(t)
	rt := harness.NewTestRuntime(t, repo, db)

	ctx := context.Background()
	ctx = observe.WithTraceID(ctx, "scenario-"+scenario.Name)
	ctx = observe.WithActorID(ctx, "scenario-actor")

	t.Cleanup(func() {
		db.Cleanup(context.Background(), t)
	})

	sc := &ScenarioContext{
		T:       t,
		Repo:    repo,
		DB:      db,
		Runtime: rt,
		Ctx:     ctx,
		State:   make(map[string]any),
	}

	for _, step := range scenario.Steps {
		passed := t.Run(step.Name, func(st *testing.T) {
			sc.T = st
			if err := step.Action(sc); err != nil {
				st.Fatalf("step %q failed: %v", step.Name, err)
			}
		})
		if !passed {
			t.Fatalf("scenario aborted: step %q failed", step.Name)
			return
		}
	}
}
