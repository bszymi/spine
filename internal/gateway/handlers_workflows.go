package gateway

import (
	"context"
	"net/http"
	"strings"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/workflow"

	"github.com/go-chi/chi/v5"
)

type workflowCreateRequest struct {
	ID           string               `json:"id"`
	Body         string               `json:"body"`
	WriteContext *writeContextRequest `json:"write_context,omitempty"`
}

type workflowUpdateRequest struct {
	Body         string               `json:"body"`
	WriteContext *writeContextRequest `json:"write_context,omitempty"`
}

type workflowValidateRequest struct {
	Body string `json:"body"`
}

func (s *Server) handleWorkflowCreate(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "workflow.create") {
		return
	}

	var req workflowCreateRequest
	if err := decodeJSON(r, &req); err != nil {
		WriteError(w, err)
		return
	}
	if req.ID == "" || req.Body == "" {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "id and body required"))
		return
	}

	// If this 503 fires in production, a new service was added to
	// gateway.ServerConfig without being wired in cmd/spine buildServerConfig.
	// See cmd/spine/serve_smoke_test.go — that test probes every endpoint
	// and is the canary for this class of regression.
	svc := s.workflowsFrom(r.Context())
	if svc == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "workflow service not configured"))
		return
	}

	ctx := r.Context()
	// Dispatch per ADR-008:
	//   - write_context present → branch-scoped write via resolveWriteContext
	//   - no write_context + reviewer → planning run under workflow-lifecycle
	//   - no write_context + operator/admin → direct commit (bypass), tagged
	//     with a Workflow-Bypass trailer for audit
	if req.WriteContext == nil && !callerHasOperatorPrivileges(ctx) {
		starter := s.wfPlanningStarterFrom(ctx)
		if starter == nil {
			// No planning starter wired — refuse rather than silently
			// demoting the reviewer to a direct commit that would skip
			// both the lifecycle review AND the bypass trailer. Bootstrap
			// recovery is the operator-bypass path (ADR-008 §5).
			WriteError(w, domain.NewError(domain.ErrUnavailable,
				"workflow planning run starter not configured; reviewer writes require the governance flow"))
			return
		}
		planning, err := starter.StartWorkflowPlanningRun(ctx, req.ID, req.Body)
		if err != nil {
			WriteError(w, err)
			return
		}
		WriteJSON(w, http.StatusCreated, map[string]any{
			"run_id":                planning.RunID,
			"branch_name":           planning.BranchName,
			"workflow_id":           req.ID,
			"workflow_path":         planning.TaskPath,
			"governing_workflow_id": planning.WorkflowID,
			"status":                planning.Status,
			"mode":                  planning.Mode,
			"trace_id":              planning.TraceID,
			"write_mode":            "planning",
		})
		return
	}
	if req.WriteContext != nil {
		branch, err := s.resolveWriteContext(ctx, req.WriteContext)
		if err != nil {
			WriteError(w, err)
			return
		}
		if branch != "" {
			ctx = workflow.WithWriteContext(ctx, workflow.WriteContext{Branch: branch})
		}
	} else if callerHasOperatorPrivileges(ctx) {
		// Operator direct-commit path: tag the commit for audit.
		ctx = workflow.WithBypass(ctx)
	}

	result, err := svc.Create(ctx, req.ID, req.Body)
	if err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusCreated, workflowWriteResponse(result, r.Context(), req.WriteContext, workflow.IsBypass(ctx)))
}

// callerHasOperatorPrivileges returns true when the authenticated actor is at
// operator level or higher. Reviewers are governed through the
// workflow-lifecycle planning flow; operators (and above) retain a
// direct-commit path for recovery (ADR-008). Uses HasAtLeast so future roles
// inserted between reviewer and operator do not silently drop into the wrong
// dispatch branch.
func callerHasOperatorPrivileges(ctx context.Context) bool {
	actor := actorFromContext(ctx)
	if actor == nil {
		return false
	}
	return actor.Role.HasAtLeast(domain.RoleOperator)
}

func (s *Server) handleWorkflowUpdate(w http.ResponseWriter, r *http.Request, id string) {
	if !s.authorize(w, r, "workflow.update") {
		return
	}

	var req workflowUpdateRequest
	if err := decodeJSON(r, &req); err != nil {
		WriteError(w, err)
		return
	}
	if req.Body == "" {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "body required"))
		return
	}

	svc := s.workflowsFrom(r.Context())
	if svc == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "workflow service not configured"))
		return
	}

	ctx := r.Context()
	if req.WriteContext != nil {
		branch, err := s.resolveWriteContext(ctx, req.WriteContext)
		if err != nil {
			WriteError(w, err)
			return
		}
		if branch != "" {
			ctx = workflow.WithWriteContext(ctx, workflow.WriteContext{Branch: branch})
		}
	} else if callerHasOperatorPrivileges(ctx) {
		// Operator direct-commit path: tag the commit for audit (ADR-008).
		ctx = workflow.WithBypass(ctx)
	}

	result, err := svc.Update(ctx, id, req.Body)
	if err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusOK, workflowWriteResponse(result, r.Context(), req.WriteContext, workflow.IsBypass(ctx)))
}

