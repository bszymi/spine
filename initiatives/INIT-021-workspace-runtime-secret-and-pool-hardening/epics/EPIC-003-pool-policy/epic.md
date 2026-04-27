---
id: EPIC-003
type: Epic
title: Per-workspace pool and cache policy
status: Draft
initiative: /initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/initiative.md
  - type: related_to
    target: /architecture/adr/ADR-012-per-workspace-connection-pool-policy.md
---

# EPIC-003 — Per-workspace pool and cache policy

---

## Purpose

INIT-009 §6 listed connection-pool pressure as an open risk but
did not commit to concrete policy. This epic decides:

- Default pool size per workspace.
- Per-workspace override mechanism.
- Idle eviction policy (when does a quiet workspace's pool get
  closed).
- Invalidation triggers (binding invalidate, repeated
  connection errors, pool stale).
- Saturation behaviour (per-workspace queueing or fail-fast).

The work is informed by the topology decision in
`smp:ADR-013-multi-workspace-rds-topology` — Spine and the
platform share a single RDS connection budget via PgBouncer.

---

## Key Work Areas

- Pool sizing defaults and configuration knobs.
- Idle eviction with documented timeout.
- `Invalidate` consumer that closes the pool and forces
  re-resolution on the next request.
- Saturation behaviour (decision: per-workspace queueing with
  bounded length and fail-fast on overflow — confirmed in
  ADR-012).
- Observability: per-workspace pool size, in-use, idle,
  saturation counters.
- ADR-012 written and accepted.

---

## Primary Outputs

- Pool implementation (likely `pgxpool` wrapper) per workspace
- Eviction & invalidation code paths
- `architecture/adr/ADR-012-per-workspace-connection-pool-policy.md`
- Observability metrics under existing Spine telemetry

---

## Acceptance Criteria

- Default pool size is documented and configurable.
- Idle eviction observably closes pools after the documented
  timeout.
- A platform `Invalidate` notification closes the affected
  workspace's pool within one round-trip and does not affect
  other workspaces' pools.
- A load-injection test pins one workspace at saturation; other
  workspaces remain healthy.
- Per-workspace pool metrics are present in Spine's
  `/metrics` endpoint.
