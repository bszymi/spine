---
id: TASK-007
type: Task
title: "Fix run.start response to include workflow_id and workflow_version"
status: Completed
epic: /initiatives/INIT-005-api-spec-conformance/epics/EPIC-001-spec-conformance/epic.md
initiative: /initiatives/INIT-005-api-spec-conformance/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-005-api-spec-conformance/epics/EPIC-001-spec-conformance/epic.md
---

# TASK-007 — Fix run.start response to include workflow_id and workflow_version

## Purpose

The spec's `RunResponse` requires `workflow_id` and optionally `workflow_version`. The current response includes `entry_step` (not in spec) but omits these fields. The resolved workflow already has `WorkflowID` and `CommitSHA`/`VersionLabel` available.

## Deliverable

- Include `workflow_id` and `workflow_version` in the `handleRunStart` response
- Remove `entry_step` from the response (not in spec)

## Acceptance Criteria

- `POST /runs` response includes `run_id`, `task_path`, `workflow_id`, `status`, `trace_id`
- `workflow_version` is included when available from the resolved workflow
