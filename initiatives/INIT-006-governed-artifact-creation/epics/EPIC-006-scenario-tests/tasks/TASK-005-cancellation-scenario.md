---
id: TASK-005
type: Task
title: "Scenario: Planning run cancellation"
status: Draft
epic: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-006-scenario-tests/epic.md
initiative: /initiatives/INIT-006-governed-artifact-creation/initiative.md
work_type: implementation
created: 2026-03-30
last_updated: 2026-03-30
links:
  - type: parent
    target: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-006-scenario-tests/epic.md
---

# TASK-005 — Scenario: Planning Run Cancellation

---

## Purpose

Validate that cancelling a planning run does not affect the main branch and cleans up the run branch.

---

## Deliverable

`internal/scenariotest/scenarios/planning_run_test.go`

Scenario steps:
1. Start planning run with initiative content
2. Create an epic on the branch via write_context
3. Cancel the run
4. Assert run status is `cancelled`
5. Assert initiative does NOT exist on main
6. Assert epic does NOT exist on main
7. Assert the run branch is cleaned up (deleted)

---

## Acceptance Criteria

- Cancelled planning runs leave no trace on main
- Branch is deleted after cancellation
- Run status correctly transitions to cancelled
