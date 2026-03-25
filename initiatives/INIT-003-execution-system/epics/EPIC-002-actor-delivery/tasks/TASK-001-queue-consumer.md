---
id: TASK-001
type: Task
title: Queue Consumer Framework
status: Completed
epic: /initiatives/INIT-003-execution-system/epics/EPIC-002-actor-delivery/epic.md
initiative: /initiatives/INIT-003-execution-system/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-003-execution-system/epics/EPIC-002-actor-delivery/epic.md
---

# TASK-001 — Queue Consumer Framework

## Purpose

Implement the consumer side of the message queue so that step assignment messages are consumed, routed to the appropriate actor type, and delivery status is tracked.

## Deliverable

- `internal/engine/consumer.go` — Queue consumer that subscribes to step_assigned events
- Routing logic: determine actor type and dispatch to appropriate delivery mechanism
- Consumer lifecycle: start, stop, graceful shutdown
- Integration with the engine orchestrator for result callbacks

## Acceptance Criteria

- Consumer subscribes to and receives step_assigned messages from the queue
- Messages are routed based on actor type (human, ai_agent, automated_system)
- Consumer runs as a background goroutine within the server lifecycle
- Graceful shutdown drains in-flight assignments
- Failed deliveries are logged with error detail
