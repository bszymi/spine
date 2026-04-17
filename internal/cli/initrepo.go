package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// InitOpts configures init-repo behavior.
type InitOpts struct {
	ArtifactsDir string // Target directory for artifacts (default: "spine")
	NoBranch     bool   // If true, commit directly to current branch
}

// InitRepo initializes a Spine repository at the given path.
// Creates directory structure per repository-structure.md, seeds governance
// templates, and initializes Git if needed. Idempotent: skips existing files.
func InitRepo(repoPath string, opts ...InitOpts) error {
	if repoPath == "" {
		repoPath = "."
	}

	opt := InitOpts{ArtifactsDir: "spine"}
	if len(opts) > 0 {
		opt = opts[0]
	}

	// Normalize artifacts dir.
	artifactsDir := normalizeArtifactsDir(opt.ArtifactsDir)

	absPath, err := filepath.Abs(repoPath)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}

	// Initialize Git if not already a repository.
	gitDir := filepath.Join(absPath, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		cmd := exec.Command("git", "init", "-b", "main", absPath)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git init: %s: %w", string(out), err)
		}
	}

	// Create .spine.yaml at repo root (if not exists).
	spineYAML := filepath.Join(absPath, ".spine.yaml")
	if _, err := os.Stat(spineYAML); os.IsNotExist(err) {
		content := fmt.Sprintf("artifacts_dir: %s\n", artifactsDir)
		if err := os.WriteFile(spineYAML, []byte(content), 0o644); err != nil {
			return fmt.Errorf("write .spine.yaml: %w", err)
		}
	}

	// Determine the base directory for artifacts.
	artifactBase := absPath
	if artifactsDir != "/" {
		artifactBase = filepath.Join(absPath, artifactsDir)
	}

	// Create directory structure inside artifacts dir.
	dirs := []string{
		"governance",
		"initiatives",
		"architecture",
		"product",
		"workflows",
		"templates",
		"tmp",
	}
	for _, dir := range dirs {
		p := filepath.Join(artifactBase, dir)
		if err := os.MkdirAll(p, 0o755); err != nil {
			return fmt.Errorf("create directory %s: %w", dir, err)
		}
	}

	// Seed files — only write if file doesn't already exist.
	seeds := map[string]string{
		"governance/charter.md":              seedCharter,
		"governance/constitution.md":         seedConstitution,
		"governance/guidelines.md":           seedGuidelines,
		"governance/repository-structure.md": seedRepoStructure,
		"governance/naming-conventions.md":   seedNamingConventions,
		"templates/task-template.md":         seedTaskTemplate,
		"templates/epic-template.md":         seedEpicTemplate,
		"templates/initiative-template.md":   seedInitiativeTemplate,
		"templates/adr-template.md":          seedADRTemplate,
		"workflows/workflow-lifecycle.yaml":  seedWorkflowLifecycle,
	}

	for relPath, content := range seeds {
		fullPath := filepath.Join(artifactBase, relPath)
		if _, err := os.Stat(fullPath); err == nil {
			continue // file exists, skip
		}
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", relPath, err)
		}
	}

	// Commit on a branch unless --no-branch.
	if !opt.NoBranch {
		if err := commitOnBranch(absPath); err != nil {
			return err
		}
	}

	return nil
}

// normalizeArtifactsDir cleans the artifacts-dir flag value for .spine.yaml.
func normalizeArtifactsDir(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" || dir == "." || dir == "./" || dir == "/" {
		return "/"
	}
	dir = strings.TrimPrefix(dir, "./")
	dir = strings.TrimSuffix(dir, "/")
	return dir
}

// commitOnBranch creates a spine/init branch, commits all changes, and pushes.
func commitOnBranch(repoDir string) error {
	run := func(args ...string) (string, error) {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repoDir
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}

	// Create and checkout the init branch.
	if _, err := run("git", "checkout", "-b", "spine/init"); err != nil {
		// Branch may already exist from a previous run.
		if _, err2 := run("git", "checkout", "spine/init"); err2 != nil {
			return fmt.Errorf("create init branch: %w", err)
		}
	}

	// Stage and commit.
	if _, err := run("git", "add", "."); err != nil {
		return fmt.Errorf("git add: %w", err)
	}

	if _, err := run("git", "commit", "-m", "Initialize Spine workspace"); err != nil {
		// May fail if nothing to commit (idempotent).
		return nil
	}

	// Push if remote exists and auto-push enabled.
	if !autoPushDisabled() {
		if _, err := run("git", "push", "-u", "origin", "spine/init"); err != nil {
			// Push failure is non-fatal — remote may not exist.
			fmt.Fprintf(os.Stderr, "Note: could not push to origin (remote may not be configured)\n")
		}
	}

	fmt.Println("Spine workspace initialized on branch 'spine/init'.")
	fmt.Println("Create a pull request to merge it to main:")
	fmt.Println("  gh pr create --base main --head spine/init")

	return nil
}

