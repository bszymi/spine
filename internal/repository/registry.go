package repository

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/store"
)

// BindingStore is the subset of store.Store that the registry uses.
// Defined narrow so unit tests can supply a fake without depending on
// pgx.
type BindingStore interface {
	GetRepositoryBinding(ctx context.Context, workspaceID, repositoryID string) (*store.RepositoryBinding, error)
	ListRepositoryBindings(ctx context.Context, workspaceID string) ([]store.RepositoryBinding, error)
}

// CatalogLoader returns the current parsed catalog for the workspace.
// Production wires this to a Git read of /.spine/repositories.yaml on
// the primary repo's authoritative branch followed by ParseCatalog;
// tests wire it to a fixed catalog. The registry calls the loader on
// each operation so the catalog stays consistent with the latest
// governance commit — caching is a caller decision.
type CatalogLoader func(ctx context.Context) (*Catalog, error)

// Repository is the joined in-memory view a service receives. It
// merges identity (catalog) with operational details (binding); for
// the primary repo, the operational fields are taken from PrimarySpec
// since no binding row exists.
type Repository struct {
	ID            string
	WorkspaceID   string
	Kind          Kind
	Name          string
	DefaultBranch string
	Role          string
	Description   string

	CloneURL       string
	CredentialsRef string
	LocalPath      string
	Status         string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// IsPrimary reports whether this is the workspace primary repo.
func (r Repository) IsPrimary() bool { return r.Kind == KindSpine }

// IsActive reports whether the repository can be used by the
// execution hot-path. The primary is always active; code repos must
// have an active binding row.
func (r Repository) IsActive() bool {
	if r.Kind == KindSpine {
		return true
	}
	return r.Status == store.RepositoryBindingStatusActive
}

// Registry resolves repositories by joining the governed catalog with
// runtime binding rows. It is the single read-side entry point for
// gateway, engine, projection, and git HTTP routing.
type Registry struct {
	workspaceID string
	primary     PrimarySpec
	loader      CatalogLoader
	store       BindingStore
}

// New constructs a Registry. workspaceID, primary spec, and loader
// must be supplied; store may be nil in tests that exercise primary-
// only flows but is required for any code-repo lookup.
func New(workspaceID string, primary PrimarySpec, loader CatalogLoader, bindings BindingStore) *Registry {
	return &Registry{
		workspaceID: workspaceID,
		primary:     primary,
		loader:      loader,
		store:       bindings,
	}
}

// Lookup resolves a repository by ID. Lookup of "spine" always
// succeeds and returns the virtual primary view. For code repos, both
// a catalog entry and an active binding row are required:
//
//   - unknown ID                    -> ErrRepositoryNotFound
//   - catalog only, no binding      -> ErrRepositoryUnbound
//   - catalog + inactive binding    -> ErrRepositoryInactive
//   - catalog + active binding      -> resolved Repository
//
// All errors are domain.SpineError-wrapped so the gateway maps them
// to HTTP statuses; the wrapped sentinels (Err*) remain matchable via
// errors.Is.
func (r *Registry) Lookup(ctx context.Context, id string) (*Repository, error) {
	if id == "" {
		return nil, domain.NewError(domain.ErrInvalidParams, "repository id required")
	}

	cat, err := r.loader(ctx)
	if err != nil {
		return nil, err
	}

	if id == PrimaryRepositoryID {
		return r.primaryRepository(cat), nil
	}

	entry, ok := cat.Get(id)
	if !ok {
		return nil, newNotFoundError(id)
	}
	if entry.Kind == KindSpine {
		// Defence in depth: a catalog where the kind=spine entry has
		// any other ID is rejected at parse time, but a future
		// loader bug should not silently mis-resolve the primary.
		return r.primaryRepository(cat), nil
	}

	if r.store == nil {
		return nil, newUnboundError(id)
	}

	binding, err := r.store.GetRepositoryBinding(ctx, r.workspaceID, id)
	if err != nil {
		var spineErr *domain.SpineError
		if errors.As(err, &spineErr) && spineErr.Code == domain.ErrNotFound {
			return nil, newUnboundError(id)
		}
		return nil, err
	}
	if binding.Status != store.RepositoryBindingStatusActive {
		return nil, newInactiveError(id)
	}

	return mergeRepository(entry, binding), nil
}

// List returns all known repositories — the primary plus every
// catalog entry, with binding details merged when present. Catalog
// entries without a binding row appear with empty operational fields
// and Status="" so callers can distinguish "registered but unbound"
// from "registered and active". Orphan binding rows (binding present,
// no catalog entry) are dropped because the catalog is authoritative.
//
// The slice is sorted by ID with the primary pinned first.
func (r *Registry) List(ctx context.Context) ([]Repository, error) {
	cat, err := r.loader(ctx)
	if err != nil {
		return nil, err
	}

	bindings := map[string]store.RepositoryBinding{}
	if r.store != nil {
		all, err := r.store.ListRepositoryBindings(ctx, r.workspaceID)
		if err != nil {
			return nil, err
		}
		for i := range all {
			bindings[all[i].RepositoryID] = all[i]
		}
	}

	out := make([]Repository, 0, len(cat.entries))
	primary := r.primaryRepository(cat)
	out = append(out, *primary)

	codeIDs := make([]string, 0, len(cat.entries))
	for id, entry := range cat.entries {
		if entry.Kind == KindSpine {
			continue
		}
		codeIDs = append(codeIDs, id)
	}
	sort.Strings(codeIDs)
	for _, id := range codeIDs {
		entry := cat.entries[id]
		if b, ok := bindings[id]; ok {
			out = append(out, *mergeRepository(entry, &b))
			continue
		}
		out = append(out, Repository{
			ID:            entry.ID,
			WorkspaceID:   r.workspaceID,
			Kind:          entry.Kind,
			Name:          entry.Name,
			DefaultBranch: entry.DefaultBranch,
			Role:          entry.Role,
			Description:   entry.Description,
		})
	}
	return out, nil
}

// ListActive returns only repositories usable on the execution hot-
// path: the primary plus catalog entries with an active binding.
// Inactive bindings, unbound catalog entries, and orphan bindings are
// all excluded.
func (r *Registry) ListActive(ctx context.Context) ([]Repository, error) {
	all, err := r.List(ctx)
	if err != nil {
		return nil, err
	}
	active := all[:0]
	for i := range all {
		if all[i].IsActive() {
			active = append(active, all[i])
		}
	}
	return active, nil
}

func (r *Registry) primaryRepository(cat *Catalog) *Repository {
	entry := cat.Primary()
	name := entry.Name
	if name == "" {
		name = r.primary.Name
	}
	branch := entry.DefaultBranch
	if branch == "" {
		branch = r.primary.DefaultBranch
	}
	return &Repository{
		ID:            PrimaryRepositoryID,
		WorkspaceID:   r.workspaceID,
		Kind:          KindSpine,
		Name:          name,
		DefaultBranch: branch,
		Role:          entry.Role,
		Description:   entry.Description,
		LocalPath:     r.primary.LocalPath,
		Status:        store.RepositoryBindingStatusActive,
	}
}

func mergeRepository(entry CatalogEntry, binding *store.RepositoryBinding) *Repository {
	branch := entry.DefaultBranch
	if binding.DefaultBranch != "" {
		// Binding-level default_branch is an explicit operational
		// override (e.g. pinning a long-lived release branch
		// without rewriting the governed catalog).
		branch = binding.DefaultBranch
	}
	return &Repository{
		ID:             entry.ID,
		WorkspaceID:    binding.WorkspaceID,
		Kind:           entry.Kind,
		Name:           entry.Name,
		DefaultBranch:  branch,
		Role:           entry.Role,
		Description:    entry.Description,
		CloneURL:       binding.CloneURL,
		CredentialsRef: binding.CredentialsRef,
		LocalPath:      binding.LocalPath,
		Status:         binding.Status,
		CreatedAt:      binding.CreatedAt,
		UpdatedAt:      binding.UpdatedAt,
	}
}
