---
id: TASK-008
type: Task
title: "Fix run.status response to flat RunStatusResponse"
status: Completed
epic: /initiatives/INIT-005-api-spec-conformance/epics/EPIC-001-spec-conformance/epic.md
initiative: /initiatives/INIT-005-api-spec-conformance/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-005-api-spec-conformance/epics/EPIC-001-spec-conformance/epic.md
---

# TASK-008 — Fix run.status response to flat RunStatusResponse

## Purpose

The spec expects a flat `RunStatusResponse` with all fields at the top level. The current handler returns a nested `{run: {...}, steps: [...]}` structure.

## Deliverable

- Flatten the `handleRunStatus` response to a single object matching the spec

## Acceptance Criteria

- `GET /runs/{run_id}` response is a flat object with: `run_id`, `task_path`, `workflow_id`, `status`, `current_step_id`, `trace_id`, `started_at`, `completed_at`, `step_executions`
- `step_executions` is an array of `StepExecution` objects (not wrapped in a `steps` key)
