package harness

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/testutil"
)

// TestRepo wraps a temporary Git repository for scenario testing.
type TestRepo struct {
	Dir string
	Git *git.CLIClient
}

// NewTestRepo creates a temporary Git repository initialized with a main
// branch and an empty initial commit. Cleanup is automatic via t.TempDir().
func NewTestRepo(t *testing.T) *TestRepo {
	t.Helper()

	dir := testutil.NewTempRepo(t)
	return &TestRepo{
		Dir: dir,
		Git: git.NewCLIClient(dir),
	}
}

// WriteArtifact creates an artifact file at the given path with the given content.
// Parent directories are created as needed.
func (r *TestRepo) WriteArtifact(t *testing.T, path, content string) {
	t.Helper()
	testutil.WriteFile(t, r.Dir, path, content)
}

// CommitAll stages all changes and creates a commit.
func (r *TestRepo) CommitAll(t *testing.T, message string) {
	t.Helper()
	testutil.GitAdd(t, r.Dir, ".", message)
}

// HeadSHA returns the current HEAD commit SHA.
func (r *TestRepo) HeadSHA(t *testing.T) string {
	t.Helper()

	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = r.Dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}
	return strings.TrimSpace(string(out))
}
