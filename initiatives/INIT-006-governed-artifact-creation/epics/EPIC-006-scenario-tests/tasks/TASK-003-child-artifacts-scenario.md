---
id: TASK-003
type: Task
title: "Scenario: Initiative with child artifacts"
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

# TASK-003 — Scenario: Initiative with Child Artifacts

---

## Purpose

Validate that multiple artifacts (initiative + epics + tasks) created on a planning run branch all merge to main on approval.

---

## Deliverable

`internal/scenariotest/scenarios/planning_run_test.go`

Scenario steps:
1. Seed initiative-lifecycle workflow
2. Sync projections
3. Start planning run with initiative content
4. Create 2 epic artifacts on the run branch via write_context
5. Create a task artifact under one epic via write_context
6. Submit draft step (ready_for_review)
7. Submit review step (approved)
8. Assert run status is `committing`
9. Execute `MergeRunBranch()`
10. Assert run status is `completed`
11. Assert initiative, both epics, and the task all exist on main
12. Sync projections (post-merge)
13. Assert artifact links and parent references are correct in projection

---

## Acceptance Criteria

- All child artifacts appear on main after merge
- Projection correctly indexes all artifacts post-merge
- Artifact links (parent references) are valid
