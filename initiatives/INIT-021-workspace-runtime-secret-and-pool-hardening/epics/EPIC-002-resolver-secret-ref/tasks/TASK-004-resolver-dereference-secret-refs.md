---
id: TASK-004
type: Task
title: Resolver dereference of binding secret refs
status: Completed
epic: /initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/epics/EPIC-002-resolver-secret-ref/epic.md
initiative: /initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/epics/EPIC-002-resolver-secret-ref/epic.md
  - type: blocked_by
    target: /initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/epics/EPIC-001-secret-client/tasks/TASK-001-secret-client-interface.md
---

# TASK-004 — Resolver dereference of binding secret refs

---

## Purpose

Add a new `WorkspaceResolver` provider that consumes bindings
from the platform's internal API and dereferences their secret
references via `SecretClient` to produce a `WorkspaceConfig`.

The existing file/env and database providers stay in place for
single-workspace deployments.

## Deliverable

New provider in `internal/workspace/`:

- HTTP client to
  `GET /api/v1/internal/workspaces/{ws}/runtime-binding`
  (smp internal endpoint).
- Service-token auth from configuration.
- Response type matching the binding shape (Spine API URL,
  Spine workspace ID, deployment mode, secret references).
- For each reference, call `SecretClient.Get`. Combine into a
  `WorkspaceConfig`.
- Errors:
  - Platform binding API down → return cached binding if
    available, else `WorkspaceUnavailable`.
  - Secret store down → `WorkspaceUnavailable`.
  - Access denied → `WorkspaceUnavailable` with structured
    error code.

## Acceptance Criteria

- New provider selectable via configuration
  (`WORKSPACE_RESOLVER=platform-binding`).
- End-to-end test: Spine resolves a workspace via the platform
  binding API + a `SecretClient` provider, opens a connection
  to the workspace DB, and serves an API call.
- Platform binding API outage falls back to the cached binding
  for the duration of the TTL **and** for an additional
  stale-on-error grace window of 30 minutes past TTL
  (configurable; see ADR-011). After the grace window expires,
  resolution fails. New (uncached) workspaces are unresolvable
  during the outage from the start.
- Single-workspace scenario tests stay green via the
  `WORKSPACE_RESOLVER=file` path; this task does not touch
  that resolver (TASK-008 owns it).
