package engine

import (
	"fmt"

	"github.com/bszymi/spine/internal/domain"
	spineEngine "github.com/bszymi/spine/internal/engine"
	"github.com/bszymi/spine/internal/scenariotest/assert"
)

// StartRun returns a step that starts a workflow run for the given task.
// Stores the run ID and entry step execution ID in scenario state.
// Requires projections to be synced first (SyncProjections step) so that
// the workflow resolver can find the workflow definitions.
func StartRun(taskPath string) Step {
	return Step{
		Name: "start-run-" + taskPath,
		Action: func(sc *ScenarioContext) error {
			if sc.Runtime.Orchestrator == nil {
				return fmt.Errorf("StartRun requires WithOrchestrator() on the runtime")
			}
			result, err := sc.Runtime.Orchestrator.StartRun(sc.Ctx, taskPath)
			if err != nil {
				return fmt.Errorf("start run for %s: %w", taskPath, err)
			}
			sc.Set("run_id", result.Run.RunID)
			sc.Set("run_status", string(result.Run.Status))
			if result.EntryStep != nil {
				sc.Set("current_execution_id", result.EntryStep.ExecutionID)
				sc.Set("current_step_id", result.EntryStep.StepID)
			}
			return nil
		},
	}
}

// SubmitStepResult returns a step that submits a result for the current
// step execution with the given outcome ID. Uses IngestResult which validates
// required outputs before routing, matching the production API path.
// Optional outputs can be provided for steps with required_outputs.
func SubmitStepResult(outcomeID string, outputs ...string) Step {
	return Step{
		Name: "submit-step-result-" + outcomeID,
		Action: func(sc *ScenarioContext) error {
			if sc.Runtime.Orchestrator == nil {
				return fmt.Errorf("SubmitStepResult requires WithOrchestrator() on the runtime")
			}
			execID := sc.MustGet("current_execution_id").(string)

			_, err := sc.Runtime.Orchestrator.IngestResult(sc.Ctx, spineEngine.SubmitRequest{
				ExecutionID:       execID,
				OutcomeID:         outcomeID,
				ArtifactsProduced: outputs,
			})
			if err != nil {
				return fmt.Errorf("submit step result %s: %w", outcomeID, err)
			}

			// Refresh run state.
			runID := sc.MustGet("run_id").(string)
			run, err := sc.Runtime.Store.GetRun(sc.Ctx, runID)
			if err != nil {
				return fmt.Errorf("get run after submit: %w", err)
			}
			sc.Set("run_status", string(run.Status))

			// Find the latest active step execution if the run is still active.
			if !run.Status.IsTerminal() && run.CurrentStepID != "" {
				steps, err := sc.Runtime.Store.ListStepExecutionsByRun(sc.Ctx, runID)
				if err != nil {
					return fmt.Errorf("list steps: %w", err)
				}
				for i := range steps {
					if !steps[i].Status.IsTerminal() && steps[i].Status != domain.StepStatusBlocked {
						sc.Set("current_execution_id", steps[i].ExecutionID)
						sc.Set("current_step_id", steps[i].StepID)
						break
					}
				}
			}

			return nil
		},
	}
}

// AssertRunStatus returns a step that asserts the current run status.
func AssertRunStatus(expected domain.RunStatus) Step {
	return Step{
		Name: fmt.Sprintf("assert-run-status-%s", expected),
		Action: func(sc *ScenarioContext) error {
			runID := sc.MustGet("run_id").(string)
			assert.RunStatus(sc.T, sc.DB, sc.Ctx, runID, expected)
			return nil
		},
	}
}

// AssertRunCompleted returns a step that asserts the run has completed.
func AssertRunCompleted() Step {
	return AssertRunStatus(domain.RunStatusCompleted)
}

// AssertCurrentStep returns a step that asserts the run's current step ID.
func AssertCurrentStep(expectedStepID string) Step {
	return Step{
		Name: "assert-current-step-" + expectedStepID,
		Action: func(sc *ScenarioContext) error {
			runID := sc.MustGet("run_id").(string)
			assert.CurrentStep(sc.T, sc.DB, sc.Ctx, runID, expectedStepID)
			return nil
		},
	}
}
