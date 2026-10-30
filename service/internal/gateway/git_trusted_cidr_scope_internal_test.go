package gateway

// INIT-024 TASK-006 regression (review finding N-2, ported from the
// review's scratch/standalone/sa2 PoC): the SPINE_GIT_HTTP_TRUSTED_CIDRS
// token-less read was decided from the peer address alone, so in shared
// mode a runner host trusted for one tenant — or any job it ran — cloned
// every tenant's repository without a token, and the answer differed for
// missing and present workspaces. Trust is now bound to the
// workspace the request resolves to. Real git-http-backend behind bare
// repositories laid out side by side under one root, as a shared-mode
// repository directory is.

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/bszymi/spine/core/auth"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/service/internal/workspace"
)

const (
	t006RunnerA  = "172.28.1.10" // ws-a's runner
	t006RunnerB  = "172.28.1.11" // ws-b's runner
	t006Outsider = "198.51.100.7"

	t006MarkerA = "tenant-a-secret"
	t006MarkerB = "tenant-b-secret"

	t006UploadInfoRefs = "info/refs?service=git-upload-pack"
)

// t006Bindings is the per-workspace form
// `ws-a=172.28.1.10/32;ws-b=172.28.1.11/32`.
var t006Bindings = map[string][]string{
	"ws-a": {t006RunnerA + "/32"},
	"ws-b": {t006RunnerB + "/32"},
}

