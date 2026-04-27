---
id: ADR-011
type: ADR
title: WorkspaceResolver dereferences secret references via the platform binding API
status: Accepted
date: 2026-04-27
decision_makers: bszymi
links:
  - type: related_to
    target: /initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/initiative.md
  - type: related_to
    target: /architecture/adr/ADR-010-secret-client-abstraction.md
  - type: related_to
    target: /initiatives/INIT-009-workspace-runtime/initiative.md
---

# ADR-011 — WorkspaceResolver dereferences secret references via the platform binding API

---

## Context

`INIT-009-workspace-runtime` shipped two `WorkspaceResolver`
providers: file/env (single workspace) and database (multi
workspace). Both load connection strings inline from their
configured source.

The platform now owns the **Workspace Runtime Binding** as a
control-plane concept (`smp:ADR-012`). Each binding contains a
Spine API URL, Spine workspace ID, deployment mode, and
**secret references** to runtime DB credentials, projection DB
credentials, and Git credentials. The platform exposes the
binding via an internal HTTP endpoint:

```
GET /api/v1/internal/workspaces/{ws}/runtime-binding
```

For shared-mode multi-workspace deployments to work, Spine's
resolver must:

1. Fetch the binding for a workspace ID from the platform.
2. Dereference each secret reference via `SecretClient` (ADR-010).
3. Assemble the `WorkspaceConfig` consumed by Spine internals.
4. Cache the result with TTL and an explicit invalidation
   channel.

Two questions need decisions:

- Where the binding lives — Spine's own DB (replicated from the
  platform) or a live HTTP fetch from the platform?
- The invalidation mechanism — TTL only, push from platform, or
  pull/long-poll?

---

## Decision

Spine adds a new resolver provider:
**`platform-binding` provider**.

It works as follows:

- On `Resolve(workspaceID)`, the provider checks the in-process
  binding cache.
- On cache miss or stale entry, it issues
  `GET /api/v1/internal/workspaces/{workspaceID}/runtime-binding`
  to the platform with a service token, ETag-aware
  (`If-None-Match`).
- The response carries: Spine API URL, Spine workspace ID,
  deployment mode, and three secret references.
- For each secret reference, the provider calls
  `SecretClient.Get` (ADR-010) and assembles the
  `WorkspaceConfig`.
- The binding is cached with a TTL of **5 minutes** by default
  (configurable). The cache holds the binding shape only —
  refs, URL, mode, IDs. Secret **values** are not cached; they
  live transiently in the connection pool's open connections
  (TASK-006).
- A `304` from the platform short-circuits the binding refetch
  (refs reused from cache) but **does not** short-circuit the
  per-ref `SecretClient.Get` that runs whenever a
  `WorkspaceConfig` is assembled (cold pool, new pool after
  idle eviction, TTL refresh). This preserves the TTL safety
  net for in-place rotations under the same ref when a webhook
  is missed or delayed.

**Invalidation mechanism:** **platform-pushes-to-Spine webhook.**

- Spine exposes an authenticated webhook endpoint:
  `POST /internal/v1/workspaces/{ws}/binding-invalidate`.
- The platform calls this after a binding mutation or
  rotation.
- Spine drops the binding cache entry and the associated
  connection pool (TASK-007), forcing re-resolution on next
  request.

The webhook auth uses the same shared service-token credential
as the binding fetch direction.

**Provider selection.** The file/env and database providers
from INIT-009 remain. Single-workspace deployments select the
file/env provider; multi-workspace AWS deployments select the
new `platform-binding` provider. Selection is configured by
`WORKSPACE_RESOLVER` (`file` | `db` | `platform-binding`).
This replaces the legacy `SPINE_WORKSPACE_MODE`
(`single`/`shared`) knob; deployments must update their
configuration. No automatic migration is provided — Spine fails
fast on startup if `SPINE_WORKSPACE_MODE` is set without
`WORKSPACE_RESOLVER`, with a clear error pointing to this ADR.

**Stale-on-error.** When the binding TTL expires and the next
platform fetch fails (network error, 5xx, timeout) for a
*previously-cached* workspace, Spine serves the stale binding
for up to **30 minutes** past TTL (configurable). During this
grace window, `SecretClient.Get` still runs on every Resolve so
in-place rotations propagate (TASK-005). After the grace window,
resolution fails and the workspace's pool is dropped on the next
invalidation trigger. Uncached workspaces fail immediately on
platform outage — there is nothing to serve stale.

**Source of truth:** the platform owns the binding
(`smp:ADR-012`). Spine never writes to the binding — it only
reads via the internal API and reacts to invalidations.

---

## Consequences

### Positive

- Single ownership of bindings (the platform), consistent with
  the control-plane / execution-plane split (Constitution).
- No replication or sync state to manage between platform and
  Spine for bindings.
- Webhook invalidation is operationally simple and observable.
- Stale-binding TTL is bounded so that even without webhook
  delivery, a rotation eventually takes effect.
- Cached bindings let Spine ride out short platform outages.
  The stale-on-error grace window (30 min past TTL by default)
  makes this concrete: previously-cached workspaces keep
  serving traffic even if a platform outage spans the TTL
  boundary.

### Negative

- A platform binding API outage prevents resolution of
  *new* (uncached) workspaces during the outage. Cached
  workspaces are bounded by the stale-on-error grace window;
  past that window, they also fail.
- Breaking config change: `SPINE_WORKSPACE_MODE` is replaced by
  `WORKSPACE_RESOLVER`. Deployments must be updated; there is
  no automatic compatibility shim.
- Webhook delivery can fail or be delayed; TTL is the safety
  net.
- HTTP call on cold path adds latency. ETag and cache mitigate
  this on the warm path.
- Spine must expose an authenticated webhook surface — a new
  attack surface to defend.

### Neutral

- The cache default of 5 minutes is a starting point. It can
  be adjusted based on operational observation; the tradeoff
  is rotation responsiveness vs platform load.

---

## Alternatives Considered

### Replicate bindings into Spine's database

Spine subscribes to changes and maintains a local copy of the
binding table. **Rejected** because it duplicates control-plane
state in the execution plane, which is exactly the boundary the
constitution avoids. Also: replication adds operational
complexity disproportionate to the latency saved.

### TTL-only (no webhook)

Skip the webhook entirely; rely on cache TTL for rotation to
propagate. **Rejected** because a 5-minute window of stale
credentials after a security-driven rotation is too long.
Webhook narrows this to seconds.

### Long-poll from Spine to platform

Spine holds a long-poll for invalidations. **Rejected** as a
default because long-polls fan out per Spine instance per
workspace; webhook is simpler and lower-overhead. A long-poll
variant could be added later if firewall/network constraints
ever block the webhook direction.

### Pull from secret store directly, no platform involvement

Spine reads the binding shape from the secret store as a JSON
blob, skipping the platform. **Rejected** because binding
structure is queryable control-plane state and JSON blobs in
a secret store are the wrong substrate. See `smp:ADR-012`
discussion of the same alternative.

---

## Links

- Initiative: `/initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/initiative.md`
- Spine companion: ADR-010, ADR-012
- Platform companion: `smp:/architecture/adrs/ADR-012-workspace-runtime-binding-model.md`
- Cross-repo overview: `smp:/architecture/runtime-binding-overview.md`
