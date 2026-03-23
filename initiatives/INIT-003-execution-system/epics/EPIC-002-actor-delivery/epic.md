---
id: EPIC-002
type: Epic
title: Actor Delivery
status: Pending
initiative: /initiatives/INIT-003-execution-system/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-003-execution-system/initiative.md
---

# EPIC-002 — Actor Delivery

---

## Purpose

Enable actual execution by implementing the delivery pipeline from engine to actors and back. Without actors receiving assignments and returning results, workflows cannot execute.

Currently, step assignment messages are published to the queue but nothing consumes them. This epic closes that gap.

---

## Key Work Areas

- Queue consumer framework for step_assigned messages
- Mock actor provider for development and testing
- Result ingestion and validation pipeline
- Assignment tracking and delivery status
- Polling endpoint for actors to check pending work

---

## Primary Outputs

- `internal/engine/consumer.go` — Queue consumer for step assignments
- `internal/actor/mock_provider.go` — Mock actor for testing
- Result submission flow wired into orchestrator
- Assignment status tracking in store

---

## Acceptance Criteria

- Step assignments are consumed from queue and routed to actors
- Mock actor provider can receive assignment and return result
- Results are validated against step requirements
- Valid results feed back into the orchestrator for step completion
- Invalid results trigger appropriate failure handling
- Assignment delivery status is tracked and queryable
