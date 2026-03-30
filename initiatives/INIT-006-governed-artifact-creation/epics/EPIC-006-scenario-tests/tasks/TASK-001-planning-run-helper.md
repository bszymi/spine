---
id: TASK-001
type: Task
title: Add StartPlanningRun scenario test helper
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

# TASK-001 — Add StartPlanningRun Scenario Test Helper

---

## Purpose

Add a reusable step builder for starting planning runs in scenario tests, parallel to the existing `StartRun()` helper.

---

## Deliverable

`internal/scenariotest/engine/workflow_steps.go`

Add `StartPlanningRun(artifactPath, artifactContent string) Step` function that:
- Calls the runtime's `StartPlanningRun()` method
- Stores `run_id`, `current_execution_id`, `current_step_id` in scenario state
- Follows the pattern of the existing `StartRun()` step builder

---

## Acceptance Criteria

- Helper follows existing step builder patterns
- Stores state keys consistent with other workflow step helpers
- Can be composed with existing steps (SyncProjections, SubmitStepResult, etc.)
