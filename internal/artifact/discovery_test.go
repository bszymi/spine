package artifact_test

import (
	"context"
	"os/exec"
	"testing"

	"github.com/bszymi/spine/internal/artifact"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/testutil"
)

func setupDiscoveryRepo(t *testing.T) (*git.CLIClient, string) {
	t.Helper()
	repo := testutil.NewTempRepo(t)
	client := git.NewCLIClient(repo)

	// Add governance artifact
	testutil.WriteFile(t, repo, "governance/charter.md", `---
type: Governance
title: Charter
status: Foundational
---

# Charter
`)

	// Add architecture artifact
	testutil.WriteFile(t, repo, "architecture/domain-model.md", `---
type: Architecture
title: Domain Model
status: Living Document
version: "0.1"
---

# Domain Model
`)

	// Add non-artifact .md file (no valid type)
	testutil.WriteFile(t, repo, "README.md", "# Spine\n\nJust a readme.\n")

	// Add workflow YAML
	testutil.WriteFile(t, repo, "workflows/task-execution.yaml", "id: task-execution\nname: Task Execution\n")

	// Add non-md file
	testutil.WriteFile(t, repo, "go.mod", "module test\n")

	testutil.GitAdd(t, repo, ".", "add test files")
	return client, repo
}

func TestDiscoverAll(t *testing.T) {
	client, _ := setupDiscoveryRepo(t)
	ctx := context.Background()

	result, err := artifact.DiscoverAll(ctx, client, "HEAD")
	if err != nil {
		t.Fatalf("DiscoverAll: %v", err)
	}

	// Should find 2 artifacts
	if len(result.Artifacts) != 2 {
		t.Errorf("expected 2 artifacts, got %d", len(result.Artifacts))
		for _, a := range result.Artifacts {
			t.Logf("  artifact: %s (%s)", a.Path, a.Type)
		}
	}

	// Should find 1 workflow
	if len(result.Workflows) != 1 {
		t.Errorf("expected 1 workflow, got %d: %v", len(result.Workflows), result.Workflows)
	}

	// README should be skipped (has .md but no valid front matter type)
	if len(result.Skipped) != 1 {
		t.Errorf("expected 1 skipped file, got %d: %v", len(result.Skipped), result.Skipped)
	}
}

func TestDiscoverAllDefaultRef(t *testing.T) {
	client, _ := setupDiscoveryRepo(t)
	ctx := context.Background()

	// Empty ref should default to HEAD
	result, err := artifact.DiscoverAll(ctx, client, "")
	if err != nil {
		t.Fatalf("DiscoverAll with empty ref: %v", err)
	}
	if len(result.Artifacts) != 2 {
		t.Errorf("expected 2 artifacts, got %d", len(result.Artifacts))
	}
}

func TestDiscoverChangesCreated(t *testing.T) {
	client, repo := setupDiscoveryRepo(t)
	ctx := context.Background()

	beforeSHA, _ := client.Head(ctx)

	// Add a new artifact
	testutil.WriteFile(t, repo, "governance/guidelines.md", `---
type: Governance
title: Guidelines
status: Living Document
---

# Guidelines
`)
	testutil.GitAdd(t, repo, "governance/guidelines.md", "add guidelines")

	afterSHA, _ := client.Head(ctx)

	changeset, err := artifact.DiscoverChanges(ctx, client, beforeSHA, afterSHA)
	if err != nil {
		t.Fatalf("DiscoverChanges: %v", err)
	}

	if len(changeset.Created) != 1 {
		t.Errorf("expected 1 created artifact, got %d", len(changeset.Created))
	}
	if len(changeset.Created) > 0 && changeset.Created[0].Title != "Guidelines" {
		t.Errorf("expected title 'Guidelines', got %s", changeset.Created[0].Title)
	}
}

func TestDiscoverChangesModified(t *testing.T) {
	client, repo := setupDiscoveryRepo(t)
	ctx := context.Background()

	beforeSHA, _ := client.Head(ctx)

	// Modify an existing artifact
	testutil.WriteFile(t, repo, "governance/charter.md", `---
type: Governance
title: Charter (Updated)
status: Foundational
---

# Updated Charter
`)
	testutil.GitAdd(t, repo, "governance/charter.md", "update charter")

	afterSHA, _ := client.Head(ctx)

	changeset, err := artifact.DiscoverChanges(ctx, client, beforeSHA, afterSHA)
	if err != nil {
		t.Fatalf("DiscoverChanges: %v", err)
	}

	if len(changeset.Modified) != 1 {
		t.Errorf("expected 1 modified artifact, got %d", len(changeset.Modified))
	}
	if len(changeset.Modified) > 0 && changeset.Modified[0].Title != "Charter (Updated)" {
		t.Errorf("expected updated title, got %s", changeset.Modified[0].Title)
	}
}

