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

type stepSubmitOutput struct {
	ArtifactsProduced []struct {
		Path         string `json:"path"`
		ArtifactType string `json:"artifact_type,omitempty"`
		Status       string `json:"status,omitempty"`
	} `json:"artifacts_produced,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
	Summary string         `json:"summary,omitempty"`
}

type stepSubmitRequest struct {
	OutcomeID        string            `json:"outcome_id"`
	Output           *stepSubmitOutput `json:"output,omitempty"`
	Rationale        string            `json:"rationale,omitempty"`
	ValidationStatus string            `json:"validation_status,omitempty"`
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
	resolved, err := s.resolveWorkflowBinding(r.Context(), req.TaskPath)
	if err != nil {
		WriteError(w, err)
		return
	}

	run := &domain.Run{
		RunID:                runID,
		TaskPath:             req.TaskPath,
		WorkflowPath:         resolved.WorkflowPath,
		WorkflowID:           resolved.WorkflowID,
		WorkflowVersion:      resolved.CommitSHA,
		WorkflowVersionLabel: resolved.VersionLabel,
		Status:               domain.RunStatusPending,
		CurrentStepID:        resolved.EntryStep,
		TraceID:              traceID,
		CreatedAt:            now,
	}

	// Set run-level timeout if configured on the workflow.
	if resolved.Timeout != "" {
		if d, err := time.ParseDuration(resolved.Timeout); err == nil {
			t := now.Add(d)
			run.TimeoutAt = &t
		}
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
			ExecutionID: fmt.Sprintf("%s-%s-1", runID, resolved.EntryStep),
			RunID:       runID,
			StepID:      resolved.EntryStep,
			Status:      domain.StepStatusWaiting,
			Attempt:     1,
			CreatedAt:   now,
		})
	}); err != nil {
		WriteError(w, err)
		return
	}

	resp := map[string]any{
		"run_id":      runID,
		"task_path":   req.TaskPath,
		"workflow_id": resolved.WorkflowID,
		"status":      domain.RunStatusActive,
		"trace_id":    traceID,
	}
	if resolved.VersionLabel != "" {
		resp["workflow_version"] = resolved.VersionLabel
	} else if resolved.CommitSHA != "" {
		resp["workflow_version"] = resolved.CommitSHA
	}
	WriteJSON(w, http.StatusCreated, resp)
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

	resp := map[string]any{
		"run_id":          run.RunID,
		"task_path":       run.TaskPath,
		"workflow_id":     run.WorkflowID,
		"status":          run.Status,
		"current_step_id": run.CurrentStepID,
		"trace_id":        run.TraceID,
		"step_executions": steps,
	}
	if run.StartedAt != nil {
		resp["started_at"] = run.StartedAt
	}
	if run.CompletedAt != nil {
		resp["completed_at"] = run.CompletedAt
	}
	WriteJSON(w, http.StatusOK, resp)
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

	executionID := chi.URLParam(r, "assignment_id")

	// When engine result handler is configured, delegate to the full
	// ingestion pipeline with required_outputs validation and orchestrator routing.
	if s.resultHandler != nil {
		var artifactPaths []string
		if req.Output != nil {
			for _, a := range req.Output.ArtifactsProduced {
				artifactPaths = append(artifactPaths, a.Path)
			}
		}
		resp, err := s.resultHandler.IngestResult(r.Context(), ResultSubmission{
			ExecutionID:       executionID,
			OutcomeID:         req.OutcomeID,
			ArtifactsProduced: artifactPaths,
		})
		if err != nil {
			WriteError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, resp)
		return
	}

	// Fallback: legacy inline handling when no engine is configured.
	if s.store == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "store not configured"))
		return
	}

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

	runAdvanced := nextStepID != "end" && nextStepID != exec.StepID
	requiresReview := false
	if runAdvanced {
		requiresReview = s.isReviewStep(r.Context(), exec.RunID, nextStepID)
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"execution_id":    exec.ExecutionID,
		"step_id":         exec.StepID,
		"outcome_id":      exec.OutcomeID,
		"next_step":       nextStepID,
		"run_advanced":    runAdvanced,
		"requires_review": requiresReview,
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
		"assignment_id": exec.ExecutionID,
		"run_id":        runID,
		"step_id":       exec.StepID,
		"actor_id":      exec.ActorID,
		"status":        "active",
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

// isReviewStep checks if a step in the workflow is a review step.
func (s *Server) isReviewStep(ctx context.Context, runID, stepID string) bool {
	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		return false
	}
	proj, err := s.store.GetWorkflowProjection(ctx, run.WorkflowPath)
	if err != nil {
		return false
	}
	var wfDef domain.WorkflowDefinition
	if err := json.Unmarshal(proj.Definition, &wfDef); err != nil {
		return false
	}
	for i := range wfDef.Steps {
		if wfDef.Steps[i].ID == stepID {
			return wfDef.Steps[i].Type == domain.StepTypeReview
		}
	}
	return false
}

// resolveWorkflow looks up the workflow for a task and returns the entry step, path, and ID.
// When a WorkflowResolver is configured, it uses ResolveBinding to find the correct
// workflow based on artifact type. Falls back to defaults if no resolver is available.
func (s *Server) resolveWorkflowBinding(ctx context.Context, taskPath string) (*ResolvedWorkflow, error) {
	if s.workflowResolver == nil || s.artifacts == nil {
		// No resolver configured — return defaults for backwards compatibility.
		return &ResolvedWorkflow{EntryStep: "start"}, nil
	}

	// Read the task to determine its type.
	art, err := s.artifacts.Read(ctx, taskPath, "HEAD")
	if err != nil {
		return nil, fmt.Errorf("read task for workflow binding: %w", err)
	}

	resolved, err := s.workflowResolver(ctx, string(art.Type), "")
	if err != nil {
		return nil, err
	}

	return resolved, nil
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
