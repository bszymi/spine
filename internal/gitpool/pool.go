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
// Lookup calls do.
//
// Optional clone-mode: enable WithCloner (and typically WithRepoBase)
// to make Client() lazily clone missing code repos, cache the
// resulting *git.CLIClient, and dedupe concurrent first-access via
// singleflight. Without those options the pool stays in the
// pass-through mode introduced in TASK-001 — useful for unit tests
// and for callers that already have clients materialised elsewhere.
package gitpool

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"

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

// Cloner performs a Git clone from a remote URL to a local path. The
// production implementation is a *git.CLIClient (its Clone method
// does not depend on its repoPath, so a single shared cloner is
// fine); tests inject a fake that materialises a directory without
// shelling out.
type Cloner interface {
	Clone(ctx context.Context, url, localPath string) error
}

// Option configures a Pool. Without options, Client() returns
// factory(LocalPath) on every call — the TASK-001 pass-through mode.
// WithCloner switches the pool into lazy clone-and-cache mode.
type Option func(*Pool)

// WithCloner enables clone-on-miss and per-repository client caching.
// Without this option the pool never touches the filesystem and never
// caches; it simply hands back factory(LocalPath) on every call.
func WithCloner(c Cloner) Option {
	return func(p *Pool) { p.cloner = c }
}

// WithRepoBase enforces that every resolved LocalPath sits under base
// before the pool will clone or open it. Empty disables the check.
// The check is a defence-in-depth guard against a corrupted binding
// row pointing at an unrelated directory: a workspace has one
// configured repo base; nothing legitimate should resolve outside it.
func WithRepoBase(base string) Option {
	return func(p *Pool) { p.repoBase = base }
}

// WithLogger replaces the default slog.Default() logger used for
// clone/cache/coalesce structured events.
func WithLogger(l *slog.Logger) Option {
	return func(p *Pool) {
		if l != nil {
			p.logger = l
		}
	}
}

// WithClock injects a time source for clone-duration measurement.
// Tests use this to make duration assertions deterministic.
func WithClock(now func() time.Time) Option {
	return func(p *Pool) {
		if now != nil {
			p.now = now
		}
	}
}

// cachedClient pairs a built client with the local path it was rooted
// at, so a cache hit can confirm the binding's LocalPath has not been
// rewritten under the pool. If a binding update changes the path, the
// cache entry becomes stale and is evicted on next access.
type cachedClient struct {
	client git.GitClient
	path   string
}

// Pool resolves Git clients by repository ID.
//
// The primary Git client is cached at construction so governance
// reads — which dominate every request — never pay the cost of a
// registry round-trip. In clone mode, code repository clients are
// built once on first access and cached; concurrent first access for
// the same repo is coalesced via singleflight so the same remote is
// never cloned twice.
type Pool struct {
	primary  git.GitClient
	resolver Resolver
	factory  ClientFactory

	cloner   Cloner
	repoBase string
	logger   *slog.Logger
	now      func() time.Time

	mu    sync.RWMutex
	cache map[string]cachedClient
	sf    singleflight.Group

	cloneCount    atomic.Int64
	cacheHits     atomic.Int64
	cacheMisses   atomic.Int64
	coalesceCount atomic.Int64
}

