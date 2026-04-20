//go:build scenario

package scenarios_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/artifact"
	"github.com/bszymi/spine/internal/branchprotect"
	"github.com/bszymi/spine/internal/event"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/projection"
	"github.com/bszymi/spine/internal/queue"
	"github.com/bszymi/spine/internal/store"
	"github.com/bszymi/spine/internal/workspace"
)

// TestWorkspace_FreshProvisioning verifies that a freshly provisioned workspace
// (new Git repo with Spine structure) is immediately usable for artifact creation
// and projection sync.
//
// Scenario: Freshly provisioned workspace is immediately usable
//   Given a new Git repo provisioned from scratch via RepoProvisioner
//   When an artifact is created in the provisioned workspace
//     And projections are synced
//   Then the artifact projection should exist in the workspace DB
func TestWorkspace_FreshProvisioning(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")

	ctx := context.Background()

	// Use registry test DB as the workspace's database.
	wsDBURL := os.Getenv("SPINE_REGISTRY_TEST_DATABASE_URL")
	if wsDBURL == "" {
		t.Skip("SPINE_REGISTRY_TEST_DATABASE_URL not set")
	}

	// --- Step 1: Provision Git repo (fresh mode) ---
	reposDir := t.TempDir()
	provisioner := workspace.NewRepoProvisioner(reposDir)

	t.Run("provision-fresh-repo", func(t *testing.T) {
		repoPath, err := provisioner.ProvisionRepo(ctx, "ws-fresh", "")
		if err != nil {
			t.Fatalf("provision repo: %v", err)
		}

		// Verify directory was created.
		expectedPath := filepath.Join(reposDir, "ws-fresh")
		if repoPath != expectedPath {
			t.Errorf("expected path %q, got %q", expectedPath, repoPath)
		}

		// Verify it's a git repo.
		if _, err := os.Stat(filepath.Join(repoPath, ".git")); err != nil {
			t.Errorf("expected .git directory: %v", err)
		}

		// Verify Spine structure was created.
		if !workspace.IsSpineRepo(repoPath) {
			t.Error("expected Spine repo indicators (.spine.yaml, governance/, or workflows/)")
		}
	})

	// --- Step 2: Connect services to the provisioned workspace ---
	wsStore, err := store.NewPostgresStore(ctx, wsDBURL)
	if err != nil {
		t.Fatalf("connect to workspace DB: %v", err)
	}
	t.Cleanup(func() { wsStore.Close() })
	if err := wsStore.ApplyMigrations(ctx, store.FindMigrationsDir()); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	wsStore.CleanupTestData(ctx, t)

	repoPath := filepath.Join(reposDir, "ws-fresh")
	gitClient := git.NewCLIClient(repoPath)
	q := queue.NewMemoryQueue(100)
	go q.Start(ctx)
	t.Cleanup(func() { q.Stop() })
	events := event.NewQueueRouter(q)

	artifactSvc := artifact.NewService(gitClient, events, repoPath)
	artifactSvc.WithPolicy(branchprotect.NewPermissive())
	projSvc := projection.NewService(gitClient, wsStore, events, 30*time.Second)

	// --- Step 3: Create artifact in provisioned workspace ---
	t.Run("create-artifact-in-provisioned-workspace", func(t *testing.T) {
		content := `---
type: Governance
title: Provisioning Test Document
status: Living Document
version: "0.1"
---

# Provisioning Test

This artifact was created after workspace provisioning.
`
		_, err := artifactSvc.Create(ctx, "governance/provisioning-test.md", content)
		if err != nil {
			t.Fatalf("create artifact: %v", err)
		}
	})

	// --- Step 4: Sync projections and verify ---
	t.Run("sync-and-verify-projections", func(t *testing.T) {
		if err := projSvc.FullRebuild(ctx); err != nil {
			t.Fatalf("full rebuild: %v", err)
		}

		result, err := wsStore.QueryArtifacts(ctx, store.ArtifactQuery{})
		if err != nil {
			t.Fatalf("query artifacts: %v", err)
		}

		found := false
		for _, a := range result.Items {
			if a.ArtifactPath == "governance/provisioning-test.md" {
				found = true
				break
			}
		}
		if !found {
			t.Error("artifact governance/provisioning-test.md not found in projections after sync")
		}
	})
}

