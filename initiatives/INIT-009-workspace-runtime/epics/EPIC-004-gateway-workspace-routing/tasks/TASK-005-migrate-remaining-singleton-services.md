---
id: TASK-005
type: Task
title: Migrate remaining singleton services to workspace-scoped construction
status: Completed
epic: /initiatives/INIT-009-workspace-runtime/epics/EPIC-004-gateway-workspace-routing/epic.md
initiative: /initiatives/INIT-009-workspace-runtime/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-004-gateway-workspace-routing/epic.md
  - type: depends_on
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-004-gateway-workspace-routing/tasks/TASK-002-service-pool.md
  - type: depends_on
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-004-gateway-workspace-routing/tasks/TASK-003-handler-context-wiring.md
---

# TASK-005 — Migrate remaining singleton services to workspace-scoped construction

---

## Purpose

Several gateway handlers and the multi-workspace scheduler still use singleton service instances instead of pulling workspace-scoped services from the `ServiceSet`. These are marked with `TODO(INIT-009)` comments in the code. This task tracks converting them all and removing the TODO markers.

## Locations

| File | Singleton | Line |
|---|---|---|
| `internal/gateway/handlers_divergence.go:43` | `branchCreator` | needs workspace-scoped construction in `ServiceSet` |
| `internal/gateway/handlers_divergence.go:76` | `branchCreator` | same |
| `internal/gateway/handlers_system.go:155` | `validator` | needs workspace-scoped construction in `ServiceSet` |
| `internal/gateway/handlers_workflow.go:74` | `planningRunStarter` | needs workspace-scoped orchestrator in `ServiceSet` |
| `internal/gateway/handlers_workflow.go:108` | `runStarter` | needs workspace-scoped orchestrator in `ServiceSet` |
| `internal/gateway/handlers_workspaces.go:82` | — | call database provisioning (EPIC-007/TASK-002) |
| `internal/scheduler/multi.go:123` | engine callbacks | needs workspace-scoped callbacks |

## Deliverable

- Replace each singleton reference with a workspace-scoped service obtained from context or `ServiceSet`
- Wire `branchCreator`, `validator`, `planningRunStarter`, `runStarter`, and engine callbacks into `ServiceSet`
- Remove all `TODO(INIT-009)` comments from the codebase
- The `handlers_workspaces.go` provisioning call depends on EPIC-007/TASK-002 — wire it when that task is ready or leave a clean interface stub

## Acceptance Criteria

- No `TODO(INIT-009)` comments remain in the codebase
- All handler services are retrieved from the workspace-scoped `ServiceSet`
- Scheduler callbacks are workspace-scoped
- Existing tests continue to pass
- Single-workspace mode behavior is unchanged
