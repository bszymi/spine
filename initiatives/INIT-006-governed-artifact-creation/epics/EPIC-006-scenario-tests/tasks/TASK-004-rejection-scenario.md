---
id: TASK-004
type: Task
title: "Scenario: Planning run rejection and rework"
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

# TASK-004 — Scenario: Planning Run Rejection and Rework

---

## Purpose

Validate that review rejection loops the planning run back to the draft step, and approval on retry succeeds.

---

## Deliverable

`internal/scenariotest/scenarios/planning_run_test.go`

Scenario steps:
1. Start planning run
2. Submit draft step (ready_for_review)
3. Submit review step with `needs_revision` outcome
4. Assert run loops back to draft step
5. Submit draft step again (ready_for_review)
6. Submit review step with `approved` outcome
7. Assert run status is `committing`
8. Execute `MergeRunBranch()`
9. Assert run status is `completed` and artifacts on main

---

## Acceptance Criteria

- Rejection does not affect main branch
- Rework loop creates new step executions correctly
- Final approval merges all accumulated changes