// TestWorkspace_CloneProvisioning verifies that cloning an existing Spine repo
// and syncing projections produces a usable workspace with existing artifacts.
//
// Scenario: Cloning an existing Spine repo produces a usable workspace
//   Given a remote Spine repo with a "Remote Charter" governance artifact
//   When the remote repo is cloned into a new workspace via RepoProvisioner
//     And projections are synced
//   Then the "Remote Charter" artifact should appear in the workspace projections
func TestWorkspace_CloneProvisioning(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")

	ctx := context.Background()

	wsDBURL := os.Getenv("SPINE_REGISTRY_TEST_DATABASE_URL")
	if wsDBURL == "" {
		t.Skip("SPINE_REGISTRY_TEST_DATABASE_URL not set")
	}

	// --- Step 1: Create a "remote" repo with Spine content ---
	remoteDir := t.TempDir()

	t.Run("setup-remote-repo", func(t *testing.T) {
		// Use raw git commands to set up the remote.
		gitEnv := append(os.Environ(),
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@spine.local",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@spine.local",
		)
		runGit := func(args ...string) {
			t.Helper()
			cmd := exec.Command("git", args...)
			cmd.Dir = remoteDir
			cmd.Env = gitEnv
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}

		runGit("init", "-b", "main")
		runGit("config", "user.email", "test@spine.local")
		runGit("config", "user.name", "Test")

		// Create .spine.yaml and a governance artifact.
		if err := os.WriteFile(filepath.Join(remoteDir, ".spine.yaml"), []byte("artifacts_dir: /\n"), 0o644); err != nil {
			t.Fatalf("write .spine.yaml: %v", err)
		}
		govDir := filepath.Join(remoteDir, "governance")
		if err := os.MkdirAll(govDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		charter := `---
type: Governance
title: Remote Charter
status: Living Document
version: "0.1"
---

# Remote Charter

This exists in the remote repo before cloning.
`
		if err := os.WriteFile(filepath.Join(govDir, "charter.md"), []byte(charter), 0o644); err != nil {
			t.Fatalf("write charter: %v", err)
		}

		runGit("add", ".")
		runGit("commit", "-m", "Seed Spine repo")
	})

	// --- Step 2: Clone the remote into a workspace ---
	reposDir := t.TempDir()
	provisioner := workspace.NewRepoProvisioner(reposDir)

	t.Run("clone-existing-spine-repo", func(t *testing.T) {
		repoPath, err := provisioner.ProvisionRepo(ctx, "ws-cloned", remoteDir)
		if err != nil {
			t.Fatalf("provision via clone: %v", err)
		}

		if !workspace.IsSpineRepo(repoPath) {
			t.Error("cloned repo should be detected as Spine repo")
		}
	})

	// --- Step 3: Connect services and sync projections ---
	wsStore, err := store.NewPostgresStore(ctx, wsDBURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { wsStore.Close() })
	if err := wsStore.ApplyMigrations(ctx, store.FindMigrationsDir()); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	wsStore.CleanupTestData(ctx, t)

	clonedPath := filepath.Join(reposDir, "ws-cloned")
	gitClient := git.NewCLIClient(clonedPath)
	q := queue.NewMemoryQueue(100)
	go q.Start(ctx)
	t.Cleanup(func() { q.Stop() })
	events := event.NewQueueRouter(q)
	projSvc := projection.NewService(gitClient, wsStore, events, 30*time.Second)

	t.Run("sync-cloned-projections", func(t *testing.T) {
		if err := projSvc.FullRebuild(ctx); err != nil {
			t.Fatalf("full rebuild: %v", err)
		}

		result, err := wsStore.QueryArtifacts(ctx, store.ArtifactQuery{})
		if err != nil {
			t.Fatalf("query: %v", err)
		}

		found := false
		for _, a := range result.Items {
			if a.ArtifactPath == "governance/charter.md" && a.Title == "Remote Charter" {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected Remote Charter artifact from cloned repo to appear in projections")
		}
	})
}
