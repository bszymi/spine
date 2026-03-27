---
id: TASK-002
type: Task
title: "Fix WriteContext schema in artifact create/update"
status: Draft
epic: /initiatives/INIT-005-api-spec-conformance/epics/EPIC-001-spec-conformance/epic.md
initiative: /initiatives/INIT-005-api-spec-conformance/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-005-api-spec-conformance/epics/EPIC-001-spec-conformance/epic.md
---

# TASK-002 — Fix WriteContext schema in artifact create/update

## Purpose

The spec defines `WriteContext` as an object with `run_id` and `task_path` properties. The implementation uses a flat string (branch name). The API should accept the higher-level `{run_id, task_path}` and resolve it to the correct branch internally — clients shouldn't know about branch names.

## Deliverable

- Updated `artifactCreateRequest` and `artifactUpdateRequest` structs in `handlers_artifacts.go` with a proper `WriteContext` struct matching the spec
- Gateway-level resolution from `run_id`/`task_path` to branch name before calling the artifact service

## Acceptance Criteria

- Request body accepts `write_context: {run_id: "...", task_path: "..."}` as an object
- Flat string `write_context` is no longer accepted
- Branch resolution happens in the gateway layer
- Existing tests updated
