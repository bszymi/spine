---
id: TASK-006
type: Task
title: "Support inline content param in artifact.validate"
status: Completed
epic: /initiatives/INIT-005-api-spec-conformance/epics/EPIC-001-spec-conformance/epic.md
initiative: /initiatives/INIT-005-api-spec-conformance/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-005-api-spec-conformance/epics/EPIC-001-spec-conformance/epic.md
---

# TASK-006 — Support inline content param in artifact.validate

## Purpose

The spec allows an optional `content` field in the request body of `POST /artifacts/{path}/validate` for dry-run validation without saving. The current implementation ignores the request body and always reads the stored artifact.

## Deliverable

- Parse optional `content` from request body in `handleArtifactValidate`
- When `content` is provided, construct a temporary artifact from it and validate
- When `content` is omitted, fall back to reading the stored artifact (current behavior)

## Acceptance Criteria

- `POST /artifacts/{path}/validate` with `{content: "..."}` validates the provided content
- `POST /artifacts/{path}/validate` with empty body validates the stored artifact
- Validation results are identical regardless of whether content is inline or stored
