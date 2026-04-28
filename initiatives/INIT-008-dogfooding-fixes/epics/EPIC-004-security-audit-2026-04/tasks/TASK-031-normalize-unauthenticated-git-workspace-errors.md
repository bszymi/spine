---
id: TASK-031
type: Task
title: Normalize unauthenticated Git workspace errors
status: Completed
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: bugfix
created: 2026-04-28
last_updated: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/epic.md
---

# TASK-031 — Normalize unauthenticated Git workspace errors

---

## Purpose

Security review finding: `/git/{workspace_id}/...` resolves the workspace and returns distinct not-found, inactive, and unavailable responses before bearer-token validation. Clients outside trusted CIDRs can use the Git smart HTTP endpoint to enumerate workspace IDs and status.

This task removes unauthenticated workspace disclosure from the Git endpoint while preserving trusted-CIDR clone/fetch behavior and authenticated push enforcement.

## Deliverable

- Update `internal/gateway/handlers_git.go` so untrusted, unauthenticated Git requests do not receive workspace existence or status details.
- Preserve trusted-CIDR read-only clone/fetch behavior.
- Preserve mandatory bearer auth for push when receive-pack is enabled.
- Add tests for unauthenticated untrusted Git requests against missing, inactive, and unavailable workspaces.

## Acceptance Criteria

- Untrusted unauthenticated Git requests receive a uniform auth failure before tenant-state details are disclosed.
- Trusted-CIDR read-only clone/fetch still works according to the configured policy.
- Push requests still require bearer auth when receive-pack is enabled.
- Authenticated Git requests still receive useful workspace resolution errors.
- `go test ./internal/gateway ./internal/githttp` passes.
