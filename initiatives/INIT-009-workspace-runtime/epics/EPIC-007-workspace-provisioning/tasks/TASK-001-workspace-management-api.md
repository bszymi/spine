---
id: TASK-001
type: Task
title: Workspace management API endpoints
status: Completed
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
  - Request body: `{ "workspace_id": "...", "display_name": "...", "git_url": "..." }`
  - `git_url` is optional — if provided, the repo is cloned from this remote; if omitted, a fresh repo is initialized
  - Response: created workspace config with status
- `GET /api/v1/workspaces` — list all workspaces with status
- `GET /api/v1/workspaces/{workspace_id}` — get workspace details
- `POST /api/v1/workspaces/{workspace_id}/deactivate` — deactivate a workspace

These endpoints are workspace-exempt (they manage workspaces, not operate within one).

### Authentication for workspace-exempt routes

Since actors are scoped per workspace, workspace management endpoints cannot authenticate against a workspace store. Authentication for these routes uses a **system operator token** — a static bearer token configured via `SPINE_OPERATOR_TOKEN` env var, validated directly by the management route group without requiring a workspace actor store. This is distinct from per-workspace actor tokens.

## Acceptance Criteria

- All four endpoints are implemented and return proper JSON responses
- Create endpoint triggers the provisioning flow (TASK-002, TASK-003)
- Endpoints authenticate via system operator token (`SPINE_OPERATOR_TOKEN`)
- Endpoints are exempt from workspace resolution middleware
- Deactivation explicitly invalidates the DBProvider cache and evicts the ServicePool entry for the workspace, so it stops serving immediately (not after cache TTL)
- Unknown workspace ID returns 404
- Duplicate workspace ID returns 409 conflict
