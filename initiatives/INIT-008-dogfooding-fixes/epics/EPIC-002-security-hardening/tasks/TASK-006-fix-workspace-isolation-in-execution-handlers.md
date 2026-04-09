---
id: TASK-006
type: Task
title: "Fix workspace isolation bypass in execution query handlers"
status: Completed
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-002-security-hardening/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: bugfix
created: 2026-04-09
last_updated: 2026-04-09
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-002-security-hardening/epic.md
---

# TASK-006 — Fix Workspace Isolation Bypass in Execution Query Handlers

---

## Purpose

All four execution query handlers in `/internal/gateway/handlers_execution_query.go` use `s.store.QueryExecutionProjections()` directly instead of `s.storeFrom(r.Context())`. In multi-workspace mode, this leaks task execution data across workspace boundaries.

---

## Deliverable

Change `s.store` to `s.storeFrom(r.Context())` in all four handlers.

---

## Acceptance Criteria

- Execution query handlers use workspace-scoped store
- No cross-workspace data leakage in shared mode
- Existing tests pass
