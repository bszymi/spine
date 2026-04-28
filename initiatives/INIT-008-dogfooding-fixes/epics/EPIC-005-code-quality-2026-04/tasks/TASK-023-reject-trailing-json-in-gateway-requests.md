---
id: TASK-023
type: Task
title: Reject trailing JSON in gateway requests
status: Pending
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: bugfix
created: 2026-04-28
last_updated: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/epic.md
---

# TASK-023 — Reject trailing JSON in gateway requests

---

## Purpose

Code-quality review finding: `internal/gateway/handlers_helpers.go` decodes JSON request bodies once and does not verify EOF. Bodies such as `{...}{...}` are accepted as the first object, which makes request semantics ambiguous and can hide malformed clients.

This task tightens the shared gateway decoder so all JSON request handlers reject trailing data consistently.

## Deliverable

- Update `decodeJSON` to reject trailing non-whitespace tokens after the first decoded JSON value.
- Keep existing content-type and max-body-size behavior unchanged.
- Add decoder tests for valid JSON, whitespace after JSON, trailing JSON object, trailing scalar, invalid content type, and oversized body.

## Acceptance Criteria

- A request body containing two JSON values is rejected with `invalid_params`.
- A request body with only whitespace after the first JSON value remains valid.
- Existing handlers do not need per-route decoder changes.
- Existing gateway request tests still pass.
- `go test ./internal/gateway` passes.