// t006GitCmd runs git with no system or global configuration, so the
// developer's own settings (credential helpers, url rewrites) never
// shape a test.
func t006GitCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@x", "-c", "commit.gpgsign=false", "-c", "credential.helper="}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "SSH_ASKPASS=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// t006Repo creates <root>/<ws>, a bare repository whose main commit
// message and run branch both carry marker, so any ref advertisement or
// clone of it shows the marker.
func t006Repo(t *testing.T, root, ws, marker string) string {
	t.Helper()
	work := t.TempDir()
	for _, args := range [][]string{
		{"-C", work, "init", "-q", "-b", "main"},
		{"-C", work, "commit", "-q", "--allow-empty", "-m", marker},
		{"-C", work, "branch", "spine/run/" + marker},
	} {
		if out, err := t006GitCmd(t, args...); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	bare := filepath.Join(root, ws)
	if out, err := t006GitCmd(t, "clone", "-q", "--bare", work, bare); err != nil {
		t.Fatalf("clone --bare: %v %s", err, out)
	}
	return bare
}

// t006Resolver serves ws-a and ws-b, reports ws-gone inactive and
// ws-down unavailable, and does not know any other ID.
type t006Resolver struct{ cfgs map[string]workspace.Config }

func (r *t006Resolver) Resolve(_ context.Context, id string) (*workspace.Config, error) {
	switch id {
	case "ws-gone":
		return nil, workspace.ErrWorkspaceInactive
	case "ws-down":
		return nil, workspace.ErrWorkspaceUnavailable
	}
	c, ok := r.cfgs[id]
	if !ok {
		return nil, workspace.ErrWorkspaceNotFound
	}
	return &c, nil
}

func (r *t006Resolver) List(context.Context) ([]workspace.Config, error) {
	out := make([]workspace.Config, 0, len(r.cfgs))
	for _, c := range r.cfgs {
		out = append(out, c)
	}
	return out, nil
}

type t006Fixture struct {
	srv      *Server
	resolver *t006Resolver
	pool     *workspace.ServicePool
	// wsAuth is, in shared mode, every workspace's own (ServiceSet) auth
	// service; see setAuth.
	wsAuth *auth.Service
}

// t006Server builds a gateway serving ws-a and ws-b with the given
// trust. shared wires a ServicePool, as spine serve does with the db and
// platform-binding resolvers; without it the server has the review PoC's
// shape (two workspaces, no pool), which the trust decision must not
// depend on.
func t006Server(t *testing.T, trusted map[string][]string, shared, receivePack bool) *t006Fixture {
	t.Helper()
	root := t.TempDir()
	resolver := &t006Resolver{cfgs: map[string]workspace.Config{
		"ws-a": {ID: "ws-a", RepoPath: t006Repo(t, root, "ws-a", t006MarkerA), Status: workspace.StatusActive},
		"ws-b": {ID: "ws-b", RepoPath: t006Repo(t, root, "ws-b", t006MarkerB), Status: workspace.StatusActive},
	}}
	f := &t006Fixture{
		srv: &Server{
			gitHTTP:    t006GitHandler(t, trusted, receivePack),
			wsResolver: resolver,
		},
		resolver: resolver,
	}
	if shared {
		f.pool = workspace.NewServicePool(context.Background(), resolver, workspace.PoolConfig{
			IdleCheckInterval: -1,
			// Each workspace authenticates against its own ServiceSet's
			// auth service, as in spine serve: shared mode never falls
			// back to the process-level one (INIT-024 TASK-007).
			Builder: func(_ context.Context, ss *workspace.ServiceSet) error {
				ss.Auth = f.wsAuth
				return nil
			},
		})
		t.Cleanup(f.pool.Close)
		f.srv.servicePool = f.pool
	}
	return f
}

// setAuth installs the auth service a test's bearers validate against:
// the server's own in single mode; in shared mode every workspace's
// ServiceSet's, which is what spine serve checks a /git bearer against
// (built ServiceSets are evicted so the next request picks it up).
func (f *t006Fixture) setAuth(authSvc *auth.Service) {
	if f.pool == nil {
		f.srv.auth = authSvc
		return
	}
	f.wsAuth = authSvc
	for id := range f.resolver.cfgs {
		f.pool.Evict(id)
	}
}

// do routes target through the real /git routes (chi, as mounted by the
// gateway) from remoteAddr, with headers as name, value pairs.
func (f *t006Fixture) do(method, target, remoteAddr string, headers ...string) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	f.srv.mountGitRoutes(r)
	req := httptest.NewRequest(method, target, http.NoBody)
	req.RemoteAddr = remoteAddr
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Add(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// AC 1 (regression, N-2): ws-a's runner clones ws-a without a token and
// gets 401 — and none of ws-b's refs — for every read of ws-b. At the
// scaffold base (a560c210 + the INIT-024 scaffold) the runner's address
// could only be trusted process-wide, and ws-b answered 200 with its
// refs, in shared mode and in the PoC's pool-less shape alike.
func TestHandleGit_TrustedCIDRScopedToWorkspace(t *testing.T) {
	for _, mode := range []struct {
		name   string
		shared bool
	}{
		{"shared mode", true},
		{"two workspaces without a ServicePool (the review PoC's server)", false},
	} {
		t.Run(mode.name, func(t *testing.T) {
			f := t006Server(t, t006Bindings, mode.shared, false)
			runnerA := t006RunnerA + ":40000"

			w := f.do(http.MethodGet, "/git/ws-a/"+t006UploadInfoRefs, runnerA)
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), t006MarkerA) {
				t.Fatalf("ws-a's runner, no token, ws-a: status %d, advertises ws-a=%v; want 200 with ws-a's refs",
					w.Code, strings.Contains(w.Body.String(), t006MarkerA))
			}

			for _, tc := range []struct{ method, path string }{
				{http.MethodGet, "/git/ws-b/" + t006UploadInfoRefs},
				{http.MethodPost, "/git/ws-b/git-upload-pack"},
				{http.MethodGet, "/git/ws-b/info/refs"}, // dumb protocol
				{http.MethodGet, "/git/ws-b/HEAD"},
				{http.MethodGet, "/git/ws-b/objects/info/packs"},
				{http.MethodGet, "/git/ws-b/spine/" + t006UploadInfoRefs}, // explicit primary repository
			} {
				w := f.do(tc.method, tc.path, runnerA)
				if w.Code != http.StatusUnauthorized || strings.Contains(w.Body.String(), t006MarkerB) {
					t.Errorf("ws-a's runner, no token, %s %s: status %d, advertises ws-b=%v; want 401 with nothing of ws-b",
						tc.method, tc.path, w.Code, strings.Contains(w.Body.String(), t006MarkerB))
				}
			}

			// Each binding works for its own workspace: ws-b's runner
			// reads ws-b and not ws-a.
			if w := f.do(http.MethodGet, "/git/ws-b/"+t006UploadInfoRefs, t006RunnerB+":40000"); w.Code != http.StatusOK ||
				!strings.Contains(w.Body.String(), t006MarkerB) {
				t.Errorf("ws-b's runner, no token, ws-b: status %d; want 200 with ws-b's refs", w.Code)
			}
			if w := f.do(http.MethodGet, "/git/ws-a/"+t006UploadInfoRefs, t006RunnerB+":40000"); w.Code != http.StatusUnauthorized {
				t.Errorf("ws-b's runner, no token, ws-a: status %d; want 401", w.Code)
			}
			// A bearer still reads any workspace the token admits.
			authSvc, bearer := gitBearerAuth(t, "dev-1", domain.RoleContributor)
			f.setAuth(authSvc)
			if w := f.do(http.MethodGet, "/git/ws-b/"+t006UploadInfoRefs, runnerA, "Authorization", "Bearer "+bearer); w.Code != http.StatusOK {
				t.Errorf("ws-a's runner with a bearer, ws-b: status %d; want 200", w.Code)
			}
		})
	}
}

