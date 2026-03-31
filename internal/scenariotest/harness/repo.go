package harness

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/testutil"
)

// TestRepo wraps a temporary Git repository for scenario testing.
// Each test gets its own isolated repository, enabling parallel execution.
type TestRepo struct {
	Dir string
	Git *git.CLIClient
}

// NewTestRepo creates a temporary Git repository initialized with a main
// branch and an empty initial commit. Cleanup is automatic via t.TempDir().
func NewTestRepo(t *testing.T) *TestRepo {
	t.Helper()

	dir := testutil.NewTempRepo(t)
	return &TestRepo{
		Dir: dir,
		Git: git.NewCLIClient(dir),
	}
}

// SeedGovernance creates the standard Spine directory structure and seeds
// governance documents (Constitution, Charter, Guidelines, Repository Structure,
// Naming Conventions). The seeded content matches what `spine init` produces.
// All files are committed as a single "Seed governance" commit with
// deterministic timestamps for reproducibility.
func (r *TestRepo) SeedGovernance(t *testing.T) {
	t.Helper()

	// Create directory structure.
	dirs := []string{
		"governance", "initiatives", "architecture",
		"product", "workflows", "templates", "tmp",
	}
	for _, dir := range dirs {
		p := filepath.Join(r.Dir, dir)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatalf("create directory %s: %v", dir, err)
		}
	}

	// Seed governance documents.
	for path, content := range seedGovernanceFiles {
		testutil.WriteFile(t, r.Dir, path, content)
	}

	r.deterministicCommit(t, ".", "Seed governance")
}

// SeedWorkflows writes the default workflow definitions and commits them.
// Must be called after SeedGovernance (which creates the workflows/ directory).
func (r *TestRepo) SeedWorkflows(t *testing.T) {
	t.Helper()

	for path, content := range seedWorkflowFiles {
		testutil.WriteFile(t, r.Dir, path, content)
	}

	r.deterministicCommit(t, ".", "Seed workflows")
}

// WriteArtifact creates an artifact file at the given path with the given content.
// Parent directories are created as needed.
func (r *TestRepo) WriteArtifact(t *testing.T, path, content string) {
	t.Helper()
	testutil.WriteFile(t, r.Dir, path, content)
}

// CommitAll stages all changes and creates a commit with deterministic
// timestamps for reproducibility.
func (r *TestRepo) CommitAll(t *testing.T, message string) {
	t.Helper()
	r.deterministicCommit(t, ".", message)
}

// deterministicCommit stages the given path and commits with fixed timestamps,
// matching the deterministic date used by testutil.NewTempRepo.
func (r *TestRepo) deterministicCommit(t *testing.T, path, message string) {
	t.Helper()

	run := func(args ...string) {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = r.Dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z",
			"GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	run("git", "add", path)
	run("git", "commit", "-m", message)
}

// HeadSHA returns the current HEAD commit SHA.
func (r *TestRepo) HeadSHA(t *testing.T) string {
	t.Helper()

	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = r.Dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// AddBareRemote creates a bare Git repository and adds it as "origin".
// Returns the path to the bare repository for verification.
// The bare directory is created as a sibling of the repo dir so it shares
// the same parent temp directory lifecycle.
func (r *TestRepo) AddBareRemote(t *testing.T) string {
	t.Helper()
	bare := filepath.Join(filepath.Dir(r.Dir), "bare")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatalf("create bare dir: %v", err)
	}
	run := func(dir string, args ...string) {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(bare, "git", "init", "--bare")
	run(r.Dir, "git", "remote", "add", "origin", bare)
	run(r.Dir, "git", "push", "-u", "origin", "main")
	return bare
}

// RemoteBranchExists checks if a branch exists on the bare remote.
func RemoteBranchExists(t *testing.T, bareDir, branch string) bool {
	t.Helper()
	cmd := exec.Command("git", "branch", "--list", branch)
	cmd.Dir = bareDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git branch --list: %v", err)
	}
	return strings.TrimSpace(string(out)) != ""
}

// RemoteHeadContains checks if the HEAD commit message on a remote branch
// contains the given substring.
func RemoteHeadContains(t *testing.T, bareDir, branch, substring string) bool {
	t.Helper()
	cmd := exec.Command("git", "log", "--oneline", "-1", branch)
	cmd.Dir = bareDir
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), substring)
}

// FileExists returns true if the file at the given repo-relative path exists.
func (r *TestRepo) FileExists(path string) bool {
	_, err := os.Stat(filepath.Join(r.Dir, path))
	return err == nil
}

