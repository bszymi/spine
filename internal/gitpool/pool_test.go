package gitpool_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/gitpool"
	"github.com/bszymi/spine/internal/repository"
)

// stubClient is a sentinel git.GitClient used only for identity
// comparisons. The pool never calls any of its methods, so we embed
// the interface without implementing it — any unexpected call would
// nil-deref loudly rather than silently succeed.
type stubClient struct{ git.GitClient }

// stubResolver fakes repository.Registry. lookups maps the requested
// ID to either an active Repository (success) or a sentinel error.
// active is what ListActive returns. recorded captures every Lookup
// call so tests can assert short-circuit behavior on the primary ID.
//
// The mutex guards recorded so concurrent first-access tests can hit
// Lookup from many goroutines under -race without false positives.
type stubResolver struct {
	mu       sync.Mutex
	lookups  map[string]lookupResult
	active   []repository.Repository
	recorded []string
}

type lookupResult struct {
	repo *repository.Repository
	err  error
}

func (s *stubResolver) Lookup(_ context.Context, id string) (*repository.Repository, error) {
	s.mu.Lock()
	s.recorded = append(s.recorded, id)
	r, ok := s.lookups[id]
	s.mu.Unlock()
	if ok {
		return r.repo, r.err
	}
	return nil, errors.New("test misconfigured: no lookup entry for " + id)
}

func (s *stubResolver) ListActive(_ context.Context) ([]repository.Repository, error) {
	return s.active, nil
}

func TestNew_RejectsNilDeps(t *testing.T) {
	cases := []struct {
		name     string
		primary  git.GitClient
		resolver gitpool.Resolver
		factory  gitpool.ClientFactory
	}{
		{name: "nil primary", primary: nil, resolver: &stubResolver{}, factory: func(string) git.GitClient { return nil }},
		{name: "nil resolver", primary: &stubClient{}, resolver: nil, factory: func(string) git.GitClient { return nil }},
		{name: "nil factory", primary: &stubClient{}, resolver: &stubResolver{}, factory: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := gitpool.New(tc.primary, tc.resolver, tc.factory); err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
		})
	}
}

func TestPrimaryClient_ReturnsCachedAndSkipsResolver(t *testing.T) {
	primary := &stubClient{}
	resolver := &stubResolver{}
	pool := newPool(t, primary, resolver, func(string) git.GitClient {
		t.Error("factory must not be called for primary")
		return nil
	})

	if pool.PrimaryClient() != primary {
		t.Error("PrimaryClient must return the cached primary instance")
	}
	if len(resolver.recorded) != 0 {
		t.Errorf("PrimaryClient must not consult resolver; recorded %v", resolver.recorded)
	}
}

func TestClient_PrimaryShortCircuits(t *testing.T) {
	// Client(ctx, "spine") must return the cached primary without ever
	// calling Lookup. The legacy fast path depends on this — every
	// governance read would otherwise pay a resolver round-trip.
	primary := &stubClient{}
	resolver := &stubResolver{}
	pool := newPool(t, primary, resolver, func(string) git.GitClient {
		t.Error("factory must not be called for primary")
		return nil
	})

	got, err := pool.Client(context.Background(), repository.PrimaryRepositoryID)
	if err != nil {
		t.Fatalf("Client(spine): %v", err)
	}
	if got != primary {
		t.Error("Client(spine) must return the cached primary instance")
	}
	if len(resolver.recorded) != 0 {
		t.Errorf("Client(spine) must not consult resolver; recorded %v", resolver.recorded)
	}
}

func TestClient_CodeRepoBuildsFromBindingPath(t *testing.T) {
	// A Lookup-resolved repo with a populated LocalPath must be turned
	// into a client by passing the path to the factory unchanged.
	primary := &stubClient{}
	codeClient := &stubClient{}
	resolver := &stubResolver{
		lookups: map[string]lookupResult{
			"payments-service": {repo: &repository.Repository{
				ID: "payments-service", Status: "active",
				LocalPath: "/var/spine/clones/payments-service",
			}},
		},
	}
	var seenPath string
	pool := newPool(t, primary, resolver, func(p string) git.GitClient {
		seenPath = p
		return codeClient
	})

	got, err := pool.Client(context.Background(), "payments-service")
	if err != nil {
		t.Fatalf("Client(payments-service): %v", err)
	}
	if got != codeClient {
		t.Error("Client must return what the factory returned")
	}
	if seenPath != "/var/spine/clones/payments-service" {
		t.Errorf("factory called with %q, want /var/spine/clones/payments-service", seenPath)
	}
}

