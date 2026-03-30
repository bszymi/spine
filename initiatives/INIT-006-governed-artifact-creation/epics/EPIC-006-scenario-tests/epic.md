---
id: EPIC-006
type: Epic
title: Scenario Tests
status: Draft
initiative: /initiatives/INIT-006-governed-artifact-creation/initiative.md
owner: bszymi
created: 2026-03-30
last_updated: 2026-03-30
links:
  - type: parent
    target: /initiatives/INIT-006-governed-artifact-creation/initiative.md
---

# EPIC-006 — Scenario Tests

---

## 1. Purpose

Validate the full planning run lifecycle through end-to-end scenario tests.

These tests exercise the complete stack — Git, database, engine, projections — to ensure planning runs work correctly from API call to branch merge.

---

## 2. Scope

### In Scope

- `StartPlanningRun` scenario test helper step
- Initiative creation golden path scenario
- Initiative with child artifacts (epics, tasks on same branch) scenario
- Planning run rejection and rework scenario
- Planning run cancellation scenario
- Assertions for branch state, artifact presence on main, projection sync

### Out of Scope

- Unit tests (covered in EPIC-002, EPIC-003, EPIC-004)
- Performance testing

---

## 3. Success Criteria

1. Golden path: plan → draft → review → approve → artifacts on main
2. Child artifacts: initiative + epics created on branch → all merged
3. Rejection: review rejects → loops to draft → approve on retry
4. Cancellation: cancel → artifacts absent from main, branch cleaned up
5. All scenarios use the existing harness and step builder patterns

---

## 4. Key Files

- `internal/scenariotest/engine/workflow_steps.go` — new step helper
- `internal/scenariotest/scenarios/planning_run_test.go` — new test file

---

## 5. Dependencies

- EPIC-002, EPIC-003, EPIC-004 — full stack must be implemented
- EPIC-005 — initiative-lifecycle workflow must exist
