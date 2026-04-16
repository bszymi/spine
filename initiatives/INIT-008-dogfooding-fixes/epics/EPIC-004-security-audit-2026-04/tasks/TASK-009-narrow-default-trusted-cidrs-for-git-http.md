---
id: TASK-009
type: Task
title: "Narrow default trusted CIDRs for git HTTP endpoint"
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

# TASK-009 — Narrow Default Trusted CIDRs For Git HTTP Endpoint

---

## Purpose

`cmd/spine/main.go:525-529` falls back to all RFC1918 ranges (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`) when `SPINE_GIT_HTTP_TRUSTED_CIDRS` is unset. On a Docker host, any container on any bridge is "trusted" and can clone/fetch repos without the auth token path in `internal/gateway/handlers_git.go:66-73`.

---

## Deliverable

- Change the default to empty (no trusted network) — requires explicit opt-in.
- Log at WARN when the list is empty and the endpoint is mounted, so operators know all clients need tokens.
- Document the recommended CIDR (the specific runner-network subnet) in the README.

---

## Acceptance Criteria

- With no env set, an unauthenticated clone from 172.17.0.5 is rejected.
- With `SPINE_GIT_HTTP_TRUSTED_CIDRS=172.17.0.0/16`, clone from inside that range succeeds.
- Regression test covers both.