func (s *Server) handleWorkflowRead(w http.ResponseWriter, r *http.Request, id string) {
	if !s.authorize(w, r, "workflow.read") {
		return
	}

	ref := r.URL.Query().Get("ref")
	if err := git.ValidateRef(ref); err != nil {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, err.Error()))
		return
	}

	svc := s.workflowsFrom(r.Context())
	if svc == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "workflow service not configured"))
		return
	}

	res, err := svc.Read(r.Context(), id, ref)
	if err != nil {
		WriteError(w, err)
		return
	}

	wf := res.Workflow
	WriteJSON(w, http.StatusOK, map[string]any{
		"id":            wf.ID,
		"workflow_path": res.Path,
		"name":          wf.Name,
		"version":       wf.Version,
		"status":        wf.Status,
		"mode":          wf.Mode,
		"applies_to":    wf.AppliesTo,
		"description":   wf.Description,
		"body":          res.Body,
		"source_commit": res.SourceCommit,
	})
}

func (s *Server) handleWorkflowList(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "workflow.list") {
		return
	}

	svc := s.workflowsFrom(r.Context())
	if svc == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "workflow service not configured"))
		return
	}

	opts := workflow.ListOptions{
		AppliesTo: r.URL.Query().Get("applies_to"),
		Status:    r.URL.Query().Get("status"),
		Mode:      r.URL.Query().Get("mode"),
	}
	items, err := svc.List(r.Context(), opts)
	if err != nil {
		WriteError(w, err)
		return
	}

	summaries := make([]map[string]any, 0, len(items))
	for _, wf := range items {
		summaries = append(summaries, map[string]any{
			"id":            wf.ID,
			"workflow_path": wf.Path,
			"name":          wf.Name,
			"version":       wf.Version,
			"status":        wf.Status,
			"mode":          wf.Mode,
			"applies_to":    wf.AppliesTo,
			"description":   wf.Description,
		})
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"items":    summaries,
		"has_more": false,
	})
}

func (s *Server) handleWorkflowValidate(w http.ResponseWriter, r *http.Request, id string) {
	if !s.authorize(w, r, "workflow.validate") {
		return
	}

	var req workflowValidateRequest
	if err := decodeJSON(r, &req); err != nil {
		WriteError(w, err)
		return
	}
	if req.Body == "" {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "body required"))
		return
	}

	svc := s.workflowsFrom(r.Context())
	if svc == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "workflow service not configured"))
		return
	}

	result := svc.ValidateBody(r.Context(), id, req.Body)
	WriteJSON(w, http.StatusOK, map[string]any{
		"workflow_id": id,
		"status":      result.Status,
		"errors":      result.Errors,
		"warnings":    result.Warnings,
	})
}

// handleWorkflowWildcard dispatches GET/PUT /workflows/{id} and POST
// /workflows/{id}/validate. Workflow IDs are simple slugs with no slashes, but
// we still register via wildcard to keep the routing shape consistent with
// the artifact surface.
func (s *Server) handleWorkflowWildcard(w http.ResponseWriter, r *http.Request) {
	raw := chi.URLParam(r, "*")
	id, suffix := splitWorkflowSuffix(raw)

	switch {
	case suffix == "/validate" && r.Method == http.MethodPost:
		s.handleWorkflowValidate(w, r, id)
	case suffix == "" && r.Method == http.MethodGet:
		s.handleWorkflowRead(w, r, id)
	case suffix == "" && r.Method == http.MethodPut:
		s.handleWorkflowUpdate(w, r, id)
	default:
		WriteError(w, domain.NewError(domain.ErrNotFound, "not found"))
	}
}

func splitWorkflowSuffix(raw string) (string, string) {
	for _, suffix := range []string{"/validate"} {
		if strings.HasSuffix(raw, suffix) {
			return strings.TrimSuffix(raw, suffix), suffix
		}
	}
	return raw, ""
}

func workflowWriteResponse(result *workflow.WriteResult, ctx context.Context, wc *writeContextRequest, bypass bool) map[string]any {
	writeMode := "authoritative"
	switch {
	case wc != nil && wc.RunID != "":
		writeMode = "proposed"
	case bypass:
		writeMode = "bypass"
	}
	resp := map[string]any{
		"id":            result.Workflow.ID,
		"workflow_path": result.Path,
		"version":       result.Workflow.Version,
		"commit_sha":    result.CommitSHA,
		"write_mode":    writeMode,
		"trace_id":      observe.TraceID(ctx),
	}
	if bypass {
		resp["workflow_bypass"] = true
	}
	return resp
}