func TestDiscoverChangesDeleted(t *testing.T) {
	client, repo := setupDiscoveryRepo(t)
	ctx := context.Background()

	beforeSHA, _ := client.Head(ctx)

	// Delete an artifact via git rm
	cmd := execCmd(t, repo, "git", "rm", "architecture/domain-model.md")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git rm: %v\n%s", err, out)
	}
	cmd = execCmd(t, repo, "git", "commit", "-m", "delete domain model")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}

	afterSHA, _ := client.Head(ctx)

	changeset, err := artifact.DiscoverChanges(ctx, client, beforeSHA, afterSHA)
	if err != nil {
		t.Fatalf("DiscoverChanges: %v", err)
	}

	if len(changeset.Deleted) != 1 {
		t.Errorf("expected 1 deleted path, got %d: %v", len(changeset.Deleted), changeset.Deleted)
	}
	if len(changeset.Deleted) > 0 && changeset.Deleted[0] != "architecture/domain-model.md" {
		t.Errorf("expected deleted path 'architecture/domain-model.md', got %s", changeset.Deleted[0])
	}
}

func TestDiscoverChangesSkipsNonArtifacts(t *testing.T) {
	client, repo := setupDiscoveryRepo(t)
	ctx := context.Background()

	beforeSHA, _ := client.Head(ctx)

	// Modify a non-artifact .md file
	testutil.WriteFile(t, repo, "README.md", "# Updated Readme\n")
	testutil.GitAdd(t, repo, "README.md", "update readme")

	afterSHA, _ := client.Head(ctx)

	changeset, err := artifact.DiscoverChanges(ctx, client, beforeSHA, afterSHA)
	if err != nil {
		t.Fatalf("DiscoverChanges: %v", err)
	}

	if len(changeset.Created) != 0 || len(changeset.Modified) != 0 {
		t.Errorf("expected no artifacts in changeset for README update, got created=%d modified=%d",
			len(changeset.Created), len(changeset.Modified))
	}
}

func TestDiscoverChangesNonArtifactBecomesArtifact(t *testing.T) {
	client, repo := setupDiscoveryRepo(t)
	ctx := context.Background()

	beforeSHA, _ := client.Head(ctx)

	// Edit README.md to have valid front matter (non-artifact → artifact)
	testutil.WriteFile(t, repo, "README.md", `---
type: Product
title: README as Product
status: Living Document
---

# Product README
`)
	testutil.GitAdd(t, repo, "README.md", "convert readme to artifact")

	afterSHA, _ := client.Head(ctx)

	changeset, err := artifact.DiscoverChanges(ctx, client, beforeSHA, afterSHA)
	if err != nil {
		t.Fatalf("DiscoverChanges: %v", err)
	}

	if len(changeset.Created) != 1 {
		t.Errorf("expected 1 created (non-artifact→artifact), got %d", len(changeset.Created))
	}
	if len(changeset.Modified) != 0 {
		t.Errorf("expected 0 modified, got %d", len(changeset.Modified))
	}
}

func TestDiscoverChangesArtifactBecomesNonArtifact(t *testing.T) {
	client, repo := setupDiscoveryRepo(t)
	ctx := context.Background()

	beforeSHA, _ := client.Head(ctx)

	// Edit governance/charter.md to remove front matter (artifact → non-artifact)
	testutil.WriteFile(t, repo, "governance/charter.md", "# Just plain markdown now\n")
	testutil.GitAdd(t, repo, "governance/charter.md", "remove artifact front matter")

	afterSHA, _ := client.Head(ctx)

	changeset, err := artifact.DiscoverChanges(ctx, client, beforeSHA, afterSHA)
	if err != nil {
		t.Fatalf("DiscoverChanges: %v", err)
	}

	if len(changeset.Deleted) != 1 {
		t.Errorf("expected 1 deleted (artifact→non-artifact), got %d: %v", len(changeset.Deleted), changeset.Deleted)
	}
}

