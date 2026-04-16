---
id: TASK-014
type: Task
title: "Apply commit.status to artifact file on merge"
status: Completed
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-001-scheduler-and-runtime/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: bugfix
created: 2026-04-11
last_updated: 2026-04-16
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-001-scheduler-and-runtime/epic.md
---

# TASK-014 — Apply commit.status to Artifact File on Merge

---

## Purpose

The artifact-creation workflow declares `commit.status: Pending` on the `approved` outcome, signaling that an approved artifact should transition from Draft to Pending when merged to main. Currently, the engine uses this metadata only to determine that a merge should occur (entering the committing state) but does not rewrite the artifact's frontmatter status in the file before or after merging. The artifact lands on main still containing `status: Draft`.

This causes the management platform to see stale status values when reading artifacts from the repository, since it relies on the file content as the source of truth.

---

## Deliverable

Update the engine's commit/merge flow to rewrite the artifact file's frontmatter `status` field on the branch before merging (or on main immediately after merging) when the terminal outcome includes `commit.status` metadata. The updated file should be committed so that the status change is part of the merge history.

---

## Acceptance Criteria

- When a workflow outcome includes `commit.status: Pending`, the artifact file on main contains `status: Pending` after the merge completes
- The status change is visible in the Git history as a committed change
- The projection sync after merge reflects the updated status
- Existing scenario tests continue to pass
- The scenario test in `artifact_creation_workflow_test.go` (`TestArtifactCreationWorkflow_DraftToPending`) passes with the commented-out assertions uncommented:
  - `AssertFileContains(taskPath, "status: Pending")`
  - `AssertProjection(taskPath, "Status", "Pending")`
- Works for all artifact types that use the creation workflow (Initiative, Epic, Task)