// New constructs a Pool. All three core arguments are required: a
// Pool without a primary client is meaningless (the primary is the
// legacy default for governance code paths), without a resolver it
// cannot route by repository ID, and without a factory it has no way
// to materialise code-repo clients. Options enable clone mode and
// supply observability hooks.
func New(primary git.GitClient, resolver Resolver, factory ClientFactory, opts ...Option) (*Pool, error) {
	if primary == nil {
		return nil, fmt.Errorf("gitpool: primary client is required")
	}
	if resolver == nil {
		return nil, fmt.Errorf("gitpool: resolver is required")
	}
	if factory == nil {
		return nil, fmt.Errorf("gitpool: client factory is required")
	}
	p := &Pool{
		primary:  primary,
		resolver: resolver,
		factory:  factory,
		logger:   slog.Default(),
		now:      time.Now,
		cache:    make(map[string]cachedClient),
	}
	for _, opt := range opts {
		opt(p)
	}
	return p, nil
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
// Without WithCloner, Client returns factory(LocalPath) every time.
// With WithCloner, Client lazily clones missing repos, caches the
// resulting client, and dedupes concurrent first access.
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

	if p.cloner == nil {
		return p.factory(repo.LocalPath), nil
	}

	if err := p.validateRepoBase(repo.LocalPath); err != nil {
		return nil, err
	}

	p.mu.RLock()
	if c, ok := p.cache[repositoryID]; ok && c.path == repo.LocalPath {
		p.mu.RUnlock()
		p.cacheHits.Add(1)
		p.logger.Debug("gitpool: cache hit", "repository_id", repositoryID, "local_path", repo.LocalPath)
		return c.client, nil
	}
	p.mu.RUnlock()

	// DoChan (rather than Do) so duplicate waiters can honour their
	// own ctx cancellation while the leader is still cloning. A slow
	// network clone must not pin a request goroutine past its
	// deadline.
	//
	// The clone runs under context.WithoutCancel(ctx) so the
	// underlying clone is not aborted when the leader's caller
	// disconnects or hits its deadline — followers with valid
	// contexts can still benefit from the in-flight clone, and the
	// repo gets cached for future callers. Each caller's select on
	// ctx.Done() below provides per-caller cancellation; cancelling
	// the clone itself requires the process to exit (sufficient for
	// the v0.x model — a global "cancel a clone" lever is out of
	// scope for this task).
	// Key the singleflight on (id, path) rather than id alone so a
	// binding update that rewrites local_path while a clone is in
	// flight does not let the second caller join the leader and
	// receive a client rooted at the old path. Distinct paths
	// produce distinct flights — exactly what we want for the
	// path-changed scenario.
	cloneCtx := context.WithoutCancel(ctx)
	sfKey := repositoryID + "\x00" + repo.LocalPath
	ch := p.sf.DoChan(sfKey, func() (any, error) {
		return p.initClient(cloneCtx, repo)
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-ch:
		if res.Err != nil {
			return nil, res.Err
		}
		if res.Shared {
			p.coalesceCount.Add(1)
			p.logger.Info("gitpool: concurrent first-access coalesced",
				"repository_id", repositoryID, "local_path", repo.LocalPath)
		}
		return res.Val.(git.GitClient), nil
	}
}

// initClient runs under singleflight for repositoryID. It re-checks
// the cache (a follower entering after the leader populated it gets a
// fast return), then stats the local path, clones if absent, and
// stores the freshly built client. A cache entry whose path does not
// match the current binding LocalPath is treated as stale — the
// repository update API can rewrite local_path, and we must not hand
// out a client rooted at a now-orphaned directory.
func (p *Pool) initClient(ctx context.Context, repo *repository.Repository) (git.GitClient, error) {
	p.mu.RLock()
	if c, ok := p.cache[repo.ID]; ok && c.path == repo.LocalPath {
		p.mu.RUnlock()
		return c.client, nil
	}
	p.mu.RUnlock()

	p.cacheMisses.Add(1)

	if !cloneExists(repo.LocalPath) {
		if strings.TrimSpace(repo.CloneURL) == "" {
			return nil, domain.NewError(domain.ErrPrecondition,
				fmt.Sprintf("repository %q has no clone_url and no local clone", repo.ID))
		}
		start := p.now()
		if err := p.cloner.Clone(ctx, repo.CloneURL, repo.LocalPath); err != nil {
			p.logger.Error("gitpool: clone failed",
				"repository_id", repo.ID,
				"local_path", repo.LocalPath,
				"error", err)
			return nil, domain.NewErrorWithCause(domain.ErrUnavailable,
				fmt.Sprintf("repository %q is unavailable: clone failed", repo.ID), err)
		}
		dur := p.now().Sub(start)
		p.cloneCount.Add(1)
		p.logger.Info("gitpool: cloned repository",
			"repository_id", repo.ID,
			"local_path", repo.LocalPath,
			"duration_ms", dur.Milliseconds())
	} else {
		p.logger.Debug("gitpool: reusing existing clone",
			"repository_id", repo.ID, "local_path", repo.LocalPath)
	}

	client := p.factory(repo.LocalPath)

	p.mu.Lock()
	p.cache[repo.ID] = cachedClient{client: client, path: repo.LocalPath}
	p.mu.Unlock()

	return client, nil
}

// validateRepoBase guards against a binding row whose LocalPath
// escapes the workspace's configured repo base. The check is skipped
// if no base was configured (single-workspace mode without
// multi-repo).
//
// Both paths are resolved through filepath.EvalSymlinks before the
// prefix comparison so a symlinked parent directory cannot smuggle
// the target outside the base. The local path may not yet exist on
// first access — the resolver walks up to the deepest existing
// ancestor and re-attaches the missing tail, which is enough to
// expose any symlinked component along the way.
func (p *Pool) validateRepoBase(localPath string) error {
	if p.repoBase == "" {
		return nil
	}
	base, err := resolveAbs(p.repoBase)
	if err != nil {
		return domain.NewErrorWithCause(domain.ErrPrecondition,
			fmt.Sprintf("repo base %q cannot be resolved", p.repoBase), err)
	}
	target, err := resolveAbs(localPath)
	if err != nil {
		return domain.NewErrorWithCause(domain.ErrPrecondition,
			fmt.Sprintf("repository local path %q cannot be resolved", localPath), err)
	}
	if target == base {
		return domain.NewError(domain.ErrPrecondition,
			fmt.Sprintf("repository local path %q must be a subdirectory of repo base %q", localPath, p.repoBase))
	}
	prefix := base + string(filepath.Separator)
	if !strings.HasPrefix(target, prefix) {
		return domain.NewError(domain.ErrPrecondition,
			fmt.Sprintf("repository local path %q is outside the workspace repo base %q", localPath, p.repoBase))
	}
	return nil
}

// resolveAbs returns an absolute, symlink-resolved form of p. When p
// itself does not yet exist (typical for a clone target), the
// resolver walks up to the deepest existing ancestor, resolves that,
// and re-attaches the still-missing tail. The fallback to a plain
// absolute path on resolver error keeps the validator usable even on
// platforms or filesystems where EvalSymlinks misbehaves.
func resolveAbs(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	rest := ""
	cur := abs
	for {
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs, nil
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(resolved, rest), nil
		}
	}
}

