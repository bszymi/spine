---
id: TASK-003
type: Task
title: Update git-integration.md repository scope
status: Pending
epic: /initiatives/INIT-009-workspace-runtime/epics/EPIC-002-architecture-update/epic.md
initiative: /initiatives/INIT-009-workspace-runtime/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-002-architecture-update/epic.md
  - type: related_to
    target: /architecture/git-integration.md
---

# TASK-003 — Update git-integration.md repository scope

---

## Purpose

Update the repository scope section of `architecture/git-integration.md` to describe workspace-scoped repository handles.

## Deliverable

Updates to `architecture/git-integration.md` §2.1.

Content should describe:

- Each workspace maps to its own Git repository
- Repository path is resolved from workspace config at the request boundary, not hardcoded per-process
- In single mode, the single repo path comes from environment variables (current behavior)
- In shared mode, each workspace's repo path comes from the workspace registry
- Git credentials and working directories are isolated per workspace
- All existing Git integration rules (branch strategy, commit model, authoritative branch) apply per workspace — they are workspace-scoped, not global

## Acceptance Criteria

- §2.1 no longer reads as if one-repo-per-process is a permanent constraint
- Workspace-scoped repository resolution is described
- Existing Git integration rules are explicitly stated to apply per workspace
- The evolution from v0.x single-repo to workspace-scoped repos is clear
