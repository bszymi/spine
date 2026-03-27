---
id: TASK-012
type: Task
title: "Fix system.validate_all response to match SystemValidationResponse"
status: Draft
epic: /initiatives/INIT-005-api-spec-conformance/epics/EPIC-001-spec-conformance/epic.md
initiative: /initiatives/INIT-005-api-spec-conformance/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-005-api-spec-conformance/epics/EPIC-001-spec-conformance/epic.md
---

# TASK-012 — Fix system.validate_all response to match SystemValidationResponse

## Purpose

The spec expects `SystemValidationResponse` with `status` (passed/failed/warnings), `total_artifacts`, `passed` (count), `warnings` (count), `failed` (count), and `results` (array of `ValidationResult`). The current implementation returns `{total_artifacts, issues, trace_id}` which is a different structure.

## Deliverable

- Restructure the `handleSystemValidate` response to match the spec
- Compute aggregate counts (passed, warnings, failed)
- Use `results` array with proper `ValidationResult` objects instead of `issues`

## Acceptance Criteria

- Response includes `status`, `total_artifacts`, `passed`, `warnings`, `failed`, `results`
- `status` is `"passed"` when no issues, `"warnings"` when only warnings, `"failed"` when errors exist
- `results` array contains `ValidationResult` objects per the spec schema
