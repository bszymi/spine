// Package gitpool provides repository-scoped Git client resolution
// for INIT-014 multi-repository workspaces.
//
// Existing services accept a single git.GitClient — that wiring still
// works for the primary Spine repository. New services that need to
// operate on per-task code repositories accept a *Pool, which hands
// back the right client (and local clone path) for a repository ID.
//
// The pool delegates all repository identification to the
// repository.Registry, so unknown/unbound/inactive lookups surface
// the same typed sentinels (repository.ErrRepository*) that direct
// Lookup calls do. Subsequent tasks in EPIC-003 (lazy clone, gateway
// route extension) build on this surface.
package gitpool

import (
	"context"
	"fmt"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/repository"
)

// Resolver is the subset of repository.Registry the pool depends on.
// Defined narrow so unit tests can supply a fake without standing up
// a full registry with catalog loader and binding store.
type Resolver interface {
	Lookup(ctx context.Context, id string) (*repository.Repository, error)
	ListActive(ctx context.Context) ([]repository.Repository, error)
}

// ClientFactory produces a git.GitClient rooted at the given local
// path. Production wires this to a thin wrapper over git.NewCLIClient;
// tests substitute a stub that returns a canned client for assertion
// without touching the filesystem.
type ClientFactory func(localPath string) git.GitClient

// Pool resolves Git clients by repository ID.
//
// The primary Git client is cached at construction so governance
// reads — which dominate every request — never pay the cost of a
// registry round-trip. Code repository clients are built on demand
// from the registry-resolved local path; later tasks in EPIC-003
// introduce lazy clone-and-cache. Until then the factory is invoked
// per call.
type Pool struct {
	primary  git.GitClient
	resolver Resolver
	factory  ClientFactory
}

// New constructs a Pool. All three arguments are required: a Pool
// without a primary client is meaningless (the primary is the legacy
// default for governance code paths), without a resolver it cannot
// route by repository ID, and without a factory it has no way to
// materialise code-repo clients.
func New(primary git.GitClient, resolver Resolver, factory ClientFactory) (*Pool, error) {
	if primary == nil {
		return nil, fmt.Errorf("gitpool: primary client is required")
	}
	if resolver == nil {
		return nil, fmt.Errorf("gitpool: resolver is required")
	}
	if factory == nil {
		return nil, fmt.Errorf("gitpool: client factory is required")
	}
	return &Pool{
		primary:  primary,
		resolver: resolver,
		factory:  factory,
	}, nil
}

// PrimaryClient returns the workspace's primary Git client. Callers
// reading governance state (artifacts, workflows, branch protection)
// should prefer this over Client(ctx, "spine") because it skips the
// resolver entirely.
func (p *Pool) PrimaryClient() git.GitClient {
	return p.primary
}

// Client returns the Git client for repositoryID. The primary ID
// short-circuits to the cached client; every other ID goes through
// the registry, so unknown/unbound/inactive errors propagate
// untouched (matchable with errors.Is on repository.ErrRepository*).
//
// An active binding with an empty local path is treated as a
// precondition failure rather than silently returning a client
// rooted at "" — that would let callers operate on an unintended
// directory.
func (p *Pool) Client(ctx context.Context, repositoryID string) (git.GitClient, error) {
	if repositoryID == repository.PrimaryRepositoryID {
		return p.primary, nil
	}
	repo, err := p.resolver.Lookup(ctx, repositoryID)
	if err != nil {
		return nil, err
	}
	if repo.LocalPath == "" {
		return nil, domain.NewError(domain.ErrPrecondition,
			fmt.Sprintf("repository %q has no local clone path", repositoryID))
	}
	return p.factory(repo.LocalPath), nil
}

// RepositoryPath returns the local clone path for repositoryID.
// Same resolution semantics as Client; "spine" returns the path
// configured on the registry's primary spec via Lookup so callers
// see one consistent source of truth.
func (p *Pool) RepositoryPath(ctx context.Context, repositoryID string) (string, error) {
	repo, err := p.resolver.Lookup(ctx, repositoryID)
	if err != nil {
		return "", err
	}
	if repo.LocalPath == "" {
		return "", domain.NewError(domain.ErrPrecondition,
			fmt.Sprintf("repository %q has no local clone path", repositoryID))
	}
	return repo.LocalPath, nil
}

// ListActive returns every repository the pool can hand out clients
// for — the primary plus code repos with an active binding.
// Delegates to the registry so callers don't need a separate
// registry handle.
func (p *Pool) ListActive(ctx context.Context) ([]repository.Repository, error) {
	return p.resolver.ListActive(ctx)
}
