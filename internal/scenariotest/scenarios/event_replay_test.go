//go:build scenario

package scenarios_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/domain"
	scenarioEngine "github.com/bszymi/spine/internal/scenariotest/engine"
	"github.com/bszymi/spine/internal/scenariotest/harness"
)

// Simple single-step workflow used for event replay and validation tests.
// No required_outputs or commit metadata so IngestResult completes in one call.
const eventReplayWorkflowYAML = `id: task-event-replay
name: Event Replay Test Workflow
version: "1.0"
status: Active
description: Single-step manual workflow for event replay scenario tests.
applies_to:
  - Task
entry_step: execute
steps:
  - id: execute
    name: Execute Task
    type: manual
    outcomes:
      - id: completed
        name: Done
        next_step: end
    timeout: "4h"
`

// eventCollector captures domain events emitted by the orchestrator.
// The MemoryQueue dispatches events in a background goroutine, so all field
// access is mutex-protected.
type eventCollector struct {
	mu     sync.Mutex
	events []domain.Event
}

func (c *eventCollector) handle(_ context.Context, ev domain.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, ev)
	return nil
}

// forRun returns all captured events for the given run ID in capture order.
func (c *eventCollector) forRun(runID string) []domain.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	var result []domain.Event
	for _, e := range c.events {
		if e.RunID == runID {
			result = append(result, e)
		}
	}
	return result
}

// hasEventType reports whether any captured event for the run has the given type.
func (c *eventCollector) hasEventForRun(runID string, et domain.EventType) bool {
	for _, e := range c.forRun(runID) {
		if e.Type == et {
			return true
		}
	}
	return false
}

// eventTypeNames returns the event type strings for a slice of events.
// Used in test error messages.
func eventTypeNames(events []domain.Event) []string {
	names := make([]string, len(events))
	for i, e := range events {
		names[i] = string(e.Type)
	}
	return names
}

// setupEventCollection returns a step that subscribes to all relevant domain
// event types and stores the collector in scenario state under "event_collector".
// Must be placed before any workflow operations that emit events.
func setupEventCollection() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "setup-event-collection",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			if sc.Runtime.Events == nil {
				return fmt.Errorf("event collection requires WithRuntimeOrchestrator()")
			}
			collector := &eventCollector{}
			for _, et := range []domain.EventType{
				domain.EventRunStarted,
				domain.EventRunCompleted,
				domain.EventRunCancelled,
				domain.EventRunFailed,
				domain.EventStepAssigned,
				domain.EventStepStarted,
				domain.EventStepCompleted,
				domain.EventStepFailed,
			} {
				if err := sc.Runtime.Events.Subscribe(sc.Ctx, et, collector.handle); err != nil {
					return fmt.Errorf("subscribe to %s: %w", et, err)
				}
			}
			sc.Set("event_collector", collector)
			return nil
		},
	}
}

// drainEventQueue returns a step that pauses briefly so that the MemoryQueue
// background goroutine can dispatch all pending events before assertions run.
// 150 ms covers the 50 ms re-queue delay for entries that arrived before
// handlers were registered, plus queue dispatch latency.
func drainEventQueue() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "drain-event-queue",
		Action: func(_ *scenarioEngine.ScenarioContext) error {
			time.Sleep(150 * time.Millisecond)
			return nil
		},
	}
}

