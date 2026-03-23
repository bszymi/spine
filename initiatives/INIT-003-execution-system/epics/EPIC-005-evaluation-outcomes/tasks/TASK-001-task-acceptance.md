---
id: TASK-001
type: Task
title: Task Acceptance and Rejection Recording
status: Pending
epic: /initiatives/INIT-003-execution-system/epics/EPIC-005-evaluation-outcomes/epic.md
initiative: /initiatives/INIT-003-execution-system/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-003-execution-system/epics/EPIC-005-evaluation-outcomes/epic.md
---

# TASK-001 — Task Acceptance and Rejection Recording

## Purpose

Implement durable recording of task-level acceptance outcomes in the task artifact YAML front matter, per ADR-004.

## Deliverable

- Task artifact schema extension: `acceptance` field (approved, rejected_with_followup, rejected_closed)
- Task artifact schema extension: `acceptance_rationale` field
- Artifact service logic to update acceptance fields on task artifacts
- API operations: `POST /tasks/{path}/accept`, `POST /tasks/{path}/reject`
- Acceptance updates committed to Git with structured trailers

## Acceptance Criteria

- Tasks can be accepted with rationale recorded in YAML front matter
- Tasks can be rejected (with followup or closed) with rationale
- Acceptance changes are committed to Git as durable governed outcomes
- Invalid acceptance transitions are rejected (e.g., accepting an already-rejected task)
- Acceptance fields are visible in artifact queries
