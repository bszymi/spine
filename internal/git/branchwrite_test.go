package git_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/testutil"
)

func createBranch(t *testing.T, repoDir, name string) {
	t.Helper()
	cmd := exec.Command("git", "branch", name)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git branch %s: %v\n%s", name, err, out)
	}
}

func resolveRef(t *testing.T, repoDir, ref string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", ref)
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git rev-parse %s: %v\n%s", ref, err, out)
	}
	return strings.TrimSpace(string(out))
}

func listWorktrees(t *testing.T, repoDir string) string {
	t.Helper()
	cmd := exec.Command("git", "worktree", "list", "--porcelain")
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git worktree list: %v\n%s", err, out)
	}
	return string(out)
}

func stagedFiles(t *testing.T, repoDir string) string {
	t.Helper()
	cmd := exec.Command("git", "diff", "--cached", "--name-only")
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git diff --cached: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestBranchWrite_EnterUnbranched(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	scope, err := git.EnterBranch(context.Background(), repo, "", nil)
	if err != nil {
		t.Fatalf("EnterBranch: %v", err)
	}
	if scope.RepoDir != repo {
		t.Errorf("expected RepoDir=%q, got %q", repo, scope.RepoDir)
	}
	if scope.Branch != "" {
		t.Errorf("expected empty Branch, got %q", scope.Branch)
	}
	// Cleanup must be a safe no-op on the unbranched scope, callable
	// multiple times.
	scope.Cleanup()
	scope.Cleanup()
}

func TestBranchWrite_EnterCreatesWorktreeAtFreshPath(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	createBranch(t, repo, "feature/x")
	want := resolveRef(t, repo, "feature/x")

	scope, err := git.EnterBranch(context.Background(), repo, "feature/x", nil)
	if err != nil {
		t.Fatalf("EnterBranch: %v", err)
	}
	defer scope.Cleanup()

	if filepath.Base(scope.RepoDir) != "wt" {
		t.Errorf("expected child name 'wt', got base=%q (RepoDir=%q)",
			filepath.Base(scope.RepoDir), scope.RepoDir)
	}

	parent := filepath.Dir(scope.RepoDir)
	info, err := os.Stat(parent)
	if err != nil {
		t.Fatalf("stat parent dir %s: %v", parent, err)
	}
	if !info.IsDir() {
		t.Errorf("parent %s should be a dir", parent)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("parent %s permission: got %o, want 0700", parent, info.Mode().Perm())
	}

	if scope.Branch != "feature/x" {
		t.Errorf("Branch: got %q, want feature/x", scope.Branch)
	}

	if got := resolveRef(t, scope.RepoDir, "HEAD"); got != want {
		t.Errorf("worktree HEAD=%s, want %s", got, want)
	}

	if listing := listWorktrees(t, repo); !strings.Contains(listing, scope.RepoDir) {
		t.Errorf("worktree list does not contain %s:\n%s", scope.RepoDir, listing)
	}
}

func TestBranchWrite_EnterValidatorRejects(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	sentinel := errors.New("bad branch")

	scope, err := git.EnterBranch(
		context.Background(), repo, "anything",
		func(string) error { return sentinel },
	)
	if scope != nil {
		t.Errorf("expected nil scope on validator failure, got %+v", scope)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error, got %v", err)
	}
}

func TestBranchWrite_EnterCleanupSurvivesCancelledContext(t *testing.T) {
	// branchwrite.go:86-94 deliberately uses a fresh bounded context
	// for the cleanup exec, so a cancelled request still removes the
	// worktree. A regression that reused the request ctx would fail
	// to start `git worktree remove` and leak .git/worktrees/<name>/.
	repo := testutil.NewTempRepo(t)
	createBranch(t, repo, "release/y")

	ctx, cancel := context.WithCancel(context.Background())
	scope, err := git.EnterBranch(ctx, repo, "release/y", nil)
	if err != nil {
		t.Fatalf("EnterBranch: %v", err)
	}
	parent := filepath.Dir(scope.RepoDir)

	// Cancel BEFORE cleanup. With the regression in place, the cleanup
	// exec would refuse to start.
	cancel()
	scope.Cleanup()

	if _, err := os.Stat(parent); !os.IsNotExist(err) {
		t.Errorf("parent dir %s should be removed, got err=%v", parent, err)
	}

	// The decisive check: .git/worktrees/wt/ metadata under the main
	// repo. With proper cleanup it's gone; with a ctx-leaking
	// regression it survives because `git worktree remove` never ran.
	wtMeta := filepath.Join(repo, ".git", "worktrees", "wt")
	if _, err := os.Stat(wtMeta); !os.IsNotExist(err) {
		t.Errorf(".git/worktrees/wt should be removed, got err=%v", err)
	}

	if listing := listWorktrees(t, repo); strings.Contains(listing, scope.RepoDir) {
		t.Errorf("worktree %s still registered after cleanup; list:\n%s",
			scope.RepoDir, listing)
	}
}

func TestBranchWrite_StageAndCommitTrailerOrdering(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	createBranch(t, repo, "trailer/branch")

	scope, err := git.EnterBranch(context.Background(), repo, "trailer/branch", nil)
	if err != nil {
		t.Fatalf("EnterBranch: %v", err)
	}
	defer scope.Cleanup()

	testutil.WriteFile(t, scope.RepoDir, "doc.md", "hello\n")

	// Trailer map intentionally given out of TrailerOrder + an
	// unknown key that should be silently dropped per branchwrite.go:107.
	res, err := git.StageAndCommit(context.Background(), scope, "doc.md", git.CommitOpts{
		Message: "Add doc",
		Trailers: map[string]string{
			"Run-ID":            "run-1",
			"Trace-ID":          "trace-1",
			"Operation":         "artifact.create",
			"Actor-ID":          "actor-1",
			"Workflow-Bypass":   "false",
			"Some-Other-Header": "should-be-dropped",
		},
		Author: git.Author{Name: "Tester", Email: "tester@example.com"},
	})
	if err != nil {
		t.Fatalf("StageAndCommit: %v", err)
	}
	if len(res.SHA) != 40 {
		t.Errorf("expected 40-char SHA, got %q", res.SHA)
	}

	cmd := exec.Command("git", "log", "-1", "--format=%B", "HEAD")
	cmd.Dir = scope.RepoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v\n%s", err, out)
	}
	body := string(out)

	wantOrdered := []string{
		"Trace-ID: trace-1",
		"Actor-ID: actor-1",
		"Run-ID: run-1",
		"Operation: artifact.create",
		"Workflow-Bypass: false",
	}
	prev := -1
	for _, line := range wantOrdered {
		idx := strings.Index(body, line)
		if idx == -1 {
			t.Errorf("missing trailer %q in body:\n%s", line, body)
			continue
		}
		if idx <= prev {
			t.Errorf("trailer %q out of order (offset %d, prev %d):\n%s",
				line, idx, prev, body)
		}
		prev = idx
	}
	if strings.Contains(body, "Some-Other-Header") {
		t.Errorf("unknown trailer leaked into body:\n%s", body)
	}
}

