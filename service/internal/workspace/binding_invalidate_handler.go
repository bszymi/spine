package workspace

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// BindingInvalidator drops cached state for a workspace so the next
// resolution refetches the binding and re-derives connection pools.
// Implementations are safe to call concurrently and idempotent: a
// call for an unknown workspace ID is a no-op.
//
// The platform-binding webhook handler calls this in response to
// POST /internal/v1/workspaces/{workspace_id}/binding-invalidate
// (ADR-011).
type BindingInvalidator interface {
	InvalidateBinding(workspaceID string)
}

// CombinedBindingInvalidator fans an invalidation out to a
// PlatformBindingProvider (drops the cached binding) and a
// ServicePool (closes the cached service set, including the
// connection pool). Either field may be nil — the invalidator
// simply skips that side.
//
// The provider is invalidated *before* the pool so that any
// in-flight Resolve that sneaks in between the two operations sees
// a missing binding rather than a stale one.
type CombinedBindingInvalidator struct {
	Provider *PlatformBindingProvider
	Pool     *ServicePool
}

// InvalidateBinding implements BindingInvalidator.
func (c *CombinedBindingInvalidator) InvalidateBinding(workspaceID string) {
	if c.Provider != nil {
		c.Provider.Invalidate(workspaceID)
	}
	if c.Pool != nil {
		c.Pool.Evict(workspaceID)
	}
}

// BindingInvalidateHandlerConfig configures a webhook handler.
type BindingInvalidateHandlerConfig struct {
	// Invalidator is called on every authorized request. Required.
	Invalidator BindingInvalidator

	// ServiceToken is the bearer token the platform must present.
	// Compared in constant time against the Authorization header.
	// Required.
	ServiceToken string
}

// NewBindingInvalidateHandler returns the HTTP handler for the
// platform → Spine invalidation webhook. The handler enforces:
//
//   - POST method only (405 otherwise).
//   - Authorization: Bearer {ServiceToken} (401 otherwise).
//   - {workspace_id} path param matches ValidateID (400 otherwise).
//
// On success it calls Invalidator.InvalidateBinding(workspace_id)
// and returns 204 No Content. The handler does not echo the
// workspace ID in error bodies and never logs the bearer token.
func NewBindingInvalidateHandler(cfg BindingInvalidateHandlerConfig) (http.Handler, error) {
	if cfg.Invalidator == nil {
		return nil, errors.New("binding invalidate handler: Invalidator is required")
	}
	if cfg.ServiceToken == "" {
		return nil, errors.New("binding invalidate handler: ServiceToken is required")
	}
	tokenBytes := []byte(cfg.ServiceToken)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !validBearerToken(r.Header.Get("Authorization"), tokenBytes) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		workspaceID := chi.URLParam(r, "workspace_id")
		if err := ValidateID(workspaceID); err != nil {
			http.Error(w, "invalid workspace_id", http.StatusBadRequest)
			return
		}
		cfg.Invalidator.InvalidateBinding(workspaceID)
		w.WriteHeader(http.StatusNoContent)
	}), nil
}

// validBearerToken does a constant-time compare on the trimmed
// Authorization header.
func validBearerToken(header string, want []byte) bool {
	const prefix = "Bearer "
	if len(header) <= len(prefix) {
		return false
	}
	if !strings.EqualFold(header[:len(prefix)], prefix) {
		return false
	}
	provided := []byte(header[len(prefix):])
	return subtle.ConstantTimeCompare(provided, want) == 1
}
