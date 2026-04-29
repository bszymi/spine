package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/bszymi/spine/internal/branchprotect"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/githttp"
	"github.com/bszymi/spine/internal/gitpool"
	"github.com/bszymi/spine/internal/repository"
	"github.com/bszymi/spine/internal/workspace"
)

// poolStubResolver resolves a single workspace with no database URL so
// buildServiceSet yields ss.Store == nil and ss.Auth == nil — the exact
// branch that used to leak pool refs before TASK-015.
type poolStubResolver struct{ cfg workspace.Config }

func (r *poolStubResolver) Resolve(_ context.Context, id string) (*workspace.Config, error) {
	if id != r.cfg.ID {
		return nil, workspace.ErrWorkspaceNotFound
	}
	c := r.cfg
	return &c, nil
}

func (r *poolStubResolver) List(_ context.Context) ([]workspace.Config, error) {
	return []workspace.Config{r.cfg}, nil
}

func TestValidateGitAuth_DoesNotLeakPoolRefs(t *testing.T) {
	ctx := context.Background()
	resolver := &poolStubResolver{cfg: workspace.Config{ID: "ws-1", RepoPath: t.TempDir()}}
	pool := workspace.NewServicePool(ctx, resolver, workspace.PoolConfig{})
	defer pool.Close()

	s := &Server{servicePool: pool}
	cfg := &workspace.Config{ID: "ws-1"}

	req := httptest.NewRequest("GET", "/git/info/refs", nil)
	req.Header.Set("Authorization", "Bearer bad-token")

	for i := 0; i < 1000; i++ {
		if _, err := s.validateGitAuth(req, cfg); err == nil {
			t.Fatalf("iteration %d: expected auth failure", i)
		}
	}

	if ref := pool.RefCount("ws-1"); ref != 0 {
		t.Fatalf("expected refCount 0 after 1000 failed auths, got %d (pool leak)", ref)
	}
}

func TestParseGitPath(t *testing.T) {
	tests := []struct {
		name     string
		pattern  string
		url      string
		wantWsID string
		wantPath string
	}{
		{
			name:     "workspace with info refs",
			pattern:  "/git/{workspace_id}/*",
			url:      "/git/ws-1/info/refs",
			wantWsID: "ws-1",
			wantPath: "/info/refs",
		},
		{
			name:     "workspace with upload-pack",
			pattern:  "/git/{workspace_id}/*",
			url:      "/git/ws-1/git-upload-pack",
			wantWsID: "ws-1",
			wantPath: "/git-upload-pack",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set up a chi router to populate URL params.
			r := chi.NewRouter()
			var gotWsID, gotPath string
			r.HandleFunc(tt.pattern, func(_ http.ResponseWriter, r *http.Request) {
				gotWsID, _, gotPath = parseGitPath(r)
			})

			req := httptest.NewRequest("GET", tt.url, http.NoBody)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if gotWsID != tt.wantWsID {
				t.Errorf("workspaceID = %q, want %q", gotWsID, tt.wantWsID)
			}
			if gotPath != tt.wantPath {
				t.Errorf("gitPath = %q, want %q", gotPath, tt.wantPath)
			}
		})
	}
}

func TestParseGitPath_SingleMode(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		wantWsID string
		wantPath string
	}{
		{
			name:     "info refs no workspace",
			url:      "/git/info/refs",
			wantWsID: "",
			wantPath: "/info/refs",
		},
		{
			name:     "upload-pack no workspace",
			url:      "/git/git-upload-pack",
			wantWsID: "",
			wantPath: "/git-upload-pack",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := chi.NewRouter()
			var gotWsID, gotPath string
			r.HandleFunc("/git/*", func(_ http.ResponseWriter, r *http.Request) {
				gotWsID, _, gotPath = parseGitPath(r)
			})

			req := httptest.NewRequest("GET", tt.url, http.NoBody)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if gotWsID != tt.wantWsID {
				t.Errorf("workspaceID = %q, want %q", gotWsID, tt.wantWsID)
			}
			if gotPath != tt.wantPath {
				t.Errorf("gitPath = %q, want %q", gotPath, tt.wantPath)
			}
		})
	}
}

// TestParseGitPath_BothRoutesRegistered reproduces the production routing
// where both /git/{workspace_id}/* and /git/* are mounted. chi prefers the
// more specific pattern, so /git/info/refs matches the first route with
// workspace_id="info" — parseGitPath must still report this as single-mode.
func TestParseGitPath_BothRoutesRegistered(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		wantWsID string
		wantPath string
	}{
		{"single-mode info/refs", "/git/info/refs", "", "/info/refs"},
		{"single-mode objects", "/git/objects/pack/pack-abc.pack", "", "/objects/pack/pack-abc.pack"},
		{"single-mode HEAD", "/git/HEAD", "", "/HEAD"},
		{"single-mode upload-pack", "/git/git-upload-pack", "", "/git-upload-pack"},
		{"workspace info/refs", "/git/ws-1/info/refs", "ws-1", "/info/refs"},
		{"workspace upload-pack", "/git/ws-1/git-upload-pack", "ws-1", "/git-upload-pack"},
		{"unknown workspace still routes as workspace", "/git/nonexistent/info/refs", "nonexistent", "/info/refs"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := chi.NewRouter()
			var gotWsID, gotPath string
			handler := func(_ http.ResponseWriter, r *http.Request) {
				gotWsID, _, gotPath = parseGitPath(r)
			}
			r.HandleFunc("/git/{workspace_id}/*", handler)
			r.HandleFunc("/git/*", handler)

			req := httptest.NewRequest("GET", tt.url, http.NoBody)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if gotWsID != tt.wantWsID {
				t.Errorf("workspaceID = %q, want %q", gotWsID, tt.wantWsID)
			}
			if gotPath != tt.wantPath {
				t.Errorf("gitPath = %q, want %q", gotPath, tt.wantPath)
			}
		})
	}
}

