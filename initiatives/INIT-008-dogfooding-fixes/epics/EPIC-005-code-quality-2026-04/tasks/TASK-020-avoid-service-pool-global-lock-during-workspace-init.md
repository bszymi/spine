---
id: TASK-020
type: Task
title: Avoid holding the service-pool global lock during workspace initialization
status: Pending
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: refactor
created: 2026-04-24
last_updated: 2026-04-24
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/epic.md
---

# TASK-020 — Avoid holding the service-pool global lock during workspace initialization

---

## Purpose

`ServicePool.Get` holds the pool-wide mutex while resolving a workspace and building its service set. First request initialization can open database pools, configure Git, load repo config, and construct projection services. A slow or broken workspace can therefore block unrelated workspaces from acquiring or releasing service sets.

## Deliverable

- Change `ServicePool.Get` so slow initialization happens outside the global mutex.
- Preserve single-initialization semantics for concurrent first requests for the same workspace, using a per-workspace initializing entry or `singleflight`-style coordination.
- Ensure failures unblock waiters and do not leave poisoned cache entries.
- Keep `Evict`, `Release`, `Close`, and idle eviction semantics intact.

## Acceptance Criteria

- Concurrent first requests for different workspaces can initialize independently.
- Concurrent first requests for the same workspace produce one service set or one shared initialization error.
- A failed initialization does not prevent a later retry.
- Tests cover concurrent same-workspace initialization, concurrent different-workspace initialization, release during eviction, and close during/after initialization.