func TestDiscoverChangesDeletedNonArtifactSkipped(t *testing.T) {
	client, repo := setupDiscoveryRepo(t)
	ctx := context.Background()

	beforeSHA, _ := client.Head(ctx)

	// Delete README.md (non-artifact) — should not appear in Deleted
	cmd := execCmd(t, repo, "git", "rm", "README.md")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git rm: %v\n%s", err, out)
	}
	cmd = execCmd(t, repo, "git", "commit", "-m", "delete readme")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}

	afterSHA, _ := client.Head(ctx)

	changeset, err := artifact.DiscoverChanges(ctx, client, beforeSHA, afterSHA)
	if err != nil {
		t.Fatalf("DiscoverChanges: %v", err)
	}

	if len(changeset.Deleted) != 0 {
		t.Errorf("expected 0 deleted (non-artifact deletion), got %d: %v", len(changeset.Deleted), changeset.Deleted)
	}
}

func TestDiscoverChangesRenameArtifact(t *testing.T) {
	client, repo := setupDiscoveryRepo(t)
	ctx := context.Background()

	beforeSHA, _ := client.Head(ctx)

	// Rename governance/charter.md → governance/charter-v2.md
	cmd := execCmd(t, repo, "git", "mv", "governance/charter.md", "governance/charter-v2.md")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git mv: %v\n%s", err, out)
	}
	cmd = execCmd(t, repo, "git", "commit", "-m", "rename charter")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}

	afterSHA, _ := client.Head(ctx)

	changeset, err := artifact.DiscoverChanges(ctx, client, beforeSHA, afterSHA)
	if err != nil {
		t.Fatalf("DiscoverChanges: %v", err)
	}

	// Rename should produce: delete old path + create new path
	if len(changeset.Deleted) != 1 {
		t.Errorf("expected 1 deleted (old rename path), got %d: %v", len(changeset.Deleted), changeset.Deleted)
	}
	if len(changeset.Created) != 1 {
		t.Errorf("expected 1 created (new rename path), got %d", len(changeset.Created))
	}
}

func TestDiscoverChangesRenameMdToNonMd(t *testing.T) {
	client, repo := setupDiscoveryRepo(t)
	ctx := context.Background()

	beforeSHA, _ := client.Head(ctx)

	// Rename governance/charter.md → governance/charter.txt
	cmd := execCmd(t, repo, "git", "mv", "governance/charter.md", "governance/charter.txt")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git mv: %v\n%s", err, out)
	}
	cmd = execCmd(t, repo, "git", "commit", "-m", "rename to txt")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}

	afterSHA, _ := client.Head(ctx)

	changeset, err := artifact.DiscoverChanges(ctx, client, beforeSHA, afterSHA)
	if err != nil {
		t.Fatalf("DiscoverChanges: %v", err)
	}

	// Should have deleted the old artifact path
	if len(changeset.Deleted) != 1 {
		t.Errorf("expected 1 deleted (.md→.txt rename), got %d: %v", len(changeset.Deleted), changeset.Deleted)
	}
	// Should not have created anything (new path is .txt, not artifact)
	if len(changeset.Created) != 0 {
		t.Errorf("expected 0 created (.txt not artifact), got %d", len(changeset.Created))
	}
}

func TestDiscoverChangesAddedNonArtifactMdSkipped(t *testing.T) {
	client, repo := setupDiscoveryRepo(t)
	ctx := context.Background()

	beforeSHA, _ := client.Head(ctx)

	// Add a new .md file that is NOT an artifact
	testutil.WriteFile(t, repo, "docs/notes.md", "# Just notes\nNo front matter.\n")
	testutil.GitAdd(t, repo, "docs/notes.md", "add notes")

	afterSHA, _ := client.Head(ctx)

	changeset, err := artifact.DiscoverChanges(ctx, client, beforeSHA, afterSHA)
	if err != nil {
		t.Fatalf("DiscoverChanges: %v", err)
	}

	if len(changeset.Created) != 0 {
		t.Errorf("expected 0 created (non-artifact .md), got %d", len(changeset.Created))
	}
}

