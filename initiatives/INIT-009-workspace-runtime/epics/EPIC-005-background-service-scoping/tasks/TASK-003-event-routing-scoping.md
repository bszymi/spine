---
id: TASK-003
type: Task
title: Workspace-scoped event routing
status: Completed
epic: /initiatives/INIT-009-workspace-runtime/epics/EPIC-005-background-service-scoping/epic.md
initiative: /initiatives/INIT-009-workspace-runtime/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-005-background-service-scoping/epic.md
---

# TASK-003 — Workspace-scoped event routing

---

## Purpose

Ensure events are scoped to workspace context so one workspace's events don't trigger processing in another.

## Deliverable

Updates to `internal/event/`.

Content should define:

- In the service pool model, each workspace's service set has its own in-memory event router — providing natural isolation without filtering
- Alternatively, events include workspace ID in their routing key and consumers filter accordingly
- Events emitted during a request are routed only to the originating workspace's services

## Acceptance Criteria

- An event in workspace A does not trigger processing in workspace B
- Event routing works correctly in both single-workspace and shared-runtime modes
