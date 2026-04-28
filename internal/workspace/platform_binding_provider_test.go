package workspace_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/secrets"
	"github.com/bszymi/spine/internal/workspace"
)

// fakeSecretClient is a programmable SecretClient for tests.
type fakeSecretClient struct {
	values map[secrets.SecretRef]string
	errs   map[secrets.SecretRef]error
	calls  atomic.Int32
}

func (f *fakeSecretClient) Get(_ context.Context, ref secrets.SecretRef) (secrets.SecretValue, secrets.VersionID, error) {
	f.calls.Add(1)
	if err, ok := f.errs[ref]; ok {
		return secrets.SecretValue{}, "", err
	}
	if v, ok := f.values[ref]; ok {
		return secrets.NewSecretValue([]byte(v)), "v1", nil
	}
	return secrets.SecretValue{}, "", secrets.ErrSecretNotFound
}

func (f *fakeSecretClient) Invalidate(_ context.Context, _ secrets.SecretRef) error { return nil }

// bindingFor returns a binding referring to refs the fakeSecretClient seeds below.
func bindingFor(workspaceID string) workspace.PlatformBinding {
	return workspace.PlatformBinding{
		WorkspaceID:      workspaceID,
		DisplayName:      workspaceID + " ws",
		SpineAPIURL:      "https://spine.example.com",
		SpineWorkspaceID: workspaceID + "-smp",
		DeploymentMode:   "shared",
		RepoPath:         "/var/spine/repos/" + workspaceID,
		ActorScope:       "workspace:" + workspaceID,
		RuntimeDBRef:     secrets.WorkspaceRef(workspaceID, secrets.PurposeRuntimeDB),
		ProjectionDBRef:  secrets.WorkspaceRef(workspaceID, secrets.PurposeProjectionDB),
		GitRef:           secrets.WorkspaceRef(workspaceID, secrets.PurposeGit),
	}
}

func seedFakeSecrets(workspaceID string) *fakeSecretClient {
	return &fakeSecretClient{
		values: map[secrets.SecretRef]string{
			secrets.WorkspaceRef(workspaceID, secrets.PurposeRuntimeDB):    "postgres://runtime/" + workspaceID,
			secrets.WorkspaceRef(workspaceID, secrets.PurposeProjectionDB): "postgres://projection/" + workspaceID,
			secrets.WorkspaceRef(workspaceID, secrets.PurposeGit):          "git-pat-" + workspaceID,
		},
	}
}

// platformServer wraps httptest.Server and counts requests.
type platformServer struct {
	server   *httptest.Server
	hits     atomic.Int32
	bindings map[string]workspace.PlatformBinding
	etag     string
	// failNext set to >0 fails that many requests in a row with 500.
	failNext atomic.Int32
	// requireToken, if set, must match the bearer token.
	requireToken string
}

func newPlatformServer(t *testing.T, bindings map[string]workspace.PlatformBinding) *platformServer {
	t.Helper()
	ps := &platformServer{
		bindings: bindings,
		etag:     `"v1"`,
	}
	ps.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ps.hits.Add(1)
		if ps.requireToken != "" && r.Header.Get("Authorization") != "Bearer "+ps.requireToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if ps.failNext.Load() > 0 {
			ps.failNext.Add(-1)
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		// Path: /api/v1/internal/workspaces/{ws}/runtime-binding
		const prefix = "/api/v1/internal/workspaces/"
		if len(r.URL.Path) <= len(prefix) || r.URL.Path[:len(prefix)] != prefix {
			http.NotFound(w, r)
			return
		}
		// rest is shaped "{ws}/runtime-binding".
		rest := r.URL.Path[len(prefix):]
		const suffix = "/runtime-binding"
		if len(rest) <= len(suffix) || rest[len(rest)-len(suffix):] != suffix {
			http.NotFound(w, r)
			return
		}
		ws := rest[:len(rest)-len(suffix)]
		binding, ok := ps.bindings[ws]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("If-None-Match") == ps.etag {
			w.Header().Set("ETag", ps.etag)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", ps.etag)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(binding)
	}))
	t.Cleanup(ps.server.Close)
	return ps
}

