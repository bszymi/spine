---
id: TASK-013
type: Task
title: "Validate X-Workspace-ID header against authenticated actor"
status: Completed
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: bugfix
created: 2026-04-16
last_updated: 2026-04-16
completed: 2026-04-16
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/epic.md
---

# TASK-013 — Validate X-Workspace-ID Header Against Authenticated Actor

---

## Purpose

`internal/gateway/middleware.go:51-94` resolves the workspace from a client-supplied header with no explicit check that the authenticated actor belongs to that workspace. Data isolation currently relies on the workspace-scoped service pool routing requests to separate DB pools. This works today, but there is no defense-in-depth guard; a future code path that falls back to a shared store would silently break isolation.

---

## Deliverable

- In the workspace middleware, after resolving the workspace, assert the authenticated actor has membership or a role in that workspace.
- Fail closed (403) if no explicit membership record is found.
- Add an integration test: actor in WS-A requests WS-B → 403.

---

## Acceptance Criteria

- Cross-workspace access attempts return 403, not 200.
- Existing single-workspace tests unaffected.
