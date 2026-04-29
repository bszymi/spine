package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bszymi/spine/internal/artifact"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/store"
	"github.com/bszymi/spine/internal/workflow"
)

// StartRunResult contains the result of starting a new run.
type StartRunResult struct {
	Run       *domain.Run
	EntryStep *domain.StepExecution
}

// startRunParams holds the pre-configured run and workflow for startRunCommon.
type startRunParams struct {
	run      *domain.Run
	wfDef    *domain.WorkflowDefinition
	onBranch func(ctx context.Context, branchName string) error // optional: called after branch creation
}

// startRunCommon handles the shared startup sequence for both standard and planning runs:
// parse timeout → persist run → create branch → (onBranch callback) → create entry step
// → activate run → emit event → activate step.
func (o *Orchestrator) startRunCommon(ctx context.Context, p startRunParams) (*StartRunResult, error) {
	log := observe.Logger(ctx)
	run := p.run
	wfDef := p.wfDef
	now := run.CreatedAt

	// Set run-level timeout if configured on the workflow.
	if wfDef.Timeout != "" {
		if d, err := time.ParseDuration(wfDef.Timeout); err == nil {
			t := now.Add(d)
			run.TimeoutAt = &t
		} else {
			log.Warn("invalid workflow timeout duration", "timeout", wfDef.Timeout, "error", err)
		}
	}

	// Create Git branches first so a failure doesn't leave an orphaned DB
	// record that the scheduler's recovery logic could later activate.
	// The primary branch is cut from HEAD; each code repo's branch is
	// cut from that repo's default branch. The branch name is identical
	// across repos so downstream tooling can address the run by a
	// single ref regardless of which repository it touches. Auto-push
	// runs per-repo inside createRunBranches so a later failure can
	// roll back symmetric refs (local + remote) on every prior repo.
	created, err := o.createRunBranches(ctx, run)
	if err != nil {
		return nil, err
	}

	if err := o.store.CreateRun(ctx, run); err != nil {
		o.rollbackRunBranches(ctx, run, created)
		return nil, fmt.Errorf("create run: %w", err)
	}

	// Optional callback for work on the branch (e.g., writing artifacts in planning runs).
	if p.onBranch != nil {
		if err := p.onBranch(ctx, run.BranchName); err != nil {
			return nil, err
		}
	}

	// Create entry step execution BEFORE activation so that a failure here
	// leaves the run in pending (recoverable by scheduler) rather than
	// active with no step (unrecoverable).
	entryStep := &domain.StepExecution{
		ExecutionID: fmt.Sprintf("%s-%s-1", run.RunID, wfDef.EntryStep),
		RunID:       run.RunID,
		StepID:      wfDef.EntryStep,
		Status:      domain.StepStatusWaiting,
		Attempt:     1,
		CreatedAt:   now,
	}
	if err := o.store.CreateStepExecution(ctx, entryStep); err != nil {
		return nil, fmt.Errorf("create entry step: %w", err)
	}

	// Activate: pending → active.
	result, err := workflow.EvaluateRunTransition(run.Status, workflow.TransitionRequest{
		Trigger: workflow.TriggerActivate,
	})
	if err != nil {
		return nil, fmt.Errorf("activate run: %w", err)
	}

	if err := o.store.UpdateRunStatus(ctx, run.RunID, result.ToStatus); err != nil {
		return nil, fmt.Errorf("update run status: %w", err)
	}
	run.Status = result.ToStatus
	startedAt := now
	run.StartedAt = &startedAt

	o.emitEvent(ctx, domain.EventRunStarted, run.RunID, run.TraceID,
		fmt.Sprintf("evt-%s-started", run.TraceID[:12]), nil)

	log.Info("run started",
		"run_id", run.RunID,
		"task_path", run.TaskPath,
		"workflow_id", wfDef.ID,
		"entry_step", wfDef.EntryStep,
		"mode", run.Mode,
	)

	// Activate the entry step so it gets an actor assignment.
	if err := o.ActivateStep(ctx, run.RunID, wfDef.EntryStep); err != nil {
		log.Warn("entry step activation failed", "step_id", wfDef.EntryStep, "error", err)
	}

	return &StartRunResult{Run: run, EntryStep: entryStep}, nil
}