func TestClient_UnknownErrorPropagates(t *testing.T) {
	notFound := repository.ErrRepositoryNotFound
	resolver := &stubResolver{
		lookups: map[string]lookupResult{
			"ghost-service": {err: notFound},
		},
	}
	pool := newPool(t, &stubClient{}, resolver, func(string) git.GitClient {
		t.Error("factory must not be called when Lookup fails")
		return nil
	})

	_, err := pool.Client(context.Background(), "ghost-service")
	if err == nil {
		t.Fatal("expected error for unknown repository")
	}
	if !errors.Is(err, notFound) {
		t.Errorf("expected ErrRepositoryNotFound to propagate, got %v", err)
	}
}

func TestClient_InactiveErrorPropagates(t *testing.T) {
	inactive := repository.ErrRepositoryInactive
	resolver := &stubResolver{
		lookups: map[string]lookupResult{
			"payments-service": {err: inactive},
		},
	}
	pool := newPool(t, &stubClient{}, resolver, func(string) git.GitClient {
		t.Error("factory must not be called when Lookup fails")
		return nil
	})

	_, err := pool.Client(context.Background(), "payments-service")
	if err == nil {
		t.Fatal("expected error for inactive repository")
	}
	if !errors.Is(err, inactive) {
		t.Errorf("expected ErrRepositoryInactive to propagate, got %v", err)
	}
}

func TestClient_UnboundErrorPropagates(t *testing.T) {
	unbound := repository.ErrRepositoryUnbound
	resolver := &stubResolver{
		lookups: map[string]lookupResult{
			"api-gateway": {err: unbound},
		},
	}
	pool := newPool(t, &stubClient{}, resolver, func(string) git.GitClient {
		t.Error("factory must not be called when Lookup fails")
		return nil
	})

	_, err := pool.Client(context.Background(), "api-gateway")
	if err == nil {
		t.Fatal("expected error for unbound repository")
	}
	if !errors.Is(err, unbound) {
		t.Errorf("expected ErrRepositoryUnbound to propagate, got %v", err)
	}
}

func TestClient_EmptyLocalPathIsPrecondition(t *testing.T) {
	// An "active" binding without a local clone path means the
	// clone-time bootstrap (TASK-002) hasn't run yet for that repo.
	// Returning a factory(""): would silently root the client in the
	// caller's CWD, so the pool must refuse instead.
	resolver := &stubResolver{
		lookups: map[string]lookupResult{
			"payments-service": {repo: &repository.Repository{
				ID: "payments-service", Status: "active", LocalPath: "",
			}},
		},
	}
	pool := newPool(t, &stubClient{}, resolver, func(string) git.GitClient {
		t.Error("factory must not be called when LocalPath is empty")
		return nil
	})

	_, err := pool.Client(context.Background(), "payments-service")
	if err == nil {
		t.Fatal("expected precondition error for empty LocalPath")
	}
}

func TestRepositoryPath_PrimaryThroughResolver(t *testing.T) {
	// RepositoryPath uniformly goes through Lookup so the primary's
	// configured RepoPath is the same single source of truth as the
	// rest of the system. Callers don't have to special-case the
	// primary.
	primaryPath := "/var/spine/repo"
	resolver := &stubResolver{
		lookups: map[string]lookupResult{
			repository.PrimaryRepositoryID: {repo: &repository.Repository{
				ID: repository.PrimaryRepositoryID, LocalPath: primaryPath,
			}},
		},
	}
	pool := newPool(t, &stubClient{}, resolver, func(string) git.GitClient { return nil })

	got, err := pool.RepositoryPath(context.Background(), repository.PrimaryRepositoryID)
	if err != nil {
		t.Fatalf("RepositoryPath(spine): %v", err)
	}
	if got != primaryPath {
		t.Errorf("got %q, want %q", got, primaryPath)
	}
}

func TestRepositoryPath_CodeRepoReturnsBindingLocalPath(t *testing.T) {
	resolver := &stubResolver{
		lookups: map[string]lookupResult{
			"payments-service": {repo: &repository.Repository{
				ID: "payments-service", Status: "active",
				LocalPath: "/var/spine/clones/payments-service",
			}},
		},
	}
	pool := newPool(t, &stubClient{}, resolver, func(string) git.GitClient { return nil })

	got, err := pool.RepositoryPath(context.Background(), "payments-service")
	if err != nil {
		t.Fatalf("RepositoryPath(payments-service): %v", err)
	}
	if got != "/var/spine/clones/payments-service" {
		t.Errorf("got %q, want /var/spine/clones/payments-service", got)
	}
}

