---
id: TASK-010
type: Task
title: "Enforce operator token minimum length at startup"
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

# TASK-010 — Enforce Operator Token Minimum Length At Startup

---

## Purpose

`internal/gateway/handlers_workspaces.go:23-24` only logs a warning for `SPINE_OPERATOR_TOKEN` shorter than 32 characters. Combined with per-IP rate limiting (bypassable via distributed sources), a 16-char token has ~95 bits of entropy and is feasibly brute-forceable.

---

## Deliverable

- Validate the operator token at server startup in `cmd/spine/main.go`.
- Refuse to start if the token is < 32 characters when operator-scoped endpoints are mounted.
- Keep the runtime warning for defense-in-depth but make it a no-op given the startup gate.

---

## Acceptance Criteria

- `spine serve` with a 16-char `SPINE_OPERATOR_TOKEN` exits with a clear error.
- Unit test covers the length check.
- Docs updated to specify the minimum.
