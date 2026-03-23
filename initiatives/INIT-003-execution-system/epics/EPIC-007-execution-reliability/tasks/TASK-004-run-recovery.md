---
id: TASK-004
type: Task
title: Run Recovery from Crashes
status: Pending
epic: /initiatives/INIT-003-execution-system/epics/EPIC-007-execution-reliability/epic.md
initiative: /initiatives/INIT-003-execution-system/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-003-execution-system/epics/EPIC-007-execution-reliability/epic.md
---

# TASK-004 — Run Recovery from Crashes

## Purpose

Implement recovery procedures so that runs interrupted by server crashes or restarts can resume from their last known state.

## Deliverable

- Startup recovery: on server start, scan for runs in `active` state and resume them
- Orphan detection: runs with no recent activity beyond threshold are flagged
- Recovery logic: re-evaluate current step state and resume progression
- Guard against duplicate execution during recovery

## Acceptance Criteria

- Server restart detects and resumes interrupted runs
- Orphaned runs are detected and either recovered or failed with detail
- Recovery does not duplicate step executions (idempotent)
- Recovery is logged with detail for debugging
- Runs that cannot be recovered are transitioned to `failed`
