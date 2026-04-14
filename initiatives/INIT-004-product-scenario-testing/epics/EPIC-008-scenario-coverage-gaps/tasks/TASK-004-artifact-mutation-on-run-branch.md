---
id: TASK-004
type: Task
title: "Artifact mutation during planning run scenarios"
status: Pending
epic: /initiatives/INIT-004-product-scenario-testing/epics/EPIC-008-scenario-coverage-gaps/epic.md
initiative: /initiatives/INIT-004-product-scenario-testing/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-004-product-scenario-testing/epics/EPIC-008-scenario-coverage-gaps/epic.md
---

# TASK-004 — Artifact mutation during planning run scenarios

---

## Purpose

All existing planning run scenarios create new artifacts on the run branch. No scenario tests updating an artifact that already exists on `main` from within a planning run — including updating its front-matter fields, changing link targets, or modifying its body. The write-context and branch-scoped artifact service paths for mutations are untested at the scenario level.

## Deliverable

Scenario tests covering:

- **Update existing artifact on run branch**: artifact exists on main with status `pending`; planning run opens; artifact is updated (e.g. description changed, status updated to `in_progress`) on the run branch; run merges; main reflects the updated content
- **Link target change during run**: artifact on main has a `blocked_by` link; planning run updates the artifact to remove the link; after merge, link is absent from projection
- **Conflict between branch update and main**: artifact is updated on run branch; same artifact is also updated directly on main before the run merges; verify the merge surface the conflict rather than silently dropping one change
- **Read-through to main for unmodified artifacts**: on a run branch, reading an artifact that was not modified on the branch returns the main version (not a 404)

## Acceptance Criteria

- Updated artifact on run branch: post-merge content on main matches the branch version
- Link removal: projection reflects link removal after merge
- Conflict case: returns an error or conflict signal rather than silently overwriting
- Read-through: unmodified artifacts are readable from the run branch context
