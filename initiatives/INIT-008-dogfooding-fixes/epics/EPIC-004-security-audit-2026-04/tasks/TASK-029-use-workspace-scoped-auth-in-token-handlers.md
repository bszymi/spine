---
id: TASK-029
type: Task
title: Use workspace-scoped auth service in token handlers
status: Pending
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: bugfix
created: 2026-04-28
last_updated: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/epic.md
---

# TASK-029 — Use workspace-scoped auth service in token handlers

---

## Purpose

Security review finding: `internal/gateway/handlers_tokens.go` uses `s.auth` directly in `handleTokenCreate` and `handleTokenRevoke`. In shared workspace mode, authenticated requests resolve services through the workspace `ServiceSet`; using the server-level auth service can create or revoke tokens in the wrong backing store if a fallback service is wired.

This task closes the tenant-isolation gap by making token lifecycle handlers use the same workspace-scoped service access pattern as the rest of the authenticated gateway.

## Deliverable

- Update `internal/gateway/handlers_tokens.go` so token create and revoke use `s.authFrom(r.Context())` or `needAuth`.
- Keep token list on the workspace-scoped store path.
- Add or adjust gateway tests covering shared-mode token create and revoke with a workspace service set.

## Acceptance Criteria

- `handleTokenCreate` does not reference `s.auth` directly.
- `handleTokenRevoke` does not reference `s.auth` directly.
- Shared-mode tests prove create and revoke operate on the workspace-scoped auth service.
- Existing token management tests still pass.
- `go test ./internal/gateway ./internal/auth` passes.
