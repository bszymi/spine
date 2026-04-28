package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/bszymi/spine/internal/secrets"
)

// FileProvider implements Resolver for the single-workspace deployment
// mode. The workspace identity (ID, repo path, SMP id) still comes
// from environment variables, but the runtime database credential is
// fetched through SecretClient — the same path used in
// platform-binding mode (ADR-010). Without a SecretClient the
// provider operates without a workspace database, which is sufficient
// for endpoints that don't need one.
//
// Environment variables:
//   - SPINE_WORKSPACE_ID: workspace identifier (default: "default")
//   - SPINE_REPO_PATH: filesystem path to the workspace's Git repository (default: ".")
//   - SMP_WORKSPACE_ID: optional Spine Management Platform workspace ID.
//
// SPINE_DATABASE_URL is no longer read by this provider directly. In
// dev/test, wire NewEnvFallbackSecretClient around a real
// SecretClient and the bootstrap shim will fall back to that env var
// for the canonical default/runtime_db ref.
type FileProvider struct {
	id             string
	repoPath       string
	smpWorkspaceID string
	secretClient   secrets.SecretClient
}

// NewFileProvider creates a FileProvider that reads non-credential
// fields from env at construction time. The secretClient may be nil
// if no workspace database is required (e.g., a tool that only
// reads governance artefacts).
func NewFileProvider(secretClient secrets.SecretClient) *FileProvider {
	id := os.Getenv("SPINE_WORKSPACE_ID")
	if id == "" {
		id = "default"
	}

	repoPath := os.Getenv("SPINE_REPO_PATH")
	if repoPath == "" {
		repoPath = "."
	}

	return &FileProvider{
		id:             id,
		repoPath:       repoPath,
		smpWorkspaceID: os.Getenv("SMP_WORKSPACE_ID"),
		secretClient:   secretClient,
	}
}

// Resolve returns the configured workspace, dereferencing the runtime
// database credential through SecretClient. If workspaceID is empty,
// the provider falls back to the configured workspace (backward
// compatible). A non-empty workspaceID that does not match returns
// ErrWorkspaceNotFound.
//
// On a missing runtime_db secret with no fallback, the resolver
// returns the workspace with an empty DatabaseURL — the same shape
// used by store-less endpoints in single-workspace mode before this
// task. Other errors (access denied, store down) are surfaced via
// ErrWorkspaceUnavailable so request handling fails closed instead
// of silently running without a database.
func (p *FileProvider) Resolve(ctx context.Context, workspaceID string) (*Config, error) {
	if workspaceID != "" && workspaceID != p.id {
		return nil, ErrWorkspaceNotFound
	}

	cfg := Config{
		ID:             p.id,
		DisplayName:    p.id,
		RepoPath:       p.repoPath,
		Status:         StatusActive,
		SMPWorkspaceID: p.smpWorkspaceID,
	}

	dbValue, err := p.fetchRuntimeDB(ctx)
	if err != nil {
		return nil, err
	}
	cfg.DatabaseURL = dbValue

	return &cfg, nil
}

// List returns a slice containing the single configured workspace.
func (p *FileProvider) List(ctx context.Context) ([]Config, error) {
	cfg, err := p.Resolve(ctx, p.id)
	if err != nil {
		return nil, err
	}
	return []Config{*cfg}, nil
}

func (p *FileProvider) fetchRuntimeDB(ctx context.Context) (secrets.SecretValue, error) {
	if p.secretClient == nil {
		return secrets.SecretValue{}, nil
	}
	ref := secrets.WorkspaceRef(p.id, secrets.PurposeRuntimeDB)
	v, _, err := p.secretClient.Get(ctx, ref)
	switch {
	case err == nil:
		// A backend that hands us a zero-length secret is
		// indistinguishable from a misprovisioned credential —
		// reject it rather than start with no store. Genuinely
		// "no DB configured" goes through the ErrSecretNotFound
		// path below, where the dev shim is allowed to map it to
		// an empty SecretValue.
		if len(v.Reveal()) == 0 {
			return secrets.SecretValue{}, fmt.Errorf("%w: runtime_db for %q returned empty value", ErrWorkspaceUnavailable, p.id)
		}
		return v, nil
	case errors.Is(err, secrets.ErrSecretNotFound):
		// "No DB configured" is only acceptable in the dev/file
		// bootstrap path: the EnvFallbackSecretClient has already
		// tried SPINE_DATABASE_URL and come up empty, matching the
		// behaviour callers handled before TASK-008 when the env
		// var was unset. Production backends (AWS) wrap directly,
		// without the shim, so a genuine ErrSecretNotFound here
		// means a misprovisioned secret — fail closed with
		// ErrWorkspaceUnavailable so traffic gets a 503 instead of
		// silently starting without a store.
		if _, devShim := p.secretClient.(*EnvFallbackSecretClient); devShim {
			return secrets.SecretValue{}, nil
		}
		return secrets.SecretValue{}, fmt.Errorf("%w: runtime_db missing for %q", ErrWorkspaceUnavailable, p.id)
	case errors.Is(err, secrets.ErrInvalidRef):
		return secrets.SecretValue{}, fmt.Errorf("%w: invalid runtime_db ref for %q", ErrWorkspaceNotFound, p.id)
	default:
		return secrets.SecretValue{}, fmt.Errorf("%w: runtime_db for %q: %v", ErrWorkspaceUnavailable, p.id, err)
	}
}
