---
id: TASK-002
type: Task
title: Planning run should auto-merge branch on approval
status: Pending
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-001-scheduler-and-runtime/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: bugfix
created: 2026-03-31
last_updated: 2026-03-31
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-001-scheduler-and-runtime/epic.md
---

# TASK-002 — Planning Run Should Auto-Merge Branch on Approval

---

## Purpose

When a planning run's review step is approved, the run completes but the branch is not merged to main. The artifacts remain on the branch and must be merged manually. This defeats the purpose of the governed creation workflow.

Found during dogfooding: INIT-001 for the Spine Management Platform was approved through the `artifact-creation` workflow, but the 16 artifacts stayed on the branch. Manual `git merge` was required.

---

## Deliverable

The approval outcome in `artifact-creation.yaml` has `commit: status: Pending`, which should trigger the `committing` state and `MergeRunBranch()`. Investigate why this path is not working for planning runs and fix it.

Likely causes:

1. The `commit` outcome handling in the engine may not trigger the merge for planning runs (it may only update the artifact status on the branch, not trigger the `committing` → `MergeRunBranch()` flow)
2. The `completeAfterMerge` path may not be reached because the run goes directly to `completed` instead of `committing`
3. The step outcome processing may skip the commit effect for planning runs

### Investigation steps

- Trace the code path from `SubmitStepResult()` with an `approved` outcome that has `commit: status: Pending`
- Check if `CompleteRun()` correctly transitions to `committing` when the outcome has a commit effect
- Check if `MergeRunBranch()` is called (or scheduled) after the `committing` transition
- Verify the branch is merged to the authoritative branch and then cleaned up

---

## Acceptance Criteria

- Planning run approval merges the branch to main automatically
- All artifacts from the branch appear on main after approval
- Branch is cleaned up after successful merge
- Existing standard run merge behavior is unchanged
- Scenario test validates the full flow: plan → draft → validate → review → approve → artifacts on main
