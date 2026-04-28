package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/store"
)

// ManagerStore is the binding-side surface the Manager needs. It
// extends BindingStore (used by the read-only Registry) with the
// write paths required to register, update, and deactivate code-repo
// bindings.
type ManagerStore interface {
	BindingStore
	GetActiveRepositoryBinding(ctx context.Context, workspaceID, repositoryID string) (*store.RepositoryBinding, error)
	CreateRepositoryBinding(ctx context.Context, b *store.RepositoryBinding) error
	UpdateRepositoryBinding(ctx context.Context, b *store.RepositoryBinding) error
	DeactivateRepositoryBinding(ctx context.Context, workspaceID, repositoryID string) error
}

// RunReferenceChecker reports whether any non-terminal Run in the
// workspace currently references a given repository. The Manager
// consults it before deactivating a code-repo binding so an in-flight
// run cannot suddenly find its operational config pulled out from
// under it.
//
// The default implementation in NopRunReferenceChecker conservatively
// reports false because per-Run repository metadata only lands in
// EPIC-004; callers that want strict deactivation safety should swap
// in an implementation that queries runs.affected_repositories once
// that column exists.
type RunReferenceChecker interface {
	AnyActiveRunReferences(ctx context.Context, workspaceID, repositoryID string) (bool, error)
}

// NopRunReferenceChecker is the placeholder implementation used until
// per-Run repository metadata exists. It always reports "no active
// runs reference this repository", which means deactivation succeeds
// based on binding state alone.
type NopRunReferenceChecker struct{}

func (NopRunReferenceChecker) AnyActiveRunReferences(context.Context, string, string) (bool, error) {
	return false, nil
}

// RegisterRequest is the input for Manager.Register. CredentialsRef
// is optional; everything else is required.
type RegisterRequest struct {
	ID             string
	Name           string
	DefaultBranch  string
	Role           string
	Description    string
	CloneURL       string
	CredentialsRef string
	LocalPath      string
}

// UpdateRequest carries optional updates to identity (catalog) and
// operational (binding) fields. Nil pointers leave the existing value
// alone. ID and kind are immutable and cannot be changed here.
type UpdateRequest struct {
	Name           *string
	DefaultBranch  *string
	Role           *string
	Description    *string
	CloneURL       *string
	CredentialsRef *string
	LocalPath      *string
}

// Manager is the write-side service for repository management. It
// orchestrates catalog identity writes and runtime binding writes,
// rolling back the catalog half if the binding half fails so the two
// stay consistent (per ADR-013 / multi-repository-integration.md
// §6.1).
type Manager struct {
	workspaceID string
	primary     PrimarySpec
	catalog     CatalogStore
	bindings    ManagerStore
	runs        RunReferenceChecker
}

// NewManager constructs a Manager. runs may be nil to use the no-op
// reference checker (deactivation will not block on active runs
// until per-run repository metadata exists).
func NewManager(workspaceID string, primary PrimarySpec, catalog CatalogStore, bindings ManagerStore, runs RunReferenceChecker) *Manager {
	if runs == nil {
		runs = NopRunReferenceChecker{}
	}
	return &Manager{
		workspaceID: workspaceID,
		primary:     primary,
		catalog:     catalog,
		bindings:    bindings,
		runs:        runs,
	}
}

// Register validates the request, writes the catalog entry, then
// writes the binding row. If the binding insert fails the catalog
// entry is rolled back so the same ID can be re-registered without
// manual cleanup.
func (m *Manager) Register(ctx context.Context, req RegisterRequest) (*Repository, error) {
	if err := validateID(req.ID); err != nil {
		return nil, err
	}
	if req.ID == PrimaryRepositoryID {
		return nil, domain.NewError(domain.ErrInvalidParams,
			"the primary 'spine' repository is implicit and cannot be registered")
	}
	if strings.TrimSpace(req.Name) == "" {
		return nil, domain.NewError(domain.ErrInvalidParams, "name is required")
	}
	if strings.TrimSpace(req.DefaultBranch) == "" {
		return nil, domain.NewError(domain.ErrInvalidParams, "default_branch is required")
	}
	if strings.TrimSpace(req.LocalPath) == "" {
		return nil, domain.NewError(domain.ErrInvalidParams, "local_path is required")
	}
	if err := ValidateCloneURL(req.CloneURL); err != nil {
		return nil, err
	}

	entry := CatalogEntry{
		ID:            req.ID,
		Kind:          KindCode,
		Name:          req.Name,
		DefaultBranch: req.DefaultBranch,
		Role:          req.Role,
		Description:   req.Description,
	}

	if err := m.catalog.AddEntry(ctx, entry); err != nil {
		return nil, err
	}

	binding := &store.RepositoryBinding{
		RepositoryID:   req.ID,
		WorkspaceID:    m.workspaceID,
		CloneURL:       req.CloneURL,
		CredentialsRef: req.CredentialsRef,
		LocalPath:      req.LocalPath,
		Status:         store.RepositoryBindingStatusActive,
	}
	if err := m.bindings.CreateRepositoryBinding(ctx, binding); err != nil {
		// Roll back the catalog write so the same ID can be retried.
		if rbErr := m.catalog.RemoveEntry(ctx, req.ID); rbErr != nil {
			return nil, fmt.Errorf("create binding: %w; rollback failed: %v", err, rbErr)
		}
		return nil, err
	}

	saved, err := m.bindings.GetRepositoryBinding(ctx, m.workspaceID, req.ID)
	if err != nil {
		return nil, err
	}
	return mergeRepository(entry, saved), nil
}

