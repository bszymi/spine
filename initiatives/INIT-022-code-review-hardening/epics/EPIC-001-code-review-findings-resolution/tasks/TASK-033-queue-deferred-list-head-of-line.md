---
id: TASK-033
type: Task
title: "Avoid head-of-line blocking in internal/queue deferred list"
status: Pending
acceptance: Pending
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: refactor
created: 2026-05-12
last_updated: 2026-05-12
links:
  - type: parent
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
  - type: related_to
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/tasks/TASK-011-bound-queue-dispatch-goroutines.md
---

# TASK-033 — Avoid head-of-line blocking in internal/queue deferred list

---

## Purpose

`internal/queue/memory.go:216-225` (the redispatch loop landed in
TASK-011) processes a bounded prefix of `q.deferred`
(`maxRedispatchPerTick = 256`) per tick and re-parks orphans at the
tail. When the head of the deferred list is a noisy missing-subscriber
type, an entry whose handler was just registered but which sits
beyond the head 256 entries waits roughly `len(backlog)/256 × 50ms`
before it is even inspected — handler is ready but delivery is
artificially delayed. This is the exact boot-race window TASK-011
intended to preserve.

This is a P2 fairness finding from the 2026-05-12 codex review of the
INIT-022 batch (commit `59d37277ee`).

## Deliverable

Pick the shape with the smallest delta that retains TASK-011's
goroutine bound and zero-drop guarantee:

- Bucket `q.deferred` by entry type and walk only buckets whose type
  currently has handlers (preferred — directly addresses the cause).
- Or: when registering a new handler, eagerly scan `q.deferred` for
  matching entries and dispatch them immediately, leaving the tick
  loop to handle steady-state.
- Or: split the per-tick budget proportionally across types so a
  single noisy type cannot exhaust the entire prefix.

## Acceptance Criteria

- New test: enqueue N entries of type A (no handler), then register a
  handler for type B and enqueue one entry of B; assert delivery of
  the B entry within one tick interval regardless of N.
- Existing `TestNoHandlerGoroutineBounded` and
  `TestNoHandlerDoesNotBlockOtherTypes` continue to pass.
- Goroutine count remains bounded; no per-entry goroutine reintroduced.

## Out of Scope

- Replacing the in-memory queue with a durable implementation.
- Changing the queue's external API (publish / subscribe / shape of
  re-queue) — internal restructure only.
