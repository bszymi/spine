---
id: TASK-030
type: Task
title: Normalize unauthenticated workspace resolution errors
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

# TASK-030 — Normalize unauthenticated workspace resolution errors

---

## Purpose

Security review finding: workspace-scoped API routes resolve `X-Workspace-ID` before bearer-token validation. Missing, inactive, unavailable, and nonexistent workspaces currently produce distinguishable responses before the caller has authenticated, which lets unauthenticated clients enumerate workspace IDs and status.

This task preserves the existing workspace-scoped auth flow while removing pre-auth tenant-state disclosure.

## Deliverable

- Change the workspace/auth middleware flow so unauthenticated requests receive a uniform authentication failure without revealing whether the workspace exists.
- Preserve clear workspace resolution errors for authenticated callers.
- Cover missing, nonexistent, inactive, and unavailable workspace cases in gateway tests.

## Acceptance Criteria

- Unauthenticated requests to workspace-scoped API routes return `401 unauthorized` without workspace existence/status detail.
- Authenticated requests still receive the correct workspace errors (`not_found`, `forbidden`, `unavailable`) when applicable.
- Shared-mode auth still validates tokens against the correct workspace-scoped store.
- Existing workspace routing and auth middleware tests pass.
- `go test ./internal/gateway ./internal/workspace` passes.
