---
id: EPIC-003
type: Epic
title: "Engine: Planning Run Support"
status: Draft
initiative: /initiatives/INIT-006-governed-artifact-creation/initiative.md
owner: bszymi
created: 2026-03-30
last_updated: 2026-03-30
links:
  - type: parent
    target: /initiatives/INIT-006-governed-artifact-creation/initiative.md
---

# EPIC-003 — Engine: Planning Run Support

---

## 1. Purpose

Implement the core engine changes that enable planning runs — creating artifacts on branches and executing workflows against branch-local artifacts.

This is the heart of the feature. A new `StartPlanningRun()` method handles the creation-mode lifecycle while leaving `StartRun()` completely untouched.

---

## 2. Scope

### In Scope

- `ArtifactWriter` interface in `engine/interfaces.go`
- `WithArtifactWriter()` setter on orchestrator
- `StartPlanningRun()` method in `engine/run.go`
- `resolveReadRef()` helper for branch-aware precondition evaluation
- Precondition updates in `engine/step.go` to read from branch for planning runs
- Unit tests for all new and modified methods

### Out of Scope

- API/gateway routing (EPIC-004)
- Workflow definitions (EPIC-005)
- Scenario tests (EPIC-006)

---

## 3. Success Criteria

1. `StartPlanningRun()` creates artifact on branch, starts run with `Mode=planning`
2. Precondition evaluation reads from run branch for planning runs
3. Existing `StartRun()` is unmodified and all existing tests pass
4. Error cases handled: invalid content, missing workflow, branch failure

---

## 4. Key Files

- `internal/engine/interfaces.go`
- `internal/engine/orchestrator.go`
- `internal/engine/run.go`
- `internal/engine/step.go`
- `internal/engine/run_test.go`
- `internal/engine/step_test.go`

---

## 5. Dependencies

- EPIC-002 (Domain Model & Storage) — `RunMode` must exist
