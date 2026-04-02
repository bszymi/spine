---
id: TASK-001
type: Task
title: Workspace ID in structured logs
status: Pending
epic: /initiatives/INIT-009-workspace-runtime/epics/EPIC-006-observability-workspace-identity/epic.md
initiative: /initiatives/INIT-009-workspace-runtime/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-006-observability-workspace-identity/epic.md
---

# TASK-001 — Workspace ID in structured logs

---

## Purpose

Ensure every log line includes workspace identity so operators can filter logs by workspace in shared runtime mode.

## Deliverable

Updates to `internal/observe/` and gateway middleware.

Content should define:

- Workspace middleware adds `workspace_id` to the `slog` context for every request
- Background services set `workspace_id` in log context when processing a specific workspace
- All existing log calls automatically include workspace_id via contextual logging

## Acceptance Criteria

- Every log line emitted during an API request includes `workspace_id`
- Every log line emitted during background processing includes `workspace_id`
- In single-workspace mode, the configured workspace ID is still present in logs
