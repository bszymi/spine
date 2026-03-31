package git_test

import (
	"context"
	"os/exec"
	"testing"

	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/testutil"
)

func newTestClient(t *testing.T) (*git.CLIClient, string) {
	t.Helper()
	repo := testutil.NewTempRepo(t)
	return git.NewCLIClient(repo), repo
}

func TestHead(t *testing.T) {
	client, _ := newTestClient(t)
	ctx := context.Background()

	sha, err := client.Head(ctx)
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if len(sha) != 40 {
		t.Fatalf("expected 40-char SHA, got %q", sha)
	}
}

func TestCommitWithTrailers(t *testing.T) {
	client, repo := newTestClient(t)
	ctx := context.Background()

	testutil.WriteFile(t, repo, "test.md", "# Test")
	stageFile(t, repo, "test.md")

	result, err := client.Commit(ctx, git.CommitOpts{
		Message: "Add test artifact",
		Trailers: map[string]string{
			"Trace-ID":  "trace-123",
			"Actor-ID":  "actor-456",
			"Run-ID":    "none",
			"Operation": "artifact.create",
		},
		Author: git.Author{Name: "Test Actor", Email: "test@spine.local"},
	})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if len(result.SHA) != 40 {
		t.Fatalf("expected 40-char SHA, got %q", result.SHA)
	}

	// Verify trailers are in the commit message
	commits, err := client.Log(ctx, git.LogOpts{Limit: 1})
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(commits) == 0 {
		t.Fatal("expected at least one commit")
	}

	c := commits[0]
	if c.Trailers["Trace-ID"] != "trace-123" {
		t.Errorf("expected Trace-ID=trace-123, got %q", c.Trailers["Trace-ID"])
	}
	if c.Trailers["Actor-ID"] != "actor-456" {
		t.Errorf("expected Actor-ID=actor-456, got %q", c.Trailers["Actor-ID"])
	}
	if c.Trailers["Operation"] != "artifact.create" {
		t.Errorf("expected Operation=artifact.create, got %q", c.Trailers["Operation"])
	}
}

func TestCreateBranchAndMergeFastForward(t *testing.T) {
	client, repo := newTestClient(t)
	ctx := context.Background()

	// Create a feature branch
	head, _ := client.Head(ctx)
	if err := client.CreateBranch(ctx, "feature", head); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}

	// Checkout feature, add a commit
	checkout(t, repo, "feature")
	testutil.WriteFile(t, repo, "feature.md", "# Feature")
	stageFile(t, repo, "feature.md")
	if _, err := client.Commit(ctx, git.CommitOpts{Message: "Add feature"}); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Checkout main, merge feature with fast-forward
	checkout(t, repo, "main")
	result, err := client.Merge(ctx, git.MergeOpts{
		Source:   "feature",
		Strategy: "fast-forward",
	})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if !result.FastForward {
		t.Error("expected fast-forward merge")
	}

	// Verify file exists on main
	content, err := client.ReadFile(ctx, "HEAD", "feature.md")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(content) != "# Feature" {
		t.Errorf("unexpected content: %s", content)
	}
}

func TestCreateBranchAndMergeCommit(t *testing.T) {
	client, repo := newTestClient(t)
	ctx := context.Background()

	head, _ := client.Head(ctx)
	if err := client.CreateBranch(ctx, "feature", head); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}

	// Add a commit on main so fast-forward is not possible
	testutil.WriteFile(t, repo, "main-change.md", "# Main")
	stageFile(t, repo, "main-change.md")
	if _, err := client.Commit(ctx, git.CommitOpts{Message: "Main change"}); err != nil {
		t.Fatalf("Commit on main: %v", err)
	}

	// Add a commit on feature
	checkout(t, repo, "feature")
	testutil.WriteFile(t, repo, "feature.md", "# Feature")
	stageFile(t, repo, "feature.md")
	if _, err := client.Commit(ctx, git.CommitOpts{Message: "Feature change"}); err != nil {
		t.Fatalf("Commit on feature: %v", err)
	}

	// Merge back to main with merge-commit
	checkout(t, repo, "main")
	result, err := client.Merge(ctx, git.MergeOpts{
		Source:   "feature",
		Strategy: "merge-commit",
		Message:  "Merge feature",
		Trailers: map[string]string{"Trace-ID": "merge-trace"},
	})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if result.FastForward {
		t.Error("expected non-fast-forward merge")
	}
}

