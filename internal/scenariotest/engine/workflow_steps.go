package engine

import (
	"fmt"

	"github.com/bszymi/spine/internal/artifact"
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

// StartPlanningRun returns a step that starts a planning run for artifact creation.
// Stores the run ID and entry step execution ID in scenario state.
// Requires projections to be synced first (SyncProjections step) so that
// the workflow resolver can find the workflow definitions.
func StartPlanningRun(artifactPath, artifactContent string) Step {
	return Step{
		Name: "start-planning-run-" + artifactPath,
		Action: func(sc *ScenarioContext) error {
			if sc.Runtime.Orchestrator == nil {
				return fmt.Errorf("StartPlanningRun requires WithOrchestrator() on the runtime")
			}
			result, err := sc.Runtime.Orchestrator.StartPlanningRun(sc.Ctx, artifactPath, artifactContent)
			if err != nil {
				return fmt.Errorf("start planning run for %s: %w", artifactPath, err)
			}
			sc.Set("run_id", result.Run.RunID)
			sc.Set("run_status", string(result.Run.Status))
			sc.Set("run_mode", string(result.Run.Mode))
			if result.EntryStep != nil {
				sc.Set("current_execution_id", result.EntryStep.ExecutionID)
				sc.Set("current_step_id", result.EntryStep.StepID)
			}
			return nil
		},
	}
}

// CreateArtifactOnBranch returns a step that creates an artifact on the current
// run's branch using WriteContext. Used for adding child artifacts during the
// draft step of a planning run.
func CreateArtifactOnBranch(path, content string) Step {
	return Step{
		Name: "create-on-branch-" + path,
		Action: func(sc *ScenarioContext) error {
			runID := sc.MustGet("run_id").(string)
			run, err := sc.Runtime.Store.GetRun(sc.Ctx, runID)
			if err != nil {
				return fmt.Errorf("get run: %w", err)
			}
			if run.BranchName == "" {
				return fmt.Errorf("run %s has no branch", runID)
			}
			ctx := artifact.WithWriteContext(sc.Ctx, artifact.WriteContext{Branch: run.BranchName})
			if _, err := sc.Runtime.Artifacts.Create(ctx, path, content); err != nil {
				return fmt.Errorf("create artifact %s on branch: %w", path, err)
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

// MergeRunBranch returns a step that merges the run branch to main.
// Used after a run enters committing state from a step with commit metadata.
func MergeRunBranch() Step {
	return Step{
		Name: "merge-run-branch",
		Action: func(sc *ScenarioContext) error {
			if sc.Runtime.Orchestrator == nil {
				return fmt.Errorf("MergeRunBranch requires WithOrchestrator() on the runtime")
			}
			runID := sc.MustGet("run_id").(string)
			if err := sc.Runtime.Orchestrator.MergeRunBranch(sc.Ctx, runID); err != nil {
				return fmt.Errorf("merge run branch: %w", err)
			}
			run, err := sc.Runtime.Store.GetRun(sc.Ctx, runID)
			if err != nil {
				return fmt.Errorf("get run after merge: %w", err)
			}
			sc.Set("run_status", string(run.Status))
			return nil
		},
	}
}

// CancelRun returns a step that cancels the current run.
func CancelRun() Step {
	return Step{
		Name: "cancel-run",
		Action: func(sc *ScenarioContext) error {
			if sc.Runtime.Orchestrator == nil {
				return fmt.Errorf("CancelRun requires WithOrchestrator() on the runtime")
			}
			runID := sc.MustGet("run_id").(string)
			if err := sc.Runtime.Orchestrator.CancelRun(sc.Ctx, runID); err != nil {
				return fmt.Errorf("cancel run: %w", err)
			}
			run, err := sc.Runtime.Store.GetRun(sc.Ctx, runID)
			if err != nil {
				return fmt.Errorf("get run after cancel: %w", err)
			}
			sc.Set("run_status", string(run.Status))
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

// AssertBranchExists returns a step that asserts the current run's branch exists.
func AssertBranchExists() Step {
	return Step{
		Name: "assert-branch-exists",
		Action: func(sc *ScenarioContext) error {
			runID := sc.MustGet("run_id").(string)
			run, err := sc.Runtime.Store.GetRun(sc.Ctx, runID)
			if err != nil {
				return fmt.Errorf("get run: %w", err)
			}
			if run.BranchName == "" {
				sc.T.Errorf("expected run %s to have a branch name", runID)
				return nil
			}
			assert.BranchExists(sc.T, sc.Repo, run.BranchName)
			sc.Set("branch_name", run.BranchName)
			return nil
		},
	}
}

// AssertBranchNotExists returns a step that asserts the current run's branch
// has been cleaned up (deleted).
func AssertBranchNotExists() Step {
	return Step{
		Name: "assert-branch-not-exists",
		Action: func(sc *ScenarioContext) error {
			branchName := sc.MustGet("branch_name").(string)
			assert.BranchNotExists(sc.T, sc.Repo, branchName)
			return nil
		},
	}
}

// WriteOnBranch returns a step that writes a file on the current run's branch
// and commits it. Used for simulating work during a standard run.
func WriteOnBranch(path, content, commitMsg string) Step {
	return Step{
		Name: "write-on-branch-" + path,
		Action: func(sc *ScenarioContext) error {
			runID := sc.MustGet("run_id").(string)
			run, err := sc.Runtime.Store.GetRun(sc.Ctx, runID)
			if err != nil {
				return fmt.Errorf("get run: %w", err)
			}
			if run.BranchName == "" {
				return fmt.Errorf("run %s has no branch", runID)
			}
			sc.Repo.CheckoutBranch(sc.T, run.BranchName)
			sc.Repo.WriteArtifact(sc.T, path, content)
			sc.Repo.CommitAll(sc.T, commitMsg)
			sc.Repo.CheckoutBranch(sc.T, "main")
			return nil
		},
	}
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
