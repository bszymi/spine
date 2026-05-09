---
id: TASK-019
type: Task
title: "Unit tests for delivery bootstrap and retention"
status: Completed
acceptance: Approved
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: test
created: 2026-05-07
last_updated: 2026-05-09
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

## Resolution (2026-05-09)

**Files**

- `internal/delivery/bootstrap_test.go` (NEW, 382 LOC) — six top-level
  tests + a 6-case table for drift detection.
- `internal/delivery/retention_test.go` (NEW, 367 LOC) — eight tests
  covering the cutoff direction, default fallback, store error path,
  log-suppression on zero-deleted, explicit-retention preservation,
  and pre-cancelled context.
- `internal/delivery/retention.go` (MODIFIED, +21/-7) — extracted
  `runRetentionPass` so the per-tick logic is unit-testable without
  driving the hardcoded 1h ticker.

**Bootstrap test shape**

- `TestBootstrapInternalSubscription_FirstCallCreatesRow` — asserts
  Create is called once with the canonical row shape (Name, TargetURL,
  SigningSecret, WorkspaceID, Status, TargetType, CreatedBy,
  EventTypes, non-empty SubscriptionID, bootstrap-source Metadata).
- `TestBootstrapInternalSubscription_SecondCallIsNoOp` — runs
  Bootstrap twice; second call must not Create or Update; subs slice
  has exactly one row (the no-duplicate AC).
- `TestBootstrapInternalSubscription_DriftTriggersUpdate` — table
  with six cases: url, token, workspace, status, event-types-shorter,
  event-types-different-order. Each case mutates exactly one field
  and asserts Update repairs that field while leaving SubscriptionID
  intact (regression bait against insert-instead-of-update).
- `*_ListErrorSurfaced` / `*_CreateErrorSurfaced` /
  `*_UpdateErrorSurfaced` — pin error wrap-through.
- `TestStringSlicesEqual` — covers the small private helper used by
  the EventTypes drift comparison (length, order, empty-vs-nil).

**Retention test shape**

- `runRetentionPass` is the unit-testable helper extracted from
  `StartRetentionCleanup`. Tests on it:
  - `_DeletesUsingPastCutoff` — asserts `before` lands in
    `[start-retention, end-retention]` (slack window for execution
    time).
  - `_DirectionFlipBait` — pins `before < callTime` and `gap ≈
    retention ± 1s`. A regression that wrote `Add(retention)` would
    push `before` into the future (live data wipe) and fail this
    test. **This is the AC's "regression bait against direction
    flip".**
  - `_StoreErrorLogged` — captures slog; asserts ERROR + "retention
    cleanup failed" + wrapped error attr; no completion line on the
    error path.
  - `_ZeroDeletedSuppressesCompletionLog` — asserts the steady-state
    log volume contract (no completion line when 0 rows deleted).
  - `_PositiveDeletedLogsCompletion` — asserts `deleted=42` attr on
    the completion line.
- `StartRetentionCleanup` (the loop wrapper) is harder to drive
  without a long sleep (1h ticker). Tests focus on what's reachable
  without firing a tick:
  - `_DefaultFallback` — retention=0 → startup log carries
    `retention=168h0m0s`; ctx cancel returns within 2s; no tick fires.
  - `_NegativeFallback` — retention=-5h falls back identically.
    Catches a regression that wrote `if retention == 0` instead of
    `<= 0`.
  - `_ExplicitRetentionPreserved` — retention=3h → startup log
    carries `retention=3h0m0s` (catches a regression that always
    overwrites with `defaultRetention`).
  - `_ContextCancelReturns` — pre-cancelled ctx returns immediately;
    DeleteExpiredDeliveries is never called.
- `TestDefaultRetentionIsSevenDays` — pins the constant.

**Layering choice**

Bootstrap takes `store.SubscriptionStore` (TASK-010 narrow interface,
6 methods). Retention takes `store.DeliveryStore` (12 methods).
fakes implement both interfaces directly, with unused methods
panicking so a regression that starts calling them is loud not silent
— the same pattern subscriber_test.go uses for `minimalStore`.

**Refactor decision (in scope)**

`StartRetentionCleanup`'s for-select loop is structurally hostile to
unit testing: the ticker interval is hardcoded to 1h, so a tick-driven
test would either sleep or require channel injection. Extracting
`runRetentionPass(ctx, st, retention, log)` is the smallest change
that opens the per-tick logic to direct testing — the parent function
becomes a thin wrapper whose only branch is `<-ctx.Done()` vs
`<-ticker.C`. Comment on the helper documents the
direction-invariant (`before = now - retention`) so the load-bearing
sign is visible at the helper's site, not just inferred from the
test name. No call-site changes in cmd_serve.go — public function
signature is unchanged.

**Coverage**

`go tool cover -func` after the changes:

- `bootstrap.go`: BootstrapInternalSubscription 97.0%,
  stringSlicesEqual 100% — file effectively at 97-100%.
- `retention.go`: StartRetentionCleanup 90.0%, runRetentionPass 100%
  — file at 90-100%.

Both files clear the AC's ≥85% threshold.

**Test gates**

- `go test ./internal/delivery/... -count=1 -race` — green.
- `go test ./...` — green except a pre-existing flake in
  `internal/secrets/file_test.go::TestFileClient_VersionChangesOnEdit`
  (the very second-resolution-mtime issue TASK-026 covers; verified
  on main with the same failure pattern).
- `make docker-lint` — 206 baseline unchanged (3 QF1006 staticcheck
  findings introduced by the initial test draft were resolved by
  extracting `waitForLogContains`).
- `gofmt -l` clean on all three files.
- Codex review pass 1 clean: "no discrete bugs introduced."
