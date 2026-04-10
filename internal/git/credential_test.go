package git_test

import (
	"context"
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
