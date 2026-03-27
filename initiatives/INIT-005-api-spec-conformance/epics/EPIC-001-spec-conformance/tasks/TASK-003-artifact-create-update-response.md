---
id: TASK-003
type: Task
title: "Add commit_sha and write_mode to artifact create/update responses"
status: Draft
epic: /initiatives/INIT-005-api-spec-conformance/epics/EPIC-001-spec-conformance/epic.md
initiative: /initiatives/INIT-005-api-spec-conformance/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-005-api-spec-conformance/epics/EPIC-001-spec-conformance/epic.md
---

# TASK-003 — Add commit_sha and write_mode to artifact create/update responses

## Purpose

The spec's `ArtifactCreateResponse` and `ArtifactUpdateResponse` both require `commit_sha` (the git commit SHA of the write) and `write_mode` (enum: `authoritative` or `proposed`). Neither field is currently returned.

## Deliverable

- Artifact service `Create` and `Update` methods return the commit SHA from the git write
- Gateway handlers include `commit_sha` and `write_mode` in responses
- `write_mode` reflects whether the write went to the authoritative branch or a task branch

## Acceptance Criteria

- `POST /artifacts` response includes `commit_sha` (string) and `write_mode` (authoritative|proposed)
- `PUT /artifacts/{path}` response includes `commit_sha` (string) and `write_mode` (authoritative|proposed)
- Commit SHA matches the actual git commit created
