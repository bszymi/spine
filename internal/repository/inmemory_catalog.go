package repository

import (
	"context"
	"sync"

	"github.com/bszymi/spine/internal/domain"
)

// CatalogStore is the read+write surface for the governed catalog.
// Production wires this to a Git-backed implementation that commits
// /.spine/repositories.yaml on the primary Spine repo. Unit tests and
// scaffolding setups can use InMemoryCatalogStore.
type CatalogStore interface {
	// Load returns the current parsed catalog. Implementations are
	// responsible for synthesising the primary entry when no catalog
	// file exists (single-repo workspace) — see ParseCatalog.
	Load(ctx context.Context) (*Catalog, error)

	// AddEntry appends a new code-repo entry. Implementations must
	// reject duplicate IDs and the reserved primary id "spine".
	AddEntry(ctx context.Context, entry CatalogEntry) error

	// UpdateEntry rewrites an existing entry's mutable identity
	// fields (name/default_branch/role/description). It does not
	// rename the ID. The kind is also immutable.
	UpdateEntry(ctx context.Context, entry CatalogEntry) error

	// RemoveEntry deletes an entry. Used by the rollback path of
	// failed registrations (the public deregistration API uses
	// CatalogStore plus binding deletion together — see ADR-013).
	RemoveEntry(ctx context.Context, id string) error
}

// InMemoryCatalogStore is a non-persistent CatalogStore implementation.
// It is safe for concurrent use within a single process; it does NOT
// survive restart and is only intended for tests, scenario fixtures,
// and the bring-up phase before a Git-backed CatalogStore is wired in.
type InMemoryCatalogStore struct {
	mu      sync.RWMutex
	primary PrimarySpec
	entries map[string]CatalogEntry
}

// NewInMemoryCatalogStore constructs an empty store seeded with the
// implicit primary entry derived from spec. The primary is always
// present and never removable.
func NewInMemoryCatalogStore(primary PrimarySpec) *InMemoryCatalogStore {
	primaryEntry := CatalogEntry{
		ID:            PrimaryRepositoryID,
		Kind:          KindSpine,
		Name:          primary.Name,
		DefaultBranch: primary.DefaultBranch,
	}
	return &InMemoryCatalogStore{
		primary: primary,
		entries: map[string]CatalogEntry{PrimaryRepositoryID: primaryEntry},
	}
}

func (s *InMemoryCatalogStore) Load(_ context.Context) (*Catalog, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cat := &Catalog{entries: make(map[string]CatalogEntry, len(s.entries))}
	for id, e := range s.entries {
		cat.entries[id] = e
		if e.Kind == KindSpine {
			cat.primary = e
		}
	}
	return cat, nil
}

func (s *InMemoryCatalogStore) AddEntry(_ context.Context, entry CatalogEntry) error {
	if entry.ID == PrimaryRepositoryID {
		return domain.NewError(domain.ErrInvalidParams,
			"cannot add a primary entry; the primary 'spine' repository is implicit")
	}
	if entry.Kind == KindSpine {
		return domain.NewError(domain.ErrInvalidParams,
			"only the implicit primary may have kind=spine")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.entries[entry.ID]; exists {
		return domain.NewError(domain.ErrAlreadyExists,
			"repository "+entry.ID+" already exists in catalog")
	}
	s.entries[entry.ID] = entry
	return nil
}

func (s *InMemoryCatalogStore) UpdateEntry(_ context.Context, entry CatalogEntry) error {
	if entry.ID == PrimaryRepositoryID {
		return domain.NewError(domain.ErrInvalidParams,
			"primary 'spine' entry is immutable through the catalog API")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.entries[entry.ID]
	if !ok {
		return domain.NewError(domain.ErrNotFound,
			"repository "+entry.ID+" not found in catalog")
	}
	if entry.Kind != "" && entry.Kind != cur.Kind {
		return domain.NewError(domain.ErrInvalidParams,
			"repository kind is immutable")
	}
	entry.Kind = cur.Kind
	s.entries[entry.ID] = entry
	return nil
}

func (s *InMemoryCatalogStore) RemoveEntry(_ context.Context, id string) error {
	if id == PrimaryRepositoryID {
		return domain.NewError(domain.ErrInvalidParams,
			"primary 'spine' entry cannot be removed")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.entries[id]; !ok {
		return domain.NewError(domain.ErrNotFound,
			"repository "+id+" not found in catalog")
	}
	delete(s.entries, id)
	return nil
}
