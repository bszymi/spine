package gateway

// Governance-path guard endpoints (ADR-016; INIT-023 TASK-006): an
// operator approves or rejects a governed merge held because it changes
// governance paths, and anyone who may read a run's status may read its
// governance state. The engine re-checks the operator role, the bound
// commit and the run state itself; this layer only authenticates, routes
// and maps errors (403 below operator, 400 malformed, 404 unknown run,
// 409 not committing or another commit, 412 no governance change, 503
// guard off or not wired).

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/engine"
)

// RunGovernanceDecider is the engine surface behind the two routes.
// *engine.Orchestrator implements it; the gateway finds it on the
// workspace's RunMergeResolver (the orchestrator), so no new ServiceSet
// field is needed. A resolver without it answers 503.
type RunGovernanceDecider interface {
	DecideGovernanceMerge(ctx context.Context, req engine.GovernanceDecisionRequest) (*engine.GovernanceDecisionResult, error)
	GovernanceMergeStatus(ctx context.Context, runID string) (*engine.GovernanceMergeStatus, error)
}

func (s *Server) governanceDeciderFrom(ctx context.Context) RunGovernanceDecider {
	if d, ok := s.runMergeResolverFrom(ctx).(RunGovernanceDecider); ok {
		return d
	}
	return nil
}

type governanceDecisionRequest struct {
	Decision  string `json:"decision"`
	CommitSHA string `json:"commit_sha"`
	Reason    string `json:"reason"`
}

// handleRunGovernanceDecision: POST /runs/{run_id}/governance-decision,
// body {"decision": "approve"|"reject", "commit_sha", "reason"}.
func (s *Server) handleRunGovernanceDecision(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "run.governance.decide") {
		return
	}
	decider := s.governanceDeciderFrom(r.Context())
	if decider == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "governance decisions are not available in this deployment"))
		return
	}
	req, ok := decodeBody[governanceDecisionRequest](w, r)
	if !ok {
		return
	}
	actor := actorFromContext(r.Context())
	if actor == nil {
		WriteError(w, domain.NewError(domain.ErrForbidden, "an authenticated operator is required"))
		return
	}
	res, err := decider.DecideGovernanceMerge(r.Context(), engine.GovernanceDecisionRequest{
		RunID:     chi.URLParam(r, "run_id"),
		CommitSHA: req.CommitSHA,
		Decision:  engine.GovernanceDecision(req.Decision),
		Actor:     domain.Actor{ActorID: actor.ActorID, Role: actor.Role},
		Reason:    req.Reason,
	})
	if err != nil {
		WriteError(w, err)
		return
	}
	paths := res.Paths
	if paths == nil {
		paths = []string{}
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"run_id":          res.RunID,
		"commit_sha":      res.CommitSHA,
		"decision":        string(res.Decision),
		"paths":           paths,
		"path_count":      res.PathCount,
		"merge_attempted": res.MergeAttempted,
		"merge_error":     res.MergeError,
		"run_status":      string(res.RunStatus),
	})
}

// handleRunGovernanceStatus: GET /runs/{run_id}/governance.
func (s *Server) handleRunGovernanceStatus(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "run.status") {
		return
	}
	decider := s.governanceDeciderFrom(r.Context())
	if decider == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "governance status is not available in this deployment"))
		return
	}
	st, err := decider.GovernanceMergeStatus(r.Context(), chi.URLParam(r, "run_id"))
	if err != nil {
		WriteError(w, err)
		return
	}
	nonNil := func(ps []string) []string {
		if ps == nil {
			return []string{}
		}
		return ps
	}
	resp := map[string]any{
		"run_id":            st.RunID,
		"run_status":        string(st.RunStatus),
		"evaluated":         st.Evaluated,
		"evaluation_error":  st.EvaluationError,
		"bound_sha":         st.BoundSHA,
		"required":          st.Required,
		"held":              st.Held,
		"blocked":           st.Blocked,
		"blocked_merge_sha": st.BlockedMergeSHA,
		"paths":             nonNil(st.Paths),
		"path_count":        st.PathCount,
		"truncated":         st.Truncated,
		"exempt_paths":      nonNil(st.ExemptPaths),
		"approval":          nil,
	}
	if a := st.Approval; a != nil {
		resp["approval"] = map[string]any{
			"sha": a.SHA, "actor_id": a.ActorID, "actor_role": a.ActorRole,
			"approved_at": a.ApprovedAt, "merge_sha": a.MergeSHA, "base_sha": a.BaseSHA,
		}
	}
	WriteJSON(w, http.StatusOK, resp)
}
