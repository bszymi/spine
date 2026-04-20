package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/bszymi/spine/internal/branchprotect"
	"github.com/bszymi/spine/internal/githttp"
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
				gotWsID, gotPath = parseGitPath(r)
			})

			req := httptest.NewRequest("GET", tt.url, nil)
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
				gotWsID, gotPath = parseGitPath(r)
			})

			req := httptest.NewRequest("GET", tt.url, nil)
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
				gotWsID, gotPath = parseGitPath(r)
			}
			r.HandleFunc("/git/{workspace_id}/*", handler)
			r.HandleFunc("/git/*", handler)

			req := httptest.NewRequest("GET", tt.url, nil)
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
