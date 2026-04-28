package gateway

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/repository"
)

// repositoryResponse is the API shape for a repository entry. It
// mirrors repository.Repository but redacts embedded credentials in
// clone_url before serialisation. credentials_ref is a pointer to a
// secret in the configured backend (ADR-010/011), not the secret
// material itself, so it is surfaced as-is for operator management.
type repositoryResponse struct {
	ID             string `json:"id"`
	WorkspaceID    string `json:"workspace_id"`
	Kind           string `json:"kind"`
	Name           string `json:"name,omitempty"`
	DefaultBranch  string `json:"default_branch,omitempty"`
	Role           string `json:"role,omitempty"`
	Description    string `json:"description,omitempty"`
	CloneURL       string `json:"clone_url,omitempty"`
	CredentialsRef string `json:"credentials_ref,omitempty"`
	LocalPath      string `json:"local_path,omitempty"`
	Status         string `json:"status,omitempty"`
}

func toRepositoryResponse(r repository.Repository) repositoryResponse {
	return repositoryResponse{
		ID:             r.ID,
		WorkspaceID:    r.WorkspaceID,
		Kind:           string(r.Kind),
		Name:           r.Name,
		DefaultBranch:  r.DefaultBranch,
		Role:           r.Role,
		Description:    r.Description,
		CloneURL:       repository.RedactCloneURL(r.CloneURL),
		CredentialsRef: r.CredentialsRef,
		LocalPath:      r.LocalPath,
		Status:         r.Status,
	}
}

type registerRepositoryRequest struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	DefaultBranch  string `json:"default_branch"`
	Role           string `json:"role,omitempty"`
	Description    string `json:"description,omitempty"`
	CloneURL       string `json:"clone_url"`
	CredentialsRef string `json:"credentials_ref,omitempty"`
	LocalPath      string `json:"local_path"`
}

type updateRepositoryRequest struct {
	Name           *string `json:"name,omitempty"`
	DefaultBranch  *string `json:"default_branch,omitempty"`
	Role           *string `json:"role,omitempty"`
	Description    *string `json:"description,omitempty"`
	CloneURL       *string `json:"clone_url,omitempty"`
	CredentialsRef *string `json:"credentials_ref,omitempty"`
	LocalPath      *string `json:"local_path,omitempty"`
}

func (s *Server) needRepoManager(w http.ResponseWriter, r *http.Request) (*repository.Manager, bool) {
	if s.repoManager == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable,
			"repository manager not configured"))
		return nil, false
	}
	// In shared multi-workspace mode the gateway resolves an
	// X-Workspace-ID per request, but ServerConfig.RepositoryManager
	// is a single process-level instance bound to one workspace.
	// Returning it for any workspace would let workspace B read or
	// mutate workspace A's catalog and bindings — a tenancy violation.
	// Until per-workspace resolution exists (workspace.ServiceSet or
	// equivalent), refuse the route in shared mode rather than silently
	// crossing tenants.
	if s.wsResolver != nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable,
			"repository manager is not yet wired for shared multi-workspace mode"))
		return nil, false
	}
	return s.repoManager, true
}

func (s *Server) handleRepositoryCreate(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "repository.create") {
		return
	}
	mgr, ok := s.needRepoManager(w, r)
	if !ok {
		return
	}
	req, ok := decodeBody[registerRepositoryRequest](w, r)
	if !ok {
		return
	}

	got, err := mgr.Register(r.Context(), repository.RegisterRequest{
		ID:             req.ID,
		Name:           req.Name,
		DefaultBranch:  req.DefaultBranch,
		Role:           req.Role,
		Description:    req.Description,
		CloneURL:       req.CloneURL,
		CredentialsRef: req.CredentialsRef,
		LocalPath:      req.LocalPath,
	})
	if err != nil {
		WriteError(w, err)
		return
	}
	WriteJSON(w, http.StatusCreated, toRepositoryResponse(*got))
}

func (s *Server) handleRepositoryList(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "repository.read") {
		return
	}
	mgr, ok := s.needRepoManager(w, r)
	if !ok {
		return
	}
	repos, err := mgr.List(r.Context())
	if err != nil {
		WriteError(w, err)
		return
	}
	items := make([]repositoryResponse, 0, len(repos))
	for i := range repos {
		items = append(items, toRepositoryResponse(repos[i]))
	}
	WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleRepositoryGet(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "repository.read") {
		return
	}
	mgr, ok := s.needRepoManager(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "repository_id")
	got, err := mgr.Get(r.Context(), id)
	if err != nil {
		WriteError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, toRepositoryResponse(*got))
}

func (s *Server) handleRepositoryUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "repository.update") {
		return
	}
	mgr, ok := s.needRepoManager(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "repository_id")
	req, ok := decodeBody[updateRepositoryRequest](w, r)
	if !ok {
		return
	}
	got, err := mgr.Update(r.Context(), id, repository.UpdateRequest{
		Name:           req.Name,
		DefaultBranch:  req.DefaultBranch,
		Role:           req.Role,
		Description:    req.Description,
		CloneURL:       req.CloneURL,
		CredentialsRef: req.CredentialsRef,
		LocalPath:      req.LocalPath,
	})
	if err != nil {
		WriteError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, toRepositoryResponse(*got))
}

func (s *Server) handleRepositoryDeactivate(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "repository.deactivate") {
		return
	}
	mgr, ok := s.needRepoManager(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "repository_id")
	if err := mgr.Deactivate(r.Context(), id); err != nil {
		WriteError(w, err)
		return
	}
	got, err := mgr.Get(r.Context(), id)
	if err != nil {
		// Deactivate succeeded but the follow-up read failed — surface
		// the read error rather than pretending success.
		var spineErr *domain.SpineError
		if errors.As(err, &spineErr) {
			WriteError(w, err)
			return
		}
		WriteError(w, domain.NewError(domain.ErrInternal, "failed to read deactivated repository"))
		return
	}
	WriteJSON(w, http.StatusOK, toRepositoryResponse(*got))
}
