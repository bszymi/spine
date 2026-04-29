---
id: TASK-002
type: Task
title: Implement code-repo-first merge order
status: Completed
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-005-merge-outcomes-and-recovery/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: implementation
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-005-merge-outcomes-and-recovery/epic.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-005-merge-outcomes-and-recovery/tasks/TASK-001-per-repository-merge-outcome-model.md
---

# TASK-002 - Implement Code-Repo-First Merge Order

---

## Purpose

Ensure the primary Spine repo records the real outcome of implementation repository merges.

## Deliverable

Update merge orchestration so affected code repositories merge before the primary repo.

Rules:

- Merge each affected code repo independently.
- Record each outcome immediately after the merge attempt.
- Merge the primary repo last only after outcome data is ready.
- Do not roll back code repo merges if a later repo fails.

## Acceptance Criteria

- Code repo outcomes are available before the primary repo ledger update.
- A failed code repo does not undo a successful code repo merge.
- Primary repo merge records success or partial failure.
- Tests pin merge ordering.

