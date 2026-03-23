---
id: TASK-003
type: Task
title: Failure Classification and Routing
status: Pending
epic: /initiatives/INIT-003-execution-system/epics/EPIC-007-execution-reliability/epic.md
initiative: /initiatives/INIT-003-execution-system/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-003-execution-system/epics/EPIC-007-execution-reliability/epic.md
---

# TASK-003 — Failure Classification and Routing

## Purpose

Implement failure classification so the engine can distinguish between transient and permanent failures and route them appropriately.

## Deliverable

- Failure classifier that categorizes failures: transient, permanent, actor_unavailable, invalid_result, git_conflict, timeout
- Routing logic: transient → retry, permanent → fail step, actor_unavailable → reassign
- Failure detail recording on step execution
- Classification used by retry logic to determine retry eligibility

## Acceptance Criteria

- All failure types are classified correctly
- Transient failures trigger retry (if retries available)
- Permanent failures skip retry and fail the step immediately
- Actor unavailable triggers reassignment to a different actor
- Git conflicts are classified and reported with detail
- Failure classification is persisted on the step execution record