// AC 1, end to end: a real `git clone` over HTTP. The test server's
// address (127.0.0.1) is bound to ws-a only: cloning ws-a works without
// a token, cloning ws-b fails and writes nothing of ws-b.
func TestHandleGit_TrustedCIDRScopedToWorkspace_RealClone(t *testing.T) {
	f := t006Server(t, map[string][]string{"ws-a": {"127.0.0.1/32"}}, true, false)
	srv := NewServer(":0", ServerConfig{
		GitHTTP:           f.srv.gitHTTP,
		WorkspaceResolver: f.resolver,
		ServicePool:       f.pool,
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	dstA := filepath.Join(t.TempDir(), "a")
	if out, err := t006GitCmd(t, "clone", "-q", ts.URL+"/git/ws-a", dstA); err != nil {
		t.Fatalf("token-less clone of ws-a from its runner: %v %s", err, out)
	}
	if out, err := t006GitCmd(t, "-C", dstA, "log", "--all", "--format=%s %D"); err != nil || !strings.Contains(out, t006MarkerA) {
		t.Fatalf("ws-a clone does not hold ws-a's history: %v %s", err, out)
	}

	dstB := filepath.Join(t.TempDir(), "b")
	out, err := t006GitCmd(t, "clone", "-q", ts.URL+"/git/ws-b", dstB)
	if err == nil {
		t.Fatalf("token-less clone of ws-b from ws-a's runner succeeded: %s", out)
	}
	if strings.Contains(out, t006MarkerB) {
		t.Fatalf("failed clone of ws-b still showed ws-b's refs: %s", out)
	}
	if _, statErr := os.Stat(filepath.Join(dstB, ".git")); statErr == nil {
		if log, _ := t006GitCmd(t, "-C", dstB, "log", "--all", "--format=%s"); strings.Contains(log, t006MarkerB) {
			t.Fatalf("ws-b's history reached the runner: %s", log)
		}
	}
	if out, err := t006GitCmd(t, "ls-remote", ts.URL+"/git/ws-b"); err == nil || strings.Contains(out, t006MarkerB) {
		t.Fatalf("token-less ls-remote of ws-b from ws-a's runner: err=%v out=%s", err, out)
	}
}

// AC 2: from a trusted address, a token-less read of a workspace that
// does not resolve (missing, inactive, unavailable) and of a real
// workspace the address is not bound to all answer the same uniform 401
// — the same status, body and content type an untrusted caller gets — so
// the bypass no longer tells present workspaces from absent ones. At the
// scaffold base the trusted caller got 404/403/503 for the first three
// and 200 for ws-b.
func TestHandleGit_UnboundWorkspaceUniform401(t *testing.T) {
	for _, shared := range []bool{true, false} {
		f := t006Server(t, t006Bindings, shared, false)
		ref := f.do(http.MethodGet, "/git/ws-b/"+t006UploadInfoRefs, t006Outsider+":40000")
		if ref.Code != http.StatusUnauthorized {
			t.Fatalf("shared=%v: untrusted caller, ws-b: status %d, want 401", shared, ref.Code)
		}
		for _, peer := range []string{t006RunnerA + ":40000", "[::ffff:" + t006RunnerA + "]:40000"} {
			for _, ws := range []string{"ws-b", "ws-missing", "ws-gone", "ws-down", "WS-A", "ws-a-"} {
				for _, path := range []string{t006UploadInfoRefs, "info/refs", "HEAD"} {
					w := f.do(http.MethodGet, "/git/"+ws+"/"+path, peer)
					if w.Code != ref.Code || w.Body.String() != ref.Body.String() ||
						w.Header().Get("Content-Type") != ref.Header().Get("Content-Type") {
						t.Errorf("shared=%v: ws-a's runner %s, no token, /git/%s/%s: %d %q (%s); want the uniform %d %q (%s)",
							shared, peer, ws, path, w.Code, w.Body.String(), w.Header().Get("Content-Type"),
							ref.Code, ref.Body.String(), ref.Header().Get("Content-Type"))
					}
				}
			}
		}
	}
}

// AC 5: a push from a bound trusted address still needs a bearer — the
// workspace binding is a read affordance only.
func TestHandleGit_PushNeverTakesWorkspaceBypass(t *testing.T) {
	f := t006Server(t, t006Bindings, true, true)
	runnerA := t006RunnerA + ":40000"
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/git/ws-a/info/refs?service=git-receive-pack"},
		{http.MethodPost, "/git/ws-a/git-receive-pack"},
		{http.MethodGet, "/git/ws-a/info/refs?service&git-receive-pack"},
		{http.MethodGet, "/git/ws-a/info/refs?service=git-upload-pack&service=git-receive-pack"},
	} {
		w := f.do(tc.method, tc.path, runnerA)
		if w.Code != http.StatusUnauthorized || w.Header().Get("Content-Type") == t015ReceivePackAdvertisement {
			t.Errorf("ws-a's runner, no token, %s %s: status %d (%s); want 401", tc.method, tc.path, w.Code, w.Header().Get("Content-Type"))
		}
	}
	// With a bearer the push advertisement is served.
	authSvc, bearer := gitBearerAuth(t, "dev-1", domain.RoleContributor)
	f.setAuth(authSvc)
	if w := f.do(http.MethodGet, "/git/ws-a/info/refs?service=git-receive-pack", runnerA, "Authorization", "Bearer "+bearer); w.Code != http.StatusOK ||
		w.Header().Get("Content-Type") != t015ReceivePackAdvertisement {
		t.Errorf("ws-a's runner with a bearer, push advertisement: status %d (%s); want 200 %s", w.Code, w.Header().Get("Content-Type"), t015ReceivePackAdvertisement)
	}

	// With receive-pack off a token-less push from the bound runner is
	// the uniform 401; with a bearer it is the 403 naming the flag.
	off := t006Server(t, t006Bindings, true, false)
	off.setAuth(authSvc)
	if w := off.do(http.MethodGet, "/git/ws-a/info/refs?service=git-receive-pack", runnerA); w.Code != http.StatusUnauthorized {
		t.Errorf("ws-a's runner, push off, no token: status %d %q; want 401", w.Code, w.Body.String())
	}
	if w := off.do(http.MethodGet, "/git/ws-a/info/refs?service=git-receive-pack", runnerA, "Authorization", "Bearer "+bearer); w.Code != http.StatusForbidden ||
		!strings.Contains(w.Body.String(), "SPINE_GIT_RECEIVE_PACK_ENABLED") {
		t.Errorf("ws-a's runner, push off, bearer: status %d %q; want 403 naming the flag", w.Code, w.Body.String())
	}
}

// t006SameResponse reports whether two responses are the same answer:
// status, body and every header.
func t006SameResponse(a, b *httptest.ResponseRecorder) bool {
	if a.Code != b.Code || a.Body.String() != b.Body.String() || len(a.Header()) != len(b.Header()) {
		return false
	}
	for k, v := range a.Header() {
		if strings.Join(v, "\x00") != strings.Join(b.Header()[k], "\x00") {
			return false
		}
	}
	return true
}

// Review round 1 (P3, pre-existing since EPIC-004 TASK-031): with push
// off (the default) a push request used to skip the auth gate whenever
// the workspace resolved, to show the 403 naming
// SPINE_GIT_RECEIVE_PACK_ENABLED, so its answer depended on whether the
// workspace exists. Now a caller not authenticated for the workspace —
// no token, a junk or non-bearer header, or (shared mode) another
// workspace's token — gets the same uniform 401 for an existing, a
// missing, an inactive and an unavailable workspace, status, body and
// headers; a caller authenticated for the workspace still gets the 403.
func TestHandleGit_PushDisabledIsNoWorkspaceOracle(t *testing.T) {
	authA, tokenA := gitBearerAuth(t, "runner-a", domain.RoleContributor)
	authB, tokenB := gitBearerAuth(t, "runner-b", domain.RoleContributor)
	for _, mode := range []struct {
		name   string
		shared bool
	}{
		{"shared mode, per-workspace auth", true},
		{"no ServicePool, process auth", false},
	} {
		f := t006Server(t, t006Bindings, false, false)
		if mode.shared {
			// Each workspace's ServiceSet authenticates against its own
			// tokens, as a workspace store does in spine serve.
			wsAuth := map[string]*auth.Service{"ws-a": authA, "ws-b": authB}
			f.pool = workspace.NewServicePool(context.Background(), f.resolver, workspace.PoolConfig{
				IdleCheckInterval: -1,
				Builder: func(_ context.Context, ss *workspace.ServiceSet) error {
					ss.Auth = wsAuth[ss.Config.ID]
					return nil
				},
			})
			t.Cleanup(f.pool.Close)
			f.srv.servicePool = f.pool
		} else {
			f.srv.auth = authA
		}

		callers := []struct{ name, header string }{
			{"no token", ""},
			{"junk bearer", "Bearer junk"},
			{"basic", "Basic eDp5"},
			{"bare scheme", "bearer"},
		}
		if mode.shared {
			callers = append(callers, struct{ name, header string }{"another workspace's token", "Bearer " + tokenB})
		}
		requests := []struct{ method, path string }{
			{http.MethodGet, "info/refs?service=git-receive-pack"},
			{http.MethodPost, "git-receive-pack"},
		}
		for _, peer := range []string{t006Outsider, t006RunnerA} {
			for _, c := range callers {
				for _, rq := range requests {
					var headers []string
					if c.header != "" {
						headers = []string{"Authorization", c.header}
					}
					ref := f.do(rq.method, "/git/ws-missing/"+rq.path, peer+":40000", headers...)
					if ref.Code != http.StatusUnauthorized ||
						!strings.Contains(ref.Body.String(), "authorization required for external git access") {
						t.Fatalf("%s, %s, %s, %s %s, missing workspace: %d %q; want the uniform 401",
							mode.name, peer, c.name, rq.method, rq.path, ref.Code, ref.Body.String())
					}
					for _, ws := range []string{"ws-a", "ws-gone", "ws-down"} {
						if w := f.do(rq.method, "/git/"+ws+"/"+rq.path, peer+":40000", headers...); !t006SameResponse(w, ref) {
							t.Errorf("%s, %s, %s, %s /git/%s/%s: %d %q %v; the missing workspace answers %d %q %v",
								mode.name, peer, c.name, rq.method, ws, rq.path, w.Code, w.Body.String(), w.Header(),
								ref.Code, ref.Body.String(), ref.Header())
						}
					}
				}
			}
		}

		// Authenticated for ws-a: the flag's 403, for both requests.
		for _, rq := range requests {
			w := f.do(rq.method, "/git/ws-a/"+rq.path, t006Outsider+":40000", "Authorization", "Bearer "+tokenA)
			if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "SPINE_GIT_RECEIVE_PACK_ENABLED") {
				t.Errorf("%s, ws-a's token, %s %s: %d %q; want 403 naming the flag", mode.name, rq.method, rq.path, w.Code, w.Body.String())
			}
		}
	}
}