// Update applies a partial update to identity and/or operational
// fields. Identity changes flow through the catalog; operational
// changes flow through the binding row.
func (m *Manager) Update(ctx context.Context, id string, req UpdateRequest) (*Repository, error) {
	if err := validateID(id); err != nil {
		return nil, err
	}
	if id == PrimaryRepositoryID {
		return nil, domain.NewError(domain.ErrInvalidParams,
			"primary 'spine' repository cannot be modified through the repositories API")
	}

	cat, err := m.catalog.Load(ctx)
	if err != nil {
		return nil, err
	}
	entry, ok := cat.Get(id)
	if !ok {
		return nil, newNotFoundError(id)
	}
	if entry.Kind == KindSpine {
		return nil, domain.NewError(domain.ErrInvalidParams,
			"primary 'spine' repository cannot be modified through the repositories API")
	}

	// Validate every requested change up front before mutating either
	// store, so a request that mixes a valid identity change with an
	// invalid operational change (e.g. a malformed clone_url) cannot
	// half-commit the identity rewrite to the catalog.
	if req.Name != nil && strings.TrimSpace(*req.Name) == "" {
		return nil, domain.NewError(domain.ErrInvalidParams, "name cannot be blank")
	}
	if req.DefaultBranch != nil && strings.TrimSpace(*req.DefaultBranch) == "" {
		return nil, domain.NewError(domain.ErrInvalidParams, "default_branch cannot be blank")
	}
	if req.LocalPath != nil && strings.TrimSpace(*req.LocalPath) == "" {
		return nil, domain.NewError(domain.ErrInvalidParams, "local_path cannot be blank")
	}
	if req.CloneURL != nil {
		if err := ValidateCloneURL(*req.CloneURL); err != nil {
			return nil, err
		}
	}

	binding, err := m.bindings.GetRepositoryBinding(ctx, m.workspaceID, id)
	if err != nil {
		return nil, err
	}

	priorEntry := entry
	if req.Name != nil {
		entry.Name = *req.Name
	}
	if req.DefaultBranch != nil {
		entry.DefaultBranch = *req.DefaultBranch
	}
	if req.Role != nil {
		entry.Role = *req.Role
	}
	if req.Description != nil {
		entry.Description = *req.Description
	}

	identityTouched := req.Name != nil || req.DefaultBranch != nil || req.Role != nil || req.Description != nil
	if identityTouched {
		if err := m.catalog.UpdateEntry(ctx, entry); err != nil {
			return nil, err
		}
	}

	bindingTouched := false
	if req.CloneURL != nil {
		binding.CloneURL = *req.CloneURL
		bindingTouched = true
	}
	if req.CredentialsRef != nil {
		binding.CredentialsRef = *req.CredentialsRef
		bindingTouched = true
	}
	if req.LocalPath != nil {
		binding.LocalPath = *req.LocalPath
		bindingTouched = true
	}

	if bindingTouched {
		// Status is left empty so the store's COALESCE preserves the
		// existing active/inactive flag and we don't accidentally
		// reactivate a deactivated binding here. See
		// internal/store/postgres_repositories.go:UpdateRepositoryBinding.
		binding.Status = ""
		if err := m.bindings.UpdateRepositoryBinding(ctx, binding); err != nil {
			// Roll back the catalog identity write so the two
			// stores stay consistent. Without this, a binding
			// failure would leave the rename/role-change committed
			// even though the operator's request as a whole failed.
			if identityTouched {
				if rbErr := m.catalog.UpdateEntry(ctx, priorEntry); rbErr != nil {
					return nil, fmt.Errorf("update binding: %w; catalog rollback failed: %v", err, rbErr)
				}
			}
			return nil, err
		}
		// Re-fetch so the merged view reflects the now-stored row,
		// including any timestamps the store refreshed.
		binding, err = m.bindings.GetRepositoryBinding(ctx, m.workspaceID, id)
		if err != nil {
			return nil, err
		}
	}

	return mergeRepository(entry, binding), nil
}

