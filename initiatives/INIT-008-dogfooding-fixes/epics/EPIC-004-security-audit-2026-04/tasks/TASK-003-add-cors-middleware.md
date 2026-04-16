---
id: TASK-003
type: Task
title: "Add deny-by-default CORS middleware to gateway"
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

# TASK-003 — Add Deny-By-Default CORS Middleware

---

## Purpose

`internal/gateway/routes.go` does not set any CORS headers. Browsers still enforce SOP, but because the server has no policy, any future relaxation or proxy misconfiguration would allow cross-origin credentialed requests. Establish an explicit deny-by-default baseline.

---

## Deliverable

- Add a CORS middleware keyed off a new `SPINE_CORS_ALLOWED_ORIGINS` list (default empty → deny).
- Reject non-preflight requests with a cross-origin `Origin` header not in the allowlist.
- Never set `Access-Control-Allow-Credentials: true` for wildcard origins.
- Include in the gateway middleware stack before auth.

---

## Acceptance Criteria

- With no env set, a request carrying `Origin: https://evil.example` is rejected.
- Listed origins pass with correct `Vary: Origin` and explicit `Access-Control-Allow-Origin` echo.
- Existing CLI/API tests unaffected (they send no `Origin` header).
