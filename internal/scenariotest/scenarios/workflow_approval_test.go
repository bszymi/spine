//go:build scenario

package scenarios_test

import (
	"fmt"
	"testing"

	"github.com/bszymi/spine/internal/domain"
	spineEngine "github.com/bszymi/spine/internal/engine"
	"github.com/bszymi/spine/internal/scenariotest/assert"
	"github.com/bszymi/spine/internal/scenariotest/engine"
	"github.com/bszymi/spine/internal/scenariotest/harness"
)

// Simplified review workflow for testing approval/rejection flows.
// Two steps: work → review, with approve/reject/rework outcomes.
const reviewTestWorkflowYAML = `id: task-review-test
name: Review Test Workflow
version: "1.0"
status: Active
description: Workflow for testing approval and rejection flows.
applies_to:
  - Task
entry_step: work
steps:
  - id: work
    name: Do Work
    type: manual
    outcomes:
      - id: done
        name: Work Complete
        next_step: review
    timeout: "4h"

  - id: review
    name: Review Work
    type: review
    outcomes:
      - id: approved
        name: Approved
        next_step: end
      - id: rejected
        name: Rejected (Closed)
        next_step: end
      - id: rework
        name: Needs Rework
        next_step: work
    timeout: "24h"
`

// setupReviewRun returns steps that seed the review workflow, create
// artifacts, sync projections, and start a run ready for testing.
func setupReviewRun(idSuffix string) []engine.Step {
	return []engine.Step{
		seedWorkflow("task-review-test", reviewTestWorkflowYAML),
		engine.SeedHierarchy(
			fmt.Sprintf("INIT-0%s", idSuffix),
			fmt.Sprintf("EPIC-0%s", idSuffix),
			fmt.Sprintf("TASK-0%s", idSuffix),
		),
		engine.SyncProjections(),
		engine.StartRun(fmt.Sprintf(
			"initiatives/init-0%s/epics/epic-0%s/tasks/task-0%s.md",
			idSuffix, idSuffix, idSuffix,
		)),
	}
}

// TestWorkflow_ApprovalFlow validates that a review step with "approved"
// outcome completes the run and records the approval decision.
//
// Scenario: Approved review completes the run
//   Given an active run on step "work"
//   When "done" is submitted on "work"
//   Then the current step should be "review"
//   When "approved" is submitted on "review"
//   Then the run should be completed
//     And the review step execution should be in Completed status
//     And total step executions should be 2
func TestWorkflow_ApprovalFlow(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "approval-flow",
		Description: "Approved review completes the run with correct outcome recorded",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: engine.Steps(
			setupReviewRun("20"),
			[]engine.Step{
				engine.AssertCurrentStep("work"),

				// Complete work.
				engine.SubmitStepResult("done"),
				engine.AssertCurrentStep("review"),

				// Approve at review.
				engine.SubmitStepResult("approved"),
				engine.AssertRunCompleted(),

				// Verify the review step recorded the approval outcome.
				{
					Name: "verify-approval-outcome",
					Action: func(sc *engine.ScenarioContext) error {
						runID := sc.MustGet("run_id").(string)
						reviewExecID := fmt.Sprintf("%s-review-1", runID)
						assert.StepExecutionStatus(sc.T, sc.DB, sc.Ctx, reviewExecID, domain.StepStatusCompleted)
						return nil
					},
				},

				// Verify step count: work + review = 2.
				assertStepCount(2),
			},
		),
	})
}

// TestWorkflow_RejectionWithRework validates that a rejection sends the
// workflow back to the work step and a subsequent approval completes it.
//
// Scenario: Rejection with rework loops back to work; re-approval completes run
//   Given an active run on step "work"
//   When "done" is submitted, then "rework" is submitted on review
//   Then the run should return to step "work" and remain Active
//   When "done" is submitted again, then "approved" on review
//   Then the run should be completed with 4 total step executions
func TestWorkflow_RejectionWithRework(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "rejection-with-rework",
		Description: "Rejected review returns to work step; re-approval completes the run",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: engine.Steps(
			setupReviewRun("21"),
			[]engine.Step{
				// First pass: work → review → rework.
				engine.SubmitStepResult("done"),
				engine.AssertCurrentStep("review"),
				engine.SubmitStepResult("rework"),
				engine.AssertCurrentStep("work"),

				// Verify the run is still active after rejection.
				engine.AssertRunStatus(domain.RunStatusActive),

				// Second pass: work → review → approved.
				engine.SubmitStepResult("done"),
				engine.AssertCurrentStep("review"),
				engine.SubmitStepResult("approved"),
				engine.AssertRunCompleted(),

				// Verify step count: work-1, review-1, work-2, review-2 = 4.
				assertStepCount(4),
			},
		),
	})
}

