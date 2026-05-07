---
id: TASK-011
type: Task
title: "Bound internal/queue dispatch goroutines"
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
