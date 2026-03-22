package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/store"
	"github.com/bszymi/spine/internal/validation"
	"github.com/bszymi/spine/internal/workflow"
	"github.com/go-chi/chi/v5"
)

type runStartRequest struct {
	TaskPath string `json:"task_path"`
}

type stepSubmitRequest struct {
	OutcomeID string `json:"outcome_id"`
}

type stepAssignRequest struct {
	ActorID string `json:"actor_id"`
}

func (s *Server) handleRunStart(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "run.start") {
		return
	}

	var req runStartRequest
	if err := decodeJSON(r, &req); err != nil {
		WriteError(w, err)
		return
	}
	if req.TaskPath == "" {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "task_path required"))
		return
	}

	if s.store == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "store not configured"))
		return
	}

	traceID := observe.TraceID(r.Context())
	now := time.Now()
	runID := fmt.Sprintf("run-%s", traceID[:8])

	// Look up workflow to get entry step and workflow path
	entryStepID, workflowPath, workflowID := s.resolveWorkflow(r.Context(), req.TaskPath)

	run := &domain.Run{
		RunID:         runID,
		TaskPath:      req.TaskPath,
		WorkflowPath:  workflowPath,
		WorkflowID:    workflowID,
		Status:        domain.RunStatusPending,
		CurrentStepID: entryStepID,
		TraceID:       traceID,
		CreatedAt:     now,
	}

	// Create run, activate, and create entry step in a transaction
	if err := s.store.WithTx(r.Context(), func(tx store.Tx) error {
		if err := tx.CreateRun(r.Context(), run); err != nil {
			return err
		}
		// Activate: pending → active
		result, err := workflow.EvaluateRunTransition(run.Status, workflow.TransitionRequest{
			Trigger: workflow.TriggerActivate,
		})
		if err != nil {
			return err
		}
		if err := tx.UpdateRunStatus(r.Context(), runID, result.ToStatus); err != nil {
			return err
		}
		// Create entry step execution
		return tx.CreateStepExecution(r.Context(), &domain.StepExecution{
			ExecutionID: fmt.Sprintf("%s-%s-1", runID, entryStepID),
			RunID:       runID,
			StepID:      entryStepID,
			Status:      domain.StepStatusWaiting,
			Attempt:     1,
			CreatedAt:   now,
		})
	}); err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusCreated, map[string]any{
		"run_id":     runID,
		"task_path":  req.TaskPath,
		"status":     domain.RunStatusActive,
		"entry_step": entryStepID,
		"trace_id":   traceID,
	})
}

func (s *Server) handleRunStatus(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "run.status") {
		return
	}

	if s.store == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "store not configured"))
		return
	}

	runID := chi.URLParam(r, "run_id")
	run, err := s.store.GetRun(r.Context(), runID)
	if err != nil {
		WriteError(w, err)
		return
	}

	steps, err := s.store.ListStepExecutionsByRun(r.Context(), runID)
	if err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"run":   run,
		"steps": steps,
	})
}

func (s *Server) handleRunCancel(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "run.cancel") {
		return
	}

	if s.store == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "store not configured"))
		return
	}

	runID := chi.URLParam(r, "run_id")
	run, err := s.store.GetRun(r.Context(), runID)
	if err != nil {
		WriteError(w, err)
		return
	}

	result, err := workflow.EvaluateRunTransition(run.Status, workflow.TransitionRequest{
		Trigger: workflow.TriggerCancel,
	})
	if err != nil {
		WriteError(w, err)
		return
	}

	if err := s.store.UpdateRunStatus(r.Context(), runID, result.ToStatus); err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"run_id": runID,
		"status": result.ToStatus,
	})
}

func (s *Server) handleStepSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "step.submit") {
		return
	}

	var req stepSubmitRequest
	if err := decodeJSON(r, &req); err != nil {
		WriteError(w, err)
		return
	}

	if s.store == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "store not configured"))
		return
	}

	executionID := chi.URLParam(r, "assignment_id")
	exec, err := s.store.GetStepExecution(r.Context(), executionID)
	if err != nil {
		WriteError(w, err)
		return
	}

	// If step is assigned, first transition to in_progress (auto-acknowledge)
	if exec.Status == domain.StepStatusAssigned {
		ackResult, err := workflow.EvaluateStepTransition(exec.Status, workflow.StepTransitionRequest{
			Trigger: workflow.StepTriggerAcknowledged,
		})
		if err != nil {
			WriteError(w, err)
			return
		}
		now := time.Now()
		exec.Status = ackResult.ToStatus
		exec.StartedAt = &now
		if err := s.store.UpdateStepExecution(r.Context(), exec); err != nil {
			WriteError(w, err)
			return
		}
	}

	result, err := workflow.EvaluateStepTransition(exec.Status, workflow.StepTransitionRequest{
		Trigger:   workflow.StepTriggerSubmit,
		OutcomeID: req.OutcomeID,
	})
	if err != nil {
		WriteError(w, err)
		return
	}

	now := time.Now()
	exec.Status = result.ToStatus
	exec.OutcomeID = req.OutcomeID
	exec.CompletedAt = &now
	if err := s.store.UpdateStepExecution(r.Context(), exec); err != nil {
		WriteError(w, err)
		return
	}

	// Determine next step from workflow definition
	nextStepID := "end"
	if exec.RunID != "" {
		nextStepID = s.resolveNextStep(r.Context(), exec)
	}

	// Advance the run and create next step if needed
	run, err := s.store.GetRun(r.Context(), exec.RunID)
	if err == nil {
		runResult, runErr := workflow.EvaluateRunTransition(run.Status, workflow.TransitionRequest{
			Trigger:    workflow.TriggerStepCompleted,
			NextStepID: nextStepID,
		})
		if runErr == nil {
			_ = s.store.UpdateRunStatus(r.Context(), run.RunID, runResult.ToStatus)
			// Create next step execution if run stays active
			if runResult.ToStatus == domain.RunStatusActive && nextStepID != "end" {
				_ = s.store.CreateStepExecution(r.Context(), &domain.StepExecution{
					ExecutionID: fmt.Sprintf("%s-%s-1", run.RunID, nextStepID),
					RunID:       run.RunID,
					StepID:      nextStepID,
					Status:      domain.StepStatusWaiting,
					Attempt:     1,
					CreatedAt:   now,
				})
			}
		}
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"execution_id": exec.ExecutionID,
		"step_id":      exec.StepID,
		"status":       exec.Status,
		"outcome_id":   exec.OutcomeID,
		"next_step":    nextStepID,
	})
}