func TestBranchWrite_StageAndCommitNoTrailers(t *testing.T) {
	// Covers the len(opts.Trailers)==0 branch — no trailer block
	// appended.
	repo := testutil.NewTempRepo(t)
	scope, err := git.EnterBranch(context.Background(), repo, "", nil)
	if err != nil {
		t.Fatalf("EnterBranch: %v", err)
	}
	defer scope.Cleanup()

	testutil.WriteFile(t, scope.RepoDir, "untrailed.md", "x\n")
	if _, err := git.StageAndCommit(context.Background(), scope, "untrailed.md", git.CommitOpts{
		Message: "Plain commit",
		Author:  git.Author{Name: "Tester", Email: "tester@example.com"},
	}); err != nil {
		t.Fatalf("StageAndCommit: %v", err)
	}

	cmd := exec.Command("git", "log", "-1", "--format=%B", "HEAD")
	cmd.Dir = repo
	out, _ := cmd.CombinedOutput()
	if strings.Contains(string(out), "Trace-ID") || strings.Contains(string(out), "Actor-ID") {
		t.Errorf("expected no trailers in body:\n%s", out)
	}
}

func TestBranchWrite_StageAndCommitRollbackOnFailure(t *testing.T) {
	// A failing pre-commit hook forces `git commit` to exit non-zero,
	// exercising the UnstageFile rollback at branchwrite.go:120. After
	// the failure, `git diff --cached` must be clean — the file must
	// not remain staged for a subsequent attempt.
	repo := testutil.NewTempRepo(t)
	// Pin core.hooksPath to this repo's .git/hooks so an ambient
	// global / system core.hooksPath setting on the test host cannot
	// bypass the failing hook installed below.
	hooksDir := filepath.Join(repo, ".git", "hooks")
	cfg := exec.Command("git", "config", "core.hooksPath", hooksDir)
	cfg.Dir = repo
	if out, err := cfg.CombinedOutput(); err != nil {
		t.Fatalf("git config core.hooksPath: %v\n%s", err, out)
	}
	hookPath := filepath.Join(hooksDir, "pre-commit")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("install pre-commit hook: %v", err)
	}

	scope, err := git.EnterBranch(context.Background(), repo, "", nil)
	if err != nil {
		t.Fatalf("EnterBranch: %v", err)
	}
	defer scope.Cleanup()

	testutil.WriteFile(t, scope.RepoDir, "rollback.md", "rb\n")

	_, err = git.StageAndCommit(context.Background(), scope, "rollback.md", git.CommitOpts{
		Message: "Will fail",
		Author:  git.Author{Name: "Tester", Email: "tester@example.com"},
	})
	if err == nil {
		t.Fatal("expected StageAndCommit to fail when pre-commit hook fails")
	}

	if got := stagedFiles(t, scope.RepoDir); got != "" {
		t.Errorf("expected clean index after rollback, got staged: %q", got)
	}

	if _, err := os.Stat(filepath.Join(scope.RepoDir, "rollback.md")); err != nil {
		t.Errorf("file should remain in working tree, got %v", err)
	}
}

func TestBranchWrite_UnstageFile(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	testutil.WriteFile(t, repo, "u.md", "u\n")

	cmd := exec.Command("git", "add", "u.md")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}

	if err := git.UnstageFile(context.Background(), repo, "u.md"); err != nil {
		t.Fatalf("UnstageFile: %v", err)
	}

	if got := stagedFiles(t, repo); got != "" {
		t.Errorf("index should be clean after UnstageFile, got %q", got)
	}
}

func TestBranchWrite_CurrentBranch(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	got, err := git.CurrentBranch(context.Background(), repo)
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}
	if got != "main" {
		t.Errorf("CurrentBranch: got %q, want main", got)
	}
}
