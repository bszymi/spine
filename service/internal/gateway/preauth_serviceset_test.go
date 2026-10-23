package gateway_test

// INIT-024 TASK-007, review finding N-15 (standalone shared mode): the
// workspace middleware used to build the workspace's ServiceSet — its
// database pool, bootstrap admin and secret sweep — before authMiddleware
// had seen a bearer, and a build failure answered 500 where a missing
// workspace answered the uniform 401. Every pre-authentication refusal in
// shared mode is now one status and one body, and a request without a
// well-formed bearer never reaches servicePool.Get.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/auth"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/service/internal/gateway"
	"github.com/bszymi/spine/service/internal/githttp"
	"github.com/bszymi/spine/service/internal/workspace"
)

// countingResolver is a workspace.Resolver over a fixed table. The pool's
// instance counts Resolve calls, which ServicePool.Get makes once per
// call — so its count is the number of servicePool.Get calls.
type countingResolver struct {
	mu    sync.Mutex
	calls map[string]int
	known map[string]workspace.Config
	errs  map[string]error
}

func (c *countingResolver) Resolve(_ context.Context, id string) (*workspace.Config, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls[id]++
	if err, ok := c.errs[id]; ok {
		return nil, err
	}
	cfg, ok := c.known[id]
	if !ok {
		return nil, workspace.ErrWorkspaceNotFound
	}
	return &cfg, nil
}

func (c *countingResolver) List(context.Context) ([]workspace.Config, error) { return nil, nil }

func (c *countingResolver) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, v := range c.calls {
		n += v
	}
	return n
}

// tokenStoreErrStore answers every token lookup with an infrastructure
// error (a database that went away after the ServiceSet was built).
type tokenStoreErrStore struct{ *fakeStore }

func (tokenStoreErrStore) GetActorByTokenHash(context.Context, string) (*domain.Actor, *domain.Token, error) {
	return nil, nil, errors.New("connection refused")
}

// preauthEnv is a shared-mode gateway: a counting resolver for the
// middleware, a second counting resolver behind a real ServicePool, and a
// Builder that wires a fake workspace store — or fails, per workspace.
type preauthEnv struct {
	ts      *httptest.Server
	pool    *countingResolver
	builds  atomic.Int32
	broken  sync.Map // workspace id -> struct{}: the Builder fails
	admin   string   // valid admin token in ws-ok and ws-flaky
	revoked string
	expired string
	suspend string // token of a suspended actor
}

const (
	wsOK       = "ws-ok"
	wsBroken   = "ws-broken"   // the ServiceSet build fails
	wsFlaky    = "ws-flaky"    // build fails until the test repairs it
	wsNoAuth   = "ws-noauth"   // builds, but with no auth service
	wsStoreErr = "ws-storeerr" // builds; every token lookup errors
	wsMissing  = "ws-missing"  // not in the registry
	wsInactive = "ws-inactive" // in the registry, inactive
)

