---
id: TASK-002
type: Task
title: Mock Actor Provider
status: Pending
epic: /initiatives/INIT-003-execution-system/epics/EPIC-002-actor-delivery/epic.md
initiative: /initiatives/INIT-003-execution-system/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-003-execution-system/epics/EPIC-002-actor-delivery/epic.md
---

# TASK-002 — Mock Actor Provider

## Purpose

Implement a mock actor provider that can receive step assignments and return configurable results. This enables end-to-end testing without external dependencies.

## Deliverable

- `internal/actor/mock_provider.go` — Mock implementation of AIProvider interface
- Configurable responses: success, failure, timeout, partial result
- Deterministic behavior for testing (no randomness unless configured)
- Usable in both unit tests and integration tests

## Acceptance Criteria

- Mock provider satisfies the AIProvider interface
- Can be configured to return specific outcomes per step
- Supports success, failure, and timeout scenarios
- Returns results in the expected format (required_outputs populated)
- Works with the queue consumer for end-to-end testing
