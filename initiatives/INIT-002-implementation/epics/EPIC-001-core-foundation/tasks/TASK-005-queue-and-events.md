---
id: TASK-005
type: Task
title: Queue and Event Router
status: Completed
epic: /initiatives/INIT-002-implementation/epics/EPIC-001-core-foundation/epic.md
initiative: /initiatives/INIT-002-implementation/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-002-implementation/epics/EPIC-001-core-foundation/epic.md
---

# TASK-005 — Queue and Event Router

## Purpose

Implement the in-process queue and event router that components use for async communication.

## Deliverable

- `internal/queue/queue.go` — Queue interface (per Implementation Guide §3.2)
- `internal/queue/memory.go` — In-process implementation using Go channels
- `internal/event/router.go` — EventRouter interface (per Implementation Guide §3.3)
- `internal/event/router_impl.go` — Implementation backed by Queue
- Event types matching Event Schemas document
- Idempotency key support on queue entries

## Acceptance Criteria

- Queue publish/subscribe works with multiple entry types
- EventRouter emits and delivers events to registered handlers
- Concurrent producers and consumers work correctly
- Queue is not durable (documented, tested — restart loses state)
- Unit tests cover publish, subscribe, acknowledge, and concurrent access
- Idempotency keys prevent duplicate processing