func TestDeleteBranch(t *testing.T) {
	client, _ := newTestClient(t)
	ctx := context.Background()

	head, _ := client.Head(ctx)
	if err := client.CreateBranch(ctx, "temp", head); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if err := client.DeleteBranch(ctx, "temp"); err != nil {
		t.Fatalf("DeleteBranch: %v", err)
	}
}

func TestDiff(t *testing.T) {
	client, repo := newTestClient(t)
	ctx := context.Background()

	before, _ := client.Head(ctx)

	testutil.WriteFile(t, repo, "new.md", "# New")
	stageFile(t, repo, "new.md")
	if _, err := client.Commit(ctx, git.CommitOpts{Message: "Add new"}); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	after, _ := client.Head(ctx)

	diffs, err := client.Diff(ctx, before, after)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}

	if len(diffs) != 1 {
		t.Fatalf("expected 1 diff, got %d", len(diffs))
	}
	if diffs[0].Path != "new.md" || diffs[0].Status != "added" {
		t.Errorf("unexpected diff: %+v", diffs[0])
	}
}

func TestReadFile(t *testing.T) {
	client, repo := newTestClient(t)
	ctx := context.Background()

	testutil.WriteFile(t, repo, "governance/test.md", "---\ntype: Governance\n---\n# Test")
	stageFile(t, repo, "governance/test.md")
	if _, err := client.Commit(ctx, git.CommitOpts{Message: "Add test"}); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	content, err := client.ReadFile(ctx, "HEAD", "governance/test.md")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(content) != "---\ntype: Governance\n---\n# Test" {
		t.Errorf("unexpected content: %s", content)
	}
}

func TestReadFileNotFound(t *testing.T) {
	client, _ := newTestClient(t)
	ctx := context.Background()

	_, err := client.ReadFile(ctx, "HEAD", "nonexistent.md")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}

	gitErr, ok := err.(*git.GitError)
	if !ok {
		t.Fatalf("expected GitError, got %T", err)
	}
	if gitErr.Kind != git.ErrKindNotFound {
		t.Errorf("expected not_found, got %s", gitErr.Kind)
	}
}

func TestListFiles(t *testing.T) {
	client, repo := newTestClient(t)
	ctx := context.Background()

	testutil.WriteFile(t, repo, "governance/a.md", "# A")
	testutil.WriteFile(t, repo, "governance/b.md", "# B")
	testutil.WriteFile(t, repo, "architecture/c.md", "# C")
	stageFile(t, repo, ".")
	if _, err := client.Commit(ctx, git.CommitOpts{Message: "Add files"}); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// List all .md files
	files, err := client.ListFiles(ctx, "HEAD", "*.md")
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) != 3 {
		t.Errorf("expected 3 files, got %d: %v", len(files), files)
	}

	// List governance/ files
	files, err = client.ListFiles(ctx, "HEAD", "governance/")
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) != 2 {
		t.Errorf("expected 2 files, got %d: %v", len(files), files)
	}
}

func TestLog(t *testing.T) {
	client, repo := newTestClient(t)
	ctx := context.Background()

	testutil.WriteFile(t, repo, "a.md", "# A")
	stageFile(t, repo, "a.md")
	if _, err := client.Commit(ctx, git.CommitOpts{Message: "First"}); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	testutil.WriteFile(t, repo, "b.md", "# B")
	stageFile(t, repo, "b.md")
	if _, err := client.Commit(ctx, git.CommitOpts{Message: "Second"}); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	commits, err := client.Log(ctx, git.LogOpts{Limit: 2})
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(commits) != 2 {
		t.Fatalf("expected 2 commits, got %d", len(commits))
	}
	if commits[0].Message != "Second" {
		t.Errorf("expected 'Second', got %q", commits[0].Message)
	}
}

