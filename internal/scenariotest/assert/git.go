package assert

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/scenariotest/harness"
)

// FileExists asserts that a file exists in the test repository.
func FileExists(t *testing.T, repo *harness.TestRepo, path string) {
	t.Helper()
	fullPath := filepath.Join(repo.Dir, path)
	if _, err := os.Stat(fullPath); err != nil {
		t.Errorf("expected file %s to exist: %v", path, err)
	}
}

// FileNotExists asserts that a file does not exist in the test repository.
func FileNotExists(t *testing.T, repo *harness.TestRepo, path string) {
	t.Helper()
	fullPath := filepath.Join(repo.Dir, path)
	if _, err := os.Stat(fullPath); err == nil {
		t.Errorf("expected file %s to not exist, but it does", path)
	}
}

// FileContains asserts that a file in the test repository contains the given substring.
func FileContains(t *testing.T, repo *harness.TestRepo, path, substring string) {
	t.Helper()
	fullPath := filepath.Join(repo.Dir, path)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !strings.Contains(string(data), substring) {
		t.Errorf("file %s does not contain %q", path, substring)
	}
}

// FileNotContains asserts that a file does not contain the given substring.
func FileNotContains(t *testing.T, repo *harness.TestRepo, path, substring string) {
	t.Helper()
	fullPath := filepath.Join(repo.Dir, path)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if strings.Contains(string(data), substring) {
		t.Errorf("file %s should not contain %q", path, substring)
	}
}

// CommitCount asserts the number of commits in the repository.
func CommitCount(t *testing.T, repo *harness.TestRepo, expected int) {
	t.Helper()
	cmd := exec.Command("git", "rev-list", "--count", "HEAD")
	cmd.Dir = repo.Dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-list --count HEAD: %v", err)
	}
	got, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("parse commit count: %v", err)
	}
	if got != expected {
		t.Errorf("commit count: got %d, want %d", got, expected)
	}
}

// LastCommitMessage asserts that the most recent commit message contains the given substring.
func LastCommitMessage(t *testing.T, repo *harness.TestRepo, contains string) {
	t.Helper()
	cmd := exec.Command("git", "log", "-1", "--format=%s")
	cmd.Dir = repo.Dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git log -1: %v", err)
	}
	msg := strings.TrimSpace(string(out))
	if !strings.Contains(msg, contains) {
		t.Errorf("last commit message %q does not contain %q", msg, contains)
	}
}

// BranchExists asserts that a branch exists in the repository.
func BranchExists(t *testing.T, repo *harness.TestRepo, branch string) {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "--verify", branch)
	cmd.Dir = repo.Dir
	if err := cmd.Run(); err != nil {
		t.Errorf("expected branch %q to exist", branch)
	}
}