func newProvider(t *testing.T, ps *platformServer, sc secrets.SecretClient) *workspace.PlatformBindingProvider {
	t.Helper()
	p, err := workspace.NewPlatformBindingProvider(workspace.PlatformBindingConfig{
		PlatformBaseURL: ps.server.URL,
		ServiceToken:    "tok",
		SecretClient:    sc,
		HTTPClient:      ps.server.Client(),
		CacheTTL:        50 * time.Millisecond,
		StaleGrace:      200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewPlatformBindingProvider: %v", err)
	}
	return p
}

func TestNewPlatformBindingProvider_RequiresFields(t *testing.T) {
	cases := []struct {
		name string
		cfg  workspace.PlatformBindingConfig
	}{
		{"missing url", workspace.PlatformBindingConfig{ServiceToken: "t", SecretClient: &fakeSecretClient{}}},
		{"missing token", workspace.PlatformBindingConfig{PlatformBaseURL: "http://x", SecretClient: &fakeSecretClient{}}},
		{"missing secret client", workspace.PlatformBindingConfig{PlatformBaseURL: "http://x", ServiceToken: "t"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := workspace.NewPlatformBindingProvider(tc.cfg); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

func TestPlatformBindingProvider_ResolveSuccess(t *testing.T) {
	ps := newPlatformServer(t, map[string]workspace.PlatformBinding{"acme": bindingFor("acme")})
	sc := seedFakeSecrets("acme")
	p := newProvider(t, ps, sc)

	cfg, err := p.Resolve(context.Background(), "acme")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.ID != "acme" {
		t.Fatalf("ID = %q", cfg.ID)
	}
	if got := string(cfg.DatabaseURL.Reveal()); got != "postgres://runtime/acme" {
		t.Fatalf("DatabaseURL = %q", got)
	}
	if cfg.SMPWorkspaceID != "acme-smp" {
		t.Fatalf("SMPWorkspaceID = %q", cfg.SMPWorkspaceID)
	}
	if cfg.Status != workspace.StatusActive {
		t.Fatalf("Status = %q", cfg.Status)
	}
	// Three refs (runtime, projection, git) per Resolve.
	if got := sc.calls.Load(); got != 3 {
		t.Fatalf("SecretClient.Get calls = %d, want 3", got)
	}
}

func TestPlatformBindingProvider_CachesBindingButRefetchesSecrets(t *testing.T) {
	ps := newPlatformServer(t, map[string]workspace.PlatformBinding{"acme": bindingFor("acme")})
	sc := seedFakeSecrets("acme")
	p := newProvider(t, ps, sc)

	if _, err := p.Resolve(context.Background(), "acme"); err != nil {
		t.Fatalf("Resolve 1: %v", err)
	}
	if _, err := p.Resolve(context.Background(), "acme"); err != nil {
		t.Fatalf("Resolve 2: %v", err)
	}

	if got := ps.hits.Load(); got != 1 {
		t.Fatalf("platform hits = %d, want 1 (binding cached)", got)
	}
	// Per ADR-011, secrets are dereferenced on every Resolve so
	// in-place rotations propagate.
	if got := sc.calls.Load(); got != 6 {
		t.Fatalf("SecretClient.Get calls = %d, want 6 (3 refs × 2 resolves)", got)
	}
}

func TestPlatformBindingProvider_RefetchesAfterTTL(t *testing.T) {
	ps := newPlatformServer(t, map[string]workspace.PlatformBinding{"acme": bindingFor("acme")})
	sc := seedFakeSecrets("acme")
	p := newProvider(t, ps, sc)

	if _, err := p.Resolve(context.Background(), "acme"); err != nil {
		t.Fatalf("Resolve 1: %v", err)
	}
	time.Sleep(80 * time.Millisecond) // > CacheTTL=50ms
	if _, err := p.Resolve(context.Background(), "acme"); err != nil {
		t.Fatalf("Resolve 2: %v", err)
	}

	if got := ps.hits.Load(); got != 2 {
		t.Fatalf("platform hits = %d, want 2 (TTL refresh)", got)
	}
}

func TestPlatformBindingProvider_NotModifiedReusesCached(t *testing.T) {
	ps := newPlatformServer(t, map[string]workspace.PlatformBinding{"acme": bindingFor("acme")})
	sc := seedFakeSecrets("acme")
	p := newProvider(t, ps, sc)

	if _, err := p.Resolve(context.Background(), "acme"); err != nil {
		t.Fatalf("Resolve 1: %v", err)
	}
	time.Sleep(80 * time.Millisecond)
	cfg, err := p.Resolve(context.Background(), "acme")
	if err != nil {
		t.Fatalf("Resolve 2: %v", err)
	}
	if got := string(cfg.DatabaseURL.Reveal()); got != "postgres://runtime/acme" {
		t.Fatalf("DatabaseURL = %q", got)
	}
	if got := ps.hits.Load(); got != 2 {
		t.Fatalf("platform hits = %d, want 2", got)
	}
}

func TestPlatformBindingProvider_NotFoundDropsCache(t *testing.T) {
	bindings := map[string]workspace.PlatformBinding{"acme": bindingFor("acme")}
	ps := newPlatformServer(t, bindings)
	sc := seedFakeSecrets("acme")
	p := newProvider(t, ps, sc)

	// Prime cache.
	if _, err := p.Resolve(context.Background(), "acme"); err != nil {
		t.Fatalf("prime: %v", err)
	}
	// Remove on the server side.
	delete(bindings, "acme")
	time.Sleep(80 * time.Millisecond) // expire TTL

	_, err := p.Resolve(context.Background(), "acme")
	if !errors.Is(err, workspace.ErrWorkspaceNotFound) {
		t.Fatalf("expected ErrWorkspaceNotFound, got %v", err)
	}
}

func TestPlatformBindingProvider_AccessDeniedSurfacesUnavailable(t *testing.T) {
	ps := newPlatformServer(t, map[string]workspace.PlatformBinding{"acme": bindingFor("acme")})
	ps.requireToken = "real-token"
	sc := seedFakeSecrets("acme")
	p, err := workspace.NewPlatformBindingProvider(workspace.PlatformBindingConfig{
		PlatformBaseURL: ps.server.URL,
		ServiceToken:    "wrong-token",
		SecretClient:    sc,
		HTTPClient:      ps.server.Client(),
		CacheTTL:        50 * time.Millisecond,
		StaleGrace:      200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewPlatformBindingProvider: %v", err)
	}
	_, err = p.Resolve(context.Background(), "acme")
	if !errors.Is(err, workspace.ErrWorkspaceUnavailable) {
		t.Fatalf("expected ErrWorkspaceUnavailable, got %v", err)
	}
}

func TestPlatformBindingProvider_PlatformDownServesStaleWithinGrace(t *testing.T) {
	ps := newPlatformServer(t, map[string]workspace.PlatformBinding{"acme": bindingFor("acme")})
	sc := seedFakeSecrets("acme")
	p := newProvider(t, ps, sc)

	if _, err := p.Resolve(context.Background(), "acme"); err != nil {
		t.Fatalf("prime: %v", err)
	}

	// Force the next refresh to fail; cached binding should still serve.
	ps.failNext.Store(10)
	time.Sleep(80 * time.Millisecond) // > TTL=50ms but < TTL+grace=250ms

	cfg, err := p.Resolve(context.Background(), "acme")
	if err != nil {
		t.Fatalf("expected stale-on-error to succeed, got %v", err)
	}
	if got := string(cfg.DatabaseURL.Reveal()); got != "postgres://runtime/acme" {
		t.Fatalf("stale DatabaseURL wrong: %q", got)
	}
}

func TestPlatformBindingProvider_PlatformDownAfterGraceFails(t *testing.T) {
	ps := newPlatformServer(t, map[string]workspace.PlatformBinding{"acme": bindingFor("acme")})
	sc := seedFakeSecrets("acme")
	p := newProvider(t, ps, sc)

	if _, err := p.Resolve(context.Background(), "acme"); err != nil {
		t.Fatalf("prime: %v", err)
	}

	ps.failNext.Store(10)
	// CacheTTL=50ms, StaleGrace=200ms — past 350ms we are well outside the grace window.
	time.Sleep(350 * time.Millisecond)

	_, err := p.Resolve(context.Background(), "acme")
	if !errors.Is(err, workspace.ErrWorkspaceUnavailable) {
		t.Fatalf("expected ErrWorkspaceUnavailable past grace, got %v", err)
	}
}

func TestPlatformBindingProvider_PlatformDownUncachedFailsImmediately(t *testing.T) {
	ps := newPlatformServer(t, map[string]workspace.PlatformBinding{"acme": bindingFor("acme")})
	sc := seedFakeSecrets("acme")
	p := newProvider(t, ps, sc)

	ps.failNext.Store(10)
	_, err := p.Resolve(context.Background(), "acme")
	if !errors.Is(err, workspace.ErrWorkspaceUnavailable) {
		t.Fatalf("expected ErrWorkspaceUnavailable on cold-start outage, got %v", err)
	}
}

func TestPlatformBindingProvider_SecretStoreDownIsUnavailable(t *testing.T) {
	ps := newPlatformServer(t, map[string]workspace.PlatformBinding{"acme": bindingFor("acme")})
	sc := seedFakeSecrets("acme")
	sc.errs = map[secrets.SecretRef]error{
		secrets.WorkspaceRef("acme", secrets.PurposeRuntimeDB): secrets.ErrSecretStoreDown,
	}
	p := newProvider(t, ps, sc)

	_, err := p.Resolve(context.Background(), "acme")
	if !errors.Is(err, workspace.ErrWorkspaceUnavailable) {
		t.Fatalf("expected ErrWorkspaceUnavailable, got %v", err)
	}
}

func TestPlatformBindingProvider_SecretAccessDeniedIsUnavailable(t *testing.T) {
	ps := newPlatformServer(t, map[string]workspace.PlatformBinding{"acme": bindingFor("acme")})
	sc := seedFakeSecrets("acme")
	sc.errs = map[secrets.SecretRef]error{
		secrets.WorkspaceRef("acme", secrets.PurposeGit): secrets.ErrAccessDenied,
	}
	p := newProvider(t, ps, sc)

	_, err := p.Resolve(context.Background(), "acme")
	if !errors.Is(err, workspace.ErrWorkspaceUnavailable) {
		t.Fatalf("expected ErrWorkspaceUnavailable, got %v", err)
	}
}

func TestPlatformBindingProvider_SecretNotFoundIsUnavailable(t *testing.T) {
	ps := newPlatformServer(t, map[string]workspace.PlatformBinding{"acme": bindingFor("acme")})
	sc := seedFakeSecrets("acme")
	sc.errs = map[secrets.SecretRef]error{
		secrets.WorkspaceRef("acme", secrets.PurposeProjectionDB): secrets.ErrSecretNotFound,
	}
	p := newProvider(t, ps, sc)

	_, err := p.Resolve(context.Background(), "acme")
	if !errors.Is(err, workspace.ErrWorkspaceUnavailable) {
		t.Fatalf("expected ErrWorkspaceUnavailable, got %v", err)
	}
}

func TestPlatformBindingProvider_InvalidWorkspaceIDIsNotFound(t *testing.T) {
	ps := newPlatformServer(t, nil)
	sc := &fakeSecretClient{}
	p := newProvider(t, ps, sc)

	_, err := p.Resolve(context.Background(), "../../etc/passwd")
	if !errors.Is(err, workspace.ErrWorkspaceNotFound) {
		t.Fatalf("expected ErrWorkspaceNotFound for traversal id, got %v", err)
	}
	if got := ps.hits.Load(); got != 0 {
		t.Fatalf("platform was hit (%d) for invalid id — must reject before fetch", got)
	}
}

func TestPlatformBindingProvider_Invalidate(t *testing.T) {
	ps := newPlatformServer(t, map[string]workspace.PlatformBinding{"acme": bindingFor("acme")})
	sc := seedFakeSecrets("acme")
	p := newProvider(t, ps, sc)

	if _, err := p.Resolve(context.Background(), "acme"); err != nil {
		t.Fatalf("prime: %v", err)
	}
	p.Invalidate("acme")
	if _, err := p.Resolve(context.Background(), "acme"); err != nil {
		t.Fatalf("Resolve after Invalidate: %v", err)
	}
	if got := ps.hits.Load(); got != 2 {
		t.Fatalf("platform hits = %d, want 2 (Invalidate forces refetch)", got)
	}
}

func TestPlatformBindingProvider_AuthHeaderIsBearer(t *testing.T) {
	var seenAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(bindingFor("acme"))
	}))
	t.Cleanup(srv.Close)
	sc := seedFakeSecrets("acme")
	p, err := workspace.NewPlatformBindingProvider(workspace.PlatformBindingConfig{
		PlatformBaseURL: srv.URL,
		ServiceToken:    "secret-token",
		SecretClient:    sc,
		HTTPClient:      srv.Client(),
	})
	if err != nil {
		t.Fatalf("NewPlatformBindingProvider: %v", err)
	}
	if _, err := p.Resolve(context.Background(), "acme"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if seenAuth != "Bearer secret-token" {
		t.Fatalf("Authorization header = %q", seenAuth)
	}
}

func TestPlatformBindingProvider_CombinedInvalidatorDropsCache(t *testing.T) {
	// End-to-end check that CombinedBindingInvalidator forces a
	// refetch on the next Resolve and (if a pool is wired) evicts
	// the corresponding pool entry.
	ps := newPlatformServer(t, map[string]workspace.PlatformBinding{"acme": bindingFor("acme")})
	sc := seedFakeSecrets("acme")
	p := newProvider(t, ps, sc)

	if _, err := p.Resolve(context.Background(), "acme"); err != nil {
		t.Fatalf("prime: %v", err)
	}

	inv := &workspace.CombinedBindingInvalidator{Provider: p}
	inv.InvalidateBinding("acme")

	// Expect a fresh platform fetch on the next Resolve, even though
	// the TTL has not elapsed.
	if _, err := p.Resolve(context.Background(), "acme"); err != nil {
		t.Fatalf("post-invalidate Resolve: %v", err)
	}
	if got := ps.hits.Load(); got != 2 {
		t.Fatalf("platform hits = %d, want 2 (CombinedBindingInvalidator must force refetch)", got)
	}
}

func TestPlatformBindingProvider_RejectsBindingForDifferentWorkspace(t *testing.T) {
	// Platform returns a binding whose workspace_id does not match the
	// requested workspace. This must not be cached and must surface as
	// ErrWorkspaceUnavailable so an attacker (or a stale platform
	// response) cannot route one workspace's request to another's
	// credentials.
	wrongBinding := bindingFor("globex") // top-level + refs all name "globex"
	ps := newPlatformServer(t, map[string]workspace.PlatformBinding{"acme": wrongBinding})
	sc := seedFakeSecrets("globex")
	p := newProvider(t, ps, sc)

	_, err := p.Resolve(context.Background(), "acme")
	if !errors.Is(err, workspace.ErrWorkspaceUnavailable) {
		t.Fatalf("expected ErrWorkspaceUnavailable for cross-tenant binding, got %v", err)
	}
	// Critically: the bad binding must not be in the cache.
	got, _ := p.List(context.Background())
	if len(got) != 0 {
		t.Fatalf("mismatched binding should not be cached, got %+v", got)
	}
	// Confirm secrets were never fetched.
	if got := sc.calls.Load(); got != 0 {
		t.Fatalf("SecretClient must not be called for mismatched binding (got %d calls)", got)
	}
}

func TestPlatformBindingProvider_RejectsBindingWithMismatchedRef(t *testing.T) {
	// Top-level workspace_id matches, but a secret ref names another
	// workspace. Still must be rejected.
	binding := bindingFor("acme")
	binding.GitRef = secrets.WorkspaceRef("globex", secrets.PurposeGit)
	ps := newPlatformServer(t, map[string]workspace.PlatformBinding{"acme": binding})
	sc := seedFakeSecrets("acme")
	p := newProvider(t, ps, sc)

	_, err := p.Resolve(context.Background(), "acme")
	if !errors.Is(err, workspace.ErrWorkspaceUnavailable) {
		t.Fatalf("expected ErrWorkspaceUnavailable for cross-tenant ref, got %v", err)
	}
}

func TestPlatformBindingProvider_ListReturnsCachedOnly(t *testing.T) {
	ps := newPlatformServer(t, map[string]workspace.PlatformBinding{
		"acme":   bindingFor("acme"),
		"globex": bindingFor("globex"),
	})
	// Seed both ws's secrets.
	sc := &fakeSecretClient{
		values: map[secrets.SecretRef]string{
			secrets.WorkspaceRef("acme", secrets.PurposeRuntimeDB):      "postgres://runtime/acme",
			secrets.WorkspaceRef("acme", secrets.PurposeProjectionDB):   "postgres://projection/acme",
			secrets.WorkspaceRef("acme", secrets.PurposeGit):            "git-pat-acme",
			secrets.WorkspaceRef("globex", secrets.PurposeRuntimeDB):    "postgres://runtime/globex",
			secrets.WorkspaceRef("globex", secrets.PurposeProjectionDB): "postgres://projection/globex",
			secrets.WorkspaceRef("globex", secrets.PurposeGit):          "git-pat-globex",
		},
	}
	p := newProvider(t, ps, sc)

	got, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List on empty cache: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("List on empty cache = %d entries, want 0", len(got))
	}

	// Resolve one and re-list.
	if _, err := p.Resolve(context.Background(), "acme"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	got, err = p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].ID != "acme" {
		t.Fatalf("List = %+v, want [acme]", got)
	}
}