func TestRepositoryPath_UnknownErrorPropagates(t *testing.T) {
	resolver := &stubResolver{
		lookups: map[string]lookupResult{
			"ghost-service": {err: repository.ErrRepositoryNotFound},
		},
	}
	pool := newPool(t, &stubClient{}, resolver, func(string) git.GitClient { return nil })

	_, err := pool.RepositoryPath(context.Background(), "ghost-service")
	if !errors.Is(err, repository.ErrRepositoryNotFound) {
		t.Errorf("expected ErrRepositoryNotFound, got %v", err)
	}
}

func TestRepositoryPath_EmptyLocalPathIsPrecondition(t *testing.T) {
	resolver := &stubResolver{
		lookups: map[string]lookupResult{
			"payments-service": {repo: &repository.Repository{
				ID: "payments-service", Status: "active", LocalPath: "",
			}},
		},
	}
	pool := newPool(t, &stubClient{}, resolver, func(string) git.GitClient { return nil })

	_, err := pool.RepositoryPath(context.Background(), "payments-service")
	if err == nil {
		t.Fatal("expected precondition error for empty LocalPath")
	}
}

// fakeCloner materialises a minimal Git working tree (just a .git
// directory) at the requested path. It records every Clone call and
// can optionally block on a release channel so tests can drive
// concurrent first-access scenarios deterministically.
type fakeCloner struct {
	mu       sync.Mutex
	calls    []cloneCall
	release  chan struct{} // when non-nil, Clone blocks until closed
	failWith error
}

type cloneCall struct {
	url  string
	path string
}

