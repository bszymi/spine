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

Automate the setup of a Git repository for a new workspace. Per [git-integration.md §2.1](/architecture/git-integration.md), each workspace operates against its own Git repository. The provisioning flow supports two modes: cloning an existing remote repo, or initializing a fresh one.

## Deliverable

Updates to `internal/workspace/provision.go` (or similar)

Content should define:

### Two provisioning modes

**Clone mode** (when `git_url` is provided):

1. Create a directory for the workspace's Git repository (e.g., `<repos_base_dir>/<workspace_id>`)
2. Clone the remote repository into this directory
3. Detect if it's already a Spine repo (check for `.spine.yaml`, `governance/`, `workflows/`)
4. **If already a Spine repo** — skip initialization; run a full projection sync to populate the workspace database with all existing artifacts, workflows, and links from the repo
5. **If not a Spine repo** — run `spine init-repo` equivalent setup (directory structure, `.spine.yaml`, seed documents, initial commit)

**Fresh mode** (when no `git_url` is provided):

1. Create a directory for the workspace's Git repository
2. Initialize a new Git repository
3. Run `spine init-repo` equivalent setup
4. Return the repository path

### Shared behavior

- The base directory for workspace repos comes from `SPINE_WORKSPACE_REPOS_DIR`
- Rollback: if any step fails, remove the partially created directory
- The full projection sync (for existing Spine repos) reuses the projection service's `FullRebuild` to scan the entire repo and populate the database

## Acceptance Criteria

- Fresh mode: a new Git repository is created with standard Spine structure
- Clone mode: an existing remote repo is cloned into the workspace directory
- Existing Spine repos are detected and their contents are synced into the projection database
- Non-Spine repos get Spine structure added on top of existing content
- The returned repo path is ready for use by the workspace's Git client
- Failed provisioning cleans up the partially created directory
- Integration tests demonstrate both modes
