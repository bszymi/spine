---
id: TASK-003
type: Task
title: Tests for branch-scoped validation and mixed creation paths
status: Pending
epic: /initiatives/INIT-011-artifact-creation-entry-point/epics/EPIC-003-branch-scoped-validation/epic.md
initiative: /initiatives/INIT-011-artifact-creation-entry-point/initiative.md
work_type: testing
created: 2026-04-08
last_updated: 2026-04-08
links:
  - type: parent
    target: /initiatives/INIT-011-artifact-creation-entry-point/epics/EPIC-003-branch-scoped-validation/epic.md
  - type: blocked_by
    target: /initiatives/INIT-011-artifact-creation-entry-point/epics/EPIC-003-branch-scoped-validation/tasks/TASK-002-update-artifact-creation-workflow.md
---

# TASK-003 — Tests for Branch-Scoped Validation and Mixed Creation Paths

---

## Purpose

Test that branch-scoped validation correctly discovers and validates artifacts regardless of how they were created (API or direct file write).

---

## Deliverable

### Unit tests

`internal/engine/branch_validation_test.go` (or appropriate location)

1. **Discovery finds all artifacts**: branch has 4 artifacts (1 epic + 3 tasks), discovery returns all 4
2. **Individual validation**: each artifact is validated for schema/required fields
3. **Cross-artifact validation**: parent links between branch artifacts resolve correctly
4. **Mixed creation paths**: 2 artifacts via ArtifactWriter, 1 via direct file write — all 3 discovered
5. **Validation failure detail**: when 1 of 4 artifacts is invalid, result includes details for the failing artifact and passes for the other 3
6. **Empty branch**: no new artifacts on branch results in validation error

### Scenario tests

`internal/scenariotest/scenarios/branch_scoped_validation_test.go`

1. **Epic with tasks — API path**:
   - Create epic via `POST /artifacts/create`
   - Add 2 tasks via `POST /artifacts/add`
   - Submit draft step
   - Validation discovers all 3 artifacts and passes
   - Review, approve, merge — all 3 land on main

2. **Epic with tasks — Git-native path**:
   - Create epic via `POST /artifacts/create`
   - Write 2 task files directly to the branch (simulating AI agent)
   - Submit draft step
   - Validation discovers all 3 artifacts and passes

3. **Mixed path**:
   - Create epic via API
   - Add 1 task via API
   - Write 1 task directly to branch
   - Validation discovers all 3

4. **Validation failure and retry**:
   - Create epic + task with invalid front-matter
   - Validation fails, returns to draft
   - Fix the task on the branch
   - Re-submit, validation passes

---

## Acceptance Criteria

- All unit and scenario tests pass
- Tests use existing harness and mock infrastructure
- Git-native path is tested (files written to branch outside of ArtifactWriter)
- No flaky tests
