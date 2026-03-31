package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bszymi/spine/internal/artifact"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/workflow"
)

// StartRunResult contains the result of starting a new run.
type StartRunResult struct {
	Run       *domain.Run
	EntryStep *domain.StepExecution
}

// StartRun creates a run for a task, resolves the governing workflow,
// transitions the run from pending to active, and activates the first step.
func (o *Orchestrator) StartRun(ctx context.Context, taskPath string) (*StartRunResult, error) {
	if taskPath == "" {
		return nil, domain.NewError(domain.ErrInvalidParams, "task_path is required")
	}

	log := observe.Logger(ctx)

	// Read the task artifact to determine its type.
	artifact, err := o.artifacts.Read(ctx, taskPath, "HEAD")
	if err != nil {
		return nil, fmt.Errorf("read task artifact: %w", err)
	}

	// Resolve the governing workflow.
	binding, err := o.workflows.ResolveWorkflow(ctx, string(artifact.Type), "")
	if err != nil {
		return nil, fmt.Errorf("resolve workflow: %w", err)
	}

	wfDef := binding.Workflow
	if wfDef.EntryStep == "" {
		return nil, domain.NewError(domain.ErrInvalidParams, "workflow has no entry_step")
	}

	// Generate identifiers.
	traceID, err := observe.GenerateTraceID()
	if err != nil {
		return nil, fmt.Errorf("generate trace ID: %w", err)
	}
	runID := fmt.Sprintf("run-%s", traceID[:8])
	now := time.Now()

	// Create run in pending status first, then create branch.
	// This avoids orphan branches if run persistence fails.
	branchName := fmt.Sprintf("spine/run/%s", runID)

	run := &domain.Run{
		RunID:                runID,
		TaskPath:             taskPath,
		WorkflowPath:         wfDef.Path,
		WorkflowID:           wfDef.ID,
		WorkflowVersion:      binding.CommitSHA,
		WorkflowVersionLabel: binding.VersionLabel,
		Status:               domain.RunStatusPending,
		CurrentStepID:        wfDef.EntryStep,
		BranchName:           branchName,
		TraceID:              traceID,
		CreatedAt:            now,
	}

	// Set run-level timeout if configured on the workflow.
	if wfDef.Timeout != "" {
		if d, err := time.ParseDuration(wfDef.Timeout); err == nil {
			t := now.Add(d)
			run.TimeoutAt = &t
		} else {
			log.Warn("invalid workflow timeout duration", "timeout", wfDef.Timeout, "error", err)
		}
	}

	if err := o.store.CreateRun(ctx, run); err != nil {
		return nil, fmt.Errorf("create run: %w", err)
	}

	// Create the Git branch after the run is persisted.
	if err := o.git.CreateBranch(ctx, branchName, "HEAD"); err != nil {
		log.Warn("failed to create run branch", "branch", branchName, "error", err)
		run.BranchName = ""
	} else if autoPushEnabled() {
		if err := o.git.PushBranch(ctx, "origin", branchName); err != nil {
			log.Warn("auto-push: failed to push run branch", "branch", branchName, "error", err)
		}
	}

	// Create entry step execution BEFORE activation so that a failure here
	// leaves the run in pending (recoverable by scheduler) rather than
	// active with no step (unrecoverable).
	entryStep := &domain.StepExecution{
		ExecutionID: fmt.Sprintf("%s-%s-1", runID, wfDef.EntryStep),
		RunID:       runID,
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

	if err := o.store.UpdateRunStatus(ctx, runID, result.ToStatus); err != nil {
		return nil, fmt.Errorf("update run status: %w", err)
	}
	run.Status = result.ToStatus
	startedAt := now
	run.StartedAt = &startedAt

	// Emit run_started event (fire-and-forget per §6.1).
	if err := o.events.Emit(ctx, domain.Event{
		EventID:   fmt.Sprintf("evt-%s-started", traceID[:12]),
		Type:      domain.EventRunStarted,
		Timestamp: now,
		RunID:     runID,
		TraceID:   traceID,
	}); err != nil {
		log.Warn("failed to emit event", "event_type", domain.EventRunStarted, "error", err)
	}

	log.Info("run started",
		"run_id", runID,
		"task_path", taskPath,
		"workflow_id", wfDef.ID,
		"entry_step", wfDef.EntryStep,
	)

	// Activate the entry step so it gets an actor assignment.
	if err := o.ActivateStep(ctx, runID, wfDef.EntryStep); err != nil {
		// Activation failure is non-fatal for run creation; the step
		// remains in waiting and can be activated later (e.g. by scheduler
		// or retry). Log but don't fail StartRun.
		log.Warn("entry step activation failed", "step_id", wfDef.EntryStep, "error", err)
	}

	return &StartRunResult{Run: run, EntryStep: entryStep}, nil
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

	log := observe.Logger(ctx)

	// Parse and validate the artifact content before any side effects.
	parsed, err := artifact.Parse(artifactPath, []byte(artifactContent))
	if err != nil {
		return nil, domain.NewError(domain.ErrInvalidParams, fmt.Sprintf("invalid artifact content: %v", err))
	}

	vResult := artifact.Validate(parsed)
	if vResult.Status != "passed" {
		return nil, domain.NewError(domain.ErrInvalidParams, fmt.Sprintf("artifact validation failed: %v", vResult.Errors))
	}

	// Resolve the governing workflow for this artifact type.
	// Resolve creation workflow for planning runs per ADR-006 §4.
	binding, err := o.workflows.ResolveWorkflowForMode(ctx, string(parsed.Type), "", "creation")
	if err != nil {
		return nil, fmt.Errorf("resolve workflow: %w", err)
	}

	wfDef := binding.Workflow
	if wfDef.EntryStep == "" {
		return nil, domain.NewError(domain.ErrInvalidParams, "workflow has no entry_step")
	}

	// Generate identifiers.
	traceID, err := observe.GenerateTraceID()
	if err != nil {
		return nil, fmt.Errorf("generate trace ID: %w", err)
	}
	runID := fmt.Sprintf("run-%s", traceID[:8])
	now := time.Now()
	branchName := fmt.Sprintf("spine/run/%s", runID)

	// Create run record with planning mode first, before branch or artifact
	// writes. This ensures the run exists in the store before any events are
	// emitted by the artifact writer. Per StartRun pattern: persist first,
	// then create branch to avoid phantom events for runs that never existed.
	run := &domain.Run{
		RunID:                runID,
		TaskPath:             artifactPath,
		WorkflowPath:         wfDef.Path,
		WorkflowID:           wfDef.ID,
		WorkflowVersion:      binding.CommitSHA,
		WorkflowVersionLabel: binding.VersionLabel,
		Status:               domain.RunStatusPending,
		Mode:                 domain.RunModePlanning,
		CurrentStepID:        wfDef.EntryStep,
		BranchName:           branchName,
		TraceID:              traceID,
		CreatedAt:            now,
	}

	if wfDef.Timeout != "" {
		if d, err := time.ParseDuration(wfDef.Timeout); err == nil {
			t := now.Add(d)
			run.TimeoutAt = &t
		} else {
			log.Warn("invalid workflow timeout duration", "timeout", wfDef.Timeout, "error", err)
		}
	}

	if err := o.store.CreateRun(ctx, run); err != nil {
		return nil, fmt.Errorf("create run: %w", err)
	}

	// Create Git branch from HEAD after run is persisted.
	if err := o.git.CreateBranch(ctx, branchName, "HEAD"); err != nil {
		return nil, fmt.Errorf("create planning branch: %w", err)
	}
	if autoPushEnabled() {
		if err := o.git.PushBranch(ctx, "origin", branchName); err != nil {
			log.Warn("auto-push: failed to push planning branch", "branch", branchName, "error", err)
		}
	}

	// Write the artifact to the branch via WriteContext.
	// Propagate run/trace metadata so commit trailers are correct.
	branchCtx := artifact.WithWriteContext(ctx, artifact.WriteContext{Branch: branchName})
	branchCtx = observe.WithTraceID(branchCtx, traceID)
	branchCtx = observe.WithRunID(branchCtx, runID)
	if _, err := o.artifactWriter.Create(branchCtx, artifactPath, artifactContent); err != nil {
		// Branch cleanup on failure (local + remote).
		if delErr := o.git.DeleteBranch(ctx, branchName); delErr != nil {
			log.Warn("failed to clean up planning branch", "branch", branchName, "error", delErr)
		}
		if autoPushEnabled() {
			if delErr := o.git.DeleteRemoteBranch(ctx, "origin", branchName); delErr != nil {
				log.Warn("auto-push: failed to clean up remote planning branch", "branch", branchName, "error", delErr)
			}
		}
		return nil, fmt.Errorf("create artifact on branch: %w", err)
	}

	// Create entry step execution.
	entryStep := &domain.StepExecution{
		ExecutionID: fmt.Sprintf("%s-%s-1", runID, wfDef.EntryStep),
		RunID:       runID,
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

	if err := o.store.UpdateRunStatus(ctx, runID, result.ToStatus); err != nil {
		return nil, fmt.Errorf("update run status: %w", err)
	}
	run.Status = result.ToStatus
	startedAt := now
	run.StartedAt = &startedAt

	if err := o.events.Emit(ctx, domain.Event{
		EventID:   fmt.Sprintf("evt-%s-started", traceID[:12]),
		Type:      domain.EventRunStarted,
		Timestamp: now,
		RunID:     runID,
		TraceID:   traceID,
	}); err != nil {
		log.Warn("failed to emit event", "event_type", domain.EventRunStarted, "error", err)
	}

	log.Info("planning run started",
		"run_id", runID,
		"artifact_path", artifactPath,
		"workflow_id", wfDef.ID,
		"entry_step", wfDef.EntryStep,
	)

	if err := o.ActivateStep(ctx, runID, wfDef.EntryStep); err != nil {
		log.Warn("entry step activation failed", "step_id", wfDef.EntryStep, "error", err)
	}

	return &StartRunResult{Run: run, EntryStep: entryStep}, nil
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
		if err := o.events.Emit(ctx, domain.Event{
			EventID:   fmt.Sprintf("evt-%s-completed", run.TraceID[:12]),
			Type:      domain.EventRunCompleted,
			Timestamp: time.Now(),
			RunID:     runID,
			TraceID:   run.TraceID,
		}); err != nil {
			log.Warn("failed to emit event", "event_type", domain.EventRunCompleted, "error", err)
		}
		log.Info("run completed", "run_id", runID)
		if run.StartedAt != nil {
			observe.GlobalMetrics.RunDuration.ObserveDuration(time.Since(*run.StartedAt))
		}
		// Clean up the run branch after successful completion.
		_ = o.CleanupRunBranch(ctx, runID)
	} else {
		log.Info("run entering commit phase", "run_id", runID)
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

	log := observe.Logger(ctx)
	payload, _ := json.Marshal(map[string]string{"reason": reason})
	if err := o.events.Emit(ctx, domain.Event{
		EventID:   fmt.Sprintf("evt-%s-failed", run.TraceID[:12]),
		Type:      domain.EventRunFailed,
		Timestamp: time.Now(),
		RunID:     runID,
		TraceID:   run.TraceID,
		Payload:   payload,
	}); err != nil {
		log.Warn("failed to emit event", "event_type", domain.EventRunFailed, "error", err)
	}

	log.Info("run failed", "run_id", runID, "reason", reason)
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

	log := observe.Logger(ctx)
	if err := o.events.Emit(ctx, domain.Event{
		EventID:   fmt.Sprintf("evt-%s-cancelled", run.TraceID[:12]),
		Type:      domain.EventRunCancelled,
		Timestamp: time.Now(),
		RunID:     runID,
		TraceID:   run.TraceID,
	}); err != nil {
		log.Warn("failed to emit event", "event_type", domain.EventRunCancelled, "error", err)
	}

	log.Info("run cancelled", "run_id", runID)
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

	log := observe.Logger(ctx)
	if err := o.events.Emit(ctx, domain.Event{
		EventID:   fmt.Sprintf("evt-%s-paused-%d", run.TraceID[:12], time.Now().UnixMilli()),
		Type:      domain.EventRunPaused,
		Timestamp: time.Now(),
		RunID:     runID,
		TraceID:   run.TraceID,
	}); err != nil {
		log.Warn("failed to emit event", "event_type", domain.EventRunPaused, "error", err)
	}

	log.Info("run paused", "run_id", runID, "reason", reason)
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

	log := observe.Logger(ctx)
	if err := o.events.Emit(ctx, domain.Event{
		EventID:   fmt.Sprintf("evt-%s-resumed-%d", run.TraceID[:12], time.Now().UnixMilli()),
		Type:      domain.EventRunResumed,
		Timestamp: time.Now(),
		RunID:     runID,
		TraceID:   run.TraceID,
	}); err != nil {
		log.Warn("failed to emit event", "event_type", domain.EventRunResumed, "error", err)
	}

	log.Info("run resumed", "run_id", runID)
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
