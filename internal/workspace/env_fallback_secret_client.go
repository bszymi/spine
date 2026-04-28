package workspace

import (
	"context"
	"errors"
	"os"

	"github.com/bszymi/spine/internal/secrets"
)

// EnvFallbackSecretClient is a single-workspace bootstrap decorator
// over a real secrets.SecretClient. It intercepts exactly one ref,
// `secret-store://workspaces/{defaultID}/runtime_db`, and falls back
// to SPINE_DATABASE_URL when the underlying provider returns
// ErrSecretNotFound. Every other ref — and every error other than
// ErrSecretNotFound on the decorated ref — passes through untouched.
//
// This shim exists so dev workflows that set `SPINE_DATABASE_URL`
// continue to work without polluting the secrets package with
// env-var awareness or whitelisting file_provider.go in EPIC-001's
// "no direct env-var reads of workspace credentials" CI grep
// (ADR-010, TASK-008).
//
// Construct with NewEnvFallbackSecretClient. Wired only when
// WORKSPACE_RESOLVER=file. Precedence: the decorated client wins;
// the env var is the fallback.
type EnvFallbackSecretClient struct {
	inner     secrets.SecretClient
	defaultID string
	envName   string
}

// NewEnvFallbackSecretClient wraps inner so that the canonical
// runtime_db ref for defaultID falls back to os.Getenv(envName) on
// ErrSecretNotFound. Returns an error if any required argument is
// missing — fail-fast at startup beats a confusing miss at request
// time.
func NewEnvFallbackSecretClient(inner secrets.SecretClient, defaultID, envName string) (*EnvFallbackSecretClient, error) {
	if inner == nil {
		return nil, errors.New("env-fallback secret client: inner client is required")
	}
	if defaultID == "" {
		return nil, errors.New("env-fallback secret client: default workspace ID is required")
	}
	if envName == "" {
		return nil, errors.New("env-fallback secret client: env var name is required")
	}
	return &EnvFallbackSecretClient{inner: inner, defaultID: defaultID, envName: envName}, nil
}

// envFallbackVersion is the synthetic VersionID returned when the
// env-var fallback supplies a value. It is intentionally distinct
// from any real provider VersionID so callers that compare versions
// (e.g., pool rotation triggers per ADR-012) can detect a fallback
// hit without parsing the URL itself.
const envFallbackVersion secrets.VersionID = "env-fallback"

// Get fetches ref through the inner client. For the canonical
// runtime_db ref of the configured default workspace, an
// ErrSecretNotFound from the inner client is replaced with a value
// read from envName. All other refs and all other errors pass
// through unchanged so this shim cannot mask provider failures
// (store down, access denied, malformed ref).
func (e *EnvFallbackSecretClient) Get(ctx context.Context, ref secrets.SecretRef) (secrets.SecretValue, secrets.VersionID, error) {
	v, vid, err := e.inner.Get(ctx, ref)
	if err == nil {
		return v, vid, nil
	}
	if !errors.Is(err, secrets.ErrSecretNotFound) {
		return v, vid, err
	}
	if ref != secrets.WorkspaceRef(e.defaultID, secrets.PurposeRuntimeDB) {
		return v, vid, err
	}
	raw := os.Getenv(e.envName)
	if raw == "" {
		return v, vid, err
	}
	return secrets.NewSecretValue([]byte(raw)), envFallbackVersion, nil
}

// Invalidate forwards to the inner client. The env-var fallback has
// no cache state of its own — re-reads happen on every Get.
func (e *EnvFallbackSecretClient) Invalidate(ctx context.Context, ref secrets.SecretRef) error {
	return e.inner.Invalidate(ctx, ref)
}

// Compile-time guard.
var _ secrets.SecretClient = (*EnvFallbackSecretClient)(nil)

// NotFoundSecretClient is a SecretClient that returns
// ErrSecretNotFound for every Get and is a no-op for every
// Invalidate. It is used as the inner client for
// EnvFallbackSecretClient in deployments that have not configured a
// real secrets backend (e.g. single-workspace dev) — every Get path
// then falls through to the env-var fallback for the canonical
// default/runtime_db ref and otherwise returns ErrSecretNotFound.
type NotFoundSecretClient struct{}

// Get always returns ErrSecretNotFound for ref.
func (NotFoundSecretClient) Get(_ context.Context, ref secrets.SecretRef) (secrets.SecretValue, secrets.VersionID, error) {
	return secrets.SecretValue{}, "", errors.Join(secrets.ErrSecretNotFound, errors.New(string(ref)))
}

// Invalidate is a no-op.
func (NotFoundSecretClient) Invalidate(_ context.Context, _ secrets.SecretRef) error { return nil }

// Compile-time guard.
var _ secrets.SecretClient = NotFoundSecretClient{}