// TestEventReplay_GoldenPathSequence verifies that the event log for a
// successfully completed run contains exactly the expected event types in
// the expected order.
//
// Scenario: Event log completeness for a completed run
//
//	Given a single-step workflow
//	When a run is started and completed
//	Then the events for the run are (in order):
//	  run_started → step_assigned → step_started → step_completed → run_completed
func TestEventReplay_GoldenPathSequence(t *testing.T) {
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "event-replay-golden-path-sequence",
		Description: "Event log completeness: completed run emits expected event types in order",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []scenarioEngine.Step{
			// Register event handlers before any operations so no events are missed.
			setupEventCollection(),

			seedWorkflow("task-event-replay", eventReplayWorkflowYAML),
			scenarioEngine.SeedHierarchy("INIT-901", "EPIC-901", "TASK-901"),
			scenarioEngine.SyncProjections(),

			scenarioEngine.StartRun("initiatives/init-901/epics/epic-901/tasks/task-901.md"),
			scenarioEngine.AssertRunStatus(domain.RunStatusActive),

			// IngestResult auto-acknowledges (step_started) and completes the step,
			// then CompleteRun emits run_completed.
			scenarioEngine.SubmitStepResult("completed"),
			scenarioEngine.AssertRunCompleted(),

			// Wait for the background queue goroutine to deliver all events.
			drainEventQueue(),

			{
				Name: "assert-event-sequence",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					runID := sc.MustGet("run_id").(string)
					collector := sc.MustGet("event_collector").(*eventCollector)
					events := collector.forRun(runID)

					expected := []domain.EventType{
						domain.EventRunStarted,
						domain.EventStepAssigned,
						domain.EventStepStarted,
						domain.EventStepCompleted,
						domain.EventRunCompleted,
					}

					if len(events) != len(expected) {
						sc.T.Errorf("event count mismatch: expected %d events %v, got %d events %v",
							len(expected), expected, len(events), eventTypeNames(events))
						return nil
					}
					for i, ev := range events {
						if ev.Type != expected[i] {
							sc.T.Errorf("event[%d]: expected %s, got %s", i, expected[i], ev.Type)
						}
					}
					return nil
				},
			},
		},
	})
}

// TestEventReplay_StateConsistency verifies that the state observable from the
// event log agrees with the state persisted in the store. After a completed run:
//   - A run_completed event is present with the correct RunID
//   - The run's stored status is "completed"
//   - The step's stored status is "completed"
//
// Scenario: State from events matches state from store
//
//	Given a run that has completed
//	Then a run_completed event exists carrying the run's RunID
//	And the run status in the store is "completed"
//	And the step status in the store is "completed"
func TestEventReplay_StateConsistency(t *testing.T) {
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "event-replay-state-consistency",
		Description: "State derived from events agrees with state stored after run completion",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []scenarioEngine.Step{
			setupEventCollection(),

			seedWorkflow("task-event-replay", eventReplayWorkflowYAML),
			scenarioEngine.SeedHierarchy("INIT-902", "EPIC-902", "TASK-902"),
			scenarioEngine.SyncProjections(),

			scenarioEngine.StartRun("initiatives/init-902/epics/epic-902/tasks/task-902.md"),
			scenarioEngine.AssertRunStatus(domain.RunStatusActive),
			scenarioEngine.SubmitStepResult("completed"),
			scenarioEngine.AssertRunCompleted(),

			drainEventQueue(),

			{
				Name: "assert-run-completed-event-matches-store",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					runID := sc.MustGet("run_id").(string)
					collector := sc.MustGet("event_collector").(*eventCollector)

					// Verify a run_completed event was emitted for this run.
					if !collector.hasEventForRun(runID, domain.EventRunCompleted) {
						sc.T.Errorf("no run_completed event found for run %s", runID)
					}

					// Verify the run status in the store agrees.
					run, err := sc.Runtime.Store.GetRun(sc.Ctx, runID)
					if err != nil {
						return fmt.Errorf("get run from store: %w", err)
					}
					if run.Status != domain.RunStatusCompleted {
						sc.T.Errorf("store run status: expected completed, got %s", run.Status)
					}

					// Verify the step status in the store agrees.
					execID := sc.MustGet("current_execution_id").(string)
					exec, err := sc.Runtime.Store.GetStepExecution(sc.Ctx, execID)
					if err != nil {
						return fmt.Errorf("get step execution from store: %w", err)
					}
					if exec.Status != domain.StepStatusCompleted {
						sc.T.Errorf("store step status: expected completed, got %s", exec.Status)
					}

					// Verify the RunID embedded in the run_completed event matches the actual run.
					for _, ev := range collector.forRun(runID) {
						if ev.Type == domain.EventRunCompleted && ev.RunID != runID {
							sc.T.Errorf("run_completed event RunID mismatch: event has %q, expected %q",
								ev.RunID, runID)
						}
					}

					return nil
				},
			},
		},
	})
}

