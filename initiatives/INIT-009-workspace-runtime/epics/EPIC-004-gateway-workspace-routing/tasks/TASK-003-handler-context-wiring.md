---
id: TASK-003
type: Task
title: Handler context wiring
status: Completed
epic: /initiatives/INIT-009-workspace-runtime/epics/EPIC-004-gateway-workspace-routing/epic.md
initiative: /initiatives/INIT-009-workspace-runtime/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-004-gateway-workspace-routing/epic.md
  - type: depends_on
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-004-gateway-workspace-routing/tasks/TASK-002-service-pool.md
---

# TASK-003 — Handler context wiring

---

## Purpose

Refactor gateway handlers to retrieve workspace-scoped services from request context instead of using single injected instances.

## Deliverable

Updates to `internal/gateway/server.go` and `internal/gateway/handlers_*.go`.

Content should define:

- Context helper functions: `workspace.FromContext(ctx)` to get the service set
- Middleware resolves workspace, gets service set from pool, stores in context
- All handlers pull services from context
- In single-workspace mode, the one service set is always in context (backward compatible)

## Acceptance Criteria

- All gateway handlers use workspace-scoped services from context
- No handler directly references a global/singleton service instance
- In single-workspace mode, behavior is identical to current Spine
- In shared mode, different requests operate against different workspace service sets concurrently
