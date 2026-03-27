---
id: EPIC-001
type: Epic
title: Spec Conformance
status: Draft
initiative: /initiatives/INIT-005-api-spec-conformance/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-005-api-spec-conformance/initiative.md
---

# EPIC-001 — Spec Conformance

---

## Purpose

Fix all request/response schema mismatches between the HTTP API implementation in `internal/gateway/` and the OpenAPI spec in `api/spec.yaml`. Every spec-defined endpoint should produce responses that match the declared schemas exactly, accept the declared request bodies, and honor the declared query parameters.

---

## Key Work Areas

- Artifact endpoint responses (field names, commit_sha, write_mode, source_commit)
- Artifact endpoint requests (WriteContext object, validate content param, link filters)
- Workflow endpoint responses (RunResponse, RunStatusResponse, StepSubmitResponse, AssignmentResponse)
- Workflow endpoint requests (StepSubmitRequest full schema)
- Task governance responses (TaskGovernanceResponse, supersede successor_path)
- Query endpoint conformance (param naming, pagination metadata, status filters)
- System endpoint responses (SystemValidationResponse, async rebuild, health metrics)
- One spec fix (query.graph `root` param)

---

## Primary Outputs

- Updated `internal/gateway/handlers_artifacts.go`
- Updated `internal/gateway/handlers_workflow.go`
- Updated `internal/gateway/handlers_tasks.go`
- Updated `internal/gateway/handlers_query.go`
- Updated `internal/gateway/handlers_system.go`
- Updated `api/spec.yaml` (query.graph param name)
- Response DTO helpers or structs as needed

---

## Acceptance Criteria

- Every response field matches the spec's required/optional field names and types
- `commit_sha` and `write_mode` are present in artifact create/update and task governance responses
- `source_commit` is present in artifact read responses
- WriteContext accepts `{run_id, task_path}` object, not a flat string
- StepSubmitRequest accepts the full output object, rationale, and validation_status
- Query endpoints include pagination metadata (`next_cursor`, `has_more`)
- Existing tests pass after changes
