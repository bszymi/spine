---
id: TASK-003
type: Task
title: Wire primary client through existing services
status: Completed
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-003-git-client-pool-and-routing/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: implementation
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-003-git-client-pool-and-routing/epic.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-003-git-client-pool-and-routing/tasks/TASK-001-git-client-pool-interface.md
---

# TASK-003 - Wire Primary Client Through Existing Services

---

## Purpose

Introduce the pool without disturbing governance services that must continue reading the primary Spine repo only.

## Deliverable

Update service construction so artifact, workflow, projection, validation, and binding resolution continue using `pool.PrimaryClient()`.

Also expose the full pool to engine and git HTTP code paths that need repository routing.

## Acceptance Criteria

- Existing single-repo tests pass without fixture changes.
- Projection sync only reads the primary repo.
- Workflow definitions still resolve from the primary repo.
- Artifact writes still target the primary repo unless explicitly routed by execution code.
- Build wiring has no nil pool paths in shared mode.

