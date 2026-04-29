---
id: TASK-001
type: Task
title: Add affected repositories to the Run model
status: Completed
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-004-multi-repo-run-lifecycle/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: implementation
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-004-multi-repo-run-lifecycle/epic.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-002-task-schema-and-repository-validation/tasks/TASK-004-run-start-repository-preconditions.md
---

# TASK-001 - Add Affected Repositories to the Run Model

---

## Purpose

Persist the repository set a run is responsible for.

## Deliverable

Extend domain and runtime schema so a run records:

- Affected repository IDs
- Primary repository participation
- Shared run branch name
- Optional per-repo branch metadata for future recovery

## Acceptance Criteria

- Standard runs derive affected repositories from the Task.
- Planning runs remain primary-repo-only.
- Missing task repository metadata produces `[spine]`.
- Runtime persistence round-trips the repository set.
- Existing run API responses remain backward compatible or gain optional fields.