func (f *fakeCloner) Clone(ctx context.Context, url, localPath string) error {
	f.mu.Lock()
	f.calls = append(f.calls, cloneCall{url: url, path: localPath})
	rel := f.release
	failWith := f.failWith
	f.mu.Unlock()

	if rel != nil {
		select {
		case <-rel:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if failWith != nil {
		return failWith
	}
	if err := os.MkdirAll(filepath.Join(localPath, ".git"), 0o755); err != nil {
		return err
	}
	return nil
}

func (f *fakeCloner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func TestClient_LazyCloneOnFirstAccess(t *testing.T) {
	// Cloner-mode pool: first Client(...) for a repo whose LocalPath
	// is missing must invoke Cloner.Clone exactly once with the
	// configured CloneURL and LocalPath. The factory is then called
	// against the cloned path to produce a cached client.
	base := t.TempDir()
	localPath := filepath.Join(base, "payments-service")
	cloner := &fakeCloner{}
	codeClient := &stubClient{}
	primary := &stubClient{}
	resolver := &stubResolver{
		lookups: map[string]lookupResult{
			"payments-service": {repo: &repository.Repository{
				ID:        "payments-service",
				Status:    "active",
				LocalPath: localPath,
				CloneURL:  "https://git.example/payments.git",
			}},
		},
	}
	var factoryCalls int
	pool := newPool(t, primary, resolver, func(p string) git.GitClient {
		factoryCalls++
		if p != localPath {
			t.Errorf("factory got %q, want %q", p, localPath)
		}
		return codeClient
	}, gitpool.WithCloner(cloner), gitpool.WithRepoBase(base))

	got, err := pool.Client(context.Background(), "payments-service")
	if err != nil {
		t.Fatalf("Client: %v", err)
	}
	if got != codeClient {
		t.Error("expected factory-built client")
	}
	if cloner.callCount() != 1 {
		t.Errorf("clone calls: got %d, want 1", cloner.callCount())
	}
	if cloner.calls[0].url != "https://git.example/payments.git" || cloner.calls[0].path != localPath {
		t.Errorf("clone called with %+v, want url/path matching binding", cloner.calls[0])
	}
	if factoryCalls != 1 {
		t.Errorf("factory calls: got %d, want 1", factoryCalls)
	}
	if s := pool.Stats(); s.Clones != 1 || s.Misses != 1 {
		t.Errorf("stats after first access: %+v, want Clones=1, Misses=1", s)
	}
}

func TestClient_SubsequentAccessReusesCachedClient(t *testing.T) {
	// After a successful first access, repeated Client() calls must
	// return the same cached instance and never re-invoke the cloner
	// or factory. This is the headline reuse property.
	base := t.TempDir()
	localPath := filepath.Join(base, "payments-service")
	cloner := &fakeCloner{}
	codeClient := &stubClient{}
	resolver := &stubResolver{
		lookups: map[string]lookupResult{
			"payments-service": {repo: &repository.Repository{
				ID: "payments-service", Status: "active",
				LocalPath: localPath, CloneURL: "https://git.example/payments.git",
			}},
		},
	}
	var factoryCalls int32
	pool := newPool(t, &stubClient{}, resolver, func(string) git.GitClient {
		atomic.AddInt32(&factoryCalls, 1)
		return codeClient
	}, gitpool.WithCloner(cloner), gitpool.WithRepoBase(base))

	for i := 0; i < 5; i++ {
		got, err := pool.Client(context.Background(), "payments-service")
		if err != nil {
			t.Fatalf("Client (iter %d): %v", i, err)
		}
		if got != codeClient {
			t.Errorf("iter %d: got different client", i)
		}
	}
	if cloner.callCount() != 1 {
		t.Errorf("clone calls: got %d, want 1 (subsequent calls must reuse cache)", cloner.callCount())
	}
	if got := atomic.LoadInt32(&factoryCalls); got != 1 {
		t.Errorf("factory calls: got %d, want 1", got)
	}
	if s := pool.Stats(); s.CacheHits != 4 {
		t.Errorf("CacheHits: got %d, want 4 (one miss + four hits)", s.CacheHits)
	}
}

func TestClient_ExistingCloneIsReusedWithoutCloning(t *testing.T) {
	// If a valid local clone (.git directory present) already lives
	// at LocalPath, the pool must build a client from it directly
	// rather than re-cloning.
	base := t.TempDir()
	localPath := filepath.Join(base, "shared-libs")
	if err := os.MkdirAll(filepath.Join(localPath, ".git"), 0o755); err != nil {
		t.Fatalf("seed clone: %v", err)
	}
	cloner := &fakeCloner{}
	codeClient := &stubClient{}
	resolver := &stubResolver{
		lookups: map[string]lookupResult{
			"shared-libs": {repo: &repository.Repository{
				ID: "shared-libs", Status: "active",
				LocalPath: localPath, CloneURL: "https://git.example/shared-libs.git",
			}},
		},
	}
	pool := newPool(t, &stubClient{}, resolver, func(string) git.GitClient { return codeClient },
		gitpool.WithCloner(cloner), gitpool.WithRepoBase(base))

	got, err := pool.Client(context.Background(), "shared-libs")
	if err != nil {
		t.Fatalf("Client: %v", err)
	}
	if got != codeClient {
		t.Error("expected factory-built client")
	}
	if cloner.callCount() != 0 {
		t.Errorf("clone calls: got %d, want 0 (existing clone must be reused)", cloner.callCount())
	}
	if s := pool.Stats(); s.Clones != 0 || s.Misses != 1 {
		t.Errorf("stats: %+v, want Clones=0, Misses=1", s)
	}
}

func TestClient_ConcurrentFirstAccessClonesOnce(t *testing.T) {
	// N goroutines hit Client() for the same repo before the clone
	// finishes. Singleflight must coalesce them into a single
	// underlying clone, every caller must receive the same client,
	// and the coalesce counter must observe at least one
	// coalesced call.
	base := t.TempDir()
	localPath := filepath.Join(base, "api-gateway")
	release := make(chan struct{})
	cloner := &fakeCloner{release: release}
	codeClient := &stubClient{}
	resolver := &stubResolver{
		lookups: map[string]lookupResult{
			"api-gateway": {repo: &repository.Repository{
				ID: "api-gateway", Status: "active",
				LocalPath: localPath, CloneURL: "https://git.example/api-gateway.git",
			}},
		},
	}
	var factoryCalls int32
	pool := newPool(t, &stubClient{}, resolver, func(string) git.GitClient {
		atomic.AddInt32(&factoryCalls, 1)
		return codeClient
	}, gitpool.WithCloner(cloner), gitpool.WithRepoBase(base))

	const N = 8
	results := make([]git.GitClient, N)
	errs := make([]error, N)
	var wg sync.WaitGroup
	wg.Add(N)
	start := make(chan struct{})
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = pool.Client(context.Background(), "api-gateway")
		}(i)
	}
	close(start)
	// Wait briefly for callers to enter singleflight before releasing
	// the cloner — otherwise the leader can finish before any
	// follower joins, defeating the coalesce.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	for i := 0; i < N; i++ {
		if errs[i] != nil {
			t.Errorf("goroutine %d: %v", i, errs[i])
		}
		if results[i] != codeClient {
			t.Errorf("goroutine %d: got different client", i)
		}
	}
	if cloner.callCount() != 1 {
		t.Errorf("clone calls: got %d, want 1 (singleflight must coalesce concurrent first access)", cloner.callCount())
	}
	if got := atomic.LoadInt32(&factoryCalls); got != 1 {
		t.Errorf("factory calls: got %d, want 1", got)
	}
	if s := pool.Stats(); s.Coalesces == 0 {
		t.Errorf("Coalesces: got %d, want >= 1", s.Coalesces)
	}
}

