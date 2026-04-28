package workspace

import (
	"context"
	"errors"
	"testing"

	"github.com/bszymi/spine/internal/secrets"
)

// stubSecretClient is a minimal in-memory SecretClient for FileProvider
// tests. The Get callback returns whatever values the test sets;
// Invalidate is a no-op so tests don't have to mock cache state.
type stubSecretClient struct {
	values map[secrets.SecretRef]string
	errs   map[secrets.SecretRef]error
}

func (s *stubSecretClient) Get(_ context.Context, ref secrets.SecretRef) (secrets.SecretValue, secrets.VersionID, error) {
	if err, ok := s.errs[ref]; ok {
		return secrets.SecretValue{}, "", err
	}
	if v, ok := s.values[ref]; ok {
		return secrets.NewSecretValue([]byte(v)), "v1", nil
	}
	return secrets.SecretValue{}, "", secrets.ErrSecretNotFound
}

func (s *stubSecretClient) Invalidate(_ context.Context, _ secrets.SecretRef) error { return nil }

// fileProviderShim builds the same SecretClient stack the cmd_serve
// wiring uses for WORKSPACE_RESOLVER=file: a stub backend wrapped in
// EnvFallbackSecretClient.
func fileProviderShim(t *testing.T, defaultID string, inner secrets.SecretClient) *EnvFallbackSecretClient {
	t.Helper()
	shim, err := NewEnvFallbackSecretClient(inner, defaultID, "SPINE_DATABASE_URL")
	if err != nil {
		t.Fatalf("NewEnvFallbackSecretClient: %v", err)
	}
	return shim
}

func TestFileProvider_Resolve_FileMountHit(t *testing.T) {
	t.Setenv("SPINE_WORKSPACE_ID", "ws-test")
	t.Setenv("SPINE_REPO_PATH", "/tmp/repo")
	t.Setenv("SPINE_DATABASE_URL", "")

	inner := &stubSecretClient{values: map[secrets.SecretRef]string{
		secrets.WorkspaceRef("ws-test", secrets.PurposeRuntimeDB): "postgres://localhost/test",
	}}
	p := NewFileProvider(fileProviderShim(t, "ws-test", inner))

	cfg, err := p.Resolve(context.Background(), "ws-test")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.ID != "ws-test" {
		t.Errorf("ID = %q, want %q", cfg.ID, "ws-test")
	}
	if got := string(cfg.DatabaseURL.Reveal()); got != "postgres://localhost/test" {
		t.Errorf("DatabaseURL.Reveal() = %q, want %q", got, "postgres://localhost/test")
	}
	if cfg.RepoPath != "/tmp/repo" {
		t.Errorf("RepoPath = %q, want %q", cfg.RepoPath, "/tmp/repo")
	}
	if cfg.Status != StatusActive {
		t.Errorf("Status = %q", cfg.Status)
	}
}

func TestFileProvider_Resolve_EnvFallback(t *testing.T) {
	// File provider returns ErrSecretNotFound; SPINE_DATABASE_URL must
	// take effect through the bootstrap shim.
	t.Setenv("SPINE_WORKSPACE_ID", "default")
	t.Setenv("SPINE_REPO_PATH", "")
	t.Setenv("SPINE_DATABASE_URL", "postgres://env-fallback/dev")

	inner := &stubSecretClient{} // every Get returns ErrSecretNotFound
	p := NewFileProvider(fileProviderShim(t, "default", inner))

	cfg, err := p.Resolve(context.Background(), "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := string(cfg.DatabaseURL.Reveal()); got != "postgres://env-fallback/dev" {
		t.Errorf("DatabaseURL = %q, want env-fallback value", got)
	}
}

func TestFileProvider_Resolve_NeitherSet_EmptyDB(t *testing.T) {
	// Inner client returns ErrSecretNotFound and SPINE_DATABASE_URL is
	// unset → DatabaseURL stays empty (no DB), matching behaviour
	// before TASK-008 when SPINE_DATABASE_URL was unset.
	t.Setenv("SPINE_WORKSPACE_ID", "default")
	t.Setenv("SPINE_REPO_PATH", "")
	t.Setenv("SPINE_DATABASE_URL", "")

	p := NewFileProvider(fileProviderShim(t, "default", &stubSecretClient{}))

	cfg, err := p.Resolve(context.Background(), "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := string(cfg.DatabaseURL.Reveal()); got != "" {
		t.Errorf("DatabaseURL = %q, want empty", got)
	}
}

