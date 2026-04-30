package gateway

import (
	"net/http"
	"strings"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/engine"
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
	ActorID          string   `json:"actor_id"`
	EligibleActorIDs []string `json:"eligible_actor_ids,omitempty"`
}

func (s *Server) handleRunStart(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "run.start") {
		return
	}

	req, ok := decodeBody[runStartRequest](w, r)
	if !ok {
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
		prs := s.planningRunStarterFrom(r.Context())
		if prs == nil {
			WriteError(w, domain.NewError(domain.ErrUnavailable, "planning run starter not configured"))
			return
		}
		result, err := prs.StartPlanningRun(r.Context(), req.TaskPath, req.ArtifactContent)
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

	rs := s.runStarterFrom(r.Context())
	if rs == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "run starter not configured"))
		return
	}

	result, err := rs.StartRun(r.Context(), req.TaskPath)
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
	if result.BranchName != "" {
		resp["branch_name"] = result.BranchName
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

	if _, ok := s.needStore(w, r); !ok {
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

	// Per-repo merge outcomes are required by EPIC-005 TASK-003 so
	// that a run in `partially-merged` exposes which repos succeeded
	// and which still need resolution. Returned as an empty slice (not
	// null) when the run produced no outcomes, so consumers do not
	// have to distinguish "no rows yet" from "field missing".
	outcomes, err := s.storeFrom(r.Context()).ListRepositoryMergeOutcomes(r.Context(), runID)
	if err != nil {
		WriteError(w, err)
		return
	}
	if outcomes == nil {
		outcomes = []domain.RepositoryMergeOutcome{}
	}

	resp := map[string]any{
		"run_id":          run.RunID,
		"task_path":       run.TaskPath,
		"workflow_id":     run.WorkflowID,
		"status":          run.Status,
		"current_step_id": run.CurrentStepID,
		"trace_id":        run.TraceID,
		"step_executions": steps,
		"merge_outcomes":  outcomes,
	}
	if run.BranchName != "" {
		resp["branch_name"] = run.BranchName
	}
	if run.StartedAt != nil {
		resp["started_at"] = run.StartedAt
	}
	if run.CompletedAt != nil {
		resp["completed_at"] = run.CompletedAt
	}
	if len(run.AffectedRepositories) > 0 {
		resp["affected_repositories"] = run.AffectedRepositories
	}
	WriteJSON(w, http.StatusOK, resp)
}

func (s *Server) handleRunCancel(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "run.cancel") {
		return
	}

	runID := chi.URLParam(r, "run_id")

	// Route through the workspace-scoped orchestrator so that events are
	// emitted and run branches are cleaned up.
	canceller := s.runCancellerFrom(r.Context())
	if canceller != nil {
		if err := canceller.CancelRun(r.Context(), runID); err != nil {
			WriteError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{
			"run_id": runID,
			"status": domain.RunStatusCancelled,
		})
		return
	}

	// Fallback: direct store update when orchestrator is not wired.
	if _, ok := s.needStore(w, r); !ok {
		return
	}

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

// handleRunRepositoryResolve marks a failed per-repo merge outcome as
// resolved-externally (EPIC-005 TASK-006). Body: {"reason": "..."}.
// The engine verifies the outcome is in failed state and records the
// authenticated actor + reason on the row plus an operational event.
func (s *Server) handleRunRepositoryResolve(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "run.merge.resolve") {
		return
	}

	resolver := s.runMergeResolverFrom(r.Context())
	if resolver == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "run merge resolver not configured"))
		return
	}

	req, ok := decodeBody[runMergeActionRequest](w, r)
	if !ok {
		return
	}
	if strings.TrimSpace(req.Reason) == "" {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "reason is required"))
		return
	}

	runID := chi.URLParam(r, "run_id")
	repoID := chi.URLParam(r, "repository_id")

	result, err := resolver.ResolveRepositoryMergeExternally(r.Context(), runID, repoID, req.Reason, req.TargetCommitSHA)
	if err != nil {
		WriteError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, runMergeActionResponse(runID, repoID,
		domain.RepositoryMergeStatusResolvedExternally, result))
}

// handleRunRepositoryRetry resets a failed per-repo merge outcome to
// pending so the next scheduler tick re-attempts it (EPIC-005 TASK-006).
// Body: {"reason": "..."}.
func (s *Server) handleRunRepositoryRetry(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "run.merge.retry") {
		return
	}

	resolver := s.runMergeResolverFrom(r.Context())
	if resolver == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "run merge resolver not configured"))
		return
	}

	req, ok := decodeBody[runMergeActionRequest](w, r)
	if !ok {
		return
	}
	if strings.TrimSpace(req.Reason) == "" {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "reason is required"))
		return
	}

	runID := chi.URLParam(r, "run_id")
	repoID := chi.URLParam(r, "repository_id")

	result, err := resolver.RetryRepositoryMerge(r.Context(), runID, repoID, req.Reason)
	if err != nil {
		WriteError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, runMergeActionResponse(runID, repoID,
		domain.RepositoryMergeStatusPending, result))
}

