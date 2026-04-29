---
id: TASK-002
type: Task
title: Create run branches across affected repositories
status: Completed
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-004-multi-repo-run-lifecycle/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: implementation
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-004-multi-repo-run-lifecycle/epic.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-004-multi-repo-run-lifecycle/tasks/TASK-001-run-affected-repositories-model.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-003-git-client-pool-and-routing/tasks/TASK-001-git-client-pool-interface.md
---

# TASK-002 - Create Run Branches Across Affected Repositories

---

## Purpose

Create the same run branch name in the primary repo and every affected code repo.

## Deliverable

Update run startup so branch creation loops over affected repositories.

Rules:

- Primary repo branch is always created.
- Code repo branches are created from each repo's default branch.
- The branch name is identical across repos.
- Branch creation happens before the run is activated.

## Acceptance Criteria

- A multi-repo task creates branches in all affected repos.
- Single-repo tasks still create only one branch.
- Branch creation uses each repo's default branch.
- Auto-push behavior applies per affected repo when enabled.
- Errors include the failing repository ID.

