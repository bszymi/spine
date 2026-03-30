---
id: TASK-004
type: Task
title: Branch-aware precondition evaluation
status: Completed
epic: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-003-engine-planning-run-support/epic.md
initiative: /initiatives/INIT-006-governed-artifact-creation/initiative.md
work_type: implementation
created: 2026-03-30
last_updated: 2026-03-30
links:
  - type: parent
    target: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-003-engine-planning-run-support/epic.md
---

# TASK-004 — Branch-Aware Precondition Evaluation

---

## Purpose

Update precondition evaluation to read artifacts from the run branch instead of HEAD when the run is a planning run.

Currently, precondition checks like `artifact_status` and `field_value` read from HEAD. For planning runs, the artifact only exists on the branch, so preconditions would fail.

---

## Deliverable

`internal/engine/step.go`

Add:
- `resolveReadRef(ctx, run) string` helper that returns the run's branch name for planning runs, or `"HEAD"` for standard runs
- Update `checkArtifactStatus()`, `checkFieldValue()`, and `checkLinksExist()` to use `resolveReadRef()` instead of hardcoded `"HEAD"`

---

## Acceptance Criteria

- Planning run preconditions read from the run branch
- Standard run preconditions continue to read from HEAD (no behavior change)
- The helper is a single function, not scattered conditionals
