package harness

import (
	"context"
	"os/exec"
	"sync"
	"testing"

	"github.com/bszymi/spine/internal/engine"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/repository"
	"github.com/bszymi/spine/internal/testutil"
)

// CodeRepoSpec describes one code repository to provision via
// WithCodeRepos.
type CodeRepoSpec struct {
	// ID is the workspace repository ID (e.g. "billing"). Required.
	ID string
	// DefaultBranch is the repo's default branch. Empty is treated as
	// "main", matching testutil.NewTempRepo's initial-branch contract.
	DefaultBranch string
}

// CodeRepo is the per-repo handle returned by WithCodeRepos. Tests use
// it to look up the on-disk path of the underlying working tree, the
// in-memory repository.Repository the resolver hands back, or to queue
// per-call merge / push failures for partial-merge recovery scenarios.
type CodeRepo struct {
	// ID mirrors the CodeRepoSpec.ID — duplicated here so callers can
	// range over the returned map without re-keying.
	ID string
	// Dir is the absolute path of the on-disk working tree. Stable for
	// the lifetime of the test (anchored to the t passed to
	// WithCodeRepos).
	Dir string
	// Repository is the catalog record installed into the orchestrator's
	// resolver. Tests asserting on per-repo metadata should consult this
	// pointer rather than hard-coding values.
	Repository *repository.Repository

	behavior *codeRepoBehavior
}

// SetNextMergeFailure schedules the next non-abort Merge call against
// this repo to return err instead of invoking the underlying CLI
// client. Once consumed, subsequent merges fall through to the real
// behavior. Calling with err == nil clears any previously-queued
// failure.
//
// MergeOpts with Strategy == "abort" always pass through to the real
// client so engine.attemptCodeRepositoryMerge's post-failure cleanup of
// MERGE_HEAD remains effective regardless of injected behavior.
func (r *CodeRepo) SetNextMergeFailure(err error) {
	r.behavior.queueMergeFailure(err)
}

// SetNextPushFailure schedules the next Push call against this repo to
// return err instead of invoking the underlying CLI client. Behaves
// like SetNextMergeFailure for queueing semantics.
func (r *CodeRepo) SetNextPushFailure(err error) {
	r.behavior.queuePushFailure(err)
}

// Client returns the git.GitClient resolved for this repo by the
// orchestrator's RepositoryGitClients wiring. Tests that drive git
// operations on the on-disk working tree directly — e.g. committing
// fixture files onto the run branch before invoking validation —
// should use this rather than re-deriving a client from Dir, so any
// queued merge / push failures still apply through the same handle.
func (r *CodeRepo) Client() git.GitClient {
	return r.behavior
}