func TestClient_CloneFailureSurfacesAsUnavailable(t *testing.T) {
	// A clone failure must turn into a domain.SpineError with code
	// ErrUnavailable so the gateway maps it to HTTP 503. The
	// underlying cause must remain matchable via errors.Is for
	// callers that need to distinguish failure modes (e.g. retry
	// policies).
	base := t.TempDir()
	localPath := filepath.Join(base, "billing")
	cloneErr := errors.New("network unreachable")
	cloner := &fakeCloner{failWith: cloneErr}
	resolver := &stubResolver{
		lookups: map[string]lookupResult{
			"billing": {repo: &repository.Repository{
				ID: "billing", Status: "active",
				LocalPath: localPath, CloneURL: "https://git.example/billing.git",
			}},
		},
	}
	pool := newPool(t, &stubClient{}, resolver, func(string) git.GitClient {
		t.Error("factory must not be called when clone fails")
		return nil
	}, gitpool.WithCloner(cloner), gitpool.WithRepoBase(base))

	_, err := pool.Client(context.Background(), "billing")
	if err == nil {
		t.Fatal("expected clone failure")
	}
	var spineErr *domain.SpineError
	if !errors.As(err, &spineErr) || spineErr.Code != domain.ErrUnavailable {
		t.Errorf("expected ErrUnavailable SpineError, got %v", err)
	}
	if !errors.Is(err, cloneErr) {
		t.Errorf("expected original cause to be matchable via errors.Is, got %v", err)
	}
}

func TestClient_RepoBaseEnforcement(t *testing.T) {
	// LocalPath outside the configured repo base is a precondition
	// failure. This guards against a stray binding row pointing at
	// /tmp or someone else's workspace.
	base := t.TempDir()
	cloner := &fakeCloner{}
	resolver := &stubResolver{
		lookups: map[string]lookupResult{
			"escaped": {repo: &repository.Repository{
				ID: "escaped", Status: "active",
				LocalPath: "/tmp/elsewhere",
				CloneURL:  "https://git.example/escaped.git",
			}},
		},
	}
	pool := newPool(t, &stubClient{}, resolver, func(string) git.GitClient {
		t.Error("factory must not be called when LocalPath escapes repo base")
		return nil
	}, gitpool.WithCloner(cloner), gitpool.WithRepoBase(base))

	_, err := pool.Client(context.Background(), "escaped")
	if err == nil {
		t.Fatal("expected precondition error for path outside repo base")
	}
	var spineErr *domain.SpineError
	if !errors.As(err, &spineErr) || spineErr.Code != domain.ErrPrecondition {
		t.Errorf("expected ErrPrecondition, got %v", err)
	}
	if cloner.callCount() != 0 {
		t.Errorf("cloner must not be invoked when repo base check fails (calls: %d)", cloner.callCount())
	}
}

