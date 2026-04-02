---
id: EPIC-005
type: Epic
title: Background Service Scoping
status: Completed
initiative: /initiatives/INIT-009-workspace-runtime/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-009-workspace-runtime/initiative.md
  - type: depends_on
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-003-workspace-registry/epic.md
  - type: depends_on
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-004-gateway-workspace-routing/epic.md
  - type: related_to
    target: /architecture/components.md
---

# EPIC-005 — Background Service Scoping

---

## Purpose

Adapt background services — scheduler, projection sync, event routing — to operate correctly across multiple workspaces. As described in [components.md §6.5](/architecture/components.md), background services iterate over active workspaces from the resolver's `List()` method and process each using its service set from the pool.

---

## Key Work Areas

- Scheduler adaptation: iterate over active workspaces for orphan detection, run timeout, and recovery
- Projection sync per workspace: each workspace maintains independent sync state and Git polling
- Event routing scoped to workspace context
- Graceful handling of workspace addition/removal at runtime
- Resource management: limit concurrent background operations across workspaces

---

## Primary Outputs

- Updated `internal/scheduler/` — multi-workspace iteration
- Updated `internal/projection/` — per-workspace sync loops
- Updated `internal/event/` — workspace-scoped event routing
- Tests for background operations across multiple workspaces

---

## Acceptance Criteria

- Scheduler detects orphans and timeouts independently per workspace
- Projection service tracks sync state independently per workspace
- Adding a new workspace starts background services for it without restart
- Removing a workspace stops its background services gracefully
- A slow or failing workspace's background work does not block other workspaces
- Events from one workspace do not trigger processing in another
