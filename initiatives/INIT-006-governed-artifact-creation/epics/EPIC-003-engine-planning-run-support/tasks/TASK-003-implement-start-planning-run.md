---
id: TASK-003
type: Task
title: Implement StartPlanningRun()
status: Draft
epic: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-003-engine-planning-run-support/epic.md
initiative: /initiatives/INIT-006-governed-artifact-creation/initiative.md
work_type: implementation
created: 2026-03-30
last_updated: 2026-03-30
links:
  - type: parent
    target: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-003-engine-planning-run-support/epic.md
---

# TASK-003 — Implement StartPlanningRun()

---

## Purpose

Implement the core method that enables governed artifact creation. This is the central deliverable of INIT-006.

---

## Deliverable

`internal/engine/run.go` — new `StartPlanningRun()` method

The method should:

1. Validate inputs: `artifactWriter` must be configured, `artifactContent` must be non-empty
2. Parse the artifact content using `artifact.Parse()`, then run full validation using `artifact.Validate()` — Parse only checks front-matter presence; Validate checks the full schema (status values, link types, required fields). Both must pass before any branch or run is created. Invalid content is rejected with `ErrInvalidParams` before side effects occur.
3. Resolve the governing workflow from the artifact type using `mode: creation` filter (see EPIC-005 TASK-004)
4. Generate run ID, trace ID, branch name (`spine/run/{runID}`)
5. Create the Git branch from HEAD
6. Set `WriteContext` with the branch name on the context
7. Call `artifactWriter.Create()` to write the artifact to the branch
8. Create the Run record with `Mode = RunModePlanning`
9. Create the entry StepExecution
10. Activate the run and entry step

The method must NOT modify `StartRun()`. It is a separate, parallel code path.

---

## Acceptance Criteria

- Artifact is created on branch, not on main
- Run record has `Mode = "planning"` and correct `TaskPath`
- Entry step is created and activated
- Error cases: missing artifact_writer returns `ErrUnavailable`, invalid content returns `ErrInvalidParams`, missing workflow returns `ErrNotFound`
- Branch cleanup on failure (if branch was created but run creation fails)
