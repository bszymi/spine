---
id: TASK-007
type: Task
title: Idle eviction and invalidation-driven pool close
status: Draft
epic: /initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/epics/EPIC-003-pool-policy/epic.md
initiative: /initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/epics/EPIC-003-pool-policy/epic.md
  - type: blocked_by
    target: /initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/epics/EPIC-003-pool-policy/tasks/TASK-006-pool-sizing-config.md
  - type: blocked_by
    target: /initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/epics/EPIC-002-resolver-secret-ref/tasks/TASK-005-cache-and-invalidation.md
---

# TASK-007 — Idle eviction and invalidation-driven pool close

---

## Purpose

Close pools that aren't being used so a Spine instance hosting
many workspaces does not hold thousands of mostly-idle
connections. Also: when a binding is invalidated by the
platform, drop the workspace's pool so the next request
re-resolves credentials.

## Deliverable

- Idle-eviction loop. Default timeout decided in ADR-012;
  initial proposal: 10 minutes of zero traffic → close pool.
- Hook into the invalidation channel from TASK-005: on
  invalidate, close the affected workspace's pool immediately.
- Metrics: pool open/close events per workspace.

## Acceptance Criteria

- A workspace receiving no traffic for the configured idle
  timeout has its pool closed and connections returned.
- A platform `Invalidate` for workspace A closes A's pool and
  leaves B's, C's, ... untouched.
- A subsequent request to A re-resolves the binding, fetches
  current credentials, and opens a new pool.
- Open/close events are visible in metrics and logs (with
  workspace ID, never with credentials).
