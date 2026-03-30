---
id: TASK-006
type: Task
title: "Scenario: Task creation through planning run"
status: Completed
epic: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-006-scenario-tests/epic.md
initiative: /initiatives/INIT-006-governed-artifact-creation/initiative.md
work_type: implementation
created: 2026-03-30
last_updated: 2026-03-30
links:
  - type: parent
    target: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-006-scenario-tests/epic.md
---

# TASK-006 — Scenario: Task Creation Through Planning Run

---

## Purpose

Validate that the generic `artifact-creation.yaml` workflow works for creating individual tasks — not just initiatives. This proves the workflow is truly type-agnostic.

---

## Deliverable

`internal/scenariotest/scenarios/planning_run_test.go`

Scenario steps:
1. Seed governance artifacts and `artifact-creation.yaml` workflow
2. Seed an existing initiative and epic on main (parent artifacts must exist for task validation)
3. Sync projections
4. Start planning run with a Task artifact (Draft status, referencing the existing epic as parent)
5. Submit "draft" step with `ready_for_review` outcome
6. Assert validate step runs automatically (cross-artifact validation — parent epic exists, links valid)
7. Submit "review" step with `approved` outcome
8. Assert run status is `committing`
9. Execute `MergeRunBranch()`
10. Assert run status is `completed`
11. Assert task artifact exists on main with status `Pending`
12. Sync projections (post-merge)
13. Assert task appears in projection with correct parent link to epic

---

## Acceptance Criteria

- Task creation uses the same `artifact-creation.yaml` workflow as initiative creation
- The workflow `mode: creation` binding resolves correctly for type `Task`
- Cross-artifact validation in the validate step checks parent epic reference
- Task lands on main with `Pending` status after approval
