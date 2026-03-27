---
id: TASK-010
type: Task
title: "Fix AssignmentResponse to include assignment_id and run_id"
status: Draft
epic: /initiatives/INIT-005-api-spec-conformance/epics/EPIC-001-spec-conformance/epic.md
initiative: /initiatives/INIT-005-api-spec-conformance/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-005-api-spec-conformance/epics/EPIC-001-spec-conformance/epic.md
---

# TASK-010 — Fix AssignmentResponse to include assignment_id and run_id

## Purpose

The spec requires `AssignmentResponse` with `assignment_id`, `run_id`, `step_id`, `actor_id`, `status` (enum: active). The current response returns `execution_id` instead of `assignment_id` and is missing `run_id`.

## Deliverable

- Map `execution_id` to `assignment_id` in the response (or introduce a proper assignment ID)
- Add `run_id` from the URL parameter to the response

## Acceptance Criteria

- `POST /runs/{run_id}/steps/{step_id}/assign` response includes `assignment_id`, `run_id`, `step_id`, `actor_id`, `status`
- `status` is `"active"` for newly assigned steps
