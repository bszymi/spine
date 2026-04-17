package gateway

import (
	"net/http"

	"github.com/bszymi/spine/internal/domain"
	"github.com/go-chi/chi/v5"
)

type createBranchRequest struct {
	BranchID  string `json:"branch_id"`
	StartStep string `json:"start_step"`
}

func (s *Server) handleCreateBranch(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "divergence.create_branch") {
		return
	}

	bc := s.branchCreatorFrom(r.Context())
	if s.storeFrom(r.Context()) == nil || bc == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "divergence not configured"))
		return
	}

	divergenceID := chi.URLParam(r, "divergence_id")

	req, ok := decodeBody[createBranchRequest](w, r)
	if !ok {
		return
	}
	if req.BranchID == "" || req.StartStep == "" {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "branch_id and start_step required"))
		return
	}

	divCtx, err := s.storeFrom(r.Context()).GetDivergenceContext(r.Context(), divergenceID)
	if err != nil {
		WriteError(w, err)
		return
	}

	branch, err := bc.CreateExploratoryBranch(r.Context(), divCtx, req.BranchID, req.StartStep)
	if err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusCreated, map[string]any{
		"branch_id":       branch.BranchID,
		"divergence_id":   divergenceID,
		"status":          branch.Status,
		"current_step_id": branch.CurrentStepID,
	})
}

func (s *Server) handleCloseWindow(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "divergence.close_window") {
		return
	}

	bc := s.branchCreatorFrom(r.Context())
	if s.storeFrom(r.Context()) == nil || bc == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "divergence not configured"))
		return
	}

	divergenceID := chi.URLParam(r, "divergence_id")

	divCtx, err := s.storeFrom(r.Context()).GetDivergenceContext(r.Context(), divergenceID)
	if err != nil {
		WriteError(w, err)
		return
	}

	if err := bc.CloseWindow(r.Context(), divCtx); err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"divergence_id": divergenceID,
		"window":        "closed",
	})
}
