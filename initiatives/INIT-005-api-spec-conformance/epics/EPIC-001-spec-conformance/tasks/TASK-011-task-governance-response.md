---
id: TASK-011
type: Task
title: "Fix TaskGovernanceResponse and handle supersede successor_path"
status: Completed
epic: /initiatives/INIT-005-api-spec-conformance/epics/EPIC-001-spec-conformance/epic.md
initiative: /initiatives/INIT-005-api-spec-conformance/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-005-api-spec-conformance/epics/EPIC-001-spec-conformance/epic.md
---

# TASK-011 — Fix TaskGovernanceResponse and handle supersede successor_path

## Purpose

All task governance responses should match `TaskGovernanceResponse`: `task_path`, `status`, `commit_sha` (required), `trace_id` (required), optional `acceptance`. Current responses use `artifact_path` instead of `task_path`, include non-spec fields (`artifact_id`, `action`), and are missing `commit_sha`.

Additionally, the `task.supersede` handler ignores `successor_path` from the request body, which the spec requires for maintaining the supersession chain.

## Deliverable

- Updated response maps in `handleTaskWildcard` for all governance actions
- Parse and record `successor_path` in the supersede handler
- Artifact service returns commit SHA for governance operations

## Acceptance Criteria

- All task governance responses use `task_path` (not `artifact_path`)
- All responses include `commit_sha` and `trace_id`
- Accept/reject responses include `acceptance` field
- `POST /tasks/{path}/supersede` reads `successor_path` from the body and records it
