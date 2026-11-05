package testutil_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bszymi/spine/testutil"
)

func TestNewTempRepo(t *testing.T) {
	repo := testutil.NewTempRepo(t)

	// Verify .git directory exists
	info, err := os.Stat(filepath.Join(repo, ".git"))
	if err != nil {
		t.Fatalf("expected .git directory: %v", err)
	}
	if !info.IsDir() {
		t.Fatal(".git is not a directory")
	}

	// Verify initial commit exists
	cmd := exec.Command("git", "log", "--oneline")
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git log failed: %v\n%s", err, out)
	}
	if len(out) == 0 {
		t.Fatal("expected at least one commit")
	}
}

func TestWriteFileAndGitAdd(t *testing.T) {
	repo := testutil.NewTempRepo(t)

	testutil.WriteFile(t, repo, "governance/test.md", "---\ntype: Governance\n---\n# Test\n")
	testutil.GitAdd(t, repo, "governance/test.md", "add test artifact")

	// Verify file is committed
	cmd := exec.Command("git", "log", "--oneline")
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git log failed: %v\n%s", err, out)
	}

	// Should have 2 commits: initial + our add
	lines := 0
	for _, b := range out {
		if b == '\n' {
			lines++
		}
	}
	if lines < 2 {
		t.Fatalf("expected at least 2 commits, got output:\n%s", out)
	}

	// Verify file content
	content, err := os.ReadFile(filepath.Join(repo, "governance/test.md"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(content) != "---\ntype: Governance\n---\n# Test\n" {
		t.Fatalf("unexpected content: %s", content)
	}
}

// TestNoGitIdentity: under the helper git commits with no identity it is
// not given, on any host (a macOS GECOS name and host name included), while
// a repository's own identity and explicit variables still commit — and
// NewTempRepo, which sets a repo-local identity, keeps working.
func TestNoGitIdentity(t *testing.T) {
	testutil.NoGitIdentity(t)
	dir := t.TempDir()
	git := func(env []string, args ...string) (string, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := git(nil, "init", "-q", "-b", "main"); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if out, err := git(nil, "commit", "-q", "--allow-empty", "-m", "x"); err == nil || !strings.Contains(out, "auto-detection is disabled") {
		t.Fatalf("a commit with no identity: err=%v, want git's refusal\n%s", err, out)
	}
	if out, err := git([]string{"GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@example.test", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@example.test"},
		"commit", "-q", "--allow-empty", "-m", "x"); err != nil {
		t.Fatalf("a commit with explicit variables: %v %s", err, out)
	}
	if out, err := git(nil, "-c", "user.name=b", "-c", "user.email=b@example.test", "commit", "-q", "--allow-empty", "-m", "y"); err != nil {
		t.Fatalf("a commit with a configured identity: %v %s", err, out)
	}
	repo := testutil.NewTempRepo(t) // its repo-local identity
	testutil.WriteFile(t, repo, "a.md", "a\n")
	testutil.GitAdd(t, repo, "a.md", "add a")
}