// TestWorkflow_RejectionClosed validates that a "rejected" outcome
// on the review step terminates the run (goes to end).
//
// Scenario: Rejected-closed review terminates the run
//   Given an active run on step "work"
//   When "done" is submitted on "work"
//     And "rejected" is submitted on "review"
//   Then the run should be completed
//     And the review step outcome should be recorded as "rejected"
//     And total step executions should be 2
func TestWorkflow_RejectionClosed(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "rejection-closed",
		Description: "Rejected-closed review terminates the run with rejection recorded",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: engine.Steps(
			setupReviewRun("22"),
			[]engine.Step{
				engine.SubmitStepResult("done"),
				engine.AssertCurrentStep("review"),

				// Reject (closed) — goes to end, completing the run.
				engine.SubmitStepResult("rejected"),
				engine.AssertRunCompleted(),

				// Verify the review step recorded the rejection outcome.
				{
					Name: "verify-rejection-outcome",
					Action: func(sc *engine.ScenarioContext) error {
						runID := sc.MustGet("run_id").(string)
						reviewExecID := fmt.Sprintf("%s-review-1", runID)
						assert.StepExecutionStatus(sc.T, sc.DB, sc.Ctx, reviewExecID, domain.StepStatusCompleted)

						// Verify outcome ID is recorded.
						exec, err := sc.Runtime.Store.GetStepExecution(sc.Ctx, reviewExecID)
						if err != nil {
							return fmt.Errorf("get review execution: %w", err)
						}
						if exec.OutcomeID != "rejected" {
							sc.T.Errorf("expected outcome 'rejected', got %q", exec.OutcomeID)
						}
						return nil
					},
				},

				// Verify step count: work + review = 2.
				assertStepCount(2),
			},
		),
	})
}

// TestWorkflow_ReworkCycleLimit validates that repeated rejections
// exceeding MaxReworkCycles (10) cause the run to fail.
//
// Scenario: Exceeding the rework cycle limit fails the run
//   Given an active run on step "work"
//   When the work -> review -> rework cycle is repeated MaxReworkCycles (10) times
//   Then on the final rework submission the run should transition to Failed status
func TestWorkflow_ReworkCycleLimit(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "rework-cycle-limit",
		Description: "Exceeding the rework cycle limit fails the run",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: engine.Steps(
			setupReviewRun("23"),
			[]engine.Step{
				// Cycle through work → review → rework until the limit is hit.
				// MaxReworkCycles = 10, so the work step can be visited 10 times.
				// On the 11th transition to work, the run should fail.
				{
					Name: "exhaust-rework-cycles",
					Action: func(sc *engine.ScenarioContext) error {
						for i := 0; i < domain.MaxReworkCycles; i++ {
							// Submit work as done.
							execID := sc.MustGet("current_execution_id").(string)
							_, err := sc.Runtime.Orchestrator.IngestResult(sc.Ctx, spineEngine.SubmitRequest{
								ExecutionID: execID,
								OutcomeID:   "done",
							})
							if err != nil {
								return fmt.Errorf("submit work (cycle %d): %w", i+1, err)
							}

							// Refresh state to get review step.
							runID := sc.MustGet("run_id").(string)
							steps, err := sc.Runtime.Store.ListStepExecutionsByRun(sc.Ctx, runID)
							if err != nil {
								return fmt.Errorf("list steps (cycle %d): %w", i+1, err)
							}
							var reviewExecID string
							for j := range steps {
								if !steps[j].Status.IsTerminal() && steps[j].Status != domain.StepStatusBlocked {
									reviewExecID = steps[j].ExecutionID
									break
								}
							}
							if reviewExecID == "" {
								return fmt.Errorf("no active review step found (cycle %d)", i+1)
							}

							// Submit review as rework.
							_, err = sc.Runtime.Orchestrator.IngestResult(sc.Ctx, spineEngine.SubmitRequest{
								ExecutionID: reviewExecID,
								OutcomeID:   "rework",
							})

							// On the last cycle, the rework should fail the run.
							if i == domain.MaxReworkCycles-1 {
								// The run should be failed now.
								run, rerr := sc.Runtime.Store.GetRun(sc.Ctx, runID)
								if rerr != nil {
									return fmt.Errorf("get run after limit: %w", rerr)
								}
								if run.Status != domain.RunStatusFailed {
									return fmt.Errorf("expected run failed after %d reworks, got %s", i+1, run.Status)
								}
								return nil
							}

							if err != nil {
								return fmt.Errorf("submit rework (cycle %d): %w", i+1, err)
							}

							// Update current execution to the new work step.
							steps, err = sc.Runtime.Store.ListStepExecutionsByRun(sc.Ctx, sc.MustGet("run_id").(string))
							if err != nil {
								return fmt.Errorf("list steps after rework (cycle %d): %w", i+1, err)
							}
							for j := range steps {
								if !steps[j].Status.IsTerminal() && steps[j].Status != domain.StepStatusBlocked {
									sc.Set("current_execution_id", steps[j].ExecutionID)
									break
								}
							}
						}
						return nil
					},
				},

				// Verify the run is in failed state.
				engine.AssertRunStatus(domain.RunStatusFailed),
			},
		),
	})
}
