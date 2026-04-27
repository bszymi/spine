---
id: ADR-012
type: ADR
title: Per-workspace connection-pool policy — sizing, eviction, invalidation
status: Proposed
date: 2026-04-27
decision_makers: bszymi
links:
  - type: related_to
    target: /initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/initiative.md
  - type: related_to
    target: /architecture/adr/ADR-011-workspace-resolver-secret-ref-dereference.md
  - type: related_to
    target: /initiatives/INIT-009-workspace-runtime/initiative.md
---

# ADR-012 — Per-workspace connection-pool policy — sizing, eviction, invalidation

---

## Context

`INIT-009-workspace-runtime` §6 listed connection-pool pressure
as an open risk: "Many workspaces in one process means many
database connection pools. Need sensible pool sizing and idle
eviction." The work was deferred. INIT-021 picks it up.

Two operational realities frame the decision:

- The platform's chosen RDS topology (`smp:ADR-013`) is
  topology (b): one RDS instance, DB-per-workspace,
  user-per-workspace, with PgBouncer in front. Spine and the
  platform share the global RDS connection budget via
  PgBouncer's per-workspace pools. Spine's per-workspace pool
  size therefore competes with the PgBouncer per-pool limit,
  not with a raw RDS limit.
- A shared Spine instance can host ~10–50 workspaces
  (`smp:deployment-model.md` §3.4). With even modest defaults,
  the per-instance connection footprint adds up quickly.

This ADR commits to defaults, behaviour under saturation, and
invalidation triggers.

---

## Decision

**Pool defaults.** Each workspace gets its own pool with:

- `min = 2`
- `max = 10`
- `acquire_timeout = 5s`
- `health_check_period = 30s`

These are configurable globally via Spine config and overridable
per workspace via the binding metadata (a future binding field;
omitted means inherit from global).

**Lazy creation.** The pool for a workspace is opened on the
first request that needs it, not at process start. This keeps
the cold footprint of a Spine instance hosting many workspaces
modest.

**Idle eviction.** If a workspace receives zero requests for
**10 minutes** (configurable), its pool is closed and its
connections returned to PgBouncer. The next request reopens
the pool.

**Invalidation triggers.** A pool is closed and dropped when:

- The platform calls Spine's binding-invalidate webhook for
  that workspace (ADR-011).
- The pool reports persistent connection errors above a
  threshold (signaling rotated credentials or RDS migration).
- The binding TTL elapses *and* the next re-fetch returns a
  changed binding.
- A subsequent `SecretClient.Get` for any of the workspace's
  resolved refs returns a `VersionID` that differs from the
  one the live pool was opened with. This closes the
  rotation-under-the-same-ref path: an in-place secret rotation
  with a missed webhook produces a `304` on the binding, but
  the resolver still calls `SecretClient.Get` (TASK-005), the
  new `VersionID` is observed, and the pool is rebuilt against
  the new credential without waiting for idle eviction or
  connection errors.

**Saturation behaviour.** When a pool is at `max` and a request
needs a connection:

- The request waits in a bounded per-workspace queue
  (`queue_size = 50` default).
- If the queue is full, the request fails fast with
  `PoolSaturated`. The API caller receives 503 with a clear
  body; the metric `spine_workspace_pool_saturation_total`
  increments.

This blast-radius bound is critical: a single noisy workspace
must not exhaust a Spine instance's overall request capacity.
ADR-011's per-workspace cache and pool drop guarantee that
saturation in workspace A does not block requests to B.

**Observability.** Per-workspace metrics on `/metrics`:

- `spine_workspace_pool_size{workspace_id="..."}`
- `spine_workspace_pool_in_use{workspace_id="..."}`
- `spine_workspace_pool_idle{workspace_id="..."}`
- `spine_workspace_pool_saturation_total{workspace_id="..."}`
- `spine_workspace_pool_open_total{workspace_id="..."}`
- `spine_workspace_pool_close_total{workspace_id="...",reason="..."}`

---

## Consequences

### Positive

- Predictable per-workspace footprint with a clear upper bound.
- Idle eviction keeps a many-workspace instance lean.
- Saturation behaviour is bounded and observable.
- Per-workspace overrides allow heavy workspaces to be tuned
  without affecting others.
- Invalidation triggers cover the three real causes of stale
  pools (rotation, persistent errors, binding change).

### Negative

- The defaults will be wrong for at least some real workloads.
  Operators will need to tune. Mitigated by per-workspace
  overrides and metrics.
- Lazy creation adds one-request latency on cold path. Usually
  irrelevant.
- Bounded-queue fail-fast means some users will see 503 under
  spikes. Better than head-of-line blocking across workspaces.

### Neutral

- The 10-minute idle timeout and the 5-minute binding TTL are
  starting points, not load-bearing constants. Both are
  configurable.

---

## Alternatives Considered

### Single shared pool across workspaces

Replace per-workspace pools with one shared pool that
multiplexes by workspace. **Rejected** because it gives up
the per-workspace blast-radius bound, complicates per-workspace
credential rotation (you cannot drop "the workspace's
connections" without affecting others), and conflicts with
INIT-009's true-resource-isolation design decision.

### Eager pool creation at startup

Open a pool per known workspace at process start.
**Rejected** because it scales footprint with workspace count
even for workspaces that get no traffic, and because cold-pool
failures would prevent process start.

### Unbounded queue on saturation

Let requests queue indefinitely. **Rejected** because it turns
a per-workspace problem into a per-instance memory problem and
hides the failure from the caller.

### TTL-only invalidation

Drop the webhook and rely on TTL plus connection-error
heuristics. **Rejected** because it widens the rotation window;
the webhook is cheap and observably correct.

---

## Links

- Initiative: `/initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/initiative.md`
- Spine companion ADRs: ADR-010, ADR-011
- Platform topology: `smp:/architecture/adrs/ADR-013-multi-workspace-rds-topology.md`
- Cross-repo overview: `smp:/architecture/runtime-binding-overview.md`
