---
id: TASK-005
type: Task
title: Binding cache and invalidation listener
status: Completed
epic: /initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/epics/EPIC-002-resolver-secret-ref/epic.md
initiative: /initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/epics/EPIC-002-resolver-secret-ref/epic.md
  - type: blocked_by
    target: /initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/epics/EPIC-002-resolver-secret-ref/tasks/TASK-004-resolver-dereference-secret-refs.md
---

# TASK-005 — Binding cache and invalidation listener

---

## Purpose

Add the binding cache plus an invalidation channel so the
platform can tell Spine "this workspace's binding has changed,
drop your cache" after a rotation or migration.

## Deliverable

- In-process cache of bindings keyed by workspace ID, with TTL.
  TTL default decided in ADR-011; configurable. The cache holds
  the binding (refs, URL, mode, IDs) — **never** the resolved
  secret values.
- ETag-aware re-fetch — `If-None-Match` on the platform call.
  `304` short-circuits the **binding refetch only**: the cached
  refs are reused, but `SecretClient.Get` still runs whenever a
  `WorkspaceConfig` must be assembled (cold pool, new pool after
  idle eviction, TTL refresh). Skipping `SecretClient.Get` on
  `304` would defeat the TTL safety net for in-place secret
  rotations under the same ref.
- Invalidation channel:
  - **Pull** model — a webhook endpoint on Spine the platform
    calls; or
  - **Push** model — a long-poll the platform answers when
    invalidated.
  ADR-011 picks one. Recommend the platform-calls-Spine webhook
  for simplicity.
- On invalidation: drop the binding cache entry, drop the
  associated connection pool (TASK-007), force re-resolution
  on next request.

## Acceptance Criteria

- TTL is enforced.
- Webhook (or chosen mechanism) authenticated by the same
  service token Spine uses to read bindings.
- Invalidation observably causes pool close + re-resolution
  in staging.
- Other workspaces' bindings and pools are unaffected.
