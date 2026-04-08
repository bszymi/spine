---
id: TASK-003
type: Task
title: Scenario tests for artifact creation flow
status: Pending
epic: /initiatives/INIT-011-artifact-creation-entry-point/epics/EPIC-002-create-entry-point/epic.md
initiative: /initiatives/INIT-011-artifact-creation-entry-point/initiative.md
work_type: testing
created: 2026-04-08
last_updated: 2026-04-08
links:
  - type: parent
    target: /initiatives/INIT-011-artifact-creation-entry-point/epics/EPIC-002-create-entry-point/epic.md
  - type: blocked_by
    target: /initiatives/INIT-011-artifact-creation-entry-point/epics/EPIC-002-create-entry-point/tasks/TASK-002-cli-command.md
  - type: blocked_by
    target: /initiatives/INIT-011-artifact-creation-entry-point/epics/EPIC-002-create-entry-point/tasks/TASK-004-artifact-add-endpoint.md
---

# TASK-003 — Scenario Tests for Artifact Creation Flow

---

## Purpose

End-to-end scenario tests that validate the full artifact creation flow from API request to artifact landing on main.

---

## Deliverable

`internal/scenariotest/scenarios/artifact_creation_entry_test.go`

### Scenarios

1. **Golden path — Task creation**
   - Start with an epic that has existing tasks (TASK-001 through TASK-003)
   - Call `POST /artifacts/create` with type=Task, parent=EPIC, title
   - Verify: planning run starts, artifact gets TASK-004, branch is created
   - Progress through the creation workflow steps (draft, validate, review)
   - Verify: artifact appears on main with correct ID, status Pending, parent link

2. **Golden path — Epic creation**
   - Call with type=Epic, parent=Initiative, title
   - Verify: correct EPIC-XXX allocation, directory structure created

3. **Collision scenario**
   - Two concurrent creation requests for tasks in the same epic
   - First one merges successfully with TASK-004
   - Second one detects collision, renumbers to TASK-005, retries and succeeds
   - Verify: both artifacts on main with distinct IDs

4. **Validation errors**
   - Missing parent: returns 404
   - Invalid type: returns 400
   - Empty title: returns 400
   - Parent type mismatch (Task with --initiative instead of --epic): returns 400

5. **First artifact in scope**
   - Create a task in an epic that has no tasks yet
   - Verify: allocates TASK-001

6. **Add artifact to planning run**
   - Create epic via `POST /artifacts/create`
   - Add task via `POST /artifacts/add` with the run ID
   - Verify: task is on the same branch, correctly numbered
   - Add second task
   - Verify: sequential numbering (TASK-001, TASK-002)

7. **Add rejected when run past draft step**
   - Create epic, submit draft step (run advances to validation)
   - Call `POST /artifacts/add` → returns 409

---

## Acceptance Criteria

- All seven scenarios pass
- Tests use the existing scenario test harness (`internal/scenariotest/harness/`)
- Collision test simulates concurrent creation realistically (two planning runs, sequential merge)
- No flaky tests — collision scenario must be deterministic