func TestClient_RepoBaseRejectsSymlinkedEscape(t *testing.T) {
	// A symlinked subdirectory of the repo base that resolves
	// outside it is a real escape, not a legitimate binding. The
	// validator must compare resolved paths so a corrupted binding
	// row cannot bypass the lexical prefix check by routing through
	// a symlink.
	base := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(base, "trojan")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}
	cloner := &fakeCloner{}
	resolver := &stubResolver{
		lookups: map[string]lookupResult{
			"trojan": {repo: &repository.Repository{
				ID: "trojan", Status: "active",
				LocalPath: filepath.Join(link, "code"),
				CloneURL:  "https://git.example/trojan.git",
			}},
		},
	}
	pool := newPool(t, &stubClient{}, resolver, func(string) git.GitClient {
		t.Error("factory must not be called for symlinked-escape path")
		return nil
	}, gitpool.WithCloner(cloner), gitpool.WithRepoBase(base))

	_, err := pool.Client(context.Background(), "trojan")
	if err == nil {
		t.Fatal("expected precondition error for symlinked path that resolves outside repo base")
	}
	var spineErr *domain.SpineError
	if !errors.As(err, &spineErr) || spineErr.Code != domain.ErrPrecondition {
		t.Errorf("expected ErrPrecondition, got %v", err)
	}
	if cloner.callCount() != 0 {
		t.Errorf("cloner must not be invoked when symlink-resolved path escapes base (calls: %d)", cloner.callCount())
	}
}

func TestClient_MissingCloneURLIsPrecondition(t *testing.T) {
	// An empty CloneURL when the local clone is also missing leaves
	// the pool with nothing to do. Surfacing this as a precondition
	// failure (vs. trying to clone "") makes the binding mistake
	// obvious instead of producing an opaque git error.
	base := t.TempDir()
	localPath := filepath.Join(base, "halfbound")
	cloner := &fakeCloner{}
	resolver := &stubResolver{
		lookups: map[string]lookupResult{
			"halfbound": {repo: &repository.Repository{
				ID: "halfbound", Status: "active",
				LocalPath: localPath, CloneURL: "",
			}},
		},
	}
	pool := newPool(t, &stubClient{}, resolver, func(string) git.GitClient {
		t.Error("factory must not be called without clone")
		return nil
	}, gitpool.WithCloner(cloner), gitpool.WithRepoBase(base))

	_, err := pool.Client(context.Background(), "halfbound")
	if err == nil {
		t.Fatal("expected precondition error for missing CloneURL")
	}
	var spineErr *domain.SpineError
	if !errors.As(err, &spineErr) || spineErr.Code != domain.ErrPrecondition {
		t.Errorf("expected ErrPrecondition, got %v", err)
	}
	if cloner.callCount() != 0 {
		t.Errorf("cloner must not be invoked when CloneURL is empty (calls: %d)", cloner.callCount())
	}
}

func TestClient_BindingPathChangeEvictsCache(t *testing.T) {
	// The repository update API can rewrite local_path. After such
	// an update, Lookup returns the new path and the pool must
	// re-clone (or open) at that new location instead of returning
	// the client cached against the old path.
	base := t.TempDir()
	pathA := filepath.Join(base, "service-a")
	pathB := filepath.Join(base, "service-b")
	cloner := &fakeCloner{}
	clientA := &stubClient{}
	clientB := &stubClient{}
	resolver := &stubResolver{
		lookups: map[string]lookupResult{
			"service": {repo: &repository.Repository{
				ID: "service", Status: "active",
				LocalPath: pathA, CloneURL: "https://git.example/service.git",
			}},
		},
	}
	pool := newPool(t, &stubClient{}, resolver, func(p string) git.GitClient {
		switch p {
		case pathA:
			return clientA
		case pathB:
			return clientB
		default:
			t.Errorf("unexpected factory path %q", p)
			return nil
		}
	}, gitpool.WithCloner(cloner), gitpool.WithRepoBase(base))

	gotA, err := pool.Client(context.Background(), "service")
	if err != nil {
		t.Fatalf("first Client: %v", err)
	}
	if gotA != clientA {
		t.Error("first call: expected clientA")
	}

	resolver.mu.Lock()
	resolver.lookups["service"] = lookupResult{repo: &repository.Repository{
		ID: "service", Status: "active",
		LocalPath: pathB, CloneURL: "https://git.example/service.git",
	}}
	resolver.mu.Unlock()

	gotB, err := pool.Client(context.Background(), "service")
	if err != nil {
		t.Fatalf("second Client (after path change): %v", err)
	}
	if gotB != clientB {
		t.Error("after binding update: expected clientB rooted at new LocalPath, got stale clientA")
	}
	if cloner.callCount() != 2 {
		t.Errorf("clone calls: got %d, want 2 (path change must trigger re-clone)", cloner.callCount())
	}
}

