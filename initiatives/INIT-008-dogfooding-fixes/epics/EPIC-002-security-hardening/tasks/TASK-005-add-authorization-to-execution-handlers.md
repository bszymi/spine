---
id: TASK-005
type: Task
title: "Add authorization to execution handlers"
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

# TASK-005 — Add Authorization to Execution Handlers

---

## Purpose

Seven execution handlers in `/internal/gateway/` (handlers_execution_query.go, handlers_claim.go, handlers_release.go, handlers_candidates.go) are behind auth middleware but none call `s.authorize()` for operation-level RBAC. Any authenticated user with any role (including `reader`) can claim steps, release assignments, or see all execution state.

---

## Deliverable

1. Add `s.authorize()` calls to all seven execution handlers
2. Add corresponding execution operations to `/internal/auth/permissions.go` operationRoles map
3. Map claim/release to at minimum `RoleContributor`, query endpoints to `RoleReader`

---

## Acceptance Criteria

- All execution endpoints enforce operation-level RBAC
- A reader-role token cannot claim or release steps
- Existing tests pass; add auth tests for execution endpoints
