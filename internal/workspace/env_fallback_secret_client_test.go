package workspace

import (
	"context"
	"errors"
	"testing"

	"github.com/bszymi/spine/internal/secrets"
)

func TestEnvFallbackSecretClient_FallbackOnNotFound(t *testing.T) {
	t.Setenv("SPINE_DATABASE_URL", "postgres://fallback/db")

	inner := &stubSecretClient{} // every Get returns ErrSecretNotFound
	c, err := NewEnvFallbackSecretClient(inner, "default", "SPINE_DATABASE_URL")
	if err != nil {
		t.Fatalf("NewEnvFallbackSecretClient: %v", err)
	}

	v, vid, err := c.Get(context.Background(), secrets.WorkspaceRef("default", secrets.PurposeRuntimeDB))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := string(v.Reveal()); got != "postgres://fallback/db" {
		t.Errorf("Reveal = %q, want fallback value", got)
	}
	if vid == "" {
		t.Errorf("VersionID empty; expected synthetic env-fallback marker")
	}
}

func TestEnvFallbackSecretClient_PassesThroughOnHit(t *testing.T) {
	t.Setenv("SPINE_DATABASE_URL", "postgres://fallback-should-not-fire")

	inner := &stubSecretClient{values: map[secrets.SecretRef]string{
		secrets.WorkspaceRef("default", secrets.PurposeRuntimeDB): "postgres://real-store/value",
	}}
	c, err := NewEnvFallbackSecretClient(inner, "default", "SPINE_DATABASE_URL")
	if err != nil {
		t.Fatalf("NewEnvFallbackSecretClient: %v", err)
	}

	v, _, err := c.Get(context.Background(), secrets.WorkspaceRef("default", secrets.PurposeRuntimeDB))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := string(v.Reveal()); got != "postgres://real-store/value" {
		t.Errorf("Reveal = %q, want real-store value (file mount wins)", got)
	}
}

func TestEnvFallbackSecretClient_DoesNotSwallowOtherErrors(t *testing.T) {
	t.Setenv("SPINE_DATABASE_URL", "postgres://this-should-not-be-reached")

	cases := map[string]error{
		"access_denied":     secrets.ErrAccessDenied,
		"secret_store_down": secrets.ErrSecretStoreDown,
		"invalid_ref":       secrets.ErrInvalidRef,
	}

	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			inner := &stubSecretClient{errs: map[secrets.SecretRef]error{
				secrets.WorkspaceRef("default", secrets.PurposeRuntimeDB): want,
			}}
			c, err := NewEnvFallbackSecretClient(inner, "default", "SPINE_DATABASE_URL")
			if err != nil {
				t.Fatalf("NewEnvFallbackSecretClient: %v", err)
			}
			_, _, err = c.Get(context.Background(), secrets.WorkspaceRef("default", secrets.PurposeRuntimeDB))
			if !errors.Is(err, want) {
				t.Errorf("err = %v, want %v (must not be swallowed)", err, want)
			}
		})
	}
}

func TestEnvFallbackSecretClient_OnlyDecoratesRuntimeDBRef(t *testing.T) {
	// Other refs (projection_db, git, or runtime_db for a different
	// workspace) must NOT be rerouted to the env var even on
	// ErrSecretNotFound. This is the security envelope of the shim:
	// only the canonical default/runtime_db is allowed to fall back.
	t.Setenv("SPINE_DATABASE_URL", "postgres://leaky-fallback")

	c, err := NewEnvFallbackSecretClient(&stubSecretClient{}, "default", "SPINE_DATABASE_URL")
	if err != nil {
		t.Fatalf("NewEnvFallbackSecretClient: %v", err)
	}

	otherRefs := []secrets.SecretRef{
		secrets.WorkspaceRef("default", secrets.PurposeProjectionDB),
		secrets.WorkspaceRef("default", secrets.PurposeGit),
		secrets.WorkspaceRef("acme", secrets.PurposeRuntimeDB),
	}
	for _, ref := range otherRefs {
		_, _, err := c.Get(context.Background(), ref)
		if !errors.Is(err, secrets.ErrSecretNotFound) {
			t.Errorf("ref %q: err = %v, want ErrSecretNotFound (no env fallback)", ref, err)
		}
	}
}

