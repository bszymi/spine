---
id: TASK-002
type: Task
title: Timeout Handling
status: Completed
epic: /initiatives/INIT-003-execution-system/epics/EPIC-007-execution-reliability/epic.md
initiative: /initiatives/INIT-003-execution-system/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-003-execution-system/epics/EPIC-007-execution-reliability/epic.md
---

# TASK-002 — Timeout Handling

## Purpose

Implement timeout enforcement for steps and runs so that stuck execution is detected and handled according to workflow configuration.

## Deliverable

- Step timeout tracking: detect steps that exceed their configured timeout duration
- Timeout outcome routing: transition timed-out steps to the configured `timeout_outcome`
- Run-level timeout: configurable maximum run duration
- Integration with scheduler for periodic timeout scanning

## Acceptance Criteria

- Steps exceeding their timeout are detected by the scheduler
- Timed-out steps transition to the configured timeout_outcome
- If no timeout_outcome is configured, timed-out steps fail
- Run-level timeouts cancel the entire run
- Timeout detection runs on the scheduler's scan interval