func TestHasCommitWithTrailer(t *testing.T) {
	client, repo := newTestClient(t)
	ctx := context.Background()

	testutil.WriteFile(t, repo, "a.md", "# A")
	stageFile(t, repo, "a.md")
	if _, err := client.Commit(ctx, git.CommitOpts{
		Message:  "Test commit",
		Trailers: map[string]string{"Trace-ID": "unique-trace-abc"},
	}); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Should find the commit
	sha, found, err := client.HasCommitWithTrailer(ctx, "Trace-ID", "unique-trace-abc")
	if err != nil {
		t.Fatalf("HasCommitWithTrailer: %v", err)
	}
	if !found {
		t.Error("expected to find commit with trailer")
	}
	if len(sha) != 40 {
		t.Errorf("expected 40-char SHA, got %q", sha)
	}

	// Should not find a nonexistent trailer
	_, found, err = client.HasCommitWithTrailer(ctx, "Trace-ID", "nonexistent")
	if err != nil {
		t.Fatalf("HasCommitWithTrailer: %v", err)
	}
	if found {
		t.Error("should not find nonexistent trailer")
	}
}

func setupRemote(t *testing.T, repoDir string) string {
	t.Helper()
	bare := t.TempDir()
	cmd := exec.CommandContext(context.Background(), "git", "init", "--bare")
	cmd.Dir = bare
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	cmd = exec.CommandContext(context.Background(), "git", "remote", "add", "origin", bare)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
	return bare
}

func TestPush(t *testing.T) {
	client, repo := newTestClient(t)
	ctx := context.Background()
	setupRemote(t, repo)

	// Add a commit so there's something to push
	testutil.WriteFile(t, repo, "push-test.md", "# Push Test")
	stageFile(t, repo, "push-test.md")
	if _, err := client.Commit(ctx, git.CommitOpts{Message: "for push"}); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	if err := client.Push(ctx, "origin", "main"); err != nil {
		t.Fatalf("Push: %v", err)
	}
}

func TestPushBranch(t *testing.T) {
	client, repo := newTestClient(t)
	ctx := context.Background()
	setupRemote(t, repo)

	// Push main first so remote has it
	if err := client.Push(ctx, "origin", "main"); err != nil {
		t.Fatalf("initial push: %v", err)
	}

	// Create and checkout a feature branch
	head, _ := client.Head(ctx)
	if err := client.CreateBranch(ctx, "feature-push", head); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	checkout(t, repo, "feature-push")

	testutil.WriteFile(t, repo, "branch-push.md", "# Branch Push")
	stageFile(t, repo, "branch-push.md")
	if _, err := client.Commit(ctx, git.CommitOpts{Message: "branch commit"}); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	if err := client.PushBranch(ctx, "origin", "feature-push"); err != nil {
		t.Fatalf("PushBranch: %v", err)
	}
}

func TestDeleteRemoteBranch(t *testing.T) {
	client, repo := newTestClient(t)
	ctx := context.Background()
	setupRemote(t, repo)

	// Push main and a feature branch
	if err := client.Push(ctx, "origin", "main"); err != nil {
		t.Fatalf("push main: %v", err)
	}

	head, _ := client.Head(ctx)
	if err := client.CreateBranch(ctx, "to-delete", head); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if err := client.Push(ctx, "origin", "to-delete"); err != nil {
		t.Fatalf("push branch: %v", err)
	}

	// Delete the remote branch
	if err := client.DeleteRemoteBranch(ctx, "origin", "to-delete"); err != nil {
		t.Fatalf("DeleteRemoteBranch: %v", err)
	}
}

func TestPushToInvalidRemote(t *testing.T) {
	client, _ := newTestClient(t)
	ctx := context.Background()

	// No remote configured — should fail with classified error
	err := client.Push(ctx, "origin", "main")
	if err == nil {
		t.Fatal("expected error pushing to unconfigured remote")
	}
	gitErr, ok := err.(*git.GitError)
	if !ok {
		t.Fatalf("expected GitError, got %T", err)
	}
	if gitErr.Op != "push" {
		t.Errorf("expected op=push, got %s", gitErr.Op)
	}
}

func TestErrorClassification(t *testing.T) {
	client := git.NewCLIClient("/nonexistent/repo")
	ctx := context.Background()

	_, err := client.Head(ctx)
	if err == nil {
		t.Fatal("expected error for nonexistent repo")
	}

	gitErr, ok := err.(*git.GitError)
	if !ok {
		t.Fatalf("expected GitError, got %T", err)
	}
	if gitErr.IsRetryable() {
		t.Error("error on nonexistent repo should not be retryable")
	}
}

// helpers

func stageFile(t *testing.T, repoDir, path string) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", "add", path)
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git add %s: %v\n%s", path, err, out)
	}
}

func checkout(t *testing.T, repoDir, branch string) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", "checkout", branch)
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git checkout %s: %v\n%s", branch, err, out)
	}
}
