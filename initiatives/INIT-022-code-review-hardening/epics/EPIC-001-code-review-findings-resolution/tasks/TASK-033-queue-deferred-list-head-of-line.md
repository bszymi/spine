---
id: TASK-033
type: Task
title: "Avoid head-of-line blocking in internal/queue deferred list"
status: Completed
acceptance: Approved
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

## Resolution (2026-05-12)

Adopted the first option from the Deliverable list: bucket `q.deferred`
by `EntryType` and walk only handled types. This is the smallest delta
that directly addresses the cause — noisy unhandled types do not even
appear in the redispatch walk, so they can never consume per-tick
budget.

**Files touched**

- `internal/queue/memory.go`
  - `MemoryQueue.deferred` is now `map[string][]Entry` (was `[]Entry`).
    `NewMemoryQueue` initialises the map.
  - `dispatch` parks unhandled entries into the per-type bucket
    (`q.deferred[entry.EntryType] = append(...)`).
  - `redispatchDeferred` snapshots which types currently have handlers
    under a single `mu.RLock`, then iterates that snapshot looking up
    each handled bucket directly. The per-tick entry-dispatch budget
    (`maxRedispatchPerTick = 256`) is shared across handled buckets;
    no-handler buckets are never visited.
  - Comments on `dispatch` and `redispatchDeferred` updated to call
    out the TASK-033 head-of-line fairness contract and the
    "Subscribe-only-appends, never-removes" invariant the snapshot
    relies on.
- `internal/queue/memory_test.go` — `TestDeferredEntryNotBlockedByOtherTypeBacklog`
  added. Publishes N = 2048 entries of `noisy_no_handler` plus one
  `target-001` of `needs_handler`, waits for `q.entries` to drain
  into the deferred map (2 s safety cap on drain, well above any
  plausible scheduling delay), subscribes for `needs_handler`, and
  asserts delivery within 200 ms. The 200 ms timeout sits between
  the pre-fix worst case (≥ 400 ms of rotation ticks for N = 2048)
  and the post-fix expectation (~50 ms, one ticker interval).

**Acceptance criteria satisfied**

- *Enqueue N entries of type A (no handler), then register a handler
  for type B and enqueue one entry of B; assert delivery within one
  tick interval regardless of N.* ✓ — Implemented as
  `TestDeferredEntryNotBlockedByOtherTypeBacklog` (publish-order is
  noisy A's then target B with neither handler registered, then
  subscribe to B once both are parked; this exercises the actual
  deferred-list path the Purpose section describes, not just the
  channel→dispatch shortcut). Bait-check: reverting
  `internal/queue/memory.go` to the flat-slice version fails the new
  test with the exact AC-shaped diagnostic *"target-001 not delivered
  within 200ms after Subscribe — head-of-line blocked behind
  noisy_no_handler bucket"*. Engine restored immediately.
- *Existing `TestNoHandlerGoroutineBounded` and
  `TestNoHandlerDoesNotBlockOtherTypes` continue to pass.* ✓ — Both
  green under `-count=10 -race`.
- *Goroutine count remains bounded; no per-entry goroutine
  reintroduced.* ✓ — Redispatch still runs entirely on the `Start`
  goroutine; the bucketing change only restructures which entries
  it scans, not how many goroutines it spawns. The unchanged
  `TestNoHandlerGoroutineBounded` (publish 200 → goroutine delta ≤ 20)
  remains the pin for this property.

**Codex review iteration**

First-pass codex flagged two P2s — (a) per-tick scan could iterate
unbounded no-handler buckets and acquire RLock per bucket; (b) the
40 ms drain-deadline fatal in the new test was a CI flake risk. Both
addressed in the same commit before review re-ran clean: (a) the
redispatch walk now iterates the *handlers* snapshot, so no-handler
buckets are never visited and `q.handlers` is read once per tick;
(b) the drain wait uses a 2-second safety cap (only fires on a
genuinely stalled dispatcher) and N was raised to 2048 with a 200 ms
threshold so the timing band tolerates a 2× scheduler penalty.

**Test gates**

- `go build ./...` — clean.
- `go vet ./...` — clean.
- `go test ./internal/queue/... -count=10 -race` — green
  (`TestNoHandlerGoroutineBounded`, `TestNoHandlerDoesNotBlockOtherTypes`,
  and the new `TestDeferredEntryNotBlockedByOtherTypeBacklog` all
  pass on every iteration).
- `go test ./internal/queue/... ./internal/engine/... ./internal/delivery/...
  ./internal/scheduler/... ./internal/event/... ./internal/artifact/...
  ./internal/workspace/... ./internal/observe/... ./cmd/spine/...
  -count=3 -race` — green (all downstream consumers of
  `queue.MemoryQueue` cleared on the new field shape).
- `make docker-lint` — 207 issues, identical to the baseline at
  commit `f0c3c4a` (no new findings on touched files).
- `codex review --uncommitted` — clean: *"The deferred queue
  bucketing change preserves the existing dispatch semantics while
  avoiding head-of-line blocking from unrelated unhandled entry
  types. I did not find any discrete regressions in the modified
  code or tests."*
