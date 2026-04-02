---
id: TASK-003
type: Task
title: Git repository provisioning
status: Pending
epic: /initiatives/INIT-009-workspace-runtime/epics/EPIC-007-workspace-provisioning/epic.md
initiative: /initiatives/INIT-009-workspace-runtime/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-007-workspace-provisioning/epic.md
---

# TASK-003 — Git repository provisioning

---

## Purpose

Automate the initialization of a Git repository for a new workspace. Per [git-integration.md §2.1](/architecture/git-integration.md), each workspace operates against its own Git repository.

## Deliverable

Updates to `internal/workspace/provision.go` (or similar)

Content should define:

- A provisioning function that:
  1. Creates a directory for the workspace's Git repository (e.g., `<repos_base_dir>/<workspace_id>`)
  2. Initializes a bare or working Git repository
  3. Runs `spine init-repo` equivalent setup (directory structure, `.spine.yaml`, seed documents)
  4. Returns the repository path for the workspace
- The base directory for workspace repos comes from `SPINE_WORKSPACE_REPOS_DIR`
- Rollback: if init fails, remove the partially created directory

## Acceptance Criteria

- A new Git repository is created and initialized for the workspace
- The repository has the standard Spine directory structure and `.spine.yaml`
- The returned repo path is ready for use by the workspace's Git client
- Failed provisioning cleans up the partially created directory
- Integration test demonstrates repo creation and initialization
