---
id: TASK-006
type: Task
title: Per-workspace pool sizing and configuration
status: Completed
epic: /initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/epics/EPIC-003-pool-policy/epic.md
initiative: /initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/epics/EPIC-003-pool-policy/epic.md
  - type: blocked_by
    target: /initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/epics/EPIC-002-resolver-secret-ref/tasks/TASK-004-resolver-dereference-secret-refs.md
---

# TASK-006 — Per-workspace pool sizing and configuration

---

## Purpose

Provide a per-workspace connection pool with sensible defaults
and a knob to override per workspace.

The pool sits behind PgBouncer (per `smp:ADR-013`), so Spine's
pool size budget is per-workspace at the PgBouncer pool level,
not the raw RDS connection level.

## Deliverable

- Pool implementation per workspace (likely `pgxpool`).
- Default pool size and overflow behaviour decided in ADR-012.
  Initial proposal: `min=2, max=10` per workspace, configurable.
- Per-workspace override via the binding metadata or via Spine
  config, decided in ADR-012.
- Saturation behaviour: bounded queue with fail-fast on
  overflow, returning a structured `PoolSaturated` error to the
  caller (request will surface 503 to the API caller).
- Metrics: `spine_workspace_pool_size`, `_in_use`, `_idle`,
  `_saturation_total` per workspace label.

## Acceptance Criteria

- Pool created lazily on first request to a workspace.
- Defaults match ADR-012.
- Saturation behaviour observable in metrics and surfaces to
  caller as documented.
- Metrics labeled by workspace ID and visible on
  `/metrics`.
