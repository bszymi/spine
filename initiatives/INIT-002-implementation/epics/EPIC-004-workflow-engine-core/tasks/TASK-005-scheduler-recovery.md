---
id: TASK-005
type: Task
title: Scheduler and Crash Recovery
status: Completed
epic: /initiatives/INIT-002-implementation/epics/EPIC-004-workflow-engine-core/epic.md
initiative: /initiatives/INIT-002-implementation/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-002-implementation/epics/EPIC-004-workflow-engine-core/epic.md
---

# TASK-005 — Scheduler and Crash Recovery

## Purpose

Implement the engine scheduler for time-based triggers and crash recovery per Engine State Machine §6.3 and Error Handling §6.

## Deliverable

- Timeout scanner (periodic check of active steps against timeout config)
- Orphan detector (Runs without recent activity)
- Retry scheduler (backoff delays for retries)
- Crash recovery sequence (per Engine State Machine §2.4 and §3.5)
- `engine_recovered` event emission
- All idempotency requirements from Error Handling §6.3

## Acceptance Criteria

- Timeout scanner detects and handles expired steps
- Orphan detector flags stale Runs
- Recovery after simulated crash correctly resumes active Runs
- Recovery handles all persisted states (pending, active, paused, committing)
- Re-processing queue entries after crash is safe (idempotent)
- Integration tests simulate crash scenarios