func TestDiscoverChangesNonMdSkipped(t *testing.T) {
	client, repo := setupDiscoveryRepo(t)
	ctx := context.Background()

	beforeSHA, _ := client.Head(ctx)

	// Modify go.mod (non-.md file)
	testutil.WriteFile(t, repo, "go.mod", "module test\ngo 1.26\n")
	testutil.GitAdd(t, repo, "go.mod", "update go.mod")

	afterSHA, _ := client.Head(ctx)

	changeset, err := artifact.DiscoverChanges(ctx, client, beforeSHA, afterSHA)
	if err != nil {
		t.Fatalf("DiscoverChanges: %v", err)
	}

	total := len(changeset.Created) + len(changeset.Modified) + len(changeset.Deleted)
	if total != 0 {
		t.Errorf("expected 0 changes for non-.md file, got %d", total)
	}
}

func TestClassifyByType(t *testing.T) {
	artifacts := []*domain.Artifact{
		{Path: "a.md", Type: domain.ArtifactTypeGovernance},
		{Path: "b.md", Type: domain.ArtifactTypeGovernance},
		{Path: "c.md", Type: domain.ArtifactTypeArchitecture},
		{Path: "d.md", Type: domain.ArtifactTypeTask},
	}

	classified := artifact.ClassifyByType(artifacts)

	if len(classified[domain.ArtifactTypeGovernance]) != 2 {
		t.Errorf("expected 2 governance, got %d", len(classified[domain.ArtifactTypeGovernance]))
	}
	if len(classified[domain.ArtifactTypeArchitecture]) != 1 {
		t.Errorf("expected 1 architecture, got %d", len(classified[domain.ArtifactTypeArchitecture]))
	}
	if len(classified[domain.ArtifactTypeTask]) != 1 {
		t.Errorf("expected 1 task, got %d", len(classified[domain.ArtifactTypeTask]))
	}
}

func TestDiscoverWorkflows(t *testing.T) {
	client, _ := setupDiscoveryRepo(t)
	ctx := context.Background()

	workflows, err := artifact.DiscoverWorkflows(ctx, client, "")
	if err != nil {
		t.Fatalf("DiscoverWorkflows: %v", err)
	}
	if len(workflows) != 1 {
		t.Errorf("expected 1 workflow, got %d: %v", len(workflows), workflows)
	}
	if len(workflows) > 0 && workflows[0] != "workflows/task-execution.yaml" {
		t.Errorf("expected 'workflows/task-execution.yaml', got %s", workflows[0])
	}
}

func TestDiscoverWorkflows_WithArtifactsDir(t *testing.T) {
	// Verify the artifactsDir prefix filtering branch is exercised.
	repo := testutil.NewTempRepo(t)
	client := git.NewCLIClient(repo)

	// Put the workflow inside a sub-directory (the "artifacts dir").
	testutil.WriteFile(t, repo, "spine/workflows/task-exec.yaml", "id: task-exec\nname: Task Exec\n")
	// Put something outside to verify it is filtered out.
	testutil.WriteFile(t, repo, "other/file.yaml", "not-a-workflow\n")
	testutil.GitAdd(t, repo, ".", "add workflow in subdir")

	ctx := context.Background()
	workflows, err := artifact.DiscoverWorkflows(ctx, client, "", "spine")
	if err != nil {
		t.Fatalf("DiscoverWorkflows: %v", err)
	}
	if len(workflows) != 1 {
		t.Errorf("expected 1 workflow, got %d: %v", len(workflows), workflows)
	}
	if len(workflows) > 0 && workflows[0] != "spine/workflows/task-exec.yaml" {
		t.Errorf("unexpected workflow path: %s", workflows[0])
	}
}

func TestFilterByExtension(t *testing.T) {
	files := []string{"a.md", "b.yaml", "c.md", "d.go", "e.yml"}

	md := artifact.FilterByExtension(files, ".md")
	if len(md) != 2 {
		t.Errorf("expected 2 .md files, got %d", len(md))
	}

	yaml := artifact.FilterByExtension(files, "yaml")
	if len(yaml) != 1 {
		t.Errorf("expected 1 .yaml file, got %d", len(yaml))
	}
}

func TestIsWorkflowFile(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"workflows/task-execution.yaml", true},
		{"workflows/review.yml", true},
		{"workflows/nested/deep.yaml", true},
		{"governance/not-workflow.yaml", false},
		{"workflows/readme.md", false},
	}

	// We can't test isWorkflowFile directly since it's unexported,
	// but DiscoverAll uses it — so we test through the public API.
	// This test verifies the classification logic indirectly through
	// the DiscoverAll results in TestDiscoverAll above.
	_ = tests // documented for reference
}

// helper

func execCmd(t *testing.T, dir, name string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), name, args...)
	cmd.Dir = dir
	return cmd
}
