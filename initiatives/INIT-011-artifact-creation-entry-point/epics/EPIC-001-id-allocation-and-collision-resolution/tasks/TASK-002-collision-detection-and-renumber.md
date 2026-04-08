---
id: TASK-002
type: Task
title: Collision detection and renumber at merge time
status: Draft
epic: /initiatives/INIT-011-artifact-creation-entry-point/epics/EPIC-001-id-allocation-and-collision-resolution/epic.md
initiative: /initiatives/INIT-011-artifact-creation-entry-point/initiative.md
work_type: implementation
created: 2026-04-08
last_updated: 2026-04-08
links:
  - type: parent
    target: /initiatives/INIT-011-artifact-creation-entry-point/epics/EPIC-001-id-allocation-and-collision-resolution/epic.md
  - type: blocked_by
    target: /initiatives/INIT-011-artifact-creation-entry-point/epics/EPIC-001-id-allocation-and-collision-resolution/tasks/TASK-001-next-id-scanner.md
---

# TASK-002 — Collision Detection and Renumber at Merge Time

---

## Purpose

When a planning run tries to merge its artifact to main and the allocated ID has been taken by a concurrent merge, detect the collision and automatically renumber.

---

## Deliverable

Extend the merge path (likely `internal/engine/merge.go` or a new `internal/artifact/renumber.go`) with collision-aware logic.

The flow:

1. Planning run completes, engine attempts to merge branch to main
2. If merge fails due to path conflict (another TASK-006 was merged while this branch was open):
   a. Re-scan main for the next available ID using `NextID()`
   b. On the branch: rename the artifact file/directory to the new ID
   c. Update the artifact's front-matter (`id` field) and heading to reflect the new ID
   d. Update any branch name references if applicable
   e. Commit the rename on the branch
   f. Retry the merge
3. If the retry also conflicts (extremely unlikely), fail with a clear error

Key considerations:

- The rename must update: file path, front-matter `id`, markdown heading, and any self-referencing links
- Other artifacts on the same branch that link to the renamed artifact (e.g., child tasks linking to their parent epic) must also have their links updated
- The retry should be limited (max 2 attempts) to avoid infinite loops

---

## Acceptance Criteria

- Merge conflict caused by ID collision is detected (distinguished from other merge conflicts)
- Artifact is correctly renumbered with the next available ID
- Front-matter, heading, and file path are all consistent after renumber
- Merge retries successfully after renumber
- Non-ID merge conflicts (e.g., content conflicts in existing files) are not caught by this handler — they should propagate as normal merge failures
- Max retry limit prevents infinite loops