// WithCodeRepos provisions on-disk code repositories and wires
// engine.RepositoryResolver + engine.RepositoryGitClients onto orch in
// a single call. Use it from a scenario step (or a unit test) that
// already constructed the orchestrator via harness.WithRuntimeOrchestrator
// or equivalent — this helper does not own orchestrator construction,
// only its multi-repo wiring.
//
// primary is the scenario harness's own TestRepo (sc.Repo). The
// installed resolver answers Lookup(repository.PrimaryRepositoryID)
// with a record pointing at primary.Dir so a task that explicitly
// declares the primary in `repositories: [spine, ...]` clears
// checkRepositoryPreconditions instead of failing with
// ErrRepositoryNotFound. The git-clients view returns primary.Git for
// the same ID even though the engine routes primary branch operations
// through Orchestrator.git directly today — symmetric wiring keeps the
// helper from accidentally producing a half-wired resolver if a future
// engine path consults RepositoryGitClients for the primary.
//
// The temp directories for code repos are anchored to t so they
// outlive per-step subtests; pass sc.ParentT (not sc.T) from a
// scenario step or branch creation will land in a working tree that
// has already been torn down by the time later steps assert against it.
//
// Calling WithCodeRepos twice on the same orchestrator replaces the
// previous wiring — engine.WithRepositoryResolver and
// WithRepositoryGitClients are setters, and the returned handles from
// the first call become unreachable from the engine.
//
// Example:
//
//	repos := harness.WithCodeRepos(sc.ParentT, sc.Runtime.Orchestrator, sc.Repo,
//	    harness.CodeRepoSpec{ID: "billing"},
//	    harness.CodeRepoSpec{ID: "shipping"},
//	)
//	billingDir := repos["billing"].Dir
//	repos["billing"].SetNextMergeFailure(errors.New("conflict"))
func WithCodeRepos(t *testing.T, orch *engine.Orchestrator, primary *TestRepo, specs ...CodeRepoSpec) map[string]*CodeRepo {
	t.Helper()
	if orch == nil {
		t.Fatalf("harness.WithCodeRepos: orch is nil; build the runtime with WithRuntimeOrchestrator first")
	}
	if primary == nil {
		t.Fatalf("harness.WithCodeRepos: primary is nil; pass the scenario harness's TestRepo (sc.Repo) so the resolver can answer Lookup(%q)", repository.PrimaryRepositoryID)
	}
	if len(specs) == 0 {
		t.Fatalf("harness.WithCodeRepos: at least one CodeRepoSpec is required")
	}
	handles := make(map[string]*CodeRepo, len(specs))
	repos := make(map[string]*repository.Repository, len(specs))
	clients := make(map[string]git.GitClient, len(specs))
	for _, spec := range specs {
		if spec.ID == "" {
			t.Fatalf("harness.WithCodeRepos: CodeRepoSpec.ID is required")
		}
		if spec.ID == repository.PrimaryRepositoryID {
			// The primary repo is owned by the scenario harness itself
			// (primary / sc.Repo). Accepting it as a code spec here
			// would either shadow that wiring with a fresh tempdir or
			// silently override the primary record built below — both
			// are configuration errors, not silent merges.
			t.Fatalf("harness.WithCodeRepos: spec ID %q is reserved for the primary repo; primary working tree is taken from the primary argument", spec.ID)
		}
		if _, dup := handles[spec.ID]; dup {
			t.Fatalf("harness.WithCodeRepos: duplicate CodeRepoSpec.ID %q", spec.ID)
		}
		defaultBranch := spec.DefaultBranch
		if defaultBranch == "" {
			defaultBranch = "main"
		}
		dir := newCodeRepoDir(t, defaultBranch)
		repoRecord := &repository.Repository{
			ID:            spec.ID,
			Kind:          repository.KindCode,
			Status:        "active",
			DefaultBranch: defaultBranch,
			LocalPath:     dir,
		}
		behavior := &codeRepoBehavior{underlying: git.NewCLIClient(dir)}
		handles[spec.ID] = &CodeRepo{
			ID:         spec.ID,
			Dir:        dir,
			Repository: repoRecord,
			behavior:   behavior,
		}
		repos[spec.ID] = repoRecord
		clients[spec.ID] = behavior
	}
	primaryRecord := &repository.Repository{
		ID:            repository.PrimaryRepositoryID,
		Kind:          repository.KindSpine,
		DefaultBranch: "main",
		LocalPath:     primary.Dir,
	}
	orch.WithRepositoryResolver(&fixedRepoResolver{primary: primaryRecord, repos: repos})
	orch.WithRepositoryGitClients(&fixedRepoGitClients{primary: primary.Git, clients: clients})
	return handles
}

// newCodeRepoDir provisions a temporary git working tree whose current
// branch matches defaultBranch. testutil.NewTempRepo always runs
// `git init -b main`, so any non-main default is realised by renaming
// the initial branch in place. Without this rename a spec advertising
// `DefaultBranch: "trunk"` would point the resolver at a ref the
// working tree did not have, and run-start's `RefSHA(repo.DefaultBranch)`
// would fail with a missing-ref error.
func newCodeRepoDir(t *testing.T, defaultBranch string) string {
	t.Helper()
	dir := testutil.NewTempRepo(t)
	if defaultBranch == "" || defaultBranch == "main" {
		return dir
	}
	cmd := exec.Command("git", "-C", dir, "branch", "-m", "main", defaultBranch)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("harness.WithCodeRepos: rename initial branch to %q in %s: %v\n%s",
			defaultBranch, dir, err, out)
	}
	return dir
}

// fixedRepoResolver is the engine.RepositoryResolver shape backing
// WithCodeRepos. The primary record is held separately from the code
// repos map so a task that explicitly declares the primary in
// `repositories: [spine, ...]` resolves through the same call site as
// the (implicit) primary baseline capture, and so a caller that
// accidentally registers an extra "spine" code spec is rejected at
// intake rather than silently overriding the primary view.
type fixedRepoResolver struct {
	primary *repository.Repository
	repos   map[string]*repository.Repository
}

func (r *fixedRepoResolver) Lookup(_ context.Context, id string) (*repository.Repository, error) {
	if id == repository.PrimaryRepositoryID {
		return r.primary, nil
	}
	repo, ok := r.repos[id]
	if !ok {
		return nil, repository.ErrRepositoryNotFound
	}
	return repo, nil
}