// Adversarial self-review: ws-a's runner tries to reach ws-b's repository
// some other way — URL and workspace-ID spellings, traversal through the
// repository and git-path segments (the repositories are siblings, so a
// `..` that reached git-http-backend would land in ws-b), the
// single-workspace route, forwarding headers naming ws-b's runner, other
// methods, an IPv4-mapped peer. None may answer 200 or show ws-b's refs.
func TestHandleGit_TrustedPeerCannotReachUnboundWorkspace(t *testing.T) {
	for _, shared := range []bool{true, false} {
		f := t006Server(t, t006Bindings, shared, false)
		for _, tc := range []struct {
			name, method, path, peer string
			headers                  []string
		}{
			{"workspace ID in another case", "GET", "/git/WS-B/" + t006UploadInfoRefs, t006RunnerA, nil},
			{"percent-encoded hyphen", "GET", "/git/ws%2Db/" + t006UploadInfoRefs, t006RunnerA, nil},
			{"percent-encoded first letter", "GET", "/git/%77s-b/" + t006UploadInfoRefs, t006RunnerA, nil},
			{"encoded slash inside the workspace segment", "GET", "/git/ws-a%2F..%2Fws-b/" + t006UploadInfoRefs, t006RunnerA, nil},
			{"NUL after the workspace ID", "GET", "/git/ws-b%00/" + t006UploadInfoRefs, t006RunnerA, nil},
			{"trailing dot", "GET", "/git/ws-b./" + t006UploadInfoRefs, t006RunnerA, nil},
			{"dot-dot as the repository segment", "GET", "/git/ws-a/../ws-b/" + t006UploadInfoRefs, t006RunnerA, nil},
			{"ws-b as a repository of ws-a", "GET", "/git/ws-a/ws-b/" + t006UploadInfoRefs, t006RunnerA, nil},
			{"traversal through the git path", "GET", "/git/ws-a/info/../../ws-b/" + t006UploadInfoRefs, t006RunnerA, nil},
			{"encoded traversal through the git path", "GET", "/git/ws-a/info/%2E%2E/%2E%2E/ws-b/" + t006UploadInfoRefs, t006RunnerA, nil},
			{"encoded-slash traversal through the git path", "GET", "/git/ws-a/info%2F..%2F..%2Fws-b/" + t006UploadInfoRefs, t006RunnerA, nil},
			{"dumb-protocol traversal", "GET", "/git/ws-a/objects/../../ws-b/HEAD", t006RunnerA, nil},
			{"upload-pack traversal", "POST", "/git/ws-a/info/../../ws-b/git-upload-pack", t006RunnerA, nil},
			{"double slash before the workspace", "GET", "/git//ws-b/" + t006UploadInfoRefs, t006RunnerA, nil},
			{"single-workspace route", "GET", "/git/" + t006UploadInfoRefs, t006RunnerA, nil},
			{"X-Forwarded-For naming ws-b's runner", "GET", "/git/ws-b/" + t006UploadInfoRefs, t006RunnerA, []string{"X-Forwarded-For", t006RunnerB}},
			{"Forwarded naming ws-b's runner", "GET", "/git/ws-b/" + t006UploadInfoRefs, t006RunnerA, []string{"Forwarded", "for=" + t006RunnerB}},
			{"X-Real-IP naming ws-b's runner", "GET", "/git/ws-b/" + t006UploadInfoRefs, t006RunnerA, []string{"X-Real-IP", t006RunnerB}},
			{"HEAD method", "HEAD", "/git/ws-b/info/refs", t006RunnerA, nil},
			{"PROPFIND method", "PROPFIND", "/git/ws-b/info/refs", t006RunnerA, nil},
			{"IPv4-mapped IPv6 peer", "GET", "/git/ws-b/" + t006UploadInfoRefs, "[::ffff:" + t006RunnerA + "]", nil},
			{"IPv6 zone on the peer", "GET", "/git/ws-b/" + t006UploadInfoRefs, "[fe80::1%25eth0]", nil},
		} {
			w := f.do(tc.method, tc.path, tc.peer+":40000", tc.headers...)
			if w.Code == http.StatusOK || strings.Contains(w.Body.String(), t006MarkerB) {
				t.Errorf("shared=%v, %s (%s %s): status %d, advertises ws-b=%v; want a refusal with nothing of ws-b",
					shared, tc.name, tc.method, tc.path, w.Code, strings.Contains(w.Body.String(), t006MarkerB))
			}
		}

		// Control: the same runner's own workspace, through the same
		// routes, is served — the refusals above are about ws-b, not a
		// broken fixture.
		for _, path := range []string{"/git/ws-a/" + t006UploadInfoRefs, "/git/ws-a/spine/" + t006UploadInfoRefs, "/git/ws-a/info/refs"} {
			if w := f.do(http.MethodGet, path, t006RunnerA+":40000"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), t006MarkerA) {
				t.Errorf("shared=%v, control %s: status %d; want 200 with ws-a's refs", shared, path, w.Code)
			}
		}
		if w := f.do(http.MethodGet, "/git/ws-a/"+t006UploadInfoRefs, "[::ffff:"+t006RunnerA+"]:40000"); w.Code != http.StatusOK {
			t.Errorf("shared=%v, control: IPv4-mapped form of ws-a's runner, ws-a: status %d; want 200", shared, w.Code)
		}
	}
}

