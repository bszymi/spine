---
id: TASK-011
type: Task
title: "Bound internal/queue dispatch goroutines"
status: Completed
acceptance: Approved
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: bugfix
created: 2026-05-07
last_updated: 2026-05-08
links:
  - type: parent
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
---

# TASK-011 — Bound internal/queue dispatch goroutines

---

## Purpose

`internal/queue/memory.go:152`'s `dispatch()` launches an unbounded
goroutine per re-queue when no handlers are registered. A noisy
publisher with no subscriber yet (boot-race window) can spawn
arbitrarily many sleepers, all racing to write back to `q.entries`.

This is a P2 concurrency finding from the 2026-05-07 code review.

## Deliverable

Replace the unbounded goroutine spawn with one of:

- A small worker pool fed by a buffered channel; back-pressure on the
  channel.
- A single dispatcher loop that uses `select` with `ctx.Done()` and a
  re-queue channel — no new goroutines per re-queue.

Whichever shape fits the existing tests with the smallest delta.

## Acceptance Criteria

- `internal/queue/memory_test.go::TestConcurrentPublish` and
  `TestConcurrentIdempotency` continue to pass.
- A new test publishes N items to a queue with no registered handlers
  and asserts the goroutine count stays bounded.
- The boot-race window (queue accepts publishes before the first
  handler registers) is preserved as documented behavior — items are
  retained, not dropped.

## Out of Scope

- Replacing the in-memory queue with a different implementation.
- Changing the queue's external API.

## Resolution (2026-05-08)

Replaced the per-requeue goroutine spawn with a deferred-list pattern
owned by `Start()`'s single dispatcher goroutine. When `dispatch()`
finds no handler for an entry's type it appends the entry to
`q.deferred` (a private slice owned by the Start goroutine). A new
50ms `redispatchTicker` fires alongside the existing `evictTicker` and
processes a bounded prefix of `q.deferred` (`maxRedispatchPerTick =
256`) per tick — entries whose type still has no handler are
re-parked, the rest are delivered.

Files:

- `internal/queue/memory.go` — added `deferredRedispatchInterval` and
  `maxRedispatchPerTick` constants, `MemoryQueue.deferred` field,
  `redispatchDeferred()` method; rewrote `dispatch()` no-handler path;
  added the ticker case to `Start()`.
- `internal/queue/memory_test.go` — added
  `TestNoHandlerGoroutineBounded` (publishes 200 entries to a queue
  with no handler and asserts goroutine delta stays under 20) and
  `TestNoHandlerDoesNotBlockOtherTypes` (asserts handled entries
  dispatch promptly while a large no-handler backlog sits on the
  deferred list).

Memory tradeoff: `q.deferred` is intentionally unbounded — capping it
would either drop items (forbidden by the AC's boot-race retention
clause) or block head-of-line for unrelated entry types. Pre-fix held
the same items in unbounded goroutines (≈8 KB stack each), so
post-fix is strictly better on memory cost. Documented inline in
`dispatch()`'s comment. Per ADR-005 the in-process queue is not
durable; misconfigured callers without handlers are an operator bug,
not a steady-state condition.

Test gates:

- `go test ./internal/queue/... -race`: 16/16 pass (including the two
  new tests).
- Full unit suite: green except the pre-existing
  `internal/secrets/TestFileClient_VersionChangesOnEdit` flake
  (verified present on `main`; tracked separately by TASK-026).
- `golangci-lint run ./...`: 206 issues — same baseline as TASK-010.
- `gosec ./...`: 88 issues — same baseline as TASK-010.
- `codex review --uncommitted`: two consecutive clean passes after
  five iteration rounds (each prior pass surfaced a real concern that
  was addressed inline; final design holds with no actionable
  findings).
