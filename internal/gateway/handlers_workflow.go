package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/validation"
	"github.com/bszymi/spine/internal/workflow"
	"github.com/go-chi/chi/v5"
)

type runStartRequest struct {
	TaskPath        string `json:"task_path"`
	Mode            string `json:"mode,omitempty"`
	ArtifactContent string `json:"artifact_content,omitempty"`
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
	if req.Mode != "" && req.Mode != "standard" && req.Mode != "planning" {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "mode must be 'standard' or 'planning'"))
		return
	}
	if req.Mode == "planning" && req.ArtifactContent == "" {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "artifact_content required when mode is 'planning'"))
		return
	}
	if req.Mode != "planning" && req.TaskPath == "" {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "task_path required"))
		return
	}

	// Route planning mode through the engine's StartPlanningRun.
	// NOTE: Planning runs require creation-mode workflow binding (EPIC-005/TASK-004).
	// Until that is implemented, planning requests are blocked to avoid creating
	// runs that bind to execution workflows and immediately stall.
	if req.Mode == "planning" {
		if s.planningRunStarter == nil {
			WriteError(w, domain.NewError(domain.ErrUnavailable, "planning run starter not configured"))
			return
		}
		// TODO(INIT-009): planningRunStarter is still a singleton — needs workspace-scoped orchestrator in ServiceSet.
		result, err := s.planningRunStarter.StartPlanningRun(r.Context(), req.TaskPath, req.ArtifactContent)
		if err != nil {
			WriteError(w, err)
			return
		}
		// Use the gateway's trace ID (from X-Trace-Id header/middleware) for response
		// consistency, since the engine generates its own internal trace ID.
		gatewayTraceID := observe.TraceID(r.Context())
		if gatewayTraceID == "" {
			gatewayTraceID = result.TraceID
		}
		resp := map[string]any{
			"run_id":      result.RunID,
			"task_path":   result.TaskPath,
			"workflow_id": result.WorkflowID,
			"status":      result.Status,
			"mode":        result.Mode,
			"trace_id":    gatewayTraceID,
		}
		if result.VersionLabel != "" {
			resp["workflow_version"] = result.VersionLabel
		} else if result.CommitSHA != "" {
			resp["workflow_version"] = result.CommitSHA
		}
		WriteJSON(w, http.StatusCreated, resp)
		return
	}

	if s.runStarter == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "run starter not configured"))
		return
	}

	// TODO(INIT-009): runStarter is still a singleton — needs workspace-scoped orchestrator in ServiceSet.
	result, err := s.runStarter.StartRun(r.Context(), req.TaskPath)
	if err != nil {
		WriteError(w, err)
		return
	}

	gatewayTraceID := observe.TraceID(r.Context())
	if gatewayTraceID == "" {
		gatewayTraceID = result.TraceID
	}
	resp := map[string]any{
		"run_id":      result.RunID,
		"task_path":   result.TaskPath,
		"workflow_id": result.WorkflowID,
		"status":      result.Status,
		"mode":        "standard",
		"trace_id":    gatewayTraceID,
	}
	if result.VersionLabel != "" {
		resp["workflow_version"] = result.VersionLabel
	} else if result.CommitSHA != "" {
		resp["workflow_version"] = result.CommitSHA
	}
	WriteJSON(w, http.StatusCreated, resp)
}

func (s *Server) handleRunStatus(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "run.status") {
		return
	}

	if s.storeFrom(r.Context()) == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "store not configured"))
		return
	}

	runID := chi.URLParam(r, "run_id")
	run, err := s.storeFrom(r.Context()).GetRun(r.Context(), runID)
	if err != nil {
		WriteError(w, err)
		return
	}

	steps, err := s.storeFrom(r.Context()).ListStepExecutionsByRun(r.Context(), runID)
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

	if s.storeFrom(r.Context()) == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "store not configured"))
		return
	}

	runID := chi.URLParam(r, "run_id")
	run, err := s.storeFrom(r.Context()).GetRun(r.Context(), runID)
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

	if err := s.storeFrom(r.Context()).UpdateRunStatus(r.Context(), runID, result.ToStatus); err != nil {
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
	if s.storeFrom(r.Context()) == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "store not configured"))
		return
	}

	exec, err := s.storeFrom(r.Context()).GetStepExecution(r.Context(), executionID)
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
		if err := s.storeFrom(r.Context()).UpdateStepExecution(r.Context(), exec); err != nil {
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
	if err := s.storeFrom(r.Context()).UpdateStepExecution(r.Context(), exec); err != nil {
		WriteError(w, err)
		return
	}

	// Determine next step from workflow definition
	nextStepID := "end"
	if exec.RunID != "" {
		nextStepID = s.resolveNextStep(r.Context(), exec)
	}

	// Advance the run and create next step if needed
	run, err := s.storeFrom(r.Context()).GetRun(r.Context(), exec.RunID)
	if err == nil {
		runResult, runErr := workflow.EvaluateRunTransition(run.Status, workflow.TransitionRequest{
			Trigger:    workflow.TriggerStepCompleted,
			NextStepID: nextStepID,
		})
		if runErr == nil {
			_ = s.storeFrom(r.Context()).UpdateRunStatus(r.Context(), run.RunID, runResult.ToStatus)
			if runResult.ToStatus == domain.RunStatusActive && nextStepID != "end" {
				_ = s.storeFrom(r.Context()).CreateStepExecution(r.Context(), &domain.StepExecution{
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

	if s.storeFrom(r.Context()) == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "store not configured"))
		return
	}

	runID := chi.URLParam(r, "run_id")
	stepID := chi.URLParam(r, "step_id")

	// Find the step execution for this run/step
	execs, err := s.storeFrom(r.Context()).ListStepExecutionsByRun(r.Context(), runID)
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
			run, _ := s.storeFrom(r.Context()).GetRun(r.Context(), runID)
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
	if err := s.storeFrom(r.Context()).UpdateStepExecution(r.Context(), exec); err != nil {
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
	run, err := s.storeFrom(ctx).GetRun(ctx, exec.RunID)
	if err != nil {
		return "end"
	}

	proj, err := s.storeFrom(ctx).GetWorkflowProjection(ctx, run.WorkflowPath)
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
	run, err := s.storeFrom(ctx).GetRun(ctx, runID)
	if err != nil {
		return false
	}
	proj, err := s.storeFrom(ctx).GetWorkflowProjection(ctx, run.WorkflowPath)
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
	if s.workflowResolver == nil || s.artifactsFrom(ctx) == nil {
		// No resolver configured — return defaults for backwards compatibility.
		return &ResolvedWorkflow{EntryStep: "start"}, nil
	}

	// Read the task to determine its type.
	art, err := s.artifactsFrom(ctx).Read(ctx, taskPath, "HEAD")
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
	run, err := s.storeFrom(ctx).GetRun(ctx, exec.RunID)
	if err != nil || run.WorkflowPath == "" {
		return nil
	}

	proj, err := s.storeFrom(ctx).GetWorkflowProjection(ctx, run.WorkflowPath)
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