func TestEnvFallbackSecretClient_EmptyEnvDoesNotMaskNotFound(t *testing.T) {
	// SPINE_DATABASE_URL unset → fall through to the original
	// ErrSecretNotFound rather than synthesizing an empty SecretValue.
	t.Setenv("SPINE_DATABASE_URL", "")

	c, err := NewEnvFallbackSecretClient(&stubSecretClient{}, "default", "SPINE_DATABASE_URL")
	if err != nil {
		t.Fatalf("NewEnvFallbackSecretClient: %v", err)
	}

	_, _, err = c.Get(context.Background(), secrets.WorkspaceRef("default", secrets.PurposeRuntimeDB))
	if !errors.Is(err, secrets.ErrSecretNotFound) {
		t.Errorf("err = %v, want ErrSecretNotFound", err)
	}
}

func TestEnvFallbackSecretClient_RequiredArgs(t *testing.T) {
	cases := []struct {
		name      string
		inner     secrets.SecretClient
		defaultID string
		envName   string
	}{
		{"nil inner", nil, "default", "SPINE_DATABASE_URL"},
		{"empty defaultID", &stubSecretClient{}, "", "SPINE_DATABASE_URL"},
		{"empty envName", &stubSecretClient{}, "default", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewEnvFallbackSecretClient(c.inner, c.defaultID, c.envName); err == nil {
				t.Errorf("expected error for %s, got nil", c.name)
			}
		})
	}
}

func TestEnvFallbackSecretClient_InvalidateForwarded(t *testing.T) {
	// Invalidate must hit the inner client even though the shim has
	// no cache state of its own — otherwise a pool eviction triggered
	// by the platform would leave a stale value cached one layer down.
	var seen secrets.SecretRef
	innerForwarder := forwardingClient{invalidate: func(ref secrets.SecretRef) error {
		seen = ref
		return nil
	}}

	c, err := NewEnvFallbackSecretClient(innerForwarder, "default", "SPINE_DATABASE_URL")
	if err != nil {
		t.Fatalf("NewEnvFallbackSecretClient: %v", err)
	}
	ref := secrets.WorkspaceRef("default", secrets.PurposeRuntimeDB)
	if err := c.Invalidate(context.Background(), ref); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	if seen != ref {
		t.Errorf("inner.Invalidate ref = %q, want %q", seen, ref)
	}
}

// forwardingClient is a minimal SecretClient that forwards Invalidate
// to the supplied callback and is unused for Get.
type forwardingClient struct {
	invalidate func(secrets.SecretRef) error
}

func (forwardingClient) Get(_ context.Context, ref secrets.SecretRef) (secrets.SecretValue, secrets.VersionID, error) {
	return secrets.SecretValue{}, "", secrets.ErrSecretNotFound
}

func (f forwardingClient) Invalidate(_ context.Context, ref secrets.SecretRef) error {
	return f.invalidate(ref)
}

func TestNotFoundSecretClient_AlwaysNotFound(t *testing.T) {
	c := NotFoundSecretClient{}
	_, _, err := c.Get(context.Background(), secrets.WorkspaceRef("any", secrets.PurposeRuntimeDB))
	if !errors.Is(err, secrets.ErrSecretNotFound) {
		t.Errorf("err = %v, want ErrSecretNotFound", err)
	}
	if err := c.Invalidate(context.Background(), secrets.WorkspaceRef("any", secrets.PurposeRuntimeDB)); err != nil {
		t.Errorf("Invalidate err = %v, want nil", err)
	}
}
