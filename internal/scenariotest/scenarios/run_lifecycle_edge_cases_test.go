//go:build scenario

package scenarios_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/bszymi/spine/internal/domain"
	spineEngine "github.com/bszymi/spine/internal/engine"
	scenarioEngine "github.com/bszymi/spine/internal/scenariotest/engine"
	"github.com/bszymi/spine/internal/scenariotest/harness"
)

// Single-step manual workflow for edge-case scenarios.
// No required_outputs so results can be submitted without artifacts.
const edgeCaseWorkflowYAML = `id: task-edge-cases
name: Edge Case Test Workflow
version: "1.0"
status: Active
description: Minimal single-step workflow for run lifecycle edge-case scenarios.
applies_to:
  - Task
entry_step: execute
steps:
  - id: execute
    name: Execute Task
    type: manual
    execution:
      mode: hybrid
      eligible_actor_types:
        - human
    outcomes:
      - id: completed
        name: Done
        next_step: end
    timeout: "4h"
`

func seedEdgeCaseWorkflow() scenarioEngine.Step {
	return seedWorkflow("task-edge-cases", edgeCaseWorkflowYAML)
}

// TestRunLifecycle_SubmitToCancelledRun verifies that submitting a step result
// after the run has been cancelled returns an error.
//
// After CancelRun, the run status is terminal (cancelled). The step execution
// remains in assigned status. When IngestResult is called, the step transitions
// through in_progress → completed internally, but the subsequent attempt to
// advance the run (CompleteRun) fails because the state machine rejects
// transitions out of a terminal state.
//
// Scenario: Submit result for a step whose run is already cancelled
//   Given an active run on the "execute" step
//   When the run is cancelled
//   Then the run status is "cancelled"
//   When a step result is submitted for the execute step
//   Then an error is returned (cannot advance a cancelled run)
func TestRunLifecycle_SubmitToCancelledRun(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "submit-to-cancelled-run",
		Description: "Submitting a step result after run cancellation returns an error",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []scenarioEngine.Step{
			seedEdgeCaseWorkflow(),
			scenarioEngine.SeedHierarchy("INIT-EC1", "EPIC-EC1", "TASK-EC1"),
			scenarioEngine.SyncProjections(),

			// Start the run and capture the execution ID.
			scenarioEngine.StartRun("initiatives/init-ec1/epics/epic-ec1/tasks/task-ec1.md"),
			scenarioEngine.AssertRunStatus(domain.RunStatusActive),

			// Cancel the run — step execution remains in assigned status.
			scenarioEngine.CancelRun(),
			scenarioEngine.AssertRunStatus(domain.RunStatusCancelled),

			// Attempt to submit a result — should return an error because the
			// state machine rejects advancing a cancelled (terminal) run.
			{
				Name: "submit-to-cancelled-returns-error",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					execID := sc.MustGet("current_execution_id").(string)
					_, err := sc.Runtime.Orchestrator.IngestResult(sc.Ctx, spineEngine.SubmitRequest{
						ExecutionID: execID,
						OutcomeID:   "completed",
					})
					if err == nil {
						return fmt.Errorf("expected error when submitting to cancelled run, got nil")
					}
					// The error originates from the run state machine rejecting
					// a transition out of the terminal "cancelled" state.
					var spineErr *domain.SpineError
					if !errors.As(err, &spineErr) {
						return fmt.Errorf("expected domain.SpineError, got: %v", err)
					}
					if spineErr.Code != domain.ErrConflict {
						return fmt.Errorf("expected ErrConflict, got code=%s: %v", spineErr.Code, err)
					}
					return nil
				},
			},
		},
	})
}

