package gateway

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/bszymi/spine/core/divergence"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/observe"
	"github.com/bszymi/spine/core/refadmit"
)

type createBranchRequest struct {
	BranchID  string `json:"branch_id"`
	StartStep string `json:"start_step"`
}

func (s *Server) handleCreateBranch(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "divergence.create_branch") {
		return
	}

	st, ok := s.needStore(w, r)
	if !ok {
		return
	}
	bc, ok := s.needBranchCreator(w, r)
	if !ok {
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
	// branch_id becomes a git ref component and a store key
	// (spine/<run>/<divergence>/<branch_id>), so it must match the
	// workflow ID grammar: 400 here, not a 500 from branch protection's
	// control-byte guard or from git (INIT-023 TASK-016 owner
	// question 6). Every caller, operators included.
	if err := divergence.ValidateExploratoryBranchID(req.BranchID); err != nil {
		WriteError(w, err)
		return
	}

	divCtx, err := st.GetDivergenceContext(r.Context(), divergenceID)
	if err != nil {
		WriteError(w, err)
		return
	}

	// A new exploratory branch is a way into another actor's run: its
	// step can be claimed and its divergence ref then pushed as the
	// bound actor (core/refadmit). The permission alone
	// (divergence.create_branch, contributor) does not tie the caller
	// to the run, so require an operator or an actor who holds a live
	// cursor of the run: the latest execution of its current step or of
	// one of its open divergence branches (refadmit.DecideBoundToRun,
	// owner question 6). A nil actor exists only in dev mode, where
	// authorize admits everything; there is no identity to bind then,
	// the same stance as the step-submit ownership check.
	if actor := actorFromContext(r.Context()); actor != nil {
		dec, err := refadmit.NewEvaluator(st).DecideBoundToRun(r.Context(), actor, divCtx.RunID)
		if err != nil {
			observe.Logger(r.Context()).Error("divergence branch creation: run binding check failed",
				"run_id", divCtx.RunID, "divergence_id", divergenceID, "error", err)
			WriteError(w, domain.NewError(domain.ErrInternal, "run binding check failed"))
			return
		}
		if !dec.Allow {
			observe.Logger(r.Context()).Warn("divergence branch creation refused: caller not bound to the run",
				"run_id", divCtx.RunID, "divergence_id", divergenceID,
				"actor_id", actor.ActorID, "reason", dec.Reason)
			WriteError(w, domain.NewError(domain.ErrForbidden,
				"creating a branch in this divergence requires holding the run's current step or "+
					"the current step of one of its open divergence branches, or the operator role"))
			return
		}
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

	st, ok := s.needStore(w, r)
	if !ok {
		return
	}
	bc, ok := s.needBranchCreator(w, r)
	if !ok {
		return
	}

	divergenceID := chi.URLParam(r, "divergence_id")

	divCtx, err := st.GetDivergenceContext(r.Context(), divergenceID)
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
