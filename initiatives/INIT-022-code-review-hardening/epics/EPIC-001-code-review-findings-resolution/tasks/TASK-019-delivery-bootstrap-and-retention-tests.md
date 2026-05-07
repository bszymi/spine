---
id: TASK-019
type: Task
title: "Unit tests for delivery bootstrap and retention"
status: Pending
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: test
created: 2026-05-07
last_updated: 2026-05-07
links:
  - type: parent
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
---

# TASK-019 — Unit tests for delivery bootstrap and retention

---

## Purpose

`internal/delivery/bootstrap.go` and `internal/delivery/retention.go`
have no `_test.go` siblings. `BootstrapInternalSubscription`
(`bootstrap.go:23`) is idempotent on startup — a regression silently
duplicates subscriptions. `StartRetentionCleanup` (`retention.go:13`)
runs an hourly delete loop with a default-7-day fallback (`:18`); a
wrong-direction comparison drops live data.

This is a P2 coverage finding from the 2026-05-07 code review.

## Deliverable

- `internal/delivery/bootstrap_test.go` — table-driven cases:
  - First call creates the subscription row.
  - Second call is a no-op (asserts ListSubscriptions dedupe by name).
  - Mismatched URL or token re-creates / updates per documented
    semantics.
  - Store error is surfaced.
- `internal/delivery/retention_test.go`:
  - Cleanup correctly deletes rows older than the cutoff.
  - Cleanup correctly preserves rows newer than the cutoff
    (regression bait against direction flip).
  - Default 7-day fallback applies when no env var is set.

## Acceptance Criteria

- Tests pass without the `integration` tag (use the existing in-memory
  test store fixture or a minimal stub).
- Coverage for both files reaches ≥85% lines.

## Out of Scope

- Webhook dispatcher tests (separate file, already partially covered).
