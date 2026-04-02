---
id: TASK-005
type: Task
title: Fix planning run scenario tests stuck in committing status
status: Completed
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-001-scheduler-and-runtime/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-001-scheduler-and-runtime/epic.md
  - type: related_to
    target: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-006-scenario-tests/epic.md
---

# TASK-005 — Fix planning run scenario tests stuck in committing status

---

## Purpose

Four planning run scenario tests are failing because runs get stuck in `committing` status instead of reaching `completed`. The tests expect the run to auto-merge after the final step completes, but the merge fails because test repos have no remote configured.

Failing tests:
- `TestPlanningRun_InitiativeCreationGoldenPath`
- `TestPlanningRun_InitiativeWithChildArtifacts`
- `TestPlanningRun_RejectionAndRework`
- `TestPlanningRun_TaskCreation`

All fail at `assert-run-status-completed` with `got "committing", want "completed"`.

## Deliverable

Fix the scenario test harness or the planning run merge flow so that:
- Either the test harness provides a local "remote" (bare repo) for merge operations
- Or the merge step handles the no-remote case gracefully in tests
- Or the auto-push failure doesn't prevent the run from completing (merge to local main should succeed even without a remote)

## Acceptance Criteria

- All 4 planning run scenario tests pass
- The fix does not mask real merge failures in production
- Other scenario tests continue to pass (72 currently passing)
