---
id: TASK-009
type: Task
title: "Fix TOCTOU race on queue idempotency check"
status: Completed
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-001-scheduler-and-runtime/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: bugfix
created: 2026-04-09
last_updated: 2026-04-09
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-001-scheduler-and-runtime/epic.md
---

# TASK-009 — Fix TOCTOU Race on Queue Idempotency Check

---

## Purpose

In `/internal/queue/memory.go` (lines 45-66), the idempotency check and channel write are not atomic. The lock is released between checking `idempotencySet` and writing to the channel. Two goroutines with the same idempotency key can both pass the check before either records the key, violating the deduplication guarantee.

---

## Deliverable

Hold the lock across both the check and the channel send, or use a pending-set pattern to prevent double-publish.

---

## Acceptance Criteria

- Concurrent publishes with the same idempotency key result in exactly one enqueue
- No deadlock when the channel is full (handle blocking appropriately)
- Existing queue tests pass; add a concurrent idempotency test
