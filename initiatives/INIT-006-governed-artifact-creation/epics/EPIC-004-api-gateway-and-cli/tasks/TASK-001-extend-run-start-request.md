---
id: TASK-001
type: Task
title: Extend runStartRequest with mode and artifact_content
status: Draft
epic: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-004-api-gateway-and-cli/epic.md
initiative: /initiatives/INIT-006-governed-artifact-creation/initiative.md
work_type: implementation
created: 2026-03-30
last_updated: 2026-03-30
links:
  - type: parent
    target: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-004-api-gateway-and-cli/epic.md
---

# TASK-001 — Extend runStartRequest with Mode and ArtifactContent

---

## Purpose

Add `mode` and `artifact_content` fields to the run start request struct so the API can accept planning run requests.

---

## Deliverable

`internal/gateway/handlers_workflow.go`

Update `runStartRequest` to include:
- `Mode string` (`json:"mode,omitempty"`) — "standard" (default) or "planning"
- `ArtifactContent string` (`json:"artifact_content,omitempty"`) — required when mode=planning

Add validation:
- If `mode == "planning"` and `artifact_content` is empty, return `ErrInvalidParams`
- If `mode` is not empty and not "standard" or "planning", return `ErrInvalidParams`

---

## Acceptance Criteria

- Struct compiles with new fields
- Validation rejects invalid combinations
- Empty mode defaults to standard behavior
