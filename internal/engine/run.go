package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

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

	// Create run in pending status.
	run := &domain.Run{
		RunID:                runID,
		TaskPath:             taskPath,
		WorkflowPath:         wfDef.Path,
		WorkflowID:           wfDef.ID,
		WorkflowVersion:      binding.CommitSHA,
		WorkflowVersionLabel: binding.VersionLabel,
		Status:               domain.RunStatusPending,
		CurrentStepID:        wfDef.EntryStep,
		TraceID:              traceID,
		CreatedAt:            now,
	}

	if err := o.store.CreateRun(ctx, run); err != nil {
		return nil, fmt.Errorf("create run: %w", err)
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
