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
	ID   string `json:"id"`
	Body string `json:"body"`
}

type workflowUpdateRequest struct {
	Body string `json:"body"`
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

	svc := s.workflowsFrom(r.Context())
	if svc == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "workflow service not configured"))
		return
	}

	result, err := svc.Create(r.Context(), req.ID, req.Body)
	if err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusCreated, workflowWriteResponse(result, r.Context()))
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

	result, err := svc.Update(r.Context(), id, req.Body)
	if err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusOK, workflowWriteResponse(result, r.Context()))
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

func workflowWriteResponse(result *workflow.WriteResult, ctx context.Context) map[string]any {
	return map[string]any{
		"id":            result.Workflow.ID,
		"workflow_path": result.Path,
		"version":       result.Workflow.Version,
		"commit_sha":    result.CommitSHA,
		"trace_id":      observe.TraceID(ctx),
	}
}