// Deactivate flips the binding row to inactive after confirming no
// non-terminal run references the repository. The catalog entry is
// left in place — soft deactivation per multi-repository-integration.md
// §6.3 (which reserves the public deregister API for full removal).
func (m *Manager) Deactivate(ctx context.Context, id string) error {
	if err := validateID(id); err != nil {
		return err
	}
	if id == PrimaryRepositoryID {
		return domain.NewError(domain.ErrInvalidParams,
			"primary 'spine' repository cannot be deactivated")
	}

	cat, err := m.catalog.Load(ctx)
	if err != nil {
		return err
	}
	entry, ok := cat.Get(id)
	if !ok {
		return newNotFoundError(id)
	}
	if entry.Kind == KindSpine {
		return domain.NewError(domain.ErrInvalidParams,
			"primary 'spine' repository cannot be deactivated")
	}

	binding, err := m.bindings.GetRepositoryBinding(ctx, m.workspaceID, id)
	if err != nil {
		return err
	}
	if binding.Status == store.RepositoryBindingStatusInactive {
		return nil
	}

	active, err := m.runs.AnyActiveRunReferences(ctx, m.workspaceID, id)
	if err != nil {
		return err
	}
	if active {
		return domain.NewError(domain.ErrPrecondition,
			fmt.Sprintf("repository %q has active runs referencing it; cancel or wait for those runs before deactivating", id))
	}

	return m.bindings.DeactivateRepositoryBinding(ctx, m.workspaceID, id)
}

// Get returns the joined repository view for id. Inactive bindings
// are surfaced (Status="inactive") so admin views can manage them.
// For the strict execution-time check use Registry.Lookup.
func (m *Manager) Get(ctx context.Context, id string) (*Repository, error) {
	if err := validateID(id); err != nil {
		return nil, err
	}
	cat, err := m.catalog.Load(ctx)
	if err != nil {
		return nil, err
	}
	if id == PrimaryRepositoryID {
		return primaryFromCatalog(cat, m.workspaceID, m.primary), nil
	}
	entry, ok := cat.Get(id)
	if !ok {
		return nil, newNotFoundError(id)
	}
	if entry.Kind == KindSpine {
		return primaryFromCatalog(cat, m.workspaceID, m.primary), nil
	}
	binding, err := m.bindings.GetRepositoryBinding(ctx, m.workspaceID, id)
	if err != nil {
		var spineErr *domain.SpineError
		if errors.As(err, &spineErr) && spineErr.Code == domain.ErrNotFound {
			return &Repository{
				ID: entry.ID, WorkspaceID: m.workspaceID, Kind: entry.Kind,
				Name: entry.Name, DefaultBranch: entry.DefaultBranch,
				Role: entry.Role, Description: entry.Description,
			}, nil
		}
		return nil, err
	}
	return mergeRepository(entry, binding), nil
}

// List returns every known repository (primary plus catalog entries),
// with binding details merged when present. Same shape as
// Registry.List — used by GET /repositories.
func (m *Manager) List(ctx context.Context) ([]Repository, error) {
	cat, err := m.catalog.Load(ctx)
	if err != nil {
		return nil, err
	}
	all, err := m.bindings.ListRepositoryBindings(ctx, m.workspaceID)
	if err != nil {
		return nil, err
	}
	bindings := make(map[string]store.RepositoryBinding, len(all))
	for i := range all {
		bindings[all[i].RepositoryID] = all[i]
	}

	out := []Repository{*primaryFromCatalog(cat, m.workspaceID, m.primary)}
	for _, entry := range cat.List() {
		if entry.Kind == KindSpine {
			continue
		}
		if b, ok := bindings[entry.ID]; ok {
			out = append(out, *mergeRepository(entry, &b))
			continue
		}
		out = append(out, Repository{
			ID: entry.ID, WorkspaceID: m.workspaceID, Kind: entry.Kind,
			Name: entry.Name, DefaultBranch: entry.DefaultBranch,
			Role: entry.Role, Description: entry.Description,
		})
	}
	return out, nil
}

func primaryFromCatalog(cat *Catalog, workspaceID string, primary PrimarySpec) *Repository {
	entry := cat.Primary()
	name := entry.Name
	if name == "" {
		name = primary.Name
	}
	branch := entry.DefaultBranch
	if branch == "" {
		branch = primary.DefaultBranch
	}
	return &Repository{
		ID:            PrimaryRepositoryID,
		WorkspaceID:   workspaceID,
		Kind:          KindSpine,
		Name:          name,
		DefaultBranch: branch,
		Role:          entry.Role,
		Description:   entry.Description,
		LocalPath:     primary.LocalPath,
		Status:        store.RepositoryBindingStatusActive,
	}
}

func validateID(id string) error {
	if id == "" {
		return domain.NewError(domain.ErrInvalidParams, "repository id required")
	}
	if len(id) > MaxIDLength {
		return domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("repository id exceeds %d-character limit", MaxIDLength))
	}
	if !idPattern.MatchString(id) {
		return domain.NewError(domain.ErrInvalidParams,
			"repository id must match "+idPattern.String())
	}
	return nil
}
