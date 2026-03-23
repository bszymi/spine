---
id: TASK-004
type: Task
title: Reference Workflows
status: Pending
epic: /initiatives/INIT-003-execution-system/epics/EPIC-003-workflow-definitions/epic.md
initiative: /initiatives/INIT-003-execution-system/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-003-execution-system/epics/EPIC-003-workflow-definitions/epic.md
---

# TASK-004 — Reference Workflows

## Purpose

Create additional workflow definitions for common patterns beyond the default task workflow.

## Deliverable

- `workflows/task-spike.yaml` — Simplified workflow for spike/investigation tasks (investigate → summarize → review)
- `workflows/adr.yaml` — ADR workflow (propose → evaluate → accept/reject)
- `workflows/epic-lifecycle.yaml` — Epic progression workflow
- Each workflow uses work_type filtering in applies_to

## Acceptance Criteria

- All workflows parse and validate successfully
- Each workflow has correct applies_to with work_type filtering
- Spike workflow binds to tasks with `work_type: spike`
- ADR workflow binds to ADR artifact type
- No binding conflicts between workflows (exactly one match per type + work_type)
