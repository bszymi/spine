package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// NewTempRepo creates a temporary Git repository for testing.
// The repository is initialized with an empty commit and cleaned up
// when the test ends.
func NewTempRepo(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()

	commands := [][]string{
		{"git", "init", "-b", "main"},
		{"git", "config", "user.email", "test@spine.local"},
		{"git", "config", "user.name", "Spine Test"},
		{"git", "commit", "--allow-empty", "-m", "initial commit"},
	}

	for _, args := range commands {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z",
			"GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git command %v failed: %v\n%s", args, err, out)
		}
	}

	return dir
}

// WriteFile creates a file in the given directory with the specified content.
// Parent directories are created as needed.
func WriteFile(t *testing.T, dir, relPath, content string) {
	t.Helper()

	fullPath := filepath.Join(dir, relPath)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(fullPath), err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", fullPath, err)
	}
}

// GitAdd stages and commits a file in the given repository.
func GitAdd(t *testing.T, repoDir, relPath, message string) {
	t.Helper()

	run := func(args ...string) {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repoDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	run("git", "add", relPath)
	run("git", "commit", "-m", message)
}
