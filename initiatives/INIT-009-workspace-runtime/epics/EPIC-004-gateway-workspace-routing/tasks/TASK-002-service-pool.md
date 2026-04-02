---
id: TASK-002
type: Task
title: Per-workspace service pool
status: Completed
epic: /initiatives/INIT-009-workspace-runtime/epics/EPIC-004-gateway-workspace-routing/epic.md
initiative: /initiatives/INIT-009-workspace-runtime/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-004-gateway-workspace-routing/epic.md
  - type: depends_on
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-004-gateway-workspace-routing/tasks/TASK-001-workspace-middleware.md
---

# TASK-002 — Per-workspace service pool

---

## Purpose

Build the service pool described in [components.md §6.5](/architecture/components.md) — a cache that lazily creates and manages per-workspace service sets.

## Deliverable

`internal/workspace/pool.go`

A service set includes: `git.CLIClient`, `store.Store`, `artifact.Service`, `projection.Service`, `engine.Orchestrator`, and any other per-workspace dependencies.

Content should define:

- `ServiceSet` struct holding all per-workspace service instances
- `ServicePool` that maps workspace ID to `ServiceSet`
- Lazy initialization: first request triggers service set creation
- Thread-safe access (no double-initialization on concurrent first requests)
- Idle eviction: service sets unused for a configurable duration are closed and removed
- Graceful shutdown: all service sets closed on runtime shutdown

## Acceptance Criteria

- First request for a workspace initializes its full service set
- Subsequent requests reuse the cached service set
- Concurrent first requests for the same workspace only initialize once
- Idle service sets are evicted after configurable timeout (default: 15 minutes)
- Eviction properly closes database connections and releases resources
- Service pool reports active workspace count for monitoring
