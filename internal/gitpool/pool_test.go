package gitpool_test

import (
	"context"
	"errors"
	"testing"

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
type stubResolver struct {
	lookups  map[string]lookupResult
	active   []repository.Repository
	recorded []string
}

type lookupResult struct {
	repo *repository.Repository
	err  error
}

func (s *stubResolver) Lookup(_ context.Context, id string) (*repository.Repository, error) {
	s.recorded = append(s.recorded, id)
	if r, ok := s.lookups[id]; ok {
		return r.repo, r.err
	}
	return nil, errors.New("test misconfigured: no lookup entry for " + id)
}

func (s *stubResolver) ListActive(_ context.Context) ([]repository.Repository, error) {
	return s.active, nil
}

func newPool(t *testing.T, primary git.GitClient, resolver gitpool.Resolver, factory gitpool.ClientFactory) *gitpool.Pool {
	t.Helper()
	p, err := gitpool.New(primary, resolver, factory)
	if err != nil {
		t.Fatalf("gitpool.New: %v", err)
	}
	return p
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