// StartRun creates a run for a task, resolves the governing workflow,
// transitions the run from pending to active, and activates the first step.
func (o *Orchestrator) StartRun(ctx context.Context, taskPath string) (*StartRunResult, error) {
	if taskPath == "" {
		return nil, domain.NewError(domain.ErrInvalidParams, "task_path is required")
	}

	art, err := o.artifacts.Read(ctx, taskPath, "HEAD")
	if err != nil {
		return nil, fmt.Errorf("read task artifact: %w", err)
	}

	// Check if the task is blocked by unresolved dependencies.
	if o.blocking != nil {
		blockResult, err := o.IsBlocked(ctx, taskPath)
		if err != nil {
			return nil, fmt.Errorf("check blocking status: %w", err)
		}
		if blockResult.Blocked {
			return nil, domain.NewError(domain.ErrPrecondition,
				fmt.Sprintf("task is blocked by: %v", blockResult.BlockedBy))
		}
	}

	// Repository runtime preconditions (INIT-014 EPIC-002 TASK-004) —
	// after blocking, before any branch-creating work, so a missing or
	// inactive binding fails the run start cleanly without touching Git.
	// Catalog-existence checks are TASK-003 territory and are expected
	// to have run at validate time before this point.
	if err := o.checkRepositoryPreconditions(ctx, art); err != nil {
		return nil, err
	}

	binding, err := o.workflows.ResolveWorkflow(ctx, string(art.Type), "")
	if err != nil {
		return nil, fmt.Errorf("resolve workflow: %w", err)
	}
	if binding.Workflow.EntryStep == "" {
		return nil, domain.NewError(domain.ErrInvalidParams, "workflow has no entry_step")
	}

	traceID, err := observe.GenerateTraceID()
	if err != nil {
		return nil, fmt.Errorf("generate trace ID: %w", err)
	}
	runID := fmt.Sprintf("run-%s", traceID[:8])

	return o.startRunCommon(ctx, startRunParams{
		run: &domain.Run{
			RunID:                runID,
			TaskPath:             taskPath,
			WorkflowPath:         binding.Workflow.Path,
			WorkflowID:           binding.Workflow.ID,
			WorkflowVersion:      binding.CommitSHA,
			WorkflowVersionLabel: binding.VersionLabel,
			Status:               domain.RunStatusPending,
			CurrentStepID:        binding.Workflow.EntryStep,
			BranchName:           generateBranchNameWithSuffix(domain.RunModeStandard, art.ID, taskPath, runID),
			AffectedRepositories: domain.AffectedRepositoriesForTask(art),
			PrimaryRepository:    true,
			TraceID:              traceID,
			CreatedAt:            time.Now(),
		},
		wfDef: binding.Workflow,
	})
}

