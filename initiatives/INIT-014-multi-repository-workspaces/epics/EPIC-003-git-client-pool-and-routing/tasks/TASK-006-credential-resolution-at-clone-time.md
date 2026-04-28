---
id: TASK-006
type: Task
title: Resolve credentials at clone time
status: Pending
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-003-git-client-pool-and-routing/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: implementation
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-003-git-client-pool-and-routing/epic.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-003-git-client-pool-and-routing/tasks/TASK-002-lazy-clone-and-client-initialization.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-001-repository-catalog-and-bindings/tasks/TASK-002-runtime-repository-binding-schema.md
---

# TASK-006 - Resolve Credentials at Clone Time

---

## Purpose

Wire the runtime binding's credential secret reference through to the Git client pool so that lazy clones of private code repositories actually authenticate.

The runtime binding schema (EPIC-001 TASK-002) declares a `credential_ref` field, but no current task delivers the resolution path from that reference to a usable Git credential at clone or fetch time.

## Deliverable

Implement credential resolution inside the `GitClientPool` clone path, reusing the existing secret client abstraction (ADR-010) and per-workspace resolver behavior (ADR-011).

The implementation should:

- Resolve `credential_ref` to a concrete credential through the secret client when initializing or refreshing a code-repo client.
- Inject credentials into clone, fetch, and push operations without persisting them to disk in plaintext.
- Treat a missing or unresolvable credential as a typed `repository-credentials-unavailable` error surfaced to the caller (run start, git HTTP push, etc.).
- Cache resolved credentials only for the lifetime of the client and invalidate on binding update.
- Redact credential values from logs and error messages.

## Acceptance Criteria

- Lazy clone of a private code repo uses the resolved credential without writing it to disk.
- Missing or unresolved credentials produce a typed error identifying the repository ID.
- Credential rotation through binding update is picked up on next access.
- No credential value appears in logs, metrics, or error responses.
- Unit tests cover resolution success, missing secret, expired secret, and binding update.
- Public-repo bindings without a `credential_ref` continue to work unchanged.
