---
id: TASK-016
type: Task
title: Cascade applyCommitStatus to all branch-added artifacts
status: Pending
created: 2026-04-20
last_updated: 2026-04-20
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/epic.md
---

# TASK-016 — Cascade applyCommitStatus to all branch-added artifacts

---

## Purpose

`applyCommitStatus` in `internal/engine/merge.go:358` rewrites the
frontmatter status on `run.TaskPath` only — the primary artifact for
the run. When a planning run uses `POST /artifacts/add` (or direct file
writes) to add child artifacts on the branch, those children are merged
to main with whatever status they were authored with (typically `Draft`).
That's wrong: main should not carry unreviewed Drafts, and the operator
review on the parent has no effect on them.

The design decision (2026-04-20, SMP dogfooding session) is that the
parent artifact's review approves the whole planned package: one review
= cascade Draft→Pending for every artifact added on the branch. This
task implements that cascade on the Spine engine side. The matching SMP
workflow change (a `verify-children` gate step) is tracked in
`SMP TASK-008` under `epic-050-workflow-configuration-improvements`.

## Deliverable

- Extend `applyCommitStatus` to enumerate every artifact file added on
  the run branch vs main (reuse `DiscoverChanges` from
  `internal/artifact/discovery.go`) and rewrite the status on each.
- Perform the rewrites atomically within one commit on the branch, so
  either all land or none do.
- Preserve current single-artifact behavior when the branch has no other
  added files.
- Log each cascade with `run_id`, `artifact_path`, and `new_status`.

## Acceptance Criteria

- `applyCommitStatus` walks all branch-added artifacts and rewrites
  frontmatter status on each.
- Existing unit tests for `applyCommitStatus` continue to pass.
- New unit test: run with parent epic + 2 child tasks cascades all
  three files to `Pending` in a single commit on the branch.
- Scenario test covers the cascade path and the unchanged
  single-artifact path.
- Behaviour is no-op when the target status already matches (as today).

## Key Files

- `internal/engine/merge.go` (`applyCommitStatus`, ~line 358)
- `internal/artifact/discovery.go` (`DiscoverChanges`)
- `internal/engine/merge_test.go`
- Scenario test suite under `internal/scenariotest/`

## Out of Scope

- The `verify-children` workflow step (SMP-side).
- Client-side guards for failed `POST /artifacts/add` calls.
- Any change to precondition types in `internal/engine/step.go`.
