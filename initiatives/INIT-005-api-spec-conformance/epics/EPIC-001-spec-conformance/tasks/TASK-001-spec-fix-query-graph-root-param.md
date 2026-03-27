---
id: TASK-001
type: Task
title: "Update spec: query.graph use root param"
status: Draft
epic: /initiatives/INIT-005-api-spec-conformance/epics/EPIC-001-spec-conformance/epic.md
initiative: /initiatives/INIT-005-api-spec-conformance/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-005-api-spec-conformance/epics/EPIC-001-spec-conformance/epic.md
---

# TASK-001 — Update spec: query.graph use root param

## Purpose

The implementation uses `root` as the query parameter for `GET /query/graph`, which is more semantically correct for graph traversal (it's the root node of the traversal). The spec currently uses `path`. Update the spec to match the implementation.

## Deliverable

- Updated `api/spec.yaml`: rename `path` to `root` for the `GET /query/graph` endpoint parameter

## Acceptance Criteria

- `api/spec.yaml` uses `root` (not `path`) for the query.graph endpoint
- Parameter description reflects graph traversal semantics
