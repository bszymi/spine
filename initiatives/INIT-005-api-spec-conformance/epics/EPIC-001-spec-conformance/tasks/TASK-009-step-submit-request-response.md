---
id: TASK-009
type: Task
title: "Fix StepSubmitRequest/Response to match spec"
status: Draft
epic: /initiatives/INIT-005-api-spec-conformance/epics/EPIC-001-spec-conformance/epic.md
initiative: /initiatives/INIT-005-api-spec-conformance/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-005-api-spec-conformance/epics/EPIC-001-spec-conformance/epic.md
---

# TASK-009 — Fix StepSubmitRequest/Response to match spec

## Purpose

The `StepSubmitRequest` and `StepSubmitResponse` schemas are significantly incomplete compared to the spec.

**Request gaps:**
- `output` should be an object with `artifacts_produced` (array of objects with path/artifact_type/status), `data`, and `summary`
- Missing `rationale` and `validation_status` (enum: passed/failed/skipped/not_run)
- Current `artifacts_produced` is a flat string array instead of object array

**Response gaps:**
- Missing `commit_sha`, `write_mode`, `validation_result`, `run_advanced`, `requires_review`

## Deliverable

- Updated `stepSubmitRequest` struct to match spec
- Updated response map in `handleStepSubmit` to include all spec fields
- Store the additional request fields (rationale, validation_status) in the step execution

## Acceptance Criteria

- Request accepts full `StepSubmitRequest` schema including nested `output` object
- Response includes all `StepSubmitResponse` fields
- `run_advanced` correctly indicates whether the run moved to the next step
- `requires_review` correctly indicates whether the next step is a review step