// cloneExists reports whether the given path already holds a Git
// working tree. The check is "is there a .git entry under this path"
// — sufficient for the v0.x clone-or-reuse decision; deeper integrity
// checks (e.g. fsck) are deferred.
func cloneExists(localPath string) bool {
	dotGit := filepath.Join(localPath, ".git")
	if _, err := os.Stat(dotGit); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false
		}
		// Any other stat error is treated as "not present"; the
		// subsequent clone attempt will surface the real error
		// (permission denied, ENOTDIR, etc.) with full context.
		return false
	}
	return true
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

// NewCLIClientFactory returns a production ClientFactory that builds
// a *git.CLIClient rooted at the given local path. The returned
// factory threads opts (push token, credential helper, etc.) through
// to every code-repo client so non-primary clients share the same
// auth profile the primary was constructed with. EPIC-003 TASK-006
// extends this with per-binding credential resolution; until then,
// the same process-level options apply uniformly.
func NewCLIClientFactory(opts ...git.CLIOption) ClientFactory {
	return func(localPath string) git.GitClient {
		return git.NewCLIClient(localPath, opts...)
	}
}

// Stats reports pool counters. Exposed for tests and for callers that
// want to surface clone activity through their own metrics pipeline
// without reaching into observe.GlobalMetrics.
type Stats struct {
	Clones    int64
	CacheHits int64
	Misses    int64
	Coalesces int64
}

// Stats returns a snapshot of the pool's counters.
func (p *Pool) Stats() Stats {
	return Stats{
		Clones:    p.cloneCount.Load(),
		CacheHits: p.cacheHits.Load(),
		Misses:    p.cacheMisses.Load(),
		Coalesces: p.coalesceCount.Load(),
	}
}
