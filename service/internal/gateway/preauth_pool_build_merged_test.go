package gateway_test

// INIT-024 integration A: TASK-007 (review finding N-15) answers every
// shared-mode refusal before a bearer is validated with one uniform 401,
// and TASK-008 bounds a workspace's ServiceSet build (InitTimeout, the
// abandoned entry that fails every caller until the build returns, the
// recovered build panic). The two meet in servicePool.Get, which shared
// mode calls before the bearer can be validated. On the merged tree every
// way a build can fail — hung past InitTimeout, abandoned, evicted while a
// caller waits on it, panicking, failing — must answer exactly what a
// missing workspace answers, on /api/v1 and on /git, even to a token that
// is valid there; and the hung build's first caller must be released at
// InitTimeout.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bszymi/spine/core/auth"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/service/internal/gateway"
	"github.com/bszymi/spine/service/internal/githttp"
	"github.com/bszymi/spine/service/internal/workspace"
)

type mergedPreauthResp struct {
	status int
	body   string
	header http.Header
}

func TestPreauth_BoundedPoolBuildFailuresAnswerUniform401(t *testing.T) {
	const initTimeout = 200 * time.Millisecond

	fs := newFakeStore()
	fs.actors["admin-1"] = &domain.Actor{ActorID: "admin-1", Type: domain.ActorTypeHuman, Name: "Admin", Role: domain.RoleAdmin, Status: domain.ActorStatusActive}
	authSvc := auth.NewService(fs)
	valid, _, err := authSvc.CreateToken(context.Background(), "admin-1", "ok", nil)
	if err != nil {
		t.Fatal(err)
	}

	// ws-hang-* builds block until the test ends; ws-panic-* builds
	// panic; ws-fail-* builds fail; ws-ok builds with the auth service
	// the valid token belongs to. Each surface gets its own hung
	// workspace, so each sees a first (timed-out) and an abandoned call.
	hang := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(hang) }) }
	ids := []string{"ws-ok"}
	for _, s := range []string{"api", "git"} {
		ids = append(ids, "ws-hang-"+s, "ws-hang-evict-"+s, "ws-panic-"+s, "ws-fail-"+s)
	}
	table := func() *countingResolver {
		known := map[string]workspace.Config{}
		for _, id := range ids {
			known[id] = workspace.Config{ID: id, RepoPath: t.TempDir(), Status: workspace.StatusActive}
		}
		return &countingResolver{calls: map[string]int{}, known: known, errs: map[string]error{}}
	}
	pool := workspace.NewServicePool(context.Background(), table(), workspace.PoolConfig{
		InitTimeout:       initTimeout,
		IdleCheckInterval: -1,
		Builder: func(_ context.Context, ss *workspace.ServiceSet) error {
			switch id := ss.Config.ID; {
			case strings.HasPrefix(id, "ws-hang-"):
				<-hang
			case strings.HasPrefix(id, "ws-panic-"):
				panic("integration A: a panicking workspace build")
			case strings.HasPrefix(id, "ws-fail-"):
				return errors.New("connect to workspace database: connection refused")
			}
			ss.Store = fs
			ss.Auth = authSvc
			return nil
		},
	})
	t.Cleanup(pool.Close)
	t.Cleanup(release) // before pool.Close: the hung builds return first

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
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	send := func(surface, ws, authz string) (mergedPreauthResp, time.Duration, error) {
		target := ts.URL + "/api/v1/system/metrics"
		if surface == "git" {
			target = ts.URL + "/git/" + ws + "/info/refs?service=git-upload-pack"
		}
		req, err := http.NewRequest(http.MethodGet, target, http.NoBody)
		if err != nil {
			return mergedPreauthResp{}, 0, err
		}
		if surface == "api" {
			req.Header.Set(gateway.WorkspaceHeader, ws)
		}
		req.Header.Set("Authorization", authz)
		start := time.Now()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return mergedPreauthResp{}, 0, err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		h := resp.Header.Clone()
		h.Del("Date")       // per response
		h.Del("X-Trace-Id") // per request
		return mergedPreauthResp{status: resp.StatusCode, body: string(b), header: h}, time.Since(start), nil
	}
	do := func(surface, ws, authz string) (mergedPreauthResp, time.Duration) {
		t.Helper()
		got, took, err := send(surface, ws, authz)
		if err != nil {
			t.Fatal(err)
		}
		return got, took
	}

	// The valid token is valid: ws-ok serves it.
	if got, _ := do("api", "ws-ok", "Bearer "+valid); got.status != http.StatusOK {
		t.Fatalf("ws-ok, valid token: status %d %q; want 200", got.status, got.body)
	}

	for _, surface := range []string{"api", "git"} {
		want, _ := do(surface, "ws-missing", "Bearer junk")
		if want.status != http.StatusUnauthorized {
			t.Fatalf("%s, missing workspace: status %d %q; want 401", surface, want.status, want.body)
		}
		first, took := do(surface, "ws-hang-"+surface, "Bearer junk")
		// Bounded by InitTimeout, with room for a loaded machine; before
		// TASK-008 this call never returned.
		if took > initTimeout+10*time.Second {
			t.Errorf("%s, hung build: the first call took %s; want about InitTimeout (%s)", surface, took, initTimeout)
		}
		// A caller waiting on a build that is evicted meanwhile (a
		// deactivation, a binding invalidation) is released at
		// InitTimeout too. If the eviction lands before the build has
		// started, the call is an ordinary hung first call.
		evictWS := "ws-hang-evict-" + surface
		type sent struct {
			got  mergedPreauthResp
			took time.Duration
			err  error
		}
		waiter := make(chan sent, 1)
		go func() {
			got, took, err := send(surface, evictWS, "Bearer "+valid)
			waiter <- sent{got, took, err}
		}()
		time.Sleep(initTimeout / 4)
		pool.Evict(evictWS)
		evicted := <-waiter
		if evicted.err != nil {
			t.Fatal(evicted.err)
		}
		if evicted.took > initTimeout+10*time.Second {
			t.Errorf("%s, evicted while waited on: the call took %s; want about InitTimeout (%s)", surface, evicted.took, initTimeout)
		}
		cases := []struct {
			name string
			got  mergedPreauthResp
		}{{"hung build, first call", first}, {"hung build evicted while waited on, valid token", evicted.got}}
		for _, c := range []struct{ name, ws, authz string }{
			{"hung build, abandoned", "ws-hang-" + surface, "Bearer junk"},
			{"hung build, abandoned, valid token", "ws-hang-" + surface, "Bearer " + valid},
			{"panicking build", "ws-panic-" + surface, "Bearer junk"},
			{"panicking build, valid token", "ws-panic-" + surface, "Bearer " + valid},
			{"failing build, valid token", "ws-fail-" + surface, "Bearer " + valid},
			{"built workspace, unknown token", "ws-ok", "Bearer junk"},
		} {
			got, _ := do(surface, c.ws, c.authz)
			cases = append(cases, struct {
				name string
				got  mergedPreauthResp
			}{c.name, got})
		}
		for _, c := range cases {
			if c.got.status != want.status || c.got.body != want.body || !reflect.DeepEqual(c.got.header, want.header) {
				t.Errorf("%s, %s: %d %q %v; want the missing workspace's %d %q %v",
					surface, c.name, c.got.status, c.got.body, c.got.header, want.status, want.body, want.header)
			}
		}
	}
}
