package gateway

import (
	"net/http"

	"github.com/bszymi/spine/internal/auth"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/store"
	"github.com/bszymi/spine/internal/validation"
)

// Handler prologue helpers.
//
// Every gateway handler opens with the same shape: authorize the request
// against an operation, then check that the service it needs is actually
// wired. The store/service-not-configured message used to drift across
// files ("store not configured", "artifact service not configured", "auth
// not configured", ...). These helpers collapse the check to one call and
// pin the canonical message.
//
// Each helper returns (service, true) on success, or writes a 503 Unavailable
// response and returns (zero, false).

func (s *Server) needStore(w http.ResponseWriter, r *http.Request) (store.Store, bool) {
	st := s.storeFrom(r.Context())
	if st == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "store not configured"))
		return nil, false
	}
	return st, true
}

func (s *Server) needAuth(w http.ResponseWriter, r *http.Request) (*auth.Service, bool) {
	a := s.authFrom(r.Context())
	if a == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "auth not configured"))
		return nil, false
	}
	return a, true
}

func (s *Server) needArtifacts(w http.ResponseWriter, r *http.Request) (ArtifactService, bool) {
	svc := s.artifactsFrom(r.Context())
	if svc == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "artifact service not configured"))
		return nil, false
	}
	return svc, true
}

func (s *Server) needWorkflows(w http.ResponseWriter, r *http.Request) (WorkflowService, bool) {
	svc := s.workflowsFrom(r.Context())
	if svc == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "workflow service not configured"))
		return nil, false
	}
	return svc, true
}

func (s *Server) needProjQuery(w http.ResponseWriter, r *http.Request) (ProjectionQuerier, bool) {
	svc := s.projQueryFrom(r.Context())
	if svc == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "projection service not configured"))
		return nil, false
	}
	return svc, true
}

func (s *Server) needProjSync(w http.ResponseWriter, r *http.Request) (ProjectionSyncer, bool) {
	svc := s.projSyncFrom(r.Context())
	if svc == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "projection sync not configured"))
		return nil, false
	}
	return svc, true
}

func (s *Server) needGit(w http.ResponseWriter, r *http.Request) (GitReader, bool) {
	g := s.gitFrom(r.Context())
	if g == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "git reader not configured"))
		return nil, false
	}
	return g, true
}

func (s *Server) needValidator(w http.ResponseWriter, r *http.Request) (*validation.Engine, bool) {
	v := s.validatorFrom(r.Context())
	if v == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "validation engine not configured"))
		return nil, false
	}
	return v, true
}

func (s *Server) needBranchCreator(w http.ResponseWriter, r *http.Request) (BranchCreator, bool) {
	bc := s.branchCreatorFrom(r.Context())
	if bc == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "branch creator not configured"))
		return nil, false
	}
	return bc, true
}

func (s *Server) needRunStarter(w http.ResponseWriter, r *http.Request) (RunStarter, bool) {
	rs := s.runStarterFrom(r.Context())
	if rs == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "run starter not configured"))
		return nil, false
	}
	return rs, true
}

func (s *Server) needRunCanceller(w http.ResponseWriter, r *http.Request) (RunCanceller, bool) {
	rc := s.runCancellerFrom(r.Context())
	if rc == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "run canceller not configured"))
		return nil, false
	}
	return rc, true
}

func (s *Server) needPlanningRunStarter(w http.ResponseWriter, r *http.Request) (PlanningRunStarter, bool) {
	p := s.planningRunStarterFrom(r.Context())
	if p == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "planning run starter not configured"))
		return nil, false
	}
	return p, true
}

func (s *Server) needWorkflowPlanningStarter(w http.ResponseWriter, r *http.Request) (WorkflowPlanningRunStarter, bool) {
	p := s.wfPlanningStarterFrom(r.Context())
	if p == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "workflow planning starter not configured"))
		return nil, false
	}
	return p, true
}

// decodeAuthedJSON combines the three-line "authorize → decode body → 400"
// prologue into a single call. T is the request struct; callers dereference
// the returned pointer. On failure the appropriate response has already been
// written.
func decodeAuthedJSON[T any](s *Server, w http.ResponseWriter, r *http.Request, op auth.Operation) (*T, bool) {
	if !s.authorize(w, r, op) {
		return nil, false
	}
	var req T
	if err := decodeJSON(r, &req); err != nil {
		WriteError(w, err)
		return nil, false
	}
	return &req, true
}

// decodeBody decodes r.Body into a T, writing the appropriate 4xx response
// on failure. Used when the authorize call happens in a different place
// than decode (e.g. a shared setStatus helper called from multiple routes
// with different ops). Returns (req, true) on success.
func decodeBody[T any](w http.ResponseWriter, r *http.Request) (T, bool) {
	var req T
	if err := decodeJSON(r, &req); err != nil {
		WriteError(w, err)
		return req, false
	}
	return req, true
}