// fixedRepoGitClients is the engine.RepositoryGitClients shape backing
// WithCodeRepos. The primary client is held alongside the code-repo
// map for the same reason fixedRepoResolver carries the primary
// record: future engine paths that consult RepositoryGitClients for
// the primary (today the engine uses Orchestrator.git directly) must
// see the harness's primary working tree rather than ErrRepositoryNotFound.
// The map values are *codeRepoBehavior so injected failures stay
// routable on the same handle the test holds.
type fixedRepoGitClients struct {
	primary git.GitClient
	clients map[string]git.GitClient
}

func (c *fixedRepoGitClients) Client(_ context.Context, repoID string) (git.GitClient, error) {
	if repoID == repository.PrimaryRepositoryID {
		return c.primary, nil
	}
	client, ok := c.clients[repoID]
	if !ok {
		return nil, repository.ErrRepositoryNotFound
	}
	return client, nil
}

// codeRepoBehavior wraps a real git.GitClient and intercepts Merge and
// Push so tests can queue per-call failures. Every other GitClient
// method delegates straight through. The interception is single-shot
// per queued failure — once Merge or Push consumes a queued error, the
// next call falls through to the underlying client.
type codeRepoBehavior struct {
	underlying git.GitClient

	mu           sync.Mutex
	mergeFailure error
	mergeQueued  bool
	pushFailure  error
	pushQueued   bool
}

func (b *codeRepoBehavior) queueMergeFailure(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.mergeFailure = err
	b.mergeQueued = err != nil
}

func (b *codeRepoBehavior) queuePushFailure(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pushFailure = err
	b.pushQueued = err != nil
}

func (b *codeRepoBehavior) Clone(ctx context.Context, url, path string) error {
	return b.underlying.Clone(ctx, url, path)
}

func (b *codeRepoBehavior) Commit(ctx context.Context, opts git.CommitOpts) (git.CommitResult, error) {
	return b.underlying.Commit(ctx, opts)
}

func (b *codeRepoBehavior) Merge(ctx context.Context, opts git.MergeOpts) (git.MergeResult, error) {
	if opts.Strategy == "abort" {
		return b.underlying.Merge(ctx, opts)
	}
	b.mu.Lock()
	if b.mergeQueued {
		err := b.mergeFailure
		b.mergeFailure = nil
		b.mergeQueued = false
		b.mu.Unlock()
		return git.MergeResult{}, err
	}
	b.mu.Unlock()
	return b.underlying.Merge(ctx, opts)
}

func (b *codeRepoBehavior) CreateBranch(ctx context.Context, name, base string) error {
	return b.underlying.CreateBranch(ctx, name, base)
}

func (b *codeRepoBehavior) DeleteBranch(ctx context.Context, name string) error {
	return b.underlying.DeleteBranch(ctx, name)
}

func (b *codeRepoBehavior) Diff(ctx context.Context, from, to string) ([]git.FileDiff, error) {
	return b.underlying.Diff(ctx, from, to)
}

func (b *codeRepoBehavior) MergeBase(ctx context.Context, a, c string) (string, error) {
	return b.underlying.MergeBase(ctx, a, c)
}

func (b *codeRepoBehavior) Log(ctx context.Context, opts git.LogOpts) ([]git.CommitInfo, error) {
	return b.underlying.Log(ctx, opts)
}

func (b *codeRepoBehavior) ReadFile(ctx context.Context, ref, path string) ([]byte, error) {
	return b.underlying.ReadFile(ctx, ref, path)
}

func (b *codeRepoBehavior) ListFiles(ctx context.Context, ref, pattern string) ([]string, error) {
	return b.underlying.ListFiles(ctx, ref, pattern)
}

func (b *codeRepoBehavior) Head(ctx context.Context) (string, error) {
	return b.underlying.Head(ctx)
}

func (b *codeRepoBehavior) RefSHA(ctx context.Context, ref string) (string, error) {
	return b.underlying.RefSHA(ctx, ref)
}

func (b *codeRepoBehavior) Push(ctx context.Context, remote, ref string) error {
	b.mu.Lock()
	if b.pushQueued {
		err := b.pushFailure
		b.pushFailure = nil
		b.pushQueued = false
		b.mu.Unlock()
		return err
	}
	b.mu.Unlock()
	return b.underlying.Push(ctx, remote, ref)
}

func (b *codeRepoBehavior) PushBranch(ctx context.Context, remote, branch string) error {
	return b.underlying.PushBranch(ctx, remote, branch)
}

func (b *codeRepoBehavior) DeleteRemoteBranch(ctx context.Context, remote, branch string) error {
	return b.underlying.DeleteRemoteBranch(ctx, remote, branch)
}
