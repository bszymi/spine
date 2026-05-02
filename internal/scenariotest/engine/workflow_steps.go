package engine

import (
	"fmt"

	"github.com/bszymi/spine/internal/artifact"
	"github.com/bszymi/spine/internal/domain"
	spineEngine "github.com/bszymi/spine/internal/engine"
	"github.com/bszymi/spine/internal/scenariotest/assert"
	"github.com/bszymi/spine/internal/workflow"
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

// StartWorkflowPlanningRun returns a step that starts a planning run governing
// a workflow-definition edit (ADR-008). Mirrors StartPlanningRun but routes
// through the workflow planning path — YAML body, workflow.Service writer,
// governing workflow resolved as applies_to: [Workflow] / mode: creation.
func StartWorkflowPlanningRun(workflowID, body string) Step {
	return Step{
		Name: "start-workflow-planning-run-" + workflowID,
		Action: func(sc *ScenarioContext) error {
			if sc.Runtime.Orchestrator == nil {
				return fmt.Errorf("StartWorkflowPlanningRun requires WithOrchestrator() on the runtime")
			}
			result, err := sc.Runtime.Orchestrator.StartWorkflowPlanningRun(sc.Ctx, workflowID, body)
			if err != nil {
				return fmt.Errorf("start workflow planning run for %s: %w", workflowID, err)
			}
			sc.Set("run_id", result.Run.RunID)
			sc.Set("run_status", string(result.Run.Status))
			sc.Set("run_mode", string(result.Run.Mode))
			sc.Set("run_workflow_id", result.Run.WorkflowID)
			sc.Set("run_workflow_sha", result.Run.WorkflowVersion)
			if result.EntryStep != nil {
				sc.Set("current_execution_id", result.EntryStep.ExecutionID)
				sc.Set("current_step_id", result.EntryStep.StepID)
			}
			return nil
		},
	}
}

// UpdateWorkflowOnBranch rewrites a workflow on the current run's branch using
// the workflow.Service with a WriteContext. Used to stack edits during the
// draft step of a workflow planning run.
func UpdateWorkflowOnBranch(workflowID, body string) Step {
	return Step{
		Name: "update-workflow-on-branch-" + workflowID,
		Action: func(sc *ScenarioContext) error {
			runID := sc.MustGet("run_id").(string)
			run, err := sc.Runtime.Store.GetRun(sc.Ctx, runID)
			if err != nil {
				return fmt.Errorf("get run: %w", err)
			}
			if run.BranchName == "" {
				return fmt.Errorf("run %s has no branch", runID)
			}
			ctx := workflow.WithWriteContext(sc.Ctx, workflow.WriteContext{Branch: run.BranchName})
			if _, err := sc.Runtime.Workflows.Update(ctx, workflowID, body); err != nil {
				return fmt.Errorf("update workflow %s on branch: %w", workflowID, err)
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

// AddSiblingTaskToRun mirrors POST /artifacts/add for Task children: it
// allocates the next ID by scanning the run's branch, builds the path via
// BuildArtifactPath, and creates the artifact. It stores the allocated ID and
// path in scenario state keyed by stateKey ("<key>_id", "<key>_path"), so
// subsequent steps can assert on both. buildContent is called with the
// allocated ID to produce the artifact body.
func AddSiblingTaskToRun(parentDir, title, stateKey string, buildContent func(id string) string) Step {
	return Step{
		Name: "add-sibling-task-" + stateKey,
		Action: func(sc *ScenarioContext) error {
			runID := sc.MustGet("run_id").(string)
			run, err := sc.Runtime.Store.GetRun(sc.Ctx, runID)
			if err != nil {
				return fmt.Errorf("get run: %w", err)
			}
			if run.BranchName == "" {
				return fmt.Errorf("run %s has no branch", runID)
			}
			id, err := artifact.NextID(sc.Ctx, sc.Repo.Git, parentDir, domain.ArtifactTypeTask, run.BranchName)
			if err != nil {
				return fmt.Errorf("allocate next task id: %w", err)
			}
			path := artifact.BuildArtifactPath(domain.ArtifactTypeTask, id, artifact.Slugify(title), parentDir)
			ctx := artifact.WithWriteContext(sc.Ctx, artifact.WriteContext{Branch: run.BranchName})
			if _, err := sc.Runtime.Artifacts.Create(ctx, path, buildContent(id)); err != nil {
				return fmt.Errorf("create sibling task %s on branch: %w", id, err)
			}
			sc.Set(stateKey+"_id", id)
			sc.Set(stateKey+"_path", path)
			return nil
		},
	}
}

// EnsureStepAssignedForTest auto-assigns the named step execution to a
// synthetic test actor if it is still in `waiting`. Used by
// scenariotests that exercise IngestResult / SubmitStepResult directly
// after StartRun: the Option B fix (INIT-020/EPIC-001/TASK-004) leaves
// non-machine steps in waiting, so the engine's submit guard would
// short-circuit before the test's intended assertion fires. This helper
// recreates the historical "phantom assigned" precondition without
// reintroducing the production bug.
//
// No-ops when the step is already assigned/in_progress or when the
// store is not wired. Returns the underlying store error only if the
// update itself fails.
func EnsureStepAssignedForTest(sc *ScenarioContext, execID string) error {
	if sc.Runtime.Store == nil {
		return nil
	}
	exec, err := sc.Runtime.Store.GetStepExecution(sc.Ctx, execID)
	if err != nil || exec == nil {
		return nil
	}
	if exec.Status != domain.StepStatusWaiting {
		return nil
	}
	exec.Status = domain.StepStatusAssigned
	if exec.ActorID == "" {
		exec.ActorID = "scenario-test-actor"
	}
	if uerr := sc.Runtime.Store.UpdateStepExecution(sc.Ctx, exec); uerr != nil {
		return fmt.Errorf("auto-assign waiting step before submit: %w", uerr)
	}
	return nil
}

// SubmitStepResult returns a step that submits a result for the current
// step execution with the given outcome ID. Uses IngestResult which validates
// required outputs before routing, matching the production API path.
// Optional outputs can be provided for steps with required_outputs.
//
// If the current step is in `waiting` (no auto-claim, no explicit /assign),
// the helper performs an implicit assignment to a synthetic test actor
// before submitting. This mirrors the historical "phantom assigned"
// auto-claim behavior that scenariotests grew up with, so that fixtures
// without an explicit claim/assign step continue to advance after the
// Option B fix (INIT-020/EPIC-001/TASK-004) tightened production semantics.
// Tests that want to assert the new state-machine semantics should call
// the orchestrator's AssignStep / ClaimStep directly instead of relying
// on this convenience. Tests that call IngestResult directly should use
// EnsureStepAssignedForTest.
func SubmitStepResult(outcomeID string, outputs ...string) Step {
	return Step{
		Name: "submit-step-result-" + outcomeID,
		Action: func(sc *ScenarioContext) error {
			if sc.Runtime.Orchestrator == nil {
				return fmt.Errorf("SubmitStepResult requires WithOrchestrator() on the runtime")
			}
			execID := sc.MustGet("current_execution_id").(string)

			if err := EnsureStepAssignedForTest(sc, execID); err != nil {
				return err
			}

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