func newPreauthEnv(t *testing.T) *preauthEnv {
	t.Helper()
	env := &preauthEnv{}

	fs := newFakeStore()
	fs.actors["admin-1"] = &domain.Actor{ActorID: "admin-1", Type: domain.ActorTypeHuman, Name: "Admin", Role: domain.RoleAdmin, Status: domain.ActorStatusActive}
	fs.actors["sus-1"] = &domain.Actor{ActorID: "sus-1", Type: domain.ActorTypeHuman, Name: "Suspended", Role: domain.RoleAdmin, Status: domain.ActorStatusActive}
	authSvc := auth.NewService(fs)
	ctx := context.Background()
	var err error
	if env.admin, _, err = authSvc.CreateToken(ctx, "admin-1", "ok", nil); err != nil {
		t.Fatal(err)
	}
	var revokedID string
	if env.revoked, revokedID, err = authSvc.CreateToken(ctx, "admin-1", "revoked", nil); err != nil {
		t.Fatal(err)
	}
	if err := fs.RevokeToken(ctx, revokedID); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	if env.expired, _, err = authSvc.CreateToken(ctx, "admin-1", "expired", &past); err != nil {
		t.Fatal(err)
	}
	if env.suspend, _, err = authSvc.CreateToken(ctx, "sus-1", "sus", nil); err != nil {
		t.Fatal(err)
	}
	fs.actors["sus-1"].Status = domain.ActorStatusSuspended
	errStore := tokenStoreErrStore{fakeStore: fs}

	table := func() *countingResolver {
		known := map[string]workspace.Config{}
		for _, id := range []string{wsOK, wsBroken, wsFlaky, wsNoAuth, wsStoreErr} {
			known[id] = workspace.Config{ID: id, RepoPath: t.TempDir(), Status: workspace.StatusActive}
		}
		return &countingResolver{
			calls: map[string]int{},
			known: known,
			errs:  map[string]error{wsInactive: workspace.ErrWorkspaceInactive},
		}
	}
	env.pool = table()
	env.broken.Store(wsBroken, struct{}{})
	env.broken.Store(wsFlaky, struct{}{})

	pool := workspace.NewServicePool(context.Background(), env.pool, workspace.PoolConfig{
		Builder: func(_ context.Context, ss *workspace.ServiceSet) error {
			env.builds.Add(1)
			if _, bad := env.broken.Load(ss.Config.ID); bad {
				return errors.New("connect to workspace database: connection refused")
			}
			switch ss.Config.ID {
			case wsNoAuth:
				ss.Store = fs
			case wsStoreErr:
				ss.Store = errStore
				ss.Auth = auth.NewService(errStore)
			default:
				ss.Store = fs
				ss.Auth = authSvc
			}
			return nil
		},
	})
	t.Cleanup(pool.Close)

	gitHandler, err := githttp.NewHandler(githttp.Config{
		ResolveRepoPath: func(context.Context, string) (string, error) { return t.TempDir(), nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := gateway.NewServer(":0", gateway.ServerConfig{
		WorkspaceResolver: table(),
		ServicePool:       pool,
		GitHTTP:           gitHandler,
	})
	env.ts = httptest.NewServer(srv.Handler())
	t.Cleanup(env.ts.Close)
	return env
}

type preauthResp struct {
	status int
	body   string
}

func (e *preauthEnv) do(t *testing.T, path, workspaceID, authz string) preauthResp {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, e.ts.URL+path, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	if workspaceID != "" {
		req.Header.Set(gateway.WorkspaceHeader, workspaceID)
	}
	if authz != "" {
		req.Header.Set("Authorization", authz)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return preauthResp{status: resp.StatusCode, body: string(b)}
}

// malformedAuthz never carries a well-formed bearer.
var malformedAuthz = []string{"", "Basic YWRtaW46eA==", "Bearer ", "Bearer", "bearer", "Token abc", "Bearerabc"}

// everyWorkspace covers each workspace state the pre-auth path can meet,
// plus no X-Workspace-ID header at all.
var everyWorkspace = []string{wsOK, wsBroken, wsFlaky, wsNoAuth, wsStoreErr, wsMissing, wsInactive, ""}

// TestMiddleware_NoServiceSetForUnauthenticated is AC 2. A request without
// a well-formed bearer never calls servicePool.Get (the pool resolver and
// the Builder both stay at zero), and every pre-authentication refusal —
// missing, inactive, build-failing, auth-less and store-erroring
// workspaces, unknown, revoked and expired tokens, a suspended actor —
// answers one status and one body. At the scaffold base a header-less
// request to a build-failing workspace built it and answered 500.
func TestMiddleware_NoServiceSetForUnauthenticated(t *testing.T) {
	env := newPreauthEnv(t)
	const path = "/api/v1/system/metrics"

	var want *preauthResp
	check := func(label string, got preauthResp) {
		t.Helper()
		if got.status != http.StatusUnauthorized {
			t.Errorf("%s: status %d body=%s, want 401", label, got.status, got.body)
			return
		}
		if want == nil {
			want = &got
			return
		}
		if got.body != want.body {
			t.Errorf("%s: body %q differs from the uniform 401 %q", label, got.body, want.body)
		}
	}

	for _, ws := range everyWorkspace {
		for _, authz := range malformedAuthz {
			check("ws="+ws+" authz="+authz, env.do(t, path, ws, authz))
		}
	}
	if n := env.pool.total(); n != 0 {
		t.Fatalf("servicePool.Get ran %d times for requests without a well-formed bearer; want 0 (calls=%v)", n, env.pool.calls)
	}
	if n := env.builds.Load(); n != 0 {
		t.Fatalf("the ServiceSet Builder ran %d times for unauthenticated requests; want 0", n)
	}

	// A well-formed but unproven bearer may build (the token can only be
	// checked against the workspace's own store), but it learns nothing
	// either: the same 401 for every workspace state and token state.
	for _, ws := range everyWorkspace {
		check("junk bearer ws="+ws, env.do(t, path, ws, "Bearer junk-token-0123456789"))
	}
	for name, tok := range map[string]string{"revoked": env.revoked, "expired": env.expired, "suspended actor": env.suspend} {
		check(name+" token ws-ok", env.do(t, path, wsOK, "Bearer "+tok))
	}
	// A token valid in ws-ok is still unproven where the workspace
	// cannot be built, has no auth service, or its store errors.
	for _, ws := range []string{wsBroken, wsFlaky, wsNoAuth, wsStoreErr, wsMissing, wsInactive} {
		check("valid ws-ok token ws="+ws, env.do(t, path, ws, "Bearer "+env.admin))
	}
}

// TestMiddleware_AuthenticatedBuildsServiceSet is AC 3. A valid bearer
// builds the ServiceSet once (then reuses it) and reaches the handler.
//
// Deviation (Implementation Notes): AC 3 asks a build failure for an
// authenticated caller to answer 500. In shared mode the bearer can only be
// validated against the workspace's own token store, which is part of the
// ServiceSet, so a caller whose workspace failed to build is never
// authenticated; the epic AC applies ("a ServiceSet build failure is a
// uniform 401 for any caller the build left unauthenticated"). The case
// below proves the 401 met a token that is valid there: once the build is
// repaired the same token gets 200.
func TestMiddleware_AuthenticatedBuildsServiceSet(t *testing.T) {
	env := newPreauthEnv(t)
	const path = "/api/v1/system/metrics"

	got := env.do(t, path, wsOK, "Bearer "+env.admin)
	if got.status != http.StatusOK {
		t.Fatalf("valid bearer: status %d body=%s, want 200", got.status, got.body)
	}
	if n := env.builds.Load(); n != 1 {
		t.Fatalf("builds after the first authenticated request = %d, want 1", n)
	}
	if got := env.do(t, path, wsOK, "Bearer "+env.admin); got.status != http.StatusOK {
		t.Fatalf("second request: status %d", got.status)
	}
	if n := env.builds.Load(); n != 1 {
		t.Fatalf("builds after a second request = %d, want 1 (the ServiceSet is reused)", n)
	}

	before := env.builds.Load()
	if got := env.do(t, path, wsFlaky, "Bearer "+env.admin); got.status != http.StatusUnauthorized {
		t.Fatalf("build failure: status %d body=%s, want the uniform 401", got.status, got.body)
	}
	if env.builds.Load() != before+1 {
		t.Fatal("the build-failing workspace was not built for a well-formed bearer")
	}
	env.broken.Delete(wsFlaky)
	if got := env.do(t, path, wsFlaky, "Bearer "+env.admin); got.status != http.StatusOK {
		t.Fatalf("after the build is repaired: status %d body=%s, want 200 (the bearer was valid)", got.status, got.body)
	}
}

// TestGitAuth_SharedModeNoPreAuthOracle is the /git half of N-15
// (adversarial review): validateGitAuth fell back to the process-level auth
// service when the workspace's ServiceSet failed to build or had none, and
// shared mode leaves that nil, so a junk bearer got 503 "authentication not
// configured" for such a workspace and the uniform 401 for a missing one;
// a token-store error surfaced as 500. Every pre-authentication /git
// refusal is now the gate's uniform 401, and a request without a
// well-formed bearer never builds.
func TestGitAuth_SharedModeNoPreAuthOracle(t *testing.T) {
	env := newPreauthEnv(t)
	gitPath := func(ws string) string { return "/git/" + ws + "/info/refs?service=git-upload-pack" }
	var want *preauthResp
	check := func(label string, got preauthResp) {
		t.Helper()
		if got.status != http.StatusUnauthorized {
			t.Errorf("%s: status %d body=%s, want 401", label, got.status, got.body)
			return
		}
		if want == nil {
			want = &got
			return
		}
		if got.body != want.body {
			t.Errorf("%s: body %q differs from the uniform 401 %q", label, got.body, want.body)
		}
	}
	for _, ws := range []string{wsOK, wsBroken, wsNoAuth, wsStoreErr, wsMissing, wsInactive} {
		check("no bearer ws="+ws, env.do(t, gitPath(ws), "", ""))
	}
	if n := env.pool.total(); n != 0 || env.builds.Load() != 0 {
		t.Fatalf("a /git request without a bearer reached servicePool.Get (%d) or the Builder (%d)", n, env.builds.Load())
	}
	for _, ws := range []string{wsOK, wsBroken, wsNoAuth, wsStoreErr, wsMissing, wsInactive} {
		check("junk bearer ws="+ws, env.do(t, gitPath(ws), "", "Bearer junk-token-0123456789"))
	}
	// A token valid in ws-ok is unproven wherever the workspace cannot
	// be built, has no auth service or its store errors.
	for _, ws := range []string{wsBroken, wsNoAuth, wsStoreErr, wsMissing, wsInactive} {
		check("valid ws-ok token ws="+ws, env.do(t, gitPath(ws), "", "Bearer "+env.admin))
	}
	for name, tok := range map[string]string{"revoked": env.revoked, "expired": env.expired, "suspended actor": env.suspend} {
		check(name+" token ws-ok", env.do(t, gitPath(wsOK), "", "Bearer "+tok))
	}
}

var _ store.Store = tokenStoreErrStore{}
