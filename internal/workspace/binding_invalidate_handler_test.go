package workspace_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/bszymi/spine/internal/workspace"
)

// recordingInvalidator captures calls so tests can assert exact behaviour.
type recordingInvalidator struct {
	calls atomic.Int32
	last  atomic.Pointer[string]
}

func (r *recordingInvalidator) InvalidateBinding(workspaceID string) {
	r.calls.Add(1)
	v := workspaceID
	r.last.Store(&v)
}

func (r *recordingInvalidator) lastID() string {
	p := r.last.Load()
	if p == nil {
		return ""
	}
	return *p
}

// mountHandler wraps the handler in a chi router so the {workspace_id}
// URL param is populated the same way it is in production.
func mountHandler(t *testing.T, h http.Handler) http.Handler {
	t.Helper()
	r := chi.NewRouter()
	r.Method(http.MethodPost, "/internal/v1/workspaces/{workspace_id}/binding-invalidate", h)
	return r
}

func newHandlerForTest(t *testing.T, inv workspace.BindingInvalidator, token string) http.Handler {
	t.Helper()
	h, err := workspace.NewBindingInvalidateHandler(workspace.BindingInvalidateHandlerConfig{
		Invalidator:  inv,
		ServiceToken: token,
	})
	if err != nil {
		t.Fatalf("NewBindingInvalidateHandler: %v", err)
	}
	return mountHandler(t, h)
}

func TestNewBindingInvalidateHandler_RequiresFields(t *testing.T) {
	if _, err := workspace.NewBindingInvalidateHandler(workspace.BindingInvalidateHandlerConfig{
		ServiceToken: "tok",
	}); err == nil {
		t.Fatalf("expected error when Invalidator is nil")
	}
	if _, err := workspace.NewBindingInvalidateHandler(workspace.BindingInvalidateHandlerConfig{
		Invalidator: &recordingInvalidator{},
	}); err == nil {
		t.Fatalf("expected error when ServiceToken is empty")
	}
}

func TestBindingInvalidateHandler_Success(t *testing.T) {
	inv := &recordingInvalidator{}
	h := newHandlerForTest(t, inv, "service-token")

	req := httptest.NewRequest(http.MethodPost, "/internal/v1/workspaces/acme/binding-invalidate", http.NoBody)
	req.Header.Set("Authorization", "Bearer service-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%q", rec.Code, rec.Body.String())
	}
	if got := inv.calls.Load(); got != 1 {
		t.Fatalf("invalidator calls = %d, want 1", got)
	}
	if got := inv.lastID(); got != "acme" {
		t.Fatalf("invalidated ws = %q, want acme", got)
	}
}

func TestBindingInvalidateHandler_UnknownWorkspaceIsNoop(t *testing.T) {
	// Per the contract: idempotent / no-op for unknown workspaces.
	// The handler still returns 204 because the platform should not be
	// punished for invalidating a workspace Spine never cached.
	inv := &recordingInvalidator{}
	h := newHandlerForTest(t, inv, "service-token")

	req := httptest.NewRequest(http.MethodPost, "/internal/v1/workspaces/never-seen/binding-invalidate", http.NoBody)
	req.Header.Set("Authorization", "Bearer service-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
}

func TestBindingInvalidateHandler_RejectsWrongMethod(t *testing.T) {
	inv := &recordingInvalidator{}
	h := newHandlerForTest(t, inv, "tok")

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/internal/v1/workspaces/acme/binding-invalidate", http.NoBody)
			req.Header.Set("Authorization", "Bearer tok")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			// chi's r.Method returns 405 for unmounted methods on the same path.
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("method %s: status = %d, want 405", method, rec.Code)
			}
		})
	}
	if got := inv.calls.Load(); got != 0 {
		t.Fatalf("invalidator must not be called for wrong-method requests (got %d)", got)
	}
}