// Adversarial self-review (P3, pre-existing): a caller that sends a junk
// or malformed bearer met the auth gate with the workspace resolved, and
// the gate surfaced the auth error's own message ("invalid token",
// "invalid authorization header format") only for a workspace that
// exists — a missing, inactive or unavailable one got the uniform
// message. The bodies told the two apart for any caller, trusted or not.
// Every 401 is now the uniform one; a valid bearer still reads.
func TestHandleGit_InvalidBearerIsNoWorkspaceOracle(t *testing.T) {
	for _, shared := range []bool{true, false} {
		f := t006Server(t, t006Bindings, shared, false)
		authSvc, bearer := gitBearerAuth(t, "dev-1", domain.RoleContributor)
		f.setAuth(authSvc)
		for _, peer := range []string{t006Outsider, t006RunnerA} {
			for _, h := range []string{"Bearer junk", "Basic eDp5", "Bearer ", "bearer", "Token " + bearer} {
				ref := f.do(http.MethodGet, "/git/ws-missing/"+t006UploadInfoRefs, peer+":40000", "Authorization", h)
				if ref.Code != http.StatusUnauthorized {
					t.Fatalf("shared=%v peer %s %q, missing workspace: status %d, want 401", shared, peer, h, ref.Code)
				}
				for _, ws := range []string{"ws-b", "ws-gone", "ws-down"} {
					w := f.do(http.MethodGet, "/git/"+ws+"/"+t006UploadInfoRefs, peer+":40000", "Authorization", h)
					if w.Code != ref.Code || w.Body.String() != ref.Body.String() {
						t.Errorf("shared=%v peer %s %q, /git/%s: %d %q; the missing workspace answers %d %q",
							shared, peer, h, ws, w.Code, w.Body.String(), ref.Code, ref.Body.String())
					}
				}
			}
		}
		if w := f.do(http.MethodGet, "/git/ws-b/"+t006UploadInfoRefs, t006Outsider+":40000", "Authorization", "Bearer "+bearer); w.Code != http.StatusOK ||
			!strings.Contains(w.Body.String(), t006MarkerB) {
			t.Errorf("shared=%v: valid bearer, ws-b: status %d; want 200 with ws-b's refs", shared, w.Code)
		}
	}
}

