package gateway_test

// INIT-024 TASK-007 review round 1 (finding 4): in shared mode the
// workspace middleware resolved the workspace before anything looked at
// the bearer, so in platform-binding mode an anonymous request made Spine
// fetch a binding from the platform (no negative cache) — and dereference
// an existing workspace's secret references — for every distinct
// workspace id it named. A request without a well-formed bearer is now
// refused before Resolve, with the same uniform 401.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/secrets"
	"github.com/bszymi/spine/service/internal/gateway"
	"github.com/bszymi/spine/service/internal/workspace"
)

func TestWorkspaceMiddleware_PlatformBindingNoFetchWithoutBearer(t *testing.T) {
	var fetches atomic.Int32
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		const prefix = "/api/v1/internal/workspaces/"
		ws := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, prefix), "/runtime-binding")
		switch ws {
		case "ws-ok":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(workspace.PlatformBinding{
				WorkspaceID: "ws-ok", SpineWorkspaceID: "ws-ok", RepoPath: t.TempDir(),
				// A ref the secret store does not hold: resolution needs
				// a dereference, which fails (unavailable).
				RuntimeDBRef: secrets.SecretRef("secret-store://workspaces/ws-ok/runtime_db"),
			})
		case "ws-down":
			http.Error(w, "boom", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(platform.Close)
	sc, err := secrets.NewFileClient(secrets.FileConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	provider, err := workspace.NewPlatformBindingProvider(workspace.PlatformBindingConfig{
		PlatformBaseURL: platform.URL, ServiceToken: "svc", SecretClient: sc,
		HTTPClient: platform.Client(), CacheTTL: time.Millisecond, StaleGrace: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	// As spine serve wires platform-binding mode: the same provider
	// resolves for the middleware and for the pool.
	pool := workspace.NewServicePool(context.Background(), provider, workspace.PoolConfig{IdleCheckInterval: -1})
	t.Cleanup(pool.Close)
	srv := gateway.NewServer(":0", gateway.ServerConfig{WorkspaceResolver: provider, ServicePool: pool})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	env := &preauthEnv{ts: ts}

	var want *preauthResp
	check := func(label string, got preauthResp) {
		t.Helper()
		if got.status != http.StatusUnauthorized {
			t.Errorf("%s: status %d body=%s, want 401", label, got.status, got.body)
			return
		}
		if want == nil {
			want = &got
		} else if got.body != want.body {
			t.Errorf("%s: body %q differs from %q", label, got.body, want.body)
		}
	}
	states := []string{"ws-ok", "ws-missing", "ws-down", "not a valid id", ""}
	for _, path := range []string{"/api/v1/system/metrics", "/api/v1/events/stream", "/api/v1/runs/run-1"} {
		for _, ws := range states {
			for _, authz := range malformedAuthz {
				check(path+" ws="+ws+" authz="+authz, env.do(t, path, ws, authz))
			}
		}
	}
	if n := fetches.Load(); n != 0 {
		t.Fatalf("the platform was asked for %d bindings by requests without a well-formed bearer; want 0", n)
	}

	// A well-formed bearer still resolves (it can only be checked
	// against the workspace's own store), and still learns nothing.
	for _, ws := range states {
		check("junk bearer ws="+ws, env.do(t, "/api/v1/system/metrics", ws, "Bearer junk-token-0123456789"))
	}
	if fetches.Load() == 0 {
		t.Fatal("a well-formed bearer should resolve the workspace; the fixture is not exercising the provider")
	}
}
