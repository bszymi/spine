---
id: TASK-008
type: Task
title: "Scenario: cancel from partially-merged"
status: Pending
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: test
created: 2026-05-07
last_updated: 2026-05-07
links:
  - type: parent
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
  - type: blocked_by
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/tasks/TASK-005-harness-with-code-repos-helper.md
---

# TASK-008 — Scenario: cancel from partially-merged

---

## Purpose

The cancel exit from `partially-merged` is asymmetric: succeeded-repo
branches are deleted by `CleanupRunBranch`, failed-repo branches are
preserved for operator inspection. This contract is documented in
`architecture/multi-repository-integration.md §4.5` and
`architecture/error-handling-and-recovery.md §5.4` but has **no
scenario guard**.

This is a P1 coverage finding from the 2026-05-07 code review.

## Deliverable

A scenario (new file or new `Test*` in TASK-006's file) that:

1. Reaches `partially-merged` with at least one merged repo and one
   failed repo.
2. Calls `run.cancel`.
3. Asserts:
   - The run lands at `cancelled`.
   - The merged repo's branch is **deleted** (not present on the local
     git tree under that repo).
   - The failed repo's branch is **preserved** (still present on the
     local git tree under that repo).
   - The primary Spine repo's run branch follows the contract
     documented in §4.5 (whatever it says — read the doc, encode the
     assertion).

## Acceptance Criteria

- Scenario passes deterministically.
- Asymmetric cleanup is asserted explicitly per repo (not "all gone"
  / "none gone").
- Removing the failed-branch-preservation conditional from
  `CleanupRunBranch` makes the scenario fail deterministically.

## Out of Scope

- The retry exit — TASK-006.
- The external-resolution exit — TASK-007.
- Run-timeout cancellation — TASK-016.