// runMergeActionResponse builds the JSON response for resolve / retry.
// blocking_repositories is always serialized (even when empty) so
// clients have a stable shape; ready_to_resume is the boolean clients
// actually branch on.
func runMergeActionResponse(runID, repoID string, status domain.RepositoryMergeStatus, result *engine.MergeRecoveryResult) map[string]any {
	resp := map[string]any{
		"run_id":        runID,
		"repository_id": repoID,
		"status":        status,
	}
	if result != nil {
		resp["ledger_commit_sha"] = result.LedgerCommitSHA
		resp["ready_to_resume"] = result.ReadyToResume
		blocking := result.BlockingRepositories
		if blocking == nil {
			blocking = []string{}
		}
		resp["blocking_repositories"] = blocking
	}
	return resp
}

// runMergeActionRequest is the JSON body for the resolve and retry
// per-repo merge endpoints. Both share the same shape — only the
// authentication-derived actor and the chosen route differ — so they
// share a request struct. TargetCommitSHA is only meaningful on the
// resolve path; the retry handler ignores it because the merge has
// not happened yet.
type runMergeActionRequest struct {
	Reason          string `json:"reason"`
	TargetCommitSHA string `json:"target_commit_sha,omitempty"`
}

func (s *Server) handleStepSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "step.submit") {
		return
	}

	req, ok := decodeBody[stepSubmitRequest](w, r)
	if !ok {
		return
	}

	executionID := chi.URLParam(r, "assignment_id")

	resultHandler := s.resultHandlerFrom(r.Context())
	if resultHandler == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "result handler not configured"))
		return
	}

	// Gateway-level ownership check: the authenticated actor must be
	// the one the step was claimed by. The downstream IngestResult
	// check remains as defense-in-depth. If the store isn't available
	// we fall through to IngestResult rather than failing closed, so
	// deployments without a fully wired store (e.g., some tests) still
	// exercise the existing code path; actor == nil only in dev mode.
	if actor := actorFromContext(r.Context()); actor != nil {
		if st := s.storeFrom(r.Context()); st != nil {
			exec, gerr := st.GetStepExecution(r.Context(), executionID)
			if gerr != nil {
				WriteError(w, gerr)
				return
			}
			if exec.ActorID != "" && exec.ActorID != actor.ActorID {
				WriteError(w, domain.NewError(domain.ErrForbidden, "step is not assigned to the authenticated actor"))
				return
			}
		}
	}

	var artifactPaths []string
	if req.Output != nil {
		for _, a := range req.Output.ArtifactsProduced {
			artifactPaths = append(artifactPaths, a.Path)
		}
	}
	resp, err := resultHandler.IngestResult(r.Context(), ResultSubmission{
		ExecutionID:       executionID,
		OutcomeID:         req.OutcomeID,
		ArtifactsProduced: artifactPaths,
	})
	if err != nil {
		WriteError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, resp)
}

func (s *Server) handleStepAssign(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "step.assign") {
		return
	}

	req, ok := decodeBody[stepAssignRequest](w, r)
	if !ok {
		return
	}
	if req.ActorID == "" {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "actor_id required"))
		return
	}

	assigner := s.assignerFor(r.Context())
	if assigner == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "step assigner not configured"))
		return
	}

	runID := chi.URLParam(r, "run_id")
	stepID := chi.URLParam(r, "step_id")

	// Precondition evaluation stays in the gateway: validators resolve
	// against task-path context that sits above the engine's state-machine
	// concerns. The assigner owns only the transition + exec update.
	if v := s.validatorFrom(r.Context()); v != nil {
		stepDef, run := assigner.LookupStepDef(r.Context(), runID, stepID)
		if stepDef != nil {
			taskPath := ""
			if run != nil {
				taskPath = run.TaskPath
			}
			precondResult := validation.EvaluatePreconditions(r.Context(), v, *stepDef, taskPath)
			if precondResult.Status == "failed" {
				WriteError(w, domain.NewErrorWithDetail(domain.ErrPrecondition,
					"step precondition failed", precondResult))
				return
			}
		}
	}

	result, err := assigner.AssignStep(r.Context(), engine.AssignRequest{
		RunID:            runID,
		StepID:           stepID,
		ActorID:          req.ActorID,
		EligibleActorIDs: req.EligibleActorIDs,
	})
	if err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"assignment_id": result.Exec.ExecutionID,
		"run_id":        runID,
		"step_id":       result.Exec.StepID,
		"actor_id":      result.Exec.ActorID,
		"status":        "active",
	})
}
