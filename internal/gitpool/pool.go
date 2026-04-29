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
//
// cred is the resolved credential for the binding — empty (zero
// Credential) for the primary repo, public code repos, and any
// pool built without a CredentialResolver. Implementations that
// honour cred must inject it via in-memory mechanisms only (e.g.
// GIT_ASKPASS env at command time); writing the token into argv
// or .git/config is forbidden by the credential-redaction
// requirement (TASK-006 ACs).
type ClientFactory func(localPath string, cred Credential) git.GitClient

// Cloner performs a Git clone from a remote URL to a local path with
// optional credentials. The production implementation builds a fresh
// *git.CLIClient per clone so cred can be wired through GIT_ASKPASS
// for that one invocation without leaking into a long-lived shared
// client; tests inject a fake that materialises a directory without
// shelling out.
//
// An empty Credential means "no auth" — clone the URL as-is.
type Cloner interface {
	Clone(ctx context.Context, url, localPath string, cred Credential) error
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

// WithCredentialResolver enables per-binding credential resolution at
// clone/fetch/push time. When configured, the pool calls Resolve once
// per cache miss for code repos with a non-empty CredentialsRef; the
// resolved Credential is threaded into both the Cloner and the
// ClientFactory so authenticated bindings can pull and push without a
// process-wide token. Resolver failure surfaces as a typed
// gitpool.ErrCredentialsUnavailable wrapped in domain.ErrPrecondition.
//
// Without this option, code repos resolve through the plain factory
// path with the zero Credential — appropriate for public mirrors,
// single-tenant deployments using the legacy SPINE_GIT_PUSH_TOKEN env,
// and unit tests that don't exercise credential paths.
func WithCredentialResolver(r CredentialResolver) Option {
	return func(p *Pool) {
		if r != nil {
			p.credResolver = r
		}
	}
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

// cachedClient pairs a built client with the operational fields it
// was constructed against, so a cache hit can confirm the binding has
// not been rewritten under the pool. A change to any of:
//
//   - LocalPath (clone moved)
//   - CredentialsRef (secret reference rotated)
//   - UpdatedAt (binding row touched, e.g. operator-driven rotation
//     behind a stable ref where the secret bytes changed but the
//     ref did not)
//
// evicts the entry on next access. UpdatedAt is the catch-all for
// "anything about the binding changed"; pinning it to the cache key
// is what makes the "credential rotation through binding update"
// AC hold for the same-ref case where AWS or another backend swapped
// values without any user-visible ref change.
type cachedClient struct {
	client    git.GitClient
	path      string
	credRef   string
	updatedAt time.Time
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

	cloner       Cloner
	credResolver CredentialResolver
	repoBase     string
	logger       *slog.Logger
	now          func() time.Time

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
	return p.clientForRepo(ctx, repo)
}

// clientForRepo runs the prepare/cache/clone path against an
// already-resolved repository snapshot. Splitting this out lets
// RepositoryPath share one Lookup with the preparation step — a
// concurrent binding update that rewrites local_path between two
// independent Lookups would otherwise let the gateway return a path
// that was never validated or cloned.
func (p *Pool) clientForRepo(ctx context.Context, repo *repository.Repository) (git.GitClient, error) {
	if repo.LocalPath == "" {
		return nil, domain.NewError(domain.ErrPrecondition,
			fmt.Sprintf("repository %q has no local clone path", repo.ID))
	}

	if p.cloner == nil {
		// Pass-through mode: the pool never touches the filesystem
		// and never caches, so credential resolution would be
		// observable only through the factory's behavior. Tests that
		// exercise this mode pass the zero Credential through; nothing
		// in production wires a CredentialResolver without WithCloner.
		return p.factory(repo.LocalPath, Credential{}), nil
	}

	if err := p.validateRepoBase(repo.LocalPath); err != nil {
		return nil, err
	}

	p.mu.RLock()
	if c, ok := p.cache[repo.ID]; ok && c.path == repo.LocalPath && c.credRef == repo.CredentialsRef && c.updatedAt.Equal(repo.UpdatedAt) {
		p.mu.RUnlock()
		p.cacheHits.Add(1)
		p.logger.Debug("gitpool: cache hit", "repository_id", repo.ID, "local_path", repo.LocalPath)
		return c.client, nil
	}
	p.mu.RUnlock()

	p.cacheMisses.Add(1)

	// Resolve credentials before clone so authenticated bindings can
	// pull from private remotes. Empty CredentialsRef yields the zero
	// Credential, which the cloner and factory treat as a no-op.
	// Resolver failure (missing secret, access denied, store down) is
	// surfaced verbatim — already wrapped as a typed
	// credentials-unavailable error by SecretCredentialResolver. The
	// SecretClient is responsible for redaction; the wrapped error
	// keeps the underlying sentinel matchable via errors.Is.
	cred, err := p.resolveCredential(ctx, repo)
	if err != nil {
		p.logger.Error("gitpool: credential resolution failed",
			"repository_id", repo.ID)
		return nil, err
	}

	if err := p.ensureClone(ctx, repo, cred); err != nil {
		return nil, err
	}

	// Take the write lock for the cache-check-then-build sequence so
	// concurrent first-access callers with identical binding
	// metadata share a single factory call: the first racer misses
	// the cache and builds the client, the rest hit the cache and
	// return the same instance. When metadata differs (a rotated
	// binding's credRef or updatedAt), the racer overwrites with
	// its own factory build, so each rotation cohort still gets a
	// client constructed against its own credential.
	p.mu.Lock()
	if c, ok := p.cache[repo.ID]; ok && c.path == repo.LocalPath && c.credRef == repo.CredentialsRef && c.updatedAt.Equal(repo.UpdatedAt) {
		p.mu.Unlock()
		return c.client, nil
	}
	client := p.factory(repo.LocalPath, cred)
	p.cache[repo.ID] = cachedClient{
		client:    client,
		path:      repo.LocalPath,
		credRef:   repo.CredentialsRef,
		updatedAt: repo.UpdatedAt,
	}
	p.mu.Unlock()

	return client, nil
}

// ensureClone makes sure a Git working tree exists at repo.LocalPath,
// invoking the cloner if not. Concurrent callers with the same target
// path are coalesced via singleflight keyed on (id, path) so callers
// whose binding metadata (credRef, updatedAt) differs do not race
// each other into the same directory. The clone uses cred — whichever
// caller's credential happened to win the singleflight is the one
// authenticating the on-disk clone; subsequent fetch/push goes through
// each caller's own per-binding client and so honours per-caller
// rotation.
func (p *Pool) ensureClone(ctx context.Context, repo *repository.Repository, cred Credential) error {
	if cloneExists(repo.LocalPath) {
		p.logger.Debug("gitpool: reusing existing clone",
			"repository_id", repo.ID, "local_path", repo.LocalPath)
		return nil
	}
	if strings.TrimSpace(repo.CloneURL) == "" {
		return domain.NewError(domain.ErrPrecondition,
			fmt.Sprintf("repository %q has no clone_url and no local clone", repo.ID))
	}

	// DoChan (rather than Do) so duplicate waiters can honour their
	// own ctx cancellation while the leader is still cloning. The
	// clone itself runs under context.WithoutCancel(ctx) so the
	// underlying git invocation is not aborted when the leader's
	// caller disconnects or hits its deadline — followers with live
	// contexts can still benefit from the in-flight clone. Each
	// caller's select on ctx.Done() below provides per-caller
	// cancellation.
	//
	// Key on (id, path) only. Different paths produce different
	// flights — exactly what we want for binding-update path
	// rewrites. Same path + different metadata (credRef, updatedAt)
	// must coalesce: two concurrent `git clone` invocations into the
	// same directory would otherwise race on the filesystem.
	//
	// Known edge case: if a binding rotates while the leader's clone
	// is in flight and the leader's credential happens to fail auth,
	// followers with the rotated credential receive the leader's
	// failure instead of retrying with their own. The next access
	// after that failure re-enters with fresh state and re-resolves.
	// This satisfies the "rotation picked up on next access" AC at
	// the level of caller-visible behavior; tightening it to "next
	// follower automatically retries" would require splitting the
	// singleflight per-credential and re-introduces the
	// concurrent-clone-into-same-path race the (id, path) key
	// closes.
	cloneCtx := context.WithoutCancel(ctx)
	sfKey := repo.ID + "\x00" + repo.LocalPath
	type cloneResult struct{}
	ch := p.sf.DoChan(sfKey, func() (any, error) {
		// Re-check inside the singleflight: a follower entering
		// after the leader's clone completed but before this Do
		// call should not re-clone.
		if cloneExists(repo.LocalPath) {
			return cloneResult{}, nil
		}
		start := p.now()
		if err := p.cloner.Clone(cloneCtx, repo.CloneURL, repo.LocalPath, cred); err != nil {
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
		return cloneResult{}, nil
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case res := <-ch:
		if res.Err != nil {
			return res.Err
		}
		if res.Shared {
			p.coalesceCount.Add(1)
			p.logger.Info("gitpool: concurrent first-access coalesced",
				"repository_id", repo.ID, "local_path", repo.LocalPath)
		}
		return nil
	}
}

// resolveCredential returns the resolved credential for repo. The
// primary repo and any pool without a CredentialResolver short-circuit
// to the empty Credential. A repo whose binding has no CredentialsRef
// also returns empty — public-repo bindings stay free of secret-store
// round-trips. Any other failure is surfaced unchanged so the caller
// can return a typed credentials-unavailable error to the gateway.
func (p *Pool) resolveCredential(ctx context.Context, repo *repository.Repository) (Credential, error) {
	if p.credResolver == nil || repo == nil {
		return Credential{}, nil
	}
	if repo.ID == repository.PrimaryRepositoryID {
		return Credential{}, nil
	}
	return p.credResolver.Resolve(ctx, repo)
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

// RepositoryPath returns the local clone path for repositoryID after
// running the same preparation Client would: in clone mode it
// triggers lazy clone-on-miss and the WithRepoBase validation; in
// pass-through mode it short-circuits. "spine" returns the
// configured primary path via Lookup so callers see one consistent
// source of truth.
//
// Use this from the gateway routing layer that only needs the
// on-disk path (e.g. to pass to git-http-backend). The single
// Lookup snapshot is reused across preparation and the returned
// path so a concurrent binding update that rewrites local_path
// cannot make us return a path that was never validated or cloned.
func (p *Pool) RepositoryPath(ctx context.Context, repositoryID string) (string, error) {
	repo, err := p.resolver.Lookup(ctx, repositoryID)
	if err != nil {
		return "", err
	}
	if repo.LocalPath == "" {
		return "", domain.NewError(domain.ErrPrecondition,
			fmt.Sprintf("repository %q has no local clone path", repositoryID))
	}
	if repositoryID == repository.PrimaryRepositoryID {
		// Primary path is taken from the registry's primary spec
		// directly; no preparation step is required.
		return repo.LocalPath, nil
	}
	if _, err := p.clientForRepo(ctx, repo); err != nil {
		return "", err
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
// factory threads baseOpts (process-wide push token, credential
// helper, etc.) through to every code-repo client so bindings without
// a per-repo credential reference still inherit the workspace's
// default auth profile.
//
// When the binding has a non-empty Credential — resolved via
// gitpool.SecretCredentialResolver from binding.CredentialsRef — the
// factory layers WithPushToken on top of baseOpts so the per-binding
// token wins for fetch and push. The resolved bytes are revealed only
// here, at the boundary with the git CLI; SecretValue's redacting
// String/MarshalJSON/LogValue keep them out of every other output.
func NewCLIClientFactory(baseOpts ...git.CLIOption) ClientFactory {
	return func(localPath string, cred Credential) git.GitClient {
		opts := append([]git.CLIOption{}, baseOpts...)
		if !cred.IsEmpty() {
			// WithoutCredentialHelper before WithPushToken so the
			// per-binding token wins even when baseOpts carries a
			// process-level SPINE_GIT_CREDENTIAL_HELPER. Without this
			// strip, NewCLIClient skips creating the GIT_ASKPASS
			// helper (the helper is non-empty) and the resolved
			// token never reaches fetch/push.
			opts = append(opts,
				git.WithoutCredentialHelper(),
				git.WithPushToken(string(cred.Token.Reveal()), cred.Username))
		}
		return git.NewCLIClient(localPath, opts...)
	}
}

// NewCLICloner returns a production Cloner that shells out to the git
// CLI for every clone, building a fresh *git.CLIClient per call so
// per-binding credentials can be wired through GIT_ASKPASS for that
// one invocation without leaking into a long-lived shared client. The
// temporary client is closed after the clone completes so its askpass
// helper file is removed even on success.
//
// Use this from production wiring (cmd/spine and the workspace pool)
// in place of the previous gitpool.WithCloner(workspacePrimaryClient)
// pattern: that older pattern shared a single CLIClient across every
// code repo, which made it impossible to inject per-binding tokens.
func NewCLICloner(baseOpts ...git.CLIOption) Cloner {
	return &cliCloner{baseOpts: baseOpts}
}

// cliCloner is the default Cloner: per Clone call it constructs a
// throwaway *git.CLIClient configured with baseOpts plus (if cred is
// non-empty) WithPushToken. The throwaway is closed after the clone
// so its temp askpass script is cleaned up; the cached runtime
// client built by ClientFactory is what survives for fetch/push.
type cliCloner struct {
	baseOpts []git.CLIOption
}

func (c *cliCloner) Clone(ctx context.Context, url, localPath string, cred Credential) error {
	opts := append([]git.CLIOption{}, c.baseOpts...)
	if !cred.IsEmpty() {
		// Same per-binding-overrides-helper rule as the factory:
		// strip any inherited credential helper before layering the
		// resolved token, so the askpass path actually engages.
		opts = append(opts,
			git.WithoutCredentialHelper(),
			git.WithPushToken(string(cred.Token.Reveal()), cred.Username))
	}
	client := git.NewCLIClient(localPath, opts...)
	defer client.Close()
	return client.Clone(ctx, url, localPath)
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
