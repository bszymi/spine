---
id: TASK-004
type: Task
title: "Add scenario tests for blocking, claiming, release, and execution queries"
status: Completed
completed: 2026-04-06
epic: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-004-execution-projections/epic.md
initiative: /initiatives/INIT-010-actor-skill-execution/initiative.md
work_type: implementation
created: 2026-04-06
last_updated: 2026-04-06
links:
  - type: parent
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-004-execution-projections/epic.md
---

# TASK-004 — Add Scenario Tests for Blocking, Claiming, Release, and Execution Queries

---

## Purpose

EPIC-001 has scenario tests for the skill system, but EPIC-002/003/004 have none.
The following features lack end-to-end scenario test coverage:

- Dependency blocking: StartRun rejected for blocked task, StartRun succeeds after blocker completes
- Task claiming: actor claims a waiting step via ClaimStep, concurrent claim conflict
- Task release: actor releases assignment, step returns to waiting
- Execution projections: /execution/tasks/* endpoints return correct data after sync
- Execution candidates: /execution/candidates filters by skills, actor type, blocking status

---

## Deliverable

1. Create `internal/scenariotest/scenarios/blocking_test.go`:
   - Task A blocked_by Task B — StartRun for A fails
   - Complete Task B — StartRun for A succeeds
   - task_unblocked event emitted

2. Create `internal/scenariotest/scenarios/claiming_test.go`:
   - Start a run, claim the waiting step, verify assignment
   - Claim already-assigned step — conflict error
   - Release claimed step — returns to waiting

3. Extend `internal/scenariotest/scenarios/skill_system_test.go`:
   - Execution projection is populated after sync
   - /execution/tasks/ready returns unblocked tasks
   - /execution/tasks/blocked returns blocked tasks with blocker details

4. Add helper steps to the scenario engine:
   - `ClaimStep(actorID, executionID)` step builder
   - `ReleaseStep(actorID, assignmentID, reason)` step builder
   - `AssertBlocked(taskPath)` / `AssertNotBlocked(taskPath)` assertion steps

---

## Acceptance Criteria

- Blocking enforcement tested end-to-end with real database
- Claim/release lifecycle tested with assignment verification
- Execution projections verified via query endpoints
- All scenario tests pass with `-tags scenario`