func TestClient_DuplicateWaiterRespectsContextCancellation(t *testing.T) {
	// While a slow first clone is in progress, a duplicate caller
	// whose ctx is cancelled must return ctx.Err() promptly rather
	// than blocking until the leader's clone completes. This is the
	// reason Client() uses singleflight.DoChan with a select on
	// ctx.Done() instead of plain Do.
	base := t.TempDir()
	localPath := filepath.Join(base, "slow-repo")
	release := make(chan struct{})
	cloner := &fakeCloner{release: release}
	resolver := &stubResolver{
		lookups: map[string]lookupResult{
			"slow-repo": {repo: &repository.Repository{
				ID: "slow-repo", Status: "active",
				LocalPath: localPath, CloneURL: "https://git.example/slow.git",
			}},
		},
	}
	pool := newPool(t, &stubClient{}, resolver, func(string) git.GitClient { return &stubClient{} },
		gitpool.WithCloner(cloner), gitpool.WithRepoBase(base))

	leaderDone := make(chan struct{})
	go func() {
		defer close(leaderDone)
		_, _ = pool.Client(context.Background(), "slow-repo")
	}()
	// Give the leader time to enter singleflight and block on the
	// cloner's release channel before the follower joins.
	time.Sleep(20 * time.Millisecond)

	followerCtx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before the call
	deadline := time.After(500 * time.Millisecond)
	done := make(chan error, 1)
	go func() {
		_, err := pool.Client(followerCtx, "slow-repo")
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	case <-deadline:
		t.Fatal("duplicate waiter did not honour cancelled ctx within 500ms")
	}

	close(release)
	<-leaderDone
}

