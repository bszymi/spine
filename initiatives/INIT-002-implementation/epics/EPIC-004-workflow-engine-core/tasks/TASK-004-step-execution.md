---
id: TASK-004
type: Task
title: Step Execution State Machine
status: Pending
epic: /initiatives/INIT-002-implementation/epics/EPIC-004-workflow-engine-core/epic.md
initiative: /initiatives/INIT-002-implementation/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-002-implementation/epics/EPIC-004-workflow-engine-core/epic.md
---

# TASK-004 — Step Execution State Machine

## Purpose

Implement the StepExecution state machine with retry, timeout, and failure classification.

## Deliverable

- StepExecution state machine: waiting → assigned → in_progress → blocked/completed/failed/skipped
- Retry behavior (new execution record per attempt)
- Failure classification (transient, permanent, actor_unavailable, invalid_result, git_conflict, timeout)
- Timeout detection and timeout_outcome application
- Result validation (outcome must be in expected_outcomes)
- Idempotent result submission

## Acceptance Criteria

- Every valid transition from Engine State Machine §3.2 works correctly
- Every invalid transition from §3.4 is rejected
- Retry creates new StepExecution; preserves failed record
- Failure classification determines retry eligibility
- Timeout detection works via scheduler scan
- Duplicate result submissions are handled idempotently
- Unit tests verify every transition and retry scenario