// StartPlanningRun creates a planning run that governs artifact creation on a branch.
// The artifact is written to the branch, not to main. Per ADR-006 §2.
func (o *Orchestrator) StartPlanningRun(ctx context.Context, artifactPath, artifactContent string) (*StartRunResult, error) {
	if o.artifactWriter == nil {
		return nil, domain.NewError(domain.ErrUnavailable, "artifact writer not configured")
	}
	if artifactContent == "" {
		return nil, domain.NewError(domain.ErrInvalidParams, "artifact_content is required")
	}
	if artifactPath == "" {
		return nil, domain.NewError(domain.ErrInvalidParams, "artifact_path is required")
	}

	parsed, err := artifact.Parse(artifactPath, []byte(artifactContent))
	if err != nil {
		return nil, domain.NewError(domain.ErrInvalidParams, fmt.Sprintf("invalid artifact content: %v", err))
	}
	vResult := artifact.Validate(parsed)
	if vResult.Status != "passed" {
		return nil, domain.NewError(domain.ErrInvalidParams, fmt.Sprintf("artifact validation failed: %v", vResult.Errors))
	}

	binding, err := o.workflows.ResolveWorkflowForMode(ctx, string(parsed.Type), "", "creation")
	if err != nil {
		return nil, fmt.Errorf("resolve workflow: %w", err)
	}
	if binding.Workflow.EntryStep == "" {
		return nil, domain.NewError(domain.ErrInvalidParams, "workflow has no entry_step")
	}

	traceID, err := observe.GenerateTraceID()
	if err != nil {
		return nil, fmt.Errorf("generate trace ID: %w", err)
	}
	runID := fmt.Sprintf("run-%s", traceID[:8])
	branchName := generateBranchNameWithSuffix(domain.RunModePlanning, parsed.ID, artifactPath, runID)

	return o.startRunCommon(ctx, startRunParams{
		run: &domain.Run{
			RunID:                runID,
			TaskPath:             artifactPath,
			WorkflowPath:         binding.Workflow.Path,
			WorkflowID:           binding.Workflow.ID,
			WorkflowVersion:      binding.CommitSHA,
			WorkflowVersionLabel: binding.VersionLabel,
			Status:               domain.RunStatusPending,
			Mode:                 domain.RunModePlanning,
			CurrentStepID:        binding.Workflow.EntryStep,
			BranchName:           branchName,
			AffectedRepositories: []string{domain.PrimaryRepositoryID},
			PrimaryRepository:    true,
			TraceID:              traceID,
			CreatedAt:            time.Now(),
		},
		wfDef: binding.Workflow,
		onBranch: func(ctx context.Context, branch string) error {
			log := observe.Logger(ctx)
			branchCtx := artifact.WithWriteContext(ctx, artifact.WriteContext{Branch: branch})
			branchCtx = observe.WithTraceID(branchCtx, traceID)
			branchCtx = observe.WithRunID(branchCtx, runID)
			if _, err := o.artifactWriter.Create(branchCtx, artifactPath, artifactContent); err != nil {
				if delErr := o.git.DeleteBranch(ctx, branch); delErr != nil {
					log.Warn("failed to clean up planning branch", "branch", branch, "error", delErr)
				}
				if autoPushEnabled() {
					if delErr := o.git.DeleteRemoteBranch(ctx, "origin", branch); delErr != nil {
						log.Warn("auto-push: failed to clean up remote planning branch", "branch", branch, "error", delErr)
					}
				}
				return fmt.Errorf("create artifact on branch: %w", err)
			}
			return nil
		},
	})
}

