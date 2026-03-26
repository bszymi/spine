package cli_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bszymi/spine/internal/cli"
)

func setupWorkflowRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	os.MkdirAll(filepath.Join(dir, "workflows"), 0o755)
	os.WriteFile(filepath.Join(dir, "workflows", "task-default.yaml"), []byte(`
id: wf-task-default
name: Task Default Lifecycle
version: "1.0.0"
status: Active
applies_to: [Task]
entry_step: draft
steps:
  - id: draft
    name: Draft
    type: manual
    outcomes:
      - id: submit
        next_step: review
  - id: review
    name: Review
    type: review
    timeout: "48h"
    outcomes:
      - id: approve
        next_step: end
      - id: reject
        next_step: draft
`), 0o644)

	os.WriteFile(filepath.Join(dir, "workflows", "adr.yaml"), []byte(`
id: wf-adr
name: ADR Review
version: "1.0.0"
status: Active
applies_to: [ADR]
entry_step: propose
steps:
  - id: propose
    name: Propose
    type: manual
    outcomes:
      - id: accept
        next_step: end
`), 0o644)

	return dir
}

func TestListWorkflows(t *testing.T) {
	dir := setupWorkflowRepo(t)

	// Should not error.
	err := cli.ListWorkflows(dir, cli.FormatJSON)
	if err != nil {
		t.Fatalf("ListWorkflows: %v", err)
	}
}

func TestShowWorkflow(t *testing.T) {
	dir := setupWorkflowRepo(t)

	err := cli.ShowWorkflow(dir, "workflows/task-default.yaml", cli.FormatJSON)
	if err != nil {
		t.Fatalf("ShowWorkflow: %v", err)
	}
}

func TestShowWorkflow_Table(t *testing.T) {
	dir := setupWorkflowRepo(t)

	err := cli.ShowWorkflow(dir, "workflows/task-default.yaml", cli.FormatTable)
	if err != nil {
		t.Fatalf("ShowWorkflow table: %v", err)
	}
}

func TestResolveWorkflow(t *testing.T) {
	dir := setupWorkflowRepo(t)

	// Create a task artifact.
	os.MkdirAll(filepath.Join(dir, "initiatives"), 0o755)
	os.WriteFile(filepath.Join(dir, "initiatives", "task.md"), []byte(`---
id: TASK-001
type: Task
title: Test Task
status: Pending
---
# Test
`), 0o644)

	err := cli.ResolveWorkflow(dir, "initiatives/task.md", cli.FormatJSON)
	if err != nil {
		t.Fatalf("ResolveWorkflow: %v", err)
	}
}

func TestResolveWorkflow_NoMatch(t *testing.T) {
	dir := setupWorkflowRepo(t)

	os.MkdirAll(filepath.Join(dir, "initiatives"), 0o755)
	os.WriteFile(filepath.Join(dir, "initiatives", "unknown.md"), []byte(`---
type: Unknown
---
`), 0o644)

	err := cli.ResolveWorkflow(dir, "initiatives/unknown.md", cli.FormatJSON)
	if err == nil {
		t.Error("expected error for unresolvable artifact type")
	}
}

func TestListWorkflows_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "workflows"), 0o755)

	err := cli.ListWorkflows(dir, cli.FormatTable)
	if err != nil {
		t.Fatalf("ListWorkflows empty: %v", err)
	}
}
