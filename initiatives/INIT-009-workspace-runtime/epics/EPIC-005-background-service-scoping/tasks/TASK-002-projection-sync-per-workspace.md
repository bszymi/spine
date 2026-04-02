---
id: TASK-002
type: Task
title: Per-workspace projection sync
status: Pending
epic: /initiatives/INIT-009-workspace-runtime/epics/EPIC-005-background-service-scoping/epic.md
initiative: /initiatives/INIT-009-workspace-runtime/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-005-background-service-scoping/epic.md
---

# TASK-002 — Per-workspace projection sync

---

## Purpose

Adapt the projection service so each workspace maintains independent sync state and Git polling. Since each workspace has its own database (per [data-model.md §7.2](/architecture/data-model.md)), the existing `'global'` sync_state row works within each workspace's database — no schema change needed.

## Deliverable

Updates to `internal/projection/` and potentially `internal/store/postgres.go`.

Content should define:

- Projection sync loop iterates over active workspaces and runs sync for each
- Each workspace syncs its Git repo to its own projection database independently
- New workspaces trigger an initial full sync on first activation
- Sync errors in one workspace do not block other workspaces

## Acceptance Criteria

- Each workspace syncs independently
- Sync progress for workspace A is not affected by workspace B
- A new workspace gets a full initial sync
- Sync errors are logged with workspace ID and do not cascade
