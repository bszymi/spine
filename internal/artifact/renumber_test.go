package artifact_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/artifact"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/testutil"
)

// ── IsIDCollision tests ──

func TestIsIDCollision_FileExistsOnTarget(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	client := git.NewCLIClient(repo)
	ctx := context.Background()

	// Create a task on main.
	testutil.WriteFile(t, repo, "tasks/TASK-006-existing.md",
		"---\nid: TASK-006\ntype: Task\ntitle: Existing\nstatus: Pending\n---\n# TASK-006\n")
	testutil.GitAdd(t, repo, ".", "add existing task")

	collision, err := artifact.IsIDCollision(ctx, client, "HEAD", "HEAD", "tasks/TASK-006-existing.md")
	if err != nil {
		t.Fatalf("IsIDCollision: %v", err)
	}
	if !collision {
		t.Error("expected collision, got false")
	}
}

func TestIsIDCollision_FileDoesNotExist(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	client := git.NewCLIClient(repo)
	ctx := context.Background()

	testutil.WriteFile(t, repo, "tasks/TASK-001-first.md",
		"---\nid: TASK-001\ntype: Task\ntitle: First\nstatus: Pending\n---\n# TASK-001\n")
	testutil.GitAdd(t, repo, ".", "add task")

	collision, err := artifact.IsIDCollision(ctx, client, "HEAD", "HEAD", "tasks/TASK-999-nonexistent.md")
	if err != nil {
		t.Fatalf("IsIDCollision: %v", err)
	}
	if collision {
		t.Error("expected no collision, got true")
	}
}

// ── RenumberArtifact tests ──

func TestRenumberArtifact_Task(t *testing.T) {
	dir := t.TempDir()

	// Create a task file.
	taskDir := filepath.Join(dir, "tasks")
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nid: TASK-006\ntype: Task\ntitle: Implement validation\nstatus: Draft\n---\n# TASK-006 — Implement validation\n"
	if err := os.WriteFile(filepath.Join(dir, "tasks/task-006-implement-validation.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := artifact.RenumberArtifact(dir, "tasks/task-006-implement-validation.md", "Task", "TASK-006", "TASK-007")
	if err != nil {
		t.Fatalf("RenumberArtifact: %v", err)
	}

	// Check result fields.
	if result.OldID != "TASK-006" {
		t.Errorf("OldID: got %s, want TASK-006", result.OldID)
	}
	if result.NewID != "TASK-007" {
		t.Errorf("NewID: got %s, want TASK-007", result.NewID)
	}
	if result.NewPath != "tasks/task-007-implement-validation.md" {
		t.Errorf("NewPath: got %s, want tasks/task-007-implement-validation.md", result.NewPath)
	}

	// Old file should be gone.
	if _, err := os.Stat(filepath.Join(dir, "tasks/task-006-implement-validation.md")); !os.IsNotExist(err) {
		t.Error("old file should not exist")
	}

	// New file should exist with updated content.
	newContent, err := os.ReadFile(filepath.Join(dir, result.NewPath))
	if err != nil {
		t.Fatalf("read new file: %v", err)
	}
	if !strings.Contains(string(newContent), "id: TASK-007") {
		t.Error("front-matter ID not updated")
	}
	if !strings.Contains(string(newContent), "# TASK-007") {
		t.Error("heading not updated")
	}
}

func TestRenumberArtifact_Epic(t *testing.T) {
	dir := t.TempDir()

	// Create an epic directory structure.
	epicDir := filepath.Join(dir, "epics/epic-004-new-feature")
	if err := os.MkdirAll(epicDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nid: EPIC-004\ntype: Epic\ntitle: New Feature\nstatus: Draft\n---\n# EPIC-004 — New Feature\n"
	if err := os.WriteFile(filepath.Join(epicDir, "epic.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := artifact.RenumberArtifact(dir, "epics/epic-004-new-feature/epic.md", "Epic", "EPIC-004", "EPIC-005")
	if err != nil {
		t.Fatalf("RenumberArtifact: %v", err)
	}

	if result.NewPath != "epics/epic-005-new-feature/epic.md" {
		t.Errorf("NewPath: got %s, want epics/epic-005-new-feature/epic.md", result.NewPath)
	}

	// New file should exist.
	newContent, err := os.ReadFile(filepath.Join(dir, result.NewPath))
	if err != nil {
		t.Fatalf("read new file: %v", err)
	}
	if !strings.Contains(string(newContent), "id: EPIC-005") {
		t.Error("front-matter ID not updated")
	}
}

// ── UpdateLinksToRenamedArtifact tests ──

func TestUpdateLinksToRenamedArtifact(t *testing.T) {
	dir := t.TempDir()

	// Create a task that links to the renamed artifact.
	taskDir := filepath.Join(dir, "tasks")
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nid: TASK-001\ntype: Task\ntitle: Child\nstatus: Draft\nlinks:\n  - type: parent\n    target: /epics/epic-004-feature/epic.md\n---\n# TASK-001\n"
	if err := os.WriteFile(filepath.Join(taskDir, "task-001-child.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	err := artifact.UpdateLinksToRenamedArtifact(dir, "tasks",
		"epics/epic-004-feature/epic.md", "epics/epic-005-feature/epic.md")
	if err != nil {
		t.Fatalf("UpdateLinksToRenamedArtifact: %v", err)
	}

	updated, err := os.ReadFile(filepath.Join(taskDir, "task-001-child.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updated), "/epics/epic-005-feature/epic.md") {
		t.Error("link not updated to new path")
	}
	if strings.Contains(string(updated), "/epics/epic-004-feature/epic.md") {
		t.Error("old link path still present")
	}
}

func TestUpdateLinksToRenamedArtifact_NoLinksUnchanged(t *testing.T) {
	dir := t.TempDir()

	taskDir := filepath.Join(dir, "tasks")
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nid: TASK-002\ntype: Task\ntitle: Unrelated\nstatus: Draft\n---\n# TASK-002\n"
	if err := os.WriteFile(filepath.Join(taskDir, "task-002-unrelated.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	err := artifact.UpdateLinksToRenamedArtifact(dir, "tasks",
		"epics/epic-004-feature/epic.md", "epics/epic-005-feature/epic.md")
	if err != nil {
		t.Fatalf("UpdateLinksToRenamedArtifact: %v", err)
	}

	updated, err := os.ReadFile(filepath.Join(taskDir, "task-002-unrelated.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(updated) != content {
		t.Error("file without links should be unchanged")
	}
}
