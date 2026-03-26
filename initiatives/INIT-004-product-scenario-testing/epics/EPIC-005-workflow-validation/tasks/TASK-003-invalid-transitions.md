---
id: TASK-003
type: Task
title: "Invalid Workflow Transition Scenarios"
status: Pending
epic: /initiatives/INIT-004-product-scenario-testing/epics/EPIC-005-workflow-validation/epic.md
initiative: /initiatives/INIT-004-product-scenario-testing/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-004-product-scenario-testing/epics/EPIC-005-workflow-validation/epic.md
---

# TASK-003 — Invalid Workflow Transition Scenarios

---

## Purpose

Implement negative scenarios that validate the system rejects invalid workflow transitions: skipping steps, transitioning to non-adjacent steps, and operating on workflows in terminal states.

## Deliverable

Scenario test suite covering:

- Attempting to skip a required step (e.g., execute -> commit without review)
- Attempting to transition to a non-adjacent step
- Attempting to progress a workflow that has already reached a terminal state
- Attempting to start a workflow for a task that already has an active workflow
- Attempting to submit an outcome without required outputs

## Acceptance Criteria

- Each invalid transition attempt is rejected with a clear error
- Workflow state is unchanged after rejected transitions
- Error messages identify the specific transition violation
- All workflow types are covered