// StartWorkflowPlanningRun creates a planning-mode run governing a workflow
// definition edit (ADR-008). The workflow body is validated, written to a
// fresh task branch, and the governing workflow is resolved as the
// `applies_to: [Workflow]` / `mode: creation` binding (today: workflow-lifecycle).
func (o *Orchestrator) StartWorkflowPlanningRun(ctx context.Context, workflowID, body string) (*StartRunResult, error) {
	if o.workflowWriter == nil {
		return nil, domain.NewError(domain.ErrUnavailable, "workflow writer not configured")
	}
	if workflowID == "" {
		return nil, domain.NewError(domain.ErrInvalidParams, "workflow id is required")
	}
	if body == "" {
		return nil, domain.NewError(domain.ErrInvalidParams, "workflow body is required")
	}

	path := workflow.WorkflowsDir + "/" + workflowID + ".yaml"

	parsed, err := workflow.Parse(path, []byte(body))
	if err != nil {
		return nil, domain.NewError(domain.ErrInvalidParams, fmt.Sprintf("invalid workflow body: %v", err))
	}
	if parsed.ID != workflowID {
		return nil, domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("body id %q does not match requested id %q", parsed.ID, workflowID))
	}
	if vResult := workflow.Validate(parsed); vResult.Status != "passed" {
		return nil, domain.NewErrorWithDetail(domain.ErrValidationFailed,
			"workflow validation failed", vResult.Errors)
	}

	binding, err := o.workflows.ResolveWorkflowForMode(ctx, string(domain.ArtifactTypeWorkflow), "", "creation")
	if err != nil {
		return nil, fmt.Errorf("resolve governing workflow: %w", err)
	}
	if binding.Workflow.EntryStep == "" {
		return nil, domain.NewError(domain.ErrInvalidParams, "governing workflow has no entry_step")
	}

	traceID, err := observe.GenerateTraceID()
	if err != nil {
		return nil, fmt.Errorf("generate trace ID: %w", err)
	}
	runID := fmt.Sprintf("run-%s", traceID[:8])
	branchName := generateBranchNameWithSuffix(domain.RunModePlanning, parsed.ID, path, runID)

	return o.startRunCommon(ctx, startRunParams{
		run: &domain.Run{
			RunID:                runID,
			TaskPath:             path,
			WorkflowPath:         binding.Workflow.Path,
			WorkflowID:           binding.Workflow.ID,
			WorkflowVersion:      binding.CommitSHA,
			WorkflowVersionLabel: binding.VersionLabel,
			Status:               domain.RunStatusPending,
			Mode:                 domain.RunModePlanning,
			CurrentStepID:        binding.Workflow.EntryStep,
			BranchName:           branchName,
			AffectedRepositories: []string{domain.PrimaryRepositoryID},
			PrimaryRepository:    true,
			TraceID:              traceID,
			CreatedAt:            time.Now(),
		},
		wfDef: binding.Workflow,
		onBranch: func(ctx context.Context, branch string) error {
			log := observe.Logger(ctx)
			branchCtx := workflow.WithWriteContext(ctx, workflow.WriteContext{Branch: branch})
			branchCtx = observe.WithTraceID(branchCtx, traceID)
			branchCtx = observe.WithRunID(branchCtx, runID)
			if _, err := o.workflowWriter.Create(branchCtx, workflowID, body); err != nil {
				if delErr := o.git.DeleteBranch(ctx, branch); delErr != nil {
					log.Warn("failed to clean up workflow planning branch", "branch", branch, "error", delErr)
				}
				if autoPushEnabled() {
					if delErr := o.git.DeleteRemoteBranch(ctx, "origin", branch); delErr != nil {
						log.Warn("auto-push: failed to clean up remote workflow planning branch", "branch", branch, "error", delErr)
					}
				}
				return fmt.Errorf("create workflow on branch: %w", err)
			}
			return nil
		},
	})
}

// CompleteRun transitions an active run to completed (or committing if
// hasCommit is true) when the terminal step has been reached.
// When hasCommit is true, the run enters the committing state for Git
// persistence before completing. Uses the run state machine to validate.
func (o *Orchestrator) CompleteRun(ctx context.Context, runID string, hasCommit bool) error {
	run, err := o.store.GetRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("get run: %w", err)
	}

	result, err := workflow.EvaluateRunTransition(run.Status, workflow.TransitionRequest{
		Trigger:    workflow.TriggerStepCompleted,
		NextStepID: "end",
		HasCommit:  hasCommit,
	})
	if err != nil {
		return err
	}

	if err := o.store.UpdateRunStatus(ctx, runID, result.ToStatus); err != nil {
		return fmt.Errorf("update run status: %w", err)
	}

	log := observe.Logger(ctx)

	// Only emit run_completed when the run actually reached completed state,
	// not when it moved to committing (which still needs Git commit).
	if result.ToStatus == domain.RunStatusCompleted {
		o.emitEvent(ctx, domain.EventRunCompleted, runID, run.TraceID,
			fmt.Sprintf("evt-%s-completed", run.TraceID[:12]), nil)
		log.Info("run completed", "run_id", runID)
		if run.StartedAt != nil {
			observe.GlobalMetrics.RunDuration.ObserveDuration(time.Since(*run.StartedAt))
		}
		// Re-evaluate tasks that were blocked by this task.
		o.CheckAndEmitBlockingTransition(ctx, run.TaskPath)
		// Update execution projection to reflect completion.
		o.updateExecutionProjection(ctx, run.TaskPath, func(proj *store.ExecutionProjection) {
			proj.AssignedActorID = ""
			proj.AssignmentStatus = "unassigned"
			proj.Status = string(domain.StatusCompleted)
		})
		// Clean up the run branch after successful completion.
		_ = o.CleanupRunBranch(ctx, runID)
	} else {
		log.Info("run entering commit phase", "run_id", runID)

		// Immediately attempt the merge rather than waiting for the
		// scheduler poll. This is critical for planning runs where
		// artifacts live only on the branch until merged. The scheduler
		// remains as a safety net for transient failures.
		if err := o.MergeRunBranch(ctx, runID); err != nil {
			log.Warn("immediate merge attempt failed, scheduler will retry",
				"run_id", runID, "error", err)
		}
	}

	return nil
}