// TestRunLifecycle_DoubleStart verifies that starting a second run for a task
// that already has an active run succeeds — the engine has no exclusivity lock
// at the StartRun level. Both runs operate independently with unique IDs.
//
// Scenario: Two StartRun calls for the same task path
//   Given a seeded task with a governing workflow
//   When StartRun is called twice for the same task path
//   Then both calls succeed, each returning a distinct run_id
//   And both runs are independently active
func TestRunLifecycle_DoubleStart(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "double-start-same-task",
		Description: "Two StartRun calls for the same task both succeed with independent run IDs",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []scenarioEngine.Step{
			seedEdgeCaseWorkflow(),
			scenarioEngine.SeedHierarchy("INIT-EC2", "EPIC-EC2", "TASK-EC2"),
			scenarioEngine.SyncProjections(),

			// First StartRun — stores run_id as "first_run_id".
			{
				Name: "start-first-run",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					taskPath := "initiatives/init-ec2/epics/epic-ec2/tasks/task-ec2.md"
					result, err := sc.Runtime.Orchestrator.StartRun(sc.Ctx, taskPath)
					if err != nil {
						return fmt.Errorf("first StartRun: %w", err)
					}
					sc.Set("first_run_id", result.Run.RunID)
					return nil
				},
			},

			// Second StartRun — stores run_id as "second_run_id".
			{
				Name: "start-second-run",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					taskPath := "initiatives/init-ec2/epics/epic-ec2/tasks/task-ec2.md"
					result, err := sc.Runtime.Orchestrator.StartRun(sc.Ctx, taskPath)
					if err != nil {
						return fmt.Errorf("second StartRun: %w", err)
					}
					sc.Set("second_run_id", result.Run.RunID)
					return nil
				},
			},

			// Verify: both run IDs are non-empty and distinct.
			{
				Name: "assert-both-runs-are-distinct",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					firstID := sc.MustGet("first_run_id").(string)
					secondID := sc.MustGet("second_run_id").(string)
					if firstID == "" {
						return fmt.Errorf("first run_id is empty")
					}
					if secondID == "" {
						return fmt.Errorf("second run_id is empty")
					}
					if firstID == secondID {
						return fmt.Errorf("expected distinct run IDs, both are %q", firstID)
					}
					return nil
				},
			},

			// Verify: both runs are independently active in the store.
			{
				Name: "assert-both-runs-active",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					for _, key := range []string{"first_run_id", "second_run_id"} {
						runID := sc.MustGet(key).(string)
						run, err := sc.Runtime.Store.GetRun(sc.Ctx, runID)
						if err != nil {
							return fmt.Errorf("get %s: %w", key, err)
						}
						if run.Status != domain.RunStatusActive {
							return fmt.Errorf("%s: expected active, got %s", key, run.Status)
						}
					}
					return nil
				},
			},
		},
	})
}

// TestRunLifecycle_IdempotentIngestResult verifies that resubmitting a step result
// via IngestResult after the step has already completed is a safe no-op.
//
// IngestResult checks whether the step execution is already terminal. If it is,
// it returns the stored state immediately without reprocessing. The run is not
// double-advanced and no error is returned.
//
// Scenario: Duplicate result submission via IngestResult
//   Given a completed single-step run
//   When IngestResult is called again with the same execution_id and outcome
//   Then no error is returned
//   And the run remains completed (not advanced again)
func TestRunLifecycle_IdempotentIngestResult(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "idempotent-ingest-result",
		Description: "Resubmitting a result for a completed step via IngestResult is a safe no-op",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []scenarioEngine.Step{
			seedEdgeCaseWorkflow(),
			scenarioEngine.SeedHierarchy("INIT-EC3", "EPIC-EC3", "TASK-EC3"),
			scenarioEngine.SyncProjections(),

			// Start the run and capture the execution ID before it advances.
			{
				Name: "start-run-and-capture-execution-id",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					taskPath := "initiatives/init-ec3/epics/epic-ec3/tasks/task-ec3.md"
					result, err := sc.Runtime.Orchestrator.StartRun(sc.Ctx, taskPath)
					if err != nil {
						return fmt.Errorf("start run: %w", err)
					}
					sc.Set("run_id", result.Run.RunID)
					if result.EntryStep == nil {
						return fmt.Errorf("expected non-nil entry step")
					}
					sc.Set("entry_execution_id", result.EntryStep.ExecutionID)
					return nil
				},
			},

			// First submission — completes the step and run.
			{
				Name: "first-result-submission",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					execID := sc.MustGet("entry_execution_id").(string)
					_, err := sc.Runtime.Orchestrator.IngestResult(sc.Ctx, spineEngine.SubmitRequest{
						ExecutionID: execID,
						OutcomeID:   "completed",
					})
					return err
				},
			},

			// Run should now be completed.
			scenarioEngine.AssertRunStatus(domain.RunStatusCompleted),

			// Second submission with the same execution ID — must not error or
			// corrupt state.
			{
				Name: "second-result-submission-is-no-op",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					execID := sc.MustGet("entry_execution_id").(string)
					resp, err := sc.Runtime.Orchestrator.IngestResult(sc.Ctx, spineEngine.SubmitRequest{
						ExecutionID: execID,
						OutcomeID:   "completed",
					})
					if err != nil {
						return fmt.Errorf("second IngestResult returned unexpected error: %w", err)
					}
					if resp == nil {
						return fmt.Errorf("expected non-nil response on idempotent resubmission")
					}
					return nil
				},
			},

			// Run remains completed — was not double-advanced.
			scenarioEngine.AssertRunStatus(domain.RunStatusCompleted),
		},
	})
}
