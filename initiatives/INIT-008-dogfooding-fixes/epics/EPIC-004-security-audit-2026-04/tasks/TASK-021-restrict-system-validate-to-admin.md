---
id: TASK-021
type: Task
title: "Restrict /system/validate to admin role"
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

# TASK-021 — Restrict /system/validate To Admin Role

---

## Purpose

`internal/gateway/handlers_system.go:223-231` returns all artifacts' validation results to any authenticated user with the `system.validate` permission. That permission is broad and currently reachable by reviewer-level actors. The response reveals the entire artifact tree and status landscape — more than any single role typically needs.

---

## Deliverable

- Raise the required permission on `/system/validate` to admin (or introduce a dedicated `system.validate.read_all` permission held only by admins).
- For non-admin callers that need per-artifact validation, route them through a scoped endpoint that returns only artifacts they already have read access to.

---

## Acceptance Criteria

- Reviewer/contributor role → 403 on `/system/validate`.
- Admin role → unchanged behavior.
- Integration tests cover both.