// FailRun transitions an active run to failed due to a permanent failure.
func (o *Orchestrator) FailRun(ctx context.Context, runID, reason string) error {
	run, err := o.store.GetRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("get run: %w", err)
	}

	result, err := workflow.EvaluateRunTransition(run.Status, workflow.TransitionRequest{
		Trigger: workflow.TriggerStepFailedPermanently,
	})
	if err != nil {
		return err
	}

	if err := o.store.UpdateRunStatus(ctx, runID, result.ToStatus); err != nil {
		return fmt.Errorf("update run status: %w", err)
	}

	payload, _ := json.Marshal(map[string]string{"reason": reason})
	o.emitEvent(ctx, domain.EventRunFailed, runID, run.TraceID,
		fmt.Sprintf("evt-%s-failed", run.TraceID[:12]), payload)

	observe.Logger(ctx).Info("run failed", "run_id", runID, "reason", reason)
	_ = o.CleanupRunBranch(ctx, runID)
	return nil
}

// CancelRun cancels an active or paused run.
func (o *Orchestrator) CancelRun(ctx context.Context, runID string) error {
	run, err := o.store.GetRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("get run: %w", err)
	}

	result, err := workflow.EvaluateRunTransition(run.Status, workflow.TransitionRequest{
		Trigger: workflow.TriggerCancel,
	})
	if err != nil {
		return err
	}

	if err := o.store.UpdateRunStatus(ctx, runID, result.ToStatus); err != nil {
		return fmt.Errorf("update run status: %w", err)
	}

	o.emitEvent(ctx, domain.EventRunCancelled, runID, run.TraceID,
		fmt.Sprintf("evt-%s-cancelled", run.TraceID[:12]), nil)

	observe.Logger(ctx).Info("run cancelled", "run_id", runID)
	_ = o.CleanupRunBranch(ctx, runID)
	return nil
}

// PauseRun transitions an active run to paused.
func (o *Orchestrator) PauseRun(ctx context.Context, runID, reason string) error {
	run, err := o.store.GetRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("get run: %w", err)
	}

	result, err := workflow.EvaluateRunTransition(run.Status, workflow.TransitionRequest{
		Trigger:     workflow.TriggerStepBlocked,
		PauseReason: reason,
	})
	if err != nil {
		return err
	}

	if err := o.store.UpdateRunStatus(ctx, runID, result.ToStatus); err != nil {
		return fmt.Errorf("update run status: %w", err)
	}

	o.emitEvent(ctx, domain.EventRunPaused, runID, run.TraceID,
		fmt.Sprintf("evt-%s-paused-%d", run.TraceID[:12], time.Now().UnixMilli()), nil)

	observe.Logger(ctx).Info("run paused", "run_id", runID, "reason", reason)
	return nil
}

// ResumeRun transitions a paused run back to active.
func (o *Orchestrator) ResumeRun(ctx context.Context, runID string) error {
	run, err := o.store.GetRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("get run: %w", err)
	}

	result, err := workflow.EvaluateRunTransition(run.Status, workflow.TransitionRequest{
		Trigger: workflow.TriggerResume,
	})
	if err != nil {
		return err
	}

	if err := o.store.UpdateRunStatus(ctx, runID, result.ToStatus); err != nil {
		return fmt.Errorf("update run status: %w", err)
	}

	o.emitEvent(ctx, domain.EventRunResumed, runID, run.TraceID,
		fmt.Sprintf("evt-%s-resumed-%d", run.TraceID[:12], time.Now().UnixMilli()), nil)

	observe.Logger(ctx).Info("run resumed", "run_id", runID)
	return nil
}

// findStepDef looks up a step definition by ID within a workflow.
func findStepDef(wf *domain.WorkflowDefinition, stepID string) *domain.StepDefinition {
	for i := range wf.Steps {
		if wf.Steps[i].ID == stepID {
			return &wf.Steps[i]
		}
	}
	return nil
}