// autoPushDisabled checks the SPINE_GIT_AUTO_PUSH env var.
func autoPushDisabled() bool {
	return strings.EqualFold(os.Getenv("SPINE_GIT_AUTO_PUSH"), "false")
}

// Seed content — minimal valid artifacts with YAML front matter.

const seedCharter = `---
type: Governance
title: Charter
status: Draft
---

# Charter

This document defines the project's mission, values, and principles.
`

const seedConstitution = `---
type: Governance
title: Constitution
status: Draft
---

# Constitution

This document defines the governance rules and constraints for the project.
`

const seedGuidelines = `---
type: Governance
title: Guidelines
status: Draft
---

# Guidelines

This document defines coding, contribution, and operational guidelines.
`

const seedRepoStructure = `---
type: Governance
title: Repository Structure
status: Draft
---

# Repository Structure

Defines the directory layout and file organization conventions for the repository.
`

const seedNamingConventions = `---
type: Governance
title: Naming Conventions
status: Draft
---

# Naming Conventions

Defines naming rules for artifacts, directories, branches, and identifiers.
`

const seedTaskTemplate = `---
id: TASK-XXX
type: Task
title: "[Task Title]"
status: Pending
epic: /initiatives/INIT-XXX/epics/EPIC-XXX/epic.md
initiative: /initiatives/INIT-XXX/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-XXX/epics/EPIC-XXX/epic.md
---

# TASK-XXX — [Task Title]

## Purpose

[What this task accomplishes and why it is needed.]

## Deliverable

- [Expected deliverable]

## Acceptance Criteria

- [Verifiable condition]
`

const seedEpicTemplate = `---
id: EPIC-XXX
type: Epic
title: "[Epic Title]"
status: Pending
initiative: /initiatives/INIT-XXX/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-XXX/initiative.md
---

# EPIC-XXX — [Epic Title]

## Purpose

[What this epic accomplishes.]

## Key Work Areas

- [Work area]

## Acceptance Criteria

- [Verifiable condition]
`

const seedInitiativeTemplate = `---
id: INIT-XXX
type: Initiative
title: "[Initiative Title]"
status: Pending
owner: "[owner]"
created: "YYYY-MM-DD"
---

# INIT-XXX — [Initiative Title]

## Intent

[What this initiative aims to accomplish.]

## Scope

- [Scope item]

## Success Criteria

- [Measurable outcome]
`

const seedADRTemplate = `---
id: ADR-XXX
type: ADR
title: "[Decision Title]"
status: Proposed
date: "YYYY-MM-DD"
decision_makers: "[names]"
---

# ADR-XXX — [Decision Title]

## Context

[What is the issue that motivates this decision?]

## Decision

[What is the change that we're proposing?]

## Consequences

[What becomes easier or more difficult as a result?]
`

// seedWorkflowLifecycle governs workflow.create/update per ADR-008.
// Seeded at init so fresh repos can edit workflows through the governed
// branch + approval flow from day one. Teams tighten governance by
// editing this file; the edit itself flows through this same workflow.
const seedWorkflowLifecycle = `id: workflow-lifecycle
name: Workflow Lifecycle
version: "1.0"
status: Active
description: >
  Governs creation and modification of workflow definitions (ADR-008).
  Two steps: draft (author the workflow body on a branch) and review
  (approve or request rework). Approval merges the branch to the
  authoritative branch; rework keeps the branch alive for further edits.
  Teams that want stricter governance extend this workflow by editing
  this file — the edit itself flows through the lifecycle workflow.
mode: creation

applies_to:
  - Workflow

entry_step: draft

steps:
  - id: draft
    name: Draft Workflow
    type: manual
    execution:
      mode: hybrid
      eligible_actor_types:
        - human
        - ai_agent
      required_skills: [workflow_authoring]
    description: >
      Author or edit the workflow definition on the planning run's branch.
      Repeated workflow.update calls on the same run_id stack commits on
      the branch. Submit when the workflow body is ready for review.
    required_outputs:
      - workflow_body
    outcomes:
      - id: submitted
        name: Submitted for Review
        next_step: review

  - id: review
    name: Review Workflow
    type: review
    execution:
      mode: human_only
      eligible_actor_types:
        - human
      required_skills: [workflow_review]
    description: >
      Evaluate the workflow change for domain correctness — step sequence,
      actor/skill assignments, retry and timeout policy. Structural validity
      is already enforced by the validator at write time; review covers
      semantics. Approve to merge the branch to the authoritative branch;
      request rework to loop back to draft and keep the branch alive.
    required_inputs:
      - workflow_body
    outcomes:
      - id: approved
        name: Approved
        next_step: end
        # Non-empty commit map signals the orchestrator to merge the run
        # branch. The "merge" key is structural — applyCommitStatus only
        # acts on a "status" key, which does not apply here since workflow
        # YAML files have no Markdown front matter to rewrite.
        commit:
          merge: "true"
      - id: needs_rework
        name: Needs Rework
        next_step: draft
    timeout: "168h"
    timeout_outcome: needs_rework
`
