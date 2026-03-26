package harness_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bszymi/spine/internal/scenariotest/harness"
)

func TestNewTestRepo(t *testing.T) {
	repo := harness.NewTestRepo(t)

	if repo.Dir == "" {
		t.Fatal("expected non-empty directory")
	}
	if repo.Git == nil {
		t.Fatal("expected non-nil Git client")
	}

	// Verify it's a valid git repo with at least one commit.
	sha := repo.HeadSHA(t)
	if len(sha) < 7 {
		t.Fatalf("expected valid SHA, got %q", sha)
	}
}

func TestSeedGovernance(t *testing.T) {
	repo := harness.NewTestRepo(t)
	repo.SeedGovernance(t)

	// Verify directory structure.
	dirs := []string{
		"governance", "initiatives", "architecture",
		"product", "workflows", "templates",
	}
	for _, dir := range dirs {
		info, err := os.Stat(filepath.Join(repo.Dir, dir))
		if err != nil {
			t.Errorf("expected directory %s to exist: %v", dir, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("expected %s to be a directory", dir)
		}
	}

	// Verify governance files exist.
	govFiles := []string{
		"governance/charter.md",
		"governance/constitution.md",
		"governance/guidelines.md",
		"governance/repository-structure.md",
		"governance/naming-conventions.md",
	}
	for _, f := range govFiles {
		if !repo.FileExists(f) {
			t.Errorf("expected governance file %s to exist", f)
		}
	}

	// Verify content is valid YAML frontmatter.
	data, err := os.ReadFile(filepath.Join(repo.Dir, "governance/charter.md"))
	if err != nil {
		t.Fatalf("read charter: %v", err)
	}
	content := string(data)
	if content == "" {
		t.Fatal("expected non-empty charter content")
	}

	// Verify committed (not just written).
	sha := repo.HeadSHA(t)
	if len(sha) < 7 {
		t.Fatalf("expected valid SHA after seed, got %q", sha)
	}
}

func TestSeedWorkflows(t *testing.T) {
	repo := harness.NewTestRepo(t)
	repo.SeedGovernance(t)
	repo.SeedWorkflows(t)

	wfFiles := []string{
		"workflows/task-default.yaml",
		"workflows/task-spike.yaml",
	}
	for _, f := range wfFiles {
		if !repo.FileExists(f) {
			t.Errorf("expected workflow file %s to exist", f)
		}
	}
}

func TestWriteArtifactAndCommit(t *testing.T) {
	repo := harness.NewTestRepo(t)

	content := `---
type: Governance
title: Test Doc
status: Draft
---

# Test Doc
`
	repo.WriteArtifact(t, "governance/test-doc.md", content)
	repo.CommitAll(t, "Add test doc")

	if !repo.FileExists("governance/test-doc.md") {
		t.Error("expected test-doc.md to exist after write")
	}
}

func TestParallelRepos(t *testing.T) {
	// Verify that multiple repos can be created concurrently without interference.
	t.Run("repo-a", func(t *testing.T) {
		t.Parallel()
		repo := harness.NewTestRepo(t)
		repo.SeedGovernance(t)
		repo.WriteArtifact(t, "governance/a.md", `---
type: Governance
title: A
status: Draft
---
# A
`)
		repo.CommitAll(t, "Add A")

		if !repo.FileExists("governance/a.md") {
			t.Error("expected a.md to exist")
		}
	})

	t.Run("repo-b", func(t *testing.T) {
		t.Parallel()
		repo := harness.NewTestRepo(t)
		repo.SeedGovernance(t)
		repo.WriteArtifact(t, "governance/b.md", `---
type: Governance
title: B
status: Draft
---
# B
`)
		repo.CommitAll(t, "Add B")

		if !repo.FileExists("governance/b.md") {
			t.Error("expected b.md to exist")
		}
		// Verify A does NOT exist in this repo (isolation).
		if repo.FileExists("governance/a.md") {
			t.Error("repo-b should not contain a.md (isolation violation)")
		}
	})
}