func (s *Server) handleStepAssign(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "step.assign") {
		return
	}

	var req stepAssignRequest
	if err := decodeJSON(r, &req); err != nil {
		WriteError(w, err)
		return
	}
	if req.ActorID == "" {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "actor_id required"))
		return
	}

	if s.store == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "store not configured"))
		return
	}

	runID := chi.URLParam(r, "run_id")
	stepID := chi.URLParam(r, "step_id")

	// Find the step execution for this run/step
	execs, err := s.store.ListStepExecutionsByRun(r.Context(), runID)
	if err != nil {
		WriteError(w, err)
		return
	}

	var exec *domain.StepExecution
	for i := range execs {
		if execs[i].StepID == stepID {
			exec = &execs[i]
		}
	}
	if exec == nil {
		WriteError(w, domain.NewError(domain.ErrNotFound, "step execution not found"))
		return
	}

	// Evaluate preconditions if validation engine is available
	if s.validator != nil {
		stepDef := s.resolveStepDef(r.Context(), exec)
		if stepDef != nil {
			run, _ := s.store.GetRun(r.Context(), runID)
			taskPath := ""
			if run != nil {
				taskPath = run.TaskPath
			}
			precondResult := validation.EvaluatePreconditions(r.Context(), s.validator, *stepDef, taskPath)
			if precondResult.Status == "failed" {
				WriteError(w, domain.NewErrorWithDetail(domain.ErrPrecondition,
					"step precondition failed", precondResult))
				return
			}
		}
	}

	result, err := workflow.EvaluateStepTransition(exec.Status, workflow.StepTransitionRequest{
		Trigger: workflow.StepTriggerAssign,
	})
	if err != nil {
		WriteError(w, err)
		return
	}

	exec.Status = result.ToStatus
	exec.ActorID = req.ActorID
	if err := s.store.UpdateStepExecution(r.Context(), exec); err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"execution_id": exec.ExecutionID,
		"step_id":      exec.StepID,
		"actor_id":     exec.ActorID,
		"status":       exec.Status,
	})
}

// resolveNextStep looks up the workflow definition to find the next step
// after the given outcome.
func (s *Server) resolveNextStep(ctx context.Context, exec *domain.StepExecution) string {
	run, err := s.store.GetRun(ctx, exec.RunID)
	if err != nil {
		return "end"
	}

	proj, err := s.store.GetWorkflowProjection(ctx, run.WorkflowPath)
	if err != nil {
		return "end"
	}

	var wfDef domain.WorkflowDefinition
	if err := json.Unmarshal(proj.Definition, &wfDef); err != nil {
		return "end"
	}

	for i := range wfDef.Steps {
		if wfDef.Steps[i].ID == exec.StepID {
			for _, outcome := range wfDef.Steps[i].Outcomes {
				if outcome.ID == exec.OutcomeID {
					if outcome.NextStep == "" {
						return "end"
					}
					return outcome.NextStep
				}
			}
		}
	}
	return "end"
}

// resolveWorkflow looks up the workflow for a task and returns the entry step, path, and ID.
// Falls back to defaults if no workflow can be resolved.
func (s *Server) resolveWorkflow(ctx context.Context, taskPath string) (entryStep, workflowPath, workflowID string) {
	entryStep = "start"
	if s.store == nil {
		return
	}

	// Try to find an active workflow that applies to tasks
	result, err := s.store.QueryArtifacts(ctx, store.ArtifactQuery{Limit: 1})
	if err != nil {
		return
	}
	_ = result // workflow binding resolution is simplified for v0.x

	// For now, return defaults — full workflow binding via ResolveBinding
	// requires WorkflowProvider which queries workflow projections
	return
}

// resolveStepDef loads the StepDefinition for a step execution from the workflow.
func (s *Server) resolveStepDef(ctx context.Context, exec *domain.StepExecution) *domain.StepDefinition {
	run, err := s.store.GetRun(ctx, exec.RunID)
	if err != nil || run.WorkflowPath == "" {
		return nil
	}

	proj, err := s.store.GetWorkflowProjection(ctx, run.WorkflowPath)
	if err != nil {
		return nil
	}

	var wfDef domain.WorkflowDefinition
	if err := json.Unmarshal(proj.Definition, &wfDef); err != nil {
		return nil
	}

	for i := range wfDef.Steps {
		if wfDef.Steps[i].ID == exec.StepID {
			return &wfDef.Steps[i]
		}
	}
	return nil
}
