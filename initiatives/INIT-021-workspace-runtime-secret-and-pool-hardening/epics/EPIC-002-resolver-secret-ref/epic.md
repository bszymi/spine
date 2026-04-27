---
id: EPIC-002
type: Epic
title: Resolver dereference of secret references
status: Pending
initiative: /initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/initiative.md
  - type: related_to
    target: /architecture/adr/ADR-011-workspace-resolver-secret-ref-dereference.md
---

# EPIC-002 — Resolver dereference of secret references

---

## Purpose

Extend `WorkspaceResolver` so that, in shared mode, it fetches
the workspace binding from the platform's internal API,
dereferences the secret references via `SecretClient`, and
assembles the `WorkspaceConfig` consumed by Spine internals.

The file/env provider path is out of scope for **this** epic.
Migration of the single-workspace resolver to `SecretClient` is
owned by EPIC-001 / TASK-008 and uses a separate code path; it
must not regress here.

---

## Key Work Areas

- New resolver provider: HTTP-platform-binding provider.
- HTTP client to the platform's
  `/api/v1/internal/workspaces/{ws}/runtime-binding` endpoint
  with service-token auth, ETag support, and bounded retries.
- Binding cache with TTL plus explicit `Invalidate` channel.
- `Invalidate` listener — receives a notification from the
  platform after a rotation (ADR-011 specifies the mechanism).
- Resolver dereference: for each secret ref in the binding, call
  `SecretClient.Get`, build the `WorkspaceConfig` value.
- Errors from binding API or secret store surface as the
  workspace's health state, not as a process panic.
- ADR-011 written and accepted.

---

## Primary Outputs

- New resolver provider in `internal/workspace/`
- HTTP client + cache + invalidation listener
- `architecture/adr/ADR-011-workspace-resolver-secret-ref-dereference.md`
- Updates to `architecture/components.md` describing the
  resolver's new code path

---

## Acceptance Criteria

- Spine can serve a workspace whose binding is fetched from the
  platform and whose credentials are fetched from the secret
  store (verified end-to-end in dev and in staging).
- `Invalidate` from the platform observably causes a binding
  re-fetch and a credential re-resolution.
- Stale-binding TTL is documented in ADR-011 and enforced.
- The single-workspace resolver path is not regressed by this
  epic. Its migration to `SecretClient` lands in TASK-008 (EPIC-001)
  and is verified there.
