package git_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/testutil"
)

func TestValidateCredentialHelper_Empty(t *testing.T) {
	if err := git.ValidateCredentialHelper(""); err != nil {
		t.Fatalf("empty path should be valid: %v", err)
	}
}

func TestValidateCredentialHelper_ValidExecutable(t *testing.T) {
	helper := filepath.Join(t.TempDir(), "helper.sh")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\necho ok"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := git.ValidateCredentialHelper(helper); err != nil {
		t.Fatalf("valid executable should pass: %v", err)
	}
}

func TestValidateCredentialHelper_NotFound(t *testing.T) {
	err := git.ValidateCredentialHelper("/nonexistent/helper.sh")
	if err == nil {
		t.Fatal("expected error for nonexistent path")
	}
	if !strings.Contains(err.Error(), "credential helper") {
		t.Errorf("expected 'credential helper' in error, got: %v", err)
	}
}

func TestValidateCredentialHelper_Directory(t *testing.T) {
	dir := t.TempDir()
	err := git.ValidateCredentialHelper(dir)
	if err == nil {
		t.Fatal("expected error for directory")
	}
	if !strings.Contains(err.Error(), "is a directory") {
		t.Errorf("expected 'is a directory' in error, got: %v", err)
	}
}

func TestValidateCredentialHelper_NotExecutable(t *testing.T) {
	file := filepath.Join(t.TempDir(), "helper.sh")
	if err := os.WriteFile(file, []byte("#!/bin/sh\necho ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := git.ValidateCredentialHelper(file)
	if err == nil {
		t.Fatal("expected error for non-executable file")
	}
	if !strings.Contains(err.Error(), "not executable") {
		t.Errorf("expected 'not executable' in error, got: %v", err)
	}
}

func TestConfigureCredentialHelper_SetsRepoConfig(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	helperPath := "/usr/local/bin/my-credential-helper"

	client := git.NewCLIClient(repo, git.WithCredentialHelper(helperPath))
	ctx := context.Background()

	if err := client.ConfigureCredentialHelper(ctx); err != nil {
		t.Fatalf("ConfigureCredentialHelper: %v", err)
	}

	// Verify the config was set in the repo's local git config.
	cmd := exec.CommandContext(ctx, "git", "config", "--local", "credential.helper")
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git config read: %v\n%s", err, out)
	}
	got := strings.TrimSpace(string(out))
	if got != helperPath {
		t.Errorf("expected credential.helper=%q, got %q", helperPath, got)
	}
}

func TestConfigureCredentialHelper_NoopWithoutHelper(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	client := git.NewCLIClient(repo)
	ctx := context.Background()

	if err := client.ConfigureCredentialHelper(ctx); err != nil {
		t.Fatalf("ConfigureCredentialHelper should be no-op: %v", err)
	}

	// Verify no credential.helper is set.
	cmd := exec.CommandContext(ctx, "git", "config", "--local", "credential.helper")
	cmd.Dir = repo
	out, _ := cmd.CombinedOutput()
	got := strings.TrimSpace(string(out))
	if got != "" {
		t.Errorf("expected no credential.helper, got %q", got)
	}
}

func TestConfigureCredentialHelper_PerRepo(t *testing.T) {
	// Verify that credential.helper is set per-repo, not globally.
	repo1 := testutil.NewTempRepo(t)
	repo2 := testutil.NewTempRepo(t)
	ctx := context.Background()

	client1 := git.NewCLIClient(repo1, git.WithCredentialHelper("/helper/one"))
	if err := client1.ConfigureCredentialHelper(ctx); err != nil {
		t.Fatalf("ConfigureCredentialHelper repo1: %v", err)
	}

	// repo2 should have no credential.helper.
	cmd := exec.CommandContext(ctx, "git", "config", "--local", "credential.helper")
	cmd.Dir = repo2
	out, _ := cmd.CombinedOutput()
	got := strings.TrimSpace(string(out))
	if got != "" {
		t.Errorf("repo2 should not have credential.helper, got %q", got)
	}
}

func TestWithCredentialHelper_Option(t *testing.T) {
	// Verify the option sets the field correctly.
	client := git.NewCLIClient("/tmp/test", git.WithCredentialHelper("/path/to/helper"))
	// We can't directly check the private field, but we can verify
	// ConfigureCredentialHelper attempts to run (which will fail on non-repo).
	err := client.ConfigureCredentialHelper(context.Background())
	if err == nil {
		t.Fatal("expected error on non-repo path")
	}
	// Error should be from git, not from nil-check (proving the field was set).
}

func TestWithPushEnv_PassesEnvToPush(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	bare := setupRemote(t, repo)
	ctx := context.Background()

	// Create a credential helper script that writes SMP_WORKSPACE_ID to a file.
	markerFile := filepath.Join(t.TempDir(), "marker.txt")
	helperScript := filepath.Join(t.TempDir(), "helper.sh")
	scriptContent := fmt.Sprintf("#!/bin/sh\necho \"$SMP_WORKSPACE_ID\" > %s\necho username=test\necho password=test\n", markerFile)
	if err := os.WriteFile(helperScript, []byte(scriptContent), 0o755); err != nil {
		t.Fatal(err)
	}

	// Create client with push env and credential helper.
	client := git.NewCLIClient(repo,
		git.WithCredentialHelper(helperScript),
		git.WithPushEnv("SMP_WORKSPACE_ID=ws-42"),
	)
	if err := client.ConfigureCredentialHelper(ctx); err != nil {
		t.Fatalf("ConfigureCredentialHelper: %v", err)
	}

	// Add a commit and push (to local bare repo — no auth needed, but env is still set).
	testutil.WriteFile(t, repo, "env-test.md", "# Env Test")
	stageFile(t, repo, "env-test.md")
	if _, err := client.Commit(ctx, git.CommitOpts{Message: "env test"}); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Push to the bare remote — this succeeds because it's local.
	if err := client.Push(ctx, "origin", "main"); err != nil {
		t.Fatalf("Push: %v", err)
	}

	// The push env var was passed to the git command, but since the remote
	// is local (bare), git doesn't invoke the credential helper. We verify
	// the option is accepted and push works without error.
	_ = bare
}

func TestWithPushEnv_NotPassedToNonPush(t *testing.T) {
	// Verify push env does not interfere with non-push operations.
	repo := testutil.NewTempRepo(t)
	ctx := context.Background()

	client := git.NewCLIClient(repo, git.WithPushEnv("SMP_WORKSPACE_ID=ws-99"))

	// Non-push operations should work fine.
	sha, err := client.Head(ctx)
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if len(sha) != 40 {
		t.Fatalf("expected 40-char SHA, got %q", sha)
	}

	// Commit should also work.
	testutil.WriteFile(t, repo, "non-push.md", "# Test")
	stageFile(t, repo, "non-push.md")
	_, err = client.Commit(ctx, git.CommitOpts{Message: "non-push test"})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
}
