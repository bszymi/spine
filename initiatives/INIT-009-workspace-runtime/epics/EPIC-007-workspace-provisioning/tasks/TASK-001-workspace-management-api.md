---
id: TASK-001
type: Task
title: Workspace management API endpoints
status: Pending
epic: /initiatives/INIT-009-workspace-runtime/epics/EPIC-007-workspace-provisioning/epic.md
initiative: /initiatives/INIT-009-workspace-runtime/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-007-workspace-provisioning/epic.md
---

# TASK-001 — Workspace management API endpoints

---

## Purpose

Add API endpoints for workspace lifecycle management. These are system-level endpoints (operator role) that manage workspaces, not workspace-scoped endpoints.

## Deliverable

New handlers in `internal/gateway/` and routes in `routes.go`.

Endpoints:

- `POST /api/v1/workspaces` — create a new workspace (triggers full provisioning)
  - Request body: `{ "workspace_id": "...", "display_name": "..." }`
  - Response: created workspace config with status
- `GET /api/v1/workspaces` — list all workspaces with status
- `GET /api/v1/workspaces/{workspace_id}` — get workspace details
- `POST /api/v1/workspaces/{workspace_id}/deactivate` — deactivate a workspace

These endpoints are workspace-exempt (they manage workspaces, not operate within one). They require operator or admin role.

## Acceptance Criteria

- All four endpoints are implemented and return proper JSON responses
- Create endpoint triggers the provisioning flow (TASK-002, TASK-003)
- Endpoints require operator role
- Endpoints are exempt from workspace resolution middleware
- Unknown workspace ID returns 404
- Duplicate workspace ID returns 409 conflict
