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

// setupWorkflowRun returns steps that seed a workflow, create an artifact
// hierarchy, sync projections, and start a run. After these steps,
// scenario state contains: run_id, current_execution_id, current_step_id.
func setupWorkflowRun() []engine.Step {
	return []engine.Step{
		seedWorkflow("task-default", goldenTaskDefaultYAML),
		engine.SeedHierarchy("INIT-010", "EPIC-010", "TASK-010"),
		engine.SyncProjections(),
		engine.StartRun("initiatives/init-010/epics/epic-010/tasks/task-010.md"),
	}
}

// TestWorkflow_RejectsInvalidOutcome verifies that submitting an outcome
// ID that doesn't exist on the current step is rejected.
//
// Scenario: Submitting a non-existent outcome ID is rejected
//   Given an active run on step "draft"
//   When outcome "nonexistent" is submitted
//   Then the submission should fail with error code invalid_params
//     And the run should remain Active on step "draft"
func TestWorkflow_RejectsInvalidOutcome(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "rejects-invalid-outcome",
		Description: "Submitting a non-existent outcome ID is rejected with invalid_params",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: engine.Steps(
			setupWorkflowRun(),
			[]engine.Step{
				// The current step is "draft" which only has outcome "ready".
				// Submitting "nonexistent" should be rejected.
				{
					Name: "submit-invalid-outcome",
					Action: func(sc *engine.ScenarioContext) error {
						execID := sc.MustGet("current_execution_id").(string)
						_, err := sc.Runtime.Orchestrator.IngestResult(sc.Ctx, spineEngine.SubmitRequest{
							ExecutionID: execID,
							OutcomeID:   "nonexistent",
						})
						assert.ErrorCode(sc.T, err, domain.ErrInvalidParams)
						assert.ErrorContains(sc.T, err, "not defined for step")
						return nil
					},
				},
				// Run should still be active and on the same step.
				engine.AssertRunStatus(domain.RunStatusActive),
				engine.AssertCurrentStep("draft"),
			},
		),
	})
}

// TestWorkflow_RejectsMissingRequiredOutputs verifies that submitting
// a step result without required outputs fails the step.
//
// Scenario: Submitting without required outputs fails the step
//   Given an active run advanced to step "execute" (requires "deliverable" output)
//   When "completed" is submitted without any artifacts produced
//   Then the submission should fail with "missing required outputs"
//     And the step execution should be in Failed status
func TestWorkflow_RejectsMissingRequiredOutputs(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "rejects-missing-required-outputs",
		Description: "Submitting without required outputs fails the step with invalid_result",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: engine.Steps(
			setupWorkflowRun(),
			[]engine.Step{
				// Advance to "execute" step which requires "deliverable" output.
				engine.SubmitStepResult("ready"),
				engine.AssertCurrentStep("execute"),

				// Submit without the required output.
				{
					Name: "submit-without-outputs",
					Action: func(sc *engine.ScenarioContext) error {
						execID := sc.MustGet("current_execution_id").(string)
						_, err := sc.Runtime.Orchestrator.IngestResult(sc.Ctx, spineEngine.SubmitRequest{
							ExecutionID: execID,
							OutcomeID:   "completed",
							// No ArtifactsProduced — should fail.
						})
						assert.ErrorContains(sc.T, err, "missing required outputs")
						return nil
					},
				},

				// Verify the step execution was failed (not the run).
				{
					Name: "verify-step-failed",
					Action: func(sc *engine.ScenarioContext) error {
						execID := sc.MustGet("current_execution_id").(string)
						assert.StepExecutionStatus(sc.T, sc.DB, sc.Ctx, execID, domain.StepStatusFailed)
						return nil
					},
				},
			},
		),
	})
}

// TestWorkflow_RejectsNonExistentExecution verifies that submitting
// a result for a non-existent execution ID returns not_found.
//
// Scenario: Submitting to a non-existent execution ID returns not_found
//   Given an active run
//   When outcome "ready" is submitted to execution ID "fake-execution-id"
//   Then the submission should fail with error code not_found
//     And the run should remain Active
func TestWorkflow_RejectsNonExistentExecution(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "rejects-nonexistent-execution",
		Description: "Submitting to a non-existent execution ID returns not_found",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: engine.Steps(
			setupWorkflowRun(),
			[]engine.Step{
				{
					Name: "submit-to-fake-execution",
					Action: func(sc *engine.ScenarioContext) error {
						_, err := sc.Runtime.Orchestrator.IngestResult(sc.Ctx, spineEngine.SubmitRequest{
							ExecutionID: "fake-execution-id",
							OutcomeID:   "ready",
						})
						assert.ErrorCode(sc.T, err, domain.ErrNotFound)
						return nil
					},
				},
				// Run should be unaffected.
				engine.AssertRunStatus(domain.RunStatusActive),
			},
		),
	})
}

// TestWorkflow_IdempotentOnCompletedStep verifies that re-submitting
// a result to an already-completed step returns success (idempotent)
// without changing the run state.
//
// Scenario: Re-submitting to a completed step is idempotent
//   Given an active run where "draft" step completed and run advanced to "execute"
//   When outcome "ready" is re-submitted to the already-completed draft execution
//   Then the submission should succeed with status "completed"
//     And the run should still be on step "execute" (not re-advanced)
func TestWorkflow_IdempotentOnCompletedStep(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "idempotent-on-completed-step",
		Description: "Re-submitting to a completed step is idempotent and does not change state",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: engine.Steps(
			setupWorkflowRun(),
			[]engine.Step{
				// Complete the draft step.
				engine.SubmitStepResult("ready"),
				engine.AssertCurrentStep("execute"),

				// Re-submit to the already-completed draft step execution.
				// Should return successfully (idempotent), not error.
				{
					Name: "re-submit-completed-step",
					Action: func(sc *engine.ScenarioContext) error {
						// The draft step execution ID follows the pattern: {runID}-draft-1
						runID := sc.MustGet("run_id").(string)
						draftExecID := fmt.Sprintf("%s-draft-1", runID)

						resp, err := sc.Runtime.Orchestrator.IngestResult(sc.Ctx, spineEngine.SubmitRequest{
							ExecutionID: draftExecID,
							OutcomeID:   "ready",
						})
						assert.NoError(sc.T, err)
						if resp.Status != domain.StepStatusCompleted {
							sc.T.Errorf("expected completed status in idempotent response, got %s", resp.Status)
						}
						return nil
					},
				},

				// Run should still be on execute (not re-advanced).
				engine.AssertCurrentStep("execute"),
			},
		),
	})
}

// TestWorkflow_RejectsEmptyExecutionID verifies that submitting with
// an empty execution ID returns invalid_params.
//
// Scenario: Submitting with an empty execution ID is rejected
//   Given an active run
//   When an outcome is submitted with an empty execution ID
//   Then the submission should fail with error code invalid_params
func TestWorkflow_RejectsEmptyExecutionID(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "rejects-empty-execution-id",
		Description: "Submitting with an empty execution ID returns invalid_params",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: engine.Steps(
			setupWorkflowRun(),
			[]engine.Step{
				{
					Name: "submit-empty-execution-id",
					Action: func(sc *engine.ScenarioContext) error {
						_, err := sc.Runtime.Orchestrator.IngestResult(sc.Ctx, spineEngine.SubmitRequest{
							ExecutionID: "",
							OutcomeID:   "ready",
						})
						assert.ErrorCode(sc.T, err, domain.ErrInvalidParams)
						return nil
					},
				},
			},
		),
	})
}