func TestIsGitProtocolSegment(t *testing.T) {
	cases := map[string]bool{
		"info":             true,
		"objects":          true,
		"git-upload-pack":  true,
		"git-receive-pack": true,
		"HEAD":             true,
		"ws-1":             false,
		"default":          false,
		"":                 false,
		"Info":             false, // case-sensitive
	}
	for seg, want := range cases {
		t.Run(seg, func(t *testing.T) {
			if got := isGitProtocolSegment(seg); got != want {
				t.Errorf("isGitProtocolSegment(%q) = %v, want %v", seg, got, want)
			}
		})
	}
}

func TestIsGitProtocolPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"info/refs", true},
		{"git-upload-pack", true},
		{"git-receive-pack", true},
		{"HEAD", true},
		{"objects/pack/pack-abc.pack", true},
		{"ws-1/info/refs", false},
		{"my-workspace/git-upload-pack", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := isGitProtocolPath(tt.path)
			if got != tt.want {
				t.Errorf("isGitProtocolPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

// TestHandleGit_DisabledPushDoesNotResolvePolicy asserts that when
// ReceivePackEnabled is false, the gateway does NOT call
// GitPushResolver for a push attempt. Otherwise a failing policy
// resolver (e.g. an unreachable workspace DB) would mask the
// documented 403 that names `SPINE_GIT_RECEIVE_PACK_ENABLED` behind
// a 500, turning a misconfiguration into a confusing operator error.
func TestHandleGit_DisabledPushDoesNotResolvePolicy(t *testing.T) {
	repo := t.TempDir()
	resolver := &poolStubResolver{cfg: workspace.Config{
		ID:       "ws-off",
		RepoPath: repo,
		Status:   workspace.StatusActive,
	}}
	handler, err := githttp.NewHandler(githttp.Config{
		ResolveRepoPath: func(_ context.Context, _ string) (string, error) {
			return repo, nil
		},
		// Flag OFF — push is unreachable by design.
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	var resolverCalled bool
	s := &Server{
		gitHTTP:    handler,
		wsResolver: resolver,
		devMode:    true,
		gitPushResolver: func(_ context.Context, _ string) (GitPushResources, func(), error) {
			resolverCalled = true
			return GitPushResources{Policy: branchprotect.NewPermissive()}, func() {}, nil
		},
	}

	req := httptest.NewRequest("POST", "/git/ws-off/git-receive-pack",
		strings.NewReader(""))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("workspace_id", "ws-off")
	rctx.URLParams.Add("*", "git-receive-pack")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()

	s.handleGit(w, req)

	if resolverCalled {
		t.Error("policy resolver must not run when receive-pack is disabled")
	}
	// And the response must be the flag-off 403 so operators can
	// find the switch.
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for disabled push, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "SPINE_GIT_RECEIVE_PACK_ENABLED") {
		t.Errorf("expected 403 body to name the flag, got: %s", w.Body.String())
	}
}

// TestHandleGit_PerWorkspacePolicyInvoked asserts that on a push
// request, the gateway calls GitPushResolver with the target
// workspace ID (not a process-wide default). In shared mode each
// workspace has its own branch-protection table; a single
// captured-at-startup policy would mix or miss rules, so this wiring
// is a correctness requirement, not an optimisation.
func TestHandleGit_PerWorkspacePolicyInvoked(t *testing.T) {
	repo := t.TempDir()
	resolver := &poolStubResolver{cfg: workspace.Config{
		ID:       "ws-7",
		RepoPath: repo,
		Status:   workspace.StatusActive,
	}}
	handler, err := githttp.NewHandler(githttp.Config{
		ResolveRepoPath: func(_ context.Context, _ string) (string, error) {
			return repo, nil
		},
		ReceivePackEnabled: true,
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	var resolvedFor string
	var released bool
	s := &Server{
		gitHTTP:    handler,
		wsResolver: resolver,
		devMode:    true, // bypass auth so we reach the policy resolver
		gitPushResolver: func(_ context.Context, workspaceID string) (GitPushResources, func(), error) {
			resolvedFor = workspaceID
			return GitPushResources{Policy: branchprotect.NewPermissive()}, func() { released = true }, nil
		},
	}

	req := httptest.NewRequest("POST", "/git/ws-7/git-receive-pack",
		strings.NewReader(""))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("workspace_id", "ws-7")
	rctx.URLParams.Add("*", "git-receive-pack")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()

	s.handleGit(w, req)

	if resolvedFor != "ws-7" {
		t.Errorf("expected policy resolved for ws-7, got %q", resolvedFor)
	}
	if !released {
		t.Errorf("expected release callback to run for pool-held reference")
	}
}

func TestHandleGit_NilHandler(t *testing.T) {
	s := &Server{} // gitHTTP is nil

	req := httptest.NewRequest("GET", "/git/info/refs", nil)
	w := httptest.NewRecorder()

	s.handleGit(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when gitHTTP is nil, got %d", w.Code)
	}
}

// TestHandleGit_TrustedCIDRDoesNotBypassPushAuth asserts that a push
// from a trusted source IP is still required to present a bearer
// token. EPIC-004 TASK-001 requires an actor identity on every push so
// the upcoming pre-receive branch-protection check (TASK-002) can pin
// the caller. Without this split a trusted runner subnet configured
// for token-less cloning could also push anonymously.
func TestHandleGit_TrustedCIDRDoesNotBypassPushAuth(t *testing.T) {
	repo := t.TempDir()
	resolver := &poolStubResolver{cfg: workspace.Config{
		ID:       "ws-1",
		RepoPath: repo,
		Status:   workspace.StatusActive,
	}}

	// Trust 127.0.0.0/8 so the test request's default RemoteAddr
	// (127.0.0.1:...) sits inside a trusted CIDR.
	handler, err := githttp.NewHandler(githttp.Config{
		ResolveRepoPath: func(_ context.Context, _ string) (string, error) {
			return repo, nil
		},
		TrustedCIDRs:       []string{"127.0.0.0/8"},
		ReceivePackEnabled: true,
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	s := &Server{
		gitHTTP:    handler,
		wsResolver: resolver,
	}

	// info/refs?service=git-receive-pack from a trusted IP, no auth
	// header. The auth gate must refuse; the read-only clone/fetch
	// path would have accepted the same RemoteAddr.
	req := httptest.NewRequest("GET",
		"/git/ws-1/info/refs?service=git-receive-pack", nil)
	req.RemoteAddr = "127.0.0.1:54321" // in trusted 127.0.0.0/8
	// Route the request through chi so URLParam("workspace_id") resolves.
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("workspace_id", "ws-1")
	rctx.URLParams.Add("*", "info/refs")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()

	s.handleGit(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for trusted-CIDR push without token, got %d (body: %s)",
			w.Code, w.Body.String())
	}
}

// TestHandleGit_PushDisabledReturns403NotAuth asserts that when the
// receive-pack flag is off, the handler's 403-with-flag-name response
// reaches the caller even without a bearer token. The auth gate must
// not hide that guidance behind a 401 — operators who hit this for the
// first time will not have configured a token yet, and the flag-name
// message is the whole point of the UX.
func TestHandleGit_PushDisabledReturns403NotAuth(t *testing.T) {
	repo := t.TempDir()
	resolver := &poolStubResolver{cfg: workspace.Config{
		ID:       "ws-1",
		RepoPath: repo,
		Status:   workspace.StatusActive,
	}}
	handler, err := githttp.NewHandler(githttp.Config{
		ResolveRepoPath: func(_ context.Context, _ string) (string, error) {
			return repo, nil
		},
		// Flag OFF.
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	s := &Server{
		gitHTTP:    handler,
		wsResolver: resolver,
	}

	req := httptest.NewRequest("GET",
		"/git/ws-1/info/refs?service=git-receive-pack", nil)
	// External IP, no auth header.
	req.RemoteAddr = "203.0.113.1:12345"
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("workspace_id", "ws-1")
	rctx.URLParams.Add("*", "info/refs")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()

	s.handleGit(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 with flag-name guidance, got %d (body: %s)",
			w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "SPINE_GIT_RECEIVE_PACK_ENABLED") {
		t.Errorf("expected 403 body to name SPINE_GIT_RECEIVE_PACK_ENABLED, got: %s",
			w.Body.String())
	}
}

// TestHandleGit_TrustedCIDRBypassesReadAuth is the counterpart of the
// push-auth test: for a clone/fetch request from a trusted IP with no
// token, the gate must pass through to the git handler (which then
// proceeds to the backend or 500s on the repo path, either of which is
// past the auth gate).
func TestHandleGit_TrustedCIDRBypassesReadAuth(t *testing.T) {
	repo := t.TempDir()
	resolver := &poolStubResolver{cfg: workspace.Config{
		ID:       "ws-1",
		RepoPath: repo,
		Status:   workspace.StatusActive,
	}}
	handler, err := githttp.NewHandler(githttp.Config{
		ResolveRepoPath: func(_ context.Context, _ string) (string, error) {
			return repo, nil
		},
		TrustedCIDRs: []string{"127.0.0.0/8"},
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	s := &Server{
		gitHTTP:    handler,
		wsResolver: resolver,
	}

	req := httptest.NewRequest("GET",
		"/git/ws-1/info/refs?service=git-upload-pack", nil)
	req.RemoteAddr = "127.0.0.1:54321" // in trusted 127.0.0.0/8
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("workspace_id", "ws-1")
	rctx.URLParams.Add("*", "info/refs")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()

	s.handleGit(w, req)

	if w.Code == http.StatusUnauthorized {
		t.Errorf("read from trusted CIDR should not be rejected by auth gate, got 401: %s",
			w.Body.String())
	}
}

// errResolver always returns the configured workspace resolution error
// — used to exercise the unauthenticated-untrusted enumeration paths
// guarded by TASK-031.
type errResolver struct{ err error }

func (r *errResolver) Resolve(_ context.Context, _ string) (*workspace.Config, error) {
	return nil, r.err
}

func (r *errResolver) List(_ context.Context) ([]workspace.Config, error) {
	return nil, r.err
}

// runUnauthenticatedGitProbe issues a clone-style request from an
// untrusted IP with no Authorization header against a workspace ID
// that the resolver will reject with the given error. The TASK-031
// contract is that the response collapses to a uniform 401 without
// the workspace ID or status appearing in the body.
func runUnauthenticatedGitProbe(t *testing.T, resolveErr error) *httptest.ResponseRecorder {
	t.Helper()
	repo := t.TempDir()
	handler, err := githttp.NewHandler(githttp.Config{
		ResolveRepoPath: func(_ context.Context, _ string) (string, error) {
			return repo, nil
		},
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	s := &Server{
		gitHTTP:    handler,
		wsResolver: &errResolver{err: resolveErr},
	}

	req := httptest.NewRequest("GET",
		"/git/secret-ws/info/refs?service=git-upload-pack", nil)
	req.RemoteAddr = "203.0.113.1:12345" // untrusted external IP, no CIDR allowlist
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("workspace_id", "secret-ws")
	rctx.URLParams.Add("*", "info/refs")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()

	s.handleGit(w, req)
	return w
}

// TestHandleGit_UnauthenticatedNotFound_Returns401 asserts that an
// untrusted unauthenticated client probing a non-existent workspace
// receives the same 401 it would for a missing or invalid bearer —
// not a 404 that confirms "this workspace does not exist."
func TestHandleGit_UnauthenticatedNotFound_Returns401(t *testing.T) {
	w := runUnauthenticatedGitProbe(t, workspace.ErrWorkspaceNotFound)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 (no tenant-state leak), got %d body=%s",
			w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "secret-ws") {
		t.Errorf("response leaks workspace ID: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "not found") {
		t.Errorf("response leaks 'not found' status: %s", w.Body.String())
	}
}

// TestHandleGit_UnauthenticatedInactive_Returns401 asserts the same
// uniform-401 contract for an inactive workspace — a 403 here would
// confirm to the caller that the workspace exists.
func TestHandleGit_UnauthenticatedInactive_Returns401(t *testing.T) {
	w := runUnauthenticatedGitProbe(t, workspace.ErrWorkspaceInactive)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 (no tenant-state leak), got %d body=%s",
			w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "inactive") {
		t.Errorf("response leaks 'inactive' status: %s", w.Body.String())
	}
}

// TestHandleGit_UnauthenticatedUnavailable_Returns401 asserts the
// same uniform-401 contract for a temporarily unavailable workspace
// — a 503 here would let an attacker correlate workspace IDs with
// outage windows.
func TestHandleGit_UnauthenticatedUnavailable_Returns401(t *testing.T) {
	w := runUnauthenticatedGitProbe(t, workspace.ErrWorkspaceUnavailable)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 (no tenant-state leak), got %d body=%s",
			w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "unavailable") {
		t.Errorf("response leaks 'unavailable' status: %s", w.Body.String())
	}
}

// TestHandleGit_TrustedCIDRStillSeesWorkspaceErrors asserts that
// trusted-CIDR callers — which the existing clone affordance trusts
// to know workspace IDs already — continue to receive distinct
// resolution errors so operators can debug genuinely missing repos.
// Hiding this from trusted callers would turn TASK-031 into a
// usability regression.
func TestHandleGit_TrustedCIDRStillSeesWorkspaceErrors(t *testing.T) {
	repo := t.TempDir()
	handler, err := githttp.NewHandler(githttp.Config{
		ResolveRepoPath: func(_ context.Context, _ string) (string, error) {
			return repo, nil
		},
		TrustedCIDRs: []string{"127.0.0.0/8"},
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	s := &Server{
		gitHTTP:    handler,
		wsResolver: &errResolver{err: workspace.ErrWorkspaceNotFound},
	}

	req := httptest.NewRequest("GET",
		"/git/missing-ws/info/refs?service=git-upload-pack", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("workspace_id", "missing-ws")
	rctx.URLParams.Add("*", "info/refs")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()

	s.handleGit(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("trusted-CIDR caller should see the distinct 404, got %d body=%s",
			w.Code, w.Body.String())
	}
}

// TestHandleGit_PushFlagOffOnMissingWorkspace_Returns401 asserts that
// the flag-off short-circuit does NOT bypass auth when the workspace
// fails to resolve. Otherwise an unauthenticated probe to
// `/git/{id}/git-receive-pack` with receive-pack disabled could still
// distinguish missing/inactive/unavailable workspaces via the leaked
// resolution error.
func TestHandleGit_PushFlagOffOnMissingWorkspace_Returns401(t *testing.T) {
	repo := t.TempDir()
	handler, err := githttp.NewHandler(githttp.Config{
		ResolveRepoPath: func(_ context.Context, _ string) (string, error) {
			return repo, nil
		},
		// Receive-pack flag deliberately OFF.
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	s := &Server{
		gitHTTP:    handler,
		wsResolver: &errResolver{err: workspace.ErrWorkspaceNotFound},
	}

	req := httptest.NewRequest("GET",
		"/git/secret-ws/info/refs?service=git-receive-pack", nil)
	req.RemoteAddr = "203.0.113.1:12345"
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("workspace_id", "secret-ws")
	rctx.URLParams.Add("*", "info/refs")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()

	s.handleGit(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for unauth probe of missing ws on flag-off receive-pack, got %d body=%s",
			w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "secret-ws") {
		t.Errorf("response leaks workspace ID: %s", w.Body.String())
	}
}

// TestParseGitPath_WithRepository covers the new shared-mode form
// `/git/{workspace_id}/{repository_id}/...`. The first wildcard
// segment is a repository ID iff it is not a known git protocol
// segment — `/git/{ws}/info/refs` must still parse as the legacy
// primary-only form, never as repo "info" with path "/refs".
func TestParseGitPath_WithRepository(t *testing.T) {
	tests := []struct {
		name       string
		url        string
		wantWsID   string
		wantRepoID string
		wantPath   string
	}{
		{"workspace + repo + info/refs", "/git/ws-1/code/info/refs", "ws-1", "code", "/info/refs"},
		{"workspace + repo + upload-pack", "/git/ws-1/code/git-upload-pack", "ws-1", "code", "/git-upload-pack"},
		{"workspace + repo + receive-pack", "/git/ws-1/code/git-receive-pack", "ws-1", "code", "/git-receive-pack"},
		{"workspace + repo + nested objects", "/git/ws-1/code/objects/pack/pack-abc.pack", "ws-1", "code", "/objects/pack/pack-abc.pack"},

		// Legacy form (no repo) — first segment IS a protocol segment.
		{"workspace + protocol info no repo", "/git/ws-1/info/refs", "ws-1", "", "/info/refs"},
		{"workspace + protocol upload-pack no repo", "/git/ws-1/git-upload-pack", "ws-1", "", "/git-upload-pack"},
		{"workspace + protocol HEAD no repo", "/git/ws-1/HEAD", "ws-1", "", "/HEAD"},

		// `spine` is a valid repository ID — the registry will
		// resolve it as the primary, so the gateway must not treat
		// it as a protocol segment.
		{"workspace + explicit spine repo", "/git/ws-1/spine/info/refs", "ws-1", "spine", "/info/refs"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := chi.NewRouter()
			var gotWs, gotRepo, gotPath string
			handler := func(_ http.ResponseWriter, r *http.Request) {
				gotWs, gotRepo, gotPath = parseGitPath(r)
			}
			r.HandleFunc("/git/{workspace_id}/*", handler)
			r.HandleFunc("/git/*", handler)

			req := httptest.NewRequest("GET", tt.url, http.NoBody)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if gotWs != tt.wantWsID {
				t.Errorf("workspaceID = %q, want %q", gotWs, tt.wantWsID)
			}
			if gotRepo != tt.wantRepoID {
				t.Errorf("repositoryID = %q, want %q", gotRepo, tt.wantRepoID)
			}
			if gotPath != tt.wantPath {
				t.Errorf("gitPath = %q, want %q", gotPath, tt.wantPath)
			}
		})
	}
}

// gitPoolResolverStub is a test-local Resolver for gitpool.New that
// records every Lookup so we can verify the gateway is actually
// asking the pool to resolve the URL repository ID rather than using
// the segment as a filesystem path.
type gitPoolResolverStub struct {
	mu      sync.Mutex
	repos   map[string]string // repositoryID -> LocalPath
	lookups []string
}

func (r *gitPoolResolverStub) Lookup(_ context.Context, id string) (*repository.Repository, error) {
	r.mu.Lock()
	r.lookups = append(r.lookups, id)
	r.mu.Unlock()
	if path, ok := r.repos[id]; ok {
		return &repository.Repository{ID: id, LocalPath: path}, nil
	}
	return nil, domain.NewErrorWithCause(domain.ErrNotFound,
		fmt.Sprintf("repository %q not found in catalog", id),
		repository.ErrRepositoryNotFound)
}

func (r *gitPoolResolverStub) ListActive(_ context.Context) ([]repository.Repository, error) {
	return nil, nil
}

func newTestPool(t *testing.T, repos map[string]string) (*gitpool.Pool, *gitPoolResolverStub) {
	t.Helper()
	primary := git.NewCLIClient(t.TempDir())
	resolver := &gitPoolResolverStub{repos: repos}
	pool, err := gitpool.New(primary, resolver, gitpool.NewCLIClientFactory())
	if err != nil {
		t.Fatalf("gitpool.New: %v", err)
	}
	return pool, resolver
}

// TestResolveGitRepoPath_PrimaryFallback asserts that an empty
// repository ID and the literal "spine" both bypass the pool and
// return the workspace's primary path. This is what keeps existing
// `/git/{ws}/info/refs` URLs working with no pool dependency.
func TestResolveGitRepoPath_PrimaryFallback(t *testing.T) {
	primaryPath := t.TempDir()
	cfg := &workspace.Config{ID: "ws-1", RepoPath: primaryPath}
	s := &Server{}

	for _, repoID := range []string{"", "spine"} {
		got, err := s.resolveGitRepoPath(context.Background(), cfg, repoID)
		if err != nil {
			t.Fatalf("repoID=%q: %v", repoID, err)
		}
		if got != primaryPath {
			t.Errorf("repoID=%q: got %q, want primary %q", repoID, got, primaryPath)
		}
	}
}

// TestResolveGitRepoPath_CodeRepoViaPool asserts that a non-primary
// repository ID is routed through the gitpool — the registry
// resolves it to the binding's LocalPath, never the URL segment as a
// filesystem path. This is the core ADR-013 invariant.
func TestResolveGitRepoPath_CodeRepoViaPool(t *testing.T) {
	codePath := t.TempDir()
	pool, resolver := newTestPool(t, map[string]string{"code": codePath})

	cfg := &workspace.Config{ID: "ws-1", RepoPath: t.TempDir()}
	s := &Server{gitPool: pool}

	got, err := s.resolveGitRepoPath(context.Background(), cfg, "code")
	if err != nil {
		t.Fatalf("resolveGitRepoPath: %v", err)
	}
	if got != codePath {
		t.Errorf("got %q, want %q", got, codePath)
	}
	if len(resolver.lookups) == 0 {
		t.Errorf("expected at least one Lookup(\"code\"), got %v", resolver.lookups)
	}
	for _, id := range resolver.lookups {
		if id != "code" {
			t.Errorf("unexpected lookup id %q", id)
		}
	}
}

// TestResolveGitRepoPath_UnknownRepoNotFound asserts that an unknown
// repository ID propagates the registry's ErrRepositoryNotFound
// (wrapped in SpineError so the gateway maps to 404). The URL
// segment must never be reused as a path.
func TestResolveGitRepoPath_UnknownRepoNotFound(t *testing.T) {
	pool, _ := newTestPool(t, map[string]string{})
	cfg := &workspace.Config{ID: "ws-1", RepoPath: t.TempDir()}
	s := &Server{gitPool: pool}

	_, err := s.resolveGitRepoPath(context.Background(), cfg, "missing")
	if err == nil {
		t.Fatal("expected error for unknown repo")
	}
	if !errors.Is(err, repository.ErrRepositoryNotFound) {
		t.Errorf("expected ErrRepositoryNotFound, got %v", err)
	}
	var se *domain.SpineError
	if !errors.As(err, &se) || se.Code != domain.ErrNotFound {
		t.Errorf("expected SpineError code=ErrNotFound, got %v", err)
	}
}

// TestResolveGitRepoPath_SharedMode_PrefersServiceSetPool asserts
// that in shared mode (servicePool configured) the workspace's own
// gitpool from ServiceSet.GitPool is used — not the process-level
// s.gitPool. Otherwise multi-workspace deployments would resolve
// `/git/{ws}/{repo}` against the wrong registry.
func TestResolveGitRepoPath_SharedMode_PrefersServiceSetPool(t *testing.T) {
	otherPool, otherResolver := newTestPool(t, map[string]string{"code": t.TempDir()})

	// Real workspace ServicePool: buildServiceSet wires a real
	// registry against the workspace primary path with no catalog
	// file, so "code" is unknown to it. If resolveGitRepoPath
	// silently fell back to otherPool, "code" would resolve
	// successfully (and otherResolver would record a Lookup).
	resolver := &poolStubResolver{cfg: workspace.Config{
		ID:       "ws-1",
		RepoPath: t.TempDir(),
		Status:   workspace.StatusActive,
	}}
	pool := workspace.NewServicePool(context.Background(), resolver, workspace.PoolConfig{})
	defer pool.Close()

	cfg := &workspace.Config{ID: "ws-1", RepoPath: t.TempDir()}
	s := &Server{
		gitPool:     otherPool, // would be the wrong choice in shared mode
		servicePool: pool,
	}

	got, err := s.resolveGitRepoPath(context.Background(), cfg, "code")
	if err == nil {
		t.Fatalf("expected lookup against ws-scoped pool to fail; got path=%q (fell back to s.gitPool)", got)
	}
	if len(otherResolver.lookups) != 0 {
		t.Errorf("server-level pool must not be consulted in shared mode, got lookups=%v",
			otherResolver.lookups)
	}
}

// errServicePoolResolver fails Resolve so servicePool.Get errors —
// resolveGitRepoPath must surface that, not silently use s.gitPool.
type errServicePoolResolver struct{}

func (errServicePoolResolver) Resolve(_ context.Context, _ string) (*workspace.Config, error) {
	return nil, workspace.ErrWorkspaceUnavailable
}

func (errServicePoolResolver) List(_ context.Context) ([]workspace.Config, error) {
	return nil, nil
}

// TestResolveGitRepoPath_SharedMode_GetErrorPropagates asserts that a
// servicePool.Get failure surfaces unchanged rather than collapsing
// to a fallback. Otherwise a workspace outage would be masked as a
// repository-not-found against the wrong registry.
func TestResolveGitRepoPath_SharedMode_GetErrorPropagates(t *testing.T) {
	otherPool, _ := newTestPool(t, map[string]string{"code": t.TempDir()})
	pool := workspace.NewServicePool(context.Background(), errServicePoolResolver{}, workspace.PoolConfig{})
	defer pool.Close()

	cfg := &workspace.Config{ID: "ws-1", RepoPath: t.TempDir()}
	s := &Server{
		gitPool:     otherPool,
		servicePool: pool,
	}

	_, err := s.resolveGitRepoPath(context.Background(), cfg, "code")
	if err == nil {
		t.Fatal("expected error to propagate from servicePool.Get")
	}
	if !errors.Is(err, workspace.ErrWorkspaceUnavailable) {
		t.Errorf("expected ErrWorkspaceUnavailable, got %v", err)
	}
}

// TestResolveGitRepoPath_NoPool asserts that a code-repo request
// without a configured gitpool fails 503 rather than silently
// resolving to the workspace primary path. A misconfigured server
// must not appear to "succeed" by serving the wrong repo.
func TestResolveGitRepoPath_NoPool(t *testing.T) {
	cfg := &workspace.Config{ID: "ws-1", RepoPath: t.TempDir()}
	s := &Server{} // no gitPool, no servicePool

	_, err := s.resolveGitRepoPath(context.Background(), cfg, "code")
	if err == nil {
		t.Fatal("expected error when no pool configured")
	}
	var se *domain.SpineError
	if !errors.As(err, &se) || se.Code != domain.ErrUnavailable {
		t.Errorf("expected SpineError code=ErrUnavailable, got %v", err)
	}
}

// TestHandleGit_PerRepoRouting_ReachesGitHandler exercises the full
// route: a request to `/git/{ws}/{repo}/info/refs` must (a) parse
// out workspace_id="ws" and repository_id="repo", (b) resolve the
// repo via the pool, (c) install the repo's LocalPath in the
// request context for the inner githttp handler. We assert that the
// pool was asked for "code" — proving the URL segment is not used
// as a filesystem path — and that the request reached the inner
// handler (not blocked by auth or workspace resolution).
func TestHandleGit_PerRepoRouting_ReachesGitHandler(t *testing.T) {
	codePath := t.TempDir()
	pool, resolver := newTestPool(t, map[string]string{"code": codePath})

	wsResolver := &poolStubResolver{cfg: workspace.Config{
		ID:       "ws-1",
		RepoPath: t.TempDir(),
		Status:   workspace.StatusActive,
	}}
	handler, err := githttp.NewHandler(githttp.Config{
		// ResolveRepoPath is required by NewHandler validation but
		// the gateway sets the path via context, so this fallback
		// path is only used if the gateway forgets to set it.
		ResolveRepoPath: func(_ context.Context, _ string) (string, error) {
			return "/should/not/be/used", nil
		},
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	s := &Server{
		gitHTTP:    handler,
		wsResolver: wsResolver,
		gitPool:    pool,
		devMode:    true, // bypass auth so we reach the repo resolver
	}

	req := httptest.NewRequest("GET",
		"/git/ws-1/code/info/refs?service=git-upload-pack", http.NoBody)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("workspace_id", "ws-1")
	rctx.URLParams.Add("*", "code/info/refs")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()

	s.handleGit(w, req)

	if len(resolver.lookups) == 0 {
		t.Errorf("expected the pool to be consulted for \"code\", got no lookups")
	}
	for _, id := range resolver.lookups {
		if id != "code" {
			t.Errorf("unexpected lookup id %q", id)
		}
	}
	// We don't assert a specific status — the temp dir is not a real
	// git repo so git-http-backend will refuse — but the response
	// must be past the auth gate (not 401) and not the workspace
	// resolution layer (not 404 with workspace id).
	if w.Code == http.StatusUnauthorized {
		t.Errorf("auth gate should have been bypassed: 401 body=%s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "ws-1") &&
		strings.Contains(w.Body.String(), "not found") {
		t.Errorf("workspace resolution should not have failed: %s", w.Body.String())
	}
}

// TestHandleGit_PerRepoRouting_UnknownRepoReturnsNotFound asserts
// that an unknown repository ID surfaces the registry's wrapped
// ErrNotFound (404) — past the auth gate, distinct codes are safe
// and operators need them to debug missing bindings.
func TestHandleGit_PerRepoRouting_UnknownRepoReturnsNotFound(t *testing.T) {
	pool, _ := newTestPool(t, map[string]string{}) // empty registry
	wsResolver := &poolStubResolver{cfg: workspace.Config{
		ID:       "ws-1",
		RepoPath: t.TempDir(),
		Status:   workspace.StatusActive,
	}}
	handler, err := githttp.NewHandler(githttp.Config{
		ResolveRepoPath: func(_ context.Context, _ string) (string, error) {
			return "/x", nil
		},
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	s := &Server{
		gitHTTP:    handler,
		wsResolver: wsResolver,
		gitPool:    pool,
		devMode:    true,
	}

	req := httptest.NewRequest("GET",
		"/git/ws-1/missing/info/refs?service=git-upload-pack", http.NoBody)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("workspace_id", "ws-1")
	rctx.URLParams.Add("*", "missing/info/refs")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()

	s.handleGit(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for unknown repository, got %d body=%s",
			w.Code, w.Body.String())
	}
}

// TestHandleGit_PerRepoRouting_InactiveRepoReturnsPrecondition
// asserts that an inactive code-repo binding surfaces past the auth
// gate as a 412 (Precondition) — the registry's typed
// ErrRepositoryInactive carries that code via SpineError. Operators
// need this to distinguish "repo never existed" (404) from "repo
// exists but binding is disabled" (412).
func TestHandleGit_PerRepoRouting_InactiveRepoReturnsPrecondition(t *testing.T) {
	pool, err := gitpool.New(git.NewCLIClient(t.TempDir()),
		&inactiveLookupResolver{}, gitpool.NewCLIClientFactory())
	if err != nil {
		t.Fatalf("gitpool.New: %v", err)
	}

	wsResolver := &poolStubResolver{cfg: workspace.Config{
		ID:       "ws-1",
		RepoPath: t.TempDir(),
		Status:   workspace.StatusActive,
	}}
	handler, err := githttp.NewHandler(githttp.Config{
		ResolveRepoPath: func(_ context.Context, _ string) (string, error) { return "/x", nil },
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	s := &Server{
		gitHTTP:    handler,
		wsResolver: wsResolver,
		gitPool:    pool,
		devMode:    true,
	}

	req := httptest.NewRequest("GET",
		"/git/ws-1/payments/info/refs?service=git-upload-pack", http.NoBody)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("workspace_id", "ws-1")
	rctx.URLParams.Add("*", "payments/info/refs")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()

	s.handleGit(w, req)

	// SpineError(ErrPrecondition) maps to 412.
	if w.Code != http.StatusPreconditionFailed {
		t.Errorf("expected 412 for inactive binding, got %d body=%s",
			w.Code, w.Body.String())
	}
}

// inactiveLookupResolver returns ErrRepositoryInactive for every
// non-primary lookup so the gateway test can exercise the inactive
// branch without standing up a Manager + binding store.
type inactiveLookupResolver struct{}

func (inactiveLookupResolver) Lookup(_ context.Context, id string) (*repository.Repository, error) {
	if id == repository.PrimaryRepositoryID {
		return &repository.Repository{ID: id, LocalPath: "/var/spine/primary"}, nil
	}
	return nil, domain.NewErrorWithCause(domain.ErrPrecondition,
		fmt.Sprintf("repository %q binding is inactive", id),
		repository.ErrRepositoryInactive)
}

func (inactiveLookupResolver) ListActive(_ context.Context) ([]repository.Repository, error) {
	return nil, nil
}

// TestHandleGit_PerRepoRouting_PushDisabledKeeps403 asserts that an
// unauthenticated `git-receive-pack` probe with the receive-pack
// flag OFF receives the inner handler's 403 (with
// SPINE_GIT_RECEIVE_PACK_ENABLED guidance) — even when the URL
// includes an unknown repository ID. Resolving the repository here
// would leak its state (404 vs 412) to a caller we deliberately
// chose not to authenticate.
func TestHandleGit_PerRepoRouting_PushDisabledKeeps403(t *testing.T) {
	pool, _ := newTestPool(t, map[string]string{}) // empty registry
	wsResolver := &poolStubResolver{cfg: workspace.Config{
		ID:       "ws-1",
		RepoPath: t.TempDir(),
		Status:   workspace.StatusActive,
	}}
	handler, err := githttp.NewHandler(githttp.Config{
		ResolveRepoPath: func(_ context.Context, _ string) (string, error) {
			return "/x", nil
		},
		// Receive-pack flag OFF.
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	s := &Server{
		gitHTTP:    handler,
		wsResolver: wsResolver,
		gitPool:    pool,
	}

	req := httptest.NewRequest("POST",
		"/git/ws-1/missing-repo/git-receive-pack", strings.NewReader(""))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("workspace_id", "ws-1")
	rctx.URLParams.Add("*", "missing-repo/git-receive-pack")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()

	s.handleGit(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for disabled push (not 404 from repo resolution), got %d body=%s",
			w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "SPINE_GIT_RECEIVE_PACK_ENABLED") {
		t.Errorf("expected flag-name guidance in body, got: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "missing-repo") {
		t.Errorf("response leaks repository ID: %s", w.Body.String())
	}
}

// TestHandleGit_PerRepoRouting_PrimaryStillWorks asserts that the
// existing `/git/{ws}/info/refs` URLs (no repository segment) keep
// resolving to the workspace primary path — the new routing must
// not regress single-repo deployments.
func TestHandleGit_PerRepoRouting_PrimaryStillWorks(t *testing.T) {
	primaryPath := t.TempDir()
	pool, resolver := newTestPool(t, map[string]string{"code": t.TempDir()})

	wsResolver := &poolStubResolver{cfg: workspace.Config{
		ID:       "ws-1",
		RepoPath: primaryPath,
		Status:   workspace.StatusActive,
	}}
	handler, err := githttp.NewHandler(githttp.Config{
		ResolveRepoPath: func(_ context.Context, _ string) (string, error) {
			return "/x", nil
		},
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	s := &Server{
		gitHTTP:    handler,
		wsResolver: wsResolver,
		gitPool:    pool,
		devMode:    true,
	}

	req := httptest.NewRequest("GET",
		"/git/ws-1/info/refs?service=git-upload-pack", http.NoBody)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("workspace_id", "ws-1")
	rctx.URLParams.Add("*", "info/refs")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()

	s.handleGit(w, req)

	if len(resolver.lookups) != 0 {
		t.Errorf("primary path must skip the pool resolver, got lookups=%v",
			resolver.lookups)
	}
	if w.Code == http.StatusUnauthorized || w.Code == http.StatusNotFound {
		t.Errorf("legacy primary URL should reach the inner handler, got %d body=%s",
			w.Code, w.Body.String())
	}
}
