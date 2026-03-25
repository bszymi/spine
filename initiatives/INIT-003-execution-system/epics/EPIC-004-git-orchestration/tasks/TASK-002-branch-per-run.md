---
id: TASK-002
type: Task
title: Branch-Per-Run Strategy
status: Completed
epic: /initiatives/INIT-003-execution-system/epics/EPIC-004-git-orchestration/epic.md
initiative: /initiatives/INIT-003-execution-system/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-003-execution-system/epics/EPIC-004-git-orchestration/epic.md
---

# TASK-002 — Branch-Per-Run Strategy

## Purpose

Implement automatic Git branch creation and lifecycle management for runs, so each run operates on an isolated branch.

## Deliverable

- Branch creation on run activation (naming: `spine/run/<run-id>`)
- Branch reference stored on the run record
- WriteContext automatically set to the run's branch during step execution
- Branch deletion after successful merge (cleanup)

## Acceptance Criteria

- A new branch is created when a run is activated
- All artifact writes during the run go to the run's branch
- The branch name follows a predictable convention
- Branch reference is persisted on the run record in the store
- Cleanup removes the branch after successful merge
