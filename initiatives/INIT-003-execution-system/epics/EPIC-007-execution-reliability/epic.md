---
id: EPIC-007
type: Epic
title: Execution Reliability
status: Completed
initiative: /initiatives/INIT-003-execution-system/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-003-execution-system/initiative.md
---

# EPIC-007 — Execution Reliability

---

## Purpose

Make the execution system resilient. Failure is not an edge case — it is the default. Steps fail, actors time out, Git commits conflict, and the scheduler must recover gracefully.

---

## Key Work Areas

- Retry logic enforcement per workflow step configuration
- Timeout handling with configurable durations
- Failure classification (transient vs permanent) and routing
- Run recovery from crashes and orphaned state
- Backoff strategy enforcement

---

## Primary Outputs

- Retry logic in engine orchestrator
- Timeout enforcement per step and run
- Failure classifier in step progression
- Recovery procedures in scheduler

---

## Acceptance Criteria

- Steps retry up to the configured limit with appropriate backoff
- Timed-out steps transition to the configured timeout_outcome
- Transient failures trigger retry; permanent failures fail the step
- Orphaned runs (stuck in active state) are detected and recovered
- Crashed runs can be resumed from the last known state
- All failure paths are tested