func TestClient_LeaderCancellationDoesNotAbortSharedClone(t *testing.T) {
	// When the leader's caller cancels its ctx mid-clone, the
	// underlying clone must continue so a second caller with a live
	// ctx can still receive a working client. The leader returns
	// ctx.Err() to its own caller; the follower receives the cloned
	// client.
	base := t.TempDir()
	localPath := filepath.Join(base, "shared-clone")
	release := make(chan struct{})
	cloner := &fakeCloner{release: release}
	codeClient := &stubClient{}
	resolver := &stubResolver{
		lookups: map[string]lookupResult{
			"shared-clone": {repo: &repository.Repository{
				ID: "shared-clone", Status: "active",
				LocalPath: localPath, CloneURL: "https://git.example/shared.git",
			}},
		},
	}
	pool := newPool(t, &stubClient{}, resolver, func(string) git.GitClient { return codeClient },
		gitpool.WithCloner(cloner), gitpool.WithRepoBase(base))

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderErrCh := make(chan error, 1)
	go func() {
		_, err := pool.Client(leaderCtx, "shared-clone")
		leaderErrCh <- err
	}()
	time.Sleep(20 * time.Millisecond)

	followerErrCh := make(chan error, 1)
	followerClientCh := make(chan git.GitClient, 1)
	go func() {
		c, err := pool.Client(context.Background(), "shared-clone")
		followerClientCh <- c
		followerErrCh <- err
	}()
	time.Sleep(20 * time.Millisecond)

	cancelLeader()
	// Leader returns ctx.Err immediately because its select sees
	// ctx.Done before the singleflight result.
	select {
	case err := <-leaderErrCh:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("leader: expected context.Canceled, got %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("leader did not return on cancel within 500ms")
	}

	// Now release the cloner so the underlying clone (running on a
	// detached ctx) can finish. The follower must still get a
	// working client back.
	close(release)
	select {
	case err := <-followerErrCh:
		if err != nil {
			t.Errorf("follower: expected success, got %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("follower did not complete after clone release")
	}
	if got := <-followerClientCh; got != codeClient {
		t.Error("follower: expected the cloned client")
	}
	if cloner.callCount() != 1 {
		t.Errorf("clone calls: got %d, want 1", cloner.callCount())
	}
}

func TestClient_PathChangeMidFlightDoesNotJoinLeaderFlight(t *testing.T) {
	// While a clone for LocalPath=A is in flight, the binding is
	// updated to LocalPath=B and a second caller arrives. The
	// second caller must NOT join the leader's singleflight (which
	// would yield a client rooted at A); it must start its own
	// flight rooted at B. Two distinct flights => two clones, each
	// at the right path.
	base := t.TempDir()
	pathA := filepath.Join(base, "alpha")
	pathB := filepath.Join(base, "beta")
	clientA := &stubClient{}
	clientB := &stubClient{}
	releaseA := make(chan struct{})
	cloner := &fakeCloner{release: releaseA}
	resolver := &stubResolver{
		lookups: map[string]lookupResult{
			"shifty": {repo: &repository.Repository{
				ID: "shifty", Status: "active",
				LocalPath: pathA, CloneURL: "https://git.example/shifty.git",
			}},
		},
	}
	pool := newPool(t, &stubClient{}, resolver, func(p string) git.GitClient {
		switch p {
		case pathA:
			return clientA
		case pathB:
			return clientB
		default:
			t.Errorf("unexpected factory path %q", p)
			return nil
		}
	}, gitpool.WithCloner(cloner), gitpool.WithRepoBase(base))

	leaderResultCh := make(chan git.GitClient, 1)
	go func() {
		c, err := pool.Client(context.Background(), "shifty")
		if err != nil {
			t.Errorf("leader: %v", err)
		}
		leaderResultCh <- c
	}()
	time.Sleep(20 * time.Millisecond)

	resolver.mu.Lock()
	resolver.lookups["shifty"] = lookupResult{repo: &repository.Repository{
		ID: "shifty", Status: "active",
		LocalPath: pathB, CloneURL: "https://git.example/shifty.git",
	}}
	resolver.mu.Unlock()

	// The follower's flight is keyed on pathB, so it does NOT
	// coalesce with the leader's flight on pathA. The follower's
	// clone uses the same fakeCloner instance — once releaseA fires,
	// both clones complete (the cloner's release channel is shared,
	// but each Clone call observes the close once).
	followerResultCh := make(chan git.GitClient, 1)
	go func() {
		c, err := pool.Client(context.Background(), "shifty")
		if err != nil {
			t.Errorf("follower: %v", err)
		}
		followerResultCh <- c
	}()
	time.Sleep(20 * time.Millisecond)

	close(releaseA)
	leaderClient := <-leaderResultCh
	followerClient := <-followerResultCh

	if leaderClient != clientA {
		t.Errorf("leader: got client rooted at %T, want clientA (rooted at %s)", leaderClient, pathA)
	}
	if followerClient != clientB {
		t.Errorf("follower: got client rooted at wrong path; mid-flight binding update must not join leader's flight")
	}
	if cloner.callCount() != 2 {
		t.Errorf("clone calls: got %d, want 2 (two distinct paths => two flights)", cloner.callCount())
	}
}

func TestClient_PrimaryShortCircuitsEvenInCloneMode(t *testing.T) {
	// Enabling clone mode must not change the primary fast path:
	// PrimaryClient and Client(spine) still skip the resolver and
	// the cloner.
	primary := &stubClient{}
	cloner := &fakeCloner{}
	resolver := &stubResolver{}
	pool := newPool(t, primary, resolver, func(string) git.GitClient {
		t.Error("factory must not be called for primary")
		return nil
	}, gitpool.WithCloner(cloner), gitpool.WithRepoBase(t.TempDir()))

	got, err := pool.Client(context.Background(), repository.PrimaryRepositoryID)
	if err != nil {
		t.Fatalf("Client(spine): %v", err)
	}
	if got != primary {
		t.Error("Client(spine) must return cached primary")
	}
	if cloner.callCount() != 0 {
		t.Error("primary path must not invoke cloner")
	}
	if len(resolver.recorded) != 0 {
		t.Errorf("primary path must not consult resolver; recorded %v", resolver.recorded)
	}
}

func newPool(t *testing.T, primary git.GitClient, resolver gitpool.Resolver, factory gitpool.ClientFactory, opts ...gitpool.Option) *gitpool.Pool {
	t.Helper()
	p, err := gitpool.New(primary, resolver, factory, opts...)
	if err != nil {
		t.Fatalf("gitpool.New: %v", err)
	}
	return p
}

func TestListActive_DelegatesToResolver(t *testing.T) {
	want := []repository.Repository{
		{ID: repository.PrimaryRepositoryID, Status: "active"},
		{ID: "payments-service", Status: "active"},
	}
	resolver := &stubResolver{active: want}
	pool := newPool(t, &stubClient{}, resolver, func(string) git.GitClient { return nil })

	got, err := pool.ListActive(context.Background())
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i].ID != want[i].ID {
			t.Errorf("ListActive[%d].ID: got %q, want %q", i, got[i].ID, want[i].ID)
		}
	}
}