// TestEventReplay_NoPhantomsAfterCancel verifies that cancelling a run does not
// produce step-completion or run-completion events. Only run_cancelled is emitted.
//
// Scenario: No phantom events after run cancellation
//
//	Given a run that has been started (step is in assigned state)
//	When the run is cancelled
//	Then a run_cancelled event is present for the run
//	And no step_completed event exists for the run
//	And no run_completed event exists for the run
func TestEventReplay_NoPhantomsAfterCancel(t *testing.T) {
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "event-replay-no-phantoms-after-cancel",
		Description: "Cancelled run emits run_cancelled but no step_completed or run_completed",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []scenarioEngine.Step{
			setupEventCollection(),

			seedWorkflow("task-event-replay", eventReplayWorkflowYAML),
			scenarioEngine.SeedHierarchy("INIT-903", "EPIC-903", "TASK-903"),
			scenarioEngine.SyncProjections(),

			scenarioEngine.StartRun("initiatives/init-903/epics/epic-903/tasks/task-903.md"),
			scenarioEngine.AssertRunStatus(domain.RunStatusActive),

			// Cancel before any step result is submitted.
			scenarioEngine.CancelRun(),
			scenarioEngine.AssertRunStatus(domain.RunStatusCancelled),

			drainEventQueue(),

			{
				Name: "assert-cancelled-no-phantom-events",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					runID := sc.MustGet("run_id").(string)
					collector := sc.MustGet("event_collector").(*eventCollector)
					events := collector.forRun(runID)

					// Must have run_cancelled.
					if !collector.hasEventForRun(runID, domain.EventRunCancelled) {
						sc.T.Errorf("expected run_cancelled event for run %s, got: %v",
							runID, eventTypeNames(events))
					}

					// Must NOT have step_completed (phantom step event after cancel).
					if collector.hasEventForRun(runID, domain.EventStepCompleted) {
						sc.T.Errorf("unexpected step_completed event after cancellation for run %s", runID)
					}

					// Must NOT have run_completed (phantom run event after cancel).
					if collector.hasEventForRun(runID, domain.EventRunCompleted) {
						sc.T.Errorf("unexpected run_completed event after cancellation for run %s", runID)
					}

					return nil
				},
			},
		},
	})
}

// TestEventReplay_MonotonicOrdering verifies that events for a single run are
// emitted in monotonically non-decreasing timestamp order. Out-of-order events
// would indicate a race condition or clock skew in event emission.
//
// Scenario: Event ordering invariant within a single run
//
//	Given a completed run
//	When all events for the run are collected
//	Then each event's timestamp is >= the previous event's timestamp
func TestEventReplay_MonotonicOrdering(t *testing.T) {
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "event-replay-monotonic-ordering",
		Description: "Events within a single run have monotonically non-decreasing timestamps",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []scenarioEngine.Step{
			setupEventCollection(),

			seedWorkflow("task-event-replay", eventReplayWorkflowYAML),
			scenarioEngine.SeedHierarchy("INIT-904", "EPIC-904", "TASK-904"),
			scenarioEngine.SyncProjections(),

			scenarioEngine.StartRun("initiatives/init-904/epics/epic-904/tasks/task-904.md"),
			scenarioEngine.AssertRunStatus(domain.RunStatusActive),
			scenarioEngine.SubmitStepResult("completed"),
			scenarioEngine.AssertRunCompleted(),

			drainEventQueue(),

			{
				Name: "assert-monotonic-timestamps",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					runID := sc.MustGet("run_id").(string)
					collector := sc.MustGet("event_collector").(*eventCollector)
					events := collector.forRun(runID)

					if len(events) < 2 {
						sc.T.Errorf("expected at least 2 events for run %s, got %d", runID, len(events))
						return nil
					}

					for i := 1; i < len(events); i++ {
						prev := events[i-1]
						curr := events[i]
						if curr.Timestamp.Before(prev.Timestamp) {
							sc.T.Errorf(
								"event ordering violation: event[%d] (%s at %s) is before event[%d] (%s at %s)",
								i, curr.Type, curr.Timestamp.Format(time.RFC3339Nano),
								i-1, prev.Type, prev.Timestamp.Format(time.RFC3339Nano),
							)
						}
					}

					return nil
				},
			},
		},
	})
}