// Review round 1 (P3): a peer trusted for the workspace its URL names,
// whose workspace does not resolve, gets the uniform 401 like any caller;
// an Info line records why, so an operator can see why a configured
// runner stopped cloning. The response is unchanged, and nothing is
// logged for an untrusted peer or a workspace the peer is not bound to.
func TestHandleGit_TrustedPeerResolveFailureIsLogged(t *testing.T) {
	buf := &lockedBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	trusted := map[string][]string{"ws-a": {t006RunnerA + "/32"}, "ws-gone": {t006RunnerA + "/32"}}
	f := t006Server(t, trusted, true, false)
	ref := f.do(http.MethodGet, "/git/ws-gone/"+t006UploadInfoRefs, t006Outsider+":40000")
	w := f.do(http.MethodGet, "/git/ws-gone/"+t006UploadInfoRefs, t006RunnerA+":40000")
	if !t006SameResponse(w, ref) || w.Code != http.StatusUnauthorized {
		t.Fatalf("bound runner, inactive workspace: %d %q; want the untrusted caller's %d %q", w.Code, w.Body.String(), ref.Code, ref.Body.String())
	}
	const line = "token-less access refused for a trusted-CIDR peer because its workspace did not resolve"
	out := buf.String()
	if n := strings.Count(out, line); n != 1 || !strings.Contains(out, "workspace_id=ws-gone") {
		t.Fatalf("log for the bound runner: %d lines, want 1 naming ws-gone:\n%s", n, out)
	}
	// Not for a workspace the runner is not bound to, nor for an
	// untrusted peer (the reference request above).
	_ = f.do(http.MethodGet, "/git/ws-missing/"+t006UploadInfoRefs, t006RunnerA+":40000")
	if n := strings.Count(buf.String(), line); n != 1 {
		t.Fatalf("log after an unbound workspace: %d lines, want still 1:\n%s", n, buf.String())
	}
}
