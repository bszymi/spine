package testutil_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/bszymi/spine/internal/testutil"
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