// seedGovernanceFiles maps repo-relative paths to seed content.
// Content matches what `spine init` (internal/cli/initrepo.go) produces.
var seedGovernanceFiles = map[string]string{
	"governance/charter.md": `---
type: Governance
title: Charter
status: Draft
---

# Charter

This document defines the project's mission, values, and principles.
`,
	"governance/constitution.md": `---
type: Governance
title: Constitution
status: Draft
---

# Constitution

This document defines the governance rules and constraints for the project.
`,
	"governance/guidelines.md": `---
type: Governance
title: Guidelines
status: Draft
---

# Guidelines

This document defines coding, contribution, and operational guidelines.
`,
	"governance/repository-structure.md": `---
type: Governance
title: Repository Structure
status: Draft
---

# Repository Structure

Defines the directory layout and file organization conventions for the repository.
`,
	"governance/naming-conventions.md": `---
type: Governance
title: Naming Conventions
status: Draft
---

# Naming Conventions

Defines naming rules for artifacts, directories, branches, and identifiers.
`,
}

// seedWorkflowFiles maps repo-relative paths to workflow YAML content.
// These are faithful copies of the checked-in workflow definitions.
// If the production workflows change, these must be updated to match.
var seedWorkflowFiles = map[string]string{
	"workflows/task-default.yaml": `id: task-default
name: Default Task Workflow
version: "1.0"
status: Active
description: >
  Standard workflow for implementation tasks. Covers the full lifecycle:
  draft setup, execution by an actor, peer review, and Git commit of outcomes.

applies_to:
  - Task

entry_step: draft

steps:
  - id: draft
    name: Draft Setup
    type: automated
    execution:
      mode: ai_only
      eligible_actor_types:
        - ai_agent
    preconditions:
      - type: artifact_status
        config:
          status: Pending
    required_inputs:
      - task_artifact
    outcomes:
      - id: ready
        name: Ready for Execution
        next_step: execute
        commit:
          status: In Progress
    retry:
      limit: 2
      backoff: fixed
    timeout: "30m"
    timeout_outcome: ready

  - id: execute
    name: Execute Task
    type: manual
    execution:
      mode: hybrid
      eligible_actor_types:
        - ai_agent
        - human
    preconditions:
      - type: field_value
        config:
          field: status
          value: In Progress
    required_outputs:
      - deliverable
    outcomes:
      - id: completed
        name: Implementation Complete
        next_step: review
      - id: blocked
        name: Blocked on Dependency
        next_step: draft
    retry:
      limit: 3
      backoff: exponential
    timeout: "4h"

  - id: review
    name: Review Deliverable
    type: review
    execution:
      mode: hybrid
      eligible_actor_types:
        - human
        - ai_agent
    preconditions:
      - type: links_exist
        config:
          link_type: parent
    required_inputs:
      - deliverable
    outcomes:
      - id: accepted
        name: Accepted
        next_step: commit
      - id: needs_rework
        name: Needs Rework
        next_step: execute
    timeout: "24h"

  - id: commit
    name: Commit Outcomes
    type: automated
    execution:
      mode: automated_only
      eligible_actor_types:
        - automated_system
    outcomes:
      - id: committed
        name: Changes Committed
        next_step: end
        commit:
          status: Completed
    retry:
      limit: 3
      backoff: exponential
    timeout: "10m"
    timeout_outcome: committed
`,
	"workflows/task-spike.yaml": `id: task-spike
name: Spike Investigation Workflow
version: "1.0"
status: Draft
description: >
  Simplified workflow for spike and investigation tasks. Focuses on
  exploring a question and summarizing findings rather than producing
  a deliverable artifact.

applies_to:
  - Task

entry_step: investigate

steps:
  - id: investigate
    name: Investigate
    type: manual
    execution:
      mode: hybrid
      eligible_actor_types:
        - ai_agent
        - human
    required_outputs:
      - findings
    outcomes:
      - id: findings_ready
        name: Findings Ready
        next_step: summarize
      - id: inconclusive
        name: Inconclusive
        next_step: end
        commit:
          status: Completed
    retry:
      limit: 2
      backoff: fixed
    timeout: "8h"

  - id: summarize
    name: Summarize Findings
    type: automated
    execution:
      mode: ai_only
      eligible_actor_types:
        - ai_agent
    required_inputs:
      - findings
    required_outputs:
      - summary
    outcomes:
      - id: summarized
        name: Summary Complete
        next_step: review
    retry:
      limit: 2
      backoff: fixed
    timeout: "1h"

  - id: review
    name: Review Summary
    type: review
    execution:
      mode: hybrid
      eligible_actor_types:
        - human
        - ai_agent
    required_inputs:
      - summary
    outcomes:
      - id: accepted
        name: Accepted
        next_step: end
        commit:
          status: Completed
      - id: needs_more_investigation
        name: Needs More Investigation
        next_step: investigate
    timeout: "24h"
`,
}
