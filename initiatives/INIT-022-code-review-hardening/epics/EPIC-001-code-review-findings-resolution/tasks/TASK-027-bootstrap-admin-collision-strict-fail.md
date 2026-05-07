---
id: TASK-027
type: Task
title: "Strict-startup error for bootstrap-admin hash collision"
status: Pending
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: bugfix
created: 2026-05-07
last_updated: 2026-05-07
links:
  - type: parent
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
---

# TASK-027 — Strict-startup error for bootstrap-admin hash collision

---

## Purpose

`internal/auth/bootstrap.go:97-114`: when a bearer hash collides with
an actor that is **not** `smp-admin`, bootstrap silently logs and
returns nil. The platform's bearer is then unable to authenticate, but
the warning is the only signal — under sampled logging, operators
won't notice.

This is a P3 hardening finding from the 2026-05-07 code review.

## Deliverable

- In `BootstrapInternalAdmin`, return an error on the
  non-`smp-admin` hash-collision branch.
- In `cmd/spine` (the workspace-load wireup), surface the error per
  the existing `SPINE_ENV=production` strict-startup philosophy:
  - In `production`: fail workspace load loudly.
  - Outside production: keep the warn-and-continue behavior for dev
    convenience, OR also fail — pick whichever matches the existing
    `BootstrapInternalSubscription` shape.

## Acceptance Criteria

- A new unit test seeds a non-`smp-admin` actor with a token whose
  hash matches `SMP_ADMIN_TOKEN`, calls `BootstrapInternalAdmin`,
  asserts the documented error class is returned.
- An end-to-end check (or a focused workspace-load test) confirms the
  workspace fails to come up under `SPINE_ENV=production`.

## Out of Scope

- Re-architecting bootstrap to support per-workspace bearers
  (separate epic-out-of-scope item per INIT-020/EPIC-002).