func TestFileProvider_Resolve_AccessDenied_FailsClosed(t *testing.T) {
	// Errors other than ErrSecretNotFound from the underlying provider
	// are not swallowed by the bootstrap shim; the resolver must fail
	// closed with ErrWorkspaceUnavailable so a request handler doesn't
	// silently run without a DB.
	t.Setenv("SPINE_WORKSPACE_ID", "default")
	t.Setenv("SPINE_REPO_PATH", "")
	t.Setenv("SPINE_DATABASE_URL", "postgres://this-is-the-fallback") // would succeed if shim swallowed AD

	inner := &stubSecretClient{errs: map[secrets.SecretRef]error{
		secrets.WorkspaceRef("default", secrets.PurposeRuntimeDB): secrets.ErrAccessDenied,
	}}
	p := NewFileProvider(fileProviderShim(t, "default", inner))

	_, err := p.Resolve(context.Background(), "")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !errors.Is(err, ErrWorkspaceUnavailable) {
		t.Errorf("err = %v, want ErrWorkspaceUnavailable", err)
	}
}

func TestFileProvider_Resolve_WrongID(t *testing.T) {
	t.Setenv("SPINE_WORKSPACE_ID", "ws-main")
	t.Setenv("SPINE_REPO_PATH", "")
	t.Setenv("SPINE_DATABASE_URL", "")

	p := NewFileProvider(fileProviderShim(t, "ws-main", &stubSecretClient{}))

	_, err := p.Resolve(context.Background(), "wrong-id")
	if !errors.Is(err, ErrWorkspaceNotFound) {
		t.Errorf("err = %v, want ErrWorkspaceNotFound", err)
	}
}

func TestFileProvider_Resolve_Defaults(t *testing.T) {
	t.Setenv("SPINE_WORKSPACE_ID", "")
	t.Setenv("SPINE_REPO_PATH", "")
	t.Setenv("SPINE_DATABASE_URL", "")

	p := NewFileProvider(fileProviderShim(t, "default", &stubSecretClient{}))

	cfg, err := p.Resolve(context.Background(), "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.ID != "default" {
		t.Errorf("ID = %q, want %q", cfg.ID, "default")
	}
	if cfg.RepoPath != "." {
		t.Errorf("RepoPath = %q, want %q", cfg.RepoPath, ".")
	}
}

func TestFileProvider_NilSecretClient_NoDB(t *testing.T) {
	// Passing nil SecretClient is a supported "no DB" configuration —
	// used by tooling that only reads governance artefacts. DatabaseURL
	// must come back empty without consulting any provider.
	t.Setenv("SPINE_WORKSPACE_ID", "ws-tool")
	t.Setenv("SPINE_REPO_PATH", "")
	t.Setenv("SPINE_DATABASE_URL", "postgres://should-be-ignored")

	p := NewFileProvider(nil)
	cfg, err := p.Resolve(context.Background(), "ws-tool")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := string(cfg.DatabaseURL.Reveal()); got != "" {
		t.Errorf("DatabaseURL = %q, want empty (nil client)", got)
	}
}

func TestFileProvider_List(t *testing.T) {
	t.Setenv("SPINE_WORKSPACE_ID", "ws-list")
	t.Setenv("SPINE_REPO_PATH", "/tmp/list")
	t.Setenv("SPINE_DATABASE_URL", "postgres://localhost/list")

	p := NewFileProvider(fileProviderShim(t, "ws-list", &stubSecretClient{}))

	configs, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(configs) != 1 {
		t.Fatalf("len(configs) = %d, want 1", len(configs))
	}
	if configs[0].ID != "ws-list" {
		t.Errorf("ID = %q, want %q", configs[0].ID, "ws-list")
	}
	if got := string(configs[0].DatabaseURL.Reveal()); got != "postgres://localhost/list" {
		t.Errorf("DatabaseURL = %q, want env-fallback value", got)
	}
}
