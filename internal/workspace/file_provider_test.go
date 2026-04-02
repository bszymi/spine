package workspace

import (
	"context"
	"testing"
)

func TestFileProvider_Resolve(t *testing.T) {
	// Set up env for test
	t.Setenv("SPINE_WORKSPACE_ID", "ws-test")
	t.Setenv("SPINE_DATABASE_URL", "postgres://localhost/test")
	t.Setenv("SPINE_REPO_PATH", "/tmp/repo")

	p := NewFileProvider()
	ctx := context.Background()

	cfg, err := p.Resolve(ctx, "ws-test")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if cfg.ID != "ws-test" {
		t.Errorf("expected ID %q, got %q", "ws-test", cfg.ID)
	}
	if cfg.DatabaseURL != "postgres://localhost/test" {
		t.Errorf("expected DatabaseURL %q, got %q", "postgres://localhost/test", cfg.DatabaseURL)
	}
	if cfg.RepoPath != "/tmp/repo" {
		t.Errorf("expected RepoPath %q, got %q", "/tmp/repo", cfg.RepoPath)
	}
	if cfg.Status != StatusActive {
		t.Errorf("expected Status %q, got %q", StatusActive, cfg.Status)
	}
}

func TestFileProvider_Resolve_AnyID(t *testing.T) {
	// In single mode, any workspace ID returns the configured workspace.
	t.Setenv("SPINE_WORKSPACE_ID", "ws-main")
	t.Setenv("SPINE_DATABASE_URL", "")
	t.Setenv("SPINE_REPO_PATH", "")

	p := NewFileProvider()
	ctx := context.Background()

	cfg, err := p.Resolve(ctx, "some-other-id")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if cfg.ID != "ws-main" {
		t.Errorf("expected ID %q, got %q", "ws-main", cfg.ID)
	}
}

func TestFileProvider_Resolve_Defaults(t *testing.T) {
	t.Setenv("SPINE_WORKSPACE_ID", "")
	t.Setenv("SPINE_DATABASE_URL", "")
	t.Setenv("SPINE_REPO_PATH", "")

	p := NewFileProvider()
	ctx := context.Background()

	cfg, err := p.Resolve(ctx, "anything")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if cfg.ID != "default" {
		t.Errorf("expected default ID %q, got %q", "default", cfg.ID)
	}
	if cfg.RepoPath != "." {
		t.Errorf("expected default RepoPath %q, got %q", ".", cfg.RepoPath)
	}
}

func TestFileProvider_List(t *testing.T) {
	t.Setenv("SPINE_WORKSPACE_ID", "ws-list")
	t.Setenv("SPINE_DATABASE_URL", "postgres://localhost/list")
	t.Setenv("SPINE_REPO_PATH", "/tmp/list")

	p := NewFileProvider()
	ctx := context.Background()

	configs, err := p.List(ctx)
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(configs) != 1 {
		t.Fatalf("expected 1 config, got %d", len(configs))
	}
	if configs[0].ID != "ws-list" {
		t.Errorf("expected ID %q, got %q", "ws-list", configs[0].ID)
	}
}
