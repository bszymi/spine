package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func gitAvailable(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/usr/bin/git"); err != nil {
		// Fall back to PATH lookup.
		if _, err := os.Stat("/opt/homebrew/bin/git"); err != nil {
			if _, err := os.Stat("/usr/local/bin/git"); err != nil {
				t.Skip("git not available on standard paths")
			}
		}
	}
}

func TestProvisionRepo_Fresh(t *testing.T) {
	gitAvailable(t)

	base := t.TempDir()
	rp := NewRepoProvisioner(base)

	path, err := rp.ProvisionRepo(context.Background(), "ws-fresh", "")
	if err != nil {
		t.Fatalf("ProvisionRepo: %v", err)
	}
	if path == "" {
		t.Fatal("empty repo path")
	}

	// Fresh mode creates the repo under baseDir/workspaceID and initializes
	// Spine's structure with a first commit.
	wantPath := filepath.Join(base, "ws-fresh")
	if path != wantPath {
		t.Errorf("path = %q, want %q", path, wantPath)
	}
	if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
		t.Errorf(".git not created: %v", err)
	}
	if !IsSpineRepo(path) {
		t.Errorf("fresh repo should be recognized as a Spine repo")
	}
}

func TestProvisionRepo_AlreadyExists(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "ws-existing"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	rp := NewRepoProvisioner(base)

	_, err := rp.ProvisionRepo(context.Background(), "ws-existing", "")
	if err == nil {
		t.Fatal("expected error when repo dir exists")
	}
}

func TestProvisionRepo_InvalidGitURL(t *testing.T) {
	base := t.TempDir()
	rp := NewRepoProvisioner(base)

	_, err := rp.ProvisionRepo(context.Background(), "ws-bad-url", "not-a-valid-scheme://bad")
	if err == nil {
		t.Fatal("expected error for invalid git URL")
	}
	// ProvisionRepo must clean up the partially-created directory on failure.
	if _, statErr := os.Stat(filepath.Join(base, "ws-bad-url")); statErr == nil {
		t.Errorf("partial directory was not cleaned up on error")
	}
}

func TestServicePool_RefCount(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	resolver := &memResolver{
		workspaces: map[string]*Config{
			"ws-1": {ID: "ws-1", RepoPath: t.TempDir(), Status: StatusActive},
		},
	}
	pool := NewServicePool(ctx, resolver, PoolConfig{})
	defer pool.Close()

	// Empty pool reports 0 for any key.
	if n := pool.RefCount("ws-1"); n != 0 {
		t.Errorf("RefCount before Get = %d, want 0", n)
	}

	// One active Get yields a non-zero refcount; Release drops it.
	if _, err := pool.Get(ctx, "ws-1"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if n := pool.RefCount("ws-1"); n == 0 {
		t.Errorf("RefCount after Get = 0, want > 0")
	}
	pool.Release("ws-1")
	if n := pool.RefCount("ws-1"); n != 0 {
		t.Errorf("RefCount after Release = %d, want 0", n)
	}

	// Unknown IDs still report 0.
	if n := pool.RefCount("does-not-exist"); n != 0 {
		t.Errorf("RefCount for unknown = %d, want 0", n)
	}
}

// memResolver is a minimal in-memory Resolver for pool tests.
type memResolver struct {
	workspaces map[string]*Config
}

func (m *memResolver) Resolve(_ context.Context, id string) (*Config, error) {
	cfg, ok := m.workspaces[id]
	if !ok {
		return nil, ErrWorkspaceNotFound
	}
	c := *cfg
	return &c, nil
}

func (m *memResolver) List(_ context.Context) ([]Config, error) {
	out := make([]Config, 0, len(m.workspaces))
	for _, c := range m.workspaces {
		out = append(out, *c)
	}
	return out, nil
}
