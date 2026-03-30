---
id: TASK-002
type: Task
title: "Scenario: Initiative creation golden path"
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

# TASK-002 — Scenario: Initiative Creation Golden Path

---

## Purpose

Validate the complete planning run lifecycle from start to merge.

---

## Deliverable

`internal/scenariotest/scenarios/planning_run_test.go`

Scenario steps:
1. Seed `initiative-lifecycle.yaml` workflow to repo
2. Sync projections
3. Start planning run with initiative content (Draft status)
4. Submit "draft" step with `ready_for_review` outcome
5. Submit "review" step with `approved` outcome
6. Assert run status is `committing` (the commit outcome triggers merge, not immediate completion)
7. Execute `MergeRunBranch()` (or trigger scheduler merge step)
8. Assert run status is `completed`
9. Assert initiative artifact exists on main
10. Assert initiative status is `In Progress` (set by commit on approval)
11. Sync projections (post-merge)
12. Assert initiative appears in projection with correct status
13. Assert planning branch is cleaned up

Note: The engine moves runs to `committing` when a step outcome has `commit` metadata. The run only transitions to `completed` after `MergeRunBranch()` succeeds. The scenario must include this merge step explicitly.

---

## Acceptance Criteria

- Scenario passes end-to-end
- Uses `harness.NewTestEnvironment()` with `WithRuntimeOrchestrator()`
- Follows existing scenario test patterns