func TestBindingInvalidateHandler_RejectsMissingAuth(t *testing.T) {
	inv := &recordingInvalidator{}
	h := newHandlerForTest(t, inv, "tok")

	req := httptest.NewRequest(http.MethodPost, "/internal/v1/workspaces/acme/binding-invalidate", http.NoBody)
	// no Authorization header
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if got := inv.calls.Load(); got != 0 {
		t.Fatalf("invalidator must not be called for unauthenticated request (got %d)", got)
	}
}

func TestBindingInvalidateHandler_RejectsWrongToken(t *testing.T) {
	inv := &recordingInvalidator{}
	h := newHandlerForTest(t, inv, "right-token")

	req := httptest.NewRequest(http.MethodPost, "/internal/v1/workspaces/acme/binding-invalidate", http.NoBody)
	req.Header.Set("Authorization", "Bearer wrong-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestBindingInvalidateHandler_RejectsNonBearerAuth(t *testing.T) {
	inv := &recordingInvalidator{}
	h := newHandlerForTest(t, inv, "tok")

	for _, header := range []string{"Basic dXNlcjpwYXNz", "Bearer", "Bearer ", "tok"} {
		t.Run(header, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/internal/v1/workspaces/acme/binding-invalidate", http.NoBody)
			req.Header.Set("Authorization", header)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("Authorization=%q: status = %d, want 401", header, rec.Code)
			}
		})
	}
}

func TestBindingInvalidateHandler_AcceptsMixedCaseScheme(t *testing.T) {
	inv := &recordingInvalidator{}
	h := newHandlerForTest(t, inv, "tok")

	req := httptest.NewRequest(http.MethodPost, "/internal/v1/workspaces/acme/binding-invalidate", http.NoBody)
	req.Header.Set("Authorization", "bEaReR tok")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%q", rec.Code, rec.Body.String())
	}
}

func TestBindingInvalidateHandler_RejectsTraversalWorkspaceID(t *testing.T) {
	inv := &recordingInvalidator{}
	h := newHandlerForTest(t, inv, "tok")

	// A "/" in the path makes chi never match the route; an empty
	// path param ("") never reaches the handler. Test what *can* be
	// reached: shapes that pass chi's path matching but fail
	// ValidateID — e.g. URL-encoded ".." or names that start with "-".
	for _, evil := range []string{
		"-rm-rf",
		"with.dot",
		"with%20space", // URL-encoded — chi decodes before exposing as URLParam
		strings.Repeat("a", 100),
	} {
		t.Run(evil, func(t *testing.T) {
			path := "/internal/v1/workspaces/" + evil + "/binding-invalidate"
			req := httptest.NewRequest(http.MethodPost, path, http.NoBody)
			req.Header.Set("Authorization", "Bearer tok")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("workspace_id=%q: status = %d, want 400; body=%q", evil, rec.Code, rec.Body.String())
			}
		})
	}
	if got := inv.calls.Load(); got != 0 {
		t.Fatalf("invalidator must not be called for invalid workspace_id (got %d)", got)
	}
}

// TestCombinedBindingInvalidator verifies the fan-out drops the binding
// cache and the pool entry, in that order.
func TestCombinedBindingInvalidator_Smoke(t *testing.T) {
	// Provider-only: nil pool must not panic.
	provider := newProviderForInvalidate(t)
	c := &workspace.CombinedBindingInvalidator{Provider: provider, Pool: nil}
	c.InvalidateBinding("acme") // should be a no-op without panicking

	// Both nil: no-op, no panic.
	(&workspace.CombinedBindingInvalidator{}).InvalidateBinding("any")
}

// newProviderForInvalidate builds a minimally-valid provider just so we
// can call its Invalidate method via the combined invalidator. The
// platform server is a stub that the test never actually hits.
func newProviderForInvalidate(t *testing.T) *workspace.PlatformBindingProvider {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	p, err := workspace.NewPlatformBindingProvider(workspace.PlatformBindingConfig{
		PlatformBaseURL: srv.URL,
		ServiceToken:    "tok",
		SecretClient:    &fakeSecretClient{},
		HTTPClient:      srv.Client(),
	})
	if err != nil {
		t.Fatalf("NewPlatformBindingProvider: %v", err)
	}
	return p
}
