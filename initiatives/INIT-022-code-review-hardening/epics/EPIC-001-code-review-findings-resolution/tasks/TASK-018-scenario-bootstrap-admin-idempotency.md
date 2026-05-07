---
id: TASK-018
type: Task
title: "Scenario: BootstrapInternalAdmin idempotency"
status: Pending
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: test
created: 2026-05-07
last_updated: 2026-05-07
links:
  - type: parent
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
  - type: related_to
    target: /initiatives/INIT-020-dogfooding-fixes-round-2/epics/EPIC-002-platform-binding-bootstrap/tasks/TASK-001-bootstrap-internal-admin.md
---

# TASK-018 — Scenario: BootstrapInternalAdmin idempotency

---

## Purpose

`auth.BootstrapInternalAdmin` (shipped in INIT-020/EPIC-002/TASK-001)
is the platform's first-touch path under
`WORKSPACE_RESOLVER=platform-binding`. Has unit tests but no scenario
coverage. Idempotency on re-resolve and behavior under env-var rotation
are unguarded.

This is a P2 coverage finding from the 2026-05-07 code review.

## Deliverable

A scenario in `internal/scenariotest/scenarios/`:

1. Configure a platform-binding workspace with `SMP_ADMIN_TOKEN=foo`.
2. Drive a first request through; assert `auth.actors` and
   `auth.tokens` rows for `smp-admin` exist with the expected hash.
3. Trigger workspace re-resolve (idle eviction → re-load) and drive a
   second request.
4. Assert: rows are NOT duplicated; the same token still authenticates;
   the no-op DEBUG log line appears.
5. Rotate the env var to `bar`, re-resolve, drive a third request.
6. Assert: a new row is inserted for `bar`'s hash; the new bearer
   authenticates immediately. The OLD row remains and the OLD bearer
   **also continues to authenticate** — this matches the documented
   v0.x contract where rotation cleanup is out of scope (see
   `internal/auth/bootstrap.go:46-47` and INIT-020/EPIC-002/TASK-001's
   "Token rotation cleanup" out-of-scope note). The scenario locks
   that contract in place; if a future task adds rotation cleanup,
   this scenario will need an explicit update.

## Acceptance Criteria

- Scenario passes deterministically.
- Each of the three states is asserted with row counts + auth
  outcomes.
- Removing the `ON CONFLICT (token_hash) DO UPDATE` clause makes the
  scenario fail at step 4.
- The dual-bearer state at step 6 is asserted explicitly (both old
  and new bearers return 200) so a future rotation-cleanup change is
  forced to update this scenario rather than silently passing.

## Out of Scope

- BootstrapInternalSubscription scenario (separate path, separate
  task if warranted).
- Rotation-cleanup of stale rows. If/when that lands, this scenario's
  step 6 expectation flips and a new test case asserts the old bearer
  fails. Until then, the dual-bearer state IS the contract.
