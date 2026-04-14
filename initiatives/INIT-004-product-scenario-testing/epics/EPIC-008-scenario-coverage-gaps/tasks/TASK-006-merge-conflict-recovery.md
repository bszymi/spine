---
id: TASK-006
type: Task
title: "Merge conflict recovery scenarios"
status: Pending
epic: /initiatives/INIT-004-product-scenario-testing/epics/EPIC-008-scenario-coverage-gaps/epic.md
initiative: /initiatives/INIT-004-product-scenario-testing/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-004-product-scenario-testing/epics/EPIC-008-scenario-coverage-gaps/epic.md
---

# TASK-006 — Merge conflict recovery scenarios

---

## Purpose

No scenario tests the case where a run branch and `main` have diverged such that an automatic merge would fail. The recovery path — surface the conflict, leave the run in a `conflict` state, allow manual resolution — is implemented but completely untested at the scenario level.

## Deliverable

Scenario tests covering:

- **Direct edit conflict**: artifact is created on a run branch; the same artifact is also modified on main before the run completes; when the run attempts to merge, a conflict is detected and the run status reflects this (e.g. `merge_conflict` or equivalent)
- **Non-conflicting parallel edit**: two artifacts are modified on main and the run branch respectively (different files); merge succeeds without conflict
- **Conflict surfaced, not silently dropped**: after a conflict is detected, the artifacts on main are unchanged (the branch's version was not silently applied)
- **Re-run after conflict resolution**: once the conflict is manually resolved (main updated to incorporate branch changes), a new run on the same task can start and complete successfully

## Acceptance Criteria

- Conflicting parallel edits result in a detectable conflict state (not a panic or silent overwrite)
- Non-conflicting parallel edits merge cleanly
- Main is unchanged when a merge conflict occurs
- A subsequent clean run on the same task succeeds after resolution
